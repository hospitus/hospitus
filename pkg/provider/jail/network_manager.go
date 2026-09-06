package jail

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/hospitus/hospitus/pkg/config"
	"github.com/hospitus/hospitus/pkg/firewall"
	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
	"github.com/hospitus/hospitus/pkg/validation"
)

// NetworkManager handles all networking operations for jails
type NetworkManager struct {
	config      *config.Config
	firewallMgr *firewall.Manager
	firewallMu  sync.Mutex
	logger      *slog.Logger
	runner      execx.Runner
}

// NewNetworkManager creates a new network manager with the given configuration
func NewNetworkManager(cfg *config.Config, logger *slog.Logger) *NetworkManager {
	if logger == nil {
		logger = slog.Default().With("component", "jail-network")
	} else {
		logger = logger.With("component", "jail-network")
	}

	return &NetworkManager{
		config: cfg,
		logger: logger,
		runner: execx.Default(),
	}
}

// cmd returns the command runner, defaulting to the real os/exec backend when
// the manager was constructed without one (e.g. a bare struct literal in tests
// that does not inject a fake).
func (nm *NetworkManager) cmd() execx.Runner {
	if nm.runner == nil {
		return execx.Default()
	}
	return nm.runner
}

// initFirewall initializes the firewall manager if not already done.
// It caches only a successful initialization: a transient failure (e.g. PF not
// yet enabled) is returned to the caller and retried on the next call, instead
// of being memoized forever as sync.Once would.
func (nm *NetworkManager) initFirewall(ctx context.Context) error {
	nm.firewallMu.Lock()
	defer nm.firewallMu.Unlock()

	if nm.firewallMgr != nil {
		return nil
	}

	mgr, err := firewall.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create firewall manager: %w", err)
	}
	if err := mgr.Initialize(ctx); err != nil {
		return fmt.Errorf("failed to initialize firewall manager: %w", err)
	}
	nm.firewallMgr = mgr
	return nil
}

// CleanupFirewallRules removes all firewall rules for a jail
func (nm *NetworkManager) CleanupFirewallRules(ctx context.Context, jailName string) error {
	if nm.config.FirewallType == "none" {
		return nil
	}

	if err := nm.initFirewall(ctx); err != nil {
		return fmt.Errorf("failed to initialize firewall for cleanup of jail %q: %w", jailName, err)
	}

	return nm.firewallMgr.RemoveAllRules(ctx, jailName)
}

// SetupNetworking configures all necessary networking for a jail
// This is the main entry point that orchestrates all network setup
func (nm *NetworkManager) SetupNetworking(ctx context.Context, jailName, jailPath, ipAddress string, withDefaultGateway bool) error {
	nm.logger.Info("setting up jail networking", "jail", jailName, "ip", ipAddress)

	// Step 1: Enable IP forwarding if configured
	if nm.config.EnableIPForwarding {
		if err := nm.EnableIPForwarding(ctx); err != nil {
			nm.logger.Warn("failed to enable IP forwarding; continuing", "jail", jailName, logging.FieldError, err)
		} else {
			nm.logger.Debug("IP forwarding enabled", "jail", jailName)
		}
	}

	// Step 2: Configure NAT if enabled and IP is in private range
	if nm.config.EnableNAT && ipAddress != "" {
		if isPrivateIP(ipAddress) {
			if err := nm.ConfigureNAT(ctx, jailName, ipAddress); err != nil {
				nm.logger.Warn("failed to configure NAT; jail may not have internet access", "jail", jailName, "ip", ipAddress, logging.FieldError, err)
			} else {
				nm.logger.Debug("NAT configured", "jail", jailName, "ip", ipAddress)
			}
		}
	}

	// Step 3: Configure gateway in jail's rc.conf for boot time.
	// Compute gateway first so we can use it as a DNS fallback in step 4.
	//
	// Only the network carrying the jail's default route writes defaultrouter:
	// /etc/rc installs whatever is there, and a jail on two networks would
	// otherwise boot with a route to a segment that leads nowhere.
	gateway := ""
	if withDefaultGateway && nm.config.DefaultGateway != "" && nm.config.DefaultGateway != "none" {
		gateway = nm.config.DefaultGateway
		if gateway == "auto" {
			gateway = getGatewayForIP(ipAddress)
		}

		if gateway != "" {
			if err := nm.ConfigureGateway(ctx, jailPath, gateway); err != nil {
				nm.logger.Warn("failed to configure gateway", "jail", jailName, "gateway", gateway, logging.FieldError, err)
			} else {
				nm.logger.Debug("gateway configured in rc.conf", "jail", jailName, "gateway", gateway)
			}
		}
	}

	// Step 4: Configure DNS resolver in jail.
	// Pass the gateway so that loopback nameservers (127.0.0.1 / ::1) from the
	// host's resolv.conf can be replaced — inside a VNET jail those addresses
	// point to the jail's own loopback, not the host's resolver.
	if err := nm.ConfigureDNS(jailPath, gateway); err != nil {
		nm.logger.Warn("failed to configure DNS", "jail", jailName, "path", jailPath, logging.FieldError, err)
	} else {
		nm.logger.Debug("DNS configured", "jail", jailName, "path", jailPath)
	}

	// NOTE: Route configuration is handled by rc.conf at boot time
	// For non-VNET jails with ip4=new, routing is managed by the host system
	// The gateway configured in rc.conf will be applied when the jail's /etc/rc runs

	// Re-apply port forwarding rules from persistence
	if err := nm.ApplyPortForwards(ctx, jailName); err != nil {
		nm.logger.Warn("failed to re-apply port forwards", "jail", jailName, logging.FieldError, err)
	}

	return nil
}

