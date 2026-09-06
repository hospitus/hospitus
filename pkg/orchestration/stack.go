package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/manifest"
	"github.com/hospitus/hospitus/pkg/provider"
)

// StackStore defines the persistence interface for stacks.
// Implemented by *datastore.Datastore.
type StackStore interface {
	CreateStack(ctx context.Context, stack *datastore.StackRecord) error
	GetStack(ctx context.Context, name string) (*datastore.StackRecord, error)
	ListStacks(ctx context.Context) ([]*datastore.StackRecord, error)
	UpdateStackStatus(ctx context.Context, name, status string) error
	DeleteStack(ctx context.Context, name string) error
	CreateStackInstance(ctx context.Context, inst *datastore.StackInstanceRecord) error
	GetStackInstances(ctx context.Context, stackName string) ([]*datastore.StackInstanceRecord, error)
	UpdateStackInstanceStatus(ctx context.Context, stackName, instanceName, status string) error
	UpdateStackInstanceHealth(ctx context.Context, stackName, instanceName, health string) error
	DeleteStackInstance(ctx context.Context, stackName, instanceName string) error
	DeleteStackInstances(ctx context.Context, stackName string) error
}

// StackStatus represents the overall status of a stack
type StackStatus string

const (
	StackStatusPending    StackStatus = "pending"
	StackStatusDeploying  StackStatus = "deploying"
	StackStatusRunning    StackStatus = "running"
	StackStatusDegraded   StackStatus = "degraded" // Some instances unhealthy
	StackStatusStopped    StackStatus = "stopped"
	StackStatusFailed     StackStatus = "failed"
	StackStatusDestroying StackStatus = "destroying"
)

// stackInstanceDelimiter separates stack name from instance name in composite IDs.
// Using underscore (_) because stack names cannot contain underscores (only hyphens),
// reducing collision risk compared to hyphen (-).
const stackInstanceDelimiter = "_"

// Stack represents a deployed stack
type Stack struct {
	Name      string
	Instances []*StackInstance
	Status    StackStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

// StackInstance represents an instance within a stack
type StackInstance struct {
	Name       string
	InstanceID string // Actual instance ID (stack_instancename)
	Provider   string
	DependsOn  []string
	Status     string
	Health     HealthStatus
	Handle     provider.InstanceHandle
}

// createdInstance tracks an instance created during DeployStack so it can be
// rolled back if a later step fails.
type createdInstance struct {
	Name     string
	Handle   provider.InstanceHandle
	Provider string
}

// cloneStack returns a deep copy of a Stack so callers never share the pointer
// the manager keeps mutating under its lock.
func cloneStack(s *Stack) *Stack {
	if s == nil {
		return nil
	}
	c := *s
	c.Instances = make([]*StackInstance, len(s.Instances))
	for i, inst := range s.Instances {
		ci := *inst
		if inst.DependsOn != nil {
			ci.DependsOn = append([]string(nil), inst.DependsOn...)
		}
		c.Instances[i] = &ci
	}
	return &c
}

// StackManager manages the lifecycle of multi-instance stacks
type StackManager struct {
	mu sync.RWMutex
	// statusLocks orders status persistence, one lock per stack; see
	// updateStackStatus.
	statusLocks   sync.Map
	stacks        map[string]*Stack
	registry      *provider.Registry
	store         StackStore
	serviceReg    *ServiceRegistry
	healthChecker *HealthChecker
	autoRestarter *AutoRestarter
	logger        *slog.Logger

	// cloudInitRoot is the only directory an instance's user_data_file may
	// name. Empty refuses the field, which is what a manager built without a
	// data directory gets.
	cloudInitRoot string
}

// NewStackManager creates a new stack manager.
// If store is provided, stacks are persisted and restored on startup.
func NewStackManager(registry *provider.Registry, store StackStore) *StackManager {
	sm := &StackManager{
		stacks:     make(map[string]*Stack),
		registry:   registry,
		store:      store,
		serviceReg: NewServiceRegistry(),
		logger:     logging.WithComponent("stack"),
	}
	return sm
}

// SetCloudInitRoot names the directory a manifest's cloud_init.user_data_file
// may read from. Until it is set, that field is refused rather than resolved
// against the whole filesystem by a daemon running as root.
func (sm *StackManager) SetCloudInitRoot(root string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.cloudInitRoot = root
}

// CloudInitRoot reports the directory user_data_file is confined to.
func (sm *StackManager) CloudInitRoot() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.cloudInitRoot
}

