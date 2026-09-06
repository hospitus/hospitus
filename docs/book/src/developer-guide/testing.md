# Testing

Hospitus uses a layered testing strategy: fast, pure-Go **unit tests** at the base,
in-process **API handler tests** in the middle, and FreeBSD **integration tests**
at the top. The golden rule that keeps the base fast and portable is: **no OS
calls in unit tests.**

---

## Running tests

```bash
# Unit tests — no root, no FreeBSD required
go test ./...
go test -short ./...                # same set, explicitly skipping anything gated on testing.Short()

# The Makefile targets (both pass -short)
make test                           # go test -v -short ./...
make test-coverage                  # -short with coverage.out (atomic mode)

# Coverage by hand
go test -short -coverprofile=/tmp/cov.out ./...
go tool cover -func=/tmp/cov.out | tail -1        # total line
go tool cover -html=/tmp/cov.out -o /tmp/cov.html # visual report

# A single package / a single test
go test -v ./pkg/provider/jail/...
go test -v -run TestJailProviderHealth ./pkg/provider/jail/...

# Integration tests (FreeBSD + root)
doas go test -v ./test/integration/...            # make test-integration
HOSPITUS_LONG_TESTS=1 doas go test -v ./test/integration/...     # + large downloads
HOSPITUS_CROSSARCH_TESTS=1 doas go test -v ./test/integration/... -run TestCrossArch
doas go test -tags integration -v ./test/integration/...      # security suite (build-tagged)
```

The corresponding Makefile targets are `make test`, `make test-coverage`,
`make test-integration`, `make test-integration-full`, and `make test-quick`.

---

## The example manifests

The unit suite only *renders* the manifests under `examples/manifests/` —
`TestRenderExampleManifests` parses each one and applies its templating. Nothing
in `go test` deploys them, so a manifest can be syntactically perfect and still
fail against a real host.

`make test-manifests` deploys them. It runs `TestExampleManifests` in
`test/integration/`, which walks `examples/manifests/`, applies each template,
waits for the instance to run, and then asks it the question the manifest
itself declares:

```sh
doas make test-manifests                                    # all of them
doas go test -v -run 'TestExampleManifests/nginx' ./test/integration/...
```

The verification is the example's own `health_check`. A manifest that publishes
a web server is asked for a page; one that runs PostgreSQL is asked
`pg_isready`. The shell scripts this replaced stopped at `hospitus podman list |
grep nginx`, which a container whose server never started passes just as
happily. Providers with no exec — bhyve and QEMU — are reported as running and
not questioned further, the same rule that governs the health-check lint test
in `pkg/manifest`.

Cases come from walking the directory, so a new example is covered the day it
is added; nothing has to be registered.

Three things to know before running it:

- **It needs root and real resources.** Jail tests download and extract a base
  system, VM tests boot a guest. A full run takes a while and several gigabytes
  of scratch space; every case tears its instances down, including on failure.
- **Keep the run attached, or detach it deliberately.** Teardown runs from Go's
  `t.Cleanup`, which a killed process never reaches. Over ssh, use `tmux` or
  `screen` if the connection may drop.
- **Manifests that need host hardware skip themselves.** A physical root disk
  or a `passthrough` PCI function is read from the manifest, and the case skips
  when the device is absent — so a host with no spare disk and no bound GPU
  stays green instead of failing or, worse, writing to the wrong disk.
- **Cloud-image manifests need an ISO builder.** A `cloud_init` section is
  rendered into a seed ISO, which needs `mkisofs`, `genisoimage` or `xorrisofs`
  — `pkg install cdrtools` on FreeBSD, `brew install cdrtools` on macOS.
  `hospitus init --check` reports it.

---

## The test pyramid

```
Integration (test/integration/)        ← Few, slow; require FreeBSD + root + ZFS
    ▲  full system
API handler (internal/api/*_test.go)   ← Medium; in-process httptest + mock provider
    ▲  handler + routing + serialization
Unit (pkg/**/*_test.go)                ← Most; fast, pure Go, any OS
    ▲  individual functions and types
```

### Rule: no OS calls in unit tests

Every test under `pkg/` must pass with `go test -short` on any OS with no root.
That means **no**:

- `exec.Command("jail", …)`, `exec.Command("bhyve", …)`
- `exec.Command("zfs", …)`, `exec.Command("pfctl", …)`
- writes outside `t.TempDir()` / `os.TempDir()`

If you need to test OS-level behavior, write an integration test instead. When a
function mixes pure logic with an OS call, extract the pure part (argument
building, parameter conversion) and unit-test that; leave the actual `exec` for
integration.

---

## Unit tests

### Table-driven

The preferred shape for functions with many input/output cases:

```go
func TestNormalizeArch(t *testing.T) {
    tests := []struct{ input, want string }{
        {"amd64", "amd64"},
        {"x86_64", "amd64"},
        {"arm64", "arm64"},
        {"aarch64", "arm64"},
        {"native", runtime.GOARCH},
    }
    for _, tt := range tests {
        t.Run(tt.input, func(t *testing.T) {
            if got := normalizeArch(tt.input); got != tt.want {
                t.Errorf("normalizeArch(%q) = %q, want %q", tt.input, got, tt.want)
            }
        })
    }
}
```

### Command-driven code: `execx.Fake`

Provider code never calls `os/exec` for its ordinary commands. It goes through
`p.cmd()`, an `execx.Runner` (`pkg/provider/execx`): production gets `execx.OS{}`,
tests inject an `execx.Fake` that records every call and returns canned output
without executing anything. This is how command building is unit-tested off a
FreeBSD host.

