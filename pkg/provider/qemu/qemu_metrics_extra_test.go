package qemu

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func writePID(t *testing.T, p *QEMUProvider, name string, pid int) {
	t.Helper()
	pidFile := filepath.Join(p.stateDir, name+".pid")
	if err := os.WriteFile(pidFile, []byte("  "+strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGetProcessMemoryGeneric(t *testing.T) {
	// Arrange: ps reports RSS in KB.
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("  20480\n"), nil // 20480 KB = 20 MB
	}}
	p := fakeProvider(t, fake)

	// Act
	mb, err := p.getProcessMemoryGeneric(context.Background(), 4242)
	// Assert
	if err != nil {
		t.Fatalf("getProcessMemoryGeneric: %v", err)
	}
	if mb != 20 {
		t.Errorf("memory = %d MB, want 20", mb)
	}
}

func TestGetProcessMemoryDarwinAndFreeBSD(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("102400\n"), nil // 100 MB
	}}
	p := fakeProvider(t, fake)

	if mb, err := p.getProcessMemoryDarwin(context.Background(), 1); err != nil || mb != 100 {
		t.Errorf("darwin memory = (%d,%v), want (100,nil)", mb, err)
	}
	if mb, err := p.getProcessMemoryFreeBSD(context.Background(), 1); err != nil || mb != 100 {
		t.Errorf("freebsd memory = (%d,%v), want (100,nil)", mb, err)
	}
}

func TestGetProcessMemoryGenericPSError(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return nil, errors.New("no such process")
	}}
	p := fakeProvider(t, fake)
	if _, err := p.getProcessMemoryGeneric(context.Background(), 4242); err == nil {
		t.Error("expected error when ps fails")
	}
}

func TestGetProcessMemoryUnparseable(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("not-a-number\n"), nil
	}}
	p := fakeProvider(t, fake)
	if _, err := p.getProcessMemoryGeneric(context.Background(), 4242); err == nil {
		t.Error("expected error when ps output is not numeric")
	}
}

func TestGetProcessMemoryReadsPIDFile(t *testing.T) {
	if runtime.GOOS == "linux" {
		// getProcessMemoryLinux reads /proc/<pid>/status before falling back to
		// ps, and a host whose pid_max exceeds 999999 can have a live process
		// there — whose VmRSS this would read instead of the fixture's.
		t.Skip("the fixture PID is only reliably absent off Linux")
	}
	// Arrange: a pid file plus ps output. On Linux a bogus PID makes
	// getProcessMemoryLinux fall back to the generic ps path.
	//
	// The identity probe comes first: a PID read from a file is not proof that
	// the process is still this VM's QEMU, so the sample only runs once ps has
	// confirmed the command line.
	fake := &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		if slices.Contains(args, "command=") {
			return []byte("qemu-system-x86_64 -name web -m 1024\n"), nil
		}
		return []byte("51200\n"), nil // 50 MB
	}}
	p := fakeProvider(t, fake)
	writePID(t, p, "web", 999999)

	// Act
	mb, err := p.getProcessMemory(context.Background(), "web")
	// Assert
	if err != nil {
		t.Fatalf("getProcessMemory: %v", err)
	}
	if mb != 50 {
		t.Errorf("memory = %d MB, want 50", mb)
	}
}

func TestGetProcessMemoryMissingPIDFile(t *testing.T) {
	p := fakeProvider(t, &execx.Fake{})
	if _, err := p.getProcessMemory(context.Background(), "ghost"); err == nil {
		t.Error("expected error when the pid file is absent")
	}
}

func TestGetProcessMemoryLinuxReadsSelf(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("getProcessMemoryLinux parses /proc, Linux-only")
	}
	// The current process definitely has a /proc/<pid>/status with VmRSS.
	p := fakeProvider(t, &execx.Fake{})
	mb, err := p.getProcessMemoryLinux(context.Background(), os.Getpid())
	if err != nil {
		t.Fatalf("getProcessMemoryLinux(self): %v", err)
	}
	if mb <= 0 {
		t.Errorf("self RSS = %d MB, want > 0", mb)
	}
}
