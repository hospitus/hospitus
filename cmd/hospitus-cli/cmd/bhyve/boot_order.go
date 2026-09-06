package bhyve

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/pkg/provider"
)

func newBootOrderCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "boot-order",
		Short: "Manage boot device order for a bhyve VM",
		Long: `View or change the UEFI boot device order for a bhyve VM.

Boot order controls which device the firmware tries first when starting.
Changes take effect on the next VM start.

Valid device types: disk, cdrom, network, floppy, usb

Examples:
  # Show the current boot order
  hospitus bhyve boot-order get myvm

  # Boot from cdrom first (e.g. for OS installation), then disk
  hospitus bhyve boot-order set myvm cdrom disk

  # Boot only from disk (default production setup)
  hospitus bhyve boot-order set myvm disk

  # Boot from network (PXE) first, then disk
  hospitus bhyve boot-order set myvm network disk

  # Boot from disk just once, then revert to normal order
  hospitus bhyve boot-order once myvm disk`,
	}

	cmd.AddCommand(newBootOrderGetCommand())
	cmd.AddCommand(newBootOrderSetCommand())
	cmd.AddCommand(newBootOrderOnceCommand())

	return cmd
}

func newBootOrderGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <vm-name>",
		Short: "Show the current boot device order",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName := args[0]
			if err := cmdutil.RequireInstanceOf(cmd.Context(), cmdutil.APIClient, vmName, "bhyve"); err != nil {
				return err
			}
			ctx := cmd.Context()

			resolvedName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.APIClient, vmName, "bhyve")
			if err != nil {
				return fmt.Errorf("VM %q not found: %w", vmName, err)
			}

			order, err := cmdutil.APIClient.GetBootOrder(ctx, resolvedName)
			if err != nil {
				return fmt.Errorf("failed to get boot order: %w", err)
			}

			if order == nil || len(order.Devices) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Boot order: (firmware default)")
				return nil
			}

			devices := make([]string, len(order.Devices))
			for i, d := range order.Devices {
				devices[i] = string(d)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Boot order: %s\n", strings.Join(devices, " → "))

			if order.OnceDevice != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Next boot override: %s\n", order.OnceDevice)
			}

			return nil
		},
	}
}

func newBootOrderSetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set <vm-name> <device> [device...]",
		Short: "Set the boot device order",
		Long: `Set the boot device priority order for a bhyve VM.

Devices are tried in the order listed. Valid device types:
  disk     - Primary hard disk / ZVOL
  cdrom    - CD/DVD-ROM drive
  network  - PXE network boot
  floppy   - Floppy disk
  usb      - USB device

Changes take effect on the next VM start.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName := args[0]
			if err := cmdutil.RequireInstanceOf(cmd.Context(), cmdutil.APIClient, vmName, "bhyve"); err != nil {
				return err
			}
			ctx := cmd.Context()

			resolvedName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.APIClient, vmName, "bhyve")
			if err != nil {
				return fmt.Errorf("VM %q not found: %w", vmName, err)
			}

			devices, err := parseBootDevices(args[1:])
			if err != nil {
				return err
			}

			order := provider.BootOrder{Devices: devices}
			if err := cmdutil.APIClient.SetBootOrder(ctx, resolvedName, order); err != nil {
				return fmt.Errorf("failed to set boot order: %w", err)
			}

			names := make([]string, len(devices))
			for i, d := range devices {
				names[i] = string(d)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Boot order set for %s: %s\n", resolvedName, strings.Join(names, " → "))
			fmt.Fprintln(cmd.OutOrStdout(), "Note: restart the VM for changes to take effect.")
			return nil
		},
	}
}

func newBootOrderOnceCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "once <vm-name> <device>",
		Short: "Boot from a specific device on the next start only",
		Long: `Override the boot device for the next start only.

After one boot cycle the VM returns to its normal boot order.
Useful for booting from an ISO to re-install or recover, without
permanently changing the boot order.

Valid device types: disk, cdrom, network, floppy, usb`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName := args[0]
			if err := cmdutil.RequireInstanceOf(cmd.Context(), cmdutil.APIClient, vmName, "bhyve"); err != nil {
				return err
			}
			ctx := cmd.Context()

			resolvedName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.APIClient, vmName, "bhyve")
			if err != nil {
				return fmt.Errorf("VM %q not found: %w", vmName, err)
			}

			devices, err := parseBootDevices([]string{args[1]})
			if err != nil {
				return err
			}

			// Fetch existing order so we only change the once field
			existing, err := cmdutil.APIClient.GetBootOrder(ctx, resolvedName)
			if err != nil {
				return fmt.Errorf("failed to get current boot order: %w", err)
			}

			order := provider.BootOrder{OnceDevice: devices[0]}
			if existing != nil {
				order.Devices = existing.Devices
			}

			if err := cmdutil.APIClient.SetBootOrder(ctx, resolvedName, order); err != nil {
				return fmt.Errorf("failed to set boot-once override: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Next boot of %s will use: %s\n", resolvedName, devices[0])
			fmt.Fprintln(cmd.OutOrStdout(), "After one boot cycle the normal boot order resumes.")
			return nil
		},
	}
}

// parseBootDevices validates and converts string device names to provider.BootDevice values.
func parseBootDevices(args []string) ([]provider.BootDevice, error) {
	valid := map[string]provider.BootDevice{
		"disk":    provider.BootDeviceHardDisk,
		"cdrom":   provider.BootDeviceCDROM,
		"network": provider.BootDeviceNetwork,
		"floppy":  provider.BootDeviceFloppy,
		"usb":     provider.BootDeviceUSB,
	}

	devices := make([]provider.BootDevice, 0, len(args))
	for _, arg := range args {
		d, ok := valid[strings.ToLower(arg)]
		if !ok {
			return nil, fmt.Errorf("unknown boot device %q — valid types: disk, cdrom, network, floppy, usb", arg)
		}
		devices = append(devices, d)
	}
	return devices, nil
}
