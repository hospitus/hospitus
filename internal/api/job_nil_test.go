package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestJobsEndpointNoNilPanic verifies a server built by NewServer has a live
// jobManager, so /api/v1/jobs no longer nil-panics (audit HIGH
// job_handlers.go:71: s.jobManager was never initialized in NewServer).
func TestJobsEndpointNoNilPanic(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	if s.jobManager == nil {
		t.Fatal("NewServer did not initialize jobManager")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	w := httptest.NewRecorder()
	// Must not panic.
	s.mux.ServeHTTP(w, req)

	if w.Code >= 500 {
		t.Errorf("GET /api/v1/jobs returned %d: %s", w.Code, w.Body.String())
	}
}
