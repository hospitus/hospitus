package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/backup"
	"github.com/hospitus/hospitus/pkg/provider"
)

// mockCheckpointProvider combines mockProvider with CheckpointProvider interface.
type mockCheckpointProvider struct {
	mockProvider
	checkpoints []provider.CheckpointInfo
	checkErr    error
}

func (m *mockCheckpointProvider) CheckpointInstance(_ context.Context, _ provider.InstanceHandle, _ string) error {
	return m.checkErr
}

func (m *mockCheckpointProvider) RestoreCheckpoint(_ context.Context, _ provider.InstanceHandle, _ string) error {
	return m.checkErr
}

func (m *mockCheckpointProvider) ListCheckpoints(_ context.Context, _ provider.InstanceHandle) ([]provider.CheckpointInfo, error) {
	if m.checkErr != nil {
		return nil, m.checkErr
	}
	return m.checkpoints, nil
}

func (m *mockCheckpointProvider) DeleteCheckpoint(_ context.Context, _ provider.InstanceHandle, _ string) error {
	return m.checkErr
}

// mockPauseProvider combines mockProvider with PauseProvider interface.
type mockPauseProvider struct {
	mockProvider
	pauseErr error
}

func (m *mockPauseProvider) PauseInstance(_ context.Context, _ provider.InstanceHandle) error {
	return m.pauseErr
}

func (m *mockPauseProvider) ResumeInstance(_ context.Context, _ provider.InstanceHandle) error {
	return m.pauseErr
}

// mockRenameProvider combines mockProvider with RenameProvider interface.
type mockRenameProvider struct {
	mockProvider
	renameErr error
}

func (m *mockRenameProvider) RenameInstance(_ context.Context, _ provider.InstanceHandle, _ string) error {
	return m.renameErr
}

// setupTestServerWithProvider creates a server using a custom provider.
func setupTestServerWithProvider(t *testing.T, prov provider.Provider) (*Server, *datastore.Datastore) {
	t.Helper()
	ds, err := datastore.NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	t.Cleanup(func() { _ = ds.Close() })
	registry := provider.NewRegistry()
	if err := registry.Register(prov); err != nil {
		t.Fatalf("Failed to register provider: %v", err)
	}
	server, err := NewServer(":8080", ds, registry, nil)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	// Confine export/import paths to the temp dir so tests may use /tmp targets.
	server.config.DataDir = os.TempDir()
	return server, ds
}

// createTestInstanceInDS is a helper to insert a test instance into the datastore.
func createTestInstanceInDS(t *testing.T, ds *datastore.Datastore, id, providerName string) {
	t.Helper()
	instance := &datastore.Instance{
		ID:       id,
		Name:     id,
		Provider: providerName,
		State:    provider.StateStopped,
		Spec:     provider.InstanceSpec{Name: id},
		Handle: provider.InstanceHandle{
			ID:       id,
			Provider: providerName,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}
	if err := ds.CreateInstance(context.Background(), instance); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
}

// ── Checkpoint handler tests ──────────────────────────────────────────────────

func TestHandleCheckpointList_NoProvider(t *testing.T) {
	server, ds := setupTestServer(t)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/vm1/checkpoints", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	// mockProvider does not implement CheckpointProvider → 501
	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", w.Code)
	}
}

func TestHandleCheckpointList_OK(t *testing.T) {
	now := time.Now().UTC()
	cp := &mockCheckpointProvider{
		checkpoints: []provider.CheckpointInfo{
			{Name: "snap1", CreatedAt: now, SizeMB: 128},
		},
	}
	cp.instances = make(map[string]provider.InstanceHandle)
	server, ds := setupTestServerWithProvider(t, cp)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/vm1/checkpoints", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleCheckpoint_Create_NoProvider(t *testing.T) {
	server, ds := setupTestServer(t)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	body, _ := json.Marshal(map[string]string{"name": "snap1"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/vm1/checkpoint", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", w.Code)
	}
}

