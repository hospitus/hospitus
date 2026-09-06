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

// configVMProvider persists a VM whose config is the supplied cfg and whose
// state is stopped.
func configVMProvider(t *testing.T, cfg *vmConfig) *BhyveProvider {
	t.Helper()
	dir := t.TempDir()
	p := &BhyveProvider{dataDir: dir, stateDir: dir, runner: &execx.Fake{}}
	vmDir := filepath.Join(dir, cfg.Name)
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := p.saveVMConfig(vmDir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := p.saveVMState(vmDir, &vmState{Name: cfg.Name, State: provider.StateStopped}); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGetInstanceStateStoppedNoProbe(t *testing.T) {
	p := configVMProvider(t, &vmConfig{Name: "web"})
	state, err := p.GetInstanceState(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("GetInstanceState: %v", err)
	}
	if state != provider.StateStopped {
		t.Errorf("state = %q, want stopped", state)
	}
}

func TestGetInstanceStateReconcilesDeadPID(t *testing.T) {
	// Arrange: a "running" VM whose recorded PID cannot exist.
	dir := t.TempDir()
	p := &BhyveProvider{dataDir: dir, stateDir: dir, runner: &execx.Fake{}}
	vmDir := filepath.Join(dir, "web")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := p.saveVMConfig(vmDir, &vmConfig{Name: "web"}); err != nil {
		t.Fatal(err)
	}
	// PID 2147483646 is effectively guaranteed not to be a live process.
	if err := p.saveVMState(vmDir, &vmState{Name: "web", State: provider.StateRunning, PID: 2147483646}); err != nil {
		t.Fatal(err)
	}

	// Act
	state, err := p.GetInstanceState(context.Background(), provider.InstanceHandle{ID: "web"})
	// Assert: liveness probe fails, state reconciles to stopped and is persisted.
	if err != nil {
		t.Fatalf("GetInstanceState: %v", err)
	}
	if state != provider.StateStopped {
		t.Errorf("state = %q, want stopped after reconcile", state)
	}
	reloaded, err := p.loadVMState(vmDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PID != 0 {
		t.Errorf("reconciled PID = %d, want 0", reloaded.PID)
	}
}

func TestGetInstanceStateMissing(t *testing.T) {
	p := &BhyveProvider{dataDir: t.TempDir()}
	if _, err := p.GetInstanceState(context.Background(), provider.InstanceHandle{ID: "ghost"}); err == nil {
		t.Error("expected error when VM state is missing")
	}
}

func TestGetVNCInfoDefaults(t *testing.T) {
	// Arrange: VNC enabled but no explicit host/port → defaults apply.
	p := configVMProvider(t, &vmConfig{Name: "web", VNCEnabled: true})

	// Act
	info, err := p.GetVNCInfo(context.Background(), provider.InstanceHandle{ID: "web"})
	// Assert
	if err != nil {
		t.Fatalf("GetVNCInfo: %v", err)
	}
	if info.Host != "127.0.0.1" || info.Port != 5900 || info.Width != 1024 || info.Height != 768 {
		t.Errorf("defaults not applied: %+v", info)
	}
	if info.URI != "vnc://127.0.0.1:5900" {
		t.Errorf("URI = %q", info.URI)
	}
	if info.Running {
		t.Error("stopped VM should report Running=false")
	}
}

func TestGetVNCInfoDisabled(t *testing.T) {
	p := configVMProvider(t, &vmConfig{Name: "web", VNCEnabled: false})
	if _, err := p.GetVNCInfo(context.Background(), provider.InstanceHandle{ID: "web"}); err == nil {
		t.Error("expected error when VNC is not enabled")
	}
}

func TestEnableVNCPersistsConfig(t *testing.T) {
	// Arrange
	p := configVMProvider(t, &vmConfig{Name: "web"})

	// Act
	err := p.EnableVNC(context.Background(), provider.InstanceHandle{ID: "web"}, 5901, 1280, 800, "127.0.0.1", true)
	// Assert
	if err != nil {
		t.Fatalf("EnableVNC: %v", err)
	}
	cfg, err := p.loadVMConfig(filepath.Join(p.dataDir, "web"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.VNCEnabled || cfg.VNCPort != 5901 || cfg.VNCWidth != 1280 || cfg.VNCHeight != 800 || !cfg.VNCWait {
		t.Errorf("VNC config not persisted: %+v", cfg)
	}
}

func TestEnableVNCRejectsNonLoopbackHost(t *testing.T) {
	p := configVMProvider(t, &vmConfig{Name: "web"})
	if err := p.EnableVNC(context.Background(), provider.InstanceHandle{ID: "web"}, 0, 0, 0, "0.0.0.0", false); err == nil {
		t.Error("expected error for a non-loopback VNC bind address")
	}
}

func TestDisableVNCClearsFlag(t *testing.T) {
	p := configVMProvider(t, &vmConfig{Name: "web", VNCEnabled: true})
	if err := p.DisableVNC(context.Background(), provider.InstanceHandle{ID: "web"}); err != nil {
		t.Fatalf("DisableVNC: %v", err)
	}
	cfg, err := p.loadVMConfig(filepath.Join(p.dataDir, "web"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VNCEnabled {
		t.Error("VNCEnabled should be false after DisableVNC")
	}
}

func TestInsertMediaAddsCDROM(t *testing.T) {
	// Arrange: an ISO file on disk and a VM with a single data disk.
	p := configVMProvider(t, &vmConfig{
		Name:        "web",
		DiskPaths:   []string{"/dev/zvol/pool/web/disk0"},
		DiskDrivers: []string{"virtio-blk"},
	})
	// The ISO lives in the VM's own directory: InsertMedia, like AttachISO,
	// refuses media outside the image directory and the VM directory.
	iso := filepath.Join(p.dataDir, "web", "install.iso")
	if err := os.WriteFile(iso, []byte("iso"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Act
	err := p.InsertMedia(context.Background(), provider.InstanceHandle{ID: "web"}, provider.MediaSpec{
		Type: provider.MediaTypeCDROM,
		Path: iso,
	})
	// Assert
	if err != nil {
		t.Fatalf("InsertMedia: %v", err)
	}
	cfg, err := p.loadVMConfig(filepath.Join(p.dataDir, "web"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.DiskDrivers) != 2 || cfg.DiskDrivers[1] != "ahci-cd" {
		t.Errorf("expected an appended ahci-cd device, got %v", cfg.DiskDrivers)
	}
	if cfg.DiskPaths[1] != iso {
		t.Errorf("cd path = %q, want %q", cfg.DiskPaths[1], iso)
	}
}

func TestInsertMediaMissingFile(t *testing.T) {
	p := configVMProvider(t, &vmConfig{Name: "web"})
	err := p.InsertMedia(context.Background(), provider.InstanceHandle{ID: "web"}, provider.MediaSpec{
		Type: provider.MediaTypeCDROM,
		Path: "/no/such/file.iso",
	})
	if err == nil {
		t.Error("expected error for a missing media file")
	}
}

func TestEjectAndListMedia(t *testing.T) {
	// Arrange: VM with one data disk and one CD-ROM at index 1.
	p := configVMProvider(t, &vmConfig{
		Name:        "web",
		DiskPaths:   []string{"/dev/zvol/pool/web/disk0", "/iso/live.iso"},
		DiskDrivers: []string{"virtio-blk", "ahci-cd"},
		BootOrder:   []int{1},
	})

	// List: one CD-ROM, inserted and bootable.
	media, err := p.ListMedia(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("ListMedia: %v", err)
	}
	if len(media) != 1 || media[0].DeviceID != "ahci-cd-1" || !media[0].Inserted || !media[0].Bootable {
		t.Fatalf("unexpected media list: %+v", media)
	}

	// Eject the CD-ROM.
	if err := p.EjectMedia(context.Background(), provider.InstanceHandle{ID: "web"}, "ahci-cd-1"); err != nil {
		t.Fatalf("EjectMedia: %v", err)
	}
	cfg, err := p.loadVMConfig(filepath.Join(p.dataDir, "web"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.DiskPaths) != 1 || len(cfg.DiskDrivers) != 1 {
		t.Errorf("CD-ROM not removed: paths=%v drivers=%v", cfg.DiskPaths, cfg.DiskDrivers)
	}
}

func TestEjectMediaNotFound(t *testing.T) {
	p := configVMProvider(t, &vmConfig{
		Name:        "web",
		DiskPaths:   []string{"/dev/zvol/pool/web/disk0"},
		DiskDrivers: []string{"virtio-blk"},
	})
	if err := p.EjectMedia(context.Background(), provider.InstanceHandle{ID: "web"}, "ahci-cd-9"); err == nil {
		t.Error("expected error when ejecting a non-existent device")
	}
}

func TestSetAndGetBootOrder(t *testing.T) {
	// Arrange: index 0 is a disk, index 1 is a CD-ROM.
	p := configVMProvider(t, &vmConfig{
		Name:        "web",
		DiskPaths:   []string{"/dev/zvol/pool/web/disk0", "/iso/live.iso"},
		DiskDrivers: []string{"virtio-blk", "ahci-cd"},
	})

	// Act: boot CD-ROM first, then disk.
	err := p.SetBootOrder(context.Background(), provider.InstanceHandle{ID: "web"}, provider.BootOrder{
		Devices: []provider.BootDevice{provider.BootDeviceCDROM, provider.BootDeviceHardDisk},
	})
	if err != nil {
		t.Fatalf("SetBootOrder: %v", err)
	}

	// Assert: GetBootOrder round-trips the device types.
	order, err := p.GetBootOrder(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("GetBootOrder: %v", err)
	}
	if len(order.Devices) != 2 || order.Devices[0] != provider.BootDeviceCDROM || order.Devices[1] != provider.BootDeviceHardDisk {
		t.Errorf("boot order = %v, want [cdrom disk]", order.Devices)
	}
}

func TestGetTrafficStatsParsesNetstat(t *testing.T) {
	// Arrange: one tap device; netstat reports byte/packet counters.
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte(netstatSample), nil
	}}
	dir := t.TempDir()
	p := &BhyveProvider{dataDir: dir, stateDir: dir, runner: fake}
	vmDir := filepath.Join(dir, "web")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := p.saveVMConfig(vmDir, &vmConfig{Name: "web", TapDevs: []string{"tap0"}}); err != nil {
		t.Fatal(err)
	}

	// Act
	stats, err := p.GetTrafficStats(context.Background(), provider.InstanceHandle{ID: "web"})
	// Assert
	if err != nil {
		t.Fatalf("GetTrafficStats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 stats entry, got %d", len(stats))
	}
	s := stats[0]
	if s.InterfaceName != "tap0" || s.RxBytes != 500000 || s.TxBytes != 400000 {
		t.Errorf("stats = %+v, want tap0 rx=500000 tx=400000", s)
	}
	// The netstat invocation must target the tap device.
	if got := strings.Join(fake.Calls[0].Args, " "); got != "-I tap0 -b -n" {
		t.Errorf("netstat args = %q", got)
	}
}

func TestResetTrafficStatsRefusesRatherThanLying(t *testing.T) {
	p := &BhyveProvider{runner: &execx.Fake{}}
	// The kernel counters cannot be reset and no baseline is persisted, so
	// answering nil told the caller a reset had happened and left it reading
	// cumulative counters as post-reset values.
	if err := p.ResetTrafficStats(context.Background(), provider.InstanceHandle{ID: "web"}); err == nil {
		t.Error("ResetTrafficStats reported success for an operation it does not perform")
	}
}
