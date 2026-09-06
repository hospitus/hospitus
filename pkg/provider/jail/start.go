package jail

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// jailStartSettleDelay is how long StartInstance waits after jail(8) returns
// before configuring VNET interfaces inside the jail, giving the jail time to
// finish coming up.
const jailStartSettleDelay = 500 * time.Millisecond

// StartInstance starts a jail
func (p *JailProvider) StartInstance(ctx context.Context, handle provider.InstanceHandle) (err error) {
	// Attach provider context so the API layer can tell a failed start from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("jail", "start", handle.ID, err) }()

	ctx, releaseLock, lockErr := p.locks.Acquire(ctx, handle.ID)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()

	jailName := handle.ID
	// "handleMetadataNil=false" means nothing to whoever ran the command, and
	// the daemon streams its log to them. Debug, like the argv below.
	p.logDebug(ctx, "StartInstance called", "jail", jailName, "handleMetadataNil", handle.Metadata == nil)

	// Ensure handle.Metadata is initialized (may be nil when called from auto-start)
	if handle.Metadata == nil {
		handle.Metadata = make(map[string]interface{})
	}

	// Check if already running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("jail %s is already running", jailName)
	}

	// Load jail configuration
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	jailConfig, err := p.loadJailConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load jail configuration: %w", err)
	}

	// Execute pre-start hooks
	preHookEnv := HookEnv{
		JailName:   jailName,
		JailPath:   jailConfig.Path,
		ZFSDataset: fmt.Sprintf("%s/%s", p.zfsParent, jailName),
		HookType:   HookPreStart,
	}
	if len(jailConfig.Networks) > 0 {
		preHookEnv.JailIP = jailConfig.Networks[0].IPv4
		preHookEnv.JailBridge = jailConfig.Networks[0].Bridge
	}
	if _, err := p.ExecuteHooks(ctx, HookPreStart, preHookEnv); err != nil {
		return fmt.Errorf("pre-start hook failed: %w", err)
	}

	// Sync Networks to Spec.Networks for existing jails with allocated IPs
	// This ensures the allocated IP is visible in the API/CLI
	needsSave := false
	if len(jailConfig.Networks) > 0 && len(jailConfig.Spec.Networks) > 0 {
		if jailConfig.Networks[0].IPv4 != jailConfig.Spec.Networks[0].IPv4 {
			jailConfig.Spec.Networks[0].IPv4 = jailConfig.Networks[0].IPv4
			needsSave = true
		}
	}

	// Ensure required directories exist in the jail filesystem
	if err := p.ensureJailDirectories(jailConfig.Path); err != nil {
		return fmt.Errorf("failed to create jail directories: %w", err)
	}

	// Build jail command
	args := []string{"-c"}

	// The path comes from the persisted config, which an earlier write — or an
	// operator editing the file — can put anywhere. It is handed to jail(8) and
	// to ensureJailDirectories, both privileged, so confine it to the root this
	// provider owns.
	if p.dataDir != "" {
		within, err := validation.PathWithin(jailConfig.Path, p.dataDir)
		if err != nil {
			return fmt.Errorf("cannot resolve the jail path %q: %w", jailConfig.Path, err)
		}
		if !within {
			return fmt.Errorf("jail %s has path %q, outside the managed jail root %s",
				jailName, jailConfig.Path, p.dataDir)
		}
	}

	// Add jail parameters
	args = append(args,
		fmt.Sprintf("name=%s", jailName),
		fmt.Sprintf("path=%s", jailConfig.Path),
		fmt.Sprintf("host.hostname=%s", jailName))

	// VNET configuration with epair interfaces (like AppJail/CBSD)
	var vnetInterfaces []*VNetInterface
	// The index each entry had in jailConfig.Networks. The setup loop skips a
	// network with no address, so positions in vnetInterfaces are compacted and
	// cannot be used to index Networks or to compare against defaultRouteOn.
	var vnetNetworkIndex []int
	var rollbackVNETs []string

	// Everything set up on the host comes back down when the start fails,
	// whether it failed before jail(8) ran or after. Only the epairs used to be
	// undone, and only until jail -c succeeded, so a jail that started and then
	// could not be addressed left its mounts and firewall rules behind — stop
	// does not remove them, it returns early on a jail that is not running.
	var mountsApplied, firewallApplied, jailCreated bool
	defer func() {
		if err == nil {
			return
		}
		// The caller's context may already be canceled, which is one of the
		// ways we get here; cleanup driven by a dead context does nothing.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()

		// Remove the jail first: its epairs and mounts are in use while it
		// runs, and nothing else will remove a jail a failed start left behind.
		if jailCreated {
			if removeErr := p.cmd().Run(cleanupCtx, "jail", "-r", jailName); removeErr != nil {
				p.logWarn(cleanupCtx, "failed to remove the jail after a failed start", "jail", jailName, logging.FieldError, removeErr)
			}
		}
		if mountsApplied {
			p.umountJail(cleanupCtx, jailName)
		}
		if firewallApplied {
			if cleanupErr := p.networkManager.CleanupFirewallRules(cleanupCtx, jailName); cleanupErr != nil {
				p.logWarn(cleanupCtx, "failed to remove firewall rules after a failed start", "jail", jailName, logging.FieldError, cleanupErr)
			}
		}
		for _, epairA := range rollbackVNETs {
			if epairA == "" {
				continue
			}
			if destroyErr := p.networkManager.DestroyVNetInterface(cleanupCtx, epairA); destroyErr != nil {
				p.logWarn(cleanupCtx, "failed to rollback orphaned VNET interface", "jail", jailName, "epair", epairA, logging.FieldError, destroyErr)
			}
		}
	}()

	var vnetIPs []string

	// One network carries the default route for the whole jail, chosen once and
	// used by both the rc.conf entry and the route installed after start.
	defaultRouteOn := defaultRouteNetwork(jailConfig.Networks, p.hospitusConfig.BridgePrefix+"0")

	if len(jailConfig.Networks) > 0 {
		// Enable VNET for the jail
		args = append(args, "vnet")
		jailConfig.JailParameters.IP4 = ""
		jailConfig.JailParameters.IP6 = ""

		for i := range jailConfig.Networks {
			network := &jailConfig.Networks[i]
			if network.IPv4 == "" {
				// VNET is already enabled above, so skipping here yields a jail with
				// a virtual network stack and no interface in it. Say so: silently
				// starting an unreachable jail is worse than a noisy one.
				p.logWarn(ctx, "network has no address; no interface will be created for it",
					"jail", jailName, "network_index", i,
					"hint", "pass --ip <addr>/<prefix> or --ip dhcp when creating the jail")
				continue
			}

			// 1. Handle DHCP allocation.
			// Serialize allocation and persist the chosen IP immediately, so a
			// concurrent StartInstance sees this lease when it scans configs and
			// cannot hand the same address to another jail (TOCTOU race).
			if network.IPv4 == "dhcp" {
				p.dhcpMu.Lock()
				allocatedIP, err := p.allocateDHCPAddress(ctx, network.IPPool)
				if err != nil {
					p.dhcpMu.Unlock()
					return fmt.Errorf("failed to allocate DHCP address for network %d: %w", i, err)
				}
				network.IPv4 = allocatedIP
				if len(jailConfig.Spec.Networks) > i {
					jailConfig.Spec.Networks[i].IPv4 = allocatedIP
				}
				if err := p.saveJailConfig(jailConfig, configPath); err != nil {
					p.dhcpMu.Unlock()
					return fmt.Errorf("failed to persist DHCP allocation for network %d: %w", i, err)
				}
				p.dhcpMu.Unlock()
			}

			// 1b. A static address goes through the same bookkeeping DHCP does,
			// or two jails end up holding one address — the conflict tracking
			// would otherwise cover only the addresses hospitus chose itself.
			if err := p.refuseAddressInUse(jailName, network.IPv4); err != nil {
				return fmt.Errorf("network %d: %w", i, err)
			}

			// 2. Ensure bridge exists
			defaultBridge := p.hospitusConfig.BridgePrefix + "0"
			bridgeName := network.Bridge
			if bridgeName == "" {
				bridgeName = defaultBridge
				// Record the resolved name: CreateVNetInterface only attaches the
				// host side of the epair when the spec names a bridge, so leaving
				// this empty gives the jail an interface attached to nothing.
				// The epair bookkeeping below persists the config on this path.
				network.Bridge = bridgeName
			}

			bridgePool := p.bridgePoolFor(bridgeName, network.IPPool, defaultBridge)

			if err := p.networkManager.EnsureBridge(ctx, bridgeName, bridgePool); err != nil {
				return fmt.Errorf("failed to ensure bridge %s: %w", bridgeName, err)
			}

			// 3. Create VNET interface (epair)
			vnetIface, err := p.networkManager.CreateVNetInterface(ctx, jailName, *network)
			if err != nil {
				return fmt.Errorf("failed to create VNET interface for network %d: %w", i, err)
			}
			vnetInterfaces = append(vnetInterfaces, vnetIface)
			vnetNetworkIndex = append(vnetNetworkIndex, i)

			// Track for rollback and persistence
			rollbackVNETs = append(rollbackVNETs, vnetIface.EpairA)
			jailConfig.VnetEpairs = append(jailConfig.VnetEpairs, vnetIface.EpairA)
			needsSave = true // Always persist epair names so stop/delete can clean them up
			if i == 0 {
				jailConfig.VnetEpair = vnetIface.EpairA // The first epair, which stop and delete read back
				handle.Metadata["vnet_epair"] = vnetIface.EpairA
			}

			// 4. Assign epairB to jail
			args = append(args, fmt.Sprintf("vnet.interface=%s", vnetIface.EpairB))
			vnetIPs = append(vnetIPs, network.IPv4)

			// 5. Setup host-side networking (NAT/PF rules) for this interface.
			// Only the network that carries the default route writes it into the
			// jail's rc.conf, so /etc/rc does not install a second one.
			firewallApplied = true
			if err := p.networkManager.SetupNetworking(ctx, jailName, jailConfig.Path, network.IPv4, i == defaultRouteOn); err != nil {
				return fmt.Errorf("failed to setup host networking for jail %s (ip %s): %w", jailName, network.IPv4, err)
			}
		}
	}

	// Outside the network branch: needsSave is set when the Spec's address is
	// out of sync, and a jail with no networks at all never reached the save
	// while it sat inside "if len(jailConfig.Networks) > 0" — so that sync was
	// simply lost.
	if needsSave {
		configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
		if err := p.saveJailConfig(jailConfig, configPath); err != nil {
			p.logWarn(ctx, "failed to persist the jail configuration before start", "jail", jailName, logging.FieldError, err)
		}
	}

	// Adjust startup behavior based on the persisted runtime type.
	// Linux jails don't provide FreeBSD's /etc/rc startup flow, so use a no-op
	// start command and persist the jail after startup. Cross-architecture jails
	// also use persist to remain available when emulated processes exit early.
	//
	// For VNET jails, we defer starting the services until after the host has
	// configured the network interfaces and assigned IPs. Otherwise, services
	// like nginx or search engines that bind to specific IPs will fail to start.
	// Merged before the switch below, not after: the VNET branch replaces
	// ExecStart with /usr/bin/true and remembers the real one to run once the
	// network is up. Merging afterwards put an overridden exec.start straight
	// back into the argv, so the jail started its services before it had an
	// address — and deferredExecStart still held the old value.
	//
	// Overrides set by "jail set" travel in the handle's metadata. They are
	// merged into the parameters before the arguments are built, not appended
	// after: an appended list can only add a parameter that is true, so
	// --allow-raw-sockets=false could not clear one the configuration sets.
	// ToJailArgs writes every boolean explicitly, in both directions.
	if handle.Metadata != nil {
		merged, err := ApplyJailParameterMap(jailConfig.JailParameters, handle.Metadata)
		if err != nil {
			return fmt.Errorf("invalid jail parameter override: %w", err)
		}
		jailConfig.JailParameters = merged
	}

	var deferredExecStart string
	isVNet := len(vnetInterfaces) > 0

	switch jailConfig.RuntimeType {
	case "linux":
		jailConfig.JailParameters.ExecStart = "/bin/true"
		jailConfig.JailParameters.ExecStop = ""
		jailConfig.JailParameters.Persist = true
	case "crossarch":
		jailConfig.JailParameters.Persist = true
	default:
		if isVNet {
			deferredExecStart = jailConfig.JailParameters.ExecStart
			if deferredExecStart == "" {
				deferredExecStart = "/bin/sh /etc/rc" // Default if empty
			}
			jailConfig.JailParameters.ExecStart = "/usr/bin/true"
			jailConfig.JailParameters.Persist = true
		}
	}

	// Validate JailParameters
	if err := ValidateJailParameters(jailConfig.JailParameters); err != nil {
		return fmt.Errorf("invalid jail parameters: %w", err)
	}

	// Add structured jail parameters using ToJailArgs()
	jailArgs, err := jailConfig.JailParameters.ToJailArgs()
	if err != nil {
		return fmt.Errorf("invalid jail parameters: %w", err)
	}
	args = append(args, jailArgs...)

	// Add legacy custom parameters with validation (backward compatibility)
	for key, value := range jailConfig.Parameters {
		// Skip already handled parameters
		if key == "vnet" || key == "autostart" || key == "autostart_priority" || key == "autostart_delay" {
			continue
		}

		if err := validation.ValidateJailParameter(key); err != nil {
			return fmt.Errorf("invalid jail parameter: %w", err)
		}

		valueStr := fmt.Sprintf("%v", value)
		if err := validation.ValidateJailParameterValue(valueStr); err != nil {
			return fmt.Errorf("invalid jail parameter value for %s: %w", key, err)
		}

		args = append(args, fmt.Sprintf("%s=%s", key, valueStr))
	}

	// Apply volume mounts
	mountsApplied = true
	if err := p.applyMounts(ctx, jailConfig); err != nil {
		return fmt.Errorf("failed to apply volume mounts for jail %s: %w", jailName, err)
	}

	// Note: exec.start is already included via ToJailArgs()
	// Do NOT use command= parameter as it makes jail wait for the command to complete
	// and return an error if any rc script fails (e.g., route configuration)

	// Ensure necessary directories exist for Linux jails or when mounts are requested
	if jailConfig.RuntimeType == "linux" || jailConfig.JailParameters.MountLinprocfs || jailConfig.JailParameters.MountLinsysfs || jailConfig.JailParameters.MountFdescfs {
		// Through the context: provider.WithLogger puts an operation-scoped
		// logger there, while networkManager.logger is always the instance one.
		p.logDebug(ctx, "ensuring directories exist for jail mounts", "jail", jailName)
		dirs := []string{"proc", "sys", "dev/shm", "dev/fd"}
		for _, d := range dirs {
			target := filepath.Join(jailConfig.Path, d)
			// If target is a symlink (e.g., dev/fd -> /proc/self/fd in Linux rootfs),
			// remove it so we can create a real directory for mount
			if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
				if err := os.Remove(target); err != nil {
					p.logWarn(ctx, "failed to remove symlink for jail mount", "jail", jailName, "dir", d, logging.FieldError, err)
					continue
				}
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				p.logWarn(ctx, "failed to create directory for jail mount", "jail", jailName, "dir", d, logging.FieldError, err)
			}
		}
	}

	// Debug, not info: the daemon streams its log to whoever ran the command,
	// and a full jail(8) argv is not what someone typing "hospitus jail start"
	// asked to see. It stays one --log-level debug away, which is where you
	// already are when a start does something unexpected.
	p.logDebug(ctx, "executing jail start command", "jail", jailName, "args", strings.Join(args, " "))
	output, err := p.cmd().CombinedOutput(ctx, "jail", args...)
	if err != nil {
		return provider.NewProviderError("jail", "start", jailName,
			fmt.Errorf("jail command failed: %s: %w", string(output), err))
	}
	jailCreated = true

	// Configure VNET networking inside jail (post-start)
	if len(vnetInterfaces) > 0 {
		// Give the jail time to fully start, but honor context cancellation.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jailStartSettleDelay):
		}

		for i, vnet := range vnetInterfaces {
			ipAddr := ""
			if i < len(vnetIPs) {
				ipAddr = vnetIPs[i]
			}
			if ipAddr == "" {
				continue
			}

			// Back to the position this interface holds in jailConfig.Networks.
			netIdx := i
			if i < len(vnetNetworkIndex) {
				netIdx = vnetNetworkIndex[i]
			}

			// Configure network inside the jail
			if err := p.networkManager.ConfigureVNetJailNetwork(ctx, jailName, vnet.Bridge, vnet.EpairB, ipAddr, jailConfig.RuntimeType, netIdx == defaultRouteOn); err != nil {
				// A running jail with no network is worse than one that did
				// not start; the deferred rollback takes it down.
				return fmt.Errorf("failed to configure VNET network in jail %s (ip %s): %w", jailName, ipAddr, err)
			}

			// Restore the name the interface was given when it was added.
			//
			// AddNetworkInterface records it in the spec's ID. Without this the
			// interface comes back as epairNb, and anything inside the jail
			// configured against the name it was told — eth1 — finds nothing.
			if netIdx < len(jailConfig.Networks) {
				if name := jailConfig.Networks[netIdx].ID; name != "" && name != vnet.EpairB {
					p.renameJailInterface(ctx, jailName, vnet.EpairB, name)
				}
			}

			// Configure DNS (use first interface for gateway fallback)
			if i == 0 {
				gateway := ""
				if p.hospitusConfig.DefaultGateway != "none" {
					gateway = getGatewayForIP(ipAddr)
				}
				if err := p.networkManager.ConfigureDNS(jailConfig.Path, gateway); err != nil {
					p.logWarn(ctx, "failed to configure DNS in VNET jail", "jail", jailName, logging.FieldError, err)
				}
			}
		}

		// Now start services if it was a VNET jail and we deferred them
		if deferredExecStart != "" && deferredExecStart != "/usr/bin/true" {
			// SECURITY: Validate command to prevent injection via /bin/sh -c
			if err := validation.ValidateExecCommand(deferredExecStart); err != nil {
				p.logWarn(ctx, "invalid exec.start command, replacing with default", "jail", jailName, "command", deferredExecStart, "error", err)
			} else {
				p.logDebug(ctx, "starting jail services after network config", "jail", jailName, "command", deferredExecStart)
				// SECURITY: Pass command and args directly to jexec as separate arguments
				// rather than wrapping in /bin/sh -c, which is vulnerable to shell injection
				// via spaces or shell metacharacters in validated command strings.
				// Use jexec jailName /bin/sh /etc/rc (split) for the default,
				// and jexec jailName <command> <args...> for validated commands.
				fields := strings.Fields(deferredExecStart)
				execArgs := append([]string{jailName}, fields...)
				if out, err := p.cmd().CombinedOutput(ctx, "jexec", execArgs...); err != nil {
					// Don't fail the whole start operation if services fail to start
					p.logWarn(ctx, "failed to start jail services", "jail", jailName, "error", err, "output", string(out))
				}
			}
		}
	}

	// Apply ZFS storage limits
	if jailConfig.Resources.DiskGB > 0 {
		zfsDataset := fmt.Sprintf("%s/%s", p.zfsParent, jailName)
		if err := p.applyZFSQuotas(ctx, zfsDataset, jailConfig.Resources.DiskGB); err != nil {
			p.logWarn(ctx, "failed to apply ZFS quotas", "jail", jailName, "zfs_dataset", zfsDataset, "disk_gb", jailConfig.Resources.DiskGB, logging.FieldError, err)
		}
	}

	// Apply RCTL limits
	if jailConfig.Resources.CPUs > 0 || jailConfig.Resources.MemoryMB > 0 {
		if err := p.applyRCTLLimits(ctx, jailName, jailConfig.Resources); err != nil {
			// Log warning but don't fail
			p.logWarn(ctx, "failed to apply RCTL limits", "jail", jailName, "cpus", jailConfig.Resources.CPUs, "memory_mb", jailConfig.Resources.MemoryMB, logging.FieldError, err)
		}
	}

	// Execute post-start hooks (non-fatal)
	postHookEnv := HookEnv{
		JailName:   jailName,
		JailPath:   jailConfig.Path,
		ZFSDataset: fmt.Sprintf("%s/%s", p.zfsParent, jailName),
		HookType:   HookPostStart,
	}
	if len(jailConfig.Networks) > 0 {
		postHookEnv.JailIP = jailConfig.Networks[0].IPv4
		postHookEnv.JailBridge = jailConfig.Networks[0].Bridge
	}
	results, err := p.ExecuteHooks(ctx, HookPostStart, postHookEnv)
	if err != nil {
		p.logWarn(ctx, "post-start hooks returned error", "jail", jailName, logging.FieldError, err)
	}
	// Log individual hook failures even when ExecuteHooks returns nil
	// (post-start hooks have failOnError=false, so errors are swallowed)
	for _, r := range results {
		if r.Error != nil {
			p.logWarn(ctx, "post-start hook failed", "jail", jailName, "command", r.Command, logging.FieldError, r.Error, "output", r.Output)
		}
	}

	return nil
}

