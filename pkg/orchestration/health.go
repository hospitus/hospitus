// Package orchestration provides multi-instance orchestration capabilities
// including health checks, automatic restart, service discovery, and stack management.
package orchestration

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
)

// Defaults and tunables for health checking and auto-restart.
const (
	// defaultHealthCheckInterval is used when a config omits Interval.
	defaultHealthCheckInterval = 30 * time.Second
	// defaultHealthCheckTimeout is used when a config omits Timeout.
	defaultHealthCheckTimeout = 10 * time.Second
	// restartStopStartDelay is the pause between stopping and starting an
	// instance during an auto-restart.
	restartStopStartDelay = 2 * time.Second
)

// HealthStatus represents the health state of an instance
type HealthStatus string

const (
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusUnhealthy HealthStatus = "unhealthy"
	HealthStatusStarting  HealthStatus = "starting"
	HealthStatusUnknown   HealthStatus = "unknown"
)

// HealthCheckConfig defines how to check instance health
type HealthCheckConfig struct {
	// Command to execute inside the instance
	Command []string

	// Interval between checks
	Interval time.Duration

	// Timeout for each check
	Timeout time.Duration

	// Retries before marking unhealthy
	Retries int

	// StartPeriod is grace period before checks begin
	StartPeriod time.Duration
}

// HealthCheckResult represents the result of a health check
type HealthCheckResult struct {
	InstanceID string
	Status     HealthStatus
	Message    string
	CheckedAt  time.Time
	Duration   time.Duration
	ExitCode   int
}

// HealthChecker manages health checks for instances
type HealthChecker struct {
	provider   provider.ExecProvider
	mu         sync.RWMutex
	configs    map[string]*HealthCheckConfig // instanceID -> config
	results    map[string]*HealthCheckResult // instanceID -> latest result
	failCounts map[string]int                // instanceID -> consecutive failures
	stopChans  map[string]chan struct{}      // instanceID -> stop channel
	callbacks  []HealthCallback
	logger     *slog.Logger

	// Callback delivery, kept off the check loop and in order. See notify.
	notifyMu    sync.Mutex
	notifyQueue []func()
	notifyBusy  bool
}

// HealthCallback is called when health status changes
type HealthCallback func(instanceID string, oldStatus, newStatus HealthStatus)

// NewHealthChecker creates a new health checker
func NewHealthChecker(execProvider provider.ExecProvider) *HealthChecker {
	return &HealthChecker{
		provider:   execProvider,
		configs:    make(map[string]*HealthCheckConfig),
		results:    make(map[string]*HealthCheckResult),
		failCounts: make(map[string]int),
		stopChans:  make(map[string]chan struct{}),
		logger:     logging.WithComponent("health-checker"),
	}
}

// OnHealthChange registers a callback for health status changes
func (hc *HealthChecker) OnHealthChange(callback HealthCallback) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.callbacks = append(hc.callbacks, callback)
}

// RegisterInstance registers an instance for health checking
func (hc *HealthChecker) RegisterInstance(instanceID string, config *HealthCheckConfig) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	// Stop existing checker if any. Delete the entry too: leaving the closed
	// channel in the map would cause a "close of closed channel" panic on the
	// next RegisterInstance for the same instance.
	if stop, ok := hc.stopChans[instanceID]; ok {
		close(stop)
		delete(hc.stopChans, instanceID)
	}

	hc.configs[instanceID] = config
	hc.results[instanceID] = &HealthCheckResult{
		InstanceID: instanceID,
		Status:     HealthStatusStarting,
		CheckedAt:  time.Now(),
	}
	hc.failCounts[instanceID] = 0
}

// UnregisterInstance stops health checking for an instance
func (hc *HealthChecker) UnregisterInstance(instanceID string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	if stop, ok := hc.stopChans[instanceID]; ok {
		close(stop)
		delete(hc.stopChans, instanceID)
	}
	delete(hc.configs, instanceID)
	delete(hc.results, instanceID)
	delete(hc.failCounts, instanceID)
}

