package client

import (
	"context"
	"fmt"
	"net/url"

	"github.com/hospitus/hospitus/pkg/provider"
)

// VNETConfig represents VNET configuration
type VNETConfig struct {
	Enabled      bool     `json:"enabled"`
	Interfaces   []string `json:"interfaces,omitempty"`
	Bridge       string   `json:"bridge,omitempty"`
	IPv4Address  string   `json:"ipv4_address,omitempty"`
	IPv6Address  string   `json:"ipv6_address,omitempty"`
	DefaultRoute string   `json:"default_route,omitempty"`
}

// GetVNETStatus retrieves VNET configuration status for a jail
func (c *Client) GetVNETStatus(ctx context.Context, instanceID string) (*VNETConfig, error) {
	var result struct {
		VNET *VNETConfig `json:"vnet"`
	}

	path := fmt.Sprintf("/api/v1/instances/%s/freebsd/vnet", url.PathEscape(instanceID))
	if err := c.doRequest(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}

	return result.VNET, nil
}

// EnableVNET enables VNET for a jail
func (c *Client) EnableVNET(ctx context.Context, instanceID string, config VNETConfig) error {
	path := fmt.Sprintf("/api/v1/instances/%s/freebsd/vnet", url.PathEscape(instanceID))
	return c.doRequest(ctx, "POST", path, config, nil)
}

// DisableVNET disables VNET for a jail
func (c *Client) DisableVNET(ctx context.Context, instanceID string) error {
	path := fmt.Sprintf("/api/v1/instances/%s/freebsd/vnet", url.PathEscape(instanceID))
	return c.doRequest(ctx, "DELETE", path, nil, nil)
}

// PortMapping represents a port forwarding rule
type PortMapping struct {
	ID         string `json:"id"`
	Instance   string `json:"instance"`
	Protocol   string `json:"protocol"`
	HostPort   int    `json:"host_port"`
	TargetPort int    `json:"target_port"`
	TargetIP   string `json:"target_ip"`
}

// ExposePortRequest contains parameters for adding port forwarding
type ExposePortRequest struct {
	Instance   string `json:"instance"`
	Provider   string `json:"provider"`
	Protocol   string `json:"protocol"`
	HostPort   int    `json:"host_port"`
	TargetPort int    `json:"target_port"`
	TargetIP   string `json:"target_ip"`
}

// ExposePortResult contains the result of adding port forwarding (same as PortMapping)
type ExposePortResult = PortMapping

