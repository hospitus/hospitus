package vfkit

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
)

// The Provider interface covers operations Virtualization.framework does not
// offer, or that vfkit does not expose. Each one says which of the two it is,
// and returns ErrUnsupportedOperation so a caller can tell "this provider
// cannot" from "this attempt failed" — reporting success would leave the caller
// believing a disk was attached or a limit applied.

// SetInstanceResources is not supported: vfkit fixes CPU and memory at boot, so
// changing them means recreating the VM.
func (p *VFKitProvider) SetInstanceResources(_ context.Context, handle provider.InstanceHandle, _ provider.ResourceSpec) error {
	return fmt.Errorf("cannot change resources of %q while it exists: vfkit sets CPU and memory at boot: %w",
		handle.ID, provider.ErrUnsupportedOperation)
}

// GetInstanceMetrics returns what the control socket knows.
//
// Virtualization.framework exposes no CPU or I/O accounting, so the figures are
// the VM's configuration rather than its consumption; inventing usage numbers
// would be worse than reporting none.
func (p *VFKitProvider) GetInstanceMetrics(ctx context.Context, handle provider.InstanceHandle) (provider.Metrics, error) {
	inspect, err := p.rest().Inspect(ctx, p.restSocket(handle.ID))
	if err != nil {
		return provider.Metrics{}, fmt.Errorf("failed to read VM details: %w", err)
	}

	// The value comes from the VM's own configuration, so it cannot exceed the
	// memory a host can have; clamp anyway rather than wrap into a negative.
	memoryMB := inspect.MemoryBytes / (1024 * 1024)
	if memoryMB > math.MaxInt64 {
		memoryMB = math.MaxInt64
	}

	return provider.Metrics{
		Timestamp:     time.Now(),
		MemoryTotalMB: int64(memoryMB), //nolint:gosec // G115: clamped to MaxInt64 above
	}, nil
}

// AttachDisk is not supported: the framework has no disk hotplug, and vfkit
// builds its device list once at boot.
func (p *VFKitProvider) AttachDisk(_ context.Context, handle provider.InstanceHandle, _ provider.DiskAttachment) error {
	return fmt.Errorf("cannot attach a disk to %q: Virtualization.framework has no disk hotplug: %w",
		handle.ID, provider.ErrUnsupportedOperation)
}

// DetachDisk is not supported; see AttachDisk.
func (p *VFKitProvider) DetachDisk(_ context.Context, handle provider.InstanceHandle, _ string) error {
	return fmt.Errorf("cannot detach a disk from %q: Virtualization.framework has no disk hotplug: %w",
		handle.ID, provider.ErrUnsupportedOperation)
}

// AttachNetwork is not supported: interfaces are declared at boot, and the VM
// gets one NAT interface from CreateInstance.
func (p *VFKitProvider) AttachNetwork(_ context.Context, handle provider.InstanceHandle, _ provider.NetworkAttachment) error {
	return fmt.Errorf("cannot attach an interface to %q: vfkit declares devices at boot: %w",
		handle.ID, provider.ErrUnsupportedOperation)
}

// DetachNetwork is not supported; see AttachNetwork.
func (p *VFKitProvider) DetachNetwork(_ context.Context, handle provider.InstanceHandle, _ string) error {
	return fmt.Errorf("cannot detach an interface from %q: vfkit declares devices at boot: %w",
		handle.ID, provider.ErrUnsupportedOperation)
}
