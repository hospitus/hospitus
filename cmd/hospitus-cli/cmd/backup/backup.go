package backup

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
)

// NewBackupCommand returns the root backup command.
func NewBackupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Manage instance backups",
		Long: `Create, list, restore, and delete backups for any instance (jail, VM, container).

Backups are ZFS snapshots. A full or incremental backup sends a stream to a
destination set with "hospitus backup config", optionally compressed. There is no
encryption: use ZFS native encryption on the dataset instead.

Examples:
  hospitus backup list
  hospitus backup list --instance myjail
  hospitus backup create myjail
  hospitus backup restore <backup-id>
  hospitus backup verify <backup-id>
  hospitus backup delete <backup-id>`,
	}

	cmd.AddCommand(newBackupListCommand())
	cmd.AddCommand(newBackupCreateCommand())
	cmd.AddCommand(newBackupConfigCommand())
	cmd.AddCommand(newBackupDeleteCommand())
	cmd.AddCommand(newBackupRestoreCommand())
	cmd.AddCommand(newBackupVerifyCommand())

	return cmd
}

func newBackupListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List backups",
		Long: `List backups. Without --instance, lists all backups across all instances.

Examples:
  hospitus backup list
  hospitus backup list --instance myjail
  hospitus backup list --output json`,
		RunE: runBackupList,
	}

	cmd.Flags().String("instance", "", "Filter backups by instance name or ID")
	cmdutil.AddOutputFlag(cmd)
	return cmd
}

func runBackupList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	instanceID, _ := cmd.Flags().GetString("instance")
	outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput)

	backups, err := cmdutil.APIClient.ListBackups(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to list backups: %w", err)
	}

	if outputFmt == "json" {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(backups)
	}

	if len(backups) == 0 {
		if instanceID != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "No backups found for instance %s\n", instanceID)
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "No backups found")
		}
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tINSTANCE\tTYPE\tSTATUS\tSIZE\tCREATED")
	for i := range backups {
		b := &backups[i]
		size := "-"
		if b.SizeBytes > 0 {
			size = fmt.Sprintf("%d MB", b.SizeBytes/1024/1024)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			b.ID, b.InstanceID, b.Type, b.Status, size,
			b.CreatedAt.Format("2006-01-02 15:04"))
	}
	return w.Flush()
}

func newBackupCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <instance>",
		Short: "Create a backup of an instance",
		Long: `Create a backup of an instance. The default backup type is 'snapshot'.

Examples:
  hospitus backup create myjail
  hospitus backup create myjail --type full`,
		Args: cobra.ExactArgs(1),
		RunE: runBackupCreate,
	}

	cmd.Flags().String("type", "snapshot", "Backup type: snapshot, full, incremental")
	return cmd
}

func runBackupCreate(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	instanceName := args[0]
	backupType, _ := cmd.Flags().GetString("type")

	instanceID, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.APIClient, instanceName, "")
	if err != nil {
		return fmt.Errorf("instance not found: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Creating %s backup for %s...\n", backupType, instanceName)

	backup, err := cmdutil.APIClient.CreateBackup(ctx, instanceID, backupType)
	if err != nil {
		return fmt.Errorf("failed to create backup: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Backup created: %s (status: %s)\n", backup.ID, backup.Status)
	return nil
}

func newBackupConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config <instance>",
		Short: "Read or set an instance's backup configuration",
		Long: `Read or set an instance's backup configuration.

A full or incremental backup needs a destination: a local directory the daemon
writes the stream into, or host:path for a remote pool reached over ssh. A
snapshot needs none.

Compression applies to a local destination, where hospitus writes and reads the
file back: gzip, zstd or lz4. Over ssh the receiving end runs zfs receive with
no shell, so set compression on the receiving pool instead.

Examples:
  hospitus backup config myjail
  hospitus backup config myjail --destination /backup/hospitus --compression zstd
  hospitus backup config myjail --destination backup@nas:tank/hospitus --schedule daily
  hospitus backup config myjail --schedule daily --keep-last 7`,
		Args: cobra.ExactArgs(1),
		RunE: runBackupConfig,
	}

	cmd.Flags().String("destination", "", "Where full and incremental backups are written: a directory, or host:path")
	cmd.Flags().String("schedule", "", "Automatic backups: hourly, daily, weekly, monthly, or a duration like 6h")
	cmd.Flags().String("compression", "", "Compress a local backup stream: gzip, zstd, lz4, or none")
	cmd.Flags().Bool("disable", false, "Turn scheduled backups off")
	cmd.Flags().Int("keep-last", 0, "Keep this many most recent backups")
	cmd.Flags().Int("keep-daily", 0, "Keep this many daily backups")
	cmd.Flags().Int("keep-weekly", 0, "Keep this many weekly backups")
	cmd.Flags().Int("keep-monthly", 0, "Keep this many monthly backups")
	return cmd
}

