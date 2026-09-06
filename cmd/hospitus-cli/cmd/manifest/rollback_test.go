package manifest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/pkg/manifest"
)

// rollbackClientStub records the deletes a rollback issues, and can be told to
// fail an attach so the volume step returns partway through.
type rollbackClientStub struct {
	cmdutil.APIClientInterface
	existing        map[string]bool
	attachFails     bool
	createdVolumes  []string
	deletedVolumes  []string
	deletedInstance []string
}

func (s *rollbackClientStub) GetVolume(_ context.Context, name string) (*client.VolumeInfo, error) {
	if s.existing[name] {
		return &client.VolumeInfo{Name: name}, nil
	}
	return nil, fmt.Errorf("volume %s not found", name)
}

func (s *rollbackClientStub) CreateVolume(_ context.Context, req client.CreateVolumeRequest) (*client.VolumeInfo, error) {
	s.createdVolumes = append(s.createdVolumes, req.Name)
	return &client.VolumeInfo{Name: req.Name}, nil
}

func (s *rollbackClientStub) AttachVolume(_ context.Context, _, _, _ string) error {
	if s.attachFails {
		return errors.New("attach refused")
	}
	return nil
}

func (s *rollbackClientStub) DeleteVolume(_ context.Context, name string) error {
	s.deletedVolumes = append(s.deletedVolumes, name)
	return nil
}

func (s *rollbackClientStub) DeleteInstance(_ context.Context, id string, _ bool) error {
	s.deletedInstance = append(s.deletedInstance, id)
	return nil
}

// twoVolumeManifest declares one volume that already exists and one that does
// not, so the two cases can be told apart.
func twoVolumeManifest() *manifest.WorkloadManifest {
	m := &manifest.WorkloadManifest{}
	m.Provider.Type = "jail"
	m.Storage.Volumes = []manifest.VolumeSpec{
		{Name: "old", Size: "10Gi", MountPath: "/old"},
		{Name: "new", Size: "20Gi", MountPath: "/new"},
	}
	return m
}

// TestApplyReportsOnlyTheVolumesItCreated is what makes the rollback safe: a
// volume that predates the apply belongs to somebody else, and deleting it
// would take their data with it.
func TestApplyReportsOnlyTheVolumesItCreated(t *testing.T) {
	stub := &rollbackClientStub{existing: map[string]bool{"db-old": true}}

	created, err := applyManagedVolumes(context.Background(), stub, twoVolumeManifest(), "db")
	if err != nil {
		t.Fatalf("applyManagedVolumes: %v", err)
	}

	if len(created) != 1 || created[0] != "db-new" {
		t.Fatalf("created = %v, want [db-new] only", created)
	}
	for _, name := range created {
		if name == "db-old" {
			t.Error("a reused volume was reported as created by this run")
		}
	}
}

// TestApplyReportsVolumesCreatedBeforeAFailure covers the partial case: the
// first volume was made, the second failed to attach, and the first is still
// there to undo.
func TestApplyReportsVolumesCreatedBeforeAFailure(t *testing.T) {
	stub := &rollbackClientStub{attachFails: true}

	created, err := applyManagedVolumes(context.Background(), stub, twoVolumeManifest(), "db")
	if err == nil {
		t.Fatal("a failing attach was reported as success")
	}
	if len(created) == 0 {
		t.Fatal("the volume created before the failure was not reported, so nothing would be cleaned up")
	}
	if created[0] != "db-old" {
		t.Errorf("created = %v, want the first volume", created)
	}
}

// TestRollbackRemovesInstancesThenVolumes pins the order and the scope.
//
// The instance has to go first: a volume still attached to a live jail cannot
// be destroyed.
func TestRollbackRemovesInstancesThenVolumes(t *testing.T) {
	stub := &rollbackClientStub{}

	rollbackApply(context.Background(), stub, []string{"db"}, []string{"db-data"})

	if len(stub.deletedInstance) != 1 || stub.deletedInstance[0] != "db" {
		t.Errorf("deleted instances = %v, want [db]", stub.deletedInstance)
	}
	if len(stub.deletedVolumes) != 1 || stub.deletedVolumes[0] != "db-data" {
		t.Errorf("deleted volumes = %v, want [db-data]", stub.deletedVolumes)
	}
}

// TestRollbackDoesNothingWithNothingToUndo keeps a failure before anything was
// built from printing a rollback that removes nothing.
func TestRollbackDoesNothingWithNothingToUndo(t *testing.T) {
	stub := &rollbackClientStub{}

	rollbackApply(context.Background(), stub, nil, nil)

	if len(stub.deletedInstance) != 0 || len(stub.deletedVolumes) != 0 {
		t.Errorf("something was deleted: instances=%v volumes=%v", stub.deletedInstance, stub.deletedVolumes)
	}
}

// TestRollbackOutlivesTheCancelledRequest covers a timeout or a Ctrl-C: the
// caller's context is already done, and a rollback bound to it would delete
// nothing at all.
func TestRollbackOutlivesTheCancelledRequest(t *testing.T) {
	stub := &rollbackClientStub{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rollbackApply(ctx, stub, []string{"db"}, []string{"db-data"})

	if len(stub.deletedInstance) == 0 {
		t.Error("the instance was not removed because the request had already been canceled")
	}
	if len(stub.deletedVolumes) == 0 {
		t.Error("the volume was not removed because the request had already been canceled")
	}
}

// TestReversedTakesAStackDownInOrder pins the teardown order: a stack is built
// dependencies first, so removal goes the other way.
func TestReversedTakesAStackDownInOrder(t *testing.T) {
	got := reversed([]string{"db", "cache", "web"})
	want := []string{"web", "cache", "db"}

	if len(got) != len(want) {
		t.Fatalf("reversed = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reversed = %v, want %v", got, want)
		}
	}
}
