package firewall

import (
	"os"
	"path/filepath"
	"testing"
)

// Tests for PFBackend pure-Go functions that don't require pfctl or system access.

func newTestPF(t *testing.T) *PFBackend {
	t.Helper()
	return NewPFBackendWithDir(t.TempDir())
}

// ── parseRdrRule ──────────────────────────────────────────────────────────────

func TestParseRdrRule_ValidRule(t *testing.T) {
	p := newTestPF(t)
	// Minimal 12-field rdr rule matching the parser's field scan
	rule := "rdr pass on em0 proto tcp from any to any port 8080 -> 10.0.0.2 port 80"
	m := p.parseRdrRule(rule, "id1", "inst1")
	if m == nil {
		t.Fatal("expected non-nil mapping")
	}
	if m.HostPort != 8080 {
		t.Errorf("HostPort: got %d, want 8080", m.HostPort)
	}
	if m.TargetPort != 80 {
		t.Errorf("TargetPort: got %d, want 80", m.TargetPort)
	}
	if m.Protocol != "tcp" {
		t.Errorf("Protocol: got %s, want tcp", m.Protocol)
	}
	if m.TargetIP != "10.0.0.2" {
		t.Errorf("TargetIP: got %s, want 10.0.0.2", m.TargetIP)
	}
	if m.ID != "id1" {
		t.Errorf("ID: got %s, want id1", m.ID)
	}
	if m.Instance != "inst1" {
		t.Errorf("Instance: got %s, want inst1", m.Instance)
	}
}

func TestParseRdrRule_TooShort(t *testing.T) {
	p := newTestPF(t)
	// Fewer than 12 fields — must return nil
	m := p.parseRdrRule("rdr pass on em0", "id", "inst")
	if m != nil {
		t.Errorf("expected nil for short rule, got %+v", m)
	}
}

func TestParseRdrRule_MissingHostPort(t *testing.T) {
	p := newTestPF(t)
	// No 'port' keyword → hostPort stays 0 → returns nil
	rule := "rdr pass on em0 proto tcp from any to any addr 10.0.0.1 -> 10.0.0.2 addr 80"
	m := p.parseRdrRule(rule, "id", "inst")
	if m != nil {
		t.Errorf("expected nil when hostPort == 0, got %+v", m)
	}
}

// ── ruleExists ───────────────────────────────────────────────────────────────

func TestRuleExists_FileNotExist(t *testing.T) {
	p := newTestPF(t)
	if p.ruleExists("/nonexistent/path/rules.conf", "id1") {
		t.Error("expected false for non-existent file")
	}
}

func TestRuleExists_Present(t *testing.T) {
	p := newTestPF(t)
	f := filepath.Join(p.rulesDir, "test.rules")
	content := "# ID: myid instance:myinstance\nrdr pass on em0 ...\n"
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if !p.ruleExists(f, "myid") {
		t.Error("expected true, rule should exist")
	}
}

func TestRuleExists_Absent(t *testing.T) {
	p := newTestPF(t)
	f := filepath.Join(p.rulesDir, "test.rules")
	content := "# ID: otherid instance:foo\nrdr pass ...\n"
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if p.ruleExists(f, "myid") {
		t.Error("expected false, 'myid' not in file")
	}
}

// ── appendRule ────────────────────────────────────────────────────────────────

func TestAppendRule_CreatesFile(t *testing.T) {
	p := newTestPF(t)
	f := filepath.Join(p.rulesDir, "rdr.rules")
	if err := p.appendRule(f, "rule1", "myinst", "rdr pass on em0 proto tcp from any to any port 9000 -> 10.0.0.1 port 80"); err != nil {
		t.Fatalf("appendRule: %v", err)
	}
	content, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	s := string(content)
	if !contains(s, "# ID: rule1 instance:myinst") {
		t.Errorf("ID comment not written, got: %s", s)
	}
	if !contains(s, "rdr pass on em0") {
		t.Errorf("rule not written, got: %s", s)
	}
}

