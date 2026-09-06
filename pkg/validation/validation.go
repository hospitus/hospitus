// Package validation provides input validation utilities for HOSPITUS.
//
// This package implements security-focused validation to prevent:
//   - SQL injection
//   - Command injection
//   - Path traversal
//
// All user input should be validated before use in:
//   - Database queries
//   - File system operations
//   - Command execution
//   - API responses
package validation

import (
	"fmt"
	"net"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/hospitus/hospitus/pkg/provider"
)

// Instance name validation. Regexes are compiled once at package init so the
// hot validation paths do not recompile them on every call.
var (
	// ValidInstanceNameRegex matches valid instance names
	// Format: alphanumeric, dash, underscore (1-63 chars)
	// Must start with alphanumeric
	ValidInstanceNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

	// validSnapshotNameRegex matches snapshot names (alphanumeric, dash,
	// underscore, dot), starting with alphanumeric.
	validSnapshotNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

	// validProviderNameRegex matches provider names.
	validProviderNameRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

	// validLabelKeyRegex matches DNS-style label keys with an optional prefix.
	validLabelKeyRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?(/[a-z0-9]([-a-z0-9.]*[a-z0-9])?)*$`)

	// validUsernameRegex matches usernames: lowercase alphanumeric, dash and
	// underscore, starting with a letter or underscore, 1-32 characters.
	validUsernameRegex = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

	// validMACRegex matches a colon-separated MAC address.
	validMACRegex = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}([0-9A-Fa-f]{2})$`)

	// Dangerous characters for command injection
	commandInjectionChars = ";&|$`(){}[]<>\\\"'\n\r\t"
)

// ValidateInstanceName validates an instance name for security.
//
// Rules:
//   - 1-63 characters
//   - Alphanumeric, dash, underscore only
//   - Must start with alphanumeric
//   - No path traversal sequences
//   - No command injection characters
//
// This prevents:
//   - Command injection (CWE-78)
//   - Path traversal (CWE-22)
//   - SQL injection via name field (CWE-89)
func ValidateInstanceName(name string) error {
	if name == "" {
		return fmt.Errorf("instance name cannot be empty")
	}

	if len(name) > 63 {
		return fmt.Errorf("instance name too long (max 63 characters)")
	}

	// The regex already restricts names to [a-zA-Z0-9_-], which excludes every
	// path-traversal ('.', '/', '\\') and command-injection character, so no
	// further character checks are needed here.
	if !ValidInstanceNameRegex.MatchString(name) {
		return fmt.Errorf("invalid instance name: must be 1-63 alphanumeric characters, dash, or underscore, starting with alphanumeric")
	}

	return nil
}

// ValidateSnapshotName validates a snapshot name for security.
//
// Rules:
//   - 1-63 characters
//   - Alphanumeric, dash, underscore, dot only
//   - Must start with alphanumeric
//   - No path traversal sequences
//   - No command injection characters
//
// Snapshots can have dots for versioning (e.g., "backup.1", "v1.0.0")
// but we still prevent path traversal.
func ValidateSnapshotName(name string) error {
	if name == "" {
		return fmt.Errorf("snapshot name cannot be empty")
	}

	if len(name) > 63 {
		return fmt.Errorf("snapshot name too long (max 63 characters)")
	}

	// Must match pattern: alphanumeric, dash, underscore, dot
	// Must start with alphanumeric
	if !validSnapshotNameRegex.MatchString(name) {
		return fmt.Errorf("invalid snapshot name: must be alphanumeric with dash, underscore, or dot (1-63 chars)")
	}

	// Prevent path traversal
	if strings.Contains(name, "..") {
		return fmt.Errorf("invalid snapshot name: path traversal not allowed")
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") {
		return fmt.Errorf("invalid snapshot name: cannot start with path separator")
	}

	// Prevent command injection (except dot which is allowed for versioning)
	if strings.ContainsAny(name, ";&|$`(){}[]<>\\\"'\n\r\t") {
		return fmt.Errorf("invalid snapshot name: contains invalid characters")
	}

	return nil
}

