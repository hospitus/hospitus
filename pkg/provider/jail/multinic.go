package jail

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Multi-NIC allows jails to have multiple network interfaces for:
//   - Separate management and data networks
//   - DMZ configurations
//   - Multi-homed services
//   - Network isolation between services
//
// Each interface can be connected to a different bridge with its own
// IP configuration (IPv4/IPv6), MTU, and routing.
//
// Example use cases:
//   - Web server with public (DMZ) and private (database) interfaces
//   - Router jail with multiple network segments
//   - Load balancer with frontend and backend networks

// NetworkInterface represents a single network interface configuration
type NetworkInterface struct {
	// Name is the interface name inside the jail (e.g., "eth0", "lan", "wan")
	// If empty, uses default naming (epairNb)
	Name string `json:"name,omitempty"`

	// Bridge is the bridge to connect this interface to
	Bridge string `json:"bridge"`

	// BridgeFlags are the flags applied to the bridge member (e.g., "private")
	BridgeFlags []string `json:"bridge_flags,omitempty"`

	// VLAN tag for the interface (0 = untagged)
	VLAN int `json:"vlan,omitempty"`

	// IPv4 configuration
	IPv4Address string `json:"ipv4_address,omitempty"` // CIDR notation: 10.0.0.2/24
	IPv4Gateway string `json:"ipv4_gateway,omitempty"` // Default gateway for this interface

	// IPv6 configuration
	IPv6Address string `json:"ipv6_address,omitempty"` // CIDR notation: fd00::2/64
	IPv6Gateway string `json:"ipv6_gateway,omitempty"` // Default IPv6 gateway

	// DHCP configuration
	DHCPv4 bool `json:"dhcpv4,omitempty"` // Use DHCP for IPv4
	DHCPv6 bool `json:"dhcpv6,omitempty"` // Use DHCPv6 for IPv6
	SLAAC  bool `json:"slaac,omitempty"`  // Use SLAAC for IPv6

	// Interface options
	MTU         int      `json:"mtu,omitempty"`         // Interface MTU (0 = default)
	MAC         string   `json:"mac,omitempty"`         // Custom MAC address
	Description string   `json:"description,omitempty"` // Interface description
	Primary     bool     `json:"primary,omitempty"`     // Is this the primary/default interface
	Routes      []Route  `json:"routes,omitempty"`      // Additional routes via this interface
	DNSServers  []string `json:"dns_servers,omitempty"` // DNS servers for this interface

	// Internal - populated after creation
	HostInterface string `json:"host_interface,omitempty"` // epairNa on host side
	JailInterface string `json:"jail_interface,omitempty"` // epairNb in jail
}

// Route represents a network route
type Route struct {
	// Destination network (CIDR) or "default"
	Destination string `json:"destination"`
	// Gateway IP address
	Gateway string `json:"gateway"`
	// Metric for route preference (lower = preferred)
	Metric int `json:"metric,omitempty"`
}

// MultiNICConfig represents multiple network interface configuration
type MultiNICConfig struct {
	// Interfaces is a list of network interfaces to configure
	Interfaces []NetworkInterface `json:"interfaces"`

	// DefaultInterface is the index of the default interface for routing (0-based)
	DefaultInterface int `json:"default_interface,omitempty"`

	// EnableIPv6 enables IPv6 stack in the jail
	EnableIPv6 bool `json:"enable_ipv6,omitempty"`

	// Hostname is set inside the jail
	Hostname string `json:"hostname,omitempty"`
}