func TestAppendRule_Idempotent(t *testing.T) {
	p := newTestPF(t)
	f := filepath.Join(p.rulesDir, "rdr.rules")
	rule := "rdr pass on em0 proto tcp from any to any port 9000 -> 10.0.0.1 port 80"
	_ = p.appendRule(f, "rule1", "myinst", rule)
	_ = p.appendRule(f, "rule1", "myinst", rule) // second call — should be no-op
	content, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	// Count occurrences of the ID comment
	count := countOccurrences(string(content), "# ID: rule1 ")
	if count != 1 {
		t.Errorf("expected exactly 1 ID comment after duplicate append, got %d", count)
	}
}

func TestAppendRule_NewlineInjection(t *testing.T) {
	p := newTestPF(t)

	tests := []struct {
		name     string
		id       string
		instance string
		rule     string
		wantErr  bool
	}{
		{
			name:     "valid rule no error",
			id:       "rule1",
			instance: "myinst",
			rule:     "rdr pass on em0 proto tcp from any to any port 9000 -> 10.0.0.1 port 80",
			wantErr:  false,
		},
		{
			name:     "rule with newline",
			id:       "rule2",
			instance: "myinst",
			rule:     "rdr pass on em0 proto tcp from any to any port 9000 -> 10.0.0.1 port 80\nrdr pass on em0 proto tcp from any to any port 9001 -> 10.0.0.1 port 81",
			wantErr:  true,
		},
		{
			name:     "rule with crlf",
			id:       "rule3",
			instance: "myinst",
			rule:     "rdr pass on em0 proto tcp from any to any port 9000 -> 10.0.0.1 port 80\r\nextra",
			wantErr:  true,
		},
		{
			name:     "rule with trailing newline ok",
			id:       "rule4",
			instance: "myinst",
			rule:     "rdr pass on em0 proto tcp from any to any port 9000 -> 10.0.0.1 port 80\n",
			wantErr:  false,
		},
		{
			name:     "instance name with newline",
			id:       "rule5",
			instance: "my\ninst",
			rule:     "rdr pass on em0 proto tcp from any to any port 9000 -> 10.0.0.1 port 80",
			wantErr:  true,
		},
		{
			name:     "id with newline",
			id:       "rule\n6",
			instance: "myinst",
			rule:     "rdr pass on em0 proto tcp from any to any port 9000 -> 10.0.0.1 port 80",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use a fresh file each time
			tf := filepath.Join(t.TempDir(), "rdr.rules")
			err := p.appendRule(tf, tt.id, tt.instance, tt.rule)
			if (err != nil) != tt.wantErr {
				t.Errorf("appendRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// ── appendRules ───────────────────────────────────────────────────────────────

func TestAppendRules_MultipleLines(t *testing.T) {
	p := newTestPF(t)
	f := filepath.Join(p.rulesDir, "rdr.rules")
	rules := []string{
		"rdr pass on em0 proto tcp from any to any port 7000 -> 10.0.0.1 port 80",
		"rdr pass on em0 proto tcp from any to any port 7001 -> 10.0.0.1 port 81",
	}
	if err := p.appendRules(f, "ruleset1", "inst1", rules); err != nil {
		t.Fatalf("appendRules: %v", err)
	}
	content, _ := os.ReadFile(f)
	s := string(content)
	if !contains(s, "# ID: ruleset1 instance:inst1") {
		t.Errorf("missing ID comment: %s", s)
	}
	if !contains(s, "port 7000") || !contains(s, "port 7001") {
		t.Errorf("missing rules: %s", s)
	}
}

func TestAppendRules_Idempotent(t *testing.T) {
	p := newTestPF(t)
	f := filepath.Join(p.rulesDir, "rdr.rules")
	rules := []string{"rdr pass on em0 proto tcp from any to any port 7000 -> 10.0.0.1 port 80"}
	_ = p.appendRules(f, "rs1", "i1", rules)
	_ = p.appendRules(f, "rs1", "i1", rules)
	content, _ := os.ReadFile(f)
	count := countOccurrences(string(content), "# ID: rs1 ")
	if count != 1 {
		t.Errorf("expected 1 ID occurrence, got %d", count)
	}
}

// ── removeRule ────────────────────────────────────────────────────────────────

func TestRemoveRule_RemovesMatchingID(t *testing.T) {
	p := newTestPF(t)
	f := filepath.Join(p.rulesDir, "rdr.rules")
	content := "# ID: keep instance:i1\nrdr pass on em0 proto tcp from any to any port 7000 -> 10.0.0.1 port 80\n" +
		"# ID: del instance:i2\nrdr pass on em0 proto tcp from any to any port 7001 -> 10.0.0.2 port 81\n"
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.removeRule(f, "del"); err != nil {
		t.Fatalf("removeRule: %v", err)
	}
	after, _ := os.ReadFile(f)
	s := string(after)
	if contains(s, "# ID: del ") {
		t.Error("ID 'del' should have been removed")
	}
	if !contains(s, "# ID: keep ") {
		t.Error("ID 'keep' should remain")
	}
}

func TestRemoveRule_NonExistentFile(t *testing.T) {
	p := newTestPF(t)
	if err := p.removeRule("/nonexistent/path/rules.conf", "anid"); err != nil {
		t.Errorf("expected nil for missing file, got %v", err)
	}
}

func TestRemoveRule_IDNotPresent(t *testing.T) {
	p := newTestPF(t)
	f := filepath.Join(p.rulesDir, "rdr.rules")
	content := "# ID: otherid instance:i1\nrdr pass on em0 ...\n"
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.removeRule(f, "missing"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	after, _ := os.ReadFile(f)
	if string(after) != content {
		t.Error("file should be unchanged when ID not found")
	}
}

// ── hostPortInUse ─────────────────────────────────────────────────────────────

func TestHostPortInUse_InUse(t *testing.T) {
	p := newTestPF(t)
	f := filepath.Join(p.rulesDir, "rdr.rules")
	content := "# ID: r1\nrdr pass on em0 proto tcp from any to any port 8080 -> 10.0.0.1 port 80\n"
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	id := p.hostPortInUse(f, 8080, "tcp")
	if id != "r1" {
		t.Errorf("expected 'r1', got %q", id)
	}
}

func TestHostPortInUse_Free(t *testing.T) {
	p := newTestPF(t)
	f := filepath.Join(p.rulesDir, "rdr.rules")
	content := "# ID: r1\nrdr pass on em0 proto tcp from any to any port 8080 -> 10.0.0.1 port 80\n"
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	id := p.hostPortInUse(f, 9090, "tcp")
	if id != "" {
		t.Errorf("expected empty, got %q", id)
	}
}

func TestHostPortInUse_FileNotExist(t *testing.T) {
	p := newTestPF(t)
	id := p.hostPortInUse("/nonexistent/file", 80, "tcp")
	if id != "" {
		t.Errorf("expected empty for missing file, got %q", id)
	}
}

// ── DetectBackend / NewBackend ─────────────────────────────────────────────────

func TestDetectBackend_FreeBSD(t *testing.T) {
	// On FreeBSD, DetectBackend should return PF without error.
	// This test runs correctly on our FreeBSD CI target.
	bt, err := DetectBackend()
	if err != nil {
		// Accept error on non-FreeBSD/non-Linux platforms
		t.Logf("DetectBackend returned error (expected on this OS): %v", err)
		return
	}
	if bt == "" {
		t.Error("expected non-empty backend type")
	}
}

func TestNewBackend_ValidTypes(t *testing.T) {
	for _, bt := range []BackendType{BackendTypePF, BackendTypeIPTables, BackendTypeNFTables} {
		b, err := NewBackend(bt)
		if err != nil {
			t.Errorf("NewBackend(%s): unexpected error: %v", bt, err)
			continue
		}
		if b == nil {
			t.Errorf("NewBackend(%s): got nil backend", bt)
		}
	}
}

func TestNewBackend_InvalidType(t *testing.T) {
	_, err := NewBackend("unknown")
	if err == nil {
		t.Error("expected error for unknown backend type")
	}
}

// ── NewManagerWithBackend constructor ─────────────────────────────────────────

func TestNewManagerWithBackend(t *testing.T) {
	mb := &mockBackend{}
	m := NewManagerWithBackend(mb)
	if m == nil {
		t.Fatal("expected non-nil Manager")
	}
	if m.backend != mb {
		t.Error("Manager.backend should be the provided mockBackend")
	}
	if m.rulesDir != DefaultRulesDir {
		t.Errorf("expected default rules dir %s, got %s", DefaultRulesDir, m.rulesDir)
	}
}

// ── generateNatRule ───────────────────────────────────────────────────────────

func TestGenerateNatRule_Valid(t *testing.T) {
	p := newTestPF(t)
	rule := NATRule{
		OutInterface:  "em0",
		SourceNetwork: "10.0.0.0/24",
		Instance:      "inst1",
	}
	got, err := p.generateNatRule(rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "nat on em0 from 10.0.0.0/24 to any -> (em0)"
	if got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestGenerateNatRule_InvalidInterface(t *testing.T) {
	p := newTestPF(t)
	rule := NATRule{
		OutInterface:  "em0; rm -rf /",
		SourceNetwork: "10.0.0.0/24",
	}
	_, err := p.generateNatRule(rule)
	if err == nil {
		t.Error("expected error for invalid interface name")
	}
}

// ── generateRdrRules ──────────────────────────────────────────────────────────

func TestGenerateRdrRules_Valid(t *testing.T) {
	p := newTestPF(t)
	mapping := PortMapping{
		HostInterface: "em0",
		Protocol:      "tcp",
		HostPort:      8080,
		TargetIP:      "10.0.0.5",
		TargetPort:    80,
	}
	rules, err := p.generateRdrRules(mapping)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) == 0 {
		t.Fatal("expected at least one rule")
	}
	if !contains(rules[0], "rdr pass on em0") {
		t.Errorf("rule missing 'rdr pass on em0': %s", rules[0])
	}
	if !contains(rules[0], "port 8080") {
		t.Errorf("rule missing host port 8080: %s", rules[0])
	}
	if !contains(rules[0], "10.0.0.5 port 80") {
		t.Errorf("rule missing target: %s", rules[0])
	}
}

func TestGenerateRdrRules_InvalidInterface(t *testing.T) {
	p := newTestPF(t)
	mapping := PortMapping{
		HostInterface: "em0; drop tables",
		Protocol:      "tcp",
		HostPort:      8080,
		TargetIP:      "10.0.0.5",
		TargetPort:    80,
	}
	_, err := p.generateRdrRules(mapping)
	if err == nil {
		t.Error("expected error for invalid interface name")
	}
}

// ── findInstancePortMappings ──────────────────────────────────────────────────

func TestFindInstancePortMappings_FileNotExist(t *testing.T) {
	p := newTestPF(t)
	mappings := p.findInstancePortMappings("/nonexistent/file.rules", "myinst")
	if mappings != nil {
		t.Errorf("expected nil for missing file, got %v", mappings)
	}
}

func TestFindInstancePortMappings_InstanceAbsent(t *testing.T) {
	p := newTestPF(t)
	f := filepath.Join(p.rulesDir, "rdr.rules")
	content := "# ID: r1 instance:other\nrdr pass on em0 proto tcp from any to any port 8080 -> 10.0.0.1 port 80\n"
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	mappings := p.findInstancePortMappings(f, "myinst")
	if len(mappings) != 0 {
		t.Errorf("expected 0 mappings for absent instance, got %d", len(mappings))
	}
}

func TestFindInstancePortMappings_InstancePresent(t *testing.T) {
	p := newTestPF(t)
	f := filepath.Join(p.rulesDir, "rdr.rules")
	content := "# ID: r1 instance:myinst\n" +
		"rdr pass on em0 proto tcp from any to any port 8080 -> 10.0.0.5 port 80\n" +
		"\n" +
		"# ID: r2 instance:other\n" +
		"rdr pass on em0 proto tcp from any to any port 9090 -> 10.0.0.6 port 90\n"
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	mappings := p.findInstancePortMappings(f, "myinst")
	if len(mappings) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(mappings))
	}
	if mappings[0].HostPort != 8080 {
		t.Errorf("HostPort: got %d, want 8080", mappings[0].HostPort)
	}
	if mappings[0].TargetPort != 80 {
		t.Errorf("TargetPort: got %d, want 80", mappings[0].TargetPort)
	}
}

// ── atomicWriteFile ───────────────────────────────────────────────────────────

func TestAtomicWriteFile_Success(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "output.conf")
	data := []byte("# test rule\nrdr pass on em0 ...\n")
	if err := atomicWriteFile(dst, data, 0o600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Errorf("content mismatch: got %q, want %q", got, data)
	}
}

func TestAtomicWriteFile_BadDir(t *testing.T) {
	err := atomicWriteFile("/nonexistent/dir/file.conf", []byte("x"), 0o600)
	if err == nil {
		t.Error("expected error writing to non-existent directory")
	}
}

// ── removeInstanceRules ───────────────────────────────────────────────────────

func TestRemoveInstanceRules_RemovesOnlyTargetInstance(t *testing.T) {
	p := newTestPF(t)
	f := filepath.Join(p.rulesDir, "rdr.rules")
	content := "# ID: r1 instance:target\n" +
		"rdr pass on em0 proto tcp from any to any port 8080 -> 10.0.0.5 port 80\n" +
		"\n" +
		"# ID: r2 instance:other\n" +
		"rdr pass on em0 proto tcp from any to any port 9090 -> 10.0.0.6 port 90\n"
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.removeInstanceRules(f, "target"); err != nil {
		t.Fatalf("removeInstanceRules: %v", err)
	}
	after, _ := os.ReadFile(f)
	s := string(after)
	if contains(s, "instance:target") {
		t.Error("target instance rules should have been removed")
	}
	if !contains(s, "instance:other") {
		t.Error("other instance rules should remain")
	}
}

func TestRemoveInstanceRules_FileNotExist(t *testing.T) {
	p := newTestPF(t)
	if err := p.removeInstanceRules("/nonexistent/file.rules", "anyinstance"); err != nil {
		t.Errorf("expected nil for missing file, got %v", err)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

// ── natAnchorDeclared ─────────────────────────────────────────────────────────

// TestNatAnchorDeclared verifies the daemon recognizes the hospitus anchor only in
// the exact bare form it loads rules into (pfctl -a hospitus). The "hospitus/*" glob
// form matches sub-anchors, not the bare "hospitus" anchor, so it must NOT satisfy
// the check — this is why the anchor lines Hospitus tells operators to add use the
// bare form.
func TestNatAnchorDeclared(t *testing.T) {
	tests := []struct {
		name    string
		ruleset string
		want    bool
	}{
		{"declared bare form", "nat-anchor \"hospitus\" all\n", true},
		{"declared among other anchors", "nat-anchor \"other\"\nnat-anchor \"hospitus\"\n", true},
		{"not declared", "nat-anchor \"other\" all\n", false},
		{"glob form does not satisfy exact anchor", "nat-anchor \"hospitus/*\" all\n", false},
		{"empty ruleset", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := natAnchorDeclared(tt.ruleset); got != tt.want {
				t.Errorf("natAnchorDeclared(%q) = %v, want %v", tt.ruleset, got, tt.want)
			}
		})
	}
}

func countOccurrences(s, sub string) int {
	count := 0
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			count++
			i += len(sub) - 1
		}
	}
	return count
}

// TestRuleExists_OrphanedMarker covers the shape that cost a host its DHCP.
//
// A marker with nothing under it made ruleExists answer yes, so AddFilterRule
// skipped writing the rule — for good. The bhyve NAT bridge's DHCP pass rule
// disappeared this way and could never be restored: dnsmasq stopped seeing
// DISCOVER packets, every VM on the bridge fell back to a link-local address,
// and the file went on claiming the rule was there.
func TestRuleExists_OrphanedMarker(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "marker followed by its rule",
			content: "# ID: dhcp instance:bridge\npass in quick on nat proto udp\n",
			want:    true,
		},
		{
			name:    "marker followed by another marker",
			content: "# ID: dhcp instance:bridge\n# ID: other instance:vm\npass in inet from 10.0.0.0/24\n",
			want:    false,
		},
		{
			name:    "marker followed by a blank line",
			content: "# ID: dhcp instance:bridge\n\npass in quick on nat proto udp\n",
			want:    false,
		},
		{
			name:    "marker at the end of the file",
			content: "pass in inet from 10.0.0.0/24\n# ID: dhcp instance:bridge\n",
			want:    false,
		},
		{
			name:    "orphaned once, written properly later",
			content: "# ID: dhcp instance:bridge\n# ID: dhcp instance:bridge\npass in quick on nat proto udp\n",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newTestPF(t)
			f := filepath.Join(p.rulesDir, "test.rules")
			if err := os.WriteFile(f, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}

			if got := p.ruleExists(f, "dhcp"); got != tt.want {
				t.Errorf("ruleExists = %v, want %v — a marker without its rule must "+
					"not stop the rule from being written", got, tt.want)
			}
		})
	}
}
