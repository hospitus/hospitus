package jail

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Lifecycle hooks allow custom scripts to run at various stages of jail lifecycle:
//   - pre-create, post-create: Before/after jail creation
//   - pre-start, post-start: Before/after jail starts
//   - pre-stop, post-stop: Before/after jail stops
//   - pre-destroy, post-destroy: Before/after jail deletion
//
// Hooks can be defined:
//   - Globally: Apply to all jails (in /etc/hospitus/hooks/ or /usr/local/etc/hospitus/hooks/)
//   - Per-jail: Apply to specific jail (in jail config or jail directory)
//
// Similar to CBSD's master_prestart, master_poststart hooks.
//
// Security:
//   - Hook scripts must reside under the allowed hook directories; both
//     config-declared and directory-discovered hooks are checked with
//     validateHookPath (absolute path, no traversal, symlink target confined).
//   - Config-based hooks are restricted to script paths only (no arbitrary shell commands).
//   - Hook scripts are trusted, root-managed code: their *contents* are executed
//     as-is and are NOT scanned for dangerous patterns. Confinement to the
//     allowed directories is the security boundary, not content inspection.

// HookType represents the type of lifecycle hook
type HookType string

const (
	HookPreCreate   HookType = "pre-create"
	HookPostCreate  HookType = "post-create"
	HookPreStart    HookType = "pre-start"
	HookPostStart   HookType = "post-start"
	HookPreStop     HookType = "pre-stop"
	HookPostStop    HookType = "post-stop"
	HookPreDestroy  HookType = "pre-destroy"
	HookPostDestroy HookType = "post-destroy"
)

// HookConfig represents a hook configuration
type HookConfig struct {
	// Type is the hook type (pre-start, post-start, etc.)
	Type HookType `json:"type"`

	// Command is the script path to execute.
	// Must be an absolute path to a script within an allowed hooks directory.
	// Arbitrary shell commands are NOT permitted for security reasons.
	Command string `json:"command"`

	// Timeout is the maximum time to wait for hook execution (default: 60s)
	Timeout time.Duration `json:"timeout,omitempty"`

	// FailOnError determines if jail operation should fail if hook fails
	// Default: true for pre-* hooks, false for post-* hooks
	FailOnError *bool `json:"fail_on_error,omitempty"`

	// Enabled allows disabling a hook without removing it
	Enabled *bool `json:"enabled,omitempty"`
}

// HookResult represents the result of hook execution
type HookResult struct {
	Type     HookType      `json:"type"`
	Command  string        `json:"command"`
	ExitCode int           `json:"exit_code"`
	Output   string        `json:"output"`
	Duration time.Duration `json:"duration"`
	Error    error         `json:"error,omitempty"`
}

// HookEnv contains environment variables passed to hooks
type HookEnv struct {
	JailName   string
	JailPath   string
	JailIP     string
	JailBridge string
	HookType   HookType
	ZFSDataset string
}

// getGlobalHooksDir returns the directory for global hooks
func (p *JailProvider) getGlobalHooksDir() string {
	// Check /usr/local/etc/hospitus/hooks first (FreeBSD standard)
	if dir := "/usr/local/etc/hospitus/hooks"; dirExists(dir) {
		return dir
	}
	// Fall back to /etc/hospitus/hooks
	return "/etc/hospitus/hooks"
}

// getJailHooksDir returns the directory for per-jail hooks
func (p *JailProvider) getJailHooksDir(jailName string) string {
	return filepath.Join(p.stateDir, "hooks", jailName)
}

// dirExists checks if a directory exists
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ExecuteHooks executes all hooks of a given type for a jail
func (p *JailProvider) ExecuteHooks(ctx context.Context, hookType HookType, env HookEnv) ([]HookResult, error) {
	var results []HookResult
	var firstError error

	// If the jail is running, try to get its real IP (for DHCP cases)
	if running, err := p.isJailRunning(ctx, env.JailName); err == nil && running {
		if ips, err := p.getJailIPs(ctx, env.JailName); err == nil && len(ips) > 0 {
			env.JailIP = ips[0].String()
		}
	}

	// Execute global hooks first
	globalResults, err := p.executeHooksFromDir(ctx, hookType, p.getGlobalHooksDir(), env)
	results = append(results, globalResults...)
	if err != nil && firstError == nil {
		firstError = err
	}

	// Execute per-jail hooks
	jailHooksDir := p.getJailHooksDir(env.JailName)
	jailResults, err := p.executeHooksFromDir(ctx, hookType, jailHooksDir, env)
	results = append(results, jailResults...)
	if err != nil && firstError == nil {
		firstError = err
	}

	// Execute hooks from jail config
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", env.JailName))
	if jailConfig, err := p.loadJailConfig(configPath); err == nil {
		configResults, err := p.executeConfigHooks(ctx, hookType, jailConfig, env)
		results = append(results, configResults...)
		if err != nil && firstError == nil {
			firstError = err
		}
	}

	return results, firstError
}

