package backup

import (
	"context"
	"testing"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

// storeWithInstance builds a datastore holding one instance, which is what the
// manager reads its configurations back from.
func storeWithInstance(t *testing.T, id string) *datastore.Datastore {
	t.Helper()
	ds, err := datastore.NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("NewDatastore: %v", err)
	}
	t.Cleanup(func() { ds.Close() })

	instance := &datastore.Instance{
		ID:       id,
		Name:     id,
		Provider: "jail",
		State:    provider.StateRunning,
	}
	instance.Spec.Name = id
	if err := ds.CreateInstance(context.Background(), instance); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	return ds
}

// TestConfigurationSurvivesARestart covers a schedule that stopped silently.
//
// The configuration handler updated in-memory maps only, and Start reloaded
// nothing, so after a daemon restart no backup ran again and nothing said so.
func TestConfigurationSurvivesARestart(t *testing.T) {
	ds := storeWithInstance(t, "web")

	first := NewBackupManager(t.TempDir(), datasetUnder("zroot"), ds)
	config := &BackupConfig{
		Enabled:        true,
		Schedule:       "daily",
		Destination:    "/backups",
		Compression:    "zstd",
		PreBackupHook:  "/usr/local/etc/hospitus/hooks/pre",
		PostBackupHook: "/usr/local/etc/hospitus/hooks/post",
		Retention:      RetentionPolicy{KeepLast: 7, KeepDaily: 3},
	}
	if err := first.Configure("web", config); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	first.Stop()

	second := NewBackupManager(t.TempDir(), datasetUnder("zroot"), ds)
	if err := second.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer second.Stop()

	got := second.GetConfig("web")
	if got == nil {
		t.Fatal("the configuration did not come back")
	}
	// The whole configuration, not the part that fits in a summary: a schedule
	// without its retention or its hooks is a different configuration.
	if got.Schedule != "daily" || got.Destination != "/backups" || got.Compression != "zstd" {
		t.Errorf("schedule/destination/compression came back as %q/%q/%q", got.Schedule, got.Destination, got.Compression)
	}
	if got.PreBackupHook != config.PreBackupHook || got.PostBackupHook != config.PostBackupHook {
		t.Errorf("hooks came back as %q/%q", got.PreBackupHook, got.PostBackupHook)
	}
	if got.Retention.KeepLast != 7 || got.Retention.KeepDaily != 3 {
		t.Errorf("retention came back as %+v", got.Retention)
	}
}

// TestBackupRecordsSurviveARestart covers backup IDs that became unknown.
//
// Records lived in a map only, so after a restart an existing backup could no
// longer be listed, verified, restored or deleted.
func TestBackupRecordsSurviveARestart(t *testing.T) {
	ds := storeWithInstance(t, "web")

	first := NewBackupManager(t.TempDir(), datasetUnder("zroot"), ds)
	first.Seed("web", &BackupInfo{
		ID:           "web-20260101-000000",
		InstanceID:   "web",
		Type:         BackupTypeSnapshot,
		Status:       BackupStatusCompleted,
		Dataset:      "zroot/hospitus/jails/web",
		SnapshotName: "hospitus-backup-20260101-000000",
	})

	second := NewBackupManager(t.TempDir(), datasetUnder("zroot"), ds)
	if err := second.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer second.Stop()

	got := second.GetBackup("web-20260101-000000")
	if got == nil {
		t.Fatal("the backup record did not come back")
	}
	if got.Dataset != "zroot/hospitus/jails/web" || got.SnapshotName != "hospitus-backup-20260101-000000" {
		t.Errorf("record came back as %+v", got)
	}
	if len(second.ListBackups("web")) != 1 {
		t.Errorf("the instance lists %d backups, want 1", len(second.ListBackups("web")))
	}
}