// ExposePort adds a port forwarding rule via the daemon API. The caller sets
// req.Provider to the instance's provider; when it is empty the server applies
// its own default rather than the client guessing one.
func (c *Client) ExposePort(ctx context.Context, instanceName string, req ExposePortRequest) (*ExposePortResult, error) {
	// Set instance name in request body (API expects it there)
	req.Instance = instanceName

	var result ExposePortResult
	if err := c.doRequest(ctx, "POST", "/api/v1/firewall/expose", req, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// UnexposePortRequest contains parameters for removing port forwarding
type UnexposePortRequest struct {
	Instance string `json:"instance"`
	HostPort int    `json:"host_port"`
	Protocol string `json:"protocol"`
}

// UnexposePort removes a port forwarding rule via the daemon API
func (c *Client) UnexposePort(ctx context.Context, instanceName string, hostPort int, protocol string) error {
	req := UnexposePortRequest{
		Instance: instanceName,
		HostPort: hostPort,
		Protocol: protocol,
	}

	return c.doRequest(ctx, "DELETE", "/api/v1/firewall/expose", req, nil)
}

// ListExposedPorts lists port forwarding rules for an instance via the daemon API
func (c *Client) ListExposedPorts(ctx context.Context, instanceName string) ([]PortMapping, error) {
	path := fmt.Sprintf("/api/v1/firewall/expose/%s", url.PathEscape(instanceName))

	var result []PortMapping
	if err := c.doRequest(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// NetworkInterfaceRequest contains parameters for adding a network interface
type NetworkInterfaceRequest struct {
	Name        string   `json:"name,omitempty"`
	Bridge      string   `json:"bridge"`
	IPv4Address string   `json:"ipv4_address,omitempty"`
	IPv4Gateway string   `json:"ipv4_gateway,omitempty"`
	IPv6Address string   `json:"ipv6_address,omitempty"`
	IPv6Gateway string   `json:"ipv6_gateway,omitempty"`
	DHCPv4      bool     `json:"dhcpv4,omitempty"`
	DHCPv6      bool     `json:"dhcpv6,omitempty"`
	SLAAC       bool     `json:"slaac,omitempty"`
	MTU         int      `json:"mtu,omitempty"`
	MAC         string   `json:"mac,omitempty"`
	DNSServers  []string `json:"dns_servers,omitempty"`
	Primary     bool     `json:"primary,omitempty"`
	Description string   `json:"description,omitempty"`
}

// NetworkInterfaceInfo contains information about a network interface
type NetworkInterfaceInfo struct {
	Name          string   `json:"name"`
	Bridge        string   `json:"bridge"`
	IPv4Address   string   `json:"ipv4_address,omitempty"`
	IPv4Gateway   string   `json:"ipv4_gateway,omitempty"`
	IPv6Address   string   `json:"ipv6_address,omitempty"`
	IPv6Gateway   string   `json:"ipv6_gateway,omitempty"`
	DHCPv4        bool     `json:"dhcpv4,omitempty"`
	DHCPv6        bool     `json:"dhcpv6,omitempty"`
	SLAAC         bool     `json:"slaac,omitempty"`
	MTU           int      `json:"mtu,omitempty"`
	MAC           string   `json:"mac,omitempty"`
	DNSServers    []string `json:"dns_servers,omitempty"`
	Primary       bool     `json:"primary"`
	Description   string   `json:"description,omitempty"`
	HostInterface string   `json:"host_interface,omitempty"`
	JailInterface string   `json:"jail_interface,omitempty"`
}

// AddNetworkInterface adds a network interface to an instance
func (c *Client) AddNetworkInterface(ctx context.Context, instanceID string, req NetworkInterfaceRequest) (*NetworkInterfaceInfo, error) {
	path := fmt.Sprintf("/api/v1/instances/%s/interfaces", url.PathEscape(instanceID))
	var result NetworkInterfaceInfo
	if err := c.doRequest(ctx, "POST", path, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// RemoveNetworkInterface removes a network interface from an instance
func (c *Client) RemoveNetworkInterface(ctx context.Context, instanceID, interfaceName string) error {
	path := fmt.Sprintf("/api/v1/instances/%s/interfaces/%s",
		url.PathEscape(instanceID),
		url.PathEscape(interfaceName))
	return c.doRequest(ctx, "DELETE", path, nil, nil)
}

// ListNetworkInterfaces lists network interfaces for an instance
func (c *Client) ListNetworkInterfaces(ctx context.Context, instanceID string) ([]NetworkInterfaceInfo, error) {
	path := fmt.Sprintf("/api/v1/instances/%s/interfaces", url.PathEscape(instanceID))
	var result []NetworkInterfaceInfo
	if err := c.doRequest(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// ListPortForwardsVM lists port forwarding rules for a QEMU VM.
func (c *Client) ListPortForwardsVM(ctx context.Context, instanceID string) ([]provider.PortForward, error) {
	path := fmt.Sprintf("/api/v1/instances/%s/port-forwards", url.PathEscape(instanceID))
	var result struct {
		PortForwards []provider.PortForward `json:"port_forwards"`
	}
	if err := c.doRequest(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}
	return result.PortForwards, nil
}

// AddPortForwardVM adds a port forwarding rule to a QEMU VM.
func (c *Client) AddPortForwardVM(ctx context.Context, instanceID string, pf provider.PortForward) error {
	path := fmt.Sprintf("/api/v1/instances/%s/port-forwards", url.PathEscape(instanceID))
	return c.doRequest(ctx, "POST", path, pf, nil)
}

// RemovePortForwardVM removes a port forwarding rule from a QEMU VM.
func (c *Client) RemovePortForwardVM(ctx context.Context, instanceID, protocol string, hostPort int) error {
	path := fmt.Sprintf("/api/v1/instances/%s/port-forwards", url.PathEscape(instanceID))
	req := struct {
		Protocol string `json:"protocol"`
		HostPort int    `json:"host_port"`
	}{Protocol: protocol, HostPort: hostPort}
	return c.doRequest(ctx, "DELETE", path, req, nil)
}
