package jail

import (
	"context"

	"github.com/hospitus/hospitus/pkg/dataset"

	"github.com/hospitus/hospitus/pkg/storage"
)

// recordingStorage stands in for the ZFS backend so volume tests can assert on
// what the provider asks for, and run on a host with no pool — including macOS,
// where the real backend does not exist at all.
type recordingStorage struct {
	storage.Manager

	mountpoint string
	// parent is the ZFS parent volumes are created under. Empty falls back to
	// the configured one rather than a literal pool name.
	parent string

	created  []storageCreateCall
	deleted  []storageDeleteCall
	createFn func(name string, opts storage.VolumeOptions) (*storage.Volume, error)
}

type storageCreateCall struct {
	Name string
	Opts storage.VolumeOptions
}

type storageDeleteCall struct {
	Name string
	Opts storage.DeleteOptions
}

func (r *recordingStorage) CreateVolume(_ context.Context, name string, opts storage.VolumeOptions) (*storage.Volume, error) {
	r.created = append(r.created, storageCreateCall{Name: name, Opts: opts})
	if r.createFn != nil {
		return r.createFn(name, opts)
	}
	// Derived, not literal: a hardcoded pool name makes an assertion on
	// FullName pass even when the provider is configured with another parent.
	parent := r.parent
	if parent == "" {
		parent = dataset.Child("volumes")
	}
	return &storage.Volume{
		Name:     name,
		FullName: parent + "/" + name,
		Path:     r.mountpoint,
	}, nil
}

func (r *recordingStorage) DeleteVolume(_ context.Context, name string, opts storage.DeleteOptions) error {
	r.deleted = append(r.deleted, storageDeleteCall{Name: name, Opts: opts})
	return nil
}
