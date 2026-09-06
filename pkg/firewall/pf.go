package firewall

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
)

const (
	// PF anchor name for Hospitus rules
	pfAnchorName = "hospitus"
)

// DefaultPFRulesDir is the default directory for PF rules
const DefaultPFRulesDir = "/var/lib/hospitus/firewall/pf"

// PFBackend implements the Backend interface for PF (Packet Filter)
type PFBackend struct {
	mu       sync.Mutex
	rulesDir string
	proxy    *TCPProxy // TCP proxy for localhost forwarding
	logger   *slog.Logger
}

// NewPFBackend creates a new PF backend with the default rules directory.
// Use NewPFBackendWithDir to specify a custom rules directory.
func NewPFBackend() *PFBackend {
	return NewPFBackendWithDir(DefaultPFRulesDir)
}

// NewPFBackendWithDir creates a new PF backend with a custom rules directory
func NewPFBackendWithDir(rulesDir string) *PFBackend {
	if rulesDir == "" {
		rulesDir = DefaultPFRulesDir
	}
	return &PFBackend{
		rulesDir: rulesDir,
		proxy:    NewTCPProxy(),
		logger:   logging.WithComponent("firewall-pf"),
	}
}

// Name returns the backend name
func (p *PFBackend) Name() BackendType {
	return BackendTypePF
}

// IsAvailable checks if PF is available
func (p *PFBackend) IsAvailable() bool {
	// Check if pfctl exists
	_, err := exec.LookPath("pfctl")
	if err != nil {
		return false
	}

	// Bound the probe so a hung pfctl cannot stall availability detection.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Check if PF is enabled
	cmd := exec.CommandContext(ctx, "pfctl", "-s", "info")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}

	return strings.Contains(string(output), "Status: Enabled")
}

// Initialize initializes the PF backend
func (p *PFBackend) Initialize(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Create rules directory
	if err := os.MkdirAll(p.rulesDir, 0o755); err != nil {
		return fmt.Errorf("failed to create rules directory: %w", err)
	}

	// Create initial anchor files if they don't exist
	natFile := filepath.Join(p.rulesDir, "nat.rules")
	rdrFile := filepath.Join(p.rulesDir, "rdr.rules")
	filterFile := filepath.Join(p.rulesDir, "filter.rules")
	combinedFile := filepath.Join(p.rulesDir, "combined.rules")

	// Use 0600 permissions for firewall rules
	// These files contain network topology information
	if _, err := os.Stat(natFile); os.IsNotExist(err) {
		if err := os.WriteFile(natFile, []byte("# Hospitus NAT rules\n"), 0o600); err != nil {
			return fmt.Errorf("failed to create nat.rules: %w", err)
		}
	}

	if _, err := os.Stat(rdrFile); os.IsNotExist(err) {
		if err := os.WriteFile(rdrFile, []byte("# Hospitus redirect rules\n"), 0o600); err != nil {
			return fmt.Errorf("failed to create rdr.rules: %w", err)
		}
	}

	if _, err := os.Stat(filterFile); os.IsNotExist(err) {
		if err := os.WriteFile(filterFile, []byte("# Hospitus filter rules\n"), 0o600); err != nil {
			return fmt.Errorf("failed to create filter.rules: %w", err)
		}
	}

	if _, err := os.Stat(combinedFile); os.IsNotExist(err) {
		if err := os.WriteFile(combinedFile, []byte("# Hospitus combined rules\n"), 0o600); err != nil {
			return fmt.Errorf("failed to create combined.rules: %w", err)
		}
	}

	// Verify the hospitus anchor is declared in the active PF ruleset. Hospitus never
	// edits the operator's pf.conf; if the anchor is missing we warn with the
	// exact lines to add and continue — NAT and redirect rules are not
	// evaluated until the operator declares the anchor and reloads PF.
	if err := p.checkAnchorNoLock(ctx); err != nil {
		p.logger.Warn("PF hospitus anchor is not declared in pf.conf; NAT/port-forwarding will not work until you add it",
			logging.FieldError, err)
		p.logger.Warn("Add these lines to /etc/pf.conf, then run: pfctl -f /etc/pf.conf",
			"line1", fmt.Sprintf("nat-anchor %q", pfAnchorName),
			"line2", fmt.Sprintf("rdr-anchor %q", pfAnchorName),
			"line3", fmt.Sprintf("anchor %q", pfAnchorName),
		)
	}

	// Load existing rules into PF anchor on initialization
	// This ensures rules persist across daemon restarts
	if err := p.reloadRulesNoLock(ctx); err != nil {
		// Log warning but don't fail initialization
		// Rules might be empty which is fine
		p.logger.Warn("Failed to load existing PF rules", logging.FieldError, err)
	}

	return nil
}

