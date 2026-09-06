package firewall

import (
	"context"
	"fmt"
	"runtime"
)

// BackendType represents a firewall backend type
type BackendType string

const (
	BackendTypePF       BackendType = "pf"
	BackendTypeIPTables BackendType = "iptables"
	BackendTypeNFTables BackendType = "nftables"
)

// Backend is the interface that all firewall backends must implement
type Backend interface {
	// Name returns the backend name
	Name() BackendType

	// IsAvailable checks if this backend is available on the system
	IsAvailable() bool

	// Initialize initializes the backend (creates anchors, chains, etc.)
	Initialize(ctx context.Context) error

	// AddPortMapping adds a port forwarding rule
	AddPortMapping(ctx context.Context, mapping PortMapping) error

	// RemovePortMapping removes a port forwarding rule
	RemovePortMapping(ctx context.Context, mapping PortMapping) error

	// AddNATRule adds a NAT/masquerade rule
	AddNATRule(ctx context.Context, rule NATRule) error

	// RemoveNATRule removes a NAT rule
	RemoveNATRule(ctx context.Context, rule NATRule) error

	// ApplyRules applies all rules for an instance
	ApplyRules(ctx context.Context, ruleSet RuleSet) error

	// RemoveAllRules removes all rules for an instance
	RemoveAllRules(ctx context.Context, instance string) error

	// ListRules lists all active rules
	ListRules(ctx context.Context) ([]PortMapping, error)

	// Cleanup cleans up any resources on shutdown
	Cleanup(ctx context.Context) error
}

// DetectBackend detects the appropriate firewall backend for the current system
func DetectBackend() (BackendType, error) {
	switch runtime.GOOS {
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		return BackendTypePF, nil
	case "linux":
		// Prefer nftables over iptables
		nft := &NFTablesBackend{}
		if nft.IsAvailable() {
			return BackendTypeNFTables, nil
		}
		ipt := &IPTablesBackend{}
		if ipt.IsAvailable() {
			return BackendTypeIPTables, nil
		}
		return "", fmt.Errorf("no firewall backend available on Linux (tried nftables, iptables)")
	default:
		return "", fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
}

// NewBackend creates a new backend of the specified type
func NewBackend(backendType BackendType) (Backend, error) {
	switch backendType {
	case BackendTypePF:
		return NewPFBackend(), nil
	case BackendTypeIPTables:
		return NewIPTablesBackend(), nil
	case BackendTypeNFTables:
		return NewNFTablesBackend(), nil
	default:
		return nil, fmt.Errorf("unknown backend type: %s", backendType)
	}
}

// NewAutoBackend creates a new backend with automatic detection
func NewAutoBackend() (Backend, error) {
	backendType, err := DetectBackend()
	if err != nil {
		return nil, err
	}
	return NewBackend(backendType)
}
