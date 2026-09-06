package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hospitus/hospitus/pkg/job"
	"github.com/hospitus/hospitus/pkg/provider"
)

// mockCloneProvider implements CloneProvider AND SnapshotProvider so we can
// test both handleCloneFromInstance and handleCloneFromSnapshot.
type mockCloneProvider struct {
	*mockProvider

	cloneInstanceFn     func(ctx context.Context, source provider.InstanceHandle, cloneName string, opts provider.CloneOptions) (provider.InstanceHandle, error)
	cloneFromSnapshotFn func(ctx context.Context, snap provider.SnapshotHandle, cloneName string, opts provider.CloneOptions) (provider.InstanceHandle, error)
	listSnapshotsFn     func(ctx context.Context, handle provider.InstanceHandle) ([]provider.SnapshotInfo, error)
}

func (m *mockCloneProvider) CloneInstance(ctx context.Context, source provider.InstanceHandle, cloneName string, opts provider.CloneOptions) (provider.InstanceHandle, error) {
	if m.cloneInstanceFn != nil {
		return m.cloneInstanceFn(ctx, source, cloneName, opts)
	}
	return provider.InstanceHandle{ID: cloneName}, nil
}

func (m *mockCloneProvider) CloneFromSnapshot(ctx context.Context, snap provider.SnapshotHandle, cloneName string, opts provider.CloneOptions) (provider.InstanceHandle, error) {
	if m.cloneFromSnapshotFn != nil {
		return m.cloneFromSnapshotFn(ctx, snap, cloneName, opts)
	}
	return provider.InstanceHandle{ID: cloneName}, nil
}

func (m *mockCloneProvider) CreateSnapshot(ctx context.Context, handle provider.InstanceHandle, name string) (provider.SnapshotHandle, error) {
	return provider.SnapshotHandle{ID: name, Instance: handle.ID}, nil
}

func (m *mockCloneProvider) DeleteSnapshot(ctx context.Context, snap provider.SnapshotHandle) error {
	return nil
}

func (m *mockCloneProvider) RestoreSnapshot(ctx context.Context, handle provider.InstanceHandle, snap provider.SnapshotHandle) error {
	return nil
}

func (m *mockCloneProvider) ListSnapshots(ctx context.Context, handle provider.InstanceHandle) ([]provider.SnapshotInfo, error) {
	if m.listSnapshotsFn != nil {
		return m.listSnapshotsFn(ctx, handle)
	}
	return []provider.SnapshotInfo{
		{
			Handle: provider.SnapshotHandle{ID: "snap1", Instance: handle.ID},
			Name:   "snap1",
		},
	}, nil
}

// mockUpgradeProvider implements UpgradeProvider.
type mockUpgradeProvider struct {
	*mockProvider
	upgradeInstanceFn func(ctx context.Context, handle provider.InstanceHandle, targetRelease string) error
}

