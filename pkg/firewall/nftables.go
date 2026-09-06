package firewall

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

const (
	// nftables table and chain names
	nftTableName        = "hospitus"
	nftPreRoutingChain  = "prerouting"
	nftPostRoutingChain = "postrouting"

	// nftHandlePrefix precedes the numeric rule handle in "nft -a list" output.
	nftHandlePrefix = "# handle "
)

// NFTablesBackend implements the Backend interface for nftables
type NFTablesBackend struct {
	mu sync.Mutex
}

// NewNFTablesBackend creates a new nftables backend
func NewNFTablesBackend() *NFTablesBackend {
	return &NFTablesBackend{}
}

// Name returns the backend name
func (n *NFTablesBackend) Name() BackendType {
	return BackendTypeNFTables
}

// IsAvailable checks if nftables is available
func (n *NFTablesBackend) IsAvailable() bool {
	_, err := exec.LookPath("nft")
	return err == nil
}

// Initialize initializes the nftables backend
func (n *NFTablesBackend) Initialize(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Create the hospitus table with nat chains
	// nft add table ip hospitus
	// nft add chain ip hospitus prerouting { type nat hook prerouting priority -100 \; }
	// nft add chain ip hospitus postrouting { type nat hook postrouting priority 100 \; }

	cmds := [][]string{
		{"add", "table", "ip", nftTableName},
		{"add", "chain", "ip", nftTableName, nftPreRoutingChain, "{ type nat hook prerouting priority -100 ; }"},
		{"add", "chain", "ip", nftTableName, nftPostRoutingChain, "{ type nat hook postrouting priority 100 ; }"},
	}

	for _, args := range cmds {
		out, err := exec.CommandContext(ctx, "nft", args...).CombinedOutput()
		if err == nil {
			continue
		}
		// Only "it is already there" is expected. Swallowing everything else
		// reported a backend as initialized when nft was missing, refused the
		// operation, or rejected the ruleset — and the first rule then failed
		// with nothing to explain it.
		if strings.Contains(strings.ToLower(string(out)), "file exists") {
			continue
		}
		return fmt.Errorf("nft %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}

	return nil
}

// AddPortMapping adds a port forwarding rule (DNAT)
func (n *NFTablesBackend) AddPortMapping(ctx context.Context, mapping PortMapping) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := mapping.Validate(); err != nil {
		return err
	}

	// The chains below live in the "ip" family, which is IPv4 only. Validate
	// accepts an IPv6 literal, and one would be written into an IPv4 ruleset as
	// an address nft cannot even parse. Say so instead.
	if ip := net.ParseIP(mapping.TargetIP); ip != nil && ip.To4() == nil {
		return fmt.Errorf("target %s is IPv6: the nftables backend serves the ip (IPv4) family only", mapping.TargetIP)
	}

	// nft add rule ip hospitus prerouting tcp dport 80 dnat to 10.0.0.2:8080 comment "hospitus:instance:id"
	rule := fmt.Sprintf("%s dport %d dnat to %s:%d comment %q",
		mapping.Protocol,
		mapping.HostPort,
		mapping.TargetIP,
		mapping.TargetPort,
		fmt.Sprintf("hospitus:%s:%s", mapping.Instance, mapping.ID),
	)

	if mapping.HostInterface != "" {
		rule = fmt.Sprintf("iifname %q %s", mapping.HostInterface, rule)
	}

	cmd := exec.CommandContext(ctx, "nft", "add", "rule", "ip", nftTableName, nftPreRoutingChain, rule)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add DNAT rule: %w: %s", err, output)
	}

	return nil
}

// RemovePortMapping removes a port forwarding rule
func (n *NFTablesBackend) RemovePortMapping(ctx context.Context, mapping PortMapping) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Find rule handle by comment and delete
	return n.removeRuleByComment(ctx, nftPreRoutingChain, mapping.Instance, mapping.ID)
}

// AddNATRule adds a MASQUERADE rule
func (n *NFTablesBackend) AddNATRule(ctx context.Context, rule NATRule) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := rule.Validate(); err != nil {
		return err
	}

	// nft add rule ip hospitus postrouting ip saddr 10.0.0.0/24 oifname "eth0" masquerade comment "hospitus:instance:id"
	nftRule := fmt.Sprintf("ip saddr %s oifname %q masquerade comment %q",
		rule.SourceNetwork,
		rule.OutInterface,
		fmt.Sprintf("hospitus:%s:%s", rule.Instance, rule.ID),
	)

	cmd := exec.CommandContext(ctx, "nft", "add", "rule", "ip", nftTableName, nftPostRoutingChain, nftRule)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add MASQUERADE rule: %w: %s", err, output)
	}

	return nil
}

// RemoveNATRule removes a NAT rule
func (n *NFTablesBackend) RemoveNATRule(ctx context.Context, rule NATRule) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	return n.removeRuleByComment(ctx, nftPostRoutingChain, rule.Instance, rule.ID)
}

// ApplyRules applies all rules for an instance
func (n *NFTablesBackend) ApplyRules(ctx context.Context, ruleSet RuleSet) error {
	// First remove existing rules
	if err := n.RemoveAllRules(ctx, ruleSet.Instance); err != nil {
		return err
	}

	// Add NAT rules
	for _, rule := range ruleSet.NATRules {
		if err := n.AddNATRule(ctx, rule); err != nil {
			return fmt.Errorf("failed to add NAT rule: %w", err)
		}
	}

	// Add port mappings
	for k := range ruleSet.PortMappings {
		mapping := ruleSet.PortMappings[k]
		if err := n.AddPortMapping(ctx, mapping); err != nil {
			return fmt.Errorf("failed to add port mapping: %w", err)
		}
	}

	return nil
}

