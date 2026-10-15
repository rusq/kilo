//go:build linux

package kilo

import (
	"errors"

	"golang.org/x/sys/unix"
)

func waitEditorInput(fd int) (int, error) {
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, 50)
	if err == nil && fds[0].Revents&unix.POLLNVAL != 0 {
		return 0, errors.New("editor input descriptor is invalid")
	}
	return n, err
}
