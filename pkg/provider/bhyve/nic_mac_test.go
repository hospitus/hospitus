package bhyve

import (
	"net"
	"strings"
	"testing"
)

// TestEveryNICGetsARecordedAddress covers how a VM's address is found.
//
// Left to bhyve, the MAC is generated at start and cannot be read back: bhyve
// rewrites its process title to "bhyve: <name>", so the arguments are gone. The
// address could then only be found in the DHCP leases, under whatever hostname
// the guest reports — "freebsd" for a stock cloud image, which matches no VM
// name and left "hospitus bhyve info" showing none.
func TestEveryNICGetsARecordedAddress(t *testing.T) {
	config := &vmConfig{TapDevs: []string{"tap_web_0", "tap_web_1"}}

	assigned, err := ensureNICMACs(config)
	if err != nil {
		t.Fatalf("ensureNICMACs: %v", err)
	}
	if !assigned {
		t.Error("no address was assigned to a VM that had none")
	}
	if len(config.NICMACs) != 2 {
		t.Fatalf("recorded %d addresses for 2 taps", len(config.NICMACs))
	}
	for i, mac := range config.NICMACs {
		parsed, err := net.ParseMAC(mac)
		if err != nil {
			t.Errorf("tap %d got %q, which is not a MAC address: %v", i, mac, err)
			continue
		}
		// Locally administered and unicast, so two VMs on one host never
		// collide and no vendor's range is borrowed.
		if parsed[0]&0x02 == 0 {
			t.Errorf("tap %d got %q, which is not locally administered", i, mac)
		}
		if parsed[0]&0x01 != 0 {
			t.Errorf("tap %d got %q, which is a multicast address", i, mac)
		}
	}
	if config.NICMACs[0] == config.NICMACs[1] {
		t.Error("both taps got the same address")
	}
}

// TestRecordedAddressesAreKept covers a restart: an address that changed would
// break every lease and every ARP entry referring to it.
func TestRecordedAddressesAreKept(t *testing.T) {
	config := &vmConfig{
		TapDevs: []string{"tap_web_0"},
		NICMACs: []string{"02:11:22:33:44:55"},
	}

	assigned, err := ensureNICMACs(config)
	if err != nil {
		t.Fatalf("ensureNICMACs: %v", err)
	}
	if assigned {
		t.Error("an address was assigned although one was already recorded")
	}
	if config.NICMACs[0] != "02:11:22:33:44:55" {
		t.Errorf("the recorded address changed to %q", config.NICMACs[0])
	}
}

// TestTheAddressReachesTheCommandLine covers the argument bhyve is given.
func TestTheAddressReachesTheCommandLine(t *testing.T) {
	p := &BhyveProvider{}
	config := &vmConfig{
		Name:     "web",
		CPUs:     1,
		MemoryMB: 512,
		TapDevs:  []string{"tap_web_0"},
		NICMACs:  []string{"02:11:22:33:44:55"},
	}

	args, err := p.buildBhyveArgs(config)
	if err != nil {
		t.Fatalf("buildBhyveArgs: %v", err)
	}
	line := strings.Join(args, " ")
	if !strings.Contains(line, "tap_web_0,mac=02:11:22:33:44:55") {
		t.Errorf("the NIC was given no address: %s", line)
	}
}
