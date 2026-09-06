package qemu

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestQEMUProviderMetadata(t *testing.T) {
	p := NewQEMUProvider()
	metadata := p.Metadata()

	if metadata.Name != "qemu" {
		t.Errorf("Expected name 'qemu', got '%s'", metadata.Name)
	}

	if metadata.Type != provider.ProviderTypeVM {
		t.Errorf("Expected type 'vm', got '%s'", metadata.Type)
	}

	if metadata.Version == "" {
		t.Error("Version should not be empty")
	}
}

func TestQEMUProviderCapabilities(t *testing.T) {
	p := NewQEMUProvider()

	// Need to initialize to detect KVM
	ctx := context.Background()
	config := provider.ProviderConfig{
		// t.TempDir rather than a fixed path: "/tmp/hospitus-test" is shared
		// between every test in this file, between runs, and between users on
		// the same host — a directory another user owns makes Initialize fail
		// for reasons that have nothing to do with the test.
		DataDir:  filepath.Join(t.TempDir(), "data"),
		StateDir: filepath.Join(t.TempDir(), "state"),
	}
	_ = p.Initialize(ctx, config)

	caps := p.Capabilities()

	if !caps.SupportsSnapshots {
		t.Error("QEMU provider should support snapshots")
	}

	// Migration is advertised as unsupported: the package contains no migration
	// code at all, and claiming it made the capability a promise nothing kept.
	if caps.SupportsMigration {
		t.Error("QEMU provider must not advertise migration it does not implement")
	}

	if caps.SupportsLiveMigration {
		t.Error("QEMU provider must not advertise live migration it does not implement")
	}

	if !caps.SupportsVNC {
		t.Error("QEMU provider should support VNC")
	}

	if !caps.SupportsCrossArch {
		t.Error("QEMU provider should support cross-architecture emulation")
	}

	// Check network types
	foundBridge := false
	foundNAT := false
	for _, nt := range caps.NetworkTypes {
		if nt == provider.NetworkTypeBridge {
			foundBridge = true
		}
		if nt == provider.NetworkTypeNAT {
			foundNAT = true
		}
	}
	if !foundBridge {
		t.Error("QEMU provider should support bridge networking")
	}
	if !foundNAT {
		t.Error("QEMU provider should support NAT networking")
	}

	// Check disk types
	foundQCOW2 := false
	foundRaw := false
	for _, dt := range caps.DiskTypes {
		if dt == provider.DiskTypeQCOW2 {
			foundQCOW2 = true
		}
		if dt == provider.DiskTypeRaw {
			foundRaw = true
		}
	}
	if !foundQCOW2 {
		t.Error("QEMU provider should support QCOW2 disks")
	}
	if !foundRaw {
		t.Error("QEMU provider should support RAW disks")
	}

	// Check platform features
	if caps.PlatformFeatures["qmp"] != true {
		t.Error("QEMU provider should support QMP")
	}

	// Check KVM availability on Linux
	if runtime.GOOS == "linux" {
		if caps.PlatformFeatures["kvm"] == nil {
			t.Error("KVM availability should be detected on Linux")
		}
	}
}

func TestQEMUProviderInitialize(t *testing.T) {
	p := NewQEMUProvider()
	ctx := context.Background()

	config := provider.ProviderConfig{
		DataDir:  filepath.Join(t.TempDir(), "data"),
		StateDir: filepath.Join(t.TempDir(), "state"),
	}

	err := p.Initialize(ctx, config)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Initialize adds "qemu" subdirectory to avoid conflicts with other providers
	expectedDataDir := filepath.Join(config.DataDir, "qemu")
	if p.dataDir != expectedDataDir {
		t.Errorf("Expected dataDir '%s', got '%s'", expectedDataDir, p.dataDir)
	}

	if p.qemuBinaries == nil {
		t.Error("QEMU binaries map should be initialized")
	}

	// Check if at least one architecture binary was found
	foundAny := false
	for arch, binary := range p.qemuBinaries {
		if binary != "" {
			foundAny = true
			t.Logf("Found QEMU binary for %s: %s", arch, binary)
		}
	}

	if !foundAny {
		t.Log("Warning: No QEMU binaries found (this is expected if QEMU is not installed)")
	}

	// Check KVM detection on Linux
	if runtime.GOOS == "linux" {
		t.Logf("KVM available: %v", p.hasKVM)
	}
}

