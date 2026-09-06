package jail

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

var _ provider.CloneProvider = (*JailProvider)(nil)

// CloneInstance creates a new jail by cloning an existing one.
//
// Jail cloning uses ZFS clone functionality which is extremely fast.
// ZFS clones share filesystem blocks with the source (copy-on-write).
//
// SECURITY: Jail names and parameters are validated.
func (p *JailProvider) CloneInstance(ctx context.Context, source provider.InstanceHandle, cloneName string, opts provider.CloneOptions) (provider.InstanceHandle, error) {
	ctx, releaseLock, lockErr := p.locks.AcquireAll(ctx, source.ID, cloneName)
	if lockErr != nil {
		return provider.InstanceHandle{}, lockErr
	}
	defer releaseLock()
	// SECURITY: Validate clone name
	if err := validation.ValidateInstanceName(cloneName); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid clone name: %w", err)
	}

	sourceDataset, err := p.datasetFor(source)
	if err != nil {
		return provider.InstanceHandle{}, err
	}

	// Get source jail info
	sourceInfo, err := p.GetInstanceInfo(ctx, source)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to get source jail info: %w", err)
	}

	// Source must be stopped for consistent clone
	if sourceInfo.State != provider.StateStopped {
		return provider.InstanceHandle{}, fmt.Errorf("source jail must be stopped for cloning (current state: %s)", sourceInfo.State)
	}

	// Create a snapshot first (required for ZFS clone). Use nanosecond
	// precision so two clones of the same source started in the same second
	// do not collide on the snapshot name.
	snapshotName := fmt.Sprintf("clone-source-%d", time.Now().UnixNano())
	sourceSnapshot := fmt.Sprintf("%s@%s", sourceDataset, snapshotName)

	if output, err := p.cmd().CombinedOutput(ctx, "zfs", "snapshot", sourceSnapshot); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create source snapshot: %w (output: %s)", err, string(output))
	}

	// Create clone dataset
	cloneDataset := fmt.Sprintf("%s/%s", p.zfsParent, cloneName)

	if opts.LinkedClone {
		// Linked clone: Uses ZFS clone (shares blocks with source)
		if output, err := p.cmd().CombinedOutput(ctx, "zfs", "clone", sourceSnapshot, cloneDataset); err != nil {
			p.zfsDestroyCleanup(ctx, sourceSnapshot)
			return provider.InstanceHandle{}, fmt.Errorf("failed to create ZFS clone: %w (output: %s)", err, string(output))
		}
	} else {
		// Full clone: Use zfs send/receive
		sendCmd := exec.CommandContext(ctx, "zfs", "send", sourceSnapshot)
		recvCmd := exec.CommandContext(ctx, "zfs", "receive", cloneDataset)

		pipe, err := sendCmd.StdoutPipe()
		if err != nil {
			p.zfsDestroyCleanup(ctx, sourceSnapshot)
			return provider.InstanceHandle{}, fmt.Errorf("failed to create pipe: %w", err)
		}
		recvCmd.Stdin = pipe

		if err := recvCmd.Start(); err != nil {
			p.zfsDestroyCleanup(ctx, sourceSnapshot)
			return provider.InstanceHandle{}, fmt.Errorf("failed to start receive: %w", err)
		}

		if err := sendCmd.Run(); err != nil {
			_ = recvCmd.Wait() // Best effort wait
			p.zfsDestroyCleanup(ctx, sourceSnapshot)
			return provider.InstanceHandle{}, fmt.Errorf("failed to send data: %w", err)
		}

		if err := recvCmd.Wait(); err != nil {
			p.zfsDestroyCleanup(ctx, cloneDataset)
			p.zfsDestroyCleanup(ctx, sourceSnapshot)
			return provider.InstanceHandle{}, fmt.Errorf("failed to receive data: %w", err)
		}

		// Cleanup source snapshot (no longer needed for full clone)
		p.zfsDestroyCleanup(ctx, sourceSnapshot)
	}

	mountpoint, err := p.getZFSMountpoint(ctx, cloneDataset)
	if err != nil {
		p.zfsDestroyCleanup(ctx, cloneDataset)
		return provider.InstanceHandle{}, fmt.Errorf("failed to get clone mountpoint: %w", err)
	}

	// Create jail spec for clone
	cloneSpec := sourceInfo.Spec
	cloneSpec.Name = cloneName

	// Apply resource customizations if provided
	if opts.CPUs > 0 {
		cloneSpec.CPUs = opts.CPUs
	}
	if opts.MemoryMB > 0 {
		cloneSpec.MemoryMB = opts.MemoryMB
	}

	// Apply custom labels and annotations
	if opts.Labels != nil {
		if cloneSpec.Labels == nil {
			cloneSpec.Labels = make(map[string]string)
		}
		for k, v := range opts.Labels {
			cloneSpec.Labels[k] = v
		}
	}
	if opts.Annotations != nil {
		if cloneSpec.Annotations == nil {
			cloneSpec.Annotations = make(map[string]string)
		}
		for k, v := range opts.Annotations {
			cloneSpec.Annotations[k] = v
		}
	}

	// Honor ResetMAC: clear inherited MAC addresses so the clone gets fresh
	// ones instead of colliding with the source on the same L2 segment.
	if opts.ResetMAC {
		for i := range cloneSpec.Networks {
			cloneSpec.Networks[i].MAC = ""
		}
	}

	// Persist the clone's config so it is a usable, manageable jail. Without
	// this the clone dataset exists but has no <stateDir>/<name>.json, so
	// start/stop/info cannot operate on it.
	cfg := p.buildJailConfig(cloneSpec, mountpoint)
	if err := p.saveJailConfig(cfg, filepath.Join(p.stateDir, cloneName+".json")); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to persist clone config: %w", err)
	}

	// Create instance handle
	handle := provider.InstanceHandle{
		ID:       cloneName,
		Provider: "jail",
		Metadata: map[string]interface{}{
			"zfs_dataset": cloneDataset,
			"path":        mountpoint,
		},
	}

	return handle, nil
}

