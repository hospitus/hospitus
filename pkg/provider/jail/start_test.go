package jail

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestApplyMounts_PathValidation(t *testing.T) {
	// applyMounts validates mount paths to prevent path traversal and injection.
	// Invalid paths are silently skipped (warning logged) rather than returning an error.
	// Valid paths result in dir creation; we test that invalid paths are rejected.

	// A fake runner and a temp state dir: NewJailProvider carries a real execx
	// runner and the real state directory, so applyMounts would end with a
	// "mount -a -F <stateDir>/fstab/<name>" against the host.
	p := NewJailProvider()
	p.runner = &execx.Fake{}
	p.stateDir = t.TempDir()
	ctx := context.Background()

	tests := []struct {
		name        string
		hostPath    string
		hostTemp    bool
		mountPath   string
		wantErrLike string // expected substring in warning, empty means no rejection expected
	}{
		{
			name:        "host_path with dotdot traversal",
			hostPath:    "/etc/../passwd",
			mountPath:   "mnt/data",
			wantErrLike: "path cannot contain '..'",
		},
		{
			name:        "mount_path with dotdot traversal",
			hostTemp:    true,
			mountPath:   "mnt/../etc",
			wantErrLike: "path cannot contain '..'",
		},
		{
			name:        "host_path with null byte",
			hostPath:    "/etc/passwd\x00extra",
			mountPath:   "mnt/data",
			wantErrLike: "path cannot contain null bytes",
		},
		{
			name:        "empty host_path",
			hostPath:    "",
			mountPath:   "mnt/data",
			wantErrLike: "", // skipped silently (hostPath == "" → continue)
		},
		{
			name:        "empty mount_path",
			hostTemp:    true,
			mountPath:   "",
			wantErrLike: "", // skipped silently (mountPath == "" → continue)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jailRoot := filepath.Join(t.TempDir(), "jail")
			if err := os.MkdirAll(jailRoot, 0o755); err != nil {
				t.Fatal(err)
			}

			hostPath := tt.hostPath
			if tt.hostTemp {
				hostPath = filepath.Join(t.TempDir(), "hostdir")
			}

			config := &jailConfig{
				Name: "testjail",
				Path: jailRoot,
				Spec: provider.InstanceSpec{
					Name: "testjail",
					ProviderConfig: map[string]interface{}{
						"mounts": []interface{}{
							map[string]interface{}{
								"host_path":  hostPath,
								"mount_path": tt.mountPath,
								"read_only":  false,
							},
						},
					},
				},
			}

			err := p.applyMounts(ctx, config)
			if err != nil {
				t.Fatalf("applyMounts returned unexpected error: %v", err)
			}

			// For paths that should be rejected, verify no directory was created
			if tt.mountPath != "" {
				targetPath := filepath.Join(jailRoot, strings.TrimPrefix(tt.mountPath, "/"))
				if dirExists(targetPath) {
					t.Errorf("expected mount target directory NOT to be created (invalid path should be skipped): %s", targetPath)
				}
			}
		})
	}
}
