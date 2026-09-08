package jail

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// StopInstance stops a jail
func (p *JailProvider) StopInstance(ctx context.Context, handle provider.InstanceHandle, opts provider.StopOptions) (err error) {
	// Attach provider context so the API layer can tell a failed stop from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("jail", "stop", handle.ID, err) }()
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}

	ctx, releaseLock, lockErr := p.locks.Acquire(ctx, handle.ID)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()

	jailName := handle.ID

	// Check if running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return err
	}
	if !running {
		return nil // Already stopped
	}

	// Get hook environment from config
	preHookEnv, _ := p.GetHookEnvForJail(jailName, HookPreStop)

	if _, err := p.ExecuteHooks(ctx, HookPreStop, preHookEnv); err != nil {
		return fmt.Errorf("pre-stop hook failed: %w", err)
	}

	// Build stop command
	// Use -R (SIGKILL) for force, -r (SIGTERM) for graceful stop
	stopFlag := "-r"
	if opts.Force {
		stopFlag = "-R"
	}
	args := []string{stopFlag, jailName}

	// The timeout bounds the stop command, not everything after it. Reassigning
	// ctx made the unmounting, the epair teardown and the firewall cleanup run
	// on a context that could already be expired — so a jail that stopped just
	// inside the deadline left its mounts and interfaces behind.
	stopCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		stopCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	output, err := p.cmd().CombinedOutput(stopCtx, "jail", args...)
	if err != nil {
		return provider.NewProviderError("jail", "stop", jailName,
			fmt.Errorf("jail command failed: %s: %w", string(output), err))
	}

	// Detached, as the start path does for its own rollback: everything below
	// runs after the jail is already down, and a caller who canceled — or a stop
	// that used up its timeout — would otherwise skip the unmounting, the
	// interface teardown and the post-stop hooks, leaving all three behind.
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancelCleanup()

	// Unmount everything related to this jail
	p.umountJail(cleanupCtx, jailName)

	// Clean up every VNET interface: the single-epair field and the multi-NIC list
	var epairsToDestroy []string

	// The single-epair field, set for a jail with one NIC
	if ep, ok := handle.Metadata["vnet_epair"].(string); ok && ep != "" {
		epairsToDestroy = append(epairsToDestroy, ep)
		delete(handle.Metadata, "vnet_epair")
	}

	// Load config file to get all epairs (both legacy and multi-nic)
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	jailConfig, cfgErr := p.loadJailConfig(configPath)
	if cfgErr == nil {
		// Legacy single epair
		if jailConfig.VnetEpair != "" {
			epairsToDestroy = append(epairsToDestroy, jailConfig.VnetEpair)
		}
		// Multi-nic epairs
		for _, epair := range jailConfig.VnetEpairs {
			if epair != "" {
				epairsToDestroy = append(epairsToDestroy, epair)
			}
		}
	}

	// Destroy the epairs, and only forget the ones that are gone. Clearing the
	// list first meant a failed destroy leaked the interface with its name.
	remaining := []string{}
	for _, epairA := range epairsToDestroy {
		if err := p.networkManager.DestroyVNetInterface(cleanupCtx, epairA); err != nil {
			p.logWarn(ctx, "failed to destroy VNET interface", "jail", jailName, "epair", epairA, logging.FieldError, err)
			remaining = append(remaining, epairA)
		}
	}
	if cfgErr == nil {
		jailConfig.VnetEpair = ""
		jailConfig.VnetEpairs = remaining
		if err := p.saveJailConfig(jailConfig, configPath); err != nil {
			p.logWarn(ctx, "failed to clear epairs from jail config", "jail", jailName, logging.FieldError, err)
		}
	}

	// Execute post-stop hooks (non-fatal). Log both an overall failure and any
	// individual hook errors, even when ExecuteHooks itself returns nil.
	postHookEnv, _ := p.GetHookEnvForJail(jailName, HookPostStop)
	results, err := p.ExecuteHooks(cleanupCtx, HookPostStop, postHookEnv)
	if err != nil {
		p.logWarn(ctx, "post-stop hooks returned an error", "jail", jailName, logging.FieldError, err)
	}
	for _, r := range results {
		if r.Error != nil {
			p.logWarn(ctx, "post-stop hook failed", "jail", jailName, logging.FieldError, r.Error)
		}
	}

	return nil
}
