package jail

import (
	"testing"
)

func TestNetworkInterfaceStruct(t *testing.T) {
	iface := NetworkInterface{
		Name:        "lan0",
		Bridge:      "hospitus0",
		IPv4Address: "10.0.0.2/24",
		IPv4Gateway: "10.0.0.1",
		IPv6Address: "fd00::2/64",
		MTU:         1500,
		Primary:     true,
		Description: "LAN interface",
	}

	if iface.Name != "lan0" {
		t.Error("NetworkInterface Name mismatch")
	}
	if iface.Bridge != "hospitus0" {
		t.Error("NetworkInterface Bridge mismatch")
	}
	if iface.IPv4Address != "10.0.0.2/24" {
		t.Error("NetworkInterface IPv4Address mismatch")
	}
	if iface.IPv4Gateway != "10.0.0.1" {
		t.Error("NetworkInterface IPv4Gateway mismatch")
	}
	if iface.IPv6Address != "fd00::2/64" {
		t.Error("NetworkInterface IPv6Address mismatch")
	}
	if iface.MTU != 1500 {
		t.Error("NetworkInterface MTU mismatch")
	}
	if !iface.Primary {
		t.Error("NetworkInterface Primary should be true")
	}
}

func TestNetworkInterfaceDHCP(t *testing.T) {
	iface := NetworkInterface{
		Name:   "wan0",
		Bridge: "external0",
		DHCPv4: true,
		DHCPv6: false,
		SLAAC:  true,
	}

	if !iface.DHCPv4 {
		t.Error("DHCPv4 should be true")
	}
	if iface.DHCPv6 {
		t.Error("DHCPv6 should be false")
	}
	if !iface.SLAAC {
		t.Error("SLAAC should be true")
	}
}

func TestRouteStruct(t *testing.T) {
	route := Route{
		Destination: "192.168.0.0/16",
		Gateway:     "10.0.0.1",
		Metric:      100,
	}

	if route.Destination != "192.168.0.0/16" {
		t.Error("Route Destination mismatch")
	}
	if route.Gateway != "10.0.0.1" {
		t.Error("Route Gateway mismatch")
	}
	if route.Metric != 100 {
		t.Error("Route Metric mismatch")
	}
}

func TestRouteDefault(t *testing.T) {
	route := Route{
		Destination: "default",
		Gateway:     "10.0.0.1",
	}

	if route.Destination != "default" {
		t.Error("Route Destination should be 'default'")
	}
}

func TestMultiNICConfig(t *testing.T) {
	config := MultiNICConfig{
		Interfaces: []NetworkInterface{
			{
				Name:        "lan0",
				Bridge:      "hospitus0",
				IPv4Address: "10.0.0.2/24",
				IPv4Gateway: "10.0.0.1",
				Primary:     true,
			},
			{
				Name:        "dmz0",
				Bridge:      "dmz",
				IPv4Address: "172.16.0.2/24",
			},
			{
				Name:   "wan0",
				Bridge: "external",
				DHCPv4: true,
			},
		},
		DefaultInterface: 0,
		EnableIPv6:       true,
		Hostname:         "webserver",
	}

	if len(config.Interfaces) != 3 {
		t.Errorf("Expected 3 interfaces, got %d", len(config.Interfaces))
	}
	if config.DefaultInterface != 0 {
		t.Error("DefaultInterface should be 0")
	}
	if !config.EnableIPv6 {
		t.Error("EnableIPv6 should be true")
	}
	if config.Hostname != "webserver" {
		t.Error("Hostname mismatch")
	}

	// Check primary interface
	if !config.Interfaces[0].Primary {
		t.Error("First interface should be primary")
	}
	if config.Interfaces[1].Primary {
		t.Error("Second interface should not be primary")
	}
}

func TestMultiNICConfigRoutes(t *testing.T) {
	config := MultiNICConfig{
		Interfaces: []NetworkInterface{
			{
				Name:        "lan0",
				Bridge:      "hospitus0",
				IPv4Address: "10.0.0.2/24",
				Routes: []Route{
					{Destination: "192.168.0.0/16", Gateway: "10.0.0.1"},
					{Destination: "172.16.0.0/12", Gateway: "10.0.0.254"},
				},
			},
		},
	}

	if len(config.Interfaces[0].Routes) != 2 {
		t.Errorf("Expected 2 routes, got %d", len(config.Interfaces[0].Routes))
	}
}

func TestHexNetmaskToCIDR(t *testing.T) {
	tests := []struct {
		hex      string
		expected string
	}{
		{"0xffffffff", "32"},
		{"0xffffff00", "24"},
		{"0xffff0000", "16"},
		{"0xff000000", "8"},
		{"0xffffffc0", "26"},
		{"0xffffffe0", "27"},
		{"0xfffffff0", "28"},
		{"0xfffffff8", "29"},
		{"0xfffffffc", "30"},
	}

	for _, tt := range tests {
		t.Run(tt.hex, func(t *testing.T) {
			result := hexNetmaskToCIDR(tt.hex)
			if result != tt.expected {
				t.Errorf("hexNetmaskToCIDR(%s) = %s, expected %s", tt.hex, result, tt.expected)
			}
		})
	}
}

