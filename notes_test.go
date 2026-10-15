package kilo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

func TestUnicodeNoteEditingAndProjection(t *testing.T) {
	e := newTestEditor(12, 5, "", nil)
	e.omitFinalNewline = true
	source := "猫e\u0301👨‍👩‍👧‍👦"
	for _, r := range source {
		e.processKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	if string(e.rows[0].chars) != source {
		t.Fatalf("Unicode input changed: %q", e.rows[0].chars)
	}
	for _, want := range []string{"猫e\u0301", "猫", ""} {
		e.delChar()
		if string(e.rows[0].chars) != want || !utf8.Valid(e.rows[0].chars) {
			t.Fatalf("broken deletion: %q, want %q", e.rows[0].chars, want)
		}
	}
	for _, r := range "猫\tZ" {
		e.processKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	if string(e.rows[0].render) != "猫"+strings.Repeat(" ", 6)+"Z" {
		t.Fatalf("tab after wide text: %q", e.rows[0].render)
	}
	sim := tcell.NewSimulationScreen("")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(12, 7)
	e.screen = sim
	e.colOff = 0
	e.cx = len("猫\tZ")
	e.refreshScreen()
	first, _, _, w := sim.GetContent(0, 0)
	last, _, _, _ := sim.GetContent(8, 0)
	x, y, visible := sim.GetCursor()
	if first != '猫' || w != 2 || last != 'Z' || x != 9 || y != 0 || !visible {
		t.Fatal(first, w, last, x, y, visible)
	}
	for _, size := range [][2]int{{4, 3}, {1, 1}, {0, 0}, {12, 7}} {
		sim.SetSize(size[0], size[1])
		e.resize()
		e.refreshScreen()
		if !utf8.Valid(e.rows[0].chars) {
			t.Fatal("resize broke source")
		}
	}
	e.moveCursor(tcell.KeyLeft)
	e.insertNewline()
	if e.rowsToString() != "猫\t\nZ" {
		t.Fatalf("Unicode split changed source: %q", e.rowsToString())
	}
}

func TestExactNoteFileSaveAndEmptyClearing(t *testing.T) {
	for _, source := range []string{"", "a", "a\n", "a\n\n", "\t cafe\u0301  \n猫", "\n"} {
		t.Run(source, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "note.md")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			e := newTestEditor(80, 24, path, nil)
			if err := e.openFile(path); err != nil {
				t.Fatal(err)
			}
			e.saveFile()
			got, err := os.ReadFile(path)
			if err != nil || string(got) != source {
				t.Fatal(string(got), source, err)
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal(info, err)
				}
			}
		})
	}
	e := newTestEditor(80, 24, "", nil, "a")
	e.setPosition(1, 0)
	e.delChar()
	e.delChar()
	if e.rowsToString() != "" {
		t.Fatalf("could not clear final newline and note: %q", e.rowsToString())
	}
	path := filepath.Join(t.TempDir(), "existing-dir")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	e = newTestEditor(80, 24, path, nil, "recovered content")
	e.dirty = true
	e.saveFile()
	if e.saveErr == nil || !e.dirty {
		t.Fatal("failed save not retained", e.saveErr)
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".kilo-save-*.tmp"))
	if err != nil || len(files) != 0 {
		t.Fatal("partial save file left", files, err)
	}
}

type readyScreen struct {
	tcell.SimulationScreen
	ready    chan struct{}
	notified bool
}

func (s *readyScreen) PollEvent() tcell.Event {
	if !s.notified {
		s.notified = true
		close(s.ready)
	}
	return s.SimulationScreen.PollEvent()
}

