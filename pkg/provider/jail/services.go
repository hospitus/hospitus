package jail

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// FreeBSD services are managed through:
//   - /etc/rc.conf: Service enablement (service_enable="YES")
//   - service(8): Start, stop, restart, status
//   - sysrc(8): Modify rc.conf settings
//
// Common services:
//   - nginx, apache24: Web servers
//   - postgresql, mysql: Databases
//   - sshd: SSH server
//   - sendmail, postfix: Mail servers

// ServiceInfo represents information about a service
type ServiceInfo struct {
	// Name is the service name (e.g., "nginx", "postgresql")
	Name string `json:"name"`

	// Enabled indicates if the service is enabled in rc.conf
	Enabled bool `json:"enabled"`

	// Running indicates if the service is currently running
	Running bool `json:"running"`

	// Description is a human-readable description
	Description string `json:"description,omitempty"`

	// RCScript is the path to the rc.d script
	RCScript string `json:"rc_script,omitempty"`
}

// ServiceAction represents an action to perform on a service
type ServiceAction string

const (
	ServiceActionStart   ServiceAction = "start"
	ServiceActionStop    ServiceAction = "stop"
	ServiceActionRestart ServiceAction = "restart"
	ServiceActionReload  ServiceAction = "reload"
	ServiceActionStatus  ServiceAction = "status"
)

// validServiceName matches an rc.d script name: sysrc builds "<name>_enable"
// and splits its argument on the first "=", so a name carrying one — say
// "sshd_enable=NO" — sets a different variable than the one asked for.
var validServiceName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

// validRcVariable matches an rc.conf variable name. sysrc treats the first "="
// as the assignment delimiter, so a name carrying one sets a different variable
// than the caller asked for.
var validRcVariable = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)

// validateRcVariable rejects anything that is not a plain rc.conf variable.
func validateRcVariable(name string) error {
	if !validRcVariable.MatchString(name) {
		return fmt.Errorf("invalid rc variable %q: expected a plain rc.conf variable name", name)
	}
	return nil
}

// validateServiceName rejects anything that is not a plain rc.d service name.
func validateServiceName(name string) error {
	if !validServiceName.MatchString(name) {
		return fmt.Errorf("invalid service name %q: expected a plain rc.d script name", name)
	}
	return nil
}

// EnableService enables a service to start at boot
func (p *JailProvider) EnableService(ctx context.Context, handle provider.InstanceHandle, serviceName string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if err := validateServiceName(serviceName); err != nil {
		return err
	}
	jailName := handle.ID

	// Check if jail is running
	if err := p.ensureJailRunning(ctx, handle); err != nil {
		return err
	}

	// Use sysrc to enable the service
	// sysrc <service>_enable=YES
	enableVar := fmt.Sprintf("%s_enable=YES", serviceName)
	output, err := p.cmd().CombinedOutput(ctx, "jexec", jailName, "sysrc", enableVar)
	if err != nil {
		return fmt.Errorf("failed to enable service %s: %w (output: %s)", serviceName, err, string(output))
	}

	return nil
}

// DisableService disables a service from starting at boot
func (p *JailProvider) DisableService(ctx context.Context, handle provider.InstanceHandle, serviceName string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if err := validateServiceName(serviceName); err != nil {
		return err
	}
	jailName := handle.ID

	// Check if jail is running
	if err := p.ensureJailRunning(ctx, handle); err != nil {
		return err
	}

	// Use sysrc to disable the service
	// sysrc <service>_enable=NO
	disableVar := fmt.Sprintf("%s_enable=NO", serviceName)
	output, err := p.cmd().CombinedOutput(ctx, "jexec", jailName, "sysrc", disableVar)
	if err != nil {
		return fmt.Errorf("failed to disable service %s: %w (output: %s)", serviceName, err, string(output))
	}

	return nil
}

// StartService starts a service
func (p *JailProvider) StartService(ctx context.Context, handle provider.InstanceHandle, serviceName string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if err := validateServiceName(serviceName); err != nil {
		return err
	}
	return p.serviceAction(ctx, handle, serviceName, ServiceActionStart)
}

// StopService stops a service
func (p *JailProvider) StopService(ctx context.Context, handle provider.InstanceHandle, serviceName string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if err := validateServiceName(serviceName); err != nil {
		return err
	}
	return p.serviceAction(ctx, handle, serviceName, ServiceActionStop)
}

// RestartService restarts a service
func (p *JailProvider) RestartService(ctx context.Context, handle provider.InstanceHandle, serviceName string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if err := validateServiceName(serviceName); err != nil {
		return err
	}
	return p.serviceAction(ctx, handle, serviceName, ServiceActionRestart)
}

