package qemu

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func fakeProvider(t *testing.T, fake *execx.Fake) *QEMUProvider {
	t.Helper()
	dir := t.TempDir()
	return &QEMUProvider{
		dataDir:  dir,
		stateDir: dir,
		imageDir: dir,
		logger:   logging.WithProvider("qemu"),
		runner:   fake,
	}
}

func TestGetProcessCPU(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte(" 12.5\n"), nil
	}}
	p := fakeProvider(t, fake)
	if err := os.WriteFile(filepath.Join(p.stateDir, "web.pid"), []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cpu, err := p.getProcessCPU(context.Background(), "web")
	if err != nil {
		t.Fatalf("getProcessCPU: %v", err)
	}
	if cpu != 12.5 {
		t.Errorf("cpu = %v, want 12.5", cpu)
	}
	// The ps invocation must target the pid from the file.
	if !slices.Contains(fake.Calls[0].Args, "4242") {
		t.Errorf("ps args = %v, want pid 4242", fake.Calls[0].Args)
	}
}

func TestGetProcessCPUMissingPIDFile(t *testing.T) {
	p := fakeProvider(t, &execx.Fake{})
	if _, err := p.getProcessCPU(context.Background(), "ghost"); err == nil {
		t.Error("expected error when pid file is absent")
	}
}

func TestDetectImageFormat(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte(`{"format":"qcow2","virtual-size":10737418240}`), nil
	}}
	p := fakeProvider(t, fake)
	if got := p.detectImageFormat(context.Background(), "/img/disk.qcow2"); got != "qcow2" {
		t.Errorf("detectImageFormat = %q, want qcow2", got)
	}
}

func TestDetectImageFormatFallsBackToRaw(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return nil, errors.New("qemu-img missing")
	}}
	p := fakeProvider(t, fake)
	if got := p.detectImageFormat(context.Background(), "/img/disk"); got != "raw" {
		t.Errorf("detectImageFormat = %q, want raw fallback", got)
	}
}

func TestCloneDiskLinkedAndFull(t *testing.T) {
	fake := &execx.Fake{}
	p := fakeProvider(t, fake)

	if err := p.cloneDisk(context.Background(), "/src.qcow2", "/dst.qcow2", true); err != nil {
		t.Fatalf("linked cloneDisk: %v", err)
	}
	if fake.Calls[0].Args[0] != "create" || !slices.Contains(fake.Calls[0].Args, "-b") {
		t.Errorf("linked clone args = %v, want create -b", fake.Calls[0].Args)
	}

	if err := p.cloneDisk(context.Background(), "/src.qcow2", "/dst2.qcow2", false); err != nil {
		t.Fatalf("full cloneDisk: %v", err)
	}
	if fake.Calls[1].Args[0] != "convert" {
		t.Errorf("full clone args = %v, want convert", fake.Calls[1].Args)
	}
}

func TestCloneDiskPropagatesError(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("disk busy"), errors.New("exit 1")
	}}
	p := fakeProvider(t, fake)
	if err := p.cloneDisk(context.Background(), "/a", "/b", false); err == nil {
		t.Error("expected error from failing qemu-img convert")
	}
}

func TestCreateDiskImageNoBacking(t *testing.T) {
	fake := &execx.Fake{}
	p := fakeProvider(t, fake)
	err := p.createDiskImage(context.Background(), "/img/new.qcow2", 10, provider.DiskTypeQCOW2, "")
	if err != nil {
		t.Fatalf("createDiskImage: %v", err)
	}
	args := fake.Calls[0].Args
	if args[0] != "create" || !slices.Contains(args, "-f") || !slices.Contains(args, "qcow2") || !slices.Contains(args, "10G") {
		t.Errorf("create args = %v, want create -f qcow2 ... 10G", args)
	}
}
