package jail

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hospitus/hospitus/pkg/config"
	"github.com/hospitus/hospitus/pkg/logging"
)

// maxPoolBits caps how large a CIDR pool may be materialized: 2^16 addresses
// (a /16). parseIPPool builds the whole candidate list in memory, so an
// oversized pool (a /8 is ~16M strings) must be refused, not attempted.
const maxPoolBits = 16

// parseIPPool parses IP pool entries (similar to CBSD nodeippool)
// Supports: CIDR (192.168.0.0/24), Range (192.168.1.10-50), and single IPs
func (p *JailProvider) parseIPPool(pool string) ([]string, error) {
	var ips []string

	// Check if it's a CIDR notation (contains /)
	if strings.Contains(pool, "/") {
		ip, ipnet, err := net.ParseCIDR(pool)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR notation %s: %w", pool, err)
		}

		if ones, bits := ipnet.Mask.Size(); bits-ones > maxPoolBits {
			return nil, fmt.Errorf("IP pool %s is too large (max /%d)", pool, 32-maxPoolBits)
		}

		// Generate all IPs in the range
		for ip := ip.Mask(ipnet.Mask); ipnet.Contains(ip); incIP(ip) {
			ips = append(ips, ip.String())
		}

		// Remove network address (first) and broadcast (last) for /24 and larger
		if len(ips) > 2 {
			ips = ips[1 : len(ips)-1]
		}

		return ips, nil
	}

	// Check if it's a range notation (contains - in last octet)
	parts := strings.Split(pool, ".")
	if len(parts) == 4 && strings.Contains(parts[3], "-") {
		rangeParts := strings.Split(parts[3], "-")
		if len(rangeParts) != 2 {
			return nil, fmt.Errorf("invalid range notation: %s", pool)
		}

		start, err := strconv.Atoi(rangeParts[0])
		if err != nil {
			return nil, fmt.Errorf("invalid range start: %s", rangeParts[0])
		}

		end, err := strconv.Atoi(rangeParts[1])
		if err != nil {
			return nil, fmt.Errorf("invalid range end: %s", rangeParts[1])
		}

		if start < 0 || start > 255 || end < 0 || end > 255 {
			return nil, fmt.Errorf("range octets must be 0-255 in %s", pool)
		}
		if start > end {
			return nil, fmt.Errorf("range start exceeds range end in %s", pool)
		}

		// The three leading fields are joined without ever being checked, so a
		// pool like "999.1.2.10-12" yields "999.1.2.10" — which allocateDHCPAddress
		// hands back and ConfigureVNetJailNetwork passes to ifconfig.
		baseIP := strings.Join(parts[:3], ".")
		for i := start; i <= end; i++ {
			candidate := fmt.Sprintf("%s.%d", baseIP, i)
			if net.ParseIP(candidate) == nil {
				return nil, fmt.Errorf("invalid IP pool format: %s", pool)
			}
			ips = append(ips, candidate)
		}

		return ips, nil
	}

	// Single IP
	if net.ParseIP(pool) != nil {
		return []string{pool}, nil
	}

	return nil, fmt.Errorf("invalid IP pool format: %s", pool)
}

// incIP increments an IP address
func incIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

// getIPPools returns the configured IP pools for DHCP allocation
// Format: "10.0.0.0/24 192.168.1.10-50" (space-separated, like CBSD nodeippool)
//
// Priority order (Unix best practice):
//  1. Environment variable (HOSPITUS_IP_POOL)
//     - Temporary override for development/testing (highest priority)
//  2. Configuration file (/etc/hospitus/hospitusd.conf or /usr/local/etc/hospitus/hospitusd.conf)
//     - Production configuration, persistent across restarts
//  3. Provider config (legacy)
//     - Backward compatibility
//  4. Default pool (10.0.0.0/24)
//     - Safe default for initial setup
func (p *JailProvider) getIPPools() []string {
	// 1. Check environment variable first (allows temporary override)
	if envPool := os.Getenv("HOSPITUS_IP_POOL"); envPool != "" {
		return strings.Fields(envPool)
	}

	// 2. Try configuration file (production)
	cfg, err := config.LoadConfigWithDefaults()
	if err == nil && cfg != nil && cfg.IPPool != "" {
		return strings.Fields(cfg.IPPool)
	}

	// 3. Check provider config (legacy)
	if poolStr, ok := p.config.Settings["ip_pool"].(string); ok && poolStr != "" {
		return strings.Fields(poolStr)
	}

	// 4. Default pool (compatible with current behavior)
	return []string{"10.0.0.0/24"}
}

