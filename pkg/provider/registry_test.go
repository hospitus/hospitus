package provider

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// mockProvider is a mock implementation of the Provider interface for testing
type mockProvider struct {
	name         string
	providerType ProviderType
	healthy      bool
}

func (m *mockProvider) Metadata() ProviderMetadata {
	return ProviderMetadata{
		Name:    m.name,
		Version: "1.0.0",
		Type:    m.providerType,
		Author:  "Test",
	}
}

func (m *mockProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		SupportsSnapshots: true,
	}
}

func (m *mockProvider) Initialize(ctx context.Context, config ProviderConfig) error {
	return nil
}

func (m *mockProvider) Shutdown(ctx context.Context) error {
	return nil
}

func (m *mockProvider) HealthCheck(ctx context.Context) error {
	if !m.healthy {
		return ErrProviderNotAvailable
	}
	return nil
}

func (m *mockProvider) CreateInstance(ctx context.Context, spec InstanceSpec) (InstanceHandle, error) {
	return InstanceHandle{}, nil
}

func (m *mockProvider) DeleteInstance(ctx context.Context, handle InstanceHandle, force bool) error {
	return nil
}

func (m *mockProvider) StartInstance(ctx context.Context, handle InstanceHandle) error {
	return nil
}

func (m *mockProvider) StopInstance(ctx context.Context, handle InstanceHandle, opts StopOptions) error {
	return nil
}

func (m *mockProvider) RestartInstance(ctx context.Context, handle InstanceHandle) error {
	return nil
}

func (m *mockProvider) GetInstanceState(ctx context.Context, handle InstanceHandle) (InstanceState, error) {
	return StateRunning, nil
}

func (m *mockProvider) GetInstanceInfo(ctx context.Context, handle InstanceHandle) (InstanceInfo, error) {
	return InstanceInfo{}, nil
}

func (m *mockProvider) ListInstances(ctx context.Context, filter InstanceFilter) ([]InstanceHandle, error) {
	return nil, nil
}

func (m *mockProvider) SetInstanceResources(ctx context.Context, handle InstanceHandle, resources ResourceSpec) error {
	return nil
}

func (m *mockProvider) GetInstanceMetrics(ctx context.Context, handle InstanceHandle) (Metrics, error) {
	return Metrics{}, nil
}

func (m *mockProvider) AttachDisk(ctx context.Context, handle InstanceHandle, disk DiskAttachment) error {
	return nil
}

func (m *mockProvider) DetachDisk(ctx context.Context, handle InstanceHandle, diskID string) error {
	return nil
}

func (m *mockProvider) AttachNetwork(ctx context.Context, handle InstanceHandle, network NetworkAttachment) error {
	return nil
}

func (m *mockProvider) DetachNetwork(ctx context.Context, handle InstanceHandle, interfaceID string) error {
	return nil
}

func TestNewRegistry(t *testing.T) {
	registry := NewRegistry()
	if registry == nil {
		t.Fatal("NewRegistry returned nil")
	}

	if registry.providers == nil {
		t.Fatal("Registry providers map is nil")
	}
}

func TestRegisterProvider(t *testing.T) {
	registry := NewRegistry()
	provider := &mockProvider{name: "test", providerType: ProviderTypeVM, healthy: true}

	err := registry.Register(provider)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Try to register again (should fail)
	err = registry.Register(provider)
	if err == nil {
		t.Error("Expected error when registering duplicate provider")
	}
}

func TestRegisterProviderEmptyName(t *testing.T) {
	registry := NewRegistry()
	provider := &mockProvider{name: "", providerType: ProviderTypeVM}

	err := registry.Register(provider)
	if err == nil {
		t.Error("Expected error when registering provider with empty name")
	}
}

func TestGetProvider(t *testing.T) {
	registry := NewRegistry()
	provider := &mockProvider{name: "test", providerType: ProviderTypeVM, healthy: true}

	if err := registry.Register(provider); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	retrieved, err := registry.Get("test")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if retrieved == nil {
		t.Fatal("Get returned nil provider")
	}

	metadata := retrieved.Metadata()
	if metadata.Name != "test" {
		t.Errorf("Expected provider name 'test', got '%s'", metadata.Name)
	}
}

func TestGetProviderNotFound(t *testing.T) {
	registry := NewRegistry()

	_, err := registry.Get("nonexistent")
	if err == nil {
		t.Error("Expected error when getting non-existent provider")
	}
}

