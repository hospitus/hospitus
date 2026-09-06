package jail

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
)

func newExportCommand() *cobra.Command {
	var (
		compress         bool
		stopInstance     bool
		includeSnapshots bool
	)

	cmd := &cobra.Command{
		Use:   "export <jail-name> <export-path>",
		Short: "Export a jail to a tarball",
		Long: `Export a jail to a tarball for backup or migration.

The export includes:
  - Jail configuration
  - Jail filesystem (as tar or ZFS stream)
  - Optionally: all snapshots

For ZFS-based jails, uses efficient ZFS send/receive.
For non-ZFS jails, creates a tarball of the filesystem.

Examples:
  # Export a jail to a file
  hospitus jail export myjail /backup/myjail.tar.gz

  # Export with compression
  hospitus jail export myjail /backup/myjail.tar.gz --compress

  # Export with all snapshots
  hospitus jail export myjail /backup/myjail.tar.gz --include-snapshots

  # Stop jail before export (for consistency)
  hospitus jail export myjail /backup/myjail.tar.gz --stop`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExport(cmd, args, compress, stopInstance, includeSnapshots)
		},
	}

	cmd.Flags().BoolVar(&compress, "compress", false, "Compress the export (gzip)")
	cmd.Flags().BoolVar(&stopInstance, "stop", false, "Stop the jail before export")
	cmd.Flags().BoolVar(&includeSnapshots, "include-snapshots", false, "Include all snapshots in export")

	return cmd
}

func runExport(cmd *cobra.Command, args []string, compress, stopInstance, includeSnapshots bool) error {
	ctx := cmd.Context()
	jailName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, jailName, "jail"); err != nil {
		return err
	}
	exportPath := args[1]

	// Make path absolute
	absPath, err := filepath.Abs(exportPath)
	if err != nil {
		return fmt.Errorf("invalid export path: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Exporting jail %s to %s...\n", jailName, absPath)

	opts := client.ExportOptions{
		ExportPath:       absPath,
		Compress:         compress,
		StopInstance:     stopInstance,
		IncludeSnapshots: includeSnapshots,
	}

	result, err := cmdutil.APIClient.ExportInstance(ctx, jailName, opts)
	if err != nil {
		return fmt.Errorf("failed to export jail: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Export completed: %s\n", result.Message)

	return nil
}
