package bhyve

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
)

func newExportVMCommand() *cobra.Command {
	var (
		compress         bool
		stopVM           bool
		includeSnapshots bool
	)

	cmd := &cobra.Command{
		Use:   "export <vm-name> <export-path>",
		Short: "Export a bhyve VM to a tarball",
		Long: `Export a bhyve VM to a tarball for backup or migration.

The export includes:
  - VM configuration (vm.conf, uefi_vars.fd)
  - All ZFS disk volumes (ZVOLs) or image files
  - Optionally: all ZFS snapshots

For ZFS-backed disks, uses efficient ZFS send/receive.
For raw image files, creates a tar archive.

Examples:
  # Export a VM to a file
  hospitus bhyve export myvm /backup/myvm.tar.gz

  # Export with gzip compression
  hospitus bhyve export myvm /backup/myvm.tar.gz --compress

  # Include all ZFS snapshots
  hospitus bhyve export myvm /backup/myvm.tar.gz --include-snapshots

  # Stop VM before export for a consistent snapshot
  hospitus bhyve export myvm /backup/myvm.tar.gz --stop`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExportVM(cmd, args, compress, stopVM, includeSnapshots)
		},
	}

	cmd.Flags().BoolVar(&compress, "compress", false, "Compress the export (gzip)")
	cmd.Flags().BoolVar(&stopVM, "stop", false, "Stop the VM before export (ensures consistency)")
	cmd.Flags().BoolVar(&includeSnapshots, "include-snapshots", false, "Include all ZFS snapshots in export")

	return cmd
}

func runExportVM(cmd *cobra.Command, args []string, compress, stopVM, includeSnapshots bool) error {
	ctx := cmd.Context()
	// Resolved, not merely checked: RequireInstanceOf confirms the argument
	// names a bhyve VM, and the export then went out under the raw argument —
	// which may be a name where the daemon wants an id.
	vmName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.APIClient, args[0], "bhyve")
	if err != nil {
		return fmt.Errorf("VM %q not found: %w", args[0], err)
	}
	exportPath := args[1]

	absPath, err := filepath.Abs(exportPath)
	if err != nil {
		return fmt.Errorf("invalid export path: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Exporting VM %q to %s...\n", vmName, absPath)

	opts := client.ExportOptions{
		ExportPath:       absPath,
		Compress:         compress,
		StopInstance:     stopVM,
		IncludeSnapshots: includeSnapshots,
	}

	result, err := cmdutil.APIClient.ExportInstance(ctx, vmName, opts)
	if err != nil {
		return fmt.Errorf("failed to export VM: %w", err)
	}

	if result == nil {
		return fmt.Errorf("the daemon reported no export result for %s", vmName)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Export completed: %s\n", result.Message)
	return nil
}

func newImportVMCommand() *cobra.Command {
	var (
		newName          string
		resetMAC         bool
		newIP            string
		startAfterImport bool
	)

	cmd := &cobra.Command{
		Use:   "import <import-path>",
		Short: "Import a bhyve VM from a tarball",
		Long: `Import a bhyve VM from a previously exported tarball.

The import:
  - Extracts disk volumes (ZFS receive for ZVOL exports, tar for images)
  - Restores VM configuration
  - Optionally renames the VM
  - Optionally assigns a new IP address
  - Optionally starts the VM after import

Examples:
  # Import a VM
  hospitus bhyve import /backup/myvm.tar.gz

  # Import with a new name
  hospitus bhyve import /backup/myvm.tar.gz --name cloned-vm

  # Import and start immediately
  hospitus bhyve import /backup/myvm.tar.gz --start

  # Import with new network configuration
  hospitus bhyve import /backup/myvm.tar.gz --reset-mac --ip 192.168.1.200/24`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImportVM(cmd, args, newName, resetMAC, newIP, startAfterImport)
		},
	}

	cmd.Flags().StringVar(&newName, "name", "", "New name for the imported VM")
	cmd.Flags().BoolVar(&resetMAC, "reset-mac", false, "Reset MAC addresses for network interfaces")
	cmd.Flags().StringVar(&newIP, "ip", "", "New IP address for the VM (e.g., 192.168.1.200/24)")
	cmd.Flags().BoolVar(&startAfterImport, "start", false, "Start the VM after import")

	return cmd
}

func runImportVM(cmd *cobra.Command, args []string, newName string, resetMAC bool, newIP string, startAfterImport bool) error {
	ctx := cmd.Context()
	importPath := args[0]

	absPath, err := filepath.Abs(importPath)
	if err != nil {
		return fmt.Errorf("invalid import path: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Importing VM from %s...\n", absPath)

	opts := client.ImportOptions{
		ImportPath:       absPath,
		Provider:         "bhyve",
		NewName:          newName,
		ResetMAC:         resetMAC,
		NewIP:            newIP,
		StartAfterImport: startAfterImport,
	}

	result, err := cmdutil.APIClient.ImportInstance(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to import VM: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Import completed: %s\n", result.Message)
	if result.Instance != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "VM name: %s\n", result.Instance.Name)
	}
	return nil
}
