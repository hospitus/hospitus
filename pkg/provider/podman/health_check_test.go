package podman

import "testing"

// TestHealthcheckConfigured verifies detection trims the trailing newline from
// `podman inspect` output, so a container without a healthcheck is not reported
// as having one (audit HIGH health.go:119: hasHealthCheck always returned true
// because "<nil>\n" != "<nil>").
func TestHealthcheckConfigured(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"<nil>\n", false},
		{"<nil>", false},
		{"", false},
		{"[]\n", false},
		{"{}\n", false},
		{"&{[CMD-SHELL curl -f http://localhost/] 30s 30s 0s 3}\n", true},
		{"map[Test:[CMD curl]]\n", true},
	}
	for _, tt := range tests {
		if got := healthcheckConfigured(tt.raw); got != tt.want {
			t.Errorf("healthcheckConfigured(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}
