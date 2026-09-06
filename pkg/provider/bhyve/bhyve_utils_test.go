package bhyve

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestParseImageSource(t *testing.T) {
	p := NewBhyveProvider()

	tests := []struct {
		name     string
		source   string
		wantType string
		wantRef  string
		wantErr  bool
	}{
		{
			name:    "empty source",
			source:  "",
			wantErr: true,
		},
		{
			name:     "no prefix defaults to cloud",
			source:   "ubuntu-24.04",
			wantType: "cloud",
			wantRef:  "ubuntu-24.04",
		},
		{
			name:     "cloud prefix",
			source:   "cloud:ubuntu-24.04",
			wantType: "cloud",
			wantRef:  "ubuntu-24.04",
		},
		{
			name:     "iso prefix",
			source:   "iso:freebsd-14.iso",
			wantType: "iso",
			wantRef:  "freebsd-14.iso",
		},
		{
			name:     "raw prefix",
			source:   "raw:/path/to/disk.img",
			wantType: "raw",
			wantRef:  "/path/to/disk.img",
		},
		{
			name:     "uppercase is normalized",
			source:   "ISO:some.iso",
			wantType: "iso",
			wantRef:  "some.iso",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotRef, err := p.parseImageSource(tt.source)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseImageSource(%q) error = %v, wantErr %v", tt.source, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if gotType != tt.wantType {
					t.Errorf("parseImageSource(%q) type = %q, want %q", tt.source, gotType, tt.wantType)
				}
				if gotRef != tt.wantRef {
					t.Errorf("parseImageSource(%q) ref = %q, want %q", tt.source, gotRef, tt.wantRef)
				}
			}
		})
	}
}

