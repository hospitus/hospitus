package jail

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

var (
	_ provider.RenameProvider  = (*JailProvider)(nil)
	_ provider.UpgradeProvider = (*JailProvider)(nil)
)

// UpgradeInstance upgrades the base system of a stopped jail to a new FreeBSD release.
//
// It runs freebsd-update(8) in the jail root directory. Installed packages are
// deliberately left alone; see the note in the body.
//
// targetRelease must be a full release string, e.g. "14.3-RELEASE".
// This is a long-running operation; callers should run it inside an async job.
func (p *JailProvider) UpgradeInstance(ctx context.Context, handle provider.InstanceHandle, targetRelease string) error {
	ctx, releaseLock, lockErr := p.locks.Acquire(ctx, handle.ID)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid jail name: %w", err)
	}
	jailName := handle.ID

	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to get jail state: %w", err)
	}
	if state != provider.StateStopped {
		return fmt.Errorf("jail must be stopped before upgrading (current state: %s)", state)
	}

	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	cfg, err := p.loadJailConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load jail config: %w", err)
	}

	jailRoot := cfg.Path
	if jailRoot == "" {
		return fmt.Errorf("jail %q has no path in config", jailName)
	}

	currentRelease := cfg.Spec.OSVersion
	if currentRelease == "" {
		currentRelease = targetRelease // fallback: let freebsd-update figure it out
	}

	logger := p.logger
	logger.Info("upgrading jail base system",
		"jail", jailName,
		"from", currentRelease,
		"to", targetRelease,
		"root", jailRoot,
	)

	// Run freebsd-update fetch + install in the jail root.
	// --currently-running tells freebsd-update the installed version.
	// -r specifies the target release.
	// PAGER=cat prevents interactive prompts.
	upgradeArgs := []string{
		"-b", jailRoot,
		"--currently-running", currentRelease,
		"-r", targetRelease,
		"upgrade",
	}
	cmd := exec.CommandContext(ctx, "freebsd-update", upgradeArgs...)
	cmd.Env = provider.MinimalEnv("PAGER=cat", "ASSUME_ALWAYS_YES=yes")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("freebsd-update upgrade failed: %w\noutput: %s", err, string(out))
	}

	installArgs := []string{
		"-b", jailRoot,
		"install",
	}
	cmd = exec.CommandContext(ctx, "freebsd-update", installArgs...)
	cmd.Env = provider.MinimalEnv("PAGER=cat", "ASSUME_ALWAYS_YES=yes")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("freebsd-update install failed: %w\noutput: %s", err, string(out))
	}

	// Note: package upgrades are intentionally NOT run here. The jail is
	// required to be stopped for the base-system upgrade, so jexec (which needs
	// a running jail) cannot execute pkg. Operators should run
	// `hospitus jail exec <name> pkg upgrade` after starting the upgraded jail.

	// Update the stored OS version.
	cfg.Spec.OSVersion = targetRelease
	if err := p.saveJailConfig(cfg, configPath); err != nil {
		return fmt.Errorf("upgrade completed but failed to update config: %w", err)
	}

	logger.Info("jail upgrade complete", "jail", jailName, "release", targetRelease)
	return nil
}