// AddNetworkInterface adds a network interface to a running jail
func (p *JailProvider) AddNetworkInterface(ctx context.Context, handle provider.InstanceHandle, iface NetworkInterface) (*NetworkInterface, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	jailName := handle.ID

	// Check if jail is running
	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return nil, fmt.Errorf("failed to get jail state: %w", err)
	}
	if state != provider.StateRunning {
		return nil, fmt.Errorf("jail must be running to add network interface")
	}

	// Check if jail has VNET enabled
	hasVnet, err := p.jailHasVNET(ctx, jailName)
	if err != nil {
		return nil, err
	}
	if !hasVnet {
		return nil, fmt.Errorf("jail must have VNET enabled to add network interfaces")
	}

	// Every value below becomes an argument to ifconfig, route or jexec.
	if err := validateNetworkInterface(jailName, iface); err != nil {
		return nil, err
	}

	// Same reason EnableVNET does it: this entry point is reachable over the
	// API, and without the check it hands the jail an address another jail or a
	// bridge already holds.
	if iface.IPv4Address != "" {
		if err := p.refuseAddressInUse(jailName, iface.IPv4Address); err != nil {
			return nil, err
		}
	}

	output, err := p.cmd().CombinedOutput(ctx, "ifconfig", "epair", "create")
	if err != nil {
		return nil, fmt.Errorf("failed to create epair: %w (output: %s)", err, string(output))
	}

	epairA := strings.TrimSpace(string(output))
	epairB := epairBSide(epairA)

	// Every failure from here on leaves the pair behind otherwise, and the host
	// accumulates one orphaned epairNa per failed attach. EnableVNET solves it
	// the same way.
	attached := false
	defer func() {
		if !attached {
			_ = p.cmd().Run(context.WithoutCancel(ctx), "ifconfig", epairA, "destroy")
		}
	}()

	iface.HostInterface = epairA
	iface.JailInterface = epairB

	// Set MAC address if specified
	if iface.MAC != "" {
		if err := p.cmd().Run(ctx, "ifconfig", epairB, "ether", iface.MAC); err != nil {
			p.logWarn(ctx, "failed to set MAC address on jail interface", "jail", jailName, "interface", epairB, "mac", iface.MAC, logging.FieldError, err)
		}
	}

	// Set MTU if specified
	if iface.MTU > 0 {
		if err := p.cmd().Run(ctx, "ifconfig", epairA, "mtu", strconv.Itoa(iface.MTU)); err != nil {
			p.logWarn(ctx, "failed to set MTU on host interface", "jail", jailName, "interface", epairA, "mtu", iface.MTU, logging.FieldError, err)
		}
		if err := p.cmd().Run(ctx, "ifconfig", epairB, "mtu", strconv.Itoa(iface.MTU)); err != nil {
			p.logWarn(ctx, "failed to set MTU on jail interface", "jail", jailName, "interface", epairB, "mtu", iface.MTU, logging.FieldError, err)
		}
	}

	// Add epairA to bridge
	if iface.Bridge != "" {
		// Create the bridge if it is not there, as starting a jail on one does.
		// Without this, adding an interface on a bridge that does not exist yet
		// failed on the attach, and the message named the epair rather than the
		// missing bridge.
		//
		// The pool is empty on purpose: only the default bridge may take the
		// default one, and this is not it.
		if err := p.networkManager.EnsureBridge(ctx, iface.Bridge, ""); err != nil {
			_ = p.cmd().Run(ctx, "ifconfig", epairA, "destroy")
			return nil, fmt.Errorf("bridge %s: %w", iface.Bridge, err)
		}
		if err := p.networkManager.attachInterfaceToBridge(ctx, epairA, provider.NetworkSpec{
			Bridge:      iface.Bridge,
			VLAN:        iface.VLAN,
			BridgeFlags: iface.BridgeFlags,
		}); err != nil {
			// Cleanup; best-effort teardown, ignore error
			_ = p.cmd().Run(ctx, "ifconfig", epairA, "destroy")
			return nil, fmt.Errorf("failed to add %s to bridge %s: %w", epairA, iface.Bridge, err)
		}
	}

	// Bring up host interface
	if err := p.cmd().Run(ctx, "ifconfig", epairA, "up"); err != nil {
		return nil, fmt.Errorf("failed to bring up %s: %w", epairA, err)
	}

	// Assign epairB to jail
	if output, err := p.cmd().CombinedOutput(ctx, "ifconfig", epairB, "vnet", jailName); err != nil {
		return nil, fmt.Errorf("failed to assign %s to jail: %w (output: %s)", epairB, err, string(output))
	}

	// Rename interface inside jail if name specified
	jailIface := epairB
	if iface.Name != "" {
		if err := p.cmd().Run(ctx, "jexec", jailName, "ifconfig", epairB, "name", iface.Name); err == nil {
			jailIface = iface.Name
			iface.JailInterface = iface.Name
		}
	}

	// Configure IPv4
	if iface.IPv4Address != "" {
		if output, err := p.cmd().CombinedOutput(ctx, "jexec", jailName, "ifconfig", jailIface, "inet", iface.IPv4Address); err != nil {
			return nil, fmt.Errorf("failed to configure IPv4: %w (output: %s)", err, string(output))
		}
	} else if iface.DHCPv4 {
		// Not through p.cmd(): execx runs a command to completion, and this one
		// must outlive the call. Not CommandContext either — binding it to the
		// request context has the kernel kill dhclient as soon as the caller
		// returns, and the interface loses its lease moments later.
		p.startDHCPClient(ctx, "dhclient", jailName, jailIface)
	}

	// Configure IPv6
	switch {
	case iface.IPv6Address != "":
		if _, err := p.cmd().CombinedOutput(ctx, "jexec", jailName, "ifconfig", jailIface, "inet6", iface.IPv6Address); err != nil {
			p.logWarn(ctx, "failed to configure IPv6 on jail interface", "jail", jailName, "interface", jailIface, "ipv6", iface.IPv6Address, logging.FieldError, err)
		}
	case iface.SLAAC:
		// Enable accept_rtadv for SLAAC
		if err := p.cmd().Run(ctx, "jexec", jailName, "ifconfig", jailIface, "inet6", "accept_rtadv"); err != nil {
			p.logWarn(ctx, "failed to enable SLAAC accept_rtadv", "jail", jailName, "interface", jailIface, logging.FieldError, err)
		}
	case iface.DHCPv6:
		p.startDHCPClient(ctx, "dhcp6c", jailName, jailIface)
	}

	// Bring up interface
	if err := p.cmd().Run(ctx, "jexec", jailName, "ifconfig", jailIface, "up"); err != nil {
		p.logWarn(ctx, "failed to bring up jail interface", "jail", jailName, "interface", jailIface, logging.FieldError, err)
	}

	// Add routes
	for _, route := range iface.Routes {
		var err error
		if route.Destination == "default" {
			err = p.cmd().Run(ctx, "jexec", jailName, "route", "add", "-net", "default", route.Gateway)
		} else {
			err = p.cmd().Run(ctx, "jexec", jailName, "route", "add", "-net", route.Destination, route.Gateway)
		}
		if err != nil {
			p.logWarn(ctx, "failed to add route", "jail", jailName, "destination", route.Destination, "gateway", route.Gateway, logging.FieldError, err)
		}
	}

	// Add default gateway if specified and this is primary
	if iface.Primary || iface.IPv4Gateway != "" {
		if iface.IPv4Gateway != "" {
			if err := p.cmd().Run(ctx, "jexec", jailName, "route", "add", "default", iface.IPv4Gateway); err != nil {
				p.logWarn(ctx, "failed to add IPv4 default gateway", "jail", jailName, "gateway", iface.IPv4Gateway, logging.FieldError, err)
			}
		}
	}

	// Add IPv6 default gateway
	if iface.IPv6Gateway != "" {
		if err := p.cmd().Run(ctx, "jexec", jailName, "route", "-6", "add", "default", iface.IPv6Gateway); err != nil {
			p.logWarn(ctx, "failed to add IPv6 default gateway", "jail", jailName, "gateway", iface.IPv6Gateway, logging.FieldError, err)
		}
	}

	// Set description if specified
	if iface.Description != "" {
		if err := p.cmd().Run(ctx, "jexec", jailName, "ifconfig", jailIface, "description", iface.Description); err != nil {
			p.logWarn(ctx, "failed to set interface description", "jail", jailName, "interface", jailIface, logging.FieldError, err)
		}
	}

	// Record the interface, or it lasts only until the jail stops.
	//
	// Everything above is runtime work: an epair is created, moved in and
	// addressed. None of it was written down, so the interface vanished on the
	// next restart without a word — "ifconfig -l" listed eth1 before and not
	// after. StartInstance builds one interface per entry in Networks, so an
	// entry here is what brings it back.
	if err := p.recordNetworkInterface(jailName, iface, jailIface); err != nil {
		p.logWarn(ctx, "interface added but not persisted; it will not survive a restart",
			"jail", jailName, "interface", jailIface, logging.FieldError, err)
	}

	attached = true
	return &iface, nil
}

