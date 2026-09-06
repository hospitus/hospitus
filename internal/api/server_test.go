package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

// mockProvider implements provider.Provider for testing
type mockProvider struct {
	instances map[string]provider.InstanceHandle
}

func newMockProvider() *mockProvider {
	return &mockProvider{
		instances: make(map[string]provider.InstanceHandle),
	}
}

func (m *mockProvider) Metadata() provider.ProviderMetadata {
	return provider.ProviderMetadata{
		Name:        "mock",
		Version:     "1.0.0",
		Type:        provider.ProviderTypeVM,
		Description: "Mock provider for testing",
	}
}

func (m *mockProvider) Capabilities() provider.ProviderCapabilities {
	return provider.ProviderCapabilities{
		SupportsSnapshots: true,
		NetworkTypes:      []provider.NetworkType{provider.NetworkTypeBridge},
		DiskTypes:         []provider.DiskType{provider.DiskTypeRaw},
	}
}

func (m *mockProvider) Initialize(ctx context.Context, config provider.ProviderConfig) error {
	return nil
}

func (m *mockProvider) Shutdown(ctx context.Context) error {
	return nil
}

func (m *mockProvider) HealthCheck(ctx context.Context) error {
	return nil
}

func (m *mockProvider) CreateInstance(ctx context.Context, spec provider.InstanceSpec) (provider.InstanceHandle, error) {
	if logger := provider.LoggerFromContext(ctx); logger != nil {
		logger.Info("Creating mock instance", "name", spec.Name)
	}

	handle := provider.InstanceHandle{
		ID:       spec.Name,
		Provider: "mock",
	}
	// Built lazily: a mockProvider composed as a literal — which several tests
	// do, wrapping it — carries a nil map, and writing to one panics.
	if m.instances == nil {
		m.instances = make(map[string]provider.InstanceHandle)
	}
	m.instances[spec.Name] = handle
	return handle, nil
}

func (m *mockProvider) DeleteInstance(ctx context.Context, handle provider.InstanceHandle, force bool) error {
	delete(m.instances, handle.ID)
	return nil
}

func (m *mockProvider) StartInstance(ctx context.Context, handle provider.InstanceHandle) error {
	return nil
}

func (m *mockProvider) StopInstance(ctx context.Context, handle provider.InstanceHandle, opts provider.StopOptions) error {
	return nil
}

func (m *mockProvider) RestartInstance(ctx context.Context, handle provider.InstanceHandle) error {
	return nil
}

func (m *mockProvider) GetInstanceState(ctx context.Context, handle provider.InstanceHandle) (provider.InstanceState, error) {
	return provider.StateRunning, nil
}

func (m *mockProvider) GetInstanceInfo(ctx context.Context, handle provider.InstanceHandle) (provider.InstanceInfo, error) {
	return provider.InstanceInfo{}, nil
}

func (m *mockProvider) ListInstances(ctx context.Context, filter provider.InstanceFilter) ([]provider.InstanceHandle, error) {
	var handles []provider.InstanceHandle
	for _, h := range m.instances {
		handles = append(handles, h)
	}
	return handles, nil
}

func (m *mockProvider) SetInstanceResources(ctx context.Context, handle provider.InstanceHandle, resources provider.ResourceSpec) error {
	return nil
}

func (m *mockProvider) GetInstanceMetrics(ctx context.Context, handle provider.InstanceHandle) (provider.Metrics, error) {
	return provider.Metrics{}, nil
}

func (m *mockProvider) AttachDisk(ctx context.Context, handle provider.InstanceHandle, disk provider.DiskAttachment) error {
	return nil
}

func (m *mockProvider) DetachDisk(ctx context.Context, handle provider.InstanceHandle, diskID string) error {
	return nil
}

func (m *mockProvider) AttachNetwork(ctx context.Context, handle provider.InstanceHandle, network provider.NetworkAttachment) error {
	return nil
}

func (m *mockProvider) DetachNetwork(ctx context.Context, handle provider.InstanceHandle, interfaceID string) error {
	return nil
}

// Test setup helper
func setupTestServer(t *testing.T) (*Server, *datastore.Datastore) {
	// Create in-memory datastore
	ds, err := datastore.NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}

	// Create registry and register mock provider
	registry := provider.NewRegistry()
	mockProv := newMockProvider()
	if err := registry.Register(mockProv); err != nil {
		t.Fatalf("Failed to register mock provider: %v", err)
	}

	// Create server with nil config (uses defaults, no auth for tests)
	server, err := NewServer(":8080", ds, registry, nil)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	return server, ds
}

