package manifest

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/pkg/manifest"
)

func newValidateCommand() *cobra.Command {
	var quiet bool

	cmd := &cobra.Command{
		Use:   "validate <manifest.toml>",
		Short: "Validate a manifest file",
		Long: `Validate a workload or stack manifest file.

This command parses and validates the manifest without actually applying it.
Use this to check for errors before deployment.

Examples:
  # Validate a single manifest
  hospitus manifest validate webserver.toml

  # Quiet mode - only output errors
  hospitus manifest validate --quiet webserver.toml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runValidate(args[0], quiet)
		},
	}

	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Only output errors")

	return cmd
}

func runValidate(manifestPath string, quiet bool) error {
	// Check if file exists
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		return fmt.Errorf("manifest file not found: %s", manifestPath)
	}

	// Parse manifest with a no-op secret store to avoid creating secrets on disk
	// during validation (especially when template scope is not yet resolved).
	secrets := manifest.NewNoopSecretStore()
	parser := manifest.NewParser(secrets)
	parsed, err := parser.ParseFile(manifestPath, nil)
	if err != nil {
		return fmt.Errorf("failed to parse manifest: %w", err)
	}

	// Validate
	validator := manifest.NewValidator()
	validationErrs := validator.Validate(parsed)

	// Report results
	if !quiet {
		if parsed.IsWorkload() {
			fmt.Printf("Manifest type: Workload\n")
			fmt.Printf("Name: %s\n", parsed.Workload.Workload.Name)
			fmt.Printf("Provider: %s\n", parsed.Workload.Provider.Type)
			fmt.Printf("Image: %s\n", parsed.Workload.Image.Source)
		} else if parsed.IsStack() {
			fmt.Printf("Manifest type: Stack\n")
			fmt.Printf("Name: %s\n", parsed.Stack.Stack.Name)
			fmt.Printf("Instances: %d\n", len(parsed.Stack.Instances))
			for i := range parsed.Stack.Instances {
				inst := &parsed.Stack.Instances[i]
				fmt.Printf("  - %s (%s)\n", inst.Name, inst.Provider)
			}
		}
		fmt.Println()
	}

	if validationErrs.HasErrors() {
		fmt.Println("Validation errors:")
		for _, e := range validationErrs {
			fmt.Printf("  ERROR: [%s] %s\n", e.Field, e.Message)
		}
		fmt.Println()
	}

	// Exit status
	if validationErrs.HasErrors() {
		return fmt.Errorf("validation failed with %d error(s)", len(validationErrs.Errors()))
	}

	if !quiet {
		fmt.Println("Manifest is valid")
	}

	return nil
}