// RemoveAllRules removes all rules for an instance
func (n *NFTablesBackend) RemoveAllRules(ctx context.Context, instance string) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Remove from both chains
	for _, chain := range []string{nftPreRoutingChain, nftPostRoutingChain} {
		if err := n.removeInstanceRulesFromChain(ctx, chain, instance); err != nil {
			return err
		}
	}

	return nil
}

// removeInstanceRulesFromChain removes all rules for an instance from a chain
func (n *NFTablesBackend) removeInstanceRulesFromChain(ctx context.Context, chain, instance string) error {
	// List rules with handles
	cmd := exec.CommandContext(ctx, "nft", "-a", "list", "chain", "ip", nftTableName, chain)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Chain might not exist
		return nil
	}

	// Find handles for rules with our comment and delete each one, collecting
	// any deletion failures instead of silently discarding them.
	var delErrs []error
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if !strings.Contains(line, fmt.Sprintf("hospitus:%s:", instance)) {
			continue
		}
		idx := strings.Index(line, nftHandlePrefix)
		if idx < 0 {
			continue
		}
		handleStr := strings.TrimSpace(line[idx+len(nftHandlePrefix):])
		var handle int
		if _, err := fmt.Sscanf(handleStr, "%d", &handle); err != nil || handle <= 0 {
			continue
		}
		delCmd := exec.CommandContext(ctx, "nft", "delete", "rule", "ip", nftTableName, chain, "handle", fmt.Sprintf("%d", handle))
		if out, err := delCmd.CombinedOutput(); err != nil {
			delErrs = append(delErrs, fmt.Errorf("handle %d: %w: %s", handle, err, strings.TrimSpace(string(out))))
		}
	}

	if len(delErrs) > 0 {
		return fmt.Errorf("failed to delete %d nft rule(s) in chain %s: %w", len(delErrs), chain, errors.Join(delErrs...))
	}
	return nil
}

// removeRuleByComment removes a specific rule by its comment
func (n *NFTablesBackend) removeRuleByComment(ctx context.Context, chain, instance, id string) error {
	// List rules with handles
	cmd := exec.CommandContext(ctx, "nft", "-a", "list", "chain", "ip", nftTableName, chain)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Only a chain that is genuinely absent means there is nothing to
		// remove. Treating every failure that way — no nft, no permission, a
		// canceled context — reported the mapping as removed while the port
		// stayed forwarded.
		if strings.Contains(strings.ToLower(string(output)), "no such file or directory") {
			return nil
		}
		return fmt.Errorf("failed to list chain %s: %w: %s", chain, err, strings.TrimSpace(string(output)))
	}

	comment := fmt.Sprintf("hospitus:%s:%s", instance, id)
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, comment) {
			if idx := strings.Index(line, nftHandlePrefix); idx >= 0 {
				handleStr := strings.TrimSpace(line[idx+len(nftHandlePrefix):])
				var handle int
				_, _ = fmt.Sscanf(handleStr, "%d", &handle)
				if handle > 0 {
					delCmd := exec.CommandContext(ctx, "nft", "delete", "rule", "ip", nftTableName, chain, "handle", fmt.Sprintf("%d", handle))
					if output, err := delCmd.CombinedOutput(); err != nil {
						return fmt.Errorf("failed to delete rule: %w: %s", err, output)
					}
					return nil
				}
			}
		}
	}

	return nil
}

// ListRules lists all active port mappings
func (n *NFTablesBackend) ListRules(ctx context.Context) ([]PortMapping, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	cmd := exec.CommandContext(ctx, "nft", "list", "chain", "ip", nftTableName, nftPreRoutingChain)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(output)), "no such file or directory") {
			return nil, nil // the chain is genuinely not there
		}
		return nil, fmt.Errorf("failed to list chain %s: %w: %s",
			nftPreRoutingChain, err, strings.TrimSpace(string(output)))
	}

	var mappings []PortMapping
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "hospitus:") {
			if mapping, ok := parseNFTMapping(line); ok {
				mappings = append(mappings, mapping)
			}
		}
	}

	return mappings, nil
}

// nftRuleFields matches the rule AddPortMapping writes:
//
//	[iifname "em0"] tcp dport 80 dnat to 10.0.0.2:8080 comment "hospitus:web:id"
//
// The comment alone gave Instance and ID, so the value handed back could not be
// passed to Validate or AddPortMapping — protocol, ports and target were empty.
var nftRuleFields = regexp.MustCompile(
	`(?:iifname "([^"]+)" )?(tcp|udp) dport (\d+) dnat to ([0-9.]+):(\d+) .*comment "hospitus:([^:"]+):([^"]*)"`)

func parseNFTMapping(line string) (PortMapping, bool) {
	m := nftRuleFields.FindStringSubmatch(line)
	if m == nil {
		return PortMapping{}, false
	}
	hostPort, err := strconv.Atoi(m[3])
	if err != nil {
		return PortMapping{}, false
	}
	targetPort, err := strconv.Atoi(m[5])
	if err != nil {
		return PortMapping{}, false
	}
	return PortMapping{
		Active:        true,
		HostInterface: m[1],
		Protocol:      Protocol(m[2]),
		HostPort:      hostPort,
		TargetIP:      m[4],
		TargetPort:    targetPort,
		Instance:      m[6],
		ID:            m[7],
	}, true
}

// Cleanup cleans up resources
func (n *NFTablesBackend) Cleanup(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Delete the entire hospitus table
	cmd := exec.CommandContext(ctx, "nft", "delete", "table", "ip", nftTableName)
	_ = cmd.Run() // Ignore errors

	return nil
}