func TestHealthEndpoint(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response["status"] != "healthy" {
		t.Errorf("Expected status 'healthy', got %v", response["status"])
	}
}

func TestListProviders(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
	w := httptest.NewRecorder()

	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var providers []provider.ProviderInfo
	if err := json.NewDecoder(w.Body).Decode(&providers); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(providers) != 1 {
		t.Errorf("Expected 1 provider, got %d", len(providers))
	}

	if providers[0].Name != "mock" {
		t.Errorf("Expected provider 'mock', got %s", providers[0].Name)
	}
}

func TestGetProviderDetail(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers/mock", nil)
	w := httptest.NewRecorder()

	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Debug: log the entire response
	t.Logf("Full response: %+v", response)

	// Check that we have metadata
	if response["metadata"] == nil {
		t.Fatal("Expected metadata in response")
	}

	// metadata is a struct in the response; read it back as a map.
	metadata, ok := response["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("metadata is not a map, got %T: %+v", response["metadata"], response["metadata"])
	}

	name, ok := metadata["Name"]
	if !ok {
		t.Fatalf("Name field not found in metadata: %+v", metadata)
	}

	if name != "mock" {
		t.Errorf("Expected name 'mock', got %v", name)
	}
}

func TestCreateInstance(t *testing.T) {
	server, _ := setupTestServer(t)

	spec := provider.InstanceSpec{
		Name:     "test-vm",
		CPUs:     2,
		MemoryMB: 2048,
		Labels: map[string]string{
			"environment": "test",
		},
		Annotations: map[string]string{},
	}

	body, _ := json.Marshal(spec)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances?provider=mock", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected status 201, got %d", w.Code)
	}

	var instance datastore.Instance
	if err := json.NewDecoder(w.Body).Decode(&instance); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if instance.Name != "test-vm" {
		t.Errorf("Expected name 'test-vm', got %s", instance.Name)
	}

	if instance.Provider != "mock" {
		t.Errorf("Expected provider 'mock', got %s", instance.Provider)
	}
}

