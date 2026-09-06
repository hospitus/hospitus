//go:build freebsd || linux
// +build freebsd linux

// ZFS storage backend implementation for the storage package.

package storage

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hospitus/hospitus/pkg/dataset"
	"github.com/hospitus/hospitus/pkg/provider/execx"
	"github.com/hospitus/hospitus/pkg/validation"
)

// ZFSBackend implements the Manager interface for ZFS
type ZFSBackend struct {
	mu sync.RWMutex

	config        Config
	parentDataset string

	// runner executes zfs(8). Tests inject an execx.Fake to assert on the exact
	// command lines without a ZFS pool; production uses execx.OS.
	runner execx.Runner
}

// NewZFSBackend creates a new ZFS storage backend
func NewZFSBackend() *ZFSBackend {
	return &ZFSBackend{runner: execx.Default()}
}

// cmd returns the command runner, defaulting to the real os/exec backend when
// the backend was built as a bare struct literal.
func (z *ZFSBackend) cmd() execx.Runner {
	if z.runner == nil {
		return execx.Default()
	}
	return z.runner
}

// Initialize sets up the ZFS storage backend
func (z *ZFSBackend) Initialize(ctx context.Context, config Config) error {
	z.mu.Lock()
	defer z.mu.Unlock()

	z.config = config

	// Set parent dataset
	if config.ZFSParentDataset != "" {
		z.parentDataset = config.ZFSParentDataset
	} else {
		z.parentDataset = dataset.Parent()
	}

	// Check if ZFS is available
	if _, err := exec.LookPath("zfs"); err != nil {
		return fmt.Errorf("zfs command not found: %w", err)
	}

	// Ensure parent dataset exists
	if err := z.ensureDataset(ctx, z.parentDataset); err != nil {
		return fmt.Errorf("failed to ensure parent dataset: %w", err)
	}

	return nil
}

// Shutdown cleans up resources
func (z *ZFSBackend) Shutdown(ctx context.Context) error {
	return nil
}

// ===== VolumeManager Implementation =====

// CreateVolume creates a new ZFS dataset
func (z *ZFSBackend) CreateVolume(ctx context.Context, name string, opts VolumeOptions) (*Volume, error) {
	z.mu.Lock()
	defer z.mu.Unlock()

	ds := z.fullDatasetName(name)

	// Check if already exists
	if z.datasetExistsNoLock(ctx, ds) {
		return nil, &StorageError{Op: "create volume", Volume: name, Backend: "zfs", Err: errExists}
	}

	// Build create command with options
	args := []string{"create"}

	// A size makes this a zvol — a block device rather than a filesystem, which
	// is what a VM disk is. The two shapes differ in more than a flag: a zvol has
	// neither quota nor mountpoint, and ZFS rejects those properties on one.
	isVolume := opts.Size != ""
	if isVolume {
		args = append(args, "-V", opts.Size)
	}

	// zfs(8) rejects a property given twice with "specified multiple times", so
	// each of these yields to an explicit entry in Properties: a caller naming
	// compression must not collide with the backend's own default.
	if _, given := opts.Properties["quota"]; !given && opts.Quota != "" && !isVolume {
		args = append(args, "-o", "quota="+opts.Quota)
	}
	if _, given := opts.Properties["mountpoint"]; !given && opts.Mountpoint != "" && !isVolume {
		args = append(args, "-o", "mountpoint="+opts.Mountpoint)
	}
	if _, given := opts.Properties["compression"]; !given && z.config.Compression != "" && z.config.Compression != "off" {
		args = append(args, "-o", "compression="+z.config.Compression)
	}

	// Add custom properties
	for k, v := range opts.Properties {
		args = append(args, "-o", k+"="+v)
	}

	// Create parent datasets if needed (-p flag)
	args = append(args, "-p", ds)

	output, err := z.cmd().CombinedOutput(ctx, "zfs", args...)
	if err != nil {
		return nil, &StorageError{
			Op:      "create volume",
			Volume:  name,
			Backend: "zfs",
			Err:     fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err),
		}
	}

	// Copy content if specified. A missing/unusable mountpoint must be a hard
	// error here: silently skipping the copy would return an empty volume that
	// the caller believes was populated from CopyFrom.
	if opts.CopyFrom != "" {
		mountpoint, mpErr := z.getMountpoint(ctx, ds)
		if mpErr != nil || mountpoint == "" || mountpoint == "none" || mountpoint == "legacy" {
			_ = z.destroyDataset(ctx, ds, true)
			return nil, &StorageError{
				Op:      "create volume",
				Volume:  name,
				Backend: "zfs",
				Err:     fmt.Errorf("cannot copy content: dataset %s has no usable mountpoint (%q): %w", ds, mountpoint, mpErr),
			}
		}
		if cpOutput, cpErr := z.cmd().CombinedOutput(ctx, "cp", "-a", opts.CopyFrom+"/.", mountpoint+"/"); cpErr != nil {
			// Cleanup on failure
			_ = z.destroyDataset(ctx, ds, true)
			return nil, fmt.Errorf("failed to copy content: %s: %w", string(cpOutput), cpErr)
		}
	}

	return z.getVolumeNoLock(ctx, name)
}