func TestQEMUProviderHealthCheck(t *testing.T) {
	p := NewQEMUProvider()
	ctx := context.Background()

	config := provider.ProviderConfig{
		DataDir:  filepath.Join(t.TempDir(), "data"),
		StateDir: filepath.Join(t.TempDir(), "state"),
	}

	if err := p.Initialize(ctx, config); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	err := p.HealthCheck(ctx)
	if err != nil {
		t.Logf("Health check failed (expected if QEMU is not installed): %v", err)
		// This is expected on systems without QEMU
		return
	}

	t.Log("Health check passed - QEMU is available")
}

func TestBuildQEMUConfig(t *testing.T) {
	p := NewQEMUProvider()
	p.hasKVM = false // Simulate non-Linux or no KVM

	spec := provider.InstanceSpec{
		Name:     "test-vm",
		CPUs:     2,
		MemoryMB: 2048,
		Disks: []provider.DiskSpec{
			{
				SizeGB: 20,
				Type:   provider.DiskTypeQCOW2,
			},
		},
		Networks: []provider.NetworkSpec{
			{
				Type: provider.NetworkTypeNAT,
			},
		},
	}

	qemuBin := "qemu-system-x86_64"
	diskPath := "/tmp/disk.qcow2"
	vmDir := t.TempDir()

	config := p.buildQEMUConfig(spec, qemuBin, diskPath, "", vmDir, "amd64")

	if config.Name != "test-vm" {
		t.Errorf("Expected name 'test-vm', got '%s'", config.Name)
	}

	if config.QEMUBin != qemuBin {
		t.Errorf("Expected qemu bin '%s', got '%s'", qemuBin, config.QEMUBin)
	}

	// Check that basic arguments are present
	foundName := false
	foundMemory := false
	foundCPU := false
	foundDisk := false

	argsStr := ""
	for _, arg := range config.Args {
		argsStr += arg + " "
	}

	for i, arg := range config.Args {
		if arg == "-name" && i+1 < len(config.Args) && config.Args[i+1] == "test-vm" {
			foundName = true
		}
		if arg == "-m" && i+1 < len(config.Args) && config.Args[i+1] == "2048" {
			foundMemory = true
		}
		if arg == "-smp" {
			foundCPU = true
		}
		if arg == "-drive" {
			foundDisk = true
		}
	}

	if !foundName {
		t.Errorf("Config args missing -name: %s", argsStr)
	}
	if !foundMemory {
		t.Errorf("Config args missing -m: %s", argsStr)
	}
	if !foundCPU {
		t.Errorf("Config args missing -smp: %s", argsStr)
	}
	if !foundDisk {
		t.Errorf("Config args missing -drive: %s", argsStr)
	}

	// Check that KVM is not enabled (since we set hasKVM = false)
	for _, arg := range config.Args {
		if arg == "-enable-kvm" {
			t.Error("KVM should not be enabled when hasKVM is false")
		}
	}
}

func TestBuildQEMUConfigWithKVM(t *testing.T) {
	p := NewQEMUProvider()
	p.hasKVM = true // Simulate Linux with KVM

	spec := provider.InstanceSpec{
		Name:     "test-vm-kvm",
		CPUs:     4,
		MemoryMB: 4096,
	}

	// KVM only accelerates a guest of the host's own architecture, so the guest
	// arch has to be the native one for the accelerator to be requested.
	config := p.buildQEMUConfig(spec, "qemu-system-x86_64", "", "", t.TempDir(), runtime.GOARCH)

	// Check that KVM is enabled
	foundKVM := false
	foundHostCPU := false

	for i, arg := range config.Args {
		if arg == "-enable-kvm" {
			foundKVM = true
		}
		if arg == "-cpu" && i+1 < len(config.Args) && config.Args[i+1] == "host" {
			foundHostCPU = true
		}
	}

	if !foundKVM {
		t.Error("KVM should be enabled when hasKVM is true")
	}
	if !foundHostCPU {
		t.Error("CPU should be 'host' when KVM is enabled")
	}
}

