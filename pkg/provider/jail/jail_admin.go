package jail

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
// jailConfBlockHeader matches the "<name> {" line that opens a jail block.
var jailConfBlockHeader = regexp.MustCompile(`(?m)^(\s*)([A-Za-z0-9_.-]+)(\s*\{)`)

// renameJailConfBlock rewrites only the block header naming oldName, leaving
// every other occurrence of that string alone.
func renameJailConfBlock(content, oldName, newName string) string {
	return jailConfBlockHeader.ReplaceAllStringFunc(content, func(m string) string {
		parts := jailConfBlockHeader.FindStringSubmatch(m)
		if len(parts) == 4 && parts[2] == oldName {
			return parts[1] + newName + parts[3]
		}
		return m
	})
}

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
	// os/exec directly, not p.cmd(): execx.Runner exposes no way to set a
	// command's environment, and these calls need one. The package contract
	// names this as the exception.
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
			// Only the path and the block header are rewritten. Replacing every
			// occurrence of the name matched inside unrelated tokens: for the
			// jail "a", "path = ..." became "pweb th = ..." and every word
			// containing an "a" was mangled.
			updated := strings.ReplaceAll(string(data), oldPath, newPath)
			updated = renameJailConfBlock(updated, oldName, newName)
			if writeErr := os.WriteFile(newJailConf, []byte(updated), 0o600); writeErr != nil {
				p.logger.Warn("failed to write new jail.conf during rename", "path", newJailConf, "error", writeErr)
			} else if rmErr := os.Remove(oldJailConf); rmErr != nil && !os.IsNotExist(rmErr) {
				p.logger.Warn("failed to remove old jail.conf", "path", oldJailConf)
			}
		}
	}

	// The Linux fstab lives at <stateDir>/fstab/<name> and its entries embed the
	// jail path, so leaving it behind means the renamed jail mounts nothing —
	// or, worse, the old file keeps pointing at a path that no longer exists.
	if err := p.renameJailFstab(oldName, newName, oldPath, newPath); err != nil {
		p.logger.Warn("failed to move the jail fstab during rename",
			"old_name", oldName, "new_name", newName, "error", err)
	}

	p.logger.Info("jail renamed", "old_name", oldName, "new_name", newName)
	return nil
}

// renameJailFstab moves <stateDir>/fstab/<old> to <new> and rewrites the jail
// paths its entries carry. A jail with no fstab is not an error.
func (p *JailProvider) renameJailFstab(oldName, newName, oldPath, newPath string) error {
	oldFstab := filepath.Join(p.stateDir, "fstab", oldName)
	newFstab := filepath.Join(p.stateDir, "fstab", newName)

	data, err := os.ReadFile(oldFstab) //nolint:gosec // G304: path built from the validated jail name
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	updated := strings.ReplaceAll(string(data), oldPath, newPath)
	if err := os.WriteFile(newFstab, []byte(updated), 0o600); err != nil {
		return err
	}
	if err := os.Remove(oldFstab); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	// The parameter records the path jail(8) is given.
	return p.updateFstabParameter(newName, newFstab)
}

// updateFstabParameter rewrites MountFstab in the renamed jail's config.
func (p *JailProvider) updateFstabParameter(name, fstabPath string) error {
	configPath := filepath.Join(p.stateDir, name+".json")
	cfg, err := p.loadJailConfig(configPath)
	if err != nil {
		return err
	}
	if cfg.JailParameters.MountFstab == "" {
		return nil
	}
	cfg.JailParameters.MountFstab = fstabPath
	return p.saveJailConfig(cfg, configPath)
}
