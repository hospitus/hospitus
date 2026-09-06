package jail

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHookTypes(t *testing.T) {
	tests := []struct {
		hookType HookType
		expected string
	}{
		{HookPreCreate, "pre-create"},
		{HookPostCreate, "post-create"},
		{HookPreStart, "pre-start"},
		{HookPostStart, "post-start"},
		{HookPreStop, "pre-stop"},
		{HookPostStop, "post-stop"},
		{HookPreDestroy, "pre-destroy"},
		{HookPostDestroy, "post-destroy"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if string(tt.hookType) != tt.expected {
				t.Errorf("HookType = %s, expected %s", tt.hookType, tt.expected)
			}
		})
	}
}

func TestGetGlobalHooksDir(t *testing.T) {
	p := &JailProvider{}
	dir := p.getGlobalHooksDir()

	// Should return a valid path
	if dir == "" {
		t.Error("getGlobalHooksDir returned empty string")
	}

	// Should be an absolute path
	if !filepath.IsAbs(dir) {
		t.Errorf("getGlobalHooksDir returned relative path: %s", dir)
	}
}

func TestGetJailHooksDir(t *testing.T) {
	p := &JailProvider{stateDir: "/var/lib/hospitus/state"}
	dir := p.getJailHooksDir("testjail")

	expected := "/var/lib/hospitus/state/hooks/testjail"
	if dir != expected {
		t.Errorf("getJailHooksDir = %s, expected %s", dir, expected)
	}
}

func TestBuildHookEnv(t *testing.T) {
	p := &JailProvider{}
	env := HookEnv{
		JailName:   "testjail",
		JailPath:   "/zroot/hospitus/jails/testjail",
		JailIP:     "10.0.0.2/24",
		JailBridge: "hospitus0",
		HookType:   HookPreStart,
		ZFSDataset: "zroot/hospitus/jails/testjail",
	}

	envVars := p.buildHookEnv(env)

	// Check for HOSPITUS-specific variables
	expectedVars := map[string]string{
		"HOSPITUS_JAIL_NAME":   "testjail",
		"HOSPITUS_JAIL_PATH":   "/zroot/hospitus/jails/testjail",
		"HOSPITUS_JAIL_IP":     "10.0.0.2/24",
		"HOSPITUS_JAIL_BRIDGE": "hospitus0",
		"HOSPITUS_HOOK_TYPE":   "pre-start",
		"HOSPITUS_ZFS_DATASET": "zroot/hospitus/jails/testjail",
	}

	for key, expected := range expectedVars {
		found := false
		for _, v := range envVars {
			if v == key+"="+expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected environment variable %s=%s not found", key, expected)
		}
	}
}

func TestDirExists(t *testing.T) {
	// Test with existing directory
	if !dirExists("/tmp") {
		t.Error("dirExists returned false for /tmp")
	}

	// Test with non-existing directory
	if dirExists("/nonexistent/directory/12345") {
		t.Error("dirExists returned true for non-existent directory")
	}
}

func TestFileExists(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "hooktest")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	// Test with existing file
	if !fileExists(tmpFile.Name()) {
		t.Error("fileExists returned false for existing file")
	}

	// Test with non-existing file
	if fileExists("/nonexistent/file/12345") {
		t.Error("fileExists returned true for non-existent file")
	}

	// Test with directory (should return false)
	if fileExists("/tmp") {
		t.Error("fileExists returned true for directory")
	}
}

func TestCreateAndDeleteHookScript(t *testing.T) {
	// Create temp state directory
	tmpDir, err := os.MkdirTemp("", "hospitus-hooks-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	p := &JailProvider{stateDir: tmpDir}
	jailName := "testjail"
	hookType := HookPreStart
	content := "echo 'Hello from hook'"

	err = p.CreateHookScript(jailName, hookType, content)
	if err != nil {
		t.Fatalf("CreateHookScript failed: %v", err)
	}

	// Verify hook was created
	hookPath := filepath.Join(tmpDir, "hooks", jailName, "pre-start.sh")
	if !fileExists(hookPath) {
		t.Error("Hook script was not created")
	}

	// Verify content has shebang
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("Failed to read hook script: %v", err)
	}
	if string(data) != "#!/usr/bin/env bash\necho 'Hello from hook'" {
		t.Errorf("Hook content mismatch: %s", string(data))
	}

	hooks, err := p.ListHookScripts(jailName)
	if err != nil {
		t.Fatalf("ListHookScripts failed: %v", err)
	}
	if len(hooks) != 1 {
		t.Errorf("Expected 1 hook, got %d", len(hooks))
	}
	if _, ok := hooks[HookPreStart]; !ok {
		t.Error("pre-start hook not found in list")
	}

	err = p.DeleteHookScript(jailName, hookType)
	if err != nil {
		t.Fatalf("DeleteHookScript failed: %v", err)
	}

	// Verify hook was deleted
	if fileExists(hookPath) {
		t.Error("Hook script was not deleted")
	}
}

