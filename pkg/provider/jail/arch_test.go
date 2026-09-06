package jail

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// TestBaseSystemHonoursTheRequestedArch covers a manifest that asks for a
// foreign architecture through the arch field rather than in the image name.
//
// The search tried the host's architecture first, so "freebsd:14.3-RELEASE"
// with arch = "arm64" extracted the amd64 base and then registered an aarch64
// emulator for it: /bin/sh inside an "arm64" jail was x86-64.
func TestBaseSystemHonoursTheRequestedArch(t *testing.T) {
	foreign := "arm64"
	if runtime.GOARCH == "arm64" {
		foreign = "amd64"
	}

	imageDir := t.TempDir()
	sets := filepath.Join(imageDir, "sets")
	if err := os.MkdirAll(sets, 0o755); err != nil {
		t.Fatal(err)
	}
	// Both bases are present, as they are on a host that runs mixed jails.
	for _, arch := range []string{runtime.GOARCH, foreign} {
		name := filepath.Join(sets, "freebsd-14.3-RELEASE-"+arch+".txz")
		if err := os.WriteFile(name, []byte("base"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	fake := &execx.Fake{Func: func(string, []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake, config: provider.ProviderConfig{DataDir: imageDir}}
	t.Setenv("HOSPITUS_IMAGE_DIR", imageDir)

	if err := p.extractBaseSystem(context.Background(), "14.3-RELEASE", t.TempDir(), foreign); err != nil {
		t.Fatalf("extractBaseSystem: %v", err)
	}

	want := "freebsd-14.3-RELEASE-" + foreign + ".txz"
	if !strings.Contains(lastCmd(fake), want) {
		t.Errorf("extracted the wrong base: want %s, commands: %v", want, fake.Calls)
	}
}

// TestBaseSystemAcceptsTheArchNamedTwice covers a manifest that writes the
// architecture in the image and in the arch field beside it — which the
// cross-architecture examples do.
//
// Appending the requested suffix to a name that already carries it looks for
// 14.3-RELEASE-arm64-arm64, and no such base exists.
func TestBaseSystemAcceptsTheArchNamedTwice(t *testing.T) {
	foreign := "arm64"
	if runtime.GOARCH == "arm64" {
		foreign = "riscv64"
	}

	imageDir := t.TempDir()
	sets := filepath.Join(imageDir, "sets")
	if err := os.MkdirAll(sets, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "freebsd-14.3-RELEASE-" + foreign + ".txz"
	if err := os.WriteFile(filepath.Join(sets, name), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &execx.Fake{Func: func(string, []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake, config: provider.ProviderConfig{DataDir: imageDir}}
	t.Setenv("HOSPITUS_IMAGE_DIR", imageDir)

	// The image already ends with the architecture, and arch says it again.
	if err := p.extractBaseSystem(context.Background(), "14.3-RELEASE-"+foreign, t.TempDir(), foreign); err != nil {
		t.Fatalf("extractBaseSystem: %v", err)
	}
	if !strings.Contains(lastCmd(fake), name) {
		t.Errorf("extracted the wrong base; commands: %v", fake.Calls)
	}
}

// TestBaseSystemRefusesAMissingArch fails loudly rather than falling back to the
// host's base, which is how the wrong architecture used to be installed.
func TestBaseSystemRefusesAMissingArch(t *testing.T) {
	imageDir := t.TempDir()
	sets := filepath.Join(imageDir, "sets")
	if err := os.MkdirAll(sets, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sets, "freebsd-14.3-RELEASE-"+runtime.GOARCH+".txz"), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &execx.Fake{Func: func(string, []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake, config: provider.ProviderConfig{DataDir: imageDir}}
	t.Setenv("HOSPITUS_IMAGE_DIR", imageDir)

	err := p.extractBaseSystem(context.Background(), "14.3-RELEASE", t.TempDir(), "riscv64")
	if err == nil {
		t.Fatal("a missing riscv64 base was accepted; the host's base would be installed instead")
	}
	if !strings.Contains(err.Error(), "riscv64") {
		t.Errorf("error = %v, want it to name the architecture that is missing", err)
	}
}
