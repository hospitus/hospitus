// Package podman implements podman-specific CLI commands
package podman

import (
	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

// NewPodmanCommand creates the podman parent command
func NewPodmanCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "podman",
		Short: "Manage Podman containers",
		Long: `Manage Podman containers - OCI-compliant container runtime.

Podman is a daemonless container engine compatible with Docker. It provides
rootless container execution and pod management capabilities.`,
	}

	// Add subcommands
	cmd.AddCommand(newCreateCommand())
	cmd.AddCommand(newListCommand())
	cmd.AddCommand(newStartCommand())
	cmd.AddCommand(newStopCommand())
	cmd.AddCommand(newRestartCommand())
	cmd.AddCommand(newDestroyCommand())
	cmd.AddCommand(newInfoCommand())
	cmd.AddCommand(newExecCommand())
	cmd.AddCommand(newConsoleCommand())
	cmd.AddCommand(newLogsCommand())
	cmd.AddCommand(newStatsCommand())
	cmd.AddCommand(newSnapshotCommand())
	cmd.AddCommand(newCloneCommand())
	cmd.AddCommand(newHealthCommand())
	cmd.AddCommand(newRenameCommand())

	cmd.AddCommand(cmdutil.NewAutostartCommand("podman", "container", "containers"))

	return cmd
}
