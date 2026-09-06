package bhyve

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/hospitus/hospitus/pkg/provider"
)

// coerceInt64 extracts an int64 from a value that may be typed as int, int64,
// float64 (the type JSON numbers decode to) or a numeric string.
func coerceInt64(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		// JSON has no integers, so a whole number arrives as a float64 — but a
		// genuinely fractional value is a mistake in the request, and
		// truncating it silently gave the caller a VM it did not ask for.
		if n != math.Trunc(n) {
			return 0, false
		}
		return int64(n), true
	case string:
		if parsed, err := strconv.ParseInt(n, 10, 64); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

// coerceInt is the int-typed counterpart of coerceInt64.
func coerceInt(v interface{}) (int, bool) {
	n, ok := coerceInt64(v)
	return int(n), ok
}

type OSProfile struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	OSType      string            `json:"os_type"`    // freebsd, linux, windows
	OSVersion   string            `json:"os_version"` // 14.0-RELEASE, ubuntu-22.04, windows-11
	CPUs        int               `json:"cpus"`
	MemoryMB    int64             `json:"memory_mb"`
	DiskSizeGB  int64             `json:"disk_size_gb"`
	DiskDriver  string            `json:"disk_driver"` // virtio-blk, ahci-hd, nvme
	UEFIBoot    bool              `json:"uefi_boot"`
	CloudInit   bool              `json:"cloud_init"` // Enable cloud-init
	Packages    []string          `json:"packages"`   // Packages to install (if supported)
	Config      map[string]string `json:"config"`     // Additional configuration
}

// Common OS profiles
var bhyveOSProfiles = map[string]OSProfile{
	"freebsd-14-server": {
		Name:        "freebsd-14-server",
		Description: "FreeBSD 14.0-RELEASE Server",
		OSType:      "freebsd",
		OSVersion:   "14.0-RELEASE",
		CPUs:        2,
		MemoryMB:    2048,
		DiskSizeGB:  20,
		DiskDriver:  "virtio-blk",
		UEFIBoot:    true,
		CloudInit:   true,
	},
	"freebsd-13-server": {
		Name:        "freebsd-13-server",
		Description: "FreeBSD 13.2-RELEASE Server",
		OSType:      "freebsd",
		OSVersion:   "13.2-RELEASE",
		CPUs:        2,
		MemoryMB:    2048,
		DiskSizeGB:  20,
		DiskDriver:  "virtio-blk",
		UEFIBoot:    true,
		CloudInit:   true,
	},
	"ubuntu-22.04-server": {
		Name:        "ubuntu-22.04-server",
		Description: "Ubuntu 22.04 LTS Server",
		OSType:      "linux",
		OSVersion:   "22.04",
		CPUs:        2,
		MemoryMB:    2048,
		DiskSizeGB:  20,
		DiskDriver:  "virtio-blk",
		UEFIBoot:    true,
		CloudInit:   true,
	},
	"windows-11": {
		Name:        "windows-11",
		Description: "Windows 11 Pro",
		OSType:      "windows",
		OSVersion:   "11",
		CPUs:        4,
		MemoryMB:    4096,
		DiskSizeGB:  64,
		DiskDriver:  "nvme",
		UEFIBoot:    true,
		CloudInit:   false,
	},
	"debian-12-server": {
		Name:        "debian-12-server",
		Description: "Debian 12 (Bookworm) Server",
		OSType:      "linux",
		OSVersion:   "12",
		CPUs:        2,
		MemoryMB:    2048,
		DiskSizeGB:  20,
		DiskDriver:  "virtio-blk",
		UEFIBoot:    true,
		CloudInit:   true,
	},
}

// ListOSProfiles returns a list of available OS profiles, sorted by name for a
// deterministic order (map iteration order is randomized in Go).
func (p *BhyveProvider) ListOSProfiles(ctx context.Context) ([]OSProfile, error) {
	profiles := make([]OSProfile, 0, len(bhyveOSProfiles))
	for name := range bhyveOSProfiles {
		profiles = append(profiles, bhyveOSProfiles[name])
	}
	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].Name < profiles[j].Name
	})
	return profiles, nil
}

// GetOSProfile returns a specific OS profile by name.
func (p *BhyveProvider) GetOSProfile(ctx context.Context, name string) (*OSProfile, error) {
	profile, ok := bhyveOSProfiles[name]
	if !ok {
		return nil, fmt.Errorf("OS profile not found: %s", name)
	}
	return &profile, nil
}

// CreateFromOSProfile creates a new VM from an OS profile.
//
// This implements CBSD's profile functionality:
//   - Quick VM creation from predefined templates
//   - Sensible defaults for common operating systems
//   - Customizable (can override profile settings)
func (p *BhyveProvider) CreateFromOSProfile(ctx context.Context, profileName, vmName string, overrides map[string]interface{}) (provider.InstanceHandle, error) {
	profile, err := p.GetOSProfile(ctx, profileName)
	if err != nil {
		return provider.InstanceHandle{}, err
	}

	// Build instance spec from profile
	spec := provider.InstanceSpec{
		Name:       vmName,
		CPUs:       profile.CPUs,
		MemoryMB:   profile.MemoryMB,
		OSType:     profile.OSType,
		OSVersion:  profile.OSVersion,
		Bootloader: "uefi",
		ProviderConfig: map[string]interface{}{
			"disk_driver": profile.DiskDriver,
		},
	}

	// Add disk
	spec.Disks = []provider.DiskSpec{
		{
			Type:   provider.DiskTypeRaw,
			SizeGB: int(profile.DiskSizeGB),
		},
	}

	// Apply overrides. Values decoded from JSON arrive as float64 (or string),
	// not int/int64, so coerce each numeric field rather than type-asserting a
	// single concrete type that a JSON body would never satisfy.
	// A key that is present but unreadable — "cpus": 1.9, "cpus": "two" — is a
	// mistake in the request. Keeping the profile default instead created a VM
	// with values the caller never asked for and reported success.
	if raw, present := overrides["cpus"]; present {
		cpus, ok := coerceInt(raw)
		if !ok {
			return provider.InstanceHandle{}, fmt.Errorf("invalid cpus override %v: expected a whole number", raw)
		}
		spec.CPUs = cpus
	}
	if raw, present := overrides["memory_mb"]; present {
		memory, ok := coerceInt64(raw)
		if !ok {
			return provider.InstanceHandle{}, fmt.Errorf("invalid memory_mb override %v: expected a whole number", raw)
		}
		spec.MemoryMB = memory
	}
	if raw, present := overrides["disk_size_gb"]; present {
		diskSize, ok := coerceInt64(raw)
		if !ok {
			return provider.InstanceHandle{}, fmt.Errorf("invalid disk_size_gb override %v: expected a whole number", raw)
		}
		spec.Disks[0].SizeGB = int(diskSize)
	}

	// Add cloud-init if enabled in profile
	if profile.CloudInit {
		if cloudInitData, ok := overrides["cloud_init"].(map[string]interface{}); ok {
			userData, _ := cloudInitData["user_data"].(string)
			metaData, _ := cloudInitData["meta_data"].(string)
			networkConfig, _ := cloudInitData["network"].(string)

			spec.CloudInit = &provider.CloudInitConfig{
				UserData: userData,
				MetaData: metaData,
				Network:  networkConfig,
			}
		} else {
			// Default cloud-init with minimal config
			spec.CloudInit = &provider.CloudInitConfig{
				UserData: "",
				MetaData: "",
			}
		}
	}

	// Create instance using the provider's CreateInstance method
	return p.CreateInstance(ctx, spec)
}