// TestBuildQEMUConfigCrossArchFallsBackToEmulation pins the rule that broke every
// amd64 cloud image on an Apple Silicon host: hardware accelerators run guest
// instructions on the host CPU, so a foreign architecture must use TCG. Asking
// for the accelerator anyway makes QEMU refuse to start.
func TestBuildQEMUConfigCrossArchFallsBackToEmulation(t *testing.T) {
	foreign := "arm64"
	if runtime.GOARCH == "arm64" {
		foreign = "amd64"
	}

	tests := []struct {
		name  string
		setup func(*QEMUProvider)
		accel string
	}{
		{"KVM host", func(p *QEMUProvider) { p.hasKVM = true }, "-enable-kvm"},
		{"NVMM host", func(p *QEMUProvider) { p.hasNVMM = true }, "nvmm"},
		{"HVF host", func(p *QEMUProvider) { p.hasHVF = true }, "hvf"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewQEMUProvider()
			tt.setup(p)

			spec := provider.InstanceSpec{Name: "cross", CPUs: 1, MemoryMB: 512}
			config := p.buildQEMUConfig(spec, "qemu-system-"+foreign, "", "", t.TempDir(), foreign)

			foundTCG := false
			for i, arg := range config.Args {
				if arg == tt.accel {
					t.Errorf("%s requested for a %s guest on a %s host", tt.accel, foreign, runtime.GOARCH)
				}
				if arg == "-accel" && i+1 < len(config.Args) && config.Args[i+1] == "tcg" {
					foundTCG = true
				}
				if arg == "-cpu" && i+1 < len(config.Args) && config.Args[i+1] == "host" {
					t.Error("-cpu host is meaningless without hardware acceleration")
				}
			}
			if !foundTCG {
				t.Errorf("expected TCG emulation for a %s guest, got %v", foreign, config.Args)
			}
		})
	}
}

// TestEmulatedCPUModel checks that the fallback model belongs to the target
// architecture: qemu64 is an x86 model and means nothing to qemu-system-aarch64.
func TestEmulatedCPUModel(t *testing.T) {
	tests := map[string]string{
		"amd64":   "qemu64",
		"i386":    "qemu64",
		"arm64":   "cortex-a72",
		"riscv64": "rv64",
	}
	for arch, want := range tests {
		if got := emulatedCPUModel(arch); got != want {
			t.Errorf("emulatedCPUModel(%q) = %q, want %q", arch, got, want)
		}
	}
}

func TestFindBinary(t *testing.T) {
	p := NewQEMUProvider()

	// Test with a binary that should exist on most systems
	binary := p.findBinary("ls")
	if binary == "" {
		t.Error("Failed to find 'ls' binary")
	}

	// Test with a binary that shouldn't exist
	binary = p.findBinary("nonexistent-binary-12345")
	if binary != "" {
		t.Error("Should not find nonexistent binary")
	}
}

// Integration tests (require QEMU and root/privileges)

func TestQEMUProviderIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	p := NewQEMUProvider()
	ctx := context.Background()

	// Initialize
	root := shortTempDir(t)
	config := provider.ProviderConfig{
		DataDir:  filepath.Join(root, "d"),
		StateDir: filepath.Join(root, "s"),
	}

	if err := p.Initialize(ctx, config); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	if err := p.HealthCheck(ctx); err != nil {
		t.Skipf("QEMU not available, skipping integration test: %v", err)
	}

	// Create instance
	spec := provider.InstanceSpec{
		Name:     "test-vm-integration",
		CPUs:     1,
		MemoryMB: 512,
		Disks: []provider.DiskSpec{
			{
				SizeGB: 1,
				Type:   provider.DiskTypeQCOW2,
			},
		},
		Networks: []provider.NetworkSpec{
			{
				Type: provider.NetworkTypeNAT,
			},
		},
	}

	handle, err := p.CreateInstance(ctx, spec)
	if err != nil {
		t.Fatalf("CreateInstance failed: %v", err)
	}

	t.Logf("Created VM: %s", handle.ID)

	// Clean up
	defer func() {
		if err := p.DeleteInstance(ctx, handle, false); err != nil {
			t.Errorf("DeleteInstance failed: %v", err)
		}
	}()

	// Get state (should be stopped)
	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		t.Fatalf("GetInstanceState failed: %v", err)
	}

	if state != provider.StateStopped {
		t.Errorf("Expected state 'stopped', got '%s'", state)
	}

	handles, err := p.ListInstances(ctx, provider.InstanceFilter{})
	if err != nil {
		t.Fatalf("ListInstances failed: %v", err)
	}

	found := false
	for _, h := range handles {
		if h.ID == handle.ID {
			found = true
			break
		}
	}

	if !found {
		t.Error("Created VM not found in list")
	}

	// Note: We don't start the VM in this test because:
	// 1. It requires a bootable disk image
	// 2. It would need to run as daemon
	// 3. Integration tests should be fast

	t.Log("Integration test passed")
}

