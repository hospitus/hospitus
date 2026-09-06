package manifest

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
)

// Validator validates manifest files
type Validator struct {
	// SupportedProviders lists available providers
	SupportedProviders []string
}

// NewValidator creates a new manifest validator
func NewValidator() *Validator {
	return &Validator{
		SupportedProviders: []string{
			ProviderTypeJail,
			ProviderTypeBhyve,
			ProviderTypeQEMU,
			ProviderTypePodman,
		},
	}
}

// Validate validates a parsed manifest
func (v *Validator) Validate(manifest *ParsedManifest) ValidationErrors {
	var errs ValidationErrors

	if manifest.Workload != nil {
		errs = append(errs, v.validateWorkload(manifest.Workload)...)
	}

	if manifest.Stack != nil {
		errs = append(errs, v.validateStack(manifest.Stack)...)
	}

	return errs
}

// validateWorkload validates a single workload manifest
func (v *Validator) validateWorkload(m *WorkloadManifest) ValidationErrors {
	var errs ValidationErrors

	// Validate metadata
	errs = append(errs, v.validateWorkloadMeta(m.Workload)...)

	// Validate provider (REQUIRED)
	errs = append(errs, v.validateProvider(m.Provider)...)

	// Determine OS type from provider overrides for jail Linux
	osType := ""
	if override, ok := m.ProviderOverrides[m.Provider.Type]; ok {
		osType = override.OSType
	}

	errs = append(errs, v.validateImage(m.Image, m.Provider.Type, osType)...)

	errs = append(errs, v.validateResources(m.Resources)...)

	// Validate networks
	for i := range m.Networks {
		network := m.Networks[i]
		errs = append(errs, v.validateNetwork(network, fmt.Sprintf("networks[%d]", i))...)
	}

	errs = append(errs, v.validateStorage(m.Storage)...)

	errs = append(errs, v.validateLifecycle("", m.Lifecycle)...)

	// Validate cloud-init
	if m.CloudInit != nil {
		errs = append(errs, v.validateCloudInit(m.CloudInit, m.Provider.Type, "cloud_init")...)
	}

	return errs
}

// validateStack validates a stack manifest
func (v *Validator) validateStack(m *StackManifest) ValidationErrors {
	var errs ValidationErrors

	// Validate stack metadata
	errs = append(errs, validateAPIVersion("stack.api_version", m.Stack.APIVersion)...)

	if m.Stack.Name == "" {
		errs = append(errs, ValidationError{Field: "stack.name", Message: "name is required"})
	} else if !isValidName(m.Stack.Name) {
		errs = append(errs, ValidationError{Field: "stack.name", Message: "invalid name format (must be alphanumeric with hyphens)"})
	}

	if len(m.Instances) == 0 {
		errs = append(errs, ValidationError{Field: "instances", Message: "at least one instance is required"})
	}

	// Track instance names for dependency validation
	instanceNames := make(map[string]bool)

	// Validate each instance
	for i := range m.Instances {
		inst := m.Instances[i]
		prefix := fmt.Sprintf("instances[%d]", i)

		if inst.Name == "" {
			errs = append(errs, ValidationError{Field: prefix + ".name", Message: "name is required"})
		} else {
			if instanceNames[inst.Name] {
				errs = append(errs, ValidationError{Field: prefix + ".name", Message: fmt.Sprintf("duplicate instance name: %s", inst.Name)})
			}
			instanceNames[inst.Name] = true
		}

		// Provider is required for each instance
		if inst.Provider == "" {
			errs = append(errs, ValidationError{Field: prefix + ".provider", Message: "provider is required"})
		} else if !v.isValidProvider(inst.Provider) {
			errs = append(errs, ValidationError{Field: prefix + ".provider", Message: fmt.Sprintf("unknown provider: %s", inst.Provider)})
		}

		// The same derivation validateWorkload makes: validateImage lets a
		// Linux jail omit image.source, and passing "" here refused a stack
		// instance that the identical workload manifest accepts.
		instOSType := ""
		if override, ok := inst.ProviderOverrides[inst.Provider]; ok {
			instOSType = override.OSType
		}
		errs = append(errs, v.validateImage(inst.Image, inst.Provider, instOSType)...)

		errs = append(errs, v.validateResources(inst.Resources)...)

		// Validate networks
		for j := range inst.Networks {
			network := inst.Networks[j]
			errs = append(errs, v.validateNetwork(network, fmt.Sprintf("%s.networks[%d]", prefix, j))...)
		}

		// Validate cloud-init
		if inst.CloudInit != nil {
			errs = append(errs, v.validateCloudInit(inst.CloudInit, inst.Provider, fmt.Sprintf("%s.cloud_init", prefix))...)
		}

		// Validate storage and lifecycle (parity with workload validation)
		errs = append(errs, v.validateStorage(inst.Storage)...)
		errs = append(errs, v.validateLifecycle(prefix, inst.Lifecycle)...)
	}

	// Validate dependencies (second pass)
	for i := range m.Instances {
		inst := m.Instances[i]
		if inst.DependsOn != nil {
			for _, dep := range inst.DependsOn.Services {
				if !instanceNames[dep] {
					errs = append(errs, ValidationError{
						Field:   fmt.Sprintf("instances[%d].depends_on.services", i),
						Message: fmt.Sprintf("unknown dependency: %s", dep),
					})
				}
				if dep == inst.Name {
					errs = append(errs, ValidationError{
						Field:   fmt.Sprintf("instances[%d].depends_on.services", i),
						Message: "instance cannot depend on itself",
					})
				}
			}
		}
	}

	// Detect multi-instance dependency cycles (e.g. a -> b -> a), which the
	// per-edge checks above cannot catch and which would deadlock startup.
	if cycle := detectDependencyCycle(m.Instances); len(cycle) > 0 {
		errs = append(errs, ValidationError{
			Field:   "instances.depends_on",
			Message: fmt.Sprintf("dependency cycle detected: %s", strings.Join(cycle, " -> ")),
		})
	}

	return errs
}

