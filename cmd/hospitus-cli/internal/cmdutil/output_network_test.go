package cmdutil

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

func TestPrintInstanceSkipsAnUnknownNetworkType(t *testing.T) {
	// A podman container declares no network, so the address hospitus reports
	// arrives with nothing else attached to it.
	inst := &datastore.Instance{Name: "web", Provider: "podman"}
	inst.Spec.Networks = []provider.NetworkSpec{{IPv4: "10.88.0.67"}}

	var out bytes.Buffer
	if err := PrintInstance(&out, inst, OutputFormatTable); err != nil {
		t.Fatalf("PrintInstance: %v", err)
	}

	if strings.Contains(out.String(), "Type:") {
		t.Errorf("printed an empty network type:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "10.88.0.67") {
		t.Errorf("address missing:\n%s", out.String())
	}
}

func TestPrintInstanceShowsAKnownNetworkType(t *testing.T) {
	inst := &datastore.Instance{Name: "web", Provider: "jail"}
	inst.Spec.Networks = []provider.NetworkSpec{{Type: "bridge", IPv4: "10.0.0.10/24"}}

	var out bytes.Buffer
	if err := PrintInstance(&out, inst, OutputFormatTable); err != nil {
		t.Fatalf("PrintInstance: %v", err)
	}

	if !strings.Contains(out.String(), "Type:       bridge") {
		t.Errorf("network type missing:\n%s", out.String())
	}
}