// CloneFromSnapshot creates a new jail from a snapshot.
//
// This is perfect for template-based provisioning where you maintain
// a "golden image" snapshot and rapidly create jails from it.
//
// SECURITY: Snapshot and clone names are validated.
func (p *JailProvider) CloneFromSnapshot(ctx context.Context, snapshot provider.SnapshotHandle, cloneName string, opts provider.CloneOptions) (provider.InstanceHandle, error) {
	// SECURITY: Validate clone name
	if err := validation.ValidateInstanceName(cloneName); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid clone name: %w", err)
	}

	// Extract ZFS snapshot name from metadata
	zfsSnapshot, ok := snapshot.Metadata["zfs_name"].(string)
	if !ok {
		return provider.InstanceHandle{}, fmt.Errorf("invalid snapshot metadata: missing zfs_name")
	}

	// Create clone dataset
	cloneDataset := fmt.Sprintf("%s/%s", p.zfsParent, cloneName)

	// Clone from snapshot using ZFS clone (always linked for snapshot clones)
	if output, err := p.cmd().CombinedOutput(ctx, "zfs", "clone", zfsSnapshot, cloneDataset); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create ZFS clone from snapshot: %w (output: %s)", err, string(output))
	}

	mountpoint, err := p.getZFSMountpoint(ctx, cloneDataset)
	if err != nil {
		p.zfsDestroyCleanup(ctx, cloneDataset)
		return provider.InstanceHandle{}, fmt.Errorf("failed to get clone mountpoint: %w", err)
	}

	// Get original jail to copy its spec
	originalHandle := provider.InstanceHandle{
		ID:       snapshot.Instance,
		Provider: "jail",
	}

	originalInfo, err := p.GetInstanceInfo(ctx, originalHandle)
	if err != nil {
		// If we can't get original info, cleanup and fail
		p.zfsDestroyCleanup(ctx, cloneDataset)
		return provider.InstanceHandle{}, fmt.Errorf("failed to get original jail info: %w", err)
	}

	// Create jail spec for clone
	cloneSpec := originalInfo.Spec
	cloneSpec.Name = cloneName

	// Apply resource customizations
	if opts.CPUs > 0 {
		cloneSpec.CPUs = opts.CPUs
	}
	if opts.MemoryMB > 0 {
		cloneSpec.MemoryMB = opts.MemoryMB
	}

	// Apply labels and annotations
	if opts.Labels != nil {
		if cloneSpec.Labels == nil {
			cloneSpec.Labels = make(map[string]string)
		}
		for k, v := range opts.Labels {
			cloneSpec.Labels[k] = v
		}
	}
	if opts.Annotations != nil {
		if cloneSpec.Annotations == nil {
			cloneSpec.Annotations = make(map[string]string)
		}
		for k, v := range opts.Annotations {
			cloneSpec.Annotations[k] = v
		}
	}

	// Honor ResetMAC: clear inherited MAC addresses so the clone gets fresh
	// ones instead of colliding with the source on the same L2 segment.
	if opts.ResetMAC {
		for i := range cloneSpec.Networks {
			cloneSpec.Networks[i].MAC = ""
		}
	}

	// Persist the clone's config so it is a usable, manageable jail. Without
	// this the clone dataset exists but has no <stateDir>/<name>.json, so
	// start/stop/info cannot operate on it.
	cfg := p.buildJailConfig(cloneSpec, mountpoint)
	if err := p.saveJailConfig(cfg, filepath.Join(p.stateDir, cloneName+".json")); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to persist clone config: %w", err)
	}

	// Create instance handle
	handle := provider.InstanceHandle{
		ID:       cloneName,
		Provider: "jail",
		Metadata: map[string]interface{}{
			"zfs_dataset": cloneDataset,
			"path":        mountpoint,
		},
	}

	return handle, nil
}