// checkAnchorNoLock verifies the hospitus anchor is declared in the active PF
// ruleset. Hospitus never edits the operator's pf.conf: when the anchor is missing
// this returns an error so the caller can warn with the exact lines to add.
// Caller must hold p.mu.
func (p *PFBackend) checkAnchorNoLock(ctx context.Context) error {
	// Check if PF is running at all
	infoCmd := exec.CommandContext(ctx, "pfctl", "-s", "info")
	infoOut, err := infoCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("PF unavailable: %w", err)
	}
	if !strings.Contains(string(infoOut), "Status: Enabled") {
		return fmt.Errorf("PF is not enabled; run: service pf start")
	}

	// Hospitus loads into three anchors, and each has to be declared for the
	// rules it carries to take effect. Checking one and reporting success let
	// port mappings load into a private anchor nothing ever consulted.
	//
	// Verified on FreeBSD: "pfctl -sn" prints the two translation anchors
	// (nat-anchor, rdr-anchor) and "pfctl -sr" prints the filter one.
	natOut, _ := exec.CommandContext(ctx, "pfctl", "-sn").CombinedOutput()
	filterOut, _ := exec.CommandContext(ctx, "pfctl", "-sr").CombinedOutput()

	var missing []string
	for _, want := range []struct{ kind, ruleset string }{
		{"nat-anchor", string(natOut)},
		{"rdr-anchor", string(natOut)},
		{"anchor", string(filterOut)},
	} {
		if !anchorDeclared(want.ruleset, want.kind) {
			missing = append(missing, fmt.Sprintf("%s %q", want.kind, pfAnchorName))
		}
	}
	if len(missing) == 0 {
		p.logger.Debug("PF anchors already present in active ruleset", "anchor", pfAnchorName)
		return nil
	}

	return fmt.Errorf("pf.conf is missing %s: %s", pluralDeclaration(len(missing)), strings.Join(missing, ", "))
}

// pluralDeclaration keeps the message readable whether one or several anchors
// are missing.
func pluralDeclaration(n int) string {
	if n == 1 {
		return "this anchor declaration"
	}
	return "these anchor declarations"
}

// anchorDeclared reports whether a declaration of the given kind for the
// hospitus anchor is present in a printed ruleset. It matches the exact bare
// anchor name Hospitus loads rules into (pfctl -a hospitus); the "hospitus/*"
// glob form matches only sub-anchors and therefore does not count.
func anchorDeclared(ruleset, kind string) bool {
	return strings.Contains(ruleset, fmt.Sprintf("%s %q", kind, pfAnchorName))
}

// natAnchorDeclared is the nat-anchor case, kept for callers that only ask
// about translation.
func natAnchorDeclared(natRuleset string) bool {
	return anchorDeclared(natRuleset, "nat-anchor")
}

// dedupeRuleLines keeps one copy of each distinct rule, carrying along the ID
// comment of the copy it keeps.
//
// A NAT rule covers a network, not an instance, so every jail on 10.0.0.0/24
// stores its own entry for the same "nat on re0 from 10.0.0.0/24". The files
// keep those entries apart so that destroying one jail does not tear down the
// NAT the others still use, but the anchor only needs the rule once; loading
// it once per jail grows the anchor with every jail started.
//
// Rules are compared on their whitespace-normalized form: PF reads "nat on re0"
// and "nat  on re0" as one rule, so the dedupe must too.
func dedupeRuleLines(content []byte) []byte {
	seen := make(map[string]bool)
	var out []byte
	var pending []string

	emit := func(line string) {
		out = append(out, line...)
		out = append(out, '\n')
	}

	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue
		case strings.HasPrefix(trimmed, "#"):
			pending = append(pending, trimmed)
			continue
		}
		key := strings.Join(strings.Fields(trimmed), " ")
		if !seen[key] {
			seen[key] = true
			for _, comment := range pending {
				emit(comment)
			}
			emit(trimmed)
		}
		pending = nil
	}
	return out
}

