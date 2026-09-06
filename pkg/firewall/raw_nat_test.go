package firewall

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAddRawNATRuleWritesToNatRules verifies a nat6 translation rule is written
// to nat.rules and never leaks into filter.rules, which would make pfctl reject
// the whole anchor (audit HIGH bhyve_network.go:336).
func TestAddRawNATRuleWritesToNatRules(t *testing.T) {
	dir := t.TempDir()
	p := NewPFBackendWithDir(dir)

	// The reload (pfctl) may fail off-host/non-root; the rule is appended first,
	// which is what we assert here.
	_ = p.AddRawNATRule(context.Background(), "web-ipv6", "hospitus-bhyve-ipv6",
		"nat6 on em0 from fd10:0:0:1::/64 -> (em0:0)")

	nat, _ := os.ReadFile(filepath.Join(dir, "nat.rules"))
	if !strings.Contains(string(nat), "nat6 on em0") {
		t.Errorf("nat6 rule not written to nat.rules:\n%s", nat)
	}
	filter, _ := os.ReadFile(filepath.Join(dir, "filter.rules"))
	if strings.Contains(string(filter), "nat6") {
		t.Errorf("nat6 rule leaked into filter.rules:\n%s", filter)
	}
}
