package jail

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
)

func newSnapshotCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Manage jail snapshots",
		Long: `Manage ZFS snapshots for jails.

Snapshots provide instant, space-efficient point-in-time copies of a jail's
filesystem. They can be used for:
  - Creating backups before risky operations
  - Testing changes safely (restore if something breaks)
  - Cloning jails efficiently

Examples:
  # Create a snapshot before upgrading
  hospitus jail snapshot create myjail before-upgrade

  # List all snapshots
  hospitus jail snapshot list myjail

  # Restore to a previous snapshot
  hospitus jail snapshot restore myjail before-upgrade

  # Delete an old snapshot
  hospitus jail snapshot delete myjail old-snapshot`,
	}

	cmd.AddCommand(newSnapshotCreateCommand())
	cmd.AddCommand(newSnapshotListCommand())
	cmd.AddCommand(newSnapshotDeleteCommand())
	cmd.AddCommand(newSnapshotRestoreCommand())

	return cmd
}

func newSnapshotCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <jail-name> <snapshot-name>",
		Short: "Create a snapshot of a jail",
		Long: `Create a ZFS snapshot of the jail's filesystem.

Snapshots are instant and initially take no additional space.
Space is consumed as the jail's filesystem changes.

Examples:
  hospitus jail snapshot create myjail daily-backup
  hospitus jail snapshot create postgres before-upgrade`,
		Args: cobra.ExactArgs(2),
		RunE: runSnapshotCreate,
	}

	return cmd
}

func runSnapshotCreate(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	jailName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, jailName, "jail"); err != nil {
		return err
	}
	snapshotName := args[1]

	fmt.Fprintf(cmd.OutOrStdout(), "Creating snapshot %s@%s...\n", jailName, snapshotName)

	if err := cmdutil.APIClient.CreateSnapshot(ctx, jailName, snapshotName); err != nil {
		return fmt.Errorf("failed to create snapshot: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Snapshot created: %s@%s\n", jailName, snapshotName)
	return nil
}