// reloadRulesNoLock rebuilds the combined rule file and loads it into the hospitus
// anchor. It does not acquire p.mu; callers must already hold the lock.
func (p *PFBackend) reloadRulesNoLock(ctx context.Context) error {
	// PF requires nat and rdr rules to be loaded together in a single pfctl call
	// because each -f flag replaces all rules in the anchor.
	// We combine both files into a single temporary file for loading.

	natFile := filepath.Join(p.rulesDir, "nat.rules")
	rdrFile := filepath.Join(p.rulesDir, "rdr.rules")
	filterFile := filepath.Join(p.rulesDir, "filter.rules")
	combinedFile := filepath.Join(p.rulesDir, "combined.rules")

	var combined []byte

	// Read nat rules (translation — must come first in PF ordering)
	natContent, err := readRuleFile(natFile)
	if err != nil {
		return err
	}
	if len(natContent) > 0 {
		combined = append(combined, natContent...)
		if len(natContent) > 0 && natContent[len(natContent)-1] != '\n' {
			combined = append(combined, '\n')
		}
	}

	// Read rdr rules (translation)
	rdrContent, err := readRuleFile(rdrFile)
	if err != nil {
		return err
	}
	if len(rdrContent) > 0 {
		combined = append(combined, rdrContent...)
		if len(rdrContent) > 0 && rdrContent[len(rdrContent)-1] != '\n' {
			combined = append(combined, '\n')
		}
	}

	// Read filter rules (pass/block — must come after translation in PF ordering)
	filterContent, err := readRuleFile(filterFile)
	if err != nil {
		return err
	}
	if len(filterContent) > 0 {
		combined = append(combined, filterContent...)
	}

	// Write combined file
	// Use 0600 permissions for firewall rules
	if err := atomicWriteFile(combinedFile, dedupeRuleLines(combined), 0o600); err != nil {
		return fmt.Errorf("failed to write combined rules: %w", err)
	}

	// Load combined rules into anchor
	cmd := exec.CommandContext(ctx, "pfctl", "-a", pfAnchorName, "-f", combinedFile)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to load rules: %w: %s", err, output)
	}

	return nil
}

// AddFilterRule appends a raw PF filter rule to the hospitus anchor's filter.rules.
// ruleID is used to prevent duplicates on restarts (idempotent).
// The rule must be valid PF syntax, e.g. "pass in quick on bridge0 proto udp ...".
func (p *PFBackend) AddFilterRule(ctx context.Context, ruleID, instance, pfRule string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	filterFile := filepath.Join(p.rulesDir, "filter.rules")
	if err := p.appendRule(filterFile, ruleID, instance, pfRule); err != nil {
		return fmt.Errorf("failed to append filter rule: %w", err)
	}
	return p.reloadRulesNoLock(ctx)
}

// AddRawNATRule appends a pre-formatted translation rule (e.g. a nat6 rule) to
// the nat ruleset. Translation rules must live in nat.rules, before the filter
// rules — writing a nat/nat6/rdr rule into filter.rules makes pfctl reject the
// whole anchor.
func (p *PFBackend) AddRawNATRule(ctx context.Context, ruleID, instance, pfRule string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	natFile := filepath.Join(p.rulesDir, "nat.rules")
	if err := p.appendRule(natFile, ruleID, instance, pfRule); err != nil {
		return fmt.Errorf("failed to append nat rule: %w", err)
	}
	return p.reloadRulesNoLock(ctx)
}

