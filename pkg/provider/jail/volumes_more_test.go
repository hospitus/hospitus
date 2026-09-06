package jail

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// volumeZFSFake returns a fake where the volume dataset exists and reports a
// mountpoint; jls always fails (jail stopped). fn overrides specific commands.
func volumeZFSFake(mountpoint string, fn func(cmd string, args []string) ([]byte, error)) *execx.Fake {
	return &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		if cmd == "jls" {
			return nil, errAssertStopped
		}
		if cmd == "zfs" && len(args) >= 1 {
			if args[0] == "get" {
				for _, a := range args {
					if a == "mountpoint" {
						return []byte(mountpoint + "\n"), nil
					}
				}
				return []byte("none\n"), nil
			}
			if args[0] == "list" {
				// The recursive listing (-r) is test-specific; delegate to fn.
				if hasArg(args, "-r") && fn != nil {
					return fn(cmd, args)
				}
				return nil, nil // existence check: exists
			}
		}
		if fn != nil {
			return fn(cmd, args)
		}
		return nil, nil
	}}
}

func TestListVolumesFull(t *testing.T) {
	mp := t.TempDir()
	fake := volumeZFSFake(mp, func(cmd string, args []string) ([]byte, error) {
		if cmd == "zfs" && len(args) >= 2 && args[0] == "list" && hasArg(args, "-r") {
			return []byte("zroot/hospitus/volumes\nzroot/hospitus/volumes/data\nzroot/hospitus/volumes/logs\n"), nil
		}
		return nil, nil
	})
	p := &JailProvider{runner: fake, stateDir: t.TempDir(), zfsParent: "zroot/hospitus/jails"}

	vols, err := p.ListVolumes(context.Background())
	if err != nil {
		t.Fatalf("ListVolumes err = %v", err)
	}
	if len(vols) != 2 {
		t.Fatalf("got %d volumes, want 2: %+v", len(vols), vols)
	}
}

func TestDeleteVolume(t *testing.T) {
	mp := t.TempDir()
	fake := volumeZFSFake(mp, nil)
	backend := &recordingStorage{mountpoint: mp}
	p := &JailProvider{runner: fake, stateDir: t.TempDir(), zfsParent: "zroot/hospitus/jails", storageBackend: backend}
	if err := p.DeleteVolume(context.Background(), "data", false); err != nil {
		t.Fatalf("DeleteVolume err = %v", err)
	}
	if len(backend.deleted) != 1 || backend.deleted[0].Name != "data" {
		t.Errorf("expected the backend to be asked to delete \"data\", got %+v", backend.deleted)
	}
}

func TestMountVolumeToJailStopped(t *testing.T) {
	stateDir := t.TempDir()
	jailRoot := t.TempDir()
	volMount := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	if err := p.saveJailConfig(&jailConfig{Name: "web", Path: jailRoot}, filepath.Join(stateDir, "web.json")); err != nil {
		t.Fatal(err)
	}
	p.runner = volumeZFSFake(volMount, nil)

	if err := p.MountVolumeToJail(context.Background(), "data", "web", "/mnt/data", false); err != nil {
		t.Fatalf("MountVolumeToJail err = %v", err)
	}
	// Jail stopped -> no mount command, but fstab entry written.
	if hasCmd(p.runner.(*execx.Fake), "mount -t nullfs -o rw "+volMount+" "+filepath.Join(jailRoot, "mnt/data")) {
		t.Error("mount should be skipped for stopped jail")
	}
}

func TestMountVolumeInvalidMountPath(t *testing.T) {
	p := &JailProvider{stateDir: t.TempDir(), zfsParent: "zroot/hospitus/jails", runner: &execx.Fake{}}
	if err := p.MountVolumeToJail(context.Background(), "data", "web", "relative/path", false); err == nil {
		t.Fatal("expected error for non-absolute mount path")
	}
}

func TestCloneVolume(t *testing.T) {
	mp := t.TempDir()
	fake := volumeZFSFake(mp, nil)
	p := &JailProvider{runner: fake, stateDir: t.TempDir(), zfsParent: "zroot/hospitus/jails"}

	_, err := p.CloneVolume(context.Background(), "data", "snap1", "data-copy")
	if err != nil {
		t.Fatalf("CloneVolume err = %v", err)
	}
	if want := "zfs clone zroot/hospitus/volumes/data@snap1 zroot/hospitus/volumes/data-copy"; !hasCmd(fake, want) {
		t.Errorf("missing clone %q; got %+v", want, fake.Calls)
	}
}

func hasArg(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
