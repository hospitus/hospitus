package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/manifest"
	"github.com/hospitus/hospitus/pkg/orchestration"
)

// StackResponse represents a stack in API responses
type StackResponse struct {
	Name      string                  `json:"name"`
	Status    string                  `json:"status"`
	Instances []StackInstanceResponse `json:"instances"`
	CreatedAt time.Time               `json:"created_at"`
	UpdatedAt time.Time               `json:"updated_at"`
}

// StackInstanceResponse represents a stack instance in API responses
type StackInstanceResponse struct {
	Name       string   `json:"name"`
	InstanceID string   `json:"instance_id"`
	Provider   string   `json:"provider"`
	Status     string   `json:"status"`
	Health     string   `json:"health"`
	DependsOn  []string `json:"depends_on,omitempty"`
}

// DeployStackRequest represents a request to deploy a stack
type DeployStackRequest struct {
	Manifest string         `json:"manifest"` // TOML manifest content
	Vars     map[string]any `json:"vars"`     // Variables for template substitution
}

// handleStacks handles stack-related requests
func (s *Server) handleStacks(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// Expected: api/v1/stacks[/stackname[/action]]

	switch len(parts) {
	case 3: // /api/v1/stacks
		switch r.Method {
		case http.MethodGet:
			s.handleListStacks(w, r)
		case http.MethodPost:
			s.handleDeployStack(w, r)
		default:
			s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
	case 4: // /api/v1/stacks/{name}
		stackName := parts[3]
		switch r.Method {
		case http.MethodGet:
			s.handleGetStack(w, r, stackName)
		case http.MethodDelete:
			s.handleDestroyStack(w, r, stackName)
		default:
			s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
	case 5: // /api/v1/stacks/{name}/{action}
		stackName := parts[3]
		action := parts[4]
		switch action {
		case "start":
			s.handleStartStack(w, r, stackName)
		case "stop":
			s.handleStopStack(w, r, stackName)
		case "services":
			s.handleStackServices(w, r, stackName)
		default:
			s.writeError(w, http.StatusNotFound, "Unknown action")
		}
	default:
		s.writeError(w, http.StatusNotFound, "Not found")
	}
}

// handleListStacks lists all deployed stacks
func (s *Server) handleListStacks(w http.ResponseWriter, r *http.Request) {
	if s.stackManager == nil {
		s.writeJSON(w, http.StatusOK, []StackResponse{})
		return
	}

	stacks := s.stackManager.ListStacks()
	response := make([]StackResponse, 0, len(stacks))

	for _, stack := range stacks {
		resp := stackToResponse(stack)
		response = append(response, resp)
	}

	s.writeJSON(w, http.StatusOK, response)
}

// handleGetStack returns details of a specific stack
func (s *Server) handleGetStack(w http.ResponseWriter, r *http.Request, stackName string) {
	if s.stackManager == nil {
		s.writeError(w, http.StatusNotFound, "Stack not found")
		return
	}

	stack := s.stackManager.GetStack(stackName)
	if stack == nil {
		s.writeError(w, http.StatusNotFound, "Stack not found")
		return
	}

	s.writeJSON(w, http.StatusOK, stackToResponse(stack))
}

// handleDeployStack deploys a new stack from a manifest
func (s *Server) handleDeployStack(w http.ResponseWriter, r *http.Request) {
	if s.stackManager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Stack manager not initialized")
		return
	}

	var req DeployStackRequest
	if err := s.decodeJSONBody(r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	// Parse the manifest.
	//
	// A stack posted here is deployed, not inspected, so its secrets have to
	// be real ones. The placeholder store rendered every {{ secret }} to
	// PLACEHOLDER-<scope>-<name>. The daemon runs as root and owns the state
	// directory, so this writes to /var/lib/hospitus/secrets.
	secrets := manifest.NewFileSecretStore("")
	parser := manifest.NewParser(secrets)
	parsed, err := parser.Parse([]byte(req.Manifest), "api-upload", req.Vars)
	if err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid manifest", err)
		return
	}

	if !parsed.IsStack() {
		s.writeError(w, http.StatusBadRequest, "Manifest is not a stack manifest")
		return
	}

	// Validate
	validator := manifest.NewValidator()
	validationErrs := validator.Validate(parsed)
	if validationErrs.HasErrors() {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Validation failed: %v", validationErrs))
		return
	}

	// SECURITY: a stack reaches the providers without passing through
	// handleCreateInstance, so this endpoint enforces the same boundary.
	// Convert each instance the way DeployStack will, and refuse the ones
	// asking for host authority the caller's key does not hold.
	for i := range parsed.Stack.Instances {
		inst := parsed.Stack.Instances[i]
		spec, convErr := manifest.InstanceConfigToSpec(&inst, s.stackManager.CloudInitRoot())
		if convErr != nil {
			s.writeLoggedError(w, http.StatusBadRequest,
				fmt.Sprintf("Invalid instance %q", inst.Name), convErr)
			return
		}
		if s.refusePrivilegedConfig(w, r, spec.ProviderConfig) {
			return
		}
	}

	// Deploy
	ctx := r.Context()
	if err := s.stackManager.DeployStack(ctx, parsed.Stack); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to deploy stack", err)
		return
	}

	stack := s.stackManager.GetStack(parsed.Stack.Stack.Name)
	if stack == nil {
		s.writeError(w, http.StatusInternalServerError, "Stack created but not found")
		return
	}

	s.writeJSON(w, http.StatusCreated, stackToResponse(stack))
}

