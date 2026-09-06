package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// markdownLink matches [text](target), the only link form the book uses.
var markdownLink = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

// TestDocumentedLinksResolve follows every link between pages.
//
// Renaming or removing a page leaves the links to it pointing nowhere, and
// mdBook renders them as ordinary text that goes nowhere when clicked.
func TestDocumentedLinksResolve(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "book", "src")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("documentation not present at %s", root)
	}

	var broken []string
	checked := 0

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		for _, m := range markdownLink.FindAllStringSubmatch(string(content), -1) {
			target := m[1]
			if i := strings.Index(target, "#"); i >= 0 {
				target = target[:i] // an anchor within a page
			}
			if target == "" || strings.HasPrefix(target, "http://") ||
				strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") {
				continue
			}

			checked++
			resolved := filepath.Join(filepath.Dir(path), target)
			if _, err := os.Stat(resolved); err != nil {
				rel, _ := filepath.Rel(root, path)
				broken = append(broken, rel+" -> "+target)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	sort.Strings(broken)
	for _, b := range broken {
		t.Errorf("link points at a page that is not there: %s", b)
	}

	if checked == 0 {
		t.Fatal("no internal links found; has the layout changed?")
	}
	t.Logf("%d internal links resolve", checked)
}
