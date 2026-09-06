package jail

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// TestReconfigureWritesTheChangeWhereStartReadsIt covers "hospitus jail set",
// which reported success over a change the jail never saw.
//
// The API recorded the new resources in SQLite while StartInstance reloads the
// provider's own JSON file, so a restarted jail came back with the limits it
// already had.
func TestReconfigureWritesTheChangeWhereStartReadsIt(t *testing.T) {
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}

	configPath := filepath.Join(stateDir, "web.json")
	original := &jailConfig{Name: "web", Path: t.TempDir()}
	original.Resources.CPUs = 1
	original.Resources.MemoryMB = 512
	original.JailParameters = DefaultJailParameters()
	original.JailParameters.AllowRawSockets = true
	if err := p.saveJailConfig(original, configPath); err != nil {
		t.Fatal(err)
	}

	spec := provider.InstanceSpec{
		CPUs:     4,
		MemoryMB: 8192,
		ProviderConfig: map[string]interface{}{
			"allow.raw_sockets": false,
			"allow.sysvipc":     true,
		},
	}
	if err := p.Reconfigure(context.Background(), provider.InstanceHandle{ID: "web"}, spec); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}

	reloaded, err := p.loadJailConfig(configPath)
	if err != nil {
		t.Fatalf("loadJailConfig: %v", err)
	}
	if reloaded.Resources.CPUs != 4 || reloaded.Resources.MemoryMB != 8192 {
		t.Errorf("resources = %d CPUs / %d MB, want 4 / 8192",
			reloaded.Resources.CPUs, reloaded.Resources.MemoryMB)
	}
	// Turning a permission off has to reach the file as well as turning one on:
	// the override list only ever carried parameters that were true.
	if reloaded.JailParameters.AllowRawSockets {
		t.Error("allow.raw_sockets was left on although the change cleared it")
	}
	if !reloaded.JailParameters.AllowSysVIPC {
		t.Error("allow.sysvipc was not turned on")
	}
	// The record has to agree with the jail: read back, it would otherwise
	// still report the permission the change removed.
	if on, _ := reloaded.Spec.ProviderConfig["allow.raw_sockets"].(bool); on {
		t.Error("the stored spec still reports allow.raw_sockets as on")
	}
}

// TestApplyJailParameterMapLeavesTheRestAlone keeps an overlay from resetting
// every parameter the map does not name.
func TestApplyJailParameterMapLeavesTheRestAlone(t *testing.T) {
	base := DefaultJailParameters()
	base.AllowSysVIPC = true
	base.HostHostname = "db"

	merged, err := ApplyJailParameterMap(base, map[string]interface{}{
		"allow.raw_sockets": true,
		// Not a jail parameter; the same map carries provider bookkeeping.
		"zfs_dataset": "zroot/hospitus/jails/db",
	})
	if err != nil {
		t.Fatalf("ApplyJailParameterMap: %v", err)
	}

	if !merged.AllowRawSockets {
		t.Error("the named parameter was not applied")
	}
	if !merged.AllowSysVIPC || merged.HostHostname != "db" {
		t.Errorf("parameters the map did not name were reset: %+v", merged)
	}
}
