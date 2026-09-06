package jail

import (
	"context"
	"errors"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestApplyRCTLLimitsDisabled(t *testing.T) {
	// sysctl returns "0" -> RACCT disabled -> hard error.
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return []byte("0\n"), nil }}
	p := &JailProvider{runner: fake}
	err := p.applyRCTLLimits(context.Background(), "web", provider.ResourceSpec{CPUs: 2})
	if err == nil {
		t.Fatal("expected error when RACCT disabled")
	}
}

func TestApplyRCTLLimitsAllResources(t *testing.T) {
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		if name == "sysctl" {
			return []byte("1\n"), nil
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}

	res := provider.ResourceSpec{
		CPUs:      2,
		MemoryMB:  512,
		ReadBPS:   1000,
		WriteBPS:  2000,
		ReadIOPS:  10,
		WriteIOPS: 20,
		MaxProc:   50,
	}
	if err := p.applyRCTLLimits(context.Background(), "web", res); err != nil {
		t.Fatalf("applyRCTLLimits err = %v", err)
	}

	want := []string{
		"rctl -a jail:web:pcpu:deny=200",
		"rctl -a jail:web:memoryuse:deny=536870912",
		"rctl -a jail:web:readbps:throttle=1000",
		"rctl -a jail:web:writebps:throttle=2000",
		"rctl -a jail:web:readiops:throttle=10",
		"rctl -a jail:web:writeiops:throttle=20",
		"rctl -a jail:web:maxproc:deny=50",
	}
	for _, w := range want {
		if !hasCmd(fake, w) {
			t.Errorf("missing command %q", w)
		}
	}
}

func TestApplyRCTLLimitsDefaultMaxproc(t *testing.T) {
	fake := &execx.Fake{Func: func(name string, _ []string) ([]byte, error) {
		if name == "sysctl" {
			return []byte("1"), nil
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}
	if err := p.applyRCTLLimits(context.Background(), "web", provider.ResourceSpec{}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if want := "rctl -a jail:web:maxproc:deny=1000"; !hasCmd(fake, want) {
		t.Errorf("missing default maxproc %q", want)
	}
}

func TestGetResourceLimits(t *testing.T) {
	out := "jail:web:memoryuse:deny=536870912\njail:web:pcpu:deny=200\n"
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return []byte(out), nil }}
	p := &JailProvider{runner: fake}

	limits, err := p.GetResourceLimits(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("GetResourceLimits err = %v", err)
	}
	if len(limits) != 2 {
		t.Fatalf("got %d limits, want 2: %+v", len(limits), limits)
	}
	if limits[0].Resource != "memoryuse" || limits[0].Action != "deny" || limits[0].Amount != "536870912" {
		t.Errorf("limit[0] = %+v", limits[0])
	}
	if want := "rctl -l jail:web"; !hasCmd(fake, want) {
		t.Errorf("PIDs were still batched into one call: %q", want)
	}
}

func TestGetResourceLimitsNoRules(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("No rules"), errors.New("exit 1")
	}}
	p := &JailProvider{runner: fake}
	limits, err := p.GetResourceLimits(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("expected no error for 'No rules', got %v", err)
	}
	if len(limits) != 0 {
		t.Errorf("expected empty limits, got %+v", limits)
	}
}

func TestGetResourceLimitsInvalidName(t *testing.T) {
	p := &JailProvider{runner: &execx.Fake{}}
	if _, err := p.GetResourceLimits(context.Background(), provider.InstanceHandle{ID: "bad;name"}); err == nil {
		t.Fatal("expected validation error for invalid name")
	}
}

func TestRemoveResourceLimits(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake}
	if err := p.RemoveResourceLimits(context.Background(), provider.InstanceHandle{ID: "web"}); err != nil {
		t.Fatalf("RemoveResourceLimits err = %v", err)
	}
	if want := "rctl -r jail:web"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestRemoveResourceLimitsNoRulesOK(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("No rules"), errors.New("exit 1")
	}}
	p := &JailProvider{runner: fake}
	if err := p.RemoveResourceLimits(context.Background(), provider.InstanceHandle{ID: "web"}); err != nil {
		t.Fatalf("expected nil for 'No rules', got %v", err)
	}
}

func TestGetCPUPriority(t *testing.T) {
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		switch name {
		case "jls":
			return []byte("42\n"), nil
		case "jexec":
			return []byte("1\n"), nil // pid list piped to head
		case "ps":
			return []byte("5\n"), nil // nice value
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}
	nice, err := p.GetCPUPriority(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("GetCPUPriority err = %v", err)
	}
	if nice != 5 {
		t.Errorf("nice = %d, want 5", nice)
	}
}

func TestSetCPUPriority(t *testing.T) {
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		switch name {
		case "jls":
			return []byte("42\n"), nil
		case "jexec":
			return []byte("1\n2\n3\n"), nil // three pids
		case "renice":
			return nil, nil
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}
	if err := p.SetCPUPriority(context.Background(), provider.InstanceHandle{ID: "web"}, 10); err != nil {
		t.Fatalf("SetCPUPriority err = %v", err)
	}
	// One renice per PID: batching them made the call fail as soon as any one
	// process had exited, which is ordinary in a live jail.
	for _, want := range []string{"renice -n 10 -p 1", "renice -n 10 -p 2", "renice -n 10 -p 3"} {
		if !hasCmd(fake, want) {
			t.Errorf("missing %q", want)
		}
	}
	if want := "renice -n 10 -p 1 2 3"; hasCmd(fake, want) {
		t.Errorf("missing %q; got %q", want, lastCmd(fake))
	}
}

func TestSetCPUPriorityInvalidRange(t *testing.T) {
	p := &JailProvider{runner: &execx.Fake{}}
	if err := p.SetCPUPriority(context.Background(), provider.InstanceHandle{ID: "web"}, 99); err == nil {
		t.Fatal("expected error for out-of-range priority")
	}
}

func TestSetCPUPriorityNotRunning(t *testing.T) {
	// jls returns 0 -> jail not running.
	fake := &execx.Fake{Func: func(name string, _ []string) ([]byte, error) {
		if name == "jls" {
			return []byte("0\n"), nil
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}
	if err := p.SetCPUPriority(context.Background(), provider.InstanceHandle{ID: "web"}, 5); err == nil {
		t.Fatal("expected error when jail not running")
	}
}

func TestIsValidRctlResourceAndAction(t *testing.T) {
	if !isValidRctlResource("memoryuse") || !isValidRctlResource("pcpu") {
		t.Error("expected valid resources to pass")
	}
	if isValidRctlResource("bogus") {
		t.Error("expected invalid resource to fail")
	}
	if !isValidRctlAction("deny") || !isValidRctlAction("log") {
		t.Error("expected valid actions to pass")
	}
	if isValidRctlAction("nuke") {
		t.Error("expected invalid action to fail")
	}
}
