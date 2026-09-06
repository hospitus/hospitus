package qemu

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/pkg/firewall"
	"github.com/hospitus/hospitus/pkg/provider"
)

func newExposeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "expose",
		Short: "Manage port forwarding for a QEMU VM",
		Long: `Manage port forwarding (expose) rules for a QEMU VM.

Port forwarding allows external traffic to reach services inside the VM.

Examples:
  # Expose port 80 on host to port 8080 in VM
  hospitus qemu expose add myvm --port 80:8080

  # Expose same port on host and VM
  hospitus qemu expose add myvm --port 80

  # List exposed ports
  hospitus qemu expose list myvm

  # Remove exposed port
  hospitus qemu expose remove myvm --port 80`,
	}

	cmd.AddCommand(newQemuExposeAddCommand())
	cmd.AddCommand(newQemuExposeRemoveCommand())
	cmd.AddCommand(newQemuExposeListCommand())

	return cmd
}

func newQemuExposeAddCommand() *cobra.Command {
	var portSpec string

	cmd := &cobra.Command{
		Use:   "add <vm-name>",
		Short: "Add a port forwarding rule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQemuExposeAdd(cmd, args, portSpec)
		},
	}

	cmd.Flags().StringVarP(&portSpec, "port", "p", "", "Port specification (host:target or just port)")
	_ = cmd.MarkFlagRequired("port")

	return cmd
}

func newQemuExposeRemoveCommand() *cobra.Command {
	var hostPort int
	var protocol string

	cmd := &cobra.Command{
		Use:   "remove <vm-name>",
		Short: "Remove a port forwarding rule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQemuExposeRemove(cmd, args, hostPort, protocol)
		},
	}

	cmd.Flags().IntVarP(&hostPort, "port", "p", 0, "Host port to remove")
	cmd.Flags().StringVar(&protocol, "protocol", "tcp", "Protocol (tcp or udp)")
	_ = cmd.MarkFlagRequired("port")

	return cmd
}

func newQemuExposeListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <vm-name>",
		Short: "List port forwarding rules",
		Args:  cobra.ExactArgs(1),
		RunE:  runQemuExposeList,
	}

	cmdutil.AddOutputFlag(cmd)

	return cmd
}

func runQemuExposeAdd(cmd *cobra.Command, args []string, portSpec string) error {
	ctx := cmd.Context()
	vmName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, vmName, "qemu"); err != nil {
		return err
	}

	proto, hostPort, guestPort, err := firewall.ParsePortSpec(portSpec)
	if err != nil {
		return fmt.Errorf("invalid port specification: %w", err)
	}

	pf := provider.PortForward{
		Protocol:  string(proto),
		HostPort:  hostPort,
		GuestPort: guestPort,
	}

	if err := cmdutil.APIClient.AddPortForwardVM(ctx, vmName, pf); err != nil {
		return fmt.Errorf("failed to add port forwarding: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Port forwarding added: host:%d -> VM:%d (%s)\n",
		hostPort, guestPort, proto)
	return nil
}

func runQemuExposeRemove(cmd *cobra.Command, args []string, hostPort int, protocol string) error {
	ctx := cmd.Context()
	vmName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, vmName, "qemu"); err != nil {
		return err
	}

	if protocol != "tcp" && protocol != "udp" {
		return fmt.Errorf("protocol must be 'tcp' or 'udp', got '%s'", protocol)
	}

	if err := cmdutil.APIClient.RemovePortForwardVM(ctx, vmName, protocol, hostPort); err != nil {
		return fmt.Errorf("failed to remove port forwarding: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Port forwarding removed: host:%d (%s)\n", hostPort, protocol)
	return nil
}

func runQemuExposeList(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	vmName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, vmName, "qemu"); err != nil {
		return err
	}

	rules, err := cmdutil.APIClient.ListPortForwardsVM(ctx, vmName)
	if err != nil {
		return fmt.Errorf("failed to list port forwarding rules: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Port forwarding rules for VM %s:\n\n", vmName)

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROTOCOL\tHOST PORT\tGUEST PORT")

	for _, r := range rules {
		fmt.Fprintf(w, "%s\t%d\t%d\n", r.Protocol, r.HostPort, r.GuestPort)
	}
	w.Flush()

	if len(rules) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "(no port forwarding rules configured for this VM)\n")
	}

	return nil
}
