// Package podman provides a Podman container provider implementation.
// Podman is a daemonless container runtime compatible with OCI containers.
// Cross-platform support:
// - Linux: Native podman
// - macOS: Via podman machine (Linux VM)
// - FreeBSD: Via podman-remote or Linux ABI compatibility
package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
	"github.com/hospitus/hospitus/pkg/validation"
)

const (
	providerName    = "podman"
	providerVersion = "1.0.0"
)

// PodmanProvider implements the provider.Provider interface for Podman containers.
type PodmanProvider struct {
	mu sync.RWMutex

	// nameMu serializes name-space operations (rename) so the uniqueness check
	// and the podman rename cannot interleave with a concurrent rename.
	nameMu sync.Mutex

	config    provider.ProviderConfig
	dataDir   string
	stateDir  string
	instances map[string]*containerInfo
	podmanBin string
	logger    *slog.Logger
	runner    execx.Runner
}

// containerInfo stores container state
type containerInfo struct {
	ID          string
	Name        string
	Image       string
	State       provider.InstanceState
	CreatedAt   time.Time
	StartedAt   time.Time
	IPAddress   string
	Labels      map[string]string
	Annotations map[string]string
}

// NewPodmanProvider creates a new Podman provider
func NewPodmanProvider() *PodmanProvider {
	return &PodmanProvider{
		instances: make(map[string]*containerInfo),
		logger:    logging.WithProvider("podman"),
		runner:    execx.Default(),
	}
}

// cmd returns the command runner, defaulting to the real os/exec backend when
// the provider was constructed without one (e.g. a bare struct literal in tests
// that does not inject a fake).
func (p *PodmanProvider) cmd() execx.Runner {
	if p.runner == nil {
		return execx.Default()
	}
	return p.runner
}

// Metadata returns provider metadata
func (p *PodmanProvider) Metadata() provider.ProviderMetadata {
	return provider.ProviderMetadata{
		Name:        providerName,
		Version:     providerVersion,
		Type:        provider.ProviderTypeContainer,
		Author:      "Hospitus Project",
		Description: "Podman container provider for OCI containers",
		Homepage:    "https://podman.io",
		License:     "BSD-2-Clause",
	}
}

// Capabilities returns what this provider supports
func (p *PodmanProvider) Capabilities() provider.ProviderCapabilities {
	return provider.ProviderCapabilities{
		SupportsSnapshots:      true, // Implemented via podman commit
		SupportsMigration:      false,
		SupportsLiveMigration:  false,
		SupportsCloning:        true,
		SupportsPause:          true,
		SupportsConsole:        true,
		SupportsVNC:            false,
		SupportsSerial:         false,
		NetworkTypes:           []provider.NetworkType{provider.NetworkTypeBridge, provider.NetworkTypeNAT, provider.NetworkTypeNone},
		MaxNetworkInterfaces:   16,
		DiskTypes:              []provider.DiskType{},
		MaxDisks:               0,
		SupportsHotplugDisk:    false,
		SupportedArchitectures: []string{"amd64", "arm64"},
		SupportsCrossArch:      true, // Via QEMU emulation
		MaxCPUs:                256,
		MaxMemoryMB:            512 * 1024, // 512GB
	}
}

// Initialize sets up the Podman provider
func (p *PodmanProvider) Initialize(ctx context.Context, config provider.ProviderConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if config.Logger != nil {
		p.logger = config.Logger.With(logging.FieldProvider, "podman")
	} else if p.logger == nil {
		p.logger = logging.WithProvider("podman")
	}

	p.config = config
	p.dataDir = config.DataDir
	p.stateDir = config.StateDir

	// Find podman binary
	podmanBin, err := exec.LookPath("podman")
	if err != nil {
		// Try podman-remote for FreeBSD
		podmanBin, err = exec.LookPath("podman-remote")
		if err != nil {
			return fmt.Errorf("podman not found: %w", err)
		}
	}
	p.podmanBin = podmanBin

	// Verify podman is working with a timeout
	versionCtx, versionCancel := context.WithTimeout(ctx, 10*time.Second)
	defer versionCancel()
	output, err := p.cmd().CombinedOutput(versionCtx, p.podmanBin, "version", "--format", "{{.Client.Version}}")
	if err != nil {
		errMsg := fmt.Sprintf("podman not responding: %v, output: %s", err, string(output))
		if runtime.GOOS == "darwin" {
			errMsg += "\n  On macOS, ensure podman machine is running:\n    podman machine init  # first time only\n    podman machine start"
		}
		return fmt.Errorf("%s", errMsg)
	}
	if len(output) == 0 {
		return fmt.Errorf("podman returned empty version")
	}

	// Create data directory
	podmanDataDir := filepath.Join(p.dataDir, "podman")
	if err := os.MkdirAll(podmanDataDir, 0o755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Load existing containers with timeout
	// Note: we call syncContainersLocked because we already hold the mutex
	syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Second)
	defer syncCancel()

	if err := p.syncContainersLocked(syncCtx); err != nil {
		// Non-fatal: log and continue with empty state. This can happen if podman
		// is not running yet or is slow during daemon startup.
		p.logger.Warn("failed to sync podman containers during init; starting with empty state",
			logging.FieldError, err)
		p.instances = make(map[string]*containerInfo)
	}

	return nil
}

// Shutdown cleans up resources
func (p *PodmanProvider) Shutdown(ctx context.Context) error {
	return nil
}

// HealthCheck verifies the provider is working
func (p *PodmanProvider) HealthCheck(ctx context.Context) error {
	if err := p.cmd().Run(ctx, p.podmanBin, "version"); err != nil {
		return fmt.Errorf("podman not responding: %w", err)
	}
	return nil
}

// containerExists asks Podman whether a container of that name is in its store.
// Hospitus keeps an in-memory view for speed, but Podman is the authority: a
// container can be removed with podman(1) without Hospitus ever hearing about it.
func (p *PodmanProvider) containerExists(ctx context.Context, name string) bool {
	return p.cmd().Run(ctx, p.podmanBin, "container", "exists", name) == nil
}

