package bhyve

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	imagepkg "github.com/hospitus/hospitus/pkg/image"
	"github.com/hospitus/hospitus/pkg/provider"
)

func newCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new bhyve VM",
		Long: `Create a new bhyve virtual machine.

Examples:
  # Create a simple VM
  hospitus bhyve create myvm

  # Create with specific image and resources
  hospitus bhyve create myvm --image ubuntu-22.04 --cpus 4 --memory 4096

  # Create with VLAN tagging on first NIC
  hospitus bhyve create myvm --vlan 100

  # Create with IPv6 NAT enabled
  hospitus bhyve create myvm --ipv6

  # Create with USB tablet for pointer integration (no VNC)
  hospitus bhyve create myvm --usb-tablet

  # Create with virtio-scsi disk controller
  hospitus bhyve create myvm --disk-driver virtio-scsi

  # Add data disks (size in GB, optional device name)
  hospitus bhyve create myvm --disk 20:data --disk 50:backup

  # Attach an existing physical disk (external disk passthrough)
  hospitus bhyve create myvm --disk physical:/dev/ada2

  # PCI passthrough (e.g. a GPU); requires VT-d/IOMMU enabled
  hospitus bhyve create myvm --passthrough 1/0/0

  # Aliases (power users)
  bcreate myvm --image freebsd-14.1`,
		Args: cobra.ExactArgs(1),
		RunE: runCreate,
	}

	cmdutil.AddCommonCreateFlags(cmd)

	// bhyve-specific flags
	cmd.Flags().Bool(cmdutil.FlagUSBTablet, false, "Add a USB HID tablet device for accurate pointer events (without VNC)")
	cmd.Flags().StringSlice(cmdutil.FlagUSBDevices, nil, "PCI USB controller(s) to pass through (e.g. 0.14.0)")
	cmd.Flags().StringSlice(cmdutil.FlagPassthrough, nil, "PCI device(s) to pass through in bhyve bus/slot/func form (e.g. 1/0/0); requires VT-d/IOMMU")
	cmd.Flags().String(cmdutil.FlagDiskDriver, "", "Default disk controller driver: virtio-blk (default), virtio-scsi, ahci-hd, nvme")
	cmd.Flags().Bool(cmdutil.FlagIPv6, false, "Enable IPv6 NAT (ULA fd10::/64) in addition to IPv4 NAT")
	cmd.Flags().String(cmdutil.FlagIPv6Prefix, "", "Override the ULA IPv6 prefix (default: fd10::1/64)")

	return cmd
}

// networksFor builds the network a VM is created with.
//
// A VM with no network flags gets NAT rather than no interface at all. Hospitus
// runs a DHCP server on its NAT bridge and on no other, so this is the one mode
// that gives a new VM an address without the operator arranging anything, and it
// is what the quick start describes. Naming a bridge, an address or bridge flags
// asks for that bridge instead.
func networksFor(bridge, ip string, bridgeFlags []string) []provider.NetworkSpec {
	if bridge == "" && ip == "" && len(bridgeFlags) == 0 {
		return []provider.NetworkSpec{{Type: provider.NetworkTypeNAT}}
	}
	return []provider.NetworkSpec{{
		Type:        provider.NetworkTypeBridge,
		Bridge:      bridge,
		IPv4:        ip,
		BridgeFlags: bridgeFlags,
	}}
}

