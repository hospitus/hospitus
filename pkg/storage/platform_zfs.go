//go:build freebsd || linux

package storage

// NewPlatformBackend returns the storage backend for the host platform.
//
// ZFS is the only backend implemented, and it exists on FreeBSD and Linux.
func NewPlatformBackend() (Manager, error) {
	return NewZFSBackend(), nil
}
