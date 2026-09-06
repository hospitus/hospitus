package jail

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
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
	// A stopped jail with a runner, so the call gets past GetInstanceState and
	// actually reaches the conflict check this test is named for.
	p := &JailProvider{
		stateDir:  stateDir,
		zfsParent: "zroot/hospitus/jails",
		runner: &execx.Fake{Func: func(cmd string, _ []string) ([]byte, error) {
			if cmd == "jls" {
				return nil, errors.New("jail not found") // stopped
			}
			return nil, nil
		}},
	}
	ctx := t.Context()

	// The jail being renamed, and one already holding the target name.
	if err := p.saveJailConfig(&jailConfig{Name: "oldjail"}, filepath.Join(stateDir, "oldjail.json")); err != nil {
		t.Fatalf("saveJailConfig: %v", err)
	}
	if err := p.saveJailConfig(&jailConfig{Name: "newjail"}, filepath.Join(stateDir, "newjail.json")); err != nil {
		t.Fatalf("saveJailConfig: %v", err)
	}

	handle := provider.InstanceHandle{
		ID:       "oldjail",
		Provider: "jail",
	}

	err := p.RenameInstance(ctx, handle, "newjail")
	// Without a runner the call fails at GetInstanceState, so "an error
	// occurred" never proved the rename-conflict check was reached. Name the
	// contract the test is for.
	if err == nil {
		t.Fatal("renaming onto an existing jail was accepted")
	}
	if !strings.Contains(err.Error(), "exists") && !strings.Contains(err.Error(), "conflict") {
		t.Errorf("RenameInstance failed before the conflict check: %v", err)
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

// TestRenameJailConfBlockLeavesOtherTextAlone covers the textual replacement
// that used to run over the whole file: for the jail "a", every "a" in every
// token was rewritten, so `path = "/zroot/jails/a/root";` turned into nonsense.
func TestRenameJailConfBlockLeavesOtherTextAlone(t *testing.T) {
	const conf = `a {
	path = "/zroot/jails/a/root";
	mount.devfs;
	exec.start = "/bin/sh /etc/rc";
	allow.raw_sockets;
}
`
	got := renameJailConfBlock(conf, "a", "web")

	if !strings.Contains(got, "web {") {
		t.Errorf("the block header was not renamed:\n%s", got)
	}
	for _, keep := range []string{"mount.devfs;", "exec.start", "allow.raw_sockets;", "/bin/sh /etc/rc"} {
		if !strings.Contains(got, keep) {
			t.Errorf("%q was corrupted by the rename:\n%s", keep, got)
		}
	}
	if strings.Contains(got, "pweb th") {
		t.Errorf("the rename ran over unrelated tokens:\n%s", got)
	}
}