// executeHooksFromDir executes hooks from a directory
func (p *JailProvider) executeHooksFromDir(ctx context.Context, hookType HookType, dir string, env HookEnv) ([]HookResult, error) {
	var results []HookResult

	// Check if directory exists
	if !dirExists(dir) {
		return results, nil
	}

	// Look for hook scripts: <hookType>.sh or <hookType>/*.sh
	hookScript := filepath.Join(dir, string(hookType)+".sh")
	hookSubdir := filepath.Join(dir, string(hookType))

	// Execute main hook script if exists. Route it through validateHookPath so
	// a symlink planted in the hooks directory cannot point execution (and the
	// subsequent chmod) at a file outside the allowed hook directories.
	if fileExists(hookScript) {
		result := p.runDiscoveredHook(ctx, hookType, hookScript, env)
		results = append(results, result)
		if result.Error != nil {
			return results, result.Error
		}
	}

	// Execute all scripts in hook subdirectory
	if dirExists(hookSubdir) {
		entries, err := os.ReadDir(hookSubdir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				if !strings.HasSuffix(entry.Name(), ".sh") {
					continue
				}
				scriptPath := filepath.Join(hookSubdir, entry.Name())
				result := p.runDiscoveredHook(ctx, hookType, scriptPath, env)
				results = append(results, result)
				if result.Error != nil {
					return results, result.Error
				}
			}
		}
	}

	return results, nil
}

// runDiscoveredHook validates a hook script path discovered on disk before
// executing it, so directory-sourced hooks get the same symlink/allowed-dir
// checks as config-declared hooks.
func (p *JailProvider) runDiscoveredHook(ctx context.Context, hookType HookType, scriptPath string, env HookEnv) HookResult {
	validatedPath, err := p.validateHookPath(scriptPath)
	if err != nil {
		return HookResult{
			Type:    hookType,
			Command: scriptPath,
			Error:   fmt.Errorf("hook script rejected: %w", err),
		}
	}
	return p.executeHook(ctx, validatedPath, env, 60*time.Second, true)
}

// executeConfigHooks executes hooks defined in jail config
func (p *JailProvider) executeConfigHooks(ctx context.Context, hookType HookType, config *JailConfig, env HookEnv) ([]HookResult, error) {
	var results []HookResult

	// Check for hooks in config
	if config.Hooks == nil {
		return results, nil
	}

	hooks, ok := config.Hooks[string(hookType)]
	if !ok {
		// Fallback to underscore variant for backward compatibility.
		// Manifest convert.go stores keys with underscores (post_start, pre_start, etc.)
		// but HookType constants use hyphens (post-start, pre-start, etc.).
		// post_create is intentionally excluded — it is handled by CLI apply inside the instance.
		if hookType != HookPostCreate {
			underscoreKey := strings.ReplaceAll(string(hookType), "-", "_")
			hooks, ok = config.Hooks[underscoreKey]
		}
	}
	if !ok {
		return results, nil
	}

	// Execute each configured hook
	for _, hookCmd := range hooks {
		if hookCmd == "" {
			continue
		}

		// Determine if this should fail on error
		failOnError := strings.HasPrefix(string(hookType), "pre-")

		timeout := 60 * time.Second
		// Config-based hooks must be validated file paths, not arbitrary shell commands.
		validatedPath, err := p.validateHookPath(hookCmd)
		if err != nil {
			result := HookResult{
				Type:    hookType,
				Command: hookCmd,
				Error:   fmt.Errorf("config hook rejected (must be an absolute path within hooks dir): %w", err),
			}
			results = append(results, result)
			if failOnError {
				return results, result.Error
			}
			continue
		}
		result := p.executeHook(ctx, validatedPath, env, timeout, failOnError)
		results = append(results, result)
		if result.Error != nil && failOnError {
			return results, result.Error
		}
	}

	return results, nil
}

