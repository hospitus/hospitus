package bhyve

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func autostartProvider(t *testing.T, vmNames ...string) *BhyveProvider {
	t.Helper()
	dir := t.TempDir()
	p := &BhyveProvider{dataDir: dir, stateDir: dir, runner: &execx.Fake{}}
	for _, name := range vmNames {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestSetAndGetAutoStart(t *testing.T) {
	// Arrange
	p := autostartProvider(t, "web")
	h := provider.InstanceHandle{ID: "web"}

	// Act
	err := p.SetAutoStart(context.Background(), h, provider.AutoStartConfig{Enabled: true, Priority: 10, DelayMS: 2000})
	if err != nil {
		t.Fatalf("SetAutoStart: %v", err)
	}

	// Assert
	got, err := p.GetAutoStart(context.Background(), h)
	if err != nil {
		t.Fatalf("GetAutoStart: %v", err)
	}
	if !got.Enabled || got.Priority != 10 || got.DelayMS != 2000 {
		t.Errorf("autostart config = %+v, want enabled/10/2000", got)
	}
}

func TestSetAutoStartUnknownVM(t *testing.T) {
	p := autostartProvider(t)
	if err := p.SetAutoStart(context.Background(), provider.InstanceHandle{ID: "ghost"}, provider.AutoStartConfig{}); err == nil {
		t.Error("expected error for a non-existent VM")
	}
}

func TestSetAutoStartRejectsBadPriority(t *testing.T) {
	p := autostartProvider(t, "web")
	if err := p.SetAutoStart(context.Background(), provider.InstanceHandle{ID: "web"}, provider.AutoStartConfig{Priority: 200}); err == nil {
		t.Error("expected error for out-of-range priority")
	}
}

func TestSetAutoStartAppliesDefaultPriority(t *testing.T) {
	p := autostartProvider(t, "web")
	h := provider.InstanceHandle{ID: "web"}
	if err := p.SetAutoStart(context.Background(), h, provider.AutoStartConfig{Enabled: true}); err != nil {
		t.Fatalf("SetAutoStart: %v", err)
	}
	got, err := p.GetAutoStart(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if got.Priority != 50 {
		t.Errorf("default priority = %d, want 50", got.Priority)
	}
}

func TestGetAutoStartDefaultsWhenUnconfigured(t *testing.T) {
	p := autostartProvider(t, "web")
	got, err := p.GetAutoStart(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("GetAutoStart: %v", err)
	}
	if got.Enabled {
		t.Errorf("unconfigured VM should report Enabled=false, got %+v", got)
	}
}

func TestListAutoStartInstancesSortedByPriority(t *testing.T) {
	// Arrange: three VMs; two auto-start with different priorities, one disabled.
	p := autostartProvider(t, "alpha", "beta", "gamma")
	ctx := context.Background()
	if err := p.SetAutoStart(ctx, provider.InstanceHandle{ID: "alpha"}, provider.AutoStartConfig{Enabled: true, Priority: 30}); err != nil {
		t.Fatal(err)
	}
	if err := p.SetAutoStart(ctx, provider.InstanceHandle{ID: "beta"}, provider.AutoStartConfig{Enabled: true, Priority: 10}); err != nil {
		t.Fatal(err)
	}
	if err := p.SetAutoStart(ctx, provider.InstanceHandle{ID: "gamma"}, provider.AutoStartConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}

	// Act
	handles, err := p.ListAutoStartInstances(ctx)
	if err != nil {
		t.Fatalf("ListAutoStartInstances: %v", err)
	}

	// Assert: only enabled VMs, ordered by ascending priority.
	if len(handles) != 2 {
		t.Fatalf("expected 2 auto-start VMs, got %d", len(handles))
	}
	if handles[0].ID != "beta" || handles[1].ID != "alpha" {
		t.Errorf("order = [%s %s], want [beta alpha]", handles[0].ID, handles[1].ID)
	}
}

func TestStartAutoStartInstancesNoneConfigured(t *testing.T) {
	p := autostartProvider(t, "web")
	if err := p.StartAutoStartInstances(context.Background()); err != nil {
		t.Errorf("StartAutoStartInstances with no enabled VMs = %v, want nil", err)
	}
}

func TestGetAutoStartInfo(t *testing.T) {
	// Arrange
	p := autostartProvider(t, "web")
	h := provider.InstanceHandle{ID: "web"}
	if err := p.SetAutoStart(context.Background(), h, provider.AutoStartConfig{Enabled: true, Priority: 20}); err != nil {
		t.Fatal(err)
	}
	// Persist a stopped state so GetInstanceState resolves cleanly.
	if err := p.saveVMState(filepath.Join(p.dataDir, "web"), &vmState{Name: "web", State: provider.StateStopped}); err != nil {
		t.Fatal(err)
	}

	// Act
	info, err := p.GetAutoStartInfo(context.Background(), h)
	// Assert
	if err != nil {
		t.Fatalf("GetAutoStartInfo: %v", err)
	}
	if info.VMName != "web" || !info.Enabled || info.Priority != 20 {
		t.Errorf("info = %+v, want web/enabled/20", info)
	}
	if info.State != string(provider.StateStopped) {
		t.Errorf("state = %q, want stopped", info.State)
	}
}