// podmanVolumeStrings collects volume mounts from a provider config as
// podman's source:dest[:opts] arguments.
func podmanVolumeStrings(config map[string]interface{}) []string {
	var out []string

	if volumes, ok := config["volumes"].([]interface{}); ok {
		for _, vol := range volumes {
			if volStr, ok := vol.(string); ok {
				out = append(out, volStr)
			}
		}
	}

	// A manifest's storage.volumes arrive as maps, the same shape the jail
	// provider reads.
	if mounts, ok := config["mounts"].([]interface{}); ok {
		for _, m := range mounts {
			mount, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			hostPath, _ := mount["host_path"].(string)
			mountPath, _ := mount["mount_path"].(string)
			if hostPath == "" || mountPath == "" {
				continue
			}
			arg := hostPath + ":" + mountPath
			if readOnly, _ := mount["read_only"].(bool); readOnly {
				arg += ":ro"
			}
			out = append(out, arg)
		}
	}

	return out
}

// checkPodmanVolume rejects a mount whose source is outside the locations hospitus
// manages.
//
// "/home/" was intentionally removed from this set: allowing the whole home tree
// let a container mount another user's data. "/tmp/hospitus-" went the same way:
// /tmp is world-writable, so any local user could create /tmp/hospitus-x as a
// symlink to / and have a rootful container bind-mount the host's root.
//
// The comparison is made on the resolved path, for the same reason: a symlink
// planted inside an allowed directory pointed anywhere a lexical prefix test
// would still accept.
func checkPodmanVolume(volStr string) error {
	allowedRoots := []string{"/var/lib/hospitus/volumes"}

	parts := strings.SplitN(volStr, ":", 3)
	if len(parts) < 2 {
		return fmt.Errorf("invalid volume mount format: %s (expected source:dest)", volStr)
	}
	sourcePath := filepath.Clean(parts[0])
	if strings.Contains(sourcePath, "..") {
		return fmt.Errorf("path traversal detected in volume mount: %s", volStr)
	}
	within, err := validation.PathWithinAny(sourcePath, allowedRoots...)
	if err != nil {
		return fmt.Errorf("cannot resolve volume source %s: %w", sourcePath, err)
	}
	if !within {
		return fmt.Errorf("volume source %s not under allowed roots %v", sourcePath, allowedRoots)
	}
	return nil
}

// isPodmanNamespaceMode reports whether a --network value selects a namespace
// rather than a network.
func isPodmanNamespaceMode(name string) bool {
	lower := strings.ToLower(name)
	return lower == "host" || strings.HasPrefix(lower, "ns:") || strings.HasPrefix(lower, "container:")
}