// ValidateProviderName validates a provider name.
func ValidateProviderName(name string) error {
	if name == "" {
		return fmt.Errorf("provider name cannot be empty")
	}

	// Provider names should be simple identifiers
	if !validProviderNameRegex.MatchString(name) {
		return fmt.Errorf("invalid provider name: must be lowercase alphanumeric with dashes (max 32 chars)")
	}

	return nil
}

// ValidateResourceLimits validates CPU and memory limits.
func ValidateResourceLimits(cpus int, memoryMB int64) error {
	if cpus < 1 {
		return fmt.Errorf("CPUs must be at least 1")
	}
	if cpus > 1024 {
		return fmt.Errorf("CPUs cannot exceed 1024")
	}

	if memoryMB < 128 {
		return fmt.Errorf("memory must be at least 128 MB")
	}
	if memoryMB > 4*1024*1024 {
		return fmt.Errorf("memory cannot exceed 4 TB")
	}

	return nil
}

// ValidateLabel validates a label key or value.
//
// Labels are used in filtering and should be safe for SQL queries.
func ValidateLabel(key, value string) error {
	if key == "" {
		return fmt.Errorf("label key cannot be empty")
	}

	// Label keys: DNS-style names (allows alphanumeric, dashes, dots, and a single slash for prefix)
	if !validLabelKeyRegex.MatchString(key) {
		return fmt.Errorf("invalid label key format")
	}

	if len(key) > 253 {
		return fmt.Errorf("label key too long (max 253 characters)")
	}

	// Label values: more permissive but still safe
	if len(value) > 253 {
		return fmt.Errorf("label value too long (max 253 characters)")
	}

	// Ensure valid UTF-8
	if !utf8.ValidString(value) {
		return fmt.Errorf("label value must be valid UTF-8")
	}

	// Prevent SQL injection in label values
	if strings.ContainsAny(value, "'\"\\;") {
		return fmt.Errorf("label value contains invalid characters")
	}

	return nil
}

// ValidateFilePath validates a file path for safety.
//
// Prevents:
//   - Path traversal
//   - Absolute paths (when relative expected)
//   - Special characters that could cause issues
func ValidateFilePath(path string, allowAbsolute bool) error {
	if path == "" {
		return fmt.Errorf("path cannot be empty")
	}

	// Check for path traversal
	if strings.Contains(path, "..") {
		return fmt.Errorf("path cannot contain '..'")
	}

	// Check for absolute paths if not allowed
	if !allowAbsolute && (strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\")) {
		return fmt.Errorf("absolute paths not allowed")
	}

	// Prevent null bytes
	if strings.Contains(path, "\x00") {
		return fmt.Errorf("path cannot contain null bytes")
	}

	return nil
}

// ValidateIPAddress validates an IPv4 or IPv6 address.
//
// Uses Go's standard net.ParseIP for robust validation which handles:
//   - IPv4 addresses (e.g., "192.168.1.1")
//   - IPv6 addresses (e.g., "2001:db8::1", "::1", "fe80::1")
//   - IPv4-mapped IPv6 addresses (e.g., "::ffff:192.168.1.1")
func ValidateIPAddress(ip string) error {
	if ip == "" {
		return fmt.Errorf("IP address cannot be empty")
	}

	// Try CIDR first
	if _, _, err := net.ParseCIDR(ip); err == nil {
		return nil
	}

	// Use Go's net.ParseIP for robust IPv4/IPv6 validation
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid IP address format: %s", ip)
	}

	return nil
}