func TestGetInstance(t *testing.T) {
	server, ds := setupTestServer(t)
	ctx := context.Background()

	// Create instance in datastore
	instance := &datastore.Instance{
		ID:       "test-vm",
		Name:     "test-vm",
		Provider: "mock",
		State:    provider.StateStopped,
		Spec:     provider.InstanceSpec{Name: "test-vm"},
		Handle: provider.InstanceHandle{
			ID:       "test-vm",
			Provider: "mock",
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	if err := ds.CreateInstance(ctx, instance); err != nil {
		t.Fatalf("Failed to create instance: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/test-vm", nil)
	w := httptest.NewRecorder()

	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var retrieved datastore.Instance
	if err := json.NewDecoder(w.Body).Decode(&retrieved); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if retrieved.ID != "test-vm" {
		t.Errorf("Expected ID 'test-vm', got %s", retrieved.ID)
	}
}

func TestListInstances(t *testing.T) {
	server, ds := setupTestServer(t)
	ctx := context.Background()

	// Create multiple instances
	for i := 0; i < 3; i++ {
		instance := &datastore.Instance{
			ID:       fmt.Sprintf("vm-%d", i),
			Name:     fmt.Sprintf("vm-%d", i),
			Provider: "mock",
			State:    provider.StateStopped,
			Spec:     provider.InstanceSpec{Name: fmt.Sprintf("vm-%d", i)},
			Handle: provider.InstanceHandle{
				ID:       fmt.Sprintf("vm-%d", i),
				Provider: "mock",
			},
			Labels:      map[string]string{},
			Annotations: map[string]string{},
		}

		if err := ds.CreateInstance(ctx, instance); err != nil {
			t.Fatalf("Failed to create instance: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	w := httptest.NewRecorder()

	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var instances []*datastore.Instance
	if err := json.NewDecoder(w.Body).Decode(&instances); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(instances) != 3 {
		t.Errorf("Expected 3 instances, got %d", len(instances))
	}
}

func TestDeleteInstance(t *testing.T) {
	server, ds := setupTestServer(t)
	ctx := context.Background()

	// Create instance
	instance := &datastore.Instance{
		ID:       "test-vm-delete",
		Name:     "test-vm-delete",
		Provider: "mock",
		State:    provider.StateStopped,
		Spec:     provider.InstanceSpec{Name: "test-vm-delete"},
		Handle: provider.InstanceHandle{
			ID:       "test-vm-delete",
			Provider: "mock",
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	if err := ds.CreateInstance(ctx, instance); err != nil {
		t.Fatalf("Failed to create instance: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/instances/test-vm-delete", nil)
	w := httptest.NewRecorder()

	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("Expected status 204, got %d", w.Code)
	}

	// Verify instance was deleted
	_, err := ds.GetInstance(ctx, "test-vm-delete")
	if err == nil {
		t.Error("Expected error when getting deleted instance")
	}
}

func TestStartStopInstance(t *testing.T) {
	server, ds := setupTestServer(t)
	ctx := context.Background()

	// Create instance
	instance := &datastore.Instance{
		ID:       "test-vm-startstop",
		Name:     "test-vm-startstop",
		Provider: "mock",
		State:    provider.StateStopped,
		Spec:     provider.InstanceSpec{Name: "test-vm-startstop"},
		Handle: provider.InstanceHandle{
			ID:       "test-vm-startstop",
			Provider: "mock",
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	if err := ds.CreateInstance(ctx, instance); err != nil {
		t.Fatalf("Failed to create instance: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/test-vm-startstop/start", nil)
	w := httptest.NewRecorder()

	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 for start, got %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/instances/test-vm-startstop/stop", nil)
	w = httptest.NewRecorder()

	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 for stop, got %d", w.Code)
	}
}

func TestCORSHeaders(t *testing.T) {
	// Test with wildcard CORS (for development)
	ds, err := datastore.NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	registry := provider.NewRegistry()
	mockProv := newMockProvider()
	if err := registry.Register(mockProv); err != nil {
		t.Fatalf("Failed to register mock provider: %v", err)
	}

	// Create server with wildcard CORS enabled
	corsConfig := &ServerConfig{
		EnableAuth:     false,
		AllowedOrigins: []string{"*"},
	}
	server, err := NewServer(":8080", ds, registry, corsConfig)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/providers", nil)
	req.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()

	// Use the wrapped handler with middleware, not just the mux
	handler := server.withMiddleware(server.mux)
	handler.ServeHTTP(w, req)

	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("CORS wildcard headers not set correctly")
	}

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 for OPTIONS, got %d", w.Code)
	}
}

func TestCORSHeadersRestricted(t *testing.T) {
	// Test with restricted CORS origins
	ds, err := datastore.NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	registry := provider.NewRegistry()
	mockProv := newMockProvider()
	if err := registry.Register(mockProv); err != nil {
		t.Fatalf("Failed to register mock provider: %v", err)
	}

	// Create server with specific allowed origin
	corsConfig := &ServerConfig{
		EnableAuth:     false,
		AllowedOrigins: []string{"https://trusted.example.com"},
	}
	server, err := NewServer(":8080", ds, registry, corsConfig)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	// Test with allowed origin
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/providers", nil)
	req.Header.Set("Origin", "https://trusted.example.com")
	w := httptest.NewRecorder()

	handler := server.withMiddleware(server.mux)
	handler.ServeHTTP(w, req)

	if w.Header().Get("Access-Control-Allow-Origin") != "https://trusted.example.com" {
		t.Errorf("CORS should allow trusted origin, got: %s", w.Header().Get("Access-Control-Allow-Origin"))
	}

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 for OPTIONS from allowed origin, got %d", w.Code)
	}

	// Test with disallowed origin
	req2 := httptest.NewRequest(http.MethodOptions, "/api/v1/providers", nil)
	req2.Header.Set("Origin", "https://untrusted.example.com")
	w2 := httptest.NewRecorder()

	handler.ServeHTTP(w2, req2)

	if w2.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS should not allow untrusted origin")
	}

	if w2.Code != http.StatusForbidden {
		t.Errorf("Expected status 403 for OPTIONS from disallowed origin, got %d", w2.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	server, _ := setupTestServer(t)

	// Try POST to /health (only GET allowed)
	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	w := httptest.NewRecorder()

	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status 405, got %d", w.Code)
	}
}

func TestInstanceNotFound(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/nonexistent", nil)
	w := httptest.NewRecorder()

	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

func TestProviderNotFound(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers/nonexistent", nil)
	w := httptest.NewRecorder()

	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

// Security-focused tests

func TestSecurityHeaders(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	handler := server.withMiddleware(server.mux)
	handler.ServeHTTP(w, req)

	// Check required security headers
	expectedHeaders := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"Cache-Control":          "no-store, no-cache, must-revalidate, private",
	}

	for header, expected := range expectedHeaders {
		got := w.Header().Get(header)
		if got != expected {
			t.Errorf("Header %s = %q, want %q", header, got, expected)
		}
	}

	// Check CSP header
	csp := w.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Error("Content-Security-Policy header not set")
	}
}

func TestAuthenticationRequired(t *testing.T) {
	// Create server with authentication enabled
	ds, err := datastore.NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	registry := provider.NewRegistry()
	mockProv := newMockProvider()
	if err := registry.Register(mockProv); err != nil {
		t.Fatalf("Failed to register mock provider: %v", err)
	}

	authConfig := &ServerConfig{
		EnableAuth: true,
		APIKeys:    []string{"test-api-key-12345"},
	}
	server, err := NewServer(":8080", ds, registry, authConfig)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	handler := server.withMiddleware(server.mux)

	// Test without API key - should fail
	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 without API key, got %d", w.Code)
	}

	// Test with invalid API key - should fail
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
	req2.Header.Set("X-API-Key", "wrong-key")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 with invalid API key, got %d", w2.Code)
	}

	// Test with valid API key - should succeed
	req3 := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
	req3.Header.Set("X-API-Key", "test-api-key-12345")
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Errorf("Expected 200 with valid API key, got %d", w3.Code)
	}
}

func TestHealthEndpointNoAuthRequired(t *testing.T) {
	// Health endpoint should work without authentication
	ds, err := datastore.NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	registry := provider.NewRegistry()
	mockProv := newMockProvider()
	if err := registry.Register(mockProv); err != nil {
		t.Fatalf("Failed to register mock provider: %v", err)
	}

	authConfig := &ServerConfig{
		EnableAuth: true,
		APIKeys:    []string{"test-api-key"},
	}
	server, err := NewServer(":8080", ds, registry, authConfig)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	handler := server.withMiddleware(server.mux)

	// Health check without API key should still work
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Health endpoint should work without auth, got %d", w.Code)
	}
}

func TestRateLimiting(t *testing.T) {
	// Create server with aggressive rate limiting for testing
	ds, err := datastore.NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	registry := provider.NewRegistry()
	mockProv := newMockProvider()
	if err := registry.Register(mockProv); err != nil {
		t.Fatalf("Failed to register mock provider: %v", err)
	}

	rateLimitConfig := &ServerConfig{
		EnableAuth:        false,
		EnableRateLimit:   true,
		RequestsPerSecond: 1,
		BurstSize:         2,
	}
	server, err := NewServer(":8080", ds, registry, rateLimitConfig)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	handler := server.withMiddleware(server.mux)

	// First few requests should succeed (within burst)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.RemoteAddr = "192.168.1.100:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Request %d should succeed, got %d", i+1, w.Code)
		}
	}

	// Next request should be rate limited
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Expected 429 (rate limited), got %d", w.Code)
	}

	// Check Retry-After header
	if w.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header should be set on rate limit")
	}
}

