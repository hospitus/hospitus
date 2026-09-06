package qemu

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestGetInstanceInfoStopped(t *testing.T) {
	// Arrange
	p := fakeProvider(t, &execx.Fake{})
	persistVM(t, p, "web")

	// Act
	info, err := p.GetInstanceInfo(context.Background(), provider.InstanceHandle{ID: "web"})
	// Assert
	if err != nil {
		t.Fatalf("GetInstanceInfo: %v", err)
	}
	if info.State != provider.StateStopped {
		t.Errorf("state = %q, want stopped", info.State)
	}
	if info.Spec.CPUs != 2 {
		t.Errorf("spec CPUs = %d, want 2", info.Spec.CPUs)
	}
}

func TestGetInstanceInfoNotFound(t *testing.T) {
	p := fakeProvider(t, &execx.Fake{})
	if _, err := p.GetInstanceInfo(context.Background(), provider.InstanceHandle{ID: "ghost"}); err == nil {
		t.Error("expected error for a non-existent instance")
	}
}

func TestGetInstanceStateNotFound(t *testing.T) {
	p := fakeProvider(t, &execx.Fake{})
	if _, err := p.GetInstanceState(context.Background(), provider.InstanceHandle{ID: "ghost"}); err == nil {
		t.Error("expected error for a missing config")
	}
}

func TestCreateUEFIVarsFileNoTemplate(t *testing.T) {
	// Arrange: a code path whose vars sibling does not exist, and no system
	// template present → the function is a no-op that returns nil.
	// The architecture is synthetic on purpose: a real one would find QEMU's own
	// template in a system directory on any host with QEMU installed, and this
	// test is about the case where nothing is found.
	const missingArch = "no-such-arch"
	dir := t.TempDir()
	p := fakeProvider(t, &execx.Fake{})
	p.uefiFirmware = map[string]string{missingArch: filepath.Join(dir, "edk2-"+missingArch+"-code.fd")}
	dest := filepath.Join(dir, "vars.fd")

	// Act
	err := p.createUEFIVarsFile(missingArch, dest)
	// Assert
	if err != nil {
		t.Fatalf("createUEFIVarsFile: %v", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("no vars file should be created when no template exists")
	}
}

func TestCreateUEFIVarsFileCopiesTemplate(t *testing.T) {
	// Arrange: create the vars template sibling of the code path.
	dir := t.TempDir()
	p := fakeProvider(t, &execx.Fake{})
	codePath := filepath.Join(dir, "edk2-x86_64-code.fd")
	varsTemplate := filepath.Join(dir, "edk2-x86_64-vars.fd")
	if err := os.WriteFile(varsTemplate, []byte("VARS-TEMPLATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.uefiFirmware = map[string]string{"x86_64": codePath}
	dest := filepath.Join(dir, "web-vars.fd")

	// Act
	if err := p.createUEFIVarsFile("x86_64", dest); err != nil {
		t.Fatalf("createUEFIVarsFile: %v", err)
	}

	// Assert
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading copied vars: %v", err)
	}
	if string(data) != "VARS-TEMPLATE" {
		t.Errorf("copied vars = %q, want VARS-TEMPLATE", string(data))
	}
}
