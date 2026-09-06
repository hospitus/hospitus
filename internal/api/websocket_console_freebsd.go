//go:build freebsd

package api

import (
	"fmt"
	"math"
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
//
// arg is an unsafe.Pointer rather than a uintptr because the conversion has to
// happen inside the syscall.Syscall call expression — that is the one form the
// garbage collector recognizes as keeping the pointee alive and pinned. A
// caller converting first handed the kernel an address the stack was free to
// move out from under it.
func ioctl(f *os.File, request uintptr, arg unsafe.Pointer) error {
	conn, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := conn.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(arg))
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
	if err := ioctl(f, syscall.TIOCGPTN, unsafe.Pointer(&n)); err != nil {
		return "", err
	}
	return fmt.Sprintf("/dev/pts/%d", n), nil
}

// unlockpt verifies we hold the master side of the PTY (FreeBSD TIOCPTMASTER).
func unlockpt(f *os.File) error {
	return ioctl(f, syscall.TIOCPTMASTER, nil)
}

// setPtySize sets the window size of a PTY.
//
// Through ioctl, not f.Fd(): a resize message arriving mid-session used to
// unregister the PTY master from the poller, and the readLoop blocked on it
// from then on pinned an OS thread for the life of the console.
func setPtySize(f *os.File, cols, rows int) error {
	ws := struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}{
		Row: clampToUint16(rows),
		Col: clampToUint16(cols),
	}
	return ioctl(f, syscall.TIOCSWINSZ, unsafe.Pointer(&ws))
}

// clampToUint16 converts a signed dimension into the uint16 range expected by
// TIOCSWINSZ, guarding against integer overflow.
//
// Here rather than in websocket_console.go: setPtySize is the only caller and
// it is platform-specific, so on macOS and Linux this was dead code.
func clampToUint16(v int) uint16 {
	if v <= 0 {
		return 0
	}
	if v >= math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(v)
}
