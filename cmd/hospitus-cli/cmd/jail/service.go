package jail

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
)

func newServiceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage services inside a jail",
		Long: `Manage FreeBSD services (rc.d scripts) inside a jail.

This command allows you to enable, disable, start, stop, and manage services
running inside a jail using FreeBSD's native rc(8) system.

Examples:
  # Enable nginx to start at boot
  hospitus jail service enable web nginx

  # Start nginx immediately
  hospitus jail service start web nginx

  # List all services in jail
  hospitus jail service list web

  # Show status of a specific service
  hospitus jail service status web nginx`,
	}

	cmd.AddCommand(newServiceEnableCommand())
	cmd.AddCommand(newServiceDisableCommand())
	cmd.AddCommand(newServiceStartCommand())
	cmd.AddCommand(newServiceStopCommand())
	cmd.AddCommand(newServiceRestartCommand())
	cmd.AddCommand(newServiceReloadCommand())
	cmd.AddCommand(newServiceStatusCommand())
	cmd.AddCommand(newServiceListCommand())

	return cmd
}

func newServiceEnableCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "enable <jail-name> <service>",
		Short: "Enable a service to start at boot",
		Long: `Enable a service in the jail to start automatically at boot time.

This adds the service to the jail's rc.conf file.

Examples:
  hospitus jail service enable web nginx
  hospitus jail service enable db postgresql`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceAction(cmd, args[0], args[1], "enable")
		},
	}
	return cmd
}

func newServiceDisableCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "disable <jail-name> <service>",
		Short: "Disable a service from starting at boot",
		Long: `Disable a service in the jail from starting automatically at boot time.

This removes the service from the jail's rc.conf file.

Examples:
  hospitus jail service disable web sendmail`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceAction(cmd, args[0], args[1], "disable")
		},
	}
	return cmd
}

func newServiceStartCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start <jail-name> <service>",
		Short: "Start a service",
		Long: `Start a service inside the jail immediately.

Examples:
  hospitus jail service start web nginx`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceAction(cmd, args[0], args[1], "start")
		},
	}
	return cmd
}

func newServiceStopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <jail-name> <service>",
		Short: "Stop a service",
		Long: `Stop a running service inside the jail.

Examples:
  hospitus jail service stop web nginx`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceAction(cmd, args[0], args[1], "stop")
		},
	}
	return cmd
}

func newServiceRestartCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restart <jail-name> <service>",
		Short: "Restart a service",
		Long: `Restart a service inside the jail.

Examples:
  hospitus jail service restart web nginx`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceAction(cmd, args[0], args[1], "restart")
		},
	}
	return cmd
}

func newServiceReloadCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reload <jail-name> <service>",
		Short: "Reload a service configuration",
		Long: `Reload a service configuration without fully restarting it.

Not all services support reload. If unsupported, this may behave like restart.

Examples:
  hospitus jail service reload web nginx`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceAction(cmd, args[0], args[1], "reload")
		},
	}
	return cmd
}

func newServiceStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <jail-name> <service>",
		Short: "Show status of a service",
		Long: `Show the current status of a service inside the jail.

Examples:
  hospitus jail service status web nginx`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceStatus(cmd, args[0], args[1])
		},
	}
	return cmd
}

func newServiceListCommand() *cobra.Command {
	var showAll bool
	var showEnabled bool
	var showRunning bool

	cmd := &cobra.Command{
		Use:   "list <jail-name>",
		Short: "List services in a jail",
		Long: `List services available in a jail.

By default, shows all services. Use flags to filter:
  --enabled   Show only enabled services
  --running   Show only running services
  --all       Show all services (default)

Examples:
  hospitus jail service list web
  hospitus jail service list web --enabled
  hospitus jail service list web --running`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceList(cmd, args[0], showAll, showEnabled, showRunning)
		},
	}

	cmd.Flags().BoolVar(&showAll, "all", true, "Show all services")
	cmd.Flags().BoolVar(&showEnabled, "enabled", false, "Show only enabled services")
	cmd.Flags().BoolVar(&showRunning, "running", false, "Show only running services")

	return cmd
}

func runServiceAction(cmd *cobra.Command, jailName, serviceName, action string) error {
	ctx := cmd.Context()
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, jailName, "jail"); err != nil {
		return err
	}

	req := client.ServiceActionRequest{
		ServiceName: serviceName,
		Action:      action,
	}

	result, err := cmdutil.APIClient.ServiceAction(ctx, jailName, req)
	if err != nil {
		return fmt.Errorf("failed to %s service %s: %w", action, serviceName, err)
	}

	if result.Success {
		fmt.Fprintf(cmd.OutOrStdout(), "Service %s: %s successful\n", serviceName, action)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Service %s: %s failed\n", serviceName, action)
		if result.Message != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Message: %s\n", result.Message)
		}
	}

	return nil
}

func runServiceStatus(cmd *cobra.Command, jailName, serviceName string) error {
	ctx := cmd.Context()

	status, err := cmdutil.APIClient.GetServiceStatus(ctx, jailName, serviceName)
	if err != nil {
		return fmt.Errorf("failed to get service status: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Service: %s\n", serviceName)
	fmt.Fprintf(cmd.OutOrStdout(), "Enabled: %v\n", status.Enabled)
	fmt.Fprintf(cmd.OutOrStdout(), "Running: %v\n", status.Running)
	if status.RCScript != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "RC Script: %s\n", status.RCScript)
	}

	return nil
}

func runServiceList(cmd *cobra.Command, jailName string, showAll, showEnabled, showRunning bool) error {
	ctx := cmd.Context()

	var filter string
	if showRunning && !showAll {
		filter = "running"
	} else if showEnabled && !showAll {
		filter = "enabled"
	}

	services, err := cmdutil.APIClient.ListServices(ctx, jailName, filter)
	if err != nil {
		return fmt.Errorf("failed to list services: %w", err)
	}

	if len(services) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No services found in jail %s\n", jailName)
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tENABLED\tRUNNING\tRC SCRIPT")

	for _, svc := range services {
		enabled := "no"
		if svc.Enabled {
			enabled = "yes"
		}
		running := "no"
		if svc.Running {
			running = "yes"
		}
		script := svc.RCScript
		if len(script) > 40 {
			script = "..." + script[len(script)-37:]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", svc.Name, enabled, running, script)
	}
	w.Flush()

	fmt.Fprintf(cmd.OutOrStdout(), "\nTotal: %d service(s)\n", len(services))

	return nil
}
