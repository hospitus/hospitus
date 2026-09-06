package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Parser handles parsing of TOML manifest files
type Parser struct {
	secrets SecretStore
}

// NewParser creates a new manifest parser
func NewParser(secrets SecretStore) *Parser {
	return &Parser{
		secrets: secrets,
	}
}

// ParseFile is a convenience function that creates a parser and parses a file
func ParseFile(path string) (*ParsedManifest, error) {
	// Use a no-op secret store to avoid disk writes during pre-render parsing
	secrets := NewNoopSecretStore()
	return NewParser(secrets).ParseFile(path, nil)
}

// ParseString is a convenience function that parses manifest content from a string
func ParseString(content string) (*ParsedManifest, error) {
	// Use a no-op secret store to avoid disk writes during pre-render parsing
	secrets := NewNoopSecretStore()
	return NewParser(secrets).Parse([]byte(content), "<string>", nil)
}

// ParseFile parses a TOML manifest file
func (p *Parser) ParseFile(path string, vars map[string]any) (*ParsedManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file: %w", err)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	return p.Parse(data, absPath, vars)
}

// Parse parses TOML manifest data
func (p *Parser) Parse(data []byte, sourcePath string, vars map[string]any) (*ParsedManifest, error) {
	// 1. Detect manifest type first (also enforces the workload/stack exclusivity
	// rule) using a light textual scan to avoid parsing unrendered templates.
	manifestType, err := p.detectManifestType(data)
	if err != nil {
		return nil, err
	}

	// 2. Resolve the final secret scope BEFORE any real render. If the name is
	// templated, render only to discover it using a no-op store, so no real
	// secrets are ever generated under a pre-render (wrong) scope name.
	scope, _ := p.detectName(data, manifestType)
	if strings.Contains(scope, "{{") {
		preview, perr := RenderTemplate(data, vars, NewNoopSecretStore(), scope)
		if perr != nil {
			return nil, fmt.Errorf("failed to resolve manifest name: %w", perr)
		}
		if resolved, _ := p.detectName(preview, manifestType); resolved != "" {
			scope = resolved
		}
	}

	// 3. Render template once with the resolved scope.
	rendered, err := RenderTemplate(data, vars, p.secrets, scope)
	if err != nil {
		return nil, fmt.Errorf("failed to render manifest: %w", err)
	}

	result := &ParsedManifest{
		FilePath: sourcePath,
		ParsedAt: time.Now(),
	}

	switch manifestType {
	case "workload":
		workload, err := p.parseWorkload(rendered)
		if err != nil {
			return nil, err
		}
		result.Workload = workload
		result.APIVersion = workload.Workload.APIVersion

	case "stack":
		stack, err := p.parseStack(rendered)
		if err != nil {
			return nil, err
		}
		result.Stack = stack
		result.APIVersion = stack.Stack.APIVersion
	}

	return result, nil
}

// detectManifestType determines if this is a workload or stack manifest
func (p *Parser) detectManifestType(data []byte) (string, error) {
	content := string(data)

	// Look for [workload] section
	hasWorkload := strings.Contains(content, "[workload]")
	// Look for [stack] section
	hasStack := strings.Contains(content, "[stack]")

	if hasWorkload && hasStack {
		return "", fmt.Errorf("manifest cannot contain both [workload] and [stack] sections")
	}

	if hasWorkload {
		return "workload", nil
	}

	if hasStack {
		return "stack", nil
	}

	return "", fmt.Errorf("manifest must contain either [workload] or [stack] section")
}

// detectName extracts the name from the workload or stack section using regex
// because full TOML parse might fail if templates are not yet rendered.
func (p *Parser) detectName(data []byte, manifestType string) (string, error) {
	content := string(data)
	var re *regexp.Regexp

	if manifestType == "workload" {
		// Look for name = "..." within [workload] section
		re = regexp.MustCompile(`\[workload\][\s\S]*?name\s*=\s*"([^"]+)"`)
	} else {
		// Look for name = "..." within [stack] section
		re = regexp.MustCompile(`\[stack\][\s\S]*?name\s*=\s*"([^"]+)"`)
	}

	matches := re.FindStringSubmatch(content)
	if len(matches) >= 2 {
		return matches[1], nil
	}

	return "default", nil
}

