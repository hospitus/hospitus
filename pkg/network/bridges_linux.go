//go:build linux

package network

import (
	"context"
	"fmt"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// linuxLister lists bridges through ip(8).
type linuxLister struct{ runner execx.Runner }

// NewPlatformLister returns the bridge lister for the host platform.
func NewPlatformLister() Lister { return &linuxLister{} }

// ListBridges returns every bridge on the host.
func (l *linuxLister) ListBridges(ctx context.Context) ([]BridgeInfo, error) {
	out, err := runnerOf(l.runner).Output(ctx, "ip", "-oneline", "link", "show", "type", "bridge")
	if err != nil {
		return nil, fmt.Errorf("failed to list bridges: %w", err)
	}
	return parseIPLink(string(out)), nil
}
