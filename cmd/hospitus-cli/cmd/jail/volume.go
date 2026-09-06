package jail

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
)

func newVolumeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volume",
		Short: "Manage persistent volumes",
		Long: `Manage ZFS-backed persistent volumes that can be attached to jails.

Volumes are named ZFS datasets that can be mounted into jails for persistent
storage. They support quotas, reservations, compression, and snapshots.

Examples:
  # Create a volume
  hospitus jail volume create data --size 10G

  # List all volumes
  hospitus jail volume list

  # Attach volume to jail
  hospitus jail volume attach web data /var/www

  # Detach volume from jail
  hospitus jail volume detach web data`,
	}

	cmd.AddCommand(newVolumeCreateCommand())
	cmd.AddCommand(newVolumeDeleteCommand())
	cmd.AddCommand(newVolumeListCommand())
	cmd.AddCommand(newVolumeInfoCommand())
	cmd.AddCommand(newVolumeAttachCommand())
	cmd.AddCommand(newVolumeDetachCommand())

	return cmd
}

func newVolumeCreateCommand() *cobra.Command {
	var size string
	var quota string
	var reservation string
	var compression string
	var description string

	cmd := &cobra.Command{
		Use:   "create <volume-name>",
		Short: "Create a new volume",
		Long: `Create a new ZFS-backed volume.

Options:
  --size         Initial size allocation (e.g., 10G, 500M)
  --quota        ZFS quota to limit maximum size
  --reservation  ZFS reservation to guarantee space
  --compression  Compression algorithm (lz4, zstd, gzip, off)

Examples:
  hospitus jail volume create data --size 10G
  hospitus jail volume create logs --quota 5G --compression zstd
  hospitus jail volume create db --reservation 20G`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVolumeCreate(cmd, args[0], size, quota, reservation, compression, description)
		},
	}

	cmd.Flags().StringVar(&size, "size", "", "Initial size allocation (e.g., 10G)")
	cmd.Flags().StringVar(&quota, "quota", "", "ZFS quota to limit maximum size")
	cmd.Flags().StringVar(&reservation, "reservation", "", "ZFS reservation to guarantee space")
	cmd.Flags().StringVar(&compression, "compression", "lz4", "Compression (lz4, zstd, gzip, off)")
	cmd.Flags().StringVar(&description, "description", "", "Volume description")

	return cmd
}

func newVolumeDeleteCommand() *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:     "delete <volume-name>",
		Aliases: []string{"rm", "destroy"},
		Short:   "Delete a volume",
		Long: `Delete a volume and the data on it. The volume must not be attached to any jail.

Examples:
  hospitus jail volume delete data
  hospitus jail volume delete temp -y`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVolumeDelete(cmd, args[0], yes)
		},
	}

	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt")

	return cmd
}

func newVolumeListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all volumes",
		Long: `List all volumes and their information.

Examples:
  hospitus jail volume list`,
		Args: cobra.NoArgs,
		RunE: runVolumeList,
	}

	cmdutil.AddOutputFlag(cmd)

	return cmd
}

func newVolumeInfoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "info <volume-name>",
		Short: "Show volume details",
		Long: `Show detailed information about a volume.

Examples:
  hospitus jail volume info data`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVolumeInfo(cmd, args[0])
		},
	}

	return cmd
}

func newVolumeAttachCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attach <jail-name> <volume-name> <mount-point>",
		Short: "Attach a volume to a jail",
		Long: `Attach a volume to a jail at the specified mount point.

The mount point path is relative to the jail root.

Examples:
  hospitus jail volume attach web data /var/www
  hospitus jail volume attach db postgres-data /var/db/postgres`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVolumeAttach(cmd, args[0], args[1], args[2])
		},
	}

	return cmd
}

func newVolumeDetachCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "detach <jail-name> <volume-name>",
		Short: "Detach a volume from a jail",
		Long: `Detach a volume from a jail.

Examples:
  hospitus jail volume detach web data`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVolumeDetach(cmd, args[0], args[1])
		},
	}

	return cmd
}

func runVolumeCreate(cmd *cobra.Command, name, size, quota, reservation, compression, description string) error {
	ctx := cmd.Context()

	req := client.CreateVolumeRequest{
		Name:        name,
		Size:        size,
		Quota:       quota,
		Reservation: reservation,
		Compression: compression,
		Description: description,
	}

	vol, err := cmdutil.APIClient.CreateVolume(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create volume: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Volume '%s' created\n", vol.Name)
	if vol.Dataset != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Dataset: %s\n", vol.Dataset)
	}

	return nil
}

