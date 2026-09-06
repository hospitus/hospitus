package jail

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// VNETConfig represents VNET configuration for a jail.
type VNETConfig struct {
	Enabled      bool     `json:"enabled"`       // Enable VNET
	Interfaces   []string `json:"interfaces"`    // Network interfaces to add to jail
	Bridge       string   `json:"bridge"`        // Bridge to attach epair to
	IPv4Address  string   `json:"ipv4_address"`  // IPv4 address for jail interface
	IPv6Address  string   `json:"ipv6_address"`  // IPv6 address for jail interface
	DefaultRoute string   `json:"default_route"` // Default gateway
}

// EnableVNET enables VNET (virtual network stack) for a jail.
//
// VNET gives the jail its own complete network stack including:
//   - Network interfaces
//   - Routing tables
//   - Firewall rules
//   - Protocol stacks
//
// This provides complete network isolation and allows the jail to:
//   - Configure its own IP addresses
//   - Run its own firewall (pf, ipfw, etc.)
//   - Create tunnel interfaces
//   - Run routing daemons
//
// VNET uses epair(4) virtual Ethernet interfaces to connect jails
// to the host or to bridges.
//
// Example:
//
//	config := VNETConfig{
//	    Enabled: true,
//	    Bridge: "hospitus0",
//	    IPv4Address: "10.0.1.10/24",
//	    DefaultRoute: "10.0.1.1",
//	}
//	err := p.EnableVNET(ctx, handle, config)
func (p *JailProvider) EnableVNET(ctx context.Context, handle provider.InstanceHandle, config VNETConfig) error {
	// SECURITY: Validate handle
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance handle: %w", err)
	}

	// SECURITY: Validate all user-supplied VNET config fields before passing to system commands
	if config.Bridge != "" {
		if err := validation.ValidateBridgeName(config.Bridge); err != nil {
			return fmt.Errorf("invalid bridge name: %w", err)
		}
	}
	if config.IPv4Address != "" && config.IPv4Address != "dhcp" {
		if err := validation.ValidateIPAddress(config.IPv4Address); err != nil {
			return fmt.Errorf("invalid IPv4 address: %w", err)
		}
	}
	if config.IPv6Address != "" {
		if err := validation.ValidateIPAddress(config.IPv6Address); err != nil {
			return fmt.Errorf("invalid IPv6 address: %w", err)
		}
	}
	if config.DefaultRoute != "" {
		if err := validation.ValidateIPAddress(config.DefaultRoute); err != nil {
			return fmt.Errorf("invalid default route: %w", err)
		}
	}

	if !config.Enabled {
		return nil // Nothing to do
	}

	// The address goes through the same bookkeeping a start does: this entry
	// point is reachable over the API, and without the check it could hand the
	// jail an address another jail or a bridge already holds.
	if err := p.refuseAddressInUse(handle.ID, config.IPv4Address); err != nil {
		return err
	}

	// Check if jail is running
	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to get jail state: %w", err)
	}

	if state != provider.StateRunning {
		return fmt.Errorf("jail must be running to configure VNET")
	}

	// Create epair interfaces
	// epair consists of two interfaces: epairNa (host side) and epairNb (jail side)
	output, err := p.cmd().CombinedOutput(ctx, "ifconfig", "epair", "create")
	if err != nil {
		return fmt.Errorf("failed to create epair: %w (output: %s)", err, string(output))
	}

	epairName := strings.TrimSpace(string(output))
	epairA := epairName          // Host side (e.g., epair0a)
	epairB := epairBSide(epairA) // Jail side (e.g., epair0b)

	// destroyEpair cleans up epairA (and implicitly epairB) on error.
	// context.Background(), not ctx: a canceled request must still have its
	// half-built interface torn down, or it leaks on the host.
	destroyEpair := func() {
		_ = p.cmd().Run(context.Background(), "ifconfig", epairA, "destroy")
	}

	// If bridge is specified, add epairA to bridge
	if config.Bridge != "" {
		output, err = p.cmd().CombinedOutput(ctx, "ifconfig", config.Bridge, "addm", epairA)
		if err != nil {
			destroyEpair()
			return fmt.Errorf("failed to add %s to bridge %s: %w (output: %s)",
				epairA, config.Bridge, err, string(output))
		}
	}

	// Bring up host side interface
	if err := p.cmd().Run(ctx, "ifconfig", epairA, "up"); err != nil {
		destroyEpair()
		return fmt.Errorf("failed to bring up %s: %w", epairA, err)
	}

	// Assign epairB to jail
	output, err = p.cmd().CombinedOutput(ctx, "ifconfig", epairB, "vnet", handle.ID)
	if err != nil {
		destroyEpair()
		return fmt.Errorf("failed to assign %s to jail %s: %w (output: %s)",
			epairB, handle.ID, err, string(output))
	}

	// Configure IP address inside jail if specified
	if config.IPv4Address != "" {
		// Use jexec to configure interface inside jail
		output, err = p.cmd().CombinedOutput(ctx, "jexec", handle.ID, "ifconfig", epairB, "inet", config.IPv4Address)
		if err != nil {
			destroyEpair()
			return fmt.Errorf("failed to configure IP %s on %s: %w (output: %s)",
				config.IPv4Address, epairB, err, string(output))
		}

		// Bring up interface inside jail
		if err := p.cmd().Run(ctx, "jexec", handle.ID, "ifconfig", epairB, "up"); err != nil {
			destroyEpair()
			return fmt.Errorf("failed to bring up %s in jail: %w", epairB, err)
		}
	}

	// Configure IPv6 if specified
	if config.IPv6Address != "" {
		output, err = p.cmd().CombinedOutput(ctx, "jexec", handle.ID, "ifconfig", epairB, "inet6", config.IPv6Address)
		if err != nil {
			destroyEpair()
			return fmt.Errorf("failed to configure IPv6 %s on %s: %w (output: %s)",
				config.IPv6Address, epairB, err, string(output))
		}
	}

	// Configure default route if specified
	if config.DefaultRoute != "" {
		output, err = p.cmd().CombinedOutput(ctx, "jexec", handle.ID, "route", "add", "default", config.DefaultRoute)
		if err != nil {
			destroyEpair()
			return fmt.Errorf("failed to add default route %s: %w (output: %s)",
				config.DefaultRoute, err, string(output))
		}
	}

	// Record the host-side epair in the jail config, as start does: stop and
	// delete destroy what is listed there, and an epair recorded nowhere stays
	// on the host forever.
	p.recordVnetEpair(ctx, handle.ID, epairA)

	return nil
}

