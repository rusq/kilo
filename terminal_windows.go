//go:build windows

package kilo

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

type windowsTerminalDriver struct{ input, output *os.File }

func newTerminalDriver(input, output *os.File) terminalDriver {
	return &windowsTerminalDriver{input: input, output: output}
}
func (d *windowsTerminalDriver) start() (func() error, error) {
	in, out := windows.Handle(d.input.Fd()), windows.Handle(d.output.Fd())
	var inputMode, outputMode uint32
	if err := windows.GetConsoleMode(in, &inputMode); err != nil {
		return nil, err
	}
	if err := windows.GetConsoleMode(out, &outputMode); err != nil {
		return nil, err
	}
	inputCP, err := windows.GetConsoleCP()
	if err != nil {
		return nil, err
	}
	outputCP, err := windows.GetConsoleOutputCP()
	if err != nil {
		return nil, err
	}
	restore := func() error {
		return errors.Join(windows.SetConsoleMode(in, inputMode), windows.SetConsoleMode(out, outputMode), windows.SetConsoleCP(inputCP), windows.SetConsoleOutputCP(outputCP))
	}
	raw := inputMode &^ (windows.ENABLE_ECHO_INPUT | windows.ENABLE_LINE_INPUT | windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_QUICK_EDIT_MODE)
	raw |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT | windows.ENABLE_EXTENDED_FLAGS
	err = errors.Join(windows.SetConsoleMode(in, raw), windows.SetConsoleMode(out, outputMode|windows.ENABLE_PROCESSED_OUTPUT|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING), windows.SetConsoleCP(65001), windows.SetConsoleOutputCP(65001))
	if err != nil {
		return nil, errors.Join(err, restore())
	}
	return restore, nil
}

var cancelConsoleRead = windows.NewLazySystemDLL("kernel32.dll").NewProc("CancelSynchronousIo")

func (d *windowsTerminalDriver) read(ctx context.Context, publish func([]byte) bool, idle func(time.Time)) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	thread, err := windows.OpenThread(windows.THREAD_TERMINATE, false, windows.GetCurrentThreadId())
	if err != nil {
		return err
	}
	defer windows.CloseHandle(thread)
	if err = cancelConsoleRead.Find(); err != nil {
		return err
	}
	finished, joined, idleJoined := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
		case <-finished:
			return
		}
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			cancelConsoleRead.Call(uintptr(thread))
			select {
			case <-finished:
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		defer close(idleJoined)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				idle(now)
			case <-finished:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { close(finished); <-joined; <-idleJoined }()
	buffer := make([]uint16, 2048)
	var high uint16
	for ctx.Err() == nil {
		var count uint32
		err = windows.ReadConsole(windows.Handle(d.input.Fd()), &buffer[0], uint32(len(buffer)), &count, nil)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if count == 0 {
			return io.EOF
		}
		data := make([]byte, 0, int(count)*3)
		for _, unit := range buffer[:count] {
			if high != 0 {
				if unit < 0xDC00 || unit > 0xDFFF {
					return errors.New("invalid UTF-16 editor input")
				}
				data = utf8.AppendRune(data, utf16.DecodeRune(rune(high), rune(unit)))
				high = 0
			} else if unit >= 0xD800 && unit <= 0xDBFF {
				high = unit
			} else if unit >= 0xDC00 && unit <= 0xDFFF {
				return errors.New("invalid UTF-16 editor input")
			} else {
				data = utf8.AppendRune(data, rune(unit))
			}
		}
		if !publish(data) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}

func openEditorTerminal() (*os.File, *os.File, error) {
	input, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	output, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		input.Close()
		return nil, nil, err
	}
	return input, output, nil
}