// runVolumeDelete removes a volume, asking first.
//
// A volume holds a workload's data and outlives the instance that used it, so
// removing one is not something to do on a typed name alone.
func runVolumeDelete(cmd *cobra.Command, name string, yes bool) error {
	ctx := cmd.Context()

	if !yes {
		fmt.Fprintf(cmd.OutOrStdout(), "Will delete volume %s and the data on it.\n", name)
		if !cmdutil.AskYesNo("Are you sure?") {
			fmt.Fprintln(cmd.OutOrStdout(), "Canceled")
			return nil
		}
	}

	if err := cmdutil.APIClient.DeleteVolume(ctx, name); err != nil {
		return fmt.Errorf("failed to delete volume: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Volume '%s' deleted\n", name)
	return nil
}

// formatQuota renders an unset size or quota as a dash rather than "0 B".
func formatQuota(bytes int64) string {
	if bytes <= 0 {
		return "-"
	}
	return formatSize(bytes)
}

func runVolumeList(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	volumes, err := cmdutil.APIClient.ListVolumes(ctx)
	if err != nil {
		return fmt.Errorf("failed to list volumes: %w", err)
	}

	if len(volumes) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No volumes found")
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSIZE\tQUOTA\tUSED\tAVAILABLE\tCOMPRESSION\tDESCRIPTION")

	for i := range volumes {
		vol := &volumes[i]
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			vol.Name,
			formatQuota(vol.Size),
			formatQuota(vol.Quota),
			formatSize(vol.Used),
			formatSize(vol.Available),
			vol.Compression,
			truncate(vol.Description, 30))
	}
	w.Flush()

	fmt.Fprintf(cmd.OutOrStdout(), "\nTotal: %d volume(s)\n", len(volumes))

	return nil
}

func runVolumeInfo(cmd *cobra.Command, name string) error {
	ctx := cmd.Context()

	vol, err := cmdutil.APIClient.GetVolume(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to get volume: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Name:        %s\n", vol.Name)
	fmt.Fprintf(cmd.OutOrStdout(), "Dataset:     %s\n", vol.Dataset)
	if vol.Size > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Size:        %s\n", formatSize(vol.Size))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Used:        %s\n", formatSize(vol.Used))
	fmt.Fprintf(cmd.OutOrStdout(), "Available:   %s\n", formatSize(vol.Available))
	if vol.Quota > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Quota:       %s\n", formatSize(vol.Quota))
	}
	if vol.Reservation > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Reservation: %s\n", formatSize(vol.Reservation))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Compression: %s\n", vol.Compression)
	if vol.MountPoint != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Mount Point: %s\n", vol.MountPoint)
	}
	if vol.Description != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Description: %s\n", vol.Description)
	}
	if vol.CreatedAt != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Created:     %s\n", vol.CreatedAt)
	}

	return nil
}

func runVolumeAttach(cmd *cobra.Command, jailName, volumeName, mountPoint string) error {
	ctx := cmd.Context()

	if err := cmdutil.APIClient.AttachVolume(ctx, jailName, volumeName, mountPoint); err != nil {
		return fmt.Errorf("failed to attach volume: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Volume '%s' attached to jail '%s' at '%s'\n",
		volumeName, jailName, mountPoint)
	return nil
}

func runVolumeDetach(cmd *cobra.Command, jailName, volumeName string) error {
	ctx := cmd.Context()

	if err := cmdutil.APIClient.DetachVolume(ctx, jailName, volumeName); err != nil {
		return fmt.Errorf("failed to detach volume: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Volume '%s' detached from jail '%s'\n", volumeName, jailName)
	return nil
}

// formatSize formats bytes into human-readable size
func formatSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
		TB = GB * 1024
	)

	switch {
	case bytes >= TB:
		return fmt.Sprintf("%.1fT", float64(bytes)/TB)
	case bytes >= GB:
		return fmt.Sprintf("%.1fG", float64(bytes)/GB)
	case bytes >= MB:
		return fmt.Sprintf("%.1fM", float64(bytes)/MB)
	case bytes >= KB:
		return fmt.Sprintf("%.1fK", float64(bytes)/KB)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

// truncate truncates a string to maxLen length
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