// CreateInstance creates a new container.
func (p *PodmanProvider) CreateInstance(ctx context.Context, spec provider.InstanceSpec) (handle provider.InstanceHandle, err error) {
	// Attach provider context so the API layer can tell a failed create from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("podman", "create", spec.Name, err) }()

	// Validate instance name before passing to podman
	if err := validation.ValidateInstanceName(spec.Name); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid container name: %w", err)
	}

	// Only the uniqueness check and name reservation need the lock. Reserve the
	// name with a placeholder, then release the lock for the slow pull/create so
	// other podman operations are not blocked for the whole (potentially minutes-
	// long) image pull.
	p.mu.Lock()
	if existing, ok := p.instances[spec.Name]; ok {
		// The map doubles as an in-process name reservation, so a create already
		// under way still wins the name. Anything else is only a cache: Podman
		// owns the container store, and a container removed with podman(1)
		// outside Hospitus would otherwise hold its name until the daemon restarts.
		if existing.State == provider.StateCreating || p.containerExists(ctx, spec.Name) {
			p.mu.Unlock()
			return provider.InstanceHandle{}, fmt.Errorf("container with name %s already exists", spec.Name)
		}
		p.logger.Info("dropping stale container record; it no longer exists in podman", "container", spec.Name)
		delete(p.instances, spec.Name)
	}
	p.instances[spec.Name] = &containerInfo{Name: spec.Name, State: provider.StateCreating}
	p.mu.Unlock()

	// Roll back the reservation unless we successfully register the real
	// container below.
	created := false
	defer func() {
		if created {
			return
		}
		p.mu.Lock()
		if c, ok := p.instances[spec.Name]; ok && c.State == provider.StateCreating {
			delete(p.instances, spec.Name)
		}
		p.mu.Unlock()
	}()

	// Build podman create command
	args := []string{"create", "--name", spec.Name}

	// Resource limits
	if spec.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(spec.CPUs))
	}
	if spec.MemoryMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", spec.MemoryMB))
	}

	// Network configuration
	if len(spec.Networks) > 0 {
		for i := range spec.Networks {
			net := &spec.Networks[i]
			switch net.Type {
			case provider.NetworkTypeBridge:
				if net.Bridge != "" {
					// host, ns:<path> and container:<id> are namespace modes, not
					// networks: host puts a rootful container in the host's own
					// network namespace. Only a named podman network is accepted.
					if isPodmanNamespaceMode(net.Bridge) {
						return provider.InstanceHandle{}, fmt.Errorf("network %q is a podman namespace mode, not a bridge network", net.Bridge)
					}
					args = append(args, "--network", net.Bridge)
				} else {
					args = append(args, "--network", "bridge")
				}
			case provider.NetworkTypeNone:
				args = append(args, "--network", "none")
			default:
				args = append(args, "--network", "bridge")
			}

			if net.IPv4 != "" && net.IPv4 != "dhcp" {
				args = append(args, "--ip", strings.Split(net.IPv4, "/")[0])
			}
		}
	}

	// Labels
	for k, v := range spec.Labels {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}

	// Add hospitus label for identification
	args = append(args, "--label", "hospitus.managed=true")

	// Volume mounts arrive in two shapes: the CLI's -v strings under "volumes",
	// and a manifest's storage.volumes under "mounts". Both are read, or a mount
	// written one way is dropped in silence.
	for _, volStr := range podmanVolumeStrings(spec.ProviderConfig) {
		if err := checkPodmanVolume(volStr); err != nil {
			return provider.InstanceHandle{}, err
		}
		args = append(args, "-v", volStr)
	}

	// Port mappings from provider config, in their string form. Validate each
	// entry instead of passing it straight to podman: a malformed or non-numeric
	// spec should be rejected with a clear error rather than surfacing as an
	// opaque podman failure.
	if ports, ok := spec.ProviderConfig["ports"].([]interface{}); ok {
		for _, port := range ports {
			portStr, ok := port.(string)
			if !ok {
				continue
			}
			if err := validatePodmanPortSpec(portStr); err != nil {
				return provider.InstanceHandle{}, err
			}
			args = append(args, "-p", portStr)
		}
	}

	// Port forwards from UWM manifest (port_forwards format)
	switch portForwards := spec.ProviderConfig["port_forwards"].(type) {
	case []map[string]interface{}:
		for _, pf := range portForwards {
			hostPort, _ := pf["host"].(int)
			containerPort, _ := pf["container"].(int)
			protocol, _ := pf["protocol"].(string)
			// Validate protocol
			if protocol != "" && protocol != "tcp" && protocol != "udp" {
				return provider.InstanceHandle{}, fmt.Errorf("invalid protocol: %s (must be tcp or udp)", protocol)
			}
			if protocol == "" {
				protocol = "tcp"
			}
			if hostPort > 0 && containerPort > 0 {
				args = append(args, "-p", fmt.Sprintf("%d:%d/%s", hostPort, containerPort, protocol))
			}
		}
	case []interface{}:
		// Handle interface{} slice (from JSON unmarshalling)
		for _, pf := range portForwards {
			pfMap, ok := pf.(map[string]interface{})
			if !ok {
				continue
			}
			hostPort := 0
			containerPort := 0
			protocol := "tcp"

			switch h := pfMap["host"].(type) {
			case float64:
				hostPort = int(h)
			case int:
				hostPort = h
			}
			switch c := pfMap["container"].(type) {
			case float64:
				containerPort = int(c)
			case int:
				containerPort = c
			}
			if p, ok := pfMap["protocol"].(string); ok && p != "" {
				protocol = p
			}

			if hostPort > 0 && containerPort > 0 {
				args = append(args, "-p", fmt.Sprintf("%d:%d/%s", hostPort, containerPort, protocol))
			}
		}
	}

	// Environment variables
	if envVars, ok := spec.ProviderConfig["environment"].(map[string]interface{}); ok {
		for k, v := range envVars {
			args = append(args, "-e", fmt.Sprintf("%s=%v", k, v))
		}
	}

	// Image - validate and normalize
	image := spec.Image
	if image == "" {
		return provider.InstanceHandle{}, fmt.Errorf("image is required")
	}

	if err := p.ValidateImageSource(image); err != nil {
		return provider.InstanceHandle{}, err
	}

	// Normalize image name (remove oci: prefix if present)
	image = normalizeImageName(image)

	// Pull image if not present locally
	if err := p.pullImageIfNeeded(ctx, image); err != nil {
		return provider.InstanceHandle{}, err
	}

	args = append(args, image)

	// Command (optional)
	// Podman passes command arguments directly as argv[] to the container
	// via exec.CommandContext. No shell interpretation occurs on the host,
	// so shell metacharacters (' > & etc.) are safe in this context.
	// We only enforce UTF-8 validity.
	if cmd, ok := spec.ProviderConfig["command"].([]interface{}); ok {
		for _, arg := range cmd {
			if argStr, ok := arg.(string); ok {
				if !utf8.ValidString(argStr) {
					return provider.InstanceHandle{}, fmt.Errorf("command argument is not valid UTF-8")
				}
				args = append(args, argStr)
			}
		}
	}

	// Execute create
	output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, args...)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create container: %w (output: %s)", err, string(output))
	}

	// Extract container ID from output
	// On FreeBSD, podman may output warnings like "WARNING: image platform (linux/amd64)..."
	// before the container ID. The container ID is always the last line and is a hex string.
	containerID := extractContainerID(string(output))
	if containerID == "" {
		return provider.InstanceHandle{}, fmt.Errorf("failed to get container ID from output: %s", string(output))
	}

	// Store container info, replacing the reservation placeholder.
	info := &containerInfo{
		ID:          containerID,
		Name:        spec.Name,
		Image:       image,
		State:       provider.StateStopped,
		CreatedAt:   time.Now(),
		Labels:      spec.Labels,
		Annotations: spec.Annotations,
	}
	p.mu.Lock()
	p.instances[spec.Name] = info
	created = true
	p.mu.Unlock()

	handle = provider.InstanceHandle{
		ID:       spec.Name,
		Provider: providerName,
		Metadata: map[string]interface{}{
			"name":  spec.Name,
			"image": image,
		},
	}

	// Apply the auto-start settings the caller asked for.
	//
	// The CLI puts them in ProviderConfig, as it does for a jail, and the jail
	// provider reads them there. This one keeps its auto-start in a file of
	// its own, written only by SetAutoStart, and never looked at
	// ProviderConfig — so "hospitus podman create --auto-start
	// --auto-start-priority 10" created the container, said nothing, and left
	// "hospitus podman autostart list" empty. Failing to record it is no reason
	// to undo a container that exists, so it is logged and the create stands.
	if enabled, ok := spec.ProviderConfig["autostart"].(bool); ok && enabled {
		cfg := provider.AutoStartConfig{Enabled: true, Priority: 50}
		if priority, ok := providerConfigInt(spec.ProviderConfig["autostart_priority"]); ok {
			cfg.Priority = priority
		}
		if delay, ok := providerConfigInt(spec.ProviderConfig["autostart_delay"]); ok {
			cfg.DelayMS = delay
		}
		if err := p.SetAutoStart(ctx, handle, cfg); err != nil {
			p.logger.Warn("container created but auto-start could not be recorded",
				"container", spec.Name, logging.FieldError, err)
		}
	}

	return handle, nil
}

