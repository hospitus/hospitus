package api

import (
	"fmt"
	"net/http"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// ServiceActionResult represents the result of a service action
type ServiceActionResult struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// ServiceStatus represents the status of a service
type ServiceStatus struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Running  bool   `json:"running"`
	RCScript string `json:"rc_script,omitempty"`
}

// handleServices handles service management operations for a jail.
//
// Routes:
//
//	GET /api/v1/instances/{id}/services - List services
//	GET /api/v1/instances/{id}/services/{service} - Get service status
//	POST /api/v1/instances/{id}/services/{service}/{action} - Perform action
func (s *Server) handleServices(w http.ResponseWriter, r *http.Request, instanceID string, parts []string) {
	ctx := r.Context()

	// Get instance
	instance, err := s.lookupInstance(ctx, instanceID)
	if err != nil {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}

	// Services only work with jail provider
	if instance.Provider != "jail" {
		s.writeError(w, http.StatusBadRequest, "Service management is only supported for jails")
		return
	}

	// Get jail provider
	prov, err := s.registry.Get("jail")
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Jail provider not available")
		return
	}

	// The capability, not *jail.JailProvider: the concrete type meant anything
	// wrapping a jail provider — a decorator, a test double — was told 501 for
	// operations it implements.
	jailProv, ok := prov.(provider.ServiceProvider)
	if !ok {
		// The provider does not offer this, which is not a server fault: 500
		// told the caller to retry something that will never work.
		s.writeError(w, http.StatusNotImplemented, "Provider does not support services")
		return
	}

	// Parse path: /api/v1/instances/{id}/services[/{service}[/{action}]]
	// parts[0] = "api", parts[1] = "v1", parts[2] = "instances", parts[3] = id, parts[4] = "services"
	//
	// Seven segments at most: ">= 7" read parts[6] and ignored whatever
	// followed, so .../services/sshd/restart/extra restarted sshd.
	if len(parts) > 7 {
		s.writeError(w, http.StatusNotFound, "Not found")
		return
	}
	serviceName := ""
	action := ""
	if len(parts) >= 6 {
		serviceName = parts[5]
	}
	if len(parts) >= 7 {
		action = parts[6]
	}

	// Validated before dispatch, like the interface and bridge names: the
	// provider's own check is what keeps a malformed name away from
	// service(8), but reaching it for one turned a caller's mistake into a 500.
	if serviceName != "" {
		if err := validation.ValidateServiceName(serviceName); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid service name", err)
			return
		}
	}

	switch {
	case serviceName == "" && r.Method == http.MethodGet:
		// List services
		filter := r.URL.Query().Get("filter")
		s.handleListServices(w, r, jailProv, instance.Handle, filter)

	case serviceName != "" && action == "" && r.Method == http.MethodGet:
		s.handleGetServiceStatus(w, r, jailProv, instance.Handle, serviceName)

	case serviceName != "" && action != "" && r.Method == http.MethodPost:
		// Perform service action
		s.handleServiceAction(w, r, jailProv, instance.Handle, serviceName, action)

	default:
		// /services and /services/{name} are read; /services/{name}/{action}
		// is performed.
		if action == "" {
			s.writeMethodNotAllowed(w, http.MethodGet)
		} else {
			s.writeMethodNotAllowed(w, http.MethodPost)
		}
	}
}

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request, jailProv provider.ServiceProvider, handle provider.InstanceHandle, filter string) {
	ctx := r.Context()

	var services []provider.ServiceInfo
	var err error

	switch filter {
	case "enabled":
		services, err = jailProv.ListEnabledServices(ctx, handle)
	case "running":
		services, err = jailProv.ListRunningServices(ctx, handle)
	default:
		services, err = jailProv.ListServices(ctx, handle)
	}

	if err != nil {
		// The service(8) output names rc scripts and paths inside the jail;
		// the operator reads it in the daemon log.
		s.logger.Error("failed to list services", logging.FieldError, err)
		s.writeError(w, http.StatusInternalServerError, "Failed to list services")
		return
	}

	// Convert to API response format. Non-nil, so a jail with no services
	// serializes as [] rather than null — the shape the interface and
	// port-forward list endpoints already answer with.
	result := make([]ServiceStatus, 0, len(services))
	for _, svc := range services {
		result = append(result, ServiceStatus{
			Name:     svc.Name,
			Enabled:  svc.Enabled,
			Running:  svc.Running,
			RCScript: svc.RCScript,
		})
	}

	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleGetServiceStatus(w http.ResponseWriter, r *http.Request, jailProv provider.ServiceProvider, handle provider.InstanceHandle, serviceName string) {
	ctx := r.Context()

	status, err := jailProv.GetServiceStatus(ctx, handle, serviceName)
	if err != nil {
		s.logger.Error("failed to get service status", logging.FieldError, err)
		s.writeError(w, http.StatusInternalServerError, "Failed to get service status")
		return
	}

	result := ServiceStatus{
		Name:     serviceName,
		Enabled:  status.Enabled,
		Running:  status.Running,
		RCScript: status.RCScript,
	}

	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleServiceAction(w http.ResponseWriter, r *http.Request, jailProv provider.ServiceProvider, handle provider.InstanceHandle, serviceName, action string) {
	ctx := r.Context()

	var err error
	switch action {
	case "enable":
		err = jailProv.EnableService(ctx, handle, serviceName)
	case "disable":
		err = jailProv.DisableService(ctx, handle, serviceName)
	case "start":
		err = jailProv.StartService(ctx, handle, serviceName)
	case "stop":
		err = jailProv.StopService(ctx, handle, serviceName)
	case "restart":
		err = jailProv.RestartService(ctx, handle, serviceName)
	case "reload":
		err = jailProv.ReloadService(ctx, handle, serviceName)
	default:
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Unknown action: %s", action))
		return
	}

	result := ServiceActionResult{Success: err == nil}
	status := http.StatusOK
	if err != nil {
		// Logged and trimmed like every other error path: a multi-line
		// service(8) failure went back whole and left no trace in the log.
		s.logger.Error("Service action failed",
			"action", action, "service", serviceName, logging.FieldError, err)
		result.Message = trimDetail(err.Error())
		// The status carries the failure too: a client that checks the code
		// and not the body read "success" from a 200 whose payload said the
		// service had not started.
		status = http.StatusInternalServerError
	}

	s.writeJSON(w, status, result)
}
