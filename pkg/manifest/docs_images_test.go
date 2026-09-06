package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// imageReference matches a catalog image named in a manifest or on a command
// line: cloud:ubuntu-24.04-amd64, iso:…, set:….
var imageReference = regexp.MustCompile(`\b(?:cloud|iso|set):([a-z0-9][a-z0-9._-]*)`)

// TestDocumentedImagesExist resolves every catalog image the documentation
// names.
//
// The manifest check validates the shape of an image source, not whether the
// image is there, so cloud:ubuntu-24.04 passed both it and the reader's eye —
// and answered "not found" at the only moment that counts.
func TestDocumentedImagesExist(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "book", "src")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("documentation not present at %s", root)
	}

	known, err := catalogNames()
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}

	missing := map[string]string{}
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		for _, m := range imageReference.FindAllStringSubmatch(string(content), -1) {
			name := m[1]
			if known[name] {
				continue
			}
			// A page that documents the error names the bad spelling on
			// purpose, and says so on the same line.
			if strings.Contains(path, "cli-reference.md") {
				continue
			}
			if _, seen := missing[name]; !seen {
				missing[name] = path
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", root, walkErr)
	}

	var names []string
	for name, where := range missing {
		names = append(names, name+"  ("+where+")")
	}
	sort.Strings(names)

	for _, n := range names {
		t.Errorf("the documentation names an image the catalog does not have: %s", n)
	}
}

// catalogNames reads the image names hospitus ships with.
func catalogNames() (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join("..", "image", "catalog.json"))
	if err != nil {
		return nil, err
	}

	var profiles []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &profiles); err != nil {
		return nil, err
	}

	names := make(map[string]bool, len(profiles))
	for _, p := range profiles {
		names[p.Name] = true
	}
	return names, nil
}

// documentedCatalogTotal matches the count a page prints under an
// "image available" listing.
var documentedCatalogTotal = regexp.MustCompile(`Total: (\d+) images? available`)

// TestDocumentedCatalogTotalMatches keeps the totals the book prints in step
// with the catalog.
//
// The images page said "Total: 39 images available" against a catalog holding
// 56, which is the sort of number a reader checks their own output against.
func TestDocumentedCatalogTotalMatches(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "book", "src")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("documentation not present at %s", root)
	}

	known, err := catalogNames()
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	want := strconv.Itoa(len(known))

	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range documentedCatalogTotal.FindAllStringSubmatch(string(content), -1) {
			if match[1] != want {
				t.Errorf("%s says %s images available; the catalog holds %s",
					filepath.Base(path), match[1], want)
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk documentation: %v", walkErr)
	}
}
