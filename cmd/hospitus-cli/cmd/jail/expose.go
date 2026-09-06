package jail

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/pkg/firewall"
)

func newExposeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "expose",
		Short: "Manage port forwarding for a jail",
		Long: `Manage port forwarding (expose) rules for a jail.

Port forwarding allows external traffic to reach services inside the jail.

Examples:
  # Expose port 80 on host to port 8080 in jail
  hospitus jail expose add web --port 80:8080

  # Expose same port on host and jail
  hospitus jail expose add web --port 80

  # Expose UDP port
  hospitus jail expose add dns --port udp/53

  # List exposed ports
  hospitus jail expose list web

  # Remove exposed port
  hospitus jail expose remove web --port 80`,
	}

	cmd.AddCommand(newExposeAddCommand())
	cmd.AddCommand(newExposeRemoveCommand())
	cmd.AddCommand(newExposeListCommand())

	return cmd
}

func newExposeAddCommand() *cobra.Command {
	var portSpec string
	var targetIP string

	cmd := &cobra.Command{
		Use:   "add <jail-name>",
		Short: "Add a port forwarding rule",
		Long: `Add a port forwarding rule to expose a jail port on the host.

Port specification formats:
  80          - Same port on host and jail (TCP)
  80:8080     - Host port 80 to jail port 8080 (TCP)
  tcp/80:8080 - Explicit TCP
  udp/53      - UDP port

Examples:
  # Forward host:80 to jail:8080
  hospitus jail expose add web --port 80:8080

  # Forward host:53 UDP to jail:53
  hospitus jail expose add dns --port udp/53

  # Name the address, for a jail whose address the daemon has not recorded
  hospitus jail expose add web --port 8080:80 --target-ip 10.0.0.12`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExposeAdd(cmd, args, portSpec, targetIP)
		},
	}

	cmd.Flags().StringVarP(&portSpec, "port", "p", "", "Port specification (host:target or just port)")
	cmd.Flags().StringVar(&targetIP, "target-ip", "",
		"Address to forward to (default: the address recorded for the jail)")
	_ = cmd.MarkFlagRequired("port")

	return cmd
}

func newExposeRemoveCommand() *cobra.Command {
	var hostPort int
	var protocol string

	cmd := &cobra.Command{
		Use:   "remove <jail-name>",
		Short: "Remove a port forwarding rule",
		Long: `Remove a port forwarding rule from a jail.

Examples:
  # Remove TCP port 80 forwarding
  hospitus jail expose remove web --port 80

  # Remove UDP port 53 forwarding
  hospitus jail expose remove dns --port 53 --protocol udp`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExposeRemove(cmd, args, hostPort, protocol)
		},
	}

	cmd.Flags().IntVarP(&hostPort, "port", "p", 0, "Host port to remove")
	cmd.Flags().StringVar(&protocol, "protocol", "tcp", "Protocol (tcp or udp)")
	_ = cmd.MarkFlagRequired("port")

	return cmd
}

func newExposeListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <jail-name>",
		Short: "List port forwarding rules",
		Long: `List all port forwarding rules for a jail.

Examples:
  hospitus jail expose list web`,
		Args: cobra.ExactArgs(1),
		RunE: runExposeList,
	}

	cmdutil.AddOutputFlag(cmd)

	return cmd
}

func runExposeAdd(cmd *cobra.Command, args []string, portSpec, targetIP string) error {
	ctx := cmd.Context()
	jailName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, jailName, "jail"); err != nil {
		return err
	}

	// Parse port specification
	proto, hostPort, targetPort, err := firewall.ParsePortSpec(portSpec)
	if err != nil {
		return fmt.Errorf("invalid port specification: %w", err)
	}

	// An empty TargetIP asks the daemon to use the address it recorded for the
	// jail; --target-ip covers the case where it has none.
	req := client.ExposePortRequest{
		Provider:   "jail",
		Protocol:   string(proto),
		HostPort:   hostPort,
		TargetPort: targetPort,
		TargetIP:   targetIP,
	}

	result, err := cmdutil.APIClient.ExposePort(ctx, jailName, req)
	if err != nil {
		return fmt.Errorf("failed to add port forwarding: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Port forwarding added: host:%d -> %s:%d (%s) (rule_id: %s)\n",
		result.HostPort, result.TargetIP, result.TargetPort, result.Protocol, result.ID)

	return nil
}

func runExposeRemove(cmd *cobra.Command, args []string, hostPort int, protocol string) error {
	ctx := cmd.Context()
	jailName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, jailName, "jail"); err != nil {
		return err
	}

	if protocol != "tcp" && protocol != "udp" {
		return fmt.Errorf("protocol must be 'tcp' or 'udp', got '%s'", protocol)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Removing port forwarding: host:%d (%s) from jail %s\n",
		hostPort, protocol, jailName)

	// Use daemon API to remove port forwarding
	if err := cmdutil.APIClient.UnexposePort(ctx, jailName, hostPort, protocol); err != nil {
		return fmt.Errorf("failed to remove port forwarding: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Port forwarding removed\n")

	return nil
}

func runExposeList(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	jailName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, jailName, "jail"); err != nil {
		return err
	}

	// Use daemon API to list port forwarding rules
	mappings, err := cmdutil.APIClient.ListExposedPorts(ctx, jailName)
	if err != nil {
		return fmt.Errorf("failed to list port forwarding rules: %w", err)
	}

	if outputFormat, _ := cmd.Flags().GetString(cmdutil.FlagOutput); outputFormat == "json" {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(mappings)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Port forwarding rules for jail %s:\n\n", jailName)

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROTOCOL\tHOST PORT\tTARGET PORT\tTARGET IP")

	for _, pm := range mappings {
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\n",
			pm.Protocol, pm.HostPort, pm.TargetPort, pm.TargetIP)
	}

	w.Flush()

	if len(mappings) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "(no port forwarding rules configured for this jail)\n")
	}

	return nil
}
