package applecontainer

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// parseIP reads an address that may carry a prefix length, as the tool reports
// it ("192.168.64.3/24").
func parseIP(address string) net.IP {
	if address == "" {
		return nil
	}
	if slash := strings.IndexByte(address, '/'); slash >= 0 {
		address = address[:slash]
	}
	return net.ParseIP(address)
}

// GetInstanceMetrics returns the container's configured resources.
//
// `container stats` reports live usage, but its output is not stable across the
// pre-1.0 releases this provider targets; reporting the configuration is honest
// where inventing usage figures would not be.
func (p *Provider) GetInstanceMetrics(ctx context.Context, handle provider.InstanceHandle) (provider.Metrics, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return provider.Metrics{}, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	record, err := p.find(ctx, handle.ID)
	if err != nil {
		return provider.Metrics{}, err
	}

	return provider.Metrics{
		Timestamp:     time.Now(),
		MemoryTotalMB: record.Configuration.Resources.MemoryInBytes / (1024 * 1024),
	}, nil
}

// The operations below belong to the Provider interface but have no counterpart
// in the tool. Each returns ErrUnsupportedOperation so a caller can tell "this
// provider cannot" from "this attempt failed".

// SetInstanceResources is not supported: CPU and memory are fixed when the
// container's VM is created.
func (p *Provider) SetInstanceResources(_ context.Context, handle provider.InstanceHandle, _ provider.ResourceSpec) error {
	return fmt.Errorf("cannot change resources of %q: they are fixed when the container is created: %w",
		handle.ID, provider.ErrUnsupportedOperation)
}

// AttachDisk is not supported: mounts are declared at creation time.
func (p *Provider) AttachDisk(_ context.Context, handle provider.InstanceHandle, _ provider.DiskAttachment) error {
	return fmt.Errorf("cannot attach a disk to %q: mounts are declared at creation: %w",
		handle.ID, provider.ErrUnsupportedOperation)
}

// DetachDisk is not supported; see AttachDisk.
func (p *Provider) DetachDisk(_ context.Context, handle provider.InstanceHandle, _ string) error {
	return fmt.Errorf("cannot detach a disk from %q: mounts are declared at creation: %w",
		handle.ID, provider.ErrUnsupportedOperation)
}

// AttachNetwork is not supported: the tool attaches one network at creation.
func (p *Provider) AttachNetwork(_ context.Context, handle provider.InstanceHandle, _ provider.NetworkAttachment) error {
	return fmt.Errorf("cannot attach a network to %q: the network is chosen at creation: %w",
		handle.ID, provider.ErrUnsupportedOperation)
}

// DetachNetwork is not supported; see AttachNetwork.
func (p *Provider) DetachNetwork(_ context.Context, handle provider.InstanceHandle, _ string) error {
	return fmt.Errorf("cannot detach a network from %q: the network is chosen at creation: %w",
		handle.ID, provider.ErrUnsupportedOperation)
}
