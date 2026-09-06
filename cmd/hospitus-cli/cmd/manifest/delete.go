package manifest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/pkg/manifest"
)

func newDeleteCommand() *cobra.Command {
	var (
		force       bool
		yes         bool
		keepVolumes bool
		timeout     time.Duration
		namespace   string
	)

	cmd := &cobra.Command{
		Use:     "delete <manifest.toml>",
		Aliases: []string{"rm", "destroy"},
		Short:   "Delete resources created from a manifest",
		Long: `Delete workloads or stacks created from a manifest.

This command reads a TOML manifest file and deletes the instances it defines.
For stack manifests, instances are deleted in reverse dependency order.

Examples:
  # Delete a workload
  hospitus manifest delete webserver.toml

  # Delete without confirmation
  hospitus manifest delete -y webserver.toml

  # Force delete (stop running instances)
  hospitus manifest delete --force webserver.toml

  # Delete a stack
  hospitus manifest delete stack.toml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDelete(cmd.Context(), args[0], force, yes, keepVolumes, timeout, namespace)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force delete (stop running instances first)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&keepVolumes, "keep-volumes", false, "Keep the ZFS volumes the manifest declares, with their data")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "Timeout for operations (Linux jails may need longer)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Namespace prefix for instance names")

	return cmd
}

func runDelete(ctx context.Context, manifestPath string, force, yes, keepVolumes bool, timeout time.Duration, namespace string) error {
	// Check if file exists
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		return fmt.Errorf("manifest file not found: %s", manifestPath)
	}

	// Parse manifest with a no-op secret store to avoid creating secrets on disk
	// during deletion (especially when template scope is not yet resolved).
	secrets := manifest.NewNoopSecretStore()
	parser := manifest.NewParser(secrets)
	parsed, err := parser.ParseFile(manifestPath, nil)
	if err != nil {
		return fmt.Errorf("failed to parse manifest: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if parsed.IsWorkload() {
		return deleteWorkload(ctx, parsed.Workload, force, yes, keepVolumes, namespace)
	} else if parsed.IsStack() {
		return deleteStack(ctx, parsed.Stack, force, yes, keepVolumes, namespace)
	}

	return fmt.Errorf("unknown manifest type")
}

func deleteWorkload(ctx context.Context, workload *manifest.WorkloadManifest, force, yes, keepVolumes bool, namespace string) error {
	name := workload.Workload.Name
	if namespace != "" {
		name = namespace + "-" + name
	}

	apiClient := cmdutil.GetClient()
	if apiClient == nil {
		return fmt.Errorf("API client not initialized")
	}

	// Resolve the actual instance name (handles manifest name != instance name mismatches)
	resolvedName, err := cmdutil.ResolveInstanceNameStrict(ctx, apiClient, name, workload.Provider.Type)
	if err == nil && resolvedName != name {
		fmt.Printf("Resolved '%s' -> '%s'\n", name, resolvedName)
		name = resolvedName
	}

	fmt.Printf("Will delete workload: %s (%s)\n", name, workload.Provider.Type)

	// A volume holds the workload's data and outlives its instance, so say what
	// is going before asking rather than after.
	volumes := managedVolumes(workload, name)
	if keepVolumes {
		volumes = nil
	}
	for _, v := range volumes {
		fmt.Printf("  and its volume: %s\n", v)
	}

	if !yes {
		if !cmdutil.AskYesNo("Are you sure?") {
			fmt.Println("Canceled")
			return nil
		}
	}

	// Stop first if force is set
	if force {
		fmt.Printf("Stopping %s...\n", name)
		if stopErr := apiClient.StopInstance(ctx, name, true); stopErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to stop %s: %v\n", name, stopErr)
		}
	}

	// Delete
	fmt.Printf("Deleting %s...\n", name)

	// An instance that is already gone is not a reason to stop: its volumes
	// outlive it, and a teardown interrupted halfway is exactly when the
	// operator runs this command again to finish the job.
	switch deleteErr := apiClient.DeleteInstance(ctx, name, force); {
	case deleteErr == nil:
		fmt.Printf("Deleted %s\n", name)
	case strings.Contains(deleteErr.Error(), "not found"):
		fmt.Printf("%s is already gone\n", name)
	default:
		return fmt.Errorf("failed to delete: %w", deleteErr)
	}

	// The instance is gone; its volumes would otherwise stay as full-size
	// datasets under names the next apply will not reuse.
	for _, v := range volumes {
		if err := apiClient.DeleteVolume(ctx, v); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to delete volume %s: %v\n", v, err)
			continue
		}
		fmt.Printf("Deleted volume %s\n", v)
	}

	return nil
}

func deleteStack(ctx context.Context, stack *manifest.StackManifest, force, yes, keepVolumes bool, namespace string) error {
	stackName := stack.Stack.Name
	if namespace != "" {
		stackName = namespace + "-" + stackName
	}

	fmt.Printf("Will delete stack: %s\n", stackName)
	fmt.Printf("  Instances: %d\n", len(stack.Instances))
	for i := range stack.Instances {
		fmt.Printf("    - %s_%s\n", stackName, stack.Instances[i].Name)
	}

	if !yes {
		if !cmdutil.AskYesNo("Are you sure?") {
			fmt.Println("Canceled")
			return nil
		}
	}

	// Delete in reverse dependency order
	order, err := buildDependencyOrder(stack.Instances)
	if err != nil {
		return err
	}

	// Reverse the order
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		order[i], order[j] = order[j], order[i]
	}

	fmt.Println("\nDeleting instances in reverse order...")

	for i := range order {
		inst := &order[i]
		instanceName := stackName + "_" + inst.Name

		workload := &manifest.WorkloadManifest{
			Workload: manifest.WorkloadMeta{Name: instanceName},
			Provider: manifest.ProviderSpec{Type: inst.Provider},
		}

		if err := deleteWorkload(ctx, workload, force, true, keepVolumes, ""); err != nil {
			fmt.Printf("Warning: failed to delete %s: %v\n", instanceName, err)
			// Continue with other instances
		}
	}

	fmt.Printf("\nStack %s deleted\n", stackName)
	return nil
}