func TestHandleCheckpoint_Create_OK(t *testing.T) {
	cp := &mockCheckpointProvider{}
	cp.instances = make(map[string]provider.InstanceHandle)
	server, ds := setupTestServerWithProvider(t, cp)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	body, _ := json.Marshal(map[string]string{"name": "snap1"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/vm1/checkpoint", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleCheckpoint_Create_InvalidName(t *testing.T) {
	cp := &mockCheckpointProvider{}
	cp.instances = make(map[string]provider.InstanceHandle)
	server, ds := setupTestServerWithProvider(t, cp)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	body, _ := json.Marshal(map[string]string{"name": "invalid name with spaces!"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/vm1/checkpoint", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleCheckpoint_Restore_OK(t *testing.T) {
	cp := &mockCheckpointProvider{}
	cp.instances = make(map[string]provider.InstanceHandle)
	server, ds := setupTestServerWithProvider(t, cp)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/vm1/checkpoint/snap1/restore", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleCheckpoint_Delete_OK(t *testing.T) {
	cp := &mockCheckpointProvider{}
	cp.instances = make(map[string]provider.InstanceHandle)
	server, ds := setupTestServerWithProvider(t, cp)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/instances/vm1/checkpoint/snap1", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleCheckpoint_NotFound(t *testing.T) {
	server, _ := setupTestServer(t)

	body, _ := json.Marshal(map[string]string{"name": "snap1"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/missing/checkpoint", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// ── Pause / Resume tests ──────────────────────────────────────────────────────

func TestHandlePauseInstance_NoProvider(t *testing.T) {
	server, ds := setupTestServer(t)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/vm1/pause", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	// mockProvider does not implement PauseProvider → 501
	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", w.Code)
	}
}

func TestHandlePauseInstance_OK(t *testing.T) {
	pp := &mockPauseProvider{}
	pp.instances = make(map[string]provider.InstanceHandle)
	server, ds := setupTestServerWithProvider(t, pp)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/vm1/pause", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleResumeInstance_OK(t *testing.T) {
	pp := &mockPauseProvider{}
	pp.instances = make(map[string]provider.InstanceHandle)
	server, ds := setupTestServerWithProvider(t, pp)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/vm1/resume", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandlePauseInstance_NotFound(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/missing/pause", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// ── Rename tests ──────────────────────────────────────────────────────────────

func TestHandleRenameInstance_NoProvider(t *testing.T) {
	server, ds := setupTestServer(t)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	// handler validates name first, then checks provider capability
	body, _ := json.Marshal(map[string]string{"name": "vm2"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/vm1/rename", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	// mockProvider does not implement RenameProvider → 501
	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", w.Code)
	}
}

func TestHandleRenameInstance_OK(t *testing.T) {
	rp := &mockRenameProvider{}
	rp.instances = make(map[string]provider.InstanceHandle)
	server, ds := setupTestServerWithProvider(t, rp)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	body, _ := json.Marshal(map[string]string{"name": "vm2"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/vm1/rename", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleRenameInstance_InvalidBody(t *testing.T) {
	rp := &mockRenameProvider{}
	rp.instances = make(map[string]provider.InstanceHandle)
	server, ds := setupTestServerWithProvider(t, rp)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/vm1/rename", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleRenameInstance_NotFound(t *testing.T) {
	server, _ := setupTestServer(t)

	// name must be valid for the handler to reach the lookup step
	body, _ := json.Marshal(map[string]string{"name": "vm2"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/missing/rename", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// ── AutoStart handler tests ───────────────────────────────────────────────────

type mockAutoStartProvider struct {
	mockProvider
	config  provider.AutoStartConfig
	handles []provider.InstanceHandle
	asErr   error
}

func (m *mockAutoStartProvider) SetAutoStart(_ context.Context, _ provider.InstanceHandle, cfg provider.AutoStartConfig) error {
	if m.asErr != nil {
		return m.asErr
	}
	m.config = cfg
	return nil
}

func (m *mockAutoStartProvider) GetAutoStart(_ context.Context, _ provider.InstanceHandle) (*provider.AutoStartConfig, error) {
	if m.asErr != nil {
		return nil, m.asErr
	}
	cfg := m.config
	return &cfg, nil
}

func (m *mockAutoStartProvider) ListAutoStartInstances(_ context.Context) ([]provider.InstanceHandle, error) {
	if m.asErr != nil {
		return nil, m.asErr
	}
	return m.handles, nil
}

func (m *mockAutoStartProvider) StartAutoStartInstances(_ context.Context) error {
	return m.asErr
}

func TestHandleAutoStart_List_NoAutoStartProvider(t *testing.T) {
	server, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/autostart", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandleAutoStart_List_WithProvider(t *testing.T) {
	ap := &mockAutoStartProvider{
		handles: []provider.InstanceHandle{{ID: "vm1", Provider: "mock"}},
		config:  provider.AutoStartConfig{Enabled: true, Priority: 50},
	}
	ap.instances = make(map[string]provider.InstanceHandle)
	server, _ := setupTestServerWithProvider(t, ap)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/autostart", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleAutoStartInstance_GetConfig(t *testing.T) {
	ap := &mockAutoStartProvider{
		config: provider.AutoStartConfig{Enabled: true, Priority: 30},
	}
	ap.instances = make(map[string]provider.InstanceHandle)
	server, _ := setupTestServerWithProvider(t, ap)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/autostart/mock/vm1", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleAutoStartInstance_SetConfig(t *testing.T) {
	ap := &mockAutoStartProvider{}
	ap.instances = make(map[string]provider.InstanceHandle)
	server, _ := setupTestServerWithProvider(t, ap)

	body, _ := json.Marshal(provider.AutoStartConfig{Enabled: true, Priority: 10})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/autostart/mock/vm1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleAutoStartInstance_Disable(t *testing.T) {
	ap := &mockAutoStartProvider{}
	ap.instances = make(map[string]provider.InstanceHandle)
	server, _ := setupTestServerWithProvider(t, ap)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/autostart/mock/vm1", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleAutoStartInstance_NoAutoStart(t *testing.T) {
	server, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/autostart/mock/vm1", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)
	// mock provider doesn't implement AutoStartProvider → 501 Not Implemented,
	// consistent with the rest of the "provider does not support <feature>" API.
	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", w.Code)
	}
}

func TestHandleAutoStartInstance_BadPath(t *testing.T) {
	server, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/autostart/mock", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// ── Backup config handler tests ───────────────────────────────────────────────

func TestHandleGetBackupConfig_NotFound(t *testing.T) {
	server, ds := setupTestServer(t)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/vm1/backups/config", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	// No config set → 404
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleSetBackupConfig_OK(t *testing.T) {
	server, ds := setupTestServer(t)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	body, _ := json.Marshal(map[string]interface{}{
		"enabled":     true,
		"schedule":    "daily",
		"destination": "/backups/vm1",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/vm1/backups/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleDeleteBackupConfig_OK(t *testing.T) {
	server, ds := setupTestServer(t)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/instances/vm1/backups/config", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
}

func TestHandleListInstanceBackups_OK(t *testing.T) {
	server, ds := setupTestServer(t)
	createTestInstanceInDS(t, ds, "vm1", "mock")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/vm1/backups", nil)
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ---- backup handler pure-Go tests ----

func TestBackupToResponse(t *testing.T) {
	now := time.Now()
	b := &backup.BackupInfo{
		ID:           "bk-001",
		InstanceID:   "myjail",
		Type:         backup.BackupTypeSnapshot,
		Status:       backup.BackupStatusCompleted,
		CreatedAt:    now,
		CompletedAt:  now,
		Size:         4096,
		SnapshotName: "zroot/hospitus/jails/myjail@bk-001",
		Destination:  "/backups/bk-001",
		Compressed:   true,
		Encrypted:    false,
		Error:        "",
		Verified:     true,
	}
	r := backupToResponse(b)
	if r.ID != "bk-001" {
		t.Errorf("ID = %q, want bk-001", r.ID)
	}
	if r.Type != "snapshot" {
		t.Errorf("Type = %q, want snapshot", r.Type)
	}
	if r.Status != "completed" {
		t.Errorf("Status = %q, want completed", r.Status)
	}
	if r.Size != 4096 {
		t.Errorf("Size = %d, want 4096", r.Size)
	}
	if !r.Compressed {
		t.Error("Compressed should be true")
	}
	if !r.Verified {
		t.Error("Verified should be true")
	}
}

func TestHandleCreateInstanceBackup_InvalidBackupType(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	body := `{"type":"invalid_type"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/myjail/backups",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	// invalid backup type → 400 bad request
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleVerifyBackup_WrongMethod(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	// GET to a verify endpoint that requires POST → 405 method not allowed
	req := httptest.NewRequest(http.MethodGet, "/api/v1/backups/bk-001/verify", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	// handleVerifyBackup checks r.Method != POST → 405
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleRestoreBackup_InvalidBody(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	// POST with invalid JSON body → 400
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backups/bk-001/restore",
		bytes.NewBufferString("not-json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
