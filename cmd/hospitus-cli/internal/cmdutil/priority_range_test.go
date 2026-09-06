package cmdutil

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestBootPriorityRangeIsStatedConsistently guards a range that has already
// drifted once.
//
// AutoStartConfig.Priority is 1-100: zero is its "not specified" value, which
// the provider replaces with the default 50, so a CLI that advertises 0-100
// accepts a number it then silently discards. The range is written out in flag
// help, long help and validation messages across several files, none of which
// the compiler relates to the others — fixing two of them left
// AddBootAutoStartFlags still promising 0-100 to every provider that uses it.
func TestBootPriorityRangeIsStatedConsistently(t *testing.T) {
	root := filepath.Join("..", "..")

	var stale []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(content), "\n") {
			if !mentionsPriority(line) {
				continue
			}
			if strings.Contains(line, "0-100") || strings.Contains(line, "0 and 100") {
				stale = append(stale, filepath.ToSlash(path)+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	for _, where := range stale {
		t.Errorf("priority is documented as 0-100 at %s, but 0 means \"not specified\" "+
			"and the provider replaces it with 50; the range is 1-100", where)
	}
}

// mentionsPriority keeps the check to lines that are actually about the
// auto-start priority, so an unrelated 0-100 (a percentage, a health score)
// does not fail this.
func mentionsPriority(line string) bool {
	l := strings.ToLower(line)
	return strings.Contains(l, "priority") || strings.Contains(l, "boot order")
}
