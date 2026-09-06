package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
)

// Auto-start allows containers to be automatically started when the hospitusd
// daemon starts. Podman implementation uses the --restart policy on containers
// and stores priority/delay metadata in the data directory as JSON files.
//
// Unlike VM/jail providers, Podman containers use a combination of:
//   - A podman restart policy ("always" when auto-start is on, "no" when off)
//     for process-level restart
//   - Data directory JSON files for priority and delay ordering

// Ensure PodmanProvider implements AutoStartProvider
var _ provider.AutoStartProvider = (*PodmanProvider)(nil)

// defaultAutoStartPriority is the priority a container gets when none was
// asked for. Priority 0 carries that meaning: it is the zero value an unset
// field arrives as, so a caller that wants to start first asks for 1.
const defaultAutoStartPriority = 50

// autostartDir returns the directory where auto-start configs are stored.
func (p *PodmanProvider) autostartDir() string {
	return filepath.Join(p.dataDir, "podman", "autostart")
}

// autostartFilePath returns the path to the auto-start config for a container.
func (p *PodmanProvider) autostartFilePath(containerName string) string {
	return filepath.Join(p.autostartDir(), containerName+".json")
}

// SetAutoStart configures auto-start settings for a Podman container.
//
// The restart policy is applied directly to the container via podman update,
// and the priority/delay metadata is stored in a JSON file.
//
// Priority 0 means "the default", 50. That convention is the API's, not this
// function's: an unset priority arrives as the zero value and there is no way
// to tell it from a deliberate 0, so callers that want the lowest priority ask
// for 1. It is applied here so every entry point — the API handler, the CLI,
// a manifest — records the same number.
func (p *PodmanProvider) SetAutoStart(ctx context.Context, handle provider.InstanceHandle, config provider.AutoStartConfig) error {
	containerName := handle.ID

	// Validate priority range
	if config.Priority < 0 || config.Priority > 100 {
		return fmt.Errorf("priority must be between 0 and 100, got %d", config.Priority)
	}

	if config.Priority == 0 && config.Enabled {
		config.Priority = defaultAutoStartPriority
	}

	// Apply restart policy via podman update
	restartPolicy := "no"
	if config.Enabled {
		restartPolicy = "always"
	}

	if out, err := p.cmd().CombinedOutput(ctx, p.podmanBin, "update", "--restart="+restartPolicy, containerName); err != nil {
		return fmt.Errorf("failed to update restart policy for container %s: %s: %w", containerName, string(out), err)
	}

	// Save priority and delay to JSON file
	if err := os.MkdirAll(p.autostartDir(), 0o755); err != nil {
		return fmt.Errorf("failed to create autostart directory: %w", err)
	}

	autostartFile := p.autostartFilePath(containerName)
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal autostart config: %w", err)
	}

	if err := os.WriteFile(autostartFile, data, 0o600); err != nil {
		return fmt.Errorf("failed to write autostart config: %w", err)
	}

	return nil
}

// GetAutoStart returns the auto-start configuration for a container.
func (p *PodmanProvider) GetAutoStart(ctx context.Context, handle provider.InstanceHandle) (*provider.AutoStartConfig, error) {
	containerName := handle.ID

	autostartFile := p.autostartFilePath(containerName)

	data, err := os.ReadFile(autostartFile)
	if err != nil {
		if os.IsNotExist(err) {
			// No auto-start configured - return disabled config
			return &provider.AutoStartConfig{
				Enabled:  false,
				Priority: defaultAutoStartPriority,
				DelayMS:  0,
			}, nil
		}
		return nil, fmt.Errorf("failed to read autostart config: %w", err)
	}

	var config provider.AutoStartConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse autostart config: %w", err)
	}

	return &config, nil
}

// ListAutoStartInstances returns all containers configured for auto-start,
// sorted by priority (lowest first).
func (p *PodmanProvider) ListAutoStartInstances(ctx context.Context) ([]provider.InstanceHandle, error) {
	autostartDir := p.autostartDir()

	entries, err := os.ReadDir(autostartDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read autostart directory: %w", err)
	}

	// Collect containers with auto-start enabled
	type containerWithPriority struct {
		handle   provider.InstanceHandle
		priority int
	}

	var containers []containerWithPriority

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		containerName := entry.Name()[:len(entry.Name())-5] // Strip .json
		handle := provider.InstanceHandle{
			ID:       containerName,
			Provider: providerName,
		}

		config, err := p.GetAutoStart(ctx, handle)
		if err != nil {
			continue // Skip containers with invalid config
		}

		if config.Enabled {
			containers = append(containers, containerWithPriority{
				handle:   handle,
				priority: config.Priority,
			})
		}
	}

	// Sort by priority (lowest first), then by name
	sort.Slice(containers, func(i, j int) bool {
		if containers[i].priority != containers[j].priority {
			return containers[i].priority < containers[j].priority
		}
		return containers[i].handle.ID < containers[j].handle.ID
	})

	// Extract handles
	handles := make([]provider.InstanceHandle, len(containers))
	for i, c := range containers {
		handles[i] = c.handle
	}

	return handles, nil
}

// StartAutoStartInstances starts all containers configured for auto-start
// in priority order with configured delays between starts.
func (p *PodmanProvider) StartAutoStartInstances(ctx context.Context) error {
	handles, err := p.ListAutoStartInstances(ctx)
	if err != nil {
		return fmt.Errorf("failed to list auto-start instances: %w", err)
	}

	if len(handles) == 0 {
		return nil
	}

	var lastErr error
	for i, handle := range handles {
		// Get auto-start config for delay
		config, err := p.GetAutoStart(ctx, handle)
		if err != nil {
			lastErr = err
			continue
		}

		// Apply delay (except for first container)
		if i > 0 && config.DelayMS > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(config.DelayMS) * time.Millisecond):
			}
		}

		// Check if already running
		state, err := p.GetInstanceState(ctx, handle)
		if err == nil && state == provider.StateRunning {
			continue // Already running
		}

		// Start the container
		if err := p.StartInstance(ctx, handle); err != nil {
			lastErr = fmt.Errorf("failed to auto-start container %s: %w", handle.ID, err)
			// Continue with next container
		}
	}

	return lastErr
}
