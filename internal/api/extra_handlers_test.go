package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/job"
	"github.com/hospitus/hospitus/pkg/provider"
)

func setupTestServerWithJobs(t *testing.T) (*Server, func()) {
	t.Helper()
	s, ds := setupTestServer(t)
	// Replace the auto-created manager (shut it down first to avoid leaking its
	// worker goroutines) with a small single-worker one for deterministic tests.
	s.jobManager.Shutdown(time.Second)
	jm := job.NewJobManager(job.JobManagerConfig{Workers: 1, QueueSize: 10}, slog.Default())
	s.jobManager = jm
	return s, func() {
		jm.Shutdown(2 * time.Second)
		ds.Close()
	}
}

func TestExecEndpointInstanceNotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	body, _ := json.Marshal(map[string]interface{}{"command": "/bin/echo", "args": []string{"hello"}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/nonexistent/exec", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("exec on nonexistent instance: expected 404, got %d", w.Code)
	}
}

func TestExecEndpointProviderUnsupported(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	// Create an instance first
	ctx := context.Background()
	inst := makeTestInstance("exec-test", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]interface{}{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/exec-test/exec", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// The mock provider implements no ExecProvider, so the handler refuses
	// before it ever looks at the missing command: one outcome, not two.
	if w.Code != http.StatusNotImplemented {
		t.Errorf("exec on a provider without exec = %d, want 501; body=%s", w.Code, w.Body.String())
	}
}

func TestJobsListEndpoint(t *testing.T) {
	s, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /api/v1/jobs: expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var result interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Errorf("GET /api/v1/jobs: invalid JSON response: %v", err)
	}
}

func TestJobStatsEndpoint(t *testing.T) {
	s, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stats", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /api/v1/jobs/stats: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestJobNotFound(t *testing.T) {
	s, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/nonexistent-uuid", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// Job IDs must be valid UUIDs; non-UUID returns 400 Bad Request
	if w.Code != http.StatusNotFound && w.Code != http.StatusBadRequest {
		t.Errorf("GET /api/v1/jobs/nonexistent: expected 400 or 404, got %d", w.Code)
	}
}

func TestInstanceHealthEndpoint(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeTestInstance("health-test", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/health-test/health", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// Mock provider doesn't implement health checks, 501 is valid; 200 if it does.
	if w.Code != http.StatusOK && w.Code != http.StatusNotImplemented {
		t.Errorf("GET /api/v1/instances/{id}/health: expected 200 or 501, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestInstanceEventsEndpoint(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeTestInstance("events-test", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/events-test/events", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /api/v1/instances/{id}/events: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestInstanceMetricsEndpoint(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeTestInstance("metrics-test", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/metrics-test/metrics", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// Either 200 (if provider supports metrics) or 501 (not implemented)
	if w.Code != http.StatusOK && w.Code != http.StatusNotImplemented {
		t.Errorf("GET /api/v1/instances/{id}/metrics: expected 200 or 501, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestListImagesEndpoint(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /api/v1/images: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestNetworkBridgesEndpoint(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/network/bridges", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK && w.Code != http.StatusNotImplemented {
		t.Errorf("GET /api/v1/network/bridges: expected 200 or 501, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestWriteJSONHelper(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	// Verify writeJSON sets proper headers and status
	w := httptest.NewRecorder()
	s.writeJSON(w, http.StatusCreated, map[string]string{"key": "value"})

	if w.Code != http.StatusCreated {
		t.Errorf("writeJSON status: expected 201, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("writeJSON Content-Type: expected application/json, got %s", ct)
	}
	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Errorf("writeJSON: invalid JSON: %v", err)
	}
}

func TestDeleteJobEndpoint(t *testing.T) {
	s, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	// Delete a non-existent job — non-UUID returns 400; valid-UUID-not-found returns 404
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/nonexistent-job-id", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound && w.Code != http.StatusBadRequest {
		t.Errorf("DELETE /api/v1/jobs/nonexistent: expected 400 or 404, got %d", w.Code)
	}
}

// makeTestInstance creates a datastore.Instance for test setup.
func makeTestInstance(name, prov string) *datastore.Instance {
	return &datastore.Instance{
		ID:          fmt.Sprintf("%s@%s", name, prov),
		Name:        name,
		Provider:    prov,
		State:       provider.StateRunning,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}
}
