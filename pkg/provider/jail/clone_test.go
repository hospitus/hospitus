package jail

import (
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

func TestCloneProviderInterfaceAssertion(t *testing.T) {
	var _ provider.CloneProvider = (*JailProvider)(nil)
}

func TestCloneNameValidation(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid simple", input: "myclone", wantErr: false},
		{name: "valid with dash", input: "my-clone", wantErr: false},
		{name: "valid with underscore", input: "my_clone", wantErr: false},
		{name: "valid numeric", input: "clone01", wantErr: false},
		{name: "empty", input: "", wantErr: true},
		{name: "too long", input: func() string {
			s := make([]byte, 64)
			for i := range s {
				s[i] = 'a'
			}
			return string(s)
		}(), wantErr: true},
		{name: "contains dot", input: "my.clone", wantErr: true},
		{name: "contains slash", input: "my/clone", wantErr: true},
		{name: "contains space", input: "my clone", wantErr: true},
		{name: "starts with dash", input: "-clone", wantErr: true},
		{name: "path traversal", input: "../etc", wantErr: true},
		{name: "command injection semicolon", input: "clone;rm -rf", wantErr: true},
		{name: "command injection backtick", input: "clone`id`", wantErr: true},
		{name: "command injection dollar", input: "clone$(id)", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validation.ValidateInstanceName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateInstanceName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestCloneInstanceRefusesForeignDataset(t *testing.T) {
	// CloneInstance should fail if source handle has no zfs_dataset metadata
	p := &JailProvider{zfsParent: "zroot/hospitus/jails"}
	ctx := t.Context()

	source := provider.InstanceHandle{
		ID:       "source-jail",
		Provider: "jail",
		Metadata: map[string]interface{}{"zfs_dataset": "zroot/ROOT/default"},
	}

	_, err := p.CloneInstance(ctx, source, "myclone", provider.CloneOptions{})
	if err == nil {
		t.Fatal("a clone was made of a dataset outside the jail parent")
	}
	if got := err.Error(); !strings.Contains(got, "not under") {
		t.Errorf("error does not say why: %s", got)
	}
}

func TestCloneInstanceInvalidName(t *testing.T) {
	p := &JailProvider{zfsParent: "zroot/hospitus/jails"}
	ctx := t.Context()

	source := provider.InstanceHandle{
		ID:       "source-jail",
		Provider: "jail",
		Metadata: map[string]interface{}{
			"zfs_dataset": "zroot/hospitus/jails/source-jail",
		},
	}

	// Invalid clone name should fail validation before touching ZFS
	_, err := p.CloneInstance(ctx, source, "../evil", provider.CloneOptions{})
	if err == nil {
		t.Fatal("Expected error for invalid clone name")
	}
	if got := err.Error(); !strings.Contains(got, "invalid clone name") {
		t.Errorf("Expected validation error, got: %s", got)
	}
}

func TestCloneFromSnapshotMissingZFSName(t *testing.T) {
	p := &JailProvider{zfsParent: "zroot/hospitus/jails"}
	ctx := t.Context()

	snapshot := provider.SnapshotHandle{
		ID:       "source_snap1",
		Instance: "source",
		Metadata: map[string]interface{}{}, // no zfs_name
	}

	_, err := p.CloneFromSnapshot(ctx, snapshot, "myclone", provider.CloneOptions{})
	if err == nil {
		t.Fatal("Expected error for missing zfs_name metadata")
	}
	if got := err.Error(); !strings.Contains(got, "missing zfs_name") {
		t.Errorf("Expected zfs_name error, got: %s", got)
	}
}

func TestCloneFromSnapshotInvalidName(t *testing.T) {
	p := &JailProvider{zfsParent: "zroot/hospitus/jails"}
	ctx := t.Context()

	snapshot := provider.SnapshotHandle{
		ID:       "source_snap1",
		Instance: "source",
		Metadata: map[string]interface{}{
			"zfs_name": "zroot/hospitus/jails/source@snap1",
		},
	}

	_, err := p.CloneFromSnapshot(ctx, snapshot, "bad;name", provider.CloneOptions{})
	if err == nil {
		t.Fatal("Expected error for invalid clone name")
	}
	if got := err.Error(); !strings.Contains(got, "invalid clone name") {
		t.Errorf("Expected validation error, got: %s", got)
	}
}

func TestCloneOptionsFields(t *testing.T) {
	opts := provider.CloneOptions{
		LinkedClone: true,
		CPUs:        4,
		MemoryMB:    2048,
		Labels:      map[string]string{"env": "staging"},
		Annotations: map[string]string{"source": "template"},
	}

	if !opts.LinkedClone {
		t.Error("LinkedClone should be true")
	}
	if opts.CPUs != 4 {
		t.Errorf("CPUs = %d, want 4", opts.CPUs)
	}
	if opts.MemoryMB != 2048 {
		t.Errorf("MemoryMB = %d, want 2048", opts.MemoryMB)
	}
	if opts.Labels["env"] != "staging" {
		t.Errorf("Labels[env] = %q, want \"staging\"", opts.Labels["env"])
	}
}
