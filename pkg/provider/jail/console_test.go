package jail

import (
	"context"
	"os"
	"runtime"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestJailProviderImplementsConsoleProvider(t *testing.T) {
	// Verify JailProvider implements ConsoleProvider interface
	var _ provider.ConsoleProvider = (*JailProvider)(nil)
}

func TestJailProviderImplementsExecProvider(t *testing.T) {
	// Verify JailProvider implements ExecProvider interface
	var _ provider.ExecProvider = (*JailProvider)(nil)
}

func TestExecCommand(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skip("Jail exec only works on FreeBSD")
	}

	// This test requires a running jail, which is hard to set up in unit tests
	// Skip by default, run manually with a test jail
	t.Skip("Requires running jail - run manually with test jail")
}

func TestExecOptions(t *testing.T) {
	tests := []struct {
		name    string
		opts    provider.ExecOptions
		wantErr bool
	}{
		{
			name: "basic command",
			opts: provider.ExecOptions{
				Command: "/bin/echo",
				Args:    []string{"hello"},
			},
			wantErr: false,
		},
		{
			name: "command with user",
			opts: provider.ExecOptions{
				Command: "/usr/bin/whoami",
				User:    "nobody",
			},
			wantErr: false,
		},
		{
			name: "command with timeout",
			opts: provider.ExecOptions{
				Command: "/bin/sleep",
				Args:    []string{"1"},
				Timeout: 5,
			},
			wantErr: false,
		},
		{
			name: "command with env",
			opts: provider.ExecOptions{
				Command: "/bin/sh",
				Args:    []string{"-c", "echo $MY_VAR"},
				Env:     map[string]string{"MY_VAR": "test"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just validate that ExecOptions struct is properly formed
			if tt.opts.Command == "" {
				t.Error("Command should not be empty")
			}
		})
	}
}

func TestExecResult(t *testing.T) {
	result := &provider.ExecResult{
		ExitCode: 0,
		Stdout:   "hello world\n",
		Stderr:   "",
	}

	if result.ExitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", result.ExitCode)
	}

	if result.Stdout != "hello world\n" {
		t.Errorf("Unexpected stdout: %s", result.Stdout)
	}

	if result.Stderr != "" {
		t.Errorf("Expected empty stderr, got: %s", result.Stderr)
	}
}

func TestGetConsoleNotRunning(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skip("Jail provider only works on FreeBSD")
	}

	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges: Initialize creates the ZFS parent dataset")
	}

	isolatedZFSParent(t)

	p := NewJailProvider()
	ctx := context.Background()

	// Initialize provider
	config := provider.ProviderConfig{
		DataDir:  "/tmp/hospitus-test/data",
		StateDir: "/tmp/hospitus-test/state",
	}
	if err := p.Initialize(ctx, config); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Try to get console for non-existent jail
	handle := provider.InstanceHandle{
		ID:       "nonexistent-jail",
		Provider: "jail",
	}

	_, err := p.GetConsole(ctx, handle)
	if err == nil {
		t.Error("Expected error for non-running jail, got nil")
	}
}

func TestExecCommandNotRunning(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skip("Jail provider only works on FreeBSD")
	}

	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges: Initialize creates the ZFS parent dataset")
	}

	isolatedZFSParent(t)

	p := NewJailProvider()
	ctx := context.Background()

	// Initialize provider
	config := provider.ProviderConfig{
		DataDir:  "/tmp/hospitus-test/data",
		StateDir: "/tmp/hospitus-test/state",
	}
	if err := p.Initialize(ctx, config); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Try to exec in non-existent jail
	handle := provider.InstanceHandle{
		ID:       "nonexistent-jail",
		Provider: "jail",
	}

	opts := provider.ExecOptions{
		Command: "/bin/echo",
		Args:    []string{"test"},
	}

	_, err := p.ExecCommand(ctx, handle, opts)
	if err == nil {
		t.Error("Expected error for non-running jail, got nil")
	}
}

