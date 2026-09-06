// Package main implements the HOSPITUS CLI (hospitus)
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/spf13/cobra/doc"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd"
	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/backup"
	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/bhyve"
	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/container"
	hospitusctx "github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/context"
	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/image"
	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/initcmd"
	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/jail"
	jobcmd "github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/job"
	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/manifest"
	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/podman"
	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/qemu"
	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/secret"
	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/vfkit"
	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

// Stamped at build time with -X. These must be vars: -X cannot write to a
// constant, and a const here silently swallowed every release stamp, so each
// build reported the same number whatever commit it came from.
var (
	version   = "dev"
	buildTime = "unknown"
	gitCommit = "unknown"
)

var (
	apiURL        string
	apiKey        string
	contextName   string
	configPath    string
	tlsSkipVerify bool
	tlsCA         string
)

func main() {
	os.Exit(run())
}

func run() int {
	// Cancel the command context on SIGINT/SIGTERM so long-running operations
	// (exec, watch loops, image fetches) can shut down cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", dedupeClauses(err.Error()))
		fmt.Fprintln(os.Stderr, "Run 'hospitus --help' for usage.")
		return 1
	}
	return 0
}

var rootCmd = &cobra.Command{
	Use:   "hospitus",
	Short: "HOSPITUS - Universal Virtualization Management Platform",
	Long: `HOSPITUS is a unified interface for managing virtual machines and containers
across multiple providers (Jail, bhyve, QEMU, Podman, and on macOS
Virtualization.framework through vfkit and Apple's container runtime).

Provider-Specific Commands:
  hospitus jail <command>      - Manage FreeBSD jails
  hospitus bhyve <command>     - Manage bhyve VMs
  hospitus qemu <command>      - Manage QEMU VMs
  hospitus podman <command>    - Manage Podman containers
  hospitus vfkit <command>     - Manage Virtualization.framework VMs (macOS)
  hospitus container <command> - Manage Apple container runtime containers (macOS)

Power-User Aliases (CBSD-style):
  jcreate, jstart, jstop, jdestroy, jls, jexec, jlogin
  bcreate, bstart, bstop, bdestroy, bls, blogin
  qcreate, qstart, qstop, qdestroy, qls
  pstart, pstop, pdestroy, pls, pexec, plogin, plogs

Use "hospitus <provider> --help" for provider-specific commands.
Use "hospitus <alias> --help" for alias documentation.`,
	Version: fmt.Sprintf("%s (commit %s, built %s)", version, gitCommit, buildTime),
	// A failing command must not bury its error under the full usage block: with
	// 129 subcommands the message scrolls out of view, leaving a container that
	// could not be removed reporting only "1 container(s) failed to destroy".
	// run() below prints the error itself, so cobra stays quiet on both counts
	// rather than printing it a second time.
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// "hospitus context" subcommands manage the config file — skip API init
		if cmd.Parent() != nil && cmd.Parent().Name() == "context" {
			return nil
		}
		if cmd.Name() == "context" {
			return nil
		}
		return cmdutil.InitClientFromConfig(apiURL, apiKey, contextName, configPath, tlsSkipVerify, tlsCA)
	},
}

