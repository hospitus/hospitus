package datastore

import (
	"context"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestNewDatastore(t *testing.T) {
	// Use in-memory database for testing
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	if ds.db == nil {
		t.Error("Database connection is nil")
	}
}

func TestCreateAndGetInstance(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()

	// Create test instance
	instance := &Instance{
		ID:       "test-vm-1",
		Name:     "test-vm",
		Provider: "qemu",
		State:    provider.StateStopped,
		Spec: provider.InstanceSpec{
			Name:     "test-vm",
			CPUs:     2,
			MemoryMB: 2048,
		},
		Handle: provider.InstanceHandle{
			ID:       "test-vm-1",
			Provider: "qemu",
		},
		Labels: map[string]string{
			"environment": "test",
			"tier":        "web",
		},
		Annotations: map[string]string{
			"created-by": "test",
		},
	}

	err = ds.CreateInstance(ctx, instance)
	if err != nil {
		t.Fatalf("Failed to create instance: %v", err)
	}

	// Verify timestamps were set
	if instance.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
	if instance.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set")
	}

	retrieved, err := ds.GetInstance(ctx, "test-vm-1")
	if err != nil {
		t.Fatalf("Failed to get instance: %v", err)
	}

	// Verify fields
	if retrieved.ID != instance.ID {
		t.Errorf("Expected ID %s, got %s", instance.ID, retrieved.ID)
	}
	if retrieved.Name != instance.Name {
		t.Errorf("Expected name %s, got %s", instance.Name, retrieved.Name)
	}
	if retrieved.Provider != instance.Provider {
		t.Errorf("Expected provider %s, got %s", instance.Provider, retrieved.Provider)
	}
	if retrieved.State != instance.State {
		t.Errorf("Expected state %s, got %s", instance.State, retrieved.State)
	}
	if retrieved.Spec.CPUs != instance.Spec.CPUs {
		t.Errorf("Expected %d CPUs, got %d", instance.Spec.CPUs, retrieved.Spec.CPUs)
	}
	if retrieved.Labels["environment"] != "test" {
		t.Errorf("Expected environment=test, got %s", retrieved.Labels["environment"])
	}
}

