// Package container implements CLI commands for manage containers on Apple's container runtime.
package container

import (
	"github.com/spf13/cobra"
)

// providerName is the name the daemon registers this provider under.
const providerName = "container"

// NewContainerCommand creates the container parent command.
func NewContainerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "container",
		Short: "Manage containers on Apple's container runtime",
		Long: `Manage OCI containers through Apple's container(1) runtime, which runs each
container inside its own lightweight VM on Virtualization.framework.

The runtime must be started first:

  container system start

A container's command travels in provider config, since an instance spec has no
field for it — without one an image like alpine exits immediately.`,
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
