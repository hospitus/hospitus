package backup

import (
	"context"
	"testing"
	"time"
)

// datasetUnder builds a resolver that places every instance under one parent,
// which is what a single-provider host looks like to the manager.
func datasetUnder(parent string) DatasetResolver {
	return func(_ context.Context, instanceID string) (string, error) {
		return parent + "/" + instanceID, nil
	}
}

func TestNewBackupManager(t *testing.T) {
	bm := NewBackupManager("/var/lib/hospitus", datasetUnder("zroot/hospitus"), nil)

	if bm.dataDir != "/var/lib/hospitus" {
		t.Error("dataDir mismatch")
	}
	dataset, err := bm.datasetFor(context.Background(), "web")
	if err != nil || dataset != "zroot/hospitus/web" {
		t.Errorf("datasetFor = %q, %v; want zroot/hospitus/web", dataset, err)
	}
	if bm.configs == nil {
		t.Error("configs should be initialized")
	}
	if bm.backups == nil {
		t.Error("backups should be initialized")
	}
}

func TestConfigure(t *testing.T) {
	bm := NewBackupManager("/var/lib/hospitus", datasetUnder("zroot/hospitus"), nil)

	config := &BackupConfig{
		Enabled:  true,
		Schedule: "1h",
		Retention: RetentionPolicy{
			KeepLast: 5,
		},
	}

	err := bm.Configure("test-instance", config)
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	retrieved := bm.GetConfig("test-instance")
	if retrieved == nil {
		t.Fatal("Config not stored")
	}
	if !retrieved.Enabled {
		t.Error("Config should be enabled")
	}
	if retrieved.Schedule != "1h" {
		t.Error("Schedule mismatch")
	}

	// Clean up
	bm.RemoveConfig("test-instance")
	if bm.GetConfig("test-instance") != nil {
		t.Error("Config should be removed")
	}
}

func TestListBackups(t *testing.T) {
	bm := NewBackupManager("/var/lib/hospitus", datasetUnder("zroot/hospitus"), nil)

	// Add some test backups
	bm.addBackup("test-instance", &BackupInfo{
		ID:         "backup-1",
		InstanceID: "test-instance",
		Type:       BackupTypeSnapshot,
		Status:     BackupStatusCompleted,
		CreatedAt:  time.Now(),
	})
	bm.addBackup("test-instance", &BackupInfo{
		ID:         "backup-2",
		InstanceID: "test-instance",
		Type:       BackupTypeSnapshot,
		Status:     BackupStatusCompleted,
		CreatedAt:  time.Now(),
	})

	backups := bm.ListBackups("test-instance")
	if len(backups) != 2 {
		t.Errorf("Expected 2 backups, got %d", len(backups))
	}
}

