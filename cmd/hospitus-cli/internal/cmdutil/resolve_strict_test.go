package cmdutil

import (
	"context"
	"fmt"
	"testing"

	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/internal/datastore"
)

// strictResolverMock implements only the two methods the strict resolver uses;
// the embedded interface leaves the rest nil (unused here).
type strictResolverMock struct {
	APIClientInterface
	instances []*datastore.Instance
}

func (m *strictResolverMock) GetInstance(_ context.Context, id string) (*datastore.Instance, error) {
	for _, inst := range m.instances {
		if inst.ID == id {
			return inst, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func (m *strictResolverMock) ListInstances(_ context.Context, _ client.ListInstancesFilter) ([]*datastore.Instance, error) {
	return m.instances, nil
}

// TestResolveInstanceNameStrict verifies destructive resolution never matches a
// near name: "jail1" must NOT resolve to "jail10" (audit HIGH
// jail/destroy.go:107 fuzzy matching destroys the wrong instance).
func TestResolveInstanceNameStrict(t *testing.T) {
	// Provider set, as the daemon always reports it: an instance with none is
	// one whose provider cannot be confirmed, and a destructive resolution
	// refuses those.
	mock := &strictResolverMock{instances: []*datastore.Instance{
		{ID: "jail10", Provider: "jail"},
		{ID: "web", Provider: "jail"},
	}}
	ctx := context.Background()

	// Substring of an existing ID must NOT resolve.
	if got, err := ResolveInstanceNameStrict(ctx, mock, "jail1", "jail"); err == nil {
		t.Errorf("resolve %q should fail, got %q", "jail1", got)
	}

	// Exact ID resolves to itself.
	if got, err := ResolveInstanceNameStrict(ctx, mock, "jail10", "jail"); err != nil || got != "jail10" {
		t.Errorf("resolve exact = (%q, %v), want (jail10, nil)", got, err)
	}

	// Unknown name fails.
	if _, err := ResolveInstanceNameStrict(ctx, mock, "ghost", "jail"); err == nil {
		t.Error("resolve unknown name should fail")
	}
}

// A provider-scoped command must not reach an instance of another provider.
// Both resolvers took an exact name match as the answer before looking at the
// provider, so "hospitus qemu start ubuntu-server" started a bhyve VM of that
// name — and destroy resolves the same way.
func TestResolversRefuseAnotherProvidersInstance(t *testing.T) {
	mock := &strictResolverMock{instances: []*datastore.Instance{
		{ID: "ubuntu-server", Provider: "bhyve"},
		{ID: "web", Provider: "jail"},
	}}
	ctx := context.Background()

	if got, err := ResolveInstanceNameStrict(ctx, mock, "ubuntu-server", "qemu"); err == nil {
		t.Errorf("strict resolve of a bhyve VM as qemu returned %q, want an error", got)
	}
	if got, err := ResolveInstanceName(ctx, mock, "ubuntu-server", "jail"); err == nil {
		t.Errorf("resolve of a bhyve VM as jail returned %q, want an error", got)
	}

	// The instance's own provider still resolves.
	if got, err := ResolveInstanceNameStrict(ctx, mock, "ubuntu-server", "bhyve"); err != nil || got != "ubuntu-server" {
		t.Errorf("resolve = (%q, %v), want (ubuntu-server, nil)", got, err)
	}
	// A caller that names no provider is unaffected.
	if got, err := ResolveInstanceNameStrict(ctx, mock, "web", ""); err != nil || got != "web" {
		t.Errorf("resolve without a provider = (%q, %v), want (web, nil)", got, err)
	}
}
