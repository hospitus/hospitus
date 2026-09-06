package jail

import (
	"context"
	"errors"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestBootstrapPkgAlreadyInstalled(t *testing.T) {
	// `which pkg` succeeds -> no bootstrap issued.
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake}
	if err := p.bootstrapPkg(context.Background(), "web"); err != nil {
		t.Fatalf("bootstrapPkg err = %v", err)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("expected only the which check, got %d calls", fake.CallCount())
	}
	if want := "jexec web which pkg"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestBootstrapPkgInstalls(t *testing.T) {
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "which" {
			return nil, errors.New("not found")
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}
	if err := p.bootstrapPkg(context.Background(), "web"); err != nil {
		t.Fatalf("bootstrapPkg err = %v", err)
	}
	if want := "jexec web env ASSUME_ALWAYS_YES=yes /usr/sbin/pkg bootstrap"; !hasCmd(fake, want) {
		t.Errorf("missing bootstrap %q", want)
	}
}

func TestInstallPackage(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake}
	if err := p.installPackage(context.Background(), "web", "nginx"); err != nil {
		t.Fatalf("installPackage err = %v", err)
	}
	if want := "jexec web env ASSUME_ALWAYS_YES=yes pkg install nginx"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestInstallPackageError(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("no packages"), errors.New("exit 1")
	}}
	p := &JailProvider{runner: fake}
	if err := p.installPackage(context.Background(), "web", "nginx"); err == nil {
		t.Fatal("expected error")
	}
}
