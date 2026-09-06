package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hospitus/hospitus/pkg/job"
)

// ---------------------------------------------------------------------------
// Helpers — shared by all job handler tests in this file
// ---------------------------------------------------------------------------

// setupJobServer returns a server with a running job manager and a real
// submitted job whose ID can be used in test requests.
// Caller must call cleanup() when done.
func setupJobServer(t *testing.T) (srv *Server, jobID string, cleanup func()) {
	t.Helper()
	srv, jmCleanup := setupTestServerWithJobs(t)

	// Submit a real job so we have a valid job ID to work with.
	j, err := srv.submitJob(context.Background(), "test", "test job", nil,
		func(ctx context.Context, j *job.Job) error {
			j.UpdateProgress(1.0, "done")
			return nil
		})
	if err != nil {
		t.Fatalf("submit test job: %v", err)
	}
	// On the job's own signal, not a fixed 50 ms: a loaded machine finishes
	// later than that, and the test then asserted against a job still
	// running.
	select {
	case <-j.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the test job never finished")
	}

	return srv, j.ID, jmCleanup
}

// ---------------------------------------------------------------------------
// handleGetJob tests
// ---------------------------------------------------------------------------

func TestHandleGetJob_OK(t *testing.T) {
	srv, jobID, cleanup := setupJobServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+jobID, nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleGetJob_NotFound(t *testing.T) {
	srv, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/00000000-0000-0000-0000-000000000000", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound && w.Code != http.StatusBadRequest {
		t.Errorf("want 404 or 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleGetJob_EmptyID(t *testing.T) {
	// The /api/v1/jobs/{id} route requires a non-empty id segment.
	// A GET to /api/v1/jobs/ (trailing slash, no ID) should return 400/404.
	srv, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	// Acceptable: 400 (bad request), 404 (no route match), or 405
	if w.Code == http.StatusOK {
		t.Errorf("empty job ID should not return 200")
	}
}

// ---------------------------------------------------------------------------
// handleDeleteJob tests
// ---------------------------------------------------------------------------

func TestHandleDeleteJob_OK(t *testing.T) {
	srv, jobID, cleanup := setupJobServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+jobID, nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent && w.Code != http.StatusOK {
		t.Errorf("want 204 or 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleDeleteJob_NotFound(t *testing.T) {
	srv, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/00000000-0000-0000-0000-000000000000", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound && w.Code != http.StatusBadRequest {
		t.Errorf("want 404 or 400, got %d: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// handleCancelJob tests
// ---------------------------------------------------------------------------

// TestHandleCancelJob_NotFound covers a cancel for a job that exists in
// neither the manager nor the datastore: that is a 404, not the 409 reserved
// for a job that exists but cannot be canceled.
func TestHandleCancelJob_NotFound(t *testing.T) {
	srv, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/00000000-0000-0000-0000-000000000000/cancel", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("cancel of an unknown job: want 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleCancelJob_FinishedIsConflict keeps 409 for the case it belongs to:
// the job is known, but its status does not allow cancellation.
func TestHandleCancelJob_FinishedIsConflict(t *testing.T) {
	srv, jobID, cleanup := setupJobServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+jobID+"/cancel", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("cancel of a completed job: want 409, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleGetJob_ReportsLiveProgress covers the manager-first lookup: while a
// job runs, only the in-memory job carries its status and progress. Preferring
// the datastore reported "pending, 0%" for the whole life of the job.
func TestHandleGetJob_ReportsLiveProgress(t *testing.T) {
	srv, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	running := make(chan struct{})
	release := make(chan struct{})
	j, err := srv.submitJob(context.Background(), "test", "long job", nil,
		func(ctx context.Context, j *job.Job) error {
			j.UpdateProgress(0.5, "halfway")
			close(running)
			<-release
			return nil
		})
	if err != nil {
		t.Fatalf("submit job: %v", err)
	}
	defer close(release)

	<-running

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+j.ID, nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var got struct {
		Status   string  `json:"status"`
		Progress float64 `json:"progress"`
		Message  string  `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	if got.Status != string(job.JobStatusRunning) {
		t.Errorf("status = %q, want running", got.Status)
	}
	if got.Progress != 0.5 {
		t.Errorf("progress = %v, want 0.5", got.Progress)
	}
	if got.Message != "halfway" {
		t.Errorf("message = %q, want \"halfway\"", got.Message)
	}
}

// TestHandleGetJobRaceFree serves a job while its worker mutates it. The
// handlers must marshal a Snapshot(); encoding the live *job.Job races with
// the worker and fails under -race.
func TestHandleGetJobRaceFree(t *testing.T) {
	srv, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	release := make(chan struct{})
	j, err := srv.submitJob(context.Background(), "test", "churning job", nil,
		func(ctx context.Context, j *job.Job) error {
			for i := 0; ; i++ {
				select {
				case <-release:
					return nil
				default:
				}
				j.UpdateProgress(float64(i%100)/100, "working")
				j.SetResult(map[string]any{"step": i})
			}
		})
	if err != nil {
		t.Fatalf("submit job: %v", err)
	}

	for i := 0; i < 50; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+j.ID, nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET job: want 200, got %d", w.Code)
		}

		req = httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
		w = httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("list jobs: want 200, got %d", w.Code)
		}
	}

	close(release)
	<-j.Done()
}

func TestHandleCancelJob_WrongMethod(t *testing.T) {
	srv, jobID, cleanup := setupJobServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+jobID+"/cancel", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// handleJobStats tests
// ---------------------------------------------------------------------------

func TestHandleJobStats_OK(t *testing.T) {
	srv, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stats", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleJobStats_WrongMethod(t *testing.T) {
	srv, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/stats", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// handleListJobs with status filter
// ---------------------------------------------------------------------------

func TestHandleListJobs_WithStatusFilter(t *testing.T) {
	srv, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?status=completed", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleJobs_WrongMethod(t *testing.T) {
	srv, cleanup := setupTestServerWithJobs(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// handleJobDetail method-not-allowed
// ---------------------------------------------------------------------------

func TestHandleJobDetail_MethodNotAllowed(t *testing.T) {
	srv, jobID, cleanup := setupJobServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/jobs/"+jobID, nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d: %s", w.Code, w.Body.String())
	}
}
