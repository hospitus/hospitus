package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── mockBackend ───────────────────────────────────────────────────────────────

// mockBackend implements Backend with configurable error injection.
// All operations are no-ops unless an error is set via the err* fields.
type mockBackend struct {
	applyErr      error
	addPortErr    error
	removePortErr error
	addNATErr     error
	removeNATErr  error
	removeAllErr  error
	cleanupErr    error
}

func (m *mockBackend) Name() BackendType                    { return "mock" }
func (m *mockBackend) IsAvailable() bool                    { return true }
func (m *mockBackend) Initialize(ctx context.Context) error { return nil }
func (m *mockBackend) AddPortMapping(ctx context.Context, pm PortMapping) error {
	return m.addPortErr
}

func (m *mockBackend) RemovePortMapping(ctx context.Context, pm PortMapping) error {
	return m.removePortErr
}
func (m *mockBackend) AddNATRule(ctx context.Context, r NATRule) error { return m.addNATErr }
func (m *mockBackend) RemoveNATRule(ctx context.Context, r NATRule) error {
	return m.removeNATErr
}

func (m *mockBackend) ApplyRules(ctx context.Context, rs RuleSet) error {
	return m.applyErr
}

func (m *mockBackend) RemoveAllRules(ctx context.Context, instance string) error {
	return m.removeAllErr
}
func (m *mockBackend) ListRules(ctx context.Context) ([]PortMapping, error) { return nil, nil }
func (m *mockBackend) Cleanup(ctx context.Context) error                    { return m.cleanupErr }

// newTestManagerWithMock builds a Manager with a mock backend writing to a temp dir.
func newTestManagerWithMock(t *testing.T, be *mockBackend) *Manager {
	t.Helper()
	return NewManagerWithBackendAndDir(be, t.TempDir())
}

// ── NewManagerWithBackendAndDir ───────────────────────────────────────────────

// TestNewManagerWithBackendAndDir_EmptyRulesDir verifies that an empty rulesDir
// is replaced with the package default.
func TestNewManagerWithBackendAndDir_EmptyRulesDir(t *testing.T) {
	m := NewManagerWithBackendAndDir(&mockBackend{}, "")
	if m.rulesDir != DefaultRulesDir {
		t.Errorf("expected DefaultRulesDir %q, got %q", DefaultRulesDir, m.rulesDir)
	}
}

// TestNewManagerWithBackendAndDir_CustomRulesDir verifies that a non-empty
// rulesDir is preserved as-is.
func TestNewManagerWithBackendAndDir_CustomRulesDir(t *testing.T) {
	custom := "/tmp/custom-rules"
	m := NewManagerWithBackendAndDir(&mockBackend{}, custom)
	if m.rulesDir != custom {
		t.Errorf("expected rulesDir %q, got %q", custom, m.rulesDir)
	}
}

// ── loadRuleSet ───────────────────────────────────────────────────────────────

// TestLoadRuleSet_NotFound verifies that loading a rule set for an instance
// whose file does not exist returns an os.IsNotExist error.
func TestLoadRuleSet_NotFound(t *testing.T) {
	m := newTestManagerWithMock(t, &mockBackend{})
	_, err := m.loadRuleSet("ghost-instance")
	if err == nil {
		t.Fatal("expected error for missing rule set, got nil")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected IsNotExist error, got: %v", err)
	}
}

