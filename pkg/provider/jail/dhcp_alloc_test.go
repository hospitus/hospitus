package jail

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// TestAllocateDHCPAddressSkipsPersisted verifies allocation skips an IP already
// written to a jail config on disk. StartInstance now persists a DHCP lease
// under dhcpMu before releasing, so a concurrent allocation observes it here
// and cannot hand out the same address (audit HIGH dhcp.go:127).
func TestAllocateDHCPAddressSkipsPersisted(t *testing.T) {
	t.Setenv("HOSPITUS_IP_POOL", "10.0.0.0/24")
	dir := t.TempDir()
	p := &JailProvider{stateDir: dir, logger: slog.Default()}
	ctx := context.Background()

	first, err := p.allocateDHCPAddress(ctx, "")
	if err != nil {
		t.Fatalf("first allocation: %v", err)
	}
	if first != "10.0.0.2/24" {
		t.Fatalf("first allocation = %q, want 10.0.0.2/24", first)
	}

	// Persist a jail holding that IP, as StartInstance now does immediately.
	cfg := &jailConfig{Networks: []provider.NetworkSpec{{IPv4: first}}}
	if err := p.saveJailConfig(cfg, filepath.Join(dir, "jail1.json")); err != nil {
		t.Fatal(err)
	}

	second, err := p.allocateDHCPAddress(ctx, "")
	if err != nil {
		t.Fatalf("second allocation: %v", err)
	}
	if second == first {
		t.Fatalf("second allocation reused %q — duplicate lease", second)
	}
	if second != "10.0.0.3/24" {
		t.Errorf("second allocation = %q, want 10.0.0.3/24", second)
	}
}

func TestRefuseAddressInUse(t *testing.T) {
	dir := t.TempDir()
	p := &JailProvider{stateDir: dir, logger: slog.Default()}

	cfg := &jailConfig{Name: "webserver", Networks: []provider.NetworkSpec{{IPv4: "10.0.0.2/24"}}}
	if err := p.saveJailConfig(cfg, filepath.Join(dir, "webserver.json")); err != nil {
		t.Fatal(err)
	}

	// Two jails both took 10.0.0.2 on the same bridge, and hospitus said nothing.
	err := p.refuseAddressInUse("proxy", "10.0.0.2/24")
	if err == nil {
		t.Fatal("a second jail was allowed to take an address already held")
	}
	if !strings.Contains(err.Error(), "webserver") {
		t.Errorf("error does not name the holder: %v", err)
	}

	// A free address is fine, and so is the jail's own.
	if err := p.refuseAddressInUse("proxy", "10.0.0.3/24"); err != nil {
		t.Errorf("free address refused: %v", err)
	}
	if err := p.refuseAddressInUse("webserver", "10.0.0.2/24"); err != nil {
		t.Errorf("a jail was refused its own address: %v", err)
	}
	// DHCP and an empty address go through their own path.
	if err := p.refuseAddressInUse("proxy", "dhcp"); err != nil {
		t.Errorf("dhcp refused: %v", err)
	}
}
