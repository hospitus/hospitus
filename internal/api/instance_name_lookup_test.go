package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

// resourceRecordingProvider records SetInstanceResources calls. It does not
// implement provider.ReconfigureProvider, which is what makes it stand for
// every provider but jail.
type resourceRecordingProvider struct {
	*mockProvider
	calls     atomic.Int32
	lastCPUs  atomic.Int32
	lastMemMB atomic.Int64
}

func (p *resourceRecordingProvider) SetInstanceResources(_ context.Context, _ provider.InstanceHandle, r provider.ResourceSpec) error {
	p.calls.Add(1)
	p.lastCPUs.Store(int32(r.CPUs))
	p.lastMemMB.Store(r.MemoryMB)
	return nil
}

// seedInstance stores an instance whose ID and name differ, which is the shape
// that made the name-resolved handlers write to the wrong key.
func seedInstance(t *testing.T, ds *datastore.Datastore, id, name string) *datastore.Instance {
	t.Helper()
	inst := &datastore.Instance{
		ID:       id,
		Name:     name,
		Provider: "mock",
		State:    provider.StateStopped,
		Spec: provider.InstanceSpec{
			Name:     name,
			CPUs:     1,
			MemoryMB: 512,
		},
		Handle: provider.InstanceHandle{ID: id, Provider: "mock"},
	}
	if err := ds.CreateInstance(context.Background(), inst); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	return inst
}

// TestPatchInstanceByNameUpdatesDatastore covers a PATCH addressed by name.
// The datastore updates are keyed by ID, so passing the raw path value made
// every by-name PATCH fail with 500.
func TestPatchInstanceByNameUpdatesDatastore(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	seedInstance(t, ds, "inst-0001", "web")

	body, _ := json.Marshal(map[string]interface{}{
		"spec": map[string]interface{}{"cpus": 4, "memory_mb": 2048},
	})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instances/web", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PATCH by name: want 200, got %d: %s", w.Code, w.Body.String())
	}

	stored, err := ds.GetInstance(context.Background(), "inst-0001")
	if err != nil {
		t.Fatalf("reload instance: %v", err)
	}
	if stored.Spec.CPUs != 4 || stored.Spec.MemoryMB != 2048 {
		t.Errorf("stored spec = %d cpus/%d MB, want 4/2048", stored.Spec.CPUs, stored.Spec.MemoryMB)
	}
}

// TestPatchAppliesResourcesWithoutReconfigureProvider covers the provider side
// of PATCH for a provider that has no Reconfigure: the change must still reach
// the provider through the mandatory SetInstanceResources, not stop at the
// datastore while the API reports success.
func TestPatchAppliesResourcesWithoutReconfigureProvider(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	rec := &resourceRecordingProvider{mockProvider: newMockProvider()}
	if _, ok := interface{}(rec).(provider.ReconfigureProvider); ok {
		t.Fatal("test provider must not implement ReconfigureProvider")
	}
	srv.registry = provider.NewRegistry()
	if err := srv.registry.Register(rec); err != nil {
		t.Fatal(err)
	}

	seedInstance(t, ds, "inst-0002", "db")

	body, _ := json.Marshal(map[string]interface{}{
		"spec": map[string]interface{}{"cpus": 8, "memory_mb": 4096},
	})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instances/db", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PATCH: want 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := rec.calls.Load(); got != 1 {
		t.Fatalf("SetInstanceResources called %d times, want 1", got)
	}
	if got := rec.lastCPUs.Load(); got != 8 {
		t.Errorf("applied CPUs = %d, want 8", got)
	}
	if got := rec.lastMemMB.Load(); got != 4096 {
		t.Errorf("applied MemoryMB = %d, want 4096", got)
	}
}

// TestPatchWithoutResourceFieldsSkipsProvider keeps the fallback narrow: a
// PATCH that changes no resource field must not call the provider.
func TestPatchWithoutResourceFieldsSkipsProvider(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	rec := &resourceRecordingProvider{mockProvider: newMockProvider()}
	srv.registry = provider.NewRegistry()
	if err := srv.registry.Register(rec); err != nil {
		t.Fatal(err)
	}

	seedInstance(t, ds, "inst-0003", "cache")

	body, _ := json.Marshal(map[string]interface{}{
		"spec": map[string]interface{}{"description": "notes only"},
	})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instances/cache", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PATCH: want 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := rec.calls.Load(); got != 0 {
		t.Errorf("SetInstanceResources called %d times for a non-resource PATCH, want 0", got)
	}
}

// TestStartInstanceByNameUpdatesStoredState covers a start addressed by name:
// the state write is keyed by ID, so the raw path value left the stored state
// stale while the instance was actually running.
func TestStartInstanceByNameUpdatesStoredState(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	seedInstance(t, ds, "inst-0004", "api")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/api/start", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("start by name: want 200, got %d: %s", w.Code, w.Body.String())
	}

	stored, err := ds.GetInstance(context.Background(), "inst-0004")
	if err != nil {
		t.Fatalf("reload instance: %v", err)
	}
	if stored.State != provider.StateRunning {
		t.Errorf("stored state = %q, want running", stored.State)
	}
}

// TestStopInstanceByNameUpdatesStoredState is the stop-side counterpart.
func TestStopInstanceByNameUpdatesStoredState(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	seedInstance(t, ds, "inst-0005", "worker")
	if err := ds.UpdateInstanceState(context.Background(), "inst-0005", provider.StateRunning); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/worker/stop", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("stop by name: want 200, got %d: %s", w.Code, w.Body.String())
	}

	stored, err := ds.GetInstance(context.Background(), "inst-0005")
	if err != nil {
		t.Fatalf("reload instance: %v", err)
	}
	if stored.State != provider.StateStopped {
		t.Errorf("stored state = %q, want stopped", stored.State)
	}
}

// TestGetInstanceEventsByName covers the events lookup: events are keyed by
// instance ID, so a name in the path used to return an empty list.
func TestGetInstanceEventsByName(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	seedInstance(t, ds, "inst-0006", "events-host")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/events-host/events", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("events by name: want 200, got %d: %s", w.Code, w.Body.String())
	}

	var events []*datastore.Event
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("events by name returned none; CreateInstance records a 'created' event")
	}
	for _, ev := range events {
		if ev.InstanceID != "inst-0006" {
			t.Errorf("event instance_id = %q, want inst-0006", ev.InstanceID)
		}
	}
}

// TestGetInstanceEventsUnknownInstance keeps the added lookup honest: an
// instance that exists under neither ID nor name is a 404, not an empty list.
func TestGetInstanceEventsUnknownInstance(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/nosuch/events", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("events for unknown instance: want 404, got %d: %s", w.Code, w.Body.String())
	}
}