// detectDependencyCycle returns the first dependency cycle found among the
// instances (as a name path ending back at the start), or nil if the
// depends_on graph is acyclic. Unknown dependencies are ignored here; they are
// reported separately.
func detectDependencyCycle(instances []InstanceConfig) []string {
	graph := make(map[string][]string, len(instances))
	exists := make(map[string]bool, len(instances))
	for i := range instances {
		exists[instances[i].Name] = true
	}
	for i := range instances {
		inst := instances[i]
		if inst.DependsOn == nil {
			continue
		}
		for _, dep := range inst.DependsOn.Services {
			if exists[dep] {
				graph[inst.Name] = append(graph[inst.Name], dep)
			}
		}
	}

	const (
		white = 0 // unvisited
		gray  = 1 // on current DFS stack
		black = 2 // fully explored
	)
	color := make(map[string]int, len(instances))
	var path []string

	var dfs func(node string) []string
	dfs = func(node string) []string {
		color[node] = gray
		path = append(path, node)
		for _, next := range graph[node] {
			switch color[next] {
			case gray:
				// Back-edge: extract the cycle from where next first appears.
				for idx := range path {
					if path[idx] == next {
						return append(append([]string{}, path[idx:]...), next)
					}
				}
			case white:
				if cyc := dfs(next); cyc != nil {
					return cyc
				}
			}
		}
		path = path[:len(path)-1]
		color[node] = black
		return nil
	}

	for i := range instances {
		name := instances[i].Name
		if color[name] == white {
			path = path[:0]
			if cyc := dfs(name); cyc != nil {
				return cyc
			}
		}
	}
	return nil
}

// validateAPIVersion rejects a manifest written against a version this build
// does not know.
//
// A manifest from a future release names fields this build would ignore, and
// applying it as though it were current is worse than refusing it. An empty
// value is not an error: the field is optional and the parser fills it in.
func validateAPIVersion(field, value string) ValidationErrors {
	if value == "" || value == APIVersion {
		return nil
	}
	return ValidationErrors{{
		Field: field,
		Message: fmt.Sprintf("unknown manifest version %q; this build understands %s",
			value, APIVersion),
	}}
}

