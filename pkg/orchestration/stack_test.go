package orchestration

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/hospitus/hospitus/pkg/manifest"
	"github.com/hospitus/hospitus/pkg/provider"
)

// testLogger returns a no-op logger for use in tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// controllableMockProvider is a mock Provider that allows tests to control which
// operations succeed or fail on a per-instance basis. It also records which
// operations were called, enabling verification of rollback behavior.
type controllableMockProvider struct {
	mu sync.Mutex

	name         string
	providerType provider.ProviderType

	// Per-instance operation controllers.
	// Keyed by instance name from the InstanceSpec (the instanceID passed to CreateInstance).
	createFail map[string]error
	startFail  map[string]error
	stopFail   map[string]error
	deleteFail map[string]error

	// Operation call records for verification
	createdInstances []string
	startedInstances []string
	stoppedInstances []string
	deletedInstances []string
}

func newControllableMockProvider(name string, providerType provider.ProviderType) *controllableMockProvider {
	return &controllableMockProvider{
		name:         name,
		providerType: providerType,
		createFail:   make(map[string]error),
		startFail:    make(map[string]error),
		stopFail:     make(map[string]error),
		deleteFail:   make(map[string]error),
	}
}

func (m *controllableMockProvider) Metadata() provider.ProviderMetadata {
	return provider.ProviderMetadata{
		Name:    m.name,
		Version: "1.0.0",
		Type:    m.providerType,
	}
}

func (m *controllableMockProvider) Capabilities() provider.ProviderCapabilities {
	return provider.ProviderCapabilities{}
}

func (m *controllableMockProvider) Initialize(ctx context.Context, config provider.ProviderConfig) error {
	return nil
}
func (m *controllableMockProvider) Shutdown(ctx context.Context) error    { return nil }
func (m *controllableMockProvider) HealthCheck(ctx context.Context) error { return nil }
func (m *controllableMockProvider) RestartInstance(ctx context.Context, handle provider.InstanceHandle) error {
	return nil
}

func (m *controllableMockProvider) GetInstanceState(ctx context.Context, handle provider.InstanceHandle) (provider.InstanceState, error) {
	return provider.StateRunning, nil
}

func (m *controllableMockProvider) GetInstanceInfo(ctx context.Context, handle provider.InstanceHandle) (provider.InstanceInfo, error) {
	return provider.InstanceInfo{}, nil
}

func (m *controllableMockProvider) ListInstances(ctx context.Context, filter provider.InstanceFilter) ([]provider.InstanceHandle, error) {
	return nil, nil
}

func (m *controllableMockProvider) SetInstanceResources(ctx context.Context, handle provider.InstanceHandle, resources provider.ResourceSpec) error {
	return nil
}

func (m *controllableMockProvider) GetInstanceMetrics(ctx context.Context, handle provider.InstanceHandle) (provider.Metrics, error) {
	return provider.Metrics{}, nil
}

func (m *controllableMockProvider) AttachDisk(ctx context.Context, handle provider.InstanceHandle, disk provider.DiskAttachment) error {
	return nil
}

func (m *controllableMockProvider) DetachDisk(ctx context.Context, handle provider.InstanceHandle, diskID string) error {
	return nil
}

func (m *controllableMockProvider) AttachNetwork(ctx context.Context, handle provider.InstanceHandle, network provider.NetworkAttachment) error {
	return nil
}

func (m *controllableMockProvider) DetachNetwork(ctx context.Context, handle provider.InstanceHandle, interfaceID string) error {
	return nil
}

func (m *controllableMockProvider) CreateInstance(ctx context.Context, spec provider.InstanceSpec) (provider.InstanceHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.createdInstances = append(m.createdInstances, spec.Name)

	if err, ok := m.createFail[spec.Name]; ok {
		return provider.InstanceHandle{}, err
	}
	return provider.InstanceHandle{ID: spec.Name, Provider: m.name}, nil
}

