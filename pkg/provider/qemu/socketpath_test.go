package qemu

import (
	"strings"
	"testing"
)

func TestCheckSocketPathLimit(t *testing.T) {
	tests := []struct {
		name    string
		vmDir   string
		wantErr bool
	}{
		{"short path", "/var/lib/hospitus/qemu/web", false},
		{"at the limit", "/" + strings.Repeat("a", maxUnixSocketPath-len("/monitor.sock")-2), false},
		{"one past the limit", "/" + strings.Repeat("a", maxUnixSocketPath-len("/monitor.sock")), true},
		{"deep scratch directory", "/private/tmp/" + strings.Repeat("nested/", 20) + "vm", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkSocketPathLimit(tt.vmDir)
			if tt.wantErr && err == nil {
				t.Fatalf("checkSocketPathLimit(%q) = nil, want an error", tt.vmDir)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("checkSocketPathLimit(%q) = %v, want nil", tt.vmDir, err)
			}
			// The message has to name the flag the operator can act on.
			if err != nil && !strings.Contains(err.Error(), "--data-dir") {
				t.Errorf("error does not point at the data directory: %v", err)
			}
		})
	}
}
