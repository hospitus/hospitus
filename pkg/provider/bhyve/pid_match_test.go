package bhyve

import "testing"

// TestCommandIsBhyveVM verifies exact VM-name matching for both shapes a bhyve
// process is seen in: the command line it is executed with, and the title it
// replaces that command line with once running.
//
// Exactness matters in both, so a VM whose name is a prefix of another is not
// matched (audit HIGH bhyve_lifecycle.go:1096 / bhyve_boot.go:103: pgrep
// matched substrings and SIGKILLed / reniced the wrong VM).
func TestCommandIsBhyveVM(t *testing.T) {
	tests := []struct {
		command string
		vmName  string
		want    bool
	}{
		{"bhyve -c 2 -s 0,hostbridge -s 3,virtio-blk,/d.img web", "web", true},
		{"bhyve -c 2 -s 0,hostbridge web-prod", "web", false}, // prefix, not exact
		{"bhyve -c 2 -s 0,hostbridge web", "web-prod", false},
		{"bhyve -s 3,virtio-blk,/vms/web/disk0 web2", "web", false}, // name in path only
		{"sleep 60 web", "web", false},                              // not bhyve
		{"", "web", false},

		// The title bhyve gives itself once running, which is what ps reports
		// for every live VM. Matching only the final argument answered false
		// here, so a running VM read as stopped and its PID was cleared.
		{"bhyve: web (bhyve)", "web", true},
		{"bhyve: web-prod (bhyve)", "web", false}, // prefix, not exact
		{"bhyve: web (bhyve)", "web-prod", false},
		{"bhyve: (bhyve)", "web", false},
		{"bhyve:", "web", false},
	}
	for _, tt := range tests {
		if got := commandIsBhyveVM(tt.command, tt.vmName); got != tt.want {
			t.Errorf("commandIsBhyveVM(%q, %q) = %v, want %v", tt.command, tt.vmName, got, tt.want)
		}
	}
}