func TestKiloCancellationAndSaveFailureRecovery(t *testing.T) {
	for _, mode := range []string{"ctrl-c", "context", "failed-save"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "note.md")
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			sim := tcell.NewSimulationScreen("")
			screen := &readyScreen{SimulationScreen: sim, ready: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- runScreen(ctx, path, screen) }()
			select {
			case <-screen.ready:
			case <-time.After(2 * time.Second):
				t.Fatal("editor failed to start")
			}
			screen.PostEventWait(tcell.NewEventKey(tcell.KeyRune, '猫', tcell.ModNone))
			switch mode {
			case "context":
				deadline := time.Now().Add(2 * time.Second)
				for {
					r, _, _, _ := screen.GetContent(0, 0)
					if r == '猫' {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("input not rendered")
					}
					time.Sleep(time.Millisecond)
				}
				cancel()
			case "ctrl-c":
				screen.PostEventWait(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone))
			case "failed-save":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				screen.PostEventWait(tcell.NewEventKey(tcell.KeyCtrlS, 0, tcell.ModNone))
				for range kiloQuitTimes + 1 {
					screen.PostEventWait(tcell.NewEventKey(tcell.KeyCtrlQ, 0, tcell.ModNone))
				}
			}
			var err error
			select {
			case err = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("editor did not stop/drain")
			}
			var recovered *RecoveryError
			if !errors.As(err, &recovered) {
				t.Fatal("unsaved work not recovered", err)
			}
			defer os.RemoveAll(filepath.Dir(recovered.Path))
			data, readErr := os.ReadFile(recovered.Path)
			if readErr != nil || string(data) != "猫" {
				t.Fatal(string(data), readErr)
			}
			if mode != "failed-save" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if mode != "failed-save" {
				saved, readErr := os.ReadFile(path)
				if readErr != nil || len(saved) != 0 {
					t.Fatal("unsaved buffer imported", string(saved), readErr)
				}
			}
		})
	}
}

type failingInitScreen struct {
	tcell.SimulationScreen
	failure   error
	finalized bool
}

func (s *failingInitScreen) Init() error { return s.failure }
func (s *failingInitScreen) Fini()       { s.finalized = true; panic("partial initialization cleanup") }
func TestKiloInitializationFailureIsReportedAndCleanupAttempted(t *testing.T) {
	sentinel := errors.New("screen init failed")
	screen := &failingInitScreen{SimulationScreen: tcell.NewSimulationScreen(""), failure: sentinel}
	err := runScreen(context.Background(), "unused", screen)
	if !errors.Is(err, sentinel) || !screen.finalized || !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatal(err, screen.finalized)
	}
}

func TestLargeNoteEndAndPaste(t *testing.T) {
	done := make(chan *editor, 1)
	source := strings.Repeat("a", maxPasteBytes-len("猫👨‍👩‍👧‍👦")) + "猫👨‍👩‍👧‍👦"
	go func() {
		e := newTestEditor(80, 24, "", nil)
		e.omitFinalNewline = true
		e.insertPaste(source)
		e.setPosition(0, 0)
		e.processKey(tcell.NewEventKey(tcell.KeyEnd, 0, tcell.ModNone))
		done <- e
	}()
	select {
	case e := <-done:
		row, col := e.position()
		if row != 0 || col != len(source) || e.rowsToString() != source || cellWidth(source[e.colOff:col]) >= 80 {
			t.Fatal("large note source/cursor incorrect", row, col, e.colOff)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("large note paste/end movement did not finish; avoid repeated prefix scans")
	}
}

type scriptedScreen struct {
	tcell.SimulationScreen
	events []tcell.Event
}

func (s *scriptedScreen) PollEvent() tcell.Event {
	if len(s.events) == 0 {
		return nil
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event
}

func TestBracketedPasteIsLiteralAndInterruptedSourceIsRecovered(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "note.md")
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			screen := &scriptedScreen{SimulationScreen: tcell.NewSimulationScreen("")}
			screen.events = []tcell.Event{
				tcell.NewEventPaste(true),
				tcell.NewEventKey(tcell.KeyRune, '猫', tcell.ModNone),
				tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone),
				tcell.NewEventKey(tcell.KeyCtrlS, 0, tcell.ModNone),
				tcell.NewEventKey(tcell.KeyCtrlQ, 0, tcell.ModNone),
				tcell.NewEventKey(tcell.KeyRune, 'Z', tcell.ModNone),
			}
			if complete {
				screen.events = append(screen.events, tcell.NewEventPaste(false), tcell.NewEventKey(tcell.KeyCtrlS, 0, tcell.ModNone), tcell.NewEventKey(tcell.KeyCtrlQ, 0, tcell.ModNone))
			}
			err := runScreen(context.Background(), path, screen)
			var recovered *RecoveryError
			if complete {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if !errors.As(err, &recovered) {
					t.Fatal("partial paste was discarded", err)
				}
				defer os.RemoveAll(filepath.Dir(recovered.Path))
				path = recovered.Path
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "猫\n\x13\x11Z" {
				t.Fatal("paste ran editor commands or changed source", string(data), err)
			}
		})
	}
}

