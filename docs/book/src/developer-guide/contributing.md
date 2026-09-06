# Contributing to Hospitus

Thank you for your interest in Hospitus! This guide covers everything you need
to submit a high-quality contribution.

---

## Table of Contents

1. [Prerequisites](#prerequisites)
2. [Setting Up Your Environment](#setting-up-your-environment)
3. [Repository Structure](#repository-structure)
4. [Development Workflow](#development-workflow)
5. [Code Conventions](#code-conventions)
6. [Writing Tests](#writing-tests)
7. [Submitting a Pull Request](#submitting-a-pull-request)
8. [Reporting Issues](#reporting-issues)

---

## Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| Go | ≥ 1.25 (see `go.mod`) | Build and test |
| FreeBSD (recommended) | 14.x | Full feature testing |
| macOS or Linux | — | CLI / QEMU / Podman development |
| `golangci-lint` | latest | Code quality |
| `mdBook` | latest | Documentation preview |

Install golangci-lint. `.golangci.yml` is in the **v2** config format, so a v1
binary cannot lint this repo; CI pins v2.13.1:
```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.1
```

Install mdBook (for docs):
```sh
cargo install mdbook
```

---

## Setting Up Your Environment

```sh
# Fork the repo on GitHub, then:
git clone https://github.com/<your-username>/hospitus.git
cd hospitus

# Install dependencies
go mod download

# Build both binaries
make build

# Run unit tests (no root needed)
go test -short ./...

# Run the linter
golangci-lint run ./...
```

### FreeBSD-Specific Setup

```sh
# Install doas if not present
pkg install doas

# Configure doas for your user
echo "permit :wheel" > /usr/local/etc/doas.conf

# Run integration tests (requires root + ZFS)
doas go test -v ./test/integration/...
```

See [Developing Without FreeBSD](dev-without-freebsd.md) if you only have macOS or Linux.

---

## Repository Structure

```
cmd/           – Binary entry points (hospitusd daemon + hospitus CLI)
internal/      – Private packages (API server, auth, datastore)
pkg/           – Shared packages (provider interface, job system, logging)
test/          – Integration tests
docs/          – This documentation (mdBook)
scripts/       – Helper scripts
examples/      – Example manifests and configurations
```

The key design is the **provider pattern**: `pkg/provider/provider.go` defines
the interface every virtualisation backend must implement. Read
[Technical Deep Dive](technical-deep-dive.md) before diving into provider code.

---

## Extending Hospitus

Three common tasks, with the exact files to touch. Each follows the
provider-abstraction shape described in [Architecture](architecture.md).

### Add a jail parameter

1. Add the field to `JailConfig` in `pkg/provider/jail/config.go`.
2. Emit it from `buildJailConfig()`.
3. Define the CLI flag in `cmd/hospitus-cli/internal/cmdutil/flags.go`.
4. Wire the flag in `cmd/hospitus-cli/cmd/jail/create.go`.
5. Add a unit test in `pkg/provider/jail/jail_test.go` (pure logic — no `jail(8)`).

### Add a CLI command

1. Create `cmd/hospitus-cli/cmd/<provider>/<command>.go` with a
   `newXxxCommand() *cobra.Command`.
2. Register it in the parent command's `AddCommand(...)`.
3. If it needs a new API call, add the method to the `APIClientInterface` in
   `cmd/hospitus-cli/internal/cmdutil/client.go`.
4. Add the matching API handler (below) if the endpoint does not exist yet.

### Add an API endpoint

1. Add the client method in `internal/client/client.go`.
2. Add its signature to `APIClientInterface`
   (`cmd/hospitus-cli/internal/cmdutil/client.go`).
3. Add the handler in `internal/api/server.go` and register the route with a
   Go 1.22+ pattern:
   `s.mux.HandleFunc("POST /api/v1/resources/{id}", s.handleResource)`.
4. Read path parameters with `id := r.PathValue("id")`.
5. For long-running work, submit a background job instead of blocking:
   `s.submitJob(ctx, "type", "description", metadata, fn)` and return
   `202 Accepted` with the job ID.
6. Add a happy-path and an error-path handler test in `internal/api/`.

### Add a whole new provider

See [The Provider Interface](providers.md#writing-a-new-provider) for the
end-to-end walkthrough (struct, interface methods, optional capabilities,
registration, CLI/API wiring).

---

## Development Workflow

### 1. Pick or create an issue

Browse [open issues](https://github.com/hospitus/hospitus/issues) or open a new one
describing your proposed change. For large changes, discuss the design in the issue
before writing code.

### 2. Create a feature branch

```sh
git checkout -b feat/your-feature-name     # new feature
git checkout -b fix/issue-123-short-desc   # bug fix
git checkout -b docs/improve-bhyve-guide   # documentation
```

### 3. Make changes

Follow the [Code Conventions](#code-conventions) below.

### 4. Test locally

```sh
go test -short ./...              # must pass
golangci-lint run ./...           # must pass (no new warnings)
go build ./...                    # must compile cleanly
```

For FreeBSD-specific changes:
```sh
doas go test -v ./pkg/provider/jail/...
doas go test -v ./test/integration/...
```

### 5. Commit with a clear message

```
<type>(<scope>): <short description>

<short body stating what and why — never a per-file changelog>

Signed-off-by: Your Name <you@example.com>
```

**Types:** `feat`, `fix`, `docs`, `test`, `refactor`, `chore`

Every commit carries a `Signed-off-by` trailer (`git commit -s`), and a single
author identity. No `Co-Authored-By` trailers and no bot committers — see
[`AGENTS.md`](https://github.com/hospitus/hospitus/blob/main/AGENTS.md).

**Examples:**
```
feat(jail): add a linked clone that shares blocks with its source
fix(network): prevent IP conflict when bridge already has an IP
docs(guides): add step-by-step bhyve cloud-init tutorial
test(pf): add pure-Go tests for NAT rule generation
```

### 6. Open a Pull Request

Push your branch and open a PR against `main`. Fill in the PR template:
- Description of the change
- How to test
- Checklist (see below)

---

## Code Conventions

### No `fmt.Printf` in library code

```go
// ✗ BAD — in provider or API code
fmt.Printf("Created jail %s\n", name)

// ✓ GOOD — structured log
logger.Info("jail created", "name", name)
```

`fmt.Println` is only acceptable in `cmd/hospitus-cli/cmd/` (CLI output).

### Standard import ordering

golangci-lint enforces three import groups with a blank line between each:

```go
import (
    // 1. stdlib
    "context"
    "fmt"

    // 2. third-party
    "github.com/spf13/cobra"

    // 3. internal (hospitus/hospitus/...)
    "github.com/hospitus/hospitus/pkg/logging"
)
```

### Context as first parameter

Every function that can block, call a subprocess, or be cancelled:

```go
// ✓ GOOD
func (p *JailProvider) CreateInstance(ctx context.Context, spec InstanceSpec) (InstanceHandle, error)

// ✗ BAD — no context
func (p *JailProvider) CreateInstance(spec InstanceSpec) (InstanceHandle, error)
```

### Error wrapping

```go
// ✓ GOOD — wraps for full chain
return fmt.Errorf("failed to create ZFS dataset: %w", err)

// ✗ BAD — loses original error
return fmt.Errorf("ZFS error: %v", err)
```

### Mutexes and defer

```go
// Always defer the unlock immediately after Lock()
p.createMu.Lock()
defer p.createMu.Unlock()
// ...
```

### Atomic file writes

When writing config or rule files that another process may read concurrently:

```go
// Write to temp file + atomic rename — never write directly to final path
```

See `pkg/firewall/pf.go` for the `atomicWriteFile` helper.

---

## Writing Tests

See [Testing Guide](testing.md) for the full strategy. Quick rules:

1. **No OS calls in unit tests.** Tests in `pkg/` must run with `go test -short` and require no FreeBSD, no root, no ZFS.
2. **Use table-driven tests** for functions with multiple input/output combinations.
3. **Use `httptest.NewRecorder`** for API handler tests — no real network needed.
4. **New handler → new test.** Every new API handler must have at least a happy-path and an error-path test.

Run coverage before and after your change:
```sh
go test -short -coverprofile=/tmp/cov.out ./...
go tool cover -func=/tmp/cov.out | grep "total"
```

---

## Submitting a Pull Request

### Checklist

- [ ] `go test -short ./...` passes
- [ ] `golangci-lint run ./...` passes (no new warnings)
- [ ] New code has tests (or explain why they're impractical)
- [ ] Structured logging used (no `fmt.Printf` in library code)
- [ ] Context passed to all blocking/exec operations
- [ ] Documentation updated (if adding a feature or changing behavior)

### Review Process

1. A maintainer will review within a few days.
2. CI must be green (tests + lint).
3. For FreeBSD-specific changes, at least one FreeBSD CI run must pass.
4. Minor style suggestions may be made — address or discuss them.
5. Once approved, maintainers will squash-merge.

---

## Reporting Issues

Please include:
- FreeBSD version (`freebsd-version -u`)
- Go version (`go version`)
- Hospitus version (`hospitus version` or commit hash)
- Full command that failed
- Full error output and relevant log lines
- Steps to reproduce

Open a [GitHub Issue](https://github.com/hospitus/hospitus/issues/new).

For security vulnerabilities, see [SECURITY.md](https://github.com/hospitus/hospitus/blob/main/SECURITY.md).