func newSnapshotListCommand() *cobra.Command {
	var (
		outputFormat string
		all          bool
		pattern      string
	)

	cmd := &cobra.Command{
		Use:   "list [jail-name]",
		Short: "List snapshots of a jail",
		Long: `List all ZFS snapshots for a jail.

Shows snapshot name, creation time, and size.

Use -a/--all to list snapshots across all jails.
Use -x/--pattern to filter jails by glob pattern (e.g., "app-*").

Examples:
  hospitus jail snapshot list myjail
  hospitus jail snapshot list myjail --output json
  hospitus jail snapshot list -a                    # List all snapshots
  hospitus jail snapshot list -x "app-*"            # List snapshots for jails matching "app-*"
  hospitus jail snapshot list --pattern "web*"      # List snapshots for jails matching "web*"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSnapshotList(cmd, args, outputFormat, all, pattern)
		},
	}

	cmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "Output format (table, json)")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "List snapshots for all jails")
	cmd.Flags().StringVarP(&pattern, "pattern", "x", "", "Filter jails by glob pattern (e.g., \"app-*\")")

	return cmd
}

// snapshotWithInstance holds snapshot info with its parent instance name
type snapshotWithInstance struct {
	Instance string `json:"instance"`
	client.SnapshotInfo
}

func runSnapshotList(cmd *cobra.Command, args []string, outputFormat string, all bool, pattern string) error {
	ctx := cmd.Context()

	// A positional jail name cannot be combined with -a/--all or -x/--pattern:
	// reject the combination rather than silently ignoring the argument.
	if len(args) > 0 && (all || pattern != "") {
		return fmt.Errorf("cannot combine a jail name with -a/--all or -x/--pattern")
	}

	// Determine which jails to list snapshots for
	var jailNames []string

	switch {
	case len(args) == 1 && !all && pattern == "":
		// Single jail specified — confirmed to be one, like every other
		// branch here reaches only jails.
		if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, args[0], "jail"); err != nil {
			return err
		}
		jailNames = []string{args[0]}
	case all || pattern != "":
		// List all instances and optionally filter by pattern
		// Filtered to jails, as runList does. Without it this walked every
		// instance on the host and asked the jail endpoints about VMs.
		instances, err := cmdutil.APIClient.ListInstances(ctx, client.ListInstancesFilter{Provider: "jail"})
		if err != nil {
			return fmt.Errorf("failed to list instances: %w", err)
		}

		for _, inst := range instances {
			// Apply pattern filter if specified
			if pattern != "" {
				matched, err := filepath.Match(pattern, inst.Name)
				if err != nil {
					return fmt.Errorf("invalid pattern %q: %w", pattern, err)
				}
				if !matched {
					continue
				}
			}
			jailNames = append(jailNames, inst.Name)
		}

		if len(jailNames) == 0 {
			if pattern != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "No jails matching pattern %q\n", pattern)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "No jails found\n")
			}
			return nil
		}

		// Sort jail names for consistent output
		sort.Strings(jailNames)
	default:
		return fmt.Errorf("jail name required (or use -a/--all or -x/--pattern)")
	}

	// Collect all snapshots
	var allSnapshots []snapshotWithInstance
	multipleJails := len(jailNames) > 1

	for _, jailName := range jailNames {
		snapshots, err := cmdutil.APIClient.ListSnapshots(ctx, jailName)
		if err != nil {
			// Skip jails that don't support snapshots or have errors
			if multipleJails {
				continue
			}
			return fmt.Errorf("failed to list snapshots for %s: %w", jailName, err)
		}

		for _, snap := range snapshots {
			allSnapshots = append(allSnapshots, snapshotWithInstance{
				Instance:     jailName,
				SnapshotInfo: snap,
			})
		}
	}

	if len(allSnapshots) == 0 {
		if multipleJails {
			if pattern != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "No snapshots found for jails matching %q\n", pattern)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "No snapshots found\n")
			}
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "No snapshots found for jail %s\n", jailNames[0])
		}
		return nil
	}

	if outputFormat == "json" {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(allSnapshots)
	}

	// Table output
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	if multipleJails {
		fmt.Fprintln(w, "INSTANCE\tSNAPSHOT\tCREATED\tSIZE")
		for _, snap := range allSnapshots {
			size := fmt.Sprintf("%dMB", snap.SizeMB)
			if snap.SizeMB == 0 {
				size = "0B"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				snap.Instance,
				snap.Name,
				snap.CreatedAt.Format("2006-01-02 15:04:05"),
				size,
			)
		}
	} else {
		fmt.Fprintln(w, "SNAPSHOT\tCREATED\tSIZE")
		for _, snap := range allSnapshots {
			size := fmt.Sprintf("%dMB", snap.SizeMB)
			if snap.SizeMB == 0 {
				size = "0B"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n",
				snap.Name,
				snap.CreatedAt.Format("2006-01-02 15:04:05"),
				size,
			)
		}
	}
	w.Flush()

	return nil
}

func newSnapshotDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <jail-name> <snapshot-name>",
		Short: "Delete a snapshot",
		Long: `Delete a ZFS snapshot.

This frees any space uniquely held by the snapshot.

Examples:
  hospitus jail snapshot delete myjail old-snapshot`,
		Args: cobra.ExactArgs(2),
		RunE: runSnapshotDelete,
	}

	return cmd
}

func runSnapshotDelete(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	jailName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, jailName, "jail"); err != nil {
		return err
	}
	snapshotName := args[1]

	fmt.Fprintf(cmd.OutOrStdout(), "Deleting snapshot %s@%s...\n", jailName, snapshotName)

	if err := cmdutil.APIClient.DeleteSnapshot(ctx, jailName, snapshotName); err != nil {
		return fmt.Errorf("failed to delete snapshot: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Snapshot deleted: %s@%s\n", jailName, snapshotName)
	return nil
}

func newSnapshotRestoreCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <jail-name> <snapshot-name>",
		Short: "Restore a jail from a snapshot",
		Long: `Restore a jail's filesystem to a previous snapshot state.

The jail must be stopped first: restoring a running jail is refused.

WARNING: This will:
  - Rollback the filesystem to the snapshot
  - Discard any changes made after the snapshot

Examples:
  # Restore after a failed upgrade
  hospitus jail stop myjail
  hospitus jail snapshot restore myjail before-upgrade
  hospitus jail start myjail`,
		Args: cobra.ExactArgs(2),
		RunE: runSnapshotRestore,
	}

	return cmd
}

func runSnapshotRestore(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	jailName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, jailName, "jail"); err != nil {
		return err
	}
	snapshotName := args[1]

	fmt.Fprintf(cmd.OutOrStdout(), "Restoring jail %s from snapshot %s...\n", jailName, snapshotName)
	fmt.Fprintf(cmd.OutOrStdout(), "WARNING: This will discard all changes made after the snapshot.\n")

	if err := cmdutil.APIClient.RestoreSnapshot(ctx, jailName, snapshotName); err != nil {
		return fmt.Errorf("failed to restore snapshot: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Snapshot restored: %s rolled back to %s\n", jailName, snapshotName)
	return nil
}
