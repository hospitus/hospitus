package manifest

import (
	"os"
	"testing"
)

func TestRenderTemplate(t *testing.T) {
	// Pin a whitelisted env var to a known value so the env-function case is
	// deterministic rather than a tautology against the ambient environment.
	t.Setenv("TZ", "UTC")

	tests := []struct {
		name     string
		raw      string
		vars     map[string]any
		expected string
		wantErr  bool
	}{
		{
			name:     "basic substitution",
			raw:      `name = "{{ .name }}"`,
			vars:     map[string]any{"name": "test-workload"},
			expected: `name = "test-workload"`,
		},
		{
			name:     "default function - used",
			raw:      `port = {{ .port | default 80 }}`,
			vars:     map[string]any{"port": 8080},
			expected: `port = 8080`,
		},
		{
			name:     "default function - fallback",
			raw:      `port = {{ .port | default 80 }}`,
			vars:     map[string]any{},
			expected: `port = 80`,
		},
		{
			name:     "env function",
			raw:      `tz = "{{ env "TZ" }}"`,
			vars:     map[string]any{},
			expected: `tz = "UTC"`,
		},
		{
			name:     "env function - non-whitelisted returns empty",
			raw:      `secret = "{{ env "AWS_SECRET_ACCESS_KEY" }}"`,
			vars:     map[string]any{},
			expected: `secret = ""`,
		},
		{
			name:     "nested variables",
			raw:      `path = "{{ .apps_dir }}/{{ .name }}"`,
			vars:     map[string]any{"apps_dir": "/nas/apps", "name": "searxng"},
			expected: `path = "/nas/apps/searxng"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RenderTemplate([]byte(tt.raw), tt.vars, nil, "")
			if (err != nil) != tt.wantErr {
				t.Errorf("RenderTemplate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if string(got) != tt.expected {
				t.Errorf("RenderTemplate() = %v, want %v", string(got), tt.expected)
			}
		})
	}
}

func TestRenderTemplateWithSecrets(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "hospitus-secrets-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	store := NewFileSecretStore(tempDir)
	scope := "test-scope"

	raw := `password = "{{ secret "db_pass" }}"`

	// First render
	rendered1, err := RenderTemplate([]byte(raw), nil, store, scope)
	if err != nil {
		t.Fatalf("First render failed: %v", err)
	}

	// Second render - should be identical
	rendered2, err := RenderTemplate([]byte(raw), nil, store, scope)
	if err != nil {
		t.Fatalf("Second render failed: %v", err)
	}

	if string(rendered1) != string(rendered2) {
		t.Errorf("Secrets not persistent: %s != %s", string(rendered1), string(rendered2))
	}

	// Rotate and check
	_, err = store.Rotate(scope, "db_pass")
	if err != nil {
		t.Fatal(err)
	}

	rendered3, err := RenderTemplate([]byte(raw), nil, store, scope)
	if err != nil {
		t.Fatal(err)
	}

	if string(rendered1) == string(rendered3) {
		t.Error("Secret did not change after rotation")
	}
}