// renameJailInterface gives a jail's interface back the name it was added under.
//
// The host's ifconfig does it through -j, rather than the jail's own through
// jexec. A foreign-architecture jail runs an emulated ifconfig that cannot open
// a netlink socket, so a rename from inside fails and the interface keeps the
// epairNb name nothing in the jail is configured against; a jail whose base
// carries no ifconfig at all fails the same way. jexec remains the fallback for
// a host whose ifconfig predates -j.
func (p *JailProvider) renameJailInterface(ctx context.Context, jailName, epairB, name string) {
	if err := p.cmd().Run(ctx, "ifconfig", "-j", jailName, epairB, "name", name); err == nil {
		return
	}
	if err := p.cmd().Run(ctx, "jexec", jailName, "ifconfig", epairB, "name", name); err != nil {
		p.logWarn(ctx, "failed to restore interface name", "jail", jailName,
			"interface", epairB, "name", name, logging.FieldError, err)
	}
}

// defaultRouteNetwork picks the network that carries the jail's default route:
// the one on the bridge hospitus NATs from, or the first addressed one when the
// jail is on none of it.
//
// One network decides for the whole jail, and the same choice drives both the
// route installed at start and the defaultrouter written into the jail's
// rc.conf. Deciding twice gives a jail on two networks a default route for
// each, and the kernel uses whichever was installed first — possibly a segment
// with no gateway beyond it, which leaves every name lookup failing.
//
// Returns -1 when no network has an address, which leaves the jail without a
// default route rather than choosing one at random.
func defaultRouteNetwork(networks []provider.NetworkSpec, defaultBridge string) int {
	first := -1
	for i := range networks {
		if networks[i].IPv4 == "" {
			continue
		}
		if first < 0 {
			first = i
		}
		if networks[i].Bridge == defaultBridge {
			return i
		}
	}
	return first
}

