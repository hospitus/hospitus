package bhyve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// healthProvider builds a provider whose data directory holds one VM in the
// given state. A nil state leaves the directory without a vm.state file, which
// is how a VM whose metadata was lost presents itself.
func healthProvider(t *testing.T, vmName string, state *vmState) *BhyveProvider {
	t.Helper()

	p := &BhyveProvider{dataDir: t.TempDir()}
	vmDir := filepath.Join(p.dataDir, vmName)
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatalf("create vm dir: %v", err)
	}
	if state != nil {
		if err := p.saveVMState(vmDir, state); err != nil {
			t.Fatalf("saveVMState: %v", err)
		}
	}
	return p
}

// findCheck returns the named check, failing the test when the health report
// does not carry one.
func findCheck(t *testing.T, health *provider.InstanceHealth, name string) provider.HealthCheck {
	t.Helper()

	for _, c := range health.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("health report has no %q check; got %+v", name, health.Checks)
	return provider.HealthCheck{}
}

// TestCheckInstanceHealthWithoutState covers a VM directory whose vm.state file
// is missing. The provider cannot tell whether the VM is running, so it has to
// report that rather than guess.
func TestCheckInstanceHealthWithoutState(t *testing.T) {
	p := healthProvider(t, "web", nil)

	health, err := p.CheckInstanceHealth(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("CheckInstanceHealth: %v", err)
	}
	if health.Status != provider.HealthStatusUnknown {
		t.Errorf("status = %v, want unknown", health.Status)
	}
	if check := findCheck(t, health, "vm_state"); check.Status != provider.HealthStatusUnknown {
		t.Errorf("vm_state check = %v, want unknown", check.Status)
	}
}

// TestCheckInstanceHealthStopped covers a VM recorded as stopped: unhealthy,
// and the report stops there instead of looking for a process.
func TestCheckInstanceHealthStopped(t *testing.T) {
	p := healthProvider(t, "web", &vmState{
		Name:  "web",
		CPUs:  2,
		State: provider.StateStopped,
	})

	health, err := p.CheckInstanceHealth(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("CheckInstanceHealth: %v", err)
	}
	if health.Status != provider.HealthStatusUnhealthy {
		t.Errorf("status = %v, want unhealthy", health.Status)
	}
	if !strings.Contains(health.Message, "not running") {
		t.Errorf("message = %q, want it to say the VM is not running", health.Message)
	}
	for _, c := range health.Checks {
		if c.Name == "process_alive" {
			t.Error("a stopped VM was checked for a live process")
		}
	}
}

// TestCheckInstanceHealthRunningWithoutPID covers the state a VM lands in when
// bhyve died without the provider noticing: recorded as running, no PID. The
// process check has to call that unhealthy instead of trusting the record.
func TestCheckInstanceHealthRunningWithoutPID(t *testing.T) {
	p := healthProvider(t, "web", &vmState{
		Name:  "web",
		CPUs:  2,
		State: provider.StateRunning,
		PID:   0,
	})

	health, err := p.CheckInstanceHealth(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("CheckInstanceHealth: %v", err)
	}

	check := findCheck(t, health, "process_alive")
	if check.Status != provider.HealthStatusUnhealthy {
		t.Errorf("process_alive = %v, want unhealthy", check.Status)
	}
	if !strings.Contains(check.Message, "no PID") {
		t.Errorf("process_alive message = %q, want it to name the missing PID", check.Message)
	}
}
