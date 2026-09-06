package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// handleSnapshots tests — routed via /api/v1/instances/{id}/snapshots[/...]
// ---------------------------------------------------------------------------

// TestHandleListSnapshots_OK: GET /api/v1/instances/{id}/snapshots → 200
func TestHandleListSnapshots_OK(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)
	defer ds.Close()

	createTestInstanceInDS(t, ds, "inst1", "mock")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/inst1/snapshots", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["instance"] != "inst1" {
		t.Errorf("want instance=inst1, got %v", resp["instance"])
	}
}

// TestHandleListSnapshots_NotFound: instance not in datastore → 404
func TestHandleListSnapshots_NotFound(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/noexist/snapshots", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}

// TestHandleListSnapshots_NoProvider: provider without snapshot support → 501
func TestHandleListSnapshots_NoProvider(t *testing.T) {
	mp := &mockProvider{}
	srv, ds := setupTestServerWithProvider(t, mp)
	defer ds.Close()

	createTestInstanceInDS(t, ds, "inst1", "mock")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/inst1/snapshots", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("want 501, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleListSnapshots_WrongMethod: PUT → 405
func TestHandleListSnapshots_WrongMethod(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)
	defer ds.Close()

	createTestInstanceInDS(t, ds, "inst1", "mock")

	req := httptest.NewRequest(http.MethodPut, "/api/v1/instances/inst1/snapshots", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", w.Code)
	}
}

// TestHandleCreateSnapshot_OK: POST /api/v1/instances/{id}/snapshots → 201
func TestHandleCreateSnapshot_OK(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)
	defer ds.Close()

	createTestInstanceInDS(t, ds, "inst1", "mock")

	body := bytes.NewBufferString(`{"name":"snap1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/inst1/snapshots", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("want 201, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleCreateSnapshot_InvalidBody: malformed JSON → 400
func TestHandleCreateSnapshot_InvalidBody(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)
	defer ds.Close()

	createTestInstanceInDS(t, ds, "inst1", "mock")

	body := bytes.NewBufferString(`not-json`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/inst1/snapshots", body)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

// TestHandleCreateSnapshot_InvalidName: bad snapshot name → 400
func TestHandleCreateSnapshot_InvalidName(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)
	defer ds.Close()

	createTestInstanceInDS(t, ds, "inst1", "mock")

	body := bytes.NewBufferString(`{"name":"../../../etc/passwd"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/inst1/snapshots", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

// TestHandleCreateSnapshot_InstanceNotFound: instance missing → 404
func TestHandleCreateSnapshot_InstanceNotFound(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)
	defer ds.Close()

	body := bytes.NewBufferString(`{"name":"snap1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/noexist/snapshots", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}

// TestHandleDeleteSnapshot_OK: DELETE /api/v1/instances/{id}/snapshots/{name} → 200
func TestHandleDeleteSnapshot_OK(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)
	defer ds.Close()

	createTestInstanceInDS(t, ds, "inst1", "mock")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/instances/inst1/snapshots/snap1", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleDeleteSnapshot_NotFound: snapshot not in list → 404
func TestHandleDeleteSnapshot_NotFound(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)
	defer ds.Close()

	createTestInstanceInDS(t, ds, "inst1", "mock")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/instances/inst1/snapshots/nonexist", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleDeleteSnapshot_WrongMethod: GET on /snapshots/{name} → 405
func TestHandleDeleteSnapshot_WrongMethod(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)
	defer ds.Close()

	createTestInstanceInDS(t, ds, "inst1", "mock")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/inst1/snapshots/snap1", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", w.Code)
	}
}

// TestHandleRestoreSnapshot_OK: POST /api/v1/instances/{id}/snapshots/{name}/restore → 200
func TestHandleRestoreSnapshot_OK(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)
	defer ds.Close()

	createTestInstanceInDS(t, ds, "inst1", "mock")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/inst1/snapshots/snap1/restore", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleRestoreSnapshot_NotFound: snapshot not in list → 404
func TestHandleRestoreSnapshot_NotFound(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)
	defer ds.Close()

	createTestInstanceInDS(t, ds, "inst1", "mock")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/inst1/snapshots/nonexist/restore", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleRestoreSnapshot_WrongMethod: GET on restore endpoint → 405
func TestHandleRestoreSnapshot_WrongMethod(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)
	defer ds.Close()

	createTestInstanceInDS(t, ds, "inst1", "mock")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/inst1/snapshots/snap1/restore", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", w.Code)
	}
}

// TestHandleSnapshots_InvalidInstanceID: bad instance name (spaces) → 400
func TestHandleSnapshots_InvalidInstanceID(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)
	defer ds.Close()

	// Use an instance ID with uppercase and special chars that pass URL routing
	// but fail validation; name "INVALID!NAME" has invalid chars per ValidateInstanceName
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/INVALID!NAME/snapshots", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	// The instance name fails ValidateInstanceName (invalid characters), which is
	// rejected up front with a deterministic 400.
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

// TestHandleSnapshots_UnknownPath: unknown sub-path → 404
func TestHandleSnapshots_UnknownPath(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)
	defer ds.Close()

	createTestInstanceInDS(t, ds, "inst1", "mock")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/inst1/snapshots/snap1/unknown/extra", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}
