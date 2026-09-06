package firewall

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── validateIfaceName ─────────────────────────────────────────────────────────

// TestValidateIfaceName_Valid verifies that well-formed FreeBSD/Linux interface
// names are accepted by validateIfaceName.
func TestValidateIfaceName_Valid(t *testing.T) {
	valid := []string{
		"em0",
		"vtnet0",
		"re0",
		"igb0",
		"lo0",
		"bge0",
		"ix0",
		"em10",      // multiple digits at end
		"hospitus0", // custom name
		"xn0",
		"A0", // uppercase start is allowed
		"eth0",
	}
	for _, name := range valid {
		t.Run(name, func(t *testing.T) {
			if err := validateIfaceName(name); err != nil {
				t.Errorf("validateIfaceName(%q) returned unexpected error: %v", name, err)
			}
		})
	}
}

// TestValidateIfaceName_Invalid verifies that interface names containing
// characters that could allow PF rule injection are rejected.
func TestValidateIfaceName_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		iface string
	}{
		{"empty string", ""},
		{"space in name", "em 0"},
		{"semicolon injection", "em0;pfctl -F all"},
		{"slash", "em0/1"},
		{"newline", "em0\n"},
		{"dollar sign", "$extif"},
		{"backslash", "em0\\foo"},
		{"starts with digit", "0em"},
		{"quote injection", `em0"pass all`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateIfaceName(tt.iface); err == nil {
				t.Errorf("validateIfaceName(%q) expected error but got nil", tt.iface)
			}
		})
	}
}

// ── atomicWriteFile ───────────────────────────────────────────────────────────

// TestAtomicWriteFile verifies that atomicWriteFile writes the correct content,
// sets the requested file permissions, and leaves no temp file behind.
func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "rules.conf")
	content := []byte("# test rule\npass all\n")
	perm := os.FileMode(0o600)

	if err := atomicWriteFile(target, content, perm); err != nil {
		t.Fatalf("atomicWriteFile returned error: %v", err)
	}

	// Content must match exactly.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content mismatch:\nwant: %q\ngot:  %q", content, got)
	}

	// Permissions must match the requested mode.
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected permissions 0600, got %04o", perm)
	}

	// No stale temp files should remain in the directory.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pf-tmp-") {
			t.Errorf("stale temp file left behind: %s", e.Name())
		}
	}
}

// TestAtomicWriteFile_Overwrite verifies that calling atomicWriteFile twice on
// the same path overwrites the previous content correctly.
func TestAtomicWriteFile_Overwrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "rules.conf")

	if err := atomicWriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := atomicWriteFile(target, []byte("updated"), 0o600); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, _ := os.ReadFile(target)
	if string(got) != "updated" {
		t.Errorf("expected %q after overwrite, got %q", "updated", got)
	}
}

// TestAtomicWriteFile_BadPath verifies that atomicWriteFile returns a
// descriptive error when the target directory does not exist.
func TestAtomicWriteFile_BadPath(t *testing.T) {
	// Use a path whose parent directory cannot possibly exist.
	target := filepath.Join(t.TempDir(), "nonexistent_dir", "rules.conf")

	err := atomicWriteFile(target, []byte("data"), 0o600)
	if err == nil {
		t.Fatal("expected error for non-existent directory, got nil")
	}
	if !strings.Contains(err.Error(), "atomicWriteFile") {
		t.Errorf("expected 'atomicWriteFile' in error message, got: %v", err)
	}
}

// ── removeInstanceRules (exact-match semantics) ───────────────────────────────

