package qemu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Compile-time assertion
var _ provider.PortForwardProvider = (*QEMUProvider)(nil)

// AddPortForward adds a port forwarding rule to a QEMU VM.
//
// The rule is persisted in the config either way. For running VMs it is also
// applied immediately via QMP; at the next start refreshHostPorts rebuilds the
// user-net hostfwd fragments from the stored rules, so a rule added to a
// stopped VM — or one added via QMP and then restarted — is on the command
// line QEMU is launched with.
func (p *QEMUProvider) AddPortForward(ctx context.Context, handle provider.InstanceHandle, pf provider.PortForward) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if pf.Protocol != "tcp" && pf.Protocol != "udp" {
		return fmt.Errorf("unsupported protocol %q: must be tcp or udp", pf.Protocol)
	}
	if pf.HostPort <= 0 || pf.HostPort > 65535 {
		return fmt.Errorf("invalid host port %d", pf.HostPort)
	}
	if pf.GuestPort <= 0 || pf.GuestPort > 65535 {
		return fmt.Errorf("invalid guest port %d", pf.GuestPort)
	}

	vmName := handle.ID

	// Load and update config
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", vmName))
	cfg, err := p.loadVMConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load VM config: %w", err)
	}

	if cfg.Spec.ProviderConfig == nil {
		cfg.Spec.ProviderConfig = make(map[string]interface{})
	}

	// Append to port_forwards list
	existing := portForwardsFromConfig(cfg.Spec.ProviderConfig)
	for _, e := range existing {
		if e.Protocol == pf.Protocol && e.HostPort == pf.HostPort {
			return fmt.Errorf("host port %d/%s is already forwarded", pf.HostPort, pf.Protocol)
		}
	}
	existing = append(existing, pf)
	cfg.Spec.ProviderConfig["port_forwards"] = portForwardsToRaw(existing)

	if err := p.saveVMConfig(cfg, configPath); err != nil {
		return fmt.Errorf("failed to save VM config: %w", err)
	}

	// If the VM is actually running, apply immediately via QMP. Determine this
	// from real process state rather than the presence of a metadata key (which
	// is absent on handles from ListInstances and can be stale otherwise).
	running, err := p.isVMRunning(ctx, vmName)
	if err == nil && running {
		qmpSocket := p.qmpSocketFor(vmName)
		if _, statErr := os.Stat(qmpSocket); statErr != nil {
			// Saved to the config either way, so the rule takes effect at the
			// next start; without this line the caller had no way to know it
			// had not taken effect now.
			p.logger.Warn("the VM is running but its QMP socket is unreachable; "+
				"the port forward will apply at the next start",
				"vm", vmName, "socket", qmpSocket, logging.FieldError, statErr)
		} else {
			qmp, err := NewQMPClient(qmpSocket)
			if err != nil {
				// Non-fatal: config is persisted, will apply on next start
				p.logger.Warn("VM running but QMP connect failed; rule will apply on restart",
					"vm", vmName, "error", err)
				return nil
			}
			defer qmp.Close()
			if err := qmp.Connect(); err != nil {
				// Non-fatal: config is persisted, will apply on next start
				p.logger.Warn("VM running but QMP connect failed; rule will apply on restart",
					"vm", vmName, "error", err)
				return nil
			}
			if err := qmp.HostfwdAdd("net0", pf.Protocol, pf.HostPort, pf.GuestPort); err != nil {
				p.logger.Warn("QMP HostfwdAdd failed; rule will apply on restart",
					"vm", vmName, "error", err)
			}
		}
	}

	return nil
}

