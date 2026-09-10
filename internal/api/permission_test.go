package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hospitus/hospitus/internal/auth"
)

// TestRequiredPermission maps request method/path to the permission the caller
// must hold (audit HIGH middleware.go:34: no route enforced permissions).
func TestRequiredPermission(t *testing.T) {
	tests := []struct {
		method, path, want string
	}{
		{http.MethodGet, "/api/v1/instances", "read"},
		{http.MethodGet, "/api/v1/instances/web", "read"},
		{http.MethodPost, "/api/v1/instances", "write"},
		{http.MethodDelete, "/api/v1/instances/web", "write"},
		{http.MethodPut, "/api/v1/instances/web", "write"},
		{http.MethodPost, "/api/v1/instances/web/exec", "exec"},
		{http.MethodGet, "/api/v1/instances/web/console", "exec"},
		{http.MethodGet, "/api/v1/instances/web/console/ws", "exec"},
		{http.MethodGet, "/api/v1/console/sessions", "exec"},
		{http.MethodGet, "/api/v1/auth/keys", "admin"},
		{http.MethodPost, "/api/v1/auth/keys", "admin"},

		// Classification is on exact path segments, so an instance whose name
		// merely starts with "exec" or "console" does not change the
		// permission its routes require.
		{http.MethodDelete, "/api/v1/instances/exec-web", "write"},
		{http.MethodDelete, "/api/v1/instances/console-web", "write"},
		{http.MethodGet, "/api/v1/instances/exec-web", "read"},
		{http.MethodGet, "/api/v1/instances/console-web", "read"},
		{http.MethodPost, "/api/v1/instances/exec-web/start", "write"},
		{http.MethodGet, "/api/v1/instances/my-console/events", "read"},
		{http.MethodPost, "/api/v1/instances/exec-web/exec", "exec"},
	}
	for _, tt := range tests {
		if got := requiredPermission(tt.method, tt.path); got != tt.want {
			t.Errorf("requiredPermission(%s, %s) = %q, want %q", tt.method, tt.path, got, tt.want)
		}
	}
}

// TestHasPerm verifies wildcard and exact-match semantics.
func TestHasPerm(t *testing.T) {
	if !hasPerm([]string{"*"}, "write") {
		t.Error("wildcard should grant write")
	}
	if hasPerm([]string{"read"}, "write") {
		t.Error("read-only key must not have write")
	}
	if !hasPerm([]string{"read"}, "read") {
		t.Error("read key should have read")
	}
	if hasPerm(nil, "read") {
		t.Error("nil perms should grant nothing")
	}
}

// TestAuthMiddlewareEnforcesPermission verifies a read-only API key can read
// but cannot perform a write (DELETE), exercising the middleware end-to-end
// (audit HIGH middleware.go:34).
func TestAuthMiddlewareEnforcesPermission(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	am := auth.NewAuthManager()
	const roKey = "READONLYKEY-1234567890abcd"
	if err := am.AddAPIKey("ro", "readonly", roKey, []string{"read"}, nil); err != nil {
		t.Fatal(err)
	}
	s.authProvider = am
	s.config.EnableAuth = true

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	h := s.authMiddleware(next)

	// read-only key + GET is allowed.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.Header.Set("X-API-Key", roKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !called {
		t.Errorf("read GET should pass, got %d called=%v", w.Code, called)
	}

	// read-only key + DELETE is forbidden and never reaches the handler.
	called = false
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/instances/web", nil)
	req.Header.Set("X-API-Key", roKey)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || called {
		t.Errorf("read DELETE should be 403 and not reach handler, got %d called=%v", w.Code, called)
	}
}
