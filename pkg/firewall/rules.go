package firewall

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/hospitus/hospitus/pkg/validation"
)

// Protocol represents a network protocol
type Protocol string

const (
	ProtocolTCP Protocol = "tcp"
	ProtocolUDP Protocol = "udp"
)

// RuleType represents the type of firewall rule
type RuleType string

const (
	RuleTypeNAT      RuleType = "nat"
	RuleTypeRedirect RuleType = "rdr"
	RuleTypeFilter   RuleType = "filter"
)

// PortMapping represents a port forwarding rule
type PortMapping struct {
	// Unique identifier for this rule
	ID string `json:"id"`

	// Instance this rule belongs to (jail name, VM name)
	Instance string `json:"instance"`

	// Provider type (jail, bhyve, qemu)
	Provider string `json:"provider"`

	// Protocol (tcp, udp)
	Protocol Protocol `json:"protocol"`

	// Host port to listen on
	HostPort int `json:"host_port"`

	// Target IP address (jail/VM IP)
	TargetIP string `json:"target_ip"`

	// Target port in the jail/VM
	TargetPort int `json:"target_port"`

	// Optional: Specific host interface to bind to
	HostInterface string `json:"host_interface,omitempty"`

	// Optional: Description
	Description string `json:"description,omitempty"`

	// Whether this rule is currently active
	Active bool `json:"active"`
}

// Validate validates the port mapping
func (pm *PortMapping) Validate() error {
	if pm.Instance == "" {
		return fmt.Errorf("instance name is required")
	}
	// Instance names are interpolated into backend rule text (PF rule files,
	// nft rule comments, iptables comments); enforce a strict character set to
	// prevent rule/argument injection (CWE-78).
	if err := validation.ValidateInstanceName(pm.Instance); err != nil {
		return fmt.Errorf("invalid instance name: %w", err)
	}

	// Provider reaches the same rule text through GenerateID, and HostInterface
	// is interpolated into the rule itself.
	if pm.Provider != "" {
		if err := validation.ValidateProviderName(pm.Provider); err != nil {
			return fmt.Errorf("invalid provider: %w", err)
		}
	}
	if pm.HostInterface != "" {
		if err := validation.ValidateInterfaceName(pm.HostInterface); err != nil {
			return fmt.Errorf("invalid host interface: %w", err)
		}
	}

	if pm.Protocol != ProtocolTCP && pm.Protocol != ProtocolUDP {
		return fmt.Errorf("protocol must be 'tcp' or 'udp', got '%s'", pm.Protocol)
	}

	if pm.HostPort < 1 || pm.HostPort > 65535 {
		return fmt.Errorf("host_port must be between 1 and 65535, got %d", pm.HostPort)
	}

	if pm.TargetPort < 1 || pm.TargetPort > 65535 {
		return fmt.Errorf("target_port must be between 1 and 65535, got %d", pm.TargetPort)
	}

	if pm.TargetIP == "" {
		return fmt.Errorf("target_ip is required")
	}

	if ip := net.ParseIP(pm.TargetIP); ip == nil {
		return fmt.Errorf("invalid target_ip: %s", pm.TargetIP)
	}

	return nil
}

// GenerateID generates a unique ID for this port mapping.
//
// Every field that defines the rule takes part, HostInterface and TargetIP
// included: two mappings differing only in those would otherwise share an ID,
// and PF reads the re-add as a rule it already has and writes nothing, so the
// second mapping silently never exists.
//
// The separator is "/", which none of the validated fields can contain —
// instance, provider and interface names are alphanumerics with "-" and "_",
// and an address holds digits, "." or ":". A "-" would not separate anything:
// instance "web-jail" with provider "bhyve" and instance "web" with provider
// "jail-bhyve" would produce the same string.
func (pm *PortMapping) GenerateID() string {
	iface := pm.HostInterface
	if iface == "" {
		iface = "any"
	}
	target := pm.TargetIP
	if target == "" {
		target = "any"
	}
	return fmt.Sprintf("%s/%s/%s/%s/%d/%s/%d",
		pm.Instance,
		pm.Provider,
		pm.Protocol,
		iface,
		pm.HostPort,
		target,
		pm.TargetPort,
	)
}

