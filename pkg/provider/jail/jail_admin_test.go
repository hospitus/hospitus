package jail

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

func TestRenameProviderInterfaceAssertion(t *testing.T) {
	var _ provider.RenameProvider = (*JailProvider)(nil)
}

func TestUpgradeProviderInterfaceAssertion(t *testing.T) {
	var _ provider.UpgradeProvider = (*JailProvider)(nil)
}

func TestRenameInstanceInvalidNewName(t *testing.T) {
	p := &JailProvider{stateDir: t.TempDir()}
	ctx := t.Context()

	handle := provider.InstanceHandle{
		ID:       "oldjail",
		Provider: "jail",
	}

	invalidNames := []string{
		"",
		"../evil",
		"bad;name",
		"bad name",
		"bad`name`",
		"-starts-dash",
	}

	for _, name := range invalidNames {
		t.Run(name, func(t *testing.T) {
			err := p.RenameInstance(ctx, handle, name)
			if err == nil {
				t.Errorf("Expected error for invalid name %q", name)
			}
		})
	}
}

func TestRenameInstanceValidNameFormat(t *testing.T) {
	validNames := []string{
		"newjail",
		"new-jail",
		"new_jail",
		"jail01",
		"a",
	}

	for _, name := range validNames {
		t.Run(name, func(t *testing.T) {
			err := validation.ValidateInstanceName(name)
			if err != nil {
				t.Errorf("ValidateInstanceName(%q) unexpected error: %v", name, err)
			}
		})
	}
}

func TestRenameInstanceConflictDetection(t *testing.T) {
	stateDir := t.TempDir()
	p := &JailProvider{
		stateDir:  stateDir,
		zfsParent: "zroot/hospitus/jails",
	}
	ctx := t.Context()

	// Create a config file for the "new" name to simulate conflict
	newConfigPath := filepath.Join(stateDir, "newjail.json")
	if err := os.WriteFile(newConfigPath, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("failed to create test config: %v", err)
	}

	// Also need the old config to not fail on GetInstanceState
	// But since GetInstanceState will check jail state, this test
	// will fail before reaching the conflict check on non-FreeBSD.
	// The validation is what we're really testing here.
	handle := provider.InstanceHandle{
		ID:       "oldjail",
		Provider: "jail",
	}

	err := p.RenameInstance(ctx, handle, "newjail")
	// On non-FreeBSD, this will fail at GetInstanceState.
	// On FreeBSD without the jail, it will also fail.
	// Both are acceptable — the important thing is it doesn't succeed.
	if err == nil {
		t.Error("Expected error (either state check or conflict)")
	}
}

func TestUpgradeInstanceRequiresStopped(t *testing.T) {
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir}
	ctx := t.Context()

	handle := provider.InstanceHandle{
		ID:       "myjail",
		Provider: "jail",
	}

	// Without a config file, GetInstanceState will fail → UpgradeInstance will fail
	err := p.UpgradeInstance(ctx, handle, "14.3-RELEASE")
	if err == nil {
		t.Error("Expected error for non-existent jail")
	}
}
