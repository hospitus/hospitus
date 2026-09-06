// Package qemu implements qemu-specific CLI commands
package qemu

import (
	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

// NewQemuCommand creates the qemu parent command
func NewQemuCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "qemu",
		Short: "Manage QEMU virtual machines",
		Long: `Manage QEMU virtual machines - universal hypervisor.

QEMU is a generic and open source machine emulator and virtualizer
supporting multiple architectures and guest operating systems.`,
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
	cmd.AddCommand(newStatsCommand())
	cmd.AddCommand(newExposeCommand())
	cmd.AddCommand(cmdutil.NewAutostartCommand("qemu", "VM", "VMs"))
	cmd.AddCommand(newMediaCommand())
	cmd.AddCommand(newBootOrderCommand())
	cmd.AddCommand(newSnapshotCommand())
	cmd.AddCommand(newPauseCommand())
	cmd.AddCommand(newResumeCommand())
	cmd.AddCommand(newRenameCommand())

	return cmd
}
