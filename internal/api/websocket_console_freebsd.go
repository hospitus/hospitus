//go:build freebsd

package api

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// ptsname returns the name of the slave pseudo-terminal device (FreeBSD).
func ptsname(f *os.File) (string, error) {
	var n int32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n)))
	if errno != 0 {
		return "", errno
	}
	return fmt.Sprintf("/dev/pts/%d", n), nil
}

// unlockpt verifies we hold the master side of the PTY (FreeBSD TIOCPTMASTER).
func unlockpt(f *os.File) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCPTMASTER, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
