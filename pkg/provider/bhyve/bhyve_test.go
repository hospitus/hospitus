package bhyve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestBhyveProviderMetadata(t *testing.T) {
	p := NewBhyveProvider()
	metadata := p.Metadata()

	if metadata.Name != "bhyve" {
		t.Errorf("Expected name 'bhyve', got '%s'", metadata.Name)
	}

	if metadata.Type != provider.ProviderTypeVM {
		t.Errorf("Expected type 'vm', got '%s'", metadata.Type)
	}

	if metadata.Version == "" {
		t.Error("Version should not be empty")
	}
}

func TestBhyveProviderCapabilities(t *testing.T) {
	p := NewBhyveProvider()

	caps := p.Capabilities()

	if !caps.SupportsSnapshots {
		t.Error("bhyve provider should support snapshots (via ZFS)")
	}

	if caps.SupportsMigration {
		t.Error("bhyve provider should not support migration")
	}

	if caps.SupportsLiveMigration {
		t.Error("bhyve provider should not support live migration")
	}

	if !caps.SupportsCloning {
		t.Error("bhyve provider should support clones (via ZFS)")
	}

	if !caps.SupportsVNC {
		t.Error("bhyve provider should support VNC")
	}

	if caps.SupportsCrossArch {
		t.Error("bhyve provider should not support cross-architecture emulation")
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
		t.Error("bhyve provider should support bridge networking")
	}
	if !foundNAT {
		t.Error("bhyve provider should support NAT networking")
	}

	// Check disk types
	foundRaw := false
	foundZVOL := false
	for _, dt := range caps.DiskTypes {
		if dt == provider.DiskTypeRaw {
			foundRaw = true
		}
		if dt == provider.DiskTypeZVOL {
			foundZVOL = true
		}
	}
	if !foundRaw {
		t.Error("bhyve provider should support RAW disks")
	}
	if !foundZVOL {
		t.Error("bhyve provider should support ZVOL disks")
	}
}

func TestBhyveProviderInitializeNonFreeBSD(t *testing.T) {
	if runtime.GOOS == "freebsd" {
		t.Skip("Skipping non-FreeBSD test on FreeBSD")
	}

	p := NewBhyveProvider()
	ctx := context.Background()

	tmpDir := t.TempDir()
	config := provider.ProviderConfig{
		DataDir:  filepath.Join(tmpDir, "data"),
		StateDir: filepath.Join(tmpDir, "state"),
	}

	err := p.Initialize(ctx, config)
	if !errors.Is(err, provider.ErrProviderNotAvailable) {
		t.Errorf("Expected ErrProviderNotAvailable on non-FreeBSD, got: %v", err)
	}
}

func TestBhyveProviderInitializeFreeBSD(t *testing.T) {
	if testing.Short() {
		t.Skip("mutates the host: creates the ZFS parent dataset")
	}
	if runtime.GOOS != "freebsd" {
		t.Skip("Skipping FreeBSD-only test")
	}

	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges: Initialize creates the ZFS parent dataset")
	}

	isolatedZFSParent(t)

	p := NewBhyveProvider()
	ctx := context.Background()

	tmpDir := t.TempDir()
	config := provider.ProviderConfig{
		DataDir:  filepath.Join(tmpDir, "data"),
		StateDir: filepath.Join(tmpDir, "state"),
	}

	err := p.Initialize(ctx, config)
	if err != nil {
		// Skip test if hardware virtualization is not available
		if strings.Contains(err.Error(), "virtualization support") {
			t.Skipf("Skipping: %v", err)
		}
		t.Fatalf("Initialize failed: %v", err)
	}

	if p.dataDir != filepath.Join(config.DataDir, "bhyve") {
		t.Errorf("Expected dataDir '%s', got '%s'", filepath.Join(config.DataDir, "bhyve"), p.dataDir)
	}

	// Check that at least one virtualization technology was detected
	if !p.hasVMX && !p.hasSVM {
		t.Log("Warning: No hardware virtualization detected (this is expected on VMs)")
	}

	t.Logf("Intel VT-x: %v", p.hasVMX)
	t.Logf("AMD-V: %v", p.hasSVM)
	t.Logf("UEFI: %v", p.hasUEFI)
	if p.hasUEFI {
		t.Logf("UEFI path: %s", p.uefiPath)
	}
}

func TestBhyveProviderHealthCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("mutates the host: creates the ZFS parent dataset")
	}
	if runtime.GOOS != "freebsd" {
		t.Skip("Skipping FreeBSD-only test")
	}

	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges: Initialize creates the ZFS parent dataset")
	}

	isolatedZFSParent(t)

	p := NewBhyveProvider()
	ctx := context.Background()

	tmpDir := t.TempDir()
	config := provider.ProviderConfig{
		DataDir:  filepath.Join(tmpDir, "data"),
		StateDir: filepath.Join(tmpDir, "state"),
	}

	if err := p.Initialize(ctx, config); err != nil {
		// Skip test if hardware virtualization is not available
		if strings.Contains(err.Error(), "virtualization support") {
			t.Skipf("Skipping: %v", err)
		}
		t.Fatalf("Initialize failed: %v", err)
	}

	err := p.HealthCheck(ctx)
	if err != nil {
		t.Logf("Health check failed (expected if bhyve not available): %v", err)
		// This is expected on systems without bhyve or hardware virtualization
		return
	}

	t.Log("Health check passed - bhyve is available")
}