// handleDestroyStack destroys a stack and all its instances
func (s *Server) handleDestroyStack(w http.ResponseWriter, r *http.Request, stackName string) {
	if s.stackManager == nil {
		s.writeError(w, http.StatusNotFound, "Stack not found")
		return
	}

	ctx := r.Context()
	if err := s.stackManager.DestroyStack(ctx, stackName); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to destroy stack", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleStartStack starts all instances in a stack
func (s *Server) handleStartStack(w http.ResponseWriter, r *http.Request, stackName string) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	if s.stackManager == nil {
		s.writeError(w, http.StatusNotFound, "Stack not found")
		return
	}

	ctx := r.Context()
	if err := s.stackManager.StartStack(ctx, stackName); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to start stack", err)
		return
	}

	stack := s.stackManager.GetStack(stackName)
	s.writeJSON(w, http.StatusOK, stackToResponse(stack))
}

// handleStopStack stops all instances in a stack
func (s *Server) handleStopStack(w http.ResponseWriter, r *http.Request, stackName string) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	if s.stackManager == nil {
		s.writeError(w, http.StatusNotFound, "Stack not found")
		return
	}

	ctx := r.Context()
	if err := s.stackManager.StopStack(ctx, stackName); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to stop stack", err)
		return
	}

	stack := s.stackManager.GetStack(stackName)
	s.writeJSON(w, http.StatusOK, stackToResponse(stack))
}

// handleStackServices returns service discovery info for a stack
func (s *Server) handleStackServices(w http.ResponseWriter, r *http.Request, stackName string) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	if s.stackManager == nil {
		s.writeError(w, http.StatusNotFound, "Stack not found")
		return
	}

	registry := s.stackManager.GetServiceRegistry()
	services := registry.List(stackName)

	response := make([]ServiceDiscoveryResponse, 0, len(services))
	for _, svc := range services {
		resp := ServiceDiscoveryResponse{
			Name:      svc.Name,
			StackName: svc.StackName,
			Addresses: svc.Addresses,
			Status:    svc.Status,
			Health:    string(svc.Health),
			Labels:    svc.Labels,
		}
		for _, p := range svc.Ports {
			resp.Ports = append(resp.Ports, ServicePortResponse{
				Port:     p.Port,
				Protocol: p.Protocol,
				Name:     p.Name,
			})
		}
		response = append(response, resp)
	}

	s.writeJSON(w, http.StatusOK, response)
}

// ServiceDiscoveryResponse represents a service in API responses
type ServiceDiscoveryResponse struct {
	Name      string                `json:"name"`
	StackName string                `json:"stack_name,omitempty"`
	Addresses []string              `json:"addresses"`
	Ports     []ServicePortResponse `json:"ports,omitempty"`
	Status    string                `json:"status"`
	Health    string                `json:"health"`
	Labels    map[string]string     `json:"labels,omitempty"`
}

// ServicePortResponse represents a service port
type ServicePortResponse struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Name     string `json:"name,omitempty"`
}

// stackToResponse converts a Stack to API response
func stackToResponse(stack *orchestration.Stack) StackResponse {
	if stack == nil {
		return StackResponse{}
	}

	resp := StackResponse{
		Name:      stack.Name,
		Status:    string(stack.Status),
		CreatedAt: stack.CreatedAt,
		UpdatedAt: stack.UpdatedAt,
		Instances: make([]StackInstanceResponse, 0, len(stack.Instances)),
	}

	for _, inst := range stack.Instances {
		instResp := StackInstanceResponse{
			Name:       inst.Name,
			InstanceID: inst.InstanceID,
			Provider:   inst.Provider,
			Status:     inst.Status,
			Health:     string(inst.Health),
			DependsOn:  inst.DependsOn,
		}
		resp.Instances = append(resp.Instances, instResp)
	}

	return resp
}