func TestConsoleInfo(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skip("Jail provider only works on FreeBSD")
	}

	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges: Initialize creates the ZFS parent dataset")
	}

	isolatedZFSParent(t)

	p := NewJailProvider()
	ctx := context.Background()

	// Initialize provider
	config := provider.ProviderConfig{
		DataDir:  "/tmp/hospitus-test/data",
		StateDir: "/tmp/hospitus-test/state",
	}
	if err := p.Initialize(ctx, config); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Get console info for non-running jail
	handle := provider.InstanceHandle{
		ID:       "test-jail",
		Provider: "jail",
	}

	info, err := p.GetConsoleInfo(ctx, handle)
	if err != nil {
		t.Fatalf("GetConsoleInfo failed: %v", err)
	}

	// For non-running jail, Available should be false
	if info.Available {
		t.Error("Expected Available to be false for non-running jail")
	}

	if info.JailName != "test-jail" {
		t.Errorf("Expected JailName 'test-jail', got '%s'", info.JailName)
	}

	if info.Command == "" {
		t.Error("Expected Command to be non-empty")
	}
}

func TestJailProviderImplementsHealthCheckProvider(t *testing.T) {
	// Verified in jail.go via compile-time assertion
}

func TestCheckInstanceHealthNotRunning(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skip("Jail provider only works on FreeBSD")
	}

	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges: Initialize creates the ZFS parent dataset")
	}

	isolatedZFSParent(t)

	p := NewJailProvider()
	ctx := context.Background()

	// Initialize provider
	config := provider.ProviderConfig{
		DataDir:  "/tmp/hospitus-test/data",
		StateDir: "/tmp/hospitus-test/state",
	}
	if err := p.Initialize(ctx, config); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Check health for non-existent jail
	handle := provider.InstanceHandle{
		ID:       "nonexistent-jail",
		Provider: "jail",
	}

	health, err := p.CheckInstanceHealth(ctx, handle)
	if err != nil {
		t.Fatalf("CheckInstanceHealth failed: %v", err)
	}

	// For non-running jail, status should be unhealthy
	if health.Status != provider.HealthStatusUnhealthy {
		t.Errorf("Expected unhealthy status, got %s", health.Status)
	}

	if health.Message != "jail is not running" {
		t.Errorf("Expected 'jail is not running' message, got %s", health.Message)
	}
}

func TestGetInstanceMetricsNotRunning(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skip("Jail provider only works on FreeBSD")
	}

	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges: Initialize creates the ZFS parent dataset")
	}

	isolatedZFSParent(t)

	p := NewJailProvider()
	ctx := context.Background()

	// Initialize provider
	config := provider.ProviderConfig{
		DataDir:  "/tmp/hospitus-test/data",
		StateDir: "/tmp/hospitus-test/state",
	}
	if err := p.Initialize(ctx, config); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Get metrics for non-running jail
	handle := provider.InstanceHandle{
		ID:       "nonexistent-jail",
		Provider: "jail",
	}

	metrics, err := p.GetInstanceMetrics(ctx, handle)
	if err != nil {
		t.Fatalf("GetInstanceMetrics failed: %v", err)
	}

	// For non-running jail, metrics should be empty/zero
	if metrics.CPUUsagePercent != 0 {
		t.Errorf("Expected 0 CPU usage for non-running jail, got %f", metrics.CPUUsagePercent)
	}

	if metrics.MemoryUsedMB != 0 {
		t.Errorf("Expected 0 memory for non-running jail, got %d", metrics.MemoryUsedMB)
	}
}

func TestHealthCheckStatuses(t *testing.T) {
	// Test HealthStatus constants
	tests := []struct {
		status provider.HealthStatus
		want   string
	}{
		{provider.HealthStatusHealthy, "healthy"},
		{provider.HealthStatusUnhealthy, "unhealthy"},
		{provider.HealthStatusDegraded, "degraded"},
		{provider.HealthStatusUnknown, "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if string(tt.status) != tt.want {
				t.Errorf("HealthStatus %v != %s", tt.status, tt.want)
			}
		})
	}
}

func TestInstanceHealthStructure(t *testing.T) {
	health := &provider.InstanceHealth{
		Status:  provider.HealthStatusHealthy,
		Message: "all checks passed",
		Checks: []provider.HealthCheck{
			{Name: "jail_running", Status: provider.HealthStatusHealthy, Message: "ok"},
			{Name: "zfs_dataset", Status: provider.HealthStatusHealthy, Message: "ok"},
		},
	}

	if health.Status != provider.HealthStatusHealthy {
		t.Errorf("Expected healthy status, got %s", health.Status)
	}

	if len(health.Checks) != 2 {
		t.Errorf("Expected 2 checks, got %d", len(health.Checks))
	}

	if health.Checks[0].Name != "jail_running" {
		t.Errorf("Expected first check to be jail_running, got %s", health.Checks[0].Name)
	}
}