// TestRemoveInstanceRules_ExactMatch is the critical regression test verifying
// that removing rules for instance "web" does NOT touch rules belonging to
// "web-server" (a different instance whose name starts with "web").
//
// The implementation uses strings.HasSuffix(line, " instance:<name>") so
// "instance:web" and "instance:web-server" are distinct suffixes.
func TestRemoveInstanceRules_ExactMatch(t *testing.T) {
	dir := t.TempDir()
	p := NewPFBackendWithDir(dir)

	rulesFile := filepath.Join(dir, "rdr.rules")

	// Build a synthetic rules file containing entries for both "web" and "web-server".
	webRule := buildRuleBlock("web-nat-tcp-80-8080", "web",
		"rdr pass on em0 proto tcp from any to any port 80 -> 10.0.0.2 port 8080")
	webServerRule := buildRuleBlock("web-server-nat-tcp-443-4430", "web-server",
		"rdr pass on em0 proto tcp from any to any port 443 -> 10.0.0.3 port 4430")

	initialContent := webRule + webServerRule
	if err := os.WriteFile(rulesFile, []byte(initialContent), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Remove rules for "web" only.
	if err := p.removeInstanceRules(rulesFile, "web"); err != nil {
		t.Fatalf("removeInstanceRules: %v", err)
	}

	remaining, err := os.ReadFile(rulesFile)
	if err != nil {
		t.Fatalf("ReadFile after removal: %v", err)
	}
	content := string(remaining)

	// "web" rules must be gone.
	if strings.Contains(content, "instance:web\n") {
		t.Error("rules for instance 'web' should have been removed")
	}
	if strings.Contains(content, "10.0.0.2") {
		t.Error("target IP for 'web' rule should have been removed")
	}

	// "web-server" rules must remain intact.
	if !strings.Contains(content, "instance:web-server") {
		t.Error("rules for instance 'web-server' must NOT be removed")
	}
	if !strings.Contains(content, "10.0.0.3") {
		t.Error("target IP for 'web-server' rule must remain")
	}
}

// TestRemoveInstanceRules_NonExistentFile verifies that removeInstanceRules
// silently succeeds when the rules file does not exist yet.
func TestRemoveInstanceRules_NonExistentFile(t *testing.T) {
	dir := t.TempDir()
	p := NewPFBackendWithDir(dir)

	// Point at a file that does not exist.
	missing := filepath.Join(dir, "no-such-file.rules")
	if err := p.removeInstanceRules(missing, "web"); err != nil {
		t.Errorf("expected no error for missing file, got: %v", err)
	}
}

// TestRemoveInstanceRules_OnlyMatchingRemoved verifies that when a file
// contains rules for multiple distinct instances, only the targeted instance's
// rules are removed and all others are preserved verbatim.
func TestRemoveInstanceRules_OnlyMatchingRemoved(t *testing.T) {
	dir := t.TempDir()
	p := NewPFBackendWithDir(dir)
	rulesFile := filepath.Join(dir, "nat.rules")

	instances := []string{"alpha", "beta", "gamma"}
	var content strings.Builder
	for _, inst := range instances {
		content.WriteString(buildRuleBlock(
			fmt.Sprintf("%s-nat", inst),
			inst,
			fmt.Sprintf("nat on em0 from 10.%s.0.0/24 to any -> (em0)", inst),
		))
	}
	if err := os.WriteFile(rulesFile, []byte(content.String()), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := p.removeInstanceRules(rulesFile, "beta"); err != nil {
		t.Fatalf("removeInstanceRules: %v", err)
	}

	got, _ := os.ReadFile(rulesFile)
	result := string(got)

	if strings.Contains(result, "instance:beta") {
		t.Error("rules for 'beta' should be removed")
	}
	if !strings.Contains(result, "instance:alpha") {
		t.Error("rules for 'alpha' must be preserved")
	}
	if !strings.Contains(result, "instance:gamma") {
		t.Error("rules for 'gamma' must be preserved")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// buildRuleBlock constructs a two-line PF rule block in the format written by
// appendRule / appendRules: a comment line with the ID and instance name,
// followed by the rule line.
func buildRuleBlock(id, instance, rule string) string {
	return fmt.Sprintf("# ID: %s instance:%s\n%s\n", id, instance, rule)
}

func TestDedupeRuleLinesKeepsOneCopyPerRule(t *testing.T) {
	// Two jails on the same network each store the NAT rule for it.
	content := []byte(`# ID: web-jail-nat instance:web
nat on re0 inet from 10.0.0.0/24 to any -> (re0)
# ID: db-jail-nat instance:db
nat on re0 inet from 10.0.0.0/24 to any -> (re0)
# ID: vm-nat instance:vm
nat on re0 inet from 10.10.0.0/24 to any -> (re0)
`)

	got := string(dedupeRuleLines(content))
	want := `# ID: web-jail-nat instance:web
nat on re0 inet from 10.0.0.0/24 to any -> (re0)
# ID: vm-nat instance:vm
nat on re0 inet from 10.10.0.0/24 to any -> (re0)
`
	if got != want {
		t.Errorf("dedupeRuleLines:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestDedupeRuleLinesKeepsDistinctRules(t *testing.T) {
	content := []byte(`# ID: a instance:web
rdr pass on re0 proto tcp to port 80 -> 10.0.0.10 port 80
# ID: b instance:db
rdr pass on re0 proto tcp to port 443 -> 10.0.0.11 port 443
`)

	if got := string(dedupeRuleLines(content)); got != string(content) {
		t.Errorf("distinct rules were altered:\ngot:\n%s\nwant:\n%s", got, content)
	}
}