// ReloadService reloads a service configuration
func (p *JailProvider) ReloadService(ctx context.Context, handle provider.InstanceHandle, serviceName string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if err := validateServiceName(serviceName); err != nil {
		return err
	}
	return p.serviceAction(ctx, handle, serviceName, ServiceActionReload)
}

// serviceAction performs an action on a service
func (p *JailProvider) serviceAction(ctx context.Context, handle provider.InstanceHandle, serviceName string, action ServiceAction) error {
	jailName := handle.ID

	// Check if jail is running
	if err := p.ensureJailRunning(ctx, handle); err != nil {
		return err
	}

	// Run service command
	output, err := p.cmd().CombinedOutput(ctx, "jexec", jailName, "service", serviceName, string(action))
	if err != nil {
		return fmt.Errorf("failed to %s service %s: %w (output: %s)", action, serviceName, err, string(output))
	}

	return nil
}

// GetServiceStatus returns the status of a service
func (p *JailProvider) GetServiceStatus(ctx context.Context, handle provider.InstanceHandle, serviceName string) (*ServiceInfo, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if err := validateServiceName(serviceName); err != nil {
		return nil, err
	}
	jailName := handle.ID

	// Check if jail is running
	if err := p.ensureJailRunning(ctx, handle); err != nil {
		return nil, err
	}

	info := &ServiceInfo{
		Name: serviceName,
	}

	// Check if enabled using sysrc
	enableVar := fmt.Sprintf("%s_enable", serviceName)
	output, err := p.cmd().Output(ctx, "jexec", jailName, "sysrc", "-n", enableVar)
	if err == nil {
		val := strings.TrimSpace(strings.ToUpper(string(output)))
		info.Enabled = val == "YES" || val == "TRUE" || val == "1"
	}

	// Check if running using service status
	if err := p.cmd().Run(ctx, "jexec", jailName, "service", serviceName, "onestatus"); err == nil {
		info.Running = true
	}

	// Try to get rc script path
	if output, err := p.cmd().Output(ctx, "jexec", jailName, "which", fmt.Sprintf("/usr/local/etc/rc.d/%s", serviceName)); err == nil {
		info.RCScript = strings.TrimSpace(string(output))
	} else {
		// Try /etc/rc.d for base system services
		if output, err := p.cmd().Output(ctx, "jexec", jailName, "which", fmt.Sprintf("/etc/rc.d/%s", serviceName)); err == nil {
			info.RCScript = strings.TrimSpace(string(output))
		}
	}

	return info, nil
}

// ListServices returns a list of available services
func (p *JailProvider) ListServices(ctx context.Context, handle provider.InstanceHandle) ([]ServiceInfo, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	jailName := handle.ID

	// Check if jail is running
	if err := p.ensureJailRunning(ctx, handle); err != nil {
		return nil, err
	}

	var services []ServiceInfo

	// /etc/rc.d is the base system's own directory and always exists in a
	// FreeBSD jail. Swallowing its failure returned an empty or partial list
	// with a nil error, which a caller reads as "this jail has no services".
	baseServices, err := p.listServicesInDir(ctx, jailName, "/etc/rc.d")
	if err != nil {
		return nil, fmt.Errorf("failed to list the base services of jail %s: %w", jailName, err)
	}
	services = append(services, baseServices...)

	// /usr/local/etc/rc.d only exists once a package installs a service, so its
	// absence is ordinary.
	if localServices, err := p.listServicesInDir(ctx, jailName, "/usr/local/etc/rc.d"); err == nil {
		services = append(services, localServices...)
	}

	return services, nil
}

// listServicesInDir lists services from a specific rc.d directory
func (p *JailProvider) listServicesInDir(ctx context.Context, jailName, dir string) ([]ServiceInfo, error) {
	output, err := p.cmd().Output(ctx, "jexec", jailName, "ls", "-1", dir)
	if err != nil {
		return nil, err
	}

	var services []ServiceInfo
	for _, name := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if name == "" {
			continue
		}

		// Skip non-service files
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".sample") {
			continue
		}

		info := ServiceInfo{
			Name:     name,
			RCScript: fmt.Sprintf("%s/%s", dir, name),
		}

		// Check if enabled
		enableVar := fmt.Sprintf("%s_enable", name)
		if out, err := p.cmd().Output(ctx, "jexec", jailName, "sysrc", "-n", enableVar); err == nil {
			val := strings.TrimSpace(strings.ToUpper(string(out)))
			info.Enabled = val == "YES" || val == "TRUE" || val == "1"
		}

		// Check if running
		if err := p.cmd().Run(ctx, "jexec", jailName, "service", name, "onestatus"); err == nil {
			info.Running = true
		}

		services = append(services, info)
	}

	return services, nil
}