// EnsureBridge creates a bridge if it doesn't exist and gives it the gateway
// address of ipPool.
//
// An empty ipPool means this bridge gets no gateway from us — not "use the
// default one". Only the default bridge may take the default pool, and the
// caller is what knows which bridge that is; falling back here as well put the
// default gateway on a named bridge and made the conflict check refuse the
// start.
func (nm *NetworkManager) EnsureBridge(ctx context.Context, bridgeName, ipPool string) error {
	if !nm.config.AutoCreateBridges {
		// Just check if it exists
		if !nm.bridgeExists(ctx, bridgeName) {
			return fmt.Errorf("bridge %s does not exist and auto_create_bridges is disabled", bridgeName)
		}
		return nil
	}

	activePool := ipPool

	// Create the bridge if it is missing. Whether it was just created or was
	// already there, the gateway address is applied afterwards: a bridge left
	// without one makes every jail attached to it unreachable, and a host that
	// already had the bridge would otherwise never get the address.
	if nm.bridgeExists(ctx, bridgeName) {
		nm.logger.Debug("bridge already exists", "bridge", bridgeName)
	} else {
		nm.logger.Info("creating bridge", "bridge", bridgeName)

		if err := nm.cmd().Run(ctx, "ifconfig", "bridge", "create", "name", bridgeName); err != nil {
			return fmt.Errorf("failed to create bridge %s: %w", bridgeName, err)
		}

		if err := nm.cmd().Run(ctx, "ifconfig", bridgeName, "up"); err != nil {
			return fmt.Errorf("failed to bring up bridge %s: %w", bridgeName, err)
		}
	}

	// Configure the gateway IP on the bridge (first address of the pool network).
	if activePool == "" {
		nm.logger.Warn("no IP pool configured for bridge; jails attached to it will have no gateway",
			"bridge", bridgeName)
		return nil
	}

	gatewayIP := nm.getGatewayIPFromSpecificPool(activePool)
	if gatewayIP == "" {
		nm.logger.Warn("could not derive a gateway address from the IP pool",
			"bridge", bridgeName, "pool", activePool)
		return nil
	}

	// Applying the address is idempotent: ifconfig alias on an address the
	// bridge already carries is a no-op, so re-running start does not disturb a
	// working bridge.
	if nm.bridgeHasAddress(ctx, bridgeName, gatewayIP) {
		nm.logger.Debug("bridge gateway already configured", "bridge", bridgeName, "ip", gatewayIP)
		return nil
	}

	// Check for IP conflicts before configuring
	if err := nm.DetectAndWarnIPConflicts(ctx, bridgeName, gatewayIP); err != nil {
		return fmt.Errorf("IP conflict check failed for bridge %q gateway %q: %w", bridgeName, gatewayIP, err)
	}

	nm.logger.Info("configuring bridge IP", "bridge", bridgeName, "ip", gatewayIP)
	if err := nm.cmd().Run(ctx, "ifconfig", bridgeName, "inet", gatewayIP, "alias"); err != nil {
		nm.logger.Warn("failed to configure bridge IP", "bridge", bridgeName, "ip", gatewayIP, logging.FieldError, err)
	} else {
		nm.logger.Debug("bridge IP configured", "bridge", bridgeName, "ip", gatewayIP)
	}

	return nil
}

// bridgeHasAddress reports whether the bridge already carries the given address,
// so EnsureBridge can stay idempotent across restarts.
func (nm *NetworkManager) bridgeHasAddress(ctx context.Context, bridgeName, addr string) bool {
	ipOnly := addr
	if idx := strings.Index(ipOnly, "/"); idx >= 0 {
		ipOnly = ipOnly[:idx]
	}

	out, err := nm.cmd().Output(ctx, "ifconfig", bridgeName)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "inet" && fields[1] == ipOnly {
			return true
		}
	}
	return false
}

