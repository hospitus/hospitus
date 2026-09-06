package bhyve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// metricsProvider persists a "web" VM with the given PID and tap device, backed
// by the supplied fake runner.
func metricsProvider(t *testing.T, fake *execx.Fake, pid int) *BhyveProvider {
	t.Helper()
	dir := t.TempDir()
	p := &BhyveProvider{dataDir: dir, runner: fake}
	vmDir := filepath.Join(dir, "web")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &vmConfig{
		Name:      "web",
		MemoryMB:  2048,
		DiskPaths: []string{"/dev/zvol/zroot/hospitus/bhyve/web/disk0"},
		TapDevs:   []string{"tap0"},
	}
	if err := p.saveVMConfig(vmDir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := p.saveVMState(vmDir, &vmState{Name: "web", State: provider.StateRunning, PID: pid}); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGetCPUUsageMatchesRecordedPID(t *testing.T) {
	// Arrange
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("  PID %CPU COMMAND\n4242 37.5 bhyve: web\n999 1.0 other\n"), nil
	}}
	p := metricsProvider(t, fake, 4242)

	// Act
	cpu, err := p.getCPUUsage(context.Background(), "web")
	// Assert
	if err != nil {
		t.Fatalf("getCPUUsage: %v", err)
	}
	if cpu != 37.5 {
		t.Errorf("cpu = %v, want 37.5", cpu)
	}
}

func TestGetCPUUsageNoPIDRecorded(t *testing.T) {
	p := metricsProvider(t, &execx.Fake{}, 0)
	if _, err := p.getCPUUsage(context.Background(), "web"); err == nil {
		t.Error("expected error when no PID is recorded")
	}
}

func TestGetCPUUsagePSError(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}}
	p := metricsProvider(t, fake, 4242)
	if _, err := p.getCPUUsage(context.Background(), "web"); err == nil {
		t.Error("expected error when ps fails")
	}
}

func TestGetCPUUsageProcessNotFound(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("  PID %CPU COMMAND\n999 1.0 other\n"), nil
	}}
	p := metricsProvider(t, fake, 4242)
	if _, err := p.getCPUUsage(context.Background(), "web"); err == nil {
		t.Error("expected error when the recorded PID is absent from ps output")
	}
}

func TestGetProcessInfoParsesState(t *testing.T) {
	// Arrange
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("  PID STATE TIME COMMAND\n4242 R 01:02:03 bhyve: web\n"), nil
	}}
	p := metricsProvider(t, fake, 4242)

	// Act
	pid, state, cpuTime, err := p.getProcessInfo(context.Background(), "web")
	// Assert
	if err != nil {
		t.Fatalf("getProcessInfo: %v", err)
	}
	if pid != 4242 || state != "R" {
		t.Errorf("pid/state = %d/%q, want 4242/R", pid, state)
	}
	if cpuTime != 3723 { // 1h 2m 3s
		t.Errorf("cpuTime = %v, want 3723", cpuTime)
	}
}

func TestGetProcessInfoNotFound(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("  PID STATE TIME COMMAND\n1 S 00:00 init\n"), nil
	}}
	p := metricsProvider(t, fake, 4242)
	if _, _, _, err := p.getProcessInfo(context.Background(), "web"); err == nil {
		t.Error("expected error when the recorded PID is absent")
	}
}

func TestParseTimeToSeconds(t *testing.T) {
	cases := map[string]float64{
		"01:30":    90,   // MM:SS
		"02:00:00": 7200, // HH:MM:SS
		"00:00:05": 5,    // HH:MM:SS
		"garbage":  0,    // unparseable
		"1:2:3:4":  0,    // too many parts
	}
	for in, want := range cases {
		if got := parseTimeToSeconds(in); got != want {
			t.Errorf("parseTimeToSeconds(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestGetDiskIOAndMetricsAreZero(t *testing.T) {
	p := &BhyveProvider{runner: &execx.Fake{}}
	r, w, err := p.getDiskIO(context.Background(), []string{"/dev/zvol/pool/web"})
	if err != nil || r != 0 || w != 0 {
		t.Errorf("getDiskIO = (%d,%d,%v), want (0,0,nil)", r, w, err)
	}
	dm, err := p.getDiskMetrics(context.Background(), "/dev/zvol/pool/web")
	if err != nil || dm.Path != "/dev/zvol/pool/web" || dm.ReadBytes != 0 {
		t.Errorf("getDiskMetrics = %+v (%v), want path-only", dm, err)
	}
}

func TestGetInstanceMetricsAggregates(t *testing.T) {
	// Arrange: ps reports CPU for the VM pid, netstat reports interface bytes.
	fake := &execx.Fake{Func: func(name string, _ []string) ([]byte, error) {
		if name == "netstat" {
			return []byte(netstatSample), nil
		}
		return []byte("  PID %CPU COMMAND\n4242 12.0 bhyve: web\n"), nil
	}}
	p := metricsProvider(t, fake, 4242)

	// Act
	m, err := p.GetInstanceMetrics(context.Background(), provider.InstanceHandle{ID: "web"})
	// Assert
	if err != nil {
		t.Fatalf("GetInstanceMetrics: %v", err)
	}
	if m.MemoryTotalMB != 2048 || m.MemoryUsedMB != 2048 {
		t.Errorf("memory = %d/%d, want 2048/2048", m.MemoryUsedMB, m.MemoryTotalMB)
	}
	if m.CPUUsagePercent != 12.0 {
		t.Errorf("cpu = %v, want 12.0", m.CPUUsagePercent)
	}
	if m.NetRxBytes != 500000 {
		t.Errorf("net rx = %d, want 500000 (Ibytes)", m.NetRxBytes)
	}
}

func TestGetInstanceMetricsMissingConfig(t *testing.T) {
	p := &BhyveProvider{dataDir: t.TempDir(), runner: &execx.Fake{}}
	if _, err := p.GetInstanceMetrics(context.Background(), provider.InstanceHandle{ID: "ghost"}); err == nil {
		t.Error("expected error when the VM config is missing")
	}
}

func TestGetDetailedMetricsPopulatesInterfaces(t *testing.T) {
	// Arrange
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		if name == "netstat" {
			return []byte(netstatSample), nil
		}
		// Both the pcpu and the state ps forms match the recorded pid.
		return []byte("4242 5.0 R 00:30 bhyve: web\n"), nil
	}}
	p := metricsProvider(t, fake, 4242)

	// Act
	info, err := p.GetDetailedMetrics(context.Background(), provider.InstanceHandle{ID: "web"})
	// Assert
	if err != nil {
		t.Fatalf("GetDetailedMetrics: %v", err)
	}
	if len(info.Interfaces) != 1 || info.Interfaces[0].Name != "tap0" {
		t.Fatalf("interfaces = %+v, want one tap0 entry", info.Interfaces)
	}
	if len(info.Disks) != 1 {
		t.Errorf("expected 1 disk entry, got %d", len(info.Disks))
	}
}

func TestGetDiskIOPropagatesNothing(t *testing.T) {
	// getDiskIO is intentionally a no-op; an error from the runner must not
	// surface because it is never called.
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return nil, errors.New("should not be called")
	}}
	p := &BhyveProvider{runner: fake}
	if _, _, err := p.getDiskIO(context.Background(), []string{"/dev/zvol/pool/web"}); err != nil {
		t.Errorf("getDiskIO returned %v, want nil", err)
	}
	if fake.CallCount() != 0 {
		t.Errorf("getDiskIO ran %d commands, want 0", fake.CallCount())
	}
}
