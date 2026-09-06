package api

import (
	"net"
	"testing"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

func TestFillFirstEmptyAddressGivesAnUndeclaredNetworkAnEntry(t *testing.T) {
	// A podman container declares no network; podman attaches it to its own.
	instance := &datastore.Instance{}

	fillFirstEmptyAddress(instance, []net.IP{net.ParseIP("10.88.0.67")})

	if len(instance.Spec.Networks) != 1 {
		t.Fatalf("networks = %v, want one", instance.Spec.Networks)
	}
	if got := instance.Spec.Networks[0].IPv4; got != "10.88.0.67" {
		t.Errorf("address = %q, want 10.88.0.67", got)
	}
}

func TestFillFirstEmptyAddressLeavesAStaticAddressAlone(t *testing.T) {
	instance := &datastore.Instance{}
	instance.Spec.Networks = []provider.NetworkSpec{{IPv4: "10.0.0.10/24"}}

	fillFirstEmptyAddress(instance, []net.IP{net.ParseIP("10.88.0.67")})

	if got := instance.Spec.Networks[0].IPv4; got != "10.0.0.10/24" {
		t.Errorf("address = %q, want the declared 10.0.0.10/24", got)
	}
	if len(instance.Spec.Networks) != 1 {
		t.Errorf("networks = %v, want one", instance.Spec.Networks)
	}
}
