package api

import (
	"context"
	"errors"
	"testing"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

// blindProvider cannot report on its instances, the way the bhyve provider
// cannot once a VM's on-disk state is gone.
type blindProvider struct {
	*mockProvider
}

func (blindProvider) GetInstanceState(context.Context, provider.InstanceHandle) (provider.InstanceState, error) {
	return "", errors.New("failed to load VM state: no such file or directory")
}

// TestSyncMarksUnreportableInstanceUnknown covers the drift this pass exists to
// correct. An instance the provider can no longer describe is not evidence that
// it is still running: leaving the stored value alone is what left a destroyed
// VM listed as running indefinitely, which in turn made it undeletable.
func TestSyncMarksUnreportableInstanceUnknown(t *testing.T) {
	srv, ds := setupTestServerWithProvider(t, blindProvider{newMockProvider()})

	inst := &datastore.Instance{
		ID:       "vm1",
		Name:     "vm1",
		Provider: "mock",
		State:    provider.StateRunning,
		Spec:     provider.InstanceSpec{Name: "vm1"},
		Handle:   provider.InstanceHandle{ID: "vm1", Provider: "mock"},
	}
	if err := ds.CreateInstance(context.Background(), inst); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	srv.SyncInstanceStates()

	got, err := ds.GetInstance(context.Background(), "vm1")
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if got.State != provider.StateUnknown {
		t.Errorf("state = %q, want %q", got.State, provider.StateUnknown)
	}
}
