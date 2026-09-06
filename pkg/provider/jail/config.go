package jail

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// JailConfig represents jail configuration (exported for hooks.go)
type JailConfig struct {
	Name           string                   `json:"name"`
	Path           string                   `json:"path"`
	Spec           provider.InstanceSpec    `json:"spec"`
	Networks       []provider.NetworkSpec   `json:"networks"`
	Parameters     map[string]interface{}   `json:"parameters"`      // Free-form jail(8) parameters, applied at start
	JailParameters JailParameters           `json:"jail_parameters"` // The structured subset the allow-list recognizes
	Resources      provider.ResourceSpec    `json:"resources"`
	AutoStart      provider.AutoStartConfig `json:"autostart,omitempty"`
	RuntimeType    string                   `json:"runtime_type,omitempty"` // freebsd, linux, crossarch
	VnetEpair      string                   `json:"vnet_epair,omitempty"`   // First host-side epair, kept for single-NIC jails
	VnetEpairs     []string                 `json:"vnet_epairs,omitempty"`  // All host-side epair interfaces
	Hooks          map[string][]string      `json:"hooks,omitempty"`        // Lifecycle hooks: hook_type -> commands
}

// jailConfig is the short name this package uses for its own configuration.
type jailConfig = JailConfig

// jailParametersFrom picks the jail(8) parameters out of a provider config.
//
// A manifest sets these under provider_overrides.jail.parameters, and the
// converter flattens them into ProviderConfig alongside everything else the
// provider is told. The allow-list is the filter: ProviderConfig also carries
// settings that are not jail parameters at all — os_type, command, bootloader —
// and passing those to jail(8) would be an error at best.

// applyHooksKey folds the "hooks" provider-config key into the jail config.
//
// It used to sit in an "if key == \"hooks\"" block just above a switch on the
// same variable, so the key dispatch appeared twice.
func (p *JailProvider) applyHooksKey(config *jailConfig, spec provider.InstanceSpec, value interface{}) {
	p.logDebug(context.Background(), "Found hooks in ProviderConfig", "jail", spec.Name)
	if hooksMap, ok := value.(map[string]interface{}); ok {
		if config.Hooks == nil {
			config.Hooks = make(map[string][]string)
		}
		for hookType, hookVal := range hooksMap {
			p.logDebug(context.Background(), "Processing hook type", "type", hookType)
			switch hv := hookVal.(type) {
			case string:
				config.Hooks[hookType] = []string{hv}
			case []string:
				config.Hooks[hookType] = hv
			case []interface{}:
				// Handle structured hooks (PostCreate)
				var commands []string
				for _, item := range hv {
					switch v := item.(type) {
					case map[string]interface{}:
						if cmds, ok := v["commands"].([]interface{}); ok {
							for _, c := range cmds {
								if s, ok := c.(string); ok {
									commands = append(commands, s)
								}
							}
						} else if cmd, ok := v["command"].(string); ok {
							commands = append(commands, cmd)
						}
					case string:
						commands = append(commands, v)
					}
				}
				p.logDebug(context.Background(), "Extracted commands for hook", "type", hookType, "count", len(commands))
				config.Hooks[hookType] = commands
			default:
				p.logDebug(context.Background(), "Unknown hook value type", "type", fmt.Sprintf("%T", hookVal))
			}
		}
	} else {
		p.logDebug(context.Background(), "hooks is not a map[string]interface{}", "type", fmt.Sprintf("%T", value))
	}
}

func jailParametersFrom(providerConfig map[string]interface{}) map[string]interface{} {
	params := make(map[string]interface{})
	for key, value := range providerConfig {
		if validation.ValidateJailParameter(key) == nil {
			params[key] = value
		}
	}
	return params
}