// providerConfigInt reads a number out of ProviderConfig, which arrives as an
// int from the CLI and as a float64 once it has been through JSON.
func providerConfigInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

// DeleteInstance removes a container
func (p *PodmanProvider) DeleteInstance(ctx context.Context, handle provider.InstanceHandle, force bool) (err error) {
	// Attach provider context so the API layer can tell a failed delete from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("podman", "delete", handle.ID, err) }()

	// The lock is taken only around the map mutation at the end, as
	// CreateInstance does. Held across the podman child processes below, a slow
	// "podman stop" serialized every other container operation for its whole
	// duration.

	// Stop the container before removing it, as the jail provider does.
	//
	// podman refuses to remove a running container and says so:
	//
	//	cannot remove container ... as it is running - running or paused
	//	containers cannot be removed without force
	//
	// so "hospitus podman destroy web -y" failed on exactly the containers
	// someone would want to destroy, while "hospitus jail destroy" on a running
	// jail worked. One CLI should not answer differently depending on which
	// provider is behind it.
	//
	// This is podman stop rather than rm -f: it asks the container to shut
	// down and only then removes it, which is what stopping a jail does.
	// --force still escalates, below.
	if !force {
		if out, stopErr := p.cmd().CombinedOutput(ctx, p.podmanBin, "stop", handle.ID); stopErr != nil {
			// A container that is already stopped, or already gone, is not a
			// reason to refuse the delete that follows.
			p.logger.Debug("stop before delete did not succeed; continuing",
				"container", handle.ID, "output", strings.TrimSpace(string(out)))
		}
	}

	args := []string{"rm"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, handle.ID)

	output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, args...)
	if err != nil {
		// Ignore "no such container" errors
		if strings.Contains(string(output), "no such container") {
			p.forgetInstance(handle.ID)
			return nil
		}
		return fmt.Errorf("failed to delete container: %w (output: %s)", err, string(output))
	}

	p.forgetInstance(handle.ID)
	return nil
}

// forgetInstance drops a container from the cache under the lock.
func (p *PodmanProvider) forgetInstance(name string) {
	p.mu.Lock()
	delete(p.instances, name)
	p.mu.Unlock()
}

// setInstanceState records a container's new state in the cache, if it is one
// we know about.
func (p *PodmanProvider) setInstanceState(name string, state provider.InstanceState, started time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	info, ok := p.instances[name]
	if !ok {
		return
	}
	info.State = state
	if !started.IsZero() {
		info.StartedAt = started
	}
}

// StartInstance starts a container
func (p *PodmanProvider) StartInstance(ctx context.Context, handle provider.InstanceHandle) (err error) {
	// Attach provider context so the API layer can tell a failed start from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("podman", "start", handle.ID, err) }()

	// The lock covers only the cache update: podman start is a child process,
	// and holding the provider mutex across it blocked every other container
	// operation for its duration.
	output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, "start", handle.ID)
	if err != nil {
		return fmt.Errorf("failed to start container: %w (output: %s)", err, string(output))
	}

	p.setInstanceState(handle.ID, provider.StateRunning, time.Now())

	return nil
}

// StopInstance stops a container
func (p *PodmanProvider) StopInstance(ctx context.Context, handle provider.InstanceHandle, opts provider.StopOptions) (err error) {
	// Attach provider context so the API layer can tell a failed stop from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("podman", "stop", handle.ID, err) }()

	// As in StartInstance, the lock covers only the cache update: a graceful
	// "podman stop" can take its whole timeout.
	args := []string{"stop"}
	if opts.Force {
		args = append(args, "-t", "0") // Immediate kill
	} else if opts.Timeout > 0 {
		args = append(args, "-t", strconv.Itoa(int(opts.Timeout.Seconds())))
	}
	args = append(args, handle.ID)

	output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, args...)
	if err != nil {
		// Ignore "already stopped" errors
		if strings.Contains(string(output), "already stopped") {
			return nil
		}
		return fmt.Errorf("failed to stop container: %w (output: %s)", err, string(output))
	}

	p.setInstanceState(handle.ID, provider.StateStopped, time.Time{})

	return nil
}

// RestartInstance restarts a container
func (p *PodmanProvider) RestartInstance(ctx context.Context, handle provider.InstanceHandle) (err error) {
	// Attach provider context so the API layer can tell a failed restart from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("podman", "restart", handle.ID, err) }()

	output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, "restart", handle.ID)
	if err != nil {
		return fmt.Errorf("failed to restart container: %w (output: %s)", err, string(output))
	}

	p.setInstanceState(handle.ID, provider.StateRunning, time.Now())

	return nil
}

// GetInstanceState returns the current state
func (p *PodmanProvider) GetInstanceState(ctx context.Context, handle provider.InstanceHandle) (provider.InstanceState, error) {
	// Use podman inspect to get real state
	output, err := p.cmd().Output(ctx, p.podmanBin, "inspect", "--format", "{{.State.Status}}", handle.ID)
	if err != nil {
		return provider.StateUnknown, err
	}

	status := strings.TrimSpace(string(output))
	switch status {
	case "running":
		return provider.StateRunning, nil
	case "created", "exited", "stopped":
		return provider.StateStopped, nil
	case "paused":
		return provider.StatePaused, nil
	case "removing":
		return provider.StateDeleting, nil
	default:
		return provider.StateUnknown, nil
	}
}