// LoadStacks restores stack state from the persistence store.
// It rebuilds in-memory Stack objects from database records.
func (sm *StackManager) LoadStacks(ctx context.Context) error {
	if sm.store == nil {
		return nil
	}

	records, err := sm.store.ListStacks(ctx)
	if err != nil {
		return fmt.Errorf("failed to list stacks from store: %w", err)
	}

	for _, rec := range records {
		instances, err := sm.store.GetStackInstances(ctx, rec.Name)
		if err != nil {
			sm.logger.Warn("Failed to load stack instances, skipping stack",
				"stack", rec.Name, logging.FieldError, err)
			continue
		}

		stack := &Stack{
			Name:      rec.Name,
			Status:    StackStatus(rec.Status),
			CreatedAt: rec.CreatedAt,
			UpdatedAt: rec.UpdatedAt,
		}

		for _, instRec := range instances {
			var handle provider.InstanceHandle
			if instRec.Handle != "" {
				if err := json.Unmarshal([]byte(instRec.Handle), &handle); err != nil {
					sm.logger.Warn("Failed to unmarshal instance handle, skipping",
						"stack", rec.Name, "instance", instRec.InstanceName, logging.FieldError, err)
					continue
				}
			}

			stackInst := &StackInstance{
				Name:       instRec.InstanceName,
				InstanceID: instRec.InstanceID,
				Provider:   instRec.Provider,
				DependsOn:  instRec.DependsOn,
				Status:     instRec.Status,
				Health:     HealthStatus(instRec.Health),
				Handle:     handle,
			}
			stack.Instances = append(stack.Instances, stackInst)
		}

		sm.mu.Lock()
		sm.stacks[rec.Name] = stack
		sm.mu.Unlock()

		sm.logger.Info("Restored stack from persistence",
			"stack", rec.Name, "instances", len(stack.Instances), "status", rec.Status)
	}

	return nil
}

// SetHealthChecker sets the health checker for the stack manager
func (sm *StackManager) SetHealthChecker(hc *HealthChecker) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.healthChecker = hc

	// Register callback to update stack status on health changes
	hc.OnHealthChange(sm.onHealthChange)
}

// SetAutoRestarter sets the auto restarter for the stack manager
func (sm *StackManager) SetAutoRestarter(ar *AutoRestarter) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.autoRestarter = ar
}

// GetServiceRegistry returns the service registry
func (sm *StackManager) GetServiceRegistry() *ServiceRegistry {
	return sm.serviceReg
}

