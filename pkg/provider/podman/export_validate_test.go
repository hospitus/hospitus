package podman

import "testing"

func TestValidateExportPath(t *testing.T) {
	p := &PodmanProvider{dataDir: "/var/lib/hospitus"}

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "within data dir", path: "/var/lib/hospitus/export.tar", wantErr: false},
		{name: "data dir itself", path: "/var/lib/hospitus", wantErr: false},
		{name: "empty path", path: "", wantErr: true},
		{name: "relative path", path: "export.tar", wantErr: true},
		{name: "outside data dir", path: "/etc/passwd", wantErr: true},
		{name: "traversal escape", path: "/var/lib/hospitus/../../etc/cron.d/x", wantErr: true},
		{name: "sibling prefix collision", path: "/var/lib/hospitus-evil/x.tar", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.validateExportPath(tt.path)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error for %q, got nil", tt.path)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.path, err)
			}
		})
	}
}

func TestValidateExportPathRequiresDataDir(t *testing.T) {
	p := &PodmanProvider{}
	if err := p.validateExportPath("/var/lib/hospitus/x.tar"); err == nil {
		t.Fatal("expected error when data directory is not configured")
	}
}