// GetInstanceInfo returns detailed information
func (p *PodmanProvider) GetInstanceInfo(ctx context.Context, handle provider.InstanceHandle) (provider.InstanceInfo, error) {
	// Get inspect data
	output, err := p.cmd().Output(ctx, p.podmanBin, "inspect", handle.ID)
	if err != nil {
		return provider.InstanceInfo{}, fmt.Errorf("failed to inspect container: %w", err)
	}

	var inspectData []map[string]interface{}
	if err := json.Unmarshal(output, &inspectData); err != nil {
		return provider.InstanceInfo{}, fmt.Errorf("failed to parse inspect output: %w", err)
	}

	if len(inspectData) == 0 {
		return provider.InstanceInfo{}, fmt.Errorf("no container data found")
	}

	data := inspectData[0]

	info := provider.InstanceInfo{
		Handle: handle,
	}

	// Parse state
	if state, ok := data["State"].(map[string]interface{}); ok {
		if status, ok := state["Status"].(string); ok {
			switch status {
			case "running":
				info.State = provider.StateRunning
			case "paused":
				info.State = provider.StatePaused
			default:
				info.State = provider.StateStopped
			}
		}
		if pid, ok := state["Pid"].(float64); ok {
			info.PID = int(pid)
		}
		if startedAt, ok := state["StartedAt"].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, startedAt); err == nil {
				info.StartedAt = t
				if info.State == provider.StateRunning {
					info.Uptime = time.Since(t)
				}
			}
		}
	}

	// Parse config
	if config, ok := data["Config"].(map[string]interface{}); ok {
		if name, ok := config["Hostname"].(string); ok {
			info.Spec.Name = name
		}
		if image, ok := config["Image"].(string); ok {
			info.Spec.Image = image
		}
		if labels, ok := config["Labels"].(map[string]interface{}); ok {
			info.Spec.Labels = make(map[string]string)
			for k, v := range labels {
				if vs, ok := v.(string); ok {
					info.Spec.Labels[k] = vs
				}
			}
		}
	}

	// Get name from our cache
	p.mu.RLock()
	if cached, ok := p.instances[handle.ID]; ok {
		info.Spec.Name = cached.Name
	}
	p.mu.RUnlock()

	return info, nil
}

// ListInstances returns all containers
func (p *PodmanProvider) ListInstances(ctx context.Context, filter provider.InstanceFilter) ([]provider.InstanceHandle, error) {
	// Sync first
	if err := p.syncContainers(ctx); err != nil {
		return nil, err
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	var handles []provider.InstanceHandle
	for id, info := range p.instances {
		// Apply state filter
		if len(filter.States) > 0 {
			matched := false
			for _, s := range filter.States {
				if info.State == s {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}

		// Apply label filter
		if len(filter.Labels) > 0 {
			matched := true
			for k, v := range filter.Labels {
				if info.Labels[k] != v {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
		}

		handles = append(handles, provider.InstanceHandle{
			ID:       id,
			Provider: providerName,
			Metadata: map[string]interface{}{
				"name":  info.Name,
				"image": info.Image,
			},
		})
	}

	return handles, nil
}

// SetInstanceResources updates resource limits
func (p *PodmanProvider) SetInstanceResources(ctx context.Context, handle provider.InstanceHandle, resources provider.ResourceSpec) error {
	args := []string{"update"}

	if resources.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(resources.CPUs))
	}
	if resources.MemoryMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", resources.MemoryMB))
	}

	args = append(args, handle.ID)

	output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, args...)
	if err != nil {
		return fmt.Errorf("failed to update resources: %w (output: %s)", err, string(output))
	}

	return nil
}

// GetInstanceMetrics returns resource usage
func (p *PodmanProvider) GetInstanceMetrics(ctx context.Context, handle provider.InstanceHandle) (provider.Metrics, error) {
	output, err := p.cmd().Output(ctx, p.podmanBin, "stats", "--no-stream", "--format", "json", handle.ID)
	if err != nil {
		return provider.Metrics{}, fmt.Errorf("failed to get stats: %w", err)
	}

	var stats []map[string]interface{}
	if err := json.Unmarshal(output, &stats); err != nil {
		return provider.Metrics{}, fmt.Errorf("failed to parse stats: %w", err)
	}

	if len(stats) == 0 {
		return provider.Metrics{}, nil
	}

	s := stats[0]
	metrics := provider.Metrics{
		Timestamp: time.Now(),
	}

	// Podman 5 renders every counter as a human-readable string; older
	// versions and podman-remote against a Linux host send numbers. Read both.

	switch cpu := s["cpu_percent"].(type) {
	case float64:
		metrics.CPUUsagePercent = cpu
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(cpu), "%"), 64); err == nil {
			metrics.CPUUsagePercent = f
		}
	}

	// "mem_usage" carries both numbers — "46.33MB / 68.58GB" — and podman 5
	// sends no "mem_limit" of its own.
	switch mem := s["mem_usage"].(type) {
	case float64:
		metrics.MemoryUsedMB = int64(mem) / 1024 / 1024
	case string:
		if used, total, ok := parseBytePair(mem); ok {
			metrics.MemoryUsedMB = used / 1024 / 1024
			metrics.MemoryTotalMB = total / 1024 / 1024
		}
	}
	if memLimit, ok := s["mem_limit"].(float64); ok {
		metrics.MemoryTotalMB = int64(memLimit) / 1024 / 1024
	}

	if blockInput, ok := s["block_input"].(float64); ok {
		metrics.DiskReadBytes = int64(blockInput)
	}
	if blockOutput, ok := s["block_output"].(float64); ok {
		metrics.DiskWriteBytes = int64(blockOutput)
	}
	if blockIO, ok := s["block_io"].(string); ok {
		if read, written, ok := parseBytePair(blockIO); ok {
			metrics.DiskReadBytes, metrics.DiskWriteBytes = read, written
		}
	}

	if netInput, ok := s["net_input"].(float64); ok {
		metrics.NetRxBytes = int64(netInput)
	}
	if netOutput, ok := s["net_output"].(float64); ok {
		metrics.NetTxBytes = int64(netOutput)
	}
	if netIO, ok := s["net_io"].(string); ok {
		if rx, tx, ok := parseBytePair(netIO); ok {
			metrics.NetRxBytes, metrics.NetTxBytes = rx, tx
		}
	}

	return metrics, nil
}