// validateWorkloadMeta validates workload metadata
func (v *Validator) validateWorkloadMeta(m WorkloadMeta) ValidationErrors {
	var errs ValidationErrors

	errs = append(errs, validateAPIVersion("workload.api_version", m.APIVersion)...)

	if m.Name == "" {
		errs = append(errs, ValidationError{Field: "workload.name", Message: "name is required"})
	} else if !isValidName(m.Name) {
		errs = append(errs, ValidationError{Field: "workload.name", Message: "invalid name format (must be alphanumeric with hyphens, max 63 chars)"})
	}

	return errs
}

// validateProvider validates provider configuration
func (v *Validator) validateProvider(p ProviderSpec) ValidationErrors {
	var errs ValidationErrors

	if p.Type == "" {
		errs = append(errs, ValidationError{Field: "provider.type", Message: "provider type is required"})
	} else if !v.isValidProvider(p.Type) {
		errs = append(errs, ValidationError{
			Field:   "provider.type",
			Message: fmt.Sprintf("unknown provider: %s (valid: %s)", p.Type, strings.Join(v.SupportedProviders, ", ")),
		})
	}

	return errs
}

// validateImage validates image configuration
func (v *Validator) validateImage(img ImageSpec, providerType, osType string) ValidationErrors {
	var errs ValidationErrors

	// Linux jails use debootstrap instead of a base image; image source is optional
	if img.Source == "" {
		if providerType == ProviderTypeJail && osType == "linux" {
			return errs
		}
		errs = append(errs, ValidationError{Field: "image.source", Message: "image source is required"})
		return errs
	}
	if img.Source == "none" {
		return errs // Boot from physical disk — no image needed
	}

	imageType, _, err := ParseImageSource(img.Source)
	if err != nil {
		errs = append(errs, ValidationError{Field: "image.source", Message: err.Error()})
		return errs
	}

	// Validate image type is compatible with provider
	errs = append(errs, v.validateImageProviderCompat(imageType, providerType)...)

	// Validate architecture
	validArchs := []string{"amd64", "arm64", "riscv64", "i386"}
	if img.Arch != "" && !slices.Contains(validArchs, img.Arch) {
		errs = append(errs, ValidationError{
			Field:   "image.arch",
			Message: fmt.Sprintf("unsupported architecture: %s (valid: %s)", img.Arch, strings.Join(validArchs, ", ")),
		})
	}

	return errs
}

// validateImageProviderCompat checks if image type is compatible with provider
func (v *Validator) validateImageProviderCompat(imageType, providerType string) ValidationErrors {
	var errs ValidationErrors

	compatible := map[string][]string{
		ProviderTypeJail:   {ImageTypeFreeBSD},
		ProviderTypeBhyve:  {ImageTypeFreeBSD, ImageTypeCloud, ImageTypeISO},
		ProviderTypeQEMU:   {ImageTypeFreeBSD, ImageTypeCloud, ImageTypeISO},
		ProviderTypePodman: {ImageTypeOCI},
	}

	if providerType == "" {
		return errs // Provider validation will catch this
	}

	validTypes, ok := compatible[providerType]
	if !ok {
		return errs // Unknown provider, will be caught elsewhere
	}

	if !slices.Contains(validTypes, imageType) {
		errs = append(errs, ValidationError{
			Field: "image.source",
			Message: fmt.Sprintf("image type '%s' is not compatible with provider '%s' (valid: %s)",
				imageType, providerType, strings.Join(validTypes, ", ")),
		})
	}

	return errs
}

// validateResources validates resource configuration
func (v *Validator) validateResources(r ResourceSpec) ValidationErrors {
	var errs ValidationErrors

	if r.CPU < 0 {
		errs = append(errs, ValidationError{Field: "resources.cpu", Message: "CPU count cannot be negative"})
	}
	if r.CPU > 256 {
		errs = append(errs, ValidationError{Field: "resources.cpu", Message: "CPU count exceeds maximum (256)"})
	}

	if r.Memory != "" {
		mb, err := ParseMemoryToMB(r.Memory)
		if err != nil {
			errs = append(errs, ValidationError{Field: "resources.memory", Message: err.Error()})
		} else if mb < 64 {
			errs = append(errs, ValidationError{Field: "resources.memory", Message: "memory must be at least 64Mi"})
		}
	}

	return errs
}