func (m *controllableMockProvider) DeleteInstance(ctx context.Context, handle provider.InstanceHandle, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.deletedInstances = append(m.deletedInstances, handle.ID)

	if err, ok := m.deleteFail[handle.ID]; ok {
		return err
	}
	return nil
}

func (m *controllableMockProvider) StartInstance(ctx context.Context, handle provider.InstanceHandle) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.startedInstances = append(m.startedInstances, handle.ID)

	if err, ok := m.startFail[handle.ID]; ok {
		return err
	}
	return nil
}

func (m *controllableMockProvider) StopInstance(ctx context.Context, handle provider.InstanceHandle, opts provider.StopOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.stoppedInstances = append(m.stoppedInstances, handle.ID)

	if err, ok := m.stopFail[handle.ID]; ok {
		return err
	}
	return nil
}

// callRecords returns a snapshot of recorded call lists.
func (m *controllableMockProvider) callRecords() (created, started, stopped, deleted []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	created = make([]string, len(m.createdInstances))
	copy(created, m.createdInstances)
	started = make([]string, len(m.startedInstances))
	copy(started, m.startedInstances)
	stopped = make([]string, len(m.stoppedInstances))
	copy(stopped, m.stoppedInstances)
	deleted = make([]string, len(m.deletedInstances))
	copy(deleted, m.deletedInstances)
	return
}

// ── Test helpers ──────────────────────────────────────────────────────────────

// setCreateFail configures CreateInstance to fail for a specific instance name.
// The instance name here is the composite instanceID used internally (e.g. "teststack_web").
func (m *controllableMockProvider) setCreateFail(instanceID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.createFail[instanceID] = err
	} else {
		delete(m.createFail, instanceID)
	}
}

func (m *controllableMockProvider) setStartFail(instanceID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.startFail[instanceID] = err
	} else {
		delete(m.startFail, instanceID)
	}
}

func (m *controllableMockProvider) setStopFail(instanceID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.stopFail[instanceID] = err
	} else {
		delete(m.stopFail, instanceID)
	}
}

func (m *controllableMockProvider) setDeleteFail(instanceID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.deleteFail[instanceID] = err
	} else {
		delete(m.deleteFail, instanceID)
	}
}

// ── Test: Successful deploy, no rollback ─────────────────────────────────────

