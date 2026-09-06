package bhyve

import (
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// TestPhysicalDiskAllowed verifies passthrough is default-deny and only matches
// allow-listed devices (audit HIGH bhyve_lifecycle.go:359).
func TestPhysicalDiskAllowed(t *testing.T) {
	// Empty allow-list denies everything.
	if physicalDiskAllowed(nil, "/dev/ada2", "/dev/ada2") {
		t.Error("empty allow-list must deny all passthrough")
	}
	allowed := []string{"/dev/ada2", "/dev/da5"}
	if !physicalDiskAllowed(allowed, "/dev/ada2", "/dev/ada2") {
		t.Error("allow-listed device should be permitted")
	}
	// Match via the resolved (symlink target) form.
	if !physicalDiskAllowed(allowed, "/dev/mydisk", "/dev/da5") {
		t.Error("resolved device path should match the allow-list")
	}
	if physicalDiskAllowed(allowed, "/dev/ada0", "/dev/ada0") {
		t.Error("non-allow-listed device (system disk) must be denied")
	}
}

// TestAllowedPhysicalDisksParsing verifies the allow-list is read from provider
// settings in the supported shapes.
func TestAllowedPhysicalDisksParsing(t *testing.T) {
	p := &BhyveProvider{config: provider.ProviderConfig{Settings: map[string]interface{}{
		"allowed_physical_disks": "/dev/ada2 /dev/da5",
	}}}
	got := p.allowedPhysicalDisks()
	if len(got) != 2 || got[0] != "/dev/ada2" || got[1] != "/dev/da5" {
		t.Errorf("string parse = %v", got)
	}

	p2 := &BhyveProvider{config: provider.ProviderConfig{Settings: map[string]interface{}{
		"allowed_physical_disks": []interface{}{"/dev/ada2"},
	}}}
	if got := p2.allowedPhysicalDisks(); len(got) != 1 || got[0] != "/dev/ada2" {
		t.Errorf("[]interface parse = %v", got)
	}

	p3 := &BhyveProvider{config: provider.ProviderConfig{}}
	if got := p3.allowedPhysicalDisks(); len(got) != 0 {
		t.Errorf("missing setting should yield empty, got %v", got)
	}
}
