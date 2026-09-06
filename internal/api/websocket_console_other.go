//go:build !freebsd

package api

import (
	"fmt"
	"os"
)

// PTY-based interactive consoles are a FreeBSD (jail/bhyve) feature. On other
// platforms these stubs let the daemon compile; the console endpoints return
// this error at runtime.
var errConsoleUnsupported = fmt.Errorf("interactive PTY console is only supported on FreeBSD")

func ptsname(_ *os.File) (string, error) { return "", errConsoleUnsupported }

func unlockpt(_ *os.File) error { return errConsoleUnsupported }

func setPtySize(_ *os.File, _, _ int) error { return errConsoleUnsupported }
