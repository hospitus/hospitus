package jail

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestCmdAccessorNilSafe(t *testing.T) {
	// A bare struct literal (no runner) must not panic when cmd() is used.
	p := &JailProvider{}
	if p.cmd() == nil {
		t.Fatal("cmd() returned nil runner")
	}
}

func TestShutdown(t *testing.T) {
	p := &JailProvider{}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown err = %v", err)
	}
}

func TestHealthCheckNonFreeBSD(t *testing.T) {
	if runtime.GOOS == "freebsd" {
		t.Skip("test asserts non-FreeBSD behavior")
	}
	p := &JailProvider{}
	if err := p.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected ErrProviderNotAvailable on non-FreeBSD host")
	}
}

func TestAttachDiskStoppedJail(t *testing.T) {
	stateDir := t.TempDir()
	jailRoot := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	if err := p.saveJailConfig(&jailConfig{Name: "web", Path: jailRoot}, filepath.Join(stateDir, "web.json")); err != nil {
		t.Fatal(err)
	}
	// jls fails -> jail stopped -> no mount command, only fstab entry written.
	fake := &execx.Fake{Func: func(cmd string, _ []string) ([]byte, error) {
		if cmd == "jls" {
			return nil, errAssertStopped
		}
		return nil, nil
	}}
	p.runner = fake

	disk := provider.DiskAttachment{
		MountPoint: "/data",
		Disk:       provider.DiskSpec{ID: "d1", Path: "/tank/data"},
	}
	if err := p.AttachDisk(context.Background(), provider.InstanceHandle{ID: "web"}, disk); err != nil {
		t.Fatalf("AttachDisk err = %v", err)
	}
	// No mount should have been issued (jail stopped).
	if hasCmd(fake, "mount -t nullfs -o rw /tank/data "+filepath.Join(jailRoot, "data")) {
		t.Error("mount should be skipped for a stopped jail")
	}
	// fstab entry should exist.
	fstab := filepath.Join(stateDir, "fstab", "web")
	if _, err := os.ReadFile(fstab); err != nil {
		t.Errorf("expected fstab file at %s: %v", fstab, err)
	}
}

func TestAttachDiskRunningJail(t *testing.T) {
	stateDir := t.TempDir()
	jailRoot := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	if err := p.saveJailConfig(&jailConfig{Name: "web", Path: jailRoot}, filepath.Join(stateDir, "web.json")); err != nil {
		t.Fatal(err)
	}
	fake := &execx.Fake{Func: func(cmd string, _ []string) ([]byte, error) { return nil, nil }} // jls ok (running), mount ok
	p.runner = fake

	disk := provider.DiskAttachment{
		MountPoint: "/data",
		Disk:       provider.DiskSpec{ID: "d1", Path: "/tank/data", ReadOnly: true},
	}
	if err := p.AttachDisk(context.Background(), provider.InstanceHandle{ID: "web"}, disk); err != nil {
		t.Fatalf("AttachDisk err = %v", err)
	}
	want := "mount -t nullfs -o ro /tank/data " + filepath.Join(jailRoot, "data")
	if !hasCmd(fake, want) {
		t.Errorf("missing mount %q; got calls %+v", want, fake.Calls)
	}
}

func TestStartAutoStartInstancesAllRunning(t *testing.T) {
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	cfg := &jailConfig{Name: "web"}
	cfg.AutoStart.Enabled = true
	if err := p.saveJailConfig(cfg, filepath.Join(stateDir, "web.json")); err != nil {
		t.Fatal(err)
	}
	// jls succeeds -> already running -> StartInstance skipped.
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p.runner = fake
	if err := p.StartAutoStartInstances(context.Background()); err != nil {
		t.Fatalf("StartAutoStartInstances err = %v", err)
	}
}

func TestStartAutoStartInstancesNone(t *testing.T) {
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails", runner: &execx.Fake{}}
	// A jail without autostart enabled.
	if err := p.saveJailConfig(&jailConfig{Name: "web"}, filepath.Join(stateDir, "web.json")); err != nil {
		t.Fatal(err)
	}
	if err := p.StartAutoStartInstances(context.Background()); err != nil {
		t.Fatalf("err = %v", err)
	}
}
