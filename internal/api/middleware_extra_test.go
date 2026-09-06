package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hospitus/hospitus/internal/auth"
)

// TestLoggingResponseWriterIsHijacker verifies the logging wrapper implements
// http.Hijacker so the WebSocket console can upgrade (audit HIGH
// middleware.go:254).
func TestLoggingResponseWriterIsHijacker(t *testing.T) {
	var _ http.Hijacker = (*loggingResponseWriter)(nil)

	// A plain recorder is not a Hijacker, so Hijack surfaces an error rather
	// than panicking — proving it delegates to the underlying writer.
	lw := &loggingResponseWriter{ResponseWriter: httptest.NewRecorder()}
	if _, _, err := lw.Hijack(); err == nil {
		t.Error("expected error when underlying writer is not a Hijacker")
	}
}

// TestAuthLimiterNotConsumedOnSuccess verifies repeated successful authenticated
// requests are not throttled by the auth (brute-force) rate limiter, whose
// burst is only 3 (audit HIGH middleware.go:388).
func TestAuthLimiterNotConsumedOnSuccess(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	am := auth.NewAuthManager()
	const key = "valid-key-abcdefghij"
	if err := am.AddAPIKey("k", "full", key, []string{"*"}, nil); err != nil {
		t.Fatal(err)
	}
	s.authProvider = am
	s.config.EnableAuth = true

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := s.authMiddleware(next)

	// Far more than the burst of 3; all must succeed since success does not
	// consume the auth limiter.
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		req.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d throttled by auth limiter on a valid key", i+1)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200", i+1, w.Code)
		}
	}
}