// getGatewayIPFromSpecificPool calculates the gateway IP for a specific pool string
func (nm *NetworkManager) getGatewayIPFromSpecificPool(pool string) string {
	return gatewayIPFromPool(pool)
}

// gatewayIPFromPool returns "<gateway>/<prefix>" for a pool string, i.e. the
// first usable address of the pool network — the address EnsureBridge puts on
// the bridge. Returns "" when no gateway can be derived.
func gatewayIPFromPool(pool string) string {
	if pool == "" {
		return ""
	}

	var networkCIDR string
	parts := strings.Fields(pool)
	for _, part := range parts {
		if strings.Contains(part, "/") {
			networkCIDR = part
			break
		} else if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) > 0 {
				firstIP := net.ParseIP(rangeParts[0])
				if firstIP != nil && firstIP.To4() != nil {
					ip := firstIP.To4()
					networkCIDR = fmt.Sprintf("%d.%d.%d.0/24", ip[0], ip[1], ip[2])
					break
				}
			}
		}
	}

	if networkCIDR == "" {
		return ""
	}

	gatewayIP := getGatewayForIP(networkCIDR)
	if gatewayIP == "" {
		return ""
	}

	_, ipNet, err := net.ParseCIDR(networkCIDR)
	if err != nil {
		return ""
	}

	maskBits, _ := ipNet.Mask.Size()
	return fmt.Sprintf("%s/%d", gatewayIP, maskBits)
}

// EnableIPForwarding enables IP packet forwarding on the host
func (nm *NetworkManager) EnableIPForwarding(ctx context.Context) error {
	nm.logger.Debug("enabling IP forwarding")

	// Enable IPv4 forwarding
	if err := nm.cmd().Run(ctx, "sysctl", "net.inet.ip.forwarding=1"); err != nil {
		return fmt.Errorf("failed to enable IPv4 forwarding: %w", err)
	}

	// Enable IPv6 forwarding if configured
	if nm.config.EnableIPv6 {
		if err := nm.cmd().Run(ctx, "sysctl", "net.inet6.ip6.forwarding=1"); err != nil {
			nm.logger.Warn("failed to enable IPv6 forwarding", logging.FieldError, err)
		}
	}

	return nil
}

// ConfigureNAT sets up NAT rules for jail internet access
// jailName is required for proper rule tracking and cleanup
func (nm *NetworkManager) ConfigureNAT(ctx context.Context, jailName, ipAddress string) error {
	if nm.config.FirewallType == "none" {
		nm.logger.Debug("NAT disabled", "firewall_type", "none")
		return nil
	}

	if err := nm.initFirewall(ctx); err != nil {
		return fmt.Errorf("failed to initialize firewall for NAT setup: %w", err)
	}

	// Get external interface
	extIf := nm.config.ExternalInterface
	if extIf == "auto" {
		var err error
		extIf, err = nm.getDefaultInterface(ctx)
		if err != nil {
			return fmt.Errorf("failed to auto-detect external interface: %w", err)
		}
	}

	// Determine network to NAT
	network := nm.config.NATNetwork
	if network == "auto" {
		network = getNetworkCIDR(ipAddress)
	}

	// Use FirewallManager to setup NAT with proper ID tracking
	_, err := nm.firewallMgr.SetupNAT(ctx, jailName, "jail", network, extIf)
	if err != nil {
		return fmt.Errorf("failed to setup NAT: %w", err)
	}

	nm.logger.Info("NAT configured for jail", "jail", jailName, "network", network, "interface", extIf)
	return nil
}

// ConfigureGateway sets up default gateway inside the jail
func (nm *NetworkManager) ConfigureGateway(ctx context.Context, jailPath, gateway string) error {
	rcConfPath := fmt.Sprintf("%s/etc/rc.conf", jailPath)

	nm.logger.Debug("configuring default gateway in jail", "gateway", gateway, "rc_conf", rcConfPath)

	// Check if rc.conf exists and if gateway is already configured
	if nm.cmd().Run(ctx, "test", "-f", rcConfPath) == nil {
		// File exists, check if gateway is already configured
		if nm.cmd().Run(ctx, "grep", "-q", "defaultrouter", rcConfPath) == nil {
			nm.logger.Debug("gateway already configured; skipping", "gateway", gateway, "rc_conf", rcConfPath)
			return nil
		}
	}

	// Append gateway configuration (creates file if it doesn't exist)
	line := fmt.Sprintf("defaultrouter=%q\n", gateway)
	f, err := os.OpenFile(rcConfPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open rc.conf for gateway config: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("failed to write gateway to rc.conf: %w", err)
	}

	nm.logger.Info("gateway configured in jail", "gateway", gateway, "rc_conf", rcConfPath)
	return nil
}

