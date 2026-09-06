// Package cmd provides command aliases for power users
package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/bhyve"
	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/jail"
	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/podman"
	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/qemu"
)

// RegisterAliases registers CBSD-style command aliases
// These provide short commands like `jcreate` as alternatives to `hospitus jail create`
func RegisterAliases(rootCmd *cobra.Command) {
	RegisterAliasesGrouped(rootCmd, "")
}

// RegisterAliasesGrouped registers CBSD-style aliases with an optional cobra group ID.
func RegisterAliasesGrouped(rootCmd *cobra.Command, groupID string) {
	addAlias := func(cmd *cobra.Command) {
		if cmd == nil {
			// Target subcommand no longer exists — skip rather than
			// registering a silent do-nothing command.
			return
		}
		if groupID != "" {
			cmd.GroupID = groupID
		}
		rootCmd.AddCommand(cmd)
	}

	// Jail aliases (j prefix)
	jailCmd := jail.NewJailCommand()

	addAlias(createAlias("jcreate", "create", jailCmd, "Create a jail (alias for 'hospitus jail create')"))
	addAlias(createAlias("jstart", "start", jailCmd, "Start a jail (alias for 'hospitus jail start')"))
	addAlias(createAlias("jstop", "stop", jailCmd, "Stop a jail (alias for 'hospitus jail stop')"))
	addAlias(createAlias("jrestart", "restart", jailCmd, "Restart a jail (alias for 'hospitus jail restart')"))
	addAlias(createAlias("jdestroy", "destroy", jailCmd, "Destroy a jail (alias for 'hospitus jail destroy')"))
	addAlias(createAlias("jls", "list", jailCmd, "List jails (alias for 'hospitus jail list')"))
	addAlias(createAlias("jinfo", "info", jailCmd, "Show jail info (alias for 'hospitus jail info')"))
	addAlias(createAlias("jexec", "exec", jailCmd, "Execute command in jail (alias for 'hospitus jail exec')"))
	addAlias(createAlias("jlogin", "console", jailCmd, "Login to jail console (alias for 'hospitus jail console')"))

	// bhyve aliases (b prefix)
	bhyveCmd := bhyve.NewBhyveCommand()

	addAlias(createAlias("bcreate", "create", bhyveCmd, "Create a bhyve VM (alias for 'hospitus bhyve create')"))
	addAlias(createAlias("bstart", "start", bhyveCmd, "Start a bhyve VM (alias for 'hospitus bhyve start')"))
	addAlias(createAlias("bstop", "stop", bhyveCmd, "Stop a bhyve VM (alias for 'hospitus bhyve stop')"))
	addAlias(createAlias("brestart", "restart", bhyveCmd, "Restart a bhyve VM (alias for 'hospitus bhyve restart')"))
	addAlias(createAlias("bdestroy", "destroy", bhyveCmd, "Destroy a bhyve VM (alias for 'hospitus bhyve destroy')"))
	addAlias(createAlias("bls", "list", bhyveCmd, "List bhyve VMs (alias for 'hospitus bhyve list')"))
	addAlias(createAlias("binfo", "info", bhyveCmd, "Show bhyve VM info (alias for 'hospitus bhyve info')"))
	addAlias(createAlias("blogin", "console", bhyveCmd, "Login to bhyve VM console (alias for 'hospitus bhyve console')"))

	// QEMU aliases (q prefix)
	qemuCmd := qemu.NewQemuCommand()

	addAlias(createAlias("qcreate", "create", qemuCmd, "Create a QEMU VM (alias for 'hospitus qemu create')"))
	addAlias(createAlias("qstart", "start", qemuCmd, "Start a QEMU VM (alias for 'hospitus qemu start')"))
	addAlias(createAlias("qstop", "stop", qemuCmd, "Stop a QEMU VM (alias for 'hospitus qemu stop')"))
	addAlias(createAlias("qrestart", "restart", qemuCmd, "Restart a QEMU VM (alias for 'hospitus qemu restart')"))
	addAlias(createAlias("qdestroy", "destroy", qemuCmd, "Destroy a QEMU VM (alias for 'hospitus qemu destroy')"))
	addAlias(createAlias("qls", "list", qemuCmd, "List QEMU VMs (alias for 'hospitus qemu list')"))
	addAlias(createAlias("qinfo", "info", qemuCmd, "Show QEMU VM info (alias for 'hospitus qemu info')"))

	// Podman aliases (p prefix)
	podmanCmd := podman.NewPodmanCommand()

	addAlias(createAlias("pstart", "start", podmanCmd, "Start a container (alias for 'hospitus podman start')"))
	addAlias(createAlias("pstop", "stop", podmanCmd, "Stop a container (alias for 'hospitus podman stop')"))
	addAlias(createAlias("prestart", "restart", podmanCmd, "Restart a container (alias for 'hospitus podman restart')"))
	addAlias(createAlias("pdestroy", "destroy", podmanCmd, "Destroy a container (alias for 'hospitus podman destroy')"))
	addAlias(createAlias("pls", "list", podmanCmd, "List containers (alias for 'hospitus podman list')"))
	addAlias(createAlias("pinfo", "info", podmanCmd, "Show container info (alias for 'hospitus podman info')"))
	addAlias(createAlias("pexec", "exec", podmanCmd, "Execute command in container (alias for 'hospitus podman exec')"))
	addAlias(createAlias("plogin", "console", podmanCmd, "Login to container console (alias for 'hospitus podman console')"))
	addAlias(createAlias("plogs", "logs", podmanCmd, "Show container logs (alias for 'hospitus podman logs')"))
}

// createAlias creates an alias command that delegates to a provider subcommand.
// Returns nil if the subcommand is not found, so callers can skip registration
// instead of exposing a silent do-nothing command.
func createAlias(aliasName, subcommandName string, providerCmd *cobra.Command, desc string) *cobra.Command {
	var targetCmd *cobra.Command
	for _, cmd := range providerCmd.Commands() {
		if cmd.Name() == subcommandName {
			targetCmd = cmd
			break
		}
	}
	if targetCmd == nil {
		return nil
	}

	useStr := targetCmd.Use
	argsPart := ""
	if idx := strings.Index(useStr, " "); idx >= 0 {
		argsPart = useStr[idx+1:]
	}

	aliasCmd := &cobra.Command{
		Use:   aliasName + " " + argsPart,
		Short: desc,
		RunE:  targetCmd.RunE,
		Args:  targetCmd.Args,
	}
	aliasCmd.Flags().AddFlagSet(targetCmd.Flags())
	if subcommandName == "exec" {
		// The exec commands turn interspersed parsing off so flags after the
		// command name belong to the program being run, not to hospitus.
		// AddFlagSet copies flag definitions but not that setting, so without
		// this `jexec web wget -q URL` fails on wget's -q.
		aliasCmd.Flags().SetInterspersed(false)
	}
	return aliasCmd
}
