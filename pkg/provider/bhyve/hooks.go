package bhyve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
)

// Lifecycle hooks run custom scripts at each stage of a VM's life.
// Lifecycle hooks allow custom scripts to run at various stages of VM lifecycle:
//   - pre-create, post-create: Before/after VM creation
//   - pre-start, post-start: Before/after VM starts
//   - pre-stop, post-stop: Before/after VM stops
//   - pre-destroy, post-destroy: Before/after VM deletion
//
// Hooks can be defined:
//   - Globally: Apply to all VMs (in /usr/local/etc/hospitus/hooks/bhyve/)
//   - Per-VM: Apply to specific VM (in state dir hooks/<vmname>/)
//
// Security:
//   - Hook scripts must reside in restricted directories (global hooks dir or
//     the per-VM hooks directory under the state dir)
//   - Path traversal is blocked and the script path is rejected if it contains
//     shell metacharacters
//   - Hooks run with a minimal environment (see buildHookEnv), so the daemon's
//     secrets are never leaked to hook scripts

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

// HookEnv contains environment variables passed to hooks
type HookEnv struct {
	VMName   string
	VMDir    string
	VNCPort  int
	HookType HookType
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

// getGlobalHooksDir returns the directory for global bhyve hooks
func (p *BhyveProvider) getGlobalHooksDir() string {
	if dir := "/usr/local/etc/hospitus/hooks/bhyve"; dirExistsBhyve(dir) {
		return dir
	}
	return "/etc/hospitus/hooks/bhyve"
}

// getVMHooksDir returns the directory for per-VM hooks
func (p *BhyveProvider) getVMHooksDir(vmName string) string {
	return filepath.Join(p.stateDir, "hooks", vmName)
}

func dirExistsBhyve(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ExecuteHooks executes all hooks of a given type for a VM
func (p *BhyveProvider) ExecuteHooks(ctx context.Context, hookType HookType, env HookEnv) ([]HookResult, error) {
	var results []HookResult
	var firstError error
	env.HookType = hookType

	// Execute global hooks first
	globalResults, err := p.executeHooksFromDir(ctx, hookType, p.getGlobalHooksDir(), env)
	results = append(results, globalResults...)
	if err != nil && firstError == nil {
		firstError = err
	}

	// Execute per-VM hooks
	vmResults, err := p.executeHooksFromDir(ctx, hookType, p.getVMHooksDir(env.VMName), env)
	results = append(results, vmResults...)
	if err != nil && firstError == nil {
		firstError = err
	}

	return results, firstError
}

// executeHooksFromDir executes hooks from a directory
func (p *BhyveProvider) executeHooksFromDir(ctx context.Context, hookType HookType, dir string, env HookEnv) ([]HookResult, error) {
	var results []HookResult

	if !dirExistsBhyve(dir) {
		return results, nil
	}

	// Look for hook scripts: <hookType>.sh or <hookType>/*.sh
	hookScript := filepath.Join(dir, string(hookType)+".sh")
	hookSubdir := filepath.Join(dir, string(hookType))

	failOnError := strings.HasPrefix(string(hookType), "pre-")

	// Execute main hook script if exists
	if fileExistsBhyve(hookScript) {
		result := p.executeHook(ctx, hookScript, env, 60*time.Second, failOnError)
		results = append(results, result)
		if result.Error != nil {
			return results, result.Error
		}
	}

	// Execute all scripts in hook subdirectory (sorted by name)
	if dirExistsBhyve(hookSubdir) {
		entries, err := os.ReadDir(hookSubdir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sh") {
					continue
				}
				scriptPath := filepath.Join(hookSubdir, entry.Name())
				result := p.executeHook(ctx, scriptPath, env, 60*time.Second, failOnError)
				results = append(results, result)
				if result.Error != nil {
					return results, result.Error
				}
			}
		}
	}

	return results, nil
}

// executeHook executes a single hook script
func (p *BhyveProvider) executeHook(ctx context.Context, scriptPath string, env HookEnv, timeout time.Duration, failOnError bool) HookResult {
	start := time.Now()

	if err := p.validateHookPath(scriptPath); err != nil {
		return HookResult{
			Type:    env.HookType,
			Command: scriptPath,
			Error:   err,
		}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, scriptPath)
	cmd.Env = p.buildHookEnv(env)

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

// validateHookPath validates that a hook script is in an allowed directory
func (p *BhyveProvider) validateHookPath(scriptPath string) error {
	if strings.ContainsAny(scriptPath, "|&;`$(){}<>!\\\"'") {
		return fmt.Errorf("hook contains shell metacharacters")
	}

	cleaned := filepath.Clean(scriptPath)
	if !filepath.IsAbs(cleaned) {
		return fmt.Errorf("hook must be an absolute path")
	}

	allowedDirs := []string{
		p.getGlobalHooksDir(),
		filepath.Join(p.stateDir, "hooks"),
	}

	for _, dir := range allowedDirs {
		if dir != "" && strings.HasPrefix(cleaned, dir+string(filepath.Separator)) {
			return nil
		}
	}

	return fmt.Errorf("hook script must be under an allowed hooks directory (got: %s)", cleaned)
}

// buildHookEnv constructs a minimal, safe environment for hook scripts.
// It does NOT inherit the daemon's process environment, which would leak
// secrets (API keys, credentials) to hook scripts running as root.
func (p *BhyveProvider) buildHookEnv(env HookEnv) []string {
	return provider.MinimalEnv(
		fmt.Sprintf("HOSPITUS_VM_NAME=%s", env.VMName),
		fmt.Sprintf("HOSPITUS_VM_DIR=%s", env.VMDir),
		fmt.Sprintf("HOSPITUS_HOOK_TYPE=%s", env.HookType),
		"HOSPITUS_PROVIDER=bhyve",
		fmt.Sprintf("HOSPITUS_DATA_DIR=%s", p.dataDir),
		fmt.Sprintf("HOSPITUS_STATE_DIR=%s", p.stateDir),
		fmt.Sprintf("HOSPITUS_VNC_PORT=%d", env.VNCPort),
	)
}

func fileExistsBhyve(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
