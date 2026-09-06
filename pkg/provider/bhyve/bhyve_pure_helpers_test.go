package bhyve

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestCoerceInt64(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want int64
		ok   bool
	}{
		{"int64", int64(42), 42, true},
		{"int", 7, 7, true},
		{"float64", float64(3.9), 3, true},
		{"numeric string", "128", 128, true},
		{"non-numeric string", "abc", 0, false},
		{"unsupported type", true, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := coerceInt64(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Errorf("coerceInt64(%v) = (%d,%v), want (%d,%v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestCoerceInt(t *testing.T) {
	if got, ok := coerceInt(float64(5)); got != 5 || !ok {
		t.Errorf("coerceInt(5.0) = (%d,%v), want (5,true)", got, ok)
	}
	if _, ok := coerceInt("nope"); ok {
		t.Error("coerceInt(non-numeric) should report ok=false")
	}
}

func TestCopyStringMap(t *testing.T) {
	if copyStringMap(nil) != nil {
		t.Error("copyStringMap(nil) should return nil")
	}
	orig := map[string]string{"a": "1", "b": "2"}
	cp := copyStringMap(orig)
	cp["a"] = "changed"
	if orig["a"] != "1" {
		t.Error("copyStringMap must not alias the source map")
	}
	if cp["b"] != "2" {
		t.Errorf("copy missing key b: %v", cp)
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"plain", "hello", "'hello'", false},
		{"empty", "", "''", false},
		{"embedded quote", "a'b", "'a'\"'\"'b'", false},
		{"tab allowed", "a\tb", "'a\tb'", false},
		{"null byte", "a\x00b", "", true},
		{"control char", "a\x01b", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := shellQuote(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Errorf("shellQuote(%q) expected error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("shellQuote(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildRemoteCommand(t *testing.T) {
	// Arrange
	opts := provider.ExecOptions{
		Command:    "ls",
		Args:       []string{"-l", "/tmp"},
		WorkingDir: "/var/log",
		Env:        map[string]string{"FOO": "bar", "BAZ": "qux"},
	}

	// Act
	got, err := buildRemoteCommand(opts)
	// Assert
	if err != nil {
		t.Fatalf("buildRemoteCommand: %v", err)
	}
	// Working dir first, env sorted (BAZ before FOO), then command + args.
	want := "cd '/var/log' && BAZ='qux' FOO='bar' 'ls' '-l' '/tmp'"
	if got != want {
		t.Errorf("buildRemoteCommand = %q, want %q", got, want)
	}
}

func TestBuildRemoteCommandRejectsBadInput(t *testing.T) {
	if _, err := buildRemoteCommand(provider.ExecOptions{Command: "a\x00b"}); err == nil {
		t.Error("expected error when command contains a null byte")
	}
	if _, err := buildRemoteCommand(provider.ExecOptions{Command: "ls", WorkingDir: "a\x00b"}); err == nil {
		t.Error("expected error when working dir contains a null byte")
	}
}

func TestResolveCloudImagePathAbsolute(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "custom.img")
	if err := os.WriteFile(img, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &BhyveProvider{imageDir: dir}
	got, err := p.resolveCloudImagePath(img, "amd64")
	if err != nil || got != img {
		t.Errorf("resolveCloudImagePath(abs) = (%q,%v), want (%q,nil)", got, err, img)
	}

	if _, err := p.resolveCloudImagePath(filepath.Join(dir, "missing.img"), "amd64"); err == nil {
		t.Error("expected error for a missing absolute image")
	}
}

func TestResolveCloudImagePathByConvention(t *testing.T) {
	dir := t.TempDir()
	cloudDir := filepath.Join(dir, "cloud")
	if err := os.MkdirAll(cloudDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cloudDir, "ubuntu-24.04-amd64.img")
	if err := os.WriteFile(want, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &BhyveProvider{imageDir: dir}
	got, err := p.resolveCloudImagePath("ubuntu-24.04", "amd64")
	if err != nil || got != want {
		t.Errorf("resolveCloudImagePath = (%q,%v), want (%q,nil)", got, err, want)
	}
}

func TestResolveCloudImagePathNotFound(t *testing.T) {
	p := &BhyveProvider{imageDir: t.TempDir()}
	if _, err := p.resolveCloudImagePath("nope", "amd64"); err == nil {
		t.Error("expected error when no candidate image exists")
	}
}

func TestBuildHookEnvIsMinimalAndScoped(t *testing.T) {
	p := &BhyveProvider{dataDir: "/data", stateDir: "/state"}
	env := p.buildHookEnv(HookEnv{VMName: "web", VMDir: "/data/web", VNCPort: 5900})

	if !slices.Contains(env, "HOSPITUS_VM_NAME=web") {
		t.Errorf("env missing HOSPITUS_VM_NAME: %v", env)
	}
	if !slices.Contains(env, "HOSPITUS_PROVIDER=bhyve") {
		t.Errorf("env missing HOSPITUS_PROVIDER: %v", env)
	}
	if !slices.Contains(env, "HOSPITUS_VNC_PORT=5900") {
		t.Errorf("env missing HOSPITUS_VNC_PORT: %v", env)
	}
	// Secrets from the daemon environment must never leak into hook scripts.
	for _, e := range env {
		if strings.HasPrefix(e, "HOSPITUS_API") || strings.Contains(strings.ToUpper(e), "SECRET") {
			t.Errorf("hook env leaked a sensitive variable: %q", e)
		}
	}
}

func TestValidateHookPath(t *testing.T) {
	dir := t.TempDir()
	p := &BhyveProvider{stateDir: dir}
	hooksDir := filepath.Join(dir, "hooks")

	if err := p.validateHookPath(filepath.Join(hooksDir, "pre-start.sh")); err != nil {
		t.Errorf("path under state hooks dir should be allowed: %v", err)
	}
	if err := p.validateHookPath("relative/path.sh"); err == nil {
		t.Error("relative paths must be rejected")
	}
	if err := p.validateHookPath("/etc/passwd"); err == nil {
		t.Error("paths outside allowed hook dirs must be rejected")
	}
	if err := p.validateHookPath("/state/hooks/evil;rm -rf.sh"); err == nil {
		t.Error("paths with shell metacharacters must be rejected")
	}
}

func TestFindSSHKey(t *testing.T) {
	dir := t.TempDir()
	p := &BhyveProvider{dataDir: dir, stateDir: dir}
	vmDir := filepath.Join(dir, "web")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := p.findSSHKey("web"); err == nil {
		t.Error("expected error when no SSH key exists")
	}

	keyPath := filepath.Join(vmDir, "ssh_key")
	if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := p.findSSHKey("web")
	if err != nil || got != keyPath {
		t.Errorf("findSSHKey = (%q,%v), want (%q,nil)", got, err, keyPath)
	}
}

func TestGuestMACForVMMissingConfig(t *testing.T) {
	p := &BhyveProvider{dataDir: t.TempDir()}
	if got := p.guestMACForVM("ghost"); got != "" {
		t.Errorf("guestMACForVM(missing) = %q, want empty", got)
	}
}
