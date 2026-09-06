package podman

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newSnapshotCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Manage Podman container snapshots",
		Long: `Manage snapshots (image commits) for Podman containers.

Snapshots are implemented using 'podman commit', which creates a new image
from the container's current filesystem state. These can be used to capture
the container at a point in time or to create derivative images.

Note: The container can be running or stopped when creating snapshots.
Restoring a snapshot replaces the running container with a new one
based on the snapshot image.

Examples:
  hospitus podman snapshot create mycontainer before-upgrade
  hospitus podman snapshot list mycontainer
  hospitus podman snapshot restore mycontainer before-upgrade
  hospitus podman snapshot delete mycontainer old-snapshot`,
	}

	cmd.AddCommand(newPodmanSnapshotCreateCommand())
	cmd.AddCommand(newPodmanSnapshotListCommand())
	cmd.AddCommand(newPodmanSnapshotDeleteCommand())
	cmd.AddCommand(newPodmanSnapshotRestoreCommand())

	return cmd
}

func newPodmanSnapshotCreateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "create <container-name> <snapshot-name>",
		Short: "Create a snapshot of a Podman container",
		Long: `Create a snapshot by committing the container's current state to an image.

Examples:
  hospitus podman snapshot create mycontainer daily-backup`,
		Args: cobra.ExactArgs(2),
		RunE: runPodmanSnapshotCreate,
	}
}

func runPodmanSnapshotCreate(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	containerName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, containerName, "podman"); err != nil {
		return err
	}
	snapshotName := args[1]

	fmt.Fprintf(cmd.OutOrStdout(), "Creating snapshot %s@%s...\n", containerName, snapshotName)

	if err := cmdutil.APIClient.CreateSnapshot(ctx, containerName, snapshotName); err != nil {
		return fmt.Errorf("failed to create snapshot: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Snapshot created: %s@%s\n", containerName, snapshotName)
	return nil
}

func newPodmanSnapshotListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <container-name>",
		Short: "List snapshots of a Podman container",
		Long: `List all snapshots (committed images) for a Podman container.

Examples:
  hospitus podman snapshot list mycontainer
  hospitus podman snapshot list mycontainer --output json`,
		Args: cobra.ExactArgs(1),
		RunE: runPodmanSnapshotList,
	}

	cmdutil.AddOutputFlag(cmd)
	return cmd
}

func runPodmanSnapshotList(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	containerName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, containerName, "podman"); err != nil {
		return err
	}
	outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput)

	snapshots, err := cmdutil.APIClient.ListSnapshots(ctx, containerName)
	if err != nil {
		return fmt.Errorf("failed to list snapshots: %w", err)
	}

	if outputFmt == "json" {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(snapshots)
	}

	if len(snapshots) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No snapshots found for container %s\n", containerName)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCREATED")
	for _, s := range snapshots {
		fmt.Fprintf(w, "%s\t%s\n", s.Name, s.CreatedAt.Format("2006-01-02 15:04"))
	}
	return w.Flush()
}

func newPodmanSnapshotDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <container-name> <snapshot-name>",
		Short: "Delete a Podman container snapshot",
		Long: `Delete a snapshot image for a Podman container.

Examples:
  hospitus podman snapshot delete mycontainer old-snapshot`,
		Args: cobra.ExactArgs(2),
		RunE: runPodmanSnapshotDelete,
	}

	cmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func runPodmanSnapshotDelete(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	containerName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, containerName, "podman"); err != nil {
		return err
	}
	snapshotName := args[1]
	yes, _ := cmd.Flags().GetBool("yes")

	if !yes {
		fmt.Fprintf(cmd.OutOrStdout(), "Delete snapshot %s@%s? [y/N] ", containerName, snapshotName)
		var confirm string
		fmt.Scanln(&confirm) //nolint:errcheck
		if confirm != "y" && confirm != "Y" {
			fmt.Fprintln(cmd.OutOrStdout(), "Canceled.")
			return nil
		}
	}

	if err := cmdutil.APIClient.DeleteSnapshot(ctx, containerName, snapshotName); err != nil {
		return fmt.Errorf("failed to delete snapshot: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Snapshot %s@%s deleted.\n", containerName, snapshotName)
	return nil
}

func newPodmanSnapshotRestoreCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <container-name> <snapshot-name>",
		Short: "Restore a Podman container from a snapshot",
		Long: `Restore the container to a previously committed snapshot.

The container will be stopped, removed, and recreated from the snapshot image.

Examples:
  hospitus podman snapshot restore mycontainer before-upgrade`,
		Args: cobra.ExactArgs(2),
		RunE: runPodmanSnapshotRestore,
	}

	cmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func runPodmanSnapshotRestore(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	containerName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, containerName, "podman"); err != nil {
		return err
	}
	snapshotName := args[1]
	yes, _ := cmd.Flags().GetBool("yes")

	if !yes {
		fmt.Fprintf(cmd.OutOrStdout(), "Restore %s to snapshot %s? [y/N] ", containerName, snapshotName)
		var confirm string
		fmt.Scanln(&confirm) //nolint:errcheck
		if confirm != "y" && confirm != "Y" {
			fmt.Fprintln(cmd.OutOrStdout(), "Canceled.")
			return nil
		}
	}

	if err := cmdutil.APIClient.RestoreSnapshot(ctx, containerName, snapshotName); err != nil {
		return fmt.Errorf("failed to restore snapshot: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Container %s restored to snapshot %s.\n", containerName, snapshotName)
	return nil
}