// validateNetwork validates network configuration
func (v *Validator) validateNetwork(n NetworkSpec, prefix string) ValidationErrors {
	var errs ValidationErrors

	if n.Name == "" {
		errs = append(errs, ValidationError{Field: prefix + ".name", Message: "network name is required"})
	}

	validTypes := []string{NetworkTypeBridge, NetworkTypeNAT, NetworkTypeMacvlan, NetworkTypeVXLAN, NetworkTypeNone}
	if n.Type != "" && !slices.Contains(validTypes, n.Type) {
		errs = append(errs, ValidationError{
			Field:   prefix + ".type",
			Message: fmt.Sprintf("invalid network type: %s (valid: %s)", n.Type, strings.Join(validTypes, ", ")),
		})
	}

	// Validate IP configuration
	if n.IP != nil {
		validModes := []string{IPModeDHCP, IPModeStatic, IPModeNone}
		if n.IP.Mode != "" && !slices.Contains(validModes, n.IP.Mode) {
			errs = append(errs, ValidationError{
				Field:   prefix + ".ip.mode",
				Message: fmt.Sprintf("invalid IP mode: %s (valid: %s)", n.IP.Mode, strings.Join(validModes, ", ")),
			})
		}

		if n.IP.Mode == IPModeStatic {
			if n.IP.Address == "" {
				errs = append(errs, ValidationError{Field: prefix + ".ip.address", Message: "static IP requires address"})
			} else if _, _, err := net.ParseCIDR(n.IP.Address); err != nil {
				errs = append(errs, ValidationError{Field: prefix + ".ip.address", Message: "invalid CIDR format"})
			}
		}

		// Validate DNS servers
		for i, dns := range n.IP.DNS {
			if net.ParseIP(dns) == nil {
				errs = append(errs, ValidationError{
					Field:   fmt.Sprintf("%s.ip.dns[%d]", prefix, i),
					Message: fmt.Sprintf("invalid DNS IP: %s", dns),
				})
			}
		}
	}

	// Validate port forwarding
	for i, port := range n.Ports {
		portPrefix := fmt.Sprintf("%s.ports[%d]", prefix, i)
		if port.Host <= 0 || port.Host > 65535 {
			errs = append(errs, ValidationError{Field: portPrefix + ".host", Message: "host port must be 1-65535"})
		}
		if port.Container <= 0 || port.Container > 65535 {
			errs = append(errs, ValidationError{Field: portPrefix + ".container", Message: "container port must be 1-65535"})
		}
		if port.Protocol != "" && port.Protocol != "tcp" && port.Protocol != "udp" {
			errs = append(errs, ValidationError{Field: portPrefix + ".protocol", Message: "protocol must be tcp or udp"})
		}
	}

	return errs
}