// buildJailConfig builds jail configuration from spec
func (p *JailProvider) buildJailConfig(spec provider.InstanceSpec, mountpoint string) *jailConfig {
	config := &jailConfig{
		Name:           spec.Name,
		Path:           mountpoint,
		Spec:           spec,
		Networks:       spec.Networks,
		Parameters:     jailParametersFrom(spec.ProviderConfig),
		JailParameters: DefaultJailParameters(),
		Resources: provider.ResourceSpec{
			CPUs:     spec.CPUs,
			MemoryMB: spec.MemoryMB,
		},
		AutoStart: provider.AutoStartConfig{
			Enabled:  false,
			Priority: 50, // Default priority
			DelayMS:  0,
		},
		RuntimeType: determineRuntimeType(spec),
	}

	// Tell the jail what it actually is. Without this it inherits the host
	// kernel's version, so a 14.3 userland on a 15.1 host reports 15.1 and pkg
	// refuses the repository that matches its base system.
	if release, reldate := freebsdUserlandVersion(spec.Image, spec.OSVersion); release != "" {
		config.JailParameters.Osrelease = release
		config.JailParameters.Osreldate = reldate
	}

	// Add provider-specific parameters
	if spec.ProviderConfig != nil {
		// Parse JailParameters from ProviderConfig
		jailParams, err := ParseJailParametersFromMap(spec.ProviderConfig)
		if err != nil {
			p.logWarn(context.Background(), "invalid jail parameter, using defaults", "error", err)
			jailParams = DefaultJailParameters()
		}
		config.JailParameters = jailParams

		// Parsing provider config replaces the whole parameter set, so the
		// userland version has to be reapplied — unless the caller named one,
		// which is theirs to decide.
		if jailParams.Osrelease == "" {
			if release, reldate := freebsdUserlandVersion(spec.Image, spec.OSVersion); release != "" {
				config.JailParameters.Osrelease = release
				config.JailParameters.Osreldate = reldate
			}
		}

		for key, value := range spec.ProviderConfig {
			// Handle hooks

			// Handle autostart configuration
			switch key {
			case "hooks":
				p.applyHooksKey(config, spec, value)
			case "autostart":
				if enabled, ok := value.(bool); ok {
					config.AutoStart.Enabled = enabled
				}
			case "autostart_priority":
				switch priority := value.(type) {
				case int:
					config.AutoStart.Priority = priority
				case float64:
					config.AutoStart.Priority = int(priority)
				}
			case "autostart_delay":
				switch delay := value.(type) {
				case int:
					config.AutoStart.DelayMS = delay
				case float64:
					config.AutoStart.DelayMS = int(delay)
				}
			}
		}

		// VNET jails use their own network stack; jail(8) rejects ip4/ip6
		// restrictions when vnet is present ("vnet jails cannot have IP
		// address restrictions"). Clear them so ToJailArgs() skips them.
		if vnet, ok := spec.ProviderConfig["vnet"]; ok {
			if enabled, isBool := vnet.(bool); isBool && enabled {
				config.JailParameters.IP4 = ""
				config.JailParameters.IP6 = ""
			}
		}
	}

	return config
}

func determineRuntimeType(spec provider.InstanceSpec) string {
	if strings.EqualFold(spec.OSType, "linux") {
		return "linux"
	}

	// The image name settles it when the spec does not. A jail built from one
	// of the catalog's Linux rootfs images without --os-type would otherwise
	// run as freebsd, and start runs /bin/sh /etc/rc inside a tree that has no
	// such file:
	//
	//	/bin/sh: can't open '/etc/rc': No such file or directory
	//
	// The jail then exists but can never start. Architecture is read back out
	// of the image name below for the same kind of reason.
	if detectImageOS(spec.Image) == "linux" {
		return "linux"
	}

	targetArch := normalizeArch(spec.Arch)
	if targetArch != "" && isCrossArch(targetArch) {
		return "crossarch"
	}

	if detectedArch := normalizeArch(detectImageArch(spec.Image)); detectedArch != "" && isCrossArch(detectedArch) {
		return "crossarch"
	}

	return "freebsd"
}

// saveJailConfig saves jail configuration to file
func (p *JailProvider) saveJailConfig(config *jailConfig, path string) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	// Atomic: writing over the live path leaves truncated JSON if the write is
	// interrupted or the filesystem fills, and loadJailConfig then fails for
	// that jail — which is every operation on it.
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// loadJailConfig loads jail configuration from file
func (p *JailProvider) loadJailConfig(path string) (*jailConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config jailConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	if config.RuntimeType == "" {
		config.RuntimeType = determineRuntimeType(config.Spec)
	}

	return &config, nil
}

