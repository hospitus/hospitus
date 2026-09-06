package bhyve

import (
	"fmt"
	"strings"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// ensureOwnDataset refuses a dataset that is not strictly below the parent this
// provider manages, or that carries a snapshot suffix or blanks.
func (p *BhyveProvider) ensureOwnDataset(ds string) error {
	if ds == "" || strings.ContainsAny(ds, "@ \t\n") || strings.Contains(ds, "/../") ||
		strings.HasSuffix(ds, "/..") || strings.HasPrefix(ds, "-") {
		return fmt.Errorf("refusing dataset %q: invalid name", ds)
	}
	if p.zfsParent == "" || !strings.HasPrefix(ds, p.zfsParent+"/") || len(ds) <= len(p.zfsParent)+1 {
		return fmt.Errorf("refusing dataset %q: not under %s", ds, p.zfsParent)
	}
	return nil
}

// ensureOwnSnapshot confines a "<dataset>@<snapshot>" name and requires the
// "@": "zfs rollback -r" and "zfs destroy" both accept a bare dataset, and
// would then roll back or destroy the whole volume rather than one snapshot.
func (p *BhyveProvider) ensureOwnSnapshot(name string) error {
	ds, snap, found := strings.Cut(name, "@")
	if !found {
		return fmt.Errorf("refusing %q: not a snapshot name", name)
	}
	// zfs(8) reads "%" as a range separator, and either side may be left blank:
	// "vol@%" names every snapshot the volume has. Validated as a plain name
	// rather than by listing forbidden characters.
	if err := validation.ValidateSnapshotName(snap); err != nil {
		return fmt.Errorf("refusing snapshot %q: %w", name, err)
	}
	return p.ensureOwnDataset(ds)
}

// snapshotFor resolves the ZFS snapshot a handle names, and binds it to the
// instance the same handle declares.
//
// Confinement to the managed parent is not enough on its own: a handle whose
// Instance names one VM and whose metadata names another VM's snapshot would
// operate on the second while every message says the first. That matters here
// because "zfs rollback -r" destroys every snapshot taken after the one it is
// given.
//
// Both producers — CreateSnapshot and ListSnapshots — set Instance from the
// instance handle, and the snapshot always lives on a disk of that VM, under
// "<zfsParent>/<instance>".
func (p *BhyveProvider) snapshotFor(snapshot provider.SnapshotHandle) (string, error) {
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
	// Strictly below "<zfsParent>/<instance>": the snapshot always lives on one
	// of the VM's disks. Accepting the instance root itself would also let
	// CloneFromSnapshot, which derives its target two directories up, place a
	// clone outside the parent this provider manages.
	ds, _, _ := strings.Cut(zfsName, "@")
	if want := fmt.Sprintf("%s/%s/", p.zfsParent, snapshot.Instance); !strings.HasPrefix(ds, want) {
		return "", fmt.Errorf("snapshot %s belongs to %s, not to the instance %q it is presented for",
			zfsName, ds, snapshot.Instance)
	}
	return zfsName, nil
}

// datasetFor resolves the "dataset" a snapshot handle carries, bound to the
// same instance as snapshotFor binds the snapshot.
func (p *BhyveProvider) datasetFor(snapshot provider.SnapshotHandle) (string, error) {
	ds, ok := snapshot.Metadata["dataset"].(string)
	if !ok || ds == "" {
		return "", fmt.Errorf("invalid snapshot metadata: missing dataset")
	}
	if err := p.ensureOwnDataset(ds); err != nil {
		return "", err
	}
	if err := validation.ValidateInstanceName(snapshot.Instance); err != nil {
		return "", fmt.Errorf("invalid snapshot instance %q: %w", snapshot.Instance, err)
	}
	if want := fmt.Sprintf("%s/%s/", p.zfsParent, snapshot.Instance); !strings.HasPrefix(ds, want) {
		return "", fmt.Errorf("dataset %s belongs to another instance than %q", ds, snapshot.Instance)
	}
	// The two names travel together in one handle and must agree: the clone is
	// made from the snapshot while its target is derived from the dataset, so a
	// mismatch clones one disk into the place computed for another.
	if zfsName, ok := snapshot.Metadata["zfs_name"].(string); ok && zfsName != "" {
		if snapDS, _, found := strings.Cut(zfsName, "@"); found && snapDS != ds {
			return "", fmt.Errorf("snapshot metadata is inconsistent: dataset %s but snapshot on %s", ds, snapDS)
		}
	}
	return ds, nil
}
