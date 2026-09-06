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
	nm := newNMWithFake(&execx.Fake{}, &config.Config{FirewallType: "none"})
	if err := nm.CleanupFirewallRules(context.Background(), "web"); err != nil {
		t.Fatalf("CleanupFirewallRules err = %v", err)
	}
}

func TestFetchManifestSHA256Offline(t *testing.T) {
	// No network in the test environment; the function is designed to return
	// an empty digest (no error) when the MANIFEST cannot be fetched.
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
