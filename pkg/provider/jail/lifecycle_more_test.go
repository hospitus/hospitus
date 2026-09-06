package jail

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/config"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func nmNone(fake *execx.Fake) *NetworkManager {
	nm := NewNetworkManager(&config.Config{FirewallType: "none"}, nil)
	nm.runner = fake
	return nm
}

func TestDeleteInstanceStopped(t *testing.T) {
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	if err := p.saveJailConfig(&jailConfig{Name: "web", Path: t.TempDir()}, filepath.Join(stateDir, "web.json")); err != nil {
		t.Fatal(err)
	}
	fake := &execx.Fake{Func: func(cmd string, _ []string) ([]byte, error) {
		if cmd == "jls" {
			return nil, errors.New("stopped")
		}
		return nil, nil
	}}
	p.runner = fake
	p.networkManager = nmNone(fake)

	handle := provider.InstanceHandle{ID: "web", Metadata: map[string]interface{}{"zfs_dataset": "zroot/hospitus/jails/web"}}
	if err := p.DeleteInstance(context.Background(), handle, false); err != nil {
		t.Fatalf("DeleteInstance err = %v", err)
	}
	if want := "zfs destroy -r zroot/hospitus/jails/web"; !hasCmd(fake, want) {
		t.Errorf("missing destroy %q", want)
	}
	// Config file must be gone.
	if _, err := os.Stat(filepath.Join(stateDir, "web.json")); !os.IsNotExist(err) {
		t.Error("expected config file removed")
	}
}

func TestRenameInstance(t *testing.T) {
	stateDir := t.TempDir()
	jailRoot := filepath.Join(t.TempDir(), "jails", "web", "root")
	if err := os.MkdirAll(jailRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails", logger: slog.Default()}
	if err := p.saveJailConfig(&jailConfig{Name: "web", Path: jailRoot}, filepath.Join(stateDir, "web.json")); err != nil {
		t.Fatal(err)
	}
	fake := &execx.Fake{Func: func(cmd string, _ []string) ([]byte, error) {
		if cmd == "jls" {
			return nil, errors.New("stopped") // stopped: rename allowed
		}
		return nil, nil // zfs list/rename succeed
	}}
	p.runner = fake

	if err := p.RenameInstance(context.Background(), provider.InstanceHandle{ID: "web"}, "api"); err != nil {
		t.Fatalf("RenameInstance err = %v", err)
	}
	if want := "zfs rename zroot/hospitus/jails/web zroot/hospitus/jails/api"; !hasCmd(fake, want) {
		t.Errorf("missing rename %q", want)
	}
	// New config exists, old removed.
	if _, err := os.Stat(filepath.Join(stateDir, "api.json")); err != nil {
		t.Errorf("expected api.json to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "web.json")); !os.IsNotExist(err) {
		t.Error("expected web.json removed")
	}
}

// TestRenameInstanceRewritesTheJailPath covers the layout a jail actually has.
//
// A jail root is <parent>/<name>; the test above uses <parent>/<name>/root,
// which hospitus never creates, and that is why nothing caught this: the rewrite
// only handled the deeper layout, so for a real jail the path was left naming
// the dataset's former mountpoint. The rename reported success, and starting
// the jail afterwards failed with
//
//	jail: exec /usr/bin/true: No such file or directory
//
// because it had been given an empty directory as its root.
func TestRenameInstanceRewritesTheJailPath(t *testing.T) {
	stateDir := t.TempDir()
	parent := t.TempDir()
	jailRoot := filepath.Join(parent, "web")
	if err := os.MkdirAll(jailRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails", logger: slog.Default()}
	if err := p.saveJailConfig(&jailConfig{Name: "web", Path: jailRoot}, filepath.Join(stateDir, "web.json")); err != nil {
		t.Fatal(err)
	}
	p.runner = &execx.Fake{Func: func(cmd string, _ []string) ([]byte, error) {
		if cmd == "jls" {
			return nil, errors.New("stopped")
		}
		return nil, nil
	}}

	if err := p.RenameInstance(context.Background(), provider.InstanceHandle{ID: "web"}, "api"); err != nil {
		t.Fatalf("RenameInstance err = %v", err)
	}

	cfg, err := p.loadJailConfig(filepath.Join(stateDir, "api.json"))
	if err != nil {
		t.Fatalf("loadJailConfig: %v", err)
	}
	if want := filepath.Join(parent, "api"); cfg.Path != want {
		t.Errorf("path after rename = %q, want %q", cfg.Path, want)
	}
}

func TestRemoveNetworkInterface(t *testing.T) {
	p, fake := runningProvider(t, "web", nil)
	if err := p.RemoveNetworkInterface(context.Background(), provider.InstanceHandle{ID: "web"}, "epair3b"); err != nil {
		t.Fatalf("RemoveNetworkInterface err = %v", err)
	}
	if want := "ifconfig epair3b -vnet web"; !hasCmd(fake, want) {
		t.Errorf("missing move-out %q", want)
	}
	if want := "ifconfig epair3a destroy"; !hasCmd(fake, want) {
		t.Errorf("missing destroy %q", want)
	}
}

func TestEnsureJailDirectories(t *testing.T) {
	p := &JailProvider{}
	jailPath := t.TempDir()
	if err := p.ensureJailDirectories(jailPath); err != nil {
		t.Fatalf("ensureJailDirectories err = %v", err)
	}
	for _, d := range []string{"dev", "tmp", "var/run"} {
		if _, err := os.Stat(filepath.Join(jailPath, d)); err != nil {
			t.Errorf("expected directory %s: %v", d, err)
		}
	}
}

func TestDetachDiskStopped(t *testing.T) {
	stateDir := t.TempDir()
	jailRoot := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	if err := p.saveJailConfig(&jailConfig{Name: "web", Path: jailRoot}, filepath.Join(stateDir, "web.json")); err != nil {
		t.Fatal(err)
	}
	// Seed an fstab entry for disk "d1".
	fstabPath := filepath.Join(stateDir, "fstab", "web")
	if err := p.addToJailFstab(fstabPath, "/tank/data", "/data", false); err != nil {
		t.Fatal(err)
	}
	// addToJailFstab keys by source path; DetachDisk looks up by diskID prefix.
	// Rewrite the fstab so the first field is the disk ID as DetachDisk expects.
	if err := os.WriteFile(fstabPath, []byte("d1\t/data\tnullfs\trw\t0\t0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &execx.Fake{Func: func(cmd string, _ []string) ([]byte, error) {
		if cmd == "jls" {
			return nil, errors.New("stopped")
		}
		return nil, nil
	}}
	p.runner = fake

	if err := p.DetachDisk(context.Background(), provider.InstanceHandle{ID: "web"}, "d1"); err != nil {
		t.Fatalf("DetachDisk err = %v", err)
	}
}
