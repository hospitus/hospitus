package jail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/storage"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestCreateVolume(t *testing.T) {
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		if name == "zfs" && len(args) >= 1 {
			switch args[0] {
			case "list":
				// existence check uses -H and must report "missing".
				if len(args) >= 2 && args[1] == "-H" {
					return nil, errors.New("does not exist")
				}
				return nil, nil // ensureZFSDataset parent exists
			case "get":
				return []byte("/zroot/hospitus/volumes/data\n"), nil
			}
		}
		return nil, nil
	}}
	backend := &recordingStorage{mountpoint: "/zroot/hospitus/volumes/data"}
	p := &JailProvider{runner: fake, stateDir: t.TempDir(), zfsParent: "zroot/hospitus/jails", storageBackend: backend}

	vol, err := p.CreateVolume(context.Background(), "data", VolumeCreateOptions{Quota: "10G", Compression: "zstd"})
	if err != nil {
		t.Fatalf("CreateVolume err = %v", err)
	}
	if vol.Name != "data" || vol.ZFSDataset != "zroot/hospitus/volumes/data" {
		t.Errorf("unexpected volume: %+v", vol)
	}

	// The dataset is the backend's job now; what this provider must get right is
	// the request it makes.
	if len(backend.created) != 1 {
		t.Fatalf("expected one volume creation, got %d", len(backend.created))
	}
	got := backend.created[0]
	if got.Name != "data" {
		t.Errorf("volume name = %q, want data", got.Name)
	}
	if got.Opts.Quota != "10G" {
		t.Errorf("quota = %q, want 10G", got.Opts.Quota)
	}
	if got.Opts.Properties["compression"] != "zstd" {
		t.Errorf("compression = %q, want zstd", got.Opts.Properties["compression"])
	}
}

func TestCreateVolumeInvalidName(t *testing.T) {
	p := &JailProvider{runner: &execx.Fake{}, stateDir: t.TempDir(), zfsParent: "zroot/hospitus/jails"}
	if _, err := p.CreateVolume(context.Background(), "bad/name", VolumeCreateOptions{}); err == nil {
		t.Fatal("expected validation error")
	}
}

// The backend owns the existence check now, so a volume that is already there is
// reported by it and must reach the caller rather than being swallowed.
func TestCreateVolumeAlreadyExists(t *testing.T) {
	backend := &recordingStorage{
		createFn: func(name string, _ storage.VolumeOptions) (*storage.Volume, error) {
			return nil, fmt.Errorf("volume %s already exists", name)
		},
	}
	p := &JailProvider{
		runner:         &execx.Fake{},
		stateDir:       t.TempDir(),
		zfsParent:      "zroot/hospitus/jails",
		storageBackend: backend,
	}

	_, err := p.CreateVolume(context.Background(), "data", VolumeCreateOptions{})
	if err == nil {
		t.Fatal("expected already-exists error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should say the volume exists, got: %v", err)
	}
}

func TestGetVolume(t *testing.T) {
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		if name == "zfs" && len(args) >= 1 && args[0] == "get" {
			// mountpoint / used / available / quota / compression queries
			for _, a := range args {
				switch a {
				case "mountpoint":
					return []byte("/zroot/hospitus/volumes/data\n"), nil
				case "used":
					return []byte("1048576\n"), nil
				case "available":
					return []byte("9999999\n"), nil
				case "quota":
					return []byte("10737418240\n"), nil
				case "compression":
					return []byte("lz4\n"), nil
				}
			}
		}
		return nil, nil // zfs list -H succeeds
	}}
	p := &JailProvider{runner: fake, stateDir: t.TempDir(), zfsParent: "zroot/hospitus/jails"}

	vol, err := p.GetVolume(context.Background(), "data")
	if err != nil {
		t.Fatalf("GetVolume err = %v", err)
	}
	if vol.UsedBytes != 1048576 || vol.Compression != "lz4" || vol.Quota != 10737418240 {
		t.Errorf("unexpected volume from ZFS: %+v", vol)
	}
}

func TestGetVolumeNotFound(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, errors.New("no dataset") }}
	p := &JailProvider{runner: fake, stateDir: t.TempDir(), zfsParent: "zroot/hospitus/jails"}
	if _, err := p.GetVolume(context.Background(), "data"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestListVolumesEmptyWhenNoParent(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, errors.New("no parent") }}
	p := &JailProvider{runner: fake, stateDir: t.TempDir(), zfsParent: "zroot/hospitus/jails"}
	vols, err := p.ListVolumes(context.Background())
	if err != nil {
		t.Fatalf("ListVolumes err = %v", err)
	}
	if len(vols) != 0 {
		t.Errorf("expected empty, got %d", len(vols))
	}
}

func TestResizeVolume(t *testing.T) {
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		if name == "zfs" && len(args) >= 1 && args[0] == "get" {
			return []byte("none\n"), nil
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake, stateDir: t.TempDir(), zfsParent: "zroot/hospitus/jails"}
	if err := p.ResizeVolume(context.Background(), "data", "20G"); err != nil {
		t.Fatalf("ResizeVolume err = %v", err)
	}
	if want := "zfs set quota=20G zroot/hospitus/volumes/data"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestSnapshotVolume(t *testing.T) {
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		if name == "zfs" && len(args) >= 1 && args[0] == "get" {
			return []byte("none\n"), nil
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake, stateDir: t.TempDir(), zfsParent: "zroot/hospitus/jails"}
	if err := p.SnapshotVolume(context.Background(), "data", "snap1"); err != nil {
		t.Fatalf("SnapshotVolume err = %v", err)
	}
	if want := "zfs snapshot zroot/hospitus/volumes/data@snap1"; !hasCmd(fake, want) {
		t.Errorf("missing %q; got %+v", want, fake.Calls)
	}
}
