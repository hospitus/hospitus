package qemu

import (
	"net"
	"testing"
)

// makeAddr is a helper to build the IPAddresses entry used by GuestNetworkInterface.
func makeAddr(ipType, ip string, prefix int) struct {
	IPAddressType string `json:"ip-address-type"`
	IPAddress     string `json:"ip-address"`
	Prefix        int    `json:"prefix"`
} {
	return struct {
		IPAddressType string `json:"ip-address-type"`
		IPAddress     string `json:"ip-address"`
		Prefix        int    `json:"prefix"`
	}{IPAddressType: ipType, IPAddress: ip, Prefix: prefix}
}

func TestFilterGuestIPs_ExcludesLoopbackInterface(t *testing.T) {
	ifaces := []GuestNetworkInterface{
		{
			Name: "lo",
			IPAddresses: []struct {
				IPAddressType string `json:"ip-address-type"`
				IPAddress     string `json:"ip-address"`
				Prefix        int    `json:"prefix"`
			}{makeAddr("ipv4", "127.0.0.1", 8)},
		},
		{
			Name: "eth0",
			IPAddresses: []struct {
				IPAddressType string `json:"ip-address-type"`
				IPAddress     string `json:"ip-address"`
				Prefix        int    `json:"prefix"`
			}{makeAddr("ipv4", "192.168.1.100", 24)},
		},
	}

	ips := filterGuestIPs(ifaces)

	if len(ips) != 1 {
		t.Fatalf("expected 1 IP, got %d: %v", len(ips), ips)
	}
	if !ips[0].Equal(net.ParseIP("192.168.1.100")) {
		t.Errorf("expected 192.168.1.100, got %v", ips[0])
	}
}

func TestFilterGuestIPs_ExcludesLo0Interface(t *testing.T) {
	ifaces := []GuestNetworkInterface{
		{
			Name: "lo0",
			IPAddresses: []struct {
				IPAddressType string `json:"ip-address-type"`
				IPAddress     string `json:"ip-address"`
				Prefix        int    `json:"prefix"`
			}{makeAddr("ipv4", "127.0.0.1", 8)},
		},
	}

	ips := filterGuestIPs(ifaces)
	if len(ips) != 0 {
		t.Errorf("expected no IPs from lo0, got %v", ips)
	}
}

func TestFilterGuestIPs_MultipleInterfaces(t *testing.T) {
	ifaces := []GuestNetworkInterface{
		{
			Name: "lo",
			IPAddresses: []struct {
				IPAddressType string `json:"ip-address-type"`
				IPAddress     string `json:"ip-address"`
				Prefix        int    `json:"prefix"`
			}{makeAddr("ipv4", "127.0.0.1", 8)},
		},
		{
			Name: "eth0",
			IPAddresses: []struct {
				IPAddressType string `json:"ip-address-type"`
				IPAddress     string `json:"ip-address"`
				Prefix        int    `json:"prefix"`
			}{makeAddr("ipv4", "192.168.1.100", 24)},
		},
		{
			Name: "eth1",
			IPAddresses: []struct {
				IPAddressType string `json:"ip-address-type"`
				IPAddress     string `json:"ip-address"`
				Prefix        int    `json:"prefix"`
			}{makeAddr("ipv6", "2001:db8::1", 64)},
		},
	}

	ips := filterGuestIPs(ifaces)
	if len(ips) != 2 {
		t.Fatalf("expected 2 IPs, got %d: %v", len(ips), ips)
	}
}

func TestFilterGuestIPs_EmptyInput(t *testing.T) {
	ips := filterGuestIPs(nil)
	if len(ips) != 0 {
		t.Errorf("expected empty result for nil input, got %v", ips)
	}
}

func TestFilterGuestIPs_SkipsLoopbackIPOnNonLoopbackIface(t *testing.T) {
	// An interface not named lo/lo0 but hosting a loopback IP should still be filtered.
	ifaces := []GuestNetworkInterface{
		{
			Name: "dummy0",
			IPAddresses: []struct {
				IPAddressType string `json:"ip-address-type"`
				IPAddress     string `json:"ip-address"`
				Prefix        int    `json:"prefix"`
			}{makeAddr("ipv4", "127.0.0.1", 8)},
		},
	}

	ips := filterGuestIPs(ifaces)
	if len(ips) != 0 {
		t.Errorf("expected loopback IP to be excluded, got %v", ips)
	}
}