// recordNetworkInterface appends an added interface to the jail's saved
// networks so StartInstance recreates it.
//
// The jail-side name goes in the spec's ID, which nothing else reads: without
// it the interface would come back as epairNb, and anything inside the jail
// configured against eth1 would miss it.
// upsertNetworkSpec replaces the entry describing the same interface, or
// appends when there is none.
func upsertNetworkSpec(list []provider.NetworkSpec, spec provider.NetworkSpec) []provider.NetworkSpec {
	for i := range list {
		if list[i].ID == spec.ID {
			list[i] = spec
			return list
		}
	}
	return append(list, spec)
}

// recordNetworkInterface appends the interface to the jail's persisted
// networks, replacing an entry that already describes the same attachment.
//
// Appending unconditionally meant one logical attachment submitted twice came
// back as two entries, and the next start built two epairs for it.
func (p *JailProvider) recordNetworkInterface(jailName string, iface NetworkInterface, jailIface string) error {
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	cfg, err := p.loadJailConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load jail config: %w", err)
	}

	spec := provider.NetworkSpec{
		ID:     jailIface,
		Type:   provider.NetworkTypeBridge,
		Bridge: iface.Bridge,
		IPv4:   iface.IPv4Address,
		IPv6:   iface.IPv6Address,
		MTU:    iface.MTU,
	}
	if iface.DHCPv4 {
		spec.IPv4 = "dhcp"
	}

	cfg.Networks = upsertNetworkSpec(cfg.Networks, spec)
	cfg.Spec.Networks = upsertNetworkSpec(cfg.Spec.Networks, spec)

	return p.saveJailConfig(cfg, configPath)
}

