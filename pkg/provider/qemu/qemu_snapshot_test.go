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
	disk := filepath.Join(p.dataDir, name+".qcow2")
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
			Name:  "web",
			Disks: []provider.DiskSpec{{Path: filepath.Join(p.dataDir, "gone.qcow2")}},
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
	disk := filepath.Join(p.dataDir, "web.qcow2")
	if err := os.WriteFile(disk, []byte("qcow"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap := provider.SnapshotHandle{Metadata: map[string]interface{}{"name": "snap1", "disk_path": disk}}

	// Act
	err := p.DeleteSnapshot(context.Background(), snap)
	// Assert
	if err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
	args := fake.Calls[0].Args
	if args[0] != "snapshot" || !slices.Contains(args, "-d") || !slices.Contains(args, "snap1") {
		t.Errorf("qemu-img args = %v, want snapshot -d snap1", args)
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
	args := fake.Calls[0].Args
	if args[0] != "snapshot" || !slices.Contains(args, "-a") || !slices.Contains(args, "snap1") {
		t.Errorf("qemu-img args = %v, want snapshot -a snap1", args)
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
