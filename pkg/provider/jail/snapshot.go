package jail

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

var _ provider.SnapshotProvider = (*JailProvider)(nil)

// CreateSnapshot creates a ZFS snapshot of the jail's filesystem.
//
// Jail snapshots use ZFS snapshots which are atomic and space-efficient.
// The jail can be running during snapshot creation (crash-consistent snapshot).
//
// SECURITY: Snapshot name is validated to prevent command injection.
func (p *JailProvider) CreateSnapshot(ctx context.Context, handle provider.InstanceHandle, name string) (provider.SnapshotHandle, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return provider.SnapshotHandle{}, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	// SECURITY: Validate snapshot name
	if err := validation.ValidateSnapshotName(name); err != nil {
		return provider.SnapshotHandle{}, fmt.Errorf("invalid snapshot name: %w", err)
	}

	dataset, err := p.datasetFor(handle)
	if err != nil {
		return provider.SnapshotHandle{}, err
	}

	// Create ZFS snapshot
	// Format: dataset@snapshot_name
	snapshotName := fmt.Sprintf("%s@%s", dataset, name)

	// SECURITY: name is validated above, dataset comes from our datastore
	// zfs(8) says why — "dataset already exists", "permission denied" — and
	// dropping its output leaves the caller with "exit status 1".
	output, err := p.cmd().CombinedOutput(ctx, "zfs", "snapshot", snapshotName)
	if err != nil {
		return provider.SnapshotHandle{}, fmt.Errorf("failed to create ZFS snapshot %s: %w (%s)",
			snapshotName, err, strings.TrimSpace(string(output)))
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
func (p *JailProvider) DeleteSnapshot(ctx context.Context, snapshot provider.SnapshotHandle) error {
	// The name is read back from a stored handle, so it is checked against the
	// datasets this provider owns and against the instance the handle declares.
	zfsName, err := p.snapshotFor(snapshot)
	if err != nil {
		return err
	}

	output, err := p.cmd().CombinedOutput(ctx, "zfs", "destroy", zfsName)
	if err != nil {
		return fmt.Errorf("failed to delete ZFS snapshot %s: %w (%s)",
			zfsName, err, strings.TrimSpace(string(output)))
	}

	return nil
}

// RestoreSnapshot restores a jail to a ZFS snapshot state.
//
// This operation uses ZFS rollback which requires the jail to be stopped.
// All snapshots created after this snapshot will be deleted.
//
// SECURITY: All paths and names are validated.
func (p *JailProvider) RestoreSnapshot(ctx context.Context, handle provider.InstanceHandle, snapshot provider.SnapshotHandle) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
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

	// Check if jail is stopped (required for ZFS rollback)
	if info.State != provider.StateStopped {
		return fmt.Errorf("jail must be stopped to restore snapshot (current state: %s)", info.State)
	}

	// "rollback -r" destroys every later snapshot of the dataset it names, so
	// the name is checked against the datasets we own and against the instance
	// this handle claims to belong to.
	zfsName, err := p.snapshotFor(snapshot)
	if err != nil {
		return err
	}

	output, err := p.cmd().CombinedOutput(ctx, "zfs", "rollback", "-r", zfsName)
	if err != nil {
		return fmt.Errorf("failed to roll back to ZFS snapshot %s: %w (%s)",
			zfsName, err, strings.TrimSpace(string(output)))
	}

	return nil
}

// ListSnapshots lists all ZFS snapshots for a jail.
//
// SECURITY: Parses ZFS output carefully to avoid injection.
func (p *JailProvider) ListSnapshots(ctx context.Context, handle provider.InstanceHandle) ([]provider.SnapshotInfo, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	dataset, err := p.datasetFor(handle)
	if err != nil {
		return nil, err
	}

	// List ZFS snapshots
	// Use -H for parseable output, -p for numeric (Unix timestamp) values, -o for specific fields
	output, err := p.cmd().CombinedOutput(ctx, "zfs", "list", "-H", "-p", "-t", "snapshot", "-o", "name,creation,used", "-r", dataset)
	if err != nil {
		// If no snapshots exist, ZFS returns an error
		if strings.Contains(string(output), "no datasets available") {
			return []provider.SnapshotInfo{}, nil
		}
		return nil, fmt.Errorf("failed to list ZFS snapshots: %w", err)
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

		// "-r" also lists snapshots of child datasets (volumes, thin clones).
		// Reporting those under the jail's own name would let a restore by name
		// roll back a child instead of the jail.
		if parts[0] != dataset {
			continue
		}

		snapshotName := parts[1]
		creationStr := fields[1]
		usedStr := fields[2]

		// Parse creation time (Unix timestamp)
		creationUnix, _ := strconv.ParseInt(creationStr, 10, 64)
		createdAt := time.Unix(creationUnix, 0)

		// Parse size (convert from bytes to MB)
		// With -p flag, ZFS returns raw bytes as a plain number (e.g., "294912")
		sizeMB := int64(0)
		if usedStr != "" {
			sizeBytes, _ := strconv.ParseInt(usedStr, 10, 64)
			sizeMB = sizeBytes / (1024 * 1024)
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
