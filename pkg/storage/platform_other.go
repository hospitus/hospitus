//go:build !freebsd && !linux

package storage

// NewPlatformBackend reports that no storage backend exists for this platform.
//
// ZFS-backed volumes are a FreeBSD and Linux feature; macOS runs QEMU and
// Podman, which manage their own disk images and image store. Returning an
// error rather than a stub that fails on every call lets the caller say the
// feature is unavailable here, instead of surfacing an error per operation.
func NewPlatformBackend() (Manager, error) {
	return nil, ErrNoPlatformBackend
}
