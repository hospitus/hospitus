package main

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// documentedFlag matches a long flag as the documentation writes one, either
// bare in a command line or wrapped in backticks in a table.
var documentedFlag = regexp.MustCompile(`--[a-z][a-z0-9-]*`)

// TestDocumentedFlagsExist resolves every flag the documentation shows against
// the command it shows it on.
//
// Only invented flags are reported: they make the reader's command fail. A
// flag the binary has and the page omits is a gap rather than a breakage, and
// reporting every one would bury the rest.
func TestDocumentedFlagsExist(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "book", "src")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("documentation not present at %s", root)
	}

	rootCmd.InitDefaultCompletionCmd()
	rootCmd.InitDefaultHelpCmd()

	var unknown []string
	for use, flags := range documentedFlags(t, root) {
		cmd := resolveCommand(strings.Fields(use))
		if cmd == nil {
			// The command itself is checked by TestDocumentedCommandsExist;
			// nothing useful to say about the flags of something absent.
			continue
		}
		for flag, source := range flags {
			if commandAcceptsFlag(cmd, flag) {
				continue
			}
			unknown = append(unknown, "hospitus "+use+" has no "+flag+"  ("+source+")")
		}
	}
	sort.Strings(unknown)

	for _, u := range unknown {
		t.Errorf("the documentation shows a flag the command does not accept: %s", u)
	}
}

// commandAcceptsFlag reports whether cmd or any parent declares the flag.
func commandAcceptsFlag(cmd *cobra.Command, flag string) bool {
	name := strings.TrimPrefix(flag, "--")
	if cmd.Flags().Lookup(name) != nil {
		return true
	}
	if cmd.InheritedFlags().Lookup(name) != nil {
		return true
	}
	for p := cmd.Parent(); p != nil; p = p.Parent() {
		if p.PersistentFlags().Lookup(name) != nil {
			return true
		}
	}
	return false
}

// resolveCommand walks the command tree, returning nil when a word names
// nothing.
func resolveCommand(words []string) *cobra.Command {
	cmd := rootCmd
	for _, w := range words {
		child := childNamed(cmd, w)
		if child == nil {
			return nil
		}
		cmd = child
	}
	return cmd
}

// documentedFlags maps a command path to the flags the documentation shows on
// it, each with the file it came from.
//
// Only fenced code blocks count, and only lines that invoke hospitus: prose
// mentions flags in passing, and a table lists them without saying which
// command they belong to. A line ending in a backslash carries its command
// path to the next, which is how most examples are written.
func documentedFlags(t *testing.T, root string) map[string]map[string]string {
	t.Helper()

	found := make(map[string]map[string]string)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		inBlock := false
		var pending string // command path of a line continued with a backslash
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "```") {
				inBlock = !inBlock
				pending = ""
				continue
			}
			if !inBlock {
				continue
			}

			continued := strings.HasSuffix(line, "\\")
			line = strings.TrimSpace(strings.TrimSuffix(line, "\\"))

			use := pending
			if use == "" {
				use = commandPathOf(line)
				if use == "" {
					pending = ""
					continue
				}
			}

			for _, flag := range documentedFlag.FindAllString(line, -1) {
				if found[use] == nil {
					found[use] = make(map[string]string)
				}
				found[use][flag] = path
			}

			if continued {
				pending = use
			} else {
				pending = ""
			}
		}
		return scanner.Err()
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return found
}

// commandPathOf returns the subcommand path a line invokes, or "" when the
// line is not a hospitus command.
func commandPathOf(line string) string {
	line = strings.TrimPrefix(line, "$ ")
	line = strings.TrimPrefix(line, "doas ")
	line = strings.TrimPrefix(line, "sudo ")
	if !strings.HasPrefix(line, "hospitus ") {
		return ""
	}

	// A global flag may precede the subcommand: "hospitus --context prod apply".
	// Skip those, and the value of any that takes one.
	fields := strings.Fields(line)[1:]
	for len(fields) > 0 && strings.HasPrefix(fields[0], "-") {
		flag := strings.TrimPrefix(strings.TrimPrefix(fields[0], "-"), "-")
		fields = fields[1:]
		// A global flag that takes a value swallows the next word too, unless
		// it was written as --flag=value.
		if !strings.Contains(flag, "=") && len(fields) > 0 &&
			rootCmd.PersistentFlags().Lookup(flag) != nil &&
			rootCmd.PersistentFlags().Lookup(flag).Value.Type() != "bool" {
			fields = fields[1:]
		}
	}

	var words []string
	for _, w := range fields {
		if !looksLikeCommandName(w) {
			break
		}
		words = append(words, w)
	}
	if len(words) == 0 {
		return ""
	}
	return strings.Join(words, " ")
}
