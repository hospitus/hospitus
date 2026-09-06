package orchestration

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
)

// ServiceInfo represents a discoverable service
type ServiceInfo struct {
	// Name is the service/instance name
	Name string

	// StackName is the parent stack name (if part of a stack)
	StackName string

	// Addresses are the IP addresses of the service
	Addresses []string

	// Ports are the exposed ports
	Ports []ServicePort

	// Status is the current service status
	Status string

	Health HealthStatus

	// Labels are service labels
	Labels map[string]string
}

// ServicePort represents an exposed port
type ServicePort struct {
	Port     int
	Protocol string // tcp, udp
	Name     string // optional name like "http", "grpc"
}

// ServiceRegistry maintains a registry of discoverable services
type ServiceRegistry struct {
	mu       sync.RWMutex
	services map[string]*ServiceInfo // serviceKey -> info
}

// NewServiceRegistry creates a new service registry
func NewServiceRegistry() *ServiceRegistry {
	return &ServiceRegistry{
		services: make(map[string]*ServiceInfo),
	}
}

// serviceKey generates a unique key for a service
func serviceKey(stackName, serviceName string) string {
	if stackName == "" {
		return serviceName
	}
	return stackName + "/" + serviceName
}

// Register registers a service in the registry
func (sr *ServiceRegistry) Register(info *ServiceInfo) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	key := serviceKey(info.StackName, info.Name)
	// A copy, so the registry owns what it holds: storing the caller's pointer
	// let a later mutation of its Labels or Addresses reach every reader
	// without passing the lock — which is exactly what Get already copies to
	// prevent in the other direction.
	sr.services[key] = cloneServiceInfo(info)
}

// Unregister removes a service from the registry
func (sr *ServiceRegistry) Unregister(stackName, serviceName string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	key := serviceKey(stackName, serviceName)
	delete(sr.services, key)
}

// cloneServiceInfo returns a deep copy of a ServiceInfo so callers never share
// the pointer the registry keeps mutating under its lock (UpdateHealth/Status).
func cloneServiceInfo(s *ServiceInfo) *ServiceInfo {
	if s == nil {
		return nil
	}
	c := *s
	c.Addresses = append([]string(nil), s.Addresses...)
	c.Ports = append([]ServicePort(nil), s.Ports...)
	if s.Labels != nil {
		c.Labels = make(map[string]string, len(s.Labels))
		for k, v := range s.Labels {
			c.Labels[k] = v
		}
	}
	return &c
}

// Get retrieves a service by name
func (sr *ServiceRegistry) Get(stackName, serviceName string) *ServiceInfo {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	key := serviceKey(stackName, serviceName)
	return cloneServiceInfo(sr.services[key])
}

