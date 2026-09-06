package qemu

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
)

func newSnapshotCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Manage QEMU VM snapshots",
		Long: `Manage disk snapshots for QEMU virtual machines.

Snapshots use qemu-img to create point-in-time copies of VM disk images.
The VM must be stopped before creating or restoring snapshots.

Examples:
  hospitus qemu snapshot create myvm before-upgrade
  hospitus qemu snapshot list myvm
  hospitus qemu snapshot restore myvm before-upgrade
  hospitus qemu snapshot delete myvm old-snapshot`,
	}

	cmd.AddCommand(newQEMUSnapshotCreateCommand())
	cmd.AddCommand(newQEMUSnapshotListCommand())
	cmd.AddCommand(newQEMUSnapshotDeleteCommand())
	cmd.AddCommand(newQEMUSnapshotRestoreCommand())

	return cmd
}

func newQEMUSnapshotCreateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "create <vm-name> <snapshot-name>",
		Short: "Create a snapshot of a QEMU VM",
		Long: `Create a disk snapshot of the QEMU VM. The VM must be stopped.

Examples:
  hospitus qemu snapshot create myvm before-upgrade`,
		Args: cobra.ExactArgs(2),
		RunE: runQEMUSnapshotCreate,
	}
}

func runQEMUSnapshotCreate(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	vmName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, vmName, "qemu"); err != nil {
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

func newQEMUSnapshotListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <vm-name>",
		Short: "List snapshots of a QEMU VM",
		Long: `List all disk snapshots for a QEMU VM.

Examples:
  hospitus qemu snapshot list myvm
  hospitus qemu snapshot list myvm --output json`,
		Args: cobra.ExactArgs(1),
		RunE: runQEMUSnapshotList,
	}

	cmdutil.AddOutputFlag(cmd)
	return cmd
}

func runQEMUSnapshotList(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	vmName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, vmName, "qemu"); err != nil {
		return err
	}
	outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput)

	snapshots, err := cmdutil.APIClient.ListSnapshots(ctx, vmName)
	if err != nil {
		return fmt.Errorf("failed to list snapshots: %w", err)
	}

	if outputFmt == "json" {
		// A nil slice encodes as "null", which a caller parsing the output has
		// to special-case; an empty list is what "no snapshots" means.
		if snapshots == nil {
			snapshots = []client.SnapshotInfo{}
		}
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

func newQEMUSnapshotDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <vm-name> <snapshot-name>",
		Short: "Delete a QEMU VM snapshot",
		Long: `Delete a disk snapshot from a QEMU VM.

Examples:
  hospitus qemu snapshot delete myvm old-snapshot`,
		Args: cobra.ExactArgs(2),
		RunE: runQEMUSnapshotDelete,
	}

	cmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func runQEMUSnapshotDelete(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	vmName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, vmName, "qemu"); err != nil {
		return err
	}
	snapshotName := args[1]
	yes, _ := cmd.Flags().GetBool("yes")

	if !yes {
		fmt.Fprintf(cmd.OutOrStdout(), "Delete snapshot %s@%s? [y/N] ", vmName, snapshotName)
		// A whole line from the command's own input: fmt.Scanln reads from
		// os.Stdin regardless of what the caller set, stops at the first
		// space, and its error was discarded — so an empty line left confirm
		// at "" and fell through to the cancel branch by luck rather than by
		// decision.
		reader := bufio.NewReader(cmd.InOrStdin())
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("failed to read confirmation: %w", err)
		}
		confirm := strings.ToLower(strings.TrimSpace(line))
		if confirm != "y" && confirm != "yes" {
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

func newQEMUSnapshotRestoreCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <vm-name> <snapshot-name>",
		Short: "Restore a QEMU VM to a snapshot",
		Long: `Restore the VM disk to a previous snapshot state. The VM must be stopped.

WARNING: This overwrites the current disk state. All changes since the
snapshot was taken will be lost.

Examples:
  hospitus qemu snapshot restore myvm before-upgrade`,
		Args: cobra.ExactArgs(2),
		RunE: runQEMUSnapshotRestore,
	}

	cmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func runQEMUSnapshotRestore(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	vmName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, vmName, "qemu"); err != nil {
		return err
	}
	snapshotName := args[1]
	yes, _ := cmd.Flags().GetBool("yes")

	if !yes {
		fmt.Fprintf(cmd.OutOrStdout(), "Restore %s to snapshot %s? Current state will be lost. [y/N] ", vmName, snapshotName)
		// A whole line from the command's own input: fmt.Scanln reads from
		// os.Stdin regardless of what the caller set, stops at the first
		// space, and its error was discarded — so an empty line left confirm
		// at "" and fell through to the cancel branch by luck rather than by
		// decision.
		reader := bufio.NewReader(cmd.InOrStdin())
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("failed to read confirmation: %w", err)
		}
		confirm := strings.ToLower(strings.TrimSpace(line))
		if confirm != "y" && confirm != "yes" {
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