func TestUpdateInstanceState(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()

	// Create instance
	instance := &Instance{
		ID:          "test-vm-2",
		Name:        "test-vm-2",
		Provider:    "qemu",
		State:       provider.StateStopped,
		Spec:        provider.InstanceSpec{Name: "test-vm-2"},
		Handle:      provider.InstanceHandle{ID: "test-vm-2", Provider: "qemu"},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	err = ds.CreateInstance(ctx, instance)
	if err != nil {
		t.Fatalf("Failed to create instance: %v", err)
	}

	err = ds.UpdateInstanceState(ctx, "test-vm-2", provider.StateRunning)
	if err != nil {
		t.Fatalf("Failed to update instance state: %v", err)
	}

	// Get instance and verify state
	retrieved, err := ds.GetInstance(ctx, "test-vm-2")
	if err != nil {
		t.Fatalf("Failed to get instance: %v", err)
	}

	if retrieved.State != provider.StateRunning {
		t.Errorf("Expected state %s, got %s", provider.StateRunning, retrieved.State)
	}

	// Verify started_at was set
	if retrieved.StartedAt == nil {
		t.Error("StartedAt should be set when state changes to running")
	}

	// Verify updated_at changed
	if retrieved.UpdatedAt.Equal(instance.UpdatedAt) {
		t.Error("UpdatedAt should have changed")
	}
}

func TestDeleteInstance(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()

	// Create instance
	instance := &Instance{
		ID:          "test-vm-3",
		Name:        "test-vm-3",
		Provider:    "qemu",
		State:       provider.StateStopped,
		Spec:        provider.InstanceSpec{Name: "test-vm-3"},
		Handle:      provider.InstanceHandle{ID: "test-vm-3", Provider: "qemu"},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	err = ds.CreateInstance(ctx, instance)
	if err != nil {
		t.Fatalf("Failed to create instance: %v", err)
	}

	err = ds.DeleteInstance(ctx, "test-vm-3")
	if err != nil {
		t.Fatalf("Failed to delete instance: %v", err)
	}

	// Verify instance no longer exists
	_, err = ds.GetInstance(ctx, "test-vm-3")
	if err == nil {
		t.Error("Expected error when getting deleted instance")
	}
}

func TestListInstances(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()

	// Create multiple instances
	instances := []*Instance{
		{
			ID:          "vm-1",
			Name:        "vm-1",
			Provider:    "qemu",
			State:       provider.StateRunning,
			Spec:        provider.InstanceSpec{Name: "vm-1"},
			Handle:      provider.InstanceHandle{ID: "vm-1", Provider: "qemu"},
			Labels:      map[string]string{"environment": "production"},
			Annotations: map[string]string{},
		},
		{
			ID:          "vm-2",
			Name:        "vm-2",
			Provider:    "jail",
			State:       provider.StateStopped,
			Spec:        provider.InstanceSpec{Name: "vm-2"},
			Handle:      provider.InstanceHandle{ID: "vm-2", Provider: "jail"},
			Labels:      map[string]string{"environment": "development"},
			Annotations: map[string]string{},
		},
		{
			ID:          "vm-3",
			Name:        "vm-3",
			Provider:    "qemu",
			State:       provider.StateRunning,
			Spec:        provider.InstanceSpec{Name: "vm-3"},
			Handle:      provider.InstanceHandle{ID: "vm-3", Provider: "qemu"},
			Labels:      map[string]string{"environment": "production"},
			Annotations: map[string]string{},
		},
	}

	for _, instance := range instances {
		err = ds.CreateInstance(ctx, instance)
		if err != nil {
			t.Fatalf("Failed to create instance %s: %v", instance.ID, err)
		}
	}

	// Test: List all instances
	all, err := ds.ListInstances(ctx, InstanceFilter{})
	if err != nil {
		t.Fatalf("Failed to list instances: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("Expected 3 instances, got %d", len(all))
	}

	// Test: Filter by provider
	qemuInstances, err := ds.ListInstances(ctx, InstanceFilter{Provider: "qemu"})
	if err != nil {
		t.Fatalf("Failed to list QEMU instances: %v", err)
	}
	if len(qemuInstances) != 2 {
		t.Errorf("Expected 2 QEMU instances, got %d", len(qemuInstances))
	}

	// Test: Filter by state
	runningInstances, err := ds.ListInstances(ctx, InstanceFilter{
		States: []provider.InstanceState{provider.StateRunning},
	})
	if err != nil {
		t.Fatalf("Failed to list running instances: %v", err)
	}
	if len(runningInstances) != 2 {
		t.Errorf("Expected 2 running instances, got %d", len(runningInstances))
	}

	// Test: Filter by labels
	prodInstances, err := ds.ListInstances(ctx, InstanceFilter{
		Labels: map[string]string{"environment": "production"},
	})
	if err != nil {
		t.Fatalf("Failed to list production instances: %v", err)
	}
	if len(prodInstances) != 2 {
		t.Errorf("Expected 2 production instances, got %d", len(prodInstances))
	}

	// Test: Combined filters
	runningQemuInstances, err := ds.ListInstances(ctx, InstanceFilter{
		Provider: "qemu",
		States:   []provider.InstanceState{provider.StateRunning},
	})
	if err != nil {
		t.Fatalf("Failed to list running QEMU instances: %v", err)
	}
	if len(runningQemuInstances) != 2 {
		t.Errorf("Expected 2 running QEMU instances, got %d", len(runningQemuInstances))
	}
}

func TestGetEvents(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()

	// Create instance (this generates a creation event)
	instance := &Instance{
		ID:          "vm-events",
		Name:        "vm-events",
		Provider:    "qemu",
		State:       provider.StateStopped,
		Spec:        provider.InstanceSpec{Name: "vm-events"},
		Handle:      provider.InstanceHandle{ID: "vm-events", Provider: "qemu"},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	err = ds.CreateInstance(ctx, instance)
	if err != nil {
		t.Fatalf("Failed to create instance: %v", err)
	}

	// Update state (this generates update events). No sleeps are needed to
	// order the events: GetEvents breaks timestamp ties on the monotonic rowid
	// (id DESC), so the most recent insert is deterministically returned first.
	_ = ds.UpdateInstanceState(ctx, "vm-events", provider.StateStarting)
	_ = ds.UpdateInstanceState(ctx, "vm-events", provider.StateRunning)
	_ = ds.UpdateInstanceState(ctx, "vm-events", provider.StateStopped)

	events, err := ds.GetEvents(ctx, "vm-events", 10)
	if err != nil {
		t.Fatalf("Failed to get events: %v", err)
	}

	// Should have at least 4 events (created + 3 updates)
	if len(events) < 4 {
		t.Errorf("Expected at least 4 events, got %d", len(events))
	}

	// Events should be in reverse chronological order
	if len(events) >= 2 {
		if events[0].Timestamp.Before(events[1].Timestamp) {
			t.Error("Events should be in reverse chronological order")
		}
	}

	// First event should be most recent state change
	if len(events) > 0 {
		if events[0].Type != EventTypeUpdated {
			t.Errorf("Expected event type %s, got %s", EventTypeUpdated, events[0].Type)
		}
		if events[0].State != provider.StateStopped {
			t.Errorf("Expected state %s, got %s", provider.StateStopped, events[0].State)
		}
	}
}

func TestConcurrentAccess(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()

	// Create base instance
	instance := &Instance{
		ID:          "vm-concurrent",
		Name:        "vm-concurrent",
		Provider:    "qemu",
		State:       provider.StateStopped,
		Spec:        provider.InstanceSpec{Name: "vm-concurrent"},
		Handle:      provider.InstanceHandle{ID: "vm-concurrent", Provider: "qemu"},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	err = ds.CreateInstance(ctx, instance)
	if err != nil {
		t.Fatalf("Failed to create instance: %v", err)
	}

	// Concurrent updates
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			_ = ds.UpdateInstanceState(ctx, "vm-concurrent", provider.StateRunning)
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify instance still exists and is in consistent state
	retrieved, err := ds.GetInstance(ctx, "vm-concurrent")
	if err != nil {
		t.Fatalf("Failed to get instance after concurrent updates: %v", err)
	}

	if retrieved.State != provider.StateRunning {
		t.Errorf("Expected state %s, got %s", provider.StateRunning, retrieved.State)
	}
}

func TestInstanceNotFound(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()

	// Try to get non-existent instance
	_, err = ds.GetInstance(ctx, "non-existent")
	if err == nil {
		t.Error("Expected error when getting non-existent instance")
	}

	// Try to update non-existent instance
	err = ds.UpdateInstanceState(ctx, "non-existent", provider.StateRunning)
	if err == nil {
		t.Error("Expected error when updating non-existent instance")
	}

	// Try to delete non-existent instance
	err = ds.DeleteInstance(ctx, "non-existent")
	if err == nil {
		t.Error("Expected error when deleting non-existent instance")
	}
}

func dsTestInstance(name, prov string) *Instance {
	return &Instance{
		ID:       name,
		Name:     name,
		Provider: prov,
		State:    provider.StateStopped,
		Spec: provider.InstanceSpec{
			Name:     name,
			CPUs:     2,
			MemoryMB: 1024,
		},
		Handle: provider.InstanceHandle{
			ID:       name,
			Provider: prov,
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}
}

func TestGetInstanceByName(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()

	// Not found returns nil, nil
	inst, err := ds.GetInstanceByName(ctx, "notexist")
	if err != nil {
		t.Fatalf("GetInstanceByName(notexist): expected nil error, got %v", err)
	}
	if inst != nil {
		t.Error("expected nil for missing instance")
	}

	// Create and find
	original := dsTestInstance("byname-test", "jail")
	if err := ds.CreateInstance(ctx, original); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	found, err := ds.GetInstanceByName(ctx, "byname-test")
	if err != nil {
		t.Fatalf("GetInstanceByName: %v", err)
	}
	if found == nil || found.Name != "byname-test" {
		t.Errorf("expected byname-test, got %v", found)
	}
}

func TestUpdateInstanceSpec(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()
	inst := dsTestInstance("spec-test", "jail")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	newSpec := provider.InstanceSpec{Name: "spec-test", CPUs: 8, MemoryMB: 4096}
	if err := ds.UpdateInstanceSpec(ctx, inst.ID, newSpec); err != nil {
		t.Fatalf("UpdateInstanceSpec: %v", err)
	}

	got, err := ds.GetInstance(ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if got.Spec.CPUs != 8 || got.Spec.MemoryMB != 4096 {
		t.Errorf("unexpected spec after update: cpus=%d mem=%d", got.Spec.CPUs, got.Spec.MemoryMB)
	}

	if err := ds.UpdateInstanceSpec(ctx, "nonexistent", newSpec); err == nil {
		t.Error("expected error for nonexistent instance")
	}
}

func TestUpdateInstanceHandle(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()
	inst := dsTestInstance("handle-test", "jail")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	newHandle := provider.InstanceHandle{ID: inst.ID, Provider: "jail"}
	if err := ds.UpdateInstanceHandle(ctx, inst.ID, newHandle); err != nil {
		t.Fatalf("UpdateInstanceHandle: %v", err)
	}

	if err := ds.UpdateInstanceHandle(ctx, "ghost", newHandle); err == nil {
		t.Error("expected error for nonexistent instance")
	}
}

func TestUpdateBackupConfig(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()
	inst := dsTestInstance("backup-cfg-test", "jail")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	cfg := &InstanceBackupConfig{
		Enabled:  true,
		Schedule: "0 2 * * *",
	}
	if err := ds.UpdateBackupConfig(ctx, inst.ID, cfg); err != nil {
		t.Fatalf("UpdateBackupConfig: %v", err)
	}

	got, err := ds.GetInstance(ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if got.Backup == nil || !got.Backup.Enabled {
		t.Error("expected backup config to be set and enabled")
	}

	if err := ds.UpdateBackupConfig(ctx, "ghost", cfg); err == nil {
		t.Error("expected error for nonexistent instance")
	}
}

func TestRenameInstance(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()
	inst := dsTestInstance("rename-old", "jail")
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	if err := ds.RenameInstance(ctx, "rename-old", "rename-new"); err != nil {
		t.Fatalf("RenameInstance: %v", err)
	}

	old, err := ds.GetInstanceByName(ctx, "rename-old")
	if err != nil {
		t.Fatalf("GetInstanceByName(old): %v", err)
	}
	if old != nil {
		t.Error("old name should not exist after rename")
	}

	newInst, err := ds.GetInstanceByName(ctx, "rename-new")
	if err != nil {
		t.Fatalf("GetInstanceByName(new): %v", err)
	}
	if newInst == nil {
		t.Error("new name should exist after rename")
	}

	if err := ds.RenameInstance(ctx, "ghost", "whatever"); err == nil {
		t.Error("expected error for nonexistent instance")
	}
}

func TestPingAndGetPath(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	if err := ds.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	if ds.GetPath() != ":memory:" {
		t.Errorf("expected GetPath=:memory:, got %q", ds.GetPath())
	}
}

func TestMigrationStatus(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	status, err := ds.MigrationStatus(context.Background())
	if err != nil {
		t.Fatalf("MigrationStatus: %v", err)
	}
	if status == nil {
		t.Fatal("expected non-nil migration status")
	}
	// NewDatastore migrates to the latest version, so the reported status must
	// be fully applied: a non-zero latest, current == latest, nothing pending.
	if status.LatestVersion == 0 {
		t.Error("expected a non-zero latest migration version")
	}
	if status.CurrentVersion != status.LatestVersion {
		t.Errorf("expected current version %d to equal latest %d",
			status.CurrentVersion, status.LatestVersion)
	}
	if status.PendingCount != 0 {
		t.Errorf("expected 0 pending migrations after init, got %d", status.PendingCount)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()

	// NewDatastore already migrated to latest. Re-running Migrate must be a
	// no-op: it must not error and must not change the schema version.
	before, err := ds.MigrationStatus(ctx)
	if err != nil {
		t.Fatalf("MigrationStatus (before): %v", err)
	}
	if err := ds.Migrate(ctx, 0); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	after, err := ds.MigrationStatus(ctx)
	if err != nil {
		t.Fatalf("MigrationStatus (after): %v", err)
	}
	if before.CurrentVersion != after.CurrentVersion {
		t.Errorf("Migrate not idempotent: version changed from %d to %d",
			before.CurrentVersion, after.CurrentVersion)
	}
	if after.PendingCount != 0 {
		t.Errorf("expected 0 pending migrations after re-migrate, got %d", after.PendingCount)
	}
}