// fallbackDNS is used when the host's resolv.conf only has loopback nameservers.
// Cloudflare's 1.1.1.1 and 1.0.0.1 are used as they are universally reachable
// once NAT is configured.
const (
	fallbackDNS  = "1.1.1.1"
	fallbackDNS2 = "1.0.0.1"
)

// ConfigureDNS copies DNS resolver configuration from host to jail.
// fallbackForLoopback is substituted for loopback nameservers (127.0.0.1 / ::1)
// that would be unreachable inside a VNET jail (typically the gateway IP).
// If fallbackForLoopback is empty, public DNS (1.1.1.1) is used instead.
func (nm *NetworkManager) ConfigureDNS(jailPath, fallbackForLoopback string) error {
	hostResolvConf := "/etc/resolv.conf"
	jailResolvConf := fmt.Sprintf("%s/etc/resolv.conf", jailPath)

	nm.logger.Debug("configuring DNS in jail", "jail_path", jailPath)

	// Determine the replacement for loopback addresses
	replacement := fallbackForLoopback
	if replacement == "" {
		replacement = fallbackDNS
	}

	content, err := os.ReadFile(hostResolvConf)
	if err != nil {
		if os.IsNotExist(err) {
			nm.logger.Warn("host resolv.conf not found; writing fallback DNS", "jail_path", jailPath)
			fallback := fmt.Sprintf("# Written by hospitusd (host has no resolv.conf)\nnameserver %s\nnameserver %s\n", fallbackDNS, fallbackDNS2)
			// resolv.conf must be world-readable so unprivileged processes inside
			// the jail can resolve DNS; 0600 would break name resolution.
			return os.WriteFile(jailResolvConf, []byte(fallback), 0o644) //nolint:gosec // G306: jail resolv.conf is intentionally world-readable
		}
		return fmt.Errorf("failed to read host resolv.conf: %w", err)
	}

	// Replace loopback nameservers — inside a VNET jail 127.0.0.1 / ::1 refer
	// to the jail's own loopback, not the host's local resolver.
	// Also filter out IPv6 nameservers since most jails don't have IPv6 routing.
	lines := strings.Split(string(content), "\n")
	hasRealDNS := false
	var filteredLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "nameserver") {
			// Preserve resolver directives (search / domain / options); the jail
			// needs these for correct name resolution. Only comments and blank
			// lines are dropped.
			if strings.HasPrefix(trimmed, "search") ||
				strings.HasPrefix(trimmed, "domain") ||
				strings.HasPrefix(trimmed, "options") {
				filteredLines = append(filteredLines, line)
			}
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		addr := fields[1]
		switch {
		case addr == "127.0.0.1" || addr == "::1" || strings.HasPrefix(addr, "127."):
			replaced := fmt.Sprintf("nameserver %s # replaced loopback by hospitusd", replacement)
			nm.logger.Debug("replaced loopback nameserver in jail resolv.conf",
				"original", addr, "replacement", replacement, "jail_path", jailPath)
			filteredLines = append(filteredLines, replaced)
		case strings.Contains(addr, ":"):
			// Skip IPv6 nameservers - jails typically don't have IPv6 routing
			nm.logger.Debug("skipping IPv6 nameserver in jail resolv.conf",
				"original", addr, "jail_path", jailPath)
		default:
			filteredLines = append(filteredLines, line)
			hasRealDNS = true
		}
	}
	lines = filteredLines

	// If no real (non-loopback) DNS was found after replacement, add public fallback
	if !hasRealDNS {
		lines = append(lines, fmt.Sprintf("nameserver %s", fallbackDNS2))
	}

	jailContent := strings.Join(lines, "\n")

	// If /etc/resolv.conf is a symlink (common in Linux rootfs),
	// remove it so we can write a real file.
	if info, err := os.Lstat(jailResolvConf); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(jailResolvConf); err != nil {
			nm.logger.Warn("failed to remove resolv.conf symlink",
				"jail_path", jailPath, logging.FieldError, err)
		} else {
			nm.logger.Debug("removed resolv.conf symlink for real file", "jail_path", jailPath)
		}
	}

	// resolv.conf must be world-readable so unprivileged processes inside the
	// jail can resolve DNS; 0600 would break name resolution.
	if err := os.WriteFile(jailResolvConf, []byte(jailContent), 0o644); err != nil { //nolint:gosec // G306: jail resolv.conf is intentionally world-readable
		return fmt.Errorf("failed to write jail resolv.conf: %w", err)
	}

	nm.logger.Info("DNS configured in jail", "jail_resolv_conf", jailResolvConf, "loopback_replacement", replacement)
	return nil
}

