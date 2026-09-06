package cmdutil

import (
	"errors"
	"fmt"
)

// ExitCodeError carries the exit status of a command run inside an instance.
//
// A RunE that called os.Exit here skipped every deferred close and left
// buffered output unwritten. Returning this instead lets cobra unwind
// normally; the main package is what turns it into the process's exit status
// (see ExitCodeFrom).
type ExitCodeError struct {
	Code int
}

func (e *ExitCodeError) Error() string {
	return fmt.Sprintf("command exited with status %d", e.Code)
}

// ExitCodeFrom reports the status a command asked the CLI to exit with.
//
// It answers (0, false) for anything else, so a caller can tell "the guest's
// command failed" from "the CLI itself failed".
func ExitCodeFrom(err error) (int, bool) {
	var exitErr *ExitCodeError
	if errors.As(err, &exitErr) {
		return exitErr.Code, true
	}
	return 0, false
}
