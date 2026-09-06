// Package context provides CLI commands for managing hospitusd server contexts.
//
// A context is a named connection profile (URL + API key + TLS options) that
// lets the hospitus CLI talk to different hospitusd instances without re-typing flags.
//
//	hospitus context add local  --url http://127.0.0.1:8080
//	hospitus context add prod   --url https://hospitus.example.com:8443 --api-key sk-...
//	hospitus context use prod
//	hospitus jail list           # ← talks to prod
package context

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

// NewContextCommand returns the root "hospitus context" command.
func NewContextCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Manage hospitusd server contexts",
		Long: `A context is a named connection profile that stores a hospitusd URL,
API key, and optional TLS settings.

Use contexts to switch between local, staging, and production servers
without setting environment variables on every command.

Examples:
  # Add a local dev server (no auth, plain HTTP)
  hospitus context add local --url http://127.0.0.1:8080

  # Add a remote production server with API key and TLS
  hospitus context add prod --url https://hospitus.prod.example.com:8443 --api-key sk-mykey

  # Add a remote server with a self-signed certificate
  hospitus context add lab --url https://192.168.10.5:8443 --api-key sk-lab --tls-ca /etc/hospitus/ca.pem

  # Switch to production
  hospitus context use prod

  # Show the active context
  hospitus context show

  # List all contexts
  hospitus context list

  # Delete a context
  hospitus context remove lab`,
	}

	cmd.AddCommand(newContextListCommand())
	cmd.AddCommand(newContextUseCommand())
	cmd.AddCommand(newContextAddCommand())
	cmd.AddCommand(newContextRemoveCommand())
	cmd.AddCommand(newContextShowCommand())

	return cmd
}

func newContextListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all configured contexts",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cmdutil.LoadConfig("")
			if err != nil {
				return err
			}

			if len(cfg.Contexts) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No contexts configured.")
				fmt.Fprintln(cmd.OutOrStdout(), `Add one with: hospitus context add <name> --url <url>`)
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ACTIVE\tNAME\tURL\tAUTH\tTLS")
			for _, c := range cfg.Contexts {
				active := " "
				if c.Name == cfg.CurrentContext {
					active = "*"
				}
				auth := "none"
				if c.APIKey != "" {
					auth = "key"
				}
				tlsInfo := "default"
				if c.TLSSkipVerify {
					tlsInfo = "skip-verify"
				} else if c.TLSCACert != "" {
					tlsInfo = "custom-ca"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", active, c.Name, c.URL, auth, tlsInfo)
			}
			return w.Flush()
		},
	}
}

func newContextUseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Set the active context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := cmdutil.LoadConfig("")
			if err != nil {
				return err
			}

			if cfg.GetContext(name) == nil {
				return fmt.Errorf("context %q not found — run 'hospitus context list' to see available contexts", name)
			}

			cfg.CurrentContext = name
			if err := cmdutil.SaveConfig("", cfg); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Switched to context %q (%s)\n", name, cfg.GetContext(name).URL)
			return nil
		},
	}
}

func newContextAddCommand() *cobra.Command {
	var (
		url           string
		apiKey        string
		tlsSkipVerify bool
		tlsCA         string
		setActive     bool
	)

	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add or update a context",
		Long: `Add a new context (or overwrite an existing one with the same name).

A context stores the hospitusd URL, API key, and TLS settings so you
don't have to pass them on every command.

The --url flag is required. Use --api-key to authenticate.
Use --tls-ca for servers with a self-signed certificate, or
--tls-skip-verify for a quick test (not recommended in production).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if url == "" {
				return fmt.Errorf("--url is required")
			}

			// Reading the key from stdin keeps it out of the process argument
			// list (visible via ps) and the shell history.
			if apiKey == "-" {
				fmt.Fprint(cmd.ErrOrStderr(), "Enter API key: ")
				line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if err != nil && err != io.EOF {
					return fmt.Errorf("failed to read API key from stdin: %w", err)
				}
				apiKey = strings.TrimSpace(line)
			}

			cfg, err := cmdutil.LoadConfig("")
			if err != nil {
				return err
			}

			isUpdate := cfg.GetContext(name) != nil
			ctx := cmdutil.Context{
				Name:          name,
				URL:           url,
				APIKey:        apiKey,
				TLSSkipVerify: tlsSkipVerify,
				TLSCACert:     tlsCA,
			}
			cfg.AddOrUpdateContext(ctx)

			if setActive || cfg.CurrentContext == "" {
				cfg.CurrentContext = name
			}

			if err := cmdutil.SaveConfig("", cfg); err != nil {
				return err
			}

			verb := "Added"
			if isUpdate {
				verb = "Updated"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s context %q → %s\n", verb, name, url)
			if setActive || cfg.CurrentContext == name {
				fmt.Fprintf(cmd.OutOrStdout(), "Active context is now %q\n", name)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&url, "url", "", "hospitusd URL, e.g. https://192.168.1.10:8443 (required)")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key for authentication (use '-' to read from stdin instead of the command line)")
	cmd.Flags().BoolVar(&tlsSkipVerify, "tls-skip-verify", false, "Skip TLS certificate verification (insecure)")
	cmd.Flags().StringVar(&tlsCA, "tls-ca", "", "Path to CA certificate bundle (PEM) for self-signed certs")
	cmd.Flags().BoolVar(&setActive, "set-active", false, "Make this the active context after adding")
	_ = cmd.MarkFlagRequired("url")

	return cmd
}

func newContextRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove a context",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := cmdutil.LoadConfig("")
			if err != nil {
				return err
			}

			if !cfg.RemoveContext(name) {
				return fmt.Errorf("context %q not found", name)
			}

			if cfg.CurrentContext == name {
				cfg.CurrentContext = ""
				fmt.Fprintf(cmd.OutOrStdout(), "Note: removed the active context — run 'hospitus context use <name>' to set a new one.\n")
			}

			if err := cmdutil.SaveConfig("", cfg); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Removed context %q\n", name)
			return nil
		},
	}
}

func newContextShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the active context",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cmdutil.LoadConfig("")
			if err != nil {
				return err
			}

			active := cfg.ActiveContext()
			if active == nil {
				if len(cfg.Contexts) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No contexts configured. Use 'hospitus context add' to get started.")
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "No active context — run 'hospitus context use <name>'.")
				}
				return nil
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Name:   %s\n", active.Name)
			fmt.Fprintf(cmd.OutOrStdout(), "URL:    %s\n", active.URL)
			if active.APIKey != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Auth:   API key (set)\n")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Auth:   none\n")
			}
			switch {
			case active.TLSSkipVerify:
				fmt.Fprintf(cmd.OutOrStdout(), "TLS:    skip-verify (insecure)\n")
			case active.TLSCACert != "":
				fmt.Fprintf(cmd.OutOrStdout(), "TLS:    custom CA (%s)\n", active.TLSCACert)
			default:
				fmt.Fprintf(cmd.OutOrStdout(), "TLS:    default (system CAs)\n")
			}
			return nil
		},
	}
}
