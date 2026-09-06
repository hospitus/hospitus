package vfkit

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newStartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "start <name>",
		Short: "Start a VM",
		Args:  cobra.ExactArgs(1),
		RunE:  runStart,
	}
}

func runStart(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := cmdutil.RequireInstanceOf(cmd.Context(), cmdutil.APIClient, name, providerName); err != nil {
		return err
	}

	if err := cmdutil.APIClient.StartInstance(cmd.Context(), name, cmd.OutOrStdout()); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "VM started: %s\n", name)
	return nil
}

func newStopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop a VM",
		Args:  cobra.ExactArgs(1),
		RunE:  runStop,
	}

	cmdutil.AddForceFlag(cmd)
	return cmd
}

func runStop(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := cmdutil.RequireInstanceOf(cmd.Context(), cmdutil.APIClient, name, providerName); err != nil {
		return err
	}
	force, _ := cmd.Flags().GetBool(cmdutil.FlagForce)

	if err := cmdutil.APIClient.StopInstance(cmd.Context(), name, force); err != nil {
		return fmt.Errorf("failed to stop VM: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "VM stopped: %s\n", name)
	return nil
}

func newRestartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <name>",
		Short: "Restart a VM",
		Args:  cobra.ExactArgs(1),
		RunE:  runRestart,
	}
}

func runRestart(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := cmdutil.RequireInstanceOf(cmd.Context(), cmdutil.APIClient, name, providerName); err != nil {
		return err
	}

	if err := cmdutil.APIClient.RestartInstance(cmd.Context(), name); err != nil {
		return fmt.Errorf("failed to restart VM: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "VM restarted: %s\n", name)
	return nil
}
