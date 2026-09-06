package manifest

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// tomlBlock matches a fenced TOML block in a Markdown file.
var tomlBlock = regexp.MustCompile("(?s)```toml\n(.*?)```")

// TestDocumentedManifestsParse runs every complete manifest in the
// documentation through the parser and the validator.
//
// A manifest that does not parse is one a reader cannot use, and the mismatch
// is invisible on the page: a plausible-looking key the schema has never had
// reads the same as a correct one.
func TestDocumentedManifestsParse(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "book", "src")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("documentation not present at %s", root)
	}

	parser := NewParser(&mockSecretStore{})
	validator := NewValidator()

	checked := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		for i, match := range tomlBlock.FindAllStringSubmatch(string(content), -1) {
			block := match[1]
			if !isCompleteManifest(block) {
				continue
			}
			checked++

			rel, _ := filepath.Rel(root, path)
			name := rel + " block " + strconv.Itoa(i)

			parsed, err := parser.Parse([]byte(block), "docs", nil)
			if err != nil {
				t.Errorf("%s does not parse: %v", name, err)
				continue
			}
			if errs := validator.Validate(parsed); len(errs) > 0 {
				t.Errorf("%s is not valid: %v", name, errs)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	if checked == 0 {
		t.Fatal("no complete manifests found in the documentation; has the layout changed?")
	}
	t.Logf("%d documented manifests parse and validate", checked)
}

// isCompleteManifest reports whether a block is meant to stand on its own.
//
// The guides are full of fragments showing one section at a time — the
// specification opens with a [workload] table and nothing else. Feeding those
// to the validator would report a missing provider and image for something
// that never claimed to be a whole file, so a block counts only when it names
// its api_version and either a provider or the instances of a stack.
func isCompleteManifest(block string) bool {
	hasHeader := strings.Contains(block, "[workload]") || strings.Contains(block, "[stack]")
	hasBody := strings.Contains(block, "[provider]") || strings.Contains(block, "[[instances]]")
	return hasHeader && hasBody
}
