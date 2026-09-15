// Package manifest implements manifest-related CLI commands
package manifest

import (
	"github.com/spf13/cobra"
)

// NewManifestCommand creates the manifest parent command
func NewManifestCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "manifest",
		Aliases: []string{"m"},
		Short:   "Manage workload manifests",
		Long: `Manage workload manifests - declarative workload definitions.

Manifests are TOML files that describe workloads (single instances) or stacks
(multi-instance deployments). Use these commands to validate, apply, and
manage manifest-based deployments.

Examples:
  # Validate a manifest
  hospitus manifest validate webserver.toml

  # Apply a manifest (create or update)
  hospitus manifest apply webserver.toml

  # Show what would be created without actually applying
  hospitus manifest apply --dry-run webserver.toml

  # Delete resources created from a manifest
  hospitus manifest delete webserver.toml`,
	}

	// Add subcommands
	cmd.AddCommand(newValidateCommand())
	cmd.AddCommand(newApplyCommand())
	cmd.AddCommand(newDeleteCommand())

	return cmd
}

// NewApplyRootCommand creates a top-level "apply" command
// This allows: hospitus apply manifest.toml
func NewApplyRootCommand() *cobra.Command {
	return newApplyCommand()
}

// NewValidateRootCommand creates a top-level "validate" command
// This allows: hospitus validate manifest.toml
func NewValidateRootCommand() *cobra.Command {
	return newValidateCommand()
}
