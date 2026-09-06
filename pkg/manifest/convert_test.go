package manifest

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/hospitus/hospitus/pkg/cloudinit"
	"github.com/hospitus/hospitus/pkg/provider"
)

// Tests for convert.go pure functions that don't require OS access or external deps.

func TestNewConverter(t *testing.T) {
	c := NewConverter()
	if c == nil {
		t.Fatal("expected non-nil Converter")
	}
}

// ── convertImageReference ─────────────────────────────────────────────────────

func TestConvertImageReference(t *testing.T) {
	c := NewConverter()
	tests := []struct {
		imageType string
		reference string
		want      string
	}{
		{ImageTypeFreeBSD, "14.3-RELEASE", "14.3-RELEASE"},
		{ImageTypeOCI, "nginx:latest", "nginx:latest"},
		{ImageTypeCloud, "ubuntu-24.04", "ubuntu-24.04"},
		{ImageTypeISO, "FreeBSD-14.3-dvd1", "FreeBSD-14.3-dvd1"},
		{"unknown", "whatever", "whatever"},
	}
	for _, tt := range tests {
		got := c.convertImageReference(tt.imageType, tt.reference)
		if got != tt.want {
			t.Errorf("convertImageReference(%q, %q) = %q, want %q", tt.imageType, tt.reference, got, tt.want)
		}
	}
}

// ── parseCloudImage ───────────────────────────────────────────────────────────

func TestParseCloudImage(t *testing.T) {
	c := NewConverter()
	tests := []struct {
		input   string
		wantOS  string
		wantVer string
	}{
		{"ubuntu-24.04", "ubuntu", "24.04"},
		{"freebsd-14.1", "freebsd", "14.1"},
		{"centos-stream-9", "centos", "stream-9"},
		{"alpine", "alpine", ""},
	}
	for _, tt := range tests {
		os, ver := c.parseCloudImage(tt.input)
		if os != tt.wantOS || ver != tt.wantVer {
			t.Errorf("parseCloudImage(%q) = (%q, %q), want (%q, %q)",
				tt.input, os, ver, tt.wantOS, tt.wantVer)
		}
	}
}

// ── convertNetwork ────────────────────────────────────────────────────────────

