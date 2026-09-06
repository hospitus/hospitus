package backup

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// seedBackups inserts n Full backups (Full type avoids the zfs-destroy path in
// DeleteBackup, so retention can be exercised without a real ZFS pool), oldest
// first, spaced one hour apart ending "now".
func seedBackups(bm *BackupManager, instanceID string, n int) {
	base := time.Now().Add(-time.Duration(n) * time.Hour)
	for i := 0; i < n; i++ {
		bm.addBackup(instanceID, &BackupInfo{
			ID:         fmt.Sprintf("bk-%02d", i),
			InstanceID: instanceID,
			Type:       BackupTypeFull,
			CreatedAt:  base.Add(time.Duration(i) * time.Hour),
		})
	}
}

func idsOf(bm *BackupManager, instanceID string) map[string]bool {
	set := make(map[string]bool)
	for _, b := range bm.ListBackups(instanceID) {
		set[b.ID] = true
	}
	return set
}

// TestApplyRetentionKeepLastDeterministic verifies KeepLast keeps exactly the N
// newest backups (audit: keepByInterval/applyRetention non-determinism, 576/547).
func TestApplyRetentionKeepLastDeterministic(t *testing.T) {
	bm := NewBackupManager("/tmp", datasetUnder("zroot/hospitus/backups"), nil)
	seedBackups(bm, "web", 30)

	bm.applyRetention(context.Background(), "web", RetentionPolicy{KeepLast: 5})

	kept := idsOf(bm, "web")
	if len(kept) != 5 {
		t.Fatalf("expected 5 kept, got %d: %v", len(kept), kept)
	}
	// The 5 newest are bk-25..bk-29.
	for i := 25; i < 30; i++ {
		id := fmt.Sprintf("bk-%02d", i)
		if !kept[id] {
			t.Errorf("expected newest backup %s to be kept, kept=%v", id, kept)
		}
	}
}

// TestApplyRetentionEmptyPolicyKeepsAll verifies that an all-zero retention
// policy deletes nothing (audit 532: empty policy wiped every backup).
func TestApplyRetentionEmptyPolicyKeepsAll(t *testing.T) {
	bm := NewBackupManager("/tmp", datasetUnder("zroot/hospitus/backups"), nil)
	seedBackups(bm, "web", 10)

	bm.applyRetention(context.Background(), "web", RetentionPolicy{})

	if got := len(bm.ListBackups("web")); got != 10 {
		t.Fatalf("empty policy must keep all backups, got %d of 10", got)
	}
}

// TestKeepByIntervalKeepsNewest verifies keepByInterval selects the newest
// buckets, not arbitrary map-order ones (audit 576).
func TestKeepByIntervalKeepsNewest(t *testing.T) {
	bm := NewBackupManager("/tmp", datasetUnder("zroot/hospitus/backups"), nil)
	seedBackups(bm, "web", 10)
	backups := bm.ListBackups("web")
	// newest first, as applyRetention sorts
	for i, j := 0, len(backups)-1; i < j; i, j = i+1, j-1 {
		backups[i], backups[j] = backups[j], backups[i]
	}
	keep := make(map[string]bool)
	bm.keepByInterval(backups, keep, 3, time.Hour)

	if len(keep) != 3 {
		t.Fatalf("expected 3 kept, got %d", len(keep))
	}
	for i := 7; i < 10; i++ {
		if !keep[fmt.Sprintf("bk-%02d", i)] {
			t.Errorf("expected newest hourly bucket bk-%02d kept, keep=%v", i, keep)
		}
	}
}
