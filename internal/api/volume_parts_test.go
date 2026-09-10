package api

import "testing"

// TestVolumeNameFromParts verifies volume-name extraction uses the route suffix
// index (audit HIGH volume_handlers.go:206: it read parts[5] of a length-2
// suffix, so attach/detach never fired).
func TestVolumeNameFromParts(t *testing.T) {
	if got := volumeNameFromParts([]string{"volumes"}); got != "" {
		t.Errorf("list path: got %q, want empty", got)
	}
	if got := volumeNameFromParts([]string{"volumes", "data"}); got != "data" {
		t.Errorf("attach path: got %q, want data", got)
	}
}
