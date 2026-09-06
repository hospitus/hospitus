package orchestration

import (
	"testing"
	"time"

	"github.com/hospitus/hospitus/pkg/manifest"
)

func TestHealthSpecToConfig(t *testing.T) {
	t.Run("returns nil when command is empty", func(t *testing.T) {
		if cfg := healthSpecToConfig(&manifest.HealthCheckSpec{}); cfg != nil {
			t.Fatalf("expected nil config for spec without command, got %+v", cfg)
		}
		if cfg := healthSpecToConfig(nil); cfg != nil {
			t.Fatalf("expected nil config for nil spec, got %+v", cfg)
		}
	})

	t.Run("applies defaults for missing fields", func(t *testing.T) {
		cfg := healthSpecToConfig(&manifest.HealthCheckSpec{Command: []string{"true"}})
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if cfg.Interval != 30*time.Second {
			t.Errorf("Interval = %v, want 30s", cfg.Interval)
		}
		if cfg.Timeout != 10*time.Second {
			t.Errorf("Timeout = %v, want 10s", cfg.Timeout)
		}
		if cfg.Retries != 3 {
			t.Errorf("Retries = %d, want 3", cfg.Retries)
		}
		if cfg.StartPeriod != 0 {
			t.Errorf("StartPeriod = %v, want 0", cfg.StartPeriod)
		}
	})

	t.Run("parses explicit values", func(t *testing.T) {
		cfg := healthSpecToConfig(&manifest.HealthCheckSpec{
			Command:     []string{"/bin/sh", "-c", "exit 0"},
			Interval:    "5s",
			Timeout:     "2s",
			Retries:     5,
			StartPeriod: "1m",
		})
		if cfg.Interval != 5*time.Second {
			t.Errorf("Interval = %v, want 5s", cfg.Interval)
		}
		if cfg.Timeout != 2*time.Second {
			t.Errorf("Timeout = %v, want 2s", cfg.Timeout)
		}
		if cfg.Retries != 5 {
			t.Errorf("Retries = %d, want 5", cfg.Retries)
		}
		if cfg.StartPeriod != time.Minute {
			t.Errorf("StartPeriod = %v, want 1m", cfg.StartPeriod)
		}
	})

	t.Run("falls back to default on invalid duration", func(t *testing.T) {
		cfg := healthSpecToConfig(&manifest.HealthCheckSpec{
			Command:  []string{"true"},
			Interval: "not-a-duration",
			Timeout:  "-3s",
		})
		if cfg.Interval != 30*time.Second {
			t.Errorf("Interval = %v, want default 30s", cfg.Interval)
		}
		if cfg.Timeout != 10*time.Second {
			t.Errorf("Timeout = %v, want default 10s", cfg.Timeout)
		}
	})
}

func TestHealthStatusConstants(t *testing.T) {
	// Verify health status values
	if HealthStatusHealthy != "healthy" {
		t.Error("HealthStatusHealthy should be 'healthy'")
	}
	if HealthStatusUnhealthy != "unhealthy" {
		t.Error("HealthStatusUnhealthy should be 'unhealthy'")
	}
	if HealthStatusStarting != "starting" {
		t.Error("HealthStatusStarting should be 'starting'")
	}
	if HealthStatusUnknown != "unknown" {
		t.Error("HealthStatusUnknown should be 'unknown'")
	}
}

func TestHealthCheckConfig(t *testing.T) {
	config := &HealthCheckConfig{
		Command:     []string{"/bin/sh", "-c", "echo ok"},
		Interval:    30 * time.Second,
		Timeout:     10 * time.Second,
		Retries:     3,
		StartPeriod: 60 * time.Second,
	}

	if len(config.Command) != 3 {
		t.Errorf("Expected 3 command parts, got %d", len(config.Command))
	}
	if config.Interval != 30*time.Second {
		t.Error("Interval mismatch")
	}
	if config.Retries != 3 {
		t.Error("Retries mismatch")
	}
}

