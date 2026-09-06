package bhyve

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/hospitus/hospitus/pkg/firewall"
	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

const (
	// natBridge is the bridge used for NAT mode networking.
	// All bhyve VMs using type="nat" share this bridge.
	natBridge = "hospitus-nat"

	// natGatewayIP is the host-side IP on the NAT bridge (VM gateway).
	natGatewayIP = "10.10.0.1"

	// natNetmask is the subnet mask for the NAT network.
	natNetmask = "255.255.255.0"

	// natCIDR is the full NAT network in CIDR notation.
	natCIDR = "10.10.0.0/24"

	// dnsmasqPidFile tracks the running dnsmasq instance for the NAT bridge.
	dnsmasqPidFile = "/var/run/hospitus-bhyve-dnsmasq.pid"

	// dnsmasqLogFile is where dnsmasq writes its log.
	dnsmasqLogFile = "/var/log/hospitus-bhyve-dnsmasq.log"

	// dnsmasqLeaseFile is where dnsmasq records DHCP leases.
	dnsmasqLeaseFile = "/var/run/hospitus-bhyve-dnsmasq.leases"
)

// initFirewall initializes the firewall manager exactly once (thread-safe).
func (p *BhyveProvider) initFirewall(ctx context.Context) error {
	p.firewallOnce.Do(func() {
		mgr, err := firewall.NewManager()
		if err != nil {
			p.firewallErr = fmt.Errorf("failed to create firewall manager: %w", err)
			return
		}
		if err := mgr.Initialize(ctx); err != nil {
			p.firewallErr = fmt.Errorf("failed to initialize firewall manager: %w", err)
			return
		}
		p.firewallMgr = mgr
	})
	return p.firewallErr
}

// ensureNATBridge creates and configures the shared NAT bridge if it does not
// already exist. It assigns natGatewayIP to the bridge and enables IP
// forwarding on the host.
func (p *BhyveProvider) ensureNATBridge(ctx context.Context) error {
	logger := logging.WithComponent("bhyve-network")

	// Check if bridge already exists and has the right IP.
	output, err := p.cmd().Output(ctx, "ifconfig", natBridge)
	if err == nil {
		// Bridge exists; verify it has the gateway IP assigned.
		if strings.Contains(string(output), natGatewayIP) {
			return nil // Already configured.
		}
		// Bridge exists but IP is missing — assign it.
		if out, err := p.cmd().CombinedOutput(ctx, "ifconfig", natBridge, "inet", natGatewayIP, "netmask", natNetmask); err != nil {
			return fmt.Errorf("failed to assign gateway IP to %s: %w (output: %s)", natBridge, err, out)
		}
		return nil
	}

	if out, err := p.cmd().CombinedOutput(ctx, "ifconfig", "bridge", "create", "name", natBridge); err != nil {
		return fmt.Errorf("failed to create NAT bridge %s: %w (output: %s)", natBridge, err, out)
	}
	logger.Info("Created NAT bridge", "bridge", natBridge)

	// Assign gateway IP.
	if out, err := p.cmd().CombinedOutput(ctx, "ifconfig", natBridge, "inet", natGatewayIP, "netmask", natNetmask, "up"); err != nil {
		return fmt.Errorf("failed to configure NAT bridge %s: %w (output: %s)", natBridge, err, out)
	}

	// Enable IP forwarding on the host so NATed VMs can reach the internet.
	if out, err := p.cmd().CombinedOutput(ctx, "sysctl", "net.inet.ip.forwarding=1"); err != nil {
		// Non-fatal: log a warning; NAT may still work if it was already enabled.
		logger.Warn("Failed to enable IP forwarding", logging.FieldError, err, "output", string(out))
	}

	logger.Info("NAT bridge ready", "bridge", natBridge, "gateway", natGatewayIP)
	return nil
}

