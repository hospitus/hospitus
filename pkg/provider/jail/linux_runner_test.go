package jail

import (
	"context"
	"errors"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestEnsureSysrc(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake}
	if err := p.ensureSysrc(context.Background(), "linux_enable", "YES"); err != nil {
		t.Fatalf("ensureSysrc err = %v", err)
	}
	if want := "sysrc linux_enable=YES"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestEnsureSysrcInvalidKey(t *testing.T) {
	p := &JailProvider{runner: &execx.Fake{}}
	if err := p.ensureSysrc(context.Background(), "bad key", "v"); err == nil {
		t.Fatal("expected error for invalid key")
	}
}

func TestSetSysctlIfNotSetAlreadySet(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		if len(args) >= 1 && args[0] == "-n" {
			return []byte("4.4.0\n"), nil // already set
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}
	if err := p.setSysctlIfNotSet(context.Background(), "compat.linux.osrelease", "4.4.0"); err != nil {
		t.Fatalf("err = %v", err)
	}
	// Should NOT issue a set command.
	if hasCmd(fake, "sysctl compat.linux.osrelease=4.4.0") {
		t.Error("should not set an already-set sysctl")
	}
}

func TestSetSysctlIfNotSetEmpty(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		if len(args) >= 1 && args[0] == "-n" {
			return []byte("\n"), nil // empty -> needs set
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}
	if err := p.setSysctlIfNotSet(context.Background(), "compat.linux.osrelease", "4.4.0"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if want := "sysctl compat.linux.osrelease=4.4.0"; !hasCmd(fake, want) {
		t.Errorf("missing set %q", want)
	}
}

func TestEnsureLinuxCompatibility(t *testing.T) {
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		if name == "kldstat" {
			return nil, errors.New("not loaded") // force kldload
		}
		if name == "sysctl" && len(args) >= 1 && args[0] == "-n" {
			return []byte("4.4.0\n"), nil // already set
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}
	if err := p.ensureLinuxCompatibility(context.Background()); err != nil {
		t.Fatalf("ensureLinuxCompatibility err = %v", err)
	}
	if want := "kldload linux64"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
	if want := "sysrc linux_enable=YES"; !hasCmd(fake, want) {
		t.Errorf("missing linux_enable %q", want)
	}
}

func TestEnsureLinuxCompatibilityModulesLoaded(t *testing.T) {
	// kldstat succeeds -> modules already loaded, no kldload.
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		if name == "sysctl" && len(args) >= 1 && args[0] == "-n" {
			return []byte("4.4.0\n"), nil
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}
	if err := p.ensureLinuxCompatibility(context.Background()); err != nil {
		t.Fatalf("err = %v", err)
	}
	if hasCmd(fake, "kldload linux64") {
		t.Error("should not kldload already-loaded modules")
	}
}

// TestEnsureLinuxCompatibilityFileLoadedNoModule covers the real linux64 case:
// the kld file is loaded but provides no module of the same name, so looking it
// up by module reports it missing. Matching the file name has to be enough —
// otherwise every start issues a kldload that can only answer "already loaded".
func TestEnsureLinuxCompatibilityFileLoadedNoModule(t *testing.T) {
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		if name == "kldstat" {
			for _, a := range args {
				if a == "-m" {
					return nil, errors.New("can't find module")
				}
			}
			return nil, nil // -n finds the file
		}
		if name == "sysctl" && len(args) >= 1 && args[0] == "-n" {
			return []byte("4.4.0\n"), nil
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}
	if err := p.ensureLinuxCompatibility(context.Background()); err != nil {
		t.Fatalf("err = %v", err)
	}
	if hasCmd(fake, "kldload linux64") {
		t.Error("kldload issued for a module whose file is already loaded")
	}
}
