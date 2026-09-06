package qemu

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// persistVM writes a VM config to the provider's state dir and creates the
// primary disk file so os.Stat checks succeed. It returns the disk path.
func persistVM(t *testing.T, p *QEMUProvider, name string) string {
	t.Helper()
	// The layout CreateInstance and CloneInstance actually use: one directory
	// per VM, holding disk0.qcow2. A flat "<dataDir>/<name>.qcow2" passed the
	// old tests but is not a path this provider ever produces.
	vmDir := filepath.Join(p.dataDir, name)
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(vmDir, "disk0.qcow2")
	if err := os.WriteFile(disk, []byte("qcow"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &vmConfig{
		Name: name,
		Spec: provider.InstanceSpec{
			Name:  name,
			CPUs:  2,
			Disks: []provider.DiskSpec{{Path: disk, Type: provider.DiskTypeQCOW2}},
		},
	}
	if err := p.saveVMConfig(cfg, filepath.Join(p.stateDir, name+".json")); err != nil {
		t.Fatal(err)
	}
	return disk
}

func TestCreateSnapshotRunsQemuImg(t *testing.T) {
	// Arrange
	fake := &execx.Fake{}
	p := fakeProvider(t, fake)
	disk := persistVM(t, p, "web")

	// Act
	snap, err := p.CreateSnapshot(context.Background(), provider.InstanceHandle{ID: "web"}, "snap1")
	// Assert
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if snap.ID != "web_snap1" {
		t.Errorf("snapshot ID = %q, want web_snap1", snap.ID)
	}
	args := fake.Calls[0].Args
	if args[0] != "snapshot" || !slices.Contains(args, "-c") || !slices.Contains(args, "snap1") || !slices.Contains(args, disk) {
		t.Errorf("qemu-img args = %v, want snapshot -c snap1 <disk>", args)
	}
}

func TestCreateSnapshotRejectsInvalidName(t *testing.T) {
	fake := &execx.Fake{}
	p := fakeProvider(t, fake)
	persistVM(t, p, "web")
	if _, err := p.CreateSnapshot(context.Background(), provider.InstanceHandle{ID: "web"}, "bad name!"); err == nil {
		t.Error("expected error for an invalid snapshot name")
	}
	if fake.CallCount() != 0 {
		t.Errorf("no qemu-img call expected for invalid name, got %d", fake.CallCount())
	}
}

func TestCreateSnapshotMissingDisk(t *testing.T) {
	// Arrange: config references a disk path that does not exist on disk.
	fake := &execx.Fake{}
	p := fakeProvider(t, fake)
	cfg := &vmConfig{
		Name: "web",
		Spec: provider.InstanceSpec{
			Name: "web",
			// A well-formed managed disk that simply is not there: with a zero
			// DiskType and a path outside the VM directory, the test could pass
			// on type or containment validation instead of on the missing file
			// it names.
			Disks: []provider.DiskSpec{{
				Path: filepath.Join(p.dataDir, "web", "gone.qcow2"),
				Type: provider.DiskTypeQCOW2,
			}},
		},
	}
	if err := p.saveVMConfig(cfg, filepath.Join(p.stateDir, "web.json")); err != nil {
		t.Fatal(err)
	}

	// Act / Assert
	if _, err := p.CreateSnapshot(context.Background(), provider.InstanceHandle{ID: "web"}, "snap1"); err == nil {
		t.Error("expected error when the disk image is missing")
	}
}

func TestDeleteSnapshotRunsQemuImg(t *testing.T) {
	// Arrange
	fake := &execx.Fake{}
	p := fakeProvider(t, fake)
	disk := persistVM(t, p, "web")
	snap := provider.SnapshotHandle{
		Instance: "web",
		Metadata: map[string]interface{}{"name": "snap1", "disk_path": disk},
	}

	// Act
	err := p.DeleteSnapshot(context.Background(), snap)
	// Assert
	if err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
	// The disk too: an assertion on the snapshot name alone passes even when
	// the command was aimed at another VM's image.
	args := fake.Calls[0].Args
	if args[0] != "snapshot" || !slices.Contains(args, "-d") || !slices.Contains(args, "snap1") {
		t.Errorf("qemu-img args = %v, want snapshot -d snap1", args)
	}
	// The resolved path: snapshotTargetFor returns what the containment check
	// cleared, and on macOS t.TempDir() sits under the /var -> /private/var
	// symlink, so the two spellings differ.
	wantDisk, err := filepath.EvalSymlinks(disk)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", disk, err)
	}
	if !slices.Contains(args, wantDisk) {
		t.Errorf("qemu-img args = %v, want the command aimed at %s", args, wantDisk)
	}
}

func TestDeleteSnapshotMissingMetadata(t *testing.T) {
	p := fakeProvider(t, &execx.Fake{})
	if err := p.DeleteSnapshot(context.Background(), provider.SnapshotHandle{}); err == nil {
		t.Error("expected error when name metadata is missing")
	}
}

func TestRestoreSnapshotRunsQemuImg(t *testing.T) {
	// Arrange
	fake := &execx.Fake{}
	p := fakeProvider(t, fake)
	disk := persistVM(t, p, "web")
	snap := provider.SnapshotHandle{
		Instance: "web",
		Metadata: map[string]interface{}{"name": "snap1", "disk_path": disk},
	}

	// Act
	err := p.RestoreSnapshot(context.Background(), provider.InstanceHandle{ID: "web"}, snap)
	// Assert
	if err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	// The disk too: an assertion on the snapshot name alone passes even when
	// the command was aimed at another VM's image.
	args := fake.Calls[0].Args
	if args[0] != "snapshot" || !slices.Contains(args, "-a") || !slices.Contains(args, "snap1") {
		t.Errorf("qemu-img args = %v, want snapshot -a snap1", args)
	}
	// The resolved path: snapshotTargetFor returns what the containment check
	// cleared, and on macOS t.TempDir() sits under the /var -> /private/var
	// symlink, so the two spellings differ.
	wantDisk, err := filepath.EvalSymlinks(disk)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", disk, err)
	}
	if !slices.Contains(args, wantDisk) {
		t.Errorf("qemu-img args = %v, want the command aimed at %s", args, wantDisk)
	}
}

func TestRestoreSnapshotRejectsForeignInstance(t *testing.T) {
	p := fakeProvider(t, &execx.Fake{})
	persistVM(t, p, "web")
	snap := provider.SnapshotHandle{Instance: "other", Metadata: map[string]interface{}{"name": "s", "disk_path": "x"}}
	if err := p.RestoreSnapshot(context.Background(), provider.InstanceHandle{ID: "web"}, snap); err == nil {
		t.Error("expected error when snapshot belongs to another instance")
	}
}

func TestListSnapshotsPropagatesError(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("qemu-img: Could not open"), errors.New("exit 1")
	}}
	p := fakeProvider(t, fake)
	persistVM(t, p, "web")
	if _, err := p.ListSnapshots(context.Background(), provider.InstanceHandle{ID: "web"}); err == nil {
		t.Error("expected error from failing qemu-img snapshot -l")
	}
}
