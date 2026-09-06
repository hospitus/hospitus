package manifest

import (
	"strings"
	"testing"
)

func TestDetectNameStaysInItsSection(t *testing.T) {
	p := &Parser{}
	cases := []struct {
		name, manifest, kind, want string
	}{
		{"workload names itself", "[workload]\nname = \"web\"\nversion = \"1\"\n", "workload", "web"},
		{"comment above the name", "[workload]\n# the VM\nname = \"web\"\n", "workload", "web"},
		{"stack names itself", "[stack]\nname = \"app\"\n\n[[instances]]\nname = \"db\"\n", "stack", "app"},
		// The case the bounded pattern exists for.
		{"nameless workload does not borrow one", "[workload]\nversion = \"1\"\n\n[network]\nname = \"br0\"\n", "workload", "default"},
		{"nameless stack does not borrow one", "[stack]\nversion = \"1\"\n\n[[instances]]\nname = \"db\"\n", "stack", "default"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := p.detectName([]byte(c.manifest), c.kind)
			if err != nil {
				t.Fatalf("detectName: %v", err)
			}
			if got != c.want {
				t.Errorf("detectName = %q, want %q", got, c.want)
			}
		})
	}
}

// A manifest whose cloud-init block holds lines that look like table headers.
// The scan is line-oriented, so without blanking those bodies it stopped at
// the first "[" inside the string — or took a name from it.
func TestDetectNameIgnoresMultilineStrings(t *testing.T) {
	p := &Parser{}

	manifest := "[workload]\n" +
		"user_data = \"\"\"\n" +
		"#cloud-config\n" +
		"[not-a-table]\n" +
		"name = \"borrowed-from-a-string\"\n" +
		"\"\"\"\n" +
		"name = \"web\"\n"

	got, err := p.detectName([]byte(manifest), "workload")
	if err != nil {
		t.Fatalf("detectName: %v", err)
	}
	if got != "web" {
		t.Errorf("detectName = %q, want %q", got, "web")
	}
}

// An indented table header still ends the section. The scan used to accept
// any line whose first character was not "[", so leading whitespace let it
// read straight through into the next table.
func TestDetectNameStopsAtAnIndentedHeader(t *testing.T) {
	p := &Parser{}

	manifest := "[workload]\n" +
		"version = \"1\"\n" +
		"\n" +
		"  [network]\n" +
		"  name = \"br0\"\n"

	got, err := p.detectName([]byte(manifest), "workload")
	if err != nil {
		t.Fatalf("detectName: %v", err)
	}
	if got != "default" {
		t.Errorf("detectName = %q, want %q", got, "default")
	}
}

// A """ in a comment opens nothing. Treating it as a delimiter blanked
// everything up to the next occurrence, and the name went with it.
func TestDetectNameIgnoresDelimitersInComments(t *testing.T) {
	p := &Parser{}

	manifest := "[workload]\n" +
		"# the user_data below is written with \"\"\" in the real manifest\n" +
		"name = \"web\"\n"

	got, err := p.detectName([]byte(manifest), "workload")
	if err != nil {
		t.Fatalf("detectName: %v", err)
	}
	if got != "web" {
		t.Errorf("detectName = %q, want %q", got, "web")
	}
}

// A whitespace-only line inside the section, and a name written as a TOML
// literal string. The scan required every intermediate line to be empty or to
// start with a printable character, and only read the basic-string form.
func TestDetectNameAcceptsBlankLinesAndLiteralStrings(t *testing.T) {
	p := &Parser{}

	manifest := "[workload]\n" +
		"version = \"1\"\n" +
		"   \n" +
		"name = 'web'\n"

	got, err := p.detectName([]byte(manifest), "workload")
	if err != nil {
		t.Fatalf("detectName: %v", err)
	}
	if got != "web" {
		t.Errorf("detectName = %q, want %q", got, "web")
	}
}

