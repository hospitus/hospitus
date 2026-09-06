package jail

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
)

// ensureZFSDataset ensures a ZFS dataset exists
func (p *JailProvider) ensureZFSDataset(ctx context.Context, dataset string) error {
	if err := p.cmd().Run(ctx, "zfs", "list", dataset); err != nil {
		// Dataset doesn't exist, create it
		return p.createZFSDataset(ctx, dataset)
	}
	return nil
}

// createZFSDataset creates a new ZFS dataset
func (p *JailProvider) createZFSDataset(ctx context.Context, dataset string) error {
	_, err := p.cmd().CombinedOutput(ctx, "zfs", "create", "-p", dataset)
	if err != nil {
		return fmt.Errorf("zfs create failed: %w", err)
	}
	return nil
}

// destroyZFSDataset destroys a ZFS dataset
func (p *JailProvider) destroyZFSDataset(ctx context.Context, dataset string, force bool) error {
	// The name reaches here from handle metadata, which an API client can
	// influence; nothing outside our own parents is destroyed on its word.
	if err := p.ensureOwnDataset(dataset); err != nil {
		return err
	}
	// Use -r to recursively destroy (essential for snapshots)
	// Use -f to force unmount if busy, only if force is requested
	args := []string{"destroy", "-r"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, dataset)

	output, err := p.cmd().CombinedOutput(ctx, "zfs", args...)
	if err == nil {
		return nil
	}

	outputStr := string(output)
	// If dataset doesn't exist, that's fine - it's already gone
	if strings.Contains(outputStr, "dataset does not exist") {
		return nil
	}

	// A jail root stays mounted-busy once the jail has been stopped, so a plain
	// destroy answers "pool or dataset is busy" for every jail that was ever
	// started, and the caller is left with an instance it cannot delete. Retry
	// once with -f, which unmounts it. Only the dataset of the jail being
	// deleted is at stake, and that jail is already stopped by this point.
	if !force && strings.Contains(outputStr, "dataset is busy") {
		forcedOutput, forcedErr := p.cmd().CombinedOutput(ctx, "zfs", "destroy", "-r", "-f", dataset)
		if forcedErr == nil {
			return nil
		}
		output, err = forcedOutput, forcedErr
		outputStr = string(output)
	}

	// zfs explains itself on stderr. Without this the caller reads "exit status
	// 1" and has to open the daemon log to learn anything at all.
	if detail := strings.TrimSpace(outputStr); detail != "" {
		return fmt.Errorf("zfs destroy failed: %w: %s", err, detail)
	}
	return fmt.Errorf("zfs destroy failed: %w", err)
}

// destroyZFSDatasetCleanup attempts to destroy a ZFS dataset during cleanup and logs any errors.
// This is used for rollback operations where we don't want to mask the original error.
// ensureOwnDataset refuses a dataset that is not strictly below one of the
// parents this provider manages, or that carries a snapshot suffix or blanks.
func (p *JailProvider) ensureOwnDataset(ds string) error {
	if ds == "" || strings.ContainsAny(ds, "@ \t\n") || strings.Contains(ds, "/../") || strings.HasSuffix(ds, "/..") || strings.HasPrefix(ds, "-") {
		return fmt.Errorf("refusing dataset %q: invalid name", ds)
	}
	for _, parent := range []string{p.zfsParent, p.getVolumesParent()} {
		if parent != "" && strings.HasPrefix(ds, parent+"/") && len(ds) > len(parent)+1 {
			return nil
		}
	}
	return fmt.Errorf("refusing dataset %q: not under %s", ds, p.zfsParent)
}

// datasetFor returns the dataset a jail lives on, refusing one this provider
// does not own.
//
// The name is read back from the instance handle, which the API lets a client
// merge into, so destroy, rollback, clone and promote all check it first.
func (p *JailProvider) datasetFor(handle provider.InstanceHandle) (string, error) {
	ds, ok := handle.Metadata["zfs_dataset"].(string)
	if !ok || ds == "" {
		ds = fmt.Sprintf("%s/%s", p.zfsParent, handle.ID)
	}
	if err := p.ensureOwnDataset(ds); err != nil {
		return "", err
	}
	return ds, nil
}

// ensureOwnSnapshot refuses a <dataset>@<snapshot> name whose dataset is not
// one of ours. `zfs rollback -r` and `zfs destroy` take these names.
func (p *JailProvider) ensureOwnSnapshot(name string) error {
	ds, snap, found := strings.Cut(name, "@")
	if !found || snap == "" {
		return fmt.Errorf("refusing %q: not a snapshot name", name)
	}
	if strings.ContainsAny(snap, "@/ \t\n") {
		return fmt.Errorf("refusing %q: invalid snapshot name", name)
	}
	return p.ensureOwnDataset(ds)
}

func (p *JailProvider) destroyZFSDatasetCleanup(ctx context.Context, dataset string) {
	// Cleanup runs on the error path, often because the caller's context was
	// canceled mid-way; that same canceled context would skip the command
	// that removes the half-built dataset and leave it orphaned.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if err := p.destroyZFSDataset(ctx, dataset, true); err != nil {
		p.logWarn(ctx, "failed to destroy ZFS dataset during cleanup", "zfs_dataset", dataset, logging.FieldError, err)
	}
}

// zfsDestroyCleanup is a helper for direct ZFS destroy operations during cleanup.
// It logs errors but doesn't return them to avoid masking the original error.
func (p *JailProvider) zfsDestroyCleanup(ctx context.Context, dataset string) {
	if err := p.cmd().Run(ctx, "zfs", "destroy", dataset); err != nil {
		p.getLogger(ctx).Error("zfs destroy cleanup failed — dataset may be orphaned",
			"dataset", dataset, "error", err)
	}
}

// getZFSMountpoint gets the mountpoint of a ZFS dataset
func (p *JailProvider) getZFSMountpoint(ctx context.Context, dataset string) (string, error) {
	output, err := p.cmd().Output(ctx, "zfs", "get", "-H", "-o", "value", "mountpoint", dataset)
	if err != nil {
		return "", fmt.Errorf("failed to get ZFS mountpoint: %w", err)
	}

	mountpoint := strings.TrimSpace(string(output))
	return mountpoint, nil
}

// applyZFSQuotas applies ZFS quotas to a jail dataset
func (p *JailProvider) applyZFSQuotas(ctx context.Context, dataset string, diskGB int) error {
	if diskGB <= 0 {
		return nil
	}

	quota := fmt.Sprintf("%dG", diskGB)
	_, err := p.cmd().CombinedOutput(ctx, "zfs", "set", "quota="+quota, dataset)
	if err != nil {
		return fmt.Errorf("failed to set ZFS quota: %w", err)
	}

	// Also set refquota to ensure the dataset itself doesn't exceed the limit
	_ = p.cmd().Run(ctx, "zfs", "set", "refquota="+quota, dataset) // Non-fatal

	return nil
}