// ValidateJailParameter validates a jail parameter key.
//
// Only whitelisted parameters are allowed to prevent command injection.
func ValidateJailParameter(key string) error {
	allowedParams := map[string]bool{
		"host.hostname":      true,
		"path":               true,
		"ip4.addr":           true,
		"ip6.addr":           true,
		"allow.raw_sockets":  true,
		"mount.devfs":        true,
		"exec.start":         true,
		"exec.stop":          true,
		"persist":            true,
		"allow.chflags":      true,
		"allow.mount":        true,
		"allow.set_hostname": true,
		"allow.sysvipc":      true,
		// Per-jail System V IPC namespaces. allow.sysvipc grants the jail the
		// host's IPC objects, which for a database jail is the opposite of what
		// is wanted; these give it namespaces of its own ("new"), which is what
		// jail(8) recommends in its place.
		"sysvmsg":        true,
		"sysvsem":        true,
		"sysvshm":        true,
		"enforce_statfs": true,
		"mount.fstab":    true,
		// allow.mount.* are kernel parameters granting permission. There is no
		// matching mount.linprocfs: jail(8) performs mounts of its own for
		// devfs, fdescfs and procfs only, and refuses anything else outright —
		// "jail: unknown parameter: mount.linprocfs" stops the jail from
		// starting at all. A Linux jail gets its /proc from fstab instead.
		"allow.mount.linprocfs": true,
		"allow.mount.linsysfs":  true,
	}

	if !allowedParams[key] {
		return fmt.Errorf("jail parameter '%s' not allowed", key)
	}

	return nil
}

// ValidateJailParameterValue validates a jail parameter value.
func ValidateJailParameterValue(value string) error {
	// Prevent command injection. Use the shared character set so this matches
	// the other validators.
	if strings.ContainsAny(value, commandInjectionChars) {
		return fmt.Errorf("parameter value contains invalid characters")
	}

	return nil
}

// ValidateProviderConfigEntry validates a single provider configuration key-value pair.
// Keys must be valid identifiers; string values must not contain command injection or
// shell expansion characters. Non-string values (numbers, booleans) are acceptable.
func ValidateProviderConfigEntry(key string, value interface{}) error {
	if key == "" {
		return fmt.Errorf("provider config key cannot be empty")
	}

	if !utf8.ValidString(key) {
		return fmt.Errorf("provider config key must be valid UTF-8")
	}

	// Reject keys with shell metacharacters or path traversal
	if strings.ContainsAny(key, ";&|$`(){}[]<>\\\"'\n\r\t/") {
		return fmt.Errorf("provider config key contains invalid characters")
	}

	if len(key) > 128 {
		return fmt.Errorf("provider config key too long (max 128 characters)")
	}

	return validateProviderConfigValue(value, false)
}

// validateProviderConfigValue checks a provider config value and anything nested
// inside it.
//
// A top-level string is a jail(8) parameter and is held to that character set. A
// nested one is not: a mount path, a cloud-init command, a hook. Those carry
// pipes, quotes and redirections by design — cloud-init runs them inside the
// guest — and the checks that matter for them are at the point of use: the key
// allow-list for jail parameters, path confinement for mounts, and the admin
// permission for anything that reaches the host.
//
// What is rejected here is what no value can carry anywhere: a null byte, which
// ends a string early in execve(2), and text that is not valid UTF-8.
func validateProviderConfigValue(value interface{}, nested bool) error {
	switch v := value.(type) {
	case string:
		if !nested {
			return ValidateJailParameterValue(v)
		}
		if strings.ContainsRune(v, 0) {
			return fmt.Errorf("provider config value contains a null byte")
		}
		if !utf8.ValidString(v) {
			return fmt.Errorf("provider config value is not valid UTF-8")
		}
	case []interface{}:
		for _, item := range v {
			if err := validateProviderConfigValue(item, true); err != nil {
				return err
			}
		}
	case map[string]interface{}:
		for k, item := range v {
			if !utf8.ValidString(k) {
				return fmt.Errorf("provider config holds a key that is not valid UTF-8")
			}
			if err := validateProviderConfigValue(item, true); err != nil {
				return err
			}
		}
	default:
		// A decoder does not always hand back interface{} collections: a
		// manifest's mounts arrive as []map[string]interface{}, a list of
		// commands as []string. Those used to fall through untouched, so the
		// checks above never saw what was inside them.
		return validateNestedCollection(value)
	}
	return nil
}

