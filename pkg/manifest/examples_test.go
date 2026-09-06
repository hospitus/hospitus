package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

// mockSecretStore is a test secret store that returns fake secrets
type mockSecretStore struct{}

func (m *mockSecretStore) Get(scope, name string) (string, error) {
	return "test-secret-" + name, nil
}

func (m *mockSecretStore) Rotate(scope, name string) (string, error) {
	return "rotated-secret-" + name, nil
}

func (m *mockSecretStore) Remove(scope, name string) error {
	return nil
}

func (m *mockSecretStore) List(scope string) ([]string, error) {
	return []string{}, nil
}

// TestRenderExampleManifests verifies that example manifests render without errors
func TestRenderExampleManifests(t *testing.T) {
	// The shared discovery helper rather than a second walk of the same tree:
	// this one swallowed every directory error and had its own depth rule, so
	// the two tests could disagree on which examples exist.
	templates := findExampleTemplates(t)
	if len(templates) == 0 {
		t.Fatal("No example manifests found")
	}

	t.Logf("Found %d example manifests to test", len(templates))

	for _, templatePath := range templates {
		t.Run(filepath.Base(filepath.Dir(templatePath)), func(t *testing.T) {
			data, err := os.ReadFile(templatePath)
			if err != nil {
				t.Fatalf("Failed to read template: %v", err)
			}

			// Render with the variables an operator supplies, and mock secrets
			vars := exampleVars("test-instance")
			secrets := &mockSecretStore{}

			rendered, err := RenderTemplate(data, vars, secrets, "workload")
			if err != nil {
				t.Fatalf("Failed to render template: %v", err)
			}

			manifest, err := ParseString(string(rendered))
			if err != nil {
				t.Fatalf("Failed to parse rendered manifest: %v", err)
			}

			// Validate
			validator := NewValidator()
			if err := validator.Validate(manifest); err != nil && err.HasErrors() {
				t.Fatalf("Validation failed: %v", err)
			}

			t.Logf("✓ %s rendered and validated successfully", filepath.Dir(templatePath))
		})
	}
}

// TestRenderExampleStacks verifies that example stack manifests render without errors
func TestRenderExampleStacks(t *testing.T) {
	stackDir := "../../examples/manifests/stacks"

	// Find all template.toml files in stacks directory
	var templates []string
	err := filepath.Walk(stackDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && info.Name() == "template.toml" {
			templates = append(templates, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Failed to walk stack directory: %v", err)
	}

	if len(templates) == 0 {
		t.Skip("No example stacks found")
	}

	t.Logf("Found %d example stacks to test", len(templates))

	for _, templatePath := range templates {
		t.Run(filepath.Base(filepath.Dir(templatePath)), func(t *testing.T) {
			data, err := os.ReadFile(templatePath)
			if err != nil {
				t.Fatalf("Failed to read template: %v", err)
			}

			// Render with minimal variables and mock secrets
			vars := exampleVars("test-stack")
			secrets := &mockSecretStore{}

			rendered, err := RenderTemplate(data, vars, secrets, "stack")
			if err != nil {
				t.Fatalf("Failed to render template: %v", err)
			}

			manifest, err := ParseString(string(rendered))
			if err != nil {
				t.Fatalf("Failed to parse rendered manifest: %v", err)
			}

			// Validate
			validator := NewValidator()
			if err := validator.Validate(manifest); err != nil && err.HasErrors() {
				t.Fatalf("Validation failed: %v", err)
			}

			t.Logf("✓ %s rendered and validated successfully", filepath.Dir(templatePath))
		})
	}
}

// exampleVars are the variables the example harness supplies.
//
// "name" is what an operator always passes; the rest are the variables an
// example legitimately requires because no default could be right — a PCI
// selector names a slot on one host and nowhere else. Rendering refuses a
// manifest with an unsupplied variable, which is what makes this list
// necessary rather than optional.
func exampleVars(name string) map[string]any {
	return map[string]any{
		"name":          name,
		"gpu_pci":       "1/0/0",
		"gpu_audio_pci": "1/0/1",
	}
}