// rejectUnknownKeys turns a key the schema does not define into an error.
//
// toml.Decode ignores what it cannot place, so "retires = 3" for "retries = 3"
// parses cleanly and the setting silently keeps its zero value. The manifest
// looks like it configured something it did not, and nothing says otherwise
// until the behavior is missing in production.
func rejectUnknownKeys(md toml.MetaData) error {
	undecoded := md.Undecoded()
	if len(undecoded) == 0 {
		return nil
	}

	keys := make([]string, 0, len(undecoded))
	for _, key := range undecoded {
		keys = append(keys, key.String())
	}
	sort.Strings(keys)

	if len(keys) == 1 {
		return fmt.Errorf("unknown field %q", keys[0])
	}
	return fmt.Errorf("unknown fields: %s", strings.Join(keys, ", "))
}

// parseWorkload parses a single workload manifest
func (p *Parser) parseWorkload(data []byte) (*WorkloadManifest, error) {
	var manifest WorkloadManifest

	md, err := toml.Decode(string(data), &manifest)
	if err != nil {
		return nil, fmt.Errorf("failed to parse workload manifest: %w", err)
	}
	if err := rejectUnknownKeys(md); err != nil {
		return nil, err
	}

	p.applyWorkloadDefaults(&manifest)

	return &manifest, nil
}

// parseStack parses a stack manifest
func (p *Parser) parseStack(data []byte) (*StackManifest, error) {
	var manifest StackManifest

	md, err := toml.Decode(string(data), &manifest)
	if err != nil {
		return nil, fmt.Errorf("failed to parse stack manifest: %w", err)
	}
	if err := rejectUnknownKeys(md); err != nil {
		return nil, err
	}

	// Apply defaults to each instance
	for i := range manifest.Instances {
		p.applyInstanceDefaults(&manifest.Instances[i])
	}

	return &manifest, nil
}

// applyWorkloadDefaults applies default values to a workload manifest
func (p *Parser) applyWorkloadDefaults(m *WorkloadManifest) {
	// Default API version
	if m.Workload.APIVersion == "" {
		m.Workload.APIVersion = APIVersion
	}

	// Default architecture
	if m.Image.Arch == "" {
		m.Image.Arch = "amd64"
	}

	// Default CPU and memory
	if m.Resources.CPU == 0 {
		m.Resources.CPU = 1
	}
	if m.Resources.Memory == "" {
		m.Resources.Memory = "512Mi"
	}

	// Default network type
	for i := range m.Networks {
		if m.Networks[i].Type == "" {
			m.Networks[i].Type = NetworkTypeBridge
		}
		if m.Networks[i].IP != nil && m.Networks[i].IP.Mode == "" {
			m.Networks[i].IP.Mode = IPModeDHCP
		}
		// Default port protocol
		for j := range m.Networks[i].Ports {
			if m.Networks[i].Ports[j].Protocol == "" {
				m.Networks[i].Ports[j].Protocol = "tcp"
			}
		}
	}

	// Default storage type
	if m.Storage.RootDisk != nil && m.Storage.RootDisk.Type == "" {
		m.Storage.RootDisk.Type = DiskTypeAuto
	}

	// Default autostart priority
	if m.Lifecycle.Autostart != nil && m.Lifecycle.Autostart.Priority == 0 {
		m.Lifecycle.Autostart.Priority = 50
	}

	// Default cloud-init: if section has content and 'enabled' was not set
	// explicitly, enable it. An explicit `enabled = false` is preserved.
	if m.CloudInit != nil {
		if m.CloudInit.Enabled == nil && !cloudInitSectionEmpty(m.CloudInit) {
			enabled := true
			m.CloudInit.Enabled = &enabled
		}

		// Default hostname to workload name
		if m.CloudInit.Hostname == "" {
			m.CloudInit.Hostname = m.Workload.Name
		}

		// Note: LockPasswd defaults to false (Go zero value), which means password login is allowed.
		// Users should explicitly set lock_passwd = true in their config for better security.
	}
}

