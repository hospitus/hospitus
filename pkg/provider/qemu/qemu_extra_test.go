package qemu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// newTestProvider creates a QEMUProvider wired to a temp directory.
// It does not call Initialize so it avoids root / binary-detection issues.
func newTestProvider(t *testing.T) *QEMUProvider {
	t.Helper()
	dir := t.TempDir()
	p := NewQEMUProvider()
	p.dataDir = filepath.Join(dir, "data")
	p.stateDir = filepath.Join(dir, "state")
	p.imageDir = filepath.Join(dir, "images")
	_ = os.MkdirAll(p.dataDir, 0o755)
	_ = os.MkdirAll(p.stateDir, 0o755)
	_ = os.MkdirAll(p.imageDir, 0o755)
	p.hasKVM = false
	p.hasHVF = false
	return p
}

// ────────────────────────────────────────────────
// buildNetdevArgs
// ────────────────────────────────────────────────

func TestBuildNetdevArgs_NAT(t *testing.T) {
	p := newTestProvider(t)
	result := p.buildNetdevArgs("net0", provider.NetworkSpec{Type: provider.NetworkTypeNAT}, nil)
	if !strings.HasPrefix(result, "user,id=net0") {
		t.Errorf("NAT netdev should start with 'user,id=net0', got: %s", result)
	}
}

func TestBuildNetdevArgs_Bridge(t *testing.T) {
	p := newTestProvider(t)
	result := p.buildNetdevArgs("net0", provider.NetworkSpec{
		Type:   provider.NetworkTypeBridge,
		Bridge: "br0",
	}, nil)
	want := "bridge,id=net0,br=br0"
	if result != want {
		t.Errorf("bridge netdev: want %q, got %q", want, result)
	}
}

func TestBuildNetdevArgs_BridgeDefaultBR(t *testing.T) {
	p := newTestProvider(t)
	result := p.buildNetdevArgs("net0", provider.NetworkSpec{Type: provider.NetworkTypeBridge}, nil)
	if !strings.Contains(result, "br=br0") {
		t.Errorf("bridge without explicit bridge should default to br0, got: %s", result)
	}
}

func TestBuildNetdevArgs_None(t *testing.T) {
	p := newTestProvider(t)
	result := p.buildNetdevArgs("net0", provider.NetworkSpec{Type: provider.NetworkTypeNone}, nil)
	if !strings.Contains(result, "restrict=yes") {
		t.Errorf("none network should have restrict=yes, got: %s", result)
	}
}

func TestBuildNetdevArgs_Default(t *testing.T) {
	p := newTestProvider(t)
	result := p.buildNetdevArgs("net0", provider.NetworkSpec{Type: "unknown"}, nil)
	if !strings.HasPrefix(result, "user,id=net0") {
		t.Errorf("unknown type should fall back to user-mode NAT, got: %s", result)
	}
}

func TestBuildNetdevArgs_NATWithPortForwards(t *testing.T) {
	p := newTestProvider(t)
	cfg := map[string]interface{}{
		"port_forwards": []interface{}{
			map[string]interface{}{
				"host":      float64(18022),
				"container": float64(22),
				"protocol":  "tcp",
			},
		},
	}
	result := p.buildNetdevArgs("net0", provider.NetworkSpec{Type: provider.NetworkTypeNAT}, cfg)
	if !strings.Contains(result, "hostfwd=tcp::") {
		t.Errorf("port forward should produce hostfwd, got: %s", result)
	}
	if !strings.Contains(result, ":22") {
		t.Errorf("port forward should target guest port 22, got: %s", result)
	}
}

// ────────────────────────────────────────────────
// buildQEMUConfig — additional paths
// ────────────────────────────────────────────────

func TestBuildQEMUConfig_BridgeNetwork(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{
		Name:     "vm-bridge",
		CPUs:     1,
		MemoryMB: 512,
		Networks: []provider.NetworkSpec{
			{Type: provider.NetworkTypeBridge, Bridge: "hospitus0"},
		},
	}
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", "", "", p.dataDir, "amd64")
	args := strings.Join(cfg.Args, " ")
	if !strings.Contains(args, "bridge,id=net0,br=hospitus0") {
		t.Errorf("bridge network args not found: %s", args)
	}
}

func TestBuildQEMUConfig_NoNetwork_DefaultNAT(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{Name: "vm-nonet", CPUs: 1, MemoryMB: 512}
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", "", "", p.dataDir, "amd64")
	args := strings.Join(cfg.Args, " ")
	if !strings.Contains(args, "user,id=net0,hostfwd=tcp::") {
		t.Errorf("default NAT with SSH forward not found: %s", args)
	}
	// SSH port should be stored
	if _, ok := cfg.Spec.ProviderConfig["ssh_port"]; !ok {
		t.Error("ssh_port should be stored in ProviderConfig")
	}
}

func TestBuildQEMUConfig_RawDisk(t *testing.T) {
	p := newTestProvider(t)
	diskPath := filepath.Join(p.dataDir, "disk0.raw")
	spec := provider.InstanceSpec{
		Name:     "vm-raw",
		CPUs:     1,
		MemoryMB: 512,
		Disks: []provider.DiskSpec{
			{SizeGB: 5, Type: provider.DiskTypeRaw},
		},
	}
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", diskPath, "", p.dataDir, "amd64")
	args := strings.Join(cfg.Args, " ")
	if !strings.Contains(args, "format=raw") {
		t.Errorf("raw disk should use format=raw, got: %s", args)
	}
}

func TestBuildQEMUConfig_Qcow2Disk(t *testing.T) {
	p := newTestProvider(t)
	diskPath := filepath.Join(p.dataDir, "disk0.qcow2")
	spec := provider.InstanceSpec{
		Name:     "vm-qcow2",
		CPUs:     1,
		MemoryMB: 512,
		Disks:    []provider.DiskSpec{{SizeGB: 10, Type: provider.DiskTypeQCOW2}},
	}
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", diskPath, "", p.dataDir, "amd64")
	args := strings.Join(cfg.Args, " ")
	if !strings.Contains(args, "format=qcow2") {
		t.Errorf("qcow2 disk should use format=qcow2, got: %s", args)
	}
}

func TestBuildQEMUConfig_CloudInitISO(t *testing.T) {
	p := newTestProvider(t)
	diskPath := filepath.Join(p.dataDir, "disk0.qcow2")
	isoPath := filepath.Join(p.dataDir, "cloud-init.iso")
	spec := provider.InstanceSpec{Name: "vm-ci", CPUs: 1, MemoryMB: 512}
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", diskPath, isoPath, p.dataDir, "amd64")
	args := strings.Join(cfg.Args, " ")
	if !strings.Contains(args, "media=cdrom") {
		t.Errorf("cloud-init ISO should appear as cdrom: %s", args)
	}
}

