package applecontainer

import (
	"context"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// listJSON is a trimmed copy of what `container list --all --format json`
// actually printed for a running container, including the field names that
// differ from the podman shape this provider is modeled on.
const listJSON = `[{
  "id": "web",
  "status": {
    "state": "running",
    "networks": [{"hostname":"web","ipv4Address":"192.168.64.3/24","macAddress":"fa:be:e6:d4:07:02"}]
  },
  "configuration": {
    "image": {"reference": "docker.io/library/alpine:latest"},
    "resources": {"cpus": 4, "memoryInBytes": 1073741824}
  }
}]`

func testProvider(fn func(name string, args []string) ([]byte, error)) (*Provider, *execx.Fake) {
	fake := &execx.Fake{Func: fn}
	return &Provider{containerBin: "container", runner: fake}, fake
}

func cmdLine(c execx.Call) string {
	return c.Name + " " + strings.Join(c.Args, " ")
}

func ranCommand(f *execx.Fake, want string) bool {
	for _, c := range f.Calls {
		if cmdLine(c) == want {
			return true
		}
	}
	return false
}

func TestCreateInstanceBuildsCommand(t *testing.T) {
	p, fake := testProvider(func(_ string, args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "list" {
			return []byte("[]"), nil // Nothing exists yet.
		}
		return nil, nil
	})

	_, err := p.CreateInstance(context.Background(), provider.InstanceSpec{
		Name:     "web",
		Image:    "docker.io/library/alpine:latest",
		CPUs:     2,
		MemoryMB: 512,
		ProviderConfig: map[string]interface{}{
			"command": []interface{}{"sleep", "120"},
		},
	})
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	// The command has to follow the image, or the tool reads it as a flag.
	want := "container create --name web --cpus 2 --memory 512M docker.io/library/alpine:latest sleep 120"
	if !ranCommand(fake, want) {
		t.Errorf("create command was not issued as expected.\nwant: %s\ngot:  %v", want, fake.Calls)
	}
}

func TestCreateInstanceRefusesExisting(t *testing.T) {
	p, _ := testProvider(func(_ string, args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "list" {
			return []byte(listJSON), nil
		}
		return nil, nil
	})

	_, err := p.CreateInstance(context.Background(), provider.InstanceSpec{
		Name:  "web",
		Image: "docker.io/library/alpine:latest",
	})
	if err == nil {
		t.Fatal("creating a container that already exists returned nil")
	}
}

// TestGetInstanceInfoDecodesRealOutput pins the field names, which are the whole
// contract with a pre-1.0 tool.
func TestGetInstanceInfoDecodesRealOutput(t *testing.T) {
	p, _ := testProvider(func(_ string, _ []string) ([]byte, error) {
		return []byte(listJSON), nil
	})

	info, err := p.GetInstanceInfo(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("GetInstanceInfo: %v", err)
	}

	if info.State != provider.StateRunning {
		t.Errorf("state = %v, want running", info.State)
	}
	if info.Spec.CPUs != 4 {
		t.Errorf("cpus = %d, want 4", info.Spec.CPUs)
	}
	if info.Spec.MemoryMB != 1024 {
		t.Errorf("memory = %d MB, want 1024", info.Spec.MemoryMB)
	}
	if len(info.IPAddresses) != 1 || info.IPAddresses[0].String() != "192.168.64.3" {
		t.Errorf("addresses = %v, want the prefix stripped from 192.168.64.3/24", info.IPAddresses)
	}
	if len(info.MACAddresses) != 1 {
		t.Errorf("MAC addresses = %v, want one", info.MACAddresses)
	}
}

// An empty store prints nothing rather than an empty array, which json.Unmarshal
// would reject.
func TestListHandlesEmptyOutput(t *testing.T) {
	p, _ := testProvider(func(_ string, _ []string) ([]byte, error) {
		return []byte(""), nil
	})

	handles, err := p.ListInstances(context.Background(), provider.InstanceFilter{})
	if err != nil {
		t.Fatalf("ListInstances on an empty store: %v", err)
	}
	if len(handles) != 0 {
		t.Errorf("expected no instances, got %v", handles)
	}
}

// Stopping something already stopped, or deleting something already gone, is the
// state the caller asked for.
func TestIdempotentStopAndDelete(t *testing.T) {
	p, _ := testProvider(func(_ string, args []string) ([]byte, error) {
		switch args[0] {
		case "stop":
			return []byte("Error: container is not running"), errFailed
		case "delete":
			return []byte("Error: container not found"), errFailed
		}
		return nil, nil
	})

	handle := provider.InstanceHandle{ID: "web"}
	if err := p.StopInstance(context.Background(), handle, provider.StopOptions{}); err != nil {
		t.Errorf("stopping an already-stopped container: %v", err)
	}
	if err := p.DeleteInstance(context.Background(), handle, false); err != nil {
		t.Errorf("deleting an absent container: %v", err)
	}
}

func TestMapState(t *testing.T) {
	cases := map[string]provider.InstanceState{
		"running":  provider.StateRunning,
		"stopped":  provider.StateStopped,
		"exited":   provider.StateStopped,
		"stopping": provider.StateStopping,
		"weird":    provider.StateUnknown,
	}
	for in, want := range cases {
		if got := mapState(in); got != want {
			t.Errorf("mapState(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestCapabilitiesRejectCrossArch(t *testing.T) {
	caps := (&Provider{}).Capabilities()
	if caps.SupportsCrossArch {
		t.Error("each container runs on the host CPU; there is no emulation")
	}
	if caps.SupportsSnapshots {
		t.Error("the tool has no snapshots")
	}
}

var errFailed = errStr("exit status 1")

type errStr string

func (e errStr) Error() string { return string(e) }
