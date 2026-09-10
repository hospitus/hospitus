package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

// makeSimpleInstance creates a test instance with ID==Name (no @provider suffix).
// Use for handlers that call s.datastore.GetInstance() directly (clone, snapshot, export).
func makeSimpleInstance(name, prov string) *datastore.Instance {
	return &datastore.Instance{
		ID:          name,
		Name:        name,
		Provider:    prov,
		State:       provider.StateRunning,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}
}

// ----------------------------------------------------------------------------
// Checkpoint handler tests (mock provider doesn't support CheckpointProvider)
// ----------------------------------------------------------------------------

func TestCheckpointCreateNotImplemented(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeSimpleInstance("cp-test", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{"name": "snap1"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/cp-test/checkpoint", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("checkpoint create: expected 501, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestCheckpointInstanceNotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	body, _ := json.Marshal(map[string]string{"name": "snap1"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/ghost/checkpoint", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestCheckpointListNotImplemented(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeSimpleInstance("cp-list", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/cp-list/checkpoints", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("checkpoint list: expected 501, got %d body=%s", w.Code, w.Body.String())
	}
}

// ----------------------------------------------------------------------------
// Pause / Resume / Rename handlers (mock doesn't support PauseProvider)
// ----------------------------------------------------------------------------

func TestPauseInstanceNotImplemented(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeSimpleInstance("pause-test", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/pause-test/pause", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("pause: expected 501, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestPauseInstanceNotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/ghost/pause", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("pause missing: expected 404, got %d", w.Code)
	}
}

func TestResumeInstanceNotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/ghost/resume", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("resume missing: expected 404, got %d", w.Code)
	}
}

func TestRenameInstanceNotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	// "name" is the JSON field name in the rename request body
	body, _ := json.Marshal(map[string]string{"name": "new-name"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/ghost/rename", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("rename missing: expected 404, got %d", w.Code)
	}
}

func TestRenameInstanceNotImplemented(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeSimpleInstance("rename-test", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	// Use plain name in URL — rename handler validates ID with ValidateInstanceName
	body, _ := json.Marshal(map[string]string{"name": "new-name"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/rename-test/rename", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("rename: expected 501, got %d body=%s", w.Code, w.Body.String())
	}
}

// ----------------------------------------------------------------------------
// Clone handler (mock doesn't support CloneProvider)
// ----------------------------------------------------------------------------

func TestCloneNotImplemented(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	// handleClone uses s.datastore.GetInstance() by ID directly; use simple ID
	inst := makeSimpleInstance("clone-src", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{"name": "clone-dst"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/clone-src/clone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("clone: expected 501, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestCloneInstanceNotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	body, _ := json.Marshal(map[string]string{"name": "dst"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/ghost/clone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("clone ghost: expected 404, got %d", w.Code)
	}
}

// ----------------------------------------------------------------------------
// Export/Import handler (mock doesn't support ExportImportProvider)
// ----------------------------------------------------------------------------

func TestExportNotImplemented(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	// handleExport uses s.datastore.GetInstance() by ID directly; use simple ID
	inst := makeSimpleInstance("export-test", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	// Inside the confinement directory: /tmp is outside it, so handleExport
	// answered 400 before ever reaching the provider, and the accepted-status
	// list was wide enough to call that a pass.
	body, _ := json.Marshal(map[string]interface{}{
		"export_path": filepath.Join(s.fileConfinementDir(), "export-test.tar"),
		"compress":    false,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/export-test/export", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// The mock provider implements no ExportImportProvider, so the handler
	// must say so rather than fail some other way.
	if w.Code != http.StatusNotImplemented {
		t.Errorf("export: got %d, want 501; body=%s", w.Code, w.Body.String())
	}
}

func TestExportInstanceNotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	// Inside the confinement directory, so the path is not what fails: the
	// instance is.
	body, _ := json.Marshal(map[string]interface{}{
		"export_path": filepath.Join(s.fileConfinementDir(), "ghost.tar"),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/ghost/export", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("export ghost: got %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

// ----------------------------------------------------------------------------
// AutoStart per-instance handlers
// Route: /api/v1/autostart/{provider}/{id}
// ----------------------------------------------------------------------------

func TestGetAutoStartInstanceNotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/autostart/unknown-provider/my-instance", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("autostart get unknown provider: expected 404/400, got %d", w.Code)
	}
}

func TestGetAutoStartInstance(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeTestInstance("as-get", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	// Route is /api/v1/autostart/{provider}/{id}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/autostart/mock/as-get", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// mock provider doesn't implement AutoStartProvider → 400 or 501
	if w.Code == http.StatusInternalServerError {
		t.Errorf("autostart get: unexpected 500 body=%s", w.Body.String())
	}
}

func TestSetAutoStartInstanceNotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	body, _ := json.Marshal(map[string]bool{"enabled": true})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/autostart/unknown-provider/ghost", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("autostart set ghost: expected 404/400, got %d", w.Code)
	}
}

func TestDisableAutoStartInstanceNotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/autostart/unknown-provider/ghost", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("autostart delete ghost: expected 404/400, got %d", w.Code)
	}
}

// ----------------------------------------------------------------------------
// Backup handlers — listing returns 200 with empty array (no manager = no data)
// ----------------------------------------------------------------------------

func TestBackupsNoManager(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/backups", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// handleListAllBackups returns 200 with empty array even without a backup manager
	if w.Code != http.StatusOK {
		t.Errorf("backups list: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestInstanceBackupsNoManager(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeTestInstance("bk-test", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/bk-test/backups", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// The exact code: httptest.NewRecorder starts at 200, so "w.Code == 0"
	// could never fire and this test asserted nothing at all.
	if w.Code != http.StatusOK {
		t.Fatalf("GET backups = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

// ----------------------------------------------------------------------------
// Snapshot handlers
// ----------------------------------------------------------------------------

func TestSnapshotNotImplemented(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	// handleSnapshots uses s.datastore.GetInstance() by ID; use simple ID
	inst := makeSimpleInstance("snap-test", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{"name": "mysnap"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/snap-test/snapshots", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("snapshot create: expected 501, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestSnapshotListNotImplemented(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeSimpleInstance("snap-list", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/snap-list/snapshots", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("snapshot list: expected 501, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestSnapshotInstanceNotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/ghost/snapshots", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("snapshots ghost: expected 404, got %d", w.Code)
	}
}

// ----------------------------------------------------------------------------
// Port forwarding (expose) endpoints
// ----------------------------------------------------------------------------

func TestExposeListInstanceNotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/ghost/expose", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expose ghost: expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

// ----------------------------------------------------------------------------
// Metrics endpoint (action "metrics" is valid in handleInstanceDetail)
// ----------------------------------------------------------------------------

func TestMetricsInstanceNotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/ghost/metrics", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("metrics ghost: expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestStatsInstanceNotImplemented(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeSimpleInstance("metrics-test", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	// /metrics is the valid metrics endpoint (not /stats which doesn't exist in routing)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/metrics-test/metrics", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// Any non-500 response is acceptable (mock may return 200/501/404)
	if w.Code == http.StatusInternalServerError {
		t.Errorf("metrics: unexpected 500 body=%s", w.Body.String())
	}
}
