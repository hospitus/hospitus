package api

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// deletingProvider records the instances it was asked to delete.
type deletingProvider struct {
	*mockProvider
	deleted  []string
	failWith error
}

func (p *deletingProvider) DeleteInstance(_ context.Context, handle provider.InstanceHandle, _ bool) error {
	if p.failWith != nil {
		return p.failWith
	}
	p.deleted = append(p.deleted, handle.ID)
	return nil
}

// TestCreatedInstanceIsRemovedWhenItCannotBeRecorded covers a jail, VM or
// container left on the host with no API record.
//
// Creation succeeded at the provider and the datastore write then failed; the
// handler returned an error and left the resource behind, where no listing shows
// it, no call stops it, and its name is held against the next attempt.
func TestCreatedInstanceIsRemovedWhenItCannotBeRecorded(t *testing.T) {
	prov := &deletingProvider{mockProvider: newMockProvider()}
	srv := &Server{config: &ServerConfig{}, logger: slog.Default()}

	cause := errors.New("database is locked")
	handle := provider.InstanceHandle{ID: "web", Provider: "mock"}

	err := srv.discardCreatedInstance(context.Background(), prov, handle, cause)

	if !errors.Is(err, cause) {
		t.Errorf("error = %v, want the original cause", err)
	}
	if len(prov.deleted) != 1 || prov.deleted[0] != "web" {
		t.Errorf("deleted = %v, want the instance that could not be recorded", prov.deleted)
	}
}

// TestUnremovableInstanceIsReportedLoudly covers the case where cleanup fails
// too: the operator has to be told, because nothing else will.
func TestUnremovableInstanceIsReportedLoudly(t *testing.T) {
	prov := &deletingProvider{mockProvider: newMockProvider(), failWith: errors.New("jail is busy")}
	srv := &Server{config: &ServerConfig{}, logger: slog.Default()}

	err := srv.discardCreatedInstance(context.Background(), prov,
		provider.InstanceHandle{ID: "web", Provider: "mock"}, errors.New("database is locked"))

	if err == nil {
		t.Fatal("an instance that could be neither recorded nor removed reported success")
	}
	if got := err.Error(); !strings.Contains(got, "could not be removed") {
		t.Errorf("error = %q, want it to say the instance is still there", got)
	}
}
