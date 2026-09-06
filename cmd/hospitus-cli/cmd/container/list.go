package container

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/internal/datastore"
)

func newListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all containers",
		RunE:    runList,
	}

	cmdutil.AddOutputFlag(cmd)
	return cmd
}

func runList(cmd *cobra.Command, args []string) error {
	outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput)

	instances, err := cmdutil.APIClient.ListInstances(cmd.Context(),
		client.ListInstancesFilter{Provider: providerName})
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	items := make([]datastore.Instance, 0, len(instances))
	for _, inst := range instances {
		items = append(items, *inst)
	}

	return cmdutil.PrintInstanceList(cmd.OutOrStdout(), items, outputFormat(outputFmt))
}

func newInfoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "info <name>",
		Aliases: []string{"get", "show"},
		Short:   "Show detailed information about a container",
		Args:    cobra.ExactArgs(1),
		RunE:    runInfo,
	}

	cmdutil.AddOutputFlag(cmd)
	return cmd
}

func runInfo(cmd *cobra.Command, args []string) error {
	name := args[0]
	outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput)

	instance, err := cmdutil.APIClient.GetInstance(cmd.Context(), name)
	if err != nil {
		return fmt.Errorf("failed to get container info: %w", err)
	}

	// Guard against naming an instance of another provider: the verbs below
	// would act on it just the same, and the mistake is easy to make.
	if instance.Provider != providerName {
		return fmt.Errorf("%s is not a container (provider: %s)", name, instance.Provider)
	}

	return cmdutil.PrintInstance(cmd.OutOrStdout(), instance, outputFormat(outputFmt))
}

// outputFormat maps the --output flag onto the printer's format.
func outputFormat(flag string) cmdutil.OutputFormat {
	if flag == "json" {
		return cmdutil.OutputFormatJSON
	}
	return cmdutil.OutputFormatTable
}
