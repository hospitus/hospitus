package jail

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	imagepkg "github.com/hospitus/hospitus/pkg/image"
	"github.com/hospitus/hospitus/pkg/provider"
)

// Defaults mirroring the create flag defaults in cmdutil (AddJailBehaviorFlags).
const (
	defaultDevfsRuleset = 4
	defaultSecurelevel  = -1
)

// containsRelease checks if a version string already contains RELEASE, STABLE, or CURRENT
func containsRelease(version string) bool {
	v := strings.ToUpper(version)
	return strings.Contains(v, "RELEASE") || strings.Contains(v, "STABLE") || strings.Contains(v, "CURRENT")
}

// nativeArch returns the FreeBSD architecture name for the host, used when
// --arch is "native" or unset so cross-arch hosts get the right image.
func nativeArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	case "riscv64":
		return "riscv64"
	default:
		return runtime.GOARCH
	}
}

func newCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new jail",
		Long: `Create a new FreeBSD jail.

Examples:
  # Create a simple jail (uses native architecture)
  hospitus jail create myjail

  # Create with specific image and resources
  hospitus jail create myjail --image freebsd-14.3-RELEASE-amd64 --cpus 2 --memory 1024

  # Create using version shortcut (builds image name automatically)
  hospitus jail create myjail -V 14.3                    # Uses freebsd-14.3-RELEASE-amd64
  hospitus jail create myjail -V 14.3 --arch arm64       # Uses freebsd-14.3-RELEASE-arm64

  # Create ARM64 jail on AMD64 host (cross-architecture)
  hospitus jail create arm-test --image freebsd-14.3-RELEASE-arm64 --vnet

  # Create with VNET and specific IP (defaults to hospitus0 bridge)
  hospitus jail create myjail --vnet --ip 10.0.0.10/24

  # Create with auto-start on boot
  hospitus jail create myjail --auto-start --auto-start-priority 10

  # Create for PostgreSQL (needs sysvipc)
  hospitus jail create pgdb --allow-sysvipc --image freebsd-14.3-RELEASE-amd64

  # Create with raw sockets (ping, traceroute)
  hospitus jail create myjail --allow-raw-sockets

  # Create with lifecycle hooks
  hospitus jail create myjail --exec-prestart "/usr/local/bin/pre.sh"

  # Aliases (power users)
  jcreate myjail --image freebsd-14.3-RELEASE-amd64`,
		Args: cobra.ExactArgs(1),
		RunE: runCreate,
	}

	cmdutil.AddCommonCreateFlags(cmd)

	// VNET and the -V version shortcut are read only here; the other
	// providers do not register them.
	cmd.Flags().Bool(cmdutil.FlagVNET, false, "Enable VNET (virtual network stack)")
	cmd.Flags().StringP(cmdutil.FlagVersion, "V", "", "FreeBSD version shortcut (e.g., 14.3, 15.0, RELEASE)")

	// Add jail-specific flags
	cmdutil.AddJailSecurityFlags(cmd)
	cmdutil.AddJailHookFlags(cmd)
	cmdutil.AddJailBehaviorFlags(cmd)

	return cmd
}

