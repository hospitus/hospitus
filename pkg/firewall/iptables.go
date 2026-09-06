package firewall

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

const (
	// Chain names for Hospitus rules
	iptablesNATChain      = "HOSPITUS_NAT"
	iptablesPreRoutChain  = "HOSPITUS_PREROUTING"
	iptablesPostRoutChain = "HOSPITUS_POSTROUTING"
)

// IPTablesBackend implements the Backend interface for iptables
type IPTablesBackend struct {
	mu sync.Mutex
}

// NewIPTablesBackend creates a new iptables backend
func NewIPTablesBackend() *IPTablesBackend {
	return &IPTablesBackend{}
}

// Name returns the backend name
func (i *IPTablesBackend) Name() BackendType {
	return BackendTypeIPTables
}

// IsAvailable checks if iptables is available
func (i *IPTablesBackend) IsAvailable() bool {
	_, err := exec.LookPath("iptables")
	return err == nil
}

// Initialize initializes the iptables backend
func (i *IPTablesBackend) Initialize(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	// Create HOSPITUS chains in nat table
	chains := []string{iptablesPreRoutChain, iptablesPostRoutChain}
	for _, chain := range chains {
		// Try to create chain (ignore error if exists)
		_ = exec.CommandContext(ctx, "iptables", "-t", "nat", "-N", chain).Run()
	}

	// Add jump rules to the built-in chains, but only if they are not already
	// present. iptables -A always appends, so calling it unconditionally on
	// every startup would accumulate duplicate jump rules; use -C as the guard.
	if err := i.ensureJump(ctx, "PREROUTING", iptablesPreRoutChain); err != nil {
		return err
	}
	if err := i.ensureJump(ctx, "POSTROUTING", iptablesPostRoutChain); err != nil {
		return err
	}

	return nil
}

// ensureJump adds a "-j targetChain" rule to a built-in nat chain only if an
// identical rule does not already exist (checked with iptables -C).
func (i *IPTablesBackend) ensureJump(ctx context.Context, builtinChain, targetChain string) error {
	check := exec.CommandContext(ctx, "iptables", "-t", "nat", "-C", builtinChain, "-j", targetChain)
	if check.Run() == nil {
		return nil // Jump rule already present
	}
	add := exec.CommandContext(ctx, "iptables", "-t", "nat", "-A", builtinChain, "-j", targetChain)
	if output, err := add.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add %s jump: %w: %s", builtinChain, err, output)
	}
	return nil
}

// AddPortMapping adds a port forwarding rule (DNAT)
func (i *IPTablesBackend) AddPortMapping(ctx context.Context, mapping PortMapping) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if err := mapping.Validate(); err != nil {
		return err
	}

	// iptables -t nat -A HOSPITUS_PREROUTING [-i <if>] -p tcp --dport 80 -j DNAT --to-destination 10.0.0.2:8080
	cmd := exec.CommandContext(ctx, "iptables", portMappingArgs("-A", mapping)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add DNAT rule: %w: %s", err, output)
	}

	return nil
}

// portMappingArgs builds the iptables argument list for a DNAT port mapping.
// op is "-A" (add) or "-D" (delete). Add and Remove MUST use identical match
// criteria — including the optional "-i <interface>" — otherwise "-D" cannot
// find the rule that "-A" created.
func portMappingArgs(op string, mapping PortMapping) []string {
	args := []string{"-t", "nat", op, iptablesPreRoutChain}
	if mapping.HostInterface != "" {
		args = append(args, "-i", mapping.HostInterface)
	}
	args = append(args,
		"-p", string(mapping.Protocol),
		"--dport", fmt.Sprintf("%d", mapping.HostPort),
		"-j", "DNAT",
		"--to-destination", fmt.Sprintf("%s:%d", mapping.TargetIP, mapping.TargetPort),
		"-m", "comment", "--comment", fmt.Sprintf("hospitus:%s:%s", mapping.Instance, mapping.ID),
	)
	return args
}

// RemovePortMapping removes a port forwarding rule
func (i *IPTablesBackend) RemovePortMapping(ctx context.Context, mapping PortMapping) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	// Delete the rule using the same match criteria (including -i) as Add,
	// otherwise the interface-scoped variant would never be removed.
	cmd := exec.CommandContext(ctx, "iptables", portMappingArgs("-D", mapping)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to remove DNAT rule: %w: %s", err, output)
	}

	return nil
}

// AddNATRule adds a MASQUERADE rule for outbound NAT
func (i *IPTablesBackend) AddNATRule(ctx context.Context, rule NATRule) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if err := rule.Validate(); err != nil {
		return err
	}

	// iptables -t nat -A HOSPITUS_POSTROUTING -s 10.0.0.0/24 -o eth0 -j MASQUERADE
	args := []string{
		"-t", "nat",
		"-A", iptablesPostRoutChain,
		"-s", rule.SourceNetwork,
		"-o", rule.OutInterface,
		"-j", "MASQUERADE",
		"-m", "comment", "--comment", fmt.Sprintf("hospitus:%s:%s", rule.Instance, rule.ID),
	}

	cmd := exec.CommandContext(ctx, "iptables", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add MASQUERADE rule: %w: %s", err, output)
	}

	return nil
}

