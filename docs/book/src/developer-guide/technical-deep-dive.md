# Hospitus — Technical Deep Dive

This document is a complete technical reference for the Hospitus codebase.
It explains every significant design decision, Go pattern, and FreeBSD
system concept used in the project, so that a new contributor with a Go
background and basic Unix knowledge can understand **why** the code is
structured the way it is and **how** each piece works.

---

## Table of Contents

1. [Project Overview](#1-project-overview)
2. [Repository Layout](#2-repository-layout)
3. [Go Concepts & Patterns Used](#3-go-concepts--patterns-used)
4. [FreeBSD Concepts](#4-freebsd-concepts)
5. [The Provider Pattern](#5-the-provider-pattern)
6. [Package-by-Package Reference](#6-package-by-package-reference)
   - [cmd/hospitusd](#cmdhospitusd)
   - [cmd/hospitus-cli](#cmdhospitus-cli)
   - [internal/api](#internalapi)
   - [internal/auth](#internalauth)
   - [internal/datastore](#internaldatastore)
   - [internal/client](#internalclient)
   - [pkg/provider](#pkgprovider)
   - [pkg/provider/jail](#pkgproviderjail)
   - [pkg/provider/bhyve](#pkgproviderbhyve)
   - [pkg/provider/qemu](#pkgproviderqemu)
   - [pkg/provider/podman](#pkgproviderpodman)
   - [pkg/firewall](#pkgfirewall)
   - [pkg/job](#pkgjob)
   - [pkg/image](#pkgimage)
   - [pkg/manifest](#pkgmanifest)
   - [pkg/orchestration](#pkgorchestration)
   - [pkg/backup](#pkgbackup)
   - [pkg/logging](#pkglogging)
   - [pkg/validation](#pkgvalidation)
   - [pkg/config](#pkgconfig)
   - [pkg/cloudinit](#pkgcloudinit)
   - [pkg/network](#pkgnetwork)
7. [Data Flows](#7-data-flows)
8. [Security Architecture](#8-security-architecture)
9. [Testing Strategy](#9-testing-strategy)
10. [FreeBSD-Specific Deep Dives](#10-freebsd-specific-deep-dives)

---

## 1. Project Overview

Hospitus is a **Go daemon + CLI** that manages FreeBSD jails, bhyve VMs, QEMU
VMs, and Podman containers through a single REST API. The daemon is called
`hospitusd`; the CLI is `hospitus`.

**The fundamental design principle** is *provider abstraction*: every
virtualisation technology implements the same `Provider` interface defined
in `pkg/provider/provider.go`. The API layer never calls jail-specific or
bhyve-specific code directly — it only knows about providers.

This means:
- Adding a new virtualisation technology requires only implementing the
  `Provider` interface and registering it.
- The CLI, API, and all middleware (auth, rate-limiting, jobs) are shared
  across all providers.

### Goals

| Goal | Implementation |
|------|---------------|
| API-first | All operations go through REST HTTP/JSON |
| Single binary | `hospitusd` embeds all providers — no plugins |
| No external deps | SQLite (embedded) for state, no Kubernetes, no etcd |
| FreeBSD first | Full jail + bhyve feature parity; macOS/Linux secondary |
| Contributor-friendly | Clean interfaces, structured logs, table-driven tests |

---

## 2. Repository Layout

```
hospitus/
├── cmd/
│   ├── hospitusd/          # Daemon entry point (main.go)
│   └── hospitus-cli/       # CLI entry point + cobra command tree
│       ├── cmd/
│       │   ├── jail/    # hospitus jail <subcommand>
│       │   ├── bhyve/   # hospitus bhyve <subcommand>
│       │   ├── qemu/    # hospitus qemu <subcommand>
│       │   ├── podman/  # hospitus podman <subcommand>
│       │   ├── vfkit/   # hospitus vfkit <subcommand>     (macOS)
│       │   ├── container/  # hospitus container <subcommand> (macOS)
│       │   ├── image/   # hospitus image <subcommand>
│       │   ├── manifest/  backup/  job/  secret/  context/  initcmd/
│       │   └── aliases.go  # CBSD-style aliases (jexec, jls, ...)
│       └── internal/cmdutil/   # Shared CLI helpers (flags, output, client)
├── internal/
│   ├── api/             # HTTP server, handlers, middleware
│   ├── auth/            # API key authentication (bcrypt)
│   ├── client/          # HTTP client used by the CLI
│   ├── crypto/          # AES-GCM encryption for sensitive datastore fields
│   ├── datastore/       # SQLite persistence layer + migrations
│   └── security/        # System security auditor, SSH manager
├── pkg/
│   ├── provider/        # Core Provider interface + registry
│   │   ├── base/        # BaseProvider with default no-op implementations
│   │   ├── jail/        # FreeBSD jail(8) provider
│   │   ├── bhyve/       # FreeBSD bhyve hypervisor provider
│   │   ├── qemu/        # QEMU/KVM provider (cross-platform)
│   │   └── podman/      # Podman/OCI container provider
│   ├── backup/          # Automated backup management
│   ├── cloudinit/       # cloud-init config generation
│   ├── config/          # hospitusd.conf parser
│   ├── firewall/        # PF/nftables firewall abstraction
│   ├── image/           # Image catalog, download, extraction
│   ├── job/             # Async job manager
│   ├── logging/         # Structured slog wrappers
│   ├── manifest/        # UWM (TOML) workload manifest parser/converter
│   ├── network/         # Platform-specific network helpers
│   ├── orchestration/   # Multi-instance stack manager
│   ├── storage/         # Storage abstraction (ZFS zvol, etc.)
│   └── validation/      # Input validation helpers
├── docs/                # User and developer documentation
├── test/
│   └── integration/     # Integration tests (require FreeBSD + root)
└── tools/               # Setup scripts (binmiscctl, etc.)
```

### `internal/` vs `pkg/`

Go uses package visibility by directory. Code under `internal/` is only
importable by code in the same module subtree — it cannot be used by
external Go modules. Code under `pkg/` is intended to be usable both
internally and potentially by external consumers.

- `internal/` contains: API server, auth, datastore, CLI utilities — things
  that should not be stable public APIs.
- `pkg/` contains: provider interfaces, job system, logging — things that
  should be stable or are shared widely.

---

## 3. Go Concepts & Patterns Used

### 3.1 Interfaces and Type Assertions

The heart of Hospitus is the `Provider` interface. In Go, interfaces are
*implicit* — a type satisfies an interface by having the correct methods,
without declaring `implements`.

```go
// pkg/provider/provider.go
type Provider interface {
    Metadata() ProviderMetadata
    Initialize(ctx context.Context, config ProviderConfig) error
    CreateInstance(ctx context.Context, spec InstanceSpec) (InstanceHandle, error)
    // ... many more methods
}
```

**Optional capabilities** are expressed as additional interfaces that
providers may or may not implement:

```go
type SnapshotProvider interface {
    CreateSnapshot(ctx context.Context, handle InstanceHandle, name string) (SnapshotHandle, error)
    DeleteSnapshot(ctx context.Context, snapshot SnapshotHandle) error
    RestoreSnapshot(ctx context.Context, handle InstanceHandle, snapshot SnapshotHandle) error
    ListSnapshots(ctx context.Context, handle InstanceHandle) ([]SnapshotInfo, error)
}
```

The API layer uses a **type assertion** to check if a provider has a
specific capability at runtime:

```go
// In an API handler:
prov, _ := s.registry.Get("jail")
if sp, ok := prov.(provider.SnapshotProvider); ok {
    // This provider supports snapshots
    sp.CreateSnapshot(ctx, handle, "mysnap")
}
```

`ok` is `false` if the underlying type does not implement `SnapshotProvider`,
so the assertion is safe even for providers that don't support snapshots.

### 3.2 Context Propagation

Every function that can be long-running or should respect cancellation takes
a `context.Context` as its first parameter. This is a Go convention.

```go
func (p *JailProvider) CreateInstance(ctx context.Context, spec InstanceSpec) (InstanceHandle, error) {
    // ctx carries:
    // - cancellation signal (e.g., client disconnected)
    // - deadline (e.g., 5 minute timeout)
    // - structured logger via logging package
    cmd := exec.CommandContext(ctx, "jail", "-c", ...)
    // If ctx is cancelled, the subprocess is killed automatically.
}
```

Contexts are created at the top (HTTP handler or job start) and passed
downward. **Never store a context in a struct.** Always pass it as a
function argument.

The `logging` package stores a `*slog.Logger` *inside* the context:

```go
// pkg/logging/logging.go
type contextKey struct{}

func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
    return context.WithValue(ctx, contextKey{}, logger)
}

func FromContext(ctx context.Context) *slog.Logger {
    if l, ok := ctx.Value(contextKey{}).(*slog.Logger); ok {
        return l
    }
    return slog.Default()
}
```

This means the logger already has the request-specific fields (provider
name, instance name, etc.) baked in when you retrieve it from context.

### 3.3 sync.Mutex and sync.RWMutex

**Problem:** Multiple HTTP requests can arrive concurrently. Without
synchronisation, two requests creating jails simultaneously could name-
collide (TOCTOU — Time-Of-Check-Time-Of-Use race).

**Solution:** `JailProvider.createMu` is a plain `sync.Mutex` that is
locked at the start of every jail creation:

```go
// pkg/provider/jail/jail.go
type JailProvider struct {
    createMu sync.Mutex // serialises creation to prevent TOCTOU
    // ...
}

func (p *JailProvider) CreateInstance(ctx context.Context, spec InstanceSpec) (...) {
    p.createMu.Lock()
    defer p.createMu.Unlock()
    // Safe: no other goroutine can be in this block
    if jailExists(spec.Name) {
        return nil, ErrInstanceExists
    }
    // ... create jail
}
```

`defer p.createMu.Unlock()` ensures the lock is released even if a `return`
or `panic` happens partway through.

For read-heavy data, `sync.RWMutex` allows multiple concurrent readers
(`RLock`) but only one writer (`Lock`):

```go
// internal/api/server.go — rate limiter map
type Server struct {
    rateLimiters  map[string]*rate.Limiter
    rateLimiterMu sync.RWMutex
}

func (s *Server) getLimiter(ip string) *rate.Limiter {
    s.rateLimiterMu.RLock()
    l, ok := s.rateLimiters[ip]
    s.rateLimiterMu.RUnlock()
    if ok {
        return l
    }
    // Need to write — upgrade to write lock
    s.rateLimiterMu.Lock()
    defer s.rateLimiterMu.Unlock()
    // Check again (another goroutine may have added it)
    if l, ok = s.rateLimiters[ip]; ok {
        return l
    }
    l = rate.NewLimiter(rate.Limit(s.config.RequestsPerSecond), s.config.BurstSize)
    s.rateLimiters[ip] = l
    return l
}
```

### 3.4 Error Wrapping

Go 1.13+ provides `fmt.Errorf("context: %w", err)` to wrap errors. The `%w`
verb wraps the original error so `errors.Is()` and `errors.As()` can
unwrap the chain:

```go
if err := p.extractBase(ctx, imagePath, jailPath); err != nil {
    return nil, fmt.Errorf("failed to extract base system: %w", err)
}
```

Callers can check:
```go
if errors.Is(err, os.ErrNotExist) { ... }
```

This is used throughout Hospitus so that error messages include the full
chain: *"failed to create instance: failed to extract base system: archive:
file not found"*.

### 3.5 The `defer` Pattern

`defer` runs a function call when the enclosing function returns, regardless
of how it returns. Common patterns in Hospitus:

```go
// Unlock a mutex
p.createMu.Lock()
defer p.createMu.Unlock()

// Close a resource
f, err := os.Open(path)
if err != nil { return err }
defer f.Close()

// Capture elapsed time for logging
start := time.Now()
defer func() {
    p.logger.Info("operation done", "elapsed", time.Since(start))
}()
```

### 3.6 `os/exec` for System Commands

Hospitus is a system administration tool. It delegates heavily to FreeBSD
userland utilities via `os/exec`:

```go
// Run jail(8) to create a jail
cmd := exec.CommandContext(ctx, "jail", "-c", "-f", jailConf, "-n", name)
output, err := cmd.CombinedOutput()
if err != nil {
    return fmt.Errorf("jail -c failed: %w (output: %s)", err, string(output))
}
```

- `CommandContext` ties the subprocess to a context — if the context is
  cancelled, the subprocess gets `SIGKILL`.
- `CombinedOutput` captures both stdout and stderr, which is essential for
  useful error messages.
- The command output is always included in the error message to help
  debugging.

### 3.7 HTTP Server (Standard Library)

Hospitus uses **Go's standard library `net/http`** — no third-party web
framework. Go 1.22 added method and path-parameter matching:

```go
// Register a route for POST /api/v1/instances/{id}/start
s.mux.HandleFunc("POST /api/v1/instances/{id}/start", s.handleStartInstance)

// Extract the path parameter in the handler
func (s *Server) handleStartInstance(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")   // Go 1.22+
    // ...
}
```

**Middleware** is implemented as a function that wraps `http.Handler`:

```go
// internal/api/middleware.go
func (s *Server) authMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        key := r.Header.Get("X-API-Key")
        if !s.authProvider.ValidateKey(key) {
            http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
            return
        }
        next.ServeHTTP(w, r)
    })
}
```

The middleware chain is assembled in `withMiddleware()`, which wraps the whole
mux — `registerRoutes()` only registers patterns:
```go
// innermost first
if s.config.EnableAuth { handler = s.authMiddleware(handler) }
handler = s.loggingMiddleware(handler)
handler = s.bodySizeLimitMiddleware(handler)
if s.config.EnableRateLimit { handler = s.rateLimitMiddleware(handler) }
handler = s.securityHeadersMiddleware(handler)
handler = s.corsMiddleware(handler)
handler = s.recoveryMiddleware(handler)
```

### 3.8 Embedding with `//go:embed`

The image catalog (`pkg/image/catalog.json`) is baked into the binary at
compile time using Go's embed directive:

```go
// pkg/image/catalog.go
//go:embed catalog.json
var defaultCatalogJSON []byte
```

This means the `hospitusd` binary is self-contained — it doesn't need a
separate `catalog.json` file at runtime. The file is compiled into the
`.data` section of the ELF binary.

### 3.9 Goroutines and the Job System

Long-running operations (instance creation, image downloads) would block
the HTTP connection if run synchronously. Hospitus uses an **async job
pattern**:

1. The HTTP handler submits the work as a `Job` and immediately returns
   `202 Accepted` with a job ID.
2. The job runs in a background goroutine.
3. The client polls `GET /api/v1/jobs/{id}` until the job is complete.

```go
// In an API handler:
j, err := s.jobManager.Submit("instance.create", "Creating jail web",
    map[string]string{"name": "web"},
    func(ctx context.Context, j *job.Job) error {
        j.UpdateProgress(0.1, "Preparing ZFS dataset...")
        if err := p.createZFSDataset(ctx, name); err != nil {
            return err
        }
        j.UpdateProgress(0.5, "Extracting base system...")
        // ...
        j.UpdateProgress(1.0, "Done")
        return nil
    })
s.writeJSON(w, http.StatusAccepted, map[string]string{"job_id": j.ID})
```

Inside the job function, `context.Context` carries the cancellation signal
— if the client cancels the job, `ctx.Done()` fires and the next
`exec.CommandContext` call is killed.

### 3.10 Structured Logging with `log/slog`

Go 1.21 introduced `log/slog` as the standard structured logger. Hospitus uses
it extensively:

```go
// Always log with structured fields, never with fmt.Sprintf in messages
logger.Info("Jail created",
    "name", spec.Name,
    "provider", "jail",
    "elapsed_ms", time.Since(start).Milliseconds(),
)
logger.Error("Failed to apply RCTL limits",
    logging.FieldError, err,  // standardised field name
    "jail", jailName,
)
```

The `pkg/logging` package provides convenience constructors:
```go
logger := logging.WithComponent("api")
logger := logging.WithProvider("jail")
logger := logging.WithInstance("jail", "web")
```

Each returns a `*slog.Logger` with pre-set fields, so every log line
emitted from that logger carries `component=api`, etc.

**Rule:** Never use `fmt.Printf`, `fmt.Println`, or `log.Printf` in provider
or API code. `fmt.Println` is only acceptable in CLI command code
(`cmd/hospitus-cli/cmd/`).

### 3.11 Atomic File Writes

When writing PF rules or other critical files, Hospitus uses an atomic write
pattern to prevent partial writes being read by another process:

```go
// pkg/firewall/pf.go
func atomicWriteFile(path string, data []byte) error {
    dir := filepath.Dir(path)
    tmpFile, err := os.CreateTemp(dir, ".hospitus-pf-*.tmp")
    if err != nil { return err }
    tmpPath := tmpFile.Name()
    defer os.Remove(tmpPath)  // Clean up temp file on failure

    if _, err := tmpFile.Write(data); err != nil {
        tmpFile.Close()
        return err
    }
    if err := tmpFile.Close(); err != nil { return err }

    // Rename is atomic on POSIX systems
    return os.Rename(tmpPath, path)
}
```

`os.Rename` is guaranteed atomic on POSIX systems when source and
destination are on the same filesystem. This ensures a reader always sees
either the old complete file or the new complete file, never a partial write.

### 3.12 bcrypt for API Keys

API keys are stored as bcrypt hashes, not in plaintext:

```go
// internal/auth/auth.go
func (am *AuthManager) AddAPIKey(id, name, key string, permissions []string, expiresAt *time.Time) error {
    hashed, err := bcrypt.GenerateFromPassword([]byte(key), bcrypt.DefaultCost)
    if err != nil { return err }
    // Store hashed key, never the original
    am.keys[id] = &APIKey{HashedKey: string(hashed), ...}
}

func (am *AuthManager) ValidateKey(key string) bool {
    for _, k := range am.keys {
        if bcrypt.CompareHashAndPassword([]byte(k.HashedKey), []byte(key)) == nil {
            return true
        }
    }
    return false
}
```

`crypto/subtle.ConstantTimeCompare` is used for string comparisons where
timing attacks are a concern (preventing attackers from inferring key
content by measuring response time).

---

## 4. FreeBSD Concepts

### 4.1 FreeBSD Jails

A **jail** is an OS-level virtualisation primitive in FreeBSD. It is
conceptually similar to a Linux container (cgroups + namespaces), but
predates Docker by many years (jails were introduced in FreeBSD 4.0, year
2000).

A jail provides:
- **Process isolation:** Processes inside the jail cannot see or interact
  with processes outside.
- **Filesystem isolation:** The jail has its own root directory; it cannot
  access the host filesystem above its root.
- **Network isolation:** (Optional) With VNET, the jail has its own
  complete network stack.
- **User isolation:** root inside a jail is not root on the host.

**The `jail(8)` command** creates and destroys jails:
```sh
# Create a jail named "web" rooted at /zroot/hospitus/jails/web/root
jail -c name=web host.hostname=web.example.com path=/zroot/hospitus/jails/web/root \
     ip4.addr=10.0.0.2/24 vnet exec.start="/bin/sh /etc/rc"
```

Hospitus generates this command dynamically from the `InstanceSpec`, using the
`parameters.go` module to build the argument list.

**Jail IDs:** When a jail is running, FreeBSD assigns it a numeric JID
(Jail ID). `jls(8)` lists running jails with their JIDs.

**`jexec(8)`:** Runs a command inside a running jail:
```sh
jexec <jid> /bin/sh  # Interactive shell inside jail
jexec <jid> service nginx start  # Run a command
```
Hospitus uses this to implement `ExecCommand` (non-interactive) and
`GetConsole` (interactive PTY via `jexec`).

### 4.2 VNET — Virtual Network Stack

By default, jails share the host's network stack and have IP aliases on
host interfaces. This is simple but limiting.

**VNET** (Virtual Network) gives a jail its own complete, private network
stack — its own routing table, its own interface list, its own firewall.
It works like a full virtual machine's network stack.

The implementation uses **epair interfaces**:

```
Host                                    Jail
────────────────────                    ────────────────────
epair0a ─── bridge0 ─── NAT ─── em0   epair0b (inside jail)
(host side)                            (jail side)
```

- `ifconfig epair create` creates a virtual ethernet cable with two ends:
  `epair0a` (host side) and `epair0b` (jail side).
- `epair0a` is added to a bridge (`hospitus0`).
- `epair0b` is assigned to the jail.
- PF NAT rules allow the jail to reach the internet via the host's
  physical interface (`em0`).

```go
// pkg/provider/jail/network_manager.go
cmd := exec.CommandContext(ctx, "ifconfig", "epair", "create")
output, _ := cmd.CombinedOutput()
epairA := strings.TrimSpace(string(output))  // e.g., "epair0a"
epairB := epairA[:len(epairA)-1] + "b"       // "epair0b"
```

**IPv6 with ULA:** Hospitus derives a stable ULA (Unique Local Address, RFC
4193) `/64` prefix for each jail based on a SHA-256 hash of the jail name.
This gives jails a deterministic IPv6 address that doesn't change between
restarts, without requiring an external DHCP server.

### 4.3 ZFS — Zettabyte File System

ZFS is the default filesystem for FreeBSD (and required for Hospitus jail
operations). Key concepts:

**Dataset:** A ZFS filesystem mounted at a path. Datasets are hierarchical:
```
zroot/                    ← pool root
  hospitus/                  ← Hospitus namespace dataset
    jails/                ← all jails
      web/                ← jail "web"
        root/             ← actual jail root filesystem
      db/                 ← jail "db"
    bases/                ← base system archives extracted here
      14.3-RELEASE-amd64/ ← a specific base
    backups/              ← backup storage
```

**Snapshot:** A point-in-time frozen copy of a dataset. Zero-cost to
create; uses copy-on-write (new blocks are written to new locations; old
blocks are preserved for the snapshot).

```sh
# Create a snapshot
zfs snapshot zroot/hospitus/jails/web@before-upgrade

# List snapshots
zfs list -t snapshot -r zroot/hospitus/jails/web

# Rollback to snapshot (destroys newer data)
zfs rollback zroot/hospitus/jails/web@before-upgrade
```

**Clone:** A writable copy of a snapshot. It starts as a thin clone
(shares blocks with the snapshot) but diverges as data is written.

```sh
# Clone from snapshot to create a new jail
zfs clone zroot/hospitus/jails/web@before-upgrade zroot/hospitus/jails/web-clone
```

Hospitus implements `SnapshotProvider` and `CloneProvider` for jails using
these exact ZFS operations.

**Why ZFS?**
- Instantaneous snapshots and clones (no `tar` needed)
- Copy-on-write → consistent snapshots even on live jails
- Built-in data integrity (checksums on every block)
- Compression (LZ4, ZSTD) and deduplication built-in
- `zfs send`/`zfs receive` for backup and migration

### 4.4 PF — Packet Filter

PF is FreeBSD's firewall. It processes all incoming and outgoing network
packets. Hospitus uses PF for:

1. **NAT (Network Address Translation):** Allows jails to access the
   internet through the host's IP address.
2. **Port forwarding (RDR rules):** Exposes jail ports to the host network.

**Anchors:** Hospitus keeps its rules isolated in a PF *anchor* named `hospitus` — a
sub-ruleset that can be updated independently of the main ruleset. The anchor's
content is stored under `/var/lib/hospitus/firewall/pf/` and loaded with
`pfctl -a hospitus`; the running rules `/etc/pf.conf` are declared once to include
this anchor:

```
# Declared in /etc/pf.conf so the kernel knows about the anchor:
anchor "hospitus"
nat-anchor "hospitus"

# Hospitus manages the anchor's content via pfctl -a hospitus
```

Hospitus never edits `/etc/pf.conf`. On startup, `pkg/firewall/pf.go`
(`checkAnchorNoLock`) only *verifies* that the `hospitus` anchor is declared in the
active ruleset; if it is missing, the daemon logs a warning with the exact lines
to add and continues — NAT and rdr rules simply are not evaluated until you
declare the anchor yourself (once) and run `pfctl -f /etc/pf.conf`. All
per-instance changes happen only inside the anchor files under
`/var/lib/hospitus/firewall/pf/`, loaded live via `pfctl -a hospitus -f`.

This design is critical for safety: Hospitus's rules are cleanly isolated in
the `hospitus` anchor. If Hospitus crashes, the main PF ruleset is unaffected.
System administrators can inspect Hospitus's rules with:
```sh
pfctl -a hospitus -s rules
pfctl -a hospitus -s nat
```

**NAT rule for a jail:**
```
nat on em0 from 10.0.0.0/24 to any -> (em0)
```
This says: packets originating from the 10.0.0.0/24 network (our jail
network) going to anywhere, should have their source IP rewritten to the
IP of `em0` (the host's external interface). This is standard MASQUERADE.

**RDR (redirect) rule for port forwarding:**
```
rdr on em0 proto tcp from any to any port 8080 -> 10.0.0.2 port 80
```
Traffic arriving on the host's port 8080 is redirected to the jail at
10.0.0.2 on port 80.

Hospitus writes rules to individual files per-jail (e.g.,
`/var/lib/hospitus/firewall/pf/web.conf`) and reloads the anchor with:
```sh
pfctl -a hospitus -f /var/lib/hospitus/firewall/pf/hospitus.conf
```

### 4.5 RCTL — Resource Control

RCTL (Resource ConTroL) is FreeBSD's mechanism for limiting resources per
process, jail, user, or login class. It's analogous to Linux cgroups.

**Prerequisite:** RCTL requires `kern.racct.enable=1` in `/boot/loader.conf`
(a kernel boot-time setting). Without this, all RCTL operations fail.

**RCTL rule syntax:** `subject:subject-id:resource:action=amount`

```sh
# Limit jail "web" to 2 CPUs (200% — 100% per core)
rctl -a jail:web:pcpu:deny=200

# Limit jail "web" to 1 GB RAM
rctl -a jail:web:memoryuse:deny=1073741824

# Throttle I/O to 100 MB/s reads
rctl -a jail:web:readbps:throttle=104857600

# Remove all limits for a jail
rctl -r jail:web
```

In Hospitus, RCTL limits are applied in `pkg/provider/jail/jail_rctl.go`.
All errors are **hard failures** — if a user configured a limit and it
cannot be applied (e.g., RCTL is disabled), the jail creation fails
rather than silently ignoring the limit. This prevents accidental
resource exhaustion.

### 4.6 bhyve — The BSD Hypervisor

`bhyve` is FreeBSD's Type-2 hypervisor (similar to KVM on Linux). It
uses hardware virtualisation extensions (Intel VT-x, AMD-V) to run full
virtual machines with unmodified operating systems.

Key components:
- `bhyve(8)`: The hypervisor process itself. Runs as a regular process.
- `bhyveload(8)`: Bootloader for FreeBSD guests (loads kernel from inside
  the VM).
- `uefi_edk2` (OVMF): UEFI firmware for booting Linux/Windows guests.
- `nmdm(4)`: Null-modem device for console access (creates a virtual
  serial cable between `/dev/nmdm0A` and `/dev/nmdm0B`).

**VM storage:** bhyve uses either:
- **ZFS zvol:** A block device backed by ZFS, one per disk, named
  `<zfs-parent>/bhyve/<vm>/disk<N>` — e.g.
  `zfs create -V 20G zroot/hospitus/bhyve/web/disk0`, appearing as
  `/dev/zvol/zroot/hospitus/bhyve/web/disk0`.
- **Raw file:** A file used as a disk image.

**Networking:** bhyve VMs use virtual TAP interfaces connected to a bridge:
```
Host: tap0 ── bridge0 ── em0 (NAT)
       ↑
VM sees: vtnet0 (virtio NIC)
```

**Hospitus bhyve provider** (`pkg/provider/bhyve/`) implements:
- Full VM lifecycle (create, start, stop, delete)
- Cloud-init support (seed ISO generation)
- VNC console access
- Snapshot/clone via ZFS
- Live console via nmdm

### 4.7 `doas` — FreeBSD's sudo

`doas` is a simpler privilege escalation tool than `sudo`. It's the
standard on FreeBSD. In all Hospitus documentation and scripts, `doas` is
used instead of `sudo`. Configuration is in `/usr/local/etc/doas.conf`.

### 4.8 Cross-Architecture Emulation

Hospitus can run ARM64 or RISC-V jails on an AMD64 host using **QEMU user-
mode emulation** and **`binmiscctl(8)`**.

`binmiscctl(8)` is a FreeBSD kernel mechanism that allows registering an
interpreter for foreign binary formats. When the kernel encounters an
ARM64 ELF binary, it automatically invokes the registered QEMU static
binary to run it.

```sh
# Register QEMU as interpreter for ARM64 ELF binaries
binmiscctl add aarch64 --interpreter /usr/local/bin/qemu-aarch64-static \
    --magic "\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\xb7" \
    --mask "\xff\xff\xff\xff\xff\xff\xff\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff" \
    --size 20
```

Hospitus's `setupCrossArchEmulation()` function copies the QEMU static binary
into the jail's filesystem so that programs inside the jail can exec
cross-architecture binaries.

---

## 5. The Provider Pattern

### 5.1 Interface Definition

```go
// pkg/provider/provider.go — the core contract
type Provider interface {
    Metadata() ProviderMetadata
    Initialize(ctx context.Context, config ProviderConfig) error
    Shutdown(ctx context.Context) error
    HealthCheck(ctx context.Context) error

    CreateInstance(ctx context.Context, spec InstanceSpec) (InstanceHandle, error)
    DeleteInstance(ctx context.Context, handle InstanceHandle, force bool) error
    StartInstance(ctx context.Context, handle InstanceHandle) error
    StopInstance(ctx context.Context, handle InstanceHandle, opts StopOptions) error
    RestartInstance(ctx context.Context, handle InstanceHandle) error

    GetInstanceState(ctx context.Context, handle InstanceHandle) (InstanceState, error)
    GetInstanceInfo(ctx context.Context, handle InstanceHandle) (InstanceInfo, error)
    ListInstances(ctx context.Context, filter InstanceFilter) ([]InstanceHandle, error)

    SetInstanceResources(ctx context.Context, handle InstanceHandle, resources ResourceSpec) error
    GetInstanceMetrics(ctx context.Context, handle InstanceHandle) (Metrics, error)

    AttachDisk(ctx context.Context, handle InstanceHandle, disk DiskAttachment) error
    DetachDisk(ctx context.Context, handle InstanceHandle, diskID string) error
    AttachNetwork(ctx context.Context, handle InstanceHandle, network NetworkAttachment) error
    DetachNetwork(ctx context.Context, handle InstanceHandle, interfaceID string) error

    Capabilities() ProviderCapabilities
}
```

### 5.2 Optional Capability Interfaces

Beyond the base interface, providers can opt into additional capabilities:

There are 16 optional interfaces. See
[The Provider Interface](providers.md#who-implements-what) for the full,
authoritative matrix, including the macOS-only backends.

| Interface | Methods | Providers |
|-----------|---------|-----------|
| `SnapshotProvider` | CreateSnapshot, DeleteSnapshot, RestoreSnapshot, ListSnapshots | jail, bhyve, qemu, podman |
| `CloneProvider` | CloneInstance, CloneFromSnapshot | jail, bhyve, qemu, podman |
| `ConsoleProvider` | GetConsole, ResizeConsole | jail, bhyve, qemu, podman |
| `ExecProvider` | ExecCommand, ExecInteractive | jail, bhyve, qemu, podman |
| `ExecStreamingProvider` | ExecCommandStream (embeds ExecProvider) | jail, podman |
| `ExportImportProvider` | ExportInstance, ImportInstance | jail, bhyve, podman |
| `AutoStartProvider` | SetAutoStart, GetAutoStart, ListAutoStartInstances, StartAutoStartInstances | jail, bhyve, qemu, podman |
| `InstanceHealthCheckProvider` | CheckInstanceHealth | jail, bhyve, podman |
| `MediaProvider` | InsertMedia, EjectMedia, ListMedia, SetBootOrder, GetBootOrder | bhyve, qemu |
| `CheckpointProvider` | CheckpointInstance, RestoreCheckpoint, ListCheckpoints, DeleteCheckpoint | bhyve |
| `PauseProvider` | PauseInstance, ResumeInstance | bhyve, qemu, podman |
| `RenameProvider` | RenameInstance | jail, bhyve, qemu, podman |
| `UpgradeProvider` | UpgradeInstance | jail |
| `PortForwardProvider` | AddPortForward, RemovePortForward, ListPortForwards | qemu |
| `InstanceAddressProvider` | InstanceAddresses | jail, bhyve, qemu, podman |
| `ReconfigureProvider` | Reconfigure | jail |

The API dispatches to these optional interfaces using type assertions:

```go
// internal/api/server.go (simplified)
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
    prov, _ := s.registry.Get(r.PathValue("provider"))
    sp, ok := prov.(provider.SnapshotProvider)
    if !ok {
        s.writeError(w, http.StatusNotImplemented, "Provider does not support snapshots")
        return
    }
    // Use sp for snapshot operations
}
```

### 5.3 Registry

`pkg/provider/registry.go` maintains the map of registered providers:

```go
type Registry struct {
    mu        sync.RWMutex
    providers map[string]Provider
}

func (r *Registry) Register(p Provider) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    name := p.Metadata().Name
    r.providers[name] = p
    return nil
}

func (r *Registry) Get(name string) (Provider, error) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    p, ok := r.providers[name]
    if !ok {
        return nil, fmt.Errorf("provider %q not found", name)
    }
    return p, nil
}
```

The daemon registers providers at startup based on what's available on the
platform. jail and bhyve are registered **only on FreeBSD** — on any other
platform they are never registered, so they do not appear in the registry at
all. QEMU and Podman are registered everywhere; macOS additionally registers
vfkit and Apple `container`, for four providers there.

### 5.4 InstanceHandle and InstanceSpec

**`InstanceSpec`** is the *desired state* — what the user wants to create:
```go
type InstanceSpec struct {
    Name        string
    Description string
    Image       string
    Arch        string
    OSType      string     // "freebsd", "linux", "windows"
    OSVersion   string
    CPUs        int
    MemoryMB    int64
    Networks    []NetworkSpec
    Disks       []DiskSpec
    Labels      map[string]string
    CloudInit   CloudInitSpec
    ProviderConfig map[string]interface{}  // provider-specific extras
    // ...
}
```

**`InstanceHandle`** is the *runtime identity* — how to refer to a running
or created instance:
```go
type InstanceHandle struct {
    ID       string  // Hospitus UUID
    Name     string  // Human-readable name
    Provider string  // Which provider manages this
}
```

The handle is stored in the SQLite datastore and passed back to provider
methods when operating on an existing instance.

---

## 6. Package-by-Package Reference

### `cmd/hospitusd`

**Entry point** for the daemon. `main.go` does:
1. Parse flags (`--addr`, `--data-dir`, `--db`, etc.).
2. Load `hospitusd.conf`.
3. Create the SQLite datastore.
4. Create the provider registry and register available providers.
5. Initialize each provider (`p.Initialize(ctx, config)`).
6. Create and configure the HTTP API server.
7. Optionally set up TLS, auth, backup manager, stack manager.
8. Start the server (blocking until signal).

### `cmd/hospitus-cli`

**The CLI client.** Uses [Cobra](https://github.com/spf13/cobra) for the
command-tree structure.

Every CLI command follows this pattern:
1. Create an HTTP client pointing to `HOSPITUS_API_URL`.
2. Build a request struct from command-line flags.
3. Make an HTTP call to the daemon.
4. Format and print the response.

**`internal/cmdutil/`** contains:
- `client.go`: `APIClientInterface` — the full mock-able API surface. Used
  in tests to substitute a real HTTP call with a stub.
- `flags.go`: All reusable flag definitions (`--cpus`, `--memory`,
  `--vnet`, `--image`, etc.) in one place.
- `output.go`: Table and JSON output formatters.
- `privilege.go`: Checks if a command needs elevated privileges and provides
  a helpful error message.

### `internal/api`

The HTTP API server. `server.go` contains the `Server` struct and all route
registration in `registerRoutes()`.

**Server struct:**
```go
type Server struct {
    mux              *http.ServeMux
    datastore        Datastore
    registry         *provider.Registry
    httpServer       *http.Server
    authProvider     auth.AuthProvider
    rateLimiters     map[string]*rate.Limiter
    authRateLimiters map[string]*rate.Limiter
    rateLimiterMu    sync.RWMutex
    config           *ServerConfig
    stackManager     *orchestration.StackManager
    backupManager    *backup.BackupManager
    networkMgr       network.Manager
    jobManager       *job.JobManager
    firewallMgr      *firewall.Manager
    storage          storage.Manager  // nil where no ZFS backend exists
    // ...
}
```

**Handler file organization:**
| File | Handlers |
|------|---------|
| `server.go` | Route registration, the `Datastore` interface, shared plumbing |
| `instance_handlers.go` | Instance CRUD, start/stop/restart, events |
| `exec_handlers.go` | Exec and console info |
| `firewall_handler.go` | Port expose/unexpose, NAT |
| `snapshot_handler.go` | Snapshot CRUD and restore |
| `clone_handler.go` | Clone |
| `checkpoint_handler.go` | Checkpoint create/restore/list/delete |
| `autostart_handler.go` | Auto-start read/set/disable, trigger |
| `backup_handlers.go` | Backup CRUD, verify, restore |
| `stack_handlers.go` | Stack deploy/destroy/start/stop |
| `job_handlers.go` | Async job status, cancel, list |
| `volume_handlers.go` | Volume management |
| `autostart_handler.go` | Autostart configuration |
| `middleware.go` | Auth, rate limiting, security headers, CORS |

**Route pattern:** All instance-scoped routes use `{id}` for the Hospitus
instance ID. Provider-scoped routes use `{provider}`. Example:
```
GET  /api/v1/providers                → list all providers
GET  /api/v1/providers/{name}         → provider details
GET  /api/v1/instances                → list all instances
POST /api/v1/instances                → create instance
GET  /api/v1/instances/{id}           → instance detail
POST /api/v1/instances/{id}/start     → start
POST /api/v1/instances/{id}/stop      → stop
POST /api/v1/instances/{id}/exec      → run command
POST /api/v1/instances/{id}/snapshots → create snapshot
POST /api/v1/instances/{id}/expose    → add port forwarding
GET  /api/v1/jobs/{id}                → job status
```

**`writeJSON` helper:**
```go
func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    json.NewEncoder(w).Encode(v)
}

func (s *Server) writeError(w http.ResponseWriter, status int, msg string) {
    s.writeJSON(w, status, map[string]string{"error": msg})
}
```

### `internal/auth`

Two authentication implementations, both satisfying `AuthProvider`:

**`AuthManager`** (production):
- Stores keys as bcrypt hashes.
- Constant-time comparison to prevent timing attacks.
- Key expiry, permissions, audit trail.
- Use when `APIKeys` is configured in server config.

**`SimpleAuthProvider`** (development / fail-closed):
- Stores plaintext keys in memory — never for production.
- Used in the **fail-closed** path: no keys configured and `--allow-no-auth`
  *not* set → an empty `SimpleAuthProvider` that rejects every request.
- When `--allow-no-auth` *is* set with no keys, auth is disabled entirely (the
  middleware is not wired) — loopback development only.

**TLS:** Native TLS via `--tls-cert` / `--tls-key` (`ListenAndServeTLS`,
`MinVersion` TLS 1.2). TLS is fail-closed: without certificates the daemon
refuses to start unless `--allow-insecure-tls` is passed (plain HTTP for
development). See [Authentication](authentication.md).

### `internal/datastore`

**SQLite-backed persistence.** The schema is versioned and migrated
automatically on startup using a migration runner in
`internal/datastore/migrations/`.

**Schema (key tables):**

```sql
-- Core instance record
CREATE TABLE instances (
    id          TEXT PRIMARY KEY,
    name        TEXT UNIQUE NOT NULL,
    provider    TEXT NOT NULL,
    state       TEXT NOT NULL,      -- running, stopped, creating, etc.
    spec        TEXT NOT NULL,      -- JSON-encoded InstanceSpec
    handle      TEXT NOT NULL,      -- JSON-encoded InstanceHandle
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at  TIMESTAMP,
    labels      TEXT,               -- JSON map
    annotations TEXT                -- JSON map
);

-- Async jobs
CREATE TABLE jobs (
    id          TEXT PRIMARY KEY,
    type        TEXT NOT NULL,
    status      TEXT NOT NULL,
    progress    REAL DEFAULT 0,
    -- ...
);
```

**Why not PostgreSQL?** SQLite requires zero infrastructure. Hospitus is
designed to run on a single FreeBSD server without a separate database
process. SQLite provides ACID transactions, good performance for the
expected scale (thousands of instances, not millions), and trivial backup
(just `cp hospitus.db hospitus.db.bak`).

**Encryption:** Sensitive fields (API keys, cloud-init passwords) are
encrypted at rest using AES-256-GCM via `internal/crypto`. The encryption
key is derived from a master key stored outside the database.

### `internal/client`

The HTTP client library used by the CLI. Every API endpoint has a
corresponding method:

```go
// internal/client/client.go
func (c *Client) CreateInstance(ctx context.Context, spec provider.InstanceSpec) (*provider.InstanceHandle, error) {
    var handle provider.InstanceHandle
    err := c.post(ctx, "/api/v1/instances", spec, &handle)
    return &handle, err
}
```

The client is abstracted behind `APIClientInterface` in
`cmd/hospitus-cli/internal/cmdutil/client.go`, which allows CLI tests to use
a mock client without making real HTTP calls.

### `pkg/provider`

The core abstraction layer. Key files:
- `provider.go`: `Provider` interface + all optional interfaces + all types
  (`InstanceSpec`, `InstanceHandle`, `ResourceSpec`, `Metrics`, etc.)
- `registry.go`: `Registry` type + global singleton.
- `errors.go`: Standard error types (`ErrInstanceNotFound`, `ErrInstanceExists`).

### `pkg/provider/jail`

The FreeBSD jail implementation. Files:

| File | Responsibility |
|------|---------------|
| `jail.go` | `JailProvider` struct, `Metadata()`, `Capabilities()`, `Initialize()` |
| `lifecycle.go` | `CreateInstance()` with TOCTOU protection, `RestartInstance()`, `umountJail()` |
| `state.go` | `GetInstanceState()`, `GetInstanceInfo()`, `GetInstanceMetrics()`, `CheckInstanceHealth()` |
| `console.go` | `GetConsole()` (PTY via jexec), `ExecCommand()` |
| `network_manager.go` | Bridge management, epair creation, IP allocation, NAT, DNS |
| `snapshot.go` | ZFS snapshot operations |
| `clone.go` | ZFS clone operations |
| `tmux.go` | Persistent tmux sessions for jails |
| `parameters.go` | `JailParameters` struct, `ParseJailParametersFromMap()`, `ToJailArgs()` |
| `jail_rctl.go` | RCTL resource limit application |
| `hooks.go` | Lifecycle hooks (pre/post start/stop) with path validation |
| `config.go` | Jail config file building |
| `crossarch.go` | Cross-architecture QEMU emulation setup |
| `volumes.go` | nullfs volume mount management |
| `dhcp.go` | DHCP lease tracking for jails |
| `autostart.go` | `AutoStartProvider` implementation |
| `services.go` | Service management inside jails |

**TOCTOU protection in `CreateInstance`:**

TOCTOU stands for "Time Of Check, Time Of Use". Without synchronisation:
1. Request A checks if jail "web" exists → no
2. Request B checks if jail "web" exists → no
3. Request A creates jail "web"
4. Request B creates jail "web" → name collision or corrupt state

`createMu` prevents this:
```go
func (p *JailProvider) CreateInstance(...) {
    p.createMu.Lock()
    defer p.createMu.Unlock()
    // Sequential from here — only one create at a time
    if p.jailExists(spec.Name) {
        return nil, provider.ErrInstanceExists
    }
    // safe to create
}
```

**`parameters.go` — `JailParameters`:**

FreeBSD's `jail(8)` has hundreds of parameters. `JailParameters` is a Go
struct that mirrors all supported parameters. `ToJailArgs()` converts it to
a `[]string` suitable for passing to `exec.Command("jail", ...)`.

`ParseJailParametersFromMap()` converts a `map[string]interface{}` (which
comes from JSON or a decoded TOML manifest) into a `JailParameters`. The switch-case in this
function is the single canonical mapping between parameter name strings
and struct fields.

### `pkg/provider/bhyve`

bhyve VM implementation, split into logical files:

| File | Responsibility |
|------|---------------|
| `bhyve_provider.go` | Provider struct, metadata, capabilities, init, OS detection |
| `bhyve_lifecycle.go` | Create, delete, start (builds bhyve command), stop |
| `bhyve_storage.go` | ZFS zvol creation, ISO attach, cloud-init ISO generation |
| `bhyve_boot.go` | Boot priority, CPU affinity |
| `bhyve_clone.go` | Clone via ZFS snapshot |
| `bhyve_checkpoint.go` | bhyve checkpoint/restore (experimental) |
| `bhyve_traffic.go` | Traffic statistics |

**Starting a VM** in bhyve is complex — the bhyve command line has many
flags. For a typical Linux VM:

```sh
bhyve \
  -c 2 \             # 2 vCPUs
  -m 2G \            # 2 GB RAM
  -H \               # yield CPU to host when idle
  -P \               # exit on PAUSE instruction (clean shutdown)
  -s 0,hostbridge \  # PCI slot 0: host bridge
  -s 1,lpc \         # PCI slot 1: LPC (for COM ports)
  -l com1,/dev/nmdm0A \  # Serial console via nmdm
  -s 2,virtio-blk,/dev/zvol/zroot/hospitus/bhyve/web/root \  # disk
  -s 3,virtio-net,tap0 \  # network
  -s 4,fbuf,tcp=0.0.0.0:5900 \  # VNC
  -l bootrom,/usr/local/share/uefi-firmware/BHYVE_UEFI.fd \  # UEFI
  web           # VM name
```

Hospitus builds this command dynamically in `bhyve_lifecycle.go`.

### `pkg/provider/qemu`

The QEMU provider is more portable than bhyve — it works on FreeBSD, Linux,
and macOS. It uses the `qemu-system-*` family of binaries.

**QMP (QEMU Machine Protocol):** QEMU exposes a JSON-RPC control socket
called QMP. Hospitus uses this for clean shutdown (`system_powerdown`),
pause/resume, and status queries. See `qmp.go`.

**Cross-platform:** On macOS, QEMU uses HVF (Hypervisor.framework) for
near-native performance. On FreeBSD with bhyve support, it can use that
too. On Linux, it uses KVM.

### `pkg/provider/podman`

The Podman provider drives the `podman(1)` command line through the
`execx.Runner` abstraction — it does not talk to the Podman REST socket. That
keeps it testable with `execx.Fake` and matches how Podman is used on FreeBSD.

### `pkg/firewall`

Firewall abstraction supporting multiple backends:

```go
// pkg/firewall/backend.go
type Backend interface {
    Name() BackendType
    IsAvailable() bool
    Initialize(ctx context.Context) error
    AddPortMapping(ctx context.Context, mapping PortMapping) error
    RemovePortMapping(ctx context.Context, mapping PortMapping) error
    AddNATRule(ctx context.Context, rule NATRule) error
    RemoveNATRule(ctx context.Context, rule NATRule) error
    // ...
}
```

**`PFBackend`** (`pf.go`): The primary backend for FreeBSD. Generates PF
rule files and reloads the `hospitus` anchor.

**`DetectBackend()`** auto-detects the appropriate backend:
- FreeBSD → PF (if `pfctl` is available and PF is enabled)
- Linux → nftables (planned) or iptables
- macOS → none (QEMU/Podman handle their own NAT)

**Rule file management:** Each instance has its own rules file in
`/var/lib/hospitus/firewall/pf/<name>.conf`. The main anchor file
`hospitus.conf` contains `include` directives for each instance file. This
makes it easy to add/remove rules per-instance without touching other
instances' rules.

**Atomic writes:** Rule files are written atomically (temp file + rename)
to prevent pfctl from reading a partial file during a reload.

### `pkg/job`

Async job manager. Core types:

```go
type JobStatus string
const (
    JobStatusPending   JobStatus = "pending"
    JobStatusRunning   JobStatus = "running"
    JobStatusCompleted JobStatus = "completed"
    JobStatusFailed    JobStatus = "failed"
    JobStatusCancelled JobStatus = "canceled"
)

type Job struct {
    ID          string
    Type        string      // "instance.create", "image.fetch", etc.
    Description string
    Status      JobStatus
    Progress    float64     // 0.0 to 1.0
    Message     string
    Result      map[string]any
    Error       string
    CreatedAt   time.Time
    StartedAt   *time.Time
    CompletedAt *time.Time
    Metadata    map[string]string
    // Internal:
    mu     sync.RWMutex
    cancel context.CancelFunc
    done   chan struct{}
}
```

**`UpdateProgress`** is called from the job function to report progress:
```go
func (j *Job) UpdateProgress(progress float64, message string) {
    j.mu.Lock()
    defer j.mu.Unlock()
    j.Progress = progress
    j.Message = message
}
```

**Cancellation:** Each job gets a `context.CancelFunc`. The cancel button
on `DELETE /api/v1/jobs/{id}` calls this function, which propagates
cancellation to any `exec.CommandContext` running inside the job.

**Persistence:** Jobs are saved to SQLite so they survive daemon restarts.
On startup, pending/running jobs are marked as `failed` (since we don't
know their actual state after a crash).

### `pkg/image`

Image catalog and download management.

**Embedded catalog:** `catalog.json` is embedded at compile time
(`//go:embed catalog.json`). It describes all available base images:
FreeBSD releases, Ubuntu cloud images, etc.

**Image naming convention:**
- `14.3-RELEASE-amd64` → FreeBSD base system set (`.txz` archive)
- `cloud:ubuntu-24.04-amd64` → Ubuntu cloud image (`.qcow2`)
- `iso:debian-12-netinst-amd64` → Debian installer ISO

**Download and extraction:**
1. Download to a temp file in the images directory.
2. Verify SHA256 checksum.
3. Extract archives with `tar -xf` — bsdtar detects the compression itself, so
   no per-format flag is passed.
4. Register the extracted base in the catalog.

### `pkg/manifest`

**Unified Workload Manifests (UWM)** are **TOML** files (`api_version =
"hospitus.io/v1"`) that describe an instance declaratively. The parser uses Go's
`text/template` first (for variable and secret substitution), then decodes the
result as TOML. A single-instance workload:

```toml
# webserver.toml
[workload]
api_version = "hospitus.io/v1"
name = "web"

[provider]
type = "jail"

[image]
source = "freebsd:14.3-RELEASE"
arch   = "amd64"

[resources]
cpu    = 2
memory = "1G"

[[networks]]
name   = "public"
type   = "bridge"
bridge = "hospitus0"
```

Template functions available in manifests are `default`, `env` (whitelisted
variables only), `randHex`, `randAlnum`, and `secret` (registered only when a
secret store is configured). See [Variables & Secrets](../uwm/variables.md) and
the authoritative [UWM Specification](../uwm/spec.md) for the full schema.

**`convert.go`** is the translation layer. `ToInstanceSpec()` converts a
`WorkloadManifest` into a `provider.InstanceSpec` — where defaults, type
marshalling, and validation happen. For VMs, a `[cloud_init]` section is rendered
and embedded in a seed ISO.

**Stack manifests** declare multiple interconnected instances with a `[stack]`
table and repeated `[[instances]]` tables; each instance can declare
`depends_on` with a `condition` of `started` or `healthy`. `ToInstanceSpecs()`
expands a stack into per-instance specs, and `pkg/orchestration` starts them in
dependency order.

### `pkg/orchestration`

The stack manager coordinates multi-instance deployments. Key concepts:

**`Stack`:** A named group of `StackInstance` objects with dependency
relationships.

**Dependency ordering:** Instances with `dependsOn` are started after their
dependencies. This uses topological sort of the dependency graph.

**Health checking:** The `HealthChecker` periodically polls instance health
and marks the stack `degraded` if any instance becomes unhealthy.

**Service registry:** The `ServiceRegistry` tracks which services are
registered and their current status/health, allowing instances to
discover each other.

### `pkg/backup`

Automated backup manager. Supports three backup types:

| Type | Mechanism | Use case |
|------|-----------|----------|
| `snapshot` | ZFS `zfs snapshot` | Fast, local, zero-copy |
| `full` | `zfs send` to file | Portable, send off-host |
| `incremental` | `zfs send -i` | Smaller than full, requires base |

**Retention policies:** Configurable per-instance:
- `keep_last`: Always keep this many most recent backups.
- `keep_daily`: Keep one backup per day for this many days.
- `keep_weekly`: Keep one per week for N weeks.

**Scheduled backups:** Via cron-style schedule strings (`"0 2 * * *"` for
daily at 2am). The backup manager has an internal ticker that evaluates
schedules.

### `pkg/logging`

Thin wrappers around `log/slog`:

```go
// Standard field names (prevents typos and ensures consistency)
const (
    FieldProvider  = "provider"
    FieldInstance  = "instance"
    FieldVM        = "vm"
    FieldComponent = "component"
    FieldAction    = "action"
    FieldError     = "err"
    FieldTaskID    = "task_id"
)

// Context-attached logger constructors
func WithComponent(name string) *slog.Logger {
    return slog.Default().With("component", name)
}

func WithProvider(name string) *slog.Logger {
    return slog.Default().With("provider", name)
}

func WithInstance(provider, instance string) *slog.Logger {
    return defaultLogger.Load().With(FieldProvider, provider, FieldInstance, instance)
}
```

### `pkg/validation`

Input validation used by provider code and API handlers:

```go
// ValidateInstanceName enforces 1-63 characters from [a-zA-Z0-9_-],
// starting with an alphanumeric
func ValidateInstanceName(name string) error

// ValidateExecCommand rejects dangerous shell constructs (`;`, `&&`, pipes,
// redirections, substitutions) to prevent command injection in lifecycle hooks
func ValidateExecCommand(cmd string) error

// ValidateInterfaceName ensures interface names are safe for use in shell commands
func ValidateInterfaceName(name string) error
```

`ValidateExecCommand` is particularly important because jail lifecycle hooks
(`exec.prestart`, `exec.start`, etc.) are passed directly to `jail(8)`,
which executes them as shell commands. Without validation, a malicious
manifest could inject arbitrary commands.

### `pkg/config`

Parser for `hospitusd.conf`. The config file uses a simple `key = value`
format (no TOML/YAML/JSON to minimize dependencies for this critical file):

```ini
# hospitusd.conf
ip_pool = 10.0.0.0/24
enable_nat = true
external_interface = em0
firewall_type = pf
bridge_prefix = hospitus
enable_ipv6 = false
```

### `pkg/cloudinit`

cloud-init is a standard mechanism for initializing cloud VMs on first
boot. It reads configuration from a "seed" — either a mounted ISO or a
metadata service endpoint.

Hospitus generates cloud-init config in two formats:
- **NoCloud:** A seed ISO containing `meta-data` and `user-data` files.
- **ConfigDrive:** An alternative format for OpenStack-compatible images.

The `Config` struct covers:
```go
type Config struct {
    Hostname    string
    Users       []User
    Packages    []string
    RunCommands []string
    SSHKeys     []string
    Network     string   // cloud-init network-config YAML
}
```

### `pkg/network`

Platform-specific network helper functions. Split by build tags:
- `freebsd.go`: FreeBSD-specific (uses `ifconfig`, `route`)
- `darwin.go`: macOS-specific (minimal, for QEMU/Podman)
- `linux.go`: Linux-specific (future)

These are used by providers to abstract platform differences.

---

## 7. Data Flows

### 7.1 Creating a Jail (Synchronous)

```
User: hospitus jail create web --image 14.3-RELEASE-amd64 --vnet --ip dhcp --cpus 2 --memory 1024

CLI (hospitus):
  1. Parse flags → build InstanceSpec
  2. POST /api/v1/instances (JSON body: InstanceSpec)
  3. Print response

API Server (hospitusd):
  4. Auth middleware: validate X-API-Key header
  5. Rate limit middleware: check IP rate limit
  6. handleCreateInstance():
     a. Decode InstanceSpec from body
     b. Validate name (alphanumeric, max 63 chars)
     c. Check name not already in datastore
     d. registry.Get("jail") → JailProvider
     e. Save instance to datastore with state=creating
     f. Call JailProvider.CreateInstance(ctx, spec)

JailProvider.CreateInstance():
  7. createMu.Lock() — serialise creation
  8. Build jail config (ZFS dataset path, network, etc.)
  9. Create ZFS dataset: zfs create zroot/hospitus/jails/web
 10. Extract base system: tar -xf /var/cache/hospitus/14.3-RELEASE-amd64.txz -C /zroot/hospitus/jails/web/root
 11. Write /etc/rc.conf, /etc/resolv.conf inside jail root
 12. Setup networking: create epair, add to bridge, configure NAT in PF
 13. Apply RCTL limits (if configured)
 14. Run lifecycle hooks (exec.prestart if set)
 15. Start jail: jail -c name=web ...
 16. createMu.Unlock()
 17. Return InstanceHandle{ID: "web", Provider: "jail"}

API Server:
 18. Update datastore: state=running, handle=InstanceHandle
 19. Write JSON response: 201 Created {id: "abc123", name: "web", state: "running"}

CLI:
 20. Print: "✓ Jail 'web' created (abc123)"
```

### 7.2 Creating a bhyve VM (Async Job)

```
User: hospitus bhyve create myvm --image cloud:ubuntu-24.04-amd64 --cpus 4 --memory 4096

CLI:
  1. POST /api/v1/instances (InstanceSpec with image=cloud:ubuntu-24.04-amd64)

API Server:
  2. Validate + get bhyve provider
  3. Submit job:
     s.submitJob(ctx, "instance.create", "Creating VM myvm", metadata,
         func(ctx, j) { bhyveProvider.CreateInstance(ctx, spec) })
  4. Return 202 Accepted {job_id: "job-xyz"}

CLI:
  5. Poll GET /api/v1/jobs/job-xyz every 2 seconds
  6. Print progress bar based on job.Progress

Background goroutine:
  7. BhyveProvider.CreateInstance():
     a. j.UpdateProgress(0.1, "Creating ZFS volumes...")
     b. zfs create -V 20G zroot/hospitus/bhyve/myvm/root
     c. j.UpdateProgress(0.3, "Downloading cloud image...")
     d. Download ubuntu-24.04.qcow2 (large file)
     e. j.UpdateProgress(0.7, "Generating cloud-init seed...")
     f. Create cloud-init seed ISO
     g. j.UpdateProgress(0.9, "Starting VM...")
     h. exec bhyve ... myvm
     i. j.UpdateProgress(1.0, "Done")
  8. Job status → completed

CLI:
  9. Job complete → print "✓ VM 'myvm' created"
```

### 7.3 Snapshot Lifecycle

```
User: hospitus jail snapshot create web before-upgrade

CLI: POST /api/v1/instances/web/snapshots {"name": "before-upgrade"}

API:
  1. Get instance from datastore → handle
  2. registry.Get("jail") → JailProvider
  3. sp, ok := prov.(provider.SnapshotProvider)
  4. sp.CreateSnapshot(ctx, handle, "before-upgrade")

JailProvider.CreateSnapshot():
  5. zfs snapshot zroot/hospitus/jails/web@before-upgrade
  6. Return SnapshotInfo{Name: "before-upgrade", CreatedAt: now}

--- Later ---

User: hospitus jail snapshot restore web before-upgrade

JailProvider.RestoreSnapshot():
  7. Stop jail if running: jail -r web
  8. zfs rollback zroot/hospitus/jails/web@before-upgrade
  9. Restart jail: jail -c ...
```

---

## 8. Security Architecture

### 8.1 Authentication

```
Client → X-API-Key: hospitus_live_abc123...
         ↓
authMiddleware
    AuthManager.ValidateKey(key)
    → bcrypt.CompareHashAndPassword(storedHash, key)
    → constant-time safe comparison
```

**Key lifecycle:**
1. A key is generated: by the rc script on first start, by `POST
   /api/v1/auth/keys`, or handed to the daemon through `--api-key`,
   `--api-key-file` or `HOSPITUS_API_KEY`. There is no `hospitus api-key`
   command.
2. The raw key is shown **once** (never stored plaintext).
3. The bcrypt hash is stored in config/datastore.
4. Subsequent `ValidateKey` calls compare the submitted key against the hash.

### 8.2 Rate Limiting

Two rate limiters using `golang.org/x/time/rate` (token bucket algorithm):
- **API rate limiter:** N requests/second per IP (default 10 req/s, burst 20 —
  `--rate-limit-rps` and `--rate-limit-burst`).
- **Auth rate limiter:** Stricter limit on auth failures per IP (prevents brute force).

Rate limiters are stored per-IP in a map with periodic cleanup of stale entries.

### 8.3 Command Injection Prevention

Lifecycle hooks (shell commands that run on jail start/stop) are validated
before being stored:

```go
// pkg/validation/validation.go
func ValidateExecCommand(cmd string) error {
    dangerous := []string{";", "&&", "||", "|", ">", "<", "`", "$(", "${"}
    for _, pattern := range dangerous {
        if strings.Contains(cmd, pattern) {
            return fmt.Errorf("command contains forbidden pattern: %q", pattern)
        }
    }
    // Also check absolute path requirement
    if !strings.HasPrefix(cmd, "/") {
        return fmt.Errorf("command must use absolute path")
    }
    return nil
}
```

### 8.4 Interface Name Validation

PF rules include interface names that come from user input. A malicious
interface name could inject PF syntax. `ValidateInterfaceName` ensures names
contain only safe characters.

### 8.5 TLS

TLS is fail-closed. The certificate and key are given with `--tls-cert` and
`--tls-key`; without them the daemon **refuses to start** rather than falling
back to plain HTTP. The single opt-out is `--allow-insecure-tls`, for
development.

### 8.6 Encryption at Rest

Sensitive datastore fields are encrypted with AES-256-GCM (`internal/crypto`).
The encryption key is derived from a master key file, not stored in the
database. Loss of the master key means loss of encrypted data.

---

## 9. Testing Strategy

### 9.1 Test Pyramid

```
Integration tests (test/integration/)   ← Few, require root + FreeBSD
   ↑
API handler tests (internal/api/)       ← Many, in-process, no OS calls
   ↑
Unit tests (pkg/...)                    ← Most, pure Go, no OS calls
```

### 9.2 Pure-Go Unit Tests

Most packages have tests that require no external tools:
- `pkg/firewall/pf_pure_test.go`: Tests PF rule generation with a temp directory
  — no `pfctl` needed.
- `pkg/provider/jail/parameters_test.go`: Tests `JailParameters` building.
- `pkg/provider/jail/crossarch_test.go`: Tests architecture detection.
- `pkg/manifest/convert_test.go`: Tests manifest → InstanceSpec conversion.

### 9.3 API Handler Tests

`internal/api/` tests use `httptest.NewRecorder` and `httptest.NewRequest`
to test handlers without a real network:

```go
func TestHandleCreateInstance_InvalidName(t *testing.T) {
    srv, ds := setupTestServer(t)
    defer ds.Close()

    body := `{"name": "INVALID-NAME!", "provider": "jail"}`
    req := httptest.NewRequest(http.MethodPost, "/api/v1/instances",
        bytes.NewBufferString(body))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()

    srv.mux.ServeHTTP(w, req)

    if w.Code != http.StatusBadRequest {
        t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
    }
}
```

The `setupTestServer()` helper creates a real `Server` with an in-memory
SQLite database and a mock provider registry.

### 9.4 Mock Provider

`internal/api` tests use an unexported `mockProvider` that satisfies the
`Provider` interface with configurable return values; capability-specific mocks
(`mockCheckpointProvider`, `mockPauseProvider`, …) embed it and add the optional
interface under test:

```go
type mockProvider struct {
    createFn func(ctx context.Context, spec provider.InstanceSpec) (provider.InstanceHandle, error)
    // ...
}

func (m *mockProvider) CreateInstance(ctx context.Context, spec provider.InstanceSpec) (provider.InstanceHandle, error) {
    if m.createFn != nil {
        return m.createFn(ctx, spec)
    }
    return provider.InstanceHandle{ID: spec.Name, Provider: "mock"}, nil
}
```

`InstanceHandle` has no `Name` field — the identity is `ID`, plus `Provider` and
a free-form `Metadata` map.

### 9.5 Integration Tests

`test/integration/` requires:
- FreeBSD with ZFS
- `doas` configured for the test user
- HOSPITUS_LONG_TESTS=1 for tests that download large images

Run with: `doas go test -v ./test/integration/...`

### 9.6 Coverage

```sh
make test-coverage    # writes coverage.out + coverage.html
```

The lowest-coverage areas are the provider implementations, which need FreeBSD
to exercise meaningfully. See [Testing](testing.md) for the conventions and the
current targets.

---

## 10. FreeBSD-Specific Deep Dives

### 10.1 The Jail Creation Sequence (Step by Step)

When `hospitus jail create web --image 14.3-RELEASE-amd64 --vnet --ip dhcp --cpus 2` is run:

1. **API receives InstanceSpec**
   ```json
   {"name": "web", "provider": "jail", "cpus": 2, "image": "14.3-RELEASE-amd64", "vnet": true}
   ```

2. **ZFS dataset creation**
   ```sh
   zfs create -p zroot/hospitus/jails/web
   # Mounts at /zroot/hospitus/jails/web
   ```

3. **Base system extraction**
   ```sh
   tar -xf /var/cache/hospitus/bases/14.3-RELEASE-amd64/base.txz \
       -C /zroot/hospitus/jails/web/root
   # Extracts /bin, /lib, /usr, /etc, etc. into the jail root
   ```

4. **Minimal host configuration**
   ```sh
   # Write /etc/rc.conf inside the jail
   echo 'hostname="web"' >> /zroot/hospitus/jails/web/root/etc/rc.conf
   echo 'sshd_enable="NO"' >> /zroot/hospitus/jails/web/root/etc/rc.conf
   ```

5. **DNS configuration**
   ```sh
   # Copy resolv.conf from host or use Hospitus defaults
   cp /etc/resolv.conf /zroot/hospitus/jails/web/root/etc/resolv.conf
   ```

6. **VNET network setup**
   ```sh
   # Create epair
   ifconfig epair create                    # returns "epair0a"
   # epair0b is the jail-side

   # Bring both up
   ifconfig epair0a up
   ifconfig epair0b up

   # Add host side to bridge
   ifconfig hospitus0 addm epair0a

   # PF NAT rule to allow jail traffic
   echo "nat on em0 from 10.0.0.0/24 to any -> (em0)" \
       > /var/lib/hospitus/firewall/pf/web.conf
   pfctl -a hospitus -f /var/lib/hospitus/firewall/pf/hospitus.conf
   ```

7. **Write jail.conf**
   ```ini
   web {
       host.hostname = "web";
       path = "/zroot/hospitus/jails/web/root";
       vnet;
       vnet.interface = "epair0b";
       exec.start = "/bin/sh /etc/rc";
       exec.stop = "/bin/sh /etc/rc.shutdown";
       mount.devfs;
   }
   ```

8. **Start jail**
   ```sh
   jail -c -f /var/lib/hospitus/state/web.conf
   ```

9. **Configure jail network from inside**
   ```sh
   # Inside jail, configure the jail-side epair
   jexec web ifconfig epair0b inet 10.0.0.2/24
   jexec web route add default 10.0.0.1
   ```

10. **Apply RCTL limits**
    ```sh
    rctl -a jail:web:pcpu:deny=200     # 2 CPUs = 200%
    rctl -a jail:web:memoryuse:deny=0  # (if memory not set, no limit)
    ```

### 10.2 PF Anchor Architecture

The complete PF setup created by Hospitus:

```
/etc/pf.conf (managed by admin):
    anchor "hospitus"
    nat-anchor "hospitus"

/var/lib/hospitus/firewall/pf/hospitus.conf (managed by Hospitus):
    include "/var/lib/hospitus/firewall/pf/web.conf"
    include "/var/lib/hospitus/firewall/pf/db.conf"

/var/lib/hospitus/firewall/pf/web.conf:
    nat on em0 from 10.0.0.2 to any -> (em0)
    rdr on em0 proto tcp from any to any port 8080 -> 10.0.0.2 port 80

/var/lib/hospitus/firewall/pf/db.conf:
    nat on em0 from 10.0.0.3 to any -> (em0)
```

When a jail is destroyed, its `.conf` file is deleted and the anchor is
reloaded. Hospitus never changes `/etc/pf.conf` — you declare the `hospitus` anchor
there once; from then on Hospitus only writes the anchor files under
`/var/lib/hospitus/firewall/pf/` and loads them into the anchor with `pfctl -a hospitus -f`.

### 10.3 ZFS Dataset Hierarchy

```
zroot                           ← ZFS pool
  hospitus                         ← Hospitus root (zfs create zroot/hospitus)
    jails                       ← All jails
      web                       ← Jail "web" dataset
        root                    ← Jail root filesystem
      db
        root
    bases                       ← Base system archives
      14.3-RELEASE-amd64        ← Extracted base
    backups                     ← Backup storage
      web                       ← Backups for "web"
        2024-01-15T02:00:00Z    ← A backup archive
    bhyve                       ← bhyve VMs (zvols live here)
      myvm
        disk0                   ← zvol for the first disk
```

**ZFS properties used by Hospitus:**
```sh
zfs set compression=lz4 zroot/hospitus/jails   # Compress all jail data
zfs set atime=off zroot/hospitus               # No access time updates (performance)
```

### 10.4 IPv6 Address Derivation

Hospitus derives stable ULA IPv6 addresses for jails without needing a DHCP
server:

```go
// pkg/provider/jail/network_manager.go
func (nm *NetworkManager) allocateIPv6(jailName string) string {
    // ULA prefix: fd00::/8 + 40 random bits = /48
    prefix := nm.config.IPv6Prefix  // e.g., "fd00::/48"

    // Derive /64 subnet from jail name hash
    h := sha256.Sum256([]byte(jailName))
    subnetID := binary.BigEndian.Uint16(h[:2])  // 16 bits = 65536 /64 subnets

    // Host portion: deterministic from remaining hash bytes
    hostID := binary.BigEndian.Uint64(h[2:10])

    // Build the IPv6 address
    return fmt.Sprintf("fd00:%04x::%016x/64", subnetID, hostID)
}
```

The prefix `fd00::/8` is the IANA-assigned range for Unique Local Addresses
(RFC 4193). They're globally routable within an organization but not on
the public internet — perfect for internal jail networking.

---

## Appendix: Key Decisions Log

| Decision | Rationale |
|----------|-----------|
| Standard library HTTP | Avoids framework bloat; Go 1.22 pattern-matching is sufficient |
| SQLite over PostgreSQL | Zero-infra deployment; suitable for <10k instances |
| Bcrypt for API keys | Industry standard; timing-attack safe |
| PF anchors (not main ruleset) | Safety: Hospitus rules isolated, admin ruleset untouched |
| ZFS as only storage backend | Snapshots/clones are first-class; no abstraction needed |
| `createMu` instead of DB transaction | TOCTOU with ZFS/jail commands outside DB; mutex is simpler and correct |
| Async jobs for long ops | Prevents HTTP timeout on slow operations (image download, base extraction) |
| Hard RCTL failures | User configured a limit — silently ignoring it would be dangerous |
| Go embed for catalog.json | Self-contained binary; no install-time data files needed |
| `log/slog` (stdlib) | No dependency, structured, compatible with all log aggregators |
| `doas` in documentation | Standard FreeBSD privilege escalation; `sudo` is a port, not base |
| No cluster/raft yet | Raft without snapshots is unusable; single-node is solid first |