func runBackupConfig(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	instanceID, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.APIClient, args[0], "")
	if err != nil {
		return fmt.Errorf("instance not found: %w", err)
	}

	// With no flag, report what is configured rather than overwrite it with
	// empty values.
	if !anyFlagChanged(cmd, "destination", "schedule", "compression", "disable",
		"keep-last", "keep-daily", "keep-weekly", "keep-monthly") {
		config, err := cmdutil.APIClient.GetBackupConfig(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("failed to read backup configuration: %w", err)
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(config)
	}

	var req client.BackupConfigRequest
	req.Destination, _ = cmd.Flags().GetString("destination")
	req.Schedule, _ = cmd.Flags().GetString("schedule")
	req.Compression, _ = cmd.Flags().GetString("compression")
	req.Retention.KeepLast, _ = cmd.Flags().GetInt("keep-last")
	req.Retention.KeepDaily, _ = cmd.Flags().GetInt("keep-daily")
	req.Retention.KeepWeekly, _ = cmd.Flags().GetInt("keep-weekly")
	req.Retention.KeepMonthly, _ = cmd.Flags().GetInt("keep-monthly")

	disable, _ := cmd.Flags().GetBool("disable")
	req.Enabled = !disable && req.Schedule != ""

	if err := cmdutil.APIClient.SetBackupConfig(ctx, instanceID, req); err != nil {
		return fmt.Errorf("failed to set backup configuration: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Backup configuration updated for %s\n", args[0])
	return nil
}

// anyFlagChanged reports whether the caller set any of the named flags.
func anyFlagChanged(cmd *cobra.Command, names ...string) bool {
	for _, name := range names {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func newBackupDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <backup-id>",
		Short: "Delete a backup",
		Long: `Delete a backup by its ID.

Examples:
  hospitus backup delete abc12345`,
		Args: cobra.ExactArgs(1),
		RunE: runBackupDelete,
	}

	cmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func runBackupDelete(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	backupID := args[0]
	yes, _ := cmd.Flags().GetBool("yes")

	if !yes {
		fmt.Fprintf(cmd.OutOrStdout(), "Delete backup %s? [y/N] ", backupID)
		var confirm string
		fmt.Scanln(&confirm) //nolint:errcheck
		if confirm != "y" && confirm != "Y" {
			fmt.Fprintln(cmd.OutOrStdout(), "Canceled.")
			return nil
		}
	}

	if err := cmdutil.APIClient.DeleteBackup(ctx, backupID); err != nil {
		return fmt.Errorf("failed to delete backup: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Backup %s deleted.\n", backupID)
	return nil
}

func newBackupRestoreCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <backup-id>",
		Short: "Restore an instance from a backup",
		Long: `Restore an instance to the state captured in the specified backup.

WARNING: This overwrites the current instance state.

Examples:
  hospitus backup restore abc12345
  hospitus backup restore abc12345 --target other-instance`,
		Args: cobra.ExactArgs(1),
		RunE: runBackupRestore,
	}

	cmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	cmd.Flags().String("target", "", "Restore to a different instance (optional)")
	return cmd
}

// restorePrompt asks before a restore, naming the instance whose contents go
// away.
func restorePrompt(backupID, target string) string {
	if target != "" {
		return fmt.Sprintf("Restore backup %s into %s? Anything already there will be lost. [y/N] ", backupID, target)
	}
	return fmt.Sprintf("Restore backup %s? Current state will be lost. [y/N] ", backupID)
}

func runBackupRestore(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	backupID := args[0]
	yes, _ := cmd.Flags().GetBool("yes")
	target, _ := cmd.Flags().GetString("target")

	if !yes {
		// Name what is actually at risk: with --target the source is left
		// alone, and warning about "current state" there reads as a threat to
		// the wrong instance.
		fmt.Fprint(cmd.OutOrStdout(), restorePrompt(backupID, target))
		var confirm string
		fmt.Scanln(&confirm) //nolint:errcheck
		if confirm != "y" && confirm != "Y" {
			fmt.Fprintln(cmd.OutOrStdout(), "Canceled.")
			return nil
		}
	}

	if err := cmdutil.APIClient.RestoreBackup(ctx, backupID, target); err != nil {
		return fmt.Errorf("failed to restore backup: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Backup %s restored successfully.\n", backupID)
	return nil
}

func newBackupVerifyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "verify <backup-id>",
		Short: "Verify backup integrity",
		Long: `Run an integrity check on a stored backup to ensure it is not corrupted.

Examples:
  hospitus backup verify abc12345`,
		Args: cobra.ExactArgs(1),
		RunE: runBackupVerify,
	}
}

func runBackupVerify(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	backupID := args[0]

	fmt.Fprintf(cmd.OutOrStdout(), "Verifying backup %s...\n", backupID)

	if err := cmdutil.APIClient.VerifyBackup(ctx, backupID); err != nil {
		return fmt.Errorf("verification failed: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Backup %s verified OK.\n", backupID)
	return nil
}