// ListEnabledServices returns only services that are enabled
func (p *JailProvider) ListEnabledServices(ctx context.Context, handle provider.InstanceHandle) ([]ServiceInfo, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	services, err := p.ListServices(ctx, handle)
	if err != nil {
		return nil, err
	}

	var enabled []ServiceInfo
	for _, svc := range services {
		if svc.Enabled {
			enabled = append(enabled, svc)
		}
	}

	return enabled, nil
}

// ListRunningServices returns only services that are currently running
func (p *JailProvider) ListRunningServices(ctx context.Context, handle provider.InstanceHandle) ([]ServiceInfo, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	services, err := p.ListServices(ctx, handle)
	if err != nil {
		return nil, err
	}

	var running []ServiceInfo
	for _, svc := range services {
		if svc.Running {
			running = append(running, svc)
		}
	}

	return running, nil
}

// SetServiceConfig sets a service configuration variable
// Example: SetServiceConfig(ctx, handle, "nginx", "nginx_flags", "-c /etc/nginx/custom.conf")
func (p *JailProvider) SetServiceConfig(ctx context.Context, handle provider.InstanceHandle, serviceName, variable, value string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if err := validateServiceName(serviceName); err != nil {
		return err
	}
	if err := validateRcVariable(variable); err != nil {
		return err
	}
	// The value is the right-hand side of "sysrc <var>=<value>"; a newline in it
	// would start another assignment.
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("service configuration value must not contain a newline")
	}
	jailName := handle.ID

	// Check if jail is running
	if err := p.ensureJailRunning(ctx, handle); err != nil {
		return err
	}

	// Use sysrc to set the variable
	setting := fmt.Sprintf("%s_%s=%s", serviceName, variable, value)
	output, err := p.cmd().CombinedOutput(ctx, "jexec", jailName, "sysrc", setting)
	if err != nil {
		return fmt.Errorf("failed to set %s: %w (output: %s)", setting, err, string(output))
	}

	return nil
}

// GetServiceConfig gets a service configuration variable
func (p *JailProvider) GetServiceConfig(ctx context.Context, handle provider.InstanceHandle, serviceName, variable string) (string, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return "", fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if err := validateServiceName(serviceName); err != nil {
		return "", err
	}
	if err := validateRcVariable(variable); err != nil {
		return "", err
	}
	jailName := handle.ID

	// Check if jail is running
	if err := p.ensureJailRunning(ctx, handle); err != nil {
		return "", err
	}

	// Use sysrc to get the variable
	varName := fmt.Sprintf("%s_%s", serviceName, variable)
	output, err := p.cmd().Output(ctx, "jexec", jailName, "sysrc", "-n", varName)
	if err != nil {
		return "", fmt.Errorf("failed to get %s: %w", varName, err)
	}

	return strings.TrimSpace(string(output)), nil
}

// ensureJailRunning checks if the jail is running and returns an error if not
func (p *JailProvider) ensureJailRunning(ctx context.Context, handle provider.InstanceHandle) error {
	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to get jail state: %w", err)
	}

	if state != provider.StateRunning {
		return fmt.Errorf("jail must be running to manage services")
	}

	return nil
}

// EnableAndStartService is a convenience function that enables and starts a service
func (p *JailProvider) EnableAndStartService(ctx context.Context, handle provider.InstanceHandle, serviceName string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if err := validateServiceName(serviceName); err != nil {
		return err
	}
	if err := p.EnableService(ctx, handle, serviceName); err != nil {
		return err
	}
	return p.StartService(ctx, handle, serviceName)
}

// isAlreadyStopped reports whether an rc.d stop failed only because the service
// was not running.
func isAlreadyStopped(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not running") || strings.Contains(msg, "isn't running")
}

// StopAndDisableService is a convenience function that stops and disables a service
func (p *JailProvider) StopAndDisableService(ctx context.Context, handle provider.InstanceHandle, serviceName string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if err := validateServiceName(serviceName); err != nil {
		return err
	}
	// A service that is already stopped is the state we want; anything else —
	// a permission failure, an unknown service, a script that errors — means
	// the service may still be running, and disabling it then reports a
	// success that leaves it up until the next boot.
	if err := p.StopService(ctx, handle, serviceName); err != nil && !isAlreadyStopped(err) {
		return fmt.Errorf("failed to stop service %s before disabling it: %w", serviceName, err)
	}
	return p.DisableService(ctx, handle, serviceName)
}
