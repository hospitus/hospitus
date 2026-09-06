package podman

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// newFakeProvider builds a PodmanProvider whose external commands are served by
// the given fake, so lifecycle logic can be exercised without a real podman.
func newFakeProvider(fake *execx.Fake) *PodmanProvider {
	return &PodmanProvider{
		instances: map[string]*containerInfo{},
		podmanBin: "podman",
		logger:    logging.WithProvider("podman"),
		runner:    fake,
	}
}

func handle(id string) provider.InstanceHandle {
	return provider.InstanceHandle{ID: id, Provider: providerName}
}

func TestStartInstance(t *testing.T) {
	fake := &execx.Fake{}
	p := newFakeProvider(fake)
	p.instances["c1"] = &containerInfo{ID: "c1", State: provider.StateStopped}

	if err := p.StartInstance(context.Background(), handle("c1")); err != nil {
		t.Fatalf("StartInstance: %v", err)
	}
	if p.instances["c1"].State != provider.StateRunning {
		t.Errorf("state = %v, want running", p.instances["c1"].State)
	}
	if len(fake.Calls) != 1 || fake.Calls[0].Args[0] != "start" || fake.Calls[0].Args[1] != "c1" {
		t.Errorf("unexpected command: %+v", fake.Calls)
	}
}

func TestStartInstanceError(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("no such container"), errors.New("exit 125")
	}}
	p := newFakeProvider(fake)
	if err := p.StartInstance(context.Background(), handle("nope")); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestStopInstanceForceBuildsArgs(t *testing.T) {
	fake := &execx.Fake{}
	p := newFakeProvider(fake)
	p.instances["c1"] = &containerInfo{ID: "c1", State: provider.StateRunning}

	err := p.StopInstance(context.Background(), handle("c1"), provider.StopOptions{Force: true})
	if err != nil {
		t.Fatalf("StopInstance: %v", err)
	}
	args := fake.Calls[0].Args
	if args[0] != "stop" || !slices.Contains(args, "-t") || !slices.Contains(args, "0") {
		t.Errorf("force stop args = %v, want stop -t 0", args)
	}
	if p.instances["c1"].State != provider.StateStopped {
		t.Errorf("state = %v, want stopped", p.instances["c1"].State)
	}
}

func TestStopInstanceAlreadyStoppedIsIgnored(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("container already stopped"), errors.New("exit 125")
	}}
	p := newFakeProvider(fake)
	if err := p.StopInstance(context.Background(), handle("c1"), provider.StopOptions{}); err != nil {
		t.Errorf("already-stopped should be ignored, got %v", err)
	}
}

func TestGetInstanceState(t *testing.T) {
	cases := map[string]provider.InstanceState{
		"running":  provider.StateRunning,
		"exited":   provider.StateStopped,
		"paused":   provider.StatePaused,
		"removing": provider.StateDeleting,
		"weird":    provider.StateUnknown,
	}
	for status, want := range cases {
		t.Run(status, func(t *testing.T) {
			fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
				return []byte(status + "\n"), nil
			}}
			p := newFakeProvider(fake)
			got, err := p.GetInstanceState(context.Background(), handle("c1"))
			if err != nil {
				t.Fatalf("GetInstanceState: %v", err)
			}
			if got != want {
				t.Errorf("status %q → %v, want %v", status, got, want)
			}
		})
	}
}

func TestRestartInstance(t *testing.T) {
	fake := &execx.Fake{}
	p := newFakeProvider(fake)
	p.instances["c1"] = &containerInfo{ID: "c1", State: provider.StateStopped}
	if err := p.RestartInstance(context.Background(), handle("c1")); err != nil {
		t.Fatalf("RestartInstance: %v", err)
	}
	if fake.Calls[0].Args[0] != "restart" {
		t.Errorf("args = %v, want restart", fake.Calls[0].Args)
	}
	if p.instances["c1"].State != provider.StateRunning {
		t.Errorf("state = %v, want running", p.instances["c1"].State)
	}
}

func TestDeleteInstanceForce(t *testing.T) {
	fake := &execx.Fake{}
	p := newFakeProvider(fake)
	p.instances["c1"] = &containerInfo{ID: "c1"}
	if err := p.DeleteInstance(context.Background(), handle("c1"), true); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if !slices.Contains(fake.Calls[0].Args, "-f") {
		t.Errorf("force delete missing -f: %v", fake.Calls[0].Args)
	}
	if _, ok := p.instances["c1"]; ok {
		t.Error("instance should be removed from cache")
	}
}

