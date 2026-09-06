package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/validation"
)

const (
	// DefaultRulesDir is the default directory for firewall rules storage
	DefaultRulesDir = "/var/lib/hospitus/firewall/rules"
)

// Manager provides high-level firewall management
type Manager struct {
	backend  Backend
	rulesDir string
	mu       sync.RWMutex
	logger   *slog.Logger
}

// NewManager creates a new firewall manager with automatic backend detection and the default rules directory.
// Use NewManagerWithDir to specify a custom rules directory.
func NewManager() (*Manager, error) {
	return NewManagerWithDir(DefaultRulesDir)
}

// NewManagerWithBackend creates a new manager with a specific backend and the default rules directory.
// Use NewManagerWithBackendAndDir to specify a custom rules directory.
func NewManagerWithBackend(backend Backend) *Manager {
	return NewManagerWithBackendAndDir(backend, DefaultRulesDir)
}

// NewManagerWithDir creates a new firewall manager with automatic backend detection and custom rules directory.
func NewManagerWithDir(rulesDir string) (*Manager, error) {
	backend, err := NewAutoBackend()
	if err != nil {
		return nil, fmt.Errorf("failed to create firewall backend: %w", err)
	}

	if rulesDir == "" {
		rulesDir = DefaultRulesDir
	}

	return &Manager{
		backend:  backend,
		rulesDir: rulesDir,
		logger:   logging.WithComponent("firewall"),
	}, nil
}

// NewManagerWithBackendAndDir creates a new manager with a specific backend and custom rules directory.
func NewManagerWithBackendAndDir(backend Backend, rulesDir string) *Manager {
	if rulesDir == "" {
		rulesDir = DefaultRulesDir
	}
	return &Manager{
		backend:  backend,
		rulesDir: rulesDir,
		logger:   logging.WithComponent("firewall"),
	}
}

// Initialize initializes the firewall manager
func (m *Manager) Initialize(ctx context.Context) error {
	m.logger.Info("Initializing firewall manager", "backend", m.backend.Name())

	// Create rules directory
	if err := os.MkdirAll(m.rulesDir, 0o755); err != nil {
		return fmt.Errorf("failed to create rules directory: %w", err)
	}

	if err := m.backend.Initialize(ctx); err != nil {
		return fmt.Errorf("failed to initialize backend: %w", err)
	}

	// Restore persisted rules. A hard error here (e.g. the rules directory is
	// unreadable) fails initialization; individual instance failures are logged
	// inside restoreRules and do not abort startup.
	if err := m.restoreRules(ctx); err != nil {
		return fmt.Errorf("failed to restore firewall rules: %w", err)
	}

	return nil
}

// Backend returns the underlying backend
func (m *Manager) Backend() Backend {
	return m.backend
}

// ExposePort creates a port forwarding rule for an instance
func (m *Manager) ExposePort(ctx context.Context, instance, provider string, protocol Protocol, hostPort, targetPort int, targetIP string) (*PortMapping, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	mapping := PortMapping{
		Instance:   instance,
		Provider:   provider,
		Protocol:   protocol,
		HostPort:   hostPort,
		TargetPort: targetPort,
		TargetIP:   targetIP,
		Active:     true,
	}
	mapping.ID = mapping.GenerateID()

	if err := mapping.Validate(); err != nil {
		return nil, err
	}

	// Add to backend
	m.logger.Info("Exposing port",
		logging.FieldInstance, instance,
		"host_port", hostPort,
		"target_port", targetPort,
		"proto", protocol)

	if err := m.backend.AddPortMapping(ctx, mapping); err != nil {
		return nil, fmt.Errorf("failed to add port mapping: %w", err)
	}

	// Persist rule
	if err := m.persistMapping(instance, mapping); err != nil {
		// Best-effort rollback of the backend rule. Surface a rollback failure
		// so the operator knows a port mapping may be left applied but unpersisted.
		if rbErr := m.backend.RemovePortMapping(ctx, mapping); rbErr != nil {
			m.logger.Warn("Failed to roll back port mapping after persist error",
				logging.FieldInstance, instance, "host_port", hostPort, logging.FieldError, rbErr)
			return nil, fmt.Errorf("failed to persist mapping: %w (rollback also failed: %w)", err, rbErr)
		}
		return nil, fmt.Errorf("failed to persist mapping: %w", err)
	}

	return &mapping, nil
}

// UnexposePort removes a port forwarding rule
func (m *Manager) UnexposePort(ctx context.Context, instance string, hostPort int, protocol Protocol) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Load existing rules
	ruleSet, err := m.loadRuleSet(instance)
	if err != nil {
		return err
	}

	// Find matching mapping
	var found *PortMapping
	var remaining []PortMapping
	for i := range ruleSet.PortMappings {
		pm := &ruleSet.PortMappings[i]
		if pm.HostPort == hostPort && pm.Protocol == protocol {
			found = pm
		} else {
			remaining = append(remaining, *pm)
		}
	}

	if found == nil {
		return fmt.Errorf("port mapping not found: %d/%s", hostPort, protocol)
	}

	if err := m.backend.RemovePortMapping(ctx, *found); err != nil {
		return fmt.Errorf("failed to remove port mapping: %w", err)
	}

	// Update persistence
	ruleSet.PortMappings = remaining
	return m.saveRuleSet(instance, ruleSet)
}

