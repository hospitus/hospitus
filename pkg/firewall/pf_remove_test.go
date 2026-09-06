package firewall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRemoveRuleRemovesAllRuleTypes verifies removeRule deletes every rule line
// in a block (rdr, nat, pass), not just rdr, so no orphan rules survive
// instance deletion (audit HIGH pf.go:930).
func TestRemoveRuleRemovesAllRuleTypes(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "rules.conf")
	content := strings.Join([]string{
		"# ID: web instance:web",
		"rdr on em0 proto tcp to port 80 -> 10.0.0.2 port 80",
		"nat on em0 from 10.0.0.2 to any -> (em0)",
		"pass in on em0 proto tcp to 10.0.0.2 port 80",
		"",
		"# ID: db instance:db",
		"rdr on em0 proto tcp to port 5432 -> 10.0.0.3 port 5432",
		"",
	}, "\n")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	p := NewPFBackendWithDir(dir)
	if err := p.removeRule(file, "web"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	for _, orphan := range []string{"10.0.0.2", "# ID: web"} {
		if strings.Contains(out, orphan) {
			t.Errorf("removeRule left orphan content %q:\n%s", orphan, out)
		}
	}
	// The unrelated block must remain.
	if !strings.Contains(out, "# ID: db") || !strings.Contains(out, "10.0.0.3") {
		t.Errorf("removeRule dropped unrelated block:\n%s", out)
	}
}

// TestRemoveInstanceRulesRemovesPass verifies removeInstanceRules also removes
// pass lines, not just rdr/nat (audit HIGH pf.go:971).
func TestRemoveInstanceRulesRemovesPass(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "rules.conf")
	content := strings.Join([]string{
		"# ID: web-pass instance:web",
		"pass in on em0 proto tcp to 10.0.0.2 port 443",
		"nat on em0 from 10.0.0.2 to any -> (em0)",
		"",
	}, "\n")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	p := NewPFBackendWithDir(dir)
	if err := p.removeInstanceRules(file, "web"); err != nil {
		t.Fatal(err)
	}

	out, _ := os.ReadFile(file)
	if strings.Contains(string(out), "10.0.0.2") {
		t.Errorf("removeInstanceRules left orphan pass/nat rule:\n%s", out)
	}
}

// TestRemoveRuleExactIDMatch verifies removeRule only deletes the block whose
// ID matches exactly, not blocks whose ID merely shares the prefix
// (audit pf.go:921 — Contains without delimiter).
func TestRemoveRuleExactIDMatch(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "rules.conf")
	content := strings.Join([]string{
		"# ID: web instance:web",
		"rdr on em0 proto tcp to port 80 -> 10.0.0.2 port 80",
		"",
		"# ID: web-extra instance:web",
		"rdr on em0 proto tcp to port 81 -> 10.0.0.9 port 81",
		"",
	}, "\n")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	p := NewPFBackendWithDir(dir)
	if err := p.removeRule(file, "web"); err != nil {
		t.Fatal(err)
	}

	out, _ := os.ReadFile(file)
	if strings.Contains(string(out), "10.0.0.2") || strings.Contains(string(out), "# ID: web ") {
		t.Errorf("removeRule did not remove the exact-match block:\n%s", out)
	}
	if !strings.Contains(string(out), "# ID: web-extra") || !strings.Contains(string(out), "10.0.0.9") {
		t.Errorf("removeRule collaterally removed a prefix-sharing block:\n%s", out)
	}
}

func TestCommentRuleID(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{"# ID: web instance:web", "web"},
		{"# ID: web-extra instance:web", "web-extra"},
		{"rdr on em0 proto tcp", ""},
		{"# ID:", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := commentRuleID(tt.line); got != tt.want {
			t.Errorf("commentRuleID(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}