func runCreate(cmd *cobra.Command, args []string) error {
	pull, _ := cmd.Flags().GetBool(cmdutil.FlagPull)
	ctx := cmd.Context()
	name := args[0]

	// The flag reaches this command from the shared create flag set and goes
	// nowhere: nothing here reads it, and the API request carries no cloud_init
	// field. The provider does build a cloud-init ISO, but only a manifest
	// reaches that path, so accepting the file here would report success over an
	// unprovisioned VM.
	if ci, _ := cmd.Flags().GetString(cmdutil.FlagCloudInit); ci != "" {
		return fmt.Errorf("--cloud-init is not wired up for the command line\n" +
			"  → put the same configuration in a manifest's [cloud_init] section\n" +
			"    and apply it with 'hospitus apply'")
	}

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
	bootloader, _ := cmd.Flags().GetString(cmdutil.FlagBootloader)
	description, _ := cmd.Flags().GetString(cmdutil.FlagDescription)
	autoStart, _ := cmd.Flags().GetBool(cmdutil.FlagStart)

	// Advanced networking flags
	bridge, _ := cmd.Flags().GetString(cmdutil.FlagBridge)
	bridgeFlags, _ := cmd.Flags().GetStringSlice(cmdutil.FlagBridgeFlags)
	ip, _ := cmd.Flags().GetString(cmdutil.FlagIP)
	vlan, _ := cmd.Flags().GetInt(cmdutil.FlagVLAN)
	ipv6, _ := cmd.Flags().GetBool(cmdutil.FlagIPv6)
	ipv6Prefix, _ := cmd.Flags().GetString(cmdutil.FlagIPv6Prefix)

	// Advanced resource limit flags
	maxProc, _ := cmd.Flags().GetInt(cmdutil.FlagMaxProc)
	readBPSStr, _ := cmd.Flags().GetString(cmdutil.FlagReadBPS)
	writeBPSStr, _ := cmd.Flags().GetString(cmdutil.FlagWriteBPS)
	readIOPS, _ := cmd.Flags().GetInt64(cmdutil.FlagReadIOPS)
	writeIOPS, _ := cmd.Flags().GetInt64(cmdutil.FlagWriteIOPS)

	readBPS, err := cmdutil.ParseSize(readBPSStr)
	if err != nil {
		return fmt.Errorf("--read-bps invalid: %w", err)
	}
	writeBPS, err := cmdutil.ParseSize(writeBPSStr)
	if err != nil {
		return fmt.Errorf("--write-bps invalid: %w", err)
	}

	// Advanced storage flags
	sectorSize, _ := cmd.Flags().GetInt(cmdutil.FlagSectorSize)

	// bhyve-specific flags
	usbTablet, _ := cmd.Flags().GetBool(cmdutil.FlagUSBTablet)
	usbDevices, _ := cmd.Flags().GetStringSlice(cmdutil.FlagUSBDevices)
	pciDevices, _ := cmd.Flags().GetStringSlice(cmdutil.FlagPassthrough)
	diskDriver, _ := cmd.Flags().GetString(cmdutil.FlagDiskDriver)

	// Disk specifications (size[:name], physical:/dev/xxx for external disks)
	diskSpecs, _ := cmd.Flags().GetStringSlice(cmdutil.FlagDisk)
	disks, err := cmdutil.ParseDiskSpecs(diskSpecs)
	if err != nil {
		return err
	}

	// Build network spec.
	//
	// A VM with no network flags gets NAT rather than no interface at all.
	// Hospitus runs a DHCP server on its NAT bridge and on no other, so this is
	// the one mode that gives a new VM an address without the operator
	// arranging anything, and it is what the quick start describes. Naming a
	// bridge or an address asks for that bridge instead.
	networks := networksFor(bridge, ip, bridgeFlags)

	// Build bhyve-specific ProviderConfig
	providerConfig := map[string]interface{}{}
	if usbTablet {
		providerConfig["usb_tablet"] = true
	}
	if len(usbDevices) > 0 {
		providerConfig["usb_devices"] = usbDevices
	}
	if len(pciDevices) > 0 {
		providerConfig["passthrough"] = pciDevices
	}
	if diskDriver != "" {
		providerConfig["disk_driver"] = diskDriver
	}
	if ipv6 {
		providerConfig["ipv6_enabled"] = true
	}
	if ipv6Prefix != "" {
		providerConfig["ipv6_prefix"] = ipv6Prefix
	}
	if vlan > 0 {
		// Single VLAN ID for the first NIC; stored as a slice so the provider
		// can map VLANIDs[i] → TapDevs[i].
		providerConfig["vlan_ids"] = []int{vlan}
	}

	// Create instance spec
	spec := provider.InstanceSpec{
		Name:           name,
		Description:    description,
		CPUs:           cpus,
		MemoryMB:       memory,
		MaxProc:        maxProc,
		ReadBPS:        readBPS,
		WriteBPS:       writeBPS,
		ReadIOPS:       readIOPS,
		WriteIOPS:      writeIOPS,
		Image:          image,
		OSType:         osType,
		OSVersion:      osVersion,
		Arch:           arch,
		Bootloader:     bootloader,
		Networks:       networks,
		Labels:         make(map[string]string),
		Annotations:    make(map[string]string),
		ProviderConfig: providerConfig,
	}

	// Apply sector size to the primary disk if specified
	if sectorSize > 0 {
		if len(disks) == 0 {
			disks = []provider.DiskSpec{{SectorSize: sectorSize}}
		} else {
			disks[0].SectorSize = sectorSize
		}
	}
	spec.Disks = disks

	req := client.CreateInstanceRequest{
		Provider: "bhyve",
		Spec:     spec,
	}

	// Create instance
	fmt.Fprintf(cmd.OutOrStdout(), "Creating bhyve VM %s...\n", name)
	instance, err := cmdutil.APIClient.CreateInstance(ctx, req)
	if err != nil {
		// Check if the error is "image not found" and offer to download
		imageRef := image
		if strings.Contains(image, ":") {
			parts := strings.SplitN(image, ":", 2)
			imageRef = parts[1]
		}

		if cmdutil.IsImageNotFoundError(err) && imageRef != "" {
			// Check if the image exists in the available catalog
			imageDir := "/var/lib/hospitus/images"
			if dir := os.Getenv("HOSPITUS_IMAGE_DIR"); dir != "" {
				imageDir = dir
			}
			catalog := imagepkg.NewCatalog(imageDir)
			profile := catalog.FindProfile(imageRef)

			if profile != nil {
				// Image exists in catalog, offer to download
				fmt.Fprintf(cmd.OutOrStdout(), "\nImage '%s' is not downloaded but is available in the catalog.\n", imageRef)
				if cmdutil.ConfirmDownload("Would you like to download it now?", pull) {
					fmt.Fprintf(cmd.OutOrStdout(), "\nDownloading image %s...\n", imageRef)
					fmt.Fprintf(cmd.OutOrStdout(), "This may take a few minutes...\n")

					if fetchErr := cmdutil.APIClient.FetchImage(ctx, imageRef, cmd.OutOrStdout()); fetchErr != nil {
						return fmt.Errorf("failed to download image: %w", fetchErr)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Image downloaded successfully.\n\n")

					// Retry creating the VM
					fmt.Fprintf(cmd.OutOrStdout(), "Retrying VM creation...\n")
					instance, err = cmdutil.APIClient.CreateInstance(ctx, req)
					if err != nil {
						return fmt.Errorf("failed to create VM after downloading image: %w", err)
					}
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "\nTo download the image manually, run:\n")
					fmt.Fprintf(cmd.OutOrStdout(), "  hospitus image fetch %s\n", imageRef)
					return fmt.Errorf("failed to create VM: image not downloaded")
				}
			} else {
				return fmt.Errorf("failed to create VM: %w (image '%s' is not in the available catalog, run 'hospitus image available' to see available images)", err, imageRef)
			}
		} else {
			return fmt.Errorf("failed to create VM: %w", err)
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "VM created: %s (ID: %s)\n", instance.Name, instance.ID)

	// Record boot auto-start, which the create request does not carry.
	//
	// The three flags were accepted and dropped: a VM created with
	// --auto-start --auto-start-priority 10 did not appear in
	// "hospitus bhyve autostart list" at all, and would not have come up with the
	// host. Only "hospitus bhyve autostart enable" ever reached the provider.
	// Jail creation reports what it recorded, so this does too.
	if boot, _ := cmd.Flags().GetBool(cmdutil.FlagBootAutoStart); boot {
		priority, _ := cmd.Flags().GetInt(cmdutil.FlagBootAutoStartPriority)
		delay, _ := cmd.Flags().GetInt(cmdutil.FlagBootAutoStartDelay)
		if _, err := cmdutil.APIClient.SetAutoStart(ctx, "bhyve", instance.ID, provider.AutoStartConfig{
			Enabled:  true,
			Priority: priority,
			DelayMS:  delay,
		}); err != nil {
			return fmt.Errorf("VM created but auto-start could not be configured: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  auto-start: priority=%d, delay=%dms\n", priority, delay)
	}

	// Auto-start if requested
	if autoStart {
		fmt.Fprintf(cmd.OutOrStdout(), "Starting VM %s...\n", name)
		if err := cmdutil.APIClient.StartInstance(ctx, instance.ID, cmd.OutOrStdout()); err != nil {
			return fmt.Errorf("VM created but failed to start: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "VM started: %s\n", name)
	}

	return nil
}