// RemovePortForward removes a port forwarding rule from a QEMU VM.
func (p *QEMUProvider) RemovePortForward(ctx context.Context, handle provider.InstanceHandle, protocol string, hostPort int) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if protocol != "tcp" && protocol != "udp" {
		return fmt.Errorf("unsupported protocol %q: must be tcp or udp", protocol)
	}

	vmName := handle.ID

	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", vmName))
	cfg, err := p.loadVMConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load VM config: %w", err)
	}

	existing := portForwardsFromConfig(cfg.Spec.ProviderConfig)
	filtered := make([]provider.PortForward, 0, len(existing))
	found := false
	for _, e := range existing {
		if e.Protocol == protocol && e.HostPort == hostPort {
			found = true
			continue
		}
		filtered = append(filtered, e)
	}
	if !found {
		return fmt.Errorf("no forwarding rule found for %s port %d", protocol, hostPort)
	}

	if cfg.Spec.ProviderConfig == nil {
		cfg.Spec.ProviderConfig = make(map[string]interface{})
	}
	cfg.Spec.ProviderConfig["port_forwards"] = portForwardsToRaw(filtered)

	if err := p.saveVMConfig(cfg, configPath); err != nil {
		return fmt.Errorf("failed to save VM config: %w", err)
	}

	// If the VM is actually running, remove immediately via QMP. Determine this
	// from real process state rather than the presence of a metadata key, as the
	// Add path does: the key is absent on handles from ListInstances and can be
	// stale otherwise.
	running, err := p.isVMRunning(ctx, vmName)
	if err == nil && running {
		qmpSocket := p.qmpSocketFor(vmName)
		if _, statErr := os.Stat(qmpSocket); statErr != nil {
			// Saved to the config either way, so the rule takes effect at the
			// next start; without this line the caller had no way to know it
			// had not taken effect now.
			p.logger.Warn("the VM is running but its QMP socket is unreachable; "+
				"the port forward will apply at the next start",
				"vm", vmName, "socket", qmpSocket, logging.FieldError, statErr)
		} else {
			qmp, err := NewQMPClient(qmpSocket)
			if err != nil {
				p.logger.Warn("VM running but QMP connect failed; rule removed from config only",
					"vm", vmName, "error", err)
				return nil
			}
			defer qmp.Close()
			if err := qmp.Connect(); err != nil {
				p.logger.Warn("VM running but QMP connect failed; rule removed from config only",
					"vm", vmName, "error", err)
				return nil
			}
			if err := qmp.HostfwdRemove("net0", protocol, hostPort); err != nil {
				p.logger.Warn("QMP HostfwdRemove failed",
					"vm", vmName, "error", err)
			}
		}
	}

	return nil
}

// ListPortForwards returns all port forwarding rules for a QEMU VM.
func (p *QEMUProvider) ListPortForwards(_ context.Context, handle provider.InstanceHandle) ([]provider.PortForward, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmName := handle.ID

	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", vmName))
	cfg, err := p.loadVMConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load VM config: %w", err)
	}

	return portForwardsFromConfig(cfg.Spec.ProviderConfig), nil
}

// portForwardsFromConfig extracts []PortForward from the raw provider config map.
func portForwardsFromConfig(pc map[string]interface{}) []provider.PortForward {
	if pc == nil {
		return nil
	}
	raw, ok := pc["port_forwards"].([]interface{})
	if !ok {
		return nil
	}
	result := make([]provider.PortForward, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		pf := provider.PortForward{Protocol: "tcp"}
		if proto, ok := m["protocol"].(string); ok && proto != "" {
			pf.Protocol = proto
		}
		if h, ok := rawPort(m["host"]); ok {
			pf.HostPort = h
		}
		if g, ok := rawPort(m["guest"]); ok {
			pf.GuestPort = g
		}
		// legacy "container" key used during creation
		if g, ok := rawPort(m["container"]); ok && pf.GuestPort == 0 {
			pf.GuestPort = g
		}
		if pf.HostPort > 0 && pf.GuestPort > 0 {
			result = append(result, pf)
		}
	}
	return result
}

// rawPort reads a port out of the raw map, which holds an int when the entry
// was just written by portForwardsToRaw and a float64 once it has been through
// JSON. Reading only float64 made a rule invisible to the code that rebuilds
// the QEMU arguments in the same call that stored it.
func rawPort(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

// portForwardsToRaw converts []PortForward back to the raw map format used in ProviderConfig.
func portForwardsToRaw(pfs []provider.PortForward) []interface{} {
	raw := make([]interface{}, 0, len(pfs))
	for _, pf := range pfs {
		raw = append(raw, map[string]interface{}{
			"protocol": pf.Protocol,
			"host":     pf.HostPort,
			"guest":    pf.GuestPort,
		})
	}
	return raw
}
