package kilo

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"golang.org/x/term"
)

type terminalDriver interface {
	start() (func() error, error)
	read(context.Context, func([]byte) bool, func(time.Time)) error
}

type inputFailureEvent struct {
	err  error
	when time.Time
}

func (e *inputFailureEvent) When() time.Time { return e.when }

// Descriptors belong to the caller. Rendering remains with tcell; one owned
// reader routes ordinary keys and raw clipboard frames to an ordered event queue.
type borrowedTTY struct {
	input, output                      *os.File
	driver                             terminalDriver
	source                             *rawInput
	ctx                                context.Context
	cancel                             context.CancelFunc
	readerDone                         chan struct{}
	neutralDone                        chan struct{}
	stopInputOnce, drainOnce, stopOnce sync.Once
	mu                                 sync.Mutex
	resize                             func()
	restore                            func() error
	started                            bool
	finalErr                           error
}

func (t *borrowedTTY) Read([]byte) (int, error)    { <-t.neutralDone; return 0, io.EOF }
func (t *borrowedTTY) Write(b []byte) (int, error) { return t.output.Write(b) }
func (t *borrowedTTY) Close() error                { return t.Stop() }
func (t *borrowedTTY) Start() error {
	restore, err := t.driver.start()
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.restore, t.started = restore, true
	t.mu.Unlock()
	go func() {
		defer close(t.readerDone)
		previous, _ := t.WindowSize()
		err := t.driver.read(t.ctx, t.source.feed, func(now time.Time) {
			t.source.idle(now)
			current, sizeErr := t.WindowSize()
			if sizeErr != nil || current == previous {
				return
			}
			previous = current
			t.mu.Lock()
			resize := t.resize
			t.mu.Unlock()
			if resize != nil {
				resize()
			}
		})
		if t.ctx.Err() == nil {
			if fault := t.source.failure(); fault != nil {
				err = fault
			}
			if err == nil {
				err = io.EOF
			}
			select {
			case t.source.events <- &inputFailureEvent{err: err, when: time.Now()}:
			case <-t.ctx.Done():
			}
		}
	}()
	return nil
}
func (t *borrowedTTY) stopInput() {
	t.stopInputOnce.Do(func() {
		t.cancel()
		t.mu.Lock()
		started := t.started
		t.mu.Unlock()
		if !started {
			return
		}
		// The maintained key scanner has a blocking event sink. Drain it while
		// the reader joins, so shutdown cannot strand a full queue's producer.
		for {
			select {
			case <-t.readerDone:
				return
			case <-t.source.events:
			}
		}
	})
}
func (t *borrowedTTY) Drain() error {
	t.stopInput()
	t.drainOnce.Do(func() { close(t.neutralDone) })
	return nil
}
func (t *borrowedTTY) Stop() error {
	t.stopOnce.Do(func() {
		t.Drain()
		t.mu.Lock()
		restore := t.restore
		t.mu.Unlock()
		if restore != nil {
			t.finalErr = restore()
		}
	})
	return t.finalErr
}
func (t *borrowedTTY) NotifyResize(cb func()) { t.mu.Lock(); t.resize = cb; t.mu.Unlock() }
func (t *borrowedTTY) WindowSize() (tcell.WindowSize, error) {
	w, h, err := term.GetSize(int(t.output.Fd()))
	return tcell.WindowSize{Width: max(w, 1), Height: max(h, 1)}, err
}

type terminalScreen struct {
	tcell.Screen
	tty           *borrowedTTY
	other         chan tcell.Event
	bridgeStop    chan struct{}
	bridgeDone    chan struct{}
	bridgeStarted bool
	closeBridge   sync.Once
}

func (s *terminalScreen) Init() error {
	if err := s.Screen.Init(); err != nil {
		return err
	}
	s.bridgeStarted = true
	go func() { defer close(s.bridgeDone); s.Screen.ChannelEvents(s.other, s.bridgeStop) }()
	return nil
}
func (s *terminalScreen) PollEvent() tcell.Event {
	select {
	case event := <-s.tty.source.events:
		return event
	case event := <-s.other:
		return event
	case <-s.tty.ctx.Done():
		return tcell.NewEventInterrupt(s.tty.ctx.Err())
	}
}
func (s *terminalScreen) stopPasteInput() ([]byte, bool) {
	s.tty.stopInput()
	return s.tty.source.snapshot()
}
func (s *terminalScreen) discardPasteInput() { s.tty.source.accept() }
func (s *terminalScreen) Fini() {
	s.tty.Drain()
	s.closeBridge.Do(func() { close(s.bridgeStop) })
	if s.bridgeStarted {
		<-s.bridgeDone
	}
	s.Screen.Fini()
}
func (s *terminalScreen) finalizationError() error { return s.tty.finalErr }

func RunTerminal(ctx context.Context, filename string, input, output *os.File) error {
	if filename == "" || input == nil || output == nil {
		return errors.New("editor requires a filename and terminal")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	readerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	tty := &borrowedTTY{input: input, output: output, ctx: readerCtx, cancel: cancel, readerDone: make(chan struct{}), neutralDone: make(chan struct{})}
	tty.source = newRawInput(readerCtx)
	tty.driver = newTerminalDriver(input, output)
	var screen tcell.Screen
	var err error
	if runtime.GOOS == "windows" && os.Getenv("TERM") == "" {
		ti, lookupErr := tcell.LookupTerminfo("xterm-256color")
		if lookupErr != nil {
			return lookupErr
		}
		screen, err = tcell.NewTerminfoScreenFromTtyTerminfo(tty, ti)
	} else {
		screen, err = tcell.NewTerminfoScreenFromTty(tty)
	}
	if err != nil {
		return err
	}
	wrapped := &terminalScreen{Screen: screen, tty: tty, other: make(chan tcell.Event, 16), bridgeStop: make(chan struct{}), bridgeDone: make(chan struct{})}
	return runScreen(ctx, filename, wrapped)
}
