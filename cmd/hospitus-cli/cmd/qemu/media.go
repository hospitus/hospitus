package qemu

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/pkg/provider"
)

var (
	mediaPath     string
	mediaReadOnly bool
	mediaBootable bool
)

// newMediaCommand creates the media parent command
func newMediaCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "media",
		Short: "Manage VM removable media (CD-ROM, ISO)",
		Long: `Manage removable media for QEMU virtual machines.

This command allows you to insert, eject, and list CD-ROM/ISO media.
Useful for OS installation workflows where you need to boot from an ISO,
complete the installation, then eject the ISO and boot from disk.

Examples:
  # Insert an ISO image
  hospitus qemu media insert myvm --path /var/lib/hospitus/images/iso/FreeBSD-14.2-RELEASE-amd64-disc1.iso

  # List media devices
  hospitus qemu media list myvm

  # Eject CD-ROM
  hospitus qemu media eject myvm ide0-cd0

  # Change the boot order (dedicated command)
  hospitus qemu boot-order set myvm disk cdrom`,
	}

	cmd.AddCommand(newMediaInsertCommand())
	cmd.AddCommand(newMediaEjectCommand())
	cmd.AddCommand(newMediaListCommand())

	return cmd
}

func newMediaInsertCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "insert <instance> [device-id]",
		Short: "Insert media into a CD-ROM drive",
		Long: `Insert an ISO image or other media into a VM's CD-ROM drive.

If device-id is not specified, the first available CD-ROM drive is used.

Examples:
  # Insert ISO into default CD-ROM
  hospitus qemu media insert myvm --path /var/lib/hospitus/images/iso/FreeBSD-14.2.iso

  # Insert into specific device
  hospitus qemu media insert myvm ide0-cd0 --path /path/to/image.iso

  # Insert read-only media (default)
  hospitus qemu media insert myvm --path /path/to/image.iso --readonly`,
		Args: cobra.RangeArgs(1, 2),
		RunE: runMediaInsert,
	}

	cmd.Flags().StringVar(&mediaPath, "path", "", "Path to the ISO or media file (required)")
	cmd.Flags().BoolVar(&mediaReadOnly, "readonly", true, "Mount media as read-only")
	cmd.Flags().BoolVar(&mediaBootable, "bootable", false, "Mark media as bootable")
	_ = cmd.MarkFlagRequired("path")

	return cmd
}

func runMediaInsert(cmd *cobra.Command, args []string) error {
	instanceName := args[0]
	if err := cmdutil.RequireInstanceOf(cmd.Context(), cmdutil.APIClient, instanceName, "qemu"); err != nil {
		return err
	}
	deviceID := ""
	if len(args) > 1 {
		deviceID = args[1]
	}

	ctx := cmd.Context()

	// Validate path exists
	if _, err := os.Stat(mediaPath); os.IsNotExist(err) {
		return fmt.Errorf("media file not found: %s", mediaPath)
	}

	spec := provider.MediaSpec{
		DeviceID: deviceID,
		Type:     provider.MediaTypeCDROM,
		Path:     mediaPath,
		ReadOnly: mediaReadOnly,
		Bootable: mediaBootable,
	}

	err := cmdutil.APIClient.InsertMedia(ctx, instanceName, spec)
	if err != nil {
		return fmt.Errorf("failed to insert media: %w", err)
	}

	fmt.Printf("Media inserted successfully into %s\n", instanceName)
	if deviceID != "" {
		fmt.Printf("  Device: %s\n", deviceID)
	}
	fmt.Printf("  Path: %s\n", mediaPath)

	return nil
}

func newMediaEjectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eject <instance> <device-id>",
		Short: "Eject media from a CD-ROM drive",
		Long: `Eject media from a VM's CD-ROM drive.

This is typically used after completing an OS installation from ISO.

Examples:
  # Eject CD-ROM
  hospitus qemu media eject myvm ide0-cd0

  # After eject, change boot order to boot from disk
  hospitus qemu boot-order set myvm disk`,
		Args: cobra.ExactArgs(2),
		RunE: runMediaEject,
	}

	return cmd
}

func runMediaEject(cmd *cobra.Command, args []string) error {
	instanceName := args[0]
	if err := cmdutil.RequireInstanceOf(cmd.Context(), cmdutil.APIClient, instanceName, "qemu"); err != nil {
		return err
	}
	deviceID := args[1]

	ctx := cmd.Context()

	err := cmdutil.APIClient.EjectMedia(ctx, instanceName, deviceID)
	if err != nil {
		return fmt.Errorf("failed to eject media: %w", err)
	}

	fmt.Printf("Media ejected from device %s on %s\n", deviceID, instanceName)

	return nil
}

func newMediaListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <instance>",
		Short: "List media devices and their status",
		Long: `List all removable media devices (CD-ROM, floppy, USB) and their current status.

Shows whether media is inserted and the path to the current media file.

Examples:
  # List media devices
  hospitus qemu media list myvm

  # Output as JSON
  hospitus qemu media list myvm --output json`,
		Args: cobra.ExactArgs(1),
		RunE: runMediaList,
	}

	cmdutil.AddOutputFlag(cmd)

	return cmd
}

func runMediaList(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	instanceName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, instanceName, "qemu"); err != nil {
		return err
	}
	outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput)

	media, err := cmdutil.APIClient.ListMedia(ctx, instanceName)
	if err != nil {
		return fmt.Errorf("failed to list media: %w", err)
	}

	if len(media) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No removable media devices found.")
		return nil
	}

	if outputFmt == "json" {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(media)
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DEVICE\tTYPE\tSTATUS\tPATH")

	for _, m := range media {
		status := "empty"
		if m.Inserted {
			status = "inserted"
		}
		path := m.Path
		if path == "" {
			path = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.DeviceID, m.Type, status, path)
	}
	w.Flush()

	return nil
}
