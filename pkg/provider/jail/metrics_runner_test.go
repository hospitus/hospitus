package jail

import (
	"context"
	"errors"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func metricsFake() *execx.Fake {
	return &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		switch name {
		case "jls":
			// -N => isJailRunning (running); -n created => uptime
			for _, a := range args {
				if a == "created" {
					return []byte("created=1000\n"), nil
				}
			}
			return nil, nil
		case "sysctl":
			return []byte("1\n"), nil
		case "rctl":
			if len(args) > 0 && args[0] == "-u" {
				return []byte("pcpu=25\ncputime=100\nmemoryuse=1048576\nnthr=5\n"), nil
			}
			// rctl -l
			return []byte("jail:web:memoryuse:deny=536870912\njail:web:maxproc:deny=1000\n"), nil
		case "jexec":
			if len(args) >= 2 && args[1] == "ps" {
				return []byte("1\n2\n3\n"), nil
			}
			if len(args) >= 2 && args[1] == "netstat" {
				return []byte("Name Mtu Net Addr Ipkts Ierrs Idrop Ibytes Opkts Oerrs Obytes Coll\n" +
					"epair0b 1500 <L> - 10 0 0 5000 20 0 8000 0\n"), nil
			}
		case "zfs":
			return []byte("1048576\n"), nil
		}
		return nil, nil
	}}
}

func TestGetJailMetricsRunning(t *testing.T) {
	p := &JailProvider{runner: metricsFake(), zfsParent: "zroot/hospitus/jails"}
	m, err := p.GetJailMetrics(context.Background(), "web")
	if err != nil {
		t.Fatalf("GetJailMetrics err = %v", err)
	}
	if m.State != "running" {
		t.Fatalf("state = %q, want running", m.State)
	}
	if m.CPUPercent != 25 {
		t.Errorf("CPUPercent = %v, want 25", m.CPUPercent)
	}
	if m.MemoryUsed != 1048576 {
		t.Errorf("MemoryUsed = %d, want 1048576", m.MemoryUsed)
	}
	if m.MemoryLimit != 536870912 {
		t.Errorf("MemoryLimit = %d, want 536870912", m.MemoryLimit)
	}
	if m.ProcessCount != 3 {
		t.Errorf("ProcessCount = %d, want 3", m.ProcessCount)
	}
	if m.NetworkRxBytes != 5000 || m.NetworkTxBytes != 8000 {
		t.Errorf("net rx/tx = %d/%d, want 5000/8000", m.NetworkRxBytes, m.NetworkTxBytes)
	}
}

func TestGetJailMetricsStopped(t *testing.T) {
	// jls -N fails -> not running; only storage metrics collected.
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		if name == "jls" {
			return nil, errors.New("not running")
		}
		if name == "zfs" {
			return []byte("2048\n"), nil
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}
	m, err := p.GetJailMetrics(context.Background(), "web")
	if err != nil {
		t.Fatalf("GetJailMetrics err = %v", err)
	}
	if m.State != "stopped" {
		t.Errorf("state = %q, want stopped", m.State)
	}
	if m.DiskUsed != 2048 {
		t.Errorf("DiskUsed = %d, want 2048", m.DiskUsed)
	}
}

func TestGetStats(t *testing.T) {
	p := &JailProvider{runner: metricsFake(), zfsParent: "zroot/hospitus/jails"}
	stats, err := p.GetStats(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("GetStats err = %v", err)
	}
	if stats["state"] != "running" {
		t.Errorf("state = %v, want running", stats["state"])
	}
	if stats["process_count"] != 3 {
		t.Errorf("process_count = %v, want 3", stats["process_count"])
	}
}

func TestGetJailUptime(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("created=100\n"), nil
	}}
	p := &JailProvider{runner: fake}
	if up := p.getJailUptime(context.Background(), "web"); up <= 0 {
		t.Errorf("uptime = %d, want positive", up)
	}
}

func TestGetJailUptimeBadOutput(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return []byte("garbage"), nil }}
	p := &JailProvider{runner: fake}
	if up := p.getJailUptime(context.Background(), "web"); up != 0 {
		t.Errorf("uptime = %d, want 0 for bad output", up)
	}
}