func TestInvalidJSONRequest(t *testing.T) {
	server, _ := setupTestServer(t)

	// Send invalid JSON
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances?provider=mock", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for invalid JSON, got %d", w.Code)
	}
}

func TestMissingRequiredFields(t *testing.T) {
	server, _ := setupTestServer(t)

	// Send instance without name
	spec := provider.InstanceSpec{
		CPUs:     2,
		MemoryMB: 2048,
	}

	body, _ := json.Marshal(spec)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances?provider=mock", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for missing name, got %d", w.Code)
	}
}

func TestMissingProvider(t *testing.T) {
	server, _ := setupTestServer(t)

	spec := provider.InstanceSpec{
		Name:     "test-vm",
		CPUs:     2,
		MemoryMB: 2048,
	}

	body, _ := json.Marshal(spec)
	// No provider specified
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for missing provider, got %d", w.Code)
	}
}

func TestRestartInstance(t *testing.T) {
	server, ds := setupTestServer(t)
	ctx := context.Background()

	// Create instance
	instance := &datastore.Instance{
		ID:       "test-vm-restart",
		Name:     "test-vm-restart",
		Provider: "mock",
		State:    provider.StateRunning,
		Spec:     provider.InstanceSpec{Name: "test-vm-restart"},
		Handle: provider.InstanceHandle{
			ID:       "test-vm-restart",
			Provider: "mock",
		},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}

	if err := ds.CreateInstance(ctx, instance); err != nil {
		t.Fatalf("Failed to create instance: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/test-vm-restart/restart", nil)
	w := httptest.NewRecorder()

	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 for restart, got %d", w.Code)
	}
}