func TestUnregisterProvider(t *testing.T) {
	registry := NewRegistry()
	provider := &mockProvider{name: "test", providerType: ProviderTypeVM, healthy: true}

	if err := registry.Register(provider); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	err := registry.Unregister("test")
	if err != nil {
		t.Fatalf("Unregister failed: %v", err)
	}

	// Try to get after unregister (should fail)
	_, err = registry.Get("test")
	if err == nil {
		t.Error("Expected error when getting unregistered provider")
	}
}

func TestUnregisterProviderNotFound(t *testing.T) {
	registry := NewRegistry()

	err := registry.Unregister("nonexistent")
	if err == nil {
		t.Error("Expected error when unregistering non-existent provider")
	}
}

func TestListProviders(t *testing.T) {
	registry := NewRegistry()

	provider1 := &mockProvider{name: "test1", providerType: ProviderTypeVM, healthy: true}
	provider2 := &mockProvider{name: "test2", providerType: ProviderTypeContainer, healthy: true}

	if err := registry.Register(provider1); err != nil {
		t.Fatalf("Register provider1 failed: %v", err)
	}

	if err := registry.Register(provider2); err != nil {
		t.Fatalf("Register provider2 failed: %v", err)
	}

	infos := registry.List()
	if len(infos) != 2 {
		t.Errorf("Expected 2 providers, got %d", len(infos))
	}

	// Check that both providers are in the list
	foundTest1 := false
	foundTest2 := false

	for _, info := range infos {
		if info.Name == "test1" {
			foundTest1 = true
			if info.Type != ProviderTypeVM {
				t.Errorf("Expected provider1 type VM, got %s", info.Type)
			}
			if !info.Available {
				t.Error("Expected provider1 to be available")
			}
		}
		if info.Name == "test2" {
			foundTest2 = true
			if info.Type != ProviderTypeContainer {
				t.Errorf("Expected provider2 type Container, got %s", info.Type)
			}
		}
	}

	if !foundTest1 || !foundTest2 {
		t.Error("Not all providers found in list")
	}
}

func TestIsAvailable(t *testing.T) {
	registry := NewRegistry()

	healthyProvider := &mockProvider{name: "healthy", providerType: ProviderTypeVM, healthy: true}
	unhealthyProvider := &mockProvider{name: "unhealthy", providerType: ProviderTypeVM, healthy: false}

	if err := registry.Register(healthyProvider); err != nil {
		t.Fatalf("Register healthy provider failed: %v", err)
	}

	if err := registry.Register(unhealthyProvider); err != nil {
		t.Fatalf("Register unhealthy provider failed: %v", err)
	}

	if !registry.IsAvailable("healthy") {
		t.Error("Expected healthy provider to be available")
	}

	if registry.IsAvailable("unhealthy") {
		t.Error("Expected unhealthy provider to be unavailable")
	}

	if registry.IsAvailable("nonexistent") {
		t.Error("Expected non-existent provider to be unavailable")
	}
}

func TestListProvidersHealthCheck(t *testing.T) {
	registry := NewRegistry()

	healthyProvider := &mockProvider{name: "healthy", providerType: ProviderTypeVM, healthy: true}
	unhealthyProvider := &mockProvider{name: "unhealthy", providerType: ProviderTypeVM, healthy: false}

	if err := registry.Register(healthyProvider); err != nil {
		t.Fatalf("Register healthy provider failed: %v", err)
	}

	if err := registry.Register(unhealthyProvider); err != nil {
		t.Fatalf("Register unhealthy provider failed: %v", err)
	}

	infos := registry.List()

	for _, info := range infos {
		if info.Name == "healthy" && !info.Available {
			t.Error("Expected healthy provider to be available in list")
		}
		if info.Name == "unhealthy" && info.Available {
			t.Error("Expected unhealthy provider to be unavailable in list")
		}
	}
}

func TestConcurrentAccess(t *testing.T) {
	registry := NewRegistry()

	// Test concurrent registration
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			provider := &mockProvider{
				name:         fmt.Sprintf("provider%d", id),
				providerType: ProviderTypeVM,
				healthy:      true,
			}
			if err := registry.Register(provider); err != nil {
				t.Errorf("Register(provider%d) failed: %v", id, err)
			}
		}(i)
	}

	// Wait for all registrations
	wg.Wait()

	// Test concurrent reads
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			registry.List()
		}()
	}

	wg.Wait()

	infos := registry.List()
	if len(infos) != 10 {
		t.Errorf("Expected 10 providers after concurrent registration, got %d", len(infos))
	}
}
