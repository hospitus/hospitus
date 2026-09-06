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
	// Two ps invocations now: the identity probe, then the sample. A PID from a
	// file is not proof that the process is still this VM's QEMU.
	fake := &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		if slices.Contains(args, "command=") {
			return []byte("qemu-system-x86_64 -name web -m 1024\n"), nil
		}
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
	if !slices.Contains(fake.Calls[len(fake.Calls)-1].Args, "4242") {
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
	got, err := p.detectImageFormat(context.Background(), "/img/disk.qcow2")
	if err != nil {
		t.Fatalf("detectImageFormat: %v", err)
	}
	if got != "qcow2" {
		t.Errorf("detectImageFormat = %q, want qcow2", got)
	}
}

// TestDetectImageFormatRefusesToGuess covers what the fallback cost.
//
// Answering "raw" for an image it could not inspect put "-F raw" on a qcow2
// backing file, which hands the guest the image's own metadata as disk contents
// and corrupts it on write. "-F" exists precisely to stop qemu from probing, so
// a wrong answer is worse than none.
func TestDetectImageFormatRefusesToGuess(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  []byte
		err  error
	}{
		{"qemu-img failed", nil, errors.New("qemu-img missing")},
		{"unreadable output", []byte("not json"), nil},
		{"no format reported", []byte(`{"virtual-size":1}`), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
				return tc.out, tc.err
			}}
			p := fakeProvider(t, fake)
			if got, err := p.detectImageFormat(context.Background(), "/img/disk"); err == nil {
				t.Errorf("detectImageFormat = %q, want a refusal", got)
			}
		})
	}
}

func TestCloneDiskLinkedAndFull(t *testing.T) {
	// A linked clone asks qemu-img what the backing file's format is before it
	// declares one with -F; a wrong answer there is applied without complaint.
	fake := &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		if slices.Contains(args, "info") {
			return []byte(`{"format":"qcow2"}`), nil
		}
		return nil, nil
	}}
	p := fakeProvider(t, fake)

	if err := p.cloneDisk(context.Background(), "/src.qcow2", "/dst.qcow2", true); err != nil {
		t.Fatalf("linked cloneDisk: %v", err)
	}
	created := fake.Calls[len(fake.Calls)-1].Args
	if created[0] != "create" || !slices.Contains(created, "-b") {
		t.Errorf("linked clone args = %v, want create -b", created)
	}

	if err := p.cloneDisk(context.Background(), "/src.qcow2", "/dst2.qcow2", false); err != nil {
		t.Fatalf("full cloneDisk: %v", err)
	}
	// The last call, not a fixed index: the linked half above now asks qemu-img
	// for the backing format first, so the positions shifted.
	converted := fake.Calls[len(fake.Calls)-1].Args
	if converted[0] != "convert" {
		t.Errorf("full clone args = %v, want convert", converted)
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