// validateNestedCollection walks a typed slice, array or map so its elements
// face the same checks an interface{} collection does. Anything else — a
// number, a bool, a time — carries nothing to check.
func validateNestedCollection(value interface{}) error {
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			if err := validateProviderConfigValue(rv.Index(i).Interface(), true); err != nil {
				return err
			}
		}
	case reflect.Map:
		for _, k := range rv.MapKeys() {
			if k.Kind() == reflect.String && !utf8.ValidString(k.String()) {
				return fmt.Errorf("provider config holds a key that is not valid UTF-8")
			}
			if err := validateProviderConfigValue(rv.MapIndex(k).Interface(), true); err != nil {
				return err
			}
		}
	case reflect.Pointer, reflect.Interface:
		if !rv.IsNil() {
			return validateProviderConfigValue(rv.Elem().Interface(), true)
		}
	}
	return nil
}

// ValidEnvKeyRegex matches valid environment variable names: letters of either
// case, digits and underscore, not starting with a digit.
var ValidEnvKeyRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateEnvVar validates an environment variable name and value.
//
// Prevent environment variable injection
// This prevents attackers from injecting malicious environment variables
// that could affect command execution (PATH, LD_PRELOAD, etc.)
func ValidateEnvVar(key, value string) error {
	if key == "" {
		return fmt.Errorf("environment variable name cannot be empty")
	}

	// Validate key format (POSIX-compliant)
	if !ValidEnvKeyRegex.MatchString(key) {
		return fmt.Errorf("invalid environment variable name: %s (must be alphanumeric with underscores, not starting with digit)", key)
	}

	// Block potentially dangerous environment variables
	dangerousVars := map[string]bool{
		"PATH":                  true,
		"LD_PRELOAD":            true,
		"LD_LIBRARY_PATH":       true,
		"DYLD_INSERT_LIBRARIES": true,
		"DYLD_LIBRARY_PATH":     true,
		"IFS":                   true,
		"SHELL":                 true,
		"ENV":                   true,
		"BASH_ENV":              true,
	}
	if dangerousVars[strings.ToUpper(key)] {
		return fmt.Errorf("environment variable %s is not allowed for security reasons", key)
	}

	// Validate value - no null bytes or control characters
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("environment variable value cannot contain null bytes")
	}
	for _, c := range value {
		if c < 32 && c != '\t' && c != '\n' {
			return fmt.Errorf("environment variable value contains invalid control character")
		}
	}

	return nil
}

