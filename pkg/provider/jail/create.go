package jail

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// CreateInstance creates a new jail
func (p *JailProvider) CreateInstance(ctx context.Context, spec provider.InstanceSpec) (_ provider.InstanceHandle, err error) {
	// Attach provider context so the API layer can tell a failed create from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("jail", "create", spec.Name, err) }()

	ctx, releaseLock, lockErr := p.locks.Acquire(ctx, spec.Name)
	if lockErr != nil {
		return provider.InstanceHandle{}, lockErr
	}
	defer releaseLock()

	jailName := spec.Name

	if err := validation.ValidateInstanceName(jailName); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid jail name: %w", err)
	}

	if err := validation.ValidateResourceLimits(spec.CPUs, spec.MemoryMB); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid resource limits: %w", err)
	}

	// Lock to prevent TOCTOU race between existence check and ZFS dataset creation.
	// This serializes concurrent CreateInstance calls for the same provider.
	p.createMu.Lock()
	defer p.createMu.Unlock()

	// Check if jail already exists (now protected by mutex)
	exists, err := p.jailExists(ctx, jailName)
	if err != nil {
		return provider.InstanceHandle{}, err
	}
	if exists {
		return provider.InstanceHandle{}, provider.ErrInstanceExists
	}

	// Refuse a static address another jail already holds, here rather than only
	// at start: two jails created with the same address each "hold" it in their
	// config, so every start of either is refused because of the other — a
	// deadlock where first come, first served should let one of them run.
	for i := range spec.Networks {
		if err := p.refuseAddressInUse(jailName, spec.Networks[i].IPv4); err != nil {
			return provider.InstanceHandle{}, fmt.Errorf("network %d: %w", i, err)
		}
	}

	// Execute pre-create hooks
	hookEnv := HookEnv{
		JailName: jailName,
		HookType: HookPreCreate,
	}
	if _, err := p.ExecuteHooks(ctx, HookPreCreate, hookEnv); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("pre-create hook failed: %w", err)
	}

	// Create ZFS dataset for jail
	zfsDataset := fmt.Sprintf("%s/%s", p.zfsParent, jailName)
	// jailExists only knows the state file, so a dataset left behind by a lost
	// state directory would be extracted over and, on any later failure,
	// destroyed by the cleanup below along with its snapshots.
	if out, err := p.cmd().Output(ctx, "zfs", "list", "-H", "-o", "name", zfsDataset); err == nil && strings.TrimSpace(string(out)) == zfsDataset {
		return provider.InstanceHandle{}, fmt.Errorf("ZFS dataset %s already exists: destroy it or import the jail instead", zfsDataset)
	}
	if err := p.createZFSDataset(ctx, zfsDataset); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create ZFS dataset: %w", err)
	}

	mountpoint, err := p.getZFSMountpoint(ctx, zfsDataset)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to get ZFS mountpoint: %w", err)
	}

	// Check if this is a Linux jail
	isLinuxJail := spec.OSType == "linux"

	// Extract base system if image specified, or setup Linux jail
	if isLinuxJail {
		// Linux jail setup
		// Get OS release info
		osRelease, err := p.GetOSRelease(spec.OSType, spec.OSVersion, spec.Arch)
		if err != nil {
			p.destroyZFSDatasetCleanup(ctx, zfsDataset)
			return provider.InstanceHandle{}, fmt.Errorf("failed to get OS release info: %w", err)
		}

		// Setup Linux jail (compatibility layer, base system, mounts)
		if err := p.setupLinuxJail(ctx, mountpoint, osRelease); err != nil {
			// Clean up on failure
			p.destroyZFSDatasetCleanup(ctx, zfsDataset)
			return provider.InstanceHandle{}, fmt.Errorf("failed to setup Linux jail: %w", err)
		}

		// A foreign-architecture Linux jail needs its emulator too. The kernel
		// resolves the interpreter registered with binmiscctl(8) inside the
		// jail's root, so without a copy there nothing in the jail can run —
		// and the jail fails to start on its own /bin/true with "No such file
		// or directory", which says nothing about the missing emulator.
		if err := p.setupCrossArchEmulation(ctx, mountpoint, osRelease.Arch); err != nil {
			p.destroyZFSDatasetCleanup(ctx, zfsDataset)
			return provider.InstanceHandle{}, fmt.Errorf("failed to setup cross-architecture emulation: %w", err)
		}
	} else {
		// FreeBSD jail - extract base system
		if spec.Image == "" {
			// No image specified - provide helpful error
			p.destroyZFSDatasetCleanup(ctx, zfsDataset)
			defaultImage, _ := p.getDefaultFreeBSDImage(ctx)
			return provider.InstanceHandle{}, fmt.Errorf("no base system image specified. Please specify an image:\n\n"+
				"  Example: --image %s\n"+
				"  Or use:  --image 14.1-RELEASE-amd64\n"+
				"  Or path: --image /path/to/base.txz\n\n"+
				"To download a base system, run:\n"+
				"  hospitus image fetch %s", defaultImage, defaultImage)
		}

		if err := p.extractBaseSystem(ctx, spec.Image, mountpoint, spec.Arch); err != nil {
			// Clean up on failure
			p.destroyZFSDatasetCleanup(ctx, zfsDataset)
			return provider.InstanceHandle{}, fmt.Errorf("failed to extract base system: %w", err)
		}

		// Setup cross-architecture emulation if needed
		// Determine target architecture from spec.Arch or detect from image name
		targetArch := spec.Arch
		if targetArch == "" || targetArch == "native" {
			targetArch = detectImageArch(spec.Image)
		}
		if targetArch != "" {
			if err := p.setupCrossArchEmulation(ctx, mountpoint, targetArch); err != nil {
				// Clean up on failure
				p.destroyZFSDatasetCleanup(ctx, zfsDataset)
				return provider.InstanceHandle{}, fmt.Errorf("failed to setup cross-architecture emulation: %w", err)
			}
		}
	}

	// Create jail configuration
	jailConfig := p.buildJailConfig(spec, mountpoint)

	// Add Linux-specific mounts if this is a Linux jail
	if isLinuxJail {
		if err := p.configureLinuxMounts(jailConfig); err != nil {
			p.destroyZFSDatasetCleanup(ctx, zfsDataset)
			return provider.InstanceHandle{}, fmt.Errorf("failed to configure Linux mounts: %w", err)
		}
	}

	// Save configuration
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	if err := p.saveJailConfig(jailConfig, configPath); err != nil {
		// Clean up on failure
		p.destroyZFSDatasetCleanup(ctx, zfsDataset)
		return provider.InstanceHandle{}, fmt.Errorf("failed to save jail configuration: %w", err)
	}

	// RCTL limits (spec.CPUs / spec.MemoryMB) are applied when the jail starts.

	handle := provider.InstanceHandle{
		ID:       jailName,
		Provider: "jail",
		Metadata: map[string]interface{}{
			"zfs_dataset": zfsDataset,
			"mountpoint":  mountpoint,
		},
	}

	// Record which userland this jail actually runs. The spec arrives by value,
	// so filling in spec.OSVersion here would go nowhere — the handle is what
	// the daemon stores. Without it a jail created from an image alone reports
	// no version at all, though the release is right there in the image name
	// and already decides osrelease for jail(8).
	if release, _ := freebsdUserlandVersion(spec.Image, spec.OSVersion); release != "" {
		handle.Metadata["os_release"] = release
	}

	// Configure DNS before post-create hooks so they can use network
	// This is needed for pkg bootstrap and other network operations in hooks
	if len(jailConfig.Networks) > 0 && jailConfig.Networks[0].IPv4 != "" {
		gateway := ""
		if p.hospitusConfig.DefaultGateway != "none" {
			gateway = getGatewayForIP(jailConfig.Networks[0].IPv4)
		}
		if err := p.networkManager.ConfigureDNS(mountpoint, gateway); err != nil {
			p.logWarn(ctx, "failed to configure DNS before post-create hooks", "jail", jailName, logging.FieldError, err)
		} else {
			p.logDebug(ctx, "DNS configured before post-create hooks", "jail", jailName)
		}
	}

	// Execute post-create hooks (non-fatal)
	postHookEnv := HookEnv{
		JailName:   jailName,
		JailPath:   mountpoint,
		ZFSDataset: zfsDataset,
		HookType:   HookPostCreate,
	}
	if len(spec.Networks) > 0 {
		postHookEnv.JailIP = spec.Networks[0].IPv4
		postHookEnv.JailBridge = spec.Networks[0].Bridge
	}
	results, err := p.ExecuteHooks(ctx, HookPostCreate, postHookEnv)
	if err != nil {
		p.logWarn(ctx, "post-create hooks returned an error", "jail", jailName, logging.FieldError, err)
	}
	// Individual failures are reported even when ExecuteHooks returns nil:
	// post-create hooks do not fail the create, so their errors are otherwise
	// swallowed and the jail comes up unprovisioned with nothing said.
	for _, r := range results {
		if r.Error != nil {
			p.logWarn(ctx, "post-create hook failed", "jail", jailName, "command", r.Command, logging.FieldError, r.Error, "output", r.Output)
		}
	}

	return handle, nil
}
