package podman

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// cloneFake serves the podman calls CloneInstance makes: a commit, an inspect
// (for GetInstanceInfo and for the resource limits), a create, and the inspect
// behind refreshContainerInfo.
func cloneFake(t *testing.T) *execx.Fake {
	t.Helper()

	inspect := []map[string]interface{}{{
		"Id":      "1111111111111111111111111111111111111111111111111111111111111111",
		"Name":    "web",
		"Created": "2026-01-01T00:00:00Z",
		"Image":   "docker.io/library/nginx:latest",
		"State":   map[string]interface{}{"Status": "running", "StartedAt": "2026-01-01T00:00:00Z"},
		"Config":  map[string]interface{}{"Labels": map[string]interface{}{"app": "web"}},
		"HostConfig": map[string]interface{}{
			"NanoCpus": 2e9,
			"Memory":   512 * 1024 * 1024,
		},
	}}
	inspectJSON, err := json.Marshal(inspect)
	if err != nil {
		t.Fatal(err)
	}

	return &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		switch {
		case len(args) > 0 && args[0] == "inspect":
			return inspectJSON, nil
		case len(args) > 0 && args[0] == "create":
			return []byte("2222222222222222222222222222222222222222222222222222222222222222"), nil
		default:
			return nil, nil
		}
	}}
}

// TestCloneInstanceKeepsTheImageItsCloneRunsFrom is the regression for the
// clone that destroyed itself: the committed image was removed on the success
// path with "rmi --force", which removes every container using the image —
// that is, the clone that had just been created from it.
func TestCloneInstanceKeepsTheImageItsCloneRunsFrom(t *testing.T) {
	fake := cloneFake(t)
	p := newFakeProvider(fake)
	p.instances["web"] = &containerInfo{ID: "c1", Name: "web", State: provider.StateRunning}

	h, err := p.CloneInstance(context.Background(), handle("web"), "web-copy", provider.CloneOptions{})
	if err != nil {
		t.Fatalf("CloneInstance: %v", err)
	}

	image, _ := h.Metadata["image"].(string)
	if image == "" {
		t.Fatal("the clone handle names no image")
	}

	for _, c := range fake.Calls {
		if len(c.Args) > 0 && c.Args[0] == "rmi" {
			t.Errorf("the clone's own image was removed: %v", c.Args)
		}
	}
}

// TestCloneInstanceReadsTheSourceLimits covers the "same settings" promise:
// GetInstanceInfo never populates CPUs/memory, so the limits have to come from
// podman inspect or the clone silently runs without them.
func TestCloneInstanceReadsTheSourceLimits(t *testing.T) {
	fake := cloneFake(t)
	p := newFakeProvider(fake)
	p.instances["web"] = &containerInfo{ID: "c1", Name: "web", State: provider.StateRunning}

	if _, err := p.CloneInstance(context.Background(), handle("web"), "web-copy", provider.CloneOptions{}); err != nil {
		t.Fatalf("CloneInstance: %v", err)
	}

	var createArgs string
	for _, c := range fake.Calls {
		if len(c.Args) > 0 && c.Args[0] == "create" {
			createArgs = strings.Join(c.Args, " ")
		}
	}
	if !strings.Contains(createArgs, "--cpus 2") {
		t.Errorf("create args = %q, want the source's 2 CPUs", createArgs)
	}
	if !strings.Contains(createArgs, "--memory 512m") {
		t.Errorf("create args = %q, want the source's 512MB limit", createArgs)
	}
}

// TestCloneInstanceRemovesTheImageWhenTheCloneFails keeps the other half of the
// bargain: an image nothing ends up using must not be left behind.
func TestCloneInstanceRemovesTheImageWhenTheCloneFails(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "create" {
			return []byte("Error: name is already in use"), errors.New("exit 125")
		}
		if len(args) > 0 && args[0] == "inspect" {
			return []byte(`[{"Id":"c1","Name":"web","State":{"Status":"running"}}]`), nil
		}
		return nil, nil
	}}
	p := newFakeProvider(fake)
	p.instances["web"] = &containerInfo{ID: "c1", Name: "web", State: provider.StateRunning}

	if _, err := p.CloneInstance(context.Background(), handle("web"), "web-copy", provider.CloneOptions{}); err == nil {
		t.Fatal("expected the failing create to fail the clone")
	}

	// Which image, not merely that some rmi ran: a change that removed the
	// wrong one — the source's, say — passed the loop as it was.
	committed := ""
	removed := ""
	for _, c := range fake.Calls {
		switch {
		case len(c.Args) > 1 && c.Args[0] == "commit":
			committed = c.Args[len(c.Args)-1]
		case len(c.Args) > 0 && c.Args[0] == "rmi":
			removed = c.Args[len(c.Args)-1]
		}
	}
	if removed == "" {
		t.Fatal("the committed image was left behind after a failed clone")
	}
	if committed != "" && removed != committed {
		t.Errorf("removed %q but the clone had committed %q", removed, committed)
	}
}
