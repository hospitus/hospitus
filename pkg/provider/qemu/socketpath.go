package qemu

import (
	"fmt"
	"path/filepath"
)

// maxUnixSocketPath is the practical limit for a Unix domain socket path.
//
// sockaddr_un.sun_path is 104 bytes on macOS and the BSDs and 108 on Linux; the
// smaller value is used everywhere so a data directory that works on one host
// works on all of them. One byte is reserved for the NUL terminator.
const maxUnixSocketPath = 104

// longestSocketName is the longest control socket QEMU is asked to create in a
// VM directory. Checking against it means a VM that passes the check cannot
// later fail on one of the other sockets.
const longestSocketName = "monitor.sock"

// checkSocketPathLimit reports whether QEMU's control sockets fit inside the
// platform limit when created in vmDir.
//
// QEMU binds the QMP, monitor and serial sockets by absolute path, and so does
// Hospitus when it connects to them, so a deep data directory makes every VM fail
// to start with an error that names a path the operator never chose. Failing
// early names the real cause: the data directory is too deep.
func checkSocketPathLimit(vmDir string) error {
	longest := filepath.Join(vmDir, longestSocketName)
	if len(longest)+1 <= maxUnixSocketPath {
		return nil
	}

	return fmt.Errorf(
		"VM directory %q is too deep: its control socket path is %d bytes and the limit is %d; "+
			"start hospitusd with a shorter --data-dir",
		vmDir, len(longest)+1, maxUnixSocketPath)
}