// restoreTapDevices recreates the tap(4) devices a VM's config records but the
// host no longer has, and puts each one back on the bridge it was created on.
//
// tap devices and bridge membership are host state a reboot takes away, while
// CreateInstance is the only other place that sets them up. Start therefore has
// to rebuild them, exactly as it re-applies the VLANs and the NAT rules.
func (p *BhyveProvider) restoreTapDevices(ctx context.Context, config *vmConfig) error {
	logger := logging.WithComponent("bhyve-network")

	for i, tapDev := range config.TapDevs {
		if tapDev == "" {
			continue
		}

		if err := p.cmd().Run(ctx, "ifconfig", tapDev); err != nil {
			out, err := p.cmd().CombinedOutput(ctx, "ifconfig", "tap", "create", "name", tapDev)
			if err != nil {
				return fmt.Errorf("failed to recreate tap device %s: %w (output: %s)", tapDev, err, out)
			}
			logger.Info("Recreated missing tap device", logging.FieldVM, config.Name, "tap", tapDev)
		}

		if out, err := p.cmd().CombinedOutput(ctx, "ifconfig", tapDev, "up"); err != nil {
			return fmt.Errorf("failed to bring up tap device %s: %w (output: %s)", tapDev, err, out)
		}

		// Only bridged NICs record a bridge; a NAT tap is re-attached by
		// setupNATForVM and a VLAN tap by setupVLANForTap.
		if i >= len(config.Bridges) || config.Bridges[i] == "" {
			continue
		}
		if err := p.ensureBridge(ctx, config.Bridges[i]); err != nil {
			return fmt.Errorf("failed to restore bridge for tap %s: %w", tapDev, err)
		}
		if err := p.attachToBridge(ctx, tapDev, config.Bridges[i]); err != nil {
			return fmt.Errorf("failed to restore bridge membership for tap %s: %w", tapDev, err)
		}
	}

	return nil
}

// setupNATForVM configures NAT networking for a single VM:
//  1. Ensure the shared NAT bridge exists.
//  2. Attach the tap device to the NAT bridge.
//  3. Add PF NAT rules so the VM can reach the internet.
//  4. Start dnsmasq for DHCP if it is available and not already running.
func (p *BhyveProvider) setupNATForVM(ctx context.Context, vmName, tapDev string) error {
	if err := p.ensureNATBridge(ctx); err != nil {
		return fmt.Errorf("NAT bridge setup failed: %w", err)
	}

	if err := p.attachToBridge(ctx, tapDev, natBridge); err != nil {
		return fmt.Errorf("failed to attach tap to NAT bridge: %w", err)
	}

	if err := p.initFirewall(ctx); err != nil {
		return fmt.Errorf("failed to initialize firewall: %w", err)
	}

	// Add a pass rule for DHCP discovery traffic on the NAT bridge.
	// DHCP DISCOVER packets are sent from 0.0.0.0:68 → 255.255.255.255:67 and
	// are NOT matched by the per-VM "pass in inet from 10.10.0.0/24" rules.
	// Without this, PF's default "block all" silently drops them and dnsmasq
	// never receives them (visible with tcpdump but absent from dnsmasq logs).
	dhcpPassRule := fmt.Sprintf(
		"pass in quick on %s proto udp from any port = 68 to any port = 67",
		natBridge,
	)
	if err := p.firewallMgr.AddFilterRule(ctx, "hospitus-bhyve-dhcp", "hospitus-bridge", dhcpPassRule); err != nil {
		return fmt.Errorf("failed to add DHCP pass rule: %w", err)
	}

	extIf, err := p.bhyveDefaultInterface(ctx)
	if err != nil {
		return fmt.Errorf("cannot determine default interface for NAT: %w", err)
	}

	if _, err := p.firewallMgr.SetupNAT(ctx, vmName, "bhyve", natCIDR, extIf); err != nil {
		return fmt.Errorf("failed to configure PF NAT for VM %s: %w", vmName, err)
	}

	// Best-effort: start dnsmasq so VMs get IPs via DHCP.
	if err := p.ensureDNSMasq(ctx); err != nil {
		logging.WithComponent("bhyve-network").Warn(
			"dnsmasq unavailable; VMs in NAT mode need a static IP or manual DHCP setup",
			logging.FieldError, err,
		)
	}

	return nil
}

// teardownNATForVM removes PF NAT rules for a VM.
func (p *BhyveProvider) teardownNATForVM(ctx context.Context, vmName string) {
	if err := p.initFirewall(ctx); err != nil {
		logging.WithComponent("bhyve-network").Error("Cannot tear down NAT rules: firewall init failed",
			"vm", vmName, logging.FieldError, err)
		return
	}
	if err := p.firewallMgr.RemoveAllRules(ctx, vmName); err != nil {
		logging.WithComponent("bhyve-network").Warn("Failed to remove NAT rules",
			"vm", vmName, logging.FieldError, err)
	}
}

