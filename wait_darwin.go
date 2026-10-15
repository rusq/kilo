//go:build darwin

package kilo

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

func waitEditorInput(fd int) (int, error) {
	// Darwin poll rejects /dev/tty with POLLNVAL, although select/read work.
	var read unix.FdSet
	if fd < 0 || fd >= int(unsafe.Sizeof(read))*8 {
		return 0, fmt.Errorf("editor input descriptor exceeds select capacity")
	}
	read.Set(fd)
	timeout := unix.NsecToTimeval(50_000_000)
	return unix.Select(fd+1, &read, nil, nil, &timeout)
}