// StartChecking starts the health check loop for an instance
func (hc *HealthChecker) StartChecking(ctx context.Context, handle provider.InstanceHandle) {
	instanceID := handle.ID

	hc.mu.Lock()
	config, ok := hc.configs[instanceID]
	if !ok {
		hc.mu.Unlock()
		return
	}

	// Stop any previous loop before replacing its stop channel, otherwise the
	// old checkLoop goroutine would leak (never signaled to exit).
	if old, ok := hc.stopChans[instanceID]; ok {
		close(old)
	}

	stop := make(chan struct{})
	hc.stopChans[instanceID] = stop
	hc.mu.Unlock()

	go hc.checkLoop(ctx, handle, config, stop)
}

// StopChecking stops the health check loop for an instance
func (hc *HealthChecker) StopChecking(instanceID string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	if stop, ok := hc.stopChans[instanceID]; ok {
		close(stop)
		delete(hc.stopChans, instanceID)
	}
}

// GetHealth returns the current health status of an instance
func (hc *HealthChecker) GetHealth(instanceID string) *HealthCheckResult {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	// A copy: handing out the stored pointer let a caller read a result the
	// check loop was writing, and write one nothing else would ever see.
	return cloneHealthResult(hc.results[instanceID])
}

// HasHealthCheck reports whether an instance has a health check registered.
//
// A dependency declared "healthy" that never registered one can never satisfy
// that condition: GetHealth answers nil forever, and the deployment waited out
// its whole timeout before rolling back.
func (hc *HealthChecker) HasHealthCheck(instanceID string) bool {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	_, ok := hc.configs[instanceID]
	return ok
}

// GetAllHealth returns health status of all registered instances
func (hc *HealthChecker) GetAllHealth() map[string]*HealthCheckResult {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	results := make(map[string]*HealthCheckResult, len(hc.results))
	for k, v := range hc.results {
		results[k] = cloneHealthResult(v)
	}
	return results
}

