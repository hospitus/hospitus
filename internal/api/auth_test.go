package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/hospitus/hospitus/internal/auth"
	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// newAuthServer builds a minimal Server wired with a SimpleAuthProvider and
// the given valid API key.  It does NOT use NewServer, avoiding bcrypt overhead
// and external resource setup (backup manager, SSH manager, etc.).
//
// Only fields required by authMiddleware and writeError are populated:
//   - config         (EnableAuth, AllowInsecureTLS)
//   - authProvider   (SimpleAuthProvider — constant-time plaintext compare)
//   - authRateLimiters / rateLimiters  (non-nil maps)
//   - logger
func newAuthServer(validKey string) *Server {
	return &Server{
		config: &ServerConfig{
			EnableAuth:       true,
			AllowInsecureTLS: true,
		},
		authProvider:     auth.NewSimpleAuthProvider([]string{validKey}),
		authRateLimiters: make(map[string]*rate.Limiter),
		rateLimiters:     make(map[string]*rate.Limiter),
		logger:           slog.Default(),
	}
}

// okHandler is a trivial HTTP handler that records whether it was reached.
func okHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reached != nil {
			*reached = true
		}
		w.WriteHeader(http.StatusOK)
	})
}

// ── TestAuthMiddleware_MissingKey ─────────────────────────────────────────────

// TestAuthMiddleware_MissingKey verifies that a request that omits the
// X-API-Key header is rejected with HTTP 401.
func TestAuthMiddleware_MissingKey(t *testing.T) {
	s := newAuthServer("secret-key")
	reached := false
	handler := s.authMiddleware(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	// No X-API-Key header set.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
	if reached {
		t.Error("downstream handler must not be called when API key is missing")
	}
	if !strings.Contains(w.Body.String(), "Missing API key") {
		t.Errorf("expected 'Missing API key' in body, got: %s", w.Body.String())
	}
}

// ── TestAuthMiddleware_InvalidKey ─────────────────────────────────────────────

// TestAuthMiddleware_InvalidKey verifies that a request carrying an incorrect
// API key is rejected with HTTP 401.
func TestAuthMiddleware_InvalidKey(t *testing.T) {
	s := newAuthServer("correct-key")
	reached := false
	handler := s.authMiddleware(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
	if reached {
		t.Error("downstream handler must not be called for an invalid key")
	}
	if !strings.Contains(w.Body.String(), "Invalid API key") {
		t.Errorf("expected 'Invalid API key' in body, got: %s", w.Body.String())
	}
}

// ── TestAuthMiddleware_ValidKey ───────────────────────────────────────────────

// TestAuthMiddleware_ValidKey verifies that a request carrying the correct
// API key is allowed through to the downstream handler.
func TestAuthMiddleware_ValidKey(t *testing.T) {
	const key = "my-valid-key"
	s := newAuthServer(key)
	reached := false
	handler := s.authMiddleware(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d — body: %s", w.Code, w.Body.String())
	}
	if !reached {
		t.Error("downstream handler was not called for a valid key")
	}
}

// ── TestAuthMiddleware_HealthBypass ───────────────────────────────────────────

// TestAuthMiddleware_HealthBypass verifies that /health is exempt from auth.
func TestAuthMiddleware_HealthBypass(t *testing.T) {
	s := newAuthServer("secret")
	reached := false
	handler := s.authMiddleware(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	// Intentionally omit the API key.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for /health without auth, got %d", w.Code)
	}
	if !reached {
		t.Error("downstream handler must be called for /health without auth")
	}
}

// ── TestRateLimitAuthBurst ────────────────────────────────────────────────────

// TestRateLimitAuthBurst verifies that more than `burst` consecutive
// authentication failures from the same IP address result in HTTP 429.
//
// The per-IP auth rate limiter is configured with burst=3 (see middleware.go).
// The first 3 requests consume all tokens and receive 401; the 4th gets 429.
func TestRateLimitAuthBurst(t *testing.T) {
	s := newAuthServer("valid-key")
	// Pre-exhaust the burst by injecting a limiter with burst=3 for the test IP.
	// httptest.NewRequest sets RemoteAddr to "192.0.2.1:1234" by default.
	testIP := "192.0.2.1"
	limiter := rate.NewLimiter(rate.Limit(5.0/60.0), 3) // 5 req/min, burst=3
	s.authRateLimiters[testIP] = limiter

	handler := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Exhaust all 3 burst tokens with wrong-key requests (expect 401 each time).
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		req.Header.Set("X-API-Key", "wrong-key")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("request %d: expected 401, got %d", i+1, w.Code)
		}
	}

	// 4th request: no tokens remaining — must get 429 (rate limited).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("4th request: expected 429, got %d — body: %s", w.Code, w.Body.String())
	}
}

// ── TestNoTLSRequiresFlag ─────────────────────────────────────────────────────

// TestNoTLSRequiresFlag verifies that Server.Start() returns an error when
// neither TLS certificates are configured nor AllowInsecureTLS is set to true.
// This prevents accidental unencrypted production deployments.
func TestNoTLSRequiresFlag(t *testing.T) {
	ds, err := datastore.NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("NewDatastore: %v", err)
	}
	registry := provider.NewRegistry()

	server, srvErr := NewServer(":0", ds, registry, &ServerConfig{
		AllowNoAuth:      true,  // avoid "no keys configured" error noise
		AllowInsecureTLS: false, // the setting under test
		EnableAuth:       false,
		// No TLSCert / TLSKey set.
	})
	if srvErr != nil {
		t.Fatalf("NewServer: %v", srvErr)
	}

	// Start() should return before binding a socket when no TLS is configured
	// and AllowInsecureTLS is false.
	startErr := server.Start()

	// Cleanup: cancel background goroutines started by Start().
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)

	if startErr == nil {
		t.Fatal("expected error from Start() with no TLS and AllowInsecureTLS=false, got nil")
	}
	if !strings.Contains(startErr.Error(), "TLS") {
		t.Errorf("expected 'TLS' in error message, got: %v", startErr)
	}
}

// TestAPIKeysWithoutEnableAuthIsInsecure pins the footgun that shipped as a
// real auth bypass: when API keys are configured but EnableAuth is left false,
// NewServer skips wiring the auth provider entirely and the server serves every
// request unauthenticated. main.go must therefore always set EnableAuth=true
// and let the server derive the concrete mode from APIKeys/AllowNoAuth.
func TestAPIKeysWithoutEnableAuthIsInsecure(t *testing.T) {
	ds, err := datastore.NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("NewDatastore: %v", err)
	}
	registry := provider.NewRegistry()
	mockProv := newMockProvider()
	if err := registry.Register(mockProv); err != nil {
		t.Fatalf("register mock provider: %v", err)
	}

	// Misconfiguration: keys present, but EnableAuth not set.
	insecure, err := NewServer(":0", ds, registry, &ServerConfig{
		EnableAuth:       false,
		AllowInsecureTLS: true,
		APIKeys:          []string{"present-but-ignored"},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if insecure.authProvider != nil {
		t.Fatal("guard assumption changed: authProvider should be nil when EnableAuth=false")
	}

	// The corrected contract: EnableAuth=true with keys enforces auth.
	secure, err := NewServer(":0", ds, registry, &ServerConfig{
		EnableAuth:       true,
		AllowInsecureTLS: true,
		APIKeys:          []string{"present-and-enforced"},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	handler := secure.withMiddleware(secure.mux)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without key when EnableAuth=true, got %d", w.Code)
	}
}
