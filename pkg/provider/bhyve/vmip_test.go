package bhyve

import "testing"

// TestGuestMACForTap verifies the guest NIC MAC is extracted from the bhyve
// device spec containing the tap, not some other NIC (audit HIGH exec.go:209).
func TestGuestMACForTap(t *testing.T) {
	cmd := "bhyve -c 2 -s 4:0,virtio-net,tap1,mac=00:a0:98:11:22:33 -s 5,virtio-net,tap2,mac=00:a0:98:aa:bb:cc web"
	if got := guestMACForTap(cmd, "tap1"); got != "00:a0:98:11:22:33" {
		t.Errorf("tap1 MAC = %q, want 00:a0:98:11:22:33", got)
	}
	if got := guestMACForTap(cmd, "tap2"); got != "00:a0:98:aa:bb:cc" {
		t.Errorf("tap2 MAC = %q, want 00:a0:98:aa:bb:cc", got)
	}
	// A tap with no mac= in its spec yields "" (cannot correlate).
	if got := guestMACForTap("bhyve -s 4,virtio-net,tap3 web", "tap3"); got != "" {
		t.Errorf("tap3 MAC = %q, want empty", got)
	}
	// Unknown tap yields "".
	if got := guestMACForTap(cmd, "tap9"); got != "" {
		t.Errorf("tap9 MAC = %q, want empty", got)
	}
}

// TestIPForMACInARP verifies the ARP lookup returns the IP for the exact MAC and
// nothing for an absent MAC, instead of the first arbitrary entry.
func TestIPForMACInARP(t *testing.T) {
	arp := "" +
		"? (10.10.0.2) at 00:a0:98:11:22:33 on bridge0 expires in 1200 seconds [ethernet]\n" +
		"? (10.10.0.3) at 00:a0:98:aa:bb:cc on bridge0 expires in 1200 seconds [ethernet]\n" +
		"? (10.10.0.9) at (incomplete) on bridge0\n"

	if got := ipForMACInARP(arp, "00:a0:98:aa:bb:cc"); got != "10.10.0.3" {
		t.Errorf("got %q, want 10.10.0.3", got)
	}
	if got := ipForMACInARP(arp, "00:a0:98:ff:ff:ff"); got != "" {
		t.Errorf("absent MAC should yield empty, got %q", got)
	}
}