// TestLoadRuleSet_CorruptJSON verifies that a malformed JSON file returns
// a parse error (not a not-found error).
func TestLoadRuleSet_CorruptJSON(t *testing.T) {
	m := newTestManagerWithMock(t, &mockBackend{})
	ruleFile := filepath.Join(m.rulesDir, "bad.json")
	if err := os.WriteFile(ruleFile, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := m.loadRuleSet("bad")
	if err == nil {
		t.Fatal("expected error for corrupt JSON, got nil")
	}
	if os.IsNotExist(err) {
		t.Error("error should not be IsNotExist for corrupt JSON")
	}
}

// TestLoadRuleSet_Success verifies round-trip: save then load returns same data.
func TestLoadRuleSet_Success(t *testing.T) {
	m := newTestManagerWithMock(t, &mockBackend{})
	rs := RuleSet{
		Instance: "web",
		Provider: "jail",
		PortMappings: []PortMapping{
			{ID: "web-jail-tcp-80-8080", Instance: "web", Provider: "jail", Protocol: ProtocolTCP, HostPort: 80, TargetPort: 8080, TargetIP: "10.0.0.2"},
		},
	}
	if err := m.saveRuleSet("web", rs); err != nil {
		t.Fatalf("saveRuleSet: %v", err)
	}

	got, err := m.loadRuleSet("web")
	if err != nil {
		t.Fatalf("loadRuleSet: %v", err)
	}
	if got.Instance != rs.Instance {
		t.Errorf("expected Instance=%q, got %q", rs.Instance, got.Instance)
	}
	if len(got.PortMappings) != 1 {
		t.Errorf("expected 1 PortMapping, got %d", len(got.PortMappings))
	}
	if got.PortMappings[0].HostPort != 80 {
		t.Errorf("expected HostPort=80, got %d", got.PortMappings[0].HostPort)
	}
}

// ── saveRuleSet ───────────────────────────────────────────────────────────────

// TestSaveRuleSet_BadDir verifies that saveRuleSet returns an error when the
// rules directory does not exist (write fails).
func TestSaveRuleSet_BadDir(t *testing.T) {
	m := &Manager{
		rulesDir: filepath.Join(t.TempDir(), "nonexistent"),
	}
	err := m.saveRuleSet("instance", RuleSet{Instance: "instance"})
	if err == nil {
		t.Fatal("expected error writing to non-existent directory, got nil")
	}
}

// ── persistMapping ────────────────────────────────────────────────────────────

// TestPersistMapping_NewEntry verifies that a new port mapping is appended.
func TestPersistMapping_NewEntry(t *testing.T) {
	m := newTestManagerWithMock(t, &mockBackend{})
	pm := PortMapping{
		ID:         "web-jail-tcp-80-8080",
		Instance:   "web",
		Provider:   "jail",
		Protocol:   ProtocolTCP,
		HostPort:   80,
		TargetPort: 8080,
		TargetIP:   "10.0.0.2",
	}

	if err := m.persistMapping("web", pm); err != nil {
		t.Fatalf("persistMapping: %v", err)
	}

	rs, err := m.loadRuleSet("web")
	if err != nil {
		t.Fatalf("loadRuleSet: %v", err)
	}
	if len(rs.PortMappings) != 1 {
		t.Errorf("expected 1 mapping, got %d", len(rs.PortMappings))
	}
}

// TestPersistMapping_DuplicateNoOp verifies that submitting the same
// host port + protocol combination a second time does NOT create a duplicate.
func TestPersistMapping_DuplicateNoOp(t *testing.T) {
	m := newTestManagerWithMock(t, &mockBackend{})
	pm := PortMapping{
		ID: "web-jail-tcp-80-8080", Instance: "web", Provider: "jail",
		Protocol: ProtocolTCP, HostPort: 80, TargetPort: 8080, TargetIP: "10.0.0.2",
	}

	// First call adds the entry.
	if err := m.persistMapping("web", pm); err != nil {
		t.Fatalf("first persistMapping: %v", err)
	}
	// Second call with same HostPort+Protocol must not duplicate.
	if err := m.persistMapping("web", pm); err != nil {
		t.Fatalf("second persistMapping: %v", err)
	}

	rs, _ := m.loadRuleSet("web")
	if len(rs.PortMappings) != 1 {
		t.Errorf("expected exactly 1 mapping after duplicate, got %d", len(rs.PortMappings))
	}
}

// ── persistNATRule ────────────────────────────────────────────────────────────

// TestPersistNATRule_NewEntry verifies a new NAT rule is appended.
func TestPersistNATRule_NewEntry(t *testing.T) {
	m := newTestManagerWithMock(t, &mockBackend{})
	rule := NATRule{
		ID: "web-nat", Instance: "web", Provider: "jail",
		SourceNetwork: "10.0.0.0/24", OutInterface: "em0", Active: true,
	}

	if err := m.persistNATRule("web", rule); err != nil {
		t.Fatalf("persistNATRule: %v", err)
	}

	rs, err := m.loadRuleSet("web")
	if err != nil {
		t.Fatalf("loadRuleSet: %v", err)
	}
	if len(rs.NATRules) != 1 {
		t.Errorf("expected 1 NAT rule, got %d", len(rs.NATRules))
	}
}

// TestPersistNATRule_UpdateExisting verifies that re-persisting a rule with
// the same ID updates it in-place (no duplicates).
func TestPersistNATRule_UpdateExisting(t *testing.T) {
	m := newTestManagerWithMock(t, &mockBackend{})
	rule := NATRule{
		ID: "web-nat", Instance: "web", Provider: "jail",
		SourceNetwork: "10.0.0.0/24", OutInterface: "em0", Active: true,
	}
	if err := m.persistNATRule("web", rule); err != nil {
		t.Fatalf("initial persistNATRule: %v", err)
	}

	// Update the OutInterface.
	rule.OutInterface = "vtnet0"
	if err := m.persistNATRule("web", rule); err != nil {
		t.Fatalf("update persistNATRule: %v", err)
	}

	rs, _ := m.loadRuleSet("web")
	if len(rs.NATRules) != 1 {
		t.Errorf("expected 1 NAT rule after update, got %d", len(rs.NATRules))
	}
	if rs.NATRules[0].OutInterface != "vtnet0" {
		t.Errorf("expected OutInterface=vtnet0, got %q", rs.NATRules[0].OutInterface)
	}
}

// ── ListExposedPortsReadOnly ──────────────────────────────────────────────────

// TestListExposedPortsReadOnly_Missing verifies nil, nil is returned when no
// rule file exists for an instance.
func TestListExposedPortsReadOnly_Missing(t *testing.T) {
	m := newTestManagerWithMock(t, &mockBackend{})
	ports, err := m.ListExposedPortsReadOnly("ghost")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ports != nil {
		t.Errorf("expected nil ports, got %v", ports)
	}
}

// TestListExposedPortsReadOnly_Found verifies the correct mappings are returned
// when a rule file exists.
func TestListExposedPortsReadOnly_Found(t *testing.T) {
	m := newTestManagerWithMock(t, &mockBackend{})
	pm := PortMapping{
		ID: "web-jail-tcp-80-8080", Instance: "web", Provider: "jail",
		Protocol: ProtocolTCP, HostPort: 80, TargetPort: 8080, TargetIP: "10.0.0.2",
	}
	if err := m.persistMapping("web", pm); err != nil {
		t.Fatalf("persistMapping: %v", err)
	}

	ports, err := m.ListExposedPortsReadOnly("web")
	if err != nil {
		t.Fatalf("ListExposedPortsReadOnly: %v", err)
	}
	if len(ports) != 1 {
		t.Errorf("expected 1 port mapping, got %d", len(ports))
	}
	if ports[0].HostPort != 80 {
		t.Errorf("expected HostPort=80, got %d", ports[0].HostPort)
	}
}

// ── GetRuleSet ────────────────────────────────────────────────────────────────

// TestGetRuleSet_NotFound verifies that GetRuleSet returns an empty RuleSet
// (not an error) when the file does not exist.
func TestGetRuleSet_NotFound(t *testing.T) {
	m := newTestManagerWithMock(t, &mockBackend{})
	rs, err := m.GetRuleSet(context.Background(), "missing")
	if err != nil {
		t.Fatalf("GetRuleSet: %v", err)
	}
	if rs.Instance != "missing" {
		t.Errorf("expected Instance=%q, got %q", "missing", rs.Instance)
	}
	if len(rs.PortMappings) != 0 || len(rs.NATRules) != 0 {
		t.Error("expected empty rule set for missing instance")
	}
}

// TestGetRuleSet_Found verifies the persisted rule set is returned correctly.
func TestGetRuleSet_Found(t *testing.T) {
	m := newTestManagerWithMock(t, &mockBackend{})
	pm := PortMapping{
		ID: "db-jail-tcp-5432-5432", Instance: "db", Provider: "jail",
		Protocol: ProtocolTCP, HostPort: 5432, TargetPort: 5432, TargetIP: "10.0.0.5",
	}
	if err := m.persistMapping("db", pm); err != nil {
		t.Fatalf("persistMapping: %v", err)
	}

	rs, err := m.GetRuleSet(context.Background(), "db")
	if err != nil {
		t.Fatalf("GetRuleSet: %v", err)
	}
	if len(rs.PortMappings) != 1 {
		t.Errorf("expected 1 port mapping, got %d", len(rs.PortMappings))
	}
}

// ── RemoveAllRules ────────────────────────────────────────────────────────────

// TestRemoveAllRules_Success verifies the rule file is deleted from disk and
// the backend is called.
func TestRemoveAllRules_Success(t *testing.T) {
	m := newTestManagerWithMock(t, &mockBackend{})
	pm := PortMapping{
		ID: "web-jail-tcp-80-8080", Instance: "web", Provider: "jail",
		Protocol: ProtocolTCP, HostPort: 80, TargetPort: 8080, TargetIP: "10.0.0.2",
	}
	if err := m.persistMapping("web", pm); err != nil {
		t.Fatalf("persistMapping: %v", err)
	}

	if err := m.RemoveAllRules(context.Background(), "web"); err != nil {
		t.Fatalf("RemoveAllRules: %v", err)
	}

	ruleFile := filepath.Join(m.rulesDir, "web.json")
	if _, err := os.Stat(ruleFile); !os.IsNotExist(err) {
		t.Error("expected rule file to be deleted after RemoveAllRules")
	}
}

// TestRemoveAllRules_NoFile verifies RemoveAllRules succeeds silently
// when no rule file exists.
func TestRemoveAllRules_NoFile(t *testing.T) {
	m := newTestManagerWithMock(t, &mockBackend{})
	if err := m.RemoveAllRules(context.Background(), "no-such-instance"); err != nil {
		t.Fatalf("RemoveAllRules on missing file: %v", err)
	}
}

// TestRemoveAllRules_BackendError verifies that backend errors propagate.
func TestRemoveAllRules_BackendError(t *testing.T) {
	be := &mockBackend{removeAllErr: errors.New("backend unavailable")}
	m := newTestManagerWithMock(t, be)
	err := m.RemoveAllRules(context.Background(), "web")
	if err == nil {
		t.Fatal("expected error from backend, got nil")
	}
	if !strings.Contains(err.Error(), "backend") {
		t.Errorf("expected 'backend' in error message, got: %v", err)
	}
}

// ── restoreRules ──────────────────────────────────────────────────────────────

// TestRestoreRules_MissingDir verifies that restoreRules returns nil when the
// rules directory does not exist (first-boot scenario).
func TestRestoreRules_MissingDir(t *testing.T) {
	m := &Manager{
		rulesDir: filepath.Join(t.TempDir(), "does-not-exist"),
		backend:  &mockBackend{},
	}
	if err := m.restoreRules(context.Background()); err != nil {
		t.Fatalf("expected nil for missing dir, got: %v", err)
	}
}

// TestRestoreRules_SkipsInvalidJSON verifies that corrupt JSON files are
// silently skipped without aborting the entire restore.
func TestRestoreRules_SkipsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	// Write a corrupt JSON file.
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{garbage"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Write a valid JSON file to confirm it is processed without error.
	rs := RuleSet{Instance: "good", Provider: "jail"}
	data, _ := json.MarshalIndent(rs, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "good.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile good.json: %v", err)
	}

	m := &Manager{
		rulesDir: dir,
		backend:  &mockBackend{},
	}
	// Should complete without error despite the bad file.
	if err := m.restoreRules(context.Background()); err != nil {
		t.Fatalf("restoreRules should not fail on invalid files, got: %v", err)
	}
}