// DeleteVolume deletes a ZFS dataset
func (z *ZFSBackend) DeleteVolume(ctx context.Context, name string, opts DeleteOptions) error {
	z.mu.Lock()
	defer z.mu.Unlock()

	ds := z.fullDatasetName(name)

	if !z.datasetExistsNoLock(ctx, ds) {
		return nil // Already deleted
	}

	args := []string{"destroy"}
	if opts.Force {
		args = append(args, "-f")
	}
	if opts.Recursive {
		args = append(args, "-r")
	}
	args = append(args, ds)

	output, err := z.cmd().CombinedOutput(ctx, "zfs", args...)
	if err != nil {
		outputStr := strings.TrimSpace(string(output))
		// If already gone, ignore
		if strings.Contains(outputStr, "dataset does not exist") {
			return nil
		}
		return &StorageError{
			Op:      "delete volume",
			Volume:  name,
			Backend: "zfs",
			Err:     fmt.Errorf("%s: %w", outputStr, err),
		}
	}

	return nil
}

// GetVolume returns information about a volume
func (z *ZFSBackend) GetVolume(ctx context.Context, name string) (*Volume, error) {
	z.mu.RLock()
	defer z.mu.RUnlock()
	return z.getVolumeNoLock(ctx, name)
}

func (z *ZFSBackend) getVolumeNoLock(ctx context.Context, name string) (*Volume, error) {
	ds := z.fullDatasetName(name)

	if !z.datasetExistsNoLock(ctx, ds) {
		return nil, &StorageError{Op: "get volume", Volume: name, Backend: "zfs", Err: errNotFound}
	}

	props, err := z.getProperties(ctx, ds)
	if err != nil {
		return nil, err
	}

	vol := &Volume{
		Name:       name,
		FullName:   ds,
		Backend:    "zfs",
		Properties: props,
		State:      "mounted",
	}

	// Parse properties
	if mp, ok := props["mountpoint"]; ok {
		vol.Path = mp
	}
	if used, ok := props["used"]; ok {
		vol.Used = parseZFSSize(used)
	}
	if avail, ok := props["available"]; ok {
		vol.Available = parseZFSSize(avail)
	}
	if quota, ok := props["quota"]; ok && quota != "none" {
		vol.Quota = parseZFSSize(quota)
	}
	if creation, ok := props["creation"]; ok {
		if ts, err := strconv.ParseInt(creation, 10, 64); err == nil {
			vol.Created = time.Unix(ts, 0)
		}
	}

	vol.Size = vol.Used + vol.Available

	// Get snapshot count
	snapshots, _ := z.listSnapshotsNoLock(ctx, name)
	vol.SnapshotCount = len(snapshots)

	return vol, nil
}

