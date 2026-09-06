package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigPaths(t *testing.T) {
	paths := DefaultConfigPaths()
	if len(paths) == 0 {
		t.Fatal("DefaultConfigPaths returned empty list")
	}
	for _, p := range paths {
		if p == "" {
			t.Error("DefaultConfigPaths contains empty string")
		}
	}
}

func TestLoadConfig_NotExist(t *testing.T) {
	cfg, err := LoadConfig("/nonexistent/path/hospitusd.conf")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if cfg != nil {
		t.Error("expected nil config for missing file")
	}
}

func TestLoadConfig_Full(t *testing.T) {
	content := `# Hospitus configuration
ip_pool = 10.100.0.0/24
enable_ip_forwarding = false
enable_nat = false
external_interface = em0
firewall_type = ipfw
default_gateway = 10.0.0.1
auto_create_bridges = false
nat_network = 10.100.0.0/24
bridge_prefix = jail
pf_anchor_name = myjails
enable_ipv6 = true
# unknown key is silently ignored
unknown_key = whatever
`
	dir := t.TempDir()
	path := filepath.Join(dir, "hospitusd.conf")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	tests := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"IPPool", cfg.IPPool, "10.100.0.0/24"},
		{"EnableIPForwarding", cfg.EnableIPForwarding, false},
		{"EnableNAT", cfg.EnableNAT, false},
		{"ExternalInterface", cfg.ExternalInterface, "em0"},
		{"FirewallType", cfg.FirewallType, "ipfw"},
		{"DefaultGateway", cfg.DefaultGateway, "10.0.0.1"},
		{"AutoCreateBridges", cfg.AutoCreateBridges, false},
		{"NATNetwork", cfg.NATNetwork, "10.100.0.0/24"},
		{"BridgePrefix", cfg.BridgePrefix, "jail"},
		{"PFAnchorName", cfg.PFAnchorName, "myjails"},
		{"EnableIPv6", cfg.EnableIPv6, true},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestLoadConfig_QuotedValues(t *testing.T) {
	content := `ip_pool = "10.200.0.0/24"
bridge_prefix = 'br'
`
	dir := t.TempDir()
	path := filepath.Join(dir, "hospitusd.conf")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.IPPool != "10.200.0.0/24" {
		t.Errorf("expected quoted value stripped, got %q", cfg.IPPool)
	}
	if cfg.BridgePrefix != "br" {
		t.Errorf("expected single-quoted value stripped, got %q", cfg.BridgePrefix)
	}
}

func TestLoadConfig_SkipsCommentsAndBlanks(t *testing.T) {
	content := `
# This is a comment
  # Indented comment

ip_pool = 10.99.0.0/24
`
	dir := t.TempDir()
	path := filepath.Join(dir, "hospitusd.conf")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.IPPool != "10.99.0.0/24" {
		t.Errorf("unexpected ip_pool: %q", cfg.IPPool)
	}
}

