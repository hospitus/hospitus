package backup

import (
	"strings"
	"testing"
)

func TestRestorePromptNamesWhatIsOverwritten(t *testing.T) {
	// With --target the source is left alone; warning about its "current
	// state" points at the wrong instance.
	withTarget := restorePrompt("web-20260830", "web-clone")
	if !strings.Contains(withTarget, "web-clone") {
		t.Errorf("prompt does not name the target: %q", withTarget)
	}
	if strings.Contains(withTarget, "Current state will be lost") {
		t.Errorf("prompt threatens the source: %q", withTarget)
	}

	plain := restorePrompt("web-20260830", "")
	if !strings.Contains(plain, "Current state will be lost") {
		t.Errorf("prompt does not warn about the source: %q", plain)
	}
}