// ListVolumes returns all managed volumes
func (z *ZFSBackend) ListVolumes(ctx context.Context) ([]Volume, error) {
	z.mu.RLock()
	defer z.mu.RUnlock()
	return z.listVolumesNoLock(ctx)
}

// listVolumesNoLock lists volumes without taking z.mu. Callers must already
// hold at least the read lock. It exists so lock-holding methods (e.g.
// ListClones) can reuse the logic without re-acquiring a non-reentrant RLock.
func (z *ZFSBackend) listVolumesNoLock(ctx context.Context) ([]Volume, error) {
	output, err := z.cmd().Output(ctx, "zfs", "list", "-H", "-r", "-t", "filesystem",
		"-o", "name", z.parentDataset)
	if err != nil {
		return nil, &StorageError{Op: "list volumes", Backend: "zfs", Err: err}
	}

	var volumes []Volume
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	for _, line := range lines {
		if line == "" || line == z.parentDataset {
			continue
		}

		// Extract short name
		name := strings.TrimPrefix(line, z.parentDataset+"/")
		if name == line {
			continue // Not a child of our parent
		}

		vol, err := z.getVolumeNoLock(ctx, name)
		if err != nil {
			continue
		}
		volumes = append(volumes, *vol)
	}

	return volumes, nil
}

// VolumeExists checks if a volume exists
func (z *ZFSBackend) VolumeExists(ctx context.Context, name string) bool {
	z.mu.RLock()
	defer z.mu.RUnlock()
	return z.datasetExistsNoLock(ctx, z.fullDatasetName(name))
}

// GetVolumePath returns the filesystem path for a volume
func (z *ZFSBackend) GetVolumePath(ctx context.Context, name string) (string, error) {
	z.mu.RLock()
	defer z.mu.RUnlock()
	return z.getMountpoint(ctx, z.fullDatasetName(name))
}

// ResizeVolume resizes a volume by adjusting the quota
func (z *ZFSBackend) ResizeVolume(ctx context.Context, name, newSize string) error {
	return z.SetQuota(ctx, name, newSize)
}

// MountVolume mounts a volume
func (z *ZFSBackend) MountVolume(ctx context.Context, name string) error {
	z.mu.Lock()
	defer z.mu.Unlock()

	ds := z.fullDatasetName(name)
	output, err := z.cmd().CombinedOutput(ctx, "zfs", "mount", ds)
	if err != nil {
		// Ignore "already mounted" errors
		if strings.Contains(string(output), "already mounted") {
			return nil
		}
		return &StorageError{Op: "mount", Volume: name, Backend: "zfs", Err: fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)}
	}
	return nil
}

// UnmountVolume unmounts a volume
func (z *ZFSBackend) UnmountVolume(ctx context.Context, name string) error {
	z.mu.Lock()
	defer z.mu.Unlock()

	ds := z.fullDatasetName(name)
	output, err := z.cmd().CombinedOutput(ctx, "zfs", "unmount", ds)
	if err != nil {
		// Ignore "not mounted" errors
		if strings.Contains(string(output), "not currently mounted") {
			return nil
		}
		return &StorageError{Op: "unmount", Volume: name, Backend: "zfs", Err: fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)}
	}
	return nil
}

// ReplicateVolume replicates a ZFS dataset to a remote target using send/receive
func (z *ZFSBackend) ReplicateVolume(ctx context.Context, name, target string, opts ReplicationOptions) error {
	zfsOpts := SendReceiveOptions{
		Incremental: "", // Default to full send if not specified in options
		Recursive:   opts.Recursive,
		Compressed:  opts.Compressed,
	}

	// fullDatasetName reads z.parentDataset, which Initialize writes under the
	// lock; take the read lock just long enough to compute the name.
	z.mu.RLock()
	ds := z.fullDatasetName(name)
	z.mu.RUnlock()
	return z.SendReceive(ctx, ds, target, zfsOpts)
}

// ===== SnapshotManager Implementation =====

