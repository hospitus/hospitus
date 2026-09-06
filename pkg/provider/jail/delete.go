package jail

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
)

// DeleteInstance deletes a jail
func (p *JailProvider) DeleteInstance(ctx context.Context, handle provider.InstanceHandle, force bool) (err error) {
	// Attach provider context so the API layer can tell a failed delete from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("jail", "delete", handle.ID, err) }()

	ctx, releaseLock, lockErr := p.locks.Acquire(ctx, handle.ID)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()

	jailName := handle.ID

	// Load config path for cleanup
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))

	// Get hook environment from config
	preHookEnv, _ := p.GetHookEnvForJail(jailName, HookPreDestroy)

	if _, err := p.ExecuteHooks(ctx, HookPreDestroy, preHookEnv); err != nil {
		return fmt.Errorf("pre-destroy hook failed: %w", err)
	}

	// Clean up firewall rules FIRST (before stopping jail)
	// This ensures rules are removed even if subsequent steps fail
	if err := p.networkManager.CleanupFirewallRules(ctx, jailName); err != nil {
		p.logWarn(ctx, "failed to cleanup firewall rules during jail deletion", "jail", jailName, logging.FieldError, err)
		// Continue with deletion - we don't want to leave the jail in a half-deleted state
	}

	// Stop jail if running (this will also clean up epair)
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return err
	}
	if running {
		if err := p.StopInstance(ctx, handle, provider.StopOptions{Force: force}); err != nil {
			return err
		}
	}

	// Always try to unmount before deletion for extra safety
	p.umountJail(ctx, jailName)

	// Clean up ALL VNET interfaces if they still exist (in case stop wasn't called or jail wasn't running)
	var epairsToDestroy []string

	// Check metadata for legacy single epair
	if ep, ok := handle.Metadata["vnet_epair"].(string); ok && ep != "" {
		epairsToDestroy = append(epairsToDestroy, ep)
	}

	// Load config file to get all epairs (both legacy and multi-nic)
	if jailConfig, err := p.loadJailConfig(configPath); err == nil {
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

	// Destroy all epair interfaces
	for _, epairA := range epairsToDestroy {
		if err := p.networkManager.DestroyVNetInterface(ctx, epairA); err != nil {
			p.logWarn(ctx, "failed to destroy VNET interface during jail deletion", "jail", jailName, "epair", epairA, logging.FieldError, err)
		}
	}

	zfsDataset, err := p.datasetFor(handle)
	if err != nil {
		return err
	}

	// Destroy ZFS dataset (this removes all data)
	if err := p.destroyZFSDataset(ctx, zfsDataset, force); err != nil {
		return fmt.Errorf("failed to destroy ZFS dataset: %w", err)
	}

	// Remove configuration file
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove configuration file: %w", err)
	}

	// Clean up per-jail hooks directory
	hookDir := p.getJailHooksDir(jailName)
	os.RemoveAll(hookDir) // Ignore errors

	// Drop the jail's fstab. Left behind, its entries survive the jail and are
	// mounted again by the next jail created under the same name — which is how
	// a destroyed jail's volume turned up inside its replacement.
	if err := os.Remove(filepath.Join(p.stateDir, "fstab", jailName)); err != nil && !os.IsNotExist(err) {
		p.logWarn(ctx, "failed to remove the jail fstab", "jail", jailName, logging.FieldError, err)
	}

	// Execute post-destroy hooks (non-fatal)
	postHookEnv := HookEnv{
		JailName:   jailName,
		ZFSDataset: zfsDataset,
		HookType:   HookPostDestroy,
	}
	if results, err := p.ExecuteHooks(ctx, HookPostDestroy, postHookEnv); err != nil {
		// Log warning but don't fail - jail is already destroyed
		for _, r := range results {
			if r.Error != nil {
				p.logWarn(ctx, "post-destroy hook failed", "jail", jailName, logging.FieldError, r.Error)
			}
		}
	}

	return nil
}
