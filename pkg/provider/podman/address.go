package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/hospitus/hospitus/pkg/provider"
)

var _ provider.InstanceAddressProvider = (*PodmanProvider)(nil)

// podmanNetworkSettings is the part of "podman inspect" that carries addresses.
// A container on the default network repeats its address at the top level and
// under Networks; one attached to several reports each only under Networks.
type podmanNetworkSettings struct {
	IPAddress         string `json:"IPAddress"`
	GlobalIPv6Address string `json:"GlobalIPv6Address"`
	Networks          map[string]struct {
		IPAddress         string `json:"IPAddress"`
		GlobalIPv6Address string `json:"GlobalIPv6Address"`
	} `json:"Networks"`
}

// InstanceAddresses reports the addresses podman assigned to a container.
func (p *PodmanProvider) InstanceAddresses(ctx context.Context, handle provider.InstanceHandle) ([]net.IP, error) {
	output, err := p.cmd().Output(ctx, p.podmanBin, "inspect", handle.ID, "--format", "{{json .NetworkSettings}}")
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container %s: %w", handle.ID, err)
	}

	var settings podmanNetworkSettings
	if err := json.Unmarshal(output, &settings); err != nil {
		return nil, fmt.Errorf("failed to parse network settings for %s: %w", handle.ID, err)
	}

	var ips []net.IP
	seen := make(map[string]bool)
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		if ip := net.ParseIP(s); ip != nil {
			seen[s] = true
			ips = append(ips, ip)
		}
	}

	add(settings.IPAddress)
	add(settings.GlobalIPv6Address)
	for _, network := range settings.Networks {
		add(network.IPAddress)
		add(network.GlobalIPv6Address)
	}

	return ips, nil
}