// ApplyPortForwards re-applies all persisted port forwarding rules for a jail.
// This is used during jail startup to ensure rules are active.
func (nm *NetworkManager) ApplyPortForwards(ctx context.Context, jailName string) error {
	if nm.config.FirewallType == "none" {
		return nil
	}

	if err := nm.initFirewall(ctx); err != nil {
		return fmt.Errorf("failed to initialize firewall for port forwards of jail %q: %w", jailName, err)
	}

	// List persisted rules for this instance
	mappings, err := nm.firewallMgr.ListExposedPorts(ctx, jailName)
	if err != nil {
		return fmt.Errorf("failed to list persisted ports: %w", err)
	}

	if len(mappings) == 0 {
		return nil
	}

	nm.logger.Info("restoring port forwarding rules", "jail", jailName, "count", len(mappings))

	for i := range mappings {
		m := &mappings[i]
		// Re-add each mapping to the backend
		if err := nm.firewallMgr.Backend().AddPortMapping(ctx, *m); err != nil {
			nm.logger.Warn("failed to restore port mapping",
				"jail", jailName,
				"host_port", m.HostPort,
				logging.FieldError, err)
		}
	}

	return nil
}

// AddJailRoute adds a default route inside a running jail
// Uses jexec -r to grant routing privileges to the command
func (nm *NetworkManager) AddJailRoute(ctx context.Context, jailName, gateway string) error {
	nm.logger.Debug("adding default route in jail", "jail", jailName, "gateway", gateway)

	// Use jexec -r to run route command with routing privileges
	output, err := nm.cmd().CombinedOutput(ctx, "jexec", "-r", jailName, "route", "add", "default", gateway)
	if err != nil {
		return fmt.Errorf("failed to add route: %w (output: %s)", err, string(output))
	}

	nm.logger.Info("default route added in jail", "jail", jailName, "gateway", gateway)
	return nil
}

// bridgeExists checks if a bridge interface exists
func (nm *NetworkManager) bridgeExists(ctx context.Context, name string) bool {
	return nm.cmd().Run(ctx, "ifconfig", name) == nil
}

// CheckIPConflicts checks if the given IP is already assigned to another interface
// Returns the conflicting interface name if found, empty string otherwise
func (nm *NetworkManager) CheckIPConflicts(ctx context.Context, targetIP, excludeInterface string) (string, error) {
	// Extract IP without CIDR mask
	ipOnly := targetIP
	if strings.Contains(targetIP, "/") {
		ipOnly = strings.Split(targetIP, "/")[0]
	}

	// Get all interfaces with their IPs
	output, err := nm.cmd().Output(ctx, "ifconfig", "-a")
	if err != nil {
		return "", fmt.Errorf("failed to get interface list: %w", err)
	}

	var currentInterface string
	lines := strings.Split(string(output), "\n")

	for _, line := range lines {
		// Interface line starts without whitespace
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			// Extract interface name (before the colon)
			parts := strings.Split(line, ":")
			if len(parts) > 0 {
				currentInterface = strings.TrimSpace(parts[0])
			}
		}

		// Check for inet line
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "inet ") {
			// Skip if this is the excluded interface
			if currentInterface == excludeInterface {
				continue
			}

			// Extract IP from "inet X.X.X.X netmask ..."
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				ifaceIP := fields[1]
				if ifaceIP == ipOnly {
					return currentInterface, nil
				}
			}
		}
	}

	return "", nil
}

// DetectAndWarnIPConflicts checks for IP conflicts and logs warnings
// This should be called during bridge setup to detect configuration issues
func (nm *NetworkManager) DetectAndWarnIPConflicts(ctx context.Context, bridgeName, bridgeIP string) error {
	conflictingIface, err := nm.CheckIPConflicts(ctx, bridgeIP, bridgeName)
	if err != nil {
		nm.logger.Warn("failed to check for IP conflicts", "bridge", bridgeName, "ip", bridgeIP, logging.FieldError, err)
		return nil // Don't fail on check errors, just warn
	}

	if conflictingIface != "" {
		nm.logger.Error("IP conflict detected on bridge", "bridge", bridgeName, "ip", bridgeIP, "interface", conflictingIface)
		nm.logger.Error("duplicate bridge IP will cause jail networking issues", "bridge", bridgeName, "ip", bridgeIP)
		nm.logger.Error("remove duplicate IP from interface", "interface", conflictingIface, "ip", strings.Split(bridgeIP, "/")[0], "command", fmt.Sprintf("doas ifconfig %s inet %s delete", conflictingIface, strings.Split(bridgeIP, "/")[0]))
		return fmt.Errorf("IP conflict: %s already assigned to %s", bridgeIP, conflictingIface)
	}

	return nil
}

// Helper functions

// isPrivateIP checks if an IP address is in a private range
func isPrivateIP(ipWithMask string) bool {
	// Extract IP from CIDR notation
	ipStr := ipWithMask
	if strings.Contains(ipWithMask, "/") {
		parts := strings.Split(ipWithMask, "/")
		ipStr = parts[0]
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	// Check private ranges
	privateRanges := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
	}

	for _, cidr := range privateRanges {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(ip) {
			return true
		}
	}

	return false
}

