package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ----------------------------------------------------------------------------
// handleAutoStart (GET /api/v1/autostart)
// With no providers in the test registry, the list should be empty.
// ----------------------------------------------------------------------------

func TestHandleListAutoStart_Empty(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/autostart", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/autostart: expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["instances"]; !ok {
		t.Error("expected 'instances' key in response")
	}
}

func TestHandleAutoStart_MethodNotAllowed(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/autostart", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /api/v1/autostart: expected 405, got %d", w.Code)
	}
}

func TestHandleTriggerAutoStart_NoProviders(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/autostart", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// With no auto-start providers, it should succeed (0 started)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/autostart: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleAutoStartInstance_UnregisteredProvider(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	// Well-formed path, but provider "jail" is not registered in the test
	// server, so it resolves to a 404 (see assertion below).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/autostart/jail/nonexistent", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// Provider "jail" not registered in test server → 404
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/autostart/jail/nonexistent: expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

// ----------------------------------------------------------------------------
// Backup handlers
// ----------------------------------------------------------------------------

func TestHandleListAllBackups(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/backups", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/backups: expected 200, got %d", w.Code)
	}
}

func TestHandleGetBackup_NotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/backups/nonexistent-backup-id", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/backups/nonexistent: expected 404, got %d", w.Code)
	}
}

func TestHandleDeleteBackup_NotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/backups/someid", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// The test server does have a backup manager — the comment here used to
	// say otherwise — so what fails is the backup: it does not exist.
	if w.Code != http.StatusNotFound {
		t.Fatalf("DELETE /api/v1/backups/someid = %d, want 404", w.Code)
	}
}

func TestHandleVerifyBackup_MethodNotAllowed(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/backups/someid/verify", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// Verify only allows POST
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/v1/backups/someid/verify: expected 405, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleRestoreBackup_MethodNotAllowed(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/backups/someid/restore", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/v1/backups/someid/restore: expected 405, got %d body=%s", w.Code, w.Body.String())
	}
}

// ----------------------------------------------------------------------------
// Firewall handlers (no firewall manager in test server)
// ----------------------------------------------------------------------------

func TestHandleExposePort_NoFirewall(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"instance":    "myjail",
		"host_port":   8080,
		"target_port": 80,
		"target_ip":   "10.1.0.2",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/firewall/expose", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/v1/firewall/expose: expected 503, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleUnexposePort_NoFirewall(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"instance":  "myjail",
		"host_port": 8080,
	})
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/firewall/expose", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("DELETE /api/v1/firewall/expose: expected 503, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleListExposedPorts_NoFirewall(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/firewall/expose/myjail", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/v1/firewall/expose/myjail: expected 503, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleSetupNAT_NoFirewall(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"instance":       "myjail",
		"provider":       "jail",
		"source_network": "10.1.0.0/24",
		"out_interface":  "hospitus0",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/firewall/nat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/v1/firewall/nat: expected 503, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleRemoveNAT_NoFirewall(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/firewall/nat", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("DELETE /api/v1/firewall/nat: expected 503, got %d body=%s", w.Code, w.Body.String())
	}
}

// ----------------------------------------------------------------------------
// Upgrade instance
// ----------------------------------------------------------------------------

func TestHandleUpgradeInstance_NotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	body, _ := json.Marshal(map[string]string{"target_release": "14.3-RELEASE"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/nonexistent/upgrade", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("POST /api/v1/instances/nonexistent/upgrade: expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

// ----------------------------------------------------------------------------
// Clone handlers
// ----------------------------------------------------------------------------

func TestHandleCloneFromInstance_NotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	body, _ := json.Marshal(map[string]string{"name": "newjail"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/nonexistent/clone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("POST /api/v1/instances/nonexistent/clone: expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleCloneFromSnapshot_NotFound(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	body, _ := json.Marshal(map[string]string{"name": "newjail", "snapshot": "snap1"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/nonexistent/clone-snapshot", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("POST clone-snapshot: expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}
