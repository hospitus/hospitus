// Package image implements image management CLI commands
package image

import (
	"github.com/spf13/cobra"
)

// NewImageCommand creates the image parent command
func NewImageCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage base system images",
		Long: `Download, list, and manage images for jails and VMs.

Hospitus supports three types of images:
  - set:   FreeBSD/Linux base system archives (.txz) for jails
  - iso:   ISO images for VM installation
  - cloud: Cloud images (qcow2, raw, img) for VMs

Use 'hospitus image available' to see all downloadable images.`,
	}

	// Add subcommands
	cmd.AddCommand(newAvailableCommand())
	cmd.AddCommand(newListCommand())
	cmd.AddCommand(newFetchCommand())
	cmd.AddCommand(newDeleteCommand())

	return cmd
}
