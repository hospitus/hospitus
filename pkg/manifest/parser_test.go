package manifest

import (
	"strings"
	"testing"
)

func TestParseWorkloadManifest(t *testing.T) {
	tomlContent := `
[workload]
api_version = "hospitus.io/v1"
name = "web-application"

[workload.labels]
tier = "frontend"
environment = "production"

[workload.annotations]
description = "Production web server"

[provider]
type = "jail"

[image]
source = "freebsd:14.3-RELEASE"
arch = "amd64"

[resources]
cpu = 4
memory = "4Gi"

[[networks]]
name = "primary"
type = "bridge"
bridge = "hospitus0"

[networks.ip]
mode = "static"
address = "10.0.0.100/24"
gateway = "10.0.0.1"
dns = ["8.8.8.8", "8.8.4.4"]

[[networks.ports]]
host = 80
container = 8080
protocol = "tcp"

[storage.root_disk]
size = "20Gi"
type = "auto"

[[storage.volumes]]
name = "data"
size = "100Gi"
mount_path = "/var/data"

[storage.volumes.zfs]
compression = "lz4"
quota = "100Gi"

[[storage.volumes]]
name = "config"
host_path = "/tank/configs/web"
mount_path = "/etc/app"
read_only = true

[lifecycle.autostart]
enabled = true
priority = 50
delay = "5s"

[lifecycle.hooks]
pre_start = "/usr/local/bin/pre-start.sh"
post_start = "/usr/local/bin/post-start.sh"
pre_stop = "/usr/local/bin/pre-stop.sh"

[provider_overrides.jail.parameters]
"allow.raw_sockets" = true
"allow.sysvipc" = true
securelevel = 0
`

	parser := NewParser(nil)
	manifest, err := parser.Parse([]byte(tomlContent), "test.toml", nil)
	if err != nil {
		t.Fatalf("Failed to parse manifest: %v", err)
	}

	if !manifest.IsWorkload() {
		t.Error("Expected workload manifest")
	}

	w := manifest.Workload
	if w.Workload.Name != "web-application" {
		t.Errorf("Expected name 'web-application', got '%s'", w.Workload.Name)
	}

	if w.Provider.Type != "jail" {
		t.Errorf("Expected provider 'jail', got '%s'", w.Provider.Type)
	}

	if w.Resources.CPU != 4 {
		t.Errorf("Expected 4 CPUs, got %d", w.Resources.CPU)
	}

	if w.Resources.Memory != "4Gi" {
		t.Errorf("Expected memory '4Gi', got '%s'", w.Resources.Memory)
	}

	if len(w.Networks) != 1 {
		t.Fatalf("Expected 1 network, got %d", len(w.Networks))
	}

	if w.Networks[0].Name != "primary" {
		t.Errorf("Expected network name 'primary', got '%s'", w.Networks[0].Name)
	}

	if len(w.Networks[0].Ports) != 1 {
		t.Fatalf("Expected 1 port, got %d", len(w.Networks[0].Ports))
	}

	if w.Networks[0].Ports[0].Host != 80 {
		t.Errorf("Expected host port 80, got %d", w.Networks[0].Ports[0].Host)
	}

	if len(w.Storage.Volumes) != 2 {
		t.Fatalf("Expected 2 volumes, got %d", len(w.Storage.Volumes))
	}
}

func TestParseStackManifest(t *testing.T) {
	tomlContent := `
[stack]
name = "web-application-stack"
api_version = "hospitus.io/v1"

[[instances]]
name = "database"
provider = "jail"

[instances.image]
source = "freebsd:14.3-RELEASE"

[instances.resources]
cpu = 2
memory = "2Gi"

[[instances.networks]]
name = "internal"
type = "bridge"
bridge = "hospitus-int"

[instances.networks.ip]
mode = "static"
address = "10.0.1.10/24"

[[instances]]
name = "webapp"
provider = "podman"

[instances.image]
source = "oci:myapp:latest"

[instances.resources]
cpu = 4
memory = "4Gi"

[instances.depends_on]
services = ["database"]
condition = "healthy"
`

	parser := NewParser(nil)
	manifest, err := parser.Parse([]byte(tomlContent), "test-stack.toml", nil)
	if err != nil {
		t.Fatalf("Failed to parse stack manifest: %v", err)
	}

	if !manifest.IsStack() {
		t.Error("Expected stack manifest")
	}

	s := manifest.Stack
	if s.Stack.Name != "web-application-stack" {
		t.Errorf("Expected stack name 'web-application-stack', got '%s'", s.Stack.Name)
	}

	if len(s.Instances) != 2 {
		t.Fatalf("Expected 2 instances, got %d", len(s.Instances))
	}

	if s.Instances[0].Name != "database" {
		t.Errorf("Expected first instance 'database', got '%s'", s.Instances[0].Name)
	}

	if s.Instances[0].Provider != "jail" {
		t.Errorf("Expected first instance provider 'jail', got '%s'", s.Instances[0].Provider)
	}

	if s.Instances[1].Name != "webapp" {
		t.Errorf("Expected second instance 'webapp', got '%s'", s.Instances[1].Name)
	}

	if s.Instances[1].DependsOn == nil {
		t.Fatal("Expected depends_on for webapp")
	}

	if len(s.Instances[1].DependsOn.Services) != 1 || s.Instances[1].DependsOn.Services[0] != "database" {
		t.Error("Expected webapp to depend on database")
	}
}