// A workload whose cloud-init block contains a line reading "[stack]".
// detectManifestType scanned the raw document for the substring, decided the
// manifest held both sections, and refused it for something it does not have.
func TestDetectManifestTypeIgnoresMultilineStrings(t *testing.T) {
	p := &Parser{}

	manifest := "[workload]\n" +
		"name = \"web\"\n" +
		"\n" +
		"[cloud_init]\n" +
		"user_data = \"\"\"\n" +
		"#cloud-config\n" +
		"write_files:\n" +
		"  - content: |\n" +
		"      [stack]\n" +
		"      name = \"not-a-manifest\"\n" +
		"\"\"\"\n"

	got, err := p.detectManifestType([]byte(manifest))
	if err != nil {
		t.Fatalf("detectManifestType: %v", err)
	}
	if got != "workload" {
		t.Errorf("detectManifestType = %q, want %q", got, "workload")
	}
}

// A real stack is still detected, and a document carrying both real sections
// is still refused.
func TestDetectManifestTypeStillSeesRealSections(t *testing.T) {
	p := &Parser{}

	if got, err := p.detectManifestType([]byte("[stack]\nname = \"app\"\n")); err != nil || got != "stack" {
		t.Errorf("detectManifestType = %q, %v; want \"stack\", nil", got, err)
	}
	if _, err := p.detectManifestType([]byte("[workload]\n\n[stack]\n")); err == nil {
		t.Error("a manifest with both sections was accepted")
	}
}

// TOML allows a comment after a table header, and a value or comment elsewhere
// may contain the word "[workload]" without opening a section.
func TestDetectManifestTypeReadsHeadersNotSubstrings(t *testing.T) {
	p := &Parser{}

	cases := []struct {
		name, manifest, want string
	}{
		{
			"header with a trailing comment",
			"[workload]  # the VM this file describes\nname = \"web\"\n",
			"workload",
		},
		{
			"the word in a single-line value",
			"[stack]\nname = \"app\"\ndescription = \"replaces the old [workload] form\"\n",
			"stack",
		},
		{
			"the word in a comment",
			"[stack]\n# migrated from a [workload] manifest\nname = \"app\"\n",
			"stack",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := p.detectManifestType([]byte(c.manifest))
			if err != nil {
				t.Fatalf("detectManifestType: %v", err)
			}
			if got != c.want {
				t.Errorf("detectManifestType = %q, want %q", got, c.want)
			}
		})
	}
}

// A variable the caller did not supply renders as "<no value>" and lands in a
// manifest field — "cloud:<no value>" as an image source — where it surfaces
// much later as a download that cannot resolve.
func TestRenderTemplateRefusesAnUnsuppliedVariable(t *testing.T) {
	_, err := RenderTemplate([]byte("[workload]\nname = \"web\"\nsource = \"cloud:{{ .release }}\"\n"),
		map[string]any{}, nil, "web")
	if err == nil {
		t.Fatal("a manifest with an unsupplied variable rendered without complaint")
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("the error does not say where: %v", err)
	}
	// The line itself stays out of it: a rendered line can hold a secret.
	if strings.Contains(err.Error(), "cloud:") {
		t.Errorf("the error quotes the rendered line: %v", err)
	}
}

// A manifest whose own text contains that marker is not refused for carrying
// it: the check counts what rendering added, not what was already there.
func TestRenderTemplateAllowsTheMarkerInSourceText(t *testing.T) {
	source := "[workload]\nname = \"web\"\n" +
		"description = \"prints <no value> when unset\"\n"
	out, err := RenderTemplate([]byte(source), map[string]any{}, nil, "web")
	if err != nil {
		t.Fatalf("a literal marker in the manifest was refused: %v", err)
	}
	if !strings.Contains(string(out), "<no value>") {
		t.Error("the literal text did not survive rendering")
	}
}

// A literal marker that rendering removes, plus a variable that is genuinely
// missing. Counting the markers before and after — as an earlier version did —
// balanced the two out and let the missing variable through.
func TestRenderTemplateSeesAMissingVariableBesideARemovedLiteral(t *testing.T) {
	source := "[workload]\n" +
		"{{ if false }}note = \"<no value>\"{{ end }}\n" +
		"source = \"cloud:{{ .release }}\"\n"

	if _, err := RenderTemplate([]byte(source), map[string]any{}, nil, "web"); err == nil {
		t.Fatal("the missing variable went unnoticed")
	}
}