// validateStorage validates storage configuration
func (v *Validator) validateStorage(s StorageSpec) ValidationErrors {
	var errs ValidationErrors

	if s.RootDisk != nil {
		if s.RootDisk.Size != "" {
			_, err := ParseMemorySize(s.RootDisk.Size)
			if err != nil {
				errs = append(errs, ValidationError{Field: "storage.root_disk.size", Message: err.Error()})
			}
		}

		validTypes := []string{DiskTypeAuto, DiskTypeZVOL, DiskTypeQCOW2, DiskTypeRaw, DiskTypePhysical}
		if s.RootDisk.Type != "" && !slices.Contains(validTypes, s.RootDisk.Type) {
			errs = append(errs, ValidationError{
				Field:   "storage.root_disk.type",
				Message: fmt.Sprintf("invalid disk type: %s (valid: %s)", s.RootDisk.Type, strings.Join(validTypes, ", ")),
			})
		}
	}

	for i, vol := range s.Volumes {
		prefix := fmt.Sprintf("storage.volumes[%d]", i)

		if vol.Name == "" {
			errs = append(errs, ValidationError{Field: prefix + ".name", Message: "volume name is required"})
		}

		if vol.MountPath == "" {
			errs = append(errs, ValidationError{Field: prefix + ".mount_path", Message: "mount path is required"})
		} else if !strings.HasPrefix(vol.MountPath, "/") {
			errs = append(errs, ValidationError{Field: prefix + ".mount_path", Message: "mount path must be absolute"})
		}

		// Size is required unless it's a host mount
		if vol.Size == "" && vol.HostPath == "" {
			errs = append(errs, ValidationError{Field: prefix + ".size", Message: "size is required for non-host volumes"})
		}

		if vol.Size != "" {
			_, err := ParseMemorySize(vol.Size)
			if err != nil {
				errs = append(errs, ValidationError{Field: prefix + ".size", Message: err.Error()})
			}
		}

		if vol.ZFS != nil {
			validCompressions := []string{"lz4", "gzip", "zstd", "lzjb", "off", ""}
			if vol.ZFS.Compression != "" && !slices.Contains(validCompressions, vol.ZFS.Compression) {
				errs = append(errs, ValidationError{
					Field:   prefix + ".zfs.compression",
					Message: fmt.Sprintf("invalid compression: %s", vol.ZFS.Compression),
				})
			}
		}
	}

	return errs
}

// validateLifecycle validates lifecycle configuration
// validateLifecycle checks a lifecycle block.
//
// prefix names the block being validated — "" for a workload, "instances[2]"
// for a stack instance — so the operator is told which instance a bad duration
// belongs to. Without it every error read "lifecycle.health_check.interval",
// whichever of ten instances it came from.
func (v *Validator) validateLifecycle(prefix string, l LifecycleSpec) ValidationErrors {
	var errs ValidationErrors

	if l.Autostart != nil {
		if l.Autostart.Priority < 0 || l.Autostart.Priority > 100 {
			errs = append(errs, ValidationError{Field: field(prefix, "lifecycle.autostart.priority"), Message: "priority must be 0-100"})
		}

		if l.Autostart.Delay != "" {
			_, err := ParseDuration(l.Autostart.Delay)
			if err != nil {
				errs = append(errs, ValidationError{Field: field(prefix, "lifecycle.autostart.delay"), Message: err.Error()})
			}
		}
	}

	// The other durations too. healthSpecToConfig replaces an unparseable one
	// with its default and restartSpecToPolicy keeps its own, so a typo in
	// "interval" or "reset_after" produced an instance checked or restarted on
	// a schedule the manifest never asked for, with nothing said about it.
	if l.HealthCheck != nil {
		// A slice, not a map: validation errors are reported to the operator in
		// the order they are collected, and map iteration would shuffle them
		// between two runs on the same manifest.
		for _, d := range []struct{ name, value string }{
			{field(prefix, "lifecycle.health_check.interval"), l.HealthCheck.Interval},
			{field(prefix, "lifecycle.health_check.timeout"), l.HealthCheck.Timeout},
			{field(prefix, "lifecycle.health_check.start_period"), l.HealthCheck.StartPeriod},
		} {
			if d.value == "" {
				continue
			}
			if _, err := ParseDuration(d.value); err != nil {
				errs = append(errs, ValidationError{Field: d.name, Message: err.Error()})
			}
		}
		if l.HealthCheck.Retries < 0 {
			errs = append(errs, ValidationError{
				Field:   field(prefix, "lifecycle.health_check.retries"),
				Message: "retries cannot be negative",
			})
		}
	}

	if l.Restart != nil {
		for _, d := range []struct{ name, value string }{
			{field(prefix, "lifecycle.restart.delay"), l.Restart.Delay},
			{field(prefix, "lifecycle.restart.reset_after"), l.Restart.ResetAfter},
		} {
			if d.value == "" {
				continue
			}
			if _, err := ParseDuration(d.value); err != nil {
				errs = append(errs, ValidationError{Field: d.name, Message: err.Error()})
			}
		}
		if l.Restart.MaxRestarts < 0 {
			errs = append(errs, ValidationError{
				Field:   field(prefix, "lifecycle.restart.max_restarts"),
				Message: "max_restarts cannot be negative",
			})
		}
	}

	if l.Hooks != nil {
		errs = append(errs, v.validateHooks(l.Hooks.PreCreate, "lifecycle.hooks.pre_create")...)
		errs = append(errs, v.validateHooks(l.Hooks.PostCreate, "lifecycle.hooks.post_create")...)
	}

	return errs
}

