package podman

import (
	"context"
	"fmt"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Podman natively supports pausing and unpausing containers via
// the podman pause and podman unpause commands.

// Ensure PodmanProvider implements PauseProvider
var _ provider.PauseProvider = (*PodmanProvider)(nil)

// PauseInstance freezes execution of a running container.
func (p *PodmanProvider) PauseInstance(ctx context.Context, handle provider.InstanceHandle) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if out, err := p.cmd().CombinedOutput(ctx, p.podmanBin, "pause", handle.ID); err != nil {
		return fmt.Errorf("podman pause failed: %s: %w", string(out), err)
	}

	p.setInstanceState(handle.ID, provider.StatePaused, time.Time{})

	return nil
}

// ResumeInstance unfreezes a paused container.
func (p *PodmanProvider) ResumeInstance(ctx context.Context, handle provider.InstanceHandle) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if out, err := p.cmd().CombinedOutput(ctx, p.podmanBin, "unpause", handle.ID); err != nil {
		return fmt.Errorf("podman unpause failed: %s: %w", string(out), err)
	}

	p.setInstanceState(handle.ID, provider.StateRunning, time.Time{})

	return nil
}
