package jail

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
)

func newNetworkCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "network",
		Aliases: []string{"net", "nic"},
		Short:   "Manage network interfaces",
		Long: `Manage network interfaces for jails.

This command allows you to add, remove, and list network interfaces
for VNET-enabled jails. Supports multiple NICs, IPv4/IPv6 dual-stack,
DHCP, DHCPv6, and SLAAC.

Examples:
  # Add a second network interface
  hospitus jail network add web --bridge internal --ipv4 10.0.1.10/24

  # Add a DHCP interface
  hospitus jail network add web --bridge external --dhcp

  # List network interfaces
  hospitus jail network list web

  # Remove a network interface
  hospitus jail network remove web lan1`,
	}

	cmd.AddCommand(newNetworkAddCommand())
	cmd.AddCommand(newNetworkRemoveCommand())
	cmd.AddCommand(newNetworkListCommand())

	return cmd
}

func newNetworkAddCommand() *cobra.Command {
	var bridge string
	var name string
	var ipv4 string
	var ipv4gw string
	var ipv6 string
	var ipv6gw string
	var dhcpv4 bool
	var dhcpv6 bool
	var slaac bool
	var mtu int
	var mac string
	var dns []string
	var primary bool
	var description string

	cmd := &cobra.Command{
		Use:   "add <jail-name>",
		Short: "Add a network interface",
		Long: `Add a new network interface to a VNET-enabled jail.

This creates a new epair interface and attaches it to the specified bridge.

Options:
  --bridge        Bridge to attach to (required)
  --name          Interface name inside jail (auto-generated if not set)
  --ipv4          IPv4 address with CIDR (e.g., 10.0.0.10/24)
  --ipv4-gateway  IPv4 gateway address
  --ipv6          IPv6 address with CIDR
  --ipv6-gateway  IPv6 gateway address
  --dhcp          Enable DHCPv4
  --dhcpv6        Enable DHCPv6
  --slaac         Enable SLAAC (IPv6 auto-configuration)
  --mtu           MTU size (default 1500)
  --mac           MAC address (auto-generated if not set)
  --dns           DNS server addresses (can be repeated)
  --primary       Set as primary interface for default route

Examples:
  # Add interface with static IP
  hospitus jail network add web --bridge internal --ipv4 10.0.1.10/24 --ipv4-gateway 10.0.1.1

  # Add DHCP interface
  hospitus jail network add web --bridge external --dhcp

  # Add dual-stack interface
  hospitus jail network add web --bridge dmz --ipv4 10.0.2.10/24 --ipv6 fd00::10/64`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNetworkAdd(cmd, args[0], client.NetworkInterfaceRequest{
				Name:        name,
				Bridge:      bridge,
				IPv4Address: ipv4,
				IPv4Gateway: ipv4gw,
				IPv6Address: ipv6,
				IPv6Gateway: ipv6gw,
				DHCPv4:      dhcpv4,
				DHCPv6:      dhcpv6,
				SLAAC:       slaac,
				MTU:         mtu,
				MAC:         mac,
				DNSServers:  dns,
				Primary:     primary,
				Description: description,
			})
		},
	}

	cmd.Flags().StringVar(&bridge, "bridge", "", "Bridge to attach to (required)")
	cmd.Flags().StringVar(&name, "name", "", "Interface name inside jail")
	cmd.Flags().StringVar(&ipv4, "ipv4", "", "IPv4 address with CIDR")
	cmd.Flags().StringVar(&ipv4gw, "ipv4-gateway", "", "IPv4 gateway address")
	cmd.Flags().StringVar(&ipv6, "ipv6", "", "IPv6 address with CIDR")
	cmd.Flags().StringVar(&ipv6gw, "ipv6-gateway", "", "IPv6 gateway address")
	cmd.Flags().BoolVar(&dhcpv4, "dhcp", false, "Enable DHCPv4")
	cmd.Flags().BoolVar(&dhcpv6, "dhcpv6", false, "Enable DHCPv6")
	cmd.Flags().BoolVar(&slaac, "slaac", false, "Enable SLAAC (IPv6 auto-config)")
	// Zero, and the help says so: the flag does not set an MTU, it leaves the
	// interface with whatever the bridge gives it. Documenting 1500 named a
	// value this command never sends.
	cmd.Flags().IntVar(&mtu, "mtu", 0, "MTU size (0 leaves the interface default)")
	cmd.Flags().StringVar(&mac, "mac", "", "MAC address")
	cmd.Flags().StringSliceVar(&dns, "dns", nil, "DNS servers")
	cmd.Flags().BoolVar(&primary, "primary", false, "Set as primary interface")
	cmd.Flags().StringVar(&description, "description", "", "Interface description")

	_ = cmd.MarkFlagRequired("bridge")

	return cmd
}

func newNetworkRemoveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "remove <jail-name> <interface-name>",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove a network interface",
		Long: `Remove a network interface from a jail.

This destroys the epair interface and removes it from the bridge.

Examples:
  hospitus jail network remove web lan1`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNetworkRemove(cmd, args[0], args[1])
		},
	}

	return cmd
}

func newNetworkListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list <jail-name>",
		Aliases: []string{"ls"},
		Short:   "List network interfaces",
		Long: `List all network interfaces for a jail.

Examples:
  hospitus jail network list web`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNetworkList(cmd, args[0])
		},
	}

	cmdutil.AddOutputFlag(cmd)

	return cmd
}

func runNetworkAdd(cmd *cobra.Command, jailName string, req client.NetworkInterfaceRequest) error {
	ctx := cmd.Context()
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, jailName, "jail"); err != nil {
		return err
	}

	iface, err := cmdutil.APIClient.AddNetworkInterface(ctx, jailName, req)
	if err != nil {
		return fmt.Errorf("failed to add network interface: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Network interface added to jail '%s'\n\n", jailName)
	fmt.Fprintf(cmd.OutOrStdout(), "Name:           %s\n", iface.Name)
	fmt.Fprintf(cmd.OutOrStdout(), "Bridge:         %s\n", iface.Bridge)
	if iface.HostInterface != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Host Interface: %s\n", iface.HostInterface)
	}
	if iface.JailInterface != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Jail Interface: %s\n", iface.JailInterface)
	}
	if iface.IPv4Address != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "IPv4:           %s\n", iface.IPv4Address)
	}
	if iface.IPv6Address != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "IPv6:           %s\n", iface.IPv6Address)
	}
	if iface.DHCPv4 {
		fmt.Fprintf(cmd.OutOrStdout(), "DHCPv4:         enabled\n")
	}
	if iface.DHCPv6 {
		fmt.Fprintf(cmd.OutOrStdout(), "DHCPv6:         enabled\n")
	}
	if iface.SLAAC {
		fmt.Fprintf(cmd.OutOrStdout(), "SLAAC:          enabled\n")
	}
	if iface.Primary {
		fmt.Fprintf(cmd.OutOrStdout(), "Primary:        yes\n")
	}

	return nil
}

func runNetworkRemove(cmd *cobra.Command, jailName, interfaceName string) error {
	ctx := cmd.Context()
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, jailName, "jail"); err != nil {
		return err
	}

	if err := cmdutil.APIClient.RemoveNetworkInterface(ctx, jailName, interfaceName); err != nil {
		return fmt.Errorf("failed to remove network interface: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Network interface '%s' removed from jail '%s'\n",
		interfaceName, jailName)
	return nil
}

func runNetworkList(cmd *cobra.Command, jailName string) error {
	ctx := cmd.Context()
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, jailName, "jail"); err != nil {
		return err
	}

	interfaces, err := cmdutil.APIClient.ListNetworkInterfaces(ctx, jailName)
	if err != nil {
		return fmt.Errorf("failed to list network interfaces: %w", err)
	}

	// The command declares --output; honor it. It was declared and never
	// read, so "hospitus jail network list web --output json" printed the table
	// and anything parsing the result got a header row.
	if outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput); outputFmt == "json" {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(interfaces)
	}

	if len(interfaces) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No network interfaces found for jail '%s'\n", jailName)
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Network interfaces for jail '%s':\n\n", jailName)

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tBRIDGE\tIPv4\tIPv6\tMODE\tPRIMARY")

	for i := range interfaces {
		iface := &interfaces[i]
		mode := getNetworkMode(*iface)
		primary := ""
		if iface.Primary {
			primary = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			iface.Name,
			iface.Bridge,
			formatIP(iface.IPv4Address),
			formatIP(iface.IPv6Address),
			mode,
			primary)
	}
	w.Flush()

	fmt.Fprintf(cmd.OutOrStdout(), "\nTotal: %d interface(s)\n", len(interfaces))

	return nil
}

// getNetworkMode returns a description of the network configuration mode
func getNetworkMode(iface client.NetworkInterfaceInfo) string {
	var modes []string

	if iface.DHCPv4 {
		modes = append(modes, "dhcp4")
	} else if iface.IPv4Address != "" {
		modes = append(modes, "static4")
	}

	switch {
	case iface.DHCPv6:
		modes = append(modes, "dhcp6")
	case iface.SLAAC:
		modes = append(modes, "slaac")
	case iface.IPv6Address != "":
		modes = append(modes, "static6")
	}

	if len(modes) == 0 {
		return "-"
	}
	return strings.Join(modes, ",")
}

// formatIP formats an IP address for display. The surrounding tabwriter
// aligns columns, so full addresses (including long IPv6 ones) are shown
// verbatim rather than truncated into an unusable prefix.
func formatIP(ip string) string {
	if ip == "" {
		return "-"
	}
	return ip
}
