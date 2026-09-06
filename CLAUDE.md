# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Conventions

[`AGENTS.md`](AGENTS.md) is the authoritative source for code style, security
invariants, test conventions, and commit rules. Read it first — it applies to
Claude Code and any other coding agent. What follows is supplementary: commands
and the architectural big picture that require reading several files to
reconstruct.

## Commands

```sh
make build                       # hospitusd + hospitus (ldflags stamp version/commit)
make test                        # go test -v -short ./...
make lint                        # golangci-lint, must be zero issues
make fmt vet
make test-coverage               # writes coverage.out + coverage.html
go test -race ./...              # run before committing
go test ./pkg/provider/jail/ -run TestBuildJailConfig    # single test
go test ./internal/api/ -run TestAuth -v -count=1        # single API test
```

`make test` passes `-short`. Tests that need a real FreeBSD host must gate on
`testing.Short()` or live in `test/integration/`.

Integration tests need root on a FreeBSD host with ZFS:

```sh
doas make test-integration       # go test ./test/integration/...
doas make test-integration-full  # adds HOSPITUS_LONG_TESTS=1 long-running cases
make test-quick                  # image-catalog subset only
```

Dev daemon (loopback, no auth, no TLS — never in production):

```sh
doas ./hospitusd --data-dir /var/lib/hospitus --state-dir /var/lib/hospitus/state \
              --db /var/lib/hospitus/hospitus.db --allow-no-auth --allow-insecure-tls
./hospitusd --migration-status      # inspect schema version
./hospitusd --migrate               # apply migrations and exit
```

Cross-compilation targets exist (`make build-freebsd|linux|darwin|darwin-arm64`,
`make build-all`). Docs are mdBook: `make docs-html` / `make docs-pdf`.

CI is one workflow, `.github/workflows/ci.yml`: lint, then tests on Linux,
macOS and FreeBSD, then a security scan and the cross-compilation matrix.
FreeBSD (14.3 and 15.1) runs in a VM through `vmactions/freebsd-vm` — GitHub
has no FreeBSD runner — and `build` waits on all three platforms. Integration
tests run in CI too: the `integration-freebsd` job runs `./test/integration/...`
as root in a FreeBSD 15.1 VM against a file-backed ZFS pool
(`HOSPITUS_ZFS_PARENT=hospitusci/hospitus`); only bhyve stays out, for lack of hardware
virtualization in the VM.

## Platform reality

Jails and bhyve are FreeBSD-only; QEMU and Podman are cross-platform. On macOS,
`registerProviders` in `cmd/hospitusd/main.go` registers qemu, podman, vfkit and
container (Apple's container runtime) — never jail or bhyve — so most of the
system cannot be exercised locally; command-level behavior is
tested instead through `execx.Fake`. Platform-divergent code uses build tags
(`pkg/network/{freebsd,linux,darwin}.go`, `pkg/storage/zfs.go`,
`internal/api/websocket_console_{freebsd,other}.go`); a change to one variant
usually needs the same change in its siblings or the non-FreeBSD build breaks.

## Architecture

**Request flow.** `hospitus` (cobra, `cmd/hospitus-cli/`) → HTTP → `internal/api`
middleware (API key auth, per-IP rate limit) → handler → `provider.Registry`
lookup → provider method → `execx.Runner` → external tool. Instance metadata is
persisted in SQLite (`internal/datastore`), independent of the provider's own
on-disk state under `--state-dir`.

**Providers.** Every backend implements `Provider` in
`pkg/provider/provider.go` (lifecycle, state queries, resources, disks,
networks). Everything optional is a *separate* interface in the same file —
`SnapshotProvider`, `CloneProvider`, `ConsoleProvider`, `ExecProvider`,
`MediaProvider`, `CheckpointProvider`, `PauseProvider`, `RenameProvider`,
`UpgradeProvider`, `PortForwardProvider`, `AutoStartProvider`,
`ExportImportProvider`, `InstanceHealthCheckProvider`. Handlers type-assert to
the optional interface and return an error when a provider does not implement
it; that assertion is how a capability becomes reachable over the API. Adding a
capability to one provider does not require touching the others.

Providers are large enough to be split by concern rather than by type: see
`pkg/provider/jail/` (`create.go`, `start.go`, `vnet.go`, `volumes.go`,
`snapshot.go`, …).

**Command execution.** Provider code calls `p.cmd()`, a nil-safe accessor
returning an `execx.Runner` (`pkg/provider/execx`). Production gets `execx.OS{}`;
tests set `p.runner = &execx.Fake{Func: ...}` and assert on the recorded command
lines. Per-provider test helpers such as
`pkg/provider/jail/runner_testutil_test.go` (`runningProvider`, `hasCmd`,
`lastCmd`) are the idiomatic entry point — reuse them rather than building a
provider by hand. `execx` deliberately abstracts only `CombinedOutput`/`Output`/
`Run`; streaming, PTY, stdin and env sites still use `os/exec` directly.

**API surface.** Routes are all registered in one place:
`(*Server).registerRoutes` in `internal/api/server.go`. Handlers are grouped by
domain into `*_handlers.go`/`*_handler.go` files. The `Datastore` interface at
the top of `server.go` is what handlers depend on — extending persistence means
extending that interface too.

**Async jobs.** Long operations (image fetch, export, clone) go through
`pkg/job`: `JobManager.Submit` returns a `*Job` immediately, workers run it in a
pool, and clients poll `/api/v1/jobs/{id}`. Jobs are mirrored into SQLite via
`internal/datastore/jobs.go`.

**Persistence.** SQLite with hand-written, ordered migrations: append a
`Migration{Version, Description, Up, Down}` to the `All` slice in
`internal/datastore/migrations/registry.go`. Never edit an already-released
migration; add a new version. Applied versions are tracked in `schema_versions`.

**Manifests and stacks.** `pkg/manifest` parses TOML into either a
`WorkloadManifest` or a `StackManifest` (type auto-detected), rendering it
through a Go-template layer with variables and generated secrets first.
`pkg/orchestration` then deploys a stack: creates instances in dependency order,
registers services, and wires health checks and auto-restart. Example manifests
live under `examples/manifests/`, grouped by category, and are exercised by
`pkg/manifest/examples_test.go` — new manifest fields should come with an
example.

**CLI.** `cmd/hospitus-cli/main.go` assembles cobra subcommand groups, one package
per provider (`cmd/jail`, `cmd/bhyve`, `cmd/qemu`, `cmd/podman`) plus
`image`, `manifest`, `backup`, `job`, `secret`, `context`, and CBSD-style
aliases in `cmd/aliases.go`. Shared plumbing — client construction, contexts and
config, output formatting, name resolution, privilege checks — lives in
`cmd/hospitus-cli/internal/cmdutil/`. The HTTP client itself is
`internal/client/`, one file per API domain.

**Config.** Daemon config: `pkg/config` (file + defaults), plus flags in
`cmd/hospitusd/main.go`. CLI config and multi-context support:
`cmd/hospitus-cli/internal/cmdutil/config.go`. ZFS layout always goes through
`pkg/dataset` (`Parent()`, `Pool()`, `Child()`).

**Logging.** `pkg/logging` wraps `log/slog` with a context-carried logger and
canonical field constants (`FieldComponent`, `FieldProvider`, …). Use
`logging.FromContext(ctx)` / `provider.LoggerFromContext(ctx)` in provider code
rather than a package-level logger.

## Scope

A daemon manages the host it runs on. There is no clustering, no replication
and no terminal UI; the REST API, the CLI and the providers are the whole
surface.