func (m *mockUpgradeProvider) UpgradeInstance(ctx context.Context, handle provider.InstanceHandle, targetRelease string) error {
	if m.upgradeInstanceFn != nil {
		return m.upgradeInstanceFn(ctx, handle, targetRelease)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Clone-from-instance tests
// ---------------------------------------------------------------------------

func TestHandleCloneFromInstance_OK(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)

	createTestInstanceInDS(t, ds, "src1", "mock")

	body, _ := json.Marshal(map[string]interface{}{"name": "clone1"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/src1/clone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleCloneFromInstance_InvalidBody(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)

	createTestInstanceInDS(t, ds, "src2", "mock")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/src2/clone", bytes.NewReader([]byte("{bad json}")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleCloneFromInstance_InvalidName(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)

	createTestInstanceInDS(t, ds, "src3", "mock")

	body, _ := json.Marshal(map[string]interface{}{"name": "inv@lid!"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/src3/clone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleCloneFromInstance_SourceNotFound(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, _ := setupTestServerWithProvider(t, cp)

	body, _ := json.Marshal(map[string]interface{}{"name": "clone-x"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/nonexistent/clone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleCloneFromInstance_NoProvider(t *testing.T) {
	// Use plain mockProvider (no CloneProvider)
	srv, ds := setupTestServerWithProvider(t, &mockProvider{})

	createTestInstanceInDS(t, ds, "src4", "mock")

	body, _ := json.Marshal(map[string]interface{}{"name": "clone4"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/src4/clone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", w.Code)
	}
}

func TestHandleCloneFromInstance_WrongMethod(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)

	createTestInstanceInDS(t, ds, "src5", "mock")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/src5/clone", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleCloneFromInstance_Conflict(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)

	createTestInstanceInDS(t, ds, "src6", "mock")
	createTestInstanceInDS(t, ds, "already-exists", "mock")

	body, _ := json.Marshal(map[string]interface{}{"name": "already-exists"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/src6/clone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Clone-from-snapshot tests
// ---------------------------------------------------------------------------

func TestHandleCloneFromSnapshot_OK(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)

	createTestInstanceInDS(t, ds, "snapsrc1", "mock")

	body, _ := json.Marshal(map[string]interface{}{"name": "snapclone1"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/snapsrc1/snapshots/snap1/clone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleCloneFromSnapshot_SnapshotNotFound(t *testing.T) {
	cp := &mockCloneProvider{
		listSnapshotsFn: func(_ context.Context, _ provider.InstanceHandle) ([]provider.SnapshotInfo, error) {
			return []provider.SnapshotInfo{}, nil // empty list
		},
	}
	srv, ds := setupTestServerWithProvider(t, cp)

	createTestInstanceInDS(t, ds, "snapsrc2", "mock")

	body, _ := json.Marshal(map[string]interface{}{"name": "snapclone2"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/snapsrc2/snapshots/missing-snap/clone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleCloneFromSnapshot_InvalidSnapshotName(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)

	createTestInstanceInDS(t, ds, "snapsrc3", "mock")

	body, _ := json.Marshal(map[string]interface{}{"name": "snapclone3"})
	// "a..b", not a literal ".." segment: http.ServeMux cleans the path and
	// answers 307 before the handler runs, so the traversal name never reached
	// ValidateSnapshotName and the test proved nothing. This keeps it inside
	// one segment, where the handler sees it.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/snapsrc3/snapshots/a..b/clone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for a snapshot name containing \"..\", got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleCloneFromSnapshot_InvalidCloneName(t *testing.T) {
	cp := &mockCloneProvider{}
	srv, ds := setupTestServerWithProvider(t, cp)

	createTestInstanceInDS(t, ds, "snapsrc4", "mock")

	body, _ := json.Marshal(map[string]interface{}{"name": "inv@lid!clone"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/snapsrc4/snapshots/snap1/clone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Upgrade handler tests
// ---------------------------------------------------------------------------

func TestHandleUpgradeInstance_NoProvider(t *testing.T) {
	// Plain mockProvider has no UpgradeProvider
	srv, ds := setupTestServerWithProvider(t, &mockProvider{})

	createTestInstanceInDS(t, ds, "upgr1", "mock")

	body, _ := json.Marshal(map[string]interface{}{"target_release": "14.3-RELEASE"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/upgr1/upgrade", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleUpgradeInstance_MissingRelease(t *testing.T) {
	up := &mockUpgradeProvider{}
	srv, ds := setupTestServerWithProvider(t, up)

	createTestInstanceInDS(t, ds, "upgr2", "mock")

	body, _ := json.Marshal(map[string]interface{}{}) // no target_release
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/upgr2/upgrade", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleUpgradeInstance_InvalidBody(t *testing.T) {
	up := &mockUpgradeProvider{}
	srv, ds := setupTestServerWithProvider(t, up)

	createTestInstanceInDS(t, ds, "upgr3", "mock")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/upgr3/upgrade", bytes.NewReader([]byte("{bad}")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleUpgradeInstance_SourceNotFound(t *testing.T) {
	up := &mockUpgradeProvider{}
	srv, _ := setupTestServerWithProvider(t, up)

	body, _ := json.Marshal(map[string]interface{}{"target_release": "14.3-RELEASE"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/no-such-instance/upgrade", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleUpgradeInstance_OK(t *testing.T) {
	up := &mockUpgradeProvider{}
	srv, ds := setupTestServerWithProvider(t, up)

	// Initialize a real job manager so submitJob doesn't panic
	jm := job.NewJobManager(job.JobManagerConfig{Workers: 1, QueueSize: 10}, slog.Default())
	srv.jobManager = jm
	defer jm.Shutdown(100 * time.Millisecond)

	createTestInstanceInDS(t, ds, "upgr4", "mock")

	body, _ := json.Marshal(map[string]interface{}{"target_release": "14.3-RELEASE"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/upgr4/upgrade", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	// Upgrade uses submitJob which returns 202 Accepted
	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
}
