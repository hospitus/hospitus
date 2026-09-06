package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMethodNotAllowedAlwaysNamesTheMethods covers the Allow header.
//
// RFC 9110 requires a 405 to carry Allow. Most of this package answered the
// status bare, so a client was told its method was wrong and never which one
// would work.
//
// Every path is pinned to the one outcome it can reach with mockProvider —
// skipping whatever was not a 405 hid three surprises: /metrics answered 404
// for a route that exists, /images redirects, and /auth/keys is refused before
// method dispatch.
func TestMethodNotAllowedAlwaysNamesTheMethods(t *testing.T) {
	s, ds := setupTestServer(t)

	inst := makeSimpleInstance("allow-probe", "mock")
	if err := ds.CreateInstance(context.Background(), inst); err != nil {
		t.Fatal(err)
	}

	// A method no route serves, so every route that dispatches on the method
	// falls to its 405.
	const wrong = "TRACE"

	cases := []struct {
		path  string
		code  int
		allow string // expected Allow, empty when the path never reaches dispatch
		why   string
	}{
		{"/api/v1/instances", http.StatusMethodNotAllowed, "GET, POST", ""},
		{"/api/v1/instances/allow-probe", http.StatusMethodNotAllowed, "GET, PATCH, DELETE", ""},
		{"/api/v1/instances/allow-probe/backups", http.StatusMethodNotAllowed, "GET, POST", ""},
		{"/api/v1/instances/allow-probe/metrics", http.StatusMethodNotAllowed, "GET", ""},
		{"/api/v1/instances/allow-probe/health", http.StatusMethodNotAllowed, "GET", ""},
		{"/api/v1/providers", http.StatusMethodNotAllowed, "GET", ""},
		{"/api/v1/jobs", http.StatusMethodNotAllowed, "GET", ""},
		{"/api/v1/jobs/some-id", http.StatusMethodNotAllowed, "GET, DELETE", ""},
		{"/api/v1/backups", http.StatusMethodNotAllowed, "GET", ""},
		{"/api/v1/stacks", http.StatusMethodNotAllowed, "GET, POST", ""},
		{"/api/v1/firewall/expose", http.StatusMethodNotAllowed, "GET, POST, DELETE", ""},
		{"/api/v1/autostart", http.StatusMethodNotAllowed, "GET, POST", ""},

		// These never reach method dispatch, and the reason differs per route.
		// Pinned so a change that makes them reachable shows up here.
		{"/api/v1/instances/allow-probe/snapshots", http.StatusNotImplemented, "", "mockProvider implements no SnapshotProvider"},
		{"/api/v1/instances/allow-probe/checkpoint", http.StatusNotImplemented, "", "mockProvider implements no CheckpointProvider"},
		{"/api/v1/instances/allow-probe/media", http.StatusNotImplemented, "", "mockProvider implements no MediaProvider"},
		{"/api/v1/images", http.StatusTemporaryRedirect, "", "the mux redirects to the trailing-slash form"},
		{"/api/v1/auth/keys", http.StatusForbidden, "", "the route needs admin, refused before dispatch"},
	}

	// /api/v1/volumes depends on the host, not on the code: NewServer keeps a
	// storage backend when the pool is there, and the route then reaches method
	// dispatch. Pinning 501 would have failed on the FreeBSD runners for a
	// reason that is not a defect.
	volumes := struct {
		path  string
		code  int
		allow string
		why   string
	}{"/api/v1/volumes", http.StatusMethodNotAllowed, "GET, POST", ""}
	if s.storage == nil {
		volumes.code, volumes.allow = http.StatusNotImplemented, ""
		volumes.why = "no ZFS storage backend on this host"
	}
	cases = append(cases, volumes)

	known := map[string]bool{
		http.MethodGet: true, http.MethodHead: true, http.MethodPost: true,
		http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
		http.MethodOptions: true,
	}

	for _, tt := range cases {
		req := httptest.NewRequest(wrong, tt.path, nil)
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, req)

		if w.Code != tt.code {
			detail := tt.why
			if detail == "" {
				detail = "should reach method dispatch"
			}
			t.Errorf("%s %s = %d, want %d (%s); body=%s", wrong, tt.path, w.Code, tt.code, detail, w.Body.String())
			continue
		}
		if tt.code != http.StatusMethodNotAllowed {
			continue
		}

		allow := w.Header().Get("Allow")
		if allow != tt.allow {
			t.Errorf("%s %s: Allow = %q, want %q", wrong, tt.path, allow, tt.allow)
			continue
		}
		for _, m := range strings.Split(allow, ", ") {
			if !known[m] {
				t.Errorf("%s %s: Allow lists %q, which is not an HTTP method", wrong, tt.path, m)
			}
			if m == wrong {
				t.Errorf("%s %s: Allow lists the very method that was refused", wrong, tt.path)
			}
		}
	}
}