func TestLoadConfig_NoEqualSign(t *testing.T) {
	content := `invalid line without equals
ip_pool = 10.77.0.0/24
`
	dir := t.TempDir()
	path := filepath.Join(dir, "hospitusd.conf")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.IPPool != "10.77.0.0/24" {
		t.Errorf("line without = should be skipped, ip_pool=%q", cfg.IPPool)
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	// Empty file → defaults applied
	dir := t.TempDir()
	path := filepath.Join(dir, "hospitusd.conf")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Booleans default to true
	if !cfg.EnableIPForwarding {
		t.Error("EnableIPForwarding should default to true")
	}
	if !cfg.EnableNAT {
		t.Error("EnableNAT should default to true")
	}
	if !cfg.AutoCreateBridges {
		t.Error("AutoCreateBridges should default to true")
	}
}

func TestLoadConfigWithDefaults_NoFile(t *testing.T) {
	// Force the lookup to an empty temp directory so the test does not depend
	// on any real /usr/local/etc or /etc configuration present on the host.
	old := configPaths
	configPaths = []string{filepath.Join(t.TempDir(), "hospitusd.conf")}
	t.Cleanup(func() { configPaths = old })

	cfg, err := LoadConfigWithDefaults()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	// Defaults should be applied
	if cfg.ExternalInterface != "auto" {
		t.Errorf("expected ExternalInterface=auto, got %q", cfg.ExternalInterface)
	}
	if cfg.FirewallType != "pf" {
		t.Errorf("expected FirewallType=pf, got %q", cfg.FirewallType)
	}
	if cfg.DefaultGateway != "auto" {
		t.Errorf("expected DefaultGateway=auto, got %q", cfg.DefaultGateway)
	}
	if cfg.NATNetwork != "auto" {
		t.Errorf("expected NATNetwork=auto, got %q", cfg.NATNetwork)
	}
	if cfg.BridgePrefix != "hospitus" {
		t.Errorf("expected BridgePrefix=hospitus, got %q", cfg.BridgePrefix)
	}
	if cfg.PFAnchorName != "hospitus" {
		t.Errorf("expected PFAnchorName=hospitus, got %q", cfg.PFAnchorName)
	}
}

func TestParseBool(t *testing.T) {
	tests := []struct {
		input string
		def   bool
		want  bool
	}{
		{"true", false, true},
		{"yes", false, true},
		{"1", false, true},
		{"on", false, true},
		{"enabled", false, true},
		{"TRUE", false, true},
		{"YES", false, true},
		{"false", true, false},
		{"no", true, false},
		{"0", true, false},
		{"off", true, false},
		{"disabled", true, false},
		{"FALSE", true, false},
		{"NO", true, false},
		{"", true, true},        // unknown → default
		{"maybe", false, false}, // unknown → default
		{"maybe", true, true},   // unknown → default
	}
	for _, tc := range tests {
		got := parseBool(tc.input, tc.def)
		if got != tc.want {
			t.Errorf("parseBool(%q, %v) = %v, want %v", tc.input, tc.def, got, tc.want)
		}
	}
}

func TestApplyDefaults_AlreadySet(t *testing.T) {
	cfg := &Config{
		ExternalInterface: "em0",
		FirewallType:      "ipfw",
		DefaultGateway:    "10.0.0.1",
		NATNetwork:        "192.168.0.0/24",
		BridgePrefix:      "br",
		PFAnchorName:      "myfirewall",
	}
	applyDefaults(cfg)
	if cfg.ExternalInterface != "em0" {
		t.Error("applyDefaults should not overwrite ExternalInterface")
	}
	if cfg.FirewallType != "ipfw" {
		t.Error("applyDefaults should not overwrite FirewallType")
	}
	if cfg.DefaultGateway != "10.0.0.1" {
		t.Error("applyDefaults should not overwrite DefaultGateway")
	}
	if cfg.NATNetwork != "192.168.0.0/24" {
		t.Error("applyDefaults should not overwrite NATNetwork")
	}
	if cfg.BridgePrefix != "br" {
		t.Error("applyDefaults should not overwrite BridgePrefix")
	}
	if cfg.PFAnchorName != "myfirewall" {
		t.Error("applyDefaults should not overwrite PFAnchorName")
	}
}

// TestLoadConfig_AllowedPhysicalDisks covers the setting that grants raw access
// to a host device. Passthrough is default-deny, so without a way to name a
// device in the configuration the feature cannot be used at all.
func TestLoadConfig_AllowedPhysicalDisks(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{name: "absent means nothing is permitted", value: "", want: nil},
		{name: "one device", value: "allowed_physical_disks = /dev/nda0", want: []string{"/dev/nda0"}},
		{
			name:  "several devices, whitespace separated",
			value: "allowed_physical_disks = /dev/nda0 /dev/nda1",
			want:  []string{"/dev/nda0", "/dev/nda1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hospitusd.conf")
			if err := os.WriteFile(path, []byte(tt.value+"\n"), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}

			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if len(cfg.AllowedPhysicalDisks) != len(tt.want) {
				t.Fatalf("got %v, want %v", cfg.AllowedPhysicalDisks, tt.want)
			}
			for i, d := range tt.want {
				if cfg.AllowedPhysicalDisks[i] != d {
					t.Errorf("device %d = %q, want %q", i, cfg.AllowedPhysicalDisks[i], d)
				}
			}
		})
	}
}
