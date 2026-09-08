package jail

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// ensureZFSDataset ensures a ZFS dataset exists
func (p *JailProvider) ensureZFSDataset(ctx context.Context, dataset string) error {
	// Same guard as destroyZFSDataset, widened by one case: creating the parent
	// itself is what this is for, while destroying it never is.
	if err := p.ensureOwnDatasetOrParent(dataset); err != nil {
		return err
	}
	if err := p.cmd().Run(ctx, "zfs", "list", dataset); err != nil {
		// Dataset doesn't exist, create it
		return p.createZFSDataset(ctx, dataset)
	}
	return nil
}

// createZFSDataset creates a new ZFS dataset
func (p *JailProvider) createZFSDataset(ctx context.Context, dataset string) error {
	if err := p.ensureOwnDatasetOrParent(dataset); err != nil {
		return err
	}
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

// ensureOwnDatasetOrSnapshot accepts "<dataset>" and "<dataset>@<snapshot>",
// confining both to the parents this provider manages.
func (p *JailProvider) ensureOwnDatasetOrSnapshot(name string) error {
	ds, snap, found := strings.Cut(name, "@")
	if !found {
		return p.ensureOwnDataset(name)
	}
	// zfs(8) reads "%" as a range separator, and either side may be left blank:
	// "vol@%" names every snapshot the volume has. Validated as a plain name
	// rather than by listing forbidden characters.
	if err := validation.ValidateSnapshotName(snap); err != nil {
		return fmt.Errorf("refusing snapshot %q: %w", name, err)
	}
	return p.ensureOwnDataset(ds)
}

// ensureOwnSnapshot is ensureOwnDatasetOrSnapshot with the "@" made mandatory.
//
// "zfs rollback -r" and "zfs destroy" take a <dataset>@<snapshot> name, and
// handing either a bare dataset would roll back or destroy the whole dataset
// rather than the snapshot the caller named.
func (p *JailProvider) ensureOwnSnapshot(name string) error {
	if !strings.Contains(name, "@") {
		return fmt.Errorf("refusing %q: not a snapshot name", name)
	}
	return p.ensureOwnDatasetOrSnapshot(name)
}

// ensureOwnDatasetOrParent is ensureOwnDataset plus the parents themselves.
//
// Creating "zroot/hospitus/jails" is the normal first step; destroying it is
// not, which is why the stricter check stays on the destroy path.
func (p *JailProvider) ensureOwnDatasetOrParent(ds string) error {
	for _, parent := range []string{p.zfsParent, p.getVolumesParent()} {
		if parent == "" || ds != parent {
			continue
		}
		// The parent itself still has to be a sane name: returning early here
		// skipped every check in ensureOwnDataset, so a misconfigured parent
		// carrying "@", whitespace or a leading dash would go straight to zfs.
		if strings.ContainsAny(ds, "@ \t\n") || strings.Contains(ds, "/../") ||
			strings.HasSuffix(ds, "/..") || strings.HasPrefix(ds, "-") {
			return fmt.Errorf("refusing dataset %q: invalid name", ds)
		}
		return nil
	}
	return p.ensureOwnDataset(ds)
}

// datasetFor returns the dataset a jail lives on, refusing one this provider
// does not own.
//
// The name is read back from the instance handle, which the API lets a client
// merge into, so destroy, rollback, clone and promote all check it first.
func (p *JailProvider) datasetFor(handle provider.InstanceHandle) (string, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return "", fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	want := fmt.Sprintf("%s/%s", p.zfsParent, handle.ID)

	ds, ok := handle.Metadata["zfs_dataset"].(string)
	if !ok || ds == "" {
		return want, nil
	}
	if err := p.ensureOwnDataset(ds); err != nil {
		return "", err
	}
	// Being managed is not enough: a handle whose ID names one jail and whose
	// metadata names another's dataset would operate on the second while every
	// message says the first.
	if ds != want {
		return "", fmt.Errorf("handle %q carries dataset %s, which belongs to another instance", handle.ID, ds)
	}
	return ds, nil
}

// snapshotFor resolves the ZFS snapshot a handle names, and binds it to the
// instance the handle declares.
//
// The same rule as datasetFor: confinement to the managed parents stops a
// snapshot from outside, but a forged handle can still set Instance to one jail
// and zfs_name to another's snapshot — and rollback -r destroys every later
// snapshot of whatever dataset it is given.
func (p *JailProvider) snapshotFor(snapshot provider.SnapshotHandle) (string, error) {
	zfsName, ok := snapshot.Metadata["zfs_name"].(string)
	if !ok || zfsName == "" {
		return "", fmt.Errorf("invalid snapshot metadata: missing zfs_name")
	}
	if err := p.ensureOwnSnapshot(zfsName); err != nil {
		return "", err
	}
	if err := validation.ValidateInstanceName(snapshot.Instance); err != nil {
		return "", fmt.Errorf("invalid snapshot instance %q: %w", snapshot.Instance, err)
	}
	ds, _, _ := strings.Cut(zfsName, "@")
	if want := fmt.Sprintf("%s/%s", p.zfsParent, snapshot.Instance); ds != want {
		return "", fmt.Errorf("snapshot %s belongs to %s, not to the instance %q it is presented for",
			zfsName, ds, snapshot.Instance)
	}
	return zfsName, nil
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
	// The same guard destroyZFSDataset applies, widened to snapshots: this path
	// legitimately destroys "<dataset>@<snapshot>", which ensureOwnDataset
	// rejects outright. The name still reaches here from handle metadata a
	// client can influence, and a cleanup path is no reason to destroy
	// something outside our own parents.
	if err := p.ensureOwnDatasetOrSnapshot(dataset); err != nil {
		p.getLogger(ctx).Error("refusing to destroy a dataset outside the managed parents",
			"dataset", dataset, "error", err)
		return
	}
	// Detached and bounded, like destroyZFSDatasetCleanup: cleanup usually runs
	// because the caller's context was just canceled, and that same context
	// would skip the command and strand the dataset.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
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

	// Same confinement as create and destroy: the dataset name reaches here
	// from handle metadata a client can influence, and "zfs set" on an
	// arbitrary dataset is a write like any other.
	if err := p.ensureOwnDatasetOrParent(dataset); err != nil {
		return err
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
