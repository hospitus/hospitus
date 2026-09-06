package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// contextExemptions are the places a fresh root context is the right answer.
//
// main.go creates the one the whole command tree runs under, and init's
// testable core accepts a nil context from its own tests.
var contextExemptions = map[string]bool{
	"main.go":             true,
	"cmd/initcmd/init.go": true,
}

// TestCommandsUseTheCancellableContext keeps Ctrl-C working.
//
// The root command runs under a context canceled on SIGINT and SIGTERM, and
// cobra hands it to every command through cmd.Context(). A command that calls
// context.Background() instead runs to completion whatever the user presses,
// which on a long export or image fetch means the terminal returns while the
// work carries on.
func TestCommandsUseTheCancellableContext(t *testing.T) {
	root := "."
	var offenders []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, "./"))
		if contextExemptions[rel] {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, "context.Background()") {
				offenders = append(offenders, rel+":"+itoa(i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the command tree: %v", err)
	}

	for _, o := range offenders {
		t.Errorf("%s uses context.Background(); use cmd.Context() so Ctrl-C reaches the request", o)
	}
}

// itoa avoids pulling strconv in for one line number.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
