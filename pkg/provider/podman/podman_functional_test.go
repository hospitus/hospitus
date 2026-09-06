package podman

import (
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Interface assertions — verify all provider interfaces compile
func TestAllInterfaceAssertions(t *testing.T) {
	var _ provider.PauseProvider = (*PodmanProvider)(nil)
	var _ provider.ConsoleProvider = (*PodmanProvider)(nil)
	var _ provider.InstanceHealthCheckProvider = (*PodmanProvider)(nil)
	var _ provider.AutoStartProvider = (*PodmanProvider)(nil)
	var _ provider.CloneProvider = (*PodmanProvider)(nil)
	var _ provider.RenameProvider = (*PodmanProvider)(nil)
	var _ provider.SnapshotProvider = (*PodmanProvider)(nil)
}

func TestRenameNameValidation(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "newname", false},
		{"valid with dash", "new-name", false},
		{"valid with underscore", "new_name", false},
		{"empty", "", true},
		{"command injection", "name;rm", true},
		{"path traversal", "../etc", true},
		{"starts with dash", "-name", true},
		{"space in name", "my name", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validation.ValidateInstanceName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateInstanceName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestCloneNameValidation(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid", "myclone", false},
		{"valid numeric", "clone01", false},
		{"empty", "", true},
		{"backtick injection", "clone`id`", true},
		{"dollar injection", "clone$(id)", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validation.ValidateInstanceName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateInstanceName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestAutostartDir(t *testing.T) {
	p := &PodmanProvider{dataDir: "/var/lib/hospitus"}
	dir := p.autostartDir()
	expected := filepath.Join("/var/lib/hospitus", "podman", "autostart")
	if dir != expected {
		t.Errorf("autostartDir() = %q, want %q", dir, expected)
	}
}

func TestAutostartFilePath(t *testing.T) {
	p := &PodmanProvider{dataDir: "/var/lib/hospitus"}
	path := p.autostartFilePath("mycontainer")
	expected := filepath.Join("/var/lib/hospitus", "podman", "autostart", "mycontainer.json")
	if path != expected {
		t.Errorf("autostartFilePath(\"mycontainer\") = %q, want %q", path, expected)
	}
}

func TestPodmanConsoleConnectionStruct(t *testing.T) {
	conn := &PodmanConsoleConnection{
		containerID: "abc123",
		closed:      false,
	}

	if conn.containerID != "abc123" {
		t.Errorf("containerID = %q, want %q", conn.containerID, "abc123")
	}
	if conn.closed {
		t.Error("closed should be false initially")
	}
}

func TestSnapshotPrefixConstant(t *testing.T) {
	if snapshotPrefix != "hospitus-snapshot-" {
		t.Errorf("snapshotPrefix = %q, want %q", snapshotPrefix, "hospitus-snapshot-")
	}
}

func TestCapabilitiesConsistency(t *testing.T) {
	p := NewPodmanProvider()
	caps := p.Capabilities()

	// Container-specific capabilities
	if !caps.SupportsPause {
		t.Error("SupportsPause should be true")
	}
	if !caps.SupportsConsole {
		t.Error("SupportsConsole should be true")
	}
	if !caps.SupportsSnapshots {
		t.Error("SupportsSnapshots should be true")
	}
	if !caps.SupportsCloning {
		t.Error("SupportsCloning should be true")
	}

	// VM-only features should be false
	if caps.SupportsVNC {
		t.Error("SupportsVNC should be false for containers")
	}
	if caps.SupportsSerial {
		t.Error("SupportsSerial should be false for containers")
	}
	if caps.SupportsGPUPassthrough {
		t.Error("SupportsGPUPassthrough should be false for containers")
	}
	if caps.SupportsPCIPassthrough {
		t.Error("SupportsPCIPassthrough should be false for containers")
	}

	// Network types
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
		t.Error("Missing NAT network type")
	}
	if !hasBridge {
		t.Error("Missing Bridge network type")
	}
}

func TestHealthStatusConstants(t *testing.T) {
	tests := []struct {
		status provider.HealthStatus
		want   string
	}{
		{provider.HealthStatusHealthy, "healthy"},
		{provider.HealthStatusUnhealthy, "unhealthy"},
		{provider.HealthStatusDegraded, "degraded"},
		{provider.HealthStatusUnknown, "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if string(tt.status) != tt.want {
				t.Errorf("HealthStatus = %q, want %q", string(tt.status), tt.want)
			}
		})
	}
}

func TestInstanceHealthStructure(t *testing.T) {
	health := &provider.InstanceHealth{
		Status:  provider.HealthStatusHealthy,
		Message: "all checks passed",
		Checks: []provider.HealthCheck{
			{Name: "container_running", Status: provider.HealthStatusHealthy, Message: "running"},
			{Name: "healthcheck", Status: provider.HealthStatusHealthy, Message: "healthy"},
		},
	}

	if health.Status != provider.HealthStatusHealthy {
		t.Errorf("Status = %q, want %q", health.Status, provider.HealthStatusHealthy)
	}
	if len(health.Checks) != 2 {
		t.Fatalf("Checks length = %d, want 2", len(health.Checks))
	}
	if health.Checks[0].Name != "container_running" {
		t.Errorf("Check[0].Name = %q, want %q", health.Checks[0].Name, "container_running")
	}
}

func TestAutoStartConfigFields(t *testing.T) {
	config := provider.AutoStartConfig{
		Enabled:  true,
		Priority: 10,
		DelayMS:  5000,
	}

	if !config.Enabled {
		t.Error("Enabled should be true")
	}
	if config.Priority != 10 {
		t.Errorf("Priority = %d, want 10", config.Priority)
	}
	if config.DelayMS != 5000 {
		t.Errorf("DelayMS = %d, want 5000", config.DelayMS)
	}
}
