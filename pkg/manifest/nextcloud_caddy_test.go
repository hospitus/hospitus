package manifest

import (
	"os"
	"strings"
	"testing"
)

func TestNextcloudCaddyfileDeniesTheSensitivePaths(t *testing.T) {
	data, err := os.ReadFile("../../examples/manifests/stacks/nextcloud/template.toml")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderTemplate(data, map[string]any{"name": "nextcloud"}, &mockSecretStore{}, "stack")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	text := string(rendered)
	for _, path := range []string{"/data/*", "/config/*", "/db_structure.xml", "/occ", "/console.php"} {
		if !strings.Contains(text, path) {
			t.Errorf("the Caddyfile does not deny %s", path)
		}
	}
	if !strings.Contains(text, "respond @forbidden 404") {
		t.Error("the @forbidden matcher is defined but never answered")
	}
}