func TestBuildBhyveArgs(t *testing.T) {
	p := NewBhyveProvider()
	p.hasUEFI = false // Simulate no UEFI for predictable output

	config := &vmConfig{
		Name:      "test-vm",
		CPUs:      2,
		MemoryMB:  2048,
		DiskPaths: []string{"/tmp/disk0.img", "/tmp/disk1.img"},
		TapDevs:   []string{"tap0", "tap1"},
		Console:   "/tmp/console",
		UEFIBoot:  false,
	}

	args, err := p.buildBhyveArgs(config)
	if err != nil {
		t.Fatalf("buildBhyveArgs: %v", err)
	}

	// Check that basic arguments are present
	foundName := false
	foundMemory := false
	foundCPU := false
	foundDisk := false
	foundNet := false
	foundHostBridge := false

	argsStr := ""
	for _, arg := range args {
		argsStr += arg + " "
	}

	for i, arg := range args {
		if arg == "-m" && i+1 < len(args) && args[i+1] == "2048M" {
			foundMemory = true
		}
		if arg == "-c" && i+1 < len(args) && args[i+1] == "cpus=2,sockets=1,cores=2,threads=1" {
			foundCPU = true
		}
		if arg == "-s" && i+1 < len(args) && args[i+1] == "4:0,virtio-blk,/tmp/disk0.img" {
			foundDisk = true
		}
		if arg == "-s" && i+1 < len(args) && args[i+1] == "6:0,virtio-net,tap0" {
			foundNet = true
		}
		if arg == "-s" && i+1 < len(args) && args[i+1] == "0:0,hostbridge" {
			foundHostBridge = true
		}
		if arg == "test-vm" {
			foundName = true
		}
	}

	if !foundMemory {
		t.Errorf("Config args missing -m 2048M: %s", argsStr)
	}
	if !foundCPU {
		t.Errorf("Config args missing -c cpus=2,sockets=1,cores=2,threads=1: %s", argsStr)
	}
	if !foundDisk {
		t.Errorf("Config args missing disk configuration: %s", argsStr)
	}
	if !foundNet {
		t.Errorf("Config args missing network configuration: %s", argsStr)
	}
	if !foundHostBridge {
		t.Errorf("Config args missing hostbridge: %s", argsStr)
	}
	if !foundName {
		t.Errorf("Config args missing VM name: %s", argsStr)
	}
}

func TestBuildBhyveArgsWithUEFI(t *testing.T) {
	p := NewBhyveProvider()
	p.hasUEFI = true
	p.uefiPath = "/usr/local/share/uefi-firmware/BHYVE_UEFI.fd"

	config := &vmConfig{
		Name:      "test-vm-uefi",
		CPUs:      4,
		MemoryMB:  4096,
		DiskPaths: []string{"/tmp/disk0.img"},
		TapDevs:   []string{},
		Console:   "",
		UEFIBoot:  true,
	}

	args, err := p.buildBhyveArgs(config)
	if err != nil {
		t.Fatalf("buildBhyveArgs: %v", err)
	}

	// Check that UEFI boot is configured
	foundBootrom := false
	foundLPC := false

	for i, arg := range args {
		if arg == "-l" && i+1 < len(args) && args[i+1] == "bootrom,/usr/local/share/uefi-firmware/BHYVE_UEFI.fd" {
			foundBootrom = true
		}
		if arg == "-s" && i+1 < len(args) && args[i+1] == "31,lpc" {
			foundLPC = true
		}
	}

	if !foundBootrom {
		t.Error("UEFI bootrom should be configured when UEFI is enabled")
	}
	if !foundLPC {
		t.Error("LPC device should be configured when UEFI is enabled")
	}
}

func TestCheckCommand(t *testing.T) {
	p := NewBhyveProvider()

	// Test with a command that should exist on most systems
	if !p.checkCommand("ls") {
		t.Error("Failed to find 'ls' command")
	}

	// Test with a command that shouldn't exist
	if p.checkCommand("nonexistent-command-12345") {
		t.Error("Should not find nonexistent command")
	}

	// Test bhyve-specific commands on FreeBSD
	if runtime.GOOS == "freebsd" {
		hasBhyve := p.checkCommand("bhyve")
		t.Logf("bhyve command available: %v", hasBhyve)

		hasBhyvectl := p.checkCommand("bhyvectl")
		t.Logf("bhyvectl command available: %v", hasBhyvectl)
	}
}