// CreateSnapshot creates a snapshot of a volume
func (z *ZFSBackend) CreateSnapshot(ctx context.Context, volumeName, snapshotName string, opts SnapshotOptions) (*Snapshot, error) {
	z.mu.Lock()
	defer z.mu.Unlock()

	ds := z.fullDatasetName(volumeName)
	fullSnapshot := fmt.Sprintf("%s@%s", ds, snapshotName)

	args := []string{"snapshot"}
	if opts.Recursive {
		args = append(args, "-r")
	}
	args = append(args, fullSnapshot)

	output, err := z.cmd().CombinedOutput(ctx, "zfs", args...)
	if err != nil {
		return nil, &StorageError{
			Op:      "create snapshot",
			Volume:  volumeName + "@" + snapshotName,
			Backend: "zfs",
			Err:     fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err),
		}
	}

	return z.getSnapshotNoLock(ctx, volumeName, snapshotName)
}

// DeleteSnapshot deletes a snapshot
func (z *ZFSBackend) DeleteSnapshot(ctx context.Context, volumeName, snapshotName string) error {
	z.mu.Lock()
	defer z.mu.Unlock()

	ds := z.fullDatasetName(volumeName)
	fullSnapshot := fmt.Sprintf("%s@%s", ds, snapshotName)

	output, err := z.cmd().CombinedOutput(ctx, "zfs", "destroy", fullSnapshot)
	if err != nil {
		outputStr := strings.TrimSpace(string(output))
		if strings.Contains(outputStr, "could not find any snapshots") {
			return nil
		}
		return &StorageError{
			Op:      "delete snapshot",
			Volume:  volumeName + "@" + snapshotName,
			Backend: "zfs",
			Err:     fmt.Errorf("%s: %w", outputStr, err),
		}
	}
	return nil
}

// ListSnapshots lists all snapshots for a volume
func (z *ZFSBackend) ListSnapshots(ctx context.Context, volumeName string) ([]Snapshot, error) {
	z.mu.RLock()
	defer z.mu.RUnlock()
	return z.listSnapshotsNoLock(ctx, volumeName)
}

func (z *ZFSBackend) listSnapshotsNoLock(ctx context.Context, volumeName string) ([]Snapshot, error) {
	ds := z.fullDatasetName(volumeName)

	output, err := z.cmd().Output(ctx, "zfs", "list", "-H", "-t", "snapshot",
		"-o", "name,creation,used,referenced", "-r", ds)
	if err != nil {
		return nil, nil // No snapshots or dataset doesn't exist
	}

	var snapshots []Snapshot
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		fullName := fields[0]
		parts := strings.SplitN(fullName, "@", 2)
		if len(parts) != 2 {
			continue
		}

		snap := Snapshot{
			Name:       parts[1],
			FullName:   fullName,
			VolumeName: volumeName,
			Size:       parseZFSSize(fields[2]),
			Referenced: parseZFSSize(fields[3]),
		}

		// Parse creation time
		if ts, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
			snap.Created = time.Unix(ts, 0)
		}

		snapshots = append(snapshots, snap)
	}

	return snapshots, nil
}

// GetSnapshot returns information about a specific snapshot
func (z *ZFSBackend) GetSnapshot(ctx context.Context, volumeName, snapshotName string) (*Snapshot, error) {
	z.mu.RLock()
	defer z.mu.RUnlock()
	return z.getSnapshotNoLock(ctx, volumeName, snapshotName)
}

