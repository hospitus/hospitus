package orchestration

import (
	"context"
	"testing"
)

func TestServiceRegistry(t *testing.T) {
	reg := NewServiceRegistry()

	// Register a service
	info := &ServiceInfo{
		Name:      "webserver",
		StackName: "mystack",
		Addresses: []string{"10.0.0.5"},
		Ports: []ServicePort{
			{Port: 80, Protocol: "tcp", Name: "http"},
			{Port: 443, Protocol: "tcp", Name: "https"},
		},
		Status: "running",
		Health: HealthStatusHealthy,
	}
	reg.Register(info)

	// Get the service
	retrieved := reg.Get("mystack", "webserver")
	if retrieved == nil {
		t.Fatal("Expected to find service")
	}
	if retrieved.Name != "webserver" {
		t.Errorf("Expected name 'webserver', got '%s'", retrieved.Name)
	}
	if len(retrieved.Addresses) != 1 {
		t.Errorf("Expected 1 address, got %d", len(retrieved.Addresses))
	}
	if retrieved.Addresses[0] != "10.0.0.5" {
		t.Error("Address mismatch")
	}

	services := reg.List("mystack")
	if len(services) != 1 {
		t.Errorf("Expected 1 service, got %d", len(services))
	}

	// Unregister
	reg.Unregister("mystack", "webserver")
	retrieved = reg.Get("mystack", "webserver")
	if retrieved != nil {
		t.Error("Service should be unregistered")
	}
}

func TestServiceRegistryNoStack(t *testing.T) {
	reg := NewServiceRegistry()

	info := &ServiceInfo{
		Name:      "standalone",
		Addresses: []string{"10.0.0.10"},
		Status:    "running",
	}
	reg.Register(info)

	retrieved := reg.Get("", "standalone")
	if retrieved == nil {
		t.Fatal("Expected to find service")
	}
	if retrieved.Name != "standalone" {
		t.Error("Name mismatch")
	}
}

func TestServiceRegistryUpdateHealth(t *testing.T) {
	reg := NewServiceRegistry()

	info := &ServiceInfo{
		Name:      "db",
		StackName: "app",
		Health:    HealthStatusStarting,
	}
	reg.Register(info)

	reg.UpdateHealth("app", "db", HealthStatusHealthy)

	retrieved := reg.Get("app", "db")
	if retrieved.Health != HealthStatusHealthy {
		t.Errorf("Expected healthy status, got %s", retrieved.Health)
	}
}

func TestServiceResolver(t *testing.T) {
	reg := NewServiceRegistry()
	resolver := NewServiceResolver(reg)

	reg.Register(&ServiceInfo{
		Name:      "api",
		StackName: "backend",
		Addresses: []string{"10.0.0.20", "10.0.0.21"},
		Ports: []ServicePort{
			{Port: 8080, Protocol: "tcp", Name: "http"},
		},
	})

	ctx := context.Background()

	// Resolve by stack/service
	addrs, err := resolver.Resolve(ctx, "backend/api")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if len(addrs) != 2 {
		t.Errorf("Expected 2 addresses, got %d", len(addrs))
	}

	withPort, err := resolver.ResolveWithPort(ctx, "backend/api", "http")
	if err != nil {
		t.Fatalf("ResolveWithPort failed: %v", err)
	}
	if len(withPort) != 2 {
		t.Errorf("Expected 2 addresses with port, got %d", len(withPort))
	}
	if withPort[0] != "10.0.0.20:8080" {
		t.Errorf("Expected '10.0.0.20:8080', got '%s'", withPort[0])
	}
}

func TestServiceResolverNotFound(t *testing.T) {
	reg := NewServiceRegistry()
	resolver := NewServiceResolver(reg)

	ctx := context.Background()

	_, err := resolver.Resolve(ctx, "nonexistent")
	if err == nil {
		t.Error("Expected error for nonexistent service")
	}
}

func TestParseServiceName(t *testing.T) {
	tests := []struct {
		input       string
		wantStack   string
		wantService string
	}{
		{"webserver", "", "webserver"},
		{"mystack/webserver", "mystack", "webserver"},
		{"webserver.mystack", "mystack", "webserver"},
	}

	for _, tt := range tests {
		stack, service := parseServiceName(tt.input)
		if stack != tt.wantStack || service != tt.wantService {
			t.Errorf("parseServiceName(%q) = (%q, %q), want (%q, %q)",
				tt.input, stack, service, tt.wantStack, tt.wantService)
		}
	}
}

func TestEnvironmentBuilder(t *testing.T) {
	reg := NewServiceRegistry()
	builder := NewEnvironmentBuilder(reg)

	// Register services
	reg.Register(&ServiceInfo{
		Name:      "database",
		StackName: "app",
		Addresses: []string{"10.0.0.5"},
		Ports: []ServicePort{
			{Port: 5432, Protocol: "tcp", Name: "postgres"},
		},
	})
	reg.Register(&ServiceInfo{
		Name:      "cache",
		StackName: "app",
		Addresses: []string{"10.0.0.6"},
		Ports: []ServicePort{
			{Port: 6379, Protocol: "tcp"},
		},
	})

	env := builder.BuildEnv("app")

	if env["DATABASE_HOST"] != "10.0.0.5" {
		t.Errorf("Expected DATABASE_HOST=10.0.0.5, got %s", env["DATABASE_HOST"])
	}
	if env["DATABASE_PORT"] != "5432" {
		t.Errorf("Expected DATABASE_PORT=5432, got %s", env["DATABASE_PORT"])
	}
	if env["DATABASE_ADDR"] != "10.0.0.5:5432" {
		t.Errorf("Expected DATABASE_ADDR=10.0.0.5:5432, got %s", env["DATABASE_ADDR"])
	}
	if env["CACHE_HOST"] != "10.0.0.6" {
		t.Errorf("Expected CACHE_HOST=10.0.0.6, got %s", env["CACHE_HOST"])
	}
}

func TestServiceRegistryUpdateStatus(t *testing.T) {
	registry := NewServiceRegistry()
	registry.Register(&ServiceInfo{
		Name:      "web",
		StackName: "mystack",
		Addresses: []string{"10.0.0.1"},
		Status:    "running",
	})

	// Update status of existing service
	registry.UpdateStatus("mystack", "web", "stopped")
	info := registry.Get("mystack", "web")
	if info == nil {
		t.Fatal("service should still exist after UpdateStatus")
	}
	if info.Status != "stopped" {
		t.Errorf("expected status 'stopped', got %q", info.Status)
	}

	// Update status of non-existent service — must not panic
	registry.UpdateStatus("mystack", "nonexistent", "running")
}

func TestSanitizeEnvName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"webserver", "WEBSERVER"},
		{"web-server", "WEB_SERVER"},
		{"my.service", "MY_SERVICE"},
		{"MyService123", "MYSERVICE123"},
		{"service_name", "SERVICE_NAME"},
	}

	for _, tt := range tests {
		got := sanitizeEnvName(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeEnvName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