// executeHook executes a hook script
func (p *JailProvider) executeHook(ctx context.Context, scriptPath string, env HookEnv, timeout time.Duration, failOnError bool) HookResult {
	start := time.Now()

	// Make script executable (owner-only: not world-readable). This is
	// best-effort: if it fails and the script is not already executable the
	// execution below surfaces the error, but we log rather than silently drop
	// the chmod failure so an operator can diagnose permission problems.
	if err := os.Chmod(scriptPath, 0o700); err != nil {
		p.logWarn(ctx, "failed to set hook script permissions", "script", scriptPath, logging.FieldError, err)
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build command
	cmd := exec.CommandContext(ctx, scriptPath)
	cmd.Env = p.buildHookEnv(env)

	// Capture output
	output, err := cmd.CombinedOutput()

	result := HookResult{
		Type:     env.HookType,
		Command:  scriptPath,
		Output:   string(output),
		Duration: time.Since(start),
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
		}
		if failOnError {
			result.Error = fmt.Errorf("hook %s failed: %w (output: %s)", scriptPath, err, string(output))
		}
	}

	return result
}

// validateHookPath validates that a hook command is a safe script path
// within an allowed hooks directory. It rejects:
//   - Arbitrary shell commands (containing shell metacharacters)
//   - Path traversal attempts (../)
//   - Scripts outside allowed hook directories
//   - Symlinks pointing outside allowed directories
func (p *JailProvider) validateHookPath(cmd string) (string, error) {
	// Reject obvious shell commands — must look like a file path
	if strings.ContainsAny(cmd, "|&;`$(){}<>!\\\"'") {
		return "", fmt.Errorf("hook contains shell metacharacters; only script paths are allowed")
	}
	if strings.HasPrefix(cmd, "-") {
		return "", fmt.Errorf("hook must be an absolute path, not a flag")
	}

	// Resolve to absolute path
	if !filepath.IsAbs(cmd) {
		return "", fmt.Errorf("hook must be an absolute path (got: %s)", cmd)
	}

	// Clean the path to remove any ../ sequences
	cleaned := filepath.Clean(cmd)

	// Build list of allowed hook directories
	baseHooksDir := filepath.Join(p.stateDir, "hooks")
	allowedDirs := []string{
		p.getGlobalHooksDir(),
		p.getJailHooksDir(""), // base hooks dir — will be checked with jail name below
		baseHooksDir,          // allow any per-jail hooks subdirectory
	}

	// Check if the script is under an allowed directory. Both sides are
	// symlink-resolved so a hooks directory reached through a link still matches.
	isAllowed, err := validation.PathWithinAny(cleaned, allowedDirs...)
	if err != nil {
		return "", fmt.Errorf("cannot verify hook script location %s: %w", cleaned, err)
	}
	if !isAllowed {
		return "", fmt.Errorf("hook script must be under an allowed hooks directory (got: %s)", cleaned)
	}

	// Verify the file exists and is a regular file (not a directory)
	info, statErr := os.Lstat(cleaned)
	if statErr != nil {
		return "", fmt.Errorf("hook script not found: %s", cleaned)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Resolve symlink and verify target is still within allowed dirs
		target, err := filepath.EvalSymlinks(cleaned)
		if err != nil {
			return "", fmt.Errorf("hook symlink target cannot be resolved: %s", cleaned)
		}
		// SECURITY: Stat the resolved target to verify it's a regular file.
		// Using info from the original Lstat (which is the symlink itself)
		// creates a TOCTOU race — we must verify the resolved target directly.
		targetInfo, err := os.Stat(target)
		if err != nil {
			return "", fmt.Errorf("hook symlink target not accessible: %s → %s", cleaned, target)
		}
		if !targetInfo.Mode().IsRegular() {
			return "", fmt.Errorf("hook symlink target is not a regular file: %s → %s", cleaned, target)
		}
		// Re-check the resolved target against the allowed dirs, resolving both
		// sides so the check is not defeated by a symlinked state directory.
		targetAllowed, checkErr := validation.PathWithinAny(target, allowedDirs...)
		if checkErr != nil {
			return "", fmt.Errorf("cannot verify hook symlink target %s: %w", target, checkErr)
		}
		if !targetAllowed {
			return "", fmt.Errorf("hook symlink target is outside allowed directories: %s → %s", cleaned, target)
		}
		cleaned = target
	} else if !info.Mode().IsRegular() {
		// Not a symlink, so Lstat info is correct — must be a regular file
		return "", fmt.Errorf("hook must be a regular file, not a directory or special file")
	}

	return cleaned, nil
}