// AddPortMapping adds a port forwarding rule
func (p *PFBackend) AddPortMapping(ctx context.Context, mapping PortMapping) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := mapping.Validate(); err != nil {
		return err
	}

	// Check for host port conflict
	rdrFile := filepath.Join(p.rulesDir, "rdr.rules")
	if conflictID := p.hostPortInUse(rdrFile, mapping.HostPort, string(mapping.Protocol)); conflictID != "" {
		// If it's the same ID, skip silently (duplicate add)
		if conflictID == mapping.ID {
			return nil
		}
		return fmt.Errorf("host port %d/%s is already in use by rule %s", mapping.HostPort, mapping.Protocol, conflictID)
	}

	// Start the localhost TCP proxy before writing any PF rule.
	//
	// The BSD kernel handles localhost connections locally, before packets ever
	// reach PF, so a rdr rule cannot serve 127.0.0.1; the proxy accepts there
	// and forwards to the jail. It is started first because binding the host
	// port is the step most likely to fail, and failing before any rule file is
	// touched leaves nothing to undo. Every later failure below tears the proxy
	// back down (stopProxyOnError), so the two never disagree.
	proxyStarted := false
	if err := p.proxy.Start(mapping); err != nil {
		// Log but don't fail - continue with PF rules for external traffic
		p.logger.Warn("Failed to start localhost proxy",
			"target_ip", mapping.TargetIP,
			"target_port", mapping.TargetPort,
			logging.FieldError, err)
	} else {
		proxyStarted = true
	}

	// If a later step fails, tear down the proxy we started so we don't leave
	// an orphaned localhost forwarder without matching PF rules.
	stopProxyOnError := func() {
		if proxyStarted {
			if stopErr := p.proxy.Stop(mapping); stopErr != nil {
				p.logger.Warn("Failed to stop localhost proxy during rollback", logging.FieldError, stopErr)
			}
		}
	}

	// Generate PF rdr rules (external interface + localhost)
	rules, err := p.generateRdrRules(mapping)
	if err != nil {
		stopProxyOnError()
		return err
	}

	// Append all rules to rdr rules file (rdrFile already defined above)
	if err := p.appendRules(rdrFile, mapping.ID, mapping.Instance, rules); err != nil {
		stopProxyOnError()
		return err
	}

	// No localhost NAT rule is written: the proxy connects from the daemon
	// process to the jail, so there is nothing to translate.

	if err := p.reloadRulesNoLock(ctx); err != nil {
		stopProxyOnError()
		return err
	}

	return nil
}

// RemovePortMapping removes a port forwarding rule
func (p *PFBackend) RemovePortMapping(ctx context.Context, mapping PortMapping) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Stop the TCP proxy for this mapping
	if err := p.proxy.Stop(mapping); err != nil {
		// Log but don't fail - continue with rule removal
		p.logger.Warn("Failed to stop localhost proxy", logging.FieldError, err)
	}

	// Remove from rdr rules file
	rdrFile := filepath.Join(p.rulesDir, "rdr.rules")
	if err := p.removeRule(rdrFile, mapping.ID); err != nil {
		return err
	}

	// Also remove the localhost NAT rule
	natFile := filepath.Join(p.rulesDir, "nat.rules")
	// Ignore error since the NAT rule might not exist (for older rules)
	_ = p.removeRule(natFile, mapping.ID+"-localhost-nat")

	return p.reloadRulesNoLock(ctx)
}

// AddNATRule adds a NAT rule
func (p *PFBackend) AddNATRule(ctx context.Context, rule NATRule) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := rule.Validate(); err != nil {
		return err
	}

	// Generate PF NAT rule
	// nat on $ext_if from 10.0.0.0/24 to any -> ($ext_if)
	pfRule, err := p.generateNatRule(rule)
	if err != nil {
		return err
	}

	// Append to nat rules file
	natFile := filepath.Join(p.rulesDir, "nat.rules")
	if err := p.appendRule(natFile, rule.ID, rule.Instance, pfRule); err != nil {
		return err
	}

	// Add companion filter pass rules so the hospitus anchor (evaluated AFTER
	// any broad "block all" without quick) overrides the block:
	//
	//   pass in  — jail-initiated traffic arriving at the host (jail → internet)
	//   pass out — host/forwarded traffic leaving towards the jail:
	//              • host browser → jail IP directly
	//              • rdr-redirected external clients → jail (port forwarding)
	filterFile := filepath.Join(p.rulesDir, "filter.rules")

	passInRule := fmt.Sprintf("pass in inet from %s keep state", rule.SourceNetwork)
	if err := p.appendRule(filterFile, rule.ID+"-pass", rule.Instance, passInRule); err != nil {
		p.removeRule(natFile, rule.ID) //nolint:errcheck
		return fmt.Errorf("failed to add filter pass-in rule: %w", err)
	}

	passOutRule := fmt.Sprintf("pass out inet to %s keep state", rule.SourceNetwork)
	if err := p.appendRule(filterFile, rule.ID+"-pass-out", rule.Instance, passOutRule); err != nil {
		p.removeRule(natFile, rule.ID)            //nolint:errcheck
		p.removeRule(filterFile, rule.ID+"-pass") //nolint:errcheck
		return fmt.Errorf("failed to add filter pass-out rule: %w", err)
	}

	return p.reloadRulesNoLock(ctx)
}

