// Package cmdutil provides utility functions for the CLI.
package cmdutil

import (
	"fmt"
	"os"
	"runtime"
)

// IsRoot returns true if the current process is running as root (uid 0)
func IsRoot() bool {
	return os.Getuid() == 0
}

// RequireRoot checks if the process is running as root and returns an error if not.
// If skipOnDarwin is true, the check is skipped on macOS (for podman machine which
// handles privileges internally).
func RequireRoot(skipOnDarwin bool) error {
	if IsRoot() {
		return nil
	}
	if skipOnDarwin && runtime.GOOS == "darwin" {
		return nil
	}
	return fmt.Errorf("this command requires root privileges\n  Run with: sudo, doas, or as root")
}

// RequireRootStrict always requires root, regardless of platform
func RequireRootStrict() error {
	return RequireRoot(false)
}