// ValidInterfaceNameRegex matches valid network interface names.
// FreeBSD: alphanumeric, max 15 chars typically
// A dash is part of the set: FreeBSD accepts one, and hospitus creates
// hospitus-nat itself. Rejecting it meant a manifest could not name the
// daemon's own NAT bridge, nor podman's cni-podman0, nor any bridge an
// operator had made with a dash in it.
var ValidInterfaceNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,14}$`)

// ValidateInterfaceName validates a network interface name.
//
// Prevent interface name injection
// Interface names are used in ifconfig and other system commands.
func ValidateInterfaceName(name string) error {
	if name == "" {
		return fmt.Errorf("interface name cannot be empty")
	}

	if len(name) > 15 {
		return fmt.Errorf("interface name too long (max 15 characters)")
	}

	// Must match valid interface name pattern
	if !ValidInterfaceNameRegex.MatchString(name) {
		return fmt.Errorf("invalid interface name: must be alphanumeric starting with letter (max 15 chars)")
	}

	// Prevent command injection characters
	if strings.ContainsAny(name, commandInjectionChars) {
		return fmt.Errorf("interface name contains invalid characters")
	}

	return nil
}

// ValidateBridgeName validates a bridge interface name.
// Bridge names typically follow the pattern: bridge0, hospitus0, etc.
func ValidateBridgeName(name string) error {
	if err := ValidateInterfaceName(name); err != nil {
		return fmt.Errorf("invalid bridge name: %w", err)
	}
	return nil
}

// IsShellInterpreter reports whether cmd is a shell interpreter that accepts
// a -c flag (sh, bash, dash, etc.).  We match on the base name so that
// /bin/sh, /usr/bin/sh, /usr/local/bin/bash all qualify.
func IsShellInterpreter(cmd string) bool {
	base := cmd
	if idx := strings.LastIndex(cmd, "/"); idx >= 0 {
		base = cmd[idx+1:]
	}
	switch base {
	case "sh", "bash", "dash", "zsh", "ksh", "csh", "tcsh":
		return true
	}
	return false
}

// ValidateExecOptions validates all fields in ExecOptions for security.
func ValidateExecOptions(opts provider.ExecOptions) error {
	if err := ValidateExecCommand(opts.Command); err != nil {
		return fmt.Errorf("invalid command: %w", err)
	}

	// Validate arguments.
	// Special case: /bin/sh -c <shellcode>
	// The shell-code argument is intentionally allowed to contain shell
	// metacharacters (|, >, &, etc.) — the shell itself interprets them.
	// We still reject null bytes and non-UTF-8 sequences.
	shellMode := IsShellInterpreter(opts.Command) &&
		len(opts.Args) >= 1 && opts.Args[0] == "-c"

	for i, arg := range opts.Args {
		if shellMode && i == 1 {
			// Shell-code argument: only reject control characters / null bytes.
			if !utf8.ValidString(arg) {
				return fmt.Errorf("invalid argument: shell command must be valid UTF-8")
			}
			if strings.ContainsAny(arg, "\x00\r") {
				return fmt.Errorf("invalid argument: shell command contains invalid control character")
			}
			continue
		}
		if err := ValidateExecArgument(arg); err != nil {
			return fmt.Errorf("invalid argument: %w", err)
		}
	}

	// Validate user
	if opts.User != "" {
		if err := ValidateUsername(opts.User); err != nil {
			return fmt.Errorf("invalid user: %w", err)
		}
	}

	// Validate working directory.
	//
	// Absolute paths are the point: the directory is resolved inside the
	// instance, where jexec(8) starts the process at the jail root and a
	// relative path has no useful base to count from. It is confined by that
	// root, and shell-quoted before it reaches a "cd".
	if opts.WorkingDir != "" {
		if err := ValidateFilePath(opts.WorkingDir, true); err != nil {
			return fmt.Errorf("invalid working directory: %w", err)
		}
	}

	// Validate environment variables
	for k, v := range opts.Env {
		if err := ValidateEnvVar(k, v); err != nil {
			return fmt.Errorf("invalid environment variable: %w", err)
		}
	}

	return nil
}

// ValidateExecCommand validates a command string for security.
func ValidateExecCommand(cmd string) error {
	if cmd == "" {
		return fmt.Errorf("command cannot be empty")
	}

	// Prevent command injection
	if strings.ContainsAny(cmd, ";&|$`(){}[]<>\\\"'\n\r\t") {
		return fmt.Errorf("command contains invalid characters")
	}

	// Prevent path traversal
	if strings.Contains(cmd, "..") {
		return fmt.Errorf("command cannot contain path traversal sequences")
	}

	// Ensure valid UTF-8
	if !utf8.ValidString(cmd) {
		return fmt.Errorf("command must be valid UTF-8")
	}

	return nil
}

// ValidateExecArgument validates a single argument of a command to run inside
// an instance.
//
// Shell metacharacters are deliberately allowed, and a blacklist must not come
// back: an argument never reaches a shell as text. The jail provider hands the
// argv array straight to jexec(8) through exec.Command, and the bhyve provider
// shell-quotes every argument before it goes on an ssh command line. Refusing
// ";" and "$" protects nothing and makes ordinary arguments impossible to pass:
// sed 's/listen 80;/listen 8080;/', or any awk program mentioning $1.
//
// ".." is allowed for the same reason: the argument is consumed by a program
// inside the instance, which the jail root confines. Traversal is checked where
// hospitus resolves a path on the host.
//
// What remains is what an argument genuinely cannot carry: a null byte ends it
// early in execve(2), and neither it nor a carriage return survives a round
// trip through an ssh command line intact.
func ValidateExecArgument(arg string) error {
	if strings.ContainsAny(arg, "\x00\r") {
		return fmt.Errorf("argument contains an invalid control character")
	}

	if !utf8.ValidString(arg) {
		return fmt.Errorf("argument must be valid UTF-8")
	}

	return nil
}

