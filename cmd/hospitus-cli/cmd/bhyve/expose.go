package bhyve

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
		Short: "Manage port forwarding for a bhyve VM",
		Long: `Manage port forwarding (expose) rules for a bhyve VM.

Port forwarding allows external traffic to reach services inside the VM.

Examples:
  # Expose port 80 on host to port 8080 in VM
  hospitus bhyve expose add myvm --port 80:8080

  # Expose same port on host and VM
  hospitus bhyve expose add myvm --port 80

  # Expose UDP port
  hospitus bhyve expose add dns-vm --port udp/53

  # List exposed ports
  hospitus bhyve expose list myvm

  # Remove exposed port
  hospitus bhyve expose remove myvm --port 80`,
	}

	cmd.AddCommand(newBhyveExposeAddCommand())
	cmd.AddCommand(newBhyveExposeRemoveCommand())
	cmd.AddCommand(newBhyveExposeListCommand())

	return cmd
}

func newBhyveExposeAddCommand() *cobra.Command {
	var portSpec string
	var targetIP string

	cmd := &cobra.Command{
		Use:   "add <vm-name>",
		Short: "Add a port forwarding rule",
		Long: `Add a port forwarding rule to expose a VM port on the host.

Port specification formats:
  80          - Same port on host and VM (TCP)
  80:8080     - Host port 80 to VM port 8080 (TCP)
  tcp/80:8080 - Explicit TCP
  udp/53      - UDP port

Examples:
  # Forward host:80 to VM:8080
  hospitus bhyve expose add myvm --port 80:8080

  # Forward host:22 to VM:22
  hospitus bhyve expose add myvm --port 22

  # Name the guest address, for a VM that took its address from DHCP
  hospitus bhyve expose add myvm --port 2222:22 --target-ip 10.10.0.87`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBhyveExposeAdd(cmd, args, portSpec, targetIP)
		},
	}

	cmd.Flags().StringVarP(&portSpec, "port", "p", "", "Port specification (host:target or just port)")
	cmd.Flags().StringVar(&targetIP, "target-ip", "",
		"Address to forward to (default: the address recorded for the VM; required for a DHCP guest, which has none)")
	_ = cmd.MarkFlagRequired("port")

	return cmd
}

func newBhyveExposeRemoveCommand() *cobra.Command {
	var hostPort int
	var protocol string

	cmd := &cobra.Command{
		Use:   "remove <vm-name>",
		Short: "Remove a port forwarding rule",
		Long: `Remove a port forwarding rule from a VM.

Examples:
  # Remove TCP port 80 forwarding
  hospitus bhyve expose remove myvm --port 80

  # Remove UDP port 53 forwarding
  hospitus bhyve expose remove myvm --port 53 --protocol udp`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBhyveExposeRemove(cmd, args, hostPort, protocol)
		},
	}

	cmd.Flags().IntVarP(&hostPort, "port", "p", 0, "Host port to remove")
	cmd.Flags().StringVar(&protocol, "protocol", "tcp", "Protocol (tcp or udp)")
	_ = cmd.MarkFlagRequired("port")

	return cmd
}

func newBhyveExposeListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <vm-name>",
		Short: "List port forwarding rules",
		Long: `List all port forwarding rules for a VM.

Examples:
  hospitus bhyve expose list myvm`,
		Args: cobra.ExactArgs(1),
		RunE: runBhyveExposeList,
	}

	cmdutil.AddOutputFlag(cmd)

	return cmd
}

func runBhyveExposeAdd(cmd *cobra.Command, args []string, portSpec, targetIP string) error {
	ctx := cmd.Context()
	vmName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, vmName, "bhyve"); err != nil {
		return err
	}

	// Parse port specification
	proto, hostPort, targetPort, err := firewall.ParsePortSpec(portSpec)
	if err != nil {
		return fmt.Errorf("invalid port specification: %w", err)
	}

	req := client.ExposePortRequest{
		Provider:   "bhyve",
		Protocol:   string(proto),
		HostPort:   hostPort,
		TargetPort: targetPort,
		TargetIP:   targetIP,
	}

	result, err := cmdutil.GetClient().ExposePort(ctx, vmName, req)
	if err != nil {
		return fmt.Errorf("failed to add port forwarding: %w", err)
	}

	if result == nil {
		return fmt.Errorf("the daemon reported no port forward for %s", vmName)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Port forwarding added: host:%d -> %s:%d (%s)\n",
		result.HostPort, result.TargetIP, result.TargetPort, result.Protocol)
	return nil
}

func runBhyveExposeRemove(cmd *cobra.Command, args []string, hostPort int, protocol string) error {
	// Checked here rather than left to the firewall backend, which answers
	// with an opaque pf error for a port no host can bind.
	if hostPort < 1 || hostPort > 65535 {
		return fmt.Errorf("--port must be between 1 and 65535, got %d", hostPort)
	}
	ctx := cmd.Context()
	vmName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, vmName, "bhyve"); err != nil {
		return err
	}

	proto := firewall.Protocol(protocol)
	if proto != firewall.ProtocolTCP && proto != firewall.ProtocolUDP {
		return fmt.Errorf("protocol must be 'tcp' or 'udp', got '%s'", protocol)
	}

	if err := cmdutil.GetClient().UnexposePort(ctx, vmName, hostPort, string(proto)); err != nil {
		return fmt.Errorf("failed to remove port forwarding: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Port forwarding removed: host:%d (%s) from VM %s\n",
		hostPort, proto, vmName)
	return nil
}

func runBhyveExposeList(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	vmName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, vmName, "bhyve"); err != nil {
		return err
	}

	ports, err := cmdutil.GetClient().ListExposedPorts(ctx, vmName)
	if err != nil {
		return fmt.Errorf("failed to list port forwarding rules: %w", err)
	}

	// The flag AddOutputFlag registers on this command, which nothing read:
	// "--output json" printed the same table as always, so a script parsing
	// it got a header and columns.
	if outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput); outputFmt == "json" {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(ports)
	}

	if len(ports) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No port forwarding rules configured for VM %s\n", vmName)
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROTOCOL\tHOST PORT\tTARGET PORT\tTARGET IP")
	fmt.Fprintln(w, "--------\t---------\t-----------\t---------")
	for _, p := range ports {
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\n", p.Protocol, p.HostPort, p.TargetPort, p.TargetIP)
	}
	return w.Flush()
}
