package jail

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestGetRCTLUsage(t *testing.T) {
	out := "cputime=12\nmemoryuse=1048576\nwallclock=100\nbogus\nreadbps=notnum\n"
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return []byte(out), nil }}
	p := &JailProvider{runner: fake}

	stats, err := p.getRCTLUsage(context.Background(), "web")
	if err != nil {
		t.Fatalf("getRCTLUsage err = %v", err)
	}
	if stats["cputime"] != 12 || stats["memoryuse"] != 1048576 || stats["wallclock"] != 100 {
		t.Errorf("unexpected stats: %+v", stats)
	}
	if _, ok := stats["readbps"]; ok {
		t.Error("non-numeric value should be skipped")
	}
	if want := "rctl -u jail:web"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestGetRCTLUsageError(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, errors.New("no rules") }}
	p := &JailProvider{runner: fake}
	if _, err := p.getRCTLUsage(context.Background(), "web"); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetInterfaceStats(t *testing.T) {
	// netstat -bin output; epair0a Ibytes at idx 7, Obytes at idx 10.
	out := "Name Mtu Network Address Ipkts Ierrs Idrop Ibytes Opkts Oerrs Obytes Coll\n" +
		"epair0a 1500 <Link> 00:00 10 0 0 5000 20 0 8000 0\n"
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return []byte(out), nil }}
	p := &JailProvider{runner: fake}

	rx, tx := p.getInterfaceStats(context.Background(), "epair0a")
	if rx != 5000 || tx != 8000 {
		t.Errorf("rx,tx = %d,%d; want 5000,8000", rx, tx)
	}
	if want := "netstat -bin"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestGetInterfaceStatsCommandError(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, errors.New("fail") }}
	p := &JailProvider{runner: fake}
	rx, tx := p.getInterfaceStats(context.Background(), "epair0a")
	if rx != 0 || tx != 0 {
		t.Errorf("expected 0,0 on error, got %d,%d", rx, tx)
	}
}

func TestCheckInstanceHealthNotRunningRunner(t *testing.T) {
	// jls fails -> not running -> unhealthy, single check.
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, errors.New("exit 1") }}
	p := &JailProvider{runner: fake, stateDir: t.TempDir()}

	health, err := p.CheckInstanceHealth(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("CheckInstanceHealth err = %v", err)
	}
	if health.Status != provider.HealthStatusUnhealthy {
		t.Errorf("status = %v, want unhealthy", health.Status)
	}
}

func TestCheckInstanceHealthAllHealthyRunner(t *testing.T) {
	// Route each command: jls (running) ok, zfs ok, ifconfig ok, jexec ok.
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		return nil, nil // everything succeeds
	}}
	p := &JailProvider{runner: fake, stateDir: t.TempDir(), zfsParent: "zroot/hospitus/jails"}

	handle := provider.InstanceHandle{ID: "web", Metadata: map[string]interface{}{"vnet_epair": "epair0a"}}
	health, err := p.CheckInstanceHealth(context.Background(), handle)
	if err != nil {
		t.Fatalf("CheckInstanceHealth err = %v", err)
	}
	if health.Status != provider.HealthStatusHealthy {
		t.Errorf("status = %v, want healthy; checks=%+v", health.Status, health.Checks)
	}
	// The ZFS dataset check must use the configured parent.
	if want := "zfs list -H zroot/hospitus/jails/web"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
	if want := "ifconfig epair0a"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestCheckInstanceHealthDegradedRunner(t *testing.T) {
	// jls (running) ok, zfs ok, but ifconfig + jexec fail -> degraded.
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		if name == "ifconfig" || name == "jexec" {
			return nil, errors.New("fail")
		}
		if name == "jls" || name == "zfs" {
			return nil, nil
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake, stateDir: t.TempDir(), zfsParent: "zroot/hospitus/jails"}
	handle := provider.InstanceHandle{ID: "web", Metadata: map[string]interface{}{"vnet_epair": "epair0a"}}

	health, err := p.CheckInstanceHealth(context.Background(), handle)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if health.Status != provider.HealthStatusDegraded {
		t.Errorf("status = %v, want degraded", health.Status)
	}
	if !strings.Contains(health.Message, "degraded") {
		t.Errorf("message = %q, want it to mention degraded", health.Message)
	}
}