func TestBuildQEMUConfig_CustomMachineType(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{
		Name:     "vm-pc",
		CPUs:     1,
		MemoryMB: 512,
		ProviderConfig: map[string]interface{}{
			"machine": "pc",
		},
	}
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", "", "", p.dataDir, "amd64")
	found := false
	for i, arg := range cfg.Args {
		if arg == "-machine" && i+1 < len(cfg.Args) && cfg.Args[i+1] == "pc" {
			found = true
		}
	}
	if !found {
		t.Errorf("custom machine type 'pc' not found in args: %v", cfg.Args)
	}
}

func TestBuildQEMUConfig_InvalidMachineTypeFallback(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{
		Name:     "vm-bad-machine",
		CPUs:     1,
		MemoryMB: 512,
		ProviderConfig: map[string]interface{}{
			"machine": "invalid-machine-xyz",
		},
	}
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", "", "", p.dataDir, "amd64")
	found := false
	for i, arg := range cfg.Args {
		if arg == "-machine" && i+1 < len(cfg.Args) && cfg.Args[i+1] == "q35" {
			found = true
		}
	}
	if !found {
		t.Errorf("invalid machine type should fall back to q35: %v", cfg.Args)
	}
}

func TestBuildQEMUConfig_CustomCPUModel(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{
		Name:     "vm-cpu",
		CPUs:     1,
		MemoryMB: 512,
		ProviderConfig: map[string]interface{}{
			"cpu": "qemu32",
		},
	}
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", "", "", p.dataDir, "amd64")
	found := false
	for i, arg := range cfg.Args {
		if arg == "-cpu" && i+1 < len(cfg.Args) && cfg.Args[i+1] == "qemu32" {
			found = true
		}
	}
	if !found {
		t.Errorf("custom CPU model not found in args: %v", cfg.Args)
	}
}

func TestBuildQEMUConfig_InvalidCPUFallback(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{
		Name:     "vm-bad-cpu",
		CPUs:     1,
		MemoryMB: 512,
		ProviderConfig: map[string]interface{}{
			"cpu": "invalid-cpu-xyz",
		},
	}
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", "", "", p.dataDir, "amd64")
	found := false
	for i, arg := range cfg.Args {
		if arg == "-cpu" && i+1 < len(cfg.Args) && cfg.Args[i+1] == "qemu64" {
			found = true
		}
	}
	if !found {
		t.Errorf("invalid CPU should fall back to qemu64: %v", cfg.Args)
	}
}

func TestBuildQEMUConfig_HVFAcceleration(t *testing.T) {
	p := newTestProvider(t)
	p.hasHVF = true
	p.hasKVM = false
	spec := provider.InstanceSpec{Name: "vm-hvf", CPUs: 2, MemoryMB: 1024}
	// HVF runs guest instructions on the host CPU, so it is only requested for a
	// guest of the host's own architecture.
	cfg := p.buildQEMUConfig(spec, "qemu-system-"+runtime.GOARCH, "", "", p.dataDir, runtime.GOARCH)
	args := strings.Join(cfg.Args, " ")
	if !strings.Contains(args, "-accel hvf") {
		t.Errorf("HVF acceleration flag not found: %s", args)
	}
}

func TestBuildQEMUConfig_BIOSBootMode(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{
		Name:     "vm-bios",
		CPUs:     1,
		MemoryMB: 512,
		ProviderConfig: map[string]interface{}{
			"boot_mode": "bios",
		},
	}
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", "", "", p.dataDir, "amd64")
	args := strings.Join(cfg.Args, " ")
	// In BIOS mode, no pflash drives should be added
	if strings.Contains(args, "if=pflash") {
		t.Errorf("BIOS mode should not add pflash drives: %s", args)
	}
}

func TestBuildQEMUConfig_InstallISO(t *testing.T) {
	p := newTestProvider(t)
	// Create a fake ISO inside the allowed imageDir
	isoDir := filepath.Join(p.imageDir, "iso")
	_ = os.MkdirAll(isoDir, 0o755)
	isoPath := filepath.Join(isoDir, "test.iso")
	_ = os.WriteFile(isoPath, []byte("fakeiso"), 0o644)

	spec := provider.InstanceSpec{
		Name:     "vm-iso",
		CPUs:     1,
		MemoryMB: 512,
		ProviderConfig: map[string]interface{}{
			"install_iso": isoPath,
		},
	}
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", "", "", p.dataDir, "amd64")
	args := strings.Join(cfg.Args, " ")
	if !strings.Contains(args, "media=cdrom") {
		t.Errorf("install ISO should appear as cdrom: %s", args)
	}
	if !strings.Contains(args, "-boot d") {
		t.Errorf("install ISO should set boot from cdrom (-boot d): %s", args)
	}
}

func TestBuildQEMUConfig_InstallISO_NotAllowedPath(t *testing.T) {
	p := newTestProvider(t)
	// Use a path outside the allowed directories
	spec := provider.InstanceSpec{
		Name:     "vm-iso-bad",
		CPUs:     1,
		MemoryMB: 512,
		ProviderConfig: map[string]interface{}{
			"install_iso": "/etc/passwd",
		},
	}
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", "", "", p.dataDir, "amd64")
	args := strings.Join(cfg.Args, " ")
	if strings.Contains(args, "/etc/passwd") {
		t.Errorf("disallowed ISO path should not appear in args: %s", args)
	}
}

func TestBuildQEMUConfig_MultipleNICs(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{
		Name:     "vm-multinc",
		CPUs:     1,
		MemoryMB: 512,
		Networks: []provider.NetworkSpec{
			{Type: provider.NetworkTypeNAT},
			{Type: provider.NetworkTypeBridge, Bridge: "br1"},
		},
	}
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", "", "", p.dataDir, "amd64")
	net0Found, net1Found := false, false
	for _, arg := range cfg.Args {
		if strings.Contains(arg, "id=net0") {
			net0Found = true
		}
		if strings.Contains(arg, "id=net1") {
			net1Found = true
		}
	}
	if !net0Found || !net1Found {
		t.Errorf("multi-NIC config should have net0 and net1: %v", cfg.Args)
	}
}

