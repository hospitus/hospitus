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
	return hc.results[instanceID]
}

// GetAllHealth returns health status of all registered instances
func (hc *HealthChecker) GetAllHealth() map[string]*HealthCheckResult {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	results := make(map[string]*HealthCheckResult, len(hc.results))
	for k, v := range hc.results {
		results[k] = v
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

		if hc.failCounts[instanceID] >= config.Retries {
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

	// Notify callbacks if status changed
	if oldStatus != newResult.Status && len(hc.callbacks) > 0 {
		for _, cb := range hc.callbacks {
			go cb(instanceID, oldStatus, newResult.Status)
		}
	}
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

	// Notify callbacks if status changed
	if oldStatus != status && len(hc.callbacks) > 0 {
		for _, cb := range hc.callbacks {
			go cb(instanceID, oldStatus, status)
		}
	}
}

// AutoRestarter handles automatic restart of unhealthy instances
type AutoRestarter struct {
	provider      provider.Provider
	healthChecker *HealthChecker
	mu            sync.Mutex
	restartPolicy map[string]*RestartPolicy // instanceID -> policy
	restartCounts map[string]int            // instanceID -> restart count
	lastRestart   map[string]time.Time      // instanceID -> last restart time
	logger        *slog.Logger
}

// RestartPolicy defines the automatic restart behavior
type RestartPolicy struct {
	// Enabled controls whether auto-restart is active
	Enabled bool

	// MaxRestarts is the maximum number of restarts (0 = unlimited)
	MaxRestarts int

	// RestartDelay is the minimum time between restarts
	RestartDelay time.Duration

	// ResetCounterAfter resets the restart counter after this duration of healthy operation
	ResetCounterAfter time.Duration
}

// NewAutoRestarter creates a new auto restarter
func NewAutoRestarter(prov provider.Provider, hc *HealthChecker) *AutoRestarter {
	ar := &AutoRestarter{
		provider:      prov,
		healthChecker: hc,
		restartPolicy: make(map[string]*RestartPolicy),
		restartCounts: make(map[string]int),
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

// RemovePolicy removes the restart policy for an instance
func (ar *AutoRestarter) RemovePolicy(instanceID string) {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	delete(ar.restartPolicy, instanceID)
	delete(ar.restartCounts, instanceID)
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

	// Reset counter if instance has been healthy for long enough
	if newStatus == HealthStatusHealthy {
		if lastRestart, ok := ar.lastRestart[instanceID]; ok {
			if time.Since(lastRestart) > policy.ResetCounterAfter {
				ar.restartCounts[instanceID] = 0
			}
		}
		return
	}

	// Only restart on transition to unhealthy
	if newStatus != HealthStatusUnhealthy {
		return
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

	handle := provider.InstanceHandle{ID: instanceID}

	ar.logger.Info("Auto-restarting unhealthy instance", logging.FieldAction, "restart", logging.FieldInstance, instanceID)

	if err := ar.provider.StopInstance(ctx, handle, provider.StopOptions{Force: false}); err != nil {
		ar.logger.Error("Failed to stop instance for restart", logging.FieldAction, "stop", logging.FieldInstance, instanceID, logging.FieldError, err)
		return
	}

	// Brief wait between stop and start, cancelable via ctx.
	select {
	case <-time.After(restartStopStartDelay):
	case <-ctx.Done():
		return
	}

	if err := ar.provider.StartInstance(ctx, handle); err != nil {
		ar.logger.Error("Failed to start instance after restart", logging.FieldAction, "start", logging.FieldInstance, instanceID, logging.FieldError, err)
		return
	}

	ar.logger.Info("Successfully restarted instance", logging.FieldAction, "restart", logging.FieldInstance, instanceID)
}
