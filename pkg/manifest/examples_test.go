package manifest

import (
	"os"
	"path/filepath"
	"strings"
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
	manifestDir := "../../examples/manifests"

	// Find all template.toml files (excluding deep nested directories like node_modules)
	var templates []string
	err := filepath.Walk(manifestDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip inaccessible directories
		}
		// Only look at templates within 4 levels deep (category/service/template.toml)
		rel, _ := filepath.Rel(manifestDir, path)
		if strings.Count(rel, string(filepath.Separator)) > 2 {
			return filepath.SkipDir
		}
		if !info.IsDir() && info.Name() == "template.toml" {
			templates = append(templates, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Failed to walk manifest directory: %v", err)
	}

	if len(templates) == 0 {
		t.Fatal("No example manifests found")
	}

	t.Logf("Found %d example manifests to test", len(templates))

	var failed []string
	for _, templatePath := range templates {
		t.Run(filepath.Base(filepath.Dir(templatePath)), func(t *testing.T) {
			data, err := os.ReadFile(templatePath)
			if err != nil {
				t.Fatalf("Failed to read template: %v", err)
			}

			// Render with minimal variables and mock secrets
			vars := map[string]any{
				"name": "test-instance",
			}
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

	if len(failed) > 0 {
		t.Errorf("%d manifests failed: %v", len(failed), failed)
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
			vars := map[string]any{
				"name": "test-stack",
			}
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