func init() {
	// Persistent flags — available to every subcommand
	rootCmd.PersistentFlags().StringVar(&apiURL, "api-url", "", "hospitusd URL (overrides context, e.g. https://host:8443)")
	rootCmd.PersistentFlags().StringVar(&apiKey, "api-key", "", "API key for authentication (overrides context)")
	rootCmd.PersistentFlags().StringVar(&contextName, "context", "", "Named context from config (~/.config/hospitus/config.yaml)")
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "Config file path (default: ~/.config/hospitus/config.yaml)")
	rootCmd.PersistentFlags().BoolVar(&tlsSkipVerify, "tls-skip-verify", false, "Skip TLS certificate verification (insecure)")
	rootCmd.PersistentFlags().StringVar(&tlsCA, "tls-ca", "", "CA certificate bundle (PEM) for custom TLS")

	// Define command groups for organized help output
	rootCmd.AddGroup(&cobra.Group{
		ID:    "providers",
		Title: "Provider Commands:",
	})
	rootCmd.AddGroup(&cobra.Group{
		ID:    "management",
		Title: "Management Commands:",
	})
	rootCmd.AddGroup(&cobra.Group{
		ID:    "aliases",
		Title: "Quick Aliases (CBSD-style):",
	})

	// Register provider commands (grouped)
	jailCmd := jail.NewJailCommand()
	jailCmd.GroupID = "providers"
	rootCmd.AddCommand(jailCmd)

	bhyveCmd := bhyve.NewBhyveCommand()
	bhyveCmd.GroupID = "providers"
	rootCmd.AddCommand(bhyveCmd)

	qemuCmd := qemu.NewQemuCommand()
	qemuCmd.GroupID = "providers"
	rootCmd.AddCommand(qemuCmd)

	podmanCmd := podman.NewPodmanCommand()
	podmanCmd.GroupID = "providers"
	rootCmd.AddCommand(podmanCmd)

	// macOS-only providers. They are registered unconditionally: the daemon
	// decides what it can offer, and a command that reports "provider not
	// found" is clearer than one that does not exist on the host you are
	// pointing at — the CLI routinely targets a machine other than its own.
	vfkitCmd := vfkit.NewVFKitCommand()
	vfkitCmd.GroupID = "providers"
	rootCmd.AddCommand(vfkitCmd)

	containerCmd := container.NewContainerCommand()
	containerCmd.GroupID = "providers"
	rootCmd.AddCommand(containerCmd)

	// Register management commands (grouped)
	imgCmd := image.NewImageCommand()
	imgCmd.GroupID = "management"
	rootCmd.AddCommand(imgCmd)

	backupCmd := backup.NewBackupCommand()
	backupCmd.GroupID = "management"
	rootCmd.AddCommand(backupCmd)

	jobCmd := jobcmd.NewJobCommand()
	jobCmd.GroupID = "management"
	rootCmd.AddCommand(jobCmd)

	manifestCmd := manifest.NewManifestCommand()
	manifestCmd.GroupID = "management"
	rootCmd.AddCommand(manifestCmd)

	applyCmd := manifest.NewApplyRootCommand()
	applyCmd.GroupID = "management"
	rootCmd.AddCommand(applyCmd)

	validateCmd := manifest.NewValidateRootCommand()
	validateCmd.GroupID = "management"
	rootCmd.AddCommand(validateCmd)

	secretCmd := secret.NewSecretCommand()
	secretCmd.GroupID = "management"
	rootCmd.AddCommand(secretCmd)

	initCmd := initcmd.NewInitCommand()
	initCmd.GroupID = "management"
	rootCmd.AddCommand(initCmd)

	ctxCmd := hospitusctx.NewContextCommand()
	ctxCmd.GroupID = "management"
	rootCmd.AddCommand(ctxCmd)

	// Man page and shell completion generation commands
	manCmd.GroupID = "management"
	rootCmd.AddCommand(manCmd)

	// Cobra built-in completion command is auto-registered; configure it
	rootCmd.CompletionOptions.DisableDefaultCmd = false

	// Register CBSD-style aliases (grouped)
	cmd.RegisterAliasesGrouped(rootCmd, "aliases")
}

// manCmd generates man pages for hospitus into a target directory.
var manCmd = &cobra.Command{
	Use:   "man [output-dir]",
	Short: "Generate man pages",
	Long: `Generate man(1) pages for all hospitus commands.

Output is written to OUTPUT-DIR (default: ./man). Install with:

  doas cp man/*.1 /usr/local/man/man1/

Then update the man database:

  doas makewhatis /usr/local/man`,
	Args:              cobra.MaximumNArgs(1),
	DisableAutoGenTag: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := "./man"
		if len(args) > 0 {
			dir = args[0]
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("cannot create output directory %q: %w", dir, err)
		}
		header := &doc.GenManHeader{
			Title:   "HOSPITUS",
			Section: "1",
			Source:  "hospitus",
			Manual:  "HOSPITUS Manual",
		}
		if err := doc.GenManTree(rootCmd, header, dir); err != nil {
			return fmt.Errorf("man page generation failed: %w", err)
		}
		// Count generated files
		entries, _ := filepath.Glob(filepath.Join(dir, "*.1"))
		fmt.Printf("Generated %d man page(s) in %s\n", len(entries), dir)
		fmt.Println("Install with:")
		fmt.Printf("  doas cp %s/*.1 /usr/local/man/man1/\n", dir)
		fmt.Println("  doas makewhatis /usr/local/man")
		return nil
	},
}
