package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Volume handler tests. The test server registers a "mock" provider rather
// than a real JailProvider, so these cover two early returns: 501 when no
// storage backend is configured, and 400 from the "volumes are a jail thing"
// check, which fires on the recorded provider name before the registry is
// consulted at all.
//
// The jail-provider type assertion further down is not reached from here:
// nothing registers a provider under the name "jail" that is not one.

// TestHandleVolumesWithoutStorageBackend covers a host with no ZFS backend.
//
// The volume endpoints serve pkg/storage, which exists on FreeBSD and Linux
// only. Where it is absent the answer is "not implemented" for every method,
// including the ones a method check would reject first: that the feature is
// unavailable is the more useful thing to report.
func TestHandleVolumesWithoutStorageBackend(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	// setupTestServer builds a server on the host platform; make the absence
	// explicit so the test means the same thing on FreeBSD and on macOS.
	srv.storage = nil

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{
			name:   "GET /volumes (no storage backend)",
			method: http.MethodGet,
			path:   "/api/v1/volumes",
			want:   http.StatusNotImplemented,
		},
		{
			name:   "POST /volumes (no storage backend)",
			method: http.MethodPost,
			path:   "/api/v1/volumes",
			want:   http.StatusNotImplemented,
		},
		{
			name:   "PATCH /volumes (no storage backend, checked before the method)",
			method: http.MethodPatch,
			path:   "/api/v1/volumes",
			want:   http.StatusNotImplemented,
		},
		{
			name:   "GET /volumes/myvol (no storage backend)",
			method: http.MethodGet,
			path:   "/api/v1/volumes/myvol",
			want:   http.StatusNotImplemented,
		},
		{
			name:   "DELETE /volumes/myvol (no storage backend)",
			method: http.MethodDelete,
			path:   "/api/v1/volumes/myvol",
			want:   http.StatusNotImplemented,
		},
		{
			name:   "PUT /volumes/myvol (no storage backend, checked before the method)",
			method: http.MethodPut,
			path:   "/api/v1/volumes/myvol",
			want:   http.StatusNotImplemented,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Errorf("got %d, want %d (body: %s)", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestHandleInstanceVolumes_NoJailProvider(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	// Create an instance so the instance lookup succeeds
	id := "inst-vol-test"
	createTestInstanceInDS(t, ds, id, "mock")

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{
			name:   "GET instance volumes (non-jail provider → 400)",
			method: http.MethodGet,
			path:   "/api/v1/instances/" + id + "/volumes",
			want:   http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Errorf("got %d, want %d (body: %s)", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}
