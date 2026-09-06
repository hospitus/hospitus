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
			// recvCmd was already running, so zfs receive may have created and
			// partly filled cloneDataset. The receive-failure path below
			// destroys it; this one left it behind, half-written.
			p.zfsDestroyCleanup(ctx, cloneDataset)
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
		sourceSnapshot = ""
	}

	// undoClone removes what has been created so far. For a linked clone the
	// source snapshot is still ours to remove — the clone depends on it, so it
	// goes second — and leaving it behind put a clone-source-<nanos> snapshot
	// on the source dataset for every failed attempt.
	undoClone := func() {
		p.zfsDestroyCleanup(ctx, cloneDataset)
		if sourceSnapshot != "" {
			p.zfsDestroyCleanup(ctx, sourceSnapshot)
		}
	}

	mountpoint, mpErr := p.getZFSMountpoint(ctx, cloneDataset)
	if mpErr != nil {
		undoClone()
		return provider.InstanceHandle{}, fmt.Errorf("failed to get clone mountpoint: %w", mpErr)
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

	return p.finalizeClone(cloneSpec, cloneName, cloneDataset, mountpoint, undoClone)
}

// finalizeClone persists a clone's configuration and builds its handle.
//
// Both clone paths ended in the same sequence; keeping one copy is what stops
// them drifting apart, which is how one of them came to skip its rollback.
// undo is what each caller has to unwind — a linked clone also owns its source
// snapshot, a snapshot clone does not.
func (p *JailProvider) finalizeClone(spec provider.InstanceSpec, cloneName, cloneDataset, mountpoint string, undo func()) (provider.InstanceHandle, error) {
	// Without this the clone dataset exists but has no <stateDir>/<name>.json,
	// so start, stop, info and delete cannot operate on it.
	cfg := p.buildJailConfig(spec, mountpoint)
	if err := p.saveJailConfig(cfg, filepath.Join(p.stateDir, cloneName+".json")); err != nil {
		undo()
		return provider.InstanceHandle{}, fmt.Errorf("failed to persist clone config: %w", err)
	}

	return provider.InstanceHandle{
		ID:       cloneName,
		Provider: "jail",
		Metadata: map[string]interface{}{
			"zfs_dataset": cloneDataset,
			"path":        mountpoint,
		},
	}, nil
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

	// CloneInstance resolves its source through datasetFor, which confines it.
	// This path reads the name straight out of caller-supplied metadata, so a
	// handle naming a dataset outside p.zfsParent would be cloned into the
	// managed tree on its word alone.
	zfsSnapshot, err := p.snapshotFor(snapshot)
	if err != nil {
		return provider.InstanceHandle{}, err
	}

	// Same lock CloneInstance takes: without it two calls naming the same clone,
	// or a concurrent delete of it, interleave the zfs clone, the mountpoint
	// read and the config write.
	ctx, release, err := p.locks.AcquireAll(ctx, snapshot.Instance, cloneName)
	if err != nil {
		return provider.InstanceHandle{}, err
	}
	defer release()

	// Create clone dataset
	cloneDataset := fmt.Sprintf("%s/%s", p.zfsParent, cloneName)

	// Clone from snapshot using ZFS clone (always linked for snapshot clones)
	if output, err := p.cmd().CombinedOutput(ctx, "zfs", "clone", zfsSnapshot, cloneDataset); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create ZFS clone from snapshot: %w (output: %s)", err, string(output))
	}

	mountpoint, mpErr := p.getZFSMountpoint(ctx, cloneDataset)
	if mpErr != nil {
		p.zfsDestroyCleanup(ctx, cloneDataset)
		return provider.InstanceHandle{}, fmt.Errorf("failed to get clone mountpoint: %w", mpErr)
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

	return p.finalizeClone(cloneSpec, cloneName, cloneDataset, mountpoint, func() {
		p.zfsDestroyCleanup(ctx, cloneDataset)
	})
}
