package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

// ----------------------------------------------------------------------------
// handleUpdateInstance
// ----------------------------------------------------------------------------

func TestUpdateInstance(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeTestInstance("update-me", "mock")
	inst.Spec.CPUs = 2
	inst.Spec.MemoryMB = 1024
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	update := map[string]interface{}{
		"spec": map[string]interface{}{
			"cpus":      4,
			"memory_mb": 2048,
		},
	}
	body, _ := json.Marshal(update)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instances/"+inst.ID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PATCH /api/v1/instances/update-me: expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var result datastore.Instance
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Spec.CPUs != 4 {
		t.Errorf("CPUs = %d, want 4", result.Spec.CPUs)
	}
	if result.Spec.MemoryMB != 2048 {
		t.Errorf("MemoryMB = %d, want 2048", result.Spec.MemoryMB)
	}
}

func TestUpdateInstanceNotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	update := map[string]interface{}{"spec": map[string]interface{}{"cpus": 4}}
	body, _ := json.Marshal(update)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instances/nonexistent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestUpdateInstanceBadJSON(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeTestInstance("update-badjson", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instances/"+inst.ID, bytes.NewReader([]byte(`{bad json`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestUpdateInstanceDescription(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeTestInstance("update-desc", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	update := map[string]interface{}{
		"spec": map[string]interface{}{
			"description": "new description",
		},
	}
	body, _ := json.Marshal(update)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instances/"+inst.ID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestUpdateInstanceProviderConfig(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeTestInstance("update-cfg", "mock")
	inst.Handle.Metadata = map[string]interface{}{}
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	update := map[string]interface{}{
		"provider_config": map[string]interface{}{
			"custom_key": "custom_value",
		},
	}
	body, _ := json.Marshal(update)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instances/"+inst.ID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

// ----------------------------------------------------------------------------
// handleJobDetail: cancel, method-not-allowed
// ----------------------------------------------------------------------------

func TestJobDetailMethodNotAllowed(t *testing.T) {
	s, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	// PATCH is not supported on /api/v1/jobs/{id}
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/jobs/some-job-id", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusNotFound && w.Code != http.StatusBadRequest {
		t.Errorf("expected 405/404/400 for unsupported method, got %d", w.Code)
	}
}

func TestJobCancelNotFound(t *testing.T) {
	s, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/nonexistent-job/cancel", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// Canceling a non-existent job should return 404 or 409
	if w.Code == http.StatusOK {
		t.Errorf("cancel non-existent job should not return 200")
	}
}

// ----------------------------------------------------------------------------
// handleListInstances with filter
// ----------------------------------------------------------------------------

func TestListInstancesWithProviderFilter(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		inst := makeTestInstance(fmt.Sprintf("filter-vm-%d", i), "mock")
		if err := ds.CreateInstance(ctx, inst); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances?provider=mock", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/instances?provider=mock: expected 200, got %d", w.Code)
	}
}

func TestListInstancesWithStateFilter(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeTestInstance("state-filter-vm", "mock")
	inst.State = provider.StateStopped
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances?state=stopped", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/instances?state=stopped: expected 200, got %d", w.Code)
	}
}

// ----------------------------------------------------------------------------
// handleProviders: method not allowed
// ----------------------------------------------------------------------------

func TestListProvidersMethodNotAllowed(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/v1/providers: expected 405, got %d", w.Code)
	}
}

func TestGetProviderDetailNotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers/nonexistent-provider", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GET /api/v1/providers/nonexistent: expected 404, got %d", w.Code)
	}
}

// ----------------------------------------------------------------------------
// GetStackManager / SetStackManager / GetBackupManager / SetBackupManager
// ----------------------------------------------------------------------------

func TestStackAndBackupManagerAccessors(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	// Should be nil by default — just verify no panic
	_ = s.GetStackManager()
	_ = s.GetBackupManager()
}

// ----------------------------------------------------------------------------
// handleInstanceDetail: unsupported method
// ----------------------------------------------------------------------------

func TestInstanceDetailMethodNotAllowed(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeTestInstance("method-test", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	// PUT is not a supported method (only GET, PATCH, DELETE at the base path)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/instances/"+inst.ID, nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// The exact code, not "anything but 200": the handler answered 404 for a
	// method it does not serve, and an assertion this loose called that a
	// pass.
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT /api/v1/instances/{id} = %d, want 405", w.Code)
	}
	if allow := w.Header().Get("Allow"); allow == "" {
		t.Error("a 405 carries no Allow header")
	}
}

// ----------------------------------------------------------------------------
// handleInstances: POST method dispatches to create
// ----------------------------------------------------------------------------

func TestInstancesPostCreatesInstance(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"name":   "post-test-vm",
		"image":  "test-image",
		"labels": map[string]string{"provider": "mock"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated && w.Code != http.StatusAccepted {
		t.Errorf("POST /api/v1/instances: expected 201/202, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestInstancesUnsupportedMethod(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/instances", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /api/v1/instances: expected 405, got %d", w.Code)
	}
}
