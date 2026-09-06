//go:build !freebsd && !linux

package network

import "context"

// otherLister reports no bridges.
type otherLister struct{}

// NewPlatformLister returns the bridge lister for the host platform.
func NewPlatformLister() Lister { return otherLister{} }

// ListBridges returns nothing: bridges are a FreeBSD and Linux feature here,
// and the macOS providers (vfkit, Apple's container runtime, QEMU) do their own
// networking without one.
func (otherLister) ListBridges(ctx context.Context) ([]BridgeInfo, error) {
	return nil, nil
}
