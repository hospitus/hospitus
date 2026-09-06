# AGENTS.md

Conventions for humans and coding agents working in this repository.

## Project

Hospitus is an API-first manager for virtual machines and containers, exposed
through a REST daemon (`hospitusd`) and a CLI (`hospitus`). Jails and bhyve are
FreeBSD-only; QEMU and Podman run on FreeBSD, Linux and macOS; vfkit and Apple's
container runtime are macOS-only. Go 1.26+, FreeBSD-first. See `README.md` for
what is and is not exercised.

## Build, test, lint

```sh
make build     # hospitusd + hospitus
make test      # go test -short ./...
make lint      # golangci-lint (must report zero issues)
make fmt vet   # gofmt + go vet
```

Fast local build: `go build -buildvcs=false -o hospitus ./cmd/hospitus-cli/` and
`... -o hospitusd ./cmd/hospitusd/`. Run the race detector before committing:
`go test -race ./...`.

## Code style

- `gofmt` + `gofumpt`; imports grouped with the module local prefix. The
  golangci-lint gate (`.golangci.yml`) is authoritative and must pass with
  zero issues.
- Logging goes through `log/slog` via `pkg/logging`. No `fmt.Printf`,
  `fmt.Println`, or `log.Printf` in library/provider code.
- External commands go through `execx.Runner` (`pkg/provider/execx`) with the
  nil-safe `p.cmd()` accessor, so command sites are unit-testable with
  `execx.Fake`. Do not add bare `exec.CommandContext` calls to provider code
  unless the site needs streaming/PTY/stdin/env that the runner cannot model.
- Wrap errors with `%w` and context. Validate input at boundaries. Prefer
  small files (extract when a file grows past ~800 lines).
- Modify code in place; never introduce parallel `OptimizedX`/`FixedX`
  variants of an existing type.

## Tests

Table-driven `go test`, AAA structure, run under `-race`. Command-driven code
is tested with `execx.Fake`; lifecycle code that needs a real FreeBSD host
(jail/bhyve/QMP/PTY) is not unit-tested and is left out of coverage
expectations.

## Security invariants (do not break)

- The daemon reads the `X-API-Key` header only — no `Authorization: Bearer`,
  no query-parameter key. Keys are bcrypt-hashed.
- Hospitus never edits the host `/etc/pf.conf`; it manages only its own `hospitus`
  PF anchor.
- The ZFS parent dataset is configurable via `pkg/dataset`
  (`HOSPITUS_ZFS_PARENT`, default `zroot/hospitus`) — never hardcode `zroot`.

## Architecture

All backends implement `Provider` in `pkg/provider/provider.go`; optional
capabilities (console, exec, snapshot, clone, autostart, export) are separate
interfaces. HTTP routes and middleware live in `internal/api/`, persistence in
`internal/datastore/` (SQLite), the CLI in `cmd/hospitus-cli/`, and the manifest
parser/templating in `pkg/manifest/`.

## Commits

- Conventional-commit subjects (`feat:`, `fix:`, `refactor:`, `docs:`,
  `test:`, `chore:`), imperative, with a short body stating what and why —
  never a per-file changelog.
- Single author identity, `Signed-off-by` on every commit, no
  `Co-Authored-By`, no bot committers.
- No `fixup!`/`squash!` or "oops" commits in the final history.

## LLM-assisted work

AI/agent output is a proposal, never authoritative: review, test, and rewrite
it before it lands. The `Signed-off-by` author is responsible for the change
regardless of how it was produced. Keep comments and commit messages factual
and terse — no essays, no marketing superlatives.
