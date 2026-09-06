package jail

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestAutoStartConfig(t *testing.T) {
	tempDir := t.TempDir()

	p := &JailProvider{
		stateDir: tempDir,
		config: provider.ProviderConfig{
			Settings: make(map[string]interface{}),
		},
	}

	// Create a test jail config
	jailConfig := &jailConfig{
		Name:       "testjail",
		Path:       "/tmp/testjail",
		Networks:   []provider.NetworkSpec{},
		Parameters: make(map[string]interface{}),
		AutoStart: provider.AutoStartConfig{
			Enabled:  false,
			Priority: 50,
			DelayMS:  0,
		},
	}

	configPath := filepath.Join(tempDir, "testjail.json")
	if err := p.saveJailConfig(jailConfig, configPath); err != nil {
		t.Fatalf("Failed to save jail config: %v", err)
	}

	ctx := context.Background()
	handle := provider.InstanceHandle{
		ID:       "testjail",
		Provider: "jail",
	}

	t.Run("SetAutoStart", func(t *testing.T) {
		config := provider.AutoStartConfig{
			Enabled:  true,
			Priority: 10,
			DelayMS:  500,
		}

		err := p.SetAutoStart(ctx, handle, config)
		if err != nil {
			t.Errorf("SetAutoStart() error = %v", err)
		}

		// Verify it was saved
		loaded, err := p.loadJailConfig(configPath)
		if err != nil {
			t.Fatalf("Failed to load jail config: %v", err)
		}

		if loaded.AutoStart.Enabled != true {
			t.Error("Expected AutoStart.Enabled to be true")
		}
		if loaded.AutoStart.Priority != 10 {
			t.Errorf("Expected AutoStart.Priority = 10, got %d", loaded.AutoStart.Priority)
		}
		if loaded.AutoStart.DelayMS != 500 {
			t.Errorf("Expected AutoStart.DelayMS = 500, got %d", loaded.AutoStart.DelayMS)
		}
	})

	t.Run("GetAutoStart", func(t *testing.T) {
		config, err := p.GetAutoStart(ctx, handle)
		if err != nil {
			t.Errorf("GetAutoStart() error = %v", err)
		}

		if config.Enabled != true {
			t.Error("Expected AutoStart.Enabled to be true")
		}
		if config.Priority != 10 {
			t.Errorf("Expected AutoStart.Priority = 10, got %d", config.Priority)
		}
	})

	t.Run("SetAutoStart_InvalidPriority", func(t *testing.T) {
		config := provider.AutoStartConfig{
			Enabled:  true,
			Priority: 150, // Invalid: > 100
			DelayMS:  0,
		}

		err := p.SetAutoStart(ctx, handle, config)
		if err == nil {
			t.Error("Expected error for invalid priority > 100")
		}
	})

	t.Run("SetAutoStart_NegativeDelay", func(t *testing.T) {
		config := provider.AutoStartConfig{
			Enabled:  true,
			Priority: 50,
			DelayMS:  -100, // Invalid: negative
		}

		err := p.SetAutoStart(ctx, handle, config)
		if err == nil {
			t.Error("Expected error for negative delay")
		}
	})
}

func TestListAutoStartInstances(t *testing.T) {
	tempDir := t.TempDir()

	p := &JailProvider{
		stateDir: tempDir,
		config: provider.ProviderConfig{
			Settings: make(map[string]interface{}),
		},
	}

	// Create multiple jail configs with different auto-start settings
	jails := []struct {
		name     string
		enabled  bool
		priority int
	}{
		{"jail1", true, 20},
		{"jail2", false, 10},
		{"jail3", true, 5},
		{"jail4", true, 20},
	}

	for _, j := range jails {
		config := &jailConfig{
			Name:       j.name,
			Path:       "/tmp/" + j.name,
			Networks:   []provider.NetworkSpec{},
			Parameters: make(map[string]interface{}),
			AutoStart: provider.AutoStartConfig{
				Enabled:  j.enabled,
				Priority: j.priority,
				DelayMS:  0,
			},
		}
		configPath := filepath.Join(tempDir, j.name+".json")
		if err := p.saveJailConfig(config, configPath); err != nil {
			t.Fatalf("Failed to save jail config for %s: %v", j.name, err)
		}
	}

	ctx := context.Background()

	t.Run("ListAutoStartInstances", func(t *testing.T) {
		handles, err := p.ListAutoStartInstances(ctx)
		if err != nil {
			t.Errorf("ListAutoStartInstances() error = %v", err)
		}

		// Should only include enabled jails (jail1, jail3, jail4)
		// Fatal, not Error: the loop below indexes handles[0..2].
		if len(handles) != 3 {
			t.Fatalf("Expected 3 auto-start instances, got %d", len(handles))
		}

		// Should be sorted by priority: jail3 (5), jail1 (20), jail4 (20)
		expectedOrder := []string{"jail3", "jail1", "jail4"}
		for i, expected := range expectedOrder {
			if handles[i].ID != expected {
				t.Errorf("Expected handles[%d].ID = %s, got %s", i, expected, handles[i].ID)
			}
		}
	})
}

func TestBuildJailConfigWithAutoStart(t *testing.T) {
	p := &JailProvider{}

	t.Run("DefaultAutoStart", func(t *testing.T) {
		spec := provider.InstanceSpec{
			Name: "testjail",
		}

		config := p.buildJailConfig(spec, "/tmp/testjail")

		if config.AutoStart.Enabled != false {
			t.Error("Expected default AutoStart.Enabled to be false")
		}
		if config.AutoStart.Priority != 50 {
			t.Errorf("Expected default AutoStart.Priority = 50, got %d", config.AutoStart.Priority)
		}
	})

	t.Run("AutoStartFromProviderConfig", func(t *testing.T) {
		spec := provider.InstanceSpec{
			Name: "testjail",
			ProviderConfig: map[string]interface{}{
				"autostart":          true,
				"autostart_priority": 10,
				"autostart_delay":    1000,
			},
		}

		config := p.buildJailConfig(spec, "/tmp/testjail")

		if config.AutoStart.Enabled != true {
			t.Error("Expected AutoStart.Enabled to be true")
		}
		if config.AutoStart.Priority != 10 {
			t.Errorf("Expected AutoStart.Priority = 10, got %d", config.AutoStart.Priority)
		}
		if config.AutoStart.DelayMS != 1000 {
			t.Errorf("Expected AutoStart.DelayMS = 1000, got %d", config.AutoStart.DelayMS)
		}
	})
}
