//go:build darwin || linux

package kilo

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type unixTerminalDriver struct{ input *os.File }

func newTerminalDriver(input, _ *os.File) terminalDriver {
	return &unixTerminalDriver{input: input}
}
func (d *unixTerminalDriver) start() (func() error, error) {
	state, err := term.MakeRaw(int(d.input.Fd()))
	if err != nil {
		return nil, err
	}
	return func() error { return term.Restore(int(d.input.Fd()), state) }, nil
}
func (d *unixTerminalDriver) read(ctx context.Context, publish func([]byte) bool, idle func(time.Time)) error {
	fd := int(d.input.Fd())
	buffer := make([]byte, 4096)
	for ctx.Err() == nil {
		n, err := waitEditorInput(fd)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			idle(time.Now())
			continue
		}
		n, err = unix.Read(fd, buffer)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.EOF
		}
		if !publish(buffer[:n]) {
			return ctx.Err()
		}
		idle(time.Now())
	}
	return ctx.Err()
}
func openEditorTerminal() (*os.File, *os.File, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	return f, f, err
}
