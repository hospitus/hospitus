package bhyve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// snapshotProvider builds a provider whose "web" VM is persisted with a single
// ZVOL disk in the stopped state, backed by the supplied fake runner.
func snapshotProvider(t *testing.T, fake *execx.Fake) *BhyveProvider {
	t.Helper()
	dir := t.TempDir()
	p := &BhyveProvider{dataDir: dir, runner: fake, zfsParent: testZFSParent}
	vmDir := filepath.Join(dir, "web")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &vmConfig{
		Name:        "web",
		CPUs:        2,
		MemoryMB:    2048,
		DiskPaths:   []string{"/dev/zvol/" + testZFSParent + "/web/disk0"},
		DiskDrivers: []string{"virtio-blk"},
	}
	if err := p.saveVMConfig(vmDir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := p.saveVMState(vmDir, &vmState{Name: "web", State: provider.StateStopped}); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCreateSnapshotRunsZFSSnapshot(t *testing.T) {
	// Arrange
	fake := &execx.Fake{}
	p := snapshotProvider(t, fake)

	// Act
	snap, err := p.CreateSnapshot(context.Background(), provider.InstanceHandle{ID: "web"}, "snap1")
	// Assert
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if snap.ID != "web_snap1" {
		t.Errorf("snapshot ID = %q, want web_snap1", snap.ID)
	}
	if got := snap.Metadata["zfs_name"]; got != testZFSParent+"/web/disk0@snap1" {
		t.Errorf("zfs_name = %v, want "+testZFSParent+"/web/disk0@snap1", got)
	}
	if !hasCmd(fake, "zfs", "snapshot "+testZFSParent+"/web/disk0@snap1") {
		t.Errorf("the snapshot command did not run; got %v", fake.Calls)
	}
}

func TestCreateSnapshotRejectsInvalidName(t *testing.T) {
	fake := &execx.Fake{}
	p := snapshotProvider(t, fake)
	if _, err := p.CreateSnapshot(context.Background(), provider.InstanceHandle{ID: "web"}, "bad name!"); err == nil {
		t.Error("expected error for invalid snapshot name")
	}
	if fake.CallCount() != 0 {
		t.Errorf("no zfs command should run for an invalid name, got %d", fake.CallCount())
	}
}

func TestCreateSnapshotRejectsNonZVOLDisk(t *testing.T) {
	// Arrange: persist a VM whose primary disk is a raw image, not a ZVOL.
	dir := t.TempDir()
	fake := &execx.Fake{}
	p := &BhyveProvider{dataDir: dir, runner: fake, zfsParent: testZFSParent}
	vmDir := filepath.Join(dir, "web")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &vmConfig{Name: "web", DiskPaths: []string{filepath.Join(vmDir, "disk0.img")}}
	if err := p.saveVMConfig(vmDir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := p.saveVMState(vmDir, &vmState{Name: "web", State: provider.StateStopped}); err != nil {
		t.Fatal(err)
	}

	// Act
	_, err := p.CreateSnapshot(context.Background(), provider.InstanceHandle{ID: "web"}, "snap1")

	// Assert
	if err == nil {
		t.Error("expected error for a non-ZVOL primary disk")
	}
}

func TestCreateSnapshotPropagatesZFSError(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("dataset does not exist"), errors.New("exit 1")
	}}
	p := snapshotProvider(t, fake)
	if _, err := p.CreateSnapshot(context.Background(), provider.InstanceHandle{ID: "web"}, "snap1"); err == nil {
		t.Error("expected error from failing zfs snapshot")
	}
}

func TestRestoreSnapshotRunsRollback(t *testing.T) {
	// Arrange
	fake := &execx.Fake{}
	p := snapshotProvider(t, fake)
	snap := provider.SnapshotHandle{
		Instance: "web",
		Metadata: map[string]interface{}{"zfs_name": testZFSParent + "/web/disk0@snap1"},
	}

	// Act
	err := p.RestoreSnapshot(context.Background(), provider.InstanceHandle{ID: "web"}, snap)
	// Assert
	if err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	if !hasCmd(fake, "zfs", "rollback -r "+testZFSParent+"/web/disk0@snap1") {
		t.Errorf("the rollback did not run; got %v", fake.Calls)
	}
}

func TestRestoreSnapshotRejectsForeignInstance(t *testing.T) {
	fake := &execx.Fake{}
	p := snapshotProvider(t, fake)
	snap := provider.SnapshotHandle{Instance: "other", Metadata: map[string]interface{}{"zfs_name": "x@y"}}
	if err := p.RestoreSnapshot(context.Background(), provider.InstanceHandle{ID: "web"}, snap); err == nil {
		t.Error("expected error when snapshot belongs to another instance")
	}
	if fake.CallCount() != 0 {
		t.Errorf("no zfs command should run for a foreign snapshot, got %d", fake.CallCount())
	}
}

func TestRestoreSnapshotRequiresStoppedInstance(t *testing.T) {
	// Arrange: persist the VM in the running state.
	dir := t.TempDir()
	fake := &execx.Fake{}
	p := &BhyveProvider{dataDir: dir, runner: fake, zfsParent: testZFSParent}
	vmDir := filepath.Join(dir, "web")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := p.saveVMConfig(vmDir, &vmConfig{Name: "web", DiskPaths: []string{"/dev/zvol/pool/web@0"}}); err != nil {
		t.Fatal(err)
	}
	if err := p.saveVMState(vmDir, &vmState{Name: "web", State: provider.StateRunning}); err != nil {
		t.Fatal(err)
	}
	snap := provider.SnapshotHandle{Instance: "web", Metadata: map[string]interface{}{"zfs_name": "pool/web@0"}}

	// Act / Assert
	if err := p.RestoreSnapshot(context.Background(), provider.InstanceHandle{ID: "web"}, snap); err == nil {
		t.Error("expected error when instance is not stopped")
	}
}

func TestListSnapshotsParsesZFSOutput(t *testing.T) {
	// Arrange: zfs list -H -p output — creation as Unix time, used as raw bytes.
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte(testZFSParent + "/web/disk0@snap1\t1700000000\t104857600\n" +
			testZFSParent + "/web/disk0@snap2\t1700000600\t2147483648\n"), nil
	}}
	p := snapshotProvider(t, fake)

	// Act
	snaps, err := p.ListSnapshots(context.Background(), provider.InstanceHandle{ID: "web"})
	// Assert
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snaps))
	}
	if snaps[0].Name != "snap1" || snaps[0].SizeMB != 100 {
		t.Errorf("snap1 = %q (%d MB), want snap1 (100 MB)", snaps[0].Name, snaps[0].SizeMB)
	}
	if snaps[1].SizeMB != 2048 {
		t.Errorf("snap2 size = %d MB, want 2048 (2G)", snaps[1].SizeMB)
	}
}

func TestListSnapshotsNoDatasetsIsEmpty(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("cannot open: no datasets available"), errors.New("exit 1")
	}}
	p := snapshotProvider(t, fake)
	snaps, err := p.ListSnapshots(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 0 {
		t.Errorf("expected no snapshots, got %d", len(snaps))
	}
}

// hasCmd reports whether any recorded call matches the given command line.
//
// Asserting on Calls[0] broke the moment provider code stopped bypassing the
// runner: a helper the fake never used to see — "zfs get volsize" — now lands
// ahead of the command under test. The position was never the point.
func hasCmd(fake *execx.Fake, name, args string) bool {
	for _, c := range fake.Calls {
		if c.Name == name && strings.Join(c.Args, " ") == args {
			return true
		}
	}
	return false
}
