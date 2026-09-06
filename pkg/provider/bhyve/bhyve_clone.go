package bhyve

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

var _ provider.CloneProvider = (*BhyveProvider)(nil)

// copyStringMap returns a shallow copy of m, or nil if m is nil. It lets clone
// specs mutate labels/annotations without aliasing the source instance's maps.
func copyStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// CloneInstance copies a stopped VM, disks and all.
//
// Every disk is copied, not just the first. A clone that carried the source's
// paths for its remaining disks had two VMs writing one ZVOL, and a
// file-backed disk beyond the first came back blank because the create path
// made a fresh image for it.
func (p *BhyveProvider) CloneInstance(ctx context.Context, source provider.InstanceHandle, cloneName string, opts provider.CloneOptions) (provider.InstanceHandle, error) {
	ctx, releaseLock, lockErr := p.locks.AcquireAll(ctx, source.ID, cloneName)
	if lockErr != nil {
		return provider.InstanceHandle{}, lockErr
	}
	defer releaseLock()

	if err := validation.ValidateInstanceName(cloneName); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid clone name: %w", err)
	}

	sourceInfo, err := p.GetInstanceInfo(ctx, source)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to get source instance: %w", err)
	}

	// Source must be stopped for consistent clone
	if sourceInfo.State != provider.StateStopped {
		return provider.InstanceHandle{}, fmt.Errorf("source instance must be stopped for cloning (current state: %s)", sourceInfo.State)
	}

	if len(sourceInfo.Spec.Disks) == 0 {
		return provider.InstanceHandle{}, fmt.Errorf("source instance has no disks")
	}

	// A physical disk cannot be copied, and handing the same device to a second
	// VM corrupts whatever is on it the moment both run.
	for i, disk := range sourceInfo.Spec.Disks {
		if disk.Type == provider.DiskTypePhysical {
			return provider.InstanceHandle{}, fmt.Errorf("disk %d of %s is a physical device (%s), which cannot be cloned; "+
				"clone the VM without it, then attach the device to whichever VM should own it", i, source.ID, disk.Path)
		}
	}

	if sourceInfo.Spec.Disks[0].Type != provider.DiskTypeZVOL {
		return provider.InstanceHandle{}, fmt.Errorf("cloning requires the boot disk to be a ZFS volume (ZVOL), got %s", sourceInfo.Spec.Disks[0].Type)
	}

	// Validate any resource overrides up-front, BEFORE creating snapshots and
	// clone datasets. Validating afterwards meant an invalid CPU/memory request
	// left orphaned snapshots and datasets behind.
	if opts.CPUs > 0 || opts.MemoryMB > 0 {
		cpus := sourceInfo.Spec.CPUs
		if opts.CPUs > 0 {
			cpus = opts.CPUs
		}
		mem := sourceInfo.Spec.MemoryMB
		if opts.MemoryMB > 0 {
			mem = opts.MemoryMB
		}
		if err := validation.ValidateResourceLimits(cpus, mem); err != nil {
			return provider.InstanceHandle{}, fmt.Errorf("invalid resource limits: %w", err)
		}
	}

	// Struct assignment is a shallow copy, so the slices and maps still alias the
	// source spec. Deep-copy everything mutated below — and ProviderConfig too,
	// which carries the TPM, VNC and passthrough settings the clone inherits.
	cloneSpec := sourceInfo.Spec
	cloneSpec.Name = cloneName
	cloneSpec.Disks = append([]provider.DiskSpec(nil), sourceInfo.Spec.Disks...)
	cloneSpec.Networks = append([]provider.NetworkSpec(nil), sourceInfo.Spec.Networks...)
	cloneSpec.Labels = copyStringMap(sourceInfo.Spec.Labels)
	cloneSpec.Annotations = copyStringMap(sourceInfo.Spec.Annotations)
	cloneSpec.ProviderConfig = copyAnyMap(sourceInfo.Spec.ProviderConfig)

	cloneDir := filepath.Join(p.dataDir, cloneName)
	copied, err := p.cloneDisks(ctx, sourceInfo.Spec.Disks, cloneSpec.Disks, cloneName, cloneDir, opts.LinkedClone)
	if err != nil {
		copied.undo(ctx, p)
		return provider.InstanceHandle{}, err
	}

	if opts.CPUs > 0 {
		cloneSpec.CPUs = opts.CPUs
	}
	if opts.MemoryMB > 0 {
		cloneSpec.MemoryMB = opts.MemoryMB
	}

	// Generate new MAC addresses if requested
	if opts.ResetMAC {
		for i := range cloneSpec.Networks {
			cloneSpec.Networks[i].MAC = ""
		}
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

	handle, err := p.CreateInstance(ctx, cloneSpec)
	if err != nil {
		copied.undo(ctx, p)
		return provider.InstanceHandle{}, err
	}

	// A UEFI guest keeps its boot entry in its own NVRAM file. Creating the
	// clone gave it a factory copy of the firmware variables, so without this
	// the guest boots to the firmware menu with no boot entry to select.
	p.copyUEFIVarsFromSource(ctx, source.ID, cloneName)

	return handle, nil
}

