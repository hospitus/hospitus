# Architecture Overview

Hospitus is a **single Go daemon (`hospitusd`) plus a CLI (`hospitus`)** that manages
FreeBSD jails, bhyve VMs, QEMU VMs, and Podman containers behind one REST API.
This page gives you the mental model: the layers, how a request flows through
them, and where state lives. For code-level detail, read the
[Technical Deep Dive](technical-deep-dive.md); for the provider contract, read
[The Provider Interface](providers.md).

---

## The one big idea: provider abstraction

Every virtualization backend implements the same `Provider` interface
(`pkg/provider/provider.go`). Nothing above the provider layer — not the API
handlers, not the CLI, not the job manager — contains jail-specific or
bhyve-specific logic. They all speak to `Provider` and its
[optional capability interfaces](providers.md#optional-capability-interfaces).

Two consequences follow:

- **Adding a backend** is (mostly) writing one more `Provider` implementation and
  registering it. The CLI, API, auth, rate limiting, jobs, and persistence are
  shared for free.
- **Feature differences are explicit.** A backend advertises a capability by
  implementing the matching optional interface; the API dispatches with a Go type
  assertion and returns `501 Not Implemented` when a backend lacks it.

---

## Layers

```
┌──────────────────────────────────────────────────────────────┐
│  Clients                                                     │
│  hospitus CLI · Go client (internal/client)                  │
└───────────────────────────────┬──────────────────────────────┘
                                 │ HTTP/JSON (REST)
┌───────────────────────────────▼──────────────────────────────┐
│  hospitusd — HTTP server (internal/api)                      │
│  middleware: recovery → CORS → security-headers              │
│              → rate-limit → body-limit → logging → auth      │
│  handlers: dispatch to providers + persist to datastore      │
├───────────────┬───────────────────────┬──────────────────────┤
│  Job manager  │  Datastore (SQLite)   │  Auth manager        │
│  (pkg/job)    │  (internal/datastore) │  (internal/auth)     │
└───────────────┴───────────────────────┴──────────────────────┘
                                 │  Provider interface
┌────────────┬────────────┬─────▼──────┬──────────────┬─────────┐
│    jail    │   bhyve    │    qemu    │    podman    │  (yours)│
│  provider  │  provider  │  provider  │   provider   │         │
└─────┬──────┴─────┬──────┴─────┬──────┴──────┬───────┴─────────┘
      ▼            ▼            ▼             ▼
  jail(8)/ZFS  bhyve(8)/vmm  qemu-system  podman(8)
  VNET/PF      tap/bridge    QMP/hostfwd  OCI
```

## Repository map

```
cmd/
  hospitusd/            Daemon entry point (flag parsing, provider registration, server start)
  hospitus-cli/         CLI entry point + cobra command tree (jail/, bhyve/, qemu/, podman/, ...)
internal/            Private packages (not importable by external modules)
  api/               HTTP server, all handlers, middleware
  auth/              API-key authentication (bcrypt AuthManager)
  client/            HTTP client used by the CLI
  crypto/            AES-GCM encryption for sensitive datastore fields
  datastore/         SQLite persistence + migrations
  security/          Audit logging, system security checks
pkg/                 Shared packages
  provider/          Provider interface + registry + the six backends
    jail/ bhyve/ qemu/ podman/ vfkit/ applecontainer/
  image/             Image catalog (embedded + remote profiles; sets/iso/cloud layout)
  job/               Async job manager (worker pool)
  manifest/          UWM (TOML) manifest parse / validate / convert
  network/           Platform network helpers (freebsd.go, linux.go, darwin.go)
  firewall/          PF rule management
  config/            hospitusd config-file parser
  logging/           slog wrappers
test/integration/    FreeBSD + root integration tests
docs/                This documentation (mdBook) + OpenAPI spec
```

`internal/` holds code that must not become a public API (server, auth,
datastore). `pkg/` holds the widely shared, more stable pieces (the provider
contract, jobs, logging).

---

## Request lifecycle: creating a jail

Tracing `hospitus jail create web --cpus 2 --memory 1024 --vnet --ip dhcp`:

1. **CLI** parses flags (`cmd/hospitus-cli/…`) and calls the Go client, which sends
   `POST /api/v1/instances` with an `InstanceSpec` JSON body and the `X-API-Key`
   header.
2. **Middleware** (`internal/api/middleware.go`) runs in order: panic recovery,
   CORS, security headers, optional rate limiting, 32 MB body cap, request
   logging, authentication. CORS and the security headers sit outermost on
   purpose, so their headers reach even a `401` or `429` short-circuit.
3. **Handler** (`internal/api/server.go` → `handleInstances`) validates the body,
   selects the provider from the registry, and calls the provider inline —
   create is synchronous (`201`, or streamed progress with
   `Accept: text/plain`).
4. **Jail provider** (`pkg/provider/jail/…`) does the real work: create the ZFS
   dataset, extract the base system, configure VNET (epair + bridge), install PF
   NAT rules, apply RCTL limits, and run `jail(8)`.
5. **Datastore** (`internal/datastore`) persists the instance row; the handler
   returns the `InstanceHandle`.

The client polls `GET /api/v1/jobs/{id}` only for the operations that actually
go through the job manager — today that is jail upgrade.

---

## State: the SQLite datastore

All durable state lives in one SQLite database (`internal/datastore`, backed by
`mattn/go-sqlite3`). Migrations in `internal/datastore/migrations/registry.go`
create these tables:

| Table | Holds |
|-------|-------|
| `instances` | instance spec, handle, state, labels, resource summary |
| `events` | per-instance event log (FK → instances, cascade delete) |
| `jobs` | async job records (status, progress, result, error) |
| `backups` | backup records, so they survive a restart |
| `stacks`, `stack_instances` | multi-instance stack orchestration |
| `api_keys` | runtime-created API keys (hashed) |
| `schema_versions` | applied migration bookkeeping |

There is **no global mutex** around the datastore — concurrency is handled by the
`database/sql` connection pool with foreign keys enabled. Sensitive fields can be
encrypted at rest with the AES-GCM `crypto.Encryptor` when configured.

Migrations can be run and inspected without starting the server:

```bash
hospitusd --migrate            # apply pending migrations and exit
hospitusd --migration-status   # show status and exit
```

---

## Async jobs

The `JobManager` (`pkg/job/job.go`) is a fixed worker pool (default **4
workers**, queue size 100) with progress reporting (0.0–1.0), context-based
cancellation, and panic recovery. Its only user today is **jail upgrade**;
create, image fetch, stack deploy, import, and backup are all synchronous
handlers. Terminal jobs are persisted to the `jobs` table
via an update hook so status survives across polls. See the
[async jobs section](technical-deep-dive.md#39-goroutines-and-the-job-system) and the
[Jobs API](api-reference.md#jobs).

---

## The provider registry

`pkg/provider/registry.go` is a `map[string]Provider` behind a `sync.RWMutex`,
keyed by `Metadata().Name`. `hospitusd` registers the backends at startup according
to `runtime.GOOS` and tool availability: jail and bhyve are registered only on
FreeBSD (off FreeBSD they do not appear at all), QEMU and Podman everywhere,
and vfkit plus Apple `container` additionally on macOS. `Registry.List()` runs
each provider's `HealthCheck()`, so a registered provider can still report
`available: false`.

---

## Design principles

| Principle | How it shows up |
|-----------|-----------------|
| API-first | Every operation is a REST call; the CLI is just a client. |
| Single binary, no plugins | All providers are compiled into `hospitusd`; no dynamic loading. |
| No external services | Embedded SQLite for state — no etcd, no Kubernetes, no message broker. |
| Context everywhere | Cancellation and timeouts propagate from HTTP down to `exec.CommandContext`. |
| Fail-closed security | TLS and auth reject rather than silently run open (see [Authentication](authentication.md)). |
| Structured logging | `log/slog` throughout (`pkg/logging`); no `fmt.Printf` in library code. |

---

## Non-goals and thin edges

Be aware of what the architecture does **not** currently include, so you do not
build on sand:

- **Multi-node clustering / replication** — a Hospitus daemon manages the host
  it runs on. There is no membership, no consensus and no replication.
- **Dynamic plugin loading** — providers are static Go code, not `.so` plugins.

---

## See also

- [Technical Deep Dive](technical-deep-dive.md) — package-by-package internals
- [The Provider Interface](providers.md) — the contract every backend implements
- [REST API](api-reference.md)
- [Authentication & Permissions](authentication.md)
