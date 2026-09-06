package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMalformedBackupBodyIsRefused covers a request body that could not be read.
//
// Every decoding error became req.Type = "snapshot", so a truncated or mistyped
// request performed a state-changing ZFS snapshot the caller never asked for.
func TestMalformedBackupBodyIsRefused(t *testing.T) {
	srv := &Server{config: &ServerConfig{}, logger: slog.Default()}

	malformed := []struct {
		name string
		body string
	}{
		{"truncated JSON", `{"type": "full"`},
		// 400, not a nil-backupManager panic: decodeJSONBody sets
		// DisallowUnknownFields, so this body never reaches the manager.
		{"a field that is not there", `{"typo": "full"}`},
		{"a type of the wrong shape", `{"type": 3}`},
		{"prose", `not json at all`},
	}

	for _, tt := range malformed {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/v1/instances/web/backups", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", "application/json")

			srv.handleCreateInstanceBackup(w, r, "web")

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d for body %q", w.Code, http.StatusBadRequest, tt.body)
			}
		})
	}
}