// forgetNetworkInterface drops the persisted entries naming any of the given
// interfaces.
//
// The counterpart to recordNetworkInterface, which had none: destroying an
// epair left its entry in the jail's Networks, and the next start built the
// interface again — a removal that undid itself at the next reboot.
//
// Several names because one attachment is known under two: the host epair and
// the in-jail name, which may have been renamed after the pair was created.
func (p *JailProvider) forgetNetworkInterface(jailName string, names ...string) error {
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	cfg, err := p.loadJailConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load jail config: %w", err)
	}

	drop := func(specs []provider.NetworkSpec) []provider.NetworkSpec {
		kept := specs[:0:0]
		for i := range specs {
			if specs[i].ID != "" && slices.Contains(names, specs[i].ID) {
				continue
			}
			kept = append(kept, specs[i])
		}
		return kept
	}
	cfg.Networks = drop(cfg.Networks)
	cfg.Spec.Networks = drop(cfg.Spec.Networks)

	return p.saveJailConfig(cfg, configPath)
}

// RemoveNetworkInterface removes a network interface from a jail
func (p *JailProvider) RemoveNetworkInterface(ctx context.Context, handle provider.InstanceHandle, interfaceName string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	jailName := handle.ID

	// The name reaches "ifconfig <name> -vnet" and, below, an epair destroy,
	// both as root on the host — so it is checked for shape before either.
	//
	// Not ownership: the provider keeps no record of which interfaces a jail
	// holds once it is stopped, and ListNetworkInterfaces asks the guest, which
	// requires it to be running. The epair branch below is guarded by its own
	// prefix test instead.
	if err := validation.ValidateInterfaceName(interfaceName); err != nil {
		return fmt.Errorf("invalid interface name %q: %w", interfaceName, err)
	}
	// Check if jail is running
	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to get jail state: %w", err)
	}

	if state == provider.StateRunning {
		// Move interface out of jail
		_ = p.cmd().Run(ctx, "ifconfig", interfaceName, "-vnet", jailName) // best-effort; interface teardown continues regardless
	}

	// Find and destroy the epair
	// If interfaceName is epairNb, destroy epairNa
	// If interfaceName was renamed, we need to find the original epair
	// The prefix decides, not the suffix. Testing "b" first meant any renamed
	// interface ending in one took the epair branch: for "web" this computed
	// "wea" and ran "ifconfig wea destroy" as root, on an interface that has
	// nothing to do with this jail.
	if !strings.HasPrefix(interfaceName, "epair") {
		// Renamed interface: without tracking the original epair there is
		// nothing safe to destroy. The record still goes, or the next start
		// rebuilds an interface the operator asked to remove.
		if err := p.forgetNetworkInterface(jailName, interfaceName); err != nil {
			p.logWarn(ctx, "interface removed but still recorded; it will come back on the next start",
				"jail", jailName, "interface", interfaceName, logging.FieldError, err)
		}
		return nil
	}
	epairA := interfaceName
	if strings.HasSuffix(interfaceName, "b") {
		epairA = strings.TrimSuffix(interfaceName, "b") + "a"
	}

	if output, err := p.cmd().CombinedOutput(ctx, "ifconfig", epairA, "destroy"); err != nil {
		return fmt.Errorf("failed to destroy %s: %w (output: %s)", epairA, err, string(output))
	}

	// Both sides, because the caller may name either: a rollback passes the
	// host epair, an operator the name inside the jail.
	if err := p.forgetNetworkInterface(jailName, interfaceName,
		strings.TrimSuffix(epairA, "a")+"b", epairA); err != nil {
		p.logWarn(ctx, "interface destroyed but still recorded; it will come back on the next start",
			"jail", jailName, "interface", interfaceName, logging.FieldError, err)
	}

	return nil
}