func (z *ZFSBackend) getSnapshotNoLock(ctx context.Context, volumeName, snapshotName string) (*Snapshot, error) {
	ds := z.fullDatasetName(volumeName)
	fullSnapshot := fmt.Sprintf("%s@%s", ds, snapshotName)

	output, err := z.cmd().Output(ctx, "zfs", "list", "-H", "-t", "snapshot",
		"-o", "name,creation,used,referenced", fullSnapshot)
	if err != nil {
		return nil, &StorageError{
			Op:      "get snapshot",
			Volume:  volumeName + "@" + snapshotName,
			Backend: "zfs",
			Err:     errNotFound,
		}
	}

	fields := strings.Fields(strings.TrimSpace(string(output)))
	if len(fields) < 4 {
		return nil, &StorageError{
			Op:      "get snapshot",
			Volume:  volumeName + "@" + snapshotName,
			Backend: "zfs",
			Err:     errNotFound,
		}
	}

	snap := &Snapshot{
		Name:       snapshotName,
		FullName:   fullSnapshot,
		VolumeName: volumeName,
		Size:       parseZFSSize(fields[2]),
		Referenced: parseZFSSize(fields[3]),
	}

	if ts, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
		snap.Created = time.Unix(ts, 0)
	}

	return snap, nil
}

// RollbackSnapshot rolls back a volume to a snapshot
func (z *ZFSBackend) RollbackSnapshot(ctx context.Context, volumeName, snapshotName string, opts RollbackOptions) error {
	z.mu.Lock()
	defer z.mu.Unlock()

	ds := z.fullDatasetName(volumeName)
	fullSnapshot := fmt.Sprintf("%s@%s", ds, snapshotName)

	args := []string{"rollback"}
	if opts.Force {
		args = append(args, "-f")
	}
	if opts.DestroyNewer {
		args = append(args, "-r")
	}
	args = append(args, fullSnapshot)

	output, err := z.cmd().CombinedOutput(ctx, "zfs", args...)
	if err != nil {
		return &StorageError{
			Op:      "rollback",
			Volume:  volumeName + "@" + snapshotName,
			Backend: "zfs",
			Err:     fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err),
		}
	}
	return nil
}

// SnapshotExists checks if a snapshot exists
func (z *ZFSBackend) SnapshotExists(ctx context.Context, volumeName, snapshotName string) bool {
	z.mu.RLock()
	defer z.mu.RUnlock()

	ds := z.fullDatasetName(volumeName)
	fullSnapshot := fmt.Sprintf("%s@%s", ds, snapshotName)

	return z.cmd().Run(ctx, "zfs", "list", "-H", "-t", "snapshot", fullSnapshot) == nil
}

// ===== CloneManager Implementation =====

// CreateClone creates a clone from a snapshot
func (z *ZFSBackend) CreateClone(ctx context.Context, snapshotFullName, cloneName string, opts CloneOptions) (*Volume, error) {
	z.mu.Lock()
	defer z.mu.Unlock()

	cloneDataset := z.fullDatasetName(cloneName)

	args := []string{"clone"}

	if _, given := opts.Properties["mountpoint"]; !given && opts.Mountpoint != "" {
		args = append(args, "-o", "mountpoint="+opts.Mountpoint)
	}

	for k, v := range opts.Properties {
		args = append(args, "-o", k+"="+v)
	}

	args = append(args, snapshotFullName, cloneDataset)

	output, err := z.cmd().CombinedOutput(ctx, "zfs", args...)
	if err != nil {
		return nil, &StorageError{
			Op:      "create clone",
			Volume:  cloneName,
			Backend: "zfs",
			Err:     fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err),
		}
	}

	return z.getVolumeNoLock(ctx, cloneName)
}

// PromoteClone promotes a clone to an independent volume
func (z *ZFSBackend) PromoteClone(ctx context.Context, cloneName string) error {
	z.mu.Lock()
	defer z.mu.Unlock()

	cloneDataset := z.fullDatasetName(cloneName)

	output, err := z.cmd().CombinedOutput(ctx, "zfs", "promote", cloneDataset)
	if err != nil {
		return &StorageError{
			Op:      "promote clone",
			Volume:  cloneName,
			Backend: "zfs",
			Err:     fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err),
		}
	}
	return nil
}

// GetCloneOrigin returns the snapshot a clone was created from
func (z *ZFSBackend) GetCloneOrigin(ctx context.Context, cloneName string) (string, error) {
	z.mu.RLock()
	defer z.mu.RUnlock()
	return z.getCloneOriginNoLock(ctx, cloneName)
}

