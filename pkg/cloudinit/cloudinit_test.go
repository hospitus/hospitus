package cloudinit

import (
	"strings"
	"testing"
)

// testGenerator creates a Generator without requiring mkisofs on PATH
func testGenerator() *Generator {
	return &Generator{mkisofsBin: "/usr/bin/mkisofs"}
}

// --- ConfigFromSpec ---

func TestConfigFromSpec_NoKeys(t *testing.T) {
	cfg := ConfigFromSpec("id1", "myhost", nil, nil)
	if cfg.InstanceID != "id1" {
		t.Errorf("expected InstanceID=id1, got %q", cfg.InstanceID)
	}
	if cfg.LocalHostname != "myhost" {
		t.Errorf("expected LocalHostname=myhost, got %q", cfg.LocalHostname)
	}
	if len(cfg.Users) != 0 {
		t.Errorf("expected no users when no SSH keys, got %d", len(cfg.Users))
	}
}

func TestConfigFromSpec_WithSSHKeys(t *testing.T) {
	keys := []string{"ssh-rsa AAAA... user@host"}
	cfg := ConfigFromSpec("id2", "web01", []NetworkConfig{{Name: "eth0", Type: "dhcp"}}, keys)
	if len(cfg.Users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(cfg.Users))
	}
	u := cfg.Users[0]
	if u.Name != "hospitus" {
		t.Errorf("expected user=hospitus, got %q", u.Name)
	}
	if !u.LockPasswd {
		t.Error("expected LockPasswd=true")
	}
	if len(u.SSHAuthorizedKeys) != 1 {
		t.Errorf("expected 1 SSH key on user, got %d", len(u.SSHAuthorizedKeys))
	}
	if len(cfg.SSHAuthorizedKeys) != 1 {
		t.Errorf("expected 1 SSH key on config, got %d", len(cfg.SSHAuthorizedKeys))
	}
	if len(cfg.Networks) != 1 {
		t.Errorf("expected 1 network, got %d", len(cfg.Networks))
	}
}

// --- NetworkFromProviderSpec ---

func TestNetworkFromProviderSpec_DHCP(t *testing.T) {
	nc := NetworkFromProviderSpec("eth0", "", "", nil, "")
	if nc.Type != "dhcp" {
		t.Errorf("empty IP should be dhcp, got %q", nc.Type)
	}
	if nc.Name != "eth0" {
		t.Errorf("expected name=eth0, got %q", nc.Name)
	}
}

func TestNetworkFromProviderSpec_DHCPExplicit(t *testing.T) {
	nc := NetworkFromProviderSpec("eth0", "dhcp", "", nil, "")
	if nc.Type != "dhcp" {
		t.Errorf("'dhcp' IP should be dhcp type, got %q", nc.Type)
	}
}

func TestNetworkFromProviderSpec_Static(t *testing.T) {
	nc := NetworkFromProviderSpec("eth0", "10.0.0.10/24", "10.0.0.1", []string{"8.8.8.8"}, "aa:bb:cc:dd:ee:ff")
	if nc.Type != "static" {
		t.Errorf("expected static, got %q", nc.Type)
	}
	if nc.Address != "10.0.0.10/24" {
		t.Errorf("unexpected address: %q", nc.Address)
	}
	if nc.Gateway != "10.0.0.1" {
		t.Errorf("unexpected gateway: %q", nc.Gateway)
	}
	if len(nc.DNS) != 1 || nc.DNS[0] != "8.8.8.8" {
		t.Errorf("unexpected DNS: %v", nc.DNS)
	}
	if nc.MACAddress != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("unexpected MAC: %q", nc.MACAddress)
	}
}

// --- generateMetaData ---

func TestGenerateMetaData(t *testing.T) {
	g := testGenerator()
	cfg := &Config{InstanceID: "hospitus-001", LocalHostname: "web01"}
	out, err := g.generateMetaData(cfg)
	if err != nil {
		t.Fatalf("generateMetaData: %v", err)
	}
	if !strings.Contains(out, `instance-id: "hospitus-001"`) {
		t.Errorf("missing instance-id in meta-data: %q", out)
	}
	if !strings.Contains(out, `local-hostname: "web01"`) {
		t.Errorf("missing local-hostname in meta-data: %q", out)
	}
}