func TestNetworkInterfaceWithDNS(t *testing.T) {
	iface := NetworkInterface{
		Name:        "lan0",
		Bridge:      "hospitus0",
		IPv4Address: "10.0.0.2/24",
		DNSServers:  []string{"8.8.8.8", "8.8.4.4", "1.1.1.1"},
	}

	if len(iface.DNSServers) != 3 {
		t.Errorf("Expected 3 DNS servers, got %d", len(iface.DNSServers))
	}
	if iface.DNSServers[0] != "8.8.8.8" {
		t.Error("First DNS server should be 8.8.8.8")
	}
}

func TestNetworkInterfaceMAC(t *testing.T) {
	iface := NetworkInterface{
		Name:   "eth0",
		Bridge: "hospitus0",
		MAC:    "02:00:00:00:00:01",
	}

	if iface.MAC != "02:00:00:00:00:01" {
		t.Error("MAC address mismatch")
	}
}

func TestMultiNICConfigDualStack(t *testing.T) {
	// Dual-stack configuration (IPv4 + IPv6)
	config := MultiNICConfig{
		Interfaces: []NetworkInterface{
			{
				Name:        "eth0",
				Bridge:      "hospitus0",
				IPv4Address: "10.0.0.2/24",
				IPv4Gateway: "10.0.0.1",
				IPv6Address: "2001:db8::2/64",
				IPv6Gateway: "2001:db8::1",
				Primary:     true,
			},
		},
		EnableIPv6: true,
	}

	iface := config.Interfaces[0]
	if iface.IPv4Address == "" {
		t.Error("IPv4Address should be set")
	}
	if iface.IPv6Address == "" {
		t.Error("IPv6Address should be set")
	}
	if iface.IPv4Gateway == "" {
		t.Error("IPv4Gateway should be set")
	}
	if iface.IPv6Gateway == "" {
		t.Error("IPv6Gateway should be set")
	}
}

func TestMultiNICConfigIPv6Only(t *testing.T) {
	// IPv6-only configuration
	config := MultiNICConfig{
		Interfaces: []NetworkInterface{
			{
				Name:        "eth0",
				Bridge:      "hospitus0",
				IPv6Address: "2001:db8::2/64",
				IPv6Gateway: "2001:db8::1",
				SLAAC:       false,
			},
		},
		EnableIPv6: true,
	}

	iface := config.Interfaces[0]
	if iface.IPv4Address != "" {
		t.Error("IPv4Address should be empty for IPv6-only")
	}
	if iface.IPv6Address == "" {
		t.Error("IPv6Address should be set")
	}
}

func TestMultiNICConfigSLAAC(t *testing.T) {
	// SLAAC configuration (automatic IPv6)
	config := MultiNICConfig{
		Interfaces: []NetworkInterface{
			{
				Name:   "eth0",
				Bridge: "hospitus0",
				SLAAC:  true,
			},
		},
		EnableIPv6: true,
	}

	iface := config.Interfaces[0]
	if !iface.SLAAC {
		t.Error("SLAAC should be enabled")
	}
}

func TestNetworkInterfaceHostJailNames(t *testing.T) {
	iface := NetworkInterface{
		Name:          "lan0",
		Bridge:        "hospitus0",
		HostInterface: "epair0a",
		JailInterface: "epair0b",
	}

	if iface.HostInterface != "epair0a" {
		t.Error("HostInterface mismatch")
	}
	if iface.JailInterface != "epair0b" {
		t.Error("JailInterface mismatch")
	}
}

func TestMultiNICTypicalWebServer(t *testing.T) {
	// Typical web server: public + private network
	config := MultiNICConfig{
		Interfaces: []NetworkInterface{
			{
				Name:        "public",
				Bridge:      "dmz",
				IPv4Address: "203.0.113.10/24",
				IPv4Gateway: "203.0.113.1",
				Primary:     true,
				Description: "Public interface",
			},
			{
				Name:        "private",
				Bridge:      "internal",
				IPv4Address: "10.0.0.10/24",
				Description: "Private interface to database",
				Routes: []Route{
					{Destination: "10.0.0.0/8", Gateway: "10.0.0.1"},
				},
			},
		},
		Hostname: "webserver",
	}

	if len(config.Interfaces) != 2 {
		t.Error("Should have 2 interfaces")
	}
	if config.Interfaces[0].Description != "Public interface" {
		t.Error("First interface description mismatch")
	}
	if len(config.Interfaces[1].Routes) != 1 {
		t.Error("Second interface should have 1 route")
	}
}
