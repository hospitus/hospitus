// Package bhyve implements bhyve-specific CLI commands
package bhyve

import (
	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

// NewBhyveCommand creates the bhyve parent command
func NewBhyveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bhyve",
		Short: "Manage bhyve virtual machines",
		Long: `Manage bhyve virtual machines - FreeBSD native hypervisor.

bhyve is the FreeBSD native hypervisor providing hardware-accelerated
virtualization with near-native performance.`,
	}

	// Add subcommands
	cmd.AddCommand(newCreateCommand())
	cmd.AddCommand(newStartCommand())
	cmd.AddCommand(newStopCommand())
	cmd.AddCommand(newRestartCommand())
	cmd.AddCommand(newDestroyCommand())
	cmd.AddCommand(newListCommand())
	cmd.AddCommand(newInfoCommand())
	cmd.AddCommand(newConsoleCommand())
	cmd.AddCommand(newVNCCommand())
	cmd.AddCommand(newStatsCommand())
	cmd.AddCommand(newExposeCommand())
	cmd.AddCommand(cmdutil.NewAutostartCommand("bhyve", "VM", "VMs"))
	cmd.AddCommand(newMediaCommand())
	cmd.AddCommand(newBootOrderCommand())
	cmd.AddCommand(newWindowsPrepCommand())
	cmd.AddCommand(newCheckpointCommand())
	cmd.AddCommand(newPauseCommand())
	cmd.AddCommand(newResumeCommand())
	cmd.AddCommand(newRenameVMCommand())
	cmd.AddCommand(newSnapshotCommand())
	cmd.AddCommand(newExportVMCommand())
	cmd.AddCommand(newImportVMCommand())
	cmd.AddCommand(newCloneVMCommand())

	return cmd
}
