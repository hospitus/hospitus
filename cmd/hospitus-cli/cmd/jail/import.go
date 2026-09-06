package jail

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
)

func newImportCommand() *cobra.Command {
	var (
		newName          string
		resetMAC         bool
		newIP            string
		startAfterImport bool
	)

	cmd := &cobra.Command{
		Use:   "import <import-path>",
		Short: "Import a jail from a tarball",
		Long: `Import a jail from a previously exported tarball.

The import:
  - Extracts the jail filesystem
  - Restores jail configuration
  - Optionally renames the jail
  - Optionally assigns a new IP address
  - Optionally starts the jail after import

For ZFS-based exports, uses efficient ZFS receive.
For tar-based exports, extracts to a new ZFS dataset.

Examples:
  # Import a jail
  hospitus jail import /backup/myjail.tar.gz

  # Import with a new name
  hospitus jail import /backup/myjail.tar.gz --name newjail

  # Import and start immediately
  hospitus jail import /backup/myjail.tar.gz --start

  # Import with new network configuration (MAC addresses)
  hospitus jail import /backup/myjail.tar.gz --reset-mac

  # Import with a new IP address (avoids conflicts with existing jails)
  hospitus jail import /backup/myjail.tar.gz --name newjail --ip 10.0.0.100/24

  # Import with new name, MAC, and IP for complete network isolation
  hospitus jail import /backup/myjail.tar.gz --name staging --reset-mac --ip 10.0.0.200/24`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(cmd, args, newName, resetMAC, newIP, startAfterImport)
		},
	}

	cmd.Flags().StringVar(&newName, "name", "", "New name for the imported jail")
	cmd.Flags().BoolVar(&resetMAC, "reset-mac", false, "Reset MAC addresses for network interfaces")
	cmd.Flags().StringVar(&newIP, "ip", "", "New IP address for the jail (e.g., 10.0.0.100/24)")
	cmd.Flags().BoolVar(&startAfterImport, "start", false, "Start the jail after import")

	return cmd
}

func runImport(cmd *cobra.Command, args []string, newName string, resetMAC bool, newIP string, startAfterImport bool) error {
	ctx := cmd.Context()
	importPath := args[0]

	// Make path absolute
	absPath, err := filepath.Abs(importPath)
	if err != nil {
		return fmt.Errorf("invalid import path: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Importing jail from %s...\n", absPath)

	opts := client.ImportOptions{
		ImportPath:       absPath,
		Provider:         "jail",
		NewName:          newName,
		ResetMAC:         resetMAC,
		NewIP:            newIP,
		StartAfterImport: startAfterImport,
	}

	result, err := cmdutil.APIClient.ImportInstance(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to import jail: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Import completed: %s\n", result.Message)
	if result.Instance != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Jail name: %s\n", result.Instance.Name)
	}

	return nil
}