// ── NewPFBackendWithDir ───────────────────────────────────────────────────────

// TestBackendGetter verifies that Manager.Backend() returns the injected backend.
func TestBackendGetter(t *testing.T) {
	be := &mockBackend{}
	m := NewManagerWithBackendAndDir(be, t.TempDir())
	if got := m.Backend(); got != be {
		t.Errorf("Backend() returned a different backend than injected")
	}
}

// TestNewBackend_UnknownType verifies that an unknown backend type returns an error.
func TestNewBackend_UnknownType(t *testing.T) {
	_, err := NewBackend("unknown-backend")
	if err == nil {
		t.Fatal("expected error for unknown backend type, got nil")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("expected 'unknown' in error, got: %v", err)
	}
}

// TestNewBackend_PF verifies that BackendTypePF returns a *PFBackend.
func TestNewBackend_PF(t *testing.T) {
	b, err := NewBackend(BackendTypePF)
	if err != nil {
		t.Fatalf("NewBackend(PF): %v", err)
	}
	if b.Name() != BackendTypePF {
		t.Errorf("expected BackendTypePF, got %q", b.Name())
	}
}

// TestListExposedPorts_NotFound verifies that ListExposedPorts returns nil,nil
// when no rule file exists for the instance.
func TestListExposedPorts_NotFound(t *testing.T) {
	m := newTestManagerWithMock(t, &mockBackend{})
	ports, err := m.ListExposedPorts(context.Background(), "ghost-instance")
	if err != nil {
		t.Fatalf("ListExposedPorts: unexpected error: %v", err)
	}
	if ports != nil {
		t.Errorf("expected nil ports for missing instance, got %v", ports)
	}
}

