package jail

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestIsJailRunning(t *testing.T) {
	tests := []struct {
		name   string
		runErr error
		want   bool
	}{
		{name: "running", runErr: nil, want: true},
		{name: "not running", runErr: errors.New("exit 1"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, tt.runErr }}
			p := &JailProvider{runner: fake}
			got, err := p.isJailRunning(context.Background(), "web")
			if err != nil {
				t.Fatalf("isJailRunning err = %v", err)
			}
			if got != tt.want {
				t.Errorf("isJailRunning = %v, want %v", got, tt.want)
			}
			if want := "jls -j web -N"; !hasCmd(fake, want) {
				t.Errorf("missing %q; got %q", want, lastCmd(fake))
			}
		})
	}
}

func TestGetJailID(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return []byte("42\n"), nil }}
	p := &JailProvider{runner: fake}
	jid, err := p.getJailID(context.Background(), "web")
	if err != nil {
		t.Fatalf("getJailID err = %v", err)
	}
	if jid != 42 {
		t.Errorf("jid = %d, want 42", jid)
	}
	if want := "jls -j web -h jid"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestGetJailIDError(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, errors.New("not found") }}
	p := &JailProvider{runner: fake}
	if _, err := p.getJailID(context.Background(), "web"); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetJailIPs(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []string
	}{
		{name: "single", output: "10.0.0.5\n", want: []string{"10.0.0.5"}},
		{name: "multiple", output: "10.0.0.5,10.0.0.6\n", want: []string{"10.0.0.5", "10.0.0.6"}},
		{name: "none dash", output: "-\n", want: nil},
		{name: "empty", output: "\n", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return []byte(tt.output), nil }}
			p := &JailProvider{runner: fake}
			ips, err := p.getJailIPs(context.Background(), "web")
			if err != nil {
				t.Fatalf("getJailIPs err = %v", err)
			}
			if len(ips) != len(tt.want) {
				t.Fatalf("got %d IPs, want %d (%v)", len(ips), len(tt.want), ips)
			}
			for i, w := range tt.want {
				if !ips[i].Equal(net.ParseIP(w)) {
					t.Errorf("ip[%d] = %v, want %v", i, ips[i], w)
				}
			}
			if want := "jls -j web -h ip4.addr"; !hasCmd(fake, want) {
				t.Errorf("missing %q", want)
			}
		})
	}
}

func TestGetDefaultFreeBSDImage(t *testing.T) {
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "-u" { // freebsd-version -u
			return []byte("14.3-RELEASE\n"), nil
		}
		return []byte("amd64\n"), nil // uname -m
	}}
	p := &JailProvider{runner: fake}
	img, err := p.getDefaultFreeBSDImage(context.Background())
	if err != nil {
		t.Fatalf("getDefaultFreeBSDImage err = %v", err)
	}
	if img != "14.3-RELEASE-amd64" {
		t.Errorf("image = %q, want 14.3-RELEASE-amd64", img)
	}
	if want := "freebsd-version -u"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
	if want := "uname -m"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestGetDefaultFreeBSDImageVersionError(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "-u" {
			return nil, errors.New("cannot detect")
		}
		return []byte("amd64\n"), nil
	}}
	p := &JailProvider{runner: fake}
	if _, err := p.getDefaultFreeBSDImage(context.Background()); err == nil {
		t.Fatal("expected error when freebsd-version fails")
	}
}

func TestJailExistsGhostCleanup(t *testing.T) {
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	// Write a config file so the config-exists check passes.
	cfg := &jailConfig{Name: "web", Path: "/x"}
	if err := p.saveJailConfig(cfg, stateDir+"/web.json"); err != nil {
		t.Fatal(err)
	}

	// zfs says the dataset is gone -> ghost instance -> config removed, false.
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("cannot open 'zroot/hospitus/jails/web': dataset does not exist\n"), errors.New("exit status 1")
	}}
	p.runner = fake

	exists, err := p.jailExists(context.Background(), "web")
	if err != nil {
		t.Fatalf("jailExists err = %v", err)
	}
	if exists {
		t.Error("expected ghost instance to report not-exists")
	}
	if want := "zfs list -H zroot/hospitus/jails/web"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
	if _, statErr := os.Stat(stateDir + "/web.json"); !os.IsNotExist(statErr) {
		t.Errorf("ghost config was not removed: %v", statErr)
	}
}

// A zfs failure that is not "dataset does not exist" says nothing about the
// dataset, so the config must survive it.
func TestJailExistsKeepsConfigOnZFSError(t *testing.T) {
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	if err := p.saveJailConfig(&jailConfig{Name: "web", Path: "/x"}, stateDir+"/web.json"); err != nil {
		t.Fatal(err)
	}

	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("internal error: failed to initialize ZFS library\n"), errors.New("exit status 1")
	}}
	p.runner = fake

	if _, err := p.jailExists(context.Background(), "web"); err == nil {
		t.Fatal("a zfs failure unrelated to existence was reported as not-exists")
	}
	if _, err := os.Stat(stateDir + "/web.json"); err != nil {
		t.Errorf("healthy jail config was removed: %v", err)
	}
}

func TestJailExistsTrue(t *testing.T) {
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	if err := p.saveJailConfig(&jailConfig{Name: "web"}, stateDir+"/web.json"); err != nil {
		t.Fatal(err)
	}
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p.runner = fake
	exists, err := p.jailExists(context.Background(), "web")
	if err != nil || !exists {
		t.Fatalf("jailExists = %v, err = %v; want true, nil", exists, err)
	}
}

func TestJailExistsNoConfig(t *testing.T) {
	p := &JailProvider{stateDir: t.TempDir(), zfsParent: "zroot/hospitus/jails"}
	exists, err := p.jailExists(context.Background(), "missing")
	if err != nil || exists {
		t.Fatalf("jailExists = %v, err = %v; want false, nil", exists, err)
	}
}