// jailExists checks if a jail configuration exists
// It verifies both the config file AND the ZFS dataset to detect ghost instances
func (p *JailProvider) jailExists(ctx context.Context, name string) (bool, error) {
	// The name becomes both a path under stateDir and a dataset argument to
	// zfs(8). A ".." would read a file outside the state directory, and an
	// option-shaped name would be read as a flag.
	if err := validation.ValidateInstanceName(name); err != nil {
		return false, fmt.Errorf("invalid jail name %q: %w", name, err)
	}
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", name))
	_, err := os.Stat(configPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	// Config file exists - verify the ZFS dataset does too. A config without a
	// dataset is a ghost instance and its config is removed.
	//
	// Only zfs(8) saying the dataset does not exist counts as a ghost: any other
	// failure (zfs unavailable, permission denied, a busy pool) says nothing
	// about the dataset, and deleting a healthy jail's metadata over it is not
	// recoverable.
	zfsDataset := fmt.Sprintf("%s/%s", p.zfsParent, name)
	if output, err := p.cmd().CombinedOutput(ctx, "zfs", "list", "-H", zfsDataset); err != nil {
		if !strings.Contains(string(output), "does not exist") {
			return false, fmt.Errorf("failed to check ZFS dataset %s: %w (output: %s)",
				zfsDataset, err, strings.TrimSpace(string(output)))
		}

		p.logWarn(ctx, "cleaning up ghost jail config because ZFS dataset is missing", "jail", name, "config_path", configPath, "zfs_dataset", zfsDataset)
		if rmErr := os.Remove(configPath); rmErr != nil {
			p.logWarn(ctx, "failed to remove ghost jail config", "jail", name, "config_path", configPath, logging.FieldError, rmErr)
		}
		return false, nil
	}

	return true, nil
}

// getDefaultFreeBSDImage detects the current FreeBSD version and returns the image identifier
func (p *JailProvider) getDefaultFreeBSDImage(ctx context.Context) (string, error) {
	// Read FreeBSD version from /bin/freebsd-version
	output, err := p.cmd().Output(ctx, "freebsd-version", "-u")
	if err != nil {
		return "", fmt.Errorf("failed to detect FreeBSD version: %w", err)
	}

	version := strings.TrimSpace(string(output))
	// version will be something like "15.0-STABLE" or "14.1-RELEASE-p3"

	// Get architecture
	arch := "amd64" // Default to amd64
	if archOutput, err := p.cmd().Output(ctx, "uname", "-m"); err == nil {
		arch = strings.TrimSpace(string(archOutput))
	}

	// Construct image identifier: VERSION-ARCH
	image := fmt.Sprintf("%s-%s", version, arch)

	return image, nil
}

// ensureJailDirectories creates necessary directories in the jail filesystem
func (p *JailProvider) ensureJailDirectories(jailPath string) error {
	// Create directories that are required by jail system
	requiredDirs := []string{
		"dev",     // Required for mount.devfs
		"tmp",     // Temporary files
		"var/run", // Runtime state files
	}

	for _, dir := range requiredDirs {
		dirPath := filepath.Join(jailPath, dir)
		// Check if path already exists (could be a symlink in Linux distros)
		if info, err := os.Lstat(dirPath); err == nil {
			// Path exists - if it's a symlink or directory, that's fine
			if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
				continue
			}
			// Otherwise it's a file, which is a problem
			return fmt.Errorf("path %s exists but is not a directory or symlink", dir)
		}
		if err := os.MkdirAll(dirPath, 0o755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	return nil
}

// isJailRunning checks if a jail is currently running
func (p *JailProvider) isJailRunning(ctx context.Context, name string) (bool, error) {
	if err := validation.ValidateInstanceName(name); err != nil {
		return false, fmt.Errorf("invalid jail name %q: %w", name, err)
	}
	err := p.cmd().Run(ctx, "jls", "-j", name, "-N")
	if err != nil {
		// Exit code non-zero means jail not running
		return false, nil
	}
	return true, nil
}

// getJailID returns the JID (jail ID) for a running jail
func (p *JailProvider) getJailID(ctx context.Context, name string) (int, error) {
	if err := validation.ValidateInstanceName(name); err != nil {
		return 0, fmt.Errorf("invalid jail name %q: %w", name, err)
	}
	output, err := p.cmd().Output(ctx, "jls", "-j", name, "-h", "jid")
	if err != nil {
		return 0, err
	}

	jid := 0
	_, err = fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &jid)
	if err != nil {
		return 0, err
	}

	return jid, nil
}

// getJailIPs returns the IP addresses assigned to a jail
func (p *JailProvider) getJailIPs(ctx context.Context, name string) ([]net.IP, error) {
	if err := validation.ValidateInstanceName(name); err != nil {
		return nil, fmt.Errorf("invalid jail name %q: %w", name, err)
	}
	output, err := p.cmd().Output(ctx, "jls", "-j", name, "-h", "ip4.addr")
	if err != nil {
		return nil, err
	}

	ipStr := strings.TrimSpace(string(output))
	if ipStr == "" || ipStr == "-" {
		return nil, nil
	}

	// IPs can be comma-separated
	ipStrs := strings.Split(ipStr, ",")
	ips := make([]net.IP, 0, len(ipStrs))
	for _, s := range ipStrs {
		if ip := net.ParseIP(strings.TrimSpace(s)); ip != nil {
			ips = append(ips, ip)
		}
	}
	return ips, nil
}