// TestRemoveNAT_NotFound verifies RemoveNAT returns nil (not an error) when
// no rule file exists for the instance.
func TestRemoveNAT_NotFound(t *testing.T) {
	m := newTestManagerWithMock(t, &mockBackend{})
	if err := m.RemoveNAT(context.Background(), "ghost-instance"); err != nil {
		t.Fatalf("RemoveNAT on missing instance: %v", err)
	}
}

// TestCleanupDelegatesToBackend verifies that Manager.Cleanup delegates to the backend.
func TestCleanupDelegatesToBackend(t *testing.T) {
	be := &mockBackend{}
	m := newTestManagerWithMock(t, be)
	if err := m.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	// Inject an error and verify it propagates.
	be.cleanupErr = errors.New("backend cleanup failed")
	if err := m.Cleanup(context.Background()); err == nil {
		t.Error("expected error from backend cleanup, got nil")
	}
}

// ── NewPFBackendWithDir ───────────────────────────────────────────────────────

// TestNewPFBackendWithDir_EmptyUsesDefault verifies that an empty rulesDir
// falls back to the DefaultPFRulesDir constant.
func TestNewPFBackendWithDir_EmptyUsesDefault(t *testing.T) {
	p := NewPFBackendWithDir("")
	if p.rulesDir != DefaultPFRulesDir {
		t.Errorf("expected rulesDir=%q, got %q", DefaultPFRulesDir, p.rulesDir)
	}
}

// TestNewPFBackendWithDir_CustomDir verifies a non-empty path is used as-is.
func TestNewPFBackendWithDir_CustomDir(t *testing.T) {
	custom := t.TempDir()
	p := NewPFBackendWithDir(custom)
	if p.rulesDir != custom {
		t.Errorf("expected rulesDir=%q, got %q", custom, p.rulesDir)
	}
}
