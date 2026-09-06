package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A health check is where an example manifest states what "working" means. The
// example test harness runs it inside the instance, so a manifest that declares
// nothing — or declares something that matches anything — reduces its own test
// to "the instance was created", which is equally true of a service that never
// started.
//
// These are the commands that succeed without proving anything. `pgrep .`
// matches every process on the system, including the one running the check.
var meaninglessHealthChecks = map[string]string{
	"pgrep .":       "matches every process, including the one running the check",
	"pgrep .*":      "matches every process, including the one running the check",
	"true":          "always succeeds",
	"/usr/bin/true": "always succeeds",
	"test -d /":     "the root directory always exists",
}

// runsCommandsInside reports whether a provider can run a health check inside
// the instance. jail and podman have an exec; a VM has no way in without
// credentials the examples do not set up, so the harness checks that those
// stay running instead.
func runsCommandsInside(providerType string) bool {
	return providerType == "jail" || providerType == "podman"
}

// exampleHealthChecks returns each instance's health-check command, keyed by
// instance name, for the instances whose provider can run one. A nil command
// means the instance declared none.
func exampleHealthChecks(m *ParsedManifest) map[string][]string {
	checks := make(map[string][]string)

	if m.Workload != nil && runsCommandsInside(m.Workload.Provider.Type) {
		var command []string
		if hc := m.Workload.Lifecycle.HealthCheck; hc != nil {
			command = hc.Command
		}
		checks[m.Workload.Workload.Name] = command
	}

	if m.Stack != nil {
		for i := range m.Stack.Instances {
			inst := &m.Stack.Instances[i]
			if !runsCommandsInside(inst.Provider) {
				continue
			}
			var command []string
			if hc := inst.Lifecycle.HealthCheck; hc != nil {
				command = hc.Command
			}
			checks[inst.Name] = command
		}
	}

	return checks
}

// TestExampleManifestsDeclareAUsableHealthCheck keeps the example harness
// honest. The harness derives its verification from the manifest, so this is
// what stops a manifest from silently contributing a test that cannot fail.
func TestExampleManifestsDeclareAUsableHealthCheck(t *testing.T) {
	for _, templatePath := range findExampleTemplates(t) {
		name := filepath.Base(filepath.Dir(templatePath))
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(templatePath)
			if err != nil {
				t.Fatalf("read template: %v", err)
			}

			rendered, err := RenderTemplate(data, map[string]any{"name": name}, &mockSecretStore{}, "workload")
			if err != nil {
				t.Fatalf("render template: %v", err)
			}
			parsed, err := ParseString(string(rendered))
			if err != nil {
				t.Fatalf("parse manifest: %v", err)
			}

			for instance, command := range exampleHealthChecks(parsed) {
				if len(command) == 0 {
					t.Errorf("instance %q declares no health_check, so its test cannot tell a working service from one that never started", instance)
					continue
				}
				joined := strings.Join(command, " ")
				if reason, bad := meaninglessHealthChecks[joined]; bad {
					t.Errorf("instance %q declares health_check %q, which %s", instance, joined, reason)
				}
			}
		})
	}
}

// findExampleTemplates returns every example manifest, which live at
// examples/manifests/<category>/<name>/template.toml.
func findExampleTemplates(t *testing.T) []string {
	t.Helper()

	root := filepath.Join("..", "..", "examples", "manifests")
	categories, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}

	var templates []string
	for _, category := range categories {
		if !category.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, category.Name()))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(root, category.Name(), entry.Name(), "template.toml")
			if _, err := os.Stat(path); err == nil {
				templates = append(templates, path)
			}
		}
	}

	if len(templates) == 0 {
		t.Fatalf("no example manifests found under %s", root)
	}
	return templates
}
