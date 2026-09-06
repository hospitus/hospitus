package qemu

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
		Short:   "List all QEMU VMs",
		// Declared, so a stray argument is a usage error rather than being
		// silently ignored: "hospitus qemu list web" listed everything.
		Args: cobra.NoArgs,
		RunE: runList,
	}

	cmdutil.AddOutputFlag(cmd)
	return cmd
}

func runList(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput)

	filter := client.ListInstancesFilter{Provider: "qemu"}
	instances, err := cmdutil.APIClient.ListInstances(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to list VMs: %w", err)
	}

	// Convert to slice
	var vms []datastore.Instance
	for _, inst := range instances {
		// A nil entry is a row the daemon could not describe; dereferencing it
		// panics, and the rest of the list is still worth showing.
		if inst == nil {
			continue
		}
		vms = append(vms, *inst)
	}

	var format cmdutil.OutputFormat
	if outputFmt == "json" {
		format = cmdutil.OutputFormatJSON
	} else {
		format = cmdutil.OutputFormatTable
	}

	return cmdutil.PrintInstanceList(cmd.OutOrStdout(), vms, format)
}