// RemoveNATRule removes a NAT rule
func (p *PFBackend) RemoveNATRule(ctx context.Context, rule NATRule) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Remove from nat rules file
	natFile := filepath.Join(p.rulesDir, "nat.rules")
	if err := p.removeRule(natFile, rule.ID); err != nil {
		return err
	}

	// Remove companion filter pass rules (both directions)
	filterFile := filepath.Join(p.rulesDir, "filter.rules")
	if err := p.removeRule(filterFile, rule.ID+"-pass"); err != nil {
		p.logger.Warn("Failed to remove companion filter pass-in rule", "rule_id", rule.ID, "error", err)
	}
	if err := p.removeRule(filterFile, rule.ID+"-pass-out"); err != nil {
		p.logger.Warn("Failed to remove companion filter pass-out rule", "rule_id", rule.ID, "error", err)
	}

	return p.reloadRulesNoLock(ctx)
}

// ApplyRules applies all rules for an instance.
//
// Not atomic: the instance's existing rules are removed first, then each NAT
// rule and port mapping is added and loaded in turn, so traffic can meet a
// partial ruleset while it runs, and a failure part-way leaves the instance
// with the rules added so far. Callers serialize their own instances.
func (p *PFBackend) ApplyRules(ctx context.Context, ruleSet RuleSet) error {
	// First remove existing rules for this instance
	if err := p.RemoveAllRules(ctx, ruleSet.Instance); err != nil {
		return err
	}

	// Add NAT rules
	for _, rule := range ruleSet.NATRules {
		if err := p.AddNATRule(ctx, rule); err != nil {
			return fmt.Errorf("failed to add NAT rule: %w", err)
		}
	}

	// Add port mappings
	for i := range ruleSet.PortMappings {
		mapping := ruleSet.PortMappings[i]
		if err := p.AddPortMapping(ctx, mapping); err != nil {
			return fmt.Errorf("failed to add port mapping: %w", err)
		}
	}

	return nil
}

// RemoveAllRules removes all rules for an instance
func (p *PFBackend) RemoveAllRules(ctx context.Context, instance string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// First, find and stop all TCP proxies for this instance
	rdrFile := filepath.Join(p.rulesDir, "rdr.rules")
	mappings := p.findInstancePortMappings(rdrFile, instance)
	for i := range mappings {
		mapping := mappings[i]
		if err := p.proxy.Stop(mapping); err != nil {
			p.logger.Warn("Failed to stop proxy",
				"host_port", mapping.HostPort,
				logging.FieldError, err)
		}
	}

	// Remove from all rule files
	natFile := filepath.Join(p.rulesDir, "nat.rules")
	filterFile := filepath.Join(p.rulesDir, "filter.rules")

	if err := p.removeInstanceRules(natFile, instance); err != nil {
		return err
	}

	if err := p.removeInstanceRules(rdrFile, instance); err != nil {
		return err
	}

	if err := p.removeInstanceRules(filterFile, instance); err != nil {
		return err
	}

	return p.reloadRulesNoLock(ctx)
}

// findInstancePortMappings parses the rdr rules file to extract port mappings for an instance
func (p *PFBackend) findInstancePortMappings(filename, instance string) []PortMapping {
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil
	}

	var mappings []PortMapping
	lines := strings.Split(string(content), "\n")
	var currentID string
	var inInstanceBlock bool

	for _, line := range lines {
		// Check if this line contains our instance marker (exact match at end of comment)
		if strings.HasSuffix(line, fmt.Sprintf(" instance:%s", instance)) {
			inInstanceBlock = true
			// Extract ID from the comment: # ID: <id> instance:<instance>
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				currentID = parts[2]
			}
			continue
		}

		// Check if this is a new ID (end of our instance block)
		if inInstanceBlock && strings.HasPrefix(line, "# ID:") && !strings.HasSuffix(line, fmt.Sprintf(" instance:%s", instance)) {
			inInstanceBlock = false
			currentID = ""
		}

		// Parse rdr rules within our instance block
		if inInstanceBlock && strings.HasPrefix(line, "rdr ") {
			mapping := p.parseRdrRule(line, currentID, instance)
			if mapping != nil {
				mappings = append(mappings, *mapping)
			}
		}

		// Stop on empty line
		if inInstanceBlock && line == "" {
			inInstanceBlock = false
			currentID = ""
		}
	}

	return mappings
}