// ListExposedPorts lists all exposed ports for an instance.
// It only reads from the JSON persistence, so no backend initialization is
// needed; the context is accepted for API symmetry and is not used.
func (m *Manager) ListExposedPorts(_ context.Context, instance string) ([]PortMapping, error) {
	return m.ListExposedPortsReadOnly(instance)
}

// ListExposedPortsReadOnly lists all exposed ports without requiring backend initialization
// This is safe to call without root permissions as it only reads the JSON files
func (m *Manager) ListExposedPortsReadOnly(instance string) ([]PortMapping, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ruleSet, err := m.loadRuleSet(instance)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	return ruleSet.PortMappings, nil
}

// AddFilterRule adds a raw PF filter rule to the hospitus anchor.
// ruleID is used to prevent duplicates on restarts (idempotent).
// This is a no-op on non-PF backends (iptables, nftables).
func (m *Manager) AddFilterRule(ctx context.Context, ruleID, instance, pfRule string) error {
	if pfb, ok := m.backend.(*PFBackend); ok {
		return pfb.AddFilterRule(ctx, ruleID, instance, pfRule)
	}
	return nil
}

// AddRawNATRule appends a pre-formatted translation rule to the nat ruleset.
func (m *Manager) AddRawNATRule(ctx context.Context, ruleID, instance, pfRule string) error {
	if pfb, ok := m.backend.(*PFBackend); ok {
		return pfb.AddRawNATRule(ctx, ruleID, instance, pfRule)
	}
	return nil
}

// natRuleID names one NAT rule of an instance.
//
// The network is part of the identity because an instance can sit on several.
// While the ID was the instance name alone, the backend saw the second network
// as a rule it already had and skipped it, so a jail whose first network was an
// internal segment got NAT for that one only and reached nothing.
func natRuleID(instance, sourceNetwork string) string {
	slug := strings.NewReplacer("/", "_", ".", "-", ":", "-").Replace(sourceNetwork)
	return fmt.Sprintf("%s-nat-%s", instance, slug)
}

// SetupNAT configures NAT for an instance
func (m *Manager) SetupNAT(ctx context.Context, instance, provider, sourceNetwork, outInterface string) (*NATRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	rule := NATRule{
		ID:            natRuleID(instance, sourceNetwork),
		Instance:      instance,
		Provider:      provider,
		SourceNetwork: sourceNetwork,
		OutInterface:  outInterface,
		Active:        true,
	}

	if err := rule.Validate(); err != nil {
		return nil, err
	}

	if err := m.backend.AddNATRule(ctx, rule); err != nil {
		return nil, fmt.Errorf("failed to add NAT rule: %w", err)
	}

	if err := m.persistNATRule(instance, rule); err != nil {
		// Best-effort rollback of the backend rule. Surface a rollback failure
		// so the operator knows a NAT rule may be left applied but unpersisted.
		if rbErr := m.backend.RemoveNATRule(ctx, rule); rbErr != nil {
			m.logger.Warn("Failed to roll back NAT rule after persist error",
				logging.FieldInstance, instance, logging.FieldError, rbErr)
			return nil, fmt.Errorf("failed to persist NAT rule: %w (rollback also failed: %w)", err, rbErr)
		}
		return nil, fmt.Errorf("failed to persist NAT rule: %w", err)
	}

	return &rule, nil
}

// RemoveNAT removes NAT configuration for an instance
func (m *Manager) RemoveNAT(ctx context.Context, instance string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ruleSet, err := m.loadRuleSet(instance)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	// Remove all NAT rules from backend
	for _, rule := range ruleSet.NATRules {
		if err := m.backend.RemoveNATRule(ctx, rule); err != nil {
			return fmt.Errorf("failed to remove NAT rule: %w", err)
		}
	}

	// Update persistence
	ruleSet.NATRules = nil
	return m.saveRuleSet(instance, ruleSet)
}

// RemoveAllRules removes all firewall rules for an instance
func (m *Manager) RemoveAllRules(ctx context.Context, instance string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.backend.RemoveAllRules(ctx, instance); err != nil {
		return fmt.Errorf("failed to remove rules from backend: %w", err)
	}

	// Remove persistence file
	ruleFile, err := m.ruleFilePath(instance)
	if err != nil {
		return err
	}
	if err := os.Remove(ruleFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove rule file: %w", err)
	}

	return nil
}