// DeployStack deploys a stack from a manifest
func (sm *StackManager) DeployStack(ctx context.Context, stackManifest *manifest.StackManifest) error {
	stackName := stackManifest.Stack.Name

	sm.mu.Lock()
	if _, exists := sm.stacks[stackName]; exists {
		sm.mu.Unlock()
		return fmt.Errorf("stack %s already exists", stackName)
	}

	stack := &Stack{
		Name:      stackName,
		Status:    StackStatusDeploying,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	sm.stacks[stackName] = stack
	sm.mu.Unlock()

	// Persist stack record
	if sm.store != nil {
		rec := &datastore.StackRecord{
			Name:   stackName,
			Status: string(StackStatusDeploying),
		}
		if manifestJSON, err := json.Marshal(stackManifest); err != nil {
			sm.logger.Warn("Failed to marshal stack manifest for persistence",
				"stack", stackName, logging.FieldError, err)
		} else {
			rec.Manifest = string(manifestJSON)
		}
		if err := sm.store.CreateStack(ctx, rec); err != nil {
			sm.mu.Lock()
			delete(sm.stacks, stackName)
			sm.mu.Unlock()
			return fmt.Errorf("failed to persist stack: %w", err)
		}
	}

	order, err := sm.buildDependencyOrder(stackManifest.Instances)
	if err != nil {
		sm.updateStackStatus(stackName, StackStatusFailed)
		return fmt.Errorf("failed to resolve dependencies: %w", err)
	}

	// Deploy instances in order with rollback on failure
	var createdHandles []createdInstance
	for i := range order {
		inst := order[i]
		instanceID := stackName + stackInstanceDelimiter + inst.Name

		sm.logger.Info("Deploying instance in stack",
			logging.FieldAction, "deploy",
			logging.FieldInstance, inst.Name,
			"stack", stackName)

		// Wait for dependencies to be healthy if condition is "healthy"
		if inst.DependsOn != nil && inst.DependsOn.Condition == manifest.DependsOnConditionHealthy {
			if err := sm.waitForDependencies(ctx, stackName, inst.DependsOn.Services); err != nil {
				sm.rollbackCreatedInstances(ctx, stackName, createdHandles)
				sm.updateStackStatus(stackName, StackStatusFailed)
				return fmt.Errorf("dependency wait failed for %s: %w", inst.Name, err)
			}
		}

		// Create the instance
		prov, err := sm.registry.Get(inst.Provider)
		if err != nil {
			sm.rollbackCreatedInstances(ctx, stackName, createdHandles)
			sm.updateStackStatus(stackName, StackStatusFailed)
			return fmt.Errorf("provider %s not available: %w", inst.Provider, err)
		}

		spec, err := sm.instanceToSpec(inst, instanceID)
		if err != nil {
			sm.rollbackCreatedInstances(ctx, stackName, createdHandles)
			sm.updateStackStatus(stackName, StackStatusFailed)
			return fmt.Errorf("instance %s: %w", inst.Name, err)
		}
		handle, err := prov.CreateInstance(ctx, spec)
		if err != nil {
			sm.rollbackCreatedInstances(ctx, stackName, createdHandles)
			sm.updateStackStatus(stackName, StackStatusFailed)
			return fmt.Errorf("failed to create instance %s: %w", inst.Name, err)
		}

		createdHandles = append(createdHandles, createdInstance{inst.Name, handle, inst.Provider})

		if err := prov.StartInstance(ctx, handle); err != nil {
			sm.rollbackCreatedInstances(ctx, stackName, createdHandles)
			sm.updateStackStatus(stackName, StackStatusFailed)
			return fmt.Errorf("failed to start instance %s: %w", inst.Name, err)
		}

		// Register in stack
		stackInst := &StackInstance{
			Name:       inst.Name,
			InstanceID: instanceID,
			Provider:   inst.Provider,
			Status:     "running",
			Health:     HealthStatusStarting,
			Handle:     handle,
		}
		if inst.DependsOn != nil {
			stackInst.DependsOn = inst.DependsOn.Services
		}

		sm.mu.Lock()
		stack.Instances = append(stack.Instances, stackInst)
		stack.UpdatedAt = time.Now()
		// Capture the deploy order under the lock; reading len(stack.Instances)
		// later (outside the lock) would race with concurrent mutations.
		deployOrder := len(stack.Instances)
		sm.mu.Unlock()

		// Persist stack instance
		if sm.store != nil {
			// A handle that cannot be encoded is a record no restart will be
			// able to reach the instance through — the same outcome the
			// CreateStackInstance failure below refuses to accept, so it is
			// refused here too instead of persisting an empty Handle.
			handleJSON, err := json.Marshal(handle)
			if err != nil {
				sm.rollbackCreatedInstances(ctx, stackName, createdHandles)
				sm.updateStackStatus(stackName, StackStatusFailed)
				return fmt.Errorf("failed to encode handle for instance %s in stack %s: %w", inst.Name, stackName, err)
			}
			instRec := &datastore.StackInstanceRecord{
				StackName:    stackName,
				InstanceName: inst.Name,
				InstanceID:   instanceID,
				Provider:     inst.Provider,
				Status:       "running",
				Health:       string(HealthStatusStarting),
				Handle:       string(handleJSON),
				DeployOrder:  deployOrder,
			}
			if inst.DependsOn != nil {
				instRec.DependsOn = inst.DependsOn.Services
			}
			// An instance the stack cannot record is one no restart will find
			// again: it keeps running, outside the stack that made it. The
			// deployment fails here rather than reporting a stack that is
			// already incomplete.
			if err := sm.store.CreateStackInstance(ctx, instRec); err != nil {
				sm.rollbackCreatedInstances(ctx, stackName, createdHandles)
				sm.updateStackStatus(stackName, StackStatusFailed)
				return fmt.Errorf("failed to record instance %s in stack %s: %w", inst.Name, stackName, err)
			}
		}

		// Register service for discovery
		sm.registerService(stackName, inst, handle)

		// Setup health checking if the manifest defines a health check
		if sm.healthChecker != nil && inst.Lifecycle.HealthCheck != nil {
			if cfg := healthSpecToConfig(inst.Lifecycle.HealthCheck); cfg != nil {
				sm.healthChecker.RegisterInstance(instanceID, cfg)
				// The request context ends with the HTTP response; the check
				// loop must not.
				sm.healthChecker.StartChecking(context.WithoutCancel(ctx), handle)
			}
		}

		// And the restart policy, with the provider that owns this instance
		// and the handle its creation returned. Nothing registered one before,
		// so onHealthChange found no policy and returned before restarting
		// anything — the auto-restarter was wired up and unreachable.
		if sm.autoRestarter != nil {
			if policy := restartSpecToPolicy(inst.Lifecycle.Restart); policy != nil {
				sm.autoRestarter.SetPolicyFor(prov, handle, policy)
			}
		}

		// Brief delay between instances
		select {
		case <-ctx.Done():
			// Cancellation mid-deploy must not leave orphaned instances or a
			// stack stuck in "deploying"; roll back and mark it failed like the
			// other error paths.
			sm.rollbackCreatedInstances(ctx, stackName, createdHandles)
			sm.updateStackStatus(stackName, StackStatusFailed)
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	sm.updateStackStatus(stackName, StackStatusRunning)
	sm.logger.Info("Stack deployed successfully",
		logging.FieldAction, "deploy",
		"stack", stackName,
		"instances", len(order))

	return nil
}

// healthSpecToConfig converts a manifest health-check spec into a runtime
// HealthCheckConfig, applying sensible defaults. It returns nil when the spec
// defines no command, since a health check without a command cannot run.
func healthSpecToConfig(spec *manifest.HealthCheckSpec) *HealthCheckConfig {
	if spec == nil || len(spec.Command) == 0 {
		return nil
	}
	cfg := &HealthCheckConfig{
		Command:     spec.Command,
		Interval:    parseDurationDefault(spec.Interval, 30*time.Second),
		Timeout:     parseDurationDefault(spec.Timeout, 10*time.Second),
		Retries:     spec.Retries,
		StartPeriod: parseDurationDefault(spec.StartPeriod, 0),
	}
	if cfg.Retries <= 0 {
		cfg.Retries = defaultHealthRetries
	}
	return cfg
}

// parseDurationDefault parses a Go duration string, returning def when the
// string is empty, invalid, or non-positive.
func parseDurationDefault(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// StopStack stops all instances in a stack
func (sm *StackManager) StopStack(ctx context.Context, stackName string) error {
	sm.mu.RLock()
	stack, ok := sm.stacks[stackName]
	if !ok {
		sm.mu.RUnlock()
		return fmt.Errorf("stack %s not found", stackName)
	}
	instances := make([]*StackInstance, len(stack.Instances))
	copy(instances, stack.Instances)
	sm.mu.RUnlock()

	// Stop in reverse dependency order
	var failures []error
	for i := len(instances) - 1; i >= 0; i-- {
		inst := instances[i]

		// Before the provider lookup, so it happens even when that fails: the
		// operator asked for the stack to stop, and a check loop left running
		// against an instance whose provider is gone produces failures the
		// auto-restarter cannot act on either.
		if sm.healthChecker != nil {
			sm.healthChecker.StopChecking(inst.InstanceID)
		}

		prov, err := sm.registry.Get(inst.Provider)
		if err != nil {
			failures = append(failures, fmt.Errorf("instance %s: provider %s unavailable: %w", inst.Name, inst.Provider, err))
			sm.logger.Warn("Provider not available for instance in stack",
				logging.FieldAction, "stop",
				logging.FieldProvider, inst.Provider,
				logging.FieldInstance, inst.InstanceID,
				"stack", stackName)
			continue
		}

		sm.logger.Info("Stopping instance in stack",
			logging.FieldAction, "stop",
			logging.FieldInstance, inst.Name,
			"stack", stackName)
		if err := prov.StopInstance(ctx, inst.Handle, provider.StopOptions{Force: false}); err != nil {
			// Still running, so it is watched again — the same as DestroyStack
			// does after a failed delete. The operator now has an instance to
			// deal with by hand, and its health is part of that.
			if sm.healthChecker != nil {
				sm.healthChecker.StartChecking(context.WithoutCancel(ctx), inst.Handle)
			}
			sm.logger.Warn("Failed to stop instance in stack",
				logging.FieldAction, "stop",
				logging.FieldInstance, inst.InstanceID,
				logging.FieldError, err)
			failures = append(failures, fmt.Errorf("instance %s: %w", inst.Name, err))
			continue
		}
	}

	// A stack marked stopped over instances that are still running tells the
	// operator the opposite of what the host is doing, and the API reports
	// success for work that did not happen.
	if len(failures) > 0 {
		sm.updateStackStatus(stackName, StackStatusDegraded)
		return fmt.Errorf("stack %s is not fully stopped: %w", stackName, errors.Join(failures...))
	}

	sm.updateStackStatus(stackName, StackStatusStopped)
	return nil
}

// StartStack starts all instances in a stack
func (sm *StackManager) StartStack(ctx context.Context, stackName string) error {
	sm.mu.RLock()
	stack, ok := sm.stacks[stackName]
	if !ok {
		sm.mu.RUnlock()
		return fmt.Errorf("stack %s not found", stackName)
	}
	instances := make([]*StackInstance, len(stack.Instances))
	copy(instances, stack.Instances)
	sm.mu.RUnlock()

	// Start in dependency order (instances are already sorted)
	for _, inst := range instances {
		prov, err := sm.registry.Get(inst.Provider)
		if err != nil {
			// Marked before returning, like every other start failure: the
			// stack kept whatever status it had, and an operator reading it
			// saw a stack that looked fine with nothing started.
			sm.updateStackStatus(stackName, StackStatusDegraded)
			return fmt.Errorf("provider %s not available: %w", inst.Provider, err)
		}

		sm.logger.Info("Starting instance in stack",
			logging.FieldAction, "start",
			logging.FieldInstance, inst.Name,
			"stack", stackName)
		if err := prov.StartInstance(ctx, inst.Handle); err != nil {
			sm.updateStackStatus(stackName, StackStatusDegraded)
			return fmt.Errorf("failed to start %s: %w", inst.InstanceID, err)
		}

		// Resume health checking
		if sm.healthChecker != nil {
			sm.healthChecker.StartChecking(context.WithoutCancel(ctx), inst.Handle)
		}

		// Brief delay between instances
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	sm.updateStackStatus(stackName, StackStatusRunning)
	return nil
}

// DestroyStack destroys all instances in a stack
func (sm *StackManager) DestroyStack(ctx context.Context, stackName string) error {
	sm.mu.Lock()
	stack, ok := sm.stacks[stackName]
	if !ok {
		sm.mu.Unlock()
		return fmt.Errorf("stack %s not found", stackName)
	}
	stack.Status = StackStatusDestroying
	instances := make([]*StackInstance, len(stack.Instances))
	copy(instances, stack.Instances)
	sm.mu.Unlock()

	// Destroy in reverse dependency order
	var failures []error
	for i := len(instances) - 1; i >= 0; i-- {
		inst := instances[i]

		prov, err := sm.registry.Get(inst.Provider)
		if err != nil {
			failures = append(failures, fmt.Errorf("instance %s: provider %s unavailable: %w", inst.Name, inst.Provider, err))
			sm.logger.Warn("Provider not available for instance in stack",
				logging.FieldAction, "destroy",
				logging.FieldProvider, inst.Provider,
				logging.FieldInstance, inst.InstanceID,
				"stack", stackName)
			continue
		}

		sm.logger.Info("Destroying instance in stack",
			logging.FieldAction, "destroy",
			logging.FieldInstance, inst.Name,
			"stack", stackName)
		// Stopped before the deletion, unregistered after it: the check loop
		// must not probe an instance being removed, but the records that let
		// an operator find it again stay until it is actually gone.
		if sm.healthChecker != nil {
			sm.healthChecker.StopChecking(inst.InstanceID)
		}

		if err := prov.DeleteInstance(ctx, inst.Handle, false); err != nil {
			// Still there, so it is watched again: the instance survives the
			// failed destroy and the operator has to deal with it, which is
			// easier with its health still reported.
			if sm.healthChecker != nil {
				sm.healthChecker.StartChecking(context.WithoutCancel(ctx), inst.Handle)
			}
			failures = append(failures, fmt.Errorf("instance %s: %w", inst.Name, err))
			sm.logger.Warn("Failed to delete instance in stack",
				logging.FieldAction, "destroy",
				logging.FieldInstance, inst.InstanceID,
				logging.FieldError, err)
			continue
		}

		// After the deletion, not before it: an instance that would not delete
		// used to lose its service entry, its health check and its restart
		// policy anyway, so it stayed running and unmanaged while the stack
		// reported it as degraded.
		sm.serviceReg.Unregister(stackName, inst.Name)
		if sm.healthChecker != nil {
			sm.healthChecker.UnregisterInstance(inst.InstanceID)
		}
		if sm.autoRestarter != nil {
			sm.autoRestarter.RemovePolicy(inst.InstanceID)
		}
	}

	// The stack record is what an operator reaches the survivors through.
	// Deleting it over instances that are still there leaves them running with
	// nothing to manage them by.
	if len(failures) > 0 {
		sm.updateStackStatus(stackName, StackStatusDegraded)
		return fmt.Errorf("stack %s is not fully destroyed and its record is kept: %w",
			stackName, errors.Join(failures...))
	}

	// Remove from persistence
	if sm.store != nil {
		if err := sm.store.DeleteStack(ctx, stackName); err != nil {
			return fmt.Errorf("stack %s instances are gone but its record remains: %w", stackName, err)
		}
	}

	sm.mu.Lock()
	delete(sm.stacks, stackName)
	sm.mu.Unlock()

	// The stack's status lock goes with it, or statusLocks grows by one entry
	// for every stack ever destroyed. Taken first, so an update still in
	// flight finishes before the entry is dropped.
	if lock, ok := sm.statusLocks.Load(stackName); ok {
		mu := lock.(*sync.Mutex)
		mu.Lock()
		sm.statusLocks.Delete(stackName)
		mu.Unlock()
	}

	sm.logger.Info("Stack destroyed", logging.FieldAction, "destroy", "stack", stackName)
	return nil
}

// GetStack returns information about a stack
func (sm *StackManager) GetStack(stackName string) *Stack {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return cloneStack(sm.stacks[stackName])
}

// ListStacks returns all managed stacks
func (sm *StackManager) ListStacks() []*Stack {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]*Stack, 0, len(sm.stacks))
	for _, stack := range sm.stacks {
		result = append(result, cloneStack(stack))
	}
	return result
}

// buildDependencyOrder returns instances in dependency order (topological sort)
func (sm *StackManager) buildDependencyOrder(instances []manifest.InstanceConfig) ([]manifest.InstanceConfig, error) {
	deps := make(map[string][]string)
	byName := make(map[string]manifest.InstanceConfig)

	for i := range instances {
		inst := instances[i]
		byName[inst.Name] = inst
		if inst.DependsOn != nil {
			deps[inst.Name] = inst.DependsOn.Services
		} else {
			deps[inst.Name] = nil
		}
	}

	// Validate dependency references first so a missing service is reported as
	// such instead of being misdiagnosed as a circular dependency below.
	for name, depList := range deps {
		for _, d := range depList {
			if _, ok := byName[d]; !ok {
				return nil, fmt.Errorf("instance %q depends on unknown service %q", name, d)
			}
		}
	}

	// Topological sort using Kahn's algorithm
	var result []manifest.InstanceConfig
	inDegree := make(map[string]int)

	for name := range byName {
		inDegree[name] = 0
	}

	for name, depList := range deps {
		inDegree[name] = len(depList)
	}

	var queue []string
	for name, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, name)
		}
	}

	// Sort queue for deterministic order
	sort.Strings(queue)

	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]

		if inst, ok := byName[name]; ok {
			result = append(result, inst)
		}

		// Reduce in-degree for dependents
		var newQueue []string
		for depName, depList := range deps {
			for _, d := range depList {
				if d == name {
					inDegree[depName]--
					if inDegree[depName] == 0 {
						newQueue = append(newQueue, depName)
					}
				}
			}
		}
		sort.Strings(newQueue)
		queue = append(queue, newQueue...)
	}

	// Duplicates first: byName collapses two instances sharing a name, so the
	// length mismatch that follows reported "circular dependency" for a
	// manifest whose real fault was a repeated name.
	if len(byName) != len(instances) {
		seen := make(map[string]bool, len(instances))
		for i := range instances {
			if seen[instances[i].Name] {
				return nil, fmt.Errorf("duplicate instance name %q in stack", instances[i].Name)
			}
			seen[instances[i].Name] = true
		}
	}
	if len(result) != len(instances) {
		return nil, fmt.Errorf("circular dependency detected")
	}

	return result, nil
}

