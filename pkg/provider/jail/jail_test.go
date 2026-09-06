package jail

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestJailProviderMetadata(t *testing.T) {
	p := NewJailProvider()
	metadata := p.Metadata()

	if metadata.Name != "jail" {
		t.Errorf("Expected name 'jail', got '%s'", metadata.Name)
	}

	if metadata.Type != provider.ProviderTypeContainer {
		t.Errorf("Expected type 'container', got '%s'", metadata.Type)
	}

	if metadata.Version == "" {
		t.Error("Version should not be empty")
	}
}

func TestJailProviderCapabilities(t *testing.T) {
	p := NewJailProvider()
	caps := p.Capabilities()

	// The flags follow the interfaces this provider implements: a handler
	// reaches a capability by type assertion, so a flag without the interface
	// behind it makes the provider answer "supported" and then refuse.
	if !caps.SupportsSnapshots {
		t.Error("SnapshotProvider is implemented but SupportsSnapshots is not set")
	}

	if !caps.SupportsCloning {
		t.Error("CloneProvider is implemented but SupportsCloning is not set")
	}

	if !caps.SupportsConsole {
		t.Error("ConsoleProvider is implemented but SupportsConsole is not set")
	}

	if caps.SupportsVNC {
		t.Error("Jail provider should not support VNC")
	}

	// Check network types
	foundBridge := false
	for _, nt := range caps.NetworkTypes {
		if nt == provider.NetworkTypeBridge {
			foundBridge = true
			break
		}
	}
	if !foundBridge {
		t.Error("Jail provider should support bridge networking")
	}

	// Check platform features
	if caps.PlatformFeatures["vnet"] != true {
		t.Error("Jail provider should support VNET")
	}
}

func TestJailProviderHealthCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("touches the host's jail subsystem")
	}
	if runtime.GOOS != "freebsd" {
		t.Skip("Jail provider only works on FreeBSD")
	}

	p := NewJailProvider()
	ctx := context.Background()

	err := p.HealthCheck(ctx)
	if err != nil {
		t.Logf("Health check failed (expected on non-FreeBSD or without jail support): %v", err)
		// This is expected on non-FreeBSD systems
		return
	}

	t.Log("Health check passed")
}

func TestJailProviderInitialize(t *testing.T) {
	if testing.Short() {
		t.Skip("needs root on FreeBSD and creates real ZFS datasets")
	}
	if runtime.GOOS != "freebsd" {
		t.Skip("Jail provider only works on FreeBSD")
	}

	// Check if running as root (required for ZFS operations)
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges for ZFS operations")
	}

	zfsParent := isolatedZFSParent(t)

	p := NewJailProvider()
	ctx := context.Background()

	config := provider.ProviderConfig{
		DataDir:  t.TempDir(),
		StateDir: t.TempDir(),
		Settings: map[string]interface{}{
			// Named explicitly here because this test exercises the setting; it
			// must still point inside the isolated tree, or the dataset is shared
			// with every other run and never cleaned up.
			"zfs_parent": zfsParent + "/jails",
		},
	}

	err := p.Initialize(ctx, config)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	if p.dataDir != filepath.Join(config.DataDir, "jails") {
		t.Errorf("Expected dataDir '%s', got '%s'", filepath.Join(config.DataDir, "jails"), p.dataDir)
	}

	if want := zfsParent + "/jails"; p.zfsParent != want {
		t.Errorf("Expected zfsParent '%s', got '%s'", want, p.zfsParent)
	}
}

func TestBuildJailConfig(t *testing.T) {
	p := NewJailProvider()

	spec := provider.InstanceSpec{
		Name:     "test-jail",
		CPUs:     2,
		MemoryMB: 2048,
		Networks: []provider.NetworkSpec{
			{
				Type: provider.NetworkTypeBridge,
				IPv4: "10.0.0.10/24",
			},
		},
	}

	config := p.buildJailConfig(spec, "/zroot/hospitus/jails/test-jail")

	if config.Name != "test-jail" {
		t.Errorf("Expected name 'test-jail', got '%s'", config.Name)
	}

	if config.Path != "/zroot/hospitus/jails/test-jail" {
		t.Errorf("Expected path '/zroot/hospitus/jails/test-jail', got '%s'", config.Path)
	}

	if config.Resources.CPUs != 2 {
		t.Errorf("Expected 2 CPUs, got %d", config.Resources.CPUs)
	}

	if config.Resources.MemoryMB != 2048 {
		t.Errorf("Expected 2048 MB memory, got %d", config.Resources.MemoryMB)
	}

	// Check default JailParameters (not Parameters map)
	if !config.JailParameters.MountDevfs {
		t.Error("Expected JailParameters.MountDevfs to be true")
	}

	// ExecClean defaults to false for better compatibility
	if config.JailParameters.ExecClean {
		t.Error("Expected JailParameters.ExecClean to be false (default)")
	}
}