// parseRdrRule extracts a PortMapping from a PF rdr rule
// Format: rdr pass on <iface> proto <proto> from any to any port <hostPort> -> <targetIP> port <targetPort>
func (p *PFBackend) parseRdrRule(rule, id, instance string) *PortMapping {
	parts := strings.Fields(rule)
	if len(parts) < 12 {
		return nil
	}

	var protocol Protocol
	var hostPort, targetPort int
	var targetIP string

	for i, part := range parts {
		switch part {
		case "proto":
			if i+1 < len(parts) {
				protocol = Protocol(parts[i+1])
			}
		case "port":
			if i+1 < len(parts) {
				port, err := strconv.Atoi(parts[i+1])
				if err == nil {
					if hostPort == 0 {
						hostPort = port
					} else {
						targetPort = port
					}
				}
			}
		case "->":
			if i+1 < len(parts) {
				targetIP = parts[i+1]
			}
		}
	}

	if hostPort > 0 && protocol != "" {
		return &PortMapping{
			ID:         id,
			Instance:   instance,
			Protocol:   protocol,
			HostPort:   hostPort,
			TargetPort: targetPort,
			TargetIP:   targetIP,
		}
	}
	return nil
}

// ListRules lists all active port mappings
func (p *PFBackend) ListRules(ctx context.Context) ([]PortMapping, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Parse rules from rdr file
	rdrFile := filepath.Join(p.rulesDir, "rdr.rules")
	content, err := os.ReadFile(rdrFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var mappings []PortMapping
	lines := strings.Split(string(content), "\n")
	var curID, curInstance string
	for _, line := range lines {
		if strings.HasPrefix(line, "# ID:") {
			// Parse ID comment. Format: # ID: <id> instance:<instance>
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				curID = parts[2]
				curInstance = ""
				for _, part := range parts[3:] {
					if strings.HasPrefix(part, "instance:") {
						curInstance = strings.TrimPrefix(part, "instance:")
						break
					}
				}
			}
			continue
		}

		// Populate protocol/ports/target from the rdr rule that follows the
		// comment, so callers get complete mappings instead of zero values.
		if curID != "" && strings.HasPrefix(line, "rdr ") {
			if mapping := p.parseRdrRule(line, curID, curInstance); mapping != nil {
				mapping.Active = true
				mappings = append(mappings, *mapping)
				curID = ""
			}
		}
	}

	return mappings, nil
}

// Cleanup cleans up resources
func (p *PFBackend) Cleanup(_ context.Context) error {
	// Stop all TCP proxies
	p.proxy.StopAll()
	return nil
}

// validateIfaceName validates that an interface name is safe to include in PF rules.
// Accepted pattern: letter, then alphanumeric/underscore chars, ending with a digit.
// Examples: em0, vtnet0, re0, hospitus0
func validateIfaceName(iface string) error {
	if iface == "" {
		return fmt.Errorf("interface name must not be empty")
	}
	// "." and "-" belong here: a FreeBSD VLAN is em0.100, and Hospitus itself
	// creates hospitus-nat, which this rejected.
	for i, c := range iface {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9' && i > 0:
		case (c == '_' || c == '-' || c == '.') && i > 0:
		default:
			return fmt.Errorf("invalid interface name %q: character %q at position %d is not allowed", iface, c, i)
		}
	}
	return nil
}

// generateRdrRules generates PF rdr rules for external interface only
// Note: Localhost forwarding is handled by the TCP proxy (proxy.go), NOT by PF rules.
// This is because BSD's kernel handles localhost connections locally before PF sees them,
// so rdr on lo0 doesn't work reliably. The TCP proxy intercepts 127.0.0.1:port and
// forwards to the jail at the application level.
func (p *PFBackend) generateRdrRules(mapping PortMapping) ([]string, error) {
	iface := mapping.HostInterface
	if iface == "" {
		// There is no useful fallback: OpenBSD's "egress" group does not exist
		// on FreeBSD, and pfctl accepts an unknown interface without
		// complaint, so a guess loaded a rule that never matched.
		detected, err := p.getDefaultInterface()
		if err != nil {
			return nil, fmt.Errorf("cannot determine the external interface for a redirection to %s: %w (name it explicitly)", mapping.TargetIP, err)
		}
		iface = detected
	}

	if err := validateIfaceName(iface); err != nil {
		return nil, fmt.Errorf("port mapping interface rejected: %w", err)
	}

	rules := []string{
		// Rule for external interface (incoming traffic from network)
		fmt.Sprintf("rdr pass on %s proto %s from any to any port %d -> %s port %d",
			iface,
			mapping.Protocol,
			mapping.HostPort,
			mapping.TargetIP,
			mapping.TargetPort,
		),
		// Note: localhost access is handled by TCP proxy in proxy.go
		// PF rdr on lo0 doesn't work because kernel handles localhost locally
	}

	return rules, nil
}