// waitForDependencies waits for all dependencies to be healthy
func (sm *StackManager) waitForDependencies(ctx context.Context, stackName string, deps []string) error {
	if sm.healthChecker == nil {
		// No health checker, just wait briefly
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	}

	// Refused before the wait, not after it: an instance with no health check
	// declared can never report healthy, so polling for one spent the full
	// five minutes proving something the registration already knew.
	for _, dep := range deps {
		instanceID := stackName + stackInstanceDelimiter + dep
		if !sm.healthChecker.HasHealthCheck(instanceID) {
			return fmt.Errorf("instance %q is depended on with condition \"healthy\" but declares no lifecycle.health_check", dep)
		}
	}

	timeout := time.After(5 * time.Minute)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		allHealthy := true
		for _, dep := range deps {
			instanceID := stackName + stackInstanceDelimiter + dep
			health := sm.healthChecker.GetHealth(instanceID)
			if health == nil || health.Status != HealthStatusHealthy {
				allHealthy = false
				break
			}
		}

		if allHealthy {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("timeout waiting for dependencies to be healthy")
		case <-ticker.C:
			continue
		}
	}
}

// instanceToSpec converts a stack instance into a provider spec, under the name
// the stack gives it.
//
// The conversion itself belongs to pkg/manifest, which owns the manifest
// contract. The copy that lived here carried the name, image, architecture,
// CPUs, memory and networks and left the rest behind: storage, cloud-init,
// environment, lifecycle and provider overrides never reached the provider, so
// a valid stack deployed something other than what it described.
func (sm *StackManager) instanceToSpec(inst manifest.InstanceConfig, instanceID string) (provider.InstanceSpec, error) {
	spec, err := manifest.InstanceConfigToSpec(&inst, sm.CloudInitRoot())
	if err != nil {
		return provider.InstanceSpec{}, err
	}
	spec.Name = instanceID
	return *spec, nil
}

