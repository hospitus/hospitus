package cmdutil

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/internal/datastore"
)

// TestCheckPlaintextKey guards the rule that an API key never goes to another
// host in the clear: it is a bearer credential, readable and replayable by
// anything on the path.
func TestCheckPlaintextKey(t *testing.T) {
	// Cleared, or an opt-out inherited from the environment turns every
	// rejection case into a pass.
	t.Setenv("HOSPITUS_ALLOW_PLAINTEXT_KEY", "")

	tests := []struct {
		name       string
		url, key   string
		skipVerify bool
		caFile     string
		wantErr    bool
	}{
		{name: "remote http with a key", url: "192.168.1.16:8080", key: "secret", wantErr: true},
		{name: "remote http:// spelled out", url: "http://hospitus.example:8080", key: "secret", wantErr: true},
		{name: "remote https", url: "https://hospitus.example:8443", key: "secret"},
		{name: "remote bare host with a CA implies https", url: "hospitus.example:8443", key: "secret", caFile: "/etc/ssl/ca.pem"},
		{name: "remote bare host with skip-verify implies https", url: "hospitus.example:8443", key: "secret", skipVerify: true},
		{name: "loopback http is fine, it never leaves the machine", url: "localhost:8080", key: "secret"},
		{name: "no key, nothing to leak", url: "192.168.1.16:8080"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkPlaintextKey(tc.url, tc.key, tc.skipVerify, tc.caFile)
			if tc.wantErr && err == nil {
				t.Fatal("the key would have gone out over http:// to another host")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}

// An explicit opt-out exists for a link that is already encrypted underneath,
// an SSH tunnel being the usual one.
func TestCheckPlaintextKeyOptOut(t *testing.T) {
	t.Setenv("HOSPITUS_ALLOW_PLAINTEXT_KEY", "1")
	if err := checkPlaintextKey("192.168.1.16:8080", "secret", false, ""); err != nil {
		t.Fatalf("HOSPITUS_ALLOW_PLAINTEXT_KEY=1 should allow it: %v", err)
	}
}

// TestInitClientRejectsAnUnknownContext: falling back silently sent the command
// to whatever daemon happened to answer on localhost:8080.
func TestInitClientRejectsAnUnknownContext(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	const body = "current_context: prod\ncontexts:\n  - name: prod\n    url: https://hospitus.example:8443\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	err := InitClientFromConfig("", "", "no-such-context", cfg, false, "")
	if err == nil {
		t.Fatal("an unknown --context was accepted and fell back to the default daemon")
	}
	if !strings.Contains(err.Error(), "no-such-context") {
		t.Errorf("the error should name the context asked for, got: %v", err)
	}
}

// TestDaemonPathRefusesARelativePathRemotely: the daemon opens the path on its
// own host, so prefixing the CLI's working directory onto it names a file
// somewhere else entirely.
func TestDaemonPathRefusesARelativePathRemotely(t *testing.T) {
	t.Cleanup(func() { daemonIsLocal, daemonURL = true, "localhost:8080" })

	daemonIsLocal, daemonURL = false, "192.168.1.16:8080"
	if _, err := DaemonPath("export", "backup.tar.gz"); err == nil {
		t.Error("a relative path was silently resolved against the wrong host")
	}
	got, err := DaemonPath("export", "/tank/backups/web.tar.gz")
	if err != nil {
		t.Fatalf("an absolute path is unambiguous and must be accepted: %v", err)
	}
	if got != "/tank/backups/web.tar.gz" {
		t.Errorf("DaemonPath rewrote an absolute path: %q", got)
	}

	daemonIsLocal = true
	if _, err := DaemonPath("export", "backup.tar.gz"); err != nil {
		t.Errorf("a relative path against the local daemon still resolves: %v", err)
	}
}

// requireMock answers GetInstance with whatever the test set.
type requireMock struct {
	APIClientInterface
	inst *datastore.Instance
	err  error
}

func (m *requireMock) GetInstance(context.Context, string) (*datastore.Instance, error) {
	return m.inst, m.err
}

// TestRequireInstanceOfPropagatesRealErrors: returning nil on any failure meant
// a guard that could not reach the daemon passed, and "hospitus qemu stop" went
// on to act on a name that may well be a bhyve VM.
func TestRequireInstanceOfPropagatesRealErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("a 404 is the operation's to report", func(t *testing.T) {
		m := &requireMock{err: fmt.Errorf("%w: no such instance", client.ErrNotFound)}
		if err := RequireInstanceOf(ctx, m, "web", "jail"); err != nil {
			t.Fatalf("a missing instance must not become a different error: %v", err)
		}
	})

	t.Run("a transport failure leaves the check unanswered", func(t *testing.T) {
		m := &requireMock{err: errors.New("dial tcp 127.0.0.1:8080: connection refused")}
		err := RequireInstanceOf(ctx, m, "web", "jail")
		if err == nil {
			t.Fatal("an unreachable daemon passed the provider check")
		}
		if !strings.Contains(err.Error(), "connection refused") {
			t.Errorf("the cause should survive, got: %v", err)
		}
	})

	t.Run("a wrong provider is still refused", func(t *testing.T) {
		m := &requireMock{inst: &datastore.Instance{ID: "web", Provider: "bhyve"}}
		if err := RequireInstanceOf(ctx, m, "web", "jail"); err == nil {
			t.Fatal("a bhyve VM was accepted by a jail command")
		}
	})
}

// TestContextKeyDoesNotFollowAURLOverride: the key belongs to the context's own
// URL. --url alone kept it and sent X-API-Key to the overridden destination,
// handing one daemon's credential to another host (CWE-200).
func TestContextKeyDoesNotFollowAURLOverride(t *testing.T) {
	// Written on the server's goroutine and read on the test's: guarded rather
	// than left to the happens-before the HTTP round-trip incidentally
	// provides.
	var mu sync.Mutex
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotKey = r.Header.Get("X-API-Key")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	body := "current_context: prod\ncontexts:\n  - name: prod\n    url: https://hospitus.example:8443\n    api_key: prod-secret\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	old := APIClient
	t.Cleanup(func() { APIClient = old })

	// The environment must not stand in for the context's key either.
	t.Setenv("HOSPITUS_API_KEY", "")
	t.Setenv("HOSPITUS_API_URL", "")

	if err := InitClientFromConfig(srv.URL, "", "", cfgPath, false, ""); err != nil {
		t.Fatalf("InitClientFromConfig: %v", err)
	}
	if _, err := APIClient.ListInstances(context.Background(), client.ListInstancesFilter{}); err != nil {
		t.Fatalf("ListInstances: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotKey == "prod-secret" {
		t.Error("the context's API key was sent to the host named by --url")
	}
}

// An explicit --api-key is the caller saying which key to use, so it still
// applies to whatever --url names.
func TestExplicitKeyIsHonoredWithAURLOverride(t *testing.T) {
	// Written on the server's goroutine and read on the test's: guarded rather
	// than left to the happens-before the HTTP round-trip incidentally
	// provides.
	var mu sync.Mutex
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotKey = r.Header.Get("X-API-Key")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	old := APIClient
	t.Cleanup(func() { APIClient = old })

	if err := InitClientFromConfig(srv.URL, "given-on-the-command-line", "", filepath.Join(t.TempDir(), "absent.yaml"), false, ""); err != nil {
		t.Fatalf("InitClientFromConfig: %v", err)
	}
	if _, err := APIClient.ListInstances(context.Background(), client.ListInstancesFilter{}); err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotKey != "given-on-the-command-line" {
		t.Errorf("X-API-Key = %q, want the key the caller passed", gotKey)
	}
}

// TestContextTLSDoesNotFollowAURLOverride: a context that legitimately skips
// verification for a daemon with a self-signed certificate silently disabled
// it for whatever host --url named (CWE-295).
//
// Driven through a real TLS server with a certificate no root trusts: if the
// context's TLSSkipVerify were still inherited, the request would succeed.
func TestContextTLSDoesNotFollowAURLOverride(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	body := "current_context: lab\ncontexts:\n  - name: lab\n    url: https://lab.example:8443\n    tls_skip_verify: true\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	old := APIClient
	t.Cleanup(func() { APIClient = old })

	if err := InitClientFromConfig(srv.URL, "", "", cfgPath, false, ""); err != nil {
		t.Fatalf("InitClientFromConfig: %v", err)
	}
	_, err := APIClient.ListInstances(context.Background(), client.ListInstancesFilter{})
	if err == nil {
		t.Fatal("the context's tls_skip_verify governed a connection to the host named by --url")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Errorf("expected a certificate failure, got: %v", err)
	}

	// And the other way round, which is what makes the first half mean
	// anything: with the context naming this very server and no override, its
	// tls_skip_verify must still govern. Passing the flag explicitly instead
	// would have proved nothing — the request would succeed however the
	// inheritance behaved.
	governing := filepath.Join(t.TempDir(), "config.yaml")
	body = "current_context: lab\ncontexts:\n  - name: lab\n    url: " + srv.URL + "\n    tls_skip_verify: true\n"
	if err := os.WriteFile(governing, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InitClientFromConfig("", "", "", governing, false, ""); err != nil {
		t.Fatalf("InitClientFromConfig: %v", err)
	}
	if _, err := APIClient.ListInstances(context.Background(), client.ListInstancesFilter{}); err != nil {
		t.Fatalf("the context's tls_skip_verify did not govern its own URL: %v", err)
	}
}

// TestRedactUserinfo: DaemonPath names the daemon in the error it returns for a
// relative path, and a URL holding credentials would have printed them.
func TestRedactUserinfo(t *testing.T) {
	tests := map[string]string{
		"https://user:hunter2@hospitus.example:8443": "redacted",
		"https://hospitus.example:8443":              "https://hospitus.example:8443",
		"192.168.1.16:8080":                          "192.168.1.16:8080",
		"localhost:8080":                             "localhost:8080",
	}
	for in, want := range tests {
		got := redactUserinfo(in)
		if strings.Contains(got, "hunter2") {
			t.Errorf("redactUserinfo(%q) leaked the password: %q", in, got)
		}
		if !strings.Contains(got, want) {
			t.Errorf("redactUserinfo(%q) = %q, want it to contain %q", in, got, want)
		}
	}
}
