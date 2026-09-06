// Package vfkit implements CLI commands for managing macOS Virtualization.framework virtual machines.
package vfkit

import (
	"github.com/spf13/cobra"
)

// providerName is the name the daemon registers this provider under.
const providerName = "vfkit"

// NewVFKitCommand creates the vfkit parent command.
func NewVFKitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vfkit",
		Short: "Manage macOS Virtualization.framework virtual machines",
		Long: `Manage virtual machines that run directly on Apple's Virtualization.framework.

vfkit puts a VM on the framework with no emulation in between, so only guests of
the host's own architecture run — arm64 on Apple silicon. For a guest of any
other architecture, use "hospitus qemu".

The framework offers no snapshots, no migration, no disk hotplug and no change
of CPU or memory on a running VM. Those verbs are absent here rather than
failing at the daemon.`,
	}

	cmd.AddCommand(newCreateCommand())
	cmd.AddCommand(newStartCommand())
	cmd.AddCommand(newStopCommand())
	cmd.AddCommand(newRestartCommand())
	cmd.AddCommand(newDestroyCommand())
	cmd.AddCommand(newListCommand())
	cmd.AddCommand(newInfoCommand())
	cmd.AddCommand(newStatsCommand())

	return cmd
}