func TestHookConfig(t *testing.T) {
	config := HookConfig{
		Type:    HookPreStart,
		Command: "/usr/local/bin/pre-start.sh",
	}

	if config.Type != HookPreStart {
		t.Error("HookConfig Type mismatch")
	}
	if config.Command != "/usr/local/bin/pre-start.sh" {
		t.Error("HookConfig Command mismatch")
	}
}

func TestHookResult(t *testing.T) {
	result := HookResult{
		Type:     HookPreStart,
		Command:  "/usr/local/bin/pre-start.sh",
		ExitCode: 0,
		Output:   "Hook executed successfully",
	}

	if result.Type != HookPreStart {
		t.Error("HookResult Type mismatch")
	}
	if result.ExitCode != 0 {
		t.Error("HookResult ExitCode mismatch")
	}
}

func TestHookEnv(t *testing.T) {
	env := HookEnv{
		JailName:   "myjail",
		JailPath:   "/zroot/hospitus/jails/myjail",
		JailIP:     "10.0.0.5/24",
		JailBridge: "hospitus0",
		HookType:   HookPostCreate,
		ZFSDataset: "zroot/hospitus/jails/myjail",
	}

	if env.JailName != "myjail" {
		t.Error("HookEnv JailName mismatch")
	}
	if env.HookType != HookPostCreate {
		t.Error("HookEnv HookType mismatch")
	}
}

func TestValidateHookPath(t *testing.T) {
	// validateHookPath ensures hook scripts are safe paths within allowed
	// directories. It rejects shell metacharacters, path traversal, and
	// symlinks pointing outside allowed directories.

	// Setup allowed hooks directories
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a valid hook script
	validHook := filepath.Join(hooksDir, "valid-hook.sh")
	if err := os.WriteFile(validHook, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Create a directory outside the allowed area
	outsideDir := filepath.Join(tmpDir, "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outsideDir, "evil.sh")
	if err := os.WriteFile(outsideFile, []byte("#!/bin/sh\necho evil\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Create a symlink inside hooks dir pointing to outside file
	symlinkOutside := filepath.Join(hooksDir, "link-to-outside.sh")
	if err := os.Symlink(outsideFile, symlinkOutside); err != nil {
		t.Fatal(err)
	}

	// Create a symlink inside hooks dir pointing to another file in hooks dir
	symlinkInside := filepath.Join(hooksDir, "link-to-valid.sh")
	if err := os.Symlink(validHook, symlinkInside); err != nil {
		t.Fatal(err)
	}

	// Create a symlink to a directory (not a regular file)
	symlinkToDir := filepath.Join(hooksDir, "link-to-dir.sh")
	if err := os.Symlink(outsideDir, symlinkToDir); err != nil {
		t.Fatal(err)
	}

	p := &JailProvider{stateDir: tmpDir}

	tests := []struct {
		name    string
		cmd     string
		wantErr bool
	}{
		{
			name:    "valid regular file passes",
			cmd:     validHook,
			wantErr: false,
		},
		{
			name:    "symlink pointing outside rejected",
			cmd:     symlinkOutside,
			wantErr: true,
		},
		{
			name:    "symlink inside allowed dirs passes",
			cmd:     symlinkInside,
			wantErr: false,
		},
		{
			name:    "shell metacharacters rejected",
			cmd:     "/bin/sh -c 'echo hi'",
			wantErr: true,
		},
		{
			name:    "path with dotdot rejected",
			cmd:     hooksDir + "/../outside/evil.sh",
			wantErr: true,
		},
		{
			name:    "symlink to directory rejected",
			cmd:     symlinkToDir,
			wantErr: true,
		},
		{
			name:    "non-absolute path rejected",
			cmd:     "hooks/valid-hook.sh",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := p.validateHookPath(tt.cmd)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateHookPath(%q) error = %v, wantErr %v", tt.cmd, err, tt.wantErr)
			}
		})
	}
}
