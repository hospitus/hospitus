package jail

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

func TestSnapshotProviderInterfaceAssertion(t *testing.T) {
	var _ provider.SnapshotProvider = (*JailProvider)(nil)
}

func TestSnapshotNameValidation(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid simple", input: "snap1", wantErr: false},
		{name: "valid with dash", input: "pre-upgrade", wantErr: false},
		{name: "valid with underscore", input: "pre_upgrade", wantErr: false},
		{name: "valid with dot", input: "v1.0.0", wantErr: false},
		{name: "valid version", input: "backup.2026-06-01", wantErr: false},
		{name: "empty", input: "", wantErr: true},
		{name: "too long", input: func() string {
			s := make([]byte, 64)
			for i := range s {
				s[i] = 'a'
			}
			return string(s)
		}(), wantErr: true},
		{name: "starts with dash", input: "-snap", wantErr: true},
		{name: "starts with dot", input: ".snap", wantErr: true},
		{name: "contains slash", input: "snap/1", wantErr: true},
		{name: "path traversal", input: "snap/../etc", wantErr: true},
		{name: "command injection", input: "snap;rm -rf /", wantErr: true},
		{name: "contains space", input: "my snap", wantErr: true},
		{name: "contains backtick", input: "snap`id`", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validation.ValidateSnapshotName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSnapshotName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestCreateSnapshotMissingZFSDataset(t *testing.T) {
	p := &JailProvider{}
	ctx := t.Context()

	handle := provider.InstanceHandle{
		ID:       "myjail",
		Provider: "jail",
		Metadata: map[string]interface{}{}, // no zfs_dataset
	}

	_, err := p.CreateSnapshot(ctx, handle, "snap1")
	if err == nil {
		t.Fatal("Expected error for missing zfs_dataset metadata")
	}
}

func TestCreateSnapshotInvalidName(t *testing.T) {
	p := &JailProvider{}
	ctx := t.Context()

	handle := provider.InstanceHandle{
		ID:       "myjail",
		Provider: "jail",
		Metadata: map[string]interface{}{
			"zfs_dataset": "zroot/hospitus/jails/myjail",
		},
	}

	_, err := p.CreateSnapshot(ctx, handle, "../evil")
	if err == nil {
		t.Fatal("Expected error for invalid snapshot name")
	}
}

func TestDeleteSnapshotMissingZFSName(t *testing.T) {
	p := &JailProvider{}
	ctx := t.Context()

	snapshot := provider.SnapshotHandle{
		ID:       "myjail_snap1",
		Instance: "myjail",
		Metadata: map[string]interface{}{}, // no zfs_name
	}

	err := p.DeleteSnapshot(ctx, snapshot)
	if err == nil {
		t.Fatal("Expected error for missing zfs_name metadata")
	}
}

func TestRestoreSnapshotInstanceMismatch(t *testing.T) {
	p := &JailProvider{}
	ctx := t.Context()

	handle := provider.InstanceHandle{
		ID:       "myjail",
		Provider: "jail",
	}
	snapshot := provider.SnapshotHandle{
		ID:       "other_snap1",
		Instance: "otherjail", // doesn't match handle.ID
		Metadata: map[string]interface{}{
			"zfs_name": "zroot/hospitus/jails/otherjail@snap1",
		},
	}

	err := p.RestoreSnapshot(ctx, handle, snapshot)
	if err == nil {
		t.Fatal("Expected error for instance mismatch")
	}
}

func TestListSnapshotsMissingZFSDataset(t *testing.T) {
	p := &JailProvider{}
	ctx := t.Context()

	handle := provider.InstanceHandle{
		ID:       "myjail",
		Provider: "jail",
		Metadata: map[string]interface{}{}, // no zfs_dataset
	}

	_, err := p.ListSnapshots(ctx, handle)
	if err == nil {
		t.Fatal("Expected error for missing zfs_dataset metadata")
	}
}

func TestSnapshotHandleConstruction(t *testing.T) {
	// Verify the snapshot handle ID format
	jailName := "web01"
	snapName := "pre-upgrade"
	expectedID := fmt.Sprintf("%s_%s", jailName, snapName)

	handle := provider.SnapshotHandle{
		ID:       expectedID,
		Instance: jailName,
		Metadata: map[string]interface{}{
			"dataset":       "zroot/hospitus/jails/web01",
			"snapshot_name": snapName,
			"zfs_name":      "zroot/hospitus/jails/web01@pre-upgrade",
			"created":       time.Now().Format(time.RFC3339),
		},
	}

	if handle.ID != "web01_pre-upgrade" {
		t.Errorf("Handle ID = %q, want %q", handle.ID, "web01_pre-upgrade")
	}
	if handle.Instance != "web01" {
		t.Errorf("Instance = %q, want %q", handle.Instance, "web01")
	}
	if handle.Metadata["zfs_name"] != "zroot/hospitus/jails/web01@pre-upgrade" {
		t.Errorf("zfs_name = %v, want zroot/hospitus/jails/web01@pre-upgrade", handle.Metadata["zfs_name"])
	}
}

func TestSnapshotInfoFields(t *testing.T) {
	created := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	info := provider.SnapshotInfo{
		Handle: provider.SnapshotHandle{
			ID:       "web01_backup1",
			Instance: "web01",
		},
		Name:      "backup1",
		CreatedAt: created,
		SizeMB:    128,
	}

	if info.Name != "backup1" {
		t.Errorf("Name = %q, want %q", info.Name, "backup1")
	}
	if info.SizeMB != 128 {
		t.Errorf("SizeMB = %d, want 128", info.SizeMB)
	}
	if !info.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", info.CreatedAt, created)
	}
}

// TestSnapshotErrorsCarryTheReason covers an error that said only
// "exit status 1".
//
// zfs(8) explains itself — "dataset already exists", "permission denied" — and
// the output was captured and then dropped, leaving the caller nothing to act
// on.
func TestSnapshotErrorsCarryTheReason(t *testing.T) {
	p, _ := runningProvider(t, "web", func(cmd string, args []string) ([]byte, error) {
		if cmd == "zfs" && len(args) > 0 && args[0] == "snapshot" {
			return []byte("cannot create snapshot 'zroot/hospitus/jails/web@snap': dataset already exists"),
				errAssertStopped
		}
		return nil, nil
	})

	_, err := p.CreateSnapshot(context.Background(), provider.InstanceHandle{
		ID:       "web",
		Metadata: map[string]interface{}{"zfs_dataset": "zroot/hospitus/jails/web"},
	}, "snap")
	if err == nil {
		t.Fatal("a failed snapshot reported success")
	}
	if !strings.Contains(err.Error(), "dataset already exists") {
		t.Errorf("error = %v, want it to carry what zfs said", err)
	}
}