// applyMounts handles mounting volumes and host_path directories into the jail
func (p *JailProvider) applyMounts(ctx context.Context, config *jailConfig) error {
	// 1. Handle host_path mounts from ProviderConfig
	var mounts []interface{}
	if config.Spec.ProviderConfig != nil {
		if m, ok := config.Spec.ProviderConfig["mounts"].([]interface{}); ok {
			mounts = m
		}
	}
	if len(mounts) > 0 {
		for _, m := range mounts {
			mount, ok := m.(map[string]interface{})
			if !ok {
				continue
			}

			hostPath, _ := mount["host_path"].(string)
			mountPath, _ := mount["mount_path"].(string)
			readOnly, _ := mount["read_only"].(bool)

			if hostPath == "" || mountPath == "" {
				continue
			}

			// SECURITY: Validate paths to prevent path traversal and injection.
			if err := validation.ValidateFilePath(hostPath, true); err != nil {
				p.logWarn(ctx, "invalid host_path, skipping mount", "jail", config.Name, "path", hostPath, "error", err)
				continue
			}
			if err := validation.ValidateFilePath(mountPath, true); err != nil {
				p.logWarn(ctx, "invalid mount_path, skipping mount", "jail", config.Name, "path", mountPath, "error", err)
				continue
			}

			// Ensure host path exists
			if err := os.MkdirAll(hostPath, 0o755); err != nil {
				p.logWarn(ctx, "failed to create host path for mount", "path", hostPath, logging.FieldError, err)
			}

			// Create mount point inside jail and mount live (host namespace).
			// The target is resolved first: the jail's own root can turn any
			// component of it into a symlink pointing back at the host.
			targetPath, targetErr := jailMountTarget(config.Path, mountPath)
			if targetErr != nil {
				p.logWarn(ctx, "refusing a mount that leaves the jail", "jail", config.Name, "path", mountPath, logging.FieldError, targetErr)
				continue
			}
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return fmt.Errorf("failed to create mount point %s: %w", targetPath, err)
			}

			// Mount using nullfs
			mountOpts := "rw"
			if readOnly {
				mountOpts = "ro"
			}

			p.logDebug(ctx, "mounting host path", "jail", config.Name, "host", hostPath, "jail_path", mountPath)
			// A warning let the jail start with the volume missing, and anything
			// written to that path landed in the jail root instead — silently,
			// and reported as a successful start. The deferred rollback takes
			// the jail down instead.
			if output, err := p.cmd().CombinedOutput(ctx, "mount", "-t", "nullfs", "-o", mountOpts, hostPath, targetPath); err != nil {
				return fmt.Errorf("failed to mount %s at %s in jail %s: %w (output: %s)",
					hostPath, mountPath, config.Name, err, strings.TrimSpace(string(output)))
			}

			// Add to fstab for persistence ONLY if not already using mount.fstab
			// (Linux jails use mount.fstab which jail(8) reads automatically;
			// adding the same entry would cause double-mount on jail start)
			if config.JailParameters.MountFstab == "" {
				fstabPath := filepath.Join(p.stateDir, "fstab", config.Name)
				if err := p.addToJailFstab(fstabPath, hostPath, targetPath, readOnly); err != nil {
					p.logWarn(ctx, "failed to add to fstab", "jail", config.Name, logging.FieldError, err)
				}
			} else {
				p.logDebug(ctx, "skipping fstab entry for Linux jail (mount.fstab already set)", "jail", config.Name)
			}
		}
	}

	// 2. Mount whatever was attached while the jail was down.
	//
	// Attaching a volume to a stopped jail cannot mount it into a root that is
	// not yet assembled, so it records the entry and leaves the mounting here.
	// jail(8) mounts this file itself when mount.fstab is set, which is the
	// Linux case, so only the others need it. mount(8) skips what is already
	// mounted, so the host_path mounts made just above are not mounted twice.
	if config.JailParameters.MountFstab == "" {
		fstabPath := filepath.Join(p.stateDir, "fstab", config.Name)
		if _, err := os.Stat(fstabPath); err == nil {
			// Same reason the host_path mounts above fail the start: a jail that
			// comes up without its volumes writes into its own root instead,
			// silently, while reporting success.
			if output, err := p.cmd().CombinedOutput(ctx, "mount", "-a", "-F", fstabPath); err != nil {
				return fmt.Errorf("failed to mount the volumes recorded in %s for jail %s: %w (output: %s)",
					fstabPath, config.Name, err, strings.TrimSpace(string(output)))
			}
		}
	}

	return nil
}

// bridgePoolFor decides which IP pool a bridge takes its gateway address from.
//
// A declared pool always wins. Otherwise only the default bridge falls back to
// the default pool, because that pool's first address is the default bridge's
// own gateway: lending it to a named bridge would put one address on two
// bridges, and the conflict check then refuses to start the jail. That is what
// a stack does when it declares the subnet on the first service using its
// bridge but not on the next one. A named bridge with no pool of its own keeps
// whatever address it already carries — the service that declared the subnet
// configures it, whichever order the two start in.
//
// Resolving the default pool here also matches how the DHCP allocator numbers
// jails: a bridge left without a gateway makes every jail on it unroutable.
func (p *JailProvider) bridgePoolFor(bridgeName, declaredPool, defaultBridge string) string {
	if declaredPool != "" {
		return declaredPool
	}
	if bridgeName != defaultBridge {
		return ""
	}
	if pools := p.getIPPools(); len(pools) > 0 {
		return pools[0]
	}
	return ""
}
