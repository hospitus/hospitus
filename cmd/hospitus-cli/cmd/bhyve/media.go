package bhyve

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/pkg/provider"
)

func newMediaCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "media",
		Short: "Manage removable media for a bhyve VM",
		Long: `Manage ISO images and removable media attached to a bhyve VM.

Media operations let you attach an installation ISO before first boot,
then detach it once the OS is installed.

Examples:
  # Attach a Windows ISO as bootable CD-ROM
  hospitus bhyve media attach win11 --iso ~/Win11_25H2_French_x64_v2.iso --bootable

  # Attach an ISO as non-bootable data disc
  hospitus bhyve media attach myvm --iso /path/to/drivers.iso

  # List attached media devices
  hospitus bhyve media list win11

  # Detach a specific device
  hospitus bhyve media detach win11 --device cdrom0

  # Detach all media (ejects first CD-ROM drive found)
  hospitus bhyve media detach win11`,
	}

	cmd.AddCommand(newMediaAttachCommand())
	cmd.AddCommand(newMediaDetachCommand())
	cmd.AddCommand(newMediaListCommand())

	return cmd
}

func newMediaAttachCommand() *cobra.Command {
	var (
		isoPath  string
		deviceID string
		bootable bool
	)

	cmd := &cobra.Command{
		Use:     "attach <vm-name>",
		Aliases: []string{"insert"},
		Short:   "Attach an ISO image to a bhyve VM",
		Long: `Attach an ISO image as a CD-ROM drive to a bhyve VM.

For stopped VMs, the ISO is recorded in the VM configuration and will
be available on next start.

For running VMs, attachment is hot-plugged where supported. Most UEFI
installation ISOs require a restart to be picked up by the firmware.

Examples:
  # Attach a Windows installation ISO
  hospitus bhyve media attach win11 --iso ~/Win11_25H2_French_x64_v2.iso --bootable

  # Attach virtio drivers ISO (non-bootable, second CD-ROM slot)
  hospitus bhyve media attach win11 --iso ~/virtio-win.iso --device cdrom1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName := args[0]
			if isoPath == "" {
				return fmt.Errorf("--iso is required")
			}

			ctx := cmd.Context()
			resolvedName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.APIClient, vmName, "bhyve")
			if err != nil {
				return fmt.Errorf("VM %q not found: %w", vmName, err)
			}

			spec := provider.MediaSpec{
				Type:     provider.MediaTypeCDROM,
				Path:     isoPath,
				ReadOnly: true,
				Bootable: bootable,
				DeviceID: deviceID,
			}

			if err := cmdutil.APIClient.InsertMedia(ctx, resolvedName, spec); err != nil {
				return fmt.Errorf("failed to attach media: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "ISO attached to %s: %s\n", resolvedName, isoPath)
			fmt.Fprintln(cmd.OutOrStdout(), "Note: restart the VM to boot from the ISO.")
			return nil
		},
	}

	cmd.Flags().StringVar(&isoPath, "iso", "", "Path to ISO image file (required)")
	cmd.Flags().StringVar(&deviceID, "device", "", "Drive slot (e.g. cdrom0, cdrom1). Default: first available")
	cmd.Flags().BoolVar(&bootable, "bootable", false, "Mark the ISO as a bootable device")
	_ = cmd.MarkFlagRequired("iso")

	return cmd
}

func newMediaDetachCommand() *cobra.Command {
	var deviceID string

	cmd := &cobra.Command{
		Use:     "detach <vm-name>",
		Aliases: []string{"eject"},
		Short:   "Detach (eject) media from a bhyve VM",
		Long: `Eject an ISO image or removable media from a bhyve VM.

If --device is not specified, the first inserted CD-ROM drive is ejected.

Examples:
  # Eject the default CD-ROM drive
  hospitus bhyve media detach win11

  # Eject a specific drive slot
  hospitus bhyve media detach win11 --device cdrom0`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName := args[0]

			ctx := cmd.Context()
			resolvedName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.APIClient, vmName, "bhyve")
			if err != nil {
				return fmt.Errorf("VM %q not found: %w", vmName, err)
			}

			// If no device specified, find the first inserted CD-ROM
			// Discovered into a local, not back into the flag variable: that
			// one outlives the invocation, so the slot found for one VM
			// became the default for the next command in the same process.
			ejectDevice := deviceID
			if ejectDevice == "" {
				media, err := cmdutil.APIClient.ListMedia(ctx, resolvedName)
				if err != nil {
					return fmt.Errorf("failed to list media: %w", err)
				}
				for _, m := range media {
					if m.Inserted {
						ejectDevice = m.DeviceID
						break
					}
				}
				if ejectDevice == "" {
					return fmt.Errorf("no inserted media found on VM %s", resolvedName)
				}
			}

			if err := cmdutil.APIClient.EjectMedia(ctx, resolvedName, ejectDevice); err != nil {
				return fmt.Errorf("failed to eject media: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Media ejected from %s (device: %s)\n", resolvedName, ejectDevice)
			fmt.Fprintln(cmd.OutOrStdout(), "Note: restart the VM to apply changes.")
			return nil
		},
	}

	cmd.Flags().StringVar(&deviceID, "device", "", "Drive slot to eject (e.g. cdrom0). Default: first inserted drive")

	return cmd
}

func newMediaListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list <vm-name>",
		Aliases: []string{"ls"},
		Short:   "List media devices attached to a bhyve VM",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName := args[0]

			ctx := cmd.Context()
			resolvedName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.APIClient, vmName, "bhyve")
			if err != nil {
				return fmt.Errorf("VM %q not found: %w", vmName, err)
			}

			media, err := cmdutil.APIClient.ListMedia(ctx, resolvedName)
			if err != nil {
				return fmt.Errorf("failed to list media: %w", err)
			}

			if len(media) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No media devices on VM %s\n", resolvedName)
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "DEVICE\tTYPE\tINSERTED\tPATH")
			for _, m := range media {
				inserted := "no"
				if m.Inserted {
					inserted = "yes"
				}
				path := m.Path
				if path == "" {
					path = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.DeviceID, m.Type, inserted, path)
			}
			return w.Flush()
		},
	}

	return cmd
}
