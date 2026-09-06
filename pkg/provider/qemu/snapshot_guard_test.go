package qemu

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// TestSnapshotHandlesAreBoundToTheirInstance covers the destructive family.
//
// The handle comes from the caller, not from CreateSnapshot. isPathAllowed
// confines a disk to the data and image directories, but every VM's disk lives
// under those roots — so one VM's handle could name another's disk, and
// "qemu-img snapshot -a" overwrites the disk it is given with the snapshot's
// contents.
func TestSnapshotHandlesAreBoundToTheirInstance(t *testing.T) {
	fake := &execx.Fake{}
	p := fakeProvider(t, fake)
	persistVM(t, p, "web")
	otherDisk := persistVM(t, p, "db")

	for _, tc := range []struct {
		name string
		snap provider.SnapshotHandle
	}{
		{"another instance's disk", provider.SnapshotHandle{
			Instance: "web",
			Metadata: map[string]interface{}{"name": "snap1", "disk_path": otherDisk},
		}},
		{"a disk outside the roots", provider.SnapshotHandle{
			Instance: "web",
			Metadata: map[string]interface{}{"name": "snap1", "disk_path": "/etc/passwd"},
		}},
		{"a traversal", provider.SnapshotHandle{
			Instance: "web",
			Metadata: map[string]interface{}{
				"name": "snap1", "disk_path": filepath.Join(p.dataDir, "web", "..", "..", "escape.qcow2"),
			},
		}},
		{"no instance at all", provider.SnapshotHandle{
			Metadata: map[string]interface{}{"name": "snap1", "disk_path": otherDisk},
		}},
		{"a name qemu-img would read as an option", provider.SnapshotHandle{
			Instance: "web",
			Metadata: map[string]interface{}{
				"name": "-l", "disk_path": filepath.Join(p.dataDir, "web", "disk0.qcow2"),
			},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The refusal has to come from the guard: a bare err != nil also
			// passes when the method failed for some unrelated reason and never
			// reached the check at all.
			err := p.DeleteSnapshot(context.Background(), tc.snap)
			if err == nil {
				t.Error("DeleteSnapshot accepted it")
			} else if !isGuardRefusal(err) {
				t.Errorf("DeleteSnapshot refused for another reason: %v", err)
			}
			err = p.RestoreSnapshot(context.Background(),
				provider.InstanceHandle{ID: "web"}, tc.snap)
			if err == nil {
				t.Error("RestoreSnapshot accepted it")
			} else if !isGuardRefusal(err) {
				t.Errorf("RestoreSnapshot refused for another reason: %v", err)
			}
		})
	}

	for _, c := range fake.Calls {
		if c.Name == "qemu-img" {
			t.Errorf("qemu-img ran on a rejected handle: %v", c.Args)
		}
	}
}

// isGuardRefusal reports whether the error came from snapshotTargetFor rather
// than from something the method does before it.
func isGuardRefusal(err error) bool {
	msg := err.Error()
	for _, want := range []string{
		"does not belong to the instance",
		// RestoreSnapshot's own binding check, which fires before the resolver
		// when the declared instance is not the one being restored.
		"does not belong to instance",
		"outside the managed directories",
		"invalid snapshot metadata",
		"invalid snapshot name",
		"invalid snapshot instance",
	} {
		if strings.Contains(msg, want) {
			return true
		}
	}
	return false
}
