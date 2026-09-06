package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

// capableProvider is a mockProvider that also answers the two optional
// interfaces these tests need. It reports a distinct name so it can be
// registered alongside nothing else and addressed by instances of its own.
type capableProvider struct {
	*mockProvider
	stored provider.AutoStartConfig
}

func (c *capableProvider) Metadata() provider.ProviderMetadata {
	m := c.mockProvider.Metadata()
	m.Name = "capable"
	return m
}

// SetAutoStart normalizes the way jail does: an enabled entry with no priority
// is stored at 50.
func (c *capableProvider) SetAutoStart(_ context.Context, _ provider.InstanceHandle, cfg provider.AutoStartConfig) error {
	if cfg.Priority == 0 && cfg.Enabled {
		cfg.Priority = 50
	}
	c.stored = cfg
	return nil
}

func (c *capableProvider) GetAutoStart(context.Context, provider.InstanceHandle) (*provider.AutoStartConfig, error) {
	cfg := c.stored
	return &cfg, nil
}

func (c *capableProvider) ListAutoStartInstances(context.Context) ([]provider.InstanceHandle, error) {
	return nil, nil
}

func (c *capableProvider) StartAutoStartInstances(context.Context) error { return nil }

func (c *capableProvider) CheckpointInstance(context.Context, provider.InstanceHandle, string) error {
	return nil
}

func (c *capableProvider) RestoreCheckpoint(context.Context, provider.InstanceHandle, string) error {
	return nil
}

func (c *capableProvider) ListCheckpoints(context.Context, provider.InstanceHandle) ([]provider.CheckpointInfo, error) {
	return nil, nil
}

func (c *capableProvider) DeleteCheckpoint(context.Context, provider.InstanceHandle, string) error {
	return nil
}

// setupServerWithProvider is setupTestServer for a caller-supplied provider.
func setupServerWithProvider(t *testing.T, prov provider.Provider) (*Server, *datastore.Datastore) {
	t.Helper()

	ds, err := datastore.NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("NewDatastore: %v", err)
	}
	t.Cleanup(func() { _ = ds.Close() })

	registry := provider.NewRegistry()
	if err := registry.Register(prov); err != nil {
		t.Fatalf("Register: %v", err)
	}

	server, err := NewServer(":8080", ds, registry, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { server.jobManager.Shutdown(2 * time.Second) })

	return server, ds
}

// TestFreeBSDRouteResolvesByName covers addressing the freebsd routes by name.
//
// This handler read the datastore by ID alone while every other instance route
// goes through lookupInstance, so a name that worked for start, stop, media and
// interfaces answered 404 here.
func TestFreeBSDRouteResolvesByName(t *testing.T) {
	s, ds := setupTestServer(t)

	inst := makeTestInstance("fbsd-by-name", "mock") // ID is "fbsd-by-name@mock"
	if err := ds.CreateInstance(context.Background(), inst); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/fbsd-by-name/freebsd/rctl", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// mockProvider implements no RctlProvider, so 501 is the reachable answer.
	// What matters is that it is no longer 404: the name resolved.
	if w.Code != http.StatusNotImplemented {
		t.Errorf("freebsd/rctl by name = %d, want 501; body=%s", w.Code, w.Body.String())
	}
}

// TestInstanceBackupsRejectsInvalidName covers an identifier that cannot name
// an instance. ListBackups and CreateSnapshot do not validate it themselves, so
// it reached the dataset resolver and came back as a 500.
func TestInstanceBackupsRejectsInvalidName(t *testing.T) {
	s, _ := setupTestServer(t)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req := httptest.NewRequest(method, "/api/v1/instances/bad.name/backups", nil)
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("%s /backups with an invalid id = %d, want 400; body=%s", method, w.Code, w.Body.String())
		}
	}
}

// TestCheckpointKnownPathWrongMethod covers a checkpoint resource reached with
// a method it does not serve. The default branch answered 404 for a path that
// exists.
func TestCheckpointKnownPathWrongMethod(t *testing.T) {
	s, ds := setupServerWithProvider(t, &capableProvider{mockProvider: newMockProvider()})

	inst := makeSimpleInstance("cp-inst", "capable")
	if err := ds.CreateInstance(context.Background(), inst); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		path  string
		allow string
	}{
		{"/api/v1/instances/cp-inst/checkpoint", http.MethodPost},
		{"/api/v1/instances/cp-inst/checkpoint/snap1", http.MethodDelete},
		{"/api/v1/instances/cp-inst/checkpoint/snap1/restore", http.MethodPost},
	} {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s = %d, want 405; body=%s", tt.path, w.Code, w.Body.String())
			continue
		}
		if got := w.Header().Get("Allow"); got != tt.allow {
			t.Errorf("GET %s: Allow = %q, want %q", tt.path, got, tt.allow)
		}
	}

	// An unknown shape stays a 404.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/cp-inst/checkpoint/snap1/bogus", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET an unknown checkpoint path = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

// TestSetAutoStartReturnsStoredConfig covers the confirmation the CLI prints.
//
// SetAutoStart takes the config by value and normalizes an absent priority to
// 50; the handler echoed the request, so the caller was told 0.
func TestSetAutoStartReturnsStoredConfig(t *testing.T) {
	s, ds := setupServerWithProvider(t, &capableProvider{mockProvider: newMockProvider()})

	inst := makeSimpleInstance("as-inst", "capable")
	if err := ds.CreateInstance(context.Background(), inst); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"enabled":true}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/autostart/capable/as-inst", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("set autostart = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got AutoStartInstanceInfo
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AutoStart.Priority != 50 {
		t.Errorf("priority = %d, want the stored 50; body=%s", got.AutoStart.Priority, w.Body.String())
	}
}