// dnsmasqArgs builds the DHCP server's command line for the NAT bridge.
func dnsmasqArgs() []string {
	return []string{
		"--interface=" + natBridge,
		// Do NOT use --bind-interface: on FreeBSD it binds the DHCP socket to the
		// unicast address (10.10.0.1:67) and misses broadcast DHCP DISCOVERs.
		// Without it, dnsmasq binds 0.0.0.0:67 and filters by interface name.
		// --port=0 disables the DNS server entirely: Hospitus only needs DHCP, and
		// leaving DNS on would fight the host resolver for UDP/53. VMs receive
		// upstream resolvers directly via the DHCP DNS option below.
		"--port=0",
		"--dhcp-range=10.10.0.10,10.10.0.200,12h",
		"--no-hosts",
		"--pid-file=" + dnsmasqPidFile,
		// Without this dnsmasq writes its leases wherever it was compiled to,
		// while lookupIPsByVM reads dnsmasqLeaseFile — so every lease lookup
		// came up empty and a VM with an address was listed without one.
		"--dhcp-leasefile=" + dnsmasqLeaseFile,
		"--log-facility=" + dnsmasqLogFile,
		"--dhcp-option=6,8.8.8.8,1.1.1.1", // DNS option
	}
}

// ensureDNSMasq starts a dnsmasq instance for the NAT bridge if dnsmasq is
// installed and not already running. Returns an error if dnsmasq is not found.
func (p *BhyveProvider) ensureDNSMasq(ctx context.Context) error {
	dnsmasqBin, err := exec.LookPath("dnsmasq")
	if err != nil {
		return fmt.Errorf("dnsmasq not found (install with: pkg install dnsmasq): %w", err)
	}

	// Check if already running for our bridge.
	if p.isProcessRunning(ctx, dnsmasqPidFile, "dnsmasq") {
		return nil
	}

	if out, err := p.cmd().CombinedOutput(ctx, dnsmasqBin, dnsmasqArgs()...); err != nil {
		return fmt.Errorf("failed to start dnsmasq: %w (output: %s)", err, out)
	}

	logging.WithComponent("bhyve-network").Info("dnsmasq started",
		"bridge", natBridge, "dhcp_range", "10.10.0.10-10.10.0.200")
	return nil
}

// isProcessRunning reports whether the PID file names a live process that is
// still the program it is supposed to be.
//
// Signal 0 alone only proves some process owns the number. After a host reboot
// or a crash the file can name a PID the kernel has reassigned; ensureDNSMasq
// then returned early believing DHCP was served, and NAT guests silently got no
// address. bhyve_lifecycle.go answers the same question about bhyve with
// commandIsBhyveVM.
func (p *BhyveProvider) isProcessRunning(ctx context.Context, pidFile, wantCommand string) bool {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	out, err := p.cmd().Output(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "command=")
	if err != nil {
		return false
	}
	fields := strings.Fields(string(out))
	return len(fields) > 0 && strings.Contains(fields[0], wantCommand)
}