// validateNetworkInterface checks every field that becomes a command argument.
//
// AddNetworkInterface passes the jail name, bridge, interface name, MAC, MTU,
// addresses, gateways and route destinations to ifconfig, route and jexec.
func validateNetworkInterface(jailName string, iface NetworkInterface) error {
	if err := validation.ValidateInstanceName(jailName); err != nil {
		return fmt.Errorf("invalid jail name %q: %w", jailName, err)
	}
	if iface.Bridge != "" {
		if err := validation.ValidateBridgeName(iface.Bridge); err != nil {
			return fmt.Errorf("invalid bridge %q: %w", iface.Bridge, err)
		}
	}
	if iface.Name != "" {
		if err := validation.ValidateInterfaceName(iface.Name); err != nil {
			return fmt.Errorf("invalid interface name %q: %w", iface.Name, err)
		}
	}
	if iface.MAC != "" && !validMAC(iface.MAC) {
		return fmt.Errorf("invalid MAC address %q", iface.MAC)
	}
	if iface.VLAN < 0 || iface.VLAN > 4094 {
		return fmt.Errorf("invalid VLAN %d: expected 0..4094", iface.VLAN)
	}
	if iface.Description != "" && strings.ContainsAny(iface.Description, "\r\n") {
		return fmt.Errorf("interface description must not contain a newline")
	}
	for _, f := range iface.BridgeFlags {
		if err := validation.ValidateBridgeFlag(f); err != nil {
			return fmt.Errorf("invalid bridge flag %q: %w", f, err)
		}
	}
	for _, ns := range iface.DNSServers {
		if err := validation.ValidateIPAddress(ns); err != nil {
			return fmt.Errorf("invalid DNS server %q: %w", ns, err)
		}
	}
	if iface.MTU != 0 && (iface.MTU < 68 || iface.MTU > 65535) {
		return fmt.Errorf("invalid MTU %d: expected 68..65535", iface.MTU)
	}
	for _, addr := range []string{iface.IPv4Address, iface.IPv6Address} {
		if addr == "" || addr == "dhcp" {
			continue
		}
		if err := validation.ValidateIPAddress(stripPrefix(addr)); err != nil {
			return fmt.Errorf("invalid address %q: %w", addr, err)
		}
	}
	for _, gw := range []string{iface.IPv4Gateway, iface.IPv6Gateway} {
		if gw == "" {
			continue
		}
		if err := validation.ValidateIPAddress(gw); err != nil {
			return fmt.Errorf("invalid gateway %q: %w", gw, err)
		}
	}
	for _, r := range iface.Routes {
		if r.Gateway != "" {
			if err := validation.ValidateIPAddress(r.Gateway); err != nil {
				return fmt.Errorf("invalid route gateway %q: %w", r.Gateway, err)
			}
		}
		if r.Destination != "" && r.Destination != "default" {
			if err := validation.ValidateIPAddress(stripPrefix(r.Destination)); err != nil {
				return fmt.Errorf("invalid route destination %q: %w", r.Destination, err)
			}
		}
	}
	return nil
}