// validateHooks checks the structured hooks that run commands in an instance.
//
// A hook is skipped when its type is not "exec", and apply reports the run as
// completed anyway:
//
//	Skipping hook 1: unsupported type ''
//	Post-create hooks completed successfully
//
// Nothing looked at hooks before this, so a manifest that forgot the type
// validated cleanly, applied cleanly, and provisioned nothing. Say it here,
// where the reader is asking whether the file is right.
func (v *Validator) validateHooks(hooks []HookSpec, prefix string) ValidationErrors {
	var errs ValidationErrors

	for i, hook := range hooks {
		field := fmt.Sprintf("%s[%d]", prefix, i)

		switch hook.Type {
		case "exec":
		case "":
			errs = append(errs, ValidationError{
				Field:   field + ".type",
				Message: `hook type is required; the only type that runs is "exec"`,
			})
		default:
			errs = append(errs, ValidationError{
				Field:   field + ".type",
				Message: fmt.Sprintf("unknown hook type %q; the only type that runs is \"exec\"", hook.Type),
			})
		}

		if len(hook.Commands) == 0 {
			errs = append(errs, ValidationError{
				Field:   field + ".commands",
				Message: "hook has no commands to run",
			})
		}

		if hook.OnFailure != "" && hook.OnFailure != "continue" && hook.OnFailure != "stop" {
			errs = append(errs, ValidationError{
				Field:   field + ".on_failure",
				Message: fmt.Sprintf("on_failure must be \"continue\" or \"stop\", not %q", hook.OnFailure),
			})
		}
	}

	return errs
}

// validateCloudInit validates cloud-init configuration
func (v *Validator) validateCloudInit(c *CloudInitSpec, providerType, prefix string) ValidationErrors {
	var errs ValidationErrors

	// Cloud-init is only valid for bhyve and qemu providers
	if providerType != "" && providerType != ProviderTypeBhyve && providerType != ProviderTypeQEMU {
		errs = append(errs, ValidationError{
			Field:   prefix,
			Message: fmt.Sprintf("cloud-init is only supported for bhyve and qemu providers, not %s", providerType),
		})
		return errs // No point validating further if provider doesn't support it
	}

	// If not enabled (explicitly false or unset), skip further validation
	if c.Enabled == nil || !*c.Enabled {
		return errs
	}

	// Validate users
	for i := range c.Users {
		user := c.Users[i]
		userPrefix := fmt.Sprintf("%s.users[%d]", prefix, i)

		if user.Name == "" {
			errs = append(errs, ValidationError{Field: userPrefix + ".name", Message: "user name is required"})
		} else if !isValidUsername(user.Name) {
			errs = append(errs, ValidationError{Field: userPrefix + ".name", Message: "invalid username format"})
		}
	}

	// user_data and user_data_file are mutually exclusive
	if c.UserData != "" && c.UserDataFile != "" {
		errs = append(errs, ValidationError{
			Field:   prefix,
			Message: "user_data and user_data_file are mutually exclusive",
		})
	}

	return errs
}

// isValidUsername checks if username follows Unix conventions
func isValidUsername(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	// Must start with lowercase letter or underscore, can contain lowercase, digits, underscore, hyphen
	re := regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)
	return re.MatchString(name)
}

// isValidProvider checks if provider type is supported
func (v *Validator) isValidProvider(provider string) bool {
	return slices.Contains(v.SupportedProviders, provider)
}

// isValidName checks if name follows naming conventions
func isValidName(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	// Must start with letter, can contain letters, numbers, hyphens
	// Cannot end with hyphen
	re := regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$|^[a-z]$`)
	return re.MatchString(strings.ToLower(name))
}

// field joins a validation prefix and a field name.
func field(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}
