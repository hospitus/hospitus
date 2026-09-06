package backup

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gzipStream writes a well-formed .zfs.gz file, which is what a compressed
// backup looks like on disk and what `gzip -t` accepts.
func gzipStream(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := gzip.NewWriter(f)
	if _, err := w.Write([]byte("stream-contents")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestVerifiedSurvivesARestart covers a verification that was recorded in
// memory only: after a daemon restart every backup showed verified: false
// again.
func TestVerifiedSurvivesARestart(t *testing.T) {
	ds := storeWithInstance(t, "web")
	dir := t.TempDir()

	first := NewBackupManager(t.TempDir(), datasetUnder("zroot"), ds)
	first.Seed("web", &BackupInfo{
		ID:         "web-20260101-000000",
		InstanceID: "web",
		Type:       BackupTypeFull,
		Status:     BackupStatusCompleted,
		Location:   gzipStream(t, dir, "web-20260101-000000.zfs.gz"),
	})

	if err := first.VerifyBackup(context.Background(), "web-20260101-000000"); err != nil {
		t.Fatalf("VerifyBackup: %v", err)
	}

	second := NewBackupManager(t.TempDir(), datasetUnder("zroot"), ds)
	if err := second.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer second.Stop()

	got := second.GetBackup("web-20260101-000000")
	if got == nil {
		t.Fatal("the backup record did not come back")
	}
	if !got.Verified {
		t.Error("the verification did not survive the restart")
	}
}

// TestVerifyIncrementalChecksItsBase covers an incremental that verified while
// the full backup it chains onto was gone: it restores nothing.
func TestVerifyIncrementalChecksItsBase(t *testing.T) {
	dir := t.TempDir()
	bm := NewBackupManager(t.TempDir(), datasetUnder("zroot"), nil)
	bm.Seed("web", &BackupInfo{
		ID:           "web-incr",
		InstanceID:   "web",
		Type:         BackupTypeIncr,
		Status:       BackupStatusCompleted,
		Location:     gzipStream(t, dir, "web-incr.zfs.gz"),
		BaseSnapshot: "zroot/web@hospitus-backup-gone",
	})

	err := bm.VerifyBackup(context.Background(), "web-incr")
	if err == nil {
		t.Fatal("an incremental verified with no base on the pool")
	}
	if !strings.Contains(err.Error(), "zroot/web@hospitus-backup-gone") {
		t.Errorf("error does not name the missing base: %v", err)
	}
}

// TestVerifyIncrementalWithoutARecordedBaseWarns keeps records written before
// base tracking verifiable: their chain is unknown, which is warned about
// rather than failed.
func TestVerifyIncrementalWithoutARecordedBaseWarns(t *testing.T) {
	dir := t.TempDir()
	bm := NewBackupManager(t.TempDir(), datasetUnder("zroot"), nil)
	bm.Seed("web", &BackupInfo{
		ID:         "web-old-incr",
		InstanceID: "web",
		Type:       BackupTypeIncr,
		Status:     BackupStatusCompleted,
		Location:   gzipStream(t, dir, "web-old-incr.zfs.gz"),
	})

	if err := bm.VerifyBackup(context.Background(), "web-old-incr"); err != nil {
		t.Fatalf("a record from before base tracking must not fail verification: %v", err)
	}
}

// TestDeleteRefusesABaseAnIncrementalNeeds covers a full backup that was
// deleted out from under the incremental chained onto it.
func TestDeleteRefusesABaseAnIncrementalNeeds(t *testing.T) {
	bm := NewBackupManager(t.TempDir(), datasetUnder("zroot"), nil)
	bm.Seed("web", &BackupInfo{
		ID: "web-full", InstanceID: "web", Type: BackupTypeFull, Status: BackupStatusCompleted,
		Dataset: "zroot/web", SnapshotName: "hospitus-backup-1",
	})
	bm.Seed("web", &BackupInfo{
		ID: "web-incr", InstanceID: "web", Type: BackupTypeIncr, Status: BackupStatusCompleted,
		Dataset: "zroot/web", SnapshotName: "hospitus-backup-2",
		BaseSnapshot: "zroot/web@hospitus-backup-1",
	})

	err := bm.DeleteBackup(context.Background(), "web-full")
	if err == nil {
		t.Fatal("the base of an incremental was deleted")
	}
	if !strings.Contains(err.Error(), "web-incr") {
		t.Errorf("error does not name the incremental that needs it: %v", err)
	}
	if bm.GetBackup("web-full") == nil {
		t.Error("the refused backup was dropped from the list anyway")
	}
}

// TestRetentionKeepsABaseAnIncrementalNeeds covers the same chain under a
// retention policy, which deletes without being asked.
func TestRetentionKeepsABaseAnIncrementalNeeds(t *testing.T) {
	bm := NewBackupManager(t.TempDir(), datasetUnder("zroot"), nil)
	now := time.Now()
	bm.Seed("web", &BackupInfo{
		ID: "web-full", InstanceID: "web", Type: BackupTypeFull, Status: BackupStatusCompleted,
		Dataset: "zroot/web", SnapshotName: "hospitus-backup-1", CreatedAt: now.Add(-72 * time.Hour),
	})
	// An older backup nothing chains onto: retention may take it.
	bm.Seed("web", &BackupInfo{
		ID: "web-stale", InstanceID: "web", Type: BackupTypeFull, Status: BackupStatusCompleted,
		CreatedAt: now.Add(-96 * time.Hour),
	})
	bm.Seed("web", &BackupInfo{
		ID: "web-incr", InstanceID: "web", Type: BackupTypeIncr, Status: BackupStatusCompleted,
		Dataset: "zroot/web", SnapshotName: "hospitus-backup-2", CreatedAt: now,
		BaseSnapshot: "zroot/web@hospitus-backup-1",
	})

	bm.applyRetention(context.Background(), "web", RetentionPolicy{KeepLast: 1})

	if bm.GetBackup("web-full") == nil {
		t.Error("retention deleted the base the newest incremental needs")
	}
	if bm.GetBackup("web-stale") != nil {
		t.Error("retention kept a backup nothing chains onto")
	}
}

// TestParseSendSize covers the size estimate a remote backup is measured by:
// nothing lands locally to stat, so `zfs send -nvP` is asked instead.
func TestParseSendSize(t *testing.T) {
	output := "full\tzroot/web@hospitus-backup-1\t350224096\nsize\t350224096\n"
	got, err := parseSendSize(output)
	if err != nil {
		t.Fatalf("parseSendSize: %v", err)
	}
	if got != 350224096 {
		t.Errorf("size = %d, want 350224096", got)
	}
	if _, err := parseSendSize("nothing here\n"); err == nil {
		t.Error("output with no size line was accepted")
	}
}