// RemoveNATRule removes a NAT rule
func (i *IPTablesBackend) RemoveNATRule(ctx context.Context, rule NATRule) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	args := []string{
		"-t", "nat",
		"-D", iptablesPostRoutChain,
		"-s", rule.SourceNetwork,
		"-o", rule.OutInterface,
		"-j", "MASQUERADE",
		"-m", "comment", "--comment", fmt.Sprintf("hospitus:%s:%s", rule.Instance, rule.ID),
	}

	cmd := exec.CommandContext(ctx, "iptables", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to remove MASQUERADE rule: %w: %s", err, output)
	}

	return nil
}

// ApplyRules applies all rules for an instance
func (i *IPTablesBackend) ApplyRules(ctx context.Context, ruleSet RuleSet) error {
	// First remove existing rules for this instance
	if err := i.RemoveAllRules(ctx, ruleSet.Instance); err != nil {
		return err
	}

	// Add NAT rules
	for _, rule := range ruleSet.NATRules {
		if err := i.AddNATRule(ctx, rule); err != nil {
			return fmt.Errorf("failed to add NAT rule: %w", err)
		}
	}

	// Add port mappings
	for k := range ruleSet.PortMappings {
		mapping := ruleSet.PortMappings[k]
		if err := i.AddPortMapping(ctx, mapping); err != nil {
			return fmt.Errorf("failed to add port mapping: %w", err)
		}
	}

	return nil
}

// RemoveAllRules removes all rules for an instance
func (i *IPTablesBackend) RemoveAllRules(ctx context.Context, instance string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	// List rules and find those with our comment
	chains := []string{iptablesPreRoutChain, iptablesPostRoutChain}
	for _, chain := range chains {
		if err := i.removeInstanceRulesFromChain(ctx, chain, instance); err != nil {
			return err
		}
	}

	return nil
}

// removeInstanceRulesFromChain removes all rules for an instance from a chain
func (i *IPTablesBackend) removeInstanceRulesFromChain(ctx context.Context, chain, instance string) error {
	// List rules
	cmd := exec.CommandContext(ctx, "iptables", "-t", "nat", "-L", chain, "--line-numbers", "-n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list rules: %w: %s", err, output)
	}

	// Find rule numbers to delete (in reverse order to avoid index shifting)
	lines := strings.Split(string(output), "\n")
	var ruleNumbers []int
	for _, line := range lines {
		if strings.Contains(line, fmt.Sprintf("hospitus:%s:", instance)) {
			// Extract line number (first field)
			fields := strings.Fields(line)
			if len(fields) > 0 {
				var num int
				_, _ = fmt.Sscanf(fields[0], "%d", &num)
				if num > 0 {
					ruleNumbers = append(ruleNumbers, num)
				}
			}
		}
	}

	// Delete in reverse order
	for j := len(ruleNumbers) - 1; j >= 0; j-- {
		cmd := exec.CommandContext(ctx, "iptables", "-t", "nat", "-D", chain, fmt.Sprintf("%d", ruleNumbers[j]))
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to delete rule %d: %w: %s", ruleNumbers[j], err, output)
		}
	}

	return nil
}

// ListRules lists all active port mappings
func (i *IPTablesBackend) ListRules(ctx context.Context) ([]PortMapping, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	cmd := exec.CommandContext(ctx, "iptables", "-t", "nat", "-L", iptablesPreRoutChain, "-n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list rules: %w: %s", err, output)
	}

	var mappings []PortMapping
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "hospitus:") {
			// Parse comment to extract instance and ID
			// This is a simplified parser
			mapping := PortMapping{
				Active: true,
			}

			// Extract from comment
			if idx := strings.Index(line, "hospitus:"); idx >= 0 {
				comment := line[idx:]
				parts := strings.Split(comment, ":")
				if len(parts) >= 3 {
					mapping.Instance = parts[1]
					mapping.ID = parts[2]
				}
			}

			if mapping.Instance != "" {
				mappings = append(mappings, mapping)
			}
		}
	}

	return mappings, nil
}

// Cleanup cleans up resources
func (i *IPTablesBackend) Cleanup(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	// Flush our chains
	for _, chain := range []string{iptablesPreRoutChain, iptablesPostRoutChain} {
		_ = exec.CommandContext(ctx, "iptables", "-t", "nat", "-F", chain).Run()
	}

	// Remove jump rules
	_ = exec.CommandContext(ctx, "iptables", "-t", "nat", "-D", "PREROUTING", "-j", iptablesPreRoutChain).Run()
	_ = exec.CommandContext(ctx, "iptables", "-t", "nat", "-D", "POSTROUTING", "-j", iptablesPostRoutChain).Run()

	// Delete our chains
	for _, chain := range []string{iptablesPreRoutChain, iptablesPostRoutChain} {
		_ = exec.CommandContext(ctx, "iptables", "-t", "nat", "-X", chain).Run()
	}

	return nil
}