func TestDeleteInstanceNoSuchContainerIsIgnored(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("Error: no such container"), errors.New("exit 1")
	}}
	p := newFakeProvider(fake)
	p.instances["c1"] = &containerInfo{ID: "c1"}
	if err := p.DeleteInstance(context.Background(), handle("c1"), false); err != nil {
		t.Errorf("no-such-container should be ignored, got %v", err)
	}
	if _, ok := p.instances["c1"]; ok {
		t.Error("instance should still be removed from cache")
	}
}

func TestAttachDetachNetwork(t *testing.T) {
	fake := &execx.Fake{}
	p := newFakeProvider(fake)
	err := p.AttachNetwork(context.Background(), handle("c1"),
		provider.NetworkAttachment{Network: provider.NetworkSpec{Bridge: "br0"}})
	if err != nil {
		t.Fatalf("AttachNetwork: %v", err)
	}
	if got := strings.Join(fake.Calls[0].Args, " "); got != "network connect br0 c1" {
		t.Errorf("attach args = %q", got)
	}

	if err := p.DetachNetwork(context.Background(), handle("c1"), "br0"); err != nil {
		t.Fatalf("DetachNetwork: %v", err)
	}
	if got := strings.Join(fake.Calls[1].Args, " "); got != "network disconnect br0 c1" {
		t.Errorf("detach args = %q", got)
	}
}

func TestDetachNetworkRequiresName(t *testing.T) {
	p := newFakeProvider(&execx.Fake{})
	if err := p.DetachNetwork(context.Background(), handle("c1"), ""); err == nil {
		t.Fatal("expected error for empty network name")
	}
}

func TestHealthCheck(t *testing.T) {
	p := newFakeProvider(&execx.Fake{})
	if err := p.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}

	bad := newFakeProvider(&execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return nil, errors.New("down")
	}})
	if err := bad.HealthCheck(context.Background()); err == nil {
		t.Error("expected HealthCheck error when podman is down")
	}
}

func TestPullImageIfNeededSkipsWhenPresent(t *testing.T) {
	// image exists → Run returns nil → no pull issued (only one call).
	fake := &execx.Fake{}
	p := newFakeProvider(fake)
	if err := p.pullImageIfNeeded(context.Background(), "nginx"); err != nil {
		t.Fatalf("pullImageIfNeeded: %v", err)
	}
	if len(fake.Calls) != 1 || fake.Calls[0].Args[0] != "image" {
		t.Errorf("expected a single image-exists check, got %+v", fake.Calls)
	}
}

func TestSetInstanceResources(t *testing.T) {
	fake := &execx.Fake{}
	p := newFakeProvider(fake)
	err := p.SetInstanceResources(context.Background(), handle("c1"),
		provider.ResourceSpec{CPUs: 2, MemoryMB: 512})
	if err != nil {
		t.Fatalf("SetInstanceResources: %v", err)
	}
	got := strings.Join(fake.Calls[0].Args, " ")
	if !strings.Contains(got, "update") || !strings.Contains(got, "--cpus 2") || !strings.Contains(got, "--memory 512m") {
		t.Errorf("update args = %q", got)
	}
}

// TestPullImageIfNeededPrefersNativeVariant covers ghcr.io/freebsd/*: the
// registry publishes a FreeBSD image and no Linux one, so a pull that forces
// --os linux fails with "no image found in image index". The native pull has to
// come first, and must not be followed by a Linux attempt once it succeeds.
func TestPullImageIfNeededPrefersNativeVariant(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "image" {
			return nil, errors.New("no such image") // force a pull
		}
		return nil, nil // any pull succeeds
	}}
	p := newFakeProvider(fake)
	if err := p.pullImageIfNeeded(context.Background(), "ghcr.io/freebsd/freebsd-runtime:14.3"); err != nil {
		t.Fatalf("pullImageIfNeeded: %v", err)
	}

	for _, c := range fake.Calls {
		for _, a := range c.Args {
			if a == "--os" {
				t.Errorf("forced an OS after the native pull succeeded: %+v", fake.Calls)
			}
		}
	}
}

