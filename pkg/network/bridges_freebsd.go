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
			// The runner reports a plain error, so a bridge that vanished
			// between the two calls cannot be told apart from a canceled
			// context or a broken ifconfig. Swallowing it turned both of those
			// into a short list the caller read as complete; a caller can retry
			// a failure, but cannot notice a silent omission.
			return nil, fmt.Errorf("failed to inspect bridge %s: %w", name, err)
		}
		bridges = append(bridges, parseIfconfig(name, detail))
	}
	return bridges, nil
}
