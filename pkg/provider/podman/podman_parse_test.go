package podman

import (
	"context"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestParseBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"--", 0},
		{"512B", 512},
		// Podman prints SI units and keeps the "i" forms for binary multiples:
		// its "68.58GB" for host memory is hw.physmem 68575399936 over 1e9.
		{"1.5kB", 1500},
		{"10MB", 10 * 1000 * 1000},
		{"2GB", 2 * 1000 * 1000 * 1000},
		{"8.192kB", 8192},
		{"1.5KiB", 1536},
		{"1MiB", 1024 * 1024},
		{"1024", 1024},
	}
	for _, c := range cases {
		got, err := parseBytes(c.in)
		if err != nil {
			t.Errorf("parseBytes(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseBytes(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	if _, err := parseBytes("not-a-number"); err == nil {
		t.Error("expected error for non-numeric input")
	}
}

func TestValidatePodmanPortSpec(t *testing.T) {
	valid := []string{"80:80", "127.0.0.1:8080:80", "53:53/udp", "1000-2000:1000-2000"}
	for _, s := range valid {
		if err := validatePodmanPortSpec(s); err != nil {
			t.Errorf("validatePodmanPortSpec(%q) unexpected error: %v", s, err)
		}
	}
	invalid := []string{"", "80", "80:80/sctp", "0:80", "80:70000", "a:b", "1:2:3:4"}
	for _, s := range invalid {
		if err := validatePodmanPortSpec(s); err == nil {
			t.Errorf("validatePodmanPortSpec(%q) expected error, got nil", s)
		}
	}
}

func TestSyncContainersParsesPsOutput(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("abc123\tweb\tnginx:latest\tUp 3 minutes\n" +
			"def456\tdb\tpostgres:16\tExited (0) 1 hour ago\n" +
			"ghi789\tcache\tredis:7\tPaused\n"), nil
	}}
	p := newFakeProvider(fake)

	handles, err := p.ListInstances(context.Background(), provider.InstanceFilter{})
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(handles) != 3 {
		t.Fatalf("got %d handles, want 3", len(handles))
	}
	if p.instances["web"].State != provider.StateRunning {
		t.Errorf("web state = %v, want running", p.instances["web"].State)
	}
	if p.instances["db"].State != provider.StateStopped {
		t.Errorf("db state = %v, want stopped", p.instances["db"].State)
	}
	if p.instances["cache"].State != provider.StatePaused {
		t.Errorf("cache state = %v, want paused", p.instances["cache"].State)
	}
}

func TestListInstancesStateFilter(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("a\tweb\tnginx\tUp\nb\tdb\tpg\tExited\n"), nil
	}}
	p := newFakeProvider(fake)

	handles, err := p.ListInstances(context.Background(),
		provider.InstanceFilter{States: []provider.InstanceState{provider.StateRunning}})
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(handles) != 1 || handles[0].ID != "web" {
		t.Errorf("state filter returned %+v, want only web", handles)
	}
}

func TestGetInstanceInfoParsesInspect(t *testing.T) {
	inspect := `[{"Id":"abc","State":{"Status":"running"},"Config":{"Image":"nginx"}}]`
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte(inspect), nil
	}}
	p := newFakeProvider(fake)
	info, err := p.GetInstanceInfo(context.Background(), handle("abc"))
	if err != nil {
		t.Fatalf("GetInstanceInfo: %v", err)
	}
	if info.State != provider.StateRunning {
		t.Errorf("info.State = %v, want running", info.State)
	}
}

func TestGetInstanceInfoInvalidJSON(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("not json"), nil
	}}
	p := newFakeProvider(fake)
	if _, err := p.GetInstanceInfo(context.Background(), handle("abc")); err == nil {
		t.Error("expected parse error for invalid inspect JSON")
	}
}