// getCloneOriginNoLock is GetCloneOrigin without taking z.mu. Callers must
// already hold at least the read lock.
func (z *ZFSBackend) getCloneOriginNoLock(ctx context.Context, cloneName string) (string, error) {
	cloneDataset := z.fullDatasetName(cloneName)

	output, err := z.cmd().Output(ctx, "zfs", "get", "-H", "-o", "value", "origin", cloneDataset)
	if err != nil {
		return "", err
	}

	origin := strings.TrimSpace(string(output))
	if origin == "-" {
		return "", nil // Not a clone
	}
	return origin, nil
}

// ListClones lists all clones of a volume
func (z *ZFSBackend) ListClones(ctx context.Context, volumeName string) ([]Volume, error) {
	z.mu.RLock()
	defer z.mu.RUnlock()

	// List all volumes and filter by origin. Use the no-lock helpers: the
	// public ListVolumes/GetCloneOrigin would re-acquire the (non-reentrant)
	// read lock and could deadlock against a pending writer.
	ds := z.fullDatasetName(volumeName)
	var clones []Volume

	allVols, err := z.listVolumesNoLock(ctx)
	if err != nil {
		return nil, err
	}

	for i := range allVols {
		vol := allVols[i]
		origin, _ := z.getCloneOriginNoLock(ctx, vol.Name)
		if strings.HasPrefix(origin, ds+"@") {
			clones = append(clones, vol)
		}
	}

	return clones, nil
}

// ===== QuotaManager Implementation =====

// SetQuota sets the quota for a volume
func (z *ZFSBackend) SetQuota(ctx context.Context, volumeName, quota string) error {
	z.mu.Lock()
	defer z.mu.Unlock()

	ds := z.fullDatasetName(volumeName)

	output, err := z.cmd().CombinedOutput(ctx, "zfs", "set", "quota="+quota, ds)
	if err != nil {
		return &StorageError{
			Op:      "set quota",
			Volume:  volumeName,
			Backend: "zfs",
			Err:     fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err),
		}
	}
	return nil
}

// GetQuota returns the current quota for a volume
func (z *ZFSBackend) GetQuota(ctx context.Context, volumeName string) (int64, error) {
	z.mu.RLock()
	defer z.mu.RUnlock()

	ds := z.fullDatasetName(volumeName)

	output, err := z.cmd().Output(ctx, "zfs", "get", "-H", "-o", "value", "-p", "quota", ds)
	if err != nil {
		return 0, err
	}

	quotaStr := strings.TrimSpace(string(output))
	if quotaStr == "0" || quotaStr == "none" {
		return 0, nil // No quota
	}

	quota, err := strconv.ParseInt(quotaStr, 10, 64)
	if err != nil {
		return 0, err
	}
	return quota, nil
}

// SetReservation sets the guaranteed space for a volume
func (z *ZFSBackend) SetReservation(ctx context.Context, volumeName, reservation string) error {
	z.mu.Lock()
	defer z.mu.Unlock()

	ds := z.fullDatasetName(volumeName)

	output, err := z.cmd().CombinedOutput(ctx, "zfs", "set", "reservation="+reservation, ds)
	if err != nil {
		return &StorageError{
			Op:      "set reservation",
			Volume:  volumeName,
			Backend: "zfs",
			Err:     fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err),
		}
	}
	return nil
}

// GetReservation returns the current reservation for a volume
func (z *ZFSBackend) GetReservation(ctx context.Context, volumeName string) (int64, error) {
	z.mu.RLock()
	defer z.mu.RUnlock()

	ds := z.fullDatasetName(volumeName)

	output, err := z.cmd().Output(ctx, "zfs", "get", "-H", "-o", "value", "-p", "reservation", ds)
	if err != nil {
		return 0, err
	}

	resStr := strings.TrimSpace(string(output))
	if resStr == "0" || resStr == "none" {
		return 0, nil
	}

	res, err := strconv.ParseInt(resStr, 10, 64)
	if err != nil {
		return 0, err
	}
	return res, nil
}