func runCreate(cmd *cobra.Command, args []string) error {
	pull, _ := cmd.Flags().GetBool(cmdutil.FlagPull)
	ctx := cmd.Context()
	name := args[0]

	// --cloud-init reaches this command from the flag set shared with bhyve and
	// qemu, and a jail does nothing with it: the provider never reads the spec
	// field. Accepting the file would report success over a jail that has
	// neither the files nor the hostname the cloud-config asked for. The
	// manifest layer refuses this combination; say the same thing here.
	if ci, _ := cmd.Flags().GetString(cmdutil.FlagCloudInit); ci != "" {
		return fmt.Errorf("cloud-init is only supported for bhyve and qemu, not jail\n" +
			"  → provision a jail with lifecycle hooks (--exec-poststart) or\n" +
			"    with the commands a manifest runs after creation")
	}

	// These three also come from the shared flag set, and the jail provider
	// reads none of them. Refuse rather than report success over a jail that
	// silently ignored them.
	if cmd.Flags().Changed(cmdutil.FlagDisk) {
		return fmt.Errorf("--disk is not supported for jails; use 'hospitus jail volume' to add ZFS volumes")
	}
	if bl, _ := cmd.Flags().GetString(cmdutil.FlagBootloader); bl != "" {
		return fmt.Errorf("--bootloader does not apply to jails; jails run on the host kernel")
	}
	if cmd.Flags().Changed(cmdutil.FlagSectorSize) {
		return fmt.Errorf("--sector-size only applies to VM disks (bhyve); jails have no virtual disk")
	}

	// Get common flags
	cpus, _ := cmd.Flags().GetInt(cmdutil.FlagCPUs)
	memory, _ := cmd.Flags().GetInt64(cmdutil.FlagMemory)

	// Validate resource flags
	if err := cmdutil.ValidateCPUs(cpus); err != nil {
		return err
	}
	if err := cmdutil.ValidateMemory(memory); err != nil {
		return err
	}

	image, _ := cmd.Flags().GetString(cmdutil.FlagImage)
	osType, _ := cmd.Flags().GetString(cmdutil.FlagOSType)
	osVersion, _ := cmd.Flags().GetString(cmdutil.FlagOSVersion)
	arch, _ := cmd.Flags().GetString(cmdutil.FlagArch)
	version, _ := cmd.Flags().GetString(cmdutil.FlagVersion)
	description, _ := cmd.Flags().GetString(cmdutil.FlagDescription)

	// Build image name from --version and --arch if --image not specified
	if image == "" && version != "" {
		// Normalize architecture
		effectiveArch := arch
		if effectiveArch == "native" || effectiveArch == "" {
			effectiveArch = nativeArch() // Use the host architecture for native
		}
		// Build image name: e.g., "freebsd-14.3-RELEASE-arm64" or "freebsd-15.0-RELEASE-amd64"
		if !containsRelease(version) {
			version += "-RELEASE"
		}
		image = fmt.Sprintf("freebsd-%s-%s", version, effectiveArch)
	}
	autoStart, _ := cmd.Flags().GetBool(cmdutil.FlagStart)
	vnet, _ := cmd.Flags().GetBool(cmdutil.FlagVNET)
	bridge, _ := cmd.Flags().GetString(cmdutil.FlagBridge)
	ip, _ := cmd.Flags().GetString(cmdutil.FlagIP)

	// Advanced networking flags
	vlan, _ := cmd.Flags().GetInt(cmdutil.FlagVLAN)
	bridgeFlags, _ := cmd.Flags().GetStringSlice(cmdutil.FlagBridgeFlags)

	// Advanced resource limit flags
	maxProc, _ := cmd.Flags().GetInt(cmdutil.FlagMaxProc)
	readBPSStr, _ := cmd.Flags().GetString(cmdutil.FlagReadBPS)
	writeBPSStr, _ := cmd.Flags().GetString(cmdutil.FlagWriteBPS)
	readIOPS, _ := cmd.Flags().GetInt64(cmdutil.FlagReadIOPS)
	writeIOPS, _ := cmd.Flags().GetInt64(cmdutil.FlagWriteIOPS)

	// bhyve create reports these; jail silently used 0, so "--read-bps 10Mo"
	// looked like it applied a limit and applied none.
	readBPS, err := cmdutil.ParseSize(readBPSStr)
	if err != nil {
		return fmt.Errorf("invalid --%s: %w", cmdutil.FlagReadBPS, err)
	}
	writeBPS, err := cmdutil.ParseSize(writeBPSStr)
	if err != nil {
		return fmt.Errorf("invalid --%s: %w", cmdutil.FlagWriteBPS, err)
	}

	bootAutoStart, _ := cmd.Flags().GetBool(cmdutil.FlagBootAutoStart)
	bootPriority, _ := cmd.Flags().GetInt(cmdutil.FlagBootAutoStartPriority)
	bootDelay, _ := cmd.Flags().GetInt(cmdutil.FlagBootAutoStartDelay)

	// Jail security flags
	allowRawSockets, _ := cmd.Flags().GetBool(cmdutil.FlagAllowRawSockets)
	allowSysVIPC, _ := cmd.Flags().GetBool(cmdutil.FlagAllowSysVIPC)
	allowMount, _ := cmd.Flags().GetBool(cmdutil.FlagAllowMount)
	allowMountDevfs, _ := cmd.Flags().GetBool(cmdutil.FlagAllowMountDevfs)
	allowMountNullfs, _ := cmd.Flags().GetBool(cmdutil.FlagAllowMountNullfs)
	allowMountTmpfs, _ := cmd.Flags().GetBool(cmdutil.FlagAllowMountTmpfs)
	allowMountZFS, _ := cmd.Flags().GetBool(cmdutil.FlagAllowMountZFS)
	allowVMM, _ := cmd.Flags().GetBool(cmdutil.FlagAllowVMM)
	allowMlock, _ := cmd.Flags().GetBool(cmdutil.FlagAllowMlock)
	allowReservedPorts, _ := cmd.Flags().GetBool(cmdutil.FlagAllowReservedPorts)

	// Jail hook flags
	execPrestart, _ := cmd.Flags().GetString(cmdutil.FlagExecPrestart)
	execPoststart, _ := cmd.Flags().GetString(cmdutil.FlagExecPoststart)
	execPrestop, _ := cmd.Flags().GetString(cmdutil.FlagExecPrestop)
	execPoststop, _ := cmd.Flags().GetString(cmdutil.FlagExecPoststop)
	execClean, _ := cmd.Flags().GetBool(cmdutil.FlagExecClean)

	// Jail behavior flags
	devfsRuleset, _ := cmd.Flags().GetInt(cmdutil.FlagDevfsRuleset)
	persist, _ := cmd.Flags().GetBool(cmdutil.FlagPersist)
	childrenMax, _ := cmd.Flags().GetInt(cmdutil.FlagChildrenMax)
	securelevel, _ := cmd.Flags().GetInt(cmdutil.FlagSecurelevel)

	// Build network spec if networking flags provided
	var networks []provider.NetworkSpec
	if vnet || bridge != "" || ip != "" || vlan > 0 || len(bridgeFlags) > 0 {
		netSpec := provider.NetworkSpec{
			Type: provider.NetworkTypeBridge,
		}
		if bridge != "" {
			netSpec.Bridge = bridge
		}
		if ip != "" {
			netSpec.IPv4 = ip
		}
		if vlan > 0 {
			netSpec.VLAN = vlan
		}
		if len(bridgeFlags) > 0 {
			netSpec.BridgeFlags = bridgeFlags
		}
		networks = append(networks, netSpec)
	}

	// Create instance spec
	spec := provider.InstanceSpec{
		Name:        name,
		Description: description,
		CPUs:        cpus,
		MemoryMB:    memory,
		MaxProc:     maxProc,
		ReadBPS:     readBPS,
		WriteBPS:    writeBPS,
		ReadIOPS:    readIOPS,
		WriteIOPS:   writeIOPS,
		Image:       image,
		OSType:      osType,
		OSVersion:   osVersion,
		Arch:        arch,
		Networks:    networks,
		Labels:      make(map[string]string),
		Annotations: make(map[string]string),
	}

	// Initialize provider config
	spec.ProviderConfig = make(map[string]interface{})

	// Add VNET flag to provider config
	if vnet {
		spec.ProviderConfig["vnet"] = true
	}

	// Add boot auto-start configuration
	if bootAutoStart {
		spec.ProviderConfig["autostart"] = true
		spec.ProviderConfig["autostart_priority"] = bootPriority
		spec.ProviderConfig["autostart_delay"] = bootDelay
	}

	// Add jail security parameters
	if allowRawSockets {
		spec.ProviderConfig["allow.raw_sockets"] = true
	}
	if allowSysVIPC {
		spec.ProviderConfig["allow.sysvipc"] = true
	}
	if allowMount {
		spec.ProviderConfig["allow.mount"] = true
	}
	if allowMountDevfs {
		spec.ProviderConfig["allow.mount.devfs"] = true
	}
	if allowMountNullfs {
		spec.ProviderConfig["allow.mount.nullfs"] = true
	}
	if allowMountTmpfs {
		spec.ProviderConfig["allow.mount.tmpfs"] = true
	}
	if allowMountZFS {
		spec.ProviderConfig["allow.mount.zfs"] = true
	}
	if allowVMM {
		spec.ProviderConfig["allow.vmm"] = true
	}
	if allowMlock {
		spec.ProviderConfig["allow.mlock"] = true
	}
	if allowReservedPorts {
		spec.ProviderConfig["allow.reserved_ports"] = true
	}

	// Add jail lifecycle hooks
	if execPrestart != "" {
		spec.ProviderConfig["exec.prestart"] = execPrestart
	}
	if execPoststart != "" {
		spec.ProviderConfig["exec.poststart"] = execPoststart
	}
	if execPrestop != "" {
		spec.ProviderConfig["exec.prestop"] = execPrestop
	}
	if execPoststop != "" {
		spec.ProviderConfig["exec.poststop"] = execPoststop
	}
	if execClean {
		spec.ProviderConfig["exec.clean"] = true
	}

	// Add jail behavior parameters
	if devfsRuleset != defaultDevfsRuleset { // Only if different from default
		spec.ProviderConfig["devfs_ruleset"] = devfsRuleset
	}
	if persist {
		spec.ProviderConfig["persist"] = true
	}
	if childrenMax > 0 {
		spec.ProviderConfig["children.max"] = childrenMax
	}
	if securelevel != defaultSecurelevel { // Only if different from default
		spec.ProviderConfig["securelevel"] = securelevel
	}

	req := client.CreateInstanceRequest{
		Provider: "jail",
		Spec:     spec,
	}

	// Create instance
	fmt.Fprintf(cmd.OutOrStdout(), "Creating jail %s...\n", name)
	instance, err := cmdutil.APIClient.CreateInstance(ctx, req)
	if err != nil {
		// Check if the error is "image not found" and offer to download
		if cmdutil.IsImageNotFoundError(err) && image != "" {
			// Check if the image exists in the available catalog
			imageDir := "/var/lib/hospitus/images"
			if dir := os.Getenv("HOSPITUS_IMAGE_DIR"); dir != "" {
				imageDir = dir
			}
			catalog := imagepkg.NewCatalog(imageDir)
			profile := catalog.FindProfile(image)

			if profile != nil {
				// Image exists in catalog, offer to download
				fmt.Fprintf(cmd.OutOrStdout(), "\nImage '%s' is not downloaded but is available in the catalog.\n", image)
				if cmdutil.ConfirmDownload("Would you like to download it now?", pull) {
					fmt.Fprintf(cmd.OutOrStdout(), "\nDownloading image %s...\n", image)
					fmt.Fprintf(cmd.OutOrStdout(), "This may take a few minutes...\n")

					if fetchErr := cmdutil.APIClient.FetchImage(ctx, image, cmd.OutOrStdout()); fetchErr != nil {
						return fmt.Errorf("failed to download image: %w", fetchErr)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Image downloaded successfully.\n\n")

					// Retry creating the jail
					fmt.Fprintf(cmd.OutOrStdout(), "Retrying jail creation...\n")
					instance, err = cmdutil.APIClient.CreateInstance(ctx, req)
					if err != nil {
						return fmt.Errorf("failed to create jail after downloading image: %w", err)
					}
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "\nTo download the image manually, run:\n")
					fmt.Fprintf(cmd.OutOrStdout(), "  hospitus image fetch %s\n", image)
					return fmt.Errorf("failed to create jail: image not downloaded")
				}
			} else {
				return fmt.Errorf("failed to create jail: %w (image '%s' is not in the available catalog, run 'hospitus image available' to see available images)", err, image)
			}
		} else {
			return fmt.Errorf("failed to create jail: %w", err)
		}
	}

	// Show boot auto-start status if enabled
	if bootAutoStart {
		fmt.Fprintf(cmd.OutOrStdout(), "Jail created: %s (ID: %s) [auto-start: priority=%d, delay=%dms]\n",
			instance.Name, instance.ID, bootPriority, bootDelay)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Jail created: %s (ID: %s)\n", instance.Name, instance.ID)
	}

	// Auto-start if requested (start now, not boot auto-start)
	if autoStart {
		fmt.Fprintf(cmd.OutOrStdout(), "Starting jail %s...\n", name)
		if err := cmdutil.APIClient.StartInstance(ctx, instance.ID, cmd.OutOrStdout()); err != nil {
			return fmt.Errorf("jail created but failed to start: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Jail started: %s\n", name)
	}

	return nil
}
