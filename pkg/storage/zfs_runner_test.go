//go:build freebsd || linux

package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// zfsBackend builds a backend whose zfs(8) calls are recorded rather than run,
// against a fake pool that starts with the datasets in existing.
//
// The pool has to carry state: create is followed by a read-back, so a fake that
// answered "missing" to every list would fail every creation, and one that
// answered "present" would make every creation a duplicate.
func zfsBackend(t *testing.T, existing ...string) (*ZFSBackend, *execx.Fake) {
	t.Helper()

	pool := make(map[string]bool, len(existing))
	for _, dataset := range existing {
		pool[dataset] = true
	}

	fake := &execx.Fake{}
	fake.Func = func(name string, args []string) ([]byte, error) {
		if name != "zfs" || len(args) == 0 {
			return nil, nil
		}

		switch args[0] {
		case "create", "clone", "snapshot":
			pool[args[len(args)-1]] = true
			return nil, nil
		case "destroy":
			delete(pool, args[len(args)-1])
			return nil, nil
		case "list":
			target := args[len(args)-1]
			if !pool[target] {
				return nil, errNoDataset
			}
			if strings.Contains(target, "@") {
				// name, creation, used, referenced — what getSnapshotNoLock reads.
				return []byte(target + "\t1755000000\t0\t1024\n"), nil
			}
			return nil, nil
		case "get":
			// getProperties reads "name property value" triples.
			target := args[len(args)-1]
			if !pool[target] {
				return nil, errNoDataset
			}
			return []byte(target + "\tmountpoint\t/" + target + "\n" +
				target + "\tused\t1024\n" +
				target + "\tavailable\t2048\n"), nil
		}
		return nil, nil
	}

	backend := &ZFSBackend{
		parentDataset: "zroot/hospitus",
		runner:        fake,
	}
	return backend, fake
}

// errNoDataset stands in for zfs(8) reporting a missing dataset.
var errNoDataset = errors.New("dataset does not exist")

// cmdLine renders a recorded call so a test can assert on the exact command.
func cmdLine(c execx.Call) string {
	if len(c.Args) == 0 {
		return c.Name
	}
	return c.Name + " " + strings.Join(c.Args, " ")
}

func ranCommand(f *execx.Fake, want string) bool {
	for _, c := range f.Calls {
		if cmdLine(c) == want {
			return true
		}
	}
	return false
}

func TestCreateVolumeBuildsZFSCommand(t *testing.T) {
	backend, fake := zfsBackend(t)
	backend.config.Compression = "lz4"

	_, err := backend.CreateVolume(context.Background(), "web", VolumeOptions{
		Quota:      "10G",
		Mountpoint: "/data/web",
	})
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}

	want := "zfs create -o quota=10G -o mountpoint=/data/web -o compression=lz4 -p zroot/hospitus/web"
	if !ranCommand(fake, want) {
		t.Errorf("create command was not issued as expected.\nwant: %s\ngot:  %v", want, fake.Calls)
	}
}

// TestCreateVolumeGivesAPropertyOnce reproduces a failure seen on a live host:
// "hospitus jail volume create pgdata --size 100G" died with "property
// 'compression' specified multiple times". The jail provider passes compression
// as a caller property and the backend added its own default beside it.
func TestCreateVolumeGivesAPropertyOnce(t *testing.T) {
	backend, fake := zfsBackend(t)
	backend.config.Compression = "lz4"

	_, err := backend.CreateVolume(context.Background(), "pgdata", VolumeOptions{
		Quota:      "120G",
		Mountpoint: "/data/pgdata",
		Properties: map[string]string{"compression": "zstd"},
	})
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}

	want := "zfs create -o quota=120G -o mountpoint=/data/pgdata -o compression=zstd -p zroot/hospitus/pgdata"
	if !ranCommand(fake, want) {
		t.Errorf("create command was not issued as expected.\nwant: %s\ngot:  %v", want, fake.Calls)
	}
}