// List returns all services, optionally filtered by stack
func (sr *ServiceRegistry) List(stackName string) []*ServiceInfo {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	var result []*ServiceInfo
	for _, svc := range sr.services {
		if stackName == "" || svc.StackName == stackName {
			result = append(result, cloneServiceInfo(svc))
		}
	}
	// Sorted: map iteration order is random, and BuildEnv writes one variable
	// per service under a sanitized prefix — two services sanitizing to the
	// same name produced a different environment on each call.
	sort.Slice(result, func(i, j int) bool {
		if result[i].StackName != result[j].StackName {
			return result[i].StackName < result[j].StackName
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// UpdateHealth updates the health status of a service
func (sr *ServiceRegistry) UpdateHealth(stackName, serviceName string, health HealthStatus) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	key := serviceKey(stackName, serviceName)
	if svc, ok := sr.services[key]; ok {
		svc.Health = health
	}
}

// UpdateStatus updates the status of a service
func (sr *ServiceRegistry) UpdateStatus(stackName, serviceName, status string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	key := serviceKey(stackName, serviceName)
	if svc, ok := sr.services[key]; ok {
		svc.Status = status
	}
}

// ServiceResolver resolves service names to addresses
type ServiceResolver struct {
	registry *ServiceRegistry
}

// NewServiceResolver creates a new service resolver
func NewServiceResolver(registry *ServiceRegistry) *ServiceResolver {
	return &ServiceResolver{registry: registry}
}

// Resolve resolves a service name to addresses
// Supports formats:
//   - "servicename" - looks up in default (empty) stack
//   - "stack/servicename" - looks up in specific stack
//   - "servicename.stack" - alternative format for stack lookup
func (sr *ServiceResolver) Resolve(ctx context.Context, name string) ([]string, error) {
	stackName, serviceName := parseServiceName(name)

	info := sr.registry.Get(stackName, serviceName)
	if info == nil {
		return nil, fmt.Errorf("service not found: %s", name)
	}

	if len(info.Addresses) == 0 {
		return nil, fmt.Errorf("service has no addresses: %s", name)
	}

	return info.Addresses, nil
}

// ResolveWithPort resolves a service to address:port combinations
func (sr *ServiceResolver) ResolveWithPort(ctx context.Context, name, portName string) ([]string, error) {
	stackName, serviceName := parseServiceName(name)

	info := sr.registry.Get(stackName, serviceName)
	if info == nil {
		return nil, fmt.Errorf("service not found: %s", name)
	}

	// Find the port
	var port *ServicePort
	for i := range info.Ports {
		if info.Ports[i].Name == portName || (portName == "" && i == 0) {
			port = &info.Ports[i]
			break
		}
	}

	if port == nil {
		return nil, fmt.Errorf("port not found: %s", portName)
	}

	// Combine addresses with port
	var result []string
	for _, addr := range info.Addresses {
		result = append(result, net.JoinHostPort(addr, fmt.Sprintf("%d", port.Port)))
	}

	return result, nil
}

// parseServiceName parses a service name into stack and service parts.
//
// Precedence:
//  1. "stack/service" — the part before the first '/' is the stack.
//  2. "service.stack" — the part after the last '.' is the stack; the service
//     name may itself contain dots (e.g. "app.v1.production").
//  3. Otherwise the whole string is the service in the default (empty) stack.
func parseServiceName(name string) (stackName, serviceName string) {
	if stack, svc, ok := strings.Cut(name, "/"); ok {
		return stack, svc
	}

	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:], name[:i]
	}

	// No stack specified
	return "", name
}

// EnvironmentBuilder generates environment variables for service discovery
type EnvironmentBuilder struct {
	registry *ServiceRegistry
}

// NewEnvironmentBuilder creates a new environment builder
func NewEnvironmentBuilder(registry *ServiceRegistry) *EnvironmentBuilder {
	return &EnvironmentBuilder{registry: registry}
}

// BuildEnv generates environment variables for service discovery
// Format:
//
//	{SERVICE}_HOST=10.0.0.5
//	{SERVICE}_PORT=8080
//	{SERVICE}_ADDR=10.0.0.5:8080
//
// For services in a stack:
//
//	{STACK}_{SERVICE}_HOST=10.0.0.5
func (eb *EnvironmentBuilder) BuildEnv(stackName string) map[string]string {
	env := make(map[string]string)

	services := eb.registry.List(stackName)
	for _, svc := range services {
		prefix := sanitizeEnvName(svc.Name)
		if svc.StackName != "" && svc.StackName != stackName {
			prefix = sanitizeEnvName(svc.StackName) + "_" + prefix
		}

		// Add primary address
		if len(svc.Addresses) > 0 {
			env[prefix+"_HOST"] = svc.Addresses[0]
		}

		// Add port and combined address
		if len(svc.Ports) > 0 {
			port := svc.Ports[0]
			env[prefix+"_PORT"] = fmt.Sprintf("%d", port.Port)
			if len(svc.Addresses) > 0 {
				env[prefix+"_ADDR"] = net.JoinHostPort(svc.Addresses[0], fmt.Sprintf("%d", port.Port))
			}

			// Add named ports
			for _, p := range svc.Ports {
				if p.Name != "" {
					portPrefix := prefix + "_" + sanitizeEnvName(p.Name)
					env[portPrefix+"_PORT"] = fmt.Sprintf("%d", p.Port)
				}
			}
		}
	}

	return env
}

// sanitizeEnvName converts a name to a valid environment variable name.
// A POSIX env name may not be empty nor start with a digit, so an empty result
// or a leading digit is prefixed with '_'.
func sanitizeEnvName(name string) string {
	result := make([]byte, 0, len(name))
	for _, c := range name {
		switch {
		case c >= 'A' && c <= 'Z':
			result = append(result, byte(c))
		case c >= 'a' && c <= 'z':
			result = append(result, byte(c-32)) // uppercase
		case c >= '0' && c <= '9':
			result = append(result, byte(c))
		case c == '-' || c == '_' || c == '.':
			result = append(result, '_')
		}
	}
	if len(result) == 0 || (result[0] >= '0' && result[0] <= '9') {
		result = append([]byte{'_'}, result...)
	}
	return string(result)
}
