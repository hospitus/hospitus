package jail

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestCloneInstanceLinked(t *testing.T) {
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	// Persist the source jail config so GetInstanceInfo works.
	srcCfg := &jailConfig{Name: "web", Path: "/zroot/hospitus/jails/web/root"}
	srcCfg.Spec = provider.InstanceSpec{Name: "web", CPUs: 1}
	if err := p.saveJailConfig(srcCfg, filepath.Join(stateDir, "web.json")); err != nil {
		t.Fatal(err)
	}

	fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		if cmd == "jls" {
			return nil, errAssertStopped // source stopped -> clone allowed
		}
		if cmd == "zfs" && len(args) >= 1 && args[0] == "get" {
			return []byte("/zroot/hospitus/jails/clone1\n"), nil
		}
		return nil, nil // snapshot, clone
	}}
	p.runner = fake

	source := provider.InstanceHandle{ID: "web", Metadata: map[string]interface{}{"zfs_dataset": "zroot/hospitus/jails/web"}}
	handle, err := p.CloneInstance(context.Background(), source, "clone1", provider.CloneOptions{LinkedClone: true})
	if err != nil {
		t.Fatalf("CloneInstance err = %v", err)
	}
	if handle.ID != "clone1" {
		t.Errorf("handle ID = %q, want clone1", handle.ID)
	}
	if !hasCmd(fake, "zfs clone") && !anyCloneCmd(fake) {
		t.Error("expected a zfs clone command")
	}
	// Clone config must be persisted.
	if _, err := p.loadJailConfig(filepath.Join(stateDir, "clone1.json")); err != nil {
		t.Errorf("expected clone config persisted: %v", err)
	}
}

func anyCloneCmd(f *execx.Fake) bool {
	for _, c := range f.Calls {
		if c.Name == "zfs" && len(c.Args) >= 1 && c.Args[0] == "clone" {
			return true
		}
	}
	return false
}

func TestCloneInstanceRunningSourceRejected(t *testing.T) {
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	if err := p.saveJailConfig(&jailConfig{Name: "web"}, filepath.Join(stateDir, "web.json")); err != nil {
		t.Fatal(err)
	}
	// jls succeeds -> source running -> clone must be rejected.
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p.runner = fake
	source := provider.InstanceHandle{ID: "web", Metadata: map[string]interface{}{"zfs_dataset": "zroot/hospitus/jails/web"}}
	if _, err := p.CloneInstance(context.Background(), source, "clone1", provider.CloneOptions{LinkedClone: true}); err == nil {
		t.Fatal("expected error cloning a running source")
	}
}

func TestCloneFromSnapshot(t *testing.T) {
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	// Persist original jail config (needed to copy its spec).
	orig := &jailConfig{Name: "web", Path: "/zroot/hospitus/jails/web/root"}
	orig.Spec = provider.InstanceSpec{Name: "web"}
	if err := p.saveJailConfig(orig, filepath.Join(stateDir, "web.json")); err != nil {
		t.Fatal(err)
	}

	fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		if cmd == "jls" {
			return nil, errAssertStopped
		}
		if cmd == "zfs" && len(args) >= 1 && args[0] == "get" {
			return []byte("/zroot/hospitus/jails/fromsnap\n"), nil
		}
		return nil, nil
	}}
	p.runner = fake

	snap := provider.SnapshotHandle{
		Instance: "web",
		Metadata: map[string]interface{}{"zfs_name": "zroot/hospitus/jails/web@golden"},
	}
	handle, err := p.CloneFromSnapshot(context.Background(), snap, "fromsnap", provider.CloneOptions{})
	if err != nil {
		t.Fatalf("CloneFromSnapshot err = %v", err)
	}
	if handle.ID != "fromsnap" {
		t.Errorf("handle ID = %q", handle.ID)
	}
	if want := "zfs clone zroot/hospitus/jails/web@golden zroot/hospitus/jails/fromsnap"; !hasCmd(fake, want) {
		t.Errorf("missing clone %q; got %+v", want, fake.Calls)
	}
}