// GetRuleSet returns the complete rule set for an instance
func (m *Manager) GetRuleSet(ctx context.Context, instance string) (*RuleSet, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ruleSet, err := m.loadRuleSet(instance)
	if err != nil {
		if os.IsNotExist(err) {
			return &RuleSet{Instance: instance}, nil
		}
		return nil, err
	}

	return &ruleSet, nil
}

// Cleanup cleans up all firewall rules
func (m *Manager) Cleanup(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.backend.Cleanup(ctx)
}

// persistMapping adds a port mapping to the persistent store
func (m *Manager) persistMapping(instance string, mapping PortMapping) error {
	ruleSet, err := m.loadRuleSet(instance)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	if ruleSet.Instance == "" {
		ruleSet.Instance = instance
	}

	// Check for duplicate (same host port and protocol): replace the existing
	// mapping in place so persisted state matches what the backend applied,
	// then save. Returning without updating would silently drop changes to the
	// target IP/port/ID.
	for k := range ruleSet.PortMappings {
		existing := ruleSet.PortMappings[k]
		if existing.HostPort == mapping.HostPort && existing.Protocol == mapping.Protocol {
			ruleSet.PortMappings[k] = mapping
			return m.saveRuleSet(instance, ruleSet)
		}
	}

	ruleSet.PortMappings = append(ruleSet.PortMappings, mapping)
	return m.saveRuleSet(instance, ruleSet)
}

// persistNATRule adds a NAT rule to the persistent store
func (m *Manager) persistNATRule(instance string, rule NATRule) error {
	ruleSet, err := m.loadRuleSet(instance)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	if ruleSet.Instance == "" {
		ruleSet.Instance = instance
	}

	// Replace existing NAT rule or add new
	found := false
	for i, existing := range ruleSet.NATRules {
		if existing.ID == rule.ID {
			ruleSet.NATRules[i] = rule
			found = true
			break
		}
	}
	if !found {
		ruleSet.NATRules = append(ruleSet.NATRules, rule)
	}

	return m.saveRuleSet(instance, ruleSet)
}

// ruleFilePath returns the persistence path for an instance after validating
// the instance name. This is the single choke point that prevents a crafted
// instance name (e.g. containing "/" or "..") from escaping m.rulesDir and
// causing arbitrary file read/write/delete as root (CWE-22).
func (m *Manager) ruleFilePath(instance string) (string, error) {
	if err := validation.ValidateInstanceName(instance); err != nil {
		return "", fmt.Errorf("invalid instance name: %w", err)
	}
	return filepath.Join(m.rulesDir, instance+".json"), nil
}

// loadRuleSet loads the rule set for an instance
func (m *Manager) loadRuleSet(instance string) (RuleSet, error) {
	ruleFile, err := m.ruleFilePath(instance)
	if err != nil {
		return RuleSet{}, err
	}
	data, err := os.ReadFile(ruleFile)
	if err != nil {
		return RuleSet{}, err
	}

	var ruleSet RuleSet
	if err := json.Unmarshal(data, &ruleSet); err != nil {
		return RuleSet{}, fmt.Errorf("failed to parse rule file: %w", err)
	}

	return ruleSet, nil
}

// saveRuleSet saves the rule set for an instance
func (m *Manager) saveRuleSet(instance string, ruleSet RuleSet) error {
	ruleFile, err := m.ruleFilePath(instance)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(ruleSet, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal rule set: %w", err)
	}

	// Atomic: os.WriteFile truncates first, so an interruption leaves invalid
	// JSON, and restoreRules skips a file it cannot parse — the rules it holds
	// are then simply not restored.
	if err := atomicWriteFile(ruleFile, data, 0o600); err != nil {
		return fmt.Errorf("failed to write rule file: %w", err)
	}

	return nil
}

// restoreRules restores all persisted rules on startup
func (m *Manager) restoreRules(ctx context.Context) error {
	entries, err := os.ReadDir(m.rulesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var restored, failed int
	var restoreErrs []error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matched, _ := filepath.Match("*.json", entry.Name())
		if !matched {
			continue
		}

		instance := entry.Name()[:len(entry.Name())-5] // Remove .json
		ruleSet, err := m.loadRuleSet(instance)
		if err != nil {
			continue // Skip invalid files
		}

		// Re-apply rules. Individual failures are collected and logged but do
		// not abort restoration of the remaining instances.
		if err := m.backend.ApplyRules(ctx, ruleSet); err != nil {
			failed++
			restoreErrs = append(restoreErrs, fmt.Errorf("instance %s: %w", instance, err))
			m.logger.Warn("Failed to restore firewall rules for instance", logging.FieldInstance, instance, logging.FieldError, err)
			continue
		}
		restored++
	}

	if failed > 0 {
		m.logger.Warn("Firewall rule restoration completed with failures",
			"restored", restored, "failed", failed, logging.FieldError, errors.Join(restoreErrs...))
	}

	return nil
}
