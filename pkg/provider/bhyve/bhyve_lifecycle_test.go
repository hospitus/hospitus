package bhyve

import (
	"context"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
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

// TestTheProviderRefusesUnusableNames goes through the provider rather than
// calling pkg/validation directly.
//
// The tests this replaces asserted that ValidateInstanceName rejects a bad
// name — which pkg/validation's own tests already cover — so they would have
// kept passing if CreateInstance or CloneInstance stopped calling it. What
// matters here is that the provider's own entry points refuse.
func TestTheProviderRefusesUnusableNames(t *testing.T) {
	p := &BhyveProvider{
		dataDir:   t.TempDir(),
		stateDir:  t.TempDir(),
		zfsParent: testZFSParent,
		runner:    &execx.Fake{},
	}
	ctx := context.Background()

	for _, name := range []string{"", "..", "a/b", "-flag", strings.Repeat("x", 200)} {
		if _, err := p.CreateInstance(ctx, provider.InstanceSpec{Name: name}); err == nil {
			t.Errorf("CreateInstance accepted the name %q", name)
		}
		if _, err := p.CloneInstance(ctx, provider.InstanceHandle{ID: "web"}, name,
			provider.CloneOptions{}); err == nil {
			t.Errorf("CloneInstance accepted the clone name %q", name)
		}
		if err := p.RenameInstance(ctx, provider.InstanceHandle{ID: "web"}, name); err == nil {
			t.Errorf("RenameInstance accepted the new name %q", name)
		}
	}
}
