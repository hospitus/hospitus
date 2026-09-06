package bhyve

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestBhyveDiskType(t *testing.T) {
	cases := map[string]provider.DiskType{
		"/dev/zvol/zroot/hospitus/bhyve/web/disk0": provider.DiskTypeZVOL,
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
	p := &BhyveProvider{dataDir: dir}
	vmDir := filepath.Join(dir, "web")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &vmConfig{
		Name:        "web",
		CPUs:        2,
		MemoryMB:    2048,
		DiskPaths:   []string{"/dev/zvol/zroot/hospitus/bhyve/web/disk0", filepath.Join(vmDir, "data.img")},
		DiskDrivers: []string{"virtio-blk", "ahci-hd"},
		TapDevs:     []string{"tap0", "tap1"},
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
	if len(info.Spec.Networks) != 2 {
		t.Errorf("expected 2 networks, got %d", len(info.Spec.Networks))
	}
}