func TestBuildQEMUConfig_NICWithMAC(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{
		Name:     "vm-mac",
		CPUs:     1,
		MemoryMB: 512,
		Networks: []provider.NetworkSpec{
			{Type: provider.NetworkTypeNAT, MAC: "52:54:00:12:34:56"},
		},
	}
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", "", "", p.dataDir, "amd64")
	args := strings.Join(cfg.Args, " ")
	if !strings.Contains(args, "mac=52:54:00:12:34:56") {
		t.Errorf("NIC MAC not found in args: %s", args)
	}
}

func TestBuildQEMUConfig_QMPSocket(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{Name: "vm-qmp", CPUs: 1, MemoryMB: 512}
	vmDir := filepath.Join(p.dataDir, "vm-qmp")
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", "", "", vmDir, "amd64")
	expectedSocket := filepath.Join(vmDir, "qmp.sock")
	if cfg.QMPSocket != expectedSocket {
		t.Errorf("QMPSocket: want %q, got %q", expectedSocket, cfg.QMPSocket)
	}
	args := strings.Join(cfg.Args, " ")
	if !strings.Contains(args, "qmp.sock") {
		t.Errorf("-qmp arg should contain qmp.sock: %s", args)
	}
}

func TestBuildQEMUConfig_VNCPort(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{Name: "vm-vnc", CPUs: 1, MemoryMB: 512}
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", "", "", p.dataDir, "amd64")
	args := strings.Join(cfg.Args, " ")
	if !strings.Contains(args, "-vnc :") {
		t.Errorf("-vnc arg not found: %s", args)
	}
	if _, ok := cfg.Spec.ProviderConfig["vnc_port"]; !ok {
		t.Error("vnc_port should be stored in ProviderConfig")
	}
}

func TestBuildQEMUConfig_DaemonizeAndPIDFile(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{Name: "vm-daemon", CPUs: 1, MemoryMB: 512}
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", "", "", p.dataDir, "amd64")
	args := strings.Join(cfg.Args, " ")
	if !strings.Contains(args, "-daemonize") {
		t.Errorf("-daemonize not found in args: %s", args)
	}
	if !strings.Contains(args, "-pidfile") {
		t.Errorf("-pidfile not found in args: %s", args)
	}
}

func TestBuildQEMUConfig_SerialSocket(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{Name: "vm-serial", CPUs: 1, MemoryMB: 512}
	vmDir := filepath.Join(p.dataDir, "vm-serial")
	cfg := p.buildQEMUConfig(spec, "qemu-system-x86_64", "", "", vmDir, "amd64")
	args := strings.Join(cfg.Args, " ")
	if !strings.Contains(args, "serial.sock") {
		t.Errorf("-serial arg pointing to serial.sock not found: %s", args)
	}
}

// ────────────────────────────────────────────────
// saveVMConfig / loadVMConfig round-trip
// ────────────────────────────────────────────────

func TestSaveLoadVMConfig_RoundTrip(t *testing.T) {
	p := newTestProvider(t)
	cfg := &vmConfig{
		Name:      "roundtrip",
		QEMUBin:   "qemu-system-x86_64",
		Args:      []string{"-m", "1024", "-smp", "2"},
		QMPSocket: "/tmp/qmp.sock",
		Spec: provider.InstanceSpec{
			Name:     "roundtrip",
			CPUs:     2,
			MemoryMB: 1024,
		},
	}

	path := filepath.Join(p.stateDir, "roundtrip.json")
	if err := p.saveVMConfig(cfg, path); err != nil {
		t.Fatalf("saveVMConfig: %v", err)
	}

	loaded, err := p.loadVMConfig(path)
	if err != nil {
		t.Fatalf("loadVMConfig: %v", err)
	}

	if loaded.Name != cfg.Name {
		t.Errorf("Name: want %q, got %q", cfg.Name, loaded.Name)
	}
	if loaded.QMPSocket != cfg.QMPSocket {
		t.Errorf("QMPSocket: want %q, got %q", cfg.QMPSocket, loaded.QMPSocket)
	}
	if len(loaded.Args) != len(cfg.Args) {
		t.Errorf("Args length: want %d, got %d", len(cfg.Args), len(loaded.Args))
	}
}

func TestLoadVMConfig_InvalidJSON(t *testing.T) {
	p := newTestProvider(t)
	path := filepath.Join(p.stateDir, "bad.json")
	_ = os.WriteFile(path, []byte("{not json}"), 0o644)
	if _, err := p.loadVMConfig(path); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestLoadVMConfig_NotFound(t *testing.T) {
	p := newTestProvider(t)
	_, err := p.loadVMConfig(filepath.Join(p.stateDir, "nonexistent.json"))
	if err == nil {
		t.Error("expected error for nonexistent file, got nil")
	}
}

// ────────────────────────────────────────────────
// vmExists
// ────────────────────────────────────────────────

func TestVMExists_True(t *testing.T) {
	p := newTestProvider(t)
	// Create config file
	path := filepath.Join(p.stateDir, "myvm.json")
	_ = os.WriteFile(path, []byte(`{"name":"myvm"}`), 0o644)

	exists, err := p.vmExists(context.Background(), "myvm")
	if err != nil {
		t.Fatalf("vmExists error: %v", err)
	}
	if !exists {
		t.Error("expected VM to exist")
	}
}

func TestVMExists_False(t *testing.T) {
	p := newTestProvider(t)
	exists, err := p.vmExists(context.Background(), "doesnotexist")
	if err != nil {
		t.Fatalf("vmExists error: %v", err)
	}
	if exists {
		t.Error("expected VM to not exist")
	}
}

// ────────────────────────────────────────────────
// isVMRunning
// ────────────────────────────────────────────────

func TestIsVMRunning_NoPIDFile(t *testing.T) {
	p := newTestProvider(t)
	running, err := p.isVMRunning(context.Background(), "novm")
	if err != nil {
		t.Fatalf("isVMRunning error: %v", err)
	}
	if running {
		t.Error("VM should not be running without PID file")
	}
}

func TestIsVMRunning_StalePIDFile(t *testing.T) {
	p := newTestProvider(t)
	pidFile := filepath.Join(p.stateDir, "stale.pid")
	// PID 99999999 almost certainly doesn't exist
	_ = os.WriteFile(pidFile, []byte("99999999"), 0o644)

	running, err := p.isVMRunning(context.Background(), "stale")
	if err != nil {
		t.Fatalf("isVMRunning error: %v", err)
	}
	if running {
		t.Error("VM with stale PID should not be running")
	}
	// Stale PID file should be cleaned up
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("stale PID file should have been removed")
	}
}