// ===== Helper Functions =====

func (z *ZFSBackend) fullDatasetName(name string) string {
	if strings.HasPrefix(name, z.parentDataset+"/") {
		return name
	}
	return z.parentDataset + "/" + name
}

func (z *ZFSBackend) ensureDataset(ctx context.Context, ds string) error {
	if z.datasetExistsNoLock(ctx, ds) {
		return nil
	}

	output, err := z.cmd().CombinedOutput(ctx, "zfs", "create", "-p", ds)
	if err != nil {
		return fmt.Errorf("zfs create failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func (z *ZFSBackend) datasetExistsNoLock(ctx context.Context, ds string) bool {
	return z.cmd().Run(ctx, "zfs", "list", "-H", ds) == nil
}

func (z *ZFSBackend) destroyDataset(ctx context.Context, ds string, recursive bool) error {
	args := []string{"destroy", "-f"}
	if recursive {
		args = append(args, "-r")
	}
	args = append(args, ds)

	return z.cmd().Run(ctx, "zfs", args...)
}

func (z *ZFSBackend) getMountpoint(ctx context.Context, ds string) (string, error) {
	output, err := z.cmd().Output(ctx, "zfs", "get", "-H", "-o", "value", "mountpoint", ds)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func (z *ZFSBackend) getProperties(ctx context.Context, ds string) (map[string]string, error) {
	output, err := z.cmd().Output(ctx, "zfs", "get", "-H", "-p",
		"all", ds)
	if err != nil {
		return nil, err
	}

	props := make(map[string]string)
	lines := strings.Split(string(output), "\n")

	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			// Format: dataset property value source
			props[fields[1]] = fields[2]
		}
	}

	return props, nil
}

// parseZFSSize parses ZFS size output (in bytes or human-readable)
func parseZFSSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" || s == "0" {
		return 0
	}

	// Try parsing as plain number first
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}

	// Parse human-readable format. ZFS may emit a trailing 'B' (e.g. "512B")
	// and either upper- or lower-case unit suffixes.
	var multiplier int64 = 1
	numStr := s

	if n := len(s); n > 1 && (s[n-1] == 'B' || s[n-1] == 'b') {
		s = s[:n-1]
		numStr = s
	}

	if len(s) > 1 {
		switch s[len(s)-1] {
		case 'K', 'k':
			multiplier = 1 << 10
			numStr = s[:len(s)-1]
		case 'M', 'm':
			multiplier = 1 << 20
			numStr = s[:len(s)-1]
		case 'G', 'g':
			multiplier = 1 << 30
			numStr = s[:len(s)-1]
		case 'T', 't':
			multiplier = 1 << 40
			numStr = s[:len(s)-1]
		case 'P', 'p':
			multiplier = 1 << 50
			numStr = s[:len(s)-1]
		case 'E', 'e':
			multiplier = 1 << 60
			numStr = s[:len(s)-1]
		}
	}

	// Try parsing the number part
	if f, err := strconv.ParseFloat(numStr, 64); err == nil {
		return int64(f * float64(multiplier))
	}

	return 0
}

// ===== ZFSManager Implementation =====