func TestVMConfigSaveLoad(t *testing.T) {
	p := NewBhyveProvider()
	vmDir := t.TempDir()
	// The console path lives under the VM directory in this test; point stateDir
	// there so validateVMConfigPaths accepts it (an unset stateDir must not
	// silently allow arbitrary console paths).
	p.stateDir = vmDir

	config := &vmConfig{
		Name:        "test-vm",
		CPUs:        4,
		MemoryMB:    8192,
		DiskPaths:   []string{filepath.Join(vmDir, "disk0.img"), filepath.Join(vmDir, "disk1.img")},
		DiskSectors: []int{512, 4096},
		TapDevs:     []string{"tap0"},
		Console:     filepath.Join(vmDir, "console"),
		UEFIBoot:    true,
		PCISlots: map[string]string{
			"disk0": "4:0",
			"nic0":  "5:0",
		},
		ReadBPS: 1000000,
	}

	if err := p.saveVMConfig(vmDir, config); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	loadedConfig, err := p.loadVMConfig(vmDir)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify
	if loadedConfig.Name != config.Name {
		t.Errorf("Expected name '%s', got '%s'", config.Name, loadedConfig.Name)
	}
	if loadedConfig.CPUs != config.CPUs {
		t.Errorf("Expected %d CPUs, got %d", config.CPUs, loadedConfig.CPUs)
	}
	if loadedConfig.MemoryMB != config.MemoryMB {
		t.Errorf("Expected %d MB memory, got %d", config.MemoryMB, loadedConfig.MemoryMB)
	}
	if loadedConfig.PCISlots["disk0"] != "4:0" {
		t.Errorf("Expected PCISlot disk0=4:0, got %s", loadedConfig.PCISlots["disk0"])
	}
	if loadedConfig.ReadBPS != 1000000 {
		t.Errorf("Expected ReadBPS 1000000, got %d", loadedConfig.ReadBPS)
	}
	if len(loadedConfig.DiskSectors) != 2 || loadedConfig.DiskSectors[1] != 4096 {
		t.Errorf("Expected DiskSectors [512, 4096], got %v", loadedConfig.DiskSectors)
	}
	if len(loadedConfig.DiskPaths) != len(config.DiskPaths) {
		t.Errorf("Expected %d disks, got %d", len(config.DiskPaths), len(loadedConfig.DiskPaths))
	}
	if loadedConfig.UEFIBoot != config.UEFIBoot {
		t.Errorf("Expected UEFIBoot=%v, got %v", config.UEFIBoot, loadedConfig.UEFIBoot)
	}
}

func TestVMStateSaveLoad(t *testing.T) {
	p := NewBhyveProvider()
	vmDir := t.TempDir()

	// Clean up
	defer func() {
		// os.RemoveAll(vmDir) // Commented for debugging
	}()

	state := &vmState{
		Name:    "test-vm",
		CPUs:    2,
		Memory:  4096,
		State:   provider.StateRunning,
		PID:     12345,
		Console: "/tmp/console",
	}

	// Create directory
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	if err := p.saveVMState(vmDir, state); err != nil {
		t.Fatalf("Failed to save state: %v", err)
	}

	loadedState, err := p.loadVMState(vmDir)
	if err != nil {
		t.Fatalf("Failed to load state: %v", err)
	}

	// Verify
	if loadedState.Name != state.Name {
		t.Errorf("Expected name '%s', got '%s'", state.Name, loadedState.Name)
	}
	if loadedState.State != state.State {
		t.Errorf("Expected state '%s', got '%s'", state.State, loadedState.State)
	}
	if loadedState.PID != state.PID {
		t.Errorf("Expected PID %d, got %d", state.PID, loadedState.PID)
	}
}

// Integration tests (require FreeBSD with bhyve support)

func TestBhyveProviderIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if runtime.GOOS != "freebsd" {
		t.Skip("Skipping FreeBSD-only integration test")
	}

	// Check if running as root (required for TAP devices and ZFS)
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges for TAP devices and ZFS")
	}

	isolatedZFSParent(t)

	p := NewBhyveProvider()
	ctx := context.Background()

	// Initialize
	tmpDir := t.TempDir()
	config := provider.ProviderConfig{
		DataDir:  filepath.Join(tmpDir, "data"),
		StateDir: filepath.Join(tmpDir, "state"),
	}

	if err := p.Initialize(ctx, config); err != nil {
		// Skip test if hardware virtualization is not available
		if strings.Contains(err.Error(), "virtualization support") {
			t.Skipf("Skipping: %v", err)
		}
		t.Fatalf("Initialize failed: %v", err)
	}

	if err := p.HealthCheck(ctx); err != nil {
		t.Skipf("bhyve not available, skipping integration test: %v", err)
	}

	// Create instance
	spec := provider.InstanceSpec{
		Name:       "test-vm-integration",
		CPUs:       1,
		MemoryMB:   512,
		Bootloader: "uefi",
		Disks: []provider.DiskSpec{
			{
				SizeGB: 1,
				Type:   provider.DiskTypeRaw,
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

	info, err := p.GetInstanceInfo(ctx, handle)
	if err != nil {
		t.Fatalf("GetInstanceInfo failed: %v", err)
	}

	if info.Spec.Name != spec.Name {
		t.Errorf("Expected name '%s', got '%s'", spec.Name, info.Spec.Name)
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
	// 2. It requires proper network setup
	// 3. Integration tests should be fast
	// 4. May require root privileges

	t.Log("Integration test passed")
}
