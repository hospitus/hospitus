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
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	// 200 alone would pass for a handler that updated nothing.
	stored, err := ds.GetInstance(ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if stored.Spec.Description != "new description" {
		t.Errorf("description = %q, want %q", stored.Spec.Description, "new description")
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
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	stored, err := ds.GetInstance(ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if got := stored.Handle.Metadata["custom_key"]; got != "custom_value" {
		t.Errorf("provider_config custom_key = %v, want custom_value", got)
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

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for unsupported method, got %d", w.Code)
	}
	if allow := w.Header().Get("Allow"); allow != "GET, DELETE" {
		t.Errorf("Allow = %q, want \"GET, DELETE\"", allow)
	}
}

func TestJobCancelNotFound(t *testing.T) {
	s, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/nonexistent-job/cancel", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// Unknown to both the manager and the datastore: 404, the code
	// handleCancelJob documents for exactly this case. "anything but 200"
	// passed while a 500 went unnoticed.
	if w.Code != http.StatusNotFound {
		t.Errorf("cancel non-existent job = %d, want 404; body=%s", w.Code, w.Body.String())
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
	// A second provider to filter out: with only "mock" instances present, a
	// handler that ignores the filter entirely returns the same list.
	other := makeTestInstance("filter-other", "someother")
	if err := ds.CreateInstance(ctx, other); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances?provider=mock", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/instances?provider=mock: expected 200, got %d", w.Code)
	}

	listed := decodeInstanceList(t, w)
	if len(listed) != 3 {
		t.Fatalf("listed %d instances, want the 3 mock ones", len(listed))
	}
	for _, inst := range listed {
		if inst.Provider != "mock" {
			t.Errorf("instance %s has provider %q, which the filter should have excluded", inst.ID, inst.Provider)
		}
	}
}

// decodeInstanceList reads the instance array out of a list response, which
// the handler wraps in an object.
func decodeInstanceList(t *testing.T, w *httptest.ResponseRecorder) []datastore.Instance {
	t.Helper()

	var wrapped struct {
		Instances []datastore.Instance `json:"instances"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &wrapped); err == nil && wrapped.Instances != nil {
		return wrapped.Instances
	}

	var bare []datastore.Instance
	if err := json.Unmarshal(w.Body.Bytes(), &bare); err != nil {
		t.Fatalf("decoding the instance list: %v (%s)", err, w.Body.String())
	}
	return bare
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

	running := makeTestInstance("state-filter-running", "mock")
	running.State = provider.StateRunning
	if err := ds.CreateInstance(ctx, running); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances?state=stopped", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/instances?state=stopped: expected 200, got %d", w.Code)
	}

	// The name, not the id (makeTestInstance builds "<name>@<provider>"), and
	// not the state either: the handler refreshes it from the provider, whose
	// mock always answers running. What this pins is which row the filter let
	// through.
	listed := decodeInstanceList(t, w)
	if len(listed) != 1 {
		t.Fatalf("state=stopped listed %d instances, want 1", len(listed))
	}
	if listed[0].Name != "state-filter-vm" {
		t.Errorf("state=stopped listed %q, want state-filter-vm", listed[0].Name)
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
