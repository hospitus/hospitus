package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/time/rate"

	"github.com/hospitus/hospitus/internal/auth"
)

// newKeyServer builds the least Server that handleCreateAuthKey needs to reach
// its input validation: a real AuthManager, since a SimpleAuthProvider is
// turned away with 501 first. No datastore, because a rejected request must
// not reach one.
func newKeyServer() *Server {
	return &Server{
		config:           &ServerConfig{EnableAuth: true},
		authProvider:     auth.NewAuthManager(),
		authRateLimiters: make(map[string]*rate.Limiter),
		rateLimiters:     make(map[string]*rate.Limiter),
		logger:           slog.Default(),
	}
}

// TestCreateAuthKeyRejectsOutOfRangeTTL covers ttl_days.
//
// Only a positive value produced an expiry, so a negative one silently took
// the same path as 0 and handed back a key that never expires. A large one
// overflowed the int64 nanoseconds of time.Duration and expired the key in the
// past.
func TestCreateAuthKeyRejectsOutOfRangeTTL(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want int
	}{
		{"negative", `{"name":"k","ttl_days":-1}`, http.StatusBadRequest},
		{"past the overflow point", `{"name":"k","ttl_days":9999999999}`, http.StatusBadRequest},
		{"just past the maximum", `{"name":"k","ttl_days":36501}`, http.StatusBadRequest},
		{"at the maximum", `{"name":"k","ttl_days":36500}`, http.StatusCreated},
		{"no expiry", `{"name":"k","ttl_days":0}`, http.StatusCreated},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newKeyServer()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/keys", bytes.NewReader([]byte(tt.body)))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.handleCreateAuthKey(w, req)

			if w.Code != tt.want {
				t.Fatalf("ttl_days %s: got %d, want %d; body=%s", tt.name, w.Code, tt.want, w.Body.String())
			}
		})
	}
}