// TestPullImageIfNeededFallsBackToLinux covers the common case: the image is
// Linux-only, so the native pull finds nothing and FreeBSD has to ask for the
// Linux variant it runs through the ABI compatibility layer.
func TestPullImageIfNeededFallsBackToLinux(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skip("the Linux fallback only applies on FreeBSD")
	}

	var sawLinuxPull bool
	fake := &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "image" {
			return nil, errors.New("no such image")
		}
		for _, a := range args {
			if a == "--os" {
				sawLinuxPull = true
				return nil, nil
			}
		}
		return nil, errors.New("no image found in image index")
	}}
	p := newFakeProvider(fake)
	if err := p.pullImageIfNeeded(context.Background(), "nginx"); err != nil {
		t.Fatalf("pullImageIfNeeded: %v", err)
	}
	if !sawLinuxPull {
		t.Error("no Linux pull attempted after the native one found nothing")
	}
}

// Recorded from "podman stats --no-stream --format json web" on podman 5.8.4,
// FreeBSD 15.1. Every counter is a string; there is no mem_limit field.
const podmanStatsJSON = `[
 {
  "id": "a7b9b3317b60",
  "name": "web",
  "cpu_time": "8m13s",
  "cpu_percent": "1.25%",
  "avg_cpu": "0.00%",
  "mem_usage": "46.33MB / 68.58GB",
  "mem_percent": "0.00%",
  "net_io": "42B / 3.036kB",
  "block_io": "8.192kB / 4.096kB",
  "pids": "1"
 }
]`

func TestGetInstanceMetricsReadsPodmanStrings(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte(podmanStatsJSON), nil
	}}
	p := newFakeProvider(fake)

	metrics, err := p.GetInstanceMetrics(context.Background(), handle("web"))
	if err != nil {
		t.Fatalf("GetInstanceMetrics: %v", err)
	}

	if metrics.CPUUsagePercent != 1.25 {
		t.Errorf("CPU = %v, want 1.25", metrics.CPUUsagePercent)
	}
	// 46.33 MB SI = 46330000 bytes, reported in MiB.
	if metrics.MemoryUsedMB != 44 {
		t.Errorf("memory used = %d MB, want 44", metrics.MemoryUsedMB)
	}
	if metrics.MemoryTotalMB != 65402 {
		t.Errorf("memory total = %d MB, want 65402", metrics.MemoryTotalMB)
	}
	if metrics.NetRxBytes != 42 {
		t.Errorf("net rx = %d, want 42", metrics.NetRxBytes)
	}
	if metrics.DiskReadBytes != 8192 || metrics.DiskWriteBytes != 4096 {
		t.Errorf("block io = %d/%d, want 8192/4096", metrics.DiskReadBytes, metrics.DiskWriteBytes)
	}
}

// TestGetInstanceMetricsReadsPodmanNumbers covers the other shape: older podman
// and podman-remote against a Linux host emit numbers. The network counters had
// no float64 case, so against such a podman the disk figures worked and the
// network ones silently stayed at zero.
func TestGetInstanceMetricsReadsPodmanNumbers(t *testing.T) {
	const numeric = `[
	 {
	  "id": "a7b9b3317b60",
	  "name": "web",
	  "cpu_percent": 1.25,
	  "mem_usage": 48545792,
	  "mem_limit": 68580000000,
	  "net_input": 42,
	  "net_output": 3036,
	  "block_input": 8192,
	  "block_output": 4096
	 }
	]`

	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte(numeric), nil
	}}
	p := newFakeProvider(fake)

	metrics, err := p.GetInstanceMetrics(context.Background(), handle("web"))
	if err != nil {
		t.Fatalf("GetInstanceMetrics: %v", err)
	}

	if metrics.NetRxBytes != 42 || metrics.NetTxBytes != 3036 {
		t.Errorf("net io = %d/%d, want 42/3036", metrics.NetRxBytes, metrics.NetTxBytes)
	}
	if metrics.DiskReadBytes != 8192 || metrics.DiskWriteBytes != 4096 {
		t.Errorf("block io = %d/%d, want 8192/4096", metrics.DiskReadBytes, metrics.DiskWriteBytes)
	}
	if metrics.CPUUsagePercent != 1.25 {
		t.Errorf("CPU = %v, want 1.25", metrics.CPUUsagePercent)
	}
}
