package provider

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Registry manages provider lifecycle and discovery
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry creates a new provider registry
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register registers a provider with the registry
func (r *Registry) Register(provider Provider) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	metadata := provider.Metadata()
	if metadata.Name == "" {
		return fmt.Errorf("provider name cannot be empty")
	}

	if _, exists := r.providers[metadata.Name]; exists {
		return fmt.Errorf("provider %s already registered", metadata.Name)
	}

	r.providers[metadata.Name] = provider
	return nil
}

// Unregister removes a provider from the registry
func (r *Registry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.providers[name]; !exists {
		return fmt.Errorf("provider %s not found", name)
	}

	delete(r.providers, name)
	return nil
}

// Get retrieves a provider by name
func (r *Registry) Get(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	provider, exists := r.providers[name]
	if !exists {
		return nil, fmt.Errorf("provider %s not found", name)
	}

	return provider, nil
}

// List returns information about all registered providers
func (r *Registry) List() []ProviderInfo {
	// Snapshot under the lock, then ask each provider outside it: a health check
	// runs a command and may take seconds, and holding the read lock that long
	// blocks every registration behind it.
	r.mu.RLock()
	providers := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		providers = append(providers, p)
	}
	r.mu.RUnlock()

	infos := make([]ProviderInfo, 0, len(providers))
	for _, provider := range providers {
		metadata := provider.Metadata()
		capabilities := provider.Capabilities()

		// Check if provider is available
		available := healthy(provider)

		infos = append(infos, ProviderInfo{
			Name:         metadata.Name,
			Type:         metadata.Type,
			Version:      metadata.Version,
			Author:       metadata.Author,
			Description:  metadata.Description,
			Available:    available,
			Capabilities: capabilities,
		})
	}

	return infos
}

// IsAvailable checks if a provider is available on the current platform
func (r *Registry) IsAvailable(name string) bool {
	provider, err := r.Get(name)
	if err != nil {
		return false
	}

	return healthy(provider)
}

// providerHealthTimeout bounds one HealthCheck. Providers shell out for it
// (podman version, container system status), and a hung tool must not pin the
// registry's lock or the /providers request.
const providerHealthTimeout = 5 * time.Second

func healthy(p Provider) bool {
	ctx, cancel := context.WithTimeout(context.Background(), providerHealthTimeout)
	defer cancel()
	return p.HealthCheck(ctx) == nil
}

// ProviderInfo contains information about a registered provider
type ProviderInfo struct {
	Name         string
	Type         ProviderType
	Version      string
	Author       string
	Description  string
	Available    bool
	Capabilities ProviderCapabilities
}

// Global registry for built-in providers
var globalRegistry = NewRegistry()

// RegisterBuiltin registers a built-in provider with the global registry
func RegisterBuiltin(provider Provider) error {
	return globalRegistry.Register(provider)
}

// GetGlobalRegistry returns the global provider registry
func GetGlobalRegistry() *Registry {
	return globalRegistry
}
