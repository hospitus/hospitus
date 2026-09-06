package jail

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// mountTable is real `mount -p` output, in the shape FreeBSD prints: device,
// mount point, type, options, dump, pass, separated by whitespace.
const mountTable = `zroot/ROOT/default	/			zfs	rw,noatime,nfsv4acls 	0 0
devfs			/dev			devfs	rw		0 0
zroot/hospitus/jails/web	/var/lib/hospitus/jails/web	zfs	rw,noatime	0 0
devfs			/var/lib/hospitus/jails/web/dev	devfs	rw		0 0
fdescfs			/var/lib/hospitus/jails/web/dev/fd	fdescfs	rw		0 0
/usr/src		/var/lib/hospitus/jails/web/usr/src	nullfs	ro		0 0
zroot/hospitus/jails/webmail	/var/lib/hospitus/jails/webmail	zfs	rw,noatime	0 0
devfs			/var/lib/hospitus/jails/webmail/dev	devfs	rw		0 0`

// mountingProvider builds a provider whose jail is recorded at root, and whose
// mount table is the fixture above.
func mountingProvider(t *testing.T, name, root string) (*JailProvider, *execx.Fake) {
	t.Helper()
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	if err := p.saveJailConfig(&jailConfig{Name: name, Path: root}, filepath.Join(stateDir, name+".json")); err != nil {
		t.Fatalf("saveJailConfig: %v", err)
	}
	fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		if cmd == "mount" && len(args) == 1 && args[0] == "-p" {
			return []byte(mountTable), nil
		}
		return nil, nil
	}}
	p.runner = fake
	return p, fake
}

// TestJailMountsReadsTheMountTable covers what a guessed list could not: an
// fdescfs the jail asked for, a nullfs volume, and nothing belonging to the
// jail next door whose name starts the same way.
func TestJailMountsReadsTheMountTable(t *testing.T) {
	p, _ := mountingProvider(t, "web", "/var/lib/hospitus/jails/web")

	got := p.jailMounts(context.Background(), "/var/lib/hospitus/jails/web")

	want := map[string]bool{
		"/var/lib/hospitus/jails/web/dev":     true,
		"/var/lib/hospitus/jails/web/dev/fd":  true,
		"/var/lib/hospitus/jails/web/usr/src": true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d mounts %v, want %d", len(got), got, len(want))
	}
	for _, m := range got {
		if !want[m] {
			t.Errorf("unexpected mount %q", m)
		}
	}
}

// TestJailMountsLeavesTheNeighbourAlone is the prefix trap: "webmail" starts
// with "web", and unmounting its filesystems would take down a running jail.
func TestJailMountsLeavesTheNeighbourAlone(t *testing.T) {
	p, _ := mountingProvider(t, "web", "/var/lib/hospitus/jails/web")

	for _, m := range p.jailMounts(context.Background(), "/var/lib/hospitus/jails/web") {
		if m == "/var/lib/hospitus/jails/webmail/dev" {
			t.Fatal("a mount belonging to the jail next door was selected")
		}
	}
}

// TestJailMountsOrdersChildrenFirst pins the order umount(8) needs: /dev/fd
// sits on /dev, and unmounting the parent first fails with EBUSY.
func TestJailMountsOrdersChildrenFirst(t *testing.T) {
	p, _ := mountingProvider(t, "web", "/var/lib/hospitus/jails/web")

	got := p.jailMounts(context.Background(), "/var/lib/hospitus/jails/web")

	dev, devfd := -1, -1
	for i, m := range got {
		switch m {
		case "/var/lib/hospitus/jails/web/dev":
			dev = i
		case "/var/lib/hospitus/jails/web/dev/fd":
			devfd = i
		}
	}
	if dev < 0 || devfd < 0 {
		t.Fatalf("both mounts should be present: %v", got)
	}
	if devfd > dev {
		t.Errorf("/dev/fd must be unmounted before /dev; got %v", got)
	}
}

// TestUmountJailUnmountsWhatIsMounted checks the caller side: every mount the
// table reports is unmounted, and nothing else is attempted.
func TestUmountJailUnmountsWhatIsMounted(t *testing.T) {
	p, fake := mountingProvider(t, "web", "/var/lib/hospitus/jails/web")

	p.umountJail(context.Background(), "web")

	for _, want := range []string{
		"umount -f /var/lib/hospitus/jails/web/dev/fd",
		"umount -f /var/lib/hospitus/jails/web/dev",
		"umount -f /var/lib/hospitus/jails/web/usr/src",
	} {
		if !hasCmd(fake, want) {
			t.Errorf("missing %q", want)
		}
	}

	// The jail root itself is a dataset, destroyed separately, and a mount that
	// is not there must not be attempted at all.
	for _, unwanted := range []string{
		"umount -f /var/lib/hospitus/jails/web",
		"umount -f /var/lib/hospitus/jails/web/proc",
		"umount -f /var/lib/hospitus/jails/web/tmp",
	} {
		if hasCmd(fake, unwanted) {
			t.Errorf("unexpected %q", unwanted)
		}
	}
}

// TestUmountJailWithAnUnreadableMountTable checks that a broken mount(8) does
// not turn into a blind unmount of guessed paths.
func TestUmountJailWithAnUnreadableMountTable(t *testing.T) {
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	if err := p.saveJailConfig(&jailConfig{Name: "web", Path: "/var/lib/hospitus/jails/web"}, filepath.Join(stateDir, "web.json")); err != nil {
		t.Fatal(err)
	}
	fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		if cmd == "mount" {
			return nil, context.DeadlineExceeded
		}
		return nil, nil
	}}
	p.runner = fake

	p.umountJail(context.Background(), "web")

	for _, c := range fake.Calls {
		if c.Name == "umount" {
			t.Errorf("umount was attempted without a mount table: %q", cmdLine(c))
		}
	}
}