// addressesInUse maps every address a jail currently holds to the jail holding
// it, skipping the one named in exclude.
//
// DHCP allocation reads this so it does not hand out an address twice; a static
// address is checked against it for the same reason.
func (p *JailProvider) addressesInUse(exclude string) (map[string]string, error) {
	files, err := filepath.Glob(filepath.Join(p.stateDir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("failed to list jail configs: %w", err)
	}

	held := make(map[string]string)
	for _, file := range files {
		jailCfg, err := p.loadJailConfig(file)
		if err != nil {
			// A config that cannot be read holds addresses nobody can see; say
			// so, because the next allocation may hand one of them out again.
			p.logWarn(context.Background(), "skipping unreadable jail config while collecting used addresses",
				"config_path", file, logging.FieldError, err)
			continue
		}
		if exclude != "" && jailCfg.Name == exclude {
			continue
		}
		for i := range jailCfg.Networks {
			network := &jailCfg.Networks[i]
			if network.IPv4 == "" || network.IPv4 == "dhcp" {
				continue
			}
			held[addressWithoutPrefix(network.IPv4)] = jailCfg.Name
		}
	}
	return held, nil
}

// addressWithoutPrefix drops the /prefix an address is written with.
func addressWithoutPrefix(addr string) string {
	if idx := strings.Index(addr, "/"); idx != -1 {
		return addr[:idx]
	}
	return addr
}

// poolGateways returns the gateway address of every pool: the first usable
// address of each pool network, which EnsureBridge assigns to the bridge
// itself. No jail may be given one.
func poolGateways(pools []string) map[string]bool {
	gateways := make(map[string]bool)
	for _, pool := range pools {
		if gw := addressWithoutPrefix(gatewayIPFromPool(pool)); gw != "" {
			gateways[gw] = true
		}
	}
	return gateways
}

// allocateDHCPAddress finds the first available IP address for DHCP allocation.
//
// declaredPool is the pool the network declares, and is tried before the
// globally configured pools: the bridge takes its gateway from that same
// declared pool, so allocating out of the global pool would put the jail on a
// subnet its bridge does not route.
//
// Pools are written as CIDR ("192.168.0.0/24"), a range ("192.168.1.10-50"), a
// single IP ("10.0.0.10"), or several of those separated by spaces.
func (p *JailProvider) allocateDHCPAddress(ctx context.Context, declaredPool string) (string, error) {
	pools := p.getIPPools()
	if declaredPool != "" {
		pools = append(strings.Fields(declaredPool), pools...)
	}

	// Reserve every pool's gateway, not just the first one's: once a pool is
	// exhausted allocation moves to the next, whose .1 is that subnet's gateway.
	usedIPs := poolGateways(pools)

	held, err := p.addressesInUse("")
	if err != nil {
		return "", err
	}
	for ip := range held {
		usedIPs[ip] = true
	}

	// Try each pool in order
	for _, pool := range pools {
		candidateIPs, err := p.parseIPPool(pool)
		if err != nil {
			// Log error but continue with next pool
			p.logWarn(ctx, "failed to parse IP pool during DHCP allocation", "pool", pool, logging.FieldError, err)
			continue
		}

		// Find first available IP in this pool
		for _, candidateIP := range candidateIPs {
			if !usedIPs[candidateIP] {
				// Determine netmask from original pool format
				var netmask string
				if strings.Contains(pool, "/") {
					// Extract CIDR mask
					parts := strings.Split(pool, "/")
					netmask = "/" + parts[1]
				} else {
					// Default to /24 for ranges and single IPs
					netmask = "/24"
				}

				return candidateIP + netmask, nil
			}
		}
	}

	return "", fmt.Errorf("no available IP addresses in configured pools: %v", pools)
}

// refuseAddressInUse fails when another jail, or a bridge, already holds the
// address.
//
// The address a jail is started with is checked, not the one it was created
// with: a jail that has been stopped keeps its address in its config, and a
// second jail asking for it would otherwise take it silently.
//
// A pool's gateway is refused too. Those addresses live on the bridges
// EnsureBridge configures and appear in no jail config, so a jail asking for
// 10.0.0.1 was accepted and then fought the bridge for it by ARP.
func (p *JailProvider) refuseAddressInUse(jailName, addr string) error {
	if addr == "" || addr == "dhcp" {
		return nil
	}

	ip := addressWithoutPrefix(addr)

	if poolGateways(p.getIPPools())[ip] {
		return fmt.Errorf("address %s is the gateway address of a configured IP pool", ip)
	}

	held, err := p.addressesInUse(jailName)
	if err != nil {
		return err
	}
	if owner, taken := held[ip]; taken {
		return fmt.Errorf("address %s is already held by jail %s", ip, owner)
	}
	return nil
}
