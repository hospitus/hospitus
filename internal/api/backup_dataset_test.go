package api

import (
	"context"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

// instanceStore answers GetInstance and nothing else, which is all the resolver
// asks of a datastore.
type instanceStore struct {
	Datastore
	instance *datastore.Instance
	err      error
}

func (s *instanceStore) GetInstance(context.Context, string) (*datastore.Instance, error) {
	return s.instance, s.err
}

// TestInstanceDatasetResolverReadsWhatTheProviderRecorded covers the dataset a
// backup is taken of.
//
// It was built as <pool>/jails/<id> for every provider, from a pool that already
// had "backups" appended, so the name existed for no instance on a normal
// installation and every snapshot, restore and verify failed.
func TestInstanceDatasetResolverReadsWhatTheProviderRecorded(t *testing.T) {
	jail := &datastore.Instance{Provider: "jail"}
	jail.Handle.Metadata = map[string]interface{}{"zfs_dataset": "zroot/hospitus/jails/web"}

	vm := &datastore.Instance{Provider: "bhyve"}
	vm.Spec.Disks = []provider.DiskSpec{
		{Path: "/dev/zvol/zroot/hospitus/bhyve/ubuntu/disk0", Type: provider.DiskTypeZVOL},
	}

	tests := []struct {
		name     string
		instance *datastore.Instance
		want     string
	}{
		{"a jail, from its handle", jail, "zroot/hospitus/jails/web"},
		{"a VM, from the parent of its ZVOL", vm, "zroot/hospitus/bhyve/ubuntu"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolve := instanceDatasetResolver(&instanceStore{instance: tt.instance})
			got, err := resolve(context.Background(), "any")
			if err != nil {
				t.Fatalf("resolver: %v", err)
			}
			if got != tt.want {
				t.Errorf("dataset = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestInstanceDatasetResolverRefusesAProviderWithoutZFS keeps a container from
// being told it has a dataset. Naming one that does not exist turns a backup
// into a failure the operator has to decode from zfs(8).
func TestInstanceDatasetResolverRefusesAProviderWithoutZFS(t *testing.T) {
	container := &datastore.Instance{Provider: "podman"}

	resolve := instanceDatasetResolver(&instanceStore{instance: container})
	_, err := resolve(context.Background(), "nginx")
	if err == nil {
		t.Fatal("a podman container was given a dataset to snapshot")
	}
	if !strings.Contains(err.Error(), "podman") {
		t.Errorf("error = %v, want it to name the provider", err)
	}
}
