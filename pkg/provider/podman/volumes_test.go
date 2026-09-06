package podman

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManifestMountReachesPodman covers a bind mount written in a manifest.
//
// The converter puts a manifest's storage.volumes under "mounts", as maps, while
// this provider read only the CLI's "volumes" strings. A mount declared in a
// manifest was converted, printed by apply and then dropped: podman inspect
// showed no mounts at all.
func TestManifestMountReachesPodman(t *testing.T) {
	config := map[string]interface{}{
		"mounts": []interface{}{
			map[string]interface{}{
				"name":       "html",
				"host_path":  "/var/lib/hospitus/volumes/site",
				"mount_path": "/usr/share/nginx/html",
				"read_only":  true,
			},
		},
	}

	got := podmanVolumeStrings(config)
	want := "/var/lib/hospitus/volumes/site:/usr/share/nginx/html:ro"
	if len(got) != 1 || got[0] != want {
		t.Errorf("volume arguments = %v, want [%s]", got, want)
	}
}

// TestCLIVolumeStillReachesPodman keeps the -v form working.
func TestCLIVolumeStillReachesPodman(t *testing.T) {
	config := map[string]interface{}{
		"volumes": []interface{}{"/var/lib/hospitus/volumes/data:/data"},
	}

	got := podmanVolumeStrings(config)
	if len(got) != 1 || got[0] != "/var/lib/hospitus/volumes/data:/data" {
		t.Errorf("volume arguments = %v, want the -v string unchanged", got)
	}
}

// TestManifestMountWithoutReadOnlyIsWritable leaves off the :ro suffix.
func TestManifestMountWithoutReadOnlyIsWritable(t *testing.T) {
	config := map[string]interface{}{
		"mounts": []interface{}{
			map[string]interface{}{
				"host_path":  "/var/lib/hospitus/volumes/data",
				"mount_path": "/data",
			},
		},
	}

	got := podmanVolumeStrings(config)
	if len(got) != 1 || strings.HasSuffix(got[0], ":ro") {
		t.Errorf("volume arguments = %v, want no read-only suffix", got)
	}
}

// TestVolumeSourceStaysUnderHospitusPaths covers the restriction a manifest must
// not slip past now that manifests reach this provider.
func TestVolumeSourceStaysUnderHospitusPaths(t *testing.T) {
	tests := []struct {
		name    string
		volume  string
		wantErr bool
	}{
		{"a hospitus volume", "/var/lib/hospitus/volumes/site:/srv", false},
		{"a world-writable temp dir", "/tmp/hospitus-build:/build", true},
		{"an arbitrary host path", "/var/www/html:/srv", true},
		{"someone's home", "/home/other/.ssh:/keys", true},
		{"traversal", "/var/lib/hospitus/volumes/../../etc:/etc", true},
		{"no destination", "/var/lib/hospitus/volumes/site", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkPodmanVolume(tt.volume)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkPodmanVolume(%q) error = %v, want error: %v", tt.volume, err, tt.wantErr)
			}
		})
	}
}

// TestVolumeAllowlistResolvesSymlinks covers the escape a lexical prefix test
// left open: a symlink named to match an allowed prefix, pointing anywhere.
func TestVolumeAllowlistResolvesSymlinks(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := checkPodmanVolume(link + ":/host"); err == nil {
		t.Error("a symlink out of the allowed roots was accepted")
	}
}
