package bhyve

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newSnapshotCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Manage bhyve VM snapshots",
		Long: `Manage ZFS snapshots for bhyve virtual machines.

Snapshots capture a point-in-time copy of the VM's ZFS dataset. The VM must
be stopped before creating or restoring snapshots.

Examples:
  hospitus bhyve snapshot create myvm before-upgrade
  hospitus bhyve snapshot list myvm
  hospitus bhyve snapshot restore myvm before-upgrade
  hospitus bhyve snapshot delete myvm old-snapshot`,
	}

	cmd.AddCommand(newBhyveSnapshotCreateCommand())
	cmd.AddCommand(newBhyveSnapshotListCommand())
	cmd.AddCommand(newBhyveSnapshotDeleteCommand())
	cmd.AddCommand(newBhyveSnapshotRestoreCommand())

	return cmd
}

func newBhyveSnapshotCreateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "create <vm-name> <snapshot-name>",
		Short: "Create a ZFS snapshot of a bhyve VM",
		Long: `Create a ZFS snapshot of the bhyve VM dataset. The VM must be stopped.

Examples:
  hospitus bhyve snapshot create myvm before-upgrade`,
		Args: cobra.ExactArgs(2),
		RunE: runBhyveSnapshotCreate,
	}
}

func runBhyveSnapshotCreate(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	vmName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, vmName, "bhyve"); err != nil {
		return err
	}
	snapshotName := args[1]

	fmt.Fprintf(cmd.OutOrStdout(), "Creating snapshot %s@%s...\n", vmName, snapshotName)

	if err := cmdutil.APIClient.CreateSnapshot(ctx, vmName, snapshotName); err != nil {
		return fmt.Errorf("failed to create snapshot: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Snapshot created: %s@%s\n", vmName, snapshotName)
	return nil
}

func newBhyveSnapshotListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <vm-name>",
		Short: "List snapshots of a bhyve VM",
		Long: `List all ZFS snapshots for a bhyve VM.

Examples:
  hospitus bhyve snapshot list myvm
  hospitus bhyve snapshot list myvm --output json`,
		Args: cobra.ExactArgs(1),
		RunE: runBhyveSnapshotList,
	}

	cmdutil.AddOutputFlag(cmd)
	return cmd
}

func runBhyveSnapshotList(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	vmName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, vmName, "bhyve"); err != nil {
		return err
	}
	outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput)

	snapshots, err := cmdutil.APIClient.ListSnapshots(ctx, vmName)
	if err != nil {
		return fmt.Errorf("failed to list snapshots: %w", err)
	}

	if outputFmt == "json" {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(snapshots)
	}

	if len(snapshots) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No snapshots found for VM %s\n", vmName)
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCREATED\tSIZE")
	for _, s := range snapshots {
		size := "-"
		if s.SizeMB > 0 {
			size = fmt.Sprintf("%d MB", s.SizeMB)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, s.CreatedAt.Format("2006-01-02 15:04"), size)
	}
	return w.Flush()
}

func newBhyveSnapshotDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <vm-name> <snapshot-name>",
		Short: "Delete a bhyve VM snapshot",
		Long: `Delete a ZFS snapshot from a bhyve VM.

Examples:
  hospitus bhyve snapshot delete myvm old-snapshot`,
		Args: cobra.ExactArgs(2),
		RunE: runBhyveSnapshotDelete,
	}

	cmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func runBhyveSnapshotDelete(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	vmName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, vmName, "bhyve"); err != nil {
		return err
	}
	snapshotName := args[1]
	yes, _ := cmd.Flags().GetBool("yes")

	if !yes {
		fmt.Fprintf(cmd.OutOrStdout(), "Delete snapshot %s@%s? [y/N] ", vmName, snapshotName)
		var confirm string
		fmt.Scanln(&confirm) //nolint:errcheck
		if confirm != "y" && confirm != "Y" {
			fmt.Fprintln(cmd.OutOrStdout(), "Canceled.")
			return nil
		}
	}

	if err := cmdutil.APIClient.DeleteSnapshot(ctx, vmName, snapshotName); err != nil {
		return fmt.Errorf("failed to delete snapshot: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Snapshot %s@%s deleted.\n", vmName, snapshotName)
	return nil
}

func newBhyveSnapshotRestoreCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <vm-name> <snapshot-name>",
		Short: "Restore a bhyve VM to a snapshot",
		Long: `Restore the VM to a previous ZFS snapshot. The VM must be stopped.

WARNING: This rolls back the entire ZFS dataset. All changes since the
snapshot was taken will be lost.

Examples:
  hospitus bhyve snapshot restore myvm before-upgrade`,
		Args: cobra.ExactArgs(2),
		RunE: runBhyveSnapshotRestore,
	}

	cmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func runBhyveSnapshotRestore(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	vmName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, vmName, "bhyve"); err != nil {
		return err
	}
	snapshotName := args[1]
	yes, _ := cmd.Flags().GetBool("yes")

	if !yes {
		fmt.Fprintf(cmd.OutOrStdout(), "Restore %s to snapshot %s? Current state will be lost. [y/N] ", vmName, snapshotName)
		var confirm string
		fmt.Scanln(&confirm) //nolint:errcheck
		if confirm != "y" && confirm != "Y" {
			fmt.Fprintln(cmd.OutOrStdout(), "Canceled.")
			return nil
		}
	}

	if err := cmdutil.APIClient.RestoreSnapshot(ctx, vmName, snapshotName); err != nil {
		return fmt.Errorf("failed to restore snapshot: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "VM %s restored to snapshot %s.\n", vmName, snapshotName)
	return nil
}
