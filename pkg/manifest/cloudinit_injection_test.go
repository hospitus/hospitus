package manifest

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/hospitus/hospitus/pkg/cloudinit"
	"github.com/hospitus/hospitus/pkg/provider"
)

// A hostname or an address is manifest text. Formatted into the document with
// Sprintf — as the meta-data and network-config were — one carrying a newline
// added a cloud-config key instead of a value, and ValidateInstanceSpec
// accepts that, because it is valid UTF-8.
//
// Asserted on the parsed documents rather than on their text: an encoder
// renders such a value as a block scalar, so the injected words do appear in
// the output, indented inside the string they belong to. What matters is that
// they are not structure.
func TestCloudInitDocumentsCannotBeInjected(t *testing.T) {
	const (
		hostname = "web\nruncmd:\n  - touch /tmp/pwned"
		address  = "10.0.0.2/24\n        gateway: 10.0.0.254"
	)

	c := NewConverter()
	ci := &cloudinit.Config{
		InstanceID:    "vm-1",
		LocalHostname: hostname,
		Networks: []cloudinit.NetworkConfig{{
			Name:    "eth0",
			Type:    "static",
			Address: address,
			Gateway: "10.0.0.1",
		}},
	}
	spec := &provider.InstanceSpec{Name: "vm-1", OSType: "linux"}
	c.generateCloudInitYAML(ci, spec)

	if spec.CloudInit == nil {
		t.Fatal("no cloud-init produced")
	}

	var meta map[string]any
	if err := yaml.Unmarshal([]byte(spec.CloudInit.MetaData), &meta); err != nil {
		t.Fatalf("meta-data does not parse: %v\n%s", err, spec.CloudInit.MetaData)
	}
	if _, injected := meta["runcmd"]; injected {
		t.Errorf("the hostname became a key:\n%s", spec.CloudInit.MetaData)
	}
	if meta["local-hostname"] != hostname {
		t.Errorf("local-hostname = %q, want the value verbatim", meta["local-hostname"])
	}

	var network networkConfigV2
	if err := yaml.Unmarshal([]byte(spec.CloudInit.Network), &network); err != nil {
		t.Fatalf("network-config does not parse: %v\n%s", err, spec.CloudInit.Network)
	}
	eth, ok := network.Ethernets["eth0"]
	if !ok {
		t.Fatalf("eth0 is missing:\n%s", spec.CloudInit.Network)
	}
	if len(eth.Addresses) != 1 || eth.Addresses[0] != address {
		t.Errorf("addresses = %q, want the value verbatim", eth.Addresses)
	}
	// The injected "gateway:" stayed inside the address string; the real route
	// is the one that was configured.
	if len(eth.Routes) != 1 || eth.Routes[0].Via != "10.0.0.1" {
		t.Errorf("routes = %+v, want a single route via 10.0.0.1", eth.Routes)
	}
}