func TestIsPathAllowed(t *testing.T) {
	p := NewQEMUProvider()
	p.dataDir = "/var/lib/hospitus/qemu"
	p.imageDir = "/var/lib/hospitus/images"

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"under dataDir", "/var/lib/hospitus/qemu/myvm/disk.qcow2", true},
		{"under imageDir", "/var/lib/hospitus/images/ubuntu.iso", true},
		{"exact dataDir", "/var/lib/hospitus/qemu", true},
		{"exact imageDir", "/var/lib/hospitus/images", true},
		{"outside both", "/etc/passwd", false},
		{"root", "/", false},
		{"partial prefix match", "/var/lib/hospitus/qemu-other/file", false},
		{"parent traversal", "/var/lib/hospitus/qemu/../../../etc/passwd", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.isPathAllowed(tt.path)
			if got != tt.want {
				t.Errorf("isPathAllowed(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestReadPID(t *testing.T) {
	p := NewQEMUProvider()
	tmpDir := t.TempDir()
	p.stateDir = tmpDir

	t.Run("valid PID file", func(t *testing.T) {
		writeFixture(t, filepath.Join(tmpDir, "test-vm.pid"), []byte("12345\n"), 0o644)
		pid, err := p.readPID("test-vm")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pid != 12345 {
			t.Errorf("expected pid 12345, got %d", pid)
		}
	})

	t.Run("missing PID file", func(t *testing.T) {
		_, err := p.readPID("nonexistent")
		if err == nil {
			t.Error("expected error for missing PID file")
		}
	})

	t.Run("invalid PID content", func(t *testing.T) {
		writeFixture(t, filepath.Join(tmpDir, "bad-vm.pid"), []byte("notanumber\n"), 0o644)
		_, err := p.readPID("bad-vm")
		if err == nil {
			t.Error("expected error for invalid PID")
		}
	})

	t.Run("zero PID", func(t *testing.T) {
		writeFixture(t, filepath.Join(tmpDir, "zero-vm.pid"), []byte("0\n"), 0o644)
		_, err := p.readPID("zero-vm")
		if err == nil {
			t.Error("expected error for zero PID")
		}
	})

	t.Run("negative PID", func(t *testing.T) {
		writeFixture(t, filepath.Join(tmpDir, "neg-vm.pid"), []byte("-1\n"), 0o644)
		_, err := p.readPID("neg-vm")
		if err == nil {
			t.Error("expected error for negative PID")
		}
	})
}

func TestRebuildArgsWithMedia(t *testing.T) {
	p := NewQEMUProvider()

	t.Run("insert into existing CDROM", func(t *testing.T) {
		args := []string{"-m", "2048", "-drive", "file=old.iso,format=raw,media=cdrom,readonly=on", "-smp", "2"}
		result := p.rebuildArgsWithMedia(args, "/new.iso", true)

		foundNew := false
		for _, arg := range result {
			if arg == "file=/new.iso,format=raw,media=cdrom,readonly=on" {
				foundNew = true
			}
			if arg == "file=old.iso,format=raw,media=cdrom,readonly=on" {
				t.Error("old ISO should be replaced")
			}
		}
		if !foundNew {
			t.Error("new ISO not found in rebuilt args")
		}
	})

	t.Run("insert without existing CDROM", func(t *testing.T) {
		args := []string{"-m", "2048", "-smp", "2"}
		result := p.rebuildArgsWithMedia(args, "/new.iso", true)

		foundDrive := false
		foundBoot := false
		for i, arg := range result {
			if arg == "-drive" && i+1 < len(result) && result[i+1] == "file=/new.iso,format=raw,media=cdrom,readonly=on" {
				foundDrive = true
			}
			if arg == "-boot" {
				foundBoot = true
			}
		}
		if !foundDrive {
			t.Error("CDROM drive should be added")
		}
		if !foundBoot {
			t.Error("boot flag should be added")
		}
	})

	t.Run("eject existing CDROM", func(t *testing.T) {
		// A boot disk alongside the CD-ROM: with only the CD-ROM present, an
		// implementation that stripped every -drive — the boot disk included —
		// passed this test.
		bootDisk := "file=/vm/disk0.qcow2,format=qcow2,if=virtio"
		args := []string{
			"-m", "2048",
			"-drive", bootDisk,
			"-drive", "file=old.iso,format=raw,media=cdrom,readonly=on",
			"-boot", "order=dc",
		}
		result := p.rebuildArgsWithMedia(args, "", false)

		for i, arg := range result {
			if arg == "-drive" && i+1 < len(result) && strings.Contains(result[i+1], "media=cdrom") {
				t.Errorf("the CD-ROM drive should be removed, found: %s", result[i+1])
			}
		}
		if !slices.Contains(result, bootDisk) {
			t.Errorf("the boot disk should survive an eject: %v", result)
		}
		// The cdrom letter goes, the rest of the order stays.
		for i, arg := range result {
			if arg == "-boot" && i+1 < len(result) && result[i+1] != "order=c" {
				t.Errorf("boot order = %q, want order=c", result[i+1])
			}
		}
	})
}

func TestRebuildArgsWithBootOrder(t *testing.T) {
	p := NewQEMUProvider()

	t.Run("replace existing boot order", func(t *testing.T) {
		args := []string{"-m", "2048", "-boot", "order=d", "-smp", "2"}
		result := p.rebuildArgsWithBootOrder(args, "cd")

		foundNewBoot := false
		for i, arg := range result {
			if arg == "-boot" && i+1 < len(result) && result[i+1] == "order=cd" {
				foundNewBoot = true
			}
			if arg == "order=d" {
				t.Error("old boot order should be replaced")
			}
		}
		if !foundNewBoot {
			t.Error("new boot order not found")
		}
	})

	t.Run("add boot order when none exists", func(t *testing.T) {
		args := []string{"-m", "2048", "-smp", "2"}
		result := p.rebuildArgsWithBootOrder(args, "c")

		found := false
		for i, arg := range result {
			if arg == "-boot" && i+1 < len(result) && result[i+1] == "order=c" {
				found = true
			}
		}
		if !found {
			t.Error("boot order should be appended")
		}
	})
}

// shortTempDir returns a unique directory whose path is short enough for a UNIX
// socket.
//
// t.TempDir builds its name from the test's, and on macOS it already sits under
// "/var/folders/<...>": the QMP and serial sockets underneath it then exceed the
// 104-byte sun_path limit, which is what CreateInstance refuses. A fixed shared
// path avoids the length but collides between runs and between users.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hq")
	if err != nil {
		t.Fatalf("creating a short temporary directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestDefaultMachineType pins the arm64 half of the regression the comment in
// qemu.go records: defaulting to q35 made every arm64 guest fail to start,
// because that machine exists only on x86.
func TestDefaultMachineType(t *testing.T) {
	for arch, want := range map[string]string{
		"amd64":   "q35",
		"i386":    "q35",
		"arm64":   "virt",
		"riscv64": "virt",
		"":        "virt",
	} {
		if got := defaultMachineType(arch); got != want {
			t.Errorf("defaultMachineType(%q) = %q, want %q", arch, got, want)
		}
	}
}

// writeFixture writes a test file and fails the test if it cannot.
//
// A discarded write error made the negative subtests below pass for the wrong
// reason: readPID then reported a missing file rather than the malformed or
// empty content each case is named for.
func writeFixture(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
