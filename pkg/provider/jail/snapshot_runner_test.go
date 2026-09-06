package jail

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestCreateSnapshotRunner(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}
	handle := provider.InstanceHandle{ID: "web", Metadata: map[string]interface{}{"zfs_dataset": "zroot/hospitus/jails/web"}}

	sh, err := p.CreateSnapshot(context.Background(), handle, "backup1")
	if err != nil {
		t.Fatalf("CreateSnapshot err = %v", err)
	}
	if sh.Metadata["zfs_name"] != "zroot/hospitus/jails/web@backup1" {
		t.Errorf("zfs_name = %v", sh.Metadata["zfs_name"])
	}
	if want := "zfs snapshot zroot/hospitus/jails/web@backup1"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestCreateSnapshotInvalidNameRunner(t *testing.T) {
	p := &JailProvider{runner: &execx.Fake{}, zfsParent: "zroot/hospitus/jails"}
	handle := provider.InstanceHandle{ID: "web", Metadata: map[string]interface{}{"zfs_dataset": "zroot/hospitus/jails/web"}}
	if _, err := p.CreateSnapshot(context.Background(), handle, "bad;name"); err == nil {
		t.Fatal("expected validation error")
	}
}

// TestCreateSnapshotDerivesDatasetWhenUnrecorded covers a handle written before
// the dataset was recorded: the name is derived from the jail's, as delete and
// promote already did.
func TestCreateSnapshotDerivesDatasetWhenUnrecorded(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}
	handle := provider.InstanceHandle{ID: "web"}
	if _, err := p.CreateSnapshot(context.Background(), handle, "backup1"); err != nil {
		t.Fatalf("CreateSnapshot err = %v", err)
	}
	if want := "zfs snapshot zroot/hospitus/jails/web@backup1"; !hasCmd(fake, want) {
		t.Errorf("missing %q; got %q", want, lastCmd(fake))
	}
}

// TestSnapshotRefusesAForeignDataset is the guard the API needs: a PATCH could
// once merge its own zfs_dataset into the handle.
func TestSnapshotRefusesAForeignDataset(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}
	handle := provider.InstanceHandle{
		ID:       "web",
		Metadata: map[string]interface{}{"zfs_dataset": "zroot/ROOT/default"},
	}

	if _, err := p.CreateSnapshot(context.Background(), handle, "backup1"); err == nil {
		t.Error("a snapshot was taken of a dataset outside the jail parent")
	}
	if _, err := p.ListSnapshots(context.Background(), handle); err == nil {
		t.Error("snapshots of a foreign dataset were listed")
	}

	snap := provider.SnapshotHandle{
		Instance: "web",
		Metadata: map[string]interface{}{"zfs_name": "zroot/ROOT/default@backup1"},
	}
	if err := p.DeleteSnapshot(context.Background(), snap); err == nil {
		t.Error("a snapshot of a foreign dataset was destroyed")
	}
	if fake.CallCount() != 0 {
		t.Errorf("a zfs command ran anyway: %q", lastCmd(fake))
	}
}

func TestDeleteSnapshotRunner(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}
	snap := provider.SnapshotHandle{Instance: "web", Metadata: map[string]interface{}{"zfs_name": "zroot/hospitus/jails/web@backup1"}}

	if err := p.DeleteSnapshot(context.Background(), snap); err != nil {
		t.Fatalf("DeleteSnapshot err = %v", err)
	}
	if want := "zfs destroy zroot/hospitus/jails/web@backup1"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestDeleteSnapshotMissingMeta(t *testing.T) {
	p := &JailProvider{runner: &execx.Fake{}, zfsParent: "zroot/hospitus/jails"}
	if err := p.DeleteSnapshot(context.Background(), provider.SnapshotHandle{}); err == nil {
		t.Fatal("expected error for missing zfs_name")
	}
}

func TestDeleteSnapshotCmdError(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, errors.New("busy") }}
	p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}
	snap := provider.SnapshotHandle{Metadata: map[string]interface{}{"zfs_name": "ds@s"}}
	if err := p.DeleteSnapshot(context.Background(), snap); err == nil {
		t.Fatal("expected error")
	}
}