// checkLoop runs the health check loop for an instance
func (hc *HealthChecker) checkLoop(ctx context.Context, handle provider.InstanceHandle, config *HealthCheckConfig, stop chan struct{}) {
	instanceID := handle.ID

	// Wait for start period
	if config.StartPeriod > 0 {
		select {
		case <-time.After(config.StartPeriod):
		case <-stop:
			return
		case <-ctx.Done():
			return
		}
	}

	// Mark as starting check phase complete
	hc.updateStatus(instanceID, HealthStatusUnknown, "Initial check pending")

	// time.NewTicker panics on a non-positive interval; fall back to a sane
	// default when the config omits it.
	interval := config.Interval
	if interval <= 0 {
		interval = defaultHealthCheckInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			hc.runCheck(ctx, handle, config)
		case <-stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

// runCheck executes a single health check
func (hc *HealthChecker) runCheck(ctx context.Context, handle provider.InstanceHandle, config *HealthCheckConfig) {
	instanceID := handle.ID

	// A missing command cannot be checked; record it instead of panicking on
	// config.Command[0].
	if len(config.Command) == 0 {
		hc.updateStatus(instanceID, HealthStatusUnknown, "No health check command configured")
		return
	}

	// A non-positive timeout would make context.WithTimeout expire immediately
	// and fail every check; fall back to a sane default.
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultHealthCheckTimeout
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()

	// Execute health check command
	opts := provider.ExecOptions{
		Command: config.Command[0],
	}
	if len(config.Command) > 1 {
		opts.Args = config.Command[1:]
	}
	result, err := hc.provider.ExecCommand(checkCtx, handle, opts)
	duration := time.Since(start)

	hc.mu.Lock()
	defer hc.mu.Unlock()

	oldResult := hc.results[instanceID]
	var oldStatus HealthStatus
	if oldResult != nil {
		oldStatus = oldResult.Status
	}

	newResult := &HealthCheckResult{
		InstanceID: instanceID,
		CheckedAt:  time.Now(),
		Duration:   duration,
	}

	exitCode := 0
	if result != nil {
		exitCode = result.ExitCode
	}

	if err != nil || exitCode != 0 {
		hc.failCounts[instanceID]++
		newResult.ExitCode = exitCode
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		newResult.Message = fmt.Sprintf("Check failed: %s (exit code: %d)", errMsg, exitCode)

		// Defaulted here too, not only in healthSpecToConfig: a config built
		// directly carries Retries == 0, and the first transient failure then
		// satisfied "failCount >= 0" and marked the instance unhealthy. A
		// configured value is used as it stands, including 1.
		retries := config.Retries
		if retries <= 0 {
			retries = defaultHealthRetries
		}
		if hc.failCounts[instanceID] >= retries {
			newResult.Status = HealthStatusUnhealthy
		} else {
			newResult.Status = oldStatus // Keep previous status until retry threshold
			if newResult.Status == "" {
				newResult.Status = HealthStatusUnknown
			}
		}
	} else {
		hc.failCounts[instanceID] = 0
		newResult.Status = HealthStatusHealthy
		newResult.ExitCode = 0
		newResult.Message = "Health check passed"
	}

	hc.results[instanceID] = newResult

	hc.notify(instanceID, oldStatus, newResult.Status)
}

// updateStatus updates the health status of an instance
func (hc *HealthChecker) updateStatus(instanceID string, status HealthStatus, message string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	oldResult := hc.results[instanceID]
	var oldStatus HealthStatus
	if oldResult != nil {
		oldStatus = oldResult.Status
	}

	hc.results[instanceID] = &HealthCheckResult{
		InstanceID: instanceID,
		Status:     status,
		Message:    message,
		CheckedAt:  time.Now(),
	}

	hc.notify(instanceID, oldStatus, status)
}

// AutoRestarter handles automatic restart of unhealthy instances
type AutoRestarter struct {
	// provider is the fallback for a policy registered without one, which is
	// how the single-provider constructor is still used.
	provider      provider.Provider
	healthChecker *HealthChecker
	mu            sync.Mutex
	restartPolicy map[string]*RestartPolicy // instanceID -> policy
	targets       map[string]restartTarget  // instanceID -> what to restart, and through whom
	restartCounts map[string]int            // instanceID -> restart count
	healthySince  map[string]time.Time      // instanceID -> when it last became healthy
	lastRestart   map[string]time.Time      // instanceID -> last restart time
	logger        *slog.Logger
}

// restartTarget is what a restart acts on.
//
// Both halves matter. A stack draws its instances from several providers, so
// restarting one through a single shared provider reached the wrong daemon
// entirely; and InstanceHandle.Metadata carries the parameter overrides jail
// lifecycle methods read, which a handle rebuilt from the id alone does not
// have.
type restartTarget struct {
	provider provider.Provider
	handle   provider.InstanceHandle
}

// RestartPolicy defines the automatic restart behavior
type RestartPolicy struct {
	// Enabled controls whether auto-restart is active
	Enabled bool

	// MaxRestarts is the maximum number of restarts (0 = unlimited)
	MaxRestarts int

	// RestartDelay is the minimum time between restarts
	RestartDelay time.Duration

	// ResetCounterAfter resets the restart counter after this duration of
	// healthy operation. Zero disables the reset entirely: the counter then
	// only ever grows, and MaxRestarts is a lifetime limit rather than a
	// per-window one.
	ResetCounterAfter time.Duration
}

// NewAutoRestarter creates a new auto restarter
func NewAutoRestarter(prov provider.Provider, hc *HealthChecker) *AutoRestarter {
	ar := &AutoRestarter{
		provider:      prov,
		healthChecker: hc,
		restartPolicy: make(map[string]*RestartPolicy),
		targets:       make(map[string]restartTarget),
		restartCounts: make(map[string]int),
		healthySince:  make(map[string]time.Time),
		lastRestart:   make(map[string]time.Time),
		logger:        logging.WithComponent("auto-restarter"),
	}

	// Register callback for health changes
	hc.OnHealthChange(ar.onHealthChange)

	return ar
}

// SetPolicy sets the restart policy for an instance
func (ar *AutoRestarter) SetPolicy(instanceID string, policy *RestartPolicy) {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	ar.restartPolicy[instanceID] = policy
}

// SetPolicyFor sets the restart policy for an instance along with the provider
// that owns it and the handle CreateInstance returned.
//
// This is what a stack uses: SetPolicy alone leaves the restarter guessing,
// and its guesses were the shared provider and a handle built from the id.
func (ar *AutoRestarter) SetPolicyFor(prov provider.Provider, handle provider.InstanceHandle, policy *RestartPolicy) {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	ar.restartPolicy[handle.ID] = policy
	ar.targets[handle.ID] = restartTarget{provider: prov, handle: handle}
}

// RemovePolicy removes the restart policy for an instance
func (ar *AutoRestarter) RemovePolicy(instanceID string) {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	delete(ar.restartPolicy, instanceID)
	delete(ar.targets, instanceID)
	delete(ar.restartCounts, instanceID)
	delete(ar.healthySince, instanceID)
	delete(ar.lastRestart, instanceID)
}

// GetRestartCount returns the current restart count for an instance
func (ar *AutoRestarter) GetRestartCount(instanceID string) int {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	return ar.restartCounts[instanceID]
}

// onHealthChange is called when an instance's health status changes
func (ar *AutoRestarter) onHealthChange(instanceID string, oldStatus, newStatus HealthStatus) {
	ar.mu.Lock()
	defer ar.mu.Unlock()

	policy, ok := ar.restartPolicy[instanceID]
	if !ok || !policy.Enabled {
		return
	}

	if newStatus == HealthStatusHealthy {
		// When it became healthy, so the window below can be measured against
		// how long it stayed that way.
		if _, running := ar.healthySince[instanceID]; !running {
			ar.healthySince[instanceID] = time.Now()
		}
		return
	}

	// Anything else ends the healthy stretch.
	healthyFor := time.Duration(0)
	if since, running := ar.healthySince[instanceID]; running {
		healthyFor = time.Since(since)
		delete(ar.healthySince, instanceID)
	}

	// Only restart on transition to unhealthy
	if newStatus != HealthStatusUnhealthy {
		return
	}

	// An instance that ran healthy for the whole window starts its budget
	// again. Measured on that stretch, not on the time since the last restart:
	// ResetCounterAfter is documented as a duration of healthy operation, and
	// an instance that flapped throughout an hour used to have its counter
	// cleared simply because the last restart was an hour ago.
	if policy.ResetCounterAfter > 0 && healthyFor > policy.ResetCounterAfter {
		ar.restartCounts[instanceID] = 0
	}

	// Check restart limits
	if policy.MaxRestarts > 0 && ar.restartCounts[instanceID] >= policy.MaxRestarts {
		ar.logger.Warn("Instance exceeded max restarts", logging.FieldAction, "restart", logging.FieldInstance, instanceID, "max_restarts", policy.MaxRestarts)
		return
	}

	// Check restart delay
	if lastRestart, ok := ar.lastRestart[instanceID]; ok {
		if time.Since(lastRestart) < policy.RestartDelay {
			ar.logger.Debug("Instance restart delayed (too soon)", logging.FieldAction, "restart", logging.FieldInstance, instanceID)
			return
		}
	}

	// Perform restart
	go ar.restartInstance(instanceID)
}

// restartInstance restarts an unhealthy instance
func (ar *AutoRestarter) restartInstance(instanceID string) {
	ar.mu.Lock()
	ar.restartCounts[instanceID]++
	ar.lastRestart[instanceID] = time.Now()
	ar.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The registered target, when there is one: the handle it carries holds
	// the metadata a rebuilt one loses, and its provider is the one that owns
	// the instance rather than whichever was passed to the constructor.
	ar.mu.Lock()
	target, registered := ar.targets[instanceID]
	ar.mu.Unlock()

	prov := ar.provider
	handle := provider.InstanceHandle{ID: instanceID}
	if registered {
		prov, handle = target.provider, target.handle
	}
	if prov == nil {
		ar.logger.Error("No provider to restart through", logging.FieldInstance, instanceID)
		return
	}

	ar.logger.Info("Auto-restarting unhealthy instance", logging.FieldAction, "restart", logging.FieldInstance, instanceID)

	if err := prov.StopInstance(ctx, handle, provider.StopOptions{Force: false}); err != nil {
		ar.logger.Error("Failed to stop instance for restart", logging.FieldAction, "stop", logging.FieldInstance, instanceID, logging.FieldError, err)
		return
	}

	// Brief wait between stop and start, cancelable via ctx.
	select {
	case <-time.After(restartStopStartDelay):
	case <-ctx.Done():
		return
	}

	if err := prov.StartInstance(ctx, handle); err != nil {
		ar.logger.Error("Failed to start instance after restart", logging.FieldAction, "start", logging.FieldInstance, instanceID, logging.FieldError, err)
		return
	}

	ar.logger.Info("Successfully restarted instance", logging.FieldAction, "restart", logging.FieldInstance, instanceID)
}

// maxPendingNotifications bounds the transition queue. One callback that never
// returns must not grow it without limit.
const maxPendingNotifications = 1024

// defaultHealthRetries is the failure count a check must reach before an
// instance is called unhealthy, when the configuration names none.
const defaultHealthRetries = 3

// cloneHealthResult copies a result so callers cannot reach the one the check
// loop is still writing to.
func cloneHealthResult(r *HealthCheckResult) *HealthCheckResult {
	if r == nil {
		return nil
	}
	c := *r
	return &c
}

// notify delivers a status transition to every registered callback.
//
// The caller must hold hc.mu: this reads hc.callbacks without taking it, which
// both call sites happen to satisfy and nothing states. The copy it makes is
// what lets the delivery below run outside that lock.
//
// Queued and drained in order rather than dispatched with "go cb(...)" per
// callback: that let two transitions for the same instance race, and a
// listener could see unhealthy after the healthy that followed it — then
// restart an instance that had already recovered.
func (hc *HealthChecker) notify(instanceID string, from, to HealthStatus) {
	if from == to || len(hc.callbacks) == 0 {
		return
	}
	callbacks := append([]HealthCallback(nil), hc.callbacks...)

	hc.notifyMu.Lock()
	// Bounded: a callback that blocks forever would otherwise let the queue
	// grow with every check of every instance. The oldest transition goes
	// first, and the drop is logged — losing the newest would leave listeners
	// believing a status the instance no longer has.
	if len(hc.notifyQueue) >= maxPendingNotifications {
		hc.notifyQueue = hc.notifyQueue[1:]
		hc.logger.Warn("health transitions are backing up; a callback is not returning",
			"instance", instanceID, "pending", len(hc.notifyQueue))
	}
	hc.notifyQueue = append(hc.notifyQueue, func() {
		for _, cb := range callbacks {
			cb(instanceID, from, to)
		}
	})
	start := !hc.notifyBusy
	hc.notifyBusy = true
	hc.notifyMu.Unlock()

	// One drainer at a time, so queued transitions run in the order they were
	// produced. It exits when the queue empties, which is why there is nothing
	// to shut down.
	if start {
		go hc.drainNotifications()
	}
}

func (hc *HealthChecker) drainNotifications() {
	for {
		hc.notifyMu.Lock()
		if len(hc.notifyQueue) == 0 {
			hc.notifyBusy = false
			hc.notifyMu.Unlock()
			return
		}
		deliver := hc.notifyQueue[0]
		hc.notifyQueue = hc.notifyQueue[1:]
		hc.notifyMu.Unlock()

		// Outside every lock: a callback that calls back into the checker —
		// StopChecking on an unhealthy instance is the obvious one — would
		// otherwise deadlock against the caller still holding hc.mu.
		deliver()
	}
}