// ValidateUsername validates a username for security.
func ValidateUsername(username string) error {
	if username == "" {
		return fmt.Errorf("username cannot be empty")
	}

	// Username validation: lowercase alphanumeric, dash and underscore, must
	// start with a letter or underscore, 1-32 characters. The regex already
	// bounds the length to 32, so no separate length check is needed.
	if !validUsernameRegex.MatchString(username) {
		return fmt.Errorf("invalid username format")
	}

	return nil
}

// validateSafeField checks that a free-form string field is valid UTF-8 and
// free of command-injection characters. name is used only in error messages.
// Empty values are considered valid (the field is optional).
func validateSafeField(name, value string) error {
	if value == "" {
		return nil
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	if strings.ContainsAny(value, commandInjectionChars) {
		return fmt.Errorf("%s contains invalid characters", name)
	}
	return nil
}

// ValidateInstanceSpec validates an instance specification for security.
func ValidateInstanceSpec(spec provider.InstanceSpec) error {
	if err := ValidateInstanceName(spec.Name); err != nil {
		return fmt.Errorf("invalid instance name: %w", err)
	}

	// Validate description
	if spec.Description != "" {
		if !utf8.ValidString(spec.Description) {
			return fmt.Errorf("description must be valid UTF-8")
		}
		if len(spec.Description) > 1000 {
			return fmt.Errorf("description too long (max 1000 characters)")
		}
	}

	// Validate image name
	if spec.Image != "" {
		if !utf8.ValidString(spec.Image) {
			return fmt.Errorf("image name must be valid UTF-8")
		}
		// Prevent path traversal in image names
		if strings.Contains(spec.Image, "..") {
			return fmt.Errorf("image name contains invalid characters")
		}
	}

	// Validate free-form command-injection-sensitive fields.
	if err := validateSafeField("OS type", spec.OSType); err != nil {
		return err
	}
	if err := validateSafeField("OS version", spec.OSVersion); err != nil {
		return err
	}
	if err := validateSafeField("architecture", spec.Arch); err != nil {
		return err
	}
	if err := validateSafeField("bootloader", spec.Bootloader); err != nil {
		return err
	}

	// Validate cloud-init configuration
	if spec.CloudInit != nil {
		if spec.CloudInit.UserData != "" {
			if !utf8.ValidString(spec.CloudInit.UserData) {
				return fmt.Errorf("cloud-init user data must be valid UTF-8")
			}
		}
		if spec.CloudInit.MetaData != "" {
			if !utf8.ValidString(spec.CloudInit.MetaData) {
				return fmt.Errorf("cloud-init meta data must be valid UTF-8")
			}
		}
		if spec.CloudInit.Network != "" {
			if !utf8.ValidString(spec.CloudInit.Network) {
				return fmt.Errorf("cloud-init network data must be valid UTF-8")
			}
		}
	}

	// Validate provider-specific configuration using the shared, stricter
	// entry validator instead of a weaker inline reimplementation.
	for k, v := range spec.ProviderConfig {
		if err := ValidateProviderConfigEntry(k, v); err != nil {
			return fmt.Errorf("invalid provider config: %w", err)
		}
	}

	// Validate labels
	for k, v := range spec.Labels {
		if err := ValidateLabel(k, v); err != nil {
			return fmt.Errorf("invalid label: %w", err)
		}
	}

	// Validate annotations
	for k, v := range spec.Annotations {
		if !utf8.ValidString(k) {
			return fmt.Errorf("annotation key must be valid UTF-8")
		}
		if !utf8.ValidString(v) {
			return fmt.Errorf("annotation value must be valid UTF-8")
		}
		if len(k) > 253 {
			return fmt.Errorf("annotation key too long (max 253 characters)")
		}
		if len(v) > 1000 {
			return fmt.Errorf("annotation value too long (max 1000 characters)")
		}
	}

	// Validate disks
	for _, disk := range spec.Disks {
		if err := ValidateDiskSpec(disk); err != nil {
			return fmt.Errorf("invalid disk spec: %w", err)
		}
	}

	// Validate networks
	for i := range spec.Networks {
		network := spec.Networks[i]
		if err := ValidateNetworkSpec(network); err != nil {
			return fmt.Errorf("invalid network spec: %w", err)
		}
	}

	return nil
}

// ValidateDiskSpec validates a disk specification for security.
func ValidateDiskSpec(disk provider.DiskSpec) error {
	if disk.ID != "" {
		if !utf8.ValidString(disk.ID) {
			return fmt.Errorf("disk ID must be valid UTF-8")
		}
		// Prevent command injection in disk ID
		if strings.ContainsAny(disk.ID, ";&|$`(){}[]<>\\\"'\n\r\t") {
			return fmt.Errorf("disk ID contains invalid characters")
		}
	}

	if disk.Path != "" {
		if !utf8.ValidString(disk.Path) {
			return fmt.Errorf("disk path must be valid UTF-8")
		}
		// Prevent path traversal in disk path
		if strings.Contains(disk.Path, "..") {
			return fmt.Errorf("disk path contains path traversal sequences")
		}
	}

	if disk.DeviceName != "" {
		if !utf8.ValidString(disk.DeviceName) {
			return fmt.Errorf("device name must be valid UTF-8")
		}
		// Prevent command injection in device name
		if strings.ContainsAny(disk.DeviceName, ";&|$`(){}[]<>\\\"'\n\r\t") {
			return fmt.Errorf("device name contains invalid characters")
		}
	}

	return nil
}

// requirePrefixLength rejects an interface address written without one.
//
// ifconfig applies the classful mask when none is given, so "10.0.0.10"
// configures a /8: the instance treats all of 10.0.0.0/8 as on-link and cannot
// reach the rest of it through its gateway. Nothing reports that — the address
// is right and the mask is silently wrong.
func requirePrefixLength(addr string) error {
	if !strings.Contains(addr, "/") {
		return fmt.Errorf("%s has no prefix length; write it as %s/24 or whatever the network uses", addr, addr)
	}
	return nil
}

// ValidateNetworkSpec validates a network specification for security.
func ValidateNetworkSpec(network provider.NetworkSpec) error {
	if network.ID != "" {
		if !utf8.ValidString(network.ID) {
			return fmt.Errorf("network ID must be valid UTF-8")
		}
		// Prevent command injection in network ID
		if strings.ContainsAny(network.ID, ";&|$`(){}[]<>\\\"'\n\r\t") {
			return fmt.Errorf("network ID contains invalid characters")
		}
	}

	if network.Bridge != "" {
		if err := ValidateBridgeName(network.Bridge); err != nil {
			return fmt.Errorf("invalid bridge name: %w", err)
		}
	}

	if network.MAC != "" {
		if !utf8.ValidString(network.MAC) {
			return fmt.Errorf("MAC address must be valid UTF-8")
		}
		// Basic MAC address format validation
		if !validMACRegex.MatchString(network.MAC) {
			return fmt.Errorf("invalid MAC address format")
		}
	}

	if network.IPv4 != "" && network.IPv4 != "dhcp" {
		if err := requireFamily(network.IPv4, 4); err != nil {
			return err
		}
		if err := ValidateIPAddress(network.IPv4); err != nil {
			return fmt.Errorf("invalid IPv4 address: %w", err)
		}
		if err := requirePrefixLength(network.IPv4); err != nil {
			return fmt.Errorf("invalid IPv4 address: %w", err)
		}
	}

	if network.IPv6 != "" {
		if err := requireFamily(network.IPv6, 6); err != nil {
			return err
		}
		if err := ValidateIPAddress(network.IPv6); err != nil {
			return fmt.Errorf("invalid IPv6 address: %w", err)
		}
		if err := requirePrefixLength(network.IPv6); err != nil {
			return fmt.Errorf("invalid IPv6 address: %w", err)
		}
	}

	// BridgeFlags become bare tokens in a root `ifconfig <bridge> <flag> <if>`
	// command, so restrict them to a known-safe allow-list.
	for _, f := range network.BridgeFlags {
		if err := ValidateBridgeFlag(f); err != nil {
			return err
		}
	}

	if network.VLAN < 0 || network.VLAN > 4094 {
		return fmt.Errorf("invalid VLAN id %d (must be 0-4094)", network.VLAN)
	}

	return nil
}

// allowedBridgePortFlags is the set of bridge member flags a caller may set.
var allowedBridgePortFlags = map[string]bool{
	"private": true, "-private": true,
	"span": true, "-span": true,
	"sticky": true, "-sticky": true,
	"learn": true, "-learn": true,
	"discover": true, "-discover": true,
}

// ValidateBridgeFlag reports whether a bridge port flag is on the allow-list.
func ValidateBridgeFlag(flag string) error {
	if !allowedBridgePortFlags[flag] {
		return fmt.Errorf("bridge flag %q not allowed", flag)
	}
	return nil
}

// SSH destination validation
var (
	// sshHostRegex validates SSH host: optional user@ prefix, then hostname/IP.
	// Allows: host, user@host, user@host.example.com, root@192.168.1.1
	//
	// Both halves must start with an alphanumeric. The host reaches ssh(1) as a
	// bare argument, so a leading dash would make "-oProxyCommand=..." parse as
	// an option rather than a destination.
	sshHostRegex = regexp.MustCompile(`^([a-zA-Z0-9][a-zA-Z0-9._-]*@)?[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

	// zfsDatasetRegex validates ZFS dataset paths.
	// Allows: pool, pool/dataset, pool/parent/child
	zfsDatasetRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_./-]*$`)
)

// ValidateSSHDestination parses and validates an SSH destination string
// in the format "user@host:pool/dataset" or "host:pool/dataset".
// Returns the SSH host and ZFS dataset path, or an error if the format
// is invalid or contains shell-unsafe characters.
func ValidateSSHDestination(dest string) (host, path string, err error) {
	parts := strings.SplitN(dest, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("destination must be in format [user@]host:pool/dataset, got %q", dest)
	}

	host = parts[0]
	path = parts[1]

	if !sshHostRegex.MatchString(host) {
		return "", "", fmt.Errorf("invalid SSH host %q: must match [user@]hostname", host)
	}

	if !zfsDatasetRegex.MatchString(path) {
		return "", "", fmt.Errorf("invalid ZFS dataset path %q: must match pool/dataset", path)
	}

	return host, path, nil
}

// requireFamily refuses an address in the wrong field.
//
// ValidateIPAddress accepts either family, so an IPv6 address in the IPv4 field
// passed validation and reached "ifconfig ... inet", which fails with a message
// that says nothing about where the address came from.
func requireFamily(addr string, family int) error {
	ip := net.ParseIP(addr)
	if ip == nil {
		if parsed, _, err := net.ParseCIDR(addr); err == nil {
			ip = parsed
		}
	}
	if ip == nil {
		return nil // shape is ValidateIPAddress's business, not this one's
	}
	isV4 := ip.To4() != nil
	switch {
	case family == 4 && !isV4:
		return fmt.Errorf("%s is an IPv6 address in an IPv4 field", addr)
	case family == 6 && isV4:
		return fmt.Errorf("%s is an IPv4 address in an IPv6 field", addr)
	}
	return nil
}