func TestListSnapshotsRunner(t *testing.T) {
	out := "zroot/hospitus/jails/web@backup1\t1700000000\t294912\n" +
		"zroot/hospitus/jails/web@backup2\t1700000100\t1048576\n"
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return []byte(out), nil }}
	p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}
	handle := provider.InstanceHandle{ID: "web", Metadata: map[string]interface{}{"zfs_dataset": "zroot/hospitus/jails/web"}}

	snaps, err := p.ListSnapshots(context.Background(), handle)
	if err != nil {
		t.Fatalf("ListSnapshots err = %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(snaps))
	}
	if snaps[0].Name != "backup1" {
		t.Errorf("snaps[0].Name = %q, want backup1", snaps[0].Name)
	}
	if snaps[1].SizeMB != 1 {
		t.Errorf("snaps[1].SizeMB = %d, want 1", snaps[1].SizeMB)
	}
}

func TestListSnapshotsNoneAvailable(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("no datasets available"), errors.New("exit 1")
	}}
	p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}
	handle := provider.InstanceHandle{ID: "web", Metadata: map[string]interface{}{"zfs_dataset": "zroot/hospitus/jails/web"}}
	snaps, err := p.ListSnapshots(context.Background(), handle)
	if err != nil {
		t.Fatalf("expected nil error for 'no datasets available', got %v", err)
	}
	if len(snaps) != 0 {
		t.Errorf("expected empty, got %d", len(snaps))
	}
}

// TestSnapshotHandlesAreBoundToTheirInstance covers the whole family at its one
// resolution point: confinement to the managed parents stops a name from
// outside, but a forged handle can still declare one jail and name another
// jail's snapshot — and "zfs rollback -r" destroys every later snapshot of
// whatever dataset it is handed.
func TestSnapshotHandlesAreBoundToTheirInstance(t *testing.T) {
	// "web" has to exist and be stopped, or RestoreSnapshot returns "instance
	// not found" before it ever looks at the snapshot — an error, so an
	// err != nil assertion would pass without the guard being reached at all.
	stateDir := t.TempDir()
	fake := &execx.Fake{Func: func(cmd string, _ []string) ([]byte, error) {
		if cmd == "jls" {
			return nil, errors.New("jail not found") // stopped
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake, stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	if err := p.saveJailConfig(&jailConfig{Name: "web", Path: t.TempDir()},
		filepath.Join(stateDir, "web.json")); err != nil {
		t.Fatalf("saveJailConfig: %v", err)
	}

	mismatched := provider.SnapshotHandle{
		Instance: "web",
		Metadata: map[string]interface{}{"zfs_name": "zroot/hospitus/jails/db@backup1"},
	}
	// The guard's own words, so a refusal for any other reason fails the test.
	const wantMsg = `snapshot zroot/hospitus/jails/db@backup1 belongs to ` +
		`zroot/hospitus/jails/db, not to the instance "web" it is presented for`

	if err := p.DeleteSnapshot(context.Background(), mismatched); err == nil ||
		!strings.Contains(err.Error(), wantMsg) {
		t.Errorf("DeleteSnapshot: got %v, want the mismatch refusal", err)
	}
	if err := p.RestoreSnapshot(context.Background(),
		provider.InstanceHandle{ID: "web"}, mismatched); err == nil ||
		!strings.Contains(err.Error(), wantMsg) {
		t.Errorf("RestoreSnapshot: got %v, want the mismatch refusal", err)
	}
	if _, err := p.CloneFromSnapshot(context.Background(), mismatched, "copy",
		provider.CloneOptions{}); err == nil ||
		!strings.Contains(err.Error(), wantMsg) {
		t.Errorf("CloneFromSnapshot: got %v, want the mismatch refusal", err)
	}

	for _, c := range fake.Snapshot() {
		if c.Name == "zfs" && len(c.Args) > 0 &&
			(c.Args[0] == "destroy" || c.Args[0] == "rollback" || c.Args[0] == "clone") {
			t.Errorf("a zfs %s ran on a mismatched handle: %v", c.Args[0], c.Args)
		}
	}
}
