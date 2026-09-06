//go:build freebsd

package network

import (
	"context"
	"fmt"
	"strings"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// freebsdLister lists bridges through ifconfig(8).
type freebsdLister struct{ runner execx.Runner }

// NewPlatformLister returns the bridge lister for the host platform.
func NewPlatformLister() Lister { return &freebsdLister{} }

// ListBridges returns every bridge on the host.
//
// FreeBSD puts every bridge interface in the "bridge" group whatever its name,
// so asking for the group finds the ones an operator named themselves as well
// as the ones the daemon created.
func (l *freebsdLister) ListBridges(ctx context.Context) ([]BridgeInfo, error) {
	out, err := runnerOf(l.runner).Output(ctx, "ifconfig", "-g", "bridge")
	if err != nil {
		return nil, fmt.Errorf("failed to list bridges: %w", err)
	}

	var bridges []BridgeInfo
	for _, name := range strings.Fields(string(out)) {
		detail, err := runnerOf(l.runner).Output(ctx, "ifconfig", name)
		if err != nil {
			// The bridge went away between the two calls; report the rest.
			continue
		}
		bridges = append(bridges, parseIfconfig(name, detail))
	}
	return bridges, nil
}
