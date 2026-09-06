package cmdutil

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/pkg/provider"
)

// NewAutostartCommand builds the "autostart" sub-command for a provider.
//
// Auto-start is the same operation whatever the workload is — enable, disable,
// list, status, all four going through the same three client calls — so it
// lives here rather than as a copy per provider.
//
// noun is what this provider's workloads are called in the output ("VM",
// "jail", "container"); nounPlural is used for the list heading.
func NewAutostartCommand(providerName, noun, nounPlural string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "autostart",
		Short: fmt.Sprintf("Manage auto-start settings for %s", nounPlural),
		Long: fmt.Sprintf(`Manage auto-start settings for %s.

Auto-start brings a %s up when hospitusd starts, typically after the host
reboots. They start in priority order — lower numbers first — with an optional
delay between them.

Examples:
  hospitus %s autostart enable my%s
  hospitus %s autostart enable my%s --priority 10 --delay 5000
  hospitus %s autostart disable my%s
  hospitus %s autostart list
  hospitus %s autostart status my%s`,
			nounPlural, noun,
			providerName, providerName,
			providerName, providerName,
			providerName, providerName,
			providerName,
			providerName, providerName),
	}

	cmd.AddCommand(newAutostartEnable(providerName, noun))
	cmd.AddCommand(newAutostartDisable(providerName, noun))
	cmd.AddCommand(newAutostartList(providerName, nounPlural))
	cmd.AddCommand(newAutostartStatus(providerName, noun))

	return cmd
}

func newAutostartEnable(providerName, noun string) *cobra.Command {
	var priority, delay int

	cmd := &cobra.Command{
		Use:   fmt.Sprintf("enable <%s-name>", providerName),
		Short: fmt.Sprintf("Enable auto-start for a %s", noun),
		Long: fmt.Sprintf(`Enable auto-start for a %s.

Priority (0-100, default 50): lower numbers start first.
Delay: milliseconds to wait before starting this %s.`, noun, noun),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if priority < 0 || priority > 100 {
				return fmt.Errorf("priority must be between 0 and 100, got %d", priority)
			}
			if delay < 0 {
				return fmt.Errorf("delay must be non-negative, got %d", delay)
			}

			ctx := cmd.Context()
			resolved, err := resolveFor(ctx, providerName, noun, args[0])
			if err != nil {
				return err
			}

			info, err := GetClient().SetAutoStart(ctx, providerName, resolved, provider.AutoStartConfig{
				Enabled:  true,
				Priority: priority,
				DelayMS:  delay,
			})
			if err != nil {
				return fmt.Errorf("failed to enable auto-start: %w", err)
			}
			// A nil result with no error is a daemon that answered something
			// this client cannot read. Printing from it panics; saying so does
			// not.
			if info == nil {
				return fmt.Errorf("the daemon reported no auto-start configuration for %s %s", noun, resolved)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Auto-start enabled for %s %s\n", noun, info.ID)
			fmt.Fprintf(cmd.OutOrStdout(), "  Priority: %d\n", info.AutoStart.Priority)
			fmt.Fprintf(cmd.OutOrStdout(), "  Delay:    %d ms\n", info.AutoStart.DelayMS)
			return nil
		},
	}

	cmd.Flags().IntVar(&priority, "priority", 50, "Start priority (0-100, lower starts first)")
	cmd.Flags().IntVar(&delay, "delay", 0, "Delay before starting in milliseconds")

	return cmd
}

func newAutostartDisable(providerName, noun string) *cobra.Command {
	return &cobra.Command{
		Use:   fmt.Sprintf("disable <%s-name>", providerName),
		Short: fmt.Sprintf("Disable auto-start for a %s", noun),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			resolved, err := resolveFor(ctx, providerName, noun, args[0])
			if err != nil {
				return err
			}

			if err := GetClient().DisableAutoStart(ctx, providerName, resolved); err != nil {
				return fmt.Errorf("failed to disable auto-start: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Auto-start disabled for %s %s\n", noun, resolved)
			return nil
		},
	}
}

func newAutostartList(providerName, nounPlural string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: fmt.Sprintf("List all %s configured for auto-start", nounPlural),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			instances, err := GetClient().ListAutoStart(cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to list auto-start %s: %w", nounPlural, err)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tPRIORITY\tDELAY (MS)")
			fmt.Fprintln(w, "----\t--------\t----------")

			found := false
			for _, inst := range instances {
				if inst.Provider == providerName && inst.AutoStart.Enabled {
					fmt.Fprintf(w, "%s\t%d\t%d\n", inst.ID, inst.AutoStart.Priority, inst.AutoStart.DelayMS)
					found = true
				}
			}
			if err := w.Flush(); err != nil {
				return err
			}

			if !found {
				fmt.Fprintf(cmd.OutOrStdout(), "No %s configured for auto-start.\n", nounPlural)
			}
			return nil
		},
	}
}

func newAutostartStatus(providerName, noun string) *cobra.Command {
	return &cobra.Command{
		Use:   fmt.Sprintf("status <%s-name>", providerName),
		Short: fmt.Sprintf("Show auto-start status for a %s", noun),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			resolved, err := resolveFor(ctx, providerName, noun, args[0])
			if err != nil {
				return err
			}

			info, err := GetClient().GetAutoStart(ctx, providerName, resolved)
			if err != nil {
				return fmt.Errorf("failed to get auto-start status: %w", err)
			}
			if info == nil {
				return fmt.Errorf("the daemon reported no auto-start configuration for %s %s", noun, resolved)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Auto-start configuration for %s %s:\n\n", noun, info.ID)
			fmt.Fprintf(cmd.OutOrStdout(), "  Enabled:  %v\n", info.AutoStart.Enabled)
			fmt.Fprintf(cmd.OutOrStdout(), "  Priority: %d\n", info.AutoStart.Priority)
			fmt.Fprintf(cmd.OutOrStdout(), "  Delay:    %d ms\n", info.AutoStart.DelayMS)
			return nil
		},
	}
}

// resolveFor turns a user-supplied name into the instance id, reporting a
// missing one in the provider's own vocabulary.
func resolveFor(ctx context.Context, providerName, noun, name string) (string, error) {
	resolved, err := ResolveInstanceName(ctx, GetClient(), name, providerName)
	if err != nil {
		return "", fmt.Errorf("%s %q not found: %w", noun, name, err)
	}
	return resolved, nil
}