// recordVnetEpair appends a host-side epair to the jail's persisted list so
// stop/delete can destroy it. Failures are logged, not returned: the interface
// is already up and working, and refusing the operation over bookkeeping would
// leave it up anyway.
func (p *JailProvider) recordVnetEpair(ctx context.Context, jailName, epairA string) {
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	jailConfig, err := p.loadJailConfig(configPath)
	if err != nil {
		p.logWarn(ctx, "failed to load jail config to record VNET epair", "jail", jailName, "epair", epairA, logging.FieldError, err)
		return
	}

	for _, existing := range jailConfig.VnetEpairs {
		if existing == epairA {
			return
		}
	}
	jailConfig.VnetEpairs = append(jailConfig.VnetEpairs, epairA)
	if jailConfig.VnetEpair == "" {
		jailConfig.VnetEpair = epairA // The first epair, which stop and delete read back
	}

	if err := p.saveJailConfig(jailConfig, configPath); err != nil {
		p.logWarn(ctx, "failed to persist VNET epair in jail config", "jail", jailName, "epair", epairA, logging.FieldError, err)
	}
}

// DisableVNET removes VNET configuration from a jail.
func (p *JailProvider) DisableVNET(ctx context.Context, handle provider.InstanceHandle) error {
	// SECURITY: Validate handle
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance handle: %w", err)
	}

	// List interfaces in jail
	output, err := p.cmd().CombinedOutput(ctx, "jexec", handle.ID, "ifconfig", "-l")
	if err != nil {
		// Jail might not be running or no VNET enabled — not an error
		return nil
	}

	interfaces := strings.Fields(string(output))

	var firstErr error
	// Remove epair interfaces from jail
	for _, iface := range interfaces {
		if !strings.HasPrefix(iface, "epair") {
			continue
		}

		// Move interface back to host before destroying
		if mvErr := p.cmd().Run(ctx, "ifconfig", iface, "-vnet", handle.ID); mvErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to move %s out of jail %s: %w", iface, handle.ID, mvErr)
			}
			continue
		}

		// Destroy epair (destroying one side destroys both)
		baseName := strings.TrimSuffix(iface, "b")
		baseName = strings.TrimSuffix(baseName, "a") + "a"
		if destroyErr := p.cmd().Run(ctx, "ifconfig", baseName, "destroy"); destroyErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to destroy epair %s: %w", baseName, destroyErr)
			}
		}
	}

	// An interface renamed at start (eth1, say) no longer looks like an epair
	// from inside the jail, so the loop above skips it and its host side is
	// never destroyed. The config records every host-side epair; destroy what
	// is left there.
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", handle.ID))
	jailConfig, err := p.loadJailConfig(configPath)
	if err != nil {
		return firstErr
	}
	for _, epairA := range jailConfig.VnetEpairs {
		if epairA == "" {
			continue
		}
		if destroyErr := p.cmd().Run(ctx, "ifconfig", epairA, "destroy"); destroyErr != nil {
			// Already destroyed by the loop above is the common case, and
			// indistinguishable from a real failure here — debug, not warn.
			p.logDebug(ctx, "recorded VNET epair could not be destroyed", "jail", handle.ID, "epair", epairA, logging.FieldError, destroyErr)
		}
	}
	jailConfig.VnetEpairs = nil
	jailConfig.VnetEpair = ""
	if saveErr := p.saveJailConfig(jailConfig, configPath); saveErr != nil {
		p.logWarn(ctx, "failed to clear VNET epairs from jail config", "jail", handle.ID, logging.FieldError, saveErr)
	}

	return firstErr
}

