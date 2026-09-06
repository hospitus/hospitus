package config

import (
	"bufio"
	"os"
	"strings"

	"github.com/hospitus/hospitus/pkg/logging"
)

// Config holds hospitusd configuration
type Config struct {
	IPPool string

	// Network automation settings
	EnableIPForwarding bool   `default:"true"` // Auto-enable IP forwarding
	EnableNAT          bool   `default:"true"` // Auto-configure NAT rules
	ExternalInterface  string `default:"auto"` // External interface for NAT (auto-detect or specify like "em0")
	FirewallType       string `default:"pf"`   // Firewall type: "pf", "ipfw", or "none"
	DefaultGateway     string `default:"auto"` // Default gateway for jails (auto-detect or specify IP)
	AutoCreateBridges  bool   `default:"true"` // Auto-create bridge interfaces if missing
	NATNetwork         string `default:"auto"` // Network to NAT (auto-detect from IP pool or specify like "10.0.0.0/24")

	// Advanced network settings
	BridgePrefix string `default:"hospitus"`  // Prefix for auto-created bridges (default: hospitus, so the first bridge is hospitus0)
	PFAnchorName string `default:"hospitus"`  // PF anchor name for NAT rules
	EnableIPv6   bool   `default:"false"`     // Enable IPv6 forwarding and NAT
	IPv6Prefix   string `default:"fd00::/48"` // ULA prefix for jail IPv6 allocation

	// AllowedPhysicalDisks lists the host devices a VM may be given raw access
	// to. Passthrough hands the guest a real disk, so this is default-deny:
	// an empty list permits nothing, and a device has to be named here before
	// any manifest can ask for it.
	AllowedPhysicalDisks []string
}

// configPaths holds the default configuration file locations, most specific
// first. Only root-owned system directories are used: hospitusd runs as root, so
// reading configuration from $HOME or the current working directory would let
// an unprivileged user influence the daemon. It is a package variable so tests
// can override it deterministically. Operators who need another location must
// pass it explicitly to LoadConfig.
var configPaths = []string{
	"/usr/local/etc/hospitus/hospitusd.conf", // FreeBSD
	"/etc/hospitus/hospitusd.conf",           // Linux
}

// DefaultConfigPaths returns the default configuration file paths to try.
func DefaultConfigPaths() []string {
	return configPaths
}

// LoadConfig loads configuration from file
// Returns nil if file doesn't exist (not an error)
func LoadConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Not an error, file just doesn't exist
		}
		return nil, err
	}
	defer file.Close()

	// Initialize with defaults - booleans default to true for network automation
	config := &Config{
		EnableIPForwarding: true,
		EnableNAT:          true,
		AutoCreateBridges:  true,
	}
	scanner := bufio.NewScanner(file)
	// Allow long lines (e.g. large IP pools); the default 64 KiB limit would
	// otherwise silently truncate and error out on oversized lines.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse key = value
		if !strings.Contains(line, "=") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Remove surrounding quotes if present
		value = strings.Trim(value, "\"'")

		// Parse known keys
		switch key {
		case "ip_pool":
			config.IPPool = value
		case "enable_ip_forwarding":
			config.EnableIPForwarding = parseBool(value, true)
		case "enable_nat":
			config.EnableNAT = parseBool(value, true)
		case "external_interface":
			config.ExternalInterface = value
		case "firewall_type":
			config.FirewallType = value
		case "default_gateway":
			config.DefaultGateway = value
		case "auto_create_bridges":
			config.AutoCreateBridges = parseBool(value, true)
		case "nat_network":
			config.NATNetwork = value
		case "bridge_prefix":
			config.BridgePrefix = value
		case "pf_anchor_name":
			config.PFAnchorName = value
		case "enable_ipv6":
			config.EnableIPv6 = parseBool(value, false)
		case "ipv6_prefix":
			config.IPv6Prefix = value
		case "allowed_physical_disks":
			config.AllowedPhysicalDisks = strings.Fields(value)
		default:
			logging.Warn("Unknown configuration key ignored", "key", key, "path", path)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Apply defaults so LoadConfig and LoadConfigWithDefaults share identical
	// semantics for a loaded file.
	applyDefaults(config)

	return config, nil
}

// LoadConfigWithDefaults tries to load config from default paths
// Returns config with defaults if no file found (not an error)
func LoadConfigWithDefaults() (*Config, error) {
	for _, path := range DefaultConfigPaths() {
		config, err := LoadConfig(path)
		if err != nil {
			return nil, err
		}
		if config != nil {
			// LoadConfig already applied defaults.
			return config, nil
		}
	}

	// No config file found, return config with defaults
	// Initialize booleans to true for automatic network setup
	config := &Config{
		EnableIPForwarding: true,
		EnableNAT:          true,
		AutoCreateBridges:  true,
	}
	applyDefaults(config)
	return config, nil
}

// applyDefaults sets default values for unset configuration options
func applyDefaults(config *Config) {
	// Network automation defaults - enabled by default for automatic setup
	if config.ExternalInterface == "" {
		config.ExternalInterface = "auto"
	}
	if config.FirewallType == "" {
		config.FirewallType = "pf"
	}
	if config.DefaultGateway == "" {
		config.DefaultGateway = "auto"
	}
	if config.NATNetwork == "" {
		config.NATNetwork = "auto"
	}
	if config.BridgePrefix == "" {
		config.BridgePrefix = "hospitus"
	}
	if config.PFAnchorName == "" {
		config.PFAnchorName = "hospitus"
	}
	if config.IPv6Prefix == "" {
		config.IPv6Prefix = "fd00::/48"
	}

	// Boolean defaults are handled in LoadConfig() and LoadConfigWithDefaults()
	// by initializing them to true before parsing the config file
}

// parseBool parses a boolean value from string, with a default fallback
func parseBool(value string, defaultValue bool) bool {
	value = strings.ToLower(value)
	switch value {
	case "true", "yes", "1", "on", "enabled":
		return true
	case "false", "no", "0", "off", "disabled":
		return false
	default:
		return defaultValue
	}
}