func TestStreamingCreateInstance(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	spec := provider.InstanceSpec{
		Name: "stream-test",
		Labels: map[string]string{
			"provider": "mock",
		},
	}
	body, _ := json.Marshal(spec)
	req, _ := http.NewRequest("POST", "/api/v1/instances", bytes.NewBuffer(body))
	req.Header.Set("Accept", "text/plain")

	w := httptest.NewRecorder()
	s.handleCreateInstance(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 for streaming create, got %d", w.Code)
	}

	output := w.Body.String()
	if !strings.Contains(output, "Creating instance stream-test with provider mock...") {
		t.Errorf("Missing expected progress message in output: %s", output)
	}
	if !strings.Contains(output, "Creating mock instance") {
		t.Errorf("Missing mock provider log message in output: %s", output)
	}
	if !strings.Contains(output, "SUCCESS:") {
		t.Errorf("Missing success message in output: %s", output)
	}
}

func TestSingleLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"already one line", "ERROR: boom\n", "ERROR: boom\n"},
		{"no trailing newline", "ERROR: boom", "ERROR: boom"},
		{"interior newline", "ERROR: failed\nQEMU error: too long\n", "ERROR: failed; QEMU error: too long\n"},
		{"carriage returns", "a\r\nb\rc\n", "a; b; c\n"},
		{"blank line dropped", "not found\n\nrun: hospitus image fetch\n", "not found; run: hospitus image fetch\n"},
		{"indentation trimmed", "failed\n  because of this\n", "failed; because of this\n"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := singleLine(tt.in); got != tt.want {
				t.Errorf("singleLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestClientErrorDetail(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantSeen bool
	}{
		{
			name:     "nil error",
			err:      nil,
			wantSeen: false,
		},
		{
			name:     "provider error is actionable",
			err:      provider.NewProviderError("podman", "delete", "web", errors.New("container is running")),
			wantSeen: true,
		},
		{
			name:     "wrapped provider error",
			err:      fmt.Errorf("delete failed: %w", provider.NewProviderError("qemu", "start", "vm1", errors.New("no such image"))),
			wantSeen: true,
		},
		{
			name:     "provider sentinel",
			err:      fmt.Errorf("cannot pause: %w", provider.ErrUnsupportedOperation),
			wantSeen: true,
		},
		{
			// Surfaced as well: the caller is an authenticated operator, and
			// "database is locked" tells them to retry, where a bare "Failed to
			// create snapshot" tells them only to go and read the log.
			name:     "internal failure is reported too",
			err:      errors.New("sql: database is locked"),
			wantSeen: true,
		},
		{
			// The case that prompted this: a provider returning a plain
			// fmt.Errorf rather than a *provider.ProviderError. It used to
			// reach the client as a bare 500 with no cause at all.
			name:     "unwrapped provider failure",
			err:      fmt.Errorf("failed to create snapshot: %w: %s", errors.New("exit status 125"), "--message is incompatible with --format oci"),
			wantSeen: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clientErrorDetail(tt.err)
			if tt.wantSeen && got == "" {
				t.Errorf("clientErrorDetail(%v) = \"\", want the cause to reach the caller", tt.err)
			}
			if !tt.wantSeen && got != "" {
				t.Errorf("clientErrorDetail(%v) = %q, want it withheld", tt.err, got)
			}
		})
	}
}

