package bhyve

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestBhyveDiskType(t *testing.T) {
	cases := map[string]provider.DiskType{
		"/dev/zvol/" + testZFSParent + "/web/disk0": provider.DiskTypeZVOL,
		"/dev/ada2":                             provider.DiskTypePhysical,
		"/var/lib/hospitus/bhyve/web/disk1.img": provider.DiskTypeRaw,
	}
	for path, want := range cases {
		if got := bhyveDiskType(path); got != want {
			t.Errorf("bhyveDiskType(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestGetInstanceInfoPopulatesSpec verifies GetInstanceInfo fills Spec.Disks and
// Spec.Networks from the VM config, which cloning relies on (audit HIGH
// bhyve_lifecycle.go:1007).
func TestGetInstanceInfoPopulatesSpec(t *testing.T) {
	dir := t.TempDir()
	// A fake runner: without one, reading the spec back shells out to zfs for
	// the ZVOL's size and the test depends on the host's pools.
	p := &BhyveProvider{dataDir: dir, zfsParent: testZFSParent, runner: &execx.Fake{}}
	vmDir := filepath.Join(dir, "web")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &vmConfig{
		Name:        "web",
		CPUs:        2,
		MemoryMB:    2048,
		DiskPaths:   []string{"/dev/zvol/" + testZFSParent + "/web/disk0", filepath.Join(vmDir, "data.img")},
		DiskDrivers: []string{"virtio-blk", "ahci-hd"},
		TapDevs:     []string{"tap0", "tap1"},
		Bridges:     []string{"bridge0", ""},
		NetTypes:    []string{"bridge", "nat"},
		NATEnabled:  true,
	}
	if err := p.saveVMConfig(vmDir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := p.saveVMState(vmDir, &vmState{Name: "web", State: provider.StateStopped}); err != nil {
		t.Fatal(err)
	}

	info, err := p.GetInstanceInfo(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("GetInstanceInfo: %v", err)
	}
	if len(info.Spec.Disks) != 2 {
		t.Fatalf("expected 2 disks, got %d", len(info.Spec.Disks))
	}
	if info.Spec.Disks[0].Type != provider.DiskTypeZVOL {
		t.Errorf("disk0 type = %q, want zvol", info.Spec.Disks[0].Type)
	}
	// The clone paths read this back and CreateInstance switches on Type: a NAT
	// NIC reported as a bridge got neither.
	if len(info.Spec.Networks) != 2 {
		t.Fatalf("networks = %d, want 2", len(info.Spec.Networks))
	}
	if got := info.Spec.Networks[0]; got.Type != provider.NetworkTypeBridge || got.Bridge != "bridge0" {
		t.Errorf("first NIC = %+v, want a bridge NIC on bridge0", got)
	}
	if got := info.Spec.Networks[1]; got.Type != provider.NetworkTypeNAT {
		t.Errorf("second NIC = %+v, want the NAT type it was created with", got)
	}
}