// getDefaultInterface returns the default route interface
func (nm *NetworkManager) getDefaultInterface(ctx context.Context) (string, error) {
	output, err := nm.cmd().Output(ctx, "route", "-n", "get", "default")
	if err != nil {
		return "", fmt.Errorf("failed to get default route: %w", err)
	}

	// Parse output for interface
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "interface:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return parts[1], nil
			}
		}
	}

	return "", fmt.Errorf("could not determine default interface from route output")
}

// getNetworkCIDR extracts network CIDR from an IP address
// E.g., "10.0.0.10/24" -> "10.0.0.0/24"
func getNetworkCIDR(ipWithMask string) string {
	if !strings.Contains(ipWithMask, "/") {
		// No mask, assume /24
		ipWithMask += "/24"
	}

	ip, network, err := net.ParseCIDR(ipWithMask)
	if err != nil {
		// Fallback to input
		return ipWithMask
	}

	// Return network address with mask
	_ = ip // Silence unused warning
	return network.String()
}

// getGatewayForIP determines the default gateway for a given IP
// Typically the first IP in the network (e.g., 10.0.0.1 for 10.0.0.0/24)
func getGatewayForIP(ipWithMask string) string {
	if !strings.Contains(ipWithMask, "/") {
		// No mask, can't determine gateway
		return ""
	}

	_, network, err := net.ParseCIDR(ipWithMask)
	if err != nil {
		return ""
	}

	// Get first IP in network (network address + 1)
	ip := network.IP
	ip = ip.To4()
	if ip == nil {
		// IPv6: use the first address in the network (typically ::1 suffix) as gateway.
		ip6 := make(net.IP, 16)
		copy(ip6, network.IP)
		ip6[15] = 1
		return ip6.String()
	}

	// Increment last octet
	gateway := make(net.IP, len(ip))
	copy(gateway, ip)
	gateway[3]++ // First usable IP

	return gateway.String()
}

// VNET Interface Management

// VNetInterface represents a VNET interface configuration
type VNetInterface struct {
	EpairA string // Host-side interface (attached to bridge)
	EpairB string // Jail-side interface
	Bridge string // Bridge to attach to
}

// CreateVNetInterface creates an epair interface for VNET jails
func (nm *NetworkManager) CreateVNetInterface(ctx context.Context, jailName string, spec provider.NetworkSpec) (*VNetInterface, error) {
	nm.logger.Info("creating VNET interface for jail", "jail", jailName)

	// Create epair interface
	output, err := nm.cmd().CombinedOutput(ctx, "ifconfig", "epair", "create")
	if err != nil {
		return nil, fmt.Errorf("failed to create epair: %w (output: %s)", err, string(output))
	}

	// Parse epair name (e.g., "epair0a")
	epairA := strings.TrimSpace(string(output))
	if epairA == "" {
		return nil, fmt.Errorf("failed to get epair name from output: %s", string(output))
	}

	// Derive epairB name (replace 'a' with 'b')
	epairB := epairA[:len(epairA)-1] + "b"

	nm.logger.Debug("created epair pair", "epair_a", epairA, "epair_b", epairB, "jail", jailName)

	// Set MTU if specified
	if spec.MTU > 0 {
		if err := nm.cmd().Run(ctx, "ifconfig", epairA, "mtu", strconv.Itoa(spec.MTU)); err != nil {
			nm.logger.Warn("Failed to set MTU on host interface", "interface", epairA, "mtu", spec.MTU, "error", err)
		}
		if err := nm.cmd().Run(ctx, "ifconfig", epairB, "mtu", strconv.Itoa(spec.MTU)); err != nil {
			nm.logger.Warn("Failed to set MTU on jail interface", "interface", epairB, "mtu", spec.MTU, "error", err)
		}
	}

	// Bring up epairA (host side)
	if err := nm.bringUpInterface(ctx, epairA); err != nil {
		// Cleanup on failure
		_ = nm.DestroyVNetInterface(ctx, epairA)
		return nil, fmt.Errorf("failed to bring up %s: %w", epairA, err)
	}

	// Attach epairA to bridge
	if spec.Bridge != "" {
		if err := nm.attachInterfaceToBridge(ctx, epairA, spec); err != nil {
			// Cleanup on failure
			_ = nm.DestroyVNetInterface(ctx, epairA)
			return nil, fmt.Errorf("failed to attach %s to bridge %s: %w", epairA, spec.Bridge, err)
		}
		nm.logger.Debug("attached epair to bridge", "epair", epairA, "bridge", spec.Bridge, "jail", jailName)
	}

	return &VNetInterface{
		EpairA: epairA,
		EpairB: epairB,
		Bridge: spec.Bridge,
	}, nil
}

