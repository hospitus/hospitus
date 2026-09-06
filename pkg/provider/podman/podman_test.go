package podman

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestPodmanProviderMetadata(t *testing.T) {
	p := NewPodmanProvider()
	meta := p.Metadata()

	if meta.Name != "podman" {
		t.Errorf("Expected name 'podman', got '%s'", meta.Name)
	}
	if meta.Type != provider.ProviderTypeContainer {
		t.Errorf("Expected type 'container', got '%s'", meta.Type)
	}
	if meta.Version == "" {
		t.Error("Version should not be empty")
	}
}

func TestPodmanProviderCapabilities(t *testing.T) {
	p := NewPodmanProvider()
	caps := p.Capabilities()

	// Check supported features
	if !caps.SupportsPause {
		t.Error("Podman should support pause")
	}
	if !caps.SupportsConsole {
		t.Error("Podman should support console")
	}
	if caps.SupportsVNC {
		t.Error("Podman should not support VNC")
	}

	// Check network types
	hasNAT := false
	hasBridge := false
	for _, nt := range caps.NetworkTypes {
		if nt == provider.NetworkTypeNAT {
			hasNAT = true
		}
		if nt == provider.NetworkTypeBridge {
			hasBridge = true
		}
	}
	if !hasNAT {
		t.Error("Expected NAT network type")
	}
	if !hasBridge {
		t.Error("Expected Bridge network type")
	}

	// Check architectures
	hasAmd64 := false
	hasArm64 := false
	for _, arch := range caps.SupportedArchitectures {
		if arch == "amd64" {
			hasAmd64 = true
		}
		if arch == "arm64" {
			hasArm64 = true
		}
	}
	if !hasAmd64 {
		t.Error("Expected amd64 architecture support")
	}
	if !hasArm64 {
		t.Error("Expected arm64 architecture support")
	}
}

func TestPodmanProviderInitialize(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real podman against the host")
	}
	// Skip if podman is not installed
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not installed")
	}

	// On FreeBSD, podman requires root (no rootless support)
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges (podman on FreeBSD)")
	}

	p := NewPodmanProvider()
	ctx := context.Background()

	config := provider.ProviderConfig{
		DataDir:  filepath.Join(t.TempDir(), "data"),
		StateDir: filepath.Join(t.TempDir(), "state"),
	}

	err := p.Initialize(ctx, config)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Verify directories are set
	if p.dataDir != config.DataDir {
		t.Errorf("Expected dataDir '%s', got '%s'", config.DataDir, p.dataDir)
	}

	// Verify podman binary is found
	if p.podmanBin == "" {
		t.Error("podmanBin should be set after initialization")
	}
}

func TestPodmanProviderHealthCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real podman against the host")
	}
	// Skip if podman is not installed
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not installed")
	}

	// On FreeBSD, podman requires root (no rootless support)
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges (podman on FreeBSD)")
	}

	p := NewPodmanProvider()
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
		t.Errorf("HealthCheck failed: %v", err)
	}

	t.Log("Health check passed - Podman is available")
}

