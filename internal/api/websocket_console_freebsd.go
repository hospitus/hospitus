//go:build freebsd

package api

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// ioctl runs one ioctl on a file through its SyscallConn.
//
// f.Fd() is what these used to call: it takes the file out of the runtime
// poller and leaves the descriptor in blocking mode for good, which for a PTY
// master means every later read blocks the OS thread it runs on. Control
// hands the raw descriptor over for the duration of the call and no longer.
func ioctl(f *os.File, request uintptr, arg uintptr) error {
	conn, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := conn.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, request, arg)
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}

// ptsname returns the name of the slave pseudo-terminal device (FreeBSD).
func ptsname(f *os.File) (string, error) {
	var n int32
	if err := ioctl(f, syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); err != nil {
		return "", err
	}
	return fmt.Sprintf("/dev/pts/%d", n), nil
}

// unlockpt verifies we hold the master side of the PTY (FreeBSD TIOCPTMASTER).
func unlockpt(f *os.File) error {
	return ioctl(f, syscall.TIOCPTMASTER, 0)
}