func TestStripEcho(t *testing.T) {
	tests := []struct {
		name      string
		detail    string
		clientMsg string
		want      string
	}{
		{
			// The case seen from the CLI: podman names the operation, the
			// handler names it again, and "repository name must be lowercase"
			// only turned up in third place.
			name:      "provider repeats the operation",
			detail:    "failed to create snapshot: exit status 125: repository name must be lowercase",
			clientMsg: "Failed to create snapshot",
			want:      "exit status 125: repository name must be lowercase",
		},
		{
			name:      "unrelated detail is untouched",
			detail:    "no space left on device",
			clientMsg: "Failed to create snapshot",
			want:      "no space left on device",
		},
		{
			// Similar opening words are not a restatement: cutting here would
			// throw away the part that says which snapshot.
			name:      "near miss is untouched",
			detail:    "failed to create snapshot dir: permission denied",
			clientMsg: "Failed to create snapshot",
			want:      "failed to create snapshot dir: permission denied",
		},
		{
			// Stripping everything would leave the caller with less than
			// before, so the echo is kept rather than emptied.
			name:      "detail that is only the echo",
			detail:    "Failed to create snapshot:",
			clientMsg: "Failed to create snapshot",
			want:      "Failed to create snapshot:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripEcho(tt.detail, tt.clientMsg); got != tt.want {
				t.Errorf("stripEcho(%q, %q) = %q, want %q", tt.detail, tt.clientMsg, got, tt.want)
			}
		})
	}
}

func TestTrimDetail(t *testing.T) {
	// A provider message spanning lines has to survive as one line: the JSON
	// field and the streaming protocol are both line-oriented.
	if got := trimDetail("failed to start QEMU\nQEMU error: too long"); got != "failed to start QEMU; QEMU error: too long" {
		t.Errorf("trimDetail did not flatten the message: %q", got)
	}

	long := strings.Repeat("x", maxErrorDetail+500)
	got := trimDetail(long)
	if len(got) <= maxErrorDetail {
		t.Errorf("trimDetail returned %d bytes, expected the cap plus a marker", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("a truncated detail must say so")
	}
}

// TestMergeInstanceSpecKeepsDeclaredResources pins the defect that made a
// started instance forget what it was asked for: the provider reports what it
// can observe and leaves the rest zero, so storing its answer wholesale erased
// the caller's own CPU and memory.
func TestMergeInstanceSpecKeepsDeclaredResources(t *testing.T) {
	declared := provider.InstanceSpec{
		Name:     "web",
		CPUs:     2,
		MemoryMB: 256,
		Image:    "docker.io/library/nginx:alpine",
		Labels:   map[string]string{"provider": "podman"},
	}

	// What a provider that reads none of this returns.
	discovered := provider.InstanceSpec{}

	merged := mergeInstanceSpec(declared, discovered)

	if merged.CPUs != 2 {
		t.Errorf("cpus = %d, want the declared 2", merged.CPUs)
	}
	if merged.MemoryMB != 256 {
		t.Errorf("memory = %d MB, want the declared 256", merged.MemoryMB)
	}
	if merged.Image != declared.Image {
		t.Errorf("image = %q, want the declared one", merged.Image)
	}
	if merged.Labels["provider"] != "podman" {
		t.Errorf("labels were dropped: %v", merged.Labels)
	}
}

// A provider that does observe something has the last word on it: that is the
// point of syncing after a start.
func TestMergeInstanceSpecTakesDiscoveredValues(t *testing.T) {
	declared := provider.InstanceSpec{
		Name:     "web",
		CPUs:     2,
		MemoryMB: 256,
		Networks: []provider.NetworkSpec{{ID: "declared"}},
	}
	discovered := provider.InstanceSpec{
		CPUs:     4,
		Arch:     "arm64",
		Networks: []provider.NetworkSpec{{ID: "observed", IPv4: "10.0.0.2/24"}},
	}

	merged := mergeInstanceSpec(declared, discovered)

	if merged.CPUs != 4 {
		t.Errorf("cpus = %d, want the observed 4", merged.CPUs)
	}
	if merged.MemoryMB != 256 {
		t.Errorf("memory = %d MB, want the declared 256 the provider said nothing about", merged.MemoryMB)
	}
	if merged.Arch != "arm64" {
		t.Errorf("arch = %q, want the observed arm64", merged.Arch)
	}
	if len(merged.Networks) != 1 || merged.Networks[0].IPv4 != "10.0.0.2/24" {
		t.Errorf("the address allocated at boot must win: %+v", merged.Networks)
	}
}