// bhyveDefaultInterface returns the network interface for the default route.
// The caller's context bounds the route lookup: setupNATForVM and
// setupVLANForTap run inside CreateInstance and StartInstance while those hold
// the per-instance lock, so an unbounded route call held it too.
func (p *BhyveProvider) bhyveDefaultInterface(ctx context.Context) (string, error) {
	output, err := p.cmd().Output(ctx, "route", "-n", "get", "default")
	if err != nil {
		return "", fmt.Errorf("failed to get default route: %w", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
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

// lookupIPsByVM finds the IP addresses for a VM by searching the dnsmasq lease
// file for an entry whose hostname matches vmName. Falls back to parsing the
// ARP table when the lease file is absent or contains no matching entry.
func (p *BhyveProvider) lookupIPsByVM(ctx context.Context, vmName string) ([]net.IP, error) {
	logger := logging.WithComponent("bhyve-network")

	// Primary: parse dnsmasq lease file.
	// Line format: <expiry_epoch> <mac> <ip> <hostname> <client_id>
	if f, err := os.Open(dnsmasqLeaseFile); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 4 {
				continue
			}
			hostname := fields[3]
			if strings.EqualFold(hostname, vmName) {
				if ip := net.ParseIP(fields[2]); ip != nil {
					return []net.IP{ip}, nil
				}
			}
		}
		if err := scanner.Err(); err != nil {
			// A read error means the lease file may have been scanned only
			// partially; log it and fall through to the ARP fallback.
			logger.Debug("error reading dnsmasq lease file", logging.FieldError, err)
		}
	}

	// Fallback: find the VM's own MAC in the ARP table.
	//
	// Not the VM's name: arp(8) never prints one. Every line reads
	// "? (10.10.0.180) at 00:a0:98:... on hospitus-nat", so a name match
	// finds nothing, or worse finds a neighbor whose line happens to hold the
	// bridge or interface name the VM was called after.
	guestMAC := p.guestMACForVM(ctx, vmName)
	if guestMAC == "" {
		return nil, nil
	}
	out, err := p.cmd().Output(ctx, "arp", "-an")
	if err != nil {
		logger.Debug("arp -an failed", logging.FieldError, err)
		return nil, nil
	}
	if addr := ipForMACInARP(string(out), guestMAC); addr != "" {
		if ip := net.ParseIP(addr); ip != nil {
			return []net.IP{ip}, nil
		}
	}

	return nil, nil
}

// ipv6NATGateway is the host-side IPv6 address on the NAT bridge (ULA).
const ipv6NATGateway = "fd10::1"

// ipv6NATPrefix is the /64 assigned to bhyve VMs for IPv6 NAT.
const ipv6NATPrefix = "fd10::1/64"

// ipv6NATCIDR is the full ULA network used for PF nat6 rules.
const ipv6NATCIDR = "fd10::/64"

// setupIPv6NATForVM adds IPv6 NAT support to a VM:
//  1. Assign a ULA /64 address to the NAT bridge if not already present.
//  2. Enable IPv6 forwarding on the host.
//  3. Add a PF nat6 rule so the VM can reach the internet via IPv6.
func (p *BhyveProvider) setupIPv6NATForVM(ctx context.Context, vmName, ipv6Prefix string) error {
	logger := logging.WithComponent("bhyve-network")

	if ipv6Prefix == "" {
		ipv6Prefix = ipv6NATPrefix
	}

	// Assign IPv6 address to the bridge if missing.
	if out, err := p.cmd().Output(ctx, "ifconfig", natBridge); err == nil {
		if !strings.Contains(string(out), ipv6NATGateway) {
			if cmdOut, cmdErr := p.cmd().CombinedOutput(ctx, "ifconfig", natBridge, "inet6", ipv6Prefix, "up"); cmdErr != nil {
				return fmt.Errorf("failed to assign IPv6 to %s: %w (output: %s)", natBridge, cmdErr, cmdOut)
			}
			logger.Info("Assigned IPv6 to NAT bridge", "bridge", natBridge, "prefix", ipv6Prefix)
		}
	}

	// Enable IPv6 forwarding.
	if out, err := p.cmd().CombinedOutput(ctx, "sysctl", "net.inet6.ip6.forwarding=1"); err != nil {
		logger.Warn("Failed to enable IPv6 forwarding", logging.FieldError, err, "output", string(out))
	}

	extIf, err := p.bhyveDefaultInterface(ctx)
	if err != nil {
		return fmt.Errorf("cannot determine default interface for IPv6 NAT: %w", err)
	}

	// Add a PF nat6 rule for this VM. It is a translation rule and must go in
	// the nat ruleset — writing it into filter.rules (as before) makes pfctl
	// reject the entire hospitus-bhyve-ipv6 anchor.
	nat6Rule := fmt.Sprintf("nat6 on %s from %s -> (%s:0)", extIf, ipv6NATCIDR, extIf)
	// The instance tag must match what teardownIPv6NATForVM removes by
	// (RemoveAllRules with vmName+"-ipv6"), or the rule is never removed.
	if err := p.firewallMgr.AddRawNATRule(ctx, vmName+"-ipv6", vmName+"-ipv6", nat6Rule); err != nil {
		return fmt.Errorf("failed to add IPv6 NAT rule for VM %s: %w", vmName, err)
	}

	logger.Info("IPv6 NAT configured", "vm", vmName, "prefix", ipv6Prefix, "extIf", extIf)
	return nil
}

// teardownIPv6NATForVM removes PF IPv6 NAT rules for a VM.
func (p *BhyveProvider) teardownIPv6NATForVM(ctx context.Context, vmName string) {
	if err := p.initFirewall(ctx); err != nil {
		logging.WithComponent("bhyve-network").Error("Cannot tear down IPv6 NAT rules: firewall init failed",
			"vm", vmName, logging.FieldError, err)
		return
	}
	if err := p.firewallMgr.RemoveAllRules(ctx, vmName+"-ipv6"); err != nil {
		logging.WithComponent("bhyve-network").Warn("Failed to remove IPv6 NAT rules",
			"vm", vmName, logging.FieldError, err)
	}
}

// ensureVLANBridge creates a per-VLAN bridge "hospitus-vlan<id>" if not already present.
func (p *BhyveProvider) ensureVLANBridge(ctx context.Context, vlanID int) (string, error) {
	bridgeName := fmt.Sprintf("hospitus-vlan%d", vlanID)
	if _, err := p.cmd().Output(ctx, "ifconfig", bridgeName); err == nil {
		return bridgeName, nil
	}
	if out, err := p.cmd().CombinedOutput(ctx, "ifconfig", "bridge", "create", "name", bridgeName); err != nil {
		return "", fmt.Errorf("failed to create VLAN bridge %s: %w (output: %s)", bridgeName, err, out)
	}
	if out, err := p.cmd().CombinedOutput(ctx, "ifconfig", bridgeName, "up"); err != nil {
		return "", fmt.Errorf("failed to bring up VLAN bridge %s: %w (output: %s)", bridgeName, err, out)
	}
	logging.WithComponent("bhyve-network").Info("Created VLAN bridge", "bridge", bridgeName, "vlan", vlanID)
	return bridgeName, nil
}

// setupVLANForTap sets up VLAN connectivity for a tap device. It creates a
// vlan(4) sub-interface of the host uplink (tagged with vlanID, named after the
// tap for traceability), then bridges both that sub-interface and the raw tap
// onto a per-VLAN bridge. bhyve still opens the untagged tap; VLAN tagging on
// egress is handled by the FreeBSD network stack via the uplink sub-interface.
func (p *BhyveProvider) setupVLANForTap(ctx context.Context, tapDev string, vlanID int) error {
	// 802.1Q: 1..4094. Nothing bounded this, so a negative or out-of-range id
	// from vlan_ids reached "ifconfig tap.<id> vlandev em0 vlan <id>" on create
	// and on every start, and the VM failed with an ifconfig error rather than
	// a configuration one. 0 means "no tagging" and never reaches here.
	if vlanID < 1 || vlanID > 4094 {
		return fmt.Errorf("invalid VLAN id %d: must be between 1 and 4094", vlanID)
	}
	logger := logging.WithComponent("bhyve-network")

	parentIf, err := p.bhyveDefaultInterface(ctx)
	if err != nil {
		return fmt.Errorf("cannot determine parent interface for VLAN: %w", err)
	}

	vlanIfName := fmt.Sprintf("%s.%d", tapDev, vlanID)

	// Create the vlan(4) sub-interface if it doesn't exist.
	if _, checkErr := p.cmd().Output(ctx, "ifconfig", vlanIfName); checkErr != nil {
		if out, err := p.cmd().CombinedOutput(ctx, "ifconfig", vlanIfName, "create"); err != nil {
			return fmt.Errorf("failed to create VLAN interface %s: %w (output: %s)", vlanIfName, err, out)
		}
	}

	// Configure vlandev and tag.
	if out, err := p.cmd().CombinedOutput(ctx, "ifconfig", vlanIfName, "vlandev", parentIf, "vlan", fmt.Sprintf("%d", vlanID), "up"); err != nil {
		return fmt.Errorf("failed to configure VLAN interface %s: %w (output: %s)", vlanIfName, err, out)
	}

	bridgeName, err := p.ensureVLANBridge(ctx, vlanID)
	if err != nil {
		return err
	}

	if err := p.attachToBridge(ctx, vlanIfName, bridgeName); err != nil {
		return fmt.Errorf("failed to attach %s to VLAN bridge %s: %w", vlanIfName, bridgeName, err)
	}
	if err := p.attachToBridge(ctx, tapDev, bridgeName); err != nil {
		return fmt.Errorf("failed to attach tap %s to VLAN bridge %s: %w", tapDev, bridgeName, err)
	}

	logger.Info("VLAN configured", "tap", tapDev, "vlan", vlanID, "bridge", bridgeName)
	return nil
}

// teardownVLANForTap destroys the vlan(4) sub-interface created for a tap device.
func (p *BhyveProvider) teardownVLANForTap(ctx context.Context, tapDev string, vlanID int) {
	vlanIfName := fmt.Sprintf("%s.%d", tapDev, vlanID)
	if out, err := p.cmd().CombinedOutput(ctx, "ifconfig", vlanIfName, "destroy"); err != nil {
		logging.WithComponent("bhyve-network").Warn("Failed to destroy VLAN interface",
			"vlan_if", vlanIfName, logging.FieldError, err, "output", string(out))
	}
}

var _ provider.InstanceAddressProvider = (*BhyveProvider)(nil)

// InstanceAddresses reports the address the VM holds, from the dnsmasq leases
// or, failing that, from the ARP table.
func (p *BhyveProvider) InstanceAddresses(ctx context.Context, handle provider.InstanceHandle) ([]net.IP, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	return p.lookupIPsByVM(ctx, handle.ID)
}