// DestroyVNetInterface destroys an epair interface
func (nm *NetworkManager) DestroyVNetInterface(ctx context.Context, epairName string) error {
	nm.logger.Debug("destroying epair", "epair", epairName)

	if err := nm.cmd().Run(ctx, "ifconfig", epairName, "destroy"); err != nil {
		return fmt.Errorf("failed to destroy %s: %w", epairName, err)
	}

	return nil
}

// bringUpInterface brings up a network interface
func (nm *NetworkManager) bringUpInterface(ctx context.Context, ifname string) error {
	if err := nm.cmd().Run(ctx, "ifconfig", ifname, "up"); err != nil {
		return fmt.Errorf("failed to bring up %s: %w", ifname, err)
	}
	return nil
}

// attachInterfaceToBridge attaches an interface to a bridge with advanced options
func (nm *NetworkManager) attachInterfaceToBridge(ctx context.Context, ifname string, spec provider.NetworkSpec) error {
	bridge := spec.Bridge
	if bridge == "" {
		return fmt.Errorf("bridge name is required")
	}

	args := []string{bridge, "addm", ifname}

	// Apply VLAN tagging to bridge port if specified
	if spec.VLAN > 0 {
		args = append(args, "untagged", strconv.Itoa(spec.VLAN))
	}

	if err := nm.cmd().Run(ctx, "ifconfig", args...); err != nil {
		return fmt.Errorf("failed to add %s to bridge %s (args: %v): %w", ifname, bridge, args, err)
	}

	// Apply bridge port flags (e.g., "private"). Defense in depth: re-validate
	// each flag against the allow-list before it reaches root's ifconfig.
	for _, flag := range spec.BridgeFlags {
		if err := validation.ValidateBridgeFlag(flag); err != nil {
			return err
		}
		if err := nm.cmd().Run(ctx, "ifconfig", bridge, flag, ifname); err != nil {
			return fmt.Errorf("failed to set flag %s on %s for bridge %s: %w", flag, ifname, bridge, err)
		}
	}

	return nil
}

// loopbackCIDR is what a jail's own lo0 is given. Assigning the address is
// separate from bringing the interface up, and a VNET jail starts with neither.
const loopbackCIDR = "127.0.0.1/8"

// ConfigureVNetJailNetwork configures networking inside a VNET jail
// This runs commands inside the jail to configure the epairB interface
// For cross-architecture jails (QEMU emulation) or Linux jails, use host-side configuration
// where possible to avoid dependency on in-jail binaries like ifconfig.
func (nm *NetworkManager) ConfigureVNetJailNetwork(ctx context.Context, jailName, bridgeName, ifname, ipAddress, runtimeType string, withDefaultRoute bool) error {
	isCrossArch := runtimeType == "crossarch"
	isLinux := runtimeType == "linux"
	hostSideConfig := isCrossArch || isLinux

	nm.logger.Info("configuring VNET network for jail", "jail", jailName, "interface", ifname, "ip", ipAddress, "runtime", runtimeType, "bridge", bridgeName)

	var output []byte
	var err error

	if hostSideConfig {
		// Use ifconfig -j from host to configure jail interface without depending on in-jail binaries
		nm.logger.Debug("using host-side network configuration", "jail", jailName, "interface", ifname)

		// Give the loopback its address, not just its flags. A VNET jail's lo0
		// starts with none, and "up" alone leaves it that way: nothing reaches
		// 127.0.0.1, so every service that talks to itself and every health
		// check aimed at localhost fails, while the listening socket is plainly
		// there in sockstat.
		//
		// A FreeBSD jail hides this: it boots /etc/rc, and rc.d/netif addresses
		// lo0 on the way. A Linux jail starts on /bin/true and has no rc, so the
		// host does it.
		if err = nm.cmd().Run(ctx, "ifconfig", "-j", jailName, "lo0", "inet", loopbackCIDR, "up"); err != nil {
			nm.logger.Warn("failed to configure lo0 from host", "jail", jailName, logging.FieldError, err)
		}

		// Configure IP address on epairB interface from host
		output, err = nm.cmd().CombinedOutput(ctx, "ifconfig", "-j", jailName, ifname, ipAddress, "up")
		if err != nil {
			return fmt.Errorf("failed to configure %s in jail from host: %w (output: %s)", ifname, err, string(output))
		}
	} else {
		// Native FreeBSD jail: use jexec to run inside jail
		// Address the loopback here too. rc.d/netif does it moments later on a
		// jail that boots /etc/rc, but a jail configured before rc runs — or
		// one told not to run it — would otherwise have no localhost at all.
		if err = nm.cmd().Run(ctx, "jexec", jailName, "ifconfig", "lo0", "inet", loopbackCIDR, "up"); err != nil {
			nm.logger.Warn("failed to configure lo0 in jail", "jail", jailName, logging.FieldError, err)
		}

		// Configure IP address on epairB interface inside jail
		output, err = nm.cmd().CombinedOutput(ctx, "jexec", jailName, "ifconfig", ifname, ipAddress, "up")
		if err != nil {
			return fmt.Errorf("failed to configure %s in jail: %w (output: %s)", ifname, err, string(output))
		}
	}
	nm.logger.Debug("configured jail interface with IP", "jail", jailName, "interface", ifname, "ip", ipAddress)

	// One default route per jail, not per interface. A jail on two networks
	// otherwise gets one for each, and the kernel uses whichever was installed
	// first: a segment its manifest describes as having no way off it takes the
	// traffic, and every name lookup fails.
	if withDefaultRoute && nm.config.DefaultGateway != "" && nm.config.DefaultGateway != "none" {
		gateway := nm.config.DefaultGateway
		if gateway == "auto" {
			gateway = getGatewayForIP(ipAddress)
		}

		if gateway != "" {
			// Ensure gateway IP is configured on the bridge (for custom subnets)
			if bridgeName != "" {
				if err := nm.EnsureGatewayOnBridge(ctx, bridgeName, gateway, ipAddress); err != nil {
					nm.logger.Warn("failed to ensure gateway on bridge", "jail", jailName, "bridge", bridgeName, "gateway", gateway, logging.FieldError, err)
				}
			}

			// Use host-side route command with -j flag to configure jail routing
			// This works for all jail types (FreeBSD, Linux, etc.) without in-jail dependencies
			nm.logger.Debug("adding default route from host", "jail", jailName, "gateway", gateway)
			output, err = nm.cmd().CombinedOutput(ctx, "route", "-j", jailName, "add", "default", gateway)
			if err != nil {
				nm.logger.Warn("failed to add default route from host", "jail", jailName, "gateway", gateway, logging.FieldError, err, "output", string(output))
			} else {
				nm.logger.Debug("added default route from host", "jail", jailName, "gateway", gateway)
			}
		}
	}

	return nil
}