// clonedArtifacts records what a clone has already made, so a later failure can
// take it all back down instead of leaving full-size volumes behind.
type clonedArtifacts struct {
	datasets  []string
	snapshots []string
	dir       string
}

// undo destroys everything the clone created, best effort.
func (c clonedArtifacts) undo(ctx context.Context, p *BhyveProvider) {
	for _, ds := range c.datasets {
		p.zfsDestroyCleanup(ctx, ds)
	}
	for _, snap := range c.snapshots {
		p.zfsDestroyCleanup(ctx, snap)
	}
	if c.dir != "" {
		if err := os.RemoveAll(c.dir); err != nil {
			slog.Warn("failed to remove the clone directory after a failed clone", "dir", c.dir, logging.FieldError, err)
		}
	}
}

// cloneDisks copies every disk of the source, rewriting dest in place with the
// paths the clone owns.
//
// ZFS volumes are snapshotted and cloned or sent; a file-backed disk is copied
// into the clone's own directory. Either way CreateInstance finds a disk that
// is already there and adopts it rather than making an empty one.
func (p *BhyveProvider) cloneDisks(ctx context.Context, source, dest []provider.DiskSpec, cloneName, cloneDir string, linked bool) (clonedArtifacts, error) {
	var made clonedArtifacts

	for i, disk := range source {
		if disk.Type != provider.DiskTypeZVOL {
			path, err := p.copyDiskFile(ctx, disk.Path, cloneDir, i)
			if err != nil {
				return made, err
			}
			made.dir = cloneDir
			dest[i].Path = path
			continue
		}

		if !strings.HasPrefix(disk.Path, "/dev/zvol/") {
			return made, fmt.Errorf("disk %d of the source names %q, which is not a ZFS volume path", i, disk.Path)
		}
		sourceDataset := strings.TrimPrefix(disk.Path, "/dev/zvol/")
		cloneDataset := fmt.Sprintf("%s/%s/disk%d", p.zfsParent, cloneName, i)

		snapshot := fmt.Sprintf("%s@clone-%s-%d", sourceDataset, cloneName, time.Now().UnixNano())
		if out, err := p.cmd().CombinedOutput(ctx, "zfs", "snapshot", snapshot); err != nil {
			return made, fmt.Errorf("failed to snapshot %s: %w (output: %s)", sourceDataset, err, out)
		}
		made.snapshots = append(made.snapshots, snapshot)

		if linked {
			if out, err := p.cmd().CombinedOutput(ctx, "zfs", "clone", snapshot, cloneDataset); err != nil {
				return made, fmt.Errorf("failed to clone %s: %w (output: %s)", snapshot, err, out)
			}
			made.datasets = append(made.datasets, cloneDataset)
		} else {
			size := disk.SizeGB
			if actual := p.zvolSizeGB(ctx, sourceDataset); actual > 0 {
				size = actual
			}
			if size <= 0 {
				return made, fmt.Errorf("cannot determine the size of %s", sourceDataset)
			}
			if out, err := p.cmd().CombinedOutput(ctx, "zfs", "create", "-p", "-V", fmt.Sprintf("%dG", size), cloneDataset); err != nil {
				return made, fmt.Errorf("failed to create the clone volume %s: %w (output: %s)", cloneDataset, err, out)
			}
			made.datasets = append(made.datasets, cloneDataset)

			if err := p.streamSnapshot(ctx, snapshot, cloneDataset); err != nil {
				return made, err
			}
		}

		dest[i].Path = "/dev/zvol/" + cloneDataset
		dest[i].Type = provider.DiskTypeZVOL
	}

	// A full clone owns its blocks; the snapshots it was copied from are only
	// scaffolding. A linked clone depends on them and they must stay.
	if !linked {
		for _, snap := range made.snapshots {
			p.zfsDestroyCleanup(ctx, snap)
		}
		made.snapshots = nil
	}

	return made, nil
}