// registerService registers an instance for service discovery
func (sm *StackManager) registerService(stackName string, inst manifest.InstanceConfig, handle provider.InstanceHandle) {
	info := &ServiceInfo{
		Name:      inst.Name,
		StackName: stackName,
		Status:    "running",
		Health:    HealthStatusStarting,
		Labels:    make(map[string]string),
	}

	// Get IP addresses from network config
	for i := range inst.Networks {
		net := inst.Networks[i]
		if net.IP != nil && net.IP.Address != "" {
			info.Addresses = append(info.Addresses, net.IP.Address)
		}
	}

	// Get ports from network config
	for i := range inst.Networks {
		net := inst.Networks[i]
		for _, port := range net.Ports {
			info.Ports = append(info.Ports, ServicePort{
				Port:     port.Container,
				Protocol: port.Protocol,
			})
		}
	}

	sm.serviceReg.Register(info)
}

// rollbackTimeout bounds the cleanup of a failed deployment. It is generous:
// stopping and destroying several instances can take a while, and giving up
// halfway leaves the operator with a stack that is neither deployed nor gone.
const rollbackTimeout = 2 * time.Minute

// rollbackCreatedInstances deletes instances that were successfully created
// before a deploy failure occurred in a later step.
func (sm *StackManager) rollbackCreatedInstances(ctx context.Context, stackName string, createdHandles []createdInstance) {
	// Cleanup has to outlive the request that triggered it. One of the callers
	// is the cancellation branch, where the caller's context is already done: a
	// provider driving exec.CommandContext would then abort every stop and
	// delete on the spot, leaving behind exactly the instances this exists to
	// remove.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()

	for _, ch := range createdHandles {
		prov, err := sm.registry.Get(ch.Provider)
		if err != nil {
			sm.logger.Warn("Rollback: cannot get provider to clean up instance",
				"stack", stackName, "instance", ch.Name, "provider", ch.Provider, logging.FieldError, err)
			continue
		}
		// Stop first, then delete (ignore errors on stop since instance may not be running)
		_ = prov.StopInstance(ctx, ch.Handle, provider.StopOptions{Force: true})
		if err := prov.DeleteInstance(ctx, ch.Handle, false); err != nil {
			sm.logger.Warn("Rollback: failed to delete instance",
				"stack", stackName, "instance", ch.Name, logging.FieldError, err)
		} else {
			sm.logger.Info("Rollback: cleaned up instance",
				"stack", stackName, "instance", ch.Name)
		}
		// The instance is gone; every record of it has to go too. A stack left
		// listing an instance that no longer exists cannot be destroyed
		// afterwards — DeleteInstance fails on the missing one and the stack
		// stays behind, degraded and undeletable.
		sm.forgetRolledBackInstance(ctx, stackName, ch.Name)
	}
}