// stripPrefix drops a CIDR suffix so the address itself can be checked.
func stripPrefix(addr string) string {
	if i := strings.Index(addr, "/"); i >= 0 {
		return addr[:i]
	}
	return addr
}

// validMAC reports whether s is a plain colon-separated MAC address.
func validMAC(s string) bool {
	_, err := net.ParseMAC(s)
	return err == nil
}

// startDHCPClient runs a DHCP client inside the jail and leaves it running.
//
// The process is deliberately detached from the request context — it holds the
// interface's lease for as long as the jail runs — and a goroutine waits on it
// so it does not linger as a zombie for the daemon's lifetime.
func (p *JailProvider) startDHCPClient(ctx context.Context, client, jailName, jailIface string) {
	cmd := exec.Command("jexec", jailName, client, jailIface) //nolint:gosec // G204: both names are validated above
	// A nil Env would hand the daemon's environment to a long-lived process
	// inside the jail.
	cmd.Env = provider.MinimalEnv()
	if err := cmd.Start(); err != nil {
		p.logWarn(ctx, "failed to start "+client, "jail", jailName, "interface", jailIface, logging.FieldError, err)
		return
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			p.logWarn(context.WithoutCancel(ctx), client+" exited",
				"jail", jailName, "interface", jailIface, logging.FieldError, err)
		}
	}()
}

// ListNetworkInterfaces lists all network interfaces in a jail

func (p *JailProvider) ListNetworkInterfaces(ctx context.Context, handle provider.InstanceHandle) ([]NetworkInterface, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	jailName := handle.ID

	// Check if jail is running
	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return nil, fmt.Errorf("failed to get jail state: %w", err)
	}
	if state != provider.StateRunning {
		return nil, fmt.Errorf("jail must be running to list interfaces")
	}

	// List interfaces
	output, err := p.cmd().Output(ctx, "jexec", jailName, "ifconfig", "-l")
	if err != nil {
		return nil, fmt.Errorf("failed to list interfaces: %w", err)
	}

	ifaceNames := strings.Fields(string(output))
	var interfaces []NetworkInterface

	for _, name := range ifaceNames {
		// Skip loopback
		if name == "lo0" {
			continue
		}

		iface := NetworkInterface{
			JailInterface: name,
		}

		// Get interface details
		output, err := p.cmd().Output(ctx, "jexec", jailName, "ifconfig", name)
		if err != nil {
			continue
		}

		ifaceOutput := string(output)

		// Parse IPv4 address
		if idx := strings.Index(ifaceOutput, "inet "); idx >= 0 {
			line := ifaceOutput[idx:]
			if endIdx := strings.Index(line, "\n"); endIdx >= 0 {
				line = line[:endIdx]
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				iface.IPv4Address = fields[1]
				// Check for netmask
				for i, f := range fields {
					if f == "netmask" && i+1 < len(fields) {
						// Convert hex netmask to CIDR
						iface.IPv4Address += "/" + hexNetmaskToCIDR(fields[i+1])
						break
					}
				}
			}
		}

		// Parse IPv6 address
		if idx := strings.Index(ifaceOutput, "inet6 "); idx >= 0 {
			line := ifaceOutput[idx:]
			if endIdx := strings.Index(line, "\n"); endIdx >= 0 {
				line = line[:endIdx]
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				iface.IPv6Address = fields[1]
			}
		}

		// Parse MTU
		if idx := strings.Index(ifaceOutput, "mtu "); idx >= 0 {
			line := ifaceOutput[idx+4:]
			if spaceIdx := strings.Index(line, " "); spaceIdx >= 0 {
				line = line[:spaceIdx]
			}
			if newlineIdx := strings.Index(line, "\n"); newlineIdx >= 0 {
				line = line[:newlineIdx]
			}
			iface.MTU, _ = strconv.Atoi(strings.TrimSpace(line))
		}

		// Parse MAC address
		if idx := strings.Index(ifaceOutput, "ether "); idx >= 0 {
			line := ifaceOutput[idx+6:]
			if spaceIdx := strings.Index(line, " "); spaceIdx >= 0 {
				line = line[:spaceIdx]
			}
			if newlineIdx := strings.Index(line, "\n"); newlineIdx >= 0 {
				line = line[:newlineIdx]
			}
			iface.MAC = strings.TrimSpace(line)
		}

		interfaces = append(interfaces, iface)
	}

	// ifconfig knows the addresses; only the saved config knows which bridge an
	// interface hangs off, and the name it was asked for. Without this the NAME
	// and BRIDGE columns of "hospitus jail network list" were printed empty for
	// every interface, including one that had just been reported as
	// "Jail Interface: eth1" on "Bridge: internal0".
	if cfg, err := p.loadJailConfig(filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))); err == nil {
		for i := range interfaces {
			interfaces[i].Name = interfaces[i].JailInterface
			for j := range cfg.Networks {
				spec := &cfg.Networks[j]
				if spec.IPv4 != "" && spec.IPv4 == interfaces[i].IPv4Address {
					interfaces[i].Bridge = spec.Bridge
					break
				}
				if spec.ID != "" && spec.ID == interfaces[i].JailInterface {
					interfaces[i].Bridge = spec.Bridge
					break
				}
			}
		}
	}

	return interfaces, nil
}

