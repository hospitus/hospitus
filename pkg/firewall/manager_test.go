package firewall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerSaveRuleSetPermissions(t *testing.T) {
	// Create a temporary directory for rules
	tempDir := t.TempDir()

	// Initialize manager with temp dir
	m := &Manager{
		rulesDir: tempDir,
	}

	// Create a dummy ruleset
	ruleSet := RuleSet{
		Instance: "test-instance",
		Provider: "test-provider",
	}

	// Save the ruleset
	err := m.saveRuleSet("test-instance", ruleSet)
	if err != nil {
		t.Fatalf("Failed to save ruleset: %v", err)
	}

	// Check file permissions
	ruleFile := filepath.Join(tempDir, "test-instance.json")
	info, err := os.Stat(ruleFile)
	if err != nil {
		t.Fatalf("Failed to stat rule file: %v", err)
	}

	// The permissions should be 0600.
	// We mask with 0777 to ignore extra bits like setuid/setgid
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("Expected file permissions 0600, got %04o", perm)
	}
}

// TestNATRuleIDIsPerNetwork covers the reason a jail on two networks reached
// nothing: the ID was the instance name alone, so the backend skipped the
// second network as a duplicate.
func TestNATRuleIDIsPerNetwork(t *testing.T) {
	internal := natRuleID("web", "10.31.0.0/24")
	public := natRuleID("web", "10.0.0.0/24")

	if internal == public {
		t.Errorf("both networks of one instance share the rule ID %q", internal)
	}
	if !strings.HasPrefix(internal, "web-nat") {
		t.Errorf("ID %q no longer names the instance and its purpose", internal)
	}
	// The ID ends up in a PF rule comment, so it must not carry a separator
	// that would end the comment or split the rule.
	for _, id := range []string{internal, public} {
		if strings.ContainsAny(id, "/\n\r ") {
			t.Errorf("rule ID %q contains a character that cannot appear in a rule comment", id)
		}
	}

	// Two instances on the same network stay distinct.
	if natRuleID("web", "10.0.0.0/24") == natRuleID("db", "10.0.0.0/24") {
		t.Error("two instances on one network share a rule ID")
	}
}