// forgetRolledBackInstance removes the traces a created instance left before
// the deployment failed: the in-memory stack entry, the persisted row, the
// service registration and the health check.
func (sm *StackManager) forgetRolledBackInstance(ctx context.Context, stackName, instanceName string) {
	instanceID := stackName + stackInstanceDelimiter + instanceName

	sm.mu.Lock()
	if stack, ok := sm.stacks[stackName]; ok {
		kept := stack.Instances[:0]
		for _, si := range stack.Instances {
			if si.Name != instanceName {
				kept = append(kept, si)
			}
		}
		stack.Instances = kept
	}
	sm.mu.Unlock()

	if sm.healthChecker != nil {
		sm.healthChecker.UnregisterInstance(instanceID)
	}
	if sm.serviceReg != nil {
		sm.serviceReg.Unregister(stackName, instanceName)
	}
	if sm.store != nil {
		if err := sm.store.DeleteStackInstance(ctx, stackName, instanceName); err != nil {
			sm.logger.Warn("Rollback: failed to remove the instance record",
				"stack", stackName, "instance", instanceName, logging.FieldError, err)
		}
	}
}

// updateStackStatus updates the status of a stack
func (sm *StackManager) updateStackStatus(stackName string, status StackStatus) {
	// Held for the whole update, so the order a stack's statuses are persisted
	// in is the order they were applied in memory. sm.mu alone could not do
	// that: releasing it before the store call — which it must, see below —
	// let two callers swap between the unlock and the write, and the database
	// ended up naming the older status.
	//
	// One lock per stack rather than one for all of them: the store write has
	// a five-second budget, and a single mutex would make every stack wait on
	// whichever one is slow to persist.
	lock, _ := sm.statusLocks.LoadOrStore(stackName, &sync.Mutex{})
	statusLock := lock.(*sync.Mutex)
	statusLock.Lock()
	defer statusLock.Unlock()

	sm.mu.Lock()
	if stack, ok := sm.stacks[stackName]; ok {
		stack.Status = status
		stack.UpdatedAt = time.Now()
	}
	// Released before the store call: that is a database write with a
	// five-second budget, and holding sm.mu across it blocked every reader —
	// ListStacks, GetStack, the API's status endpoint — for its duration.
	sm.mu.Unlock()

	// Persist status change
	if sm.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sm.store.UpdateStackStatus(ctx, stackName, string(status)); err != nil {
			sm.logger.Warn("Failed to persist stack status",
				"stack", stackName, "status", status, logging.FieldError, err)
		}
	}
}