```go
fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
    if cmd == "zfs" {
        return []byte("zroot/hospitus/jails/web\n"), nil
    }
    return nil, nil
}}
p := &JailProvider{stateDir: t.TempDir(), runner: fake}
// ... call the method, then assert on fake.Calls
```

Each provider package has its own helpers on top of this — in
`pkg/provider/jail/runner_testutil_test.go`, `runningProvider(t, name, fn)`
builds a provider whose named jail is persisted and reported running, while
`lastCmd(fake)` and `hasCmd(fake, "zfs snapshot …")` assert on the exact
command lines issued. Reuse them rather than assembling a provider by hand.

Only `CombinedOutput`, `Output`, and `Run` are abstracted; streaming, PTY,
stdin and env sites still use `os/exec` directly and are integration territory.

### Pure firewall tests

`pkg/firewall/pf_pure_test.go` verifies PF rule *generation* without ever calling
`pfctl`: it writes rule files to `t.TempDir()` and asserts on their content. This
is the model for testing anything that produces config for an external tool —
test the artefact, not the tool.

### Compile-time interface assertions

The cheapest provider test is the `var _ provider.Provider = (*JailProvider)(nil)`
assertion in each backend: the package fails to compile the moment an
implementation drifts from the interface. Add one per optional interface you
implement (see [The Provider Interface](providers.md)).

---

## API handler tests

Handler tests run the server in-process with `net/http/httptest` — no TCP, no
real providers. The helpers live in `internal/api/server_test.go`:

- `newMockProvider() *mockProvider` — a controllable in-memory provider you can
  make return errors on demand.
- `setupTestServer(t) (*Server, *datastore.Datastore)` — wires a mock provider, an
  in-memory datastore, and a `*Server`.
- `NewServer(addr, ds, registry, config)` — the constructor under test.

```go
func TestHandleCreateInstance(t *testing.T) {
    srv, _ := setupTestServer(t)

    body := `{"name":"test","provider":"mock","image":"base"}`
    req := httptest.NewRequest(http.MethodPost, "/api/v1/instances", strings.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()

    srv.Handler().ServeHTTP(w, req)

    if w.Code != http.StatusCreated && w.Code != http.StatusAccepted {
        t.Fatalf("got %d: %s", w.Code, w.Body)
    }
}
```

Inject controlled failures through the mock to exercise error paths (a create
that returns "disk full" should surface as `500`, an unknown id as `404`, and so
on).

---

## Integration tests

`test/integration/` exercises the real jail/bhyve stack and therefore needs
FreeBSD with root, ZFS, and PF. Most files run under a plain
`doas go test ./test/integration/...`; the security suite
(`security_test.go`) is behind a `//go:build integration` tag, so add
`-tags integration` to include it.

Environment gates:

| Variable | Effect |
|----------|--------|
| `HOSPITUS_LONG_TESTS=1` | Enable tests that download large images (base.txz, cloud images). |
| `HOSPITUS_CROSSARCH_TESTS=1` | Enable cross-architecture jail tests (needs `qemu-user-static` + `binmiscctl`). |

Prerequisites: ZFS available, PF enabled (`sysrc pf_enable=YES && service pf
start`), and RACCT enabled (`kern.racct.enable=1` in `/boot/loader.conf`, then
reboot). See the [Host Setup guide](../getting-started/host-setup.md).

---

## Coverage expectations

Because so much of the jail/bhyve code only runs on FreeBSD with root, the
`-short` (no-OS) coverage number is intentionally modest — roughly a third of
lines overall, with the pure-logic packages much higher:

| Package | Realistic `-short` target |
|---------|---------------------------|
| `pkg/firewall/` | 80%+ (pure Go, fully testable) |
| `pkg/manifest/` | 80%+ (pure Go, no OS calls) |
| `pkg/job/` | 75%+ (goroutine-heavy but mockable) |
| `internal/api/` | 70%+ (httptest-based) |
| `pkg/provider/jail/parameters.go`, `crossarch.go` | 90%+ (pure conversion logic) |
| `pkg/provider/jail/` lifecycle | low under `-short`; the rest is integration-only |

When adding a feature, run coverage before and after and keep the pure-logic
paths well covered — the OS-bound paths belong in integration tests, not in the
coverage target.

---

## Writing new tests

**A new API handler** → add a happy-path test plus the obvious error paths: wrong
method, malformed JSON (`400`), unknown id (`404`), and a provider error (`500`).

**A new provider capability** → unit-test the pure argument/spec logic; put the
real OS integration in `test/integration/`.

**A new package utility** → full coverage is expected. If it is untestable
without OS access, extract the pure part and test that.

---

## Lint and format

`golangci-lint` is configured in `.golangci.yml`. Key rules:

| Linter | Enforces |
|--------|----------|
| `goimports` | stdlib / third-party / internal import grouping |
| `govet` | printf format strings, lost cancels, suspicious constructs |
| `staticcheck` | deprecated APIs, unreachable code |
| `gosec` | common security mistakes |
| `errcheck` | no silently ignored errors |
| `errorlint` | error wrapping and comparison |

Keeping `fmt.Printf` out of non-CLI packages is a project convention, not a
linter rule — nothing enforces it automatically.

```bash
make fmt      # gofmt
make vet      # go vet
make lint     # golangci-lint run ./...
```

Fix all warnings before opening a PR — see [Contributing](contributing.md).

---

## See also

- [Contributing](contributing.md)
- [The Provider Interface](providers.md)
- [Developing Without FreeBSD](dev-without-freebsd.md)
