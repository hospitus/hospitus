package datastore

import (
	"context"
	"testing"
)

func newTestDS(t *testing.T) *Datastore {
	t.Helper()
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("NewDatastore: %v", err)
	}
	t.Cleanup(func() { ds.Close() })
	return ds
}

// ----------------------------------------------------------------------------
// CreateStack / GetStack
// ----------------------------------------------------------------------------

func TestCreateAndGetStack(t *testing.T) {
	ds := newTestDS(t)
	ctx := context.Background()

	stack := &StackRecord{
		Name:     "mystack",
		Status:   "pending",
		Manifest: `{"version":"1.0"}`,
	}
	if err := ds.CreateStack(ctx, stack); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}

	got, err := ds.GetStack(ctx, "mystack")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if got == nil {
		t.Fatal("GetStack returned nil")
	}
	if got.Name != "mystack" {
		t.Errorf("Name = %s, want mystack", got.Name)
	}
	if got.Status != "pending" {
		t.Errorf("Status = %s, want pending", got.Status)
	}
	if got.Manifest != `{"version":"1.0"}` {
		t.Errorf("Manifest = %s", got.Manifest)
	}
}

func TestGetStackNotFound(t *testing.T) {
	ds := newTestDS(t)
	ctx := context.Background()

	got, err := ds.GetStack(ctx, "nonexistent")
	if err != nil {
		t.Errorf("GetStack nonexistent: unexpected error: %v", err)
	}
	if got != nil {
		t.Error("GetStack nonexistent: expected nil")
	}
}

// ----------------------------------------------------------------------------
// ListStacks
// ----------------------------------------------------------------------------

func TestListStacksEmpty(t *testing.T) {
	ds := newTestDS(t)
	ctx := context.Background()

	stacks, err := ds.ListStacks(ctx)
	if err != nil {
		t.Fatalf("ListStacks: %v", err)
	}
	if len(stacks) != 0 {
		t.Errorf("expected 0 stacks, got %d", len(stacks))
	}
}

func TestListStacksMultiple(t *testing.T) {
	ds := newTestDS(t)
	ctx := context.Background()

	for _, name := range []string{"stack-a", "stack-b", "stack-c"} {
		if err := ds.CreateStack(ctx, &StackRecord{Name: name, Status: "running", Manifest: "{}"}); err != nil {
			t.Fatalf("CreateStack %s: %v", name, err)
		}
	}

	stacks, err := ds.ListStacks(ctx)
	if err != nil {
		t.Fatalf("ListStacks: %v", err)
	}
	if len(stacks) != 3 {
		t.Errorf("expected 3 stacks, got %d", len(stacks))
	}
}

// ----------------------------------------------------------------------------
// UpdateStackStatus
// ----------------------------------------------------------------------------

func TestUpdateStackStatus(t *testing.T) {
	ds := newTestDS(t)
	ctx := context.Background()

	if err := ds.CreateStack(ctx, &StackRecord{Name: "s1", Status: "pending", Manifest: "{}"}); err != nil {
		t.Fatal(err)
	}

	if err := ds.UpdateStackStatus(ctx, "s1", "running"); err != nil {
		t.Fatalf("UpdateStackStatus: %v", err)
	}

	got, err := ds.GetStack(ctx, "s1")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if got == nil {
		t.Fatal("expected stack, got nil")
	}
	if got.Status != "running" {
		t.Errorf("status = %s, want running", got.Status)
	}
}

func TestUpdateStackStatusNotFound(t *testing.T) {
	ds := newTestDS(t)
	ctx := context.Background()

	err := ds.UpdateStackStatus(ctx, "ghost", "running")
	if err == nil {
		t.Error("UpdateStackStatus on nonexistent stack should return error")
	}
}

// ----------------------------------------------------------------------------
// DeleteStack
// ----------------------------------------------------------------------------

func TestDeleteStack(t *testing.T) {
	ds := newTestDS(t)
	ctx := context.Background()

	if err := ds.CreateStack(ctx, &StackRecord{Name: "todel", Status: "stopped", Manifest: "{}"}); err != nil {
		t.Fatal(err)
	}

	if err := ds.DeleteStack(ctx, "todel"); err != nil {
		t.Fatalf("DeleteStack: %v", err)
	}

	got, err := ds.GetStack(ctx, "todel")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if got != nil {
		t.Error("stack should have been deleted")
	}
}

func TestDeleteStackNotFound(t *testing.T) {
	ds := newTestDS(t)
	ctx := context.Background()

	err := ds.DeleteStack(ctx, "ghost")
	if err == nil {
		t.Error("DeleteStack on nonexistent stack should return error")
	}
}