func TestParseMemorySize(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		{"512Mi", 512 * 1024 * 1024, false},
		{"4Gi", 4 * 1024 * 1024 * 1024, false},
		{"1Ti", 1024 * 1024 * 1024 * 1024, false},
		{"1000M", 1000 * 1000 * 1000, false},
		{"1G", 1000 * 1000 * 1000, false},
		{"1024", 1024, false},
		{"", 0, true},
		{"invalid", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParseMemorySize(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error for input '%s'", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error for '%s': %v", tt.input, err)
				return
			}
			if result != tt.expected {
				t.Errorf("For '%s': expected %d, got %d", tt.input, tt.expected, result)
			}
		})
	}
}

func TestParseImageSource(t *testing.T) {
	tests := []struct {
		input    string
		wantType string
		wantRef  string
		wantErr  bool
	}{
		{"freebsd:14.3-RELEASE", "freebsd", "14.3-RELEASE", false},
		{"oci:nginx:latest", "oci", "nginx:latest", false},
		{"cloud:ubuntu-24.04", "cloud", "ubuntu-24.04", false},
		{"iso:FreeBSD-14.3-amd64-dvd1", "iso", "FreeBSD-14.3-amd64-dvd1", false},
		{"invalid", "", "", true},
		{"unknown:ref", "", "", true},
		{"", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			imgType, ref, err := ParseImageSource(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error for input '%s'", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error for '%s': %v", tt.input, err)
				return
			}
			if imgType != tt.wantType {
				t.Errorf("For '%s': expected type '%s', got '%s'", tt.input, tt.wantType, imgType)
			}
			if ref != tt.wantRef {
				t.Errorf("For '%s': expected ref '%s', got '%s'", tt.input, tt.wantRef, ref)
			}
		})
	}
}

func TestDetectManifestType(t *testing.T) {
	parser := NewParser(nil)

	tests := []struct {
		name     string
		content  string
		expected string
		wantErr  bool
	}{
		{
			name:     "workload",
			content:  "[workload]\nname = \"test\"",
			expected: "workload",
		},
		{
			name:     "stack",
			content:  "[stack]\nname = \"test\"",
			expected: "stack",
		},
		{
			name:    "both",
			content: "[workload]\n[stack]",
			wantErr: true,
		},
		{
			name:    "neither",
			content: "[other]\nname = \"test\"",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parser.detectManifestType([]byte(tt.content))
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error")
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("Expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestDefaultValues(t *testing.T) {
	tomlContent := `
[workload]
name = "minimal"

[provider]
type = "jail"

[image]
source = "freebsd:14.3-RELEASE"
`

	parser := NewParser(nil)
	manifest, err := parser.Parse([]byte(tomlContent), "test.toml", nil)
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	w := manifest.Workload

	// Check defaults were applied
	if w.Workload.APIVersion != APIVersion {
		t.Errorf("Expected default API version '%s', got '%s'", APIVersion, w.Workload.APIVersion)
	}

	if w.Image.Arch != "amd64" {
		t.Errorf("Expected default arch 'amd64', got '%s'", w.Image.Arch)
	}

	if w.Resources.CPU != 1 {
		t.Errorf("Expected default CPU 1, got %d", w.Resources.CPU)
	}

	if w.Resources.Memory != "512Mi" {
		t.Errorf("Expected default memory '512Mi', got '%s'", w.Resources.Memory)
	}
}

// TestParseRejectsUnknownFields covers the typo that used to pass silently:
// toml.Decode leaves a key it cannot place at its zero value, so the manifest
// reads as if it configured something it never did.
func TestParseRejectsUnknownFields(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		wantKey  string
	}{
		{
			name: "misspelled field",
			manifest: `
[workload]
name = "web"

[provider]
type = "jail"

[lifecycle.health_check]
command = ["true"]
retires = 3
`,
			wantKey: "lifecycle.health_check.retires",
		},
		{
			name: "field on the wrong table",
			manifest: `
[workload]
name = "web"

[provider]
type = "jail"

[storage.root_disk]
size = "20Gi"

[storage.root_disk.zfs]
compression = "lz4"
`,
			wantKey: "storage.root_disk.zfs",
		},
	}

	parser := NewParser(nil)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parser.Parse([]byte(tt.manifest), tt.name+".toml", nil)
			if err == nil {
				t.Fatalf("expected a parse error naming %q, got none", tt.wantKey)
			}
			if !strings.Contains(err.Error(), tt.wantKey) {
				t.Errorf("error should name the offending key %q, got: %v", tt.wantKey, err)
			}
		})
	}
}
