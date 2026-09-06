package podman

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestCommitArgsDoNotPause(t *testing.T) {
	args := commitArgs("web", "hospitus-snapshot-web-before", "before")

	// ocijail has no pause; asking for one fails the whole commit.
	for _, arg := range args {
		if arg == "--pause=true" || arg == "-p" {
			t.Fatalf("commit asks podman to pause the container: %v", args)
		}
	}

	want := []string{
		"commit", "--pause=false", "--format", "oci",
		"--change", "LABEL " + snapshotContainerLabel + "=web",
		"--change", "LABEL " + snapshotNameLabel + "=before",
		"web", "hospitus-snapshot-web-before",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("commitArgs = %v, want %v", args, want)
	}
}

// TestListSnapshotsFiltersByLabel verifies the listing asks podman for the
// images labeled with this container, and ignores an image that carries
// another container's label even if podman returns it.
func TestListSnapshotsFiltersByLabel(t *testing.T) {
	const images = `[
	 {"Id":"aaa","Names":["localhost/hospitus-snapshot-web-s1:latest"],"Created":1700000000,"Size":1048576,
	  "Labels":{"` + snapshotContainerLabel + `":"web","` + snapshotNameLabel + `":"s1"}},
	 {"Id":"bbb","Names":["localhost/hospitus-snapshot-web-2-s1:latest"],"Created":1700000000,"Size":1048576,
	  "Labels":{"` + snapshotContainerLabel + `":"web-2","` + snapshotNameLabel + `":"s1"}},
	 {"Id":"ccc","Names":["localhost/hospitus-snapshot-web-old:latest"],"Created":1700000000,"Size":1048576}
	]`

	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte(images), nil
	}}
	p := newFakeProvider(fake)

	snapshots, err := p.ListSnapshots(context.Background(), handle("web"))
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}

	if len(snapshots) != 1 || snapshots[0].Name != "s1" || snapshots[0].Handle.ID != "aaa" {
		t.Fatalf("snapshots = %+v, want only web's own s1", snapshots)
	}

	filter := strings.Join(fake.Calls[0].Args, " ")
	if !strings.Contains(filter, "label="+snapshotContainerLabel+"=web") {
		t.Errorf("listing did not filter by label: %q", filter)
	}
}

// TestCreateSnapshotAsksPodmanWhetherTheContainerExists covers the cold cache:
// the provider's map is empty after a restart while podman was down, and a
// snapshot of a container podman has must still work.
func TestCreateSnapshotAsksPodmanWhetherTheContainerExists(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		if len(args) > 1 && args[0] == "container" && args[1] == "exists" {
			return nil, nil // podman knows it
		}
		// No image by that name yet: "podman commit" onto an existing one moves
		// the tag and leaves the old image dangling, so CreateSnapshot refuses
		// a name already taken.
		if len(args) > 1 && args[0] == "image" && args[1] == "exists" {
			return nil, errors.New("image not known")
		}
		return []byte("sha256:deadbeef\n"), nil
	}}
	p := newFakeProvider(fake) // no cached instances

	snap, err := p.CreateSnapshot(context.Background(), handle("web"), "s1")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if snap.ID != "sha256:deadbeef" {
		t.Errorf("snapshot ID = %q, want the committed image ID", snap.ID)
	}
	if snap.Metadata["container"] != "web" || snap.Metadata["name"] != "s1" {
		t.Errorf("metadata = %+v", snap.Metadata)
	}
}

func TestCreateSnapshotRejectsAnUnknownContainer(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		if len(args) > 1 && args[0] == "container" && args[1] == "exists" {
			return nil, errUnknownContainer
		}
		return nil, nil
	}}
	p := newFakeProvider(fake)

	if _, err := p.CreateSnapshot(context.Background(), handle("ghost"), "s1"); err == nil {
		t.Fatal("expected an error for a container podman does not have")
	}
}

// errUnknownContainer stands in for podman's exit status on "container exists".
var errUnknownContainer = provider.ErrInstanceNotFound