// TestCreateVolumeLetsPropertiesOverrideQuotaAndMountpoint covers the other two
// properties the backend emits beside Properties.
func TestCreateVolumeLetsPropertiesOverrideQuotaAndMountpoint(t *testing.T) {
	backend, fake := zfsBackend(t)
	backend.config.Compression = ""

	_, err := backend.CreateVolume(context.Background(), "web", VolumeOptions{
		Quota:      "10G",
		Mountpoint: "/data/web",
		Properties: map[string]string{"quota": "20G", "mountpoint": "/srv/web"},
	})
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}

	var createLine string
	for _, c := range fake.Calls {
		line := cmdLine(c)
		if strings.Count(line, "quota=") > 1 || strings.Count(line, "mountpoint=") > 1 {
			t.Errorf("a property reached zfs twice: %s", line)
		}
		if strings.Contains(line, "zfs create") {
			createLine = line
		}
	}

	// The duplicate check alone still passes if a property is dropped or
	// rewritten: the dataset would then take the default mountpoint and no
	// quota at all. Assert the values that were asked for.
	if createLine == "" {
		t.Fatal("no zfs create call was made")
	}
	for _, want := range []string{"quota=20G", "mountpoint=/srv/web"} {
		if !strings.Contains(createLine, want) {
			t.Errorf("zfs create is missing %s: %s", want, createLine)
		}
	}
}

func TestCreateVolumeRefusesExistingDataset(t *testing.T) {
	backend, _ := zfsBackend(t, "zroot/hospitus/web")

	_, err := backend.CreateVolume(context.Background(), "web", VolumeOptions{})
	if err == nil {
		t.Fatal("CreateVolume on an existing dataset returned nil")
	}
	if !strings.Contains(err.Error(), "exist") {
		t.Errorf("error should say the volume exists, got: %v", err)
	}
}

func TestCreateSnapshotBuildsZFSCommand(t *testing.T) {
	backend, fake := zfsBackend(t, "zroot/hospitus/web")

	_, err := backend.CreateSnapshot(context.Background(), "web", "before-upgrade", SnapshotOptions{})
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	want := "zfs snapshot zroot/hospitus/web@before-upgrade"
	if !ranCommand(fake, want) {
		t.Errorf("snapshot command was not issued as expected.\nwant: %s\ngot:  %v", want, fake.Calls)
	}
}

func TestSetQuotaBuildsZFSCommand(t *testing.T) {
	backend, fake := zfsBackend(t, "zroot/hospitus/web")

	if err := backend.SetQuota(context.Background(), "web", "20G"); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}

	want := "zfs set quota=20G zroot/hospitus/web"
	if !ranCommand(fake, want) {
		t.Errorf("quota command was not issued as expected.\nwant: %s\ngot:  %v", want, fake.Calls)
	}
}

// TestDeleteVolumeHonoursRecursive checks the flag that decides whether children
// go with the dataset, since getting it wrong either fails or destroys too much.
func TestDeleteVolumeHonoursRecursive(t *testing.T) {
	tests := []struct {
		name string
		opts DeleteOptions
		want string
	}{
		{"plain", DeleteOptions{}, "zfs destroy zroot/hospitus/web"},
		{"recursive", DeleteOptions{Recursive: true}, "zfs destroy -r zroot/hospitus/web"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, fake := zfsBackend(t, "zroot/hospitus/web")

			if err := backend.DeleteVolume(context.Background(), "web", tt.opts); err != nil {
				t.Fatalf("DeleteVolume: %v", err)
			}
			if !ranCommand(fake, tt.want) {
				t.Errorf("want %q, got %v", tt.want, fake.Calls)
			}
		})
	}
}

// TestCreateVolumeAsZVOL covers the block-device shape a VM disk needs.
//
// A zvol is created with -V and has neither a quota nor a mountpoint; passing
// either makes ZFS refuse the command, so the builder has to drop them rather
// than merely ignore them.
func TestCreateVolumeAsZVOL(t *testing.T) {
	backend, fake := zfsBackend(t)

	_, err := backend.CreateVolume(context.Background(), "vm1-disk0", VolumeOptions{
		Size:       "20G",
		Quota:      "50G",
		Mountpoint: "/should-not-be-used",
	})
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}

	want := "zfs create -V 20G -p zroot/hospitus/vm1-disk0"
	if !ranCommand(fake, want) {
		t.Errorf("zvol command was not issued as expected.\nwant: %s\ngot:  %v", want, fake.Calls)
	}
}

// A filesystem dataset keeps quota and mountpoint; only a zvol drops them.
func TestCreateVolumeAsFilesystemKeepsQuotaAndMountpoint(t *testing.T) {
	backend, fake := zfsBackend(t)

	_, err := backend.CreateVolume(context.Background(), "data", VolumeOptions{
		Quota:      "50G",
		Mountpoint: "/data",
	})
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}

	want := "zfs create -o quota=50G -o mountpoint=/data -p zroot/hospitus/data"
	if !ranCommand(fake, want) {
		t.Errorf("filesystem command was not issued as expected.\nwant: %s\ngot:  %v", want, fake.Calls)
	}
}
