package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

// boolPtr returns a pointer to b, for building *bool struct fields in tests.
func boolPtr(b bool) *bool { return &b }

// TestSecretStoreRotateRejectsTraversal covers the one caller that reached
// generateAndSave without validating first. Rotate is `hospitus secret rotate
// <scope> <name>`, so the scope is whatever the operator typed, and it has to
// be checked before any directory is created for it.
func TestSecretStoreRotateRejectsTraversal(t *testing.T) {
	parent := t.TempDir()
	baseDir := filepath.Join(parent, "secrets")
	store := NewFileSecretStore(baseDir)

	if _, err := store.Rotate("../escaped", "key"); err == nil {
		t.Fatal("Rotate accepted a scope that climbs out of the base directory")
	}

	if _, err := os.Stat(filepath.Join(parent, "escaped")); err == nil {
		t.Error("Rotate created a directory outside the base directory")
	}
}

// --- FileSecretStore tests ---

func TestFileSecretStore_RemoveAndList(t *testing.T) {
	dir := t.TempDir()
	store := NewFileSecretStore(dir)

	// List empty scope returns empty slice
	secrets, err := store.List("myapp")
	if err != nil {
		t.Fatalf("List(myapp) on empty: %v", err)
	}
	if len(secrets) != 0 {
		t.Errorf("expected 0 secrets, got %d", len(secrets))
	}

	// Rotate (generates and saves) a secret
	_, err = store.Rotate("myapp", "db-pass")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// List should now show db-pass
	secrets, err = store.List("myapp")
	if err != nil {
		t.Fatalf("List after rotate: %v", err)
	}
	if len(secrets) != 1 || secrets[0] != "db-pass" {
		t.Errorf("expected [db-pass], got %v", secrets)
	}

	// Remove it
	if err := store.Remove("myapp", "db-pass"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// List should be empty again
	secrets, err = store.List("myapp")
	if err != nil {
		t.Fatalf("List after remove: %v", err)
	}
	if len(secrets) != 0 {
		t.Errorf("expected 0 after remove, got %d", len(secrets))
	}
}

func TestFileSecretStore_List_InvalidScope(t *testing.T) {
	dir := t.TempDir()
	store := NewFileSecretStore(dir)

	cases := []string{"", ".", "..", "a/b", "a\\b"}
	for _, sc := range cases {
		_, err := store.List(sc)
		if err == nil {
			t.Errorf("List(%q): expected error for invalid scope", sc)
		}
	}
}

func TestFileSecretStore_Remove_NonExistent(t *testing.T) {
	dir := t.TempDir()
	store := NewFileSecretStore(dir)

	// Remove non-existent secret should return error (os.Remove on non-existent path)
	err := store.Remove("myapp", "nonexistent")
	if err == nil {
		t.Error("expected error removing non-existent secret")
	}
}

// --- ParseFile / ParseString tests ---

func TestParseFile_Success(t *testing.T) {
	dir := t.TempDir()
	content := `
[workload]
api_version = "v1"
name = "parse-test"

[provider]
type = "jail"

[image]
source = "freebsd:14.3-RELEASE"

[resources]
cpu = 2
memory = "512Mi"
`
	path := filepath.Join(dir, "workload.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	parsed, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if parsed == nil {
		t.Fatal("expected non-nil ParsedManifest")
	}
	if parsed.GetName() != "parse-test" {
		t.Errorf("expected name parse-test, got %q", parsed.GetName())
	}
}

func TestParseFile_NotFound(t *testing.T) {
	_, err := ParseFile("/tmp/hospitus-no-such-manifest-file.toml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestParseString_InvalidType(t *testing.T) {
	_, err := ParseString("[something]\nname = \"foo\"")
	if err == nil {
		t.Error("expected error for unknown manifest type")
	}
}

func TestParseString_Stack(t *testing.T) {
	content := `
[stack]
api_version = "v1"
name = "my-stack"

[[instances]]
name = "web"
provider = "jail"

[instances.image]
source = "freebsd:14.3-RELEASE"

[instances.resources]
cpu = 1
memory = "256Mi"
`
	parsed, err := ParseString(content)
	if err != nil {
		t.Fatalf("ParseString(stack): %v", err)
	}
	if parsed.GetName() != "my-stack" {
		t.Errorf("expected my-stack, got %q", parsed.GetName())
	}
}

// --- ToInstanceSpecs test ---

func TestToInstanceSpecs_Basic(t *testing.T) {
	c := NewConverter()

	m := &StackManifest{
		Stack: StackMeta{Name: "my-stack"},
		Instances: []InstanceConfig{
			{
				Name:     "web",
				Provider: "jail",
				Image:    ImageSpec{Source: "freebsd:14.3-RELEASE"},
				Resources: ResourceSpec{
					CPU:    2,
					Memory: "512Mi",
				},
			},
		},
	}

	specs, err := c.ToInstanceSpecs(m)
	if err != nil {
		t.Fatalf("ToInstanceSpecs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}
	if specs[0].Name != "web" {
		t.Errorf("expected name web, got %s", specs[0].Name)
	}
	if specs[0].CPUs != 2 {
		t.Errorf("expected 2 CPUs, got %d", specs[0].CPUs)
	}
}

func TestToInstanceSpecs_InvalidMemory(t *testing.T) {
	c := NewConverter()

	m := &StackManifest{
		Instances: []InstanceConfig{
			{
				Name:      "bad",
				Image:     ImageSpec{Source: "freebsd:14.3-RELEASE"},
				Resources: ResourceSpec{Memory: "bad-size"},
			},
		},
	}

	_, err := c.ToInstanceSpecs(m)
	if err == nil {
		t.Error("expected error for invalid memory")
	}
}

// --- ToCloudInitConfig tests ---

func TestToCloudInitConfig_Nil(t *testing.T) {
	cfg, err := ToCloudInitConfig(nil, "id", "host", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Error("expected nil config for nil spec")
	}
}

func TestToCloudInitConfig_Disabled(t *testing.T) {
	spec := &CloudInitSpec{Enabled: boolPtr(false)}
	cfg, err := ToCloudInitConfig(spec, "id", "host", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Error("expected nil config for disabled spec")
	}
}

func TestToCloudInitConfig_Basic(t *testing.T) {
	spec := &CloudInitSpec{
		Enabled:  boolPtr(true),
		Packages: []string{"git", "curl"},
		RunCMD:   []string{"echo hello"},
	}
	cfg, err := ToCloudInitConfig(spec, "test-id", "myhost", nil, "")
	if err != nil {
		t.Fatalf("ToCloudInitConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.InstanceID != "test-id" {
		t.Errorf("expected instance id test-id, got %s", cfg.InstanceID)
	}
	if cfg.LocalHostname != "myhost" {
		t.Errorf("expected hostname myhost, got %s", cfg.LocalHostname)
	}
	if len(cfg.Packages) != 2 {
		t.Errorf("expected 2 packages, got %d", len(cfg.Packages))
	}
}

func TestToCloudInitConfig_CustomHostname(t *testing.T) {
	spec := &CloudInitSpec{
		Enabled:  boolPtr(true),
		Hostname: "overridden",
	}
	cfg, err := ToCloudInitConfig(spec, "id", "original", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LocalHostname != "overridden" {
		t.Errorf("expected overridden hostname, got %s", cfg.LocalHostname)
	}
}

func TestToCloudInitConfig_UserData(t *testing.T) {
	spec := &CloudInitSpec{
		Enabled:  boolPtr(true),
		UserData: "#cloud-config\npackages:\n  - git\n",
	}
	cfg, err := ToCloudInitConfig(spec, "id", "host", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CustomUserData == "" {
		t.Error("expected custom user data to be set")
	}
}

func TestToCloudInitConfig_UserDataFile(t *testing.T) {
	dir := t.TempDir()
	udFile := filepath.Join(dir, "userdata.yaml")
	if err := os.WriteFile(udFile, []byte("#cloud-config\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	spec := &CloudInitSpec{
		Enabled:      boolPtr(true),
		UserDataFile: udFile,
	}
	cfg, err := ToCloudInitConfig(spec, "id", "host", nil, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CustomUserData == "" {
		t.Error("expected custom user data from file")
	}
}

func TestToCloudInitConfig_UserDataFile_NotFound(t *testing.T) {
	spec := &CloudInitSpec{
		Enabled:      boolPtr(true),
		UserDataFile: "/tmp/hospitus-no-such-userdata-file.yaml",
	}
	_, err := ToCloudInitConfig(spec, "id", "host", nil, "")
	if err == nil {
		t.Error("expected error for missing user_data_file")
	}
}

func TestToCloudInitConfig_WithNetworks(t *testing.T) {
	dhcpMode := IPModeDHCP
	staticMode := IPModeStatic

	networks := []NetworkSpec{
		{
			Name: "eth0",
			IP:   &IPConfig{Mode: dhcpMode},
		},
		{
			Name: "eth1",
			IP: &IPConfig{
				Mode:    staticMode,
				Address: "192.168.1.10/24",
				Gateway: "192.168.1.1",
				DNS:     []string{"8.8.8.8"},
			},
		},
		{
			Name: "eth2",
			// No IP config — should be skipped
		},
		{
			Name: "eth3",
			IP:   &IPConfig{Mode: IPModeNone},
		},
	}

	spec := &CloudInitSpec{Enabled: boolPtr(true)}
	cfg, err := ToCloudInitConfig(spec, "id", "host", networks, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// eth0 (dhcp) + eth1 (static) should be included, eth2 (no ip) + eth3 (none) skipped
	if len(cfg.Networks) != 2 {
		t.Errorf("expected 2 networks, got %d", len(cfg.Networks))
	}
}

func TestToCloudInitConfig_Users(t *testing.T) {
	spec := &CloudInitSpec{
		Enabled: boolPtr(true),
		Users: []CloudInitUser{
			{
				Name:   "deploy",
				Groups: []string{"wheel"},
				Shell:  "/bin/sh",
				Sudo:   "ALL=(ALL) NOPASSWD:ALL",
			},
		},
	}
	cfg, err := ToCloudInitConfig(spec, "id", "host", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Users) != 1 || cfg.Users[0].Name != "deploy" {
		t.Errorf("expected user deploy, got %v", cfg.Users)
	}
}