func TestUnsafeSourceIsVisibleWithoutChangingBytesOrCursor(t *testing.T) {
	e := newTestEditor(80, 24, "", nil)
	e.omitFinalNewline = true
	e.insertPaste("a\x01\u202e\u200b\u0301b")
	row := e.rows[0]
	if string(row.chars) != "a\x01\u202e\u200b\u0301b" || strings.ContainsRune(string(row.render), '\u202e') || strings.ContainsRune(string(row.render), '\x01') {
		t.Fatal("unsafe projection", string(row.chars), string(row.render))
	}
	sim := tcell.NewSimulationScreen("")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(80, 26)
	e.screen = sim
	e.refreshScreen()
	r, _, _, _ := sim.GetContent(1, 0)
	x, _, _ := sim.GetCursor()
	if r != '\\' || x != cellWidth(string(row.chars)) {
		t.Fatal("projection/cursor mismatch", r, x, string(row.render))
	}
}

func TestMaterializedEOFDoesNotAddNewlines(t *testing.T) {
	for _, source := range []string{"", "a", "a\n", "\n", "a\n\n"} {
		for _, paste := range []string{"b", "猫\nZ"} {
			t.Run(fmt.Sprintf("%q/%q", source, paste), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "note.md")
				if err := os.WriteFile(path, []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
				e := newTestEditor(80, 24, path, nil)
				if err := e.openFile(path); err != nil {
					t.Fatal(err)
				}
				last := e.lastRow()
				col := 0
				if last < len(e.rows) {
					col = len(e.rows[last].chars)
				}
				e.setPosition(last, col)
				e.insertPaste(paste)
				e.saveFile()
				data, err := os.ReadFile(path)
				if err != nil || string(data) != source+paste {
					t.Fatal("EOF insertion changed newline bytes", string(data), err)
				}
				for range []rune(paste) {
					e.delChar()
				}
				if e.rowsToString() != source {
					t.Fatal("type/backspace changed untouched note", e.rowsToString(), source)
				}
			})
		}
	}
}

type blockedFindScreen struct {
	tcell.SimulationScreen
	calls                   int
	inFind, release, posted chan struct{}
	once                    sync.Once
	postErr                 error
}

func (s *blockedFindScreen) PollEvent() tcell.Event {
	s.calls++
	if s.calls == 1 {
		return tcell.NewEventKey(tcell.KeyRune, '猫', tcell.ModNone)
	}
	if s.calls == 2 {
		return tcell.NewEventKey(tcell.KeyCtrlF, 0, tcell.ModNone)
	}
	if s.calls == 3 {
		close(s.inFind)
		<-s.release
	}
	return s.SimulationScreen.PollEvent()
}

func (s *blockedFindScreen) PostEvent(event tcell.Event) error {
	err := s.SimulationScreen.PostEvent(event)
	if _, ok := event.(*tcell.EventInterrupt); ok {
		s.once.Do(func() { s.postErr = err; close(s.posted) })
	}
	return err
}

func TestFindCancellationWithFullEventQueueRecoversDirtyBuffer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	screen := &blockedFindScreen{SimulationScreen: tcell.NewSimulationScreen(""), inFind: make(chan struct{}), release: make(chan struct{}), posted: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runScreen(ctx, path, screen) }()
	select {
	case <-screen.inFind:
	case <-time.After(2 * time.Second):
		t.Fatal("find did not start")
	}
	for {
		err := screen.SimulationScreen.PostEvent(tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone))
		if errors.Is(err, tcell.ErrEventQFull) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	select {
	case <-screen.posted:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not attempt wakeup")
	}
	if !errors.Is(screen.postErr, tcell.ErrEventQFull) {
		t.Fatal("fixture must exercise dropped interrupt", screen.postErr)
	}
	close(screen.release)
	select {
	case err := <-done:
		var recovered *RecoveryError
		if !errors.As(err, &recovered) || !errors.Is(err, context.Canceled) {
			t.Fatal("find cancellation lost recovery", err)
		}
		defer os.RemoveAll(filepath.Dir(recovered.Path))
		data, readErr := os.ReadFile(recovered.Path)
		if readErr != nil || string(data) != "猫original" {
			t.Fatal(string(data), readErr)
		}
	case <-time.After(2 * time.Second):
		screen.SimulationScreen.PostEventWait(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
		t.Fatal("find cancellation blocked after dropped interrupt")
	}
}

