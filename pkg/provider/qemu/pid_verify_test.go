package qemu

import (
	"context"
	"os"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// TestPidBelongsToVMRejectsNonQEMU verifies the PID-identity check does not
// match an unrelated process, so a reused PID is never killed as if it were the
// VM's QEMU process (audit HIGH qemu_lifecycle.go:360).
func TestPidBelongsToVMRejectsNonQEMU(t *testing.T) {
	ctx := context.Background()

	// The test process itself is a real, running non-qemu process — which is
	// all this assertion needs. Spawning "sleep" through os/exec added a child
	// to reap for nothing, in a package whose rule is that external tools go
	// through the injected runner.
	p := &QEMUProvider{}
	if p.pidBelongsToVM(ctx, os.Getpid(), "web") {
		t.Error("the test process must not be identified as the VM's qemu process")
	}

	// A PID that does not exist.
	if p.pidBelongsToVM(ctx, 2147483000, "web") {
		t.Error("a nonexistent PID must not be identified as a qemu process")
	}
}

// TestPidBelongsToVMMatchesTheNameArgumentWhole covers the substring match the
// check used to do: "web" appeared inside "-name web2", so stopping "web"
// would have killed web2's QEMU after a PID reuse.
func TestPidBelongsToVMMatchesTheNameArgumentWhole(t *testing.T) {
	psLine := func(line string) *QEMUProvider {
		return &QEMUProvider{runner: &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
			return []byte(line), nil
		}}}
	}
	ctx := context.Background()

	p := psLine("/usr/local/bin/qemu-system-x86_64 -name web2 -m 1024")
	if p.pidBelongsToVM(ctx, 42, "web") {
		t.Error(`"web" must not match the VM named "web2"`)
	}
	if !p.pidBelongsToVM(ctx, 42, "web2") {
		t.Error(`"web2" should match its own -name argument`)
	}

	// The name appearing anywhere but after -name is not this VM either.
	other := psLine("/usr/local/bin/qemu-system-x86_64 -name other -drive file=/data/web/disk0.qcow2")
	if other.pidBelongsToVM(ctx, 42, "web") {
		t.Error("a path containing the VM name must not count as identity")
	}

	// A non-qemu process holding a reused PID.
	sleep := psLine("sleep 30")
	if sleep.pidBelongsToVM(ctx, 42, "web") {
		t.Error("a non-qemu command must not be identified as the VM")
	}
}