// GetDataset returns ZFS dataset information
func (z *ZFSBackend) GetDataset(ctx context.Context, name string) (*ZFSDataset, error) {
	z.mu.RLock()
	defer z.mu.RUnlock()

	fullName := z.fullDatasetName(name)

	// Get properties
	props := []string{"name", "type", "origin", "compression", "mountpoint", "mounted", "recordsize"}
	output, err := z.cmd().Output(ctx, "zfs", "get", "-Hp", "-o", "value", strings.Join(props, ","), fullName)
	if err != nil {
		return nil, fmt.Errorf("failed to get dataset properties: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < len(props) {
		return nil, fmt.Errorf("unexpected output from zfs get")
	}

	ds := &ZFSDataset{}
	ds.FullName = lines[0]
	ds.Type = lines[1]
	ds.Origin = lines[2]
	ds.Compression = lines[3]
	ds.Mountpoint = lines[4]
	ds.Mounted = lines[5] == "yes"

	// recordsize is "-" for snapshots/volumes; only assign when it parses.
	if val, err := strconv.Atoi(lines[6]); err == nil {
		ds.Recordsize = val
	}

	return ds, nil
}

// SetProperty sets a ZFS property
func (z *ZFSBackend) SetProperty(ctx context.Context, ds, property, value string) error {
	return z.cmd().Run(ctx, "zfs", "set", property+"="+value, ds)
}

// GetProperty gets a ZFS property value
func (z *ZFSBackend) GetProperty(ctx context.Context, ds, property string) (string, error) {
	out, err := z.cmd().Output(ctx, "zfs", "get", "-H", "-o", "value", property, ds)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SendReceive sends a dataset to another pool/host via SSH.
// Commands are connected via Go pipes instead of shell pipelines
// to prevent command injection (CWE-78).
func (z *ZFSBackend) SendReceive(ctx context.Context, source, destination string, opts SendReceiveOptions) error {
	z.mu.RLock()
	defer z.mu.RUnlock()

	// Derive a cancelable context: if either side dies, canceling kills the
	// other so a broken receive can never leave zfs send blocked forever
	// writing into a pipe whose reader is gone.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Build ZFS send command
	sendArgs := []string{"send"}
	if opts.Incremental != "" {
		sendArgs = append(sendArgs, "-i", opts.Incremental)
	}
	if opts.Recursive {
		sendArgs = append(sendArgs, "-R")
	}
	if opts.Compressed {
		sendArgs = append(sendArgs, "-c")
	}
	sendArgs = append(sendArgs, source)

	// This pair stays on os/exec: the send stream is piped straight into receive,
	// which is the streaming shape a canned-output Runner cannot model.
	sendCmd := exec.CommandContext(runCtx, "zfs", sendArgs...)

	// Build receive command — remote via SSH or local
	var recvCmd *exec.Cmd
	if strings.Contains(destination, ":") {
		host, target, err := validation.ValidateSSHDestination(destination)
		if err != nil {
			return fmt.Errorf("invalid destination: %w", err)
		}
		recvCmd = exec.CommandContext(runCtx, "ssh", "--", host, "zfs", "receive", "-F", target)
	} else {
		recvCmd = exec.CommandContext(runCtx, "zfs", "receive", "-F", destination)
	}

	// Connect sendCmd stdout → recvCmd stdin via Go pipe
	pipe, err := sendCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create pipe: %w", err)
	}
	recvCmd.Stdin = pipe

	var output bytes.Buffer
	recvCmd.Stdout = &output
	recvCmd.Stderr = &output

	if err := recvCmd.Start(); err != nil {
		return fmt.Errorf("failed to start zfs receive: %w", err)
	}
	if err := sendCmd.Start(); err != nil {
		return fmt.Errorf("failed to start zfs send: %w", err)
	}

	// Wait on both processes concurrently. If either exits with an error,
	// cancel the shared context so the peer is torn down instead of blocking.
	var sendErr, recvErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if recvErr = recvCmd.Wait(); recvErr != nil {
			cancel()
		}
	}()
	go func() {
		defer wg.Done()
		if sendErr = sendCmd.Wait(); sendErr != nil {
			cancel()
		}
	}()
	wg.Wait()

	if sendErr != nil {
		return fmt.Errorf("zfs send failed: %s: %w", output.String(), sendErr)
	}
	if recvErr != nil {
		return fmt.Errorf("zfs receive failed: %s: %w", output.String(), recvErr)
	}

	return nil
}

// Scrub initiates a scrub on the pool
func (z *ZFSBackend) Scrub(ctx context.Context, pool string) error {
	return z.cmd().Run(ctx, "zpool", "scrub", pool)
}