func TestFindPasteDoesNotExecuteCommandsAndEndedStreamExits(t *testing.T) {
	for _, ended := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "note.md")
		if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
		screen := &scriptedScreen{SimulationScreen: tcell.NewSimulationScreen("")}
		screen.events = []tcell.Event{tcell.NewEventKey(tcell.KeyCtrlF, 0, tcell.ModNone), tcell.NewEventPaste(true), tcell.NewEventKey(tcell.KeyRune, 'o', tcell.ModNone), tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), tcell.NewEventKey(tcell.KeyRune, 'X', tcell.ModNone), tcell.NewEventKey(tcell.KeyCtrlS, 0, tcell.ModNone), tcell.NewEventKey(tcell.KeyCtrlQ, 0, tcell.ModNone), tcell.NewEventPaste(false)}
		if !ended {
			screen.events = append(screen.events, tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), tcell.NewEventKey(tcell.KeyCtrlQ, 0, tcell.ModNone))
		}
		err := runScreen(context.Background(), path, screen)
		if ended && (err == nil || !strings.Contains(err.Error(), "ended during find")) || !ended && err != nil {
			t.Fatal("find exit incorrect", ended, err)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || string(data) != "original" {
			t.Fatal("find paste edited/saved source", string(data), readErr)
		}
	}
}

func TestHorizontalViewportUsesSameCellsAcrossMixedRows(t *testing.T) {
	e := newTestEditor(4, 5, "", nil, "猫猫猫", "abcde", "\tZ", "e\u0301abcd")
	e.omitFinalNewline = true
	sim := tcell.NewSimulationScreen("")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(4, 7)
	e.screen = sim
	e.setPosition(0, len("猫猫猫"))
	e.refreshScreen()
	if e.cellOff != 4 {
		t.Fatal("unexpected viewport origin", e.cellOff)
	}
	for _, cell := range []struct {
		x, y int
		want rune
	}{{0, 0, '猫'}, {0, 1, 'e'}, {3, 2, ' '}, {0, 3, 'd'}} {
		got, _, _, _ := sim.GetContent(cell.x, cell.y)
		if got != cell.want {
			t.Fatal("rows used different cell origins", cell, got)
		}
	}
	e.moveCursor(tcell.KeyDown)
	e.refreshScreen()
	row, col := e.position()
	x, y, _ := sim.GetCursor()
	if row != 1 || col != 5 || x != 1 || y != 1 || e.cellOff != 4 {
		t.Fatal("mixed-row cursor/scroll changed", row, col, x, y, e.cellOff)
	}
}

func TestInsertionCaretAdvancesPastMergedGrapheme(t *testing.T) {
	for _, test := range []struct{ source, paste, next, want string }{
		{"\u0301", "a", "b", "a\u0301b"},
		{"\u0301Z", "x\na", "b", "x\na\u0301bZ"},
		{"\u200d👩", "👨", "Z", "👨\u200d👩Z"},
		{"\u200d👩X", "x\n👨", "Z", "x\n👨\u200d👩ZX"},
	} {
		t.Run(test.want, func(t *testing.T) {
			e := newTestEditor(80, 24, "", nil, test.source)
			e.omitFinalNewline = true
			e.insertPaste(test.paste)
			e.insertText(test.next)
			if e.rowsToString() != test.want {
				t.Fatal("insertion reversed source after joining grapheme", e.rowsToString(), test.want)
			}
		})
	}
}

func TestCRLFPasteNormalizesOnceIncludingPartialRecovery(t *testing.T) {
	for _, complete := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "note.md")
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		screen := &scriptedScreen{SimulationScreen: tcell.NewSimulationScreen("")}
		screen.events = []tcell.Event{tcell.NewEventPaste(true), tcell.NewEventKey(tcell.KeyRune, 'A', tcell.ModNone), tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), tcell.NewEventKey(tcell.KeyLF, 0, tcell.ModNone), tcell.NewEventKey(tcell.KeyRune, 'B', tcell.ModNone)}
		if complete {
			screen.events = append(screen.events, tcell.NewEventPaste(false), tcell.NewEventKey(tcell.KeyCtrlS, 0, tcell.ModNone), tcell.NewEventKey(tcell.KeyCtrlQ, 0, tcell.ModNone))
		}
		err := runScreen(t.Context(), path, screen)
		if complete {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			var recovered *RecoveryError
			if !errors.As(err, &recovered) || !recovered.Partial {
				t.Fatal("unfinished paste recovery missing", err)
			}
			defer os.RemoveAll(filepath.Dir(recovered.Path))
			path = recovered.Path
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || string(data) != "A\nB" {
			t.Fatal("CRLF paste duplicated newline", string(data), readErr)
		}
	}
}