// --- generateUserData ---

func TestGenerateUserData_Minimal(t *testing.T) {
	g := testGenerator()
	cfg := &Config{}
	out, err := g.generateUserData(cfg)
	if err != nil {
		t.Fatalf("generateUserData: %v", err)
	}
	if !strings.HasPrefix(out, "#cloud-config") {
		n := min(len(out), 20)
		t.Errorf("expected #cloud-config header, got %q", out[:n])
	}
}

func TestGenerateUserData_WithUsers(t *testing.T) {
	g := testGenerator()
	cfg := &Config{
		Users: []UserConfig{
			{
				Name:              "admin",
				Groups:            []string{"wheel", "sudo"},
				Shell:             "/bin/bash",
				Sudo:              "ALL=(ALL) NOPASSWD:ALL",
				SSHAuthorizedKeys: []string{"ssh-rsa AAAA..."},
				LockPasswd:        true,
			},
		},
	}
	out, err := g.generateUserData(cfg)
	if err != nil {
		t.Fatalf("generateUserData with users: %v", err)
	}
	if !strings.Contains(out, `name: "admin"`) {
		t.Errorf("missing user name: %q", out)
	}
	if !strings.Contains(out, `shell: "/bin/bash"`) {
		t.Errorf("missing shell: %q", out)
	}
	if !strings.Contains(out, "lock_passwd: true") {
		t.Errorf("missing lock_passwd: %q", out)
	}
}

func TestGenerateUserData_WithPackagesAndRunCMD(t *testing.T) {
	g := testGenerator()
	cfg := &Config{
		Packages: []string{"nginx", "curl"},
		RunCMD:   []string{"systemctl enable nginx", "systemctl start nginx"},
	}
	out, err := g.generateUserData(cfg)
	if err != nil {
		t.Fatalf("generateUserData: %v", err)
	}
	if !strings.Contains(out, `- "nginx"`) {
		t.Errorf("missing nginx package: %q", out)
	}
	if !strings.Contains(out, "systemctl enable nginx") {
		t.Errorf("missing runcmd: %q", out)
	}
}

func TestGenerateUserData_WithSSHKeys(t *testing.T) {
	g := testGenerator()
	cfg := &Config{
		SSHAuthorizedKeys: []string{"ssh-rsa BBBBB..."},
	}
	out, err := g.generateUserData(cfg)
	if err != nil {
		t.Fatalf("generateUserData: %v", err)
	}
	if !strings.Contains(out, "ssh_authorized_keys") {
		t.Errorf("missing ssh_authorized_keys: %q", out)
	}
}

func TestGenerateUserData_WithCustomData(t *testing.T) {
	g := testGenerator()
	cfg := &Config{
		CustomUserData: "write_files:\n  - path: /etc/myapp.conf\n",
	}
	out, err := g.generateUserData(cfg)
	if err != nil {
		t.Fatalf("generateUserData: %v", err)
	}
	if !strings.Contains(out, "write_files") {
		t.Errorf("missing custom user data: %q", out)
	}
}

func TestGenerateUserData_WithDoasAndPlainPasswd(t *testing.T) {
	g := testGenerator()
	cfg := &Config{
		Users: []UserConfig{
			{
				Name:            "bsduser",
				Doas:            "permit nopass bsduser as root",
				PlainTextPasswd: "secret123",
			},
		},
	}
	out, err := g.generateUserData(cfg)
	if err != nil {
		t.Fatalf("generateUserData: %v", err)
	}
	if !strings.Contains(out, "doas:") {
		t.Errorf("missing doas in user-data: %q", out)
	}
	if !strings.Contains(out, "plain_text_passwd:") {
		t.Errorf("missing plain_text_passwd in user-data: %q", out)
	}
}

