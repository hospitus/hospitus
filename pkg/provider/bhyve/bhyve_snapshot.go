package bhyve

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

var _ provider.SnapshotProvider = (*BhyveProvider)(nil)

// Snapshot support implementation using ZFS

// CreateSnapshot creates a ZFS snapshot of the instance's disk.
//
// Bhyve snapshots use ZFS snapshots which are atomic and space-efficient.
// The instance can be running during snapshot creation (crash-consistent snapshot).
//
// SECURITY: Snapshot name is validated to prevent command injection.
func (p *BhyveProvider) CreateSnapshot(ctx context.Context, handle provider.InstanceHandle, name string) (provider.SnapshotHandle, error) {
	// SECURITY: Validate snapshot name
	if err := validation.ValidateSnapshotName(name); err != nil {
		return provider.SnapshotHandle{}, fmt.Errorf("invalid snapshot name: %w", err)
	}

	info, err := p.GetInstanceInfo(ctx, handle)
	if err != nil {
		return provider.SnapshotHandle{}, fmt.Errorf("failed to get instance info: %w", err)
	}

	// Find the primary disk (should be a ZVOL)
	if len(info.Spec.Disks) == 0 {
		return provider.SnapshotHandle{}, fmt.Errorf("instance has no disks")
	}

	primaryDisk := info.Spec.Disks[0]
	if primaryDisk.Type != provider.DiskTypeZVOL {
		return provider.SnapshotHandle{}, fmt.Errorf("snapshot requires ZFS volume (ZVOL), got %s", primaryDisk.Type)
	}

	// Extract ZFS dataset name from path
	// Path format: /dev/zvol/pool/dataset
	zvolPath := primaryDisk.Path
	if !strings.HasPrefix(zvolPath, "/dev/zvol/") {
		return provider.SnapshotHandle{}, fmt.Errorf("invalid ZVOL path: %s", zvolPath)
	}

	dataset := strings.TrimPrefix(zvolPath, "/dev/zvol/")

	// Create ZFS snapshot
	// Format: dataset@snapshot_name
	snapshotName := fmt.Sprintf("%s@%s", dataset, name)

	// SECURITY: name is validated above, dataset comes from our datastore
	output, err := p.cmd().CombinedOutput(ctx, "zfs", "snapshot", snapshotName)
	if err != nil {
		return provider.SnapshotHandle{}, fmt.Errorf("failed to create ZFS snapshot: %w (output: %s)", err, string(output))
	}

	// Create snapshot handle
	snapshotHandle := provider.SnapshotHandle{
		ID:       fmt.Sprintf("%s_%s", handle.ID, name),
		Instance: handle.ID,
		Metadata: map[string]interface{}{
			"dataset":       dataset,
			"snapshot_name": name,
			"zfs_name":      snapshotName,
			"created":       time.Now().Format(time.RFC3339),
		},
	}

	return snapshotHandle, nil
}

// DeleteSnapshot deletes a ZFS snapshot.
//
// SECURITY: Uses validated snapshot information from handle.
func (p *BhyveProvider) DeleteSnapshot(ctx context.Context, snapshot provider.SnapshotHandle) error {
	// Extract ZFS snapshot name from metadata
	zfsName, ok := snapshot.Metadata["zfs_name"].(string)
	if !ok {
		return fmt.Errorf("invalid snapshot metadata: missing zfs_name")
	}

	// Delete ZFS snapshot
	// SECURITY: zfsName was constructed during creation from validated inputs
	output, err := p.cmd().CombinedOutput(ctx, "zfs", "destroy", zfsName)
	if err != nil {
		return fmt.Errorf("failed to delete ZFS snapshot: %w (output: %s)", err, string(output))
	}

	return nil
}