// getDefaultInterface returns the interface carrying the default route.
//
// It reports an error rather than guessing: a redirection rule on the wrong
// interface loads without complaint and never matches.
func (p *PFBackend) getDefaultInterface() (string, error) {
	// Bound the route lookup so a hung "route" cannot stall rule generation.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "route", "-n", "get", "default")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("route -n get default: %w", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "interface:") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		if err := validateIfaceName(parts[1]); err != nil {
			return "", fmt.Errorf("route named an unusable interface: %w", err)
		}
		return parts[1], nil
	}
	return "", fmt.Errorf("no default route")
}

// generateNatRule generates a PF NAT rule
func (p *PFBackend) generateNatRule(rule NATRule) (string, error) {
	if err := validateIfaceName(rule.OutInterface); err != nil {
		return "", fmt.Errorf("NAT rule interface rejected: %w", err)
	}
	return fmt.Sprintf("nat on %s from %s to any -> (%s)",
		rule.OutInterface,
		rule.SourceNetwork,
		rule.OutInterface,
	), nil
}

// checkRuleText rejects multi-line injection in the fields that reach a PF rule
// file. PF rules are stored as text; a newline in any of them ends the line the
// writer intended and starts one the caller chose, inside Hospitus's own anchor.
func checkRuleText(id, instance string, rules ...string) error {
	if strings.ContainsAny(id, "\r\n") {
		return fmt.Errorf("rule ID contains newline")
	}
	if strings.ContainsAny(instance, "\r\n") {
		return fmt.Errorf("instance name contains newline")
	}
	for _, rule := range rules {
		if strings.ContainsAny(rule, "\r") {
			return fmt.Errorf("PF rule contains carriage return")
		}
		// A single trailing newline is how formatted PF rules arrive; an
		// embedded one would split a rule into several independent ones.
		if strings.Contains(strings.TrimSuffix(rule, "\n"), "\n") {
			return fmt.Errorf("PF rule contains multiple lines")
		}
	}
	return nil
}

// readRuleFile reads one of the anchor's rule files. A file that is not there
// yet is empty; anything else is an error.
//
// Discarding a read failure here was silent data loss: the combined file was
// written without those rules and "pfctl -a hospitus -f" then replaced the live
// anchor with the reduced set, so active NAT, redirect or filter rules simply
// vanished while the caller saw success.
func readRuleFile(path string) ([]byte, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read rule file %s: %w", path, err)
	}
	return content, nil
}

// appendRule appends a rule to a file with ID tracking
// Skips if a rule with the same ID already exists
func (p *PFBackend) appendRule(filename, id, instance, rule string) error {
	// Check if rule already exists
	if p.ruleExists(filename, id) {
		return nil // Rule already exists, skip
	}

	if err := checkRuleText(id, instance, rule); err != nil {
		return err
	}

	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	// Write ID comment with instance name and rule
	content := fmt.Sprintf("# ID: %s instance:%s\n%s\n", id, instance, rule)
	_, err = f.WriteString(content)
	return err
}

// ruleExists reports whether the file holds a rule with this ID — the marker
// and the rule under it, not the marker alone.
//
// An ID comment with nothing beneath it is worse than no entry at all: callers
// skip adding a rule they believe is present, so it is never written again and
// the file keeps saying it is there.
func (p *PFBackend) ruleExists(filename, id string) bool {
	content, err := os.ReadFile(filename)
	if err != nil {
		return false
	}

	marker := fmt.Sprintf("# ID: %s ", id)
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, marker) {
			continue
		}
		// The rule is the next line. A comment, a blank line or the end of the
		// file means the marker is orphaned and the rule has to be written.
		if i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if next != "" && !strings.HasPrefix(next, "#") {
				return true
			}
		}
	}
	return false
}

// hostPortInUse checks if a host port is already used by another rule
// Returns the ID of the conflicting rule or empty string if no conflict
func (p *PFBackend) hostPortInUse(filename string, hostPort int, protocol string) string {
	content, err := os.ReadFile(filename)
	if err != nil {
		return ""
	}

	lines := strings.Split(string(content), "\n")
	var currentID string

	for _, line := range lines {
		// Track current ID from comment lines
		if strings.HasPrefix(line, "# ID:") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				currentID = parts[2]
			}
			continue
		}

		// Check rdr rules for the host port
		if strings.HasPrefix(line, "rdr ") {
			// Parse: rdr pass on <iface> proto <proto> from any to any port <hostPort> -> ...
			if strings.Contains(line, fmt.Sprintf("proto %s", protocol)) &&
				strings.Contains(line, fmt.Sprintf(" port %d ", hostPort)) {
				return currentID
			}
		}
	}

	return ""
}