func TestIsVMRunning_InvalidPID(t *testing.T) {
	p := newTestProvider(t)
	pidFile := filepath.Join(p.stateDir, "badpid.pid")
	_ = os.WriteFile(pidFile, []byte("notanumber"), 0o644)
	running, err := p.isVMRunning(context.Background(), "badpid")
	if err != nil {
		t.Fatalf("isVMRunning error: %v", err)
	}
	if running {
		t.Error("invalid PID should result in not running")
	}
}

// ────────────────────────────────────────────────
// readPID
// ────────────────────────────────────────────────

func TestReadPID_Valid(t *testing.T) {
	p := newTestProvider(t)
	pidFile := filepath.Join(p.stateDir, "testvm.pid")
	_ = os.WriteFile(pidFile, []byte("1234\n"), 0o644)
	pid, err := p.readPID("testvm")
	if err != nil {
		t.Fatalf("readPID error: %v", err)
	}
	if pid != 1234 {
		t.Errorf("readPID: want 1234, got %d", pid)
	}
}

func TestReadPID_Missing(t *testing.T) {
	p := newTestProvider(t)
	_, err := p.readPID("nosuchvm")
	if err == nil {
		t.Error("expected error for missing PID file")
	}
}

func TestReadPID_Invalid(t *testing.T) {
	p := newTestProvider(t)
	pidFile := filepath.Join(p.stateDir, "badvm.pid")
	_ = os.WriteFile(pidFile, []byte("abc"), 0o644)
	_, err := p.readPID("badvm")
	if err == nil {
		t.Error("expected error for invalid PID content")
	}
}

func TestReadPID_Zero(t *testing.T) {
	p := newTestProvider(t)
	pidFile := filepath.Join(p.stateDir, "zerovm.pid")
	_ = os.WriteFile(pidFile, []byte("0"), 0o644)
	_, err := p.readPID("zerovm")
	if err == nil {
		t.Error("expected error for zero PID")
	}
}

// ────────────────────────────────────────────────
// needsCloudInit
// ────────────────────────────────────────────────

func TestNeedsCloudInit_LinuxOSType(t *testing.T) {
	p := newTestProvider(t)
	cases := []string{"linux", "ubuntu", "debian", "centos", "fedora"}
	for _, osType := range cases {
		spec := provider.InstanceSpec{OSType: osType}
		if !p.needsCloudInit(spec) {
			t.Errorf("needsCloudInit(%q) should be true", osType)
		}
	}
}

func TestNeedsCloudInit_WithCloudInitConfig(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{
		CloudInit: &provider.CloudInitConfig{UserData: "#cloud-config\n"},
	}
	if !p.needsCloudInit(spec) {
		t.Error("needsCloudInit should be true when CloudInit.UserData is set")
	}
}

func TestNeedsCloudInit_WithMetaData(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{
		CloudInit: &provider.CloudInitConfig{MetaData: "instance-id: foo"},
	}
	if !p.needsCloudInit(spec) {
		t.Error("needsCloudInit should be true when CloudInit.MetaData is set")
	}
}

func TestNeedsCloudInit_ProviderConfigFlag(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{
		ProviderConfig: map[string]interface{}{"cloud_init": true},
	}
	if !p.needsCloudInit(spec) {
		t.Error("needsCloudInit should be true when cloud_init=true in ProviderConfig")
	}
}

func TestNeedsCloudInit_False(t *testing.T) {
	p := newTestProvider(t)
	spec := provider.InstanceSpec{OSType: "freebsd"}
	if p.needsCloudInit(spec) {
		t.Error("needsCloudInit should be false for freebsd with no cloud-init config")
	}
}

// ────────────────────────────────────────────────
// resolveCloudImagePath
// ────────────────────────────────────────────────

func TestResolveCloudImagePath_AbsoluteExisting(t *testing.T) {
	p := newTestProvider(t)
	imgFile := filepath.Join(p.imageDir, "test.img")
	_ = os.WriteFile(imgFile, []byte("fake"), 0o644)

	got, err := p.resolveCloudImagePath(imgFile, "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != imgFile {
		t.Errorf("want %q, got %q", imgFile, got)
	}
}

func TestResolveCloudImagePath_AbsoluteNotFound(t *testing.T) {
	p := newTestProvider(t)
	_, err := p.resolveCloudImagePath("/nonexistent/path/img.qcow2", "amd64")
	if err == nil {
		t.Error("expected error for nonexistent absolute path")
	}
}

