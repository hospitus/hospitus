package jail

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// stoppedJail builds a provider whose jail is not running, so attaching records
// an fstab entry instead of mounting.
func stoppedJail(t *testing.T, name string) (*JailProvider, string, string) {
	t.Helper()
	stateDir := t.TempDir()
	jailRoot := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	if err := p.saveJailConfig(&jailConfig{Name: name, Path: jailRoot}, filepath.Join(stateDir, name+".json")); err != nil {
		t.Fatal(err)
	}
	p.runner = &execx.Fake{Func: func(cmd string, _ []string) ([]byte, error) {
		if cmd == "jls" {
			return nil, errAssertStopped
		}
		return nil, nil
	}}
	return p, stateDir, jailRoot
}

// TestFstabRecordsTheHostPath covers the target an fstab entry must name.
//
// jail(8) reads mount.fstab from the host, so its mount points are host paths —
// which is what configureLinuxMounts writes. The other callers passed the path
// as seen from inside the jail, so an entry for a volume at /var/db/postgres
// named the host's own /var/db/postgres, and the "umount -a -F" issued during
// cleanup aimed at it.
func TestFstabRecordsTheHostPath(t *testing.T) {
	p, stateDir, jailRoot := stoppedJail(t, "web")

	disk := provider.DiskAttachment{
		MountPoint: "/data",
		Disk:       provider.DiskSpec{ID: "d1", Path: "/tank/data"},
	}
	if err := p.AttachDisk(context.Background(), provider.InstanceHandle{ID: "web"}, disk); err != nil {
		t.Fatalf("AttachDisk: %v", err)
	}

	content := readFstab(t, stateDir, "web")
	want := filepath.Join(jailRoot, "data")
	if !strings.Contains(content, want) {
		t.Errorf("fstab does not name the host path %s:\n%s", want, content)
	}
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "/data" {
			t.Errorf("fstab names the path as seen from inside the jail: %s", line)
		}
	}
}

// TestFstabDoesNotRepeatAnEntry keeps the file from growing on every start,
// which re-adds the mounts it has just made.
func TestFstabDoesNotRepeatAnEntry(t *testing.T) {
	p, stateDir, _ := stoppedJail(t, "web")

	disk := provider.DiskAttachment{
		MountPoint: "/data",
		Disk:       provider.DiskSpec{ID: "d1", Path: "/tank/data"},
	}
	for i := 0; i < 3; i++ {
		if err := p.AttachDisk(context.Background(), provider.InstanceHandle{ID: "web"}, disk); err != nil {
			t.Fatalf("AttachDisk: %v", err)
		}
	}

	lines := strings.Split(strings.TrimSpace(readFstab(t, stateDir, "web")), "\n")
	if len(lines) != 1 {
		t.Errorf("expected one entry after three identical attaches, got %d:\n%s",
			len(lines), strings.Join(lines, "\n"))
	}
}

// TestStartMountsTheJailFstab covers the step that makes a volume attached to a
// stopped jail actually appear once it starts. Nothing mounted this file, so
// such a volume was reported attached and was never there.
func TestStartMountsTheJailFstab(t *testing.T) {
	stateDir := t.TempDir()
	fstabPath := filepath.Join(stateDir, "fstab", "web")
	if err := os.MkdirAll(filepath.Dir(fstabPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fstabPath, []byte("/vol\t/jails/web/data\tnullfs\trw\t0\t0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := &execx.Fake{Func: func(string, []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{stateDir: stateDir, runner: fake}
	config := &jailConfig{Name: "web", Path: t.TempDir()}

	if err := p.applyMounts(context.Background(), config); err != nil {
		t.Fatalf("applyMounts: %v", err)
	}
	if !hasCmd(fake, "mount -a -F "+fstabPath) {
		t.Errorf("the jail fstab was not mounted; commands: %v", fake.Calls)
	}
}

// TestStartLeavesTheFstabToJailForLinux avoids mounting twice: jail(8) mounts
// mount.fstab itself, which is how a Linux jail gets its linprocfs and friends.
func TestStartLeavesTheFstabToJailForLinux(t *testing.T) {
	stateDir := t.TempDir()
	fstabPath := filepath.Join(stateDir, "fstab", "alpine")
	if err := os.MkdirAll(filepath.Dir(fstabPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fstabPath, []byte("/vol\t/jails/alpine/data\tnullfs\trw\t0\t0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := &execx.Fake{Func: func(string, []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{stateDir: stateDir, runner: fake}
	config := &jailConfig{Name: "alpine", Path: t.TempDir()}
	config.JailParameters.MountFstab = fstabPath

	if err := p.applyMounts(context.Background(), config); err != nil {
		t.Fatalf("applyMounts: %v", err)
	}
	if hasCmd(fake, "mount -a -F "+fstabPath) {
		t.Errorf("mounted a fstab that jail(8) mounts itself; commands: %v", fake.Calls)
	}
}

func readFstab(t *testing.T, stateDir, jail string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(stateDir, "fstab", jail))
	if err != nil {
		t.Fatalf("reading the jail fstab: %v", err)
	}
	return string(content)
}

// TestInterfaceRenameUsesTheHostIfconfig covers a foreign-architecture jail.
//
// The rename ran the jail's own ifconfig through jexec. Under emulation that
// binary cannot open a netlink socket, so the rename always failed and the
// interface kept its epairNb name — the one nothing inside the jail was
// configured against.
func TestInterfaceRenameUsesTheHostIfconfig(t *testing.T) {
	fake := &execx.Fake{Func: func(string, []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake}

	p.renameJailInterface(context.Background(), "arm64-test", "epair10b", "default")

	if !hasCmd(fake, "ifconfig -j arm64-test epair10b name default") {
		t.Errorf("the host ifconfig was not used; commands: %v", fake.Calls)
	}
	if hasCmd(fake, "jexec arm64-test ifconfig epair10b name default") {
		t.Errorf("fell back to the jail's own ifconfig although the host's worked: %v", fake.Calls)
	}
}

// TestInterfaceRenameFallsBackToJexec keeps a host whose ifconfig predates -j
// working.
func TestInterfaceRenameFallsBackToJexec(t *testing.T) {
	fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		if cmd == "ifconfig" && len(args) > 0 && args[0] == "-j" {
			return nil, errAssertStopped
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}

	p.renameJailInterface(context.Background(), "web", "epair2b", "eth1")

	if !hasCmd(fake, "jexec web ifconfig epair2b name eth1") {
		t.Errorf("no fallback was attempted; commands: %v", fake.Calls)
	}
}
