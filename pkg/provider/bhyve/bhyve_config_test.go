package bhyve

import (
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// providerSpec builds an InstanceSpec carrying the given ProviderConfig.
func providerSpec(pc map[string]interface{}) provider.InstanceSpec {
	return provider.InstanceSpec{ProviderConfig: pc}
}

func TestParseVMConfig(t *testing.T) {
	conf := `name=web
cpus=4
memory=2048
disks=/dev/zvol/POOL/hospitus/bhyve/web/disk0,/dev/zvol/POOL/hospitus/bhyve/web/disk1
disk_drivers=virtio-blk,ahci-cd
boot_order=0,1
taps=tap0,tap1
bridges=hospitus0,
nic_drivers=virtio-net,e1000
console=/dev/nmdm-web-A
uefi=true
uefi_vars=/var/lib/hospitus/bhyve/web/uefi-vars.fd
passthrough=1/0/0,2/0/0
vnc_enabled=true
vnc_port=5900
vnc_width=1024
vnc_height=768
vnc_wait=true
vnc_host=127.0.0.1
read_bps=10485760
write_bps=20971520
read_iops=1000
write_iops=2000
tpm_enabled=true
tpm_sock_path=/var/run/swtpm-web.sock
virtio_rng=true
ignore_msr=true
nat_enabled=true
ipv6_enabled=true
ipv6_prefix=fd10::1/64
usb_tablet=true
usb_devices=0.14.0
vlan_ids=100,200
disk_driver=nvme
disk_sectors=512,4096
pci_slot_nic0=4:0
`
	c := parseVMConfig([]byte(conf))

	if c.Name != "web" {
		t.Errorf("Name = %q, want web", c.Name)
	}
	if c.CPUs != 4 {
		t.Errorf("CPUs = %d, want 4", c.CPUs)
	}
	if c.MemoryMB != 2048 {
		t.Errorf("MemoryMB = %d, want 2048", c.MemoryMB)
	}
	if len(c.DiskPaths) != 2 {
		t.Errorf("DiskPaths = %v, want 2 entries", c.DiskPaths)
	}
	if len(c.DiskDrivers) != 2 || c.DiskDrivers[1] != "ahci-cd" {
		t.Errorf("DiskDrivers = %v", c.DiskDrivers)
	}
	if len(c.BootOrder) != 2 || c.BootOrder[1] != 1 {
		t.Errorf("BootOrder = %v", c.BootOrder)
	}
	if len(c.Bridges) != 2 || c.Bridges[0] != "hospitus0" || c.Bridges[1] != "" {
		t.Errorf("Bridges = %v, want [hospitus0 \"\"]", c.Bridges)
	}
	if len(c.NICDrivers) != 2 || c.NICDrivers[0] != "virtio-net" {
		t.Errorf("NICDrivers = %v", c.NICDrivers)
	}
	if !c.UEFIBoot {
		t.Error("UEFIBoot should be true")
	}
	if len(c.Passthrough) != 2 || c.Passthrough[0] != "1/0/0" {
		t.Errorf("Passthrough = %v", c.Passthrough)
	}
	if !c.VNCEnabled || c.VNCPort != 5900 || c.VNCWidth != 1024 || c.VNCHeight != 768 {
		t.Errorf("VNC fields wrong: %+v", c)
	}
	if c.ReadBPS != 10485760 || c.WriteIOPS != 2000 {
		t.Errorf("rate limits wrong: read_bps=%d write_iops=%d", c.ReadBPS, c.WriteIOPS)
	}
	if !c.TPMEnabled || c.TPMSockPath == "" || !c.VirtioRNG || !c.MSRIgnoreUnimplemented {
		t.Errorf("tpm/rng/msr flags wrong: %+v", c)
	}
	if !c.NATEnabled || !c.IPv6Enabled || c.IPv6Prefix != "fd10::1/64" {
		t.Errorf("network flags wrong: %+v", c)
	}
	if len(c.VLANIDs) != 2 || c.VLANIDs[1] != 200 {
		t.Errorf("VLANIDs = %v", c.VLANIDs)
	}
	if c.DiskDriver != "nvme" {
		t.Errorf("DiskDriver = %q", c.DiskDriver)
	}
	if len(c.DiskSectors) != 2 || c.DiskSectors[1] != 4096 {
		t.Errorf("DiskSectors = %v", c.DiskSectors)
	}
	if c.PCISlots["nic0"] != "4:0" {
		t.Errorf("PCISlots = %v", c.PCISlots)
	}
}

func TestParseVMConfigDefaultsOnInvalid(t *testing.T) {
	c := parseVMConfig([]byte("name=x\ncpus=abc\nmemory=notnum\n"))
	if c.CPUs != 1 {
		t.Errorf("invalid cpus should default to 1, got %d", c.CPUs)
	}
	if c.MemoryMB != 512 {
		t.Errorf("invalid memory should default to 512, got %d", c.MemoryMB)
	}
}

func TestParseVMConfigRejectsUnknownNICDriver(t *testing.T) {
	c := parseVMConfig([]byte("nic_drivers=virtio-net,malicious-driver\n"))
	if len(c.NICDrivers) != 2 {
		t.Fatalf("NICDrivers = %v", c.NICDrivers)
	}
	if c.NICDrivers[1] != "" {
		t.Errorf("unsupported NIC driver should be blanked, got %q", c.NICDrivers[1])
	}
}

func TestParseVMConfigIgnoresMalformedLines(t *testing.T) {
	c := parseVMConfig([]byte("garbage-no-equals\n\nname=ok\n# comment-ish\n"))
	if c.Name != "ok" {
		t.Errorf("Name = %q, want ok", c.Name)
	}
}

func TestValidateVMConfigPaths(t *testing.T) {
	p := &BhyveProvider{stateDir: "/var/lib/hospitus/state"}
	vmDir := "/var/lib/hospitus/bhyve/web"

	t.Run("allows zvol, vmdir, physical, nmdm console", func(t *testing.T) {
		c := &vmConfig{
			DiskPaths: []string{
				"/dev/zvol/" + testZFSParent + "/web/disk0",
				vmDir + "/disk1.img",
				"/dev/ada2",
			},
			TapDevs: []string{"tap0", "tap1"},
			Console: "/dev/nmdm-web-A",
		}
		if err := p.validateVMConfigPaths(c, vmDir); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("rejects disk outside allowed locations", func(t *testing.T) {
		c := &vmConfig{DiskPaths: []string{"/etc/passwd"}}
		if err := p.validateVMConfigPaths(c, vmDir); err == nil {
			t.Error("expected error for disk outside allowed locations")
		}
	})

	t.Run("allows cdrom anywhere but rejects traversal", func(t *testing.T) {
		ok := &vmConfig{DiskPaths: []string{"/srv/iso/install.iso"}, DiskDrivers: []string{"ahci-cd"}}
		if err := p.validateVMConfigPaths(ok, vmDir); err != nil {
			t.Errorf("cdrom anywhere should be allowed: %v", err)
		}
		bad := &vmConfig{DiskPaths: []string{"/srv/../etc/shadow"}, DiskDrivers: []string{"ahci-cd"}}
		if err := p.validateVMConfigPaths(bad, vmDir); err == nil {
			t.Error("cdrom with traversal should be rejected")
		}
	})

	t.Run("rejects tap with path separators", func(t *testing.T) {
		c := &vmConfig{TapDevs: []string{"../evil"}}
		if err := p.validateVMConfigPaths(c, vmDir); err == nil {
			t.Error("expected error for tap with traversal")
		}
	})

	t.Run("rejects console outside state dir", func(t *testing.T) {
		c := &vmConfig{Console: "/tmp/evil"}
		if err := p.validateVMConfigPaths(c, vmDir); err == nil {
			t.Error("expected error for console outside state dir")
		}
	})
}

func TestApplyProviderConfig(t *testing.T) {
	p := &BhyveProvider{}

	t.Run("nil config is a no-op", func(t *testing.T) {
		c := &vmConfig{}
		if err := p.applyProviderConfig(c, providerSpec(nil), "vm"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("parses typed and stringified values", func(t *testing.T) {
		c := &vmConfig{TapDevs: []string{"tap0"}}
		pc := map[string]interface{}{
			"passthrough":  []string{"1/0/0"},
			"vnc":          "127.0.0.1:5901",
			"vnc_width":    1280,
			"vnc_height":   1024.0, // float64 as from JSON
			"tpm":          "true",
			"virtio_rng":   true,
			"nic_driver":   "e1000",
			"ignore_msr":   "true",
			"usb_tablet":   true,
			"usb_devices":  []interface{}{"0.14.0"},
			"disk_driver":  "nvme",
			"vlan_ids":     []interface{}{100, 200.0},
			"ipv6_enabled": "true",
			"ipv6_prefix":  "fd10::1/64",
		}
		if err := p.applyProviderConfig(c, providerSpec(pc), "vm"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(c.Passthrough) != 1 || c.Passthrough[0] != "1/0/0" {
			t.Errorf("Passthrough = %v", c.Passthrough)
		}
		if !c.VNCEnabled || c.VNCHost != "127.0.0.1" || c.VNCPort != 5901 {
			t.Errorf("VNC = %+v", c)
		}
		if c.VNCWidth != 1280 || c.VNCHeight != 1024 {
			t.Errorf("VNC dims = %dx%d", c.VNCWidth, c.VNCHeight)
		}
		if !c.TPMEnabled || !c.VirtioRNG || !c.MSRIgnoreUnimplemented || !c.USBTablet {
			t.Errorf("bool flags = %+v", c)
		}
		if len(c.NICDrivers) != 1 || c.NICDrivers[0] != "e1000" {
			t.Errorf("NICDrivers = %v", c.NICDrivers)
		}
		if c.DiskDriver != "nvme" {
			t.Errorf("DiskDriver = %q", c.DiskDriver)
		}
		if len(c.VLANIDs) != 2 || c.VLANIDs[1] != 200 {
			t.Errorf("VLANIDs = %v", c.VLANIDs)
		}
		if !c.IPv6Enabled || c.IPv6Prefix != "fd10::1/64" {
			t.Errorf("ipv6 = %+v", c)
		}
	})

	t.Run("rejects unknown NIC driver", func(t *testing.T) {
		c := &vmConfig{}
		err := p.applyProviderConfig(c, providerSpec(map[string]interface{}{"nic_driver": "evil"}), "vm")
		if err == nil {
			t.Fatal("expected error for unknown NIC driver")
		}
	})

	t.Run("rejects insecure VNC host without opt-in", func(t *testing.T) {
		c := &vmConfig{}
		err := p.applyProviderConfig(c, providerSpec(map[string]interface{}{"vnc": "0.0.0.0:5900"}), "vm")
		if err == nil {
			t.Fatal("expected error for 0.0.0.0 VNC without vnc_insecure")
		}
	})

	t.Run("allows insecure VNC host with opt-in", func(t *testing.T) {
		c := &vmConfig{}
		pc := map[string]interface{}{"vnc": "0.0.0.0:5900", "vnc_insecure": true}
		if err := p.applyProviderConfig(c, providerSpec(pc), "vm"); err != nil {
			t.Fatalf("unexpected error with vnc_insecure: %v", err)
		}
	})
}
