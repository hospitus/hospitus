package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestValidateUserFilePathConfinement verifies that export/import paths are
// confined to a base directory, preventing arbitrary file write as root
// (audit CRITICAL: internal/api/export_import_handler.go).
func TestValidateUserFilePathConfinement(t *testing.T) {
	base := "/var/lib/hospitus"
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"inside base", "/var/lib/hospitus/exports/web.tar", false},
		{"base subdir", "/var/lib/hospitus/backups/web.tar", false},
		{"outside base absolute", "/etc/master.passwd", true},
		{"outside base loader", "/boot/loader.conf", true},
		{"traversal resolving outside", "/var/lib/hospitus/../etc/passwd", true},
		{"sibling prefix trick", "/var/lib/hospitus-evil/x.tar", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateUserFilePath(tt.path, base)
			if tt.wantErr && err == nil {
				t.Errorf("validateUserFilePath(%q) = nil, want error", tt.path)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("validateUserFilePath(%q) = %v, want nil", tt.path, err)
			}
		})
	}
}

// TestExportRejectsPathOutsideDataDir verifies the HTTP handler refuses an
// export path outside the data directory with 400, not proceeding to tar.
func TestExportRejectsPathOutsideDataDir(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	ctx := context.Background()
	inst := makeSimpleInstance("victim", "mock")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"export_path": "/etc/master.passwd",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/victim/export", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("export to /etc/master.passwd: expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}
