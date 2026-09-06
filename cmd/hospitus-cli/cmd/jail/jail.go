// Package jail implements jail-specific CLI commands
package jail

import (
	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

// NewJailCommand creates the jail parent command
func NewJailCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jail",
		Short: "Manage FreeBSD jails",
		Long: `Manage FreeBSD jails - lightweight OS-level virtualization.

Jails provide a secure, isolated environment for running processes with their
own files, processes, and network configuration.`,
	}

	// Add subcommands
	cmd.AddCommand(newCreateCommand())
	cmd.AddCommand(newStartCommand())
	cmd.AddCommand(newStopCommand())
	cmd.AddCommand(newRestartCommand())
	cmd.AddCommand(newDestroyCommand())
	cmd.AddCommand(newListCommand())
	cmd.AddCommand(newInfoCommand())
	cmd.AddCommand(newSetCommand())
	cmd.AddCommand(newExecCommand())
	cmd.AddCommand(newConsoleCommand())
	cmd.AddCommand(newTmuxCommand())
	cmd.AddCommand(newExposeCommand())
	cmd.AddCommand(newExportCommand())
	cmd.AddCommand(newImportCommand())
	cmd.AddCommand(newSnapshotCommand())
	cmd.AddCommand(newCloneCommand())
	cmd.AddCommand(newStatsCommand())
	cmd.AddCommand(newHealthCommand())
	cmd.AddCommand(newServiceCommand())
	cmd.AddCommand(newVolumeCommand())
	cmd.AddCommand(newNetworkCommand())
	cmd.AddCommand(newRenameCommand())
	cmd.AddCommand(newUpgradeCommand())

	cmd.AddCommand(cmdutil.NewAutostartCommand("jail", "jail", "jails"))

	return cmd
}