// ----------------------------------------------------------------------------
// CreateStackInstance / GetStackInstances
// ----------------------------------------------------------------------------

func TestCreateAndGetStackInstances(t *testing.T) {
	ds := newTestDS(t)
	ctx := context.Background()

	if err := ds.CreateStack(ctx, &StackRecord{Name: "mystack2", Status: "pending", Manifest: "{}"}); err != nil {
		t.Fatal(err)
	}

	inst := &StackInstanceRecord{
		StackName:    "mystack2",
		InstanceName: "web",
		InstanceID:   "web@jail",
		Provider:     "jail",
		DependsOn:    []string{},
		Status:       "pending",
		Health:       "unknown",
		Handle:       `{"id":"web@jail"}`,
		DeployOrder:  0,
	}
	if err := ds.CreateStackInstance(ctx, inst); err != nil {
		t.Fatalf("CreateStackInstance: %v", err)
	}

	insts, err := ds.GetStackInstances(ctx, "mystack2")
	if err != nil {
		t.Fatalf("GetStackInstances: %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(insts))
	}
	if insts[0].InstanceName != "web" {
		t.Errorf("InstanceName = %s, want web", insts[0].InstanceName)
	}
}

func TestCreateStackInstanceWithDependsOn(t *testing.T) {
	ds := newTestDS(t)
	ctx := context.Background()

	if err := ds.CreateStack(ctx, &StackRecord{Name: "depstack", Status: "pending", Manifest: "{}"}); err != nil {
		t.Fatal(err)
	}

	inst := &StackInstanceRecord{
		StackName:    "depstack",
		InstanceName: "app",
		InstanceID:   "app@jail",
		Provider:     "jail",
		DependsOn:    []string{"db", "cache"},
		Status:       "pending",
		Health:       "unknown",
		Handle:       "{}",
		DeployOrder:  1,
	}
	if err := ds.CreateStackInstance(ctx, inst); err != nil {
		t.Fatalf("CreateStackInstance with deps: %v", err)
	}

	insts, err := ds.GetStackInstances(ctx, "depstack")
	if err != nil {
		t.Fatalf("GetStackInstances: %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("expected 1, got %d", len(insts))
	}
	if len(insts[0].DependsOn) != 2 {
		t.Errorf("DependsOn = %v, want [db cache]", insts[0].DependsOn)
	}
}

func TestGetStackInstancesEmpty(t *testing.T) {
	ds := newTestDS(t)
	ctx := context.Background()

	if err := ds.CreateStack(ctx, &StackRecord{Name: "emptystack", Status: "pending", Manifest: "{}"}); err != nil {
		t.Fatal(err)
	}

	insts, err := ds.GetStackInstances(ctx, "emptystack")
	if err != nil {
		t.Fatalf("GetStackInstances: %v", err)
	}
	if len(insts) != 0 {
		t.Errorf("expected 0, got %d", len(insts))
	}
}

// ----------------------------------------------------------------------------
// UpdateStackInstance (if exists)
// ----------------------------------------------------------------------------

func TestUpdateStackInstanceStatus(t *testing.T) {
	ds := newTestDS(t)
	ctx := context.Background()

	if err := ds.CreateStack(ctx, &StackRecord{Name: "upd", Status: "running", Manifest: "{}"}); err != nil {
		t.Fatal(err)
	}

	inst := &StackInstanceRecord{
		StackName:    "upd",
		InstanceName: "svc",
		InstanceID:   "svc@jail",
		Provider:     "jail",
		Status:       "pending",
		Health:       "unknown",
		Handle:       "{}",
		DeployOrder:  0,
	}
	if err := ds.CreateStackInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	if err := ds.UpdateStackInstanceStatus(ctx, "upd", "svc", "running"); err != nil {
		t.Fatalf("UpdateStackInstanceStatus: %v", err)
	}

	if err := ds.UpdateStackInstanceHealth(ctx, "upd", "svc", "healthy"); err != nil {
		t.Fatalf("UpdateStackInstanceHealth: %v", err)
	}

	insts, err := ds.GetStackInstances(ctx, "upd")
	if err != nil {
		t.Fatalf("GetStackInstances: %v", err)
	}
	if len(insts) == 0 {
		t.Fatal("no instances returned")
	}
	if insts[0].Status != "running" {
		t.Errorf("status = %s, want running", insts[0].Status)
	}
	if insts[0].Health != "healthy" {
		t.Errorf("health = %s, want healthy", insts[0].Health)
	}
}
