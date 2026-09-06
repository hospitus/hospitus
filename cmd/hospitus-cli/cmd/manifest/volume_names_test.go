package manifest

import (
	"context"
	"testing"

	"github.com/hospitus/hospitus/pkg/manifest"
)

// TestApplyAndDeleteNameTheSameVolumes is the property that keeps a delete from
// leaving datasets behind.
//
// The two sides used to compute the name independently, and delete never looked
// at volumes at all: a clean apply-then-delete cycle left every declared volume
// on the host, full size, under a name the next apply would not reuse.
func TestApplyAndDeleteNameTheSameVolumes(t *testing.T) {
	stub := &rollbackClientStub{}
	m := twoVolumeManifest()

	created, err := applyManagedVolumes(context.Background(), stub, m, "db")
	if err != nil {
		t.Fatalf("applyManagedVolumes: %v", err)
	}
	forDeletion := managedVolumes(m, "db")

	if len(created) != len(forDeletion) {
		t.Fatalf("apply created %v, delete would remove %v", created, forDeletion)
	}
	for i := range created {
		if created[i] != forDeletion[i] {
			t.Errorf("apply created %q, delete would remove %q", created[i], forDeletion[i])
		}
	}
}

// TestManagedVolumesSkipsBindMountsAndOtherProviders covers what must not be
// deleted: a host_path volume has no dataset of its own, and a VM's sized
// volume is a disk in the instance spec, not a managed volume.
func TestManagedVolumesSkipsBindMountsAndOtherProviders(t *testing.T) {
	m := &manifest.WorkloadManifest{}
	m.Provider.Type = "jail"
	m.Storage.Volumes = []manifest.VolumeSpec{
		{Name: "data", Size: "10Gi", MountPath: "/data"},
		{Name: "src", HostPath: "/usr/src", MountPath: "/usr/src"},
	}

	got := managedVolumes(m, "box")
	if len(got) != 1 || got[0] != "box-data" {
		t.Errorf("managedVolumes = %v, want [box-data] only", got)
	}

	m.Provider.Type = "bhyve"
	if got := managedVolumes(m, "box"); got != nil {
		t.Errorf("managedVolumes for a VM = %v, want none", got)
	}
}
