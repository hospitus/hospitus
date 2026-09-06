package network

import "testing"

// freebsdBridge is real ifconfig(8) output, captured from a running Hospitus
// NAT bridge on FreeBSD 16.0-CURRENT.
const freebsdBridge = `hospitus-nat: flags=1008843<UP,BROADCAST,RUNNING,SIMPLEX,MULTICAST,LOWER_UP> metric 0 mtu 1500
	options=10<VLAN_HWTAGGING>
	ether 58:9c:fc:10:a9:39
	inet 10.10.0.1 netmask 0xffffff00 broadcast 10.10.0.255
	id 00:00:00:00:00:00 priority 32768 hellotime 2 fwddelay 15
	maxage 20 holdcnt 6 proto rstp maxaddr 2000 timeout 1200
	root id 00:00:00:00:00:00 priority 32768 ifcost 0 port 0
	bridge flags=0<>
	member: tap_windows_0 flags=143<LEARNING,DISCOVER,AUTOEDGE,AUTOPTP>
	        port 12 priority 128 path cost 2000000 vlan protocol 802.1q
	groups: bridge
	nd6 options=809<PERFORMNUD,IFDISABLED,STABLEADDR>`

// TestParseIfconfigReadsABridge covers the fields the API reports.
func TestParseIfconfigReadsABridge(t *testing.T) {
	got := parseIfconfig("hospitus0", []byte(freebsdBridge))

	if got.State != "up" {
		t.Errorf("state = %q, want up", got.State)
	}
	if got.MTU != 1500 {
		t.Errorf("mtu = %d, want 1500", got.MTU)
	}
	if got.IPAddress != "10.10.0.1" {
		t.Errorf("address = %q, want 10.10.0.1", got.IPAddress)
	}
	if got.MacAddress != "58:9c:fc:10:a9:39" {
		t.Errorf("mac = %q, want 58:9c:fc:10:d0:b3", got.MacAddress)
	}
	if len(got.Members) != 1 || got.Members[0] != "tap_windows_0" {
		t.Errorf("members = %v, want [tap_windows_0]", got.Members)
	}
}

// TestParseIfconfigIgnoresTheMemberContinuationLine guards the one shape that
// reads like a second member: the port/cost line indented under a member.
func TestParseIfconfigIgnoresTheMemberContinuationLine(t *testing.T) {
	got := parseIfconfig("hospitus0", []byte(freebsdBridge))
	for _, m := range got.Members {
		if m == "port" {
			t.Fatalf("the continuation line was read as a member: %v", got.Members)
		}
	}
}

// TestParseIfconfigDownBridge checks that a bridge without UP reads as down,
// and that the hexadecimal flag value cannot satisfy the match on its own.
func TestParseIfconfigDownBridge(t *testing.T) {
	out := "hospitus1: flags=8802<BROADCAST,SIMPLEX,MULTICAST> metric 0 mtu 1500\n\tether 02:00:00:00:00:01"
	got := parseIfconfig("hospitus1", []byte(out))

	if got.State != "down" {
		t.Errorf("state = %q, want down", got.State)
	}
	if got.MTU != 1500 {
		t.Errorf("mtu = %d, want 1500", got.MTU)
	}
	if len(got.Members) != 0 {
		t.Errorf("members = %v, want none", got.Members)
	}
}

// TestParseIPLinkReadsBridges covers the Linux listing.
func TestParseIPLinkReadsBridges(t *testing.T) {
	out := `3: br0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP mode DEFAULT group default qlen 1000\    link/ether 02:42:1a:2b:3c:4d brd ff:ff:ff:ff:ff:ff
7: br1: <NO-CARRIER,BROADCAST,MULTICAST> mtu 9000 qdisc noqueue state DOWN mode DEFAULT group default qlen 1000\    link/ether 02:42:1a:2b:3c:4e brd ff:ff:ff:ff:ff:ff`

	got := parseIPLink(out)
	if len(got) != 2 {
		t.Fatalf("got %d bridges, want 2", len(got))
	}
	if got[0].Name != "br0" || got[0].State != "up" || got[0].MTU != 1500 {
		t.Errorf("first bridge = %+v", got[0])
	}
	if got[1].Name != "br1" || got[1].State != "down" || got[1].MTU != 9000 {
		t.Errorf("second bridge = %+v", got[1])
	}
	if got[0].MacAddress != "02:42:1a:2b:3c:4d" {
		t.Errorf("mac = %q", got[0].MacAddress)
	}
}