// validatePodmanPortSpec validates a podman "-p" port mapping such as
// "8080:80", "127.0.0.1:8080:80", "8080:80/udp" or a range "8000-8010:8000-8010".
// It rejects empty, malformed, or non-numeric specs before they reach podman.
func validatePodmanPortSpec(spec string) error {
	if spec == "" {
		return fmt.Errorf("empty port mapping")
	}
	s := spec
	// Optional /proto suffix.
	if i := strings.LastIndex(s, "/"); i >= 0 {
		proto := strings.ToLower(s[i+1:])
		if proto != "tcp" && proto != "udp" {
			return fmt.Errorf("invalid protocol in port mapping %q (must be tcp or udp)", spec)
		}
		s = s[:i]
	}
	parts := strings.Split(s, ":")
	// [ip:]hostPort:containerPort — the last two components are ports.
	if len(parts) < 2 || len(parts) > 3 {
		return fmt.Errorf("invalid port mapping %q (expected [ip:]host:container[/proto])", spec)
	}
	for _, portPart := range parts[len(parts)-2:] {
		if err := validatePortComponent(portPart); err != nil {
			return fmt.Errorf("invalid port mapping %q: %w", spec, err)
		}
	}
	return nil
}

// validatePortComponent validates a single port or "start-end" range (1-65535).
func validatePortComponent(p string) error {
	check := func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("port %q is not numeric", v)
		}
		if n < 1 || n > 65535 {
			return fmt.Errorf("port %d out of range (1-65535)", n)
		}
		return nil
	}
	if lo, hi, ok := strings.Cut(p, "-"); ok {
		if err := check(lo); err != nil {
			return err
		}
		return check(hi)
	}
	return check(p)
}

// parseBytePair splits the "a / b" counters podman prints for memory, block and
// network usage, such as "46.33MB / 68.58GB".
func parseBytePair(s string) (first, second int64, ok bool) {
	left, right, found := strings.Cut(s, "/")
	if !found {
		return 0, 0, false
	}
	a, err := parseBytes(strings.TrimSpace(left))
	if err != nil {
		return 0, 0, false
	}
	b, err := parseBytes(strings.TrimSpace(right))
	if err != nil {
		return 0, 0, false
	}
	return a, b, true
}

// parseBytes parses a human-readable byte string (e.g. "1.5kB", "10MB", "500B") into int64.
func parseBytes(s string) (int64, error) {
	if s == "" || s == "--" {
		return 0, nil
	}
	s = strings.TrimSpace(s)

	// Remove trailing 'B' if present
	s = strings.TrimSuffix(s, "B")

	// Split numeric part from suffix
	var numPart string
	var suffix string
	for i, c := range s {
		if (c < '0' || c > '9') && c != '.' && c != '-' {
			numPart = s[:i]
			suffix = strings.ToUpper(strings.TrimSpace(s[i:]))
			break
		}
	}
	if numPart == "" {
		numPart = s
	}

	val, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, err
	}

	// The trailing "B" was already trimmed above, so binary suffixes arrive here
	// as "KI"/"MI"/"GI"/"TI" (e.g. "1.5KiB" → "1.5Ki" → "KI").
	//
	// Podman prints SI units and keeps the "i" forms for binary multiples:
	// "8.192kB" is exactly 8192 bytes. Reading kB as 1024 overstated every
	// counter, by 2.4% at kB and 7.4% at GB.
	switch suffix {
	case "K", "KB":
		return int64(val * 1000), nil
	case "KI", "KIB":
		return int64(val * 1024), nil
	case "M", "MB":
		return int64(val * 1000 * 1000), nil
	case "MI", "MIB":
		return int64(val * 1024 * 1024), nil
	case "G", "GB":
		return int64(val * 1000 * 1000 * 1000), nil
	case "GI", "GIB":
		return int64(val * 1024 * 1024 * 1024), nil
	case "T", "TB":
		return int64(val * 1000 * 1000 * 1000 * 1000), nil
	case "TI", "TIB":
		return int64(val * 1024 * 1024 * 1024 * 1024), nil
	default:
		return int64(val), nil
	}
}

// AttachDisk - not supported for Podman
func (p *PodmanProvider) AttachDisk(ctx context.Context, handle provider.InstanceHandle, disk provider.DiskAttachment) error {
	return fmt.Errorf("disk attachment not supported for Podman containers")
}

// DetachDisk - not supported for Podman
func (p *PodmanProvider) DetachDisk(ctx context.Context, handle provider.InstanceHandle, diskID string) error {
	return fmt.Errorf("disk detachment not supported for Podman containers")
}

// AttachNetwork connects a container to a network with "podman network
// connect", which works on a live container — no recreation needed.
// network.Network.Bridge carries the podman network name.
func (p *PodmanProvider) AttachNetwork(ctx context.Context, handle provider.InstanceHandle, network provider.NetworkAttachment) error {
	output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, "network", "connect", network.Network.Bridge, handle.ID)
	if err != nil {
		return fmt.Errorf("failed to connect network: %w (output: %s)", err, string(output))
	}
	return nil
}

// DetachNetwork disconnects a network from a container.
// For Podman, the interfaceID parameter is expected to be the network name,
// not a network interface ID. This is because Podman uses network names
// for connect/disconnect operations, unlike VM providers that use interface IDs.
func (p *PodmanProvider) DetachNetwork(ctx context.Context, handle provider.InstanceHandle, networkName string) error {
	if networkName == "" {
		return fmt.Errorf("network name is required")
	}
	output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, "network", "disconnect", networkName, handle.ID)
	if err != nil {
		return fmt.Errorf("failed to disconnect network %q: %w (output: %s)", networkName, err, string(output))
	}
	return nil
}

