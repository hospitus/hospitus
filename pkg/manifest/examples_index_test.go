package manifest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The examples index and the examples themselves, kept in agreement.
//
// Ninety-two of the README's ninety-eight links pointed at directories that do
// not exist: the catalog described a set of manifests that was never
// shipped, and nothing checked it.
func TestExamplesReadmeMatchesTheManifests(t *testing.T) {
	root := filepath.Join("..", "..", "examples")

	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}

	linked := map[string]bool{}
	for _, m := range regexp.MustCompile(`\]\((manifests/[^)]+)\)`).FindAllStringSubmatch(string(readme), -1) {
		path := strings.TrimSuffix(m[1], "/")
		linked[path] = true
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Errorf("README links %s, which does not exist", path)
		}
	}

	for _, template := range findExampleTemplates(t) {
		dir := filepath.Dir(template)
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			t.Fatalf("Rel(%q): %v", dir, err)
		}
		if !linked[filepath.ToSlash(rel)] {
			t.Errorf("%s is not listed in the README", rel)
		}
	}
}