// onHealthChange handles health status changes for stack instances
func (sm *StackManager) onHealthChange(instanceID string, oldStatus, newStatus HealthStatus) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Find the stack and instance
	for _, stack := range sm.stacks {
		for _, inst := range stack.Instances {
			if inst.InstanceID == instanceID {
				inst.Health = newStatus

				// Update service registry
				sm.serviceReg.UpdateHealth(stack.Name, inst.Name, newStatus)

				// Update stack status based on instance health
				sm.updateStackHealthStatus(stack)
				return
			}
		}
	}
}

// updateStackHealthStatus updates stack status based on instance health
func (sm *StackManager) updateStackHealthStatus(stack *Stack) {
	if stack.Status == StackStatusStopped || stack.Status == StackStatusDestroying {
		return
	}

	allHealthy := true
	anyUnhealthy := false

	for _, inst := range stack.Instances {
		if inst.Health == HealthStatusUnhealthy {
			anyUnhealthy = true
			allHealthy = false
		} else if inst.Health != HealthStatusHealthy {
			allHealthy = false
		}
	}

	if anyUnhealthy {
		stack.Status = StackStatusDegraded
	} else if allHealthy {
		stack.Status = StackStatusRunning
	}
	stack.UpdatedAt = time.Now()
}

// restartSpecToPolicy converts a manifest restart spec into a runtime policy.
// It returns nil when the manifest asks for nothing, so an instance without
// the block keeps the previous behavior of never being restarted.
func restartSpecToPolicy(spec *manifest.RestartSpec) *RestartPolicy {
	if spec == nil || !spec.Enabled {
		return nil
	}

	policy := &RestartPolicy{
		Enabled:      true,
		MaxRestarts:  spec.MaxRestarts,
		RestartDelay: restartDefaultDelay,
	}
	if spec.Delay != "" {
		if d, err := time.ParseDuration(spec.Delay); err == nil {
			policy.RestartDelay = d
		}
	}
	if spec.ResetAfter != "" {
		if d, err := time.ParseDuration(spec.ResetAfter); err == nil {
			policy.ResetCounterAfter = d
		}
	}
	return policy
}

// restartDefaultDelay is the minimum gap between two restarts when the
// manifest names none: long enough that a service failing on start does not
// spin.
const restartDefaultDelay = 30 * time.Second