// syncContainers syncs our state with actual podman state
func (p *PodmanProvider) syncContainers(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.syncContainersLocked(ctx)
}

// syncContainersLocked syncs our state with actual podman state
// Caller must hold p.mu
func (p *PodmanProvider) syncContainersLocked(ctx context.Context) error {
	// List all containers managed by hospitus
	output, err := p.cmd().Output(ctx, p.podmanBin, "ps", "-a", "--filter", "label=hospitus.managed=true",
		"--format", "{{.ID}}\t{{.Names}}\t{{.Image}}\t{{.Status}}")
	if err != nil {
		// Surface the failure instead of masking it as "no containers": callers
		// (e.g. ListInstances) can no longer distinguish an empty host from a
		// broken podman. Initialize downgrades this to a warning on purpose.
		return fmt.Errorf("podman ps failed: %w", err)
	}

	// Clear and rebuild
	p.instances = make(map[string]*containerInfo)

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) < 4 {
			continue
		}

		id := fields[0]
		name := fields[1]
		image := fields[2]
		status := strings.ToLower(fields[3])

		state := provider.StateStopped
		if strings.HasPrefix(status, "up") {
			state = provider.StateRunning
		} else if strings.Contains(status, "paused") {
			state = provider.StatePaused
		}

		p.instances[name] = &containerInfo{
			ID:    id,
			Name:  name,
			Image: image,
			State: state,
		}
	}

	return nil
}

// ===== ExecProvider Implementation =====

// ExecCommand executes a command inside a container
func (p *PodmanProvider) ExecCommand(ctx context.Context, handle provider.InstanceHandle, opts provider.ExecOptions) (*provider.ExecResult, error) {
	args := []string{"exec"}

	// Add working directory
	if opts.WorkingDir != "" {
		args = append(args, "-w", opts.WorkingDir)
	}

	// Add user
	if opts.User != "" {
		args = append(args, "-u", opts.User)
	}

	// Add environment variables
	for k, v := range opts.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, handle.ID, opts.Command)
	args = append(args, opts.Args...)

	var execCtx context.Context
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(opts.Timeout)*time.Second)
		defer cancel()
	} else {
		execCtx = ctx
	}

	// Capture stdout and stderr separately so both are returned even on a
	// successful run (cmd.Output only surfaced stderr on failure, silently
	// dropping any warnings a command wrote to stderr while exiting 0).
	var outBuf, errBuf bytes.Buffer
	cmd := exec.CommandContext(execCtx, p.podmanBin, args...)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()

	result := &provider.ExecResult{
		Stdout: outBuf.String(),
		Stderr: errBuf.String(),
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("exec failed: %w", err)
		}
	}

	return result, nil
}

// ExecInteractive executes an interactive command
func (p *PodmanProvider) ExecInteractive(ctx context.Context, handle provider.InstanceHandle, opts provider.ExecOptions) error {
	args := []string{"exec", "-it"}

	if opts.WorkingDir != "" {
		args = append(args, "-w", opts.WorkingDir)
	}

	if opts.User != "" {
		args = append(args, "-u", opts.User)
	}

	for k, v := range opts.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, handle.ID, opts.Command)
	args = append(args, opts.Args...)

	cmd := exec.CommandContext(ctx, p.podmanBin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// ===== ExecStreamingProvider Implementation =====

// ExecCommandStream executes a command inside a container and streams output to the provided writers.
// This allows real-time output display for long-running commands.
func (p *PodmanProvider) ExecCommandStream(ctx context.Context, handle provider.InstanceHandle, opts provider.ExecOptions, stdout, stderr io.Writer) (int, error) {
	args := []string{"exec"}

	// Add working directory
	if opts.WorkingDir != "" {
		args = append(args, "-w", opts.WorkingDir)
	}

	// Add user
	if opts.User != "" {
		args = append(args, "-u", opts.User)
	}

	// Add environment variables
	for k, v := range opts.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, handle.ID, opts.Command)
	args = append(args, opts.Args...)

	var execCtx context.Context
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(opts.Timeout)*time.Second)
		defer cancel()
	} else {
		execCtx = ctx
	}

	cmd := exec.CommandContext(execCtx, p.podmanBin, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()

	exitCode := 0
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exitCode = exitError.ExitCode()
		} else {
			return -1, fmt.Errorf("command execution failed: %w", err)
		}
	}

	return exitCode, nil
}

// ValidateImageSource validates that an image source is valid for Podman
// Podman supports OCI images in format: oci:image:tag or docker://image:tag
func (p *PodmanProvider) ValidateImageSource(source string) error {
	if source == "" {
		return fmt.Errorf("image source is required")
	}

	// Accept oci: prefix (UWM format)
	if strings.HasPrefix(source, "oci:") {
		return nil
	}

	// Accept docker:// prefix
	if strings.HasPrefix(source, "docker://") {
		return nil
	}

	// Accept plain image names (e.g., nginx:alpine, docker.io/library/nginx)
	// These are valid podman image references
	if strings.Contains(source, "/") || strings.Contains(source, ":") {
		return nil
	}

	// Accept simple image names (e.g., nginx, alpine)
	if source != "" && !strings.Contains(source, " ") {
		return nil
	}

	return fmt.Errorf("invalid image source for podman: %s (use oci:image:tag or image:tag format)", source)
}