func TestDeployStackSuccessful(t *testing.T) {
	reg := provider.NewRegistry()
	mock := newControllableMockProvider("jail", provider.ProviderTypeContainer)
	if err := reg.Register(mock); err != nil {
		t.Fatalf("register mock: %v", err)
	}

	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		registry:   reg,
		serviceReg: NewServiceRegistry(),
		logger:     testLogger(),
	}

	stackManifest := &manifest.StackManifest{
		Stack: manifest.StackMeta{Name: "teststack"},
		Instances: []manifest.InstanceConfig{
			{Name: "web", Provider: "jail", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
			{Name: "db", Provider: "jail", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
		},
	}

	ctx := context.Background()
	err := sm.DeployStack(ctx, stackManifest)
	if err != nil {
		t.Fatalf("DeployStack failed unexpectedly: %v", err)
	}

	// Verify stack state
	stack := sm.GetStack("teststack")
	if stack == nil {
		t.Fatal("stack not found after deploy")
	}
	if stack.Status != StackStatusRunning {
		t.Errorf("expected status running, got %s", stack.Status)
	}
	if len(stack.Instances) != 2 {
		t.Errorf("expected 2 instances, got %d", len(stack.Instances))
	}

	// Verify no rollback occurred
	created, started, stopped, deleted := mock.callRecords()
	if len(created) != 2 {
		t.Errorf("expected 2 creates, got %d: %v", len(created), created)
	}
	if len(started) != 2 {
		t.Errorf("expected 2 starts, got %d: %v", len(started), started)
	}
	if len(stopped) != 0 {
		t.Errorf("expected 0 stops (no rollback), got %d: %v", len(stopped), stopped)
	}
	if len(deleted) != 0 {
		t.Errorf("expected 0 deletes (no rollback), got %d: %v", len(deleted), deleted)
	}

	// Verify instances are registered in the service registry
	svc1 := sm.serviceReg.Get("teststack", "web")
	if svc1 == nil {
		t.Error("service 'web' not registered")
	}
	svc2 := sm.serviceReg.Get("teststack", "db")
	if svc2 == nil {
		t.Error("service 'db' not registered")
	}
}

// ── Test: Partial failure on CreateInstance triggers rollback ────────────────

func TestDeployStackCreateInstanceFailsRollback(t *testing.T) {
	reg := provider.NewRegistry()
	mock := newControllableMockProvider("jail", provider.ProviderTypeContainer)
	if err := reg.Register(mock); err != nil {
		t.Fatalf("register mock: %v", err)
	}

	// Second instance ("web", alphabetically after "app") will fail to create
	mock.setCreateFail("teststack_web", fmt.Errorf("ZFS dataset creation failed"))

	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		registry:   reg,
		serviceReg: NewServiceRegistry(),
		logger:     testLogger(),
	}

	stackManifest := &manifest.StackManifest{
		Stack: manifest.StackMeta{Name: "teststack"},
		Instances: []manifest.InstanceConfig{
			{Name: "web", Provider: "jail", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
			{Name: "app", Provider: "jail", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
		},
	}

	ctx := context.Background()
	err := sm.DeployStack(ctx, stackManifest)
	if err == nil {
		t.Fatal("expected DeployStack to fail, got nil")
	}
	t.Logf("DeployStack error (expected): %v", err)

	// Verify stack status is failed
	stack := sm.GetStack("teststack")
	if stack == nil {
		t.Fatal("stack should still exist after failed deploy")
	}
	if stack.Status != StackStatusFailed {
		t.Errorf("expected status failed, got %s", stack.Status)
	}
	// The instance created before the failure was rolled back, so the stack
	// lists none. A stack that kept listing a deleted instance could not be
	// destroyed afterwards: DeleteInstance failed on the missing one and left
	// the stack behind.
	if len(stack.Instances) != 0 {
		t.Errorf("expected the rolled-back instance to be forgotten, got %d still listed", len(stack.Instances))
	}

	// Verify rollback: app was created, started, then stopped and deleted
	created, started, stopped, deleted := mock.callRecords()
	if len(created) != 2 {
		t.Errorf("expected 2 creates (app succeeded, web attempted), got %d: %v", len(created), created)
	}
	if created[0] != "teststack_app" {
		t.Errorf("expected app created first (alpha order), got %s", created[0])
	}
	if created[1] != "teststack_web" {
		t.Errorf("expected web created second, got %s", created[1])
	}
	if len(started) != 1 {
		t.Errorf("expected 1 start (app only), got %d: %v", len(started), started)
	}
	if started[0] != "teststack_app" {
		t.Errorf("expected app started, got %s", started[0])
	}
	// Rollback should stop and delete app
	if len(stopped) != 1 {
		t.Errorf("expected 1 stop (rollback of app), got %d: %v", len(stopped), stopped)
	}
	if stopped[0] != "teststack_app" {
		t.Errorf("expected app stopped in rollback, got %s", stopped[0])
	}
	if len(deleted) != 1 {
		t.Errorf("expected 1 delete (rollback of app), got %d: %v", len(deleted), deleted)
	}
	if deleted[0] != "teststack_app" {
		t.Errorf("expected app deleted in rollback, got %s", deleted[0])
	}
}

// ── Test: Partial failure on StartInstance triggers rollback ─────────────────

func TestDeployStackStartInstanceFailsRollback(t *testing.T) {
	reg := provider.NewRegistry()
	mock := newControllableMockProvider("jail", provider.ProviderTypeContainer)
	if err := reg.Register(mock); err != nil {
		t.Fatalf("register mock: %v", err)
	}

	// Second instance ("web", alphabetically after "app") will fail to start
	mock.setStartFail("teststack_web", fmt.Errorf("jail start failed: VNET not available"))

	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		registry:   reg,
		serviceReg: NewServiceRegistry(),
		logger:     testLogger(),
	}

	stackManifest := &manifest.StackManifest{
		Stack: manifest.StackMeta{Name: "teststack"},
		Instances: []manifest.InstanceConfig{
			{Name: "web", Provider: "jail", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
			{Name: "app", Provider: "jail", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
		},
	}

	ctx := context.Background()
	err := sm.DeployStack(ctx, stackManifest)
	if err == nil {
		t.Fatal("expected DeployStack to fail, got nil")
	}
	t.Logf("DeployStack error (expected): %v", err)

	// Verify stack status is failed
	stack := sm.GetStack("teststack")
	if stack == nil {
		t.Fatal("stack should still exist after failed deploy")
	}
	if stack.Status != StackStatusFailed {
		t.Errorf("expected status failed, got %s", stack.Status)
	}

	// Both were created and web was started, but db failed to start → rollback both
	created, started, stopped, deleted := mock.callRecords()
	if len(created) != 2 {
		t.Errorf("expected 2 creates, got %d: %v", len(created), created)
	}
	if len(started) != 2 {
		t.Errorf("expected 2 start attempts (web succeeded, db failed), got %d: %v", len(started), started)
	}
	// Rollback: stop and delete both (web was already started, db was just created)
	if len(stopped) != 2 {
		t.Errorf("expected 2 stops (rollback), got %d: %v", len(stopped), stopped)
	}
	if len(deleted) != 2 {
		t.Errorf("expected 2 deletes (rollback), got %d: %v", len(deleted), deleted)
	}
}

// ── Test: Rollback where StopInstance itself fails ───────────────────────────

func TestDeployStackRollbackStopFails(t *testing.T) {
	reg := provider.NewRegistry()
	mock := newControllableMockProvider("jail", provider.ProviderTypeContainer)
	if err := reg.Register(mock); err != nil {
		t.Fatalf("register mock: %v", err)
	}

	// Second instance ("web", alphabetically after "app") fails to create
	mock.setCreateFail("teststack_web", fmt.Errorf("out of disk space"))
	// The first instance's StopInstance will fail during rollback
	mock.setStopFail("teststack_app", fmt.Errorf("jail stop timeout"))

	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		registry:   reg,
		serviceReg: NewServiceRegistry(),
		logger:     testLogger(),
	}

	stackManifest := &manifest.StackManifest{
		Stack: manifest.StackMeta{Name: "teststack"},
		Instances: []manifest.InstanceConfig{
			{Name: "web", Provider: "jail", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
			{Name: "app", Provider: "jail", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
		},
	}

	ctx := context.Background()
	err := sm.DeployStack(ctx, stackManifest)
	if err == nil {
		t.Fatal("expected DeployStack to fail, got nil")
	}

	// Verify: despite stop error, delete was still attempted
	created, started, stopped, deleted := mock.callRecords()
	if len(stopped) != 1 {
		t.Errorf("expected 1 stop attempt (even though it failed), got %d: %v", len(stopped), stopped)
	}
	if len(deleted) != 1 {
		t.Errorf("expected 1 delete (should proceed after stop failure), got %d: %v", len(deleted), deleted)
	}
	if deleted[0] != "teststack_app" {
		t.Errorf("expected app deleted in rollback, got %s", deleted[0])
	}
	t.Logf("created=%v started=%v stopped=%v deleted=%v", created, started, stopped, deleted)
}

// ── Test: Rollback where DeleteInstance itself fails ─────────────────────────

func TestDeployStackRollbackDeleteFails(t *testing.T) {
	reg := provider.NewRegistry()
	mock := newControllableMockProvider("jail", provider.ProviderTypeContainer)
	if err := reg.Register(mock); err != nil {
		t.Fatalf("register mock: %v", err)
	}

	// Second instance ("web", alphabetically after "app") fails to create
	mock.setCreateFail("teststack_web", fmt.Errorf("image not found"))
	// The first instance's DeleteInstance will fail during rollback
	mock.setDeleteFail("teststack_app", fmt.Errorf("ZFS destroy failed: dataset busy"))

	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		registry:   reg,
		serviceReg: NewServiceRegistry(),
		logger:     testLogger(),
	}

	stackManifest := &manifest.StackManifest{
		Stack: manifest.StackMeta{Name: "teststack"},
		Instances: []manifest.InstanceConfig{
			{Name: "web", Provider: "jail", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
			{Name: "app", Provider: "jail", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
		},
	}

	ctx := context.Background()
	err := sm.DeployStack(ctx, stackManifest)
	if err == nil {
		t.Fatal("expected DeployStack to fail, got nil")
	}

	// Verify: stop and delete were both attempted; delete failed but doesn't crash
	created, started, stopped, deleted := mock.callRecords()
	if len(stopped) != 1 {
		t.Errorf("expected 1 stop, got %d: %v", len(stopped), stopped)
	}
	if stopped[0] != "teststack_app" {
		t.Errorf("expected app stopped in rollback, got %s", stopped[0])
	}
	if len(deleted) != 1 {
		t.Errorf("expected 1 delete attempt (even though it failed), got %d: %v", len(deleted), deleted)
	}
	if deleted[0] != "teststack_app" {
		t.Errorf("expected app deleted in rollback, got %s", deleted[0])
	}
	t.Logf("created=%v started=%v stopped=%v deleted=%v", created, started, stopped, deleted)
}

// ── Test: Dependency ordering — B depends on A; B fails, A rolled back ──────

func TestDeployStackDependencyFailureRollback(t *testing.T) {
	reg := provider.NewRegistry()
	mock := newControllableMockProvider("jail", provider.ProviderTypeContainer)
	if err := reg.Register(mock); err != nil {
		t.Fatalf("register mock: %v", err)
	}

	// Instance C (leaf) fails to start; A and B should be rolled back
	mock.setStartFail("teststack_c", fmt.Errorf("crashed on start"))

	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		registry:   reg,
		serviceReg: NewServiceRegistry(),
		logger:     testLogger(),
	}

	// A → B → C  (C depends on B, B depends on A)
	stackManifest := &manifest.StackManifest{
		Stack: manifest.StackMeta{Name: "teststack"},
		Instances: []manifest.InstanceConfig{
			{
				Name: "c", Provider: "jail",
				Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"},
				DependsOn: &manifest.DependsOnConfig{
					Services: []string{"b"},
				},
			},
			{
				Name: "b", Provider: "jail",
				Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"},
				DependsOn: &manifest.DependsOnConfig{
					Services: []string{"a"},
				},
			},
			{
				Name: "a", Provider: "jail",
				Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"},
			},
		},
	}

	ctx := context.Background()
	err := sm.DeployStack(ctx, stackManifest)
	if err == nil {
		t.Fatal("expected DeployStack to fail, got nil")
	}
	t.Logf("DeployStack error (expected): %v", err)

	// Verify topological order: a → b → c
	created, started, stopped, deleted := mock.callRecords()
	if len(created) != 3 {
		t.Fatalf("expected 3 creates, got %d: %v", len(created), created)
	}
	if created[0] != "teststack_a" {
		t.Errorf("expected 'a' created first (no deps), got %s", created[0])
	}
	if created[1] != "teststack_b" {
		t.Errorf("expected 'b' created second, got %s", created[1])
	}
	if created[2] != "teststack_c" {
		t.Errorf("expected 'c' created third, got %s", created[2])
	}
	if len(started) != 3 {
		t.Errorf("expected 3 start attempts, got %d: %v", len(started), started)
	}
	// C failed to start → all three should be rolled back
	if len(stopped) != 3 {
		t.Errorf("expected 3 stops (rollback all), got %d: %v", len(stopped), stopped)
	}
	if len(deleted) != 3 {
		t.Errorf("expected 3 deletes (rollback all), got %d: %v", len(deleted), deleted)
	}

	stack := sm.GetStack("teststack")
	if stack == nil {
		t.Fatal("stack should still exist after failed deploy")
	}
	if stack.Status != StackStatusFailed {
		t.Errorf("expected status failed, got %s", stack.Status)
	}
}

// ── Test: rollbackCreatedInstances directly (unit test of the function) ──────

func TestRollbackCreatedInstances(t *testing.T) {
	reg := provider.NewRegistry()
	mock := newControllableMockProvider("jail", provider.ProviderTypeContainer)
	if err := reg.Register(mock); err != nil {
		t.Fatalf("register mock: %v", err)
	}

	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		registry:   reg,
		serviceReg: NewServiceRegistry(),
		logger:     testLogger(),
	}

	// Three handles: all should be stopped then deleted
	handles := []createdInstance{
		{Name: "web", Handle: provider.InstanceHandle{ID: "mystack_web", Provider: "jail"}, Provider: "jail"},
		{Name: "db", Handle: provider.InstanceHandle{ID: "mystack_db", Provider: "jail"}, Provider: "jail"},
		{Name: "cache", Handle: provider.InstanceHandle{ID: "mystack_cache", Provider: "jail"}, Provider: "jail"},
	}

	ctx := context.Background()
	// This function does not return errors; it logs warnings internally.
	sm.rollbackCreatedInstances(ctx, "mystack", handles)

	created, started, stopped, deleted := mock.callRecords()
	if len(created) != 0 {
		t.Errorf("expected 0 creates (rollback only), got %d", len(created))
	}
	if len(started) != 0 {
		t.Errorf("expected 0 starts, got %d", len(started))
	}
	if len(stopped) != 3 {
		t.Errorf("expected 3 stops, got %d: %v", len(stopped), stopped)
	}
	if len(deleted) != 3 {
		t.Errorf("expected 3 deletes, got %d: %v", len(deleted), deleted)
	}
}

func TestRollbackCreatedInstancesProviderMissing(t *testing.T) {
	reg := provider.NewRegistry()
	// Do NOT register any provider — this simulates a provider that went away

	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		registry:   reg,
		serviceReg: NewServiceRegistry(),
		logger:     testLogger(),
	}

	handles := []createdInstance{
		{Name: "ghost", Handle: provider.InstanceHandle{ID: "mystack_ghost", Provider: "nonexistent"}, Provider: "nonexistent"},
	}

	ctx := context.Background()
	// Should not panic; should just log a warning
	sm.rollbackCreatedInstances(ctx, "mystack", handles)
}

func TestRollbackCreatedInstancesStopFailsDeleteProceeds(t *testing.T) {
	reg := provider.NewRegistry()
	mock := newControllableMockProvider("jail", provider.ProviderTypeContainer)
	if err := reg.Register(mock); err != nil {
		t.Fatalf("register mock: %v", err)
	}

	mock.setStopFail("mystack_app", fmt.Errorf("force stop rejected"))
	mock.setDeleteFail("mystack_app", fmt.Errorf("dataset still busy"))

	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		registry:   reg,
		serviceReg: NewServiceRegistry(),
		logger:     testLogger(),
	}

	handles := []createdInstance{
		{Name: "app", Handle: provider.InstanceHandle{ID: "mystack_app", Provider: "jail"}, Provider: "jail"},
	}

	ctx := context.Background()
	sm.rollbackCreatedInstances(ctx, "mystack", handles)

	_, _, stopped, deleted := mock.callRecords()
	if len(stopped) != 1 {
		t.Errorf("expected 1 stop attempt, got %d: %v", len(stopped), stopped)
	}
	if len(deleted) != 1 {
		t.Errorf("expected 1 delete attempt (proceeds after stop failure), got %d: %v", len(deleted), deleted)
	}
}

// ── Test: Empty stack deploy (no instances) ──────────────────────────────────

func TestDeployStackEmptyInstances(t *testing.T) {
	reg := provider.NewRegistry()

	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		registry:   reg,
		serviceReg: NewServiceRegistry(),
		logger:     testLogger(),
	}

	stackManifest := &manifest.StackManifest{
		Stack:     manifest.StackMeta{Name: "empty-stack"},
		Instances: []manifest.InstanceConfig{},
	}

	ctx := context.Background()
	err := sm.DeployStack(ctx, stackManifest)
	if err != nil {
		t.Fatalf("DeployStack with no instances should succeed, got: %v", err)
	}

	stack := sm.GetStack("empty-stack")
	if stack == nil {
		t.Fatal("stack not found after deploy")
	}
	if stack.Status != StackStatusRunning {
		t.Errorf("expected status running, got %s", stack.Status)
	}
	if len(stack.Instances) != 0 {
		t.Errorf("expected 0 instances, got %d", len(stack.Instances))
	}
}