// --- generateNetworkConfig ---

func TestGenerateNetworkConfig_DHCP(t *testing.T) {
	g := testGenerator()
	cfg := &Config{
		Networks: []NetworkConfig{
			{Name: "eth0", Type: "dhcp"},
		},
	}
	out, err := g.generateNetworkConfig(cfg)
	if err != nil {
		t.Fatalf("generateNetworkConfig: %v", err)
	}
	if !strings.Contains(out, "version: 2") {
		t.Errorf("missing version: 2: %q", out)
	}
	if !strings.Contains(out, "dhcp4: true") {
		t.Errorf("missing dhcp4: true: %q", out)
	}
}

func TestGenerateNetworkConfig_Static(t *testing.T) {
	g := testGenerator()
	cfg := &Config{
		Networks: []NetworkConfig{
			{
				Name:       "eth0",
				Type:       "static",
				Address:    "10.0.0.10/24",
				Gateway:    "10.0.0.1",
				DNS:        []string{"8.8.8.8", "8.8.4.4"},
				MACAddress: "aa:bb:cc:dd:ee:ff",
			},
		},
	}
	out, err := g.generateNetworkConfig(cfg)
	if err != nil {
		t.Fatalf("generateNetworkConfig: %v", err)
	}
	if !strings.Contains(out, "dhcp4: false") {
		t.Errorf("missing dhcp4: false: %q", out)
	}
	if !strings.Contains(out, "10.0.0.10/24") {
		t.Errorf("missing static address: %q", out)
	}
	if !strings.Contains(out, `via: "10.0.0.1"`) {
		t.Errorf("missing gateway: %q", out)
	}
	if !strings.Contains(out, "8.8.8.8") {
		t.Errorf("missing DNS: %q", out)
	}
	if !strings.Contains(out, `macaddress: "aa:bb:cc:dd:ee:ff"`) {
		t.Errorf("missing MAC match: %q", out)
	}
}

func TestGenerateNetworkConfig_Empty(t *testing.T) {
	g := testGenerator()
	cfg := &Config{}
	out, err := g.generateNetworkConfig(cfg)
	if err != nil {
		t.Fatalf("generateNetworkConfig empty: %v", err)
	}
	if !strings.Contains(out, "version: 2") {
		t.Errorf("expected version: 2 even for empty config: %q", out)
	}
}

// TestUserDataQuotesValuesFromTheManifest covers the injection the quoting
// exists to stop: a newline in a value would otherwise close the scalar and let
// the manifest add cloud-config keys the guest then executes.
func TestUserDataQuotesValuesFromTheManifest(t *testing.T) {
	g := testGenerator()
	cfg := &Config{
		Users: []UserConfig{{
			Name:  "admin\nruncmd:\n  - touch /tmp/pwned",
			Shell: "/bin/sh",
		}},
	}

	out, err := g.generateUserData(cfg)
	if err != nil {
		t.Fatalf("generateUserData: %v", err)
	}

	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "runcmd:") {
			t.Fatalf("the name injected a top-level key:\n%s", out)
		}
	}
	if !strings.Contains(out, `\n`) {
		t.Errorf("the newline was not escaped:\n%s", out)
	}
}

// TestDoasRuleSubstitutesTheUser covers the placeholder UserConfig.Doas
// documents. Writing the rule literally grants nothing to the user it names.
func TestDoasRuleSubstitutesTheUser(t *testing.T) {
	g := testGenerator()
	cfg := &Config{
		Users: []UserConfig{{Name: "operator", Doas: "permit nopass %u as root"}},
	}

	out, err := g.generateUserData(cfg)
	if err != nil {
		t.Fatalf("generateUserData: %v", err)
	}

	if !strings.Contains(out, "permit nopass operator as root") {
		t.Errorf("%%u was not replaced with the user name:\n%s", out)
	}
	if strings.Contains(out, "%u") {
		t.Errorf("the placeholder reached the guest:\n%s", out)
	}
}