// ConfigureMultiNIC configures multiple network interfaces for a jail
func (p *JailProvider) ConfigureMultiNIC(ctx context.Context, handle provider.InstanceHandle, config MultiNICConfig) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	// Checked before anything is added, and whether or not there are interfaces
	// to add: a hostname refused after the loop would leave the jail half
	// configured for a value that was never going to be accepted.
	if config.Hostname != "" {
		if err := validation.ValidateJailParameterValue(config.Hostname); err != nil {
			return fmt.Errorf("invalid hostname %q: %w", config.Hostname, err)
		}
	}

	if len(config.Interfaces) == 0 && config.Hostname == "" {
		return nil
	}

	// Add each interface. Both names are kept: the host epair is what
	// RemoveNetworkInterface can destroy, and the in-jail name is what the
	// persisted record is filed under once a rename has happened.
	type addedInterface struct{ host, jail string }
	var addedInterfaces []addedInterface
	for i := range config.Interfaces {
		iface := &config.Interfaces[i]
		// Mark primary interface
		if i == config.DefaultInterface {
			iface.Primary = true
		}

		added, err := p.AddNetworkInterface(ctx, handle, *iface)
		if err != nil {
			return fmt.Errorf("failed to add interface %d: %w", i, err)
		}
		if added != nil {
			// The host side, not the display name: RemoveNetworkInterface
			// destroys an epair only when the name it is given still carries
			// the "epair" prefix, and AddNetworkInterface overwrites
			// JailInterface with the new name when the in-jail rename
			// succeeds. HostInterface keeps the prefix, and destroying epairNa
			// takes both sides with it.
			host := added.HostInterface
			if host == "" {
				host = added.JailInterface
			}
			if host == "" {
				host = added.Name
			}
			addedInterfaces = append(addedInterfaces, addedInterface{host: host, jail: added.JailInterface})
		}
	}

	// Set hostname if specified. The value was checked before the loop above,
	// so this cannot fail on a malformed name after interfaces were added.
	if config.Hostname != "" {
		if output, err := p.cmd().CombinedOutput(ctx, "jexec", handle.ID, "hostname", config.Hostname); err != nil {
			for _, added := range addedInterfaces {
				// Best-effort, but not silent: a rollback that fails leaves an
				// interface ConfigureMultiNIC reports as removed, and the
				// operator had no way to know.
				if rmErr := p.RemoveNetworkInterface(ctx, handle, added.host); rmErr != nil {
					p.logWarn(ctx, "failed to roll back interface", "jail", handle.ID,
						"interface", added.host, logging.FieldError, rmErr)
				}
				// RemoveNetworkInterface forgets the record under the name it
				// was given and the epair's two sides. A renamed interface is
				// filed under neither, so it is dropped by name here.
				if added.jail != "" && added.jail != added.host {
					if fErr := p.forgetNetworkInterface(handle.ID, added.jail); fErr != nil {
						p.logWarn(ctx, "interface rolled back but still recorded", "jail", handle.ID,
							"interface", added.jail, logging.FieldError, fErr)
					}
				}
			}
			return fmt.Errorf("failed to set hostname %q: %w (output: %s)", config.Hostname, err, string(output))
		}
	}

	return nil
}

