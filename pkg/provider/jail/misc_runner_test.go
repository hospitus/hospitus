package jail

import (
	"context"
	"testing"

	"github.com/hospitus/hospitus/pkg/config"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestApplyPortForwardsFirewallNone(t *testing.T) {
	nm := newNMWithFake(&execx.Fake{}, &config.Config{FirewallType: "none"})
	if err := nm.ApplyPortForwards(context.Background(), "web"); err != nil {
		t.Fatalf("ApplyPortForwards err = %v", err)
	}
}

func TestCleanupFirewallRulesNone(t *testing.T) {
	fake := &execx.Fake{}
	nm := newNMWithFake(fake, &config.Config{FirewallType: "none"})
	if err := nm.CleanupFirewallRules(context.Background(), "web"); err != nil {
		t.Fatalf("CleanupFirewallRules err = %v", err)
	}
	// "none" means no firewall commands at all. Checking only the error let an
	// implementation run pfctl and swallow its result.
	if calls := fake.Snapshot(); len(calls) != 0 {
		t.Errorf("firewall type \"none\" still issued commands: %v", calls)
	}
}

func TestFetchManifestSHA256Offline(t *testing.T) {
	// This reaches the network. The assumption that the test environment has
	// none does not hold on a developer machine or a CI runner with egress, and
	// the outcome then depends on a FreeBSD mirror rather than on this code.
	if testing.Short() {
		t.Skip("performs an outbound HTTP request")
	}
	sha, err := FetchManifestSHA256("amd64", "14.3-RELEASE")
	if err != nil {
		t.Fatalf("FetchManifestSHA256 err = %v", err)
	}
	_ = sha // may be empty offline; the point is no error and the code path runs
}

func TestAttachDetachNetworkDelegate(t *testing.T) {
	// DetachNetwork delegates to RemoveNetworkInterface; verify it reaches the
	// runner for a running jail.
	p, fake := runningProvider(t, "web", nil)
	if err := p.DetachNetwork(context.Background(), provider.InstanceHandle{ID: "web"}, "epair4b"); err != nil {
		t.Fatalf("DetachNetwork err = %v", err)
	}
	if want := "ifconfig epair4b -vnet web"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}
