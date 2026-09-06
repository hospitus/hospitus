package main

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestDocumentedCommandsExist resolves every hospitus command the documentation
// shows against the command tree.
//
// A guide that tells the reader to run something the binary does not have is
// worse than one that says nothing: they follow it, get "unknown command", and
// cannot tell whether they mistyped it or the page went stale. Renaming a
// subcommand is easy; finding every page that named it is not, and this is what
// notices.
//
// Words are followed as long as they can name subcommands, and a word offering
// alternatives — "create|list|info" — is checked branch by branch. The path
// ends at the first word that cannot: an instance name, a path, a flag or a
// placeholder, which the documentation is full of on purpose.
func TestDocumentedCommandsExist(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "book", "src")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("documentation not present at %s", root)
	}

	commands := documentedCommands(t, root)
	if len(commands) == 0 {
		t.Fatal("no hospitus commands found in the documentation; has the layout changed?")
	}

	// cobra adds "completion" and "help" during Execute, not at declaration, so
	// a tree enumerated before then is missing commands the docs rightly use.
	rootCmd.InitDefaultCompletionCmd()
	rootCmd.InitDefaultHelpCmd()

	var unknown []string
	for path, sources := range commands {
		if bad := unresolved(t, strings.Fields(path)); bad != "" {
			unknown = append(unknown, bad+"  (in `hospitus "+path+"`, "+sources[0]+")")
		}
	}
	sort.Strings(unknown)

	for _, u := range unknown {
		t.Errorf("the documentation names a command the CLI does not have: %s", u)
	}
	t.Logf("%d distinct command paths documented, all present", len(commands))
}

// unresolved returns the first word that should name a subcommand and does
// not, or "" when the whole path resolves.
//
// A command that groups others — "hospitus jail", with no Run of its own — must be
// followed by one of them. A leaf takes arguments, and whatever follows it is
// an instance name or a path, which this says nothing about.
func unresolved(t *testing.T, words []string) string {
	t.Helper()

	cmd := rootCmd
	for i, w := range words {
		child := childNamed(cmd, w)
		if child == nil {
			// Only a complaint if the command reached needs a subcommand.
			if cmd.Runnable() {
				return ""
			}
			return w
		}
		cmd = child
		_ = i
	}
	return ""
}

// childNamed finds a subcommand by name or alias.
func childNamed(parent *cobra.Command, name string) *cobra.Command {
	for _, sub := range parent.Commands() {
		if sub.Name() == name {
			return sub
		}
		for _, alias := range sub.Aliases {
			if alias == name {
				return sub
			}
		}
	}
	return nil
}

// documentedCommands maps each subcommand the docs invoke to the files that
// invoke it. Only fenced code blocks count: prose mentions "the hospitus anchor"
// and the like, which is not a command.
func documentedCommands(t *testing.T, root string) map[string][]string {
	t.Helper()

	found := make(map[string][]string)
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
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "```") {
				inBlock = !inBlock
				continue
			}
			if !inBlock {
				continue
			}

			// A console block prefixes its commands with the prompt.
			line = strings.TrimPrefix(line, "$ ")
			line = strings.TrimPrefix(line, "doas ")
			line = strings.TrimPrefix(line, "sudo ")
			if !strings.HasPrefix(line, "hospitus ") {
				continue
			}

			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			// Keep the words that could name subcommands; the first thing that
			// cannot — a flag, a placeholder, an instance name with a dot —
			// ends the path.
			//
			// A word may offer alternatives, as the reference does when it
			// summarizes a provider: "hospitus bhyve create|list|info|...". Each
			// branch names a command in its own right, and stopping at the "|"
			// checked only the provider name — leaving the densest lists in
			// the documentation, 54 subcommands across three providers,
			// unverified.
			var words []string
			var alternatives []string
			for _, w := range fields[1:] {
				if branches := strings.Split(w, "|"); len(branches) > 1 {
					allNames := true
					for _, b := range branches {
						if !looksLikeCommandName(b) {
							allNames = false
							break
						}
					}
					if allNames {
						alternatives = branches
					}
					break
				}
				if !looksLikeCommandName(w) {
					break
				}
				words = append(words, w)
			}
			if len(words) == 0 {
				continue
			}

			prefix := strings.Join(words, " ")
			if len(alternatives) == 0 {
				found[prefix] = append(found[prefix], path)
				continue
			}
			for _, alt := range alternatives {
				full := prefix + " " + alt
				found[full] = append(found[full], path)
			}
		}
		return scanner.Err()
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return found
}

// looksLikeCommandName reports whether a token could name a subcommand, which
// keeps the arrows and boxes of an ASCII diagram out of the results.
func looksLikeCommandName(s string) bool {
	// A dash is fine inside a name — "auto-start" — but a leading one makes it
	// a flag, and "hospitus --version" names no subcommand at all.
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-':
		default:
			return false
		}
	}
	return true
}