// jailHasVNET checks if a jail has VNET enabled
func (p *JailProvider) jailHasVNET(ctx context.Context, jailName string) (bool, error) {
	output, err := p.cmd().Output(ctx, "jls", "-j", jailName, "-n", "vnet")
	if err != nil {
		return false, fmt.Errorf("failed to check VNET status: %w", err)
	}

	// Output is "vnet=1" or "vnet=0" or "vnet=new"
	line := strings.TrimSpace(string(output))
	return strings.Contains(line, "=1") || strings.Contains(line, "=new"), nil
}

// hexNetmaskToCIDR converts a hex netmask (like 0xffffff00) to CIDR prefix length
func hexNetmaskToCIDR(hex string) string {
	hex = strings.TrimPrefix(hex, "0x")
	if len(hex) != 8 {
		return "24" // Default
	}

	// Count bits
	bits := 0
	for _, c := range hex {
		var nibble int
		_, _ = fmt.Sscanf(string(c), "%x", &nibble)
		for nibble > 0 {
			bits += nibble & 1
			nibble >>= 1
		}
	}

	return strconv.Itoa(bits)
}

// GetDefaultRoute returns the default gateway for a jail
func (p *JailProvider) GetDefaultRoute(ctx context.Context, handle provider.InstanceHandle) (string, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return "", fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	output, err := p.cmd().Output(ctx, "jexec", handle.ID, "route", "-n", "get", "default")
	if err != nil {
		return "", nil // No default route
	}

	// Parse "gateway: x.x.x.x" from output
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gateway:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "gateway:")), nil
		}
	}

	return "", nil
}

// SetDefaultRoute sets the default gateway for a jail
func (p *JailProvider) SetDefaultRoute(ctx context.Context, handle provider.InstanceHandle, gateway string, ipv6 bool) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	jailName := handle.ID

	// Validated before the delete below: the current default route goes first,
	// and a gateway the add then refuses leaves the jail with none at all —
	// which includes an address of the wrong family, since "route -6 add" will
	// not take an IPv4 gateway.
	if err := validation.ValidateIPAddress(gateway); err != nil {
		return fmt.Errorf("invalid gateway %q: %w", gateway, err)
	}
	if addr := net.ParseIP(gateway); addr == nil || (addr.To4() != nil) == ipv6 {
		family := "IPv4"
		if ipv6 {
			family = "IPv6"
		}
		return fmt.Errorf("gateway %q is not an %s address", gateway, family)
	}

	// Delete existing default route
	if ipv6 {
		// best-effort; deleting a non-existent route is expected to fail
		_ = p.cmd().Run(ctx, "jexec", jailName, "route", "-6", "delete", "default")
		if output, err := p.cmd().CombinedOutput(ctx, "jexec", jailName, "route", "-6", "add", "default", gateway); err != nil {
			return fmt.Errorf("failed to set IPv6 default route: %w (output: %s)", err, string(output))
		}
	} else {
		// best-effort; deleting a non-existent route is expected to fail
		_ = p.cmd().Run(ctx, "jexec", jailName, "route", "delete", "default")
		if output, err := p.cmd().CombinedOutput(ctx, "jexec", jailName, "route", "add", "default", gateway); err != nil {
			return fmt.Errorf("failed to set default route: %w (output: %s)", err, string(output))
		}
	}

	return nil
}
