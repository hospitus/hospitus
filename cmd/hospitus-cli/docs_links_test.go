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
			target, fragment := m[1], ""
			if i := strings.Index(target, "#"); i >= 0 {
				target, fragment = target[:i], target[i+1:]
			}
			if strings.HasPrefix(m[1], "http://") || strings.HasPrefix(m[1], "https://") ||
				strings.HasPrefix(m[1], "mailto:") {
				continue
			}

			// A same-page link is all fragment.
			page := path
			if target != "" {
				page = filepath.Join(filepath.Dir(path), target)
			}

			checked++
			rel, _ := filepath.Rel(root, path)
			if _, err := os.Stat(page); err != nil {
				broken = append(broken, rel+" -> "+m[1])
				continue
			}
			// The fragment too: mdBook renders a link to a heading that is not
			// there as ordinary text, and it goes nowhere when clicked.
			if fragment != "" && !hasAnchor(page, fragment) {
				broken = append(broken, rel+" -> "+m[1]+" (no such heading)")
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

// headingAnchor turns a Markdown heading into the anchor mdBook gives it:
// lowercased, non-alphanumerics dropped, spaces to hyphens.
func headingAnchor(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(heading)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// hasAnchor reports whether a page carries the heading, or an explicit HTML
// anchor, that a fragment names.
func hasAnchor(page, fragment string) bool {
	content, err := os.ReadFile(page)
	if err != nil {
		return false
	}
	want := strings.ToLower(fragment)
	source := string(content)
	if strings.Contains(source, `id="`+fragment+`"`) ||
		strings.Contains(source, `name="`+fragment+`"`) {
		return true
	}
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		if headingAnchor(strings.TrimLeft(trimmed, "# ")) == want {
			return true
		}
	}
	return false
}