func TestIsValidContainerID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"full 64-char hex ID", "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", true},
		{"short 12-char hex ID", "a1b2c3d4e5f6", true},
		{"uppercase hex", "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2", true},
		{"mixed case hex", "a1B2c3D4e5F6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", true},
		{"empty string", "", false},
		{"too short (11 chars)", "a1b2c3d4e5f", false},
		{"wrong length (13 chars)", "a1b2c3d4e5f6a", false},
		{"63-char hex", "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1", false},
		{"non-hex in 64-char string", "z1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", false},
		{"non-hex in 12-char string", "a1b2c3g4e5f6", false},
		{"spaces", "a1b2 3d4e5f6", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidContainerID(tt.input)
			if got != tt.want {
				t.Errorf("isValidContainerID(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractContainerID(t *testing.T) {
	validID64 := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	validID12 := "a1b2c3d4e5f6"

	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "clean single-line output",
			output: validID64 + "\n",
			want:   validID64,
		},
		{
			name:   "ID with trailing newline",
			output: validID64,
			want:   validID64,
		},
		{
			name: "warnings before ID",
			output: "WARNING: some compatibility warning\n" +
				"WARNING: another warning\n" +
				validID64 + "\n",
			want: validID64,
		},
		{
			name: "warnings after ID (ID not at end)",
			output: validID64 + "\n" +
				"WARNING: post-run warning\n",
			want: validID64,
		},
		{
			name: "warnings before and after ID",
			output: "WARNING: pre warning\n" +
				validID64 + "\n" +
				"WARNING: post warning\n",
			want: validID64,
		},
		{
			name:   "short 12-char ID",
			output: validID12 + "\n",
			want:   validID12,
		},
		{
			name:   "empty output",
			output: "",
			want:   "",
		},
		{
			name:   "only warnings",
			output: "WARNING: no ID here\nWARNING: still no ID\n",
			want:   "",
		},
		{
			name:   "only whitespace",
			output: "   \n\n\t\n",
			want:   "",
		},
		{
			name:   "error line only",
			output: "Error: container not created\n",
			want:   "",
		},
		{
			name:   "multiple blank lines around ID",
			output: "\n\n" + validID64 + "\n\n",
			want:   validID64,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractContainerID(tt.output)
			if got != tt.want {
				t.Errorf("extractContainerID(%q) = %q, want %q", tt.output, got, tt.want)
			}
		})
	}
}

func TestIsShortName(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  bool
	}{
		{"simple name", "nginx", true},
		{"name with tag", "nginx:latest", true},
		{"name with version", "nginx:1.27-alpine", true},
		{"namespace/image", "bitnami/nginx", true},
		{"namespace/image with tag", "bitnami/nginx:latest", true},
		{"docker.io registry", "docker.io/library/nginx", false},
		{"ghcr.io registry", "ghcr.io/org/image", false},
		{"ghcr.io with tag", "ghcr.io/org/image:tag", false},
		{"localhost", "localhost/foo", false},
		{"localhost with port", "localhost:5000/img", false},
		{"registry with port", "registry:5000/img", false},
		{"quay.io", "quay.io/prometheus/node-exporter", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isShortName(tt.image)
			if got != tt.want {
				t.Errorf("isShortName(%q) = %v, want %v", tt.image, got, tt.want)
			}
		})
	}
}

func TestNormalizeImageName(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{"oci prefix simple", "oci:nginx", "docker.io/library/nginx"},
		{"oci prefix with tag", "oci:nginx:latest", "docker.io/library/nginx:latest"},
		{"docker:// prefix simple", "docker://nginx", "docker.io/library/nginx"},
		{"docker:// prefix with tag", "docker://nginx:alpine", "docker.io/library/nginx:alpine"},
		{"plain short name", "nginx", "docker.io/library/nginx"},
		{"plain short name with tag", "nginx:latest", "docker.io/library/nginx:latest"},
		{"namespaced short name", "bitnami/nginx", "docker.io/bitnami/nginx"},
		{"namespaced with tag", "bitnami/nginx:latest", "docker.io/bitnami/nginx:latest"},
		{"already qualified docker.io", "docker.io/library/nginx", "docker.io/library/nginx"},
		{"already qualified ghcr.io", "ghcr.io/org/image:tag", "ghcr.io/org/image:tag"},
		{"quay.io image", "quay.io/prometheus/node-exporter:latest", "quay.io/prometheus/node-exporter:latest"},
		{"oci prefix with namespace", "oci:bitnami/nginx", "docker.io/bitnami/nginx"},
		{"docker:// with registry", "docker://docker.io/library/nginx", "docker.io/library/nginx"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeImageName(tt.source)
			if got != tt.want {
				t.Errorf("normalizeImageName(%q) = %q, want %q", tt.source, got, tt.want)
			}
		})
	}
}