// RestoreSnapshot restores an instance to a ZFS snapshot state.
//
// This operation uses ZFS rollback which requires the instance to be stopped.
// All snapshots created after this snapshot will be deleted.
//
// SECURITY: All paths and names are validated.
func (p *BhyveProvider) RestoreSnapshot(ctx context.Context, handle provider.InstanceHandle, snapshot provider.SnapshotHandle) error {
	ctx, releaseLock, lockErr := p.locks.Acquire(ctx, handle.ID)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()
	// Verify snapshot belongs to this instance
	if snapshot.Instance != handle.ID {
		return fmt.Errorf("snapshot does not belong to instance %s", handle.ID)
	}

	info, err := p.GetInstanceInfo(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to get instance info: %w", err)
	}

	// Check if instance is stopped (required for ZFS rollback)
	if info.State != provider.StateStopped {
		return fmt.Errorf("instance must be stopped to restore snapshot (current state: %s)", info.State)
	}

	// Extract ZFS snapshot name from metadata
	zfsName, ok := snapshot.Metadata["zfs_name"].(string)
	if !ok {
		return fmt.Errorf("invalid snapshot metadata: missing zfs_name")
	}

	// Rollback to ZFS snapshot
	// SECURITY: zfsName was constructed during creation from validated inputs
	output, err := p.cmd().CombinedOutput(ctx, "zfs", "rollback", "-r", zfsName)
	if err != nil {
		return fmt.Errorf("failed to rollback ZFS snapshot: %w (output: %s)", err, string(output))
	}

	return nil
}

// ListSnapshots lists all ZFS snapshots for an instance.
//
// SECURITY: Parses ZFS output carefully to avoid injection.
func (p *BhyveProvider) ListSnapshots(ctx context.Context, handle provider.InstanceHandle) ([]provider.SnapshotInfo, error) {
	info, err := p.GetInstanceInfo(ctx, handle)
	if err != nil {
		return nil, fmt.Errorf("failed to get instance info: %w", err)
	}

	// Find the primary disk
	if len(info.Spec.Disks) == 0 {
		return nil, fmt.Errorf("instance has no disks")
	}

	primaryDisk := info.Spec.Disks[0]
	if primaryDisk.Type != provider.DiskTypeZVOL {
		return nil, fmt.Errorf("snapshot requires ZFS volume (ZVOL), got %s", primaryDisk.Type)
	}

	// Extract ZFS dataset name from path
	zvolPath := primaryDisk.Path
	if !strings.HasPrefix(zvolPath, "/dev/zvol/") {
		return nil, fmt.Errorf("invalid ZVOL path: %s", zvolPath)
	}

	dataset := strings.TrimPrefix(zvolPath, "/dev/zvol/")

	// List ZFS snapshots
	// Use -H for parseable output, -o for specific fields
	// -p asks zfs for a Unix timestamp, which is what the creation field is
	// parsed as below. Without it zfs prints "Tue Aug 26 8:15 2026", the parse
	// yields zero, and every snapshot is listed as created on 1970-01-01. The
	// jail provider passes it.
	output, err := p.cmd().CombinedOutput(ctx, "zfs", "list", "-H", "-p", "-t", "snapshot", "-o", "name,creation,used", "-r", dataset)
	if err != nil {
		// If no snapshots exist, ZFS returns an error
		if strings.Contains(string(output), "no datasets available") {
			return []provider.SnapshotInfo{}, nil
		}
		return nil, fmt.Errorf("failed to list ZFS snapshots: %w (output: %s)", err, string(output))
	}

	// Parse output
	snapshots := []provider.SnapshotInfo{}
	lines := strings.Split(string(output), "\n")

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Parse tab-separated fields: name creation used
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}

		// Extract snapshot name from format: dataset@snapshot_name
		fullName := fields[0]
		parts := strings.Split(fullName, "@")
		if len(parts) != 2 {
			continue
		}

		snapshotName := parts[1]
		creationStr := fields[1]
		usedStr := fields[2]

		// Parse creation time (Unix timestamp)
		creationUnix, _ := strconv.ParseInt(creationStr, 10, 64)
		createdAt := time.Unix(creationUnix, 0)

		// Parse size: with -p the 'used' field is a raw byte count.
		sizeMB := int64(0)
		if usedBytes, err := strconv.ParseInt(usedStr, 10, 64); err == nil {
			sizeMB = usedBytes / (1024 * 1024)
		}

		// Create snapshot info
		snapshotInfo := provider.SnapshotInfo{
			Handle: provider.SnapshotHandle{
				ID:       fmt.Sprintf("%s_%s", handle.ID, snapshotName),
				Instance: handle.ID,
				Metadata: map[string]interface{}{
					"dataset":       dataset,
					"snapshot_name": snapshotName,
					"zfs_name":      fullName,
				},
			},
			Name:      snapshotName,
			CreatedAt: createdAt,
			SizeMB:    sizeMB,
		}

		snapshots = append(snapshots, snapshotInfo)
	}

	return snapshots, nil
}
