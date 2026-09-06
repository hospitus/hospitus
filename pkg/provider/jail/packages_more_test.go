package jail

import (
	"context"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestInstallPackagesRunning(t *testing.T) {
	// Jail already running -> no start/stop; bootstrap + install issued.
	p, fake := runningProvider(t, "web", func(cmd string, args []string) ([]byte, error) {
		// `which pkg` succeeds so bootstrap is skipped.
		return nil, nil
	})
	err := p.InstallPackages(context.Background(), provider.InstanceHandle{ID: "web"}, []string{"nginx", "git"})
	if err != nil {
		t.Fatalf("InstallPackages err = %v", err)
	}
	if want := "jexec web env ASSUME_ALWAYS_YES=yes pkg install nginx"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
	if want := "jexec web env ASSUME_ALWAYS_YES=yes pkg install git"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestInstallPackagesEmpty(t *testing.T) {
	p, _ := runningProvider(t, "web", nil)
	if err := p.InstallPackages(context.Background(), provider.InstanceHandle{ID: "web"}, nil); err != nil {
		t.Fatalf("empty install should be no-op, got %v", err)
	}
}

func TestInstallPackagesInvalidName(t *testing.T) {
	p, _ := runningProvider(t, "web", nil)
	err := p.InstallPackages(context.Background(), provider.InstanceHandle{ID: "web"}, []string{"-rf"})
	if err == nil {
		t.Fatal("expected validation error for bad package name")
	}
}

func TestListPackages(t *testing.T) {
	p, _ := runningProvider(t, "web", func(cmd string, args []string) ([]byte, error) {
		if cmd == "jexec" && len(args) >= 2 && args[1] == "pkg" {
			return []byte("nginx-1.24.0 High performance web server\ngit-2.44.0 Version control\n"), nil
		}
		return nil, nil
	})
	pkgs, err := p.ListPackages(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("ListPackages err = %v", err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("got %d packages, want 2", len(pkgs))
	}
	if pkgs[0].Name != "nginx-1.24.0" {
		t.Errorf("pkgs[0].Name = %q", pkgs[0].Name)
	}
}

func TestRemovePackage(t *testing.T) {
	p, fake := runningProvider(t, "web", nil)
	if err := p.RemovePackage(context.Background(), provider.InstanceHandle{ID: "web"}, "nginx"); err != nil {
		t.Fatalf("RemovePackage err = %v", err)
	}
	if want := "jexec web env ASSUME_ALWAYS_YES=yes pkg delete nginx"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestRemovePackageInvalidName(t *testing.T) {
	p, _ := runningProvider(t, "web", nil)
	if err := p.RemovePackage(context.Background(), provider.InstanceHandle{ID: "web"}, "bad;name"); err == nil {
		t.Fatal("expected validation error")
	}
}