// Integration tests (require FreeBSD with ZFS and jail support)

func TestJailProviderIntegration(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skip("Jail provider only works on FreeBSD")
	}

	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Check if running as root (required for ZFS and jail operations)
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges for ZFS and jail operations")
	}

	zfsParent := isolatedZFSParent(t)

	p := NewJailProvider()
	ctx := context.Background()

	// Initialize
	config := provider.ProviderConfig{
		DataDir:  t.TempDir(),
		StateDir: t.TempDir(),
		Settings: map[string]interface{}{
			// Named explicitly here because this test exercises the setting; it
			// must still point inside the isolated tree, or the dataset is shared
			// with every other run and never cleaned up.
			"zfs_parent": zfsParent + "/jails",
		},
	}

	if err := p.Initialize(ctx, config); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	if err := p.HealthCheck(ctx); err != nil {
		t.Fatalf("HealthCheck failed: %v", err)
	}

	// Create instance
	spec := provider.InstanceSpec{
		Name:     "test-jail-integration",
		CPUs:     1,
		MemoryMB: 512,
		Networks: []provider.NetworkSpec{
			{
				Type: provider.NetworkTypeBridge,
				IPv4: "10.0.0.100/24",
			},
		},
	}

	handle, err := p.CreateInstance(ctx, spec)
	if err != nil {
		// Skip test if permissions are insufficient (requires root)
		if strings.Contains(err.Error(), "permission denied") {
			t.Skipf("Skipping: %v", err)
		}
		t.Fatalf("CreateInstance failed: %v", err)
	}

	t.Logf("Created jail: %s", handle.ID)

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
		t.Error("Created jail not found in list")
	}

	// Note: Starting and stopping requires a populated base system
	// which we haven't implemented extraction for yet
	// So we skip those tests for now

	t.Log("Integration test passed")
}

// TestAttachDiskRefusesEscapingMountPoints covers the mount target: TrimPrefix
// removes only a leading "/", so a traversal survives it and filepath.Join
// resolves outside the jail root — where the directory would be created, the
// nullfs mount laid down and the path written into the jail's fstab, as root.
func TestAttachDiskRefusesEscapingMountPoints(t *testing.T) {
	stateDir := t.TempDir()
	jailRoot := filepath.Join(t.TempDir(), "jails", "web")
	if err := os.MkdirAll(jailRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &JailConfig{Name: "web", Path: jailRoot}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "web.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	p := &JailProvider{stateDir: stateDir}
	handle := provider.InstanceHandle{ID: "web"}

	for _, mp := range []string{"../../etc", "/../../var/db", "../escape", "a/../../../tmp"} {
		err := p.AttachDisk(context.Background(), handle,
			provider.DiskAttachment{MountPoint: mp, Disk: provider.DiskSpec{Path: t.TempDir()}})
		if err == nil {
			t.Errorf("mount point %q was accepted", mp)
		}
	}
}

// TestMaskFromBitsHandlesIPv6Prefixes covers the panic: net.CIDRMask answers
// nil outside 0..32, and an IPv6 jail address arrives here with 64.
func TestMaskFromBitsHandlesIPv6Prefixes(t *testing.T) {
	if got := getMaskFromBits(24); got != "255.255.255.0" {
		t.Errorf("getMaskFromBits(24) = %q", got)
	}
	for _, bits := range []int{-1, 33, 64, 128} {
		if got := getMaskFromBits(bits); got != "" {
			t.Errorf("getMaskFromBits(%d) = %q, want an empty string", bits, got)
		}
	}
}