func TestConvertNetwork_Bridge(t *testing.T) {
	c := NewConverter()
	net := NetworkSpec{
		Name:   "eth0",
		Type:   NetworkTypeBridge,
		Bridge: "hospitus0",
	}
	pNet, err := c.convertNetwork(net, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pNet.Bridge != "hospitus0" {
		t.Errorf("Bridge: got %q, want %q", pNet.Bridge, "hospitus0")
	}
}

func TestConvertNetwork_NAT(t *testing.T) {
	c := NewConverter()
	net := NetworkSpec{Type: NetworkTypeNAT}
	pNet, err := c.convertNetwork(net, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pNet.Type == "" {
		t.Error("expected non-empty network type for NAT")
	}
}

func TestConvertNetwork_NoneType(t *testing.T) {
	c := NewConverter()
	net := NetworkSpec{Type: NetworkTypeNone}
	_, err := c.convertNetwork(net, 0)
	if err != nil {
		t.Fatalf("unexpected error for 'none' type: %v", err)
	}
}

func TestConvertNetwork_UnknownType(t *testing.T) {
	c := NewConverter()
	net := NetworkSpec{Type: "bogus-type"}
	_, err := c.convertNetwork(net, 0)
	if err == nil {
		t.Error("expected error for unknown network type")
	}
}

func TestConvertNetwork_DHCPDefault(t *testing.T) {
	c := NewConverter()
	net := NetworkSpec{
		Type: NetworkTypeBridge,
		IP:   &IPConfig{Mode: IPModeDHCP},
	}
	pNet, err := c.convertNetwork(net, 0)
	if err != nil {
		t.Fatal(err)
	}
	if pNet.IPv4 != "dhcp" {
		t.Errorf("expected IPv4='dhcp', got %q", pNet.IPv4)
	}
}

func TestConvertNetwork_StaticIP(t *testing.T) {
	c := NewConverter()
	net := NetworkSpec{
		Type: NetworkTypeBridge,
		IP:   &IPConfig{Mode: IPModeStatic, Address: "192.168.1.10/24"},
	}
	pNet, err := c.convertNetwork(net, 0)
	if err != nil {
		t.Fatal(err)
	}
	if pNet.IPv4 != "192.168.1.10/24" {
		t.Errorf("expected IPv4='192.168.1.10/24', got %q", pNet.IPv4)
	}
}

func TestConvertNetwork_NoIPConfig(t *testing.T) {
	c := NewConverter()
	// Empty IP — default mode is treated as DHCP
	net := NetworkSpec{
		Type: NetworkTypeBridge,
		IP:   &IPConfig{},
	}
	pNet, err := c.convertNetwork(net, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Empty mode falls through to dhcp branch
	if pNet.IPv4 != "dhcp" {
		t.Errorf("expected IPv4='dhcp' for empty mode, got %q", pNet.IPv4)
	}
}

// ── GetMounts ─────────────────────────────────────────────────────────────────

func TestGetMounts_Empty(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{}
	mounts := c.GetMounts(m)
	if len(mounts) != 0 {
		t.Errorf("expected 0 mounts, got %d", len(mounts))
	}
}

func TestGetMounts_WithHostPath(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{
		Storage: StorageSpec{
			Volumes: []VolumeSpec{
				{Name: "data", HostPath: "/host/data", MountPath: "/data", ReadOnly: true},
				{Name: "nohostpath", MountPath: "/tmp"}, // no HostPath — should be skipped
			},
		},
	}
	mounts := c.GetMounts(m)
	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(mounts))
	}
	if mounts[0].HostPath != "/host/data" {
		t.Errorf("HostPath: got %q, want /host/data", mounts[0].HostPath)
	}
	if !mounts[0].ReadOnly {
		t.Error("expected ReadOnly=true")
	}
}

// ── GetPortForwards ───────────────────────────────────────────────────────────

func TestGetPortForwards_Empty(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{}
	fwds := c.GetPortForwards(m)
	if len(fwds) != 0 {
		t.Errorf("expected 0 forwards, got %d", len(fwds))
	}
}

func TestGetPortForwards_MultipleNetworks(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{
		Networks: []NetworkSpec{
			{
				Ports: []PortConfig{
					{Host: 8080, Container: 80, Protocol: "tcp"},
					{Host: 4433, Container: 443, Protocol: "tcp"},
				},
			},
			{
				Ports: []PortConfig{
					{Host: 5353, Container: 53, Protocol: "udp"},
				},
			},
		},
	}
	fwds := c.GetPortForwards(m)
	if len(fwds) != 3 {
		t.Fatalf("expected 3 forwards, got %d", len(fwds))
	}
	if fwds[0].HostPort != 8080 || fwds[0].ContainerPort != 80 {
		t.Errorf("first forward: got %d->%d, want 8080->80", fwds[0].HostPort, fwds[0].ContainerPort)
	}
	if fwds[2].Protocol != "udp" {
		t.Errorf("third forward protocol: got %q, want 'udp'", fwds[2].Protocol)
	}
}

// ── ToInstanceSpec ────────────────────────────────────────────────────────────

func TestToInstanceSpec_Basic(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{
		Workload: WorkloadMeta{Name: "myapp"},
		Provider: ProviderSpec{Type: ProviderTypePodman},
		Image:    ImageSpec{Source: "oci:nginx:latest"},
		Resources: ResourceSpec{
			CPU:    2,
			Memory: "512Mi",
		},
	}
	spec, err := c.ToInstanceSpec(m)
	if err != nil {
		t.Fatalf("ToInstanceSpec: %v", err)
	}
	if spec.Name != "myapp" {
		t.Errorf("Name: got %q, want 'myapp'", spec.Name)
	}
	if spec.CPUs != 2 {
		t.Errorf("CPUs: got %d, want 2", spec.CPUs)
	}
	if spec.MemoryMB != 512 {
		t.Errorf("MemoryMB: got %d, want 512", spec.MemoryMB)
	}
}

func TestToInstanceSpec_InvalidMemory(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{
		Workload: WorkloadMeta{Name: "bad"},
		Provider: ProviderSpec{Type: ProviderTypeJail},
		Image:    ImageSpec{Source: "freebsd:14.3-RELEASE"},
		Resources: ResourceSpec{
			Memory: "notmemory",
		},
	}
	_, err := c.ToInstanceSpec(m)
	if err == nil {
		t.Error("expected error for invalid memory size")
	}
}

func TestToInstanceSpec_InvalidImageSource(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{
		Workload: WorkloadMeta{Name: "bad"},
		Provider: ProviderSpec{Type: ProviderTypeQEMU},
		Image:    ImageSpec{Source: "bad-source-no-colon"},
	}
	_, err := c.ToInstanceSpec(m)
	if err == nil {
		t.Error("expected error for invalid image source")
	}
}

// Convenience function test
func TestToInstanceSpecConvenience(t *testing.T) {
	m := &WorkloadManifest{
		Workload: WorkloadMeta{Name: "conv"},
		Provider: ProviderSpec{Type: ProviderTypePodman},
		Image:    ImageSpec{Source: "oci:alpine:latest"},
	}
	spec, err := ToInstanceSpec(m)
	if err != nil {
		t.Fatalf("ToInstanceSpec convenience: %v", err)
	}
	if spec.Name != "conv" {
		t.Errorf("Name: got %q", spec.Name)
	}
}

// --- convertStorage tests ---

func TestConvertStorage_Nil(t *testing.T) {
	c := NewConverter()
	disks, err := c.convertStorage(StorageSpec{})
	if err != nil {
		t.Fatalf("convertStorage empty: %v", err)
	}
	if len(disks) != 0 {
		t.Errorf("expected 0 disks, got %d", len(disks))
	}
}

func TestConvertStorage_RootDiskZVOL(t *testing.T) {
	c := NewConverter()
	disks, err := c.convertStorage(StorageSpec{
		RootDisk: &RootDiskSpec{
			Type: DiskTypeZVOL,
			Size: "10Gi",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(disks))
	}
	if !disks[0].Bootable {
		t.Error("root disk should be bootable")
	}
	if disks[0].SizeGB != 10 {
		t.Errorf("expected SizeGB=10, got %d", disks[0].SizeGB)
	}
}

func TestConvertStorage_RootDiskQCOW2(t *testing.T) {
	c := NewConverter()
	disks, err := c.convertStorage(StorageSpec{
		RootDisk: &RootDiskSpec{
			Type: DiskTypeQCOW2,
			Size: "20G",
		},
	})
	if err != nil || len(disks) != 1 {
		t.Fatalf("err=%v disks=%d", err, len(disks))
	}
}

func TestConvertStorage_RootDiskRaw(t *testing.T) {
	c := NewConverter()
	disks, err := c.convertStorage(StorageSpec{
		RootDisk: &RootDiskSpec{Type: DiskTypeRaw, Size: "5G"},
	})
	if err != nil || len(disks) != 1 {
		t.Fatalf("err=%v disks=%d", err, len(disks))
	}
}

func TestConvertStorage_RootDiskPhysical(t *testing.T) {
	c := NewConverter()
	disks, err := c.convertStorage(StorageSpec{
		RootDisk: &RootDiskSpec{Type: DiskTypePhysical, Path: "/dev/da0"},
	})
	if err != nil || len(disks) != 1 {
		t.Fatalf("err=%v disks=%d", err, len(disks))
	}
	if disks[0].Path != "/dev/da0" {
		t.Errorf("expected path /dev/da0, got %q", disks[0].Path)
	}
}

func TestConvertStorage_RootDiskAuto(t *testing.T) {
	c := NewConverter()
	disks, err := c.convertStorage(StorageSpec{
		RootDisk: &RootDiskSpec{Type: DiskTypeAuto},
	})
	if err != nil || len(disks) != 1 {
		t.Fatalf("err=%v disks=%d", err, len(disks))
	}
	if disks[0].Type != "" {
		t.Errorf("auto type should remain empty, got %q", disks[0].Type)
	}
}

func TestConvertStorage_RootDiskInvalidSize(t *testing.T) {
	c := NewConverter()
	_, err := c.convertStorage(StorageSpec{
		RootDisk: &RootDiskSpec{Type: DiskTypeZVOL, Size: "invalid"},
	})
	if err == nil {
		t.Error("expected error for invalid root disk size")
	}
}

func TestConvertStorage_VolumeWithSize(t *testing.T) {
	c := NewConverter()
	disks, err := c.convertStorage(StorageSpec{
		Volumes: []VolumeSpec{
			{Name: "data", Size: "5G"},
			{Name: "logs", HostPath: "/var/logs"}, // should be skipped
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only "data" should be in disks (logs has HostPath)
	if len(disks) != 1 {
		t.Errorf("expected 1 disk (host path skipped), got %d", len(disks))
	}
	if disks[0].ID != "data" {
		t.Errorf("expected id=data, got %q", disks[0].ID)
	}
}

func TestConvertStorage_VolumeInvalidSize(t *testing.T) {
	c := NewConverter()
	_, err := c.convertStorage(StorageSpec{
		Volumes: []VolumeSpec{{Name: "v", Size: "bad"}},
	})
	if err == nil {
		t.Error("expected error for invalid volume size")
	}
}

// --- NoopSecretStore tests ---

func TestNoopSecretStore_Get(t *testing.T) {
	s := NewNoopSecretStore()
	val, err := s.Get("myapp", "db-pass")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "PLACEHOLDER-myapp-db-pass" {
		t.Errorf("unexpected value: %q", val)
	}
}

func TestNoopSecretStore_Rotate(t *testing.T) {
	s := NewNoopSecretStore()
	v1, _ := s.Get("myapp", "key")
	v2, err := s.Rotate("myapp", "key")
	if err != nil || v1 != v2 {
		t.Errorf("Rotate should return same placeholder: v1=%q v2=%q err=%v", v1, v2, err)
	}
}

func TestNoopSecretStore_Remove(t *testing.T) {
	s := NewNoopSecretStore()
	if err := s.Remove("myapp", "key"); err != nil {
		t.Fatalf("Remove should be no-op: %v", err)
	}
}

func TestNoopSecretStore_List(t *testing.T) {
	s := NewNoopSecretStore()
	list, err := s.List("myapp")
	if err != nil || len(list) != 0 {
		t.Fatalf("List should return empty slice: err=%v list=%v", err, list)
	}
}

// --- ValidationError / ValidationErrors tests ---

func TestValidationError_Error(t *testing.T) {
	e := &ValidationError{Field: "name", Message: "must not be empty"}
	if e.Error() != "name: must not be empty" {
		t.Errorf("unexpected: %q", e.Error())
	}
}

func TestValidationErrors_Empty(t *testing.T) {
	var errs ValidationErrors
	if errs.Error() != "no errors" {
		t.Errorf("unexpected: %q", errs.Error())
	}
	if errs.HasErrors() {
		t.Error("empty ValidationErrors should not have errors")
	}
	if errs.HasWarnings() {
		t.Error("HasWarnings should return false")
	}
	// Errors() returns self (nil for empty slice) — just verify it doesn't panic
	_ = errs.Errors()
	// Warnings() always returns nil — just verify it doesn't panic
	_ = errs.Warnings()
}

func TestValidationErrors_Single(t *testing.T) {
	errs := ValidationErrors{{Field: "cpu", Message: "must be > 0"}}
	if errs.Error() != "cpu: must be > 0" {
		t.Errorf("unexpected: %q", errs.Error())
	}
}

func TestValidationErrors_Multiple(t *testing.T) {
	errs := ValidationErrors{
		{Field: "cpu", Message: "must be > 0"},
		{Field: "mem", Message: "must be > 0"},
	}
	msg := errs.Error()
	if msg == "" || msg == "no errors" {
		t.Errorf("unexpected: %q", msg)
	}
}

// --- ParsedManifest.GetName tests ---

func TestParsedManifest_GetName_Workload(t *testing.T) {
	pm := &ParsedManifest{
		Workload: &WorkloadManifest{
			Workload: WorkloadMeta{Name: "myjail"},
		},
	}
	if pm.GetName() != "myjail" {
		t.Errorf("expected 'myjail', got %q", pm.GetName())
	}
}

func TestParsedManifest_GetName_Stack(t *testing.T) {
	pm := &ParsedManifest{
		Stack: &StackManifest{
			Stack: StackMeta{Name: "mystack"},
		},
	}
	if pm.GetName() != "mystack" {
		t.Errorf("expected 'mystack', got %q", pm.GetName())
	}
}

func TestParsedManifest_GetName_Empty(t *testing.T) {
	pm := &ParsedManifest{}
	if pm.GetName() != "" {
		t.Errorf("expected empty, got %q", pm.GetName())
	}
}

// --- generateRandomString via template funcs ---

func TestTemplateRandHex(t *testing.T) {
	tmpl := []byte(`{{ randHex 8 }}`)
	result, err := RenderTemplate(tmpl, nil, nil, "")
	if err != nil {
		t.Fatalf("RenderTemplate randHex: %v", err)
	}
	if len(result) != 8 {
		t.Errorf("expected 8 hex chars, got %d: %q", len(result), result)
	}
}

func TestTemplateRandAlnum(t *testing.T) {
	tmpl := []byte(`{{ randAlnum 12 }}`)
	result, err := RenderTemplate(tmpl, nil, nil, "")
	if err != nil {
		t.Fatalf("RenderTemplate randAlnum: %v", err)
	}
	if len(result) != 12 {
		t.Errorf("expected 12 alnum chars, got %d: %q", len(result), result)
	}
}

func TestTemplateSecret_NoopStore(t *testing.T) {
	store := NewNoopSecretStore()
	tmpl := []byte(`{{ secret "mykey" }}`)
	result, err := RenderTemplate(tmpl, nil, store, "myapp")
	if err != nil {
		t.Fatalf("RenderTemplate secret: %v", err)
	}
	if string(result) != "PLACEHOLDER-myapp-mykey" {
		t.Errorf("unexpected: %q", result)
	}
}

func TestTemplateSecret_NoScope(t *testing.T) {
	store := NewNoopSecretStore()
	tmpl := []byte(`{{ secret "mykey" }}`)
	_, err := RenderTemplate(tmpl, nil, store, "")
	if err == nil {
		t.Error("expected error when scope is empty")
	}
}

// ── generateCloudInitYAML ─────────────────────────────────────────────────────

func TestGenerateCloudInitYAML_Minimal(t *testing.T) {
	c := NewConverter()
	ciConfig := &cloudinit.Config{
		InstanceID:    "testvm",
		LocalHostname: "testvm",
	}
	spec := &provider.InstanceSpec{Name: "testvm", OSType: "linux"}
	c.generateCloudInitYAML(ciConfig, spec)
	if spec.CloudInit == nil {
		t.Fatal("expected CloudInit to be set")
	}
	if !strings.HasPrefix(spec.CloudInit.UserData, "#cloud-config\n") {
		t.Errorf("UserData should start with #cloud-config, got: %s", spec.CloudInit.UserData)
	}
	if !strings.Contains(spec.CloudInit.MetaData, "testvm") {
		t.Errorf("MetaData missing instance ID: %s", spec.CloudInit.MetaData)
	}
}

func TestGenerateCloudInitYAML_WithUsers(t *testing.T) {
	c := NewConverter()
	ciConfig := &cloudinit.Config{
		InstanceID:    "vm1",
		LocalHostname: "vm1",
		Users: []cloudinit.UserConfig{
			{
				Name:              "admin",
				Groups:            []string{"wheel", "sudo"},
				Shell:             "/bin/bash",
				Sudo:              "ALL=(ALL) NOPASSWD:ALL",
				Doas:              "permit nopass admin",
				PlainTextPasswd:   "secret123",
				LockPasswd:        false,
				SSHAuthorizedKeys: []string{"ssh-rsa AAAA... user@host"},
			},
			{
				Name:       "locked",
				LockPasswd: true,
			},
		},
	}
	spec := &provider.InstanceSpec{Name: "vm1", OSType: "linux"}
	c.generateCloudInitYAML(ciConfig, spec)
	ud := spec.CloudInit.UserData
	if !strings.Contains(ud, "users:") {
		t.Error("missing users section")
	}
	if !strings.Contains(ud, "name: admin") {
		t.Error("missing admin user")
	}
	if !strings.Contains(ud, "groups: wheel, sudo") {
		t.Error("missing groups")
	}
	if !strings.Contains(ud, "sudo: ALL=(ALL) NOPASSWD:ALL") {
		t.Error("missing sudo")
	}
	if !strings.Contains(ud, "doas: permit nopass admin") {
		t.Error("missing doas")
	}
	if !strings.Contains(ud, "plain_text_passwd: secret123") {
		t.Error("missing plain_text_passwd")
	}
	if !strings.Contains(ud, "lock_passwd: false") {
		t.Error("missing lock_passwd: false for unlocked user")
	}
	if !strings.Contains(ud, "lock_passwd: true") {
		t.Error("missing lock_passwd: true for locked user")
	}
	if !strings.Contains(ud, "ssh_authorized_keys:") {
		t.Error("missing ssh_authorized_keys under user")
	}
}

func TestGenerateCloudInitYAML_WithPackagesAndRunCMD(t *testing.T) {
	c := NewConverter()
	ciConfig := &cloudinit.Config{
		InstanceID:    "vm2",
		LocalHostname: "vm2",
		Packages:      []string{"vim", "curl"},
		RunCMD:        []string{"echo hello", "touch /tmp/done"},
	}
	spec := &provider.InstanceSpec{Name: "vm2", OSType: "linux"}
	c.generateCloudInitYAML(ciConfig, spec)
	ud := spec.CloudInit.UserData
	if !strings.Contains(ud, "packages:\n") {
		t.Error("missing packages section")
	}
	if !strings.Contains(ud, "  - vim") || !strings.Contains(ud, "  - curl") {
		t.Error("missing package entries")
	}
	if !strings.Contains(ud, "runcmd:\n") {
		t.Error("missing runcmd section")
	}
	if !strings.Contains(ud, "  - echo hello") {
		t.Error("missing runcmd entry")
	}
}

func TestGenerateCloudInitYAML_GlobalSSHKeys(t *testing.T) {
	c := NewConverter()
	ciConfig := &cloudinit.Config{
		InstanceID:        "vm3",
		LocalHostname:     "vm3",
		SSHAuthorizedKeys: []string{"ssh-ed25519 AAAA... test"},
	}
	spec := &provider.InstanceSpec{Name: "vm3", OSType: "linux"}
	c.generateCloudInitYAML(ciConfig, spec)
	if !strings.Contains(spec.CloudInit.UserData, "ssh_authorized_keys:\n") {
		t.Error("missing top-level ssh_authorized_keys")
	}
}

func TestGenerateCloudInitYAML_NoYAMLInjection(t *testing.T) {
	c := NewConverter()
	payload := "nginx\nruncmd:\n  - rm -rf /"
	ciConfig := &cloudinit.Config{
		InstanceID:    "vm",
		LocalHostname: "vm",
		Packages:      []string{payload},
	}
	spec := &provider.InstanceSpec{Name: "vm", OSType: "linux"}
	c.generateCloudInitYAML(ciConfig, spec)
	ud := spec.CloudInit.UserData

	// The malicious payload must not become a real top-level runcmd key.
	if strings.Contains(ud, "\nruncmd:\n  - rm -rf /") {
		t.Errorf("YAML injection: payload became a real key:\n%s", ud)
	}

	// Round-trip: the document must parse and preserve the payload as a single
	// package scalar, with no injected runcmd key.
	var parsed struct {
		Packages []string `yaml:"packages"`
		RunCMD   []string `yaml:"runcmd"`
	}
	body := strings.TrimPrefix(ud, "#cloud-config\n")
	if err := yaml.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("generated user-data is not valid YAML: %v", err)
	}
	if len(parsed.RunCMD) != 0 {
		t.Errorf("injection created a runcmd key: %v", parsed.RunCMD)
	}
	if len(parsed.Packages) != 1 || parsed.Packages[0] != payload {
		t.Errorf("package payload not preserved as a single scalar: %#v", parsed.Packages)
	}
}

func TestGenerateCloudInitYAML_CustomUserData(t *testing.T) {
	c := NewConverter()
	ciConfig := &cloudinit.Config{
		InstanceID:     "vm4",
		LocalHostname:  "vm4",
		CustomUserData: "# custom footer\nbootcmd:\n  - echo boot\n",
	}
	spec := &provider.InstanceSpec{Name: "vm4", OSType: "linux"}
	c.generateCloudInitYAML(ciConfig, spec)
	if !strings.Contains(spec.CloudInit.UserData, "custom footer") {
		t.Error("missing custom user data")
	}
}

func TestGenerateCloudInitYAML_StaticNetworkLinux(t *testing.T) {
	c := NewConverter()
	ciConfig := &cloudinit.Config{
		InstanceID:    "vm5",
		LocalHostname: "vm5",
		Networks: []cloudinit.NetworkConfig{
			{Name: "ens3", Type: "static", Address: "10.0.0.5/24", Gateway: "10.0.0.1", DNS: []string{"8.8.8.8"}},
		},
	}
	spec := &provider.InstanceSpec{Name: "vm5", OSType: "ubuntu"}
	c.generateCloudInitYAML(ciConfig, spec)
	nc := spec.CloudInit.Network
	if !strings.Contains(nc, "version: 2") {
		t.Errorf("Linux should use version 2, got: %s", nc)
	}
	if !strings.Contains(nc, "addresses:") {
		t.Error("missing addresses in network config")
	}
	if !strings.Contains(nc, "10.0.0.1") {
		t.Error("missing gateway in network config")
	}
	if !strings.Contains(nc, "8.8.8.8") {
		t.Error("missing DNS in network config")
	}
}

func TestGenerateCloudInitYAML_StaticNetworkFreeBSD(t *testing.T) {
	c := NewConverter()
	ciConfig := &cloudinit.Config{
		InstanceID:    "vm6",
		LocalHostname: "vm6",
		Networks: []cloudinit.NetworkConfig{
			{Name: "vtnet0", Type: "static", Address: "10.0.0.6/24", Gateway: "10.0.0.1", DNS: []string{"1.1.1.1"}},
		},
	}
	spec := &provider.InstanceSpec{Name: "vm6", OSType: "freebsd"}
	c.generateCloudInitYAML(ciConfig, spec)
	nc := spec.CloudInit.Network
	if !strings.Contains(nc, "version: 1") {
		t.Errorf("FreeBSD should use version 1, got: %s", nc)
	}
	if !strings.Contains(nc, "type: physical") {
		t.Error("missing type: physical in FreeBSD network config")
	}
}

func TestGenerateCloudInitYAML_AllDHCP_NoNetworkConfig(t *testing.T) {
	c := NewConverter()
	ciConfig := &cloudinit.Config{
		InstanceID:    "vm7",
		LocalHostname: "vm7",
		Networks: []cloudinit.NetworkConfig{
			{Name: "ens3", Type: "dhcp"},
			{Name: "ens4", Type: "dhcp"},
		},
	}
	spec := &provider.InstanceSpec{Name: "vm7", OSType: "ubuntu"}
	c.generateCloudInitYAML(ciConfig, spec)
	if spec.CloudInit.Network != "" {
		t.Errorf("allDHCP should produce empty network config, got: %s", spec.CloudInit.Network)
	}
}

// ── generateFromCloudInitSpec ─────────────────────────────────────────────────

func TestGenerateFromCloudInitSpec_Basic(t *testing.T) {
	c := NewConverter()
	ciSpec := &CloudInitSpec{
		Enabled:  boolPtr(true),
		Packages: []string{"nginx"},
		RunCMD:   []string{"systemctl enable nginx"},
	}
	spec := &provider.InstanceSpec{Name: "vm8", OSType: "ubuntu"}
	c.generateFromCloudInitSpec(ciSpec, nil, spec)
	if spec.CloudInit == nil {
		t.Fatal("expected CloudInit to be populated")
	}
	if !strings.Contains(spec.CloudInit.UserData, "nginx") {
		t.Error("missing package in user data")
	}
}

func TestGenerateFromCloudInitSpec_HostnameOverride(t *testing.T) {
	c := NewConverter()
	ciSpec := &CloudInitSpec{
		Enabled:  boolPtr(true),
		Hostname: "custom-host",
	}
	spec := &provider.InstanceSpec{Name: "vm9", OSType: "ubuntu"}
	c.generateFromCloudInitSpec(ciSpec, nil, spec)
	if !strings.Contains(spec.CloudInit.MetaData, "custom-host") {
		t.Errorf("hostname override not applied: %s", spec.CloudInit.MetaData)
	}
}

func TestGenerateFromCloudInitSpec_WithUsers(t *testing.T) {
	c := NewConverter()
	ciSpec := &CloudInitSpec{
		Enabled: boolPtr(true),
		Users: []CloudInitUser{
			{Name: "deploy", Groups: []string{"docker"}, Shell: "/bin/sh", Sudo: "ALL=(ALL) NOPASSWD:ALL"},
		},
	}
	spec := &provider.InstanceSpec{Name: "vm10", OSType: "linux"}
	c.generateFromCloudInitSpec(ciSpec, nil, spec)
	if !strings.Contains(spec.CloudInit.UserData, "name: deploy") {
		t.Error("missing user in cloud-init output")
	}
}

func TestGenerateFromCloudInitSpec_WithDHCPNetwork(t *testing.T) {
	c := NewConverter()
	ciSpec := &CloudInitSpec{Enabled: boolPtr(true)}
	nets := []NetworkSpec{
		{IP: &IPConfig{Mode: IPModeDHCP}},
	}
	spec := &provider.InstanceSpec{Name: "vm11", OSType: "ubuntu"}
	c.generateFromCloudInitSpec(ciSpec, nets, spec)
	// DHCP network → network config should be empty (all DHCP)
	if spec.CloudInit.Network != "" {
		t.Errorf("expected empty network config for DHCP, got: %s", spec.CloudInit.Network)
	}
}

func TestGenerateFromCloudInitSpec_WithStaticNetwork(t *testing.T) {
	c := NewConverter()
	ciSpec := &CloudInitSpec{Enabled: boolPtr(true)}
	nets := []NetworkSpec{
		{IP: &IPConfig{Mode: IPModeStatic, Address: "192.168.1.10/24", Gateway: "192.168.1.1", DNS: []string{"8.8.8.8"}}},
	}
	spec := &provider.InstanceSpec{Name: "vm12", OSType: "ubuntu"}
	c.generateFromCloudInitSpec(ciSpec, nets, spec)
	if !strings.Contains(spec.CloudInit.Network, "192.168.1.10") {
		t.Errorf("missing static IP in network config: %s", spec.CloudInit.Network)
	}
}

func TestGenerateFromCloudInitSpec_SkipsIPModeNone(t *testing.T) {
	c := NewConverter()
	ciSpec := &CloudInitSpec{Enabled: boolPtr(true)}
	nets := []NetworkSpec{
		{IP: &IPConfig{Mode: IPModeNone}},
	}
	spec := &provider.InstanceSpec{Name: "vm13", OSType: "ubuntu"}
	c.generateFromCloudInitSpec(ciSpec, nets, spec)
	// IPModeNone should be skipped — no network entries → no network config
	if spec.CloudInit.Network != "" {
		t.Errorf("expected empty network config for IPModeNone, got: %s", spec.CloudInit.Network)
	}
}

func TestGenerateFromCloudInitSpec_NilNetworkSpec(t *testing.T) {
	c := NewConverter()
	ciSpec := &CloudInitSpec{Enabled: boolPtr(true)}
	nets := []NetworkSpec{
		{IP: nil}, // nil IP → defaults to DHCP
	}
	spec := &provider.InstanceSpec{Name: "vm14", OSType: "ubuntu"}
	c.generateFromCloudInitSpec(ciSpec, nets, spec)
	// nil IP → DHCP → all DHCP → no network config
	if spec.CloudInit.Network != "" {
		t.Errorf("expected empty network config for nil IP spec, got: %s", spec.CloudInit.Network)
	}
}

func TestGenerateFromCloudInitSpec_WithCustomUserData(t *testing.T) {
	c := NewConverter()
	ciSpec := &CloudInitSpec{
		Enabled:  boolPtr(true),
		UserData: "# my custom data\n",
	}
	spec := &provider.InstanceSpec{Name: "vm15", OSType: "ubuntu"}
	c.generateFromCloudInitSpec(ciSpec, nil, spec)
	if !strings.Contains(spec.CloudInit.UserData, "my custom data") {
		t.Error("missing custom user data")
	}
}

// ── convertProviderConfig ─────────────────────────────────────────────────────

func TestConvertProviderConfig_NoOverrides(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{
		Provider: ProviderSpec{Type: "jail"},
	}
	spec := &provider.InstanceSpec{}
	config := c.convertProviderConfig(m, spec)
	if len(config) != 0 {
		t.Errorf("expected empty config for no overrides, got: %v", config)
	}
}

func TestConvertProviderConfig_BhyveOverrides(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{
		Provider: ProviderSpec{Type: "bhyve"},
		ProviderOverrides: map[string]ProviderConfig{
			"bhyve": {
				Bootloader:  "uefi",
				ConsoleType: "vnc",
				VNC:         "0.0.0.0:5900",
				DiskDriver:  "virtio-blk",
				Passthrough: []string{"0/1/0"},
				OSType:      "freebsd",
				OSVersion:   "14.3",
				Parameters:  map[string]interface{}{"custom": "value"},
			},
		},
	}
	spec := &provider.InstanceSpec{}
	config := c.convertProviderConfig(m, spec)
	if config["bootloader"] != "uefi" {
		t.Errorf("missing bootloader: %v", config)
	}
	if config["console_type"] != "vnc" {
		t.Errorf("missing console_type: %v", config)
	}
	if config["vnc"] != "0.0.0.0:5900" {
		t.Errorf("missing vnc: %v", config)
	}
	if config["disk_driver"] != "virtio-blk" {
		t.Errorf("missing disk_driver: %v", config)
	}
	if config["custom"] != "value" {
		t.Errorf("missing custom parameter: %v", config)
	}
	if spec.OSType != "freebsd" {
		t.Errorf("OSType not set: %s", spec.OSType)
	}
	if spec.OSVersion != "14.3" {
		t.Errorf("OSVersion not set: %s", spec.OSVersion)
	}
}

func TestConvertProviderConfig_QEMUOverrides(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{
		Provider: ProviderSpec{Type: "qemu"},
		ProviderOverrides: map[string]ProviderConfig{
			"qemu": {
				Machine: "virt",
				CPU:     "cortex-a72",
			},
		},
	}
	spec := &provider.InstanceSpec{}
	config := c.convertProviderConfig(m, spec)
	if config["machine"] != "virt" {
		t.Errorf("missing machine: %v", config)
	}
	if config["cpu"] != "cortex-a72" {
		t.Errorf("missing cpu: %v", config)
	}
}

func TestConvertProviderConfig_PodmanCommand(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{
		Provider: ProviderSpec{Type: "podman"},
		ProviderOverrides: map[string]ProviderConfig{
			"podman": {
				Command: []string{"nginx", "-g", "daemon off;"},
			},
		},
	}
	spec := &provider.InstanceSpec{}
	config := c.convertProviderConfig(m, spec)
	if cmd, ok := config["command"]; !ok {
		t.Errorf("missing command: %v", config)
	} else if args, ok := cmd.([]string); !ok || len(args) != 3 {
		t.Errorf("unexpected command: %v", cmd)
	}
}

func TestConvertProviderConfig_WithVolumeMounts(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{
		Provider: ProviderSpec{Type: "jail"},
		Storage: StorageSpec{
			Volumes: []VolumeSpec{
				{Name: "data", HostPath: "/srv/data", MountPath: "/data", ReadOnly: false},
				{Name: "nohost"}, // no HostPath → should be skipped
			},
		},
	}
	spec := &provider.InstanceSpec{}
	config := c.convertProviderConfig(m, spec)
	mounts, ok := config["mounts"]
	if !ok {
		t.Fatalf("expected mounts in config, got: %v", config)
	}
	mountList, ok := mounts.([]map[string]interface{})
	if !ok || len(mountList) != 1 {
		t.Errorf("expected 1 mount, got: %v", mounts)
	}
	if mountList[0]["name"] != "data" {
		t.Errorf("unexpected mount name: %v", mountList[0])
	}
}

func TestConvertProviderConfig_WithZFSConfig(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{
		Provider: ProviderSpec{Type: "jail"},
		Storage: StorageSpec{
			Volumes: []VolumeSpec{
				{Name: "zdata", ZFS: &ZFSConfig{Compression: "lz4", Quota: "10G"}},
			},
		},
	}
	spec := &provider.InstanceSpec{}
	config := c.convertProviderConfig(m, spec)
	zfsKey := "zfs_zdata"
	zfsCfg, ok := config[zfsKey]
	if !ok {
		t.Fatalf("expected %q in config, got: %v", zfsKey, config)
	}
	zfsMap, ok := zfsCfg.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map for ZFS config, got: %T", zfsCfg)
	}
	if zfsMap["compression"] != "lz4" {
		t.Errorf("missing compression: %v", zfsMap)
	}
	if zfsMap["quota"] != "10G" {
		t.Errorf("missing quota: %v", zfsMap)
	}
}

// ── generateCloudInitForVM ────────────────────────────────────────────────────

func TestGenerateCloudInitForVM_NotVM(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{
		Workload:  WorkloadMeta{Name: "j1"},
		Provider:  ProviderSpec{Type: "jail"},
		Image:     ImageSpec{Source: "freebsd:14.3-RELEASE"},
		Resources: ResourceSpec{CPU: 1, Memory: "512M"},
	}
	spec := &provider.InstanceSpec{Name: "j1", OSType: "freebsd"}
	c.generateCloudInitForVM(m, spec)
	if spec.CloudInit != nil {
		t.Error("jail provider should not get cloud-init config")
	}
}

func TestGenerateCloudInitForVM_BhyveCloudImage(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{
		Workload:  WorkloadMeta{Name: "vm1"},
		Provider:  ProviderSpec{Type: "bhyve"},
		Image:     ImageSpec{Source: "cloud:ubuntu-24.04"},
		Resources: ResourceSpec{CPU: 2, Memory: "2G"},
	}
	spec := &provider.InstanceSpec{Name: "vm1", OSType: "ubuntu", OSVersion: "24.04"}
	c.generateCloudInitForVM(m, spec)
	if spec.CloudInit == nil {
		t.Error("bhyve + cloud image should generate cloud-init config")
	}
	if !strings.HasPrefix(spec.CloudInit.UserData, "#cloud-config") {
		t.Errorf("unexpected UserData: %s", spec.CloudInit.UserData)
	}
}

func TestGenerateCloudInitForVM_BhyveWithExplicitCloudInit(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{
		Workload:  WorkloadMeta{Name: "vm2"},
		Provider:  ProviderSpec{Type: "bhyve"},
		Image:     ImageSpec{Source: "cloud:ubuntu-24.04"},
		Resources: ResourceSpec{CPU: 1, Memory: "1G"},
		CloudInit: &CloudInitSpec{
			Enabled:  boolPtr(true),
			Packages: []string{"htop"},
		},
	}
	spec := &provider.InstanceSpec{Name: "vm2", OSType: "ubuntu"}
	c.generateCloudInitForVM(m, spec)
	if spec.CloudInit == nil {
		t.Fatal("expected cloud-init config from explicit spec")
	}
	if !strings.Contains(spec.CloudInit.UserData, "htop") {
		t.Error("missing package from explicit cloud-init spec")
	}
}

func TestGenerateCloudInitForVM_SkipsIfAlreadySet(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{
		Workload:  WorkloadMeta{Name: "vm3"},
		Provider:  ProviderSpec{Type: "qemu"},
		Image:     ImageSpec{Source: "cloud:ubuntu-24.04"},
		Resources: ResourceSpec{CPU: 1, Memory: "1G"},
	}
	spec := &provider.InstanceSpec{
		Name:      "vm3",
		OSType:    "ubuntu",
		CloudInit: &provider.CloudInitConfig{UserData: "# already set"},
	}
	c.generateCloudInitForVM(m, spec)
	if spec.CloudInit.UserData != "# already set" {
		t.Error("should not overwrite existing cloud-init UserData")
	}
}

func TestGenerateCloudInitForVM_QEMULinuxOS(t *testing.T) {
	c := NewConverter()
	m := &WorkloadManifest{
		Workload:  WorkloadMeta{Name: "vm4"},
		Provider:  ProviderSpec{Type: "qemu"},
		Image:     ImageSpec{Source: "iso:some-linux.iso"},
		Resources: ResourceSpec{CPU: 1, Memory: "1G"},
	}
	spec := &provider.InstanceSpec{Name: "vm4", OSType: "linux"}
	c.generateCloudInitForVM(m, spec)
	if spec.CloudInit == nil {
		t.Error("qemu + linux OS should generate cloud-init config")
	}
}