func TestResolveISOPath(t *testing.T) {
	// Create a temp dir with a fake ISO
	tmpDir := t.TempDir()
	isoDir := filepath.Join(tmpDir, "iso")
	if err := os.MkdirAll(isoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create test ISO files
	isoFile := filepath.Join(isoDir, "test.iso")
	if err := os.WriteFile(isoFile, []byte("fake iso"), 0o644); err != nil {
		t.Fatal(err)
	}
	noExtFile := filepath.Join(isoDir, "noext")
	if err := os.WriteFile(noExtFile, []byte("fake iso"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := NewBhyveProvider()
	p.imageDir = tmpDir

	// Absolute path — file exists
	resolved, err := p.resolveISOPath(isoFile)
	if err != nil {
		t.Errorf("resolveISOPath(absolute existing): unexpected error: %v", err)
	}
	if resolved != isoFile {
		t.Errorf("resolveISOPath(absolute) = %q, want %q", resolved, isoFile)
	}

	// Absolute path — file does not exist
	_, err = p.resolveISOPath("/nonexistent/path.iso")
	if err == nil {
		t.Error("resolveISOPath(absolute nonexistent): expected error, got nil")
	}

	// Relative — finds by exact name
	resolved, err = p.resolveISOPath("test.iso")
	if err != nil {
		t.Errorf("resolveISOPath(relative with ext): unexpected error: %v", err)
	}
	if resolved != isoFile {
		t.Errorf("resolveISOPath(relative) = %q, want %q", resolved, isoFile)
	}

	// Relative — finds by appending .iso
	resolved, err = p.resolveISOPath("noext")
	if err != nil {
		t.Errorf("resolveISOPath(relative, .iso added): unexpected error: %v", err)
	}
	_ = resolved // path can vary

	// Relative — not found
	_, err = p.resolveISOPath("missing")
	if err == nil {
		t.Error("resolveISOPath(missing): expected error, got nil")
	}
}

func TestListAndGetOSProfiles(t *testing.T) {
	p := NewBhyveProvider()
	ctx := context.Background()

	profiles, err := p.ListOSProfiles(ctx)
	if err != nil {
		t.Fatalf("ListOSProfiles() error: %v", err)
	}

	if len(profiles) == 0 {
		t.Fatal("ListOSProfiles() returned no profiles — expected at least one")
	}

	// Get each profile by name
	for _, profile := range profiles {
		got, err := p.GetOSProfile(ctx, profile.Name)
		if err != nil {
			t.Errorf("GetOSProfile(%q) error: %v", profile.Name, err)
			continue
		}
		if got.Name != profile.Name {
			t.Errorf("GetOSProfile(%q).Name = %q, want %q", profile.Name, got.Name, profile.Name)
		}
	}

	// Non-existent profile
	_, err = p.GetOSProfile(ctx, "nonexistent-profile-xyz")
	if err == nil {
		t.Error("GetOSProfile(nonexistent) expected error, got nil")
	}
}

func TestBhyveProviderMetadataExtra(t *testing.T) {
	p := NewBhyveProvider()
	metadata := p.Metadata()

	if metadata.Name != "bhyve" {
		t.Errorf("Metadata().Name = %q, want %q", metadata.Name, "bhyve")
	}
	if metadata.Version == "" {
		t.Error("Metadata().Version should not be empty")
	}
}

// ----------------------------------------------------------------------------
// getBhyveDiskDriver
// ----------------------------------------------------------------------------

func TestGetBhyveDiskDriver(t *testing.T) {
	p := newTestProvider(false, "")

	cases := []struct {
		name       string
		spec       provider.InstanceSpec
		diskIndex  int
		wantDriver DiskDriver
	}{
		{
			name:       "default virtio-blk for data disk",
			spec:       provider.InstanceSpec{},
			diskIndex:  1,
			wantDriver: DiskDriverVirtioBlk,
		},
		{
			name: "global override ahci-hd",
			spec: provider.InstanceSpec{
				ProviderConfig: map[string]interface{}{"disk_driver": "ahci-hd"},
			},
			diskIndex:  0,
			wantDriver: DiskDriverAHCIHD,
		},
		{
			name: "global override nvme",
			spec: provider.InstanceSpec{
				ProviderConfig: map[string]interface{}{"disk_driver": "nvme"},
			},
			diskIndex:  0,
			wantDriver: DiskDriverNVMe,
		},
		{
			name: "global override virtio alias",
			spec: provider.InstanceSpec{
				ProviderConfig: map[string]interface{}{"disk_driver": "virtio"},
			},
			diskIndex:  0,
			wantDriver: DiskDriverVirtioBlk,
		},
		{
			name: "global override ahci alias",
			spec: provider.InstanceSpec{
				ProviderConfig: map[string]interface{}{"disk_driver": "ahci"},
			},
			diskIndex:  0,
			wantDriver: DiskDriverAHCIHD,
		},
		{
			name: "per-disk override disk_0_driver=nvme",
			spec: provider.InstanceSpec{
				ProviderConfig: map[string]interface{}{"disk_0_driver": "nvme"},
			},
			diskIndex:  0,
			wantDriver: DiskDriverNVMe,
		},
		{
			name: "physical disk defaults to ahci-hd",
			spec: provider.InstanceSpec{
				Disks: []provider.DiskSpec{{Type: provider.DiskTypePhysical}},
			},
			diskIndex:  0,
			wantDriver: DiskDriverAHCIHD,
		},
		{
			name: "uefi disk0 defaults to ahci-hd",
			spec: provider.InstanceSpec{
				ProviderConfig: map[string]interface{}{"bootloader": "uefi"},
			},
			diskIndex:  0,
			wantDriver: DiskDriverAHCIHD,
		},
		{
			name: "uefi disk1 defaults to virtio-blk",
			spec: provider.InstanceSpec{
				ProviderConfig: map[string]interface{}{"bootloader": "uefi"},
			},
			diskIndex:  1,
			wantDriver: DiskDriverVirtioBlk,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := p.getBhyveDiskDriver(tc.spec, tc.diskIndex)
			if got != tc.wantDriver {
				t.Errorf("getBhyveDiskDriver diskIndex=%d = %q, want %q", tc.diskIndex, got, tc.wantDriver)
			}
		})
	}
}

func TestBootOrderByIndex(t *testing.T) {
	dir := t.TempDir()
	p := newTestProvider(false, "")
	p.dataDir = dir

	// Create a fake VM config with 3 disks using allowed paths inside temp dir.
	vmName := "boot-test-vm"
	vmDir := filepath.Join(dir, vmName)
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Disk paths must be inside p.dataDir to pass path validation.
	disk0 := filepath.Join(vmDir, "disk0.img")
	disk1 := filepath.Join(vmDir, "disk1.img")
	disk2 := filepath.Join(vmDir, "disk2.img")
	config := &vmConfig{
		Name:      vmName,
		CPUs:      1,
		MemoryMB:  512,
		DiskPaths: []string{disk0, disk1, disk2},
	}
	if err := p.saveVMConfig(vmDir, config); err != nil {
		t.Fatalf("saveVMConfig: %v", err)
	}

	handle := provider.InstanceHandle{ID: vmName}

	// Get initial boot order (should be empty)
	order, err := p.getBootOrderByIndex(context.Background(), handle)
	if err != nil {
		t.Fatalf("getBootOrderByIndex: %v", err)
	}
	if len(order) != 0 {
		t.Errorf("initial boot order should be empty, got %v", order)
	}

	// Set boot order
	newOrder := []int{2, 0, 1}
	if err := p.setBootOrderByIndex(context.Background(), handle, newOrder); err != nil {
		t.Fatalf("setBootOrderByIndex: %v", err)
	}

	// Get it back
	order, err = p.getBootOrderByIndex(context.Background(), handle)
	if err != nil {
		t.Fatalf("getBootOrderByIndex after set: %v", err)
	}
	if len(order) != len(newOrder) {
		t.Fatalf("expected %d entries, got %d", len(newOrder), len(order))
	}
	for i, v := range newOrder {
		if order[i] != v {
			t.Errorf("order[%d] = %d, want %d", i, order[i], v)
		}
	}

	// Invalid: index out of range
	if err := p.setBootOrderByIndex(context.Background(), handle, []int{0, 3}); err == nil {
		t.Error("Expected error for out-of-range boot index")
	}

	// Invalid: too many entries
	if err := p.setBootOrderByIndex(context.Background(), handle, []int{0, 1, 2, 3}); err == nil {
		t.Error("Expected error for too many boot entries")
	}
}

func TestValidateVNCHost(t *testing.T) {
	tests := []struct {
		name        string
		host        string
		vncInsecure bool
		wantErr     bool
	}{
		{
			name:    "127.0.0.1 allowed",
			host:    "127.0.0.1",
			wantErr: false,
		},
		{
			name:    "::1 allowed",
			host:    "::1",
			wantErr: false,
		},
		{
			name:    "localhost allowed",
			host:    "localhost",
			wantErr: false,
		},
		{
			name:    "0.0.0.0 without insecure flag rejected",
			host:    "0.0.0.0",
			wantErr: true,
		},
		{
			name:        "0.0.0.0 with insecure flag allowed",
			host:        "0.0.0.0",
			vncInsecure: true,
			wantErr:     false,
		},
		{
			name:    ":: without insecure flag rejected",
			host:    "::",
			wantErr: true,
		},
		{
			name:        ":: with insecure flag allowed",
			host:        "::",
			vncInsecure: true,
			wantErr:     false,
		},
		{
			name:    "empty string allowed",
			host:    "",
			wantErr: false,
		},
		{
			name:    "192.168.1.1 without insecure flag rejected",
			host:    "192.168.1.1",
			wantErr: true,
		},
		{
			name:        "192.168.1.1 with insecure flag allowed",
			host:        "192.168.1.1",
			vncInsecure: true,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVNCHost(tt.host, tt.vncInsecure)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateVNCHost(%q, %v) error = %v, wantErr %v", tt.host, tt.vncInsecure, err, tt.wantErr)
			}
		})
	}
}
