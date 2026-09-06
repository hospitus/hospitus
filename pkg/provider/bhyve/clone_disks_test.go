package bhyve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// cloningProvider builds a provider whose zfs calls succeed and whose volumes
// all report 20 GiB, so cloneDisks can be exercised without a pool.
func cloningProvider(t *testing.T) (*BhyveProvider, *execx.Fake) {
	t.Helper()
	dir := t.TempDir()
	p := &BhyveProvider{dataDir: dir, stateDir: dir, zfsParent: testZFSParent}
	fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		if cmd == "zfs" && len(args) > 1 && args[0] == "get" {
			return []byte("21474836480"), nil
		}
		return nil, nil
	}}
	p.runner = fake
	// The full-clone path pipes zfs send into zfs receive, which no fake runner
	// can stand in for; record the transfer instead of performing it.
	p.sendReceive = func(_ context.Context, snapshot, dest string) error {
		fake.Calls = append(fake.Calls, execx.Call{Name: "zfs", Args: []string{"send-receive", snapshot, dest}})
		return nil
	}
	return p, fake
}

// calls renders every recorded command line.
func calls(f *execx.Fake) []string {
	out := make([]string, 0, len(f.Calls))
	for _, c := range f.Calls {
		out = append(out, c.Name+" "+strings.Join(c.Args, " "))
	}
	return out
}

