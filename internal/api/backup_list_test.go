package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hospitus/hospitus/pkg/backup"
)

// TestListAllBackupsReturnsWhatExists covers a collection endpoint that always
// answered with an empty list, and a detail endpoint that always answered 404,
// whatever backups the manager held.
func TestListAllBackupsReturnsWhatExists(t *testing.T) {
	bm := backup.NewBackupManager(t.TempDir(), nil, nil)
	bm.Seed("web", &backup.BackupInfo{ID: "web-1", InstanceID: "web", Type: backup.BackupTypeSnapshot})
	bm.Seed("db", &backup.BackupInfo{ID: "db-1", InstanceID: "db", Type: backup.BackupTypeFull})

	srv := &Server{config: &ServerConfig{}, logger: slog.Default(), backupManager: bm}

	w := httptest.NewRecorder()
	srv.handleListAllBackups(w, httptest.NewRequest(http.MethodGet, "/api/v1/backups", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var listed []BackupInfoResponse
	if err := json.NewDecoder(w.Body).Decode(&listed); err != nil {
		t.Fatalf("decoding the listing: %v", err)
	}
	if len(listed) != 2 {
		t.Errorf("listed %d backups, want the two that exist", len(listed))
	}
}

// TestGetBackupFindsOneAcrossInstances covers the detail endpoint, which could
// never succeed.
func TestGetBackupFindsOneAcrossInstances(t *testing.T) {
	bm := backup.NewBackupManager(t.TempDir(), nil, nil)
	bm.Seed("web", &backup.BackupInfo{ID: "web-1", InstanceID: "web", Type: backup.BackupTypeSnapshot})

	srv := &Server{config: &ServerConfig{}, logger: slog.Default(), backupManager: bm}

	w := httptest.NewRecorder()
	srv.handleGetBackup(w, httptest.NewRequest(http.MethodGet, "/api/v1/backups/web-1", nil), "web-1")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d for a backup that exists, want %d", w.Code, http.StatusOK)
	}

	w = httptest.NewRecorder()
	srv.handleGetBackup(w, httptest.NewRequest(http.MethodGet, "/api/v1/backups/absent", nil), "absent")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d for a backup that does not exist, want %d", w.Code, http.StatusNotFound)
	}
}
