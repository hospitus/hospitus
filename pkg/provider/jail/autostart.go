package jail

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
)

// Ensure JailProvider implements AutoStartProvider
var _ provider.AutoStartProvider = (*JailProvider)(nil)

// SetAutoStart configures auto-start settings for a jail
func (p *JailProvider) SetAutoStart(ctx context.Context, handle provider.InstanceHandle, config provider.AutoStartConfig) error {
	jailName := handle.ID

	// Validate priority range
	if config.Priority < 0 || config.Priority > 100 {
		return fmt.Errorf("priority must be between 0 and 100, got %d", config.Priority)
	}

	// Validate delay
	if config.DelayMS < 0 {
		return fmt.Errorf("delay must be non-negative, got %d", config.DelayMS)
	}

	// Load existing jail configuration
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	jailConfig, err := p.loadJailConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load jail configuration: %w", err)
	}

	// Update auto-start configuration
	jailConfig.AutoStart = config

	// Save updated configuration
	if err := p.saveJailConfig(jailConfig, configPath); err != nil {
		return fmt.Errorf("failed to save jail configuration: %w", err)
	}

	return nil
}

// GetAutoStart returns the auto-start configuration for a jail
func (p *JailProvider) GetAutoStart(ctx context.Context, handle provider.InstanceHandle) (*provider.AutoStartConfig, error) {
	jailName := handle.ID

	// Load jail configuration
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	jailConfig, err := p.loadJailConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load jail configuration: %w", err)
	}

	return &jailConfig.AutoStart, nil
}

// autoStartInstance holds information about an instance configured for auto-start
type autoStartInstance struct {
	Handle   provider.InstanceHandle
	Config   provider.AutoStartConfig
	JailName string
}

// ListAutoStartInstances returns all jails configured for auto-start, sorted by priority
func (p *JailProvider) ListAutoStartInstances(ctx context.Context) ([]provider.InstanceHandle, error) {
	instances, err := p.getAutoStartInstances(ctx)
	if err != nil {
		return nil, err
	}

	// Extract handles in priority order
	handles := make([]provider.InstanceHandle, len(instances))
	for i, inst := range instances {
		handles[i] = inst.Handle
	}

	return handles, nil
}

// getAutoStartInstances returns all jails configured for auto-start with full config
func (p *JailProvider) getAutoStartInstances(ctx context.Context) ([]autoStartInstance, error) {
	var instances []autoStartInstance

	// List all jail configurations
	entries, err := os.ReadDir(p.stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return instances, nil
		}
		return nil, fmt.Errorf("failed to read state directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		configPath := filepath.Join(p.stateDir, entry.Name())
		jailConfig, err := p.loadJailConfig(configPath)
		if err != nil {
			p.logWarn(ctx, "failed to load jail config for auto-start discovery", "config_path", configPath, logging.FieldError, err)
			continue
		}

		// Only include jails with auto-start enabled
		if jailConfig.AutoStart.Enabled {
			instances = append(instances, autoStartInstance{
				Handle: provider.InstanceHandle{
					ID:       jailConfig.Name,
					Provider: "jail",
				},
				Config:   jailConfig.AutoStart,
				JailName: jailConfig.Name,
			})
		}
	}

	// Sort by priority (lowest first)
	sort.Slice(instances, func(i, j int) bool {
		if instances[i].Config.Priority != instances[j].Config.Priority {
			return instances[i].Config.Priority < instances[j].Config.Priority
		}
		// If same priority, sort alphabetically by name
		return instances[i].JailName < instances[j].JailName
	})

	return instances, nil
}

// StartAutoStartInstances starts all jails configured for auto-start in priority order
func (p *JailProvider) StartAutoStartInstances(ctx context.Context) error {
	instances, err := p.getAutoStartInstances(ctx)
	if err != nil {
		return fmt.Errorf("failed to list auto-start instances: %w", err)
	}

	if len(instances) == 0 {
		p.logInfo(ctx, "no jails configured for auto-start")
		return nil
	}

	p.logInfo(ctx, "starting auto-start jails", "count", len(instances))

	var startErrors []string

	for _, inst := range instances {
		// Check if context is canceled
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Check if already running
		running, err := p.isJailRunning(ctx, inst.JailName)
		if err != nil {
			p.logWarn(ctx, "failed to check jail state during auto-start", logging.FieldInstance, inst.JailName, logging.FieldError, err)
		}
		if running {
			p.logInfo(ctx, "jail already running; skipping auto-start", logging.FieldInstance, inst.JailName)
			continue
		}

		if inst.Config.DelayMS > 0 {
			p.logInfo(ctx, "waiting before auto-starting jail", logging.FieldInstance, inst.JailName, "delay_ms", inst.Config.DelayMS, "priority", inst.Config.Priority)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(inst.Config.DelayMS) * time.Millisecond):
			}
		}

		// Start the jail
		p.logInfo(ctx, "auto-starting jail", logging.FieldInstance, inst.JailName, "priority", inst.Config.Priority)
		if err := p.StartInstance(ctx, inst.Handle); err != nil {
			errMsg := fmt.Sprintf("failed to start jail %s: %v", inst.JailName, err)
			p.logError(ctx, "failed to auto-start jail", logging.FieldInstance, inst.JailName, "priority", inst.Config.Priority, logging.FieldError, err)
			startErrors = append(startErrors, errMsg)
			// Continue with other jails even if one fails
			continue
		}

		p.logInfo(ctx, "auto-started jail successfully", logging.FieldInstance, inst.JailName, "priority", inst.Config.Priority)
	}

	if len(startErrors) > 0 {
		return fmt.Errorf("errors during auto-start: %s", strings.Join(startErrors, "; "))
	}

	p.logInfo(ctx, "auto-start complete", "started", len(instances)-len(startErrors), "total", len(instances))
	return nil
}