// buildHookEnv builds a minimal, safe environment for hook execution.
// Only a controlled set of variables is passed to prevent leaking
// sensitive process environment (API keys, tokens, etc.) into hooks.
func (p *JailProvider) buildHookEnv(env HookEnv) []string {
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		fmt.Sprintf("HOSPITUS_JAIL_NAME=%s", env.JailName),
		fmt.Sprintf("HOSPITUS_JAIL_PATH=%s", env.JailPath),
		fmt.Sprintf("HOSPITUS_JAIL_IP=%s", env.JailIP),
		fmt.Sprintf("HOSPITUS_JAIL_BRIDGE=%s", env.JailBridge),
		fmt.Sprintf("HOSPITUS_HOOK_TYPE=%s", env.HookType),
		fmt.Sprintf("HOSPITUS_ZFS_DATASET=%s", env.ZFSDataset),
	}
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// CreateHookScript creates a hook script for a jail.
//
// SECURITY: the hook type is validated and the destination is confined to the
// jail's hooks directory (traversal via jailName is rejected). The script
// content itself is written verbatim and is NOT scanned for dangerous
// patterns — hooks are trusted, root-managed code.
func (p *JailProvider) CreateHookScript(jailName string, hookType HookType, content string) error {
	// Validate hook type
	switch hookType {
	case HookPreCreate, HookPostCreate, HookPreStart, HookPostStart,
		HookPreStop, HookPostStop, HookPreDestroy, HookPostDestroy:
		// Valid
	default:
		return fmt.Errorf("invalid hook type: %s", hookType)
	}

	hookDir := p.getJailHooksDir(jailName)
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		return fmt.Errorf("failed to create hooks directory: %w", err)
	}

	hookPath := filepath.Join(hookDir, string(hookType)+".sh")

	// SECURITY: Verify resolved path is still within the hooks directory
	// (prevents directory traversal via jailName). Both sides are symlink-resolved.
	baseHooksDir := filepath.Join(p.stateDir, "hooks")
	within, err := validation.PathWithin(hookDir, baseHooksDir)
	if err != nil {
		return fmt.Errorf("cannot verify hook directory %s: %w", hookDir, err)
	}
	if !within {
		return fmt.Errorf("hook directory is outside allowed path: %s", hookDir)
	}

	// Add shebang if not present
	if !strings.HasPrefix(content, "#!") {
		content = "#!/usr/bin/env bash\n" + content
	}

	if err := os.WriteFile(hookPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("failed to write hook script: %w", err)
	}
	// Restore the execute bit; the file was written 0o600 to satisfy G306.
	if err := os.Chmod(hookPath, 0o700); err != nil {
		return fmt.Errorf("failed to set hook script permissions: %w", err)
	}

	return nil
}

// DeleteHookScript deletes a hook script for a jail
func (p *JailProvider) DeleteHookScript(jailName string, hookType HookType) error {
	hookDir := p.getJailHooksDir(jailName)
	hookPath := filepath.Join(hookDir, string(hookType)+".sh")

	if err := os.Remove(hookPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete hook script: %w", err)
	}

	return nil
}

// ListHookScripts lists all hook scripts for a jail
func (p *JailProvider) ListHookScripts(jailName string) (map[HookType]string, error) {
	hooks := make(map[HookType]string)

	hookDir := p.getJailHooksDir(jailName)
	if !dirExists(hookDir) {
		return hooks, nil
	}

	entries, err := os.ReadDir(hookDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read hooks directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sh") {
			continue
		}

		hookName := strings.TrimSuffix(entry.Name(), ".sh")
		hookType := HookType(hookName)

		// Validate hook type
		switch hookType {
		case HookPreCreate, HookPostCreate, HookPreStart, HookPostStart,
			HookPreStop, HookPostStop, HookPreDestroy, HookPostDestroy:
			// Valid hook type
			hookPath := filepath.Join(hookDir, entry.Name())
			content, err := os.ReadFile(hookPath)
			if err == nil {
				hooks[hookType] = string(content)
			}
		}
	}

	return hooks, nil
}

// GetHookEnvForJail builds hook environment from jail config
func (p *JailProvider) GetHookEnvForJail(jailName string, hookType HookType) (HookEnv, error) {
	env := HookEnv{
		JailName: jailName,
		HookType: hookType,
	}

	// Load jail config if available
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	if config, err := p.loadJailConfig(configPath); err == nil {
		env.JailPath = config.Path
		env.ZFSDataset = fmt.Sprintf("%s/%s", p.zfsParent, jailName)

		// Get network info
		if len(config.Networks) > 0 {
			env.JailIP = config.Networks[0].IPv4
			env.JailBridge = config.Networks[0].Bridge
		}
	}

	return env, nil
}
