package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/time/rate"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// newMiddlewareServer builds a minimal Server to exercise middleware functions.
// It does not require a database or providers.
func newMiddlewareServer(cfg *ServerConfig) *Server {
	if cfg == nil {
		cfg = &ServerConfig{}
	}
	return &Server{
		config:           cfg,
		authRateLimiters: make(map[string]*rate.Limiter),
		rateLimiters:     make(map[string]*rate.Limiter),
		logger:           slog.Default(),
	}
}

// ── extractClientIP ───────────────────────────────────────────────────────────

// TestExtractClientIP_NoTrustedProxies verifies that when no trusted proxies are
// configured, extractClientIP always returns the direct connection IP, ignoring
// any X-Forwarded-For or X-Real-IP headers.
func TestExtractClientIP_NoTrustedProxies(t *testing.T) {
	s := newMiddlewareServer(&ServerConfig{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	ip := s.extractClientIP(req)
	if ip != "10.0.0.5" {
		t.Errorf("expected direct IP 10.0.0.5, got %q", ip)
	}
}

// TestExtractClientIP_TrustedProxy_XForwardedFor covers which entry of
// X-Forwarded-For is believed.
//
// The header reads "client, proxy1, proxy2": each hop appends the address it
// saw, so only the rightmost entries were written by infrastructure we trust.
// This used to take the leftmost, which is whatever the client sent — so a
// caller could name its own address, take a fresh rate-limit bucket per
// request, and choose what the audit log recorded.
func TestExtractClientIP_TrustedProxy_XForwardedFor(t *testing.T) {
	cases := []struct {
		name      string
		trusted   []string
		forwarded string
		want      string
	}{
		{
			// Only the direct peer is trusted, so the rightmost entry — the
			// address that peer saw — is the closest one we did not write.
			name:      "the nearest hop the proxy reported",
			trusted:   []string{"127.0.0.1"},
			forwarded: "203.0.113.10, 10.0.0.1",
			want:      "10.0.0.1",
		},
		{
			// With the inner proxy trusted too, the walk continues past it.
			name:      "past our own proxies",
			trusted:   []string{"127.0.0.1", "10.0.0.1"},
			forwarded: "203.0.113.10, 10.0.0.1",
			want:      "203.0.113.10",
		},
		{
			// The spoof: a client prepends an address of its choosing. It must
			// not be believed over the one the proxy actually observed.
			name:      "a client-supplied prefix is ignored",
			trusted:   []string{"127.0.0.1", "10.0.0.1"},
			forwarded: "1.2.3.4, 203.0.113.10, 10.0.0.1",
			want:      "203.0.113.10",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newMiddlewareServer(&ServerConfig{TrustedProxies: c.trusted})
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "127.0.0.1:9000"
			req.Header.Set("X-Forwarded-For", c.forwarded)

			if ip := s.extractClientIP(req); ip != c.want {
				t.Errorf("extractClientIP = %q, want %q", ip, c.want)
			}
		})
	}
}

// TestExtractClientIP_TrustedProxy_XRealIP verifies that X-Real-IP is used when
// X-Forwarded-For is absent and the request comes from a trusted proxy.
func TestExtractClientIP_TrustedProxy_XRealIP(t *testing.T) {
	s := newMiddlewareServer(&ServerConfig{
		TrustedProxies: []string{"127.0.0.1"},
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:9000"
	req.Header.Set("X-Real-IP", "198.51.100.42")

	ip := s.extractClientIP(req)
	if ip != "198.51.100.42" {
		t.Errorf("expected 198.51.100.42 from X-Real-IP, got %q", ip)
	}
}

// TestExtractClientIP_UntrustedProxy verifies that forwarded headers are ignored
// when the request comes from an untrusted IP.
func TestExtractClientIP_UntrustedProxy(t *testing.T) {
	s := newMiddlewareServer(&ServerConfig{
		TrustedProxies: []string{"10.0.0.1"},
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.50:4321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	ip := s.extractClientIP(req)
	if ip != "192.168.1.50" {
		t.Errorf("expected direct IP 192.168.1.50, got %q", ip)
	}
}

// TestExtractClientIP_TrustedCIDR verifies that a CIDR range (e.g. 10.0.0.0/8)
// in TrustedProxies matches addresses within that range.
func TestExtractClientIP_TrustedCIDR(t *testing.T) {
	s := newMiddlewareServer(&ServerConfig{
		TrustedProxies: []string{"10.0.0.0/8"},
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.20.30.40:80"
	req.Header.Set("X-Forwarded-For", "203.0.113.99")

	ip := s.extractClientIP(req)
	if ip != "203.0.113.99" {
		t.Errorf("expected 203.0.113.99 from trusted CIDR, got %q", ip)
	}
}

// TestExtractClientIP_InvalidRemoteAddr verifies graceful fallback when
// RemoteAddr cannot be split into host:port (e.g. a Unix socket path).
func TestExtractClientIP_InvalidRemoteAddr(t *testing.T) {
	s := newMiddlewareServer(&ServerConfig{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "not-an-addr" // no port component

	ip := s.extractClientIP(req)
	// Should fall back to the raw string — not crash.
	if ip == "" {
		t.Error("expected non-empty IP fallback for unparseable RemoteAddr")
	}
}

// TestExtractClientIP_XForwardedForInvalidIP verifies that an invalid IP in
// X-Forwarded-For is ignored and the direct connection IP is returned.
func TestExtractClientIP_XForwardedForInvalidIP(t *testing.T) {
	s := newMiddlewareServer(&ServerConfig{
		TrustedProxies: []string{"127.0.0.1"},
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:9000"
	req.Header.Set("X-Forwarded-For", "not-an-ip")

	ip := s.extractClientIP(req)
	// Invalid forwarded IP should fall through to X-Real-IP / direct IP.
	if ip != "127.0.0.1" {
		t.Errorf("expected direct IP 127.0.0.1 when forwarded IP is invalid, got %q", ip)
	}
}

// ── recoveryMiddleware ────────────────────────────────────────────────────────

// TestRecoveryMiddleware_PanicReturns500 verifies that the recovery middleware
// catches a panicking handler and returns HTTP 500.
func TestRecoveryMiddleware_PanicReturns500(t *testing.T) {
	s := newMiddlewareServer(nil)

	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic in handler")
	})

	wrapped := s.recoveryMiddleware(panicHandler)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	w := httptest.NewRecorder()

	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 after panic, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Internal server error") {
		t.Errorf("expected 'Internal server error' in body, got: %s", w.Body.String())
	}
}

// TestRecoveryMiddleware_NoPanicPassesThrough verifies that a normal handler
// is unaffected by the recovery wrapper.
func TestRecoveryMiddleware_NoPanicPassesThrough(t *testing.T) {
	s := newMiddlewareServer(nil)

	normalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := s.recoveryMiddleware(normalHandler)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 from normal handler, got %d", w.Code)
	}
}

// ── loggingMiddleware branches ────────────────────────────────────────────────

// TestLoggingMiddleware_AuthRequired verifies that a non-health path with auth
// enabled and a key present logs the AUTHENTICATED auth status (no panic).
func TestLoggingMiddleware_AuthRequired(t *testing.T) {
	s := newMiddlewareServer(&ServerConfig{EnableAuth: true})

	handler := s.loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.Header.Set("X-API-Key", "some-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// TestLoggingMiddleware_Unauthenticated verifies the UNAUTHENTICATED branch
// (auth enabled but no key provided).
func TestLoggingMiddleware_Unauthenticated(t *testing.T) {
	s := newMiddlewareServer(&ServerConfig{EnableAuth: true})

	handler := s.loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	// No X-API-Key set.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// TestLoggingMiddleware_NoAuthRequired verifies the NO_AUTH_REQUIRED branch
// (auth disabled, non-health path).
func TestLoggingMiddleware_NoAuthRequired(t *testing.T) {
	s := newMiddlewareServer(&ServerConfig{EnableAuth: false})

	handler := s.loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// TestLoggingMiddleware_RateLimited verifies the 429 security-logging branch.
func TestLoggingMiddleware_RateLimited(t *testing.T) {
	s := newMiddlewareServer(&ServerConfig{EnableAuth: false})

	handler := s.loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", w.Code)
	}
}

// TestLoggingMiddleware_ForbiddenPath verifies the 403 security-logging branch.
func TestLoggingMiddleware_ForbiddenPath(t *testing.T) {
	s := newMiddlewareServer(&ServerConfig{EnableAuth: false})

	handler := s.loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/something", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

// TestLoggingMiddleware_HealthPath verifies the PUBLIC branch for /health.
func TestLoggingMiddleware_HealthPath(t *testing.T) {
	s := newMiddlewareServer(&ServerConfig{EnableAuth: true})

	handler := s.loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for /health path, got %d", w.Code)
	}
}

// ── securityHeadersMiddleware ─────────────────────────────────────────────────

// TestSecurityHeadersMiddleware_Headers verifies the security headers are set.
func TestSecurityHeadersMiddleware_Headers(t *testing.T) {
	s := newMiddlewareServer(nil)

	handler := s.securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	mustHave := []struct{ header, wantContains string }{
		{"X-Content-Type-Options", "nosniff"},
		{"X-Frame-Options", "DENY"},
		{"Cache-Control", "no-store"},
		{"Content-Security-Policy", "default-src"},
	}
	for _, tc := range mustHave {
		got := w.Header().Get(tc.header)
		if !strings.Contains(got, tc.wantContains) {
			t.Errorf("header %s = %q, want substring %q", tc.header, got, tc.wantContains)
		}
	}
}

// TestSecurityHeadersMiddleware_HSTSWithTLS verifies that HSTS is set when
// both TLSCert and TLSKey are non-empty (i.e. TLS is actually configured).
func TestSecurityHeadersMiddleware_HSTSWithTLS(t *testing.T) {
	s := newMiddlewareServer(&ServerConfig{
		TLSCert: "/path/to/cert.pem",
		TLSKey:  "/path/to/key.pem",
	})

	handler := s.securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	hsts := w.Header().Get("Strict-Transport-Security")
	if hsts == "" {
		t.Error("expected Strict-Transport-Security header when TLS is configured, got empty")
	}
	if !strings.Contains(hsts, "max-age=") {
		t.Errorf("expected HSTS max-age directive, got: %q", hsts)
	}
}

// TestSecurityHeadersMiddleware_NoHSTSWithoutTLS verifies that HSTS is NOT set
// when TLS cert/key are empty.
func TestSecurityHeadersMiddleware_NoHSTSWithoutTLS(t *testing.T) {
	s := newMiddlewareServer(&ServerConfig{
		TLSCert: "",
		TLSKey:  "",
	})

	handler := s.securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	hsts := w.Header().Get("Strict-Transport-Security")
	if hsts != "" {
		t.Errorf("expected no HSTS when TLS is not configured, got: %q", hsts)
	}
}

// ── writeJSON / writeError ────────────────────────────────────────────────────

// TestWriteJSON_SetsContentType verifies that writeJSON sets the correct
// Content-Type and encodes the body.
func TestWriteJSON_SetsContentType(t *testing.T) {
	s := newMiddlewareServer(nil)
	w := httptest.NewRecorder()
	s.writeJSON(w, http.StatusCreated, map[string]string{"key": "value"})

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
	if w.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "value") {
		t.Errorf("expected JSON body to contain 'value', got: %s", w.Body.String())
	}
}

// TestWriteError_Format verifies that writeError produces a JSON error object.
func TestWriteError_Format(t *testing.T) {
	s := newMiddlewareServer(nil)
	w := httptest.NewRecorder()
	s.writeError(w, http.StatusBadRequest, "bad input")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "bad input") {
		t.Errorf("expected 'bad input' in body, got: %s", w.Body.String())
	}
}