// String returns a human-readable representation
func (pm *PortMapping) String() string {
	return fmt.Sprintf("%s:%d -> %s:%d (%s)",
		pm.HostInterface, pm.HostPort,
		pm.TargetIP, pm.TargetPort,
		pm.Protocol,
	)
}

// NATRule represents a NAT/masquerade rule for outbound traffic
type NATRule struct {
	// Unique identifier
	ID string `json:"id"`

	// Instance this rule belongs to
	Instance string `json:"instance"`

	// Provider type
	Provider string `json:"provider"`

	// Source network (jail/VM network)
	SourceNetwork string `json:"source_network"`

	// Outbound interface for NAT
	OutInterface string `json:"out_interface"`

	// Whether this rule is currently active
	Active bool `json:"active"`
}

// Validate validates the NAT rule
func (nr *NATRule) Validate() error {
	if nr.Instance == "" {
		return fmt.Errorf("instance name is required")
	}
	// See PortMapping.Validate: instance names reach backend rule text.
	if err := validation.ValidateInstanceName(nr.Instance); err != nil {
		return fmt.Errorf("invalid instance name: %w", err)
	}

	if nr.SourceNetwork == "" {
		return fmt.Errorf("source_network is required")
	}

	_, _, err := net.ParseCIDR(nr.SourceNetwork)
	if err != nil {
		return fmt.Errorf("invalid source_network CIDR: %s", nr.SourceNetwork)
	}

	if nr.OutInterface == "" {
		return fmt.Errorf("out_interface is required")
	}

	return nil
}

// ParsePortSpec parses a port specification like "80", "80:8080", "tcp/80:8080"
func ParsePortSpec(spec string) (protocol Protocol, hostPort, targetPort int, err error) {
	protocol = ProtocolTCP
	portSpec := spec

	// Check for protocol prefix
	if strings.Contains(spec, "/") {
		parts := strings.SplitN(spec, "/", 2)
		switch strings.ToLower(parts[0]) {
		case "tcp":
			protocol = ProtocolTCP
		case "udp":
			protocol = ProtocolUDP
		default:
			return "", 0, 0, fmt.Errorf("invalid protocol: %s", parts[0])
		}
		portSpec = parts[1]
	}

	// Parse port mapping
	if strings.Contains(portSpec, ":") {
		parts := strings.SplitN(portSpec, ":", 2)
		hp, err := strconv.Atoi(parts[0])
		if err != nil {
			return "", 0, 0, fmt.Errorf("invalid host port: %s", parts[0])
		}
		tp, err := strconv.Atoi(parts[1])
		if err != nil {
			return "", 0, 0, fmt.Errorf("invalid target port: %s", parts[1])
		}
		hostPort = hp
		targetPort = tp
	} else {
		// Same port on both sides
		p, err := strconv.Atoi(portSpec)
		if err != nil {
			return "", 0, 0, fmt.Errorf("invalid port: %s", portSpec)
		}
		hostPort = p
		targetPort = p
	}

	if hostPort < 1 || hostPort > 65535 {
		return "", 0, 0, fmt.Errorf("host port out of range (1-65535): %d", hostPort)
	}
	if targetPort < 1 || targetPort > 65535 {
		return "", 0, 0, fmt.Errorf("target port out of range (1-65535): %d", targetPort)
	}

	return protocol, hostPort, targetPort, nil
}

// RuleSet represents a collection of firewall rules for an instance
type RuleSet struct {
	Instance     string        `json:"instance"`
	Provider     string        `json:"provider"`
	PortMappings []PortMapping `json:"port_mappings"`
	NATRules     []NATRule     `json:"nat_rules"`
}