// GetVNETStatus returns the VNET configuration status for a jail.
func (p *JailProvider) GetVNETStatus(ctx context.Context, handle provider.InstanceHandle) (*VNETConfig, error) {
	// SECURITY: Validate handle
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance handle: %w", err)
	}

	// Check if jail has vnet enabled
	output, err := p.cmd().CombinedOutput(ctx, "jls", "-j", handle.ID, "vnet")
	if err != nil {
		return nil, fmt.Errorf("failed to get VNET status: %w", err)
	}

	vnetStatus := strings.TrimSpace(string(output))

	config := &VNETConfig{
		Enabled: vnetStatus == "new" || vnetStatus == "1",
	}

	if !config.Enabled {
		return config, nil
	}

	// List interfaces in jail
	output, err = p.cmd().CombinedOutput(ctx, "jexec", handle.ID, "ifconfig", "-l")
	if err == nil {
		config.Interfaces = strings.Fields(string(output))
	}

	// Get IP addresses
	for _, iface := range config.Interfaces {
		if strings.HasPrefix(iface, "epair") || strings.HasPrefix(iface, "vtnet") || strings.HasPrefix(iface, "em") {
			// Get IPv4
			output, err = p.cmd().CombinedOutput(ctx, "jexec", handle.ID, "ifconfig", iface, "inet")
			if err == nil && strings.Contains(string(output), "inet ") {
				// Parse output to extract IP
				lines := strings.Split(string(output), "\n")
				for _, line := range lines {
					if strings.Contains(line, "inet ") {
						fields := strings.Fields(line)
						if len(fields) >= 2 {
							config.IPv4Address = fields[1]
							break
						}
					}
				}
			}
		}
	}

	return config, nil
}

// epairBSide returns the jail-side (b) interface name for an epair given its
// host-side (a) name, e.g. "epair0a" -> "epair0b". "ifconfig epair create"
// always returns the "a" side, so only the trailing "a" must change. A naive
// strings.Replace(name, "a", "b", 1) would instead rewrite the first "a" in
// "epair" itself ("epair0a" -> "epbir0a"), producing a non-existent interface.
func epairBSide(epairA string) string {
	return strings.TrimSuffix(epairA, "a") + "b"
}