// applyInstanceDefaults applies default values to a stack instance
func (p *Parser) applyInstanceDefaults(inst *InstanceConfig) {
	// Default architecture
	if inst.Image.Arch == "" {
		inst.Image.Arch = "amd64"
	}

	// Default CPU and memory
	if inst.Resources.CPU == 0 {
		inst.Resources.CPU = 1
	}
	if inst.Resources.Memory == "" {
		inst.Resources.Memory = "512Mi"
	}

	// Default network type
	for i := range inst.Networks {
		if inst.Networks[i].Type == "" {
			inst.Networks[i].Type = NetworkTypeBridge
		}
		if inst.Networks[i].IP != nil && inst.Networks[i].IP.Mode == "" {
			inst.Networks[i].IP.Mode = IPModeDHCP
		}
	}

	// Default depends_on condition
	if inst.DependsOn != nil && inst.DependsOn.Condition == "" {
		inst.DependsOn.Condition = DependsOnConditionStarted
	}

	// Default cloud-init: if section has content and 'enabled' was not set
	// explicitly, enable it. An explicit `enabled = false` is preserved.
	if inst.CloudInit != nil {
		if inst.CloudInit.Enabled == nil && !cloudInitSectionEmpty(inst.CloudInit) {
			enabled := true
			inst.CloudInit.Enabled = &enabled
		}

		// Default hostname to instance name
		if inst.CloudInit.Hostname == "" {
			inst.CloudInit.Hostname = inst.Name
		}
	}
}

// cloudInitSectionEmpty reports whether a cloud-init section carries no
// meaningful content, in which case it should not be auto-enabled.
func cloudInitSectionEmpty(c *CloudInitSpec) bool {
	return len(c.Users) == 0 && len(c.Packages) == 0 && len(c.RunCMD) == 0 &&
		len(c.SSHAuthorizedKeys) == 0 && c.UserData == "" && c.UserDataFile == ""
}

// ParseMemorySize parses a memory size string like "4Gi" or "512Mi" to bytes
func ParseMemorySize(size string) (int64, error) {
	if size == "" {
		return 0, fmt.Errorf("empty size string")
	}

	// Pattern: number followed by unit (Ki, Mi, Gi, Ti, K, M, G, T, or just bytes)
	re := regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([KMGT]i?)?[Bb]?$`)
	matches := re.FindStringSubmatch(strings.TrimSpace(size))

	if matches == nil {
		return 0, fmt.Errorf("invalid size format: %s", size)
	}

	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number in size: %s", size)
	}

	unit := strings.ToUpper(matches[2])

	var multiplier float64

	switch unit {
	case "":
		multiplier = 1
	case "K":
		multiplier = 1000
	case "KI":
		multiplier = 1024
	case "M":
		multiplier = 1000 * 1000
	case "MI":
		multiplier = 1024 * 1024
	case "G":
		multiplier = 1000 * 1000 * 1000
	case "GI":
		multiplier = 1024 * 1024 * 1024
	case "T":
		multiplier = 1000 * 1000 * 1000 * 1000
	case "TI":
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown unit: %s", unit)
	}

	return int64(value * multiplier), nil
}

// ParseMemoryToMB parses a memory size string to megabytes
func ParseMemoryToMB(size string) (int64, error) {
	bytes, err := ParseMemorySize(size)
	if err != nil {
		return 0, err
	}
	return bytes / (1024 * 1024), nil
}

// ParseDuration parses a duration string like "5s", "1m", "2h"
func ParseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	return time.ParseDuration(s)
}

// ParseImageSource parses an image source string and returns type and reference
// Format: "type:reference"
// Examples:
//   - "freebsd:14.3-RELEASE" → (freebsd, 14.3-RELEASE)
//   - "oci:nginx:latest" → (oci, nginx:latest)
//   - "cloud:ubuntu-24.04" → (cloud, ubuntu-24.04)
func ParseImageSource(source string) (imageType, reference string, err error) {
	if source == "" {
		return "", "", fmt.Errorf("empty image source")
	}
	if source == "none" {
		return "none", "none", nil // Boot from physical disk — no image
	}

	parts := strings.SplitN(source, ":", 2)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("invalid image source format: %s (expected type:reference)", source)
	}

	imageType = strings.ToLower(parts[0])
	reference = parts[1]

	// Validate image type
	switch imageType {
	case ImageTypeFreeBSD, ImageTypeOCI, ImageTypeCloud, ImageTypeISO:
		// Valid types
	default:
		return "", "", fmt.Errorf("unknown image type: %s (valid: freebsd, oci, cloud, iso)", imageType)
	}

	if reference == "" {
		return "", "", fmt.Errorf("empty image reference in: %s", source)
	}

	return imageType, reference, nil
}
