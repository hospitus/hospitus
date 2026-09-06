package podman

import (
	"context"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// TestSnapshotHandlesAreBoundToTheirInstance covers the handle-forging family.
//
// CreateSnapshot names its image "<prefix><container>-<snapshot>", so the image
// a handle carries is checkable against the instance it declares. Without that,
// a handle could point RestoreSnapshot at another container's snapshot image —
// or at a name podman reads as an option.
func TestSnapshotHandlesAreBoundToTheirInstance(t *testing.T) {
	fake := &execx.Fake{}
	p := &PodmanProvider{runner: fake, podmanBin: "podman"}

	for _, tc := range []struct {
		name  string
		image string
	}{
		{"another container's snapshot", snapshotPrefix + "db-backup1"},
		{"an unrelated host image", "docker.io/library/alpine"},
		{"a name podman reads as an option", "-v/etc:/host"},
		{"a prefix that only looks right", snapshotPrefix + "web2-backup1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := provider.SnapshotHandle{
				Instance: "web",
				Metadata: map[string]interface{}{"imageName": tc.image, "container": "web"},
			}
			if err := p.RestoreSnapshot(context.Background(),
				provider.InstanceHandle{ID: "web"}, snap); err == nil {
				t.Error("RestoreSnapshot accepted it")
			}
			if _, err := p.CloneFromSnapshot(context.Background(), snap, "copy",
				provider.CloneOptions{}); err == nil {
				t.Error("CloneFromSnapshot accepted it")
			}
		})
	}

	// A container that disagrees with the declared instance.
	mismatched := provider.SnapshotHandle{
		Instance: "web",
		Metadata: map[string]interface{}{
			"imageName": snapshotPrefix + "web-backup1", "container": "db",
		},
	}
	if _, err := p.CloneFromSnapshot(context.Background(), mismatched, "copy",
		provider.CloneOptions{}); err == nil {
		t.Error("CloneFromSnapshot accepted a handle naming another container")
	}

	for _, c := range fake.Calls {
		t.Errorf("podman ran on a rejected handle: %v", c.Args)
	}
}

// TestAHandleFromListSnapshotsIsAccepted covers the round trip the guard broke.
//
// podman reports an image as "localhost/<name>:latest" and ListSnapshots stores
// that form, while the guard compared it against the bare prefix — so every
// handle the provider itself produced was refused. CreateSnapshot had a second
// version of the same problem: it put the cached container id in Instance and
// the container name in the metadata, which snapshotContainerFor compares.
func TestAHandleFromListSnapshotsIsAccepted(t *testing.T) {
	p := &PodmanProvider{}

	qualified := provider.SnapshotHandle{
		Instance: "web",
		Metadata: map[string]interface{}{
			"imageName": "localhost/" + snapshotPrefix + "web-s1:latest",
			"container": "web",
		},
	}
	if _, err := p.snapshotImageFor(qualified); err != nil {
		t.Errorf("snapshotImageFor refused the form podman reports: %v", err)
	}
	if _, err := p.snapshotContainerFor(qualified); err != nil {
		t.Errorf("snapshotContainerFor: %v", err)
	}

	// And the confinement still holds through the qualified form.
	foreign := provider.SnapshotHandle{
		Instance: "web",
		Metadata: map[string]interface{}{
			"imageName": "localhost/" + snapshotPrefix + "db-s1:latest",
			"container": "web",
		},
	}
	if got, err := p.snapshotImageFor(foreign); err == nil {
		t.Errorf("snapshotImageFor accepted another container's snapshot: %q", got)
	}
}
