package dataset

import "testing"

// TestParentDefaultAndOverride verifies the parent dataset is configurable so
// Hospitus is not locked to a pool named "zroot" (audit HIGH: hardcoded zroot).
func TestParentDefaultAndOverride(t *testing.T) {
	t.Setenv("HOSPITUS_ZFS_PARENT", "")
	if got := Parent(); got != "zroot/hospitus" {
		t.Errorf("default Parent() = %q, want zroot/hospitus", got)
	}
	if got := Pool(); got != "zroot" {
		t.Errorf("default Pool() = %q, want zroot", got)
	}
	if got := Child("jails"); got != "zroot/hospitus/jails" {
		t.Errorf("default Child(jails) = %q", got)
	}

	t.Setenv("HOSPITUS_ZFS_PARENT", "tank/vms/")
	if got := Parent(); got != "tank/vms" {
		t.Errorf("override Parent() = %q, want tank/vms", got)
	}
	if got := Pool(); got != "tank" {
		t.Errorf("override Pool() = %q, want tank", got)
	}
	if got := Child("backups"); got != "tank/vms/backups" {
		t.Errorf("override Child(backups) = %q, want tank/vms/backups", got)
	}
}