// streamSnapshot returns the injected streamer, or the production one.
func (p *BhyveProvider) streamSnapshot(ctx context.Context, snapshot, dest string) error {
	if p.sendReceive != nil {
		return p.sendReceive(ctx, snapshot, dest)
	}
	return p.zfsSendReceive(ctx, snapshot, dest)
}

// zfsSendReceive streams one snapshot into an existing volume.
//
// This is the one call the Runner abstraction cannot model: it wires a pipe
// between two processes rather than collecting output.
func (p *BhyveProvider) zfsSendReceive(ctx context.Context, snapshot, dest string) error {
	send := exec.CommandContext(ctx, "zfs", "send", snapshot)
	receive := exec.CommandContext(ctx, "zfs", "receive", "-F", dest)

	pipe, err := send.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create the pipe between send and receive: %w", err)
	}
	receive.Stdin = pipe

	if err := receive.Start(); err != nil {
		return fmt.Errorf("failed to start zfs receive: %w", err)
	}
	if err := send.Run(); err != nil {
		_ = receive.Wait()
		return fmt.Errorf("failed to send %s: %w", snapshot, err)
	}
	if err := receive.Wait(); err != nil {
		return fmt.Errorf("failed to receive into %s: %w", dest, err)
	}
	return nil
}

// copyDiskFile copies a file-backed disk into the clone's own directory,
// preserving sparseness where the filesystem supports it.
func (p *BhyveProvider) copyDiskFile(ctx context.Context, sourcePath, cloneDir string, index int) (string, error) {
	if sourcePath == "" {
		return "", fmt.Errorf("disk %d of the source has no path to copy", index)
	}
	if err := os.MkdirAll(cloneDir, 0o750); err != nil {
		return "", fmt.Errorf("failed to create the clone directory: %w", err)
	}

	dest := filepath.Join(cloneDir, fmt.Sprintf("disk%d%s", index, filepath.Ext(sourcePath)))
	if out, err := p.cmd().CombinedOutput(ctx, "cp", "-p", sourcePath, dest); err != nil {
		return "", fmt.Errorf("failed to copy disk %d from %s: %w (output: %s)", index, sourcePath, err, out)
	}
	return dest, nil
}

// copyUEFIVarsFromSource gives the clone the source's firmware variables.
//
// Best effort: a VM without UEFI has none, and a guest that boots from its
// disk without a firmware boot entry is a nuisance, not a failed clone.
func (p *BhyveProvider) copyUEFIVarsFromSource(ctx context.Context, sourceName, cloneName string) {
	sourceVars := filepath.Join(p.dataDir, sourceName, "uefi_vars.fd")
	if _, err := os.Stat(sourceVars); err != nil {
		return
	}
	cloneVars := filepath.Join(p.dataDir, cloneName, "uefi_vars.fd")
	if out, err := p.cmd().CombinedOutput(ctx, "cp", "-p", sourceVars, cloneVars); err != nil {
		slog.Warn("failed to copy the firmware variables to the clone; it may boot to the firmware menu",
			"source", sourceVars, "clone", cloneVars, logging.FieldError, err, "output", string(out))
	}
}