// hasPrefixCall reports whether any recorded call starts with prefix.
func hasPrefixCall(f *execx.Fake, prefix string) bool {
	for _, line := range calls(f) {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// TestCloneDisksClonesEveryVolume is the defect this covers: only the first
// disk used to be copied, and the remaining entries kept the source's paths,
// so two VMs ended up writing one ZVOL.
func TestCloneDisksClonesEveryVolume(t *testing.T) {
	p, fake := cloningProvider(t)

	source := []provider.DiskSpec{
		{Type: provider.DiskTypeZVOL, Path: "/dev/zvol/" + testZFSParent + "/web/disk0", SizeGB: 20},
		{Type: provider.DiskTypeZVOL, Path: "/dev/zvol/" + testZFSParent + "/web/disk1", SizeGB: 50},
		{Type: provider.DiskTypeZVOL, Path: "/dev/zvol/" + testZFSParent + "/web/disk2", SizeGB: 10},
	}
	dest := append([]provider.DiskSpec(nil), source...)

	if _, err := p.cloneDisks(context.Background(), source, dest, "web2", filepath.Join(p.dataDir, "web2"), true); err != nil {
		t.Fatalf("cloneDisks: %v", err)
	}

	for i := range dest {
		want := "/dev/zvol/" + testZFSParent + "/web2/disk" + string(rune('0'+i))
		if dest[i].Path != want {
			t.Errorf("disk %d path = %q, want %q", i, dest[i].Path, want)
		}
		if strings.Contains(dest[i].Path, "/web/") {
			t.Errorf("disk %d still points at the source: %q", i, dest[i].Path)
		}
	}

	for i := 0; i < 3; i++ {
		target := testZFSParent + "/web2/disk" + string(rune('0'+i))
		if !hasPrefixCall(fake, "zfs clone ") || !strings.Contains(strings.Join(calls(fake), "\n"), target) {
			t.Errorf("no clone was made for %s; calls: %v", target, calls(fake))
		}
	}
}

// TestCloneDisksCopiesAFileBackedDisk covers the other half of the same defect:
// a raw image beyond the first came back blank, because the create path made a
// fresh one for it.
func TestCloneDisksCopiesAFileBackedDisk(t *testing.T) {
	p, fake := cloningProvider(t)

	sourceDir := filepath.Join(p.dataDir, "web")
	if err := os.MkdirAll(sourceDir, 0o750); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(sourceDir, "disk1.img")
	if err := os.WriteFile(image, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	source := []provider.DiskSpec{
		{Type: provider.DiskTypeZVOL, Path: "/dev/zvol/" + testZFSParent + "/web/disk0", SizeGB: 20},
		{Type: provider.DiskTypeRaw, Path: image, SizeGB: 5},
	}
	dest := append([]provider.DiskSpec(nil), source...)

	if _, err := p.cloneDisks(context.Background(), source, dest, "web2", filepath.Join(p.dataDir, "web2"), true); err != nil {
		t.Fatalf("cloneDisks: %v", err)
	}

	wantDest := filepath.Join(p.dataDir, "web2", "disk1.img")
	if dest[1].Path != wantDest {
		t.Errorf("file-backed disk path = %q, want %q", dest[1].Path, wantDest)
	}
	if !hasPrefixCall(fake, "cp -p "+image) {
		t.Errorf("the image was not copied; calls: %v", calls(fake))
	}
}

// TestCloneDisksKeepsSnapshotsOnlyForALinkedClone pins the difference: a
// linked clone depends on the snapshot it was made from, a full clone does not
// and leaving it behind pins the source's blocks forever.
func TestCloneDisksKeepsSnapshotsOnlyForALinkedClone(t *testing.T) {
	source := []provider.DiskSpec{{Type: provider.DiskTypeZVOL, Path: "/dev/zvol/" + testZFSParent + "/web/disk0", SizeGB: 20}}

	for _, tc := range []struct {
		name        string
		linked      bool
		wantDestroy bool
	}{
		{"linked clone keeps its snapshot", true, false},
		{"full clone removes the scaffolding", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, fake := cloningProvider(t)
			dest := append([]provider.DiskSpec(nil), source...)

			made, err := p.cloneDisks(context.Background(), source, dest, "web2", filepath.Join(p.dataDir, "web2"), tc.linked)
			if err != nil {
				t.Fatalf("cloneDisks: %v", err)
			}

			destroyed := hasPrefixCall(fake, "zfs destroy") && strings.Contains(strings.Join(calls(fake), "\n"), "@clone-web2-")
			if destroyed != tc.wantDestroy {
				t.Errorf("snapshot destroyed = %v, want %v; calls: %v", destroyed, tc.wantDestroy, calls(fake))
			}
			if tc.linked && len(made.snapshots) == 0 {
				t.Error("a linked clone must keep its snapshot recorded for rollback")
			}
			if !tc.linked && len(made.snapshots) != 0 {
				t.Errorf("a full clone should record no snapshot to undo, got %v", made.snapshots)
			}
		})
	}
}

// TestCloneDisksRefusesANonVolumePath guards the path parsing: a ZVOL disk
// whose path is not under /dev/zvol would otherwise produce a dataset name
// built out of nonsense.
func TestCloneDisksRefusesANonVolumePath(t *testing.T) {
	p, _ := cloningProvider(t)
	source := []provider.DiskSpec{{Type: provider.DiskTypeZVOL, Path: "/var/lib/hospitus/bhyve/web/disk0", SizeGB: 20}}
	dest := append([]provider.DiskSpec(nil), source...)

	if _, err := p.cloneDisks(context.Background(), source, dest, "web2", filepath.Join(p.dataDir, "web2"), true); err == nil {
		t.Fatal("a ZVOL disk outside /dev/zvol was accepted")
	}
}

// TestCopyAnyMapDoesNotAliasTheSource covers the clone's provider config, which
// carries the TPM, VNC and passthrough settings: writing to the clone's copy
// used to write into the source instance's own spec.
func TestCopyAnyMapDoesNotAliasTheSource(t *testing.T) {
	source := map[string]interface{}{"tpm": true, "vnc_port": 5900}

	clone := copyAnyMap(source)
	clone["vnc_port"] = 5901

	if source["vnc_port"] != 5900 {
		t.Errorf("the source's config was changed to %v", source["vnc_port"])
	}
	if copyAnyMap(nil) != nil {
		t.Error("a nil config should copy to nil, not to an empty map")
	}
}

// stoppedVM records a stopped VM with the given disks, so CloneInstance can
// read it back through GetInstanceInfo.
func stoppedVM(t *testing.T, p *BhyveProvider, name string, diskPaths []string) {
	t.Helper()
	vmDir := filepath.Join(p.dataDir, name)
	if err := os.MkdirAll(vmDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := &vmConfig{Name: name, CPUs: 2, MemoryMB: 2048, DiskPaths: diskPaths}
	if err := p.saveVMConfig(vmDir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := p.saveVMState(vmDir, &vmState{Name: name, State: provider.StateStopped}); err != nil {
		t.Fatal(err)
	}
}

// TestCloneRefusesAPhysicalDisk covers the case a copy cannot serve: handing
// the same block device to a second VM corrupts it the moment both run.
func TestCloneRefusesAPhysicalDisk(t *testing.T) {
	p, fake := cloningProvider(t)

	// A real device path is what makes the source's disk read back as physical.
	stoppedVM(t, p, "win", []string{"/dev/zvol/" + testZFSParent + "/win/disk0", "/dev/nda0"})

	_, err := p.CloneInstance(context.Background(), provider.InstanceHandle{ID: "win"}, "win2", provider.CloneOptions{LinkedClone: true})
	if err == nil {
		t.Fatal("a VM with a physical disk was cloned")
	}
	if !strings.Contains(err.Error(), "/dev/nda0") {
		t.Errorf("error %q does not name the device that cannot be cloned", err)
	}
	for _, line := range calls(fake) {
		if strings.HasPrefix(line, "zfs snapshot") || strings.HasPrefix(line, "zfs clone") {
			t.Errorf("the refusal came after work had started: %q", line)
		}
	}
}
