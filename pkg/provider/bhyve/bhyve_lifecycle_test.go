package bhyve

import (
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Interface assertions — verify all claimed provider interfaces compile
func TestAllInterfaceAssertions(t *testing.T) {
	var _ provider.SnapshotProvider = (*BhyveProvider)(nil)
	var _ provider.CloneProvider = (*BhyveProvider)(nil)
	var _ provider.ConsoleProvider = (*BhyveProvider)(nil)
	var _ provider.ExportImportProvider = (*BhyveProvider)(nil)
	var _ provider.AutoStartProvider = (*BhyveProvider)(nil)
	var _ provider.MediaProvider = (*BhyveProvider)(nil)
	var _ provider.CheckpointProvider = (*BhyveProvider)(nil)
	var _ provider.PauseProvider = (*BhyveProvider)(nil)
	var _ provider.RenameProvider = (*BhyveProvider)(nil)
}

func TestCapabilitiesTruthfulness(t *testing.T) {
	p := NewBhyveProvider()
	caps := p.Capabilities()

	// These were fixed from false advertising in the audit
	if caps.SupportsNUMA {
		t.Error("SupportsNUMA should be false (no NUMA topology code)")
	}
	if caps.SupportsHotplugDisk {
		t.Error("SupportsHotplugDisk should be false (requires restart)")
	}
	if caps.SupportsBallooning {
		t.Error("SupportsBallooning should be false")
	}
	if caps.SupportsMigration {
		t.Error("SupportsMigration should be false")
	}
	if caps.SupportsLiveMigration {
		t.Error("SupportsLiveMigration should be false")
	}

	// These should be true
	if !caps.SupportsSnapshots {
		t.Error("SupportsSnapshots should be true (ZFS)")
	}
	if !caps.SupportsCloning {
		t.Error("SupportsCloning should be true (ZFS)")
	}
	if !caps.SupportsPause {
		t.Error("SupportsPause should be true")
	}
	if !caps.SupportsConsole {
		t.Error("SupportsConsole should be true (nmdm)")
	}
	if !caps.SupportsVNC {
		t.Error("SupportsVNC should be true")
	}
	if !caps.SupportsSerial {
		t.Error("SupportsSerial should be true")
	}
	if !caps.SupportsGPUPassthrough {
		t.Error("SupportsGPUPassthrough should be true")
	}
	if !caps.SupportsPCIPassthrough {
		t.Error("SupportsPCIPassthrough should be true")
	}

	// Architecture: only amd64
	if len(caps.SupportedArchitectures) != 1 || caps.SupportedArchitectures[0] != "amd64" {
		t.Errorf("SupportedArchitectures = %v, want [\"amd64\"]", caps.SupportedArchitectures)
	}
}

func TestCreateInstanceNameValidation(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid", input: "myvm", wantErr: false},
		{name: "valid with dash", input: "my-vm", wantErr: false},
		{name: "empty", input: "", wantErr: true},
		{name: "command injection", input: "vm;rm -rf /", wantErr: true},
		{name: "path traversal", input: "../etc", wantErr: true},
		{name: "starts with dash", input: "-myvm", wantErr: true},
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

func TestSnapshotNameValidation(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid", input: "snap1", wantErr: false},
		{name: "valid with dot", input: "v1.0", wantErr: false},
		{name: "empty", input: "", wantErr: true},
		{name: "command injection", input: "snap;id", wantErr: true},
		{name: "path traversal", input: "../snap", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validation.ValidateSnapshotName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSnapshotName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
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
		{name: "valid", input: "myclone", wantErr: false},
		{name: "empty", input: "", wantErr: true},
		{name: "command injection", input: "clone$(id)", wantErr: true},
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

func TestVMConfigStruct(t *testing.T) {
	config := vmConfig{
		Name:     "test-vm",
		CPUs:     4,
		MemoryMB: 8192,
		DiskPaths: []string{
			"/dev/zvol/zroot/hospitus/bhyve/test-vm/disk0",
		},
		DiskDrivers: []string{"virtio-blk"},
		TapDevs:     []string{"tap_test-vm_0"},
		NICDrivers:  []string{"virtio-net"},
		Console:     "/var/lib/hospitus/bhyve/test-vm/console",
		UEFIBoot:    true,
		VNCEnabled:  true,
		VNCPort:     5900,
		VNCWidth:    1024,
		VNCHeight:   768,
		VNCHost:     "127.0.0.1",
	}

	if config.Name != "test-vm" {
		t.Errorf("Name = %q, want %q", config.Name, "test-vm")
	}
	if config.CPUs != 4 {
		t.Errorf("CPUs = %d, want 4", config.CPUs)
	}
	if config.MemoryMB != 8192 {
		t.Errorf("MemoryMB = %d, want 8192", config.MemoryMB)
	}
	if !config.UEFIBoot {
		t.Error("UEFIBoot should be true")
	}
	if !config.VNCEnabled {
		t.Error("VNCEnabled should be true")
	}
	if len(config.DiskPaths) != 1 {
		t.Errorf("DiskPaths length = %d, want 1", len(config.DiskPaths))
	}
	if len(config.TapDevs) != 1 {
		t.Errorf("TapDevs length = %d, want 1", len(config.TapDevs))
	}
}

func TestVMConfigDefaults(t *testing.T) {
	config := vmConfig{
		Name:     "minimal",
		CPUs:     1,
		MemoryMB: 512,
	}

	// Verify zero-value defaults make sense
	if config.VNCEnabled {
		t.Error("VNCEnabled should default to false")
	}
	if config.UEFIBoot {
		t.Error("UEFIBoot should default to false (needs explicit enable)")
	}
	if config.VNCWait {
		t.Error("VNCWait should default to false")
	}
	if len(config.Passthrough) != 0 {
		t.Error("Passthrough should default to empty")
	}
}

func TestValidateBootloaderAcceptsOnlyWhatBoots(t *testing.T) {
	for _, ok := range []string{"", "uefi"} {
		if err := validateBootloader(ok); err != nil {
			t.Errorf("validateBootloader(%q) = %v, want nil", ok, err)
		}
	}
	// Each of these created a VM that bhyve refused with "no bootrom was
	// configured" and exit status 4.
	for _, bad := range []string{"bios", "grub", "grub-bhyve", "bhyveload"} {
		if err := validateBootloader(bad); err == nil {
			t.Errorf("validateBootloader(%q) = nil, want an error", bad)
		}
	}
}
