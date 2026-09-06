package jail

import (
	"reflect"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		wantErr  bool
	}{
		// Ordinary strings are quoted too. Quoting only what looks dangerous
		// requires the list of dangerous characters to be right, and it was
		// not: ";" was missing, so "foo;id" went through untouched into a
		// string /bin/sh -c would run.
		{"hello", "'hello'", false},
		{"hello123", "'hello123'", false},
		{"/bin/sh", "'/bin/sh'", false},
		{"ls", "'ls'", false},

		// Command separators and redirections
		{"foo;id", "'foo;id'", false},
		{"a&&b", "'a&&b'", false},
		{"a|b", "'a|b'", false},
		{"a>b", "'a>b'", false},
		{"$(id)", "'$(id)'", false},

		// Strings with spaces
		{"hello world", "'hello world'", false},
		{"my file.txt", "'my file.txt'", false},

		// Strings with single quotes
		{"it's", "'it'\"'\"'s'", false},
		{"don't", "'don'\"'\"'t'", false},

		// Strings with double quotes
		{`say "hello"`, `'say "hello"'`, false},

		// Strings with dollar sign
		{"$HOME", "'$HOME'", false},
		{"${VAR}", "'${VAR}'", false},

		// Strings with backtick
		{"`cmd`", "'`cmd`'", false},

		// Strings with backslash
		{`a\b`, `'a\b'`, false},

		// Strings with special shell chars
		{"hello*world", "'hello*world'", false},
		{"file?.txt", "'file?.txt'", false},
		{"cmd!arg", "'cmd!arg'", false},

		// Tab is allowed
		{"col1\tcol2", "'col1\tcol2'", false},

		// Empty string: '' keeps it one empty argument instead of none
		{"", "''", false},

		// Null byte — security: must return error (was silently truncating)
		{"hel\x00lo", "", true},

		// Control character (ASCII 1) — security: must return error (was silently truncating)
		{"hel\x01lo", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := shellQuote(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("shellQuote(%q) expected error, got %q", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Errorf("shellQuote(%q) unexpected error: %v", tt.input, err)
				return
			}
			if got != tt.expected {
				t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestBuildJexecArgs(t *testing.T) {
	tests := []struct {
		name        string
		jailName    string
		opts        provider.ExecOptions
		interactive bool
		want        []string
		wantErr     bool
	}{
		{
			name:        "simple command",
			jailName:    "web01",
			opts:        provider.ExecOptions{Command: "/bin/echo", Args: []string{"hello"}},
			interactive: false,
			want:        []string{"web01", "/bin/echo", "hello"},
		},
		{
			name:        "command with user",
			jailName:    "web01",
			opts:        provider.ExecOptions{Command: "/bin/echo", User: "root"},
			interactive: false,
			want:        []string{"-U", "root", "web01", "/bin/echo"},
		},
		{
			name:        "interactive default shell",
			jailName:    "myjail",
			opts:        provider.ExecOptions{},
			interactive: true,
			want:        []string{"myjail", "/bin/sh"},
		},
		{
			name:        "non-interactive no command",
			jailName:    "myjail",
			opts:        provider.ExecOptions{},
			interactive: false,
			wantErr:     true,
		},
		{
			name:        "command with workdir",
			jailName:    "web01",
			opts:        provider.ExecOptions{Command: "/bin/ls", WorkingDir: "tmp"},
			interactive: false,
			want:        []string{"web01", "/bin/sh", "-c", "cd 'tmp' && /bin/ls"},
		},
		{
			// A working directory is resolved inside the jail, where jexec
			// starts at the root, so the useful form is absolute — and it is
			// the only form the guides show.
			name:        "absolute workdir",
			jailName:    "web01",
			opts:        provider.ExecOptions{Command: "/bin/ls", WorkingDir: "/var/log"},
			interactive: false,
			want:        []string{"web01", "/bin/sh", "-c", "cd '/var/log' && /bin/ls"},
		},
		{
			// The wrapper above is a shell string, so an argument carrying a
			// separator must reach it quoted rather than as a second command.
			name:     "workdir argument cannot start a second command",
			jailName: "web01",
			opts: provider.ExecOptions{
				Command:    "/bin/ls",
				Args:       []string{"foo;id"},
				WorkingDir: "/tmp",
			},
			interactive: false,
			want:        []string{"web01", "/bin/sh", "-c", "cd '/tmp' && /bin/ls 'foo;id'"},
		},
		{
			name:        "shell mode: /bin/sh -c",
			jailName:    "web01",
			opts:        provider.ExecOptions{Command: "/bin/sh", Args: []string{"-c", "echo hello && ls /tmp"}},
			interactive: false,
			want:        []string{"web01", "/bin/sh", "-c", "echo hello && ls /tmp"},
		},
		{
			name:     "invalid user",
			jailName: "web01",
			opts:     provider.ExecOptions{Command: "/bin/ls", User: "../../root"},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildJexecArgs(tt.jailName, tt.opts, tt.interactive)
			if (err != nil) != tt.wantErr {
				t.Errorf("buildJexecArgs() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildJexecArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}