// normalizeImageName converts UWM image source to podman-compatible format
// and ensures fully qualified image names for FreeBSD compatibility
func normalizeImageName(source string) string {
	// Remove oci: prefix if present
	source = strings.TrimPrefix(source, "oci:")
	// Remove docker:// prefix if present
	source = strings.TrimPrefix(source, "docker://")

	// Check if the image is a short name (no registry specified)
	// Short names need to be prefixed with docker.io/library/ for FreeBSD
	// where unqualified-search-registries may not be configured
	if isShortName(source) {
		// Check if it's a single-component name (e.g., nginx:tag) or has a namespace
		parts := strings.SplitN(source, "/", 2)
		if len(parts) == 1 {
			// Single component (e.g., nginx:1.27-alpine) -> docker.io/library/nginx:1.27-alpine
			return "docker.io/library/" + source
		}
		// Has namespace but no registry (e.g., bitnami/nginx:latest) -> docker.io/bitnami/nginx:latest
		return "docker.io/" + source
	}

	return source
}

// isShortName checks if an image name is a short name (no registry specified)
func isShortName(image string) bool {
	// Split on first slash
	parts := strings.SplitN(image, "/", 2)
	if len(parts) == 1 {
		// No slash at all (e.g., nginx:1.27-alpine) - definitely a short name
		return true
	}

	// Check if the first part looks like a registry (contains a dot or is localhost)
	firstPart := parts[0]
	if strings.Contains(firstPart, ".") || firstPart == "localhost" || strings.Contains(firstPart, ":") {
		// Looks like a registry (docker.io, ghcr.io, localhost:5000, etc.)
		return false
	}

	// First part doesn't look like a registry (e.g., bitnami/nginx) - it's a namespace
	return true
}

// extractContainerID extracts the container ID from podman output
// Podman may output warnings (especially on FreeBSD) before the container ID.
// The container ID is a 64-character hex string, usually on the last non-empty line.
func extractContainerID(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")

	// Iterate from the end to find the container ID (hex string)
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		// Skip lines that look like warnings
		if strings.HasPrefix(line, "WARNING:") || strings.HasPrefix(line, "Error:") {
			continue
		}

		// Container ID should be a 64-character hex string
		// But short IDs (12 chars) are also valid
		if isValidContainerID(line) {
			return line
		}
	}

	return ""
}

// isValidContainerID checks if a string looks like a container ID (hex string)
func isValidContainerID(s string) bool {
	// Container IDs are typically 64 characters (full) or 12 characters (short)
	if len(s) != 64 && len(s) != 12 {
		return false
	}

	// Check if all characters are hex digits
	for _, c := range s {
		if !isHexDigit(c) {
			return false
		}
	}

	return true
}

// isHexDigit reports whether c is a hexadecimal digit, either case.
func isHexDigit(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// pullImageIfNeeded pulls an image if it doesn't exist locally
func (p *PodmanProvider) pullImageIfNeeded(ctx context.Context, image string) error {
	// Check if image exists locally
	if err := p.cmd().Run(ctx, p.podmanBin, "image", "exists", image); err == nil {
		// Image exists, no need to pull
		return nil
	}

	// Pull whatever variant matches the host first. On FreeBSD that is a native
	// FreeBSD image when the registry publishes one — ghcr.io/freebsd/* does,
	// and asking for Linux there fails with "no image found in image index".
	output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, "pull", image)
	if err == nil {
		return nil
	}

	// Most images are Linux-only, and FreeBSD runs those through the Linux ABI
	// compatibility layer, so a native pull finding nothing is expected rather
	// than fatal. Ask for the Linux variant before giving up.
	if runtime.GOOS == "freebsd" {
		linuxOutput, linuxErr := p.cmd().CombinedOutput(ctx, p.podmanBin, "pull", "--os", "linux", image)
		if linuxErr == nil {
			return nil
		}
		// Report both attempts. For an image that only ships a FreeBSD variant,
		// the Linux error alone reads "no image found ... OS linux" and points
		// away from whatever actually stopped the native pull.
		return fmt.Errorf("failed to pull image %s: native: %w (output: %s); linux: %w (output: %s)",
			image, err, string(output), linuxErr, string(linuxOutput))
	}

	return fmt.Errorf("failed to pull image %s: %w (output: %s)", image, err, string(output))
}

// refreshContainerInfo refreshes the cached container info from podman.
func (p *PodmanProvider) refreshContainerInfo(ctx context.Context, containerName string) error {
	output, err := p.cmd().Output(ctx, p.podmanBin, "inspect", "--format", "json", containerName)
	if err != nil {
		return fmt.Errorf("failed to inspect container: %w", err)
	}

	var inspectData []struct {
		ID      string `json:"Id"`
		Name    string `json:"Name"`
		Created string `json:"Created"`
		Image   string `json:"Image"`
		State   struct {
			Status    string `json:"Status"`
			StartedAt string `json:"StartedAt"`
		} `json:"State"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		NetworkSettings struct {
			IPAddress string `json:"IPAddress"`
		} `json:"NetworkSettings"`
	}

	if err := json.Unmarshal(output, &inspectData); err != nil {
		return fmt.Errorf("failed to parse container info: %w", err)
	}

	if len(inspectData) == 0 {
		return fmt.Errorf("container not found")
	}

	data := inspectData[0]

	state := provider.StateStopped
	switch data.State.Status {
	case "running":
		state = provider.StateRunning
	case "paused":
		state = provider.StatePaused
	}

	createdAt, _ := time.Parse(time.RFC3339, data.Created)
	startedAt, _ := time.Parse(time.RFC3339, data.State.StartedAt)

	p.mu.Lock()
	p.instances[containerName] = &containerInfo{
		ID:        data.ID,
		Name:      containerName,
		Image:     data.Image,
		State:     state,
		CreatedAt: createdAt,
		StartedAt: startedAt,
		IPAddress: data.NetworkSettings.IPAddress,
		Labels:    data.Config.Labels,
	}
	p.mu.Unlock()

	return nil
}

// Ensure PodmanProvider implements provider.Provider and provider.ExecProvider
var (
	_ provider.Provider              = (*PodmanProvider)(nil)
	_ provider.ExecProvider          = (*PodmanProvider)(nil)
	_ provider.ExecStreamingProvider = (*PodmanProvider)(nil)
)
