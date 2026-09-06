package bhyve

import (
	"context"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
	"github.com/hospitus/hospitus/pkg/storage"
)

// fakeStorage records the volumes a provider asks the backend to create.
type fakeStorage struct {
	storage.Manager

	created []struct {
		name string
		opts storage.VolumeOptions
	}
}

func (f *fakeStorage) CreateVolume(_ context.Context, name string, opts storage.VolumeOptions) (*storage.Volume, error) {
	f.created = append(f.created, struct {
		name string
		opts storage.VolumeOptions
	}{name, opts})
	return &storage.Volume{Name: name, FullName: name}, nil
}

// TestCreateZVOLDelegatesToStorage checks that a VM disk is created as a sized
// volume through the storage backend rather than by hand-built zfs arguments.
func TestCreateZVOLDelegatesToStorage(t *testing.T) {
	backend := &fakeStorage{}
	p := &BhyveProvider{
		zfsParent:      testZFSParent,
		storageBackend: backend,
		// The existence probe must fail so creation proceeds.
		runner: &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
			if len(args) > 0 && args[0] == "list" {
				return nil, errNoVolume
			}
			return nil, nil
		}},
	}

	zvol := testZFSParent + "/vm1/disk0"
	if err := p.createZVOL(context.Background(), zvol, 20); err != nil {
		t.Fatalf("createZVOL: %v", err)
	}

	if len(backend.created) != 1 {
		t.Fatalf("expected one volume to be created, got %d", len(backend.created))
	}
	got := backend.created[0]
	if got.name != zvol {
		t.Errorf("volume name = %q, want %q", got.name, zvol)
	}
	if got.opts.Size != "20G" {
		t.Errorf("size = %q, want 20G — without it the backend creates a filesystem, not a block device", got.opts.Size)
	}
}

// An orphan left by a failed create is reused rather than recreated, which is a
// bhyve recovery decision the storage backend must not be asked to make.
func TestCreateZVOLReusesExistingVolume(t *testing.T) {
	backend := &fakeStorage{}
	p := &BhyveProvider{
		zfsParent:      testZFSParent,
		storageBackend: backend,
		// Every command succeeds, so the probe reports the volume present.
		runner: &execx.Fake{},
	}

	if err := p.createZVOL(context.Background(), testZFSParent+"/vm1/disk0", 20); err != nil {
		t.Fatalf("createZVOL: %v", err)
	}
	if len(backend.created) != 0 {
		t.Errorf("an existing volume must not be recreated, got %v", backend.created)
	}
}

var errNoVolume = errStr("dataset does not exist")

type errStr string

func (e errStr) Error() string { return string(e) }

func TestZVOLExistsQueriesVolumeType(t *testing.T) {
	fake := &execx.Fake{}
	p := &BhyveProvider{runner: fake}

	if !p.zvolExists(context.Background(), testZFSParent+"/vm1/disk0") {
		t.Error("zvolExists = false when zfs reports success")
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("expected one zfs call, got %d", len(fake.Calls))
	}
	got := strings.Join(fake.Calls[0].Args, " ")
	if got != "list -t volume -H "+testZFSParent+"/vm1/disk0" {
		t.Errorf("zfs args = %q", got)
	}
}
