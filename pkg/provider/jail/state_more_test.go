package jail

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestGetInstanceStateRunning(t *testing.T) {
	p, _ := runningProvider(t, "web", nil)
	state, err := p.GetInstanceState(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("GetInstanceState err = %v", err)
	}
	if state != provider.StateRunning {
		t.Errorf("state = %v, want running", state)
	}
}

func TestGetInstanceStateNotFound(t *testing.T) {
	p := &JailProvider{stateDir: t.TempDir(), runner: &execx.Fake{}}
	if _, err := p.GetInstanceState(context.Background(), provider.InstanceHandle{ID: "ghost"}); err == nil {
		t.Fatal("expected ErrInstanceNotFound")
	}
}

func TestGetInstanceInfoRunning(t *testing.T) {
	// The running helper reports jid 1 for all jls queries.
	p, _ := runningProvider(t, "web", nil)
	info, err := p.GetInstanceInfo(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("GetInstanceInfo err = %v", err)
	}
	if info.State != provider.StateRunning {
		t.Errorf("state = %v, want running", info.State)
	}
	if info.PID != 1 {
		t.Errorf("PID = %d, want 1", info.PID)
	}
}

func TestListInstances(t *testing.T) {
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails", runner: &execx.Fake{}}
	for _, n := range []string{"web", "db"} {
		if err := p.saveJailConfig(&jailConfig{Name: n}, filepath.Join(stateDir, n+".json")); err != nil {
			t.Fatal(err)
		}
	}
	handles, err := p.ListInstances(context.Background(), provider.InstanceFilter{})
	if err != nil {
		t.Fatalf("ListInstances err = %v", err)
	}
	if len(handles) != 2 {
		t.Errorf("got %d instances, want 2", len(handles))
	}
}

func TestGetInstanceMetricsStopped(t *testing.T) {
	// jls -N fails -> not running -> empty metrics with timestamp.
	p, _ := runningProviderStopped(t, "web")
	m, err := p.GetInstanceMetrics(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("GetInstanceMetrics err = %v", err)
	}
	if m.Timestamp.IsZero() {
		t.Error("expected timestamp to be set")
	}
}

func TestGetInstanceMetricsRunning(t *testing.T) {
	p, _ := runningProvider(t, "web", func(cmd string, args []string) ([]byte, error) {
		if cmd == "rctl" {
			return []byte("wallclock=100\ncputime=50\nmemoryuse=1048576\n"), nil
		}
		if cmd == "netstat" {
			return []byte("Name Mtu Net Addr Ipkts Ierrs Idrop Ibytes Opkts Oerrs Obytes Coll\n" +
				"epair0a 1500 <L> - 1 0 0 4096 2 0 8192 0\n"), nil
		}
		return nil, nil
	})
	handle := provider.InstanceHandle{ID: "web", Metadata: map[string]interface{}{"vnet_epair": "epair0a"}}
	m, err := p.GetInstanceMetrics(context.Background(), handle)
	if err != nil {
		t.Fatalf("GetInstanceMetrics err = %v", err)
	}
	if m.MemoryUsedMB != 1 {
		t.Errorf("MemoryUsedMB = %d, want 1", m.MemoryUsedMB)
	}
	if m.CPUUsagePercent != 50 {
		t.Errorf("CPUUsagePercent = %v, want 50", m.CPUUsagePercent)
	}
	if m.NetRxBytes != 4096 || m.NetTxBytes != 8192 {
		t.Errorf("net rx/tx = %d/%d, want 4096/8192", m.NetRxBytes, m.NetTxBytes)
	}
}

func TestSetInstanceResources(t *testing.T) {
	// Jail stopped -> RCTL not applied, just persists config.
	p, _ := runningProviderStopped(t, "web")
	err := p.SetInstanceResources(context.Background(), provider.InstanceHandle{ID: "web"}, provider.ResourceSpec{CPUs: 2, MemoryMB: 256})
	if err != nil {
		t.Fatalf("SetInstanceResources err = %v", err)
	}
	// Reload config and verify persisted.
	cfg, err := p.loadJailConfig(filepath.Join(p.stateDir, "web.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Resources.MemoryMB != 256 {
		t.Errorf("persisted MemoryMB = %d, want 256", cfg.Resources.MemoryMB)
	}
}