func TestValidateImageSource(t *testing.T) {
	p := NewPodmanProvider()

	tests := []struct {
		name    string
		source  string
		wantErr bool
	}{
		{"empty string", "", true},
		{"string with spaces", "image name with space", true},
		{"oci prefix", "oci:nginx", false},
		{"oci with tag", "oci:nginx:latest", false},
		{"docker:// prefix", "docker://nginx", false},
		{"docker:// with registry", "docker://docker.io/library/nginx", false},
		{"name with tag (colon)", "nginx:latest", false},
		{"fully qualified with slash", "docker.io/library/nginx", false},
		{"simple short name", "nginx", false},
		{"ghcr.io image", "ghcr.io/org/image:tag", false},
		{"namespaced image", "bitnami/nginx", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.ValidateImageSource(tt.source)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateImageSource(%q) error = %v, wantErr %v", tt.source, err, tt.wantErr)
			}
		})
	}
}

func TestSnapshotPrefix(t *testing.T) {
	if snapshotPrefix != "hospitus-snapshot-" {
		t.Errorf("snapshotPrefix = %q, want %q", snapshotPrefix, "hospitus-snapshot-")
	}
}

func TestContainerIDEdgeCases(t *testing.T) {
	// Exactly 12 hex chars — valid short ID
	if !isValidContainerID("abcdef012345") {
		t.Error("12-char lowercase hex should be valid")
	}
	// Exactly 64 hex chars — valid full ID
	full := strings.Repeat("ab", 32)
	if !isValidContainerID(full) {
		t.Errorf("64-char hex should be valid, got false for %q", full)
	}
	// 11 chars — invalid
	if isValidContainerID("abcdef01234") {
		t.Error("11-char hex should be invalid")
	}
}

func TestExtractContainerIDPicksLast(t *testing.T) {
	// When multiple valid IDs are present, the last one should be returned
	id1 := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	id2 := "b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3"
	output := id1 + "\n" + id2 + "\n"
	got := extractContainerID(output)
	if got != id2 {
		t.Errorf("expected last ID %q, got %q", id2, got)
	}
}

func TestPodmanContainerLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real podman against the host")
	}

	// Skip if podman is not installed
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not installed")
	}

	// On FreeBSD, podman requires root (no rootless support)
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges (podman on FreeBSD)")
	}

	p := NewPodmanProvider()
	ctx := context.Background()

	config := provider.ProviderConfig{
		DataDir:  filepath.Join(t.TempDir(), "data"),
		StateDir: filepath.Join(t.TempDir(), "state"),
	}

	if err := p.Initialize(ctx, config); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	containerName := "test-lifecycle-container"
	var containerID string

	// Cleanup any existing container with this name
	_ = p.DeleteInstance(ctx, provider.InstanceHandle{ID: containerName}, false)

	// Defer cleanup
	defer func() {
		if containerID != "" {
			_ = p.StopInstance(ctx, provider.InstanceHandle{ID: containerID}, provider.StopOptions{Force: true})
			_ = p.DeleteInstance(ctx, provider.InstanceHandle{ID: containerID}, false)
		}
	}()

	t.Run("Create", func(t *testing.T) {
		handle := provider.InstanceHandle{ID: containerName}
		_ = p.DeleteInstance(ctx, handle, false)
		spec := provider.InstanceSpec{
			Name:     containerName,
			Image:    "docker.io/library/busybox:latest",
			CPUs:     1,
			MemoryMB: 128,
			ProviderConfig: map[string]interface{}{
				// Keep container running with sleep
				"command": []interface{}{"sleep", "3600"},
			},
		}

		handle, err := p.CreateInstance(ctx, spec)
		if err != nil {
			t.Fatalf("CreateInstance failed: %v", err)
		}

		if handle.ID == "" {
			t.Error("Expected non-empty handle ID")
		}
		containerID = handle.ID
		t.Logf("Created container with handle: %s", handle.ID)
	})

	t.Run("Start", func(t *testing.T) {
		if containerID == "" {
			t.Skip("No container ID from Create")
		}
		handle := provider.InstanceHandle{ID: containerID}
		err := p.StartInstance(ctx, handle)
		if err != nil {
			t.Fatalf("StartInstance failed: %v", err)
		}
		t.Log("Container started successfully")
	})

	t.Run("GetState", func(t *testing.T) {
		if containerID == "" {
			t.Skip("No container ID from Create")
		}
		handle := provider.InstanceHandle{ID: containerID}
		state, err := p.GetInstanceState(ctx, handle)
		if err != nil {
			t.Fatalf("GetInstanceState failed: %v", err)
		}

		if state != provider.StateRunning {
			t.Errorf("Expected state 'running', got '%s'", state)
		}
	})

	t.Run("ListInstances", func(t *testing.T) {
		if containerID == "" {
			t.Skip("No container ID from Create")
		}
		filter := provider.InstanceFilter{}
		handles, err := p.ListInstances(ctx, filter)
		if err != nil {
			t.Fatalf("ListInstances failed: %v", err)
		}

		// ListInstances may return short IDs (12 chars), so use prefix matching
		found := false
		for _, h := range handles {
			// Check if either ID is a prefix of the other
			if strings.HasPrefix(containerID, h.ID) || strings.HasPrefix(h.ID, containerID) {
				found = true
				break
			}
		}

		if !found {
			t.Logf("Looking for container ID: %s", containerID)
			t.Logf("Found handles: %v", handles)
			t.Error("Created container not found in list")
		}
	})

	t.Run("Stop", func(t *testing.T) {
		if containerID == "" {
			t.Skip("No container ID from Create")
		}
		handle := provider.InstanceHandle{ID: containerID}
		err := p.StopInstance(ctx, handle, provider.StopOptions{})
		if err != nil {
			t.Fatalf("StopInstance failed: %v", err)
		}
		t.Log("Container stopped successfully")
	})

	t.Run("Delete", func(t *testing.T) {
		if containerID == "" {
			t.Skip("No container ID from Create")
		}
		handle := provider.InstanceHandle{ID: containerID}
		err := p.DeleteInstance(ctx, handle, false)
		if err != nil {
			t.Fatalf("DeleteInstance failed: %v", err)
		}
		t.Log("Container deleted successfully")
	})
}

func TestCreateInstanceVolumeAllowlist(t *testing.T) {
	p := NewPodmanProvider()
	// Initialize with minimal state so we hit volume validation before podman exec
	p.instances = make(map[string]*containerInfo)

	// A source the daemon cannot traverse is refused too, but by the resolver
	// rather than the allow-list, so its message depends on the host: /root is
	// 0700 on Linux and absent on macOS. anyErr covers that row.
	tests := []struct {
		name    string
		volume  string
		wantErr string
		anyErr  bool
	}{
		{"allowed /var/lib/hospitus/volumes", "/var/lib/hospitus/volumes/data:/data", "", false},
		{"blocked /tmp/hospitus- (world-writable, symlinkable)", "/tmp/hospitus-abc123:/tmp", "not under allowed roots", false},
		{"blocked /home/user (home no longer allowlisted)", "/home/user/data:/data", "not under allowed roots", false},
		{"blocked /etc", "/etc/passwd:/etc/passwd", "not under allowed roots", false},
		{"blocked /root", "/root/.ssh:/root/.ssh", "", true},
		{"blocked /", "/:/host", "not under allowed roots", false},
		{"blocked /var/lib/hospitus (no volumes subdir)", "/var/lib/hospitus/state:/data", "not under allowed roots", false},
		{"path traversal", "/var/lib/hospitus/volumes/../../../etc:/data", "not under allowed roots", false},
		{"traversal under allowed prefix", "/home/user/../../../etc:/data", "not under allowed roots", false},
		{"missing dest", "/var/lib/hospitus/volumes/data", "invalid volume mount format", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := provider.InstanceSpec{
				Name:  "test-vol",
				Image: "docker.io/library/busybox:latest",
				ProviderConfig: map[string]interface{}{
					"volumes": []interface{}{tt.volume},
				},
			}

			_, err := p.CreateInstance(context.Background(), spec)
			if tt.anyErr {
				if err == nil {
					t.Error("expected the mount to be refused, got nil")
				}
				return
			}
			if tt.wantErr == "" {
				// Expect to pass volume validation but may fail on podman exec
				// (since podman binary isn't set up). We check it doesn't fail
				// on the volume check specifically.
				if err != nil && strings.Contains(err.Error(), "not under allowed roots") {
					t.Errorf("volume should be allowed but got: %v", err)
				}
				if err != nil && strings.Contains(err.Error(), "path traversal") {
					t.Errorf("volume should be allowed but got: %v", err)
				}
				if err != nil && strings.Contains(err.Error(), "invalid volume mount format") {
					t.Errorf("volume should be allowed but got: %v", err)
				}
			} else {
				if err == nil {
					t.Error("expected error but got nil")
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
				}
			}
		})
	}
}

