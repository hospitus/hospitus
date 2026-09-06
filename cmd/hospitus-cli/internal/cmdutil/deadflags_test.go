package cmdutil

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	// A flag declared without binding a variable: cmd.Flags().String("name",
	// …). The value goes nowhere unless something looks it up by name later.
	// The P variants (StringP, BoolP, …) declare a shorthand as well and are
	// just as unreachable without a Get call, so they belong here too.
	declaredByName = regexp.MustCompile(
		`Flags\(\)\.(?:String|Bool|Int|Int64|StringSlice|StringArray|Duration)P?\(\s*([^,]+?),`)

	// Reading one back, by Get… or by asking whether it was set.
	readByName = regexp.MustCompile(
		`Flags\(\)\.(?:Get[A-Za-z0-9]+|Changed)\(\s*([^)]+?)\s*\)`)
)

// TestNoFlagIsDeclaredAndNeverRead finds flags the CLI accepts and discards.
//
// A flag declared by name — Flags().String("name", …) — can only be reached
// through Flags().GetString("name"). Without that call it parses, sets
// nothing, and the command still reports success. --cloud-init, --auto-start,
// --output and --network each did exactly that.
//
// A flag bound to a variable is out of scope: StringVar(&bridge, …) is read by
// reading bridge, which this cannot see.
func TestNoFlagIsDeclaredAndNeverRead(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("CLI sources not present at %s", root)
	}

	declared := map[string]string{} // flag name or constant -> where declared
	read := map[string]bool{}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source := string(content)

		if !strings.HasSuffix(path, "_test.go") {
			for _, m := range declaredByName.FindAllStringSubmatch(source, -1) {
				name := normalizeFlagRef(m[1])
				if _, seen := declared[name]; !seen {
					declared[name] = path
				}
			}
		}
		for _, m := range readByName.FindAllStringSubmatch(source, -1) {
			read[normalizeFlagRef(m[1])] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	if len(declared) == 0 {
		t.Fatal("no flags found; has the CLI layout changed?")
	}

	var dead []string
	for name, where := range declared {
		if !read[name] {
			dead = append(dead, name+" declared in "+where)
		}
	}
	sort.Strings(dead)

	for _, d := range dead {
		t.Errorf("flag is declared and never read, so the command accepts it and discards it: %s", d)
	}
	t.Logf("%d flags declared by name, all read back", len(declared))
}

// normalizeFlagRef drops the package qualifier, so FlagPull declared inside
// cmdutil and cmdutil.FlagPull read from a command are the same flag.
func normalizeFlagRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.LastIndex(ref, "."); i >= 0 {
		return ref[i+1:]
	}
	return ref
}