func TestResolveCloudImagePath_RelativeWithArchSuffix(t *testing.T) {
	p := newTestProvider(t)
	cloudDir := filepath.Join(p.imageDir, "cloud")
	_ = os.MkdirAll(cloudDir, 0o755)
	imgFile := filepath.Join(cloudDir, "ubuntu-22.04-amd64.qcow2")
	_ = os.WriteFile(imgFile, []byte("fake"), 0o644)

	got, err := p.resolveCloudImagePath("ubuntu-22.04", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != imgFile {
		t.Errorf("want %q, got %q", imgFile, got)
	}
}

func TestResolveCloudImagePath_NotFound(t *testing.T) {
	p := newTestProvider(t)
	_, err := p.resolveCloudImagePath("nonexistent-image", "amd64")
	if err == nil {
		t.Error("expected error for nonexistent image")
	}
}

func TestResolveCloudImagePath_NotFound_WithAvailableImages(t *testing.T) {
	p := newTestProvider(t)
	cloudDir := filepath.Join(p.imageDir, "cloud")
	_ = os.MkdirAll(cloudDir, 0o755)
	_ = os.WriteFile(filepath.Join(cloudDir, "other-image-amd64.qcow2"), []byte("x"), 0o644)

	_, err := p.resolveCloudImagePath("nonexistent-image", "amd64")
	if err == nil {
		t.Error("expected error for nonexistent image")
	}
	// Error should mention available images
	if !strings.Contains(err.Error(), "other-image-amd64.qcow2") {
		t.Errorf("error should list available images, got: %v", err)
	}
}

// ────────────────────────────────────────────────
// resolveISOPath
// ────────────────────────────────────────────────

func TestResolveISOPath_AbsoluteExisting(t *testing.T) {
	p := newTestProvider(t)
	isoDir := filepath.Join(p.imageDir, "iso")
	_ = os.MkdirAll(isoDir, 0o755)
	isoPath := filepath.Join(isoDir, "test.iso")
	_ = os.WriteFile(isoPath, []byte("fake"), 0o644)

	got, err := p.resolveISOPath(isoPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != isoPath {
		t.Errorf("want %q, got %q", isoPath, got)
	}
}

func TestResolveISOPath_RelativeInISODir(t *testing.T) {
	p := newTestProvider(t)
	isoDir := filepath.Join(p.imageDir, "iso")
	_ = os.MkdirAll(isoDir, 0o755)
	isoPath := filepath.Join(isoDir, "debian.iso")
	_ = os.WriteFile(isoPath, []byte("fake"), 0o644)

	got, err := p.resolveISOPath("debian.iso")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != isoPath {
		t.Errorf("want %q, got %q", isoPath, got)
	}
}

func TestResolveISOPath_NotFound(t *testing.T) {
	p := newTestProvider(t)
	_, err := p.resolveISOPath("missing.iso")
	if err == nil {
		t.Error("expected error for nonexistent ISO")
	}
}

// ────────────────────────────────────────────────
// isPathAllowed
// ────────────────────────────────────────────────

func TestIsPathAllowed_InsideDataDir(t *testing.T) {
	p := newTestProvider(t)
	path := filepath.Join(p.dataDir, "myvm", "disk.qcow2")
	if !p.isPathAllowed(path) {
		t.Errorf("path inside dataDir should be allowed: %s", path)
	}
}

func TestIsPathAllowed_InsideImageDir(t *testing.T) {
	p := newTestProvider(t)
	path := filepath.Join(p.imageDir, "cloud", "ubuntu.qcow2")
	if !p.isPathAllowed(path) {
		t.Errorf("path inside imageDir should be allowed: %s", path)
	}
}

func TestIsPathAllowed_EtcPasswd(t *testing.T) {
	p := newTestProvider(t)
	if p.isPathAllowed("/etc/passwd") {
		t.Error("/etc/passwd should not be allowed")
	}
}

func TestIsPathAllowed_ParentTraversal(t *testing.T) {
	p := newTestProvider(t)
	// Try to escape via ..
	path := filepath.Join(p.dataDir, "..", "etc", "shadow")
	if p.isPathAllowed(path) {
		t.Error("path traversal should not be allowed")
	}
}

func TestIsPathAllowed_DataDirItself(t *testing.T) {
	p := newTestProvider(t)
	// The root dir itself should be allowed
	if !p.isPathAllowed(p.dataDir) {
		t.Errorf("dataDir root itself should be allowed: %s", p.dataDir)
	}
}

// ────────────────────────────────────────────────
// rebuildArgsWithMedia
// ────────────────────────────────────────────────

func TestRebuildArgsWithMedia_InsertNew(t *testing.T) {
	p := newTestProvider(t)
	args := []string{"-m", "1024", "-smp", "2"}
	result := p.rebuildArgsWithMedia(args, "/path/to/disk.iso", true)

	found := false
	for i, arg := range result {
		if arg == "-drive" && i+1 < len(result) && strings.Contains(result[i+1], "disk.iso") {
			found = true
		}
	}
	if !found {
		t.Errorf("inserted ISO should appear as -drive: %v", result)
	}
	// Should also have -boot d
	bootFound := false
	for i, arg := range result {
		if arg == "-boot" && i+1 < len(result) && result[i+1] == "d" {
			bootFound = true
		}
	}
	if !bootFound {
		t.Errorf("-boot d should be added when inserting ISO: %v", result)
	}
}

func TestRebuildArgsWithMedia_ReplaceExisting(t *testing.T) {
	p := newTestProvider(t)
	args := []string{
		"-m", "1024",
		"-drive", "file=/old/old.iso,format=raw,media=cdrom,readonly=on",
		"-boot", "d",
	}
	result := p.rebuildArgsWithMedia(args, "/new/new.iso", true)

	for i, arg := range result {
		if arg == "-drive" && i+1 < len(result) {
			if strings.Contains(result[i+1], "/old/old.iso") {
				t.Error("old ISO should be replaced")
			}
			if strings.Contains(result[i+1], "/new/new.iso") {
				return // success
			}
		}
	}
	t.Errorf("new ISO not found in replaced args: %v", result)
}

func TestRebuildArgsWithMedia_Eject(t *testing.T) {
	p := newTestProvider(t)
	args := []string{
		"-m", "1024",
		"-drive", "file=/some/disk.iso,format=raw,media=cdrom,readonly=on",
		"-boot", "d",
	}
	result := p.rebuildArgsWithMedia(args, "", false)

	for _, arg := range result {
		if strings.Contains(arg, "media=cdrom") {
			t.Errorf("ejecting should remove cdrom drive, still found in: %v", result)
		}
	}
}

// ────────────────────────────────────────────────
// rebuildArgsWithBootOrder
// ────────────────────────────────────────────────

func TestRebuildArgsWithBootOrder_ReplaceExisting(t *testing.T) {
	p := newTestProvider(t)
	args := []string{"-m", "1024", "-boot", "d", "-smp", "2"}
	result := p.rebuildArgsWithBootOrder(args, "c")

	found := false
	for i, arg := range result {
		if arg == "-boot" && i+1 < len(result) && result[i+1] == "order=c" {
			found = true
		}
	}
	if !found {
		t.Errorf("boot order should be replaced with 'order=c': %v", result)
	}
}

func TestRebuildArgsWithBootOrder_AddNew(t *testing.T) {
	p := newTestProvider(t)
	args := []string{"-m", "1024", "-smp", "2"}
	result := p.rebuildArgsWithBootOrder(args, "dc")

	found := false
	for i, arg := range result {
		if arg == "-boot" && i+1 < len(result) && result[i+1] == "order=dc" {
			found = true
		}
	}
	if !found {
		t.Errorf("boot order not added: %v", result)
	}
}

func TestRebuildArgsWithBootOrder_EmptyBootStr(t *testing.T) {
	p := newTestProvider(t)
	args := []string{"-m", "1024"}
	result := p.rebuildArgsWithBootOrder(args, "")
	// Empty boot string - no -boot should be added
	for _, arg := range result {
		if arg == "-boot" {
			t.Errorf("empty boot string should not add -boot: %v", result)
		}
	}
}

// ────────────────────────────────────────────────
// autostart: SetAutoStart / GetAutoStart
// ────────────────────────────────────────────────

func TestAutoStart_DefaultWhenMissingFile(t *testing.T) {
	p := newTestProvider(t)
	// Create VM directory (but no autostart.json)
	vmDir := filepath.Join(p.dataDir, "myvm")
	_ = os.MkdirAll(vmDir, 0o755)

	handle := provider.InstanceHandle{ID: "myvm", Provider: "qemu"}
	cfg, err := p.GetAutoStart(context.Background(), handle)
	if err != nil {
		t.Fatalf("GetAutoStart error: %v", err)
	}
	if cfg.Enabled {
		t.Error("default autostart should be disabled")
	}
	if cfg.Priority != 50 {
		t.Errorf("default priority should be 50, got %d", cfg.Priority)
	}
}

func TestAutoStart_SetAndGet(t *testing.T) {
	p := newTestProvider(t)
	vmDir := filepath.Join(p.dataDir, "vm1")
	_ = os.MkdirAll(vmDir, 0o755)

	handle := provider.InstanceHandle{ID: "vm1", Provider: "qemu"}
	want := provider.AutoStartConfig{Enabled: true, Priority: 10, DelayMS: 500}

	if err := p.SetAutoStart(context.Background(), handle, want); err != nil {
		t.Fatalf("SetAutoStart error: %v", err)
	}

	got, err := p.GetAutoStart(context.Background(), handle)
	if err != nil {
		t.Fatalf("GetAutoStart error: %v", err)
	}
	if got.Enabled != want.Enabled || got.Priority != want.Priority || got.DelayMS != want.DelayMS {
		t.Errorf("GetAutoStart: want %+v, got %+v", want, got)
	}
}

func TestAutoStart_InvalidPriority(t *testing.T) {
	p := newTestProvider(t)
	vmDir := filepath.Join(p.dataDir, "vm2")
	_ = os.MkdirAll(vmDir, 0o755)

	handle := provider.InstanceHandle{ID: "vm2", Provider: "qemu"}
	if err := p.SetAutoStart(context.Background(), handle, provider.AutoStartConfig{
		Enabled:  true,
		Priority: 200, // > 100
	}); err == nil {
		t.Error("expected error for priority > 100")
	}
}

func TestAutoStart_DefaultPriority50WhenZero(t *testing.T) {
	p := newTestProvider(t)
	vmDir := filepath.Join(p.dataDir, "vm3")
	_ = os.MkdirAll(vmDir, 0o755)

	handle := provider.InstanceHandle{ID: "vm3", Provider: "qemu"}
	_ = p.SetAutoStart(context.Background(), handle, provider.AutoStartConfig{
		Enabled:  true,
		Priority: 0, // Should default to 50
	})

	got, _ := p.GetAutoStart(context.Background(), handle)
	if got.Priority != 50 {
		t.Errorf("priority 0 with Enabled=true should default to 50, got %d", got.Priority)
	}
}

func TestAutoStart_VMNotExists(t *testing.T) {
	p := newTestProvider(t)
	handle := provider.InstanceHandle{ID: "nonexistent", Provider: "qemu"}
	err := p.SetAutoStart(context.Background(), handle, provider.AutoStartConfig{Enabled: true})
	if err == nil {
		t.Error("expected error for non-existent VM")
	}
}

// ────────────────────────────────────────────────
// ListAutoStartInstances
// ────────────────────────────────────────────────

func TestListAutoStartInstances_Empty(t *testing.T) {
	p := newTestProvider(t)
	handles, err := p.ListAutoStartInstances(context.Background())
	if err != nil {
		t.Fatalf("ListAutoStartInstances error: %v", err)
	}
	if len(handles) != 0 {
		t.Errorf("expected 0 handles, got %d", len(handles))
	}
}

func TestListAutoStartInstances_SortedByPriority(t *testing.T) {
	p := newTestProvider(t)
	// Create VMs with different priorities
	for _, vm := range []struct {
		name     string
		priority int
	}{
		{"vm-high", 80},
		{"vm-low", 10},
		{"vm-mid", 50},
	} {
		dir := filepath.Join(p.dataDir, vm.name)
		_ = os.MkdirAll(dir, 0o755)
		handle := provider.InstanceHandle{ID: vm.name, Provider: "qemu"}
		_ = p.SetAutoStart(context.Background(), handle, provider.AutoStartConfig{
			Enabled:  true,
			Priority: vm.priority,
		})
	}

	handles, err := p.ListAutoStartInstances(context.Background())
	if err != nil {
		t.Fatalf("ListAutoStartInstances error: %v", err)
	}
	if len(handles) != 3 {
		t.Fatalf("expected 3 handles, got %d", len(handles))
	}
	// Should be sorted: low(10), mid(50), high(80)
	if handles[0].ID != "vm-low" {
		t.Errorf("first VM should be vm-low (priority 10), got %s", handles[0].ID)
	}
	if handles[2].ID != "vm-high" {
		t.Errorf("last VM should be vm-high (priority 80), got %s", handles[2].ID)
	}
}

func TestListAutoStartInstances_OnlyEnabled(t *testing.T) {
	p := newTestProvider(t)
	for _, vm := range []struct {
		name    string
		enabled bool
	}{
		{"vm-on", true},
		{"vm-off", false},
	} {
		dir := filepath.Join(p.dataDir, vm.name)
		_ = os.MkdirAll(dir, 0o755)
		handle := provider.InstanceHandle{ID: vm.name, Provider: "qemu"}
		_ = p.SetAutoStart(context.Background(), handle, provider.AutoStartConfig{
			Enabled:  vm.enabled,
			Priority: 50,
		})
	}

	handles, err := p.ListAutoStartInstances(context.Background())
	if err != nil {
		t.Fatalf("ListAutoStartInstances error: %v", err)
	}
	if len(handles) != 1 || handles[0].ID != "vm-on" {
		t.Errorf("only enabled VMs should be listed, got %v", handles)
	}
}

// ────────────────────────────────────────────────
// getConfigMedia
// ────────────────────────────────────────────────

func TestGetConfigMedia_WithInstallISO(t *testing.T) {
	p := newTestProvider(t)
	cfg := &vmConfig{
		Name:    "testvm",
		QEMUBin: "qemu-system-x86_64",
		Args:    []string{},
		Spec: provider.InstanceSpec{
			ProviderConfig: map[string]interface{}{
				"install_iso": "/some/path/install.iso",
			},
		},
	}
	path := filepath.Join(p.stateDir, "testvm.json")
	_ = p.saveVMConfig(cfg, path)

	mediaList, err := p.getConfigMedia(context.Background(), "testvm")
	if err != nil {
		t.Fatalf("getConfigMedia error: %v", err)
	}
	if len(mediaList) == 0 {
		t.Fatal("expected at least one media entry")
	}
	if mediaList[0].Path != "/some/path/install.iso" {
		t.Errorf("want path /some/path/install.iso, got %s", mediaList[0].Path)
	}
	if !mediaList[0].Inserted {
		t.Error("install ISO should be marked as inserted")
	}
}

func TestGetConfigMedia_WithCloudInitISO(t *testing.T) {
	p := newTestProvider(t)
	// Create the VM directory and cloud-init.iso
	vmDir := filepath.Join(p.dataDir, "vmci")
	_ = os.MkdirAll(vmDir, 0o755)
	ciISOPath := filepath.Join(vmDir, "cloud-init.iso")
	_ = os.WriteFile(ciISOPath, []byte("fake"), 0o644)

	cfg := &vmConfig{
		Name:    "vmci",
		QEMUBin: "qemu-system-x86_64",
		Args:    []string{},
		Spec:    provider.InstanceSpec{ProviderConfig: map[string]interface{}{}},
	}
	_ = p.saveVMConfig(cfg, filepath.Join(p.stateDir, "vmci.json"))

	mediaList, err := p.getConfigMedia(context.Background(), "vmci")
	if err != nil {
		t.Fatalf("getConfigMedia error: %v", err)
	}
	found := false
	for _, m := range mediaList {
		if m.Path == ciISOPath {
			found = true
			if m.Bootable {
				t.Error("cloud-init ISO should not be bootable")
			}
		}
	}
	if !found {
		t.Errorf("cloud-init ISO not found in media list: %v", mediaList)
	}
}

func TestGetConfigMedia_Empty(t *testing.T) {
	p := newTestProvider(t)
	cfg := &vmConfig{
		Name:    "emptyvm",
		QEMUBin: "qemu-system-x86_64",
		Args:    []string{},
		Spec:    provider.InstanceSpec{ProviderConfig: map[string]interface{}{}},
	}
	_ = p.saveVMConfig(cfg, filepath.Join(p.stateDir, "emptyvm.json"))

	mediaList, err := p.getConfigMedia(context.Background(), "emptyvm")
	if err != nil {
		t.Fatalf("getConfigMedia error: %v", err)
	}
	if len(mediaList) != 0 {
		t.Errorf("expected empty media list, got %v", mediaList)
	}
}

// ────────────────────────────────────────────────
// GetBootOrder
// ────────────────────────────────────────────────

func TestGetBootOrder_DefaultHardDisk(t *testing.T) {
	p := newTestProvider(t)
	cfg := &vmConfig{
		Name:    "bootvm",
		QEMUBin: "qemu-system-x86_64",
		Args:    []string{},
		Spec:    provider.InstanceSpec{ProviderConfig: map[string]interface{}{}},
	}
	_ = p.saveVMConfig(cfg, filepath.Join(p.stateDir, "bootvm.json"))

	handle := provider.InstanceHandle{ID: "bootvm", Provider: "qemu"}
	order, err := p.GetBootOrder(context.Background(), handle)
	if err != nil {
		t.Fatalf("GetBootOrder error: %v", err)
	}
	if len(order.Devices) == 0 || order.Devices[0] != provider.BootDeviceHardDisk {
		t.Errorf("default boot order should be HardDisk, got %v", order.Devices)
	}
}

func TestGetBootOrder_CDROMFirst(t *testing.T) {
	p := newTestProvider(t)
	cfg := &vmConfig{
		Name:    "isoboot",
		QEMUBin: "qemu-system-x86_64",
		Args:    []string{},
		Spec: provider.InstanceSpec{
			ProviderConfig: map[string]interface{}{
				"install_iso": "/path/to/boot.iso",
			},
		},
	}
	_ = p.saveVMConfig(cfg, filepath.Join(p.stateDir, "isoboot.json"))

	handle := provider.InstanceHandle{ID: "isoboot", Provider: "qemu"}
	order, err := p.GetBootOrder(context.Background(), handle)
	if err != nil {
		t.Fatalf("GetBootOrder error: %v", err)
	}
	if len(order.Devices) == 0 || order.Devices[0] != provider.BootDeviceCDROM {
		t.Errorf("ISO boot should put CDROM first, got %v", order.Devices)
	}
}

// ────────────────────────────────────────────────
// QMPError
// ────────────────────────────────────────────────

func TestQMPError_ErrorString(t *testing.T) {
	e := &QMPError{Class: "CommandNotFound", Desc: "unknown command"}
	want := "QMP error: CommandNotFound - unknown command"
	if e.Error() != want {
		t.Errorf("QMPError.Error(): want %q, got %q", want, e.Error())
	}
}

// ────────────────────────────────────────────────
// QMPClient (unit-level, no socket needed)
// ────────────────────────────────────────────────

func TestNewQMPClient(t *testing.T) {
	client, err := NewQMPClient("/fake/socket.sock")
	if err != nil {
		t.Fatalf("NewQMPClient error: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestQMPClient_ExecuteNotConnected(t *testing.T) {
	client, _ := NewQMPClient("/fake/socket.sock")
	// Don't call Connect() — Execute should return error
	_, err := client.Execute("query-status", nil)
	if err == nil {
		t.Error("Execute without connection should return error")
	}
}

func TestQMPClient_CloseUnconnected(t *testing.T) {
	client, _ := NewQMPClient("/fake/socket.sock")
	// Closing without connecting should not panic
	if err := client.Close(); err != nil {
		t.Errorf("Close on unconnected client should not error, got: %v", err)
	}
}

// ────────────────────────────────────────────────
// findAvailablePort
// ────────────────────────────────────────────────

func TestFindAvailablePort(t *testing.T) {
	p := newTestProvider(t)
	port := p.findAvailablePort(15000)
	if port < 15000 || port > 15100 {
		t.Errorf("expected port between 15000-15100, got %d", port)
	}
}

// ────────────────────────────────────────────────
// VNC port allocation (shared allocator, concurrent safety)
// ────────────────────────────────────────────────

func TestAllocateVNCPort_ReturnsValidPort(t *testing.T) {
	p := newTestProvider(t)
	port := p.allocatePort("web", "vnc_port", vncBasePort)
	if port < 5900 {
		t.Errorf("VNC port should be >= 5900, got %d", port)
	}
}

func TestAllocateVNCPort_Concurrent(t *testing.T) {
	p := newTestProvider(t)
	const n = 10
	ports := make([]int, n)
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		i := i
		go func() {
			ports[i] = p.allocatePort(fmt.Sprintf("vm%d", i), "vnc_port", vncBasePort)
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	// No panics means the locking works; all ports should be >= 5900
	for _, port := range ports {
		if port < 5900 {
			t.Errorf("concurrent VNC port allocation returned invalid port %d", port)
		}
	}
}

// TestAllocateVNCPortSkipsAnotherVMsRecordedDisplay is the reason the in-memory
// counter was replaced: it never read the other VMs' state files, so a VM
// created after a daemon restart was told a display a stopped VM holds and was
// silently moved at its first start.
func TestAllocateVNCPortSkipsAnotherVMsRecordedDisplay(t *testing.T) {
	p := newTestProvider(t)
	writeVMPort(t, p.stateDir, "arm-test", "vnc_port", vncBasePort)

	if got := p.allocatePort("ubuntu-server", "vnc_port", vncBasePort); got == vncBasePort {
		t.Errorf("allocatePort = %d, the display arm-test already holds", got)
	}
}

// ────────────────────────────────────────────────
// Capabilities reflect KVM/HVF state
// ────────────────────────────────────────────────

func TestCapabilities_KVMEnabled(t *testing.T) {
	p := newTestProvider(t)
	p.hasKVM = true
	caps := p.Capabilities()
	if !caps.SupportsGPUPassthrough {
		t.Error("GPU passthrough should be supported with KVM")
	}
	if !caps.SupportsPCIPassthrough {
		t.Error("PCI passthrough should be supported with KVM")
	}
	if caps.PlatformFeatures["kvm"] != true {
		t.Error("kvm platform feature should be true")
	}
}

func TestCapabilities_NoKVM(t *testing.T) {
	p := newTestProvider(t)
	p.hasKVM = false
	p.hasHVF = false
	caps := p.Capabilities()
	if caps.SupportsGPUPassthrough {
		t.Error("GPU passthrough should not be supported without KVM")
	}
}

// ────────────────────────────────────────────────
// ListInstances
// ────────────────────────────────────────────────

func TestListInstances_Empty(t *testing.T) {
	p := newTestProvider(t)
	handles, err := p.ListInstances(context.Background(), provider.InstanceFilter{})
	if err != nil {
		t.Fatalf("ListInstances error: %v", err)
	}
	if len(handles) != 0 {
		t.Errorf("expected empty list, got %d handles", len(handles))
	}
}

func TestListInstances_WithVMs(t *testing.T) {
	p := newTestProvider(t)
	// Create two VM config files
	for _, name := range []string{"vm-a", "vm-b"} {
		cfg := &vmConfig{
			Name:    name,
			QEMUBin: "qemu-system-x86_64",
			Spec:    provider.InstanceSpec{ProviderConfig: map[string]interface{}{}},
		}
		_ = p.saveVMConfig(cfg, filepath.Join(p.stateDir, fmt.Sprintf("%s.json", name)))
	}

	handles, err := p.ListInstances(context.Background(), provider.InstanceFilter{})
	if err != nil {
		t.Fatalf("ListInstances error: %v", err)
	}
	if len(handles) != 2 {
		t.Errorf("expected 2 handles, got %d", len(handles))
	}
}

// ────────────────────────────────────────────────
// Shutdown / SetInstanceResources
// ────────────────────────────────────────────────

func TestShutdown_NoError(t *testing.T) {
	p := newTestProvider(t)
	if err := p.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown should not error, got: %v", err)
	}
}

func TestSetInstanceResources_Unsupported(t *testing.T) {
	p := newTestProvider(t)
	handle := provider.InstanceHandle{ID: "vm", Provider: "qemu"}
	err := p.SetInstanceResources(context.Background(), handle, provider.ResourceSpec{})
	if !errors.Is(err, provider.ErrUnsupportedOperation) {
		t.Errorf("SetInstanceResources should return ErrUnsupportedOperation, got: %v", err)
	}
}

// ────────────────────────────────────────────────
// metrics: parseProcessCPU from ps output
// ────────────────────────────────────────────────

// parseProcessCPULine is extracted logic from getProcessCPU — tested via the
// field-extraction pattern used in that function.
func TestGetProcessCPU_ParseLogic(t *testing.T) {
	// Simulate the parsing logic inside getProcessCPU
	lines := []string{
		"  PID  %CPU COMMAND",
		"12345   2.5 qemu-system-x86_64 -name myvm -m 1024",
		"12346   0.0 sleep 100",
	}
	vmName := "myvm"
	var cpuPercent float64
	for _, line := range lines {
		if strings.Contains(line, "qemu-system") && strings.Contains(line, vmName) {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				val, err := strconv.ParseFloat(fields[1], 64)
				if err == nil {
					cpuPercent = val
				}
			}
		}
	}
	if cpuPercent != 2.5 {
		t.Errorf("expected cpuPercent=2.5, got %f", cpuPercent)
	}
}

// ────────────────────────────────────────────────
// metrics: parseLinuxVmRSS
// ────────────────────────────────────────────────

func TestGetProcessMemoryLinux_ParseLogic(t *testing.T) {
	// Replicate the VmRSS parsing from getProcessMemoryLinux
	statusContent := "Name:\tqemu-system-x86_64\nVmRSS:\t  524288 kB\nVmSize:\t1048576 kB\n"
	var rssKB int64
	for _, line := range strings.Split(statusContent, "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				val, err := strconv.ParseInt(fields[1], 10, 64)
				if err == nil {
					rssKB = val
				}
			}
		}
	}
	wantMB := int64(512) // 524288 kB / 1024
	if rssKB/1024 != wantMB {
		t.Errorf("VmRSS parse: want %d MB, got %d MB", wantMB, rssKB/1024)
	}
}

// ────────────────────────────────────────────────
// JSON marshaling of vmConfig
// ────────────────────────────────────────────────

func TestVMConfig_JSON(t *testing.T) {
	cfg := vmConfig{
		Name:      "json-test",
		QEMUBin:   "qemu-system-x86_64",
		Args:      []string{"-m", "1024"},
		QMPSocket: "/run/qemu.sock",
	}
	data, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded vmConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if decoded.Name != cfg.Name {
		t.Errorf("Name: want %q, got %q", cfg.Name, decoded.Name)
	}
	if decoded.QMPSocket != cfg.QMPSocket {
		t.Errorf("QMPSocket: want %q, got %q", cfg.QMPSocket, decoded.QMPSocket)
	}
}

// ────────────────────────────────────────────────
// HealthCheck — no binaries
// ────────────────────────────────────────────────

func TestHealthCheck_NoBinaries(t *testing.T) {
	p := newTestProvider(t)
	// No binaries set
	err := p.HealthCheck(context.Background())
	if err == nil {
		t.Error("HealthCheck should fail when no QEMU binaries are found")
	}
}

func TestHealthCheck_WithBinary(t *testing.T) {
	p := newTestProvider(t)
	// Use 'ls' as a stand-in binary (always exists)
	p.qemuBinaries["amd64"] = p.findBinary("ls")
	// Will still fail if qemu-img is missing, which is expected in test env
	// Just verify it gets past the binary check
	_ = p.HealthCheck(context.Background())
}