func TestHealthCheckResult(t *testing.T) {
	result := &HealthCheckResult{
		InstanceID: "test-instance",
		Status:     HealthStatusHealthy,
		Message:    "Health check passed",
		CheckedAt:  time.Now(),
		Duration:   100 * time.Millisecond,
		ExitCode:   0,
	}

	if result.InstanceID != "test-instance" {
		t.Error("InstanceID mismatch")
	}
	if result.Status != HealthStatusHealthy {
		t.Error("Status should be healthy")
	}
	if result.ExitCode != 0 {
		t.Error("ExitCode should be 0")
	}
}

func TestRestartPolicy(t *testing.T) {
	policy := &RestartPolicy{
		Enabled:           true,
		MaxRestarts:       5,
		RestartDelay:      10 * time.Second,
		ResetCounterAfter: 5 * time.Minute,
	}

	if !policy.Enabled {
		t.Error("Policy should be enabled")
	}
	if policy.MaxRestarts != 5 {
		t.Error("MaxRestarts mismatch")
	}
	if policy.RestartDelay != 10*time.Second {
		t.Error("RestartDelay mismatch")
	}
}

// ── NewHealthChecker ──────────────────────────────────────────────────────────

func TestNewHealthChecker(t *testing.T) {
	hc := NewHealthChecker(nil)
	if hc == nil {
		t.Fatal("expected non-nil HealthChecker")
	}
	if hc.configs == nil {
		t.Error("configs map should be initialized")
	}
	if hc.results == nil {
		t.Error("results map should be initialized")
	}
}

func TestHealthChecker_RegisterUnregister(t *testing.T) {
	hc := NewHealthChecker(nil)
	cfg := &HealthCheckConfig{
		Command:  []string{"echo", "ok"},
		Interval: time.Second,
	}
	hc.RegisterInstance("inst1", cfg)

	if r := hc.GetHealth("inst1"); r == nil {
		t.Error("expected health result after register")
	} else if r.Status != HealthStatusStarting {
		t.Errorf("expected starting status, got %s", r.Status)
	}

	hc.UnregisterInstance("inst1")
	if r := hc.GetHealth("inst1"); r != nil {
		t.Error("expected nil after unregister")
	}
}

func TestHealthChecker_GetAllHealth(t *testing.T) {
	hc := NewHealthChecker(nil)
	cfg := &HealthCheckConfig{Command: []string{"true"}, Interval: time.Second}
	hc.RegisterInstance("a", cfg)
	hc.RegisterInstance("b", cfg)

	all := hc.GetAllHealth()
	if len(all) != 2 {
		t.Errorf("expected 2 entries, got %d", len(all))
	}
}

func TestHealthChecker_OnHealthChange(t *testing.T) {
	hc := NewHealthChecker(nil)

	seen := make(chan [3]string, 4)
	hc.OnHealthChange(func(id string, old, updated HealthStatus) {
		seen <- [3]string{id, string(old), string(updated)}
	})

	// Driven through updateStatus rather than left unasserted: the callback
	// was registered and never called, so the test proved only that
	// registering one does not panic.
	hc.updateStatus("web", HealthStatusHealthy, "up")
	hc.updateStatus("web", HealthStatusUnhealthy, "down")

	want := [][3]string{
		// An instance with no result yet has an empty previous status, not
		// HealthStatusUnknown.
		{"web", "", string(HealthStatusHealthy)},
		{"web", string(HealthStatusHealthy), string(HealthStatusUnhealthy)},
	}
	for i, w := range want {
		select {
		case got := <-seen:
			// In order: the transitions are queued and drained one at a time,
			// so a listener never sees them swapped.
			if got != w {
				t.Errorf("transition %d = %v, want %v", i, got, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("transition %d never reached the callback", i)
		}
	}
}

// ── NewAutoRestarter ──────────────────────────────────────────────────────────

func TestNewAutoRestarter(t *testing.T) {
	hc := NewHealthChecker(nil)
	ar := NewAutoRestarter(nil, hc)
	if ar == nil {
		t.Fatal("expected non-nil AutoRestarter")
	}

	// Set and remove a policy
	policy := &RestartPolicy{Enabled: true, MaxRestarts: 3}
	ar.SetPolicy("inst1", policy)
	if c := ar.GetRestartCount("inst1"); c != 0 {
		t.Errorf("expected 0 restart count, got %d", c)
	}
	ar.RemovePolicy("inst1")
	// Should not panic
	ar.RemovePolicy("nonexistent")
}