// RenameInstance renames a stopped jail.
//
// Steps:
//  1. Verify the jail is stopped.
//  2. Rename the ZFS dataset.
//  3. Move the jail.conf file and update its contents.
//  4. Move the state config file.
//  5. The caller (API handler) must update the datastore record via RenameInstance.
func (p *JailProvider) RenameInstance(ctx context.Context, handle provider.InstanceHandle, newName string) error {
	ctx, releaseLock, lockErr := p.locks.AcquireAll(ctx, handle.ID, newName)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()
	if err := validation.ValidateInstanceName(newName); err != nil {
		return fmt.Errorf("invalid new name: %w", err)
	}
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid jail name: %w", err)
	}

	oldName := handle.ID

	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to get jail state: %w", err)
	}
	if state != provider.StateStopped {
		return fmt.Errorf("jail must be stopped to rename (current state: %s)", state)
	}

	// Check the new name doesn't already exist.
	newConfigPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", newName))
	if _, err := os.Stat(newConfigPath); err == nil {
		return fmt.Errorf("jail %q already exists", newName)
	}

	oldDataset := fmt.Sprintf("%s/%s", p.zfsParent, oldName)
	newDataset := fmt.Sprintf("%s/%s", p.zfsParent, newName)

	// Rename ZFS dataset (if present).
	zfsExists := p.cmd().Run(ctx, "zfs", "list", "-H", oldDataset) == nil
	if zfsExists {
		if out, err := p.cmd().CombinedOutput(ctx, "zfs", "rename", oldDataset, newDataset); err != nil {
			return fmt.Errorf("zfs rename failed: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}

	// Load, update, and save the config file under the new name.
	oldConfigPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", oldName))
	cfg, err := p.loadJailConfig(oldConfigPath)
	if err != nil {
		if zfsExists {
			_ = p.cmd().Run(ctx, "zfs", "rename", newDataset, oldDataset)
		}
		return fmt.Errorf("failed to load jail config: %w", err)
	}

	oldPath := cfg.Path
	// Reconstruct the new path from components instead of a textual
	// ReplaceAll, which would also rewrite any other occurrence of oldName in
	// the path (e.g. inside "jails" for oldName "a"). The jail root is laid out
	// as <parent>/<name>/<base> (default: <...>/jails/<name>/root); only the
	// name component is rewritten. If the path does not follow that layout we
	// leave it unchanged rather than risk corrupting it.
	newPath := oldPath
	switch {
	case filepath.Base(oldPath) == oldName:
		// <parent>/<name>, which is what a jail actually gets. Only this
		// layout was missing, and missing it left the path pointing at the
		// dataset's former mountpoint: the jail then started on an empty
		// directory and failed with "exec /usr/bin/true: No such file or
		// directory", having created dev, tmp and var inside it.
		newPath = filepath.Join(filepath.Dir(oldPath), newName)
	case filepath.Base(filepath.Dir(oldPath)) == oldName:
		// <parent>/<name>/<base>, e.g. a root subdirectory.
		newPath = filepath.Join(filepath.Dir(filepath.Dir(oldPath)), newName, filepath.Base(oldPath))
	}

	cfg.Name = newName
	cfg.Path = newPath

	if err := p.saveJailConfig(cfg, newConfigPath); err != nil {
		if zfsExists {
			_ = p.cmd().Run(ctx, "zfs", "rename", newDataset, oldDataset)
		}
		return fmt.Errorf("failed to save renamed config: %w", err)
	}

	// Remove old config file.
	if err := os.Remove(oldConfigPath); err != nil && !os.IsNotExist(err) {
		p.logger.Warn("failed to remove old config file", "path", oldConfigPath)
	}

	// Rename jail.conf file if it exists (written to /etc/jail.conf.d/<name>.conf).
	oldJailConf := fmt.Sprintf("/etc/jail.conf.d/%s.conf", oldName)
	newJailConf := fmt.Sprintf("/etc/jail.conf.d/%s.conf", newName)
	if _, err := os.Stat(oldJailConf); err == nil {
		data, err := os.ReadFile(oldJailConf)
		if err != nil {
			p.logger.Warn("failed to read old jail.conf during rename", "path", oldJailConf, "error", err)
		} else {
			// Rewrite the full old path first (unambiguous), then the name.
			updated := strings.ReplaceAll(string(data), oldPath, newPath)
			updated = strings.ReplaceAll(updated, oldName, newName)
			if writeErr := os.WriteFile(newJailConf, []byte(updated), 0o600); writeErr != nil {
				p.logger.Warn("failed to write new jail.conf during rename", "path", newJailConf, "error", writeErr)
			} else if rmErr := os.Remove(oldJailConf); rmErr != nil && !os.IsNotExist(rmErr) {
				p.logger.Warn("failed to remove old jail.conf", "path", oldJailConf)
			}
		}
	}

	p.logger.Info("jail renamed", "old_name", oldName, "new_name", newName)
	return nil
}