// ── Test: Provider not available in registry ─────────────────────────────────

func TestDeployStackProviderNotAvailable(t *testing.T) {
	reg := provider.NewRegistry()
	// Don't register anything — provider will not be found

	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		registry:   reg,
		serviceReg: NewServiceRegistry(),
		logger:     testLogger(),
	}

	stackManifest := &manifest.StackManifest{
		Stack: manifest.StackMeta{Name: "teststack"},
		Instances: []manifest.InstanceConfig{
			{Name: "web", Provider: "nonexistent", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
		},
	}

	ctx := context.Background()
	err := sm.DeployStack(ctx, stackManifest)
	if err == nil {
		t.Fatal("expected error for unavailable provider")
	}

	stack := sm.GetStack("teststack")
	if stack == nil {
		t.Fatal("stack should still exist")
	}
	if stack.Status != StackStatusFailed {
		t.Errorf("expected status failed, got %s", stack.Status)
	}
}

// ── Test: Multiple instances with multiple failures ──────────────────────────

func TestDeployStackMultipleFailuresRollback(t *testing.T) {
	reg := provider.NewRegistry()
	mock := newControllableMockProvider("jail", provider.ProviderTypeContainer)
	if err := reg.Register(mock); err != nil {
		t.Fatalf("register mock: %v", err)
	}

	// Instance 3 of 5 fails on create
	mock.setCreateFail("teststack_c", fmt.Errorf("disk full"))

	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		registry:   reg,
		serviceReg: NewServiceRegistry(),
		logger:     testLogger(),
	}

	stackManifest := &manifest.StackManifest{
		Stack: manifest.StackMeta{Name: "teststack"},
		Instances: []manifest.InstanceConfig{
			{Name: "a", Provider: "jail", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
			{Name: "b", Provider: "jail", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
			{Name: "c", Provider: "jail", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
			{Name: "d", Provider: "jail", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
			{Name: "e", Provider: "jail", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
		},
	}

	ctx := context.Background()
	err := sm.DeployStack(ctx, stackManifest)
	if err == nil {
		t.Fatal("expected DeployStack to fail, got nil")
	}

	_, started, stopped, deleted := mock.callRecords()
	if len(started) != 2 {
		t.Errorf("expected 2 starts (a, b succeeded), got %d: %v", len(started), started)
	}
	// a and b were created and started; they should both be rolled back
	if len(stopped) != 2 {
		t.Errorf("expected 2 stops (rollback of a, b), got %d: %v", len(stopped), stopped)
	}
	if len(deleted) != 2 {
		t.Errorf("expected 2 deletes (rollback of a, b), got %d: %v", len(deleted), deleted)
	}
	// d and e were never reached
	for _, name := range started {
		if name == "teststack_d" || name == "teststack_e" {
			t.Errorf("instance %s should not have been started", name)
		}
	}
}

// ── Test: updateStackStatus ──────────────────────────────────────────────────

func TestUpdateStackStatus(t *testing.T) {
	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		serviceReg: NewServiceRegistry(),
	}

	// Initial status
	stack := &Stack{
		Name:      "mystack",
		Status:    StackStatusDeploying,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	sm.stacks["mystack"] = stack

	sm.updateStackStatus("mystack", StackStatusRunning)

	if sm.stacks["mystack"].Status != StackStatusRunning {
		t.Errorf("expected running, got %s", sm.stacks["mystack"].Status)
	}

	sm.updateStackStatus("mystack", StackStatusFailed)
	if sm.stacks["mystack"].Status != StackStatusFailed {
		t.Errorf("expected failed, got %s", sm.stacks["mystack"].Status)
	}

	// Update non-existent stack (should not panic)
	sm.updateStackStatus("nonexistent", StackStatusRunning)
}

// ── Test: Circular dependency detection ──────────────────────────────────────

func TestDeployStackCircularDependency(t *testing.T) {
	reg := provider.NewRegistry()
	mock := newControllableMockProvider("jail", provider.ProviderTypeContainer)
	if err := reg.Register(mock); err != nil {
		t.Fatalf("register mock: %v", err)
	}

	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		registry:   reg,
		serviceReg: NewServiceRegistry(),
		logger:     testLogger(),
	}

	stackManifest := &manifest.StackManifest{
		Stack: manifest.StackMeta{Name: "circtest"},
		Instances: []manifest.InstanceConfig{
			{
				Name: "a", Provider: "jail",
				Image:     manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"},
				DependsOn: &manifest.DependsOnConfig{Services: []string{"b"}},
			},
			{
				Name: "b", Provider: "jail",
				Image:     manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"},
				DependsOn: &manifest.DependsOnConfig{Services: []string{"a"}},
			},
		},
	}

	ctx := context.Background()
	err := sm.DeployStack(ctx, stackManifest)
	if err == nil {
		t.Fatal("expected error for circular dependency")
	}
	if stack := sm.GetStack("circtest"); stack == nil || stack.Status != StackStatusFailed {
		t.Errorf("expected stack to be marked as failed, status=%v", stack)
	}
}

// ── Test: Existing stack detection ───────────────────────────────────────────

func TestDeployStackAlreadyExists(t *testing.T) {
	reg := provider.NewRegistry()

	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		registry:   reg,
		serviceReg: NewServiceRegistry(),
		logger:     testLogger(),
	}

	// Pre-register a stack
	sm.stacks["existing"] = &Stack{
		Name:   "existing",
		Status: StackStatusRunning,
	}

	stackManifest := &manifest.StackManifest{
		Stack:     manifest.StackMeta{Name: "existing"},
		Instances: []manifest.InstanceConfig{},
	}

	ctx := context.Background()
	err := sm.DeployStack(ctx, stackManifest)
	if err == nil {
		t.Fatal("expected error for duplicate stack name")
	}
}

// ── Test: Context cancellation during deploy ─────────────────────────────────

func TestDeployStackContextCancellation(t *testing.T) {
	reg := provider.NewRegistry()
	mock := newControllableMockProvider("jail", provider.ProviderTypeContainer)
	if err := reg.Register(mock); err != nil {
		t.Fatalf("register mock: %v", err)
	}

	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		registry:   reg,
		serviceReg: NewServiceRegistry(),
		logger:     testLogger(),
	}

	stackManifest := &manifest.StackManifest{
		Stack: manifest.StackMeta{Name: "ctxcancel"},
		Instances: []manifest.InstanceConfig{
			{Name: "a", Provider: "jail", Image: manifest.ImageSpec{Source: "freebsd:14.3-RELEASE"}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately (before any instance is created)

	err := sm.DeployStack(ctx, stackManifest)
	if err == nil {
		t.Fatal("expected error from context cancellation")
	}
}