// appendRules appends multiple rules to a file with ID tracking
// Skips if a rule with the same ID already exists
func (p *PFBackend) appendRules(filename, id, instance string, rules []string) error {
	// Check if rule already exists
	if p.ruleExists(filename, id) {
		return nil // Rule already exists, skip
	}

	if err := checkRuleText(id, instance, rules...); err != nil {
		return err
	}

	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	// Write ID comment with instance name and all rules
	content := fmt.Sprintf("# ID: %s instance:%s\n", id, instance)
	for _, rule := range rules {
		content += rule + "\n"
	}
	_, err = f.WriteString(content)
	return err
}

// removeRule removes a rule by ID from a file
// This handles multiple rules per ID (skips until next ID comment or EOF)
func (p *PFBackend) removeRule(filename, id string) error {
	content, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string
	skipping := false

	for _, line := range lines {
		// Check if this is our target ID. Match the exact ID field of the
		// comment ("# ID: <id> instance:<instance>") rather than a substring,
		// so that removing "abc" does not also drop "abcdef".
		if commentRuleID(line) == id {
			skipping = true
			continue
		}
		if skipping {
			// A new ID comment ends our block — keep it.
			if strings.HasPrefix(line, "# ID:") {
				skipping = false
				newLines = append(newLines, line)
				continue
			}
			// A blank separator ends our block — drop it with the block.
			if strings.TrimSpace(line) == "" {
				skipping = false
				continue
			}
			// Any rule line belonging to our block (rdr/nat/pass/block/…): drop it.
			continue
		}
		newLines = append(newLines, line)
	}

	return atomicWriteFile(filename, []byte(strings.Join(newLines, "\n")), 0o600)
}

// commentRuleID returns the rule ID declared in an "# ID: <id> ..." comment
// line, or "" if the line is not a well-formed ID comment.
func commentRuleID(line string) string {
	if !strings.HasPrefix(line, "# ID:") {
		return ""
	}
	parts := strings.Fields(line)
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

// removeInstanceRules removes all rules for an instance
// This handles multiple rules per ID (skips until next ID comment or EOF)
func (p *PFBackend) removeInstanceRules(filename, instance string) error {
	content, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string
	skipping := false

	for _, line := range lines {
		// Check if this line contains our instance (exact match at end of comment)
		// The suffix check is safe because the format includes " instance:" prefix.
		// Even if instance name "foo" is a suffix of "bar-foo", " instance:foo" is not a suffix of " instance:bar-foo".
		if strings.HasSuffix(line, fmt.Sprintf(" instance:%s", instance)) {
			skipping = true
			continue
		}
		if skipping {
			// A new ID comment ends our block — keep it.
			if strings.HasPrefix(line, "# ID:") {
				skipping = false
				newLines = append(newLines, line)
				continue
			}
			// A blank separator ends our block — drop it with the block.
			if strings.TrimSpace(line) == "" {
				skipping = false
				continue
			}
			// Any rule line belonging to our block (rdr/nat/pass/block/…): drop it.
			continue
		}
		newLines = append(newLines, line)
	}

	return atomicWriteFile(filename, []byte(strings.Join(newLines, "\n")), 0o600)
}

// atomicWriteFile writes data to filename atomically via a temp file + rename.
// This prevents a partial write from corrupting the PF rule file.
func atomicWriteFile(filename string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(filename)
	tmp, err := os.CreateTemp(dir, ".pf-tmp-*")
	if err != nil {
		return fmt.Errorf("atomicWriteFile: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		// Clean up on failure (no-op if rename succeeded)
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("atomicWriteFile: write: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("atomicWriteFile: chmod: %w", err)
	}
	// Rename is atomic for the name, not for the contents: without this a crash
	// after the rename can leave the new name pointing at an empty file, and the
	// anchor would load nothing.
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("atomicWriteFile: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomicWriteFile: close: %w", err)
	}
	if err := os.Rename(tmpName, filename); err != nil {
		return fmt.Errorf("atomicWriteFile: rename: %w", err)
	}
	return nil
}