// EnsureGatewayOnBridge ensures the gateway IP is configured on the bridge
// This is needed when a jail uses a custom subnet that wasn't in the original IP pool
func (nm *NetworkManager) EnsureGatewayOnBridge(ctx context.Context, bridgeName, gateway, jailIP string) error {
	// Check if gateway is already on the bridge
	if nm.hasIPOnInterface(ctx, bridgeName, gateway) {
		nm.logger.Debug("gateway already configured on bridge", "bridge", bridgeName, "gateway", gateway)
		return nil
	}

	// Get netmask from jail IP for proper CIDR configuration
	maskBits := 24 // default
	if strings.Contains(jailIP, "/") {
		_, ipNet, err := net.ParseCIDR(jailIP)
		if err == nil {
			maskBits, _ = ipNet.Mask.Size()
		}
	}

	gatewayWithMask := fmt.Sprintf("%s/%d", gateway, maskBits)
	nm.logger.Info("adding gateway IP to bridge for custom subnet", "bridge", bridgeName, "gateway", gatewayWithMask, "jail_ip", jailIP)

	// Add gateway IP as alias on bridge
	output, err := nm.cmd().CombinedOutput(ctx, "ifconfig", bridgeName, "inet", gateway, "netmask", getMaskFromBits(maskBits), "alias")
	if err != nil {
		return fmt.Errorf("failed to add gateway IP to bridge: %w (output: %s)", err, string(output))
	}

	nm.logger.Info("gateway added to bridge", "bridge", bridgeName, "gateway", gateway)
	return nil
}

// hasIPOnInterface checks if the given IP is configured on an interface
func (nm *NetworkManager) hasIPOnInterface(ctx context.Context, ifname, targetIP string) bool {
	output, err := nm.cmd().Output(ctx, "ifconfig", ifname)
	if err != nil {
		return false
	}

	// Compare the address field after "inet"/"inet6" for exact equality rather
	// than a substring match, so 10.0.0.1 does not spuriously match 10.0.0.10.
	ipOnly := targetIP
	if strings.Contains(ipOnly, "/") {
		ipOnly = strings.Split(ipOnly, "/")[0]
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && (fields[0] == "inet" || fields[0] == "inet6") {
			addr := fields[1]
			if strings.Contains(addr, "/") { // some ifconfig variants print inet a.b.c.d/24
				addr = strings.Split(addr, "/")[0]
			}
			if addr == ipOnly {
				return true
			}
		}
	}
	return false
}

// getMaskFromBits converts CIDR mask bits to dotted decimal notation
func getMaskFromBits(bits int) string {
	mask := net.CIDRMask(bits, 32)
	return fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])
}