// copyAnyMap returns a shallow copy of m, or nil if m is nil.
//
// Provider configuration is a flat map of scalars in practice; the copy stops
// the clone's settings from writing through to the source instance's spec.
func copyAnyMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// CloneFromSnapshot creates a new instance from a snapshot.
//
// This is perfect for template-based provisioning where you maintain
// a "golden image" snapshot and rapidly create instances from it.
//
// SECURITY: Snapshot and clone names are validated.
func (p *BhyveProvider) CloneFromSnapshot(ctx context.Context, snapshot provider.SnapshotHandle, cloneName string, opts provider.CloneOptions) (provider.InstanceHandle, error) {
	// SECURITY: Validate clone name
	if err := validation.ValidateInstanceName(cloneName); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid clone name: %w", err)
	}

	// Extract ZFS snapshot name from metadata
	zfsSnapshot, ok := snapshot.Metadata["zfs_name"].(string)
	if !ok {
		return provider.InstanceHandle{}, fmt.Errorf("invalid snapshot metadata: missing zfs_name")
	}

	dataset, ok := snapshot.Metadata["dataset"].(string)
	if !ok {
		return provider.InstanceHandle{}, fmt.Errorf("invalid snapshot metadata: missing dataset")
	}

	// Create clone dataset name, where CreateInstance will look for it.
	cloneDataset := fmt.Sprintf("%s/%s/disk0", filepath.Dir(filepath.Dir(dataset)), cloneName)

	// Clone from snapshot using ZFS clone
	cmd := exec.CommandContext(ctx, "zfs", "clone", zfsSnapshot, cloneDataset)
	if output, err := cmd.CombinedOutput(); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create ZFS clone from snapshot: %w (output: %s)", err, string(output))
	}

	// Get original instance to copy its spec
	originalHandle := provider.InstanceHandle{
		ID:       snapshot.Instance,
		Provider: "bhyve",
	}

	originalInfo, err := p.GetInstanceInfo(ctx, originalHandle)
	if err != nil {
		// If we can't get original info, cleanup and fail
		p.zfsDestroyCleanup(ctx, cloneDataset)
		return provider.InstanceHandle{}, fmt.Errorf("failed to get original instance info: %w", err)
	}

	// Create instance spec for clone. Deep-copy the aliased slices/maps so the
	// mutations below cannot leak into the original instance's spec.
	cloneSpec := originalInfo.Spec
	cloneSpec.Name = cloneName
	cloneSpec.Disks = append([]provider.DiskSpec(nil), originalInfo.Spec.Disks...)
	cloneSpec.Networks = append([]provider.NetworkSpec(nil), originalInfo.Spec.Networks...)
	cloneSpec.Labels = copyStringMap(originalInfo.Spec.Labels)
	cloneSpec.Annotations = copyStringMap(originalInfo.Spec.Annotations)

	// Apply resource customizations
	if opts.CPUs > 0 {
		if err := validation.ValidateResourceLimits(opts.CPUs, cloneSpec.MemoryMB); err != nil {
			p.zfsDestroyCleanup(ctx, cloneDataset)
			return provider.InstanceHandle{}, fmt.Errorf("invalid CPU count: %w", err)
		}
		cloneSpec.CPUs = opts.CPUs
	}
	if opts.MemoryMB > 0 {
		if err := validation.ValidateResourceLimits(cloneSpec.CPUs, opts.MemoryMB); err != nil {
			p.zfsDestroyCleanup(ctx, cloneDataset)
			return provider.InstanceHandle{}, fmt.Errorf("invalid memory size: %w", err)
		}
		cloneSpec.MemoryMB = opts.MemoryMB
	}

	// Update disk path
	cloneSpec.Disks[0].Path = fmt.Sprintf("/dev/zvol/%s", cloneDataset)

	// Generate new MAC addresses if requested
	if opts.ResetMAC {
		for i := range cloneSpec.Networks {
			cloneSpec.Networks[i].MAC = ""
		}
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

	// Create the cloned instance
	return p.CreateInstance(ctx, cloneSpec)
}

// zvolSizeGB returns the size of a ZFS volume in whole gigabytes, or 0 when it
// cannot be read.
func (p *BhyveProvider) zvolSizeGB(ctx context.Context, dataset string) int {
	out, err := exec.CommandContext(ctx, "zfs", "get", "-Hp", "-o", "value", "volsize", dataset).Output()
	if err != nil {
		return 0
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || size <= 0 {
		return 0
	}
	return int(size / (1024 * 1024 * 1024))
}