func TestParseSchedule(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"hourly", time.Hour},
		{"daily", 24 * time.Hour},
		{"weekly", 7 * 24 * time.Hour},
		{"monthly", 30 * 24 * time.Hour},
		{"1h", time.Hour},
		{"6h", 6 * time.Hour},
		{"12h", 12 * time.Hour},
		{"24h", 24 * time.Hour},
		{"30m", 30 * time.Minute},
		{"invalid", 0},
	}

	for _, tt := range tests {
		got := parseSchedule(tt.input)
		if got != tt.want {
			t.Errorf("parseSchedule(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestKeepByInterval_ZeroCount(t *testing.T) {
	bm := NewBackupManager("/tmp", datasetUnder("zroot"), nil)
	keep := make(map[string]bool)
	// count=0 means keep nothing via this interval
	bm.keepByInterval(nil, keep, 0, time.Hour)
	if len(keep) != 0 {
		t.Error("keepByInterval with count=0 should add nothing to keep")
	}
}

func TestKeepByInterval_KeepsDistinctBuckets(t *testing.T) {
	bm := NewBackupManager("/tmp", datasetUnder("zroot"), nil)
	now := time.Now()
	backups := []*BackupInfo{
		{ID: "b1", CreatedAt: now.Add(-1 * time.Hour)},
		{ID: "b2", CreatedAt: now.Add(-2 * time.Hour)},
		{ID: "b3", CreatedAt: now.Add(-3 * time.Hour)},
	}
	keep := make(map[string]bool)
	bm.keepByInterval(backups, keep, 2, time.Hour)
	if len(keep) != 2 {
		t.Errorf("expected 2 entries in keep, got %d", len(keep))
	}
}

func TestKeepByInterval_AllSameBucket(t *testing.T) {
	bm := NewBackupManager("/tmp", datasetUnder("zroot"), nil)
	// All within the same minute — same hourly bucket
	now := time.Now().Truncate(time.Hour)
	backups := []*BackupInfo{
		{ID: "x1", CreatedAt: now.Add(1 * time.Minute)},
		{ID: "x2", CreatedAt: now.Add(2 * time.Minute)},
		{ID: "x3", CreatedAt: now.Add(3 * time.Minute)},
	}
	keep := make(map[string]bool)
	bm.keepByInterval(backups, keep, 5, time.Hour)
	// Only 1 bucket, so only 1 kept
	if len(keep) != 1 {
		t.Errorf("expected 1 entry in keep (same bucket), got %d", len(keep))
	}
}

func TestFindPreviousSnapshot_Empty(t *testing.T) {
	bm := NewBackupManager("/tmp", datasetUnder("zroot"), nil)
	result := bm.findPreviousSnapshot("nonexistent")
	if result != "" {
		t.Errorf("expected empty string for unknown instance, got %q", result)
	}
}

func TestFindPreviousSnapshot_NoCompleted(t *testing.T) {
	bm := NewBackupManager("/tmp", datasetUnder("zroot"), nil)
	bm.backups["inst1"] = []*BackupInfo{
		{ID: "b1", InstanceID: "inst1", Type: BackupTypeFull, Status: BackupStatusFailed, SnapshotName: "snap1"},
		{ID: "b2", InstanceID: "inst1", Type: BackupTypeFull, Status: BackupStatusRunning, SnapshotName: "snap2"},
	}
	result := bm.findPreviousSnapshot("inst1")
	if result != "" {
		t.Errorf("expected empty string when no completed backup, got %q", result)
	}
}

func TestFindPreviousSnapshot_ReturnsLastCompleted(t *testing.T) {
	bm := NewBackupManager("/tmp", datasetUnder("zroot"), nil)
	bm.backups["inst1"] = []*BackupInfo{
		{
			ID: "b1", InstanceID: "inst1", Type: BackupTypeFull, Status: BackupStatusCompleted,
			SnapshotName: "snap1", Dataset: "zroot/inst1", Location: "/backups/b1.zfs",
		},
		{
			ID: "b2", InstanceID: "inst1", Type: BackupTypeFull, Status: BackupStatusFailed,
			SnapshotName: "snap2", Dataset: "zroot/inst1",
		},
		{
			ID: "b3", InstanceID: "inst1", Type: BackupTypeIncr, Status: BackupStatusCompleted,
			SnapshotName: "snap3", Dataset: "zroot/inst1", Location: "/backups/b3.zfs",
		},
	}
	// The dataset comes from the record, not from the instance name: an instance
	// deleted since the backup was taken still has its snapshot where it was.
	result := bm.findPreviousSnapshot("inst1")
	if result != "zroot/inst1@snap3" {
		t.Errorf("previous snapshot = %q, want zroot/inst1@snap3", result)
	}
}

// TestFindPreviousSnapshotSkipsRecordsWithNoStream covers an incremental that
// chained onto a snapshot-type backup: that record has no stream file anywhere,
// so the resulting stream had no stored base and could not be restored from the
// backup files alone.
func TestFindPreviousSnapshotSkipsRecordsWithNoStream(t *testing.T) {
	bm := NewBackupManager("/tmp", datasetUnder("zroot"), nil)
	bm.backups["inst1"] = []*BackupInfo{
		{
			ID: "full", InstanceID: "inst1", Type: BackupTypeFull, Status: BackupStatusCompleted,
			SnapshotName: "snap1", Dataset: "zroot/inst1", Location: "/backups/full.zfs",
		},
		// A local snapshot: no stream was written for it.
		{
			ID: "snap", InstanceID: "inst1", Type: BackupTypeSnapshot, Status: BackupStatusCompleted,
			SnapshotName: "snap2", Dataset: "zroot/inst1",
		},
		// A full backup whose stream never landed.
		{
			ID: "nolocation", InstanceID: "inst1", Type: BackupTypeFull, Status: BackupStatusCompleted,
			SnapshotName: "snap3", Dataset: "zroot/inst1",
		},
	}

	if got := bm.findPreviousSnapshot("inst1"); got != "zroot/inst1@snap1" {
		t.Errorf("previous snapshot = %q, want the last backup with a stored stream", got)
	}
}

func TestApplyRetention_EmptyBackups(t *testing.T) {
	bm := NewBackupManager("/tmp", datasetUnder("zroot"), nil)
	// Should not panic on empty backups
	bm.applyRetention(context.TODO(), "nonexistent", RetentionPolicy{KeepLast: 3})
}

func TestApplyRetention_KeepLast(t *testing.T) {
	bm := NewBackupManager("/tmp", datasetUnder("zroot"), nil)
	now := time.Now()
	bm.backups["inst1"] = []*BackupInfo{
		{
			ID: "b1", InstanceID: "inst1", Status: BackupStatusCompleted, SnapshotName: "s1",
			CreatedAt: now.Add(-3 * 24 * time.Hour),
		},
		{
			ID: "b2", InstanceID: "inst1", Status: BackupStatusCompleted, SnapshotName: "s2",
			CreatedAt: now.Add(-2 * 24 * time.Hour),
		},
		{
			ID: "b3", InstanceID: "inst1", Status: BackupStatusCompleted, SnapshotName: "s3",
			CreatedAt: now.Add(-1 * 24 * time.Hour),
		},
	}
	// These backups have no Type, so DeleteBackup only removes them from the
	// in-memory list (no ZFS is touched). KeepLast:2 must retain the two newest
	// (b2, b3) and prune the oldest (b1).
	bm.applyRetention(context.TODO(), "inst1", RetentionPolicy{
		KeepLast: 2,
		MinAge:   0, // no min age so pruning is attempted
	})

	bm.mu.RLock()
	remaining := append([]*BackupInfo(nil), bm.backups["inst1"]...)
	bm.mu.RUnlock()

	if len(remaining) != 2 {
		t.Fatalf("expected 2 backups to remain, got %d", len(remaining))
	}
	kept := map[string]bool{}
	for _, b := range remaining {
		kept[b.ID] = true
	}
	if !kept["b2"] || !kept["b3"] {
		t.Errorf("expected b2 and b3 to remain, got %v", kept)
	}
	if kept["b1"] {
		t.Errorf("expected b1 (oldest) to be pruned, but it remains")
	}
}