func TestCreateInstanceInvalidName(t *testing.T) {
	p := NewPodmanProvider()
	p.instances = make(map[string]*containerInfo)

	tests := []struct {
		name    string
		vmName  string
		wantErr bool
	}{
		{"valid name", "my-container", false},
		{"valid with numbers", "web-01", false},
		{"empty name", "", true},
		{"name with spaces", "my container", true},
		{"name with semicolon", "test;rm -rf /", true},
		{"name with backtick", "test`id`", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := provider.InstanceSpec{
				Name:  tt.vmName,
				Image: "docker.io/library/busybox:latest",
			}

			_, err := p.CreateInstance(context.Background(), spec)
			if tt.wantErr && err == nil {
				t.Error("expected error but got nil")
			}
			if !tt.wantErr && err != nil && strings.Contains(err.Error(), "invalid container name") {
				t.Errorf("expected valid name but got: %v", err)
			}
		})
	}
}

func TestCreateInstanceDuplicateName(t *testing.T) {
	p := NewPodmanProvider()
	// The map is keyed by container name everywhere in the provider.
	p.instances = map[string]*containerInfo{
		"existing-container": {Name: "existing-container"},
	}
	// Podman confirms the container is really there, so the name is taken.
	p.runner = &execx.Fake{}

	spec := provider.InstanceSpec{
		Name:  "existing-container",
		Image: "docker.io/library/busybox:latest",
	}

	_, err := p.CreateInstance(context.Background(), spec)
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %v", err)
	}
}

// TestCreateInstanceDropsStaleRecord covers a container removed with podman(1)
// outside Hospitus: its cached record must not hold the name until the daemon is
// restarted.
func TestCreateInstanceDropsStaleRecord(t *testing.T) {
	p := NewPodmanProvider()
	p.instances = map[string]*containerInfo{
		"ghost": {Name: "ghost", State: provider.StateStopped},
	}
	// "container exists" fails: podman does not have it. Every other command
	// succeeds so creation can proceed past the uniqueness check.
	p.runner = &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "container" && args[1] == "exists" {
			return nil, errNoSuchContainer
		}
		return nil, nil
	}}

	spec := provider.InstanceSpec{
		Name:  "ghost",
		Image: "docker.io/library/busybox:latest",
	}

	if _, err := p.CreateInstance(context.Background(), spec); err != nil &&
		strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a container absent from podman must not hold its name: %v", err)
	}
}

// errNoSuchContainer stands in for `podman container exists` failing.
var errNoSuchContainer = errors.New("no such container")

// TestContainerExists checks the query issued against podman.
func TestContainerExists(t *testing.T) {
	p := NewPodmanProvider()
	p.podmanBin = "podman"
	fake := &execx.Fake{}
	p.runner = fake

	if !p.containerExists(context.Background(), "web") {
		t.Error("containerExists = false when podman reports success")
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("expected one podman call, got %d", len(fake.Calls))
	}
	got := strings.Join(fake.Calls[0].Args, " ")
	if got != "container exists web" {
		t.Errorf("podman args = %q, want %q", got, "container exists web")
	}
}
