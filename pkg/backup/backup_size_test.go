package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupSizeMeasuresTheFileItWrote(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "web-20260830.zfs.zst")
	if err := os.WriteFile(file, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}

	// A fresh snapshot's "used" is zero; the stream on disk is not.
	if got := backupSize(file, 0); got != 4096 {
		t.Errorf("backupSize = %d, want 4096", got)
	}
}

func TestBackupSizeFallsBackForARemoteStream(t *testing.T) {
	// A stream received on another host has no local file to measure.
	if got := backupSize("backup-host:tank/hospitus", 2048); got != 2048 {
		t.Errorf("backupSize = %d, want the snapshot figure 2048", got)
	}
	if got := backupSize("", 512); got != 512 {
		t.Errorf("backupSize = %d, want 512", got)
	}
}

func TestVerifyStreamCatchesADamagedFile(t *testing.T) {
	dir := t.TempDir()
	bm := &BackupManager{}
	ctx := context.Background()

	// A file whose extension promises zstd but whose bytes are not.
	broken := filepath.Join(dir, "web-20260830.zfs.zst")
	if err := os.WriteFile(broken, []byte("not a compressed stream"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := bm.verifyStream(ctx, &BackupInfo{ID: "web-1", Location: broken}); err == nil {
		t.Error("a damaged stream was reported intact")
	}

	// An empty file is not a backup either.
	empty := filepath.Join(dir, "web-empty.zfs.zst")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := bm.verifyStream(ctx, &BackupInfo{ID: "web-2", Location: empty}); err == nil {
		t.Error("an empty stream was reported intact")
	}
}

func TestVerifyStreamLeavesARemoteStreamToItsHost(t *testing.T) {
	bm := &BackupManager{}
	err := bm.verifyStream(context.Background(), &BackupInfo{ID: "web-3", Location: "backup-host:tank/hospitus"})
	if err == nil {
		t.Fatal("a stream on another host was reported verified")
	}
	if !strings.Contains(err.Error(), "backup-host") {
		t.Errorf("error does not name the host: %v", err)
	}
}
