package podman

import (
	"context"
	"fmt"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Podman natively supports renaming containers via the podman rename command.

// Ensure PodmanProvider implements RenameProvider
var _ provider.RenameProvider = (*PodmanProvider)(nil)

// RenameInstance renames a stopped container.
//
// Podman allows renaming stopped or running containers. We require the
// container to be stopped for safety and consistency with the provider API.
func (p *PodmanProvider) RenameInstance(ctx context.Context, handle provider.InstanceHandle, newName string) error {
	if err := validation.ValidateInstanceName(newName); err != nil {
		return fmt.Errorf("invalid container name: %w", err)
	}

	oldName := handle.ID

	// Serialize name operations so the uniqueness check below and the podman
	// rename cannot race with a concurrent rename to the same target name.
	p.nameMu.Lock()
	defer p.nameMu.Unlock()

	// Check if new name already exists
	p.mu.RLock()
	for _, c := range p.instances {
		if c.Name == newName {
			p.mu.RUnlock()
			return fmt.Errorf("container with name %s already exists", newName)
		}
	}
	p.mu.RUnlock()

	// Check current state
	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to get container state: %w", err)
	}
	if state != provider.StateStopped {
		return fmt.Errorf("container %s must be stopped before renaming (current state: %s)", oldName, state)
	}

	// Execute rename
	output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, "rename", oldName, newName)
	if err != nil {
		return fmt.Errorf("failed to rename container: %w: %s", err, string(output))
	}

	// Update our cache
	p.mu.Lock()
	if info, ok := p.instances[oldName]; ok {
		delete(p.instances, oldName)
		info.Name = newName
		p.instances[newName] = info
	}
	p.mu.Unlock()

	return nil
}
