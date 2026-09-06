package podman

import (
	"context"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// Recorded from "podman inspect web --format '{{json .NetworkSettings}}'" on
// podman 5.8.4. The address appears both at the top level and under Networks.
const podmanNetworkSettingsJSON = `{"Gateway":"10.88.0.1","IPAddress":"10.88.0.67",` +
	`"IPPrefixLen":16,"GlobalIPv6Address":"","MacAddress":"58:9c:fc:10:ab:c0",` +
	`"Networks":{"podman":{"Gateway":"10.88.0.1","IPAddress":"10.88.0.67",` +
	`"GlobalIPv6Address":"","NetworkID":"2f259bab93aa"}}}`

func TestInstanceAddressesReportsTheContainerAddressOnce(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte(podmanNetworkSettingsJSON), nil
	}}
	p := newFakeProvider(fake)

	ips, err := p.InstanceAddresses(context.Background(), handle("web"))
	if err != nil {
		t.Fatalf("InstanceAddresses: %v", err)
	}

	if len(ips) != 1 {
		t.Fatalf("addresses = %v, want one", ips)
	}
	if ips[0].String() != "10.88.0.67" {
		t.Errorf("address = %s, want 10.88.0.67", ips[0])
	}
}

func TestInstanceAddressesReadsEveryNetwork(t *testing.T) {
	const twoNetworks = `{"IPAddress":"","GlobalIPv6Address":"","Networks":{` +
		`"podman":{"IPAddress":"10.88.0.67"},"internal":{"IPAddress":"172.16.0.5"}}}`

	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte(twoNetworks), nil
	}}
	p := newFakeProvider(fake)

	ips, err := p.InstanceAddresses(context.Background(), handle("web"))
	if err != nil {
		t.Fatalf("InstanceAddresses: %v", err)
	}
	if len(ips) != 2 {
		t.Fatalf("addresses = %v, want two", ips)
	}
}
