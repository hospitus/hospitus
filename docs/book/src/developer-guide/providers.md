# The Provider Interface

Every virtualization backend in Hospitus — jails, bhyve, QEMU, Podman, and on
macOS vfkit and Apple `container` — is a
**provider**: a Go type that implements the `Provider` interface defined in
[`pkg/provider/provider.go`](https://github.com/hospitus/hospitus/blob/main/pkg/provider/provider.go).
The API server, CLI, job manager, and datastore never call a backend directly.
They talk to the `Provider` interface, so adding a new virtualization technology
is (mostly) a matter of writing one more implementation and registering it.

This page is the reference for that contract: the mandatory interface, the
optional capability interfaces, which providers implement what, and a
step-by-step guide to writing your own.

---

## The core `Provider` interface

Everything in the mandatory interface must be implemented by every provider,
even if the answer is "not supported". The signatures below are verbatim from
`pkg/provider/provider.go`.

```go
type Provider interface {
    // Identity
    Metadata() ProviderMetadata

    // Lifecycle of the provider itself
    Initialize(ctx context.Context, config ProviderConfig) error
    Shutdown(ctx context.Context) error
    HealthCheck(ctx context.Context) error

    // Instance lifecycle
    CreateInstance(ctx context.Context, spec InstanceSpec) (InstanceHandle, error)
    DeleteInstance(ctx context.Context, handle InstanceHandle, force bool) error
    StartInstance(ctx context.Context, handle InstanceHandle) error
    StopInstance(ctx context.Context, handle InstanceHandle, opts StopOptions) error
    RestartInstance(ctx context.Context, handle InstanceHandle) error

    // State and inventory
    GetInstanceState(ctx context.Context, handle InstanceHandle) (InstanceState, error)
    GetInstanceInfo(ctx context.Context, handle InstanceHandle) (InstanceInfo, error)
    ListInstances(ctx context.Context, filter InstanceFilter) ([]InstanceHandle, error)

    // Resources and metrics
    SetInstanceResources(ctx context.Context, handle InstanceHandle, resources ResourceSpec) error
    GetInstanceMetrics(ctx context.Context, handle InstanceHandle) (Metrics, error)

    // Hot-plug storage and networking
    AttachDisk(ctx context.Context, handle InstanceHandle, disk DiskAttachment) error
    DetachDisk(ctx context.Context, handle InstanceHandle, diskID string) error
    AttachNetwork(ctx context.Context, handle InstanceHandle, network NetworkAttachment) error
    DetachNetwork(ctx context.Context, handle InstanceHandle, interfaceID string) error

    // Feature discovery
    Capabilities() ProviderCapabilities
}
```

### Why the interface is shaped this way

- **Context first, everywhere.** Every method that can block, spawn a
  subprocess, or run for a while takes `context.Context` as its first argument.
  Cancelling the context (client disconnect, job cancellation, daemon shutdown)
  propagates all the way down to the `exec.CommandContext` that runs `jail(8)`
  or `bhyve(8)`.
- **Opaque handles.** Callers never construct provider-internal identifiers.
  `CreateInstance` returns an `InstanceHandle`, and every later call passes that
  handle back. The handle carries only `ID`, `Provider`, and a `Metadata` map —
  the provider is free to store whatever it needs on disk or in the datastore.
- **`Capabilities()` for coarse discovery, optional interfaces for real
  dispatch.** `Capabilities()` returns a `ProviderCapabilities` struct the UI
  can use to grey out unavailable buttons. The *actual* runtime dispatch, though,
  is done with Go type assertions against the optional interfaces below — that is
  the source of truth for "does this provider really support snapshots".

### Key data types

| Type | Role | Notable fields |
|------|------|----------------|
| `InstanceSpec` | Desired state passed to `CreateInstance` | `Name`, `Description`, `CPUs int`, `MemoryMB int64`, `MaxProc`, `ReadBPS/WriteBPS/ReadIOPS/WriteIOPS`, `Image`, `OSType`, `OSVersion`, `Arch`, `CloudInit`, `Bootloader`, `Disks []DiskSpec`, `Networks []NetworkSpec`, `ProviderConfig map[string]interface{}`, `Labels`, `Annotations` |
| `InstanceHandle` | Runtime identity returned by `CreateInstance` | `ID string`, `Provider string`, `Metadata map[string]interface{}` |
| `InstanceState` | Current lifecycle state (a `string` enum) | see below |
| `StopOptions` | Passed to `StopInstance` | `Force bool`, `Timeout time.Duration` |
| `ProviderMetadata` | Returned by `Metadata()` | `Name`, `Type ProviderType`, `Version`, `Author`, `Description` |

`InstanceState` is one of the constants defined in `provider.go`:

```
unknown  creating  stopped  starting  running
paused   stopping  migrating  deleting  error
```

`ProviderType` is either `ProviderTypeVM` (`"vm"`) or `ProviderTypeContainer`
(`"container"`). The `ProviderConfig` map on `InstanceSpec` is the escape hatch
for backend-specific knobs (for example bhyve boot firmware, QEMU machine type)
that do not belong in the shared struct.

---

## Optional capability interfaces

A provider advertises extra features by implementing additional interfaces.
The API server checks for them with a type assertion, e.g.:

```go
prov, _ := s.registry.Get("jail")
if sp, ok := prov.(provider.SnapshotProvider); ok {
    // this provider really supports snapshots
    _, err := sp.CreateSnapshot(ctx, handle, "before-upgrade")
} else {
    s.writeError(w, http.StatusNotImplemented, "provider does not support snapshots")
}
```

If the assertion fails, the endpoint returns `501 Not Implemented` — the
provider does not have that method. This is how one code path serves six
very different backends without a `switch` on provider name.

All 16 optional interfaces, verbatim from `pkg/provider/provider.go`:

```go
type SnapshotProvider interface {
    CreateSnapshot(ctx context.Context, handle InstanceHandle, name string) (SnapshotHandle, error)
    DeleteSnapshot(ctx context.Context, snapshot SnapshotHandle) error
    RestoreSnapshot(ctx context.Context, handle InstanceHandle, snapshot SnapshotHandle) error
    ListSnapshots(ctx context.Context, handle InstanceHandle) ([]SnapshotInfo, error)
}

type CloneProvider interface {
    CloneInstance(ctx context.Context, source InstanceHandle, cloneName string, opts CloneOptions) (InstanceHandle, error)
    CloneFromSnapshot(ctx context.Context, snapshot SnapshotHandle, cloneName string, opts CloneOptions) (InstanceHandle, error)
}

type ConsoleProvider interface {
    GetConsole(ctx context.Context, handle InstanceHandle) (ConsoleConnection, error)
    ResizeConsole(ctx context.Context, handle InstanceHandle, width, height int) error
}

type ExecProvider interface {
    ExecCommand(ctx context.Context, handle InstanceHandle, opts ExecOptions) (*ExecResult, error)
    ExecInteractive(ctx context.Context, handle InstanceHandle, opts ExecOptions) error
}

type ExecStreamingProvider interface {
    ExecProvider // embeds ExecProvider
    ExecCommandStream(ctx context.Context, handle InstanceHandle, opts ExecOptions, stdout, stderr io.Writer) (int, error)
}

type ExportImportProvider interface {
    ExportInstance(ctx context.Context, handle InstanceHandle, exportPath string, opts ExportOptions) error
    ImportInstance(ctx context.Context, importPath string, opts ImportOptions) (InstanceHandle, error)
}

type AutoStartProvider interface {
    SetAutoStart(ctx context.Context, handle InstanceHandle, config AutoStartConfig) error
    GetAutoStart(ctx context.Context, handle InstanceHandle) (*AutoStartConfig, error)
    ListAutoStartInstances(ctx context.Context) ([]InstanceHandle, error)
    StartAutoStartInstances(ctx context.Context) error
}

type InstanceHealthCheckProvider interface {
    CheckInstanceHealth(ctx context.Context, handle InstanceHandle) (*InstanceHealth, error)
}

type MediaProvider interface {
    InsertMedia(ctx context.Context, handle InstanceHandle, media MediaSpec) error
    EjectMedia(ctx context.Context, handle InstanceHandle, deviceID string) error
    ListMedia(ctx context.Context, handle InstanceHandle) ([]MediaInfo, error)
    SetBootOrder(ctx context.Context, handle InstanceHandle, order BootOrder) error
    GetBootOrder(ctx context.Context, handle InstanceHandle) (*BootOrder, error)
}

type CheckpointProvider interface {
    CheckpointInstance(ctx context.Context, handle InstanceHandle, name string) error
    RestoreCheckpoint(ctx context.Context, handle InstanceHandle, name string) error
    ListCheckpoints(ctx context.Context, handle InstanceHandle) ([]CheckpointInfo, error)
    DeleteCheckpoint(ctx context.Context, handle InstanceHandle, name string) error
}

type PauseProvider interface {
    PauseInstance(ctx context.Context, handle InstanceHandle) error
    ResumeInstance(ctx context.Context, handle InstanceHandle) error
}

type RenameProvider interface {
    RenameInstance(ctx context.Context, handle InstanceHandle, newName string) error
}

type UpgradeProvider interface {
    UpgradeInstance(ctx context.Context, handle InstanceHandle, targetRelease string) error
}

type PortForwardProvider interface {
    AddPortForward(ctx context.Context, handle InstanceHandle, pf PortForward) error
    RemovePortForward(ctx context.Context, handle InstanceHandle, protocol string, hostPort int) error
    ListPortForwards(ctx context.Context, handle InstanceHandle) ([]PortForward, error)
}

type InstanceAddressProvider interface {
    InstanceAddresses(ctx context.Context, handle InstanceHandle) ([]net.IP, error)
}

type ReconfigureProvider interface {
    Reconfigure(ctx context.Context, handle InstanceHandle, spec InstanceSpec) error
}
```

`InstanceAddressProvider` reports the addresses an instance holds *right now* —
including a DHCP lease the stored spec never sees — without the cost of a full
`GetInstanceInfo`; it is what the firewall/expose API uses to fill in a missing
`target_ip`. `ReconfigureProvider` applies a changed spec to an existing
instance's own on-disk configuration, so a `PATCH` actually reaches the
instance at its next start.

### Who implements what

This matrix records which backend implements which interface — the honest answer
to "does provider Y support feature X": if a cell says no, the corresponding API
endpoint returns `501` for that provider. Most backends mark their capabilities
with a `var _ provider.X = (*Provider)(nil)` compile-time assertion, but not all
of them do, so the assertions are a strong hint rather than the full list; the
methods themselves are.

| Interface | jail | bhyve | qemu | podman |
|-----------|:----:|:-----:|:----:|:------:|
| `SnapshotProvider` | ✅ | ✅ | ✅ | ✅ |
| `CloneProvider` | ✅ | ✅ | ✅ | ✅ |
| `ConsoleProvider` | ✅ | ✅ | ✅ | ✅ |
| `ExecProvider` | ✅ | ✅ | ✅ | ✅ |
| `ExecStreamingProvider` | ✅ | — | — | ✅ |
| `ExportImportProvider` | ✅ | ✅ | — | ✅ |
| `AutoStartProvider` | ✅ | ✅ | ✅ | ✅ |
| `InstanceHealthCheckProvider` | ✅ | ✅ | — | ✅ |
| `MediaProvider` | — | ✅ | ✅ | — |
| `CheckpointProvider` | — | ✅ | — | — |
| `PauseProvider` | — | ✅ | ✅ | ✅ |
| `RenameProvider` | ✅ | ✅ | ✅ | ✅ |
| `UpgradeProvider` | ✅ | — | — | — |
| `PortForwardProvider` | — | — | ✅ | — |
| `InstanceAddressProvider` | ✅ | ✅ | ✅ | ✅ |
| `ReconfigureProvider` | ✅ | — | — | — |

The macOS-only backends are outside this table: vfkit implements the mandatory
interface only, and Apple `container` adds `InstanceAddressProvider`.

A few things this table encodes about the real backends:

- **Snapshots and clones are universal** — jail/bhyve/qemu back them with ZFS,
  and Podman fakes them with `podman commit`.
- **`MediaProvider` (ISO insert/eject + boot order)** only makes sense for the
  two hypervisors, so jail and Podman skip it.
- **`CheckpointProvider`** (suspend-to-disk / live snapshot of RAM) is bhyve-only.
- **`UpgradeProvider`** is jail-only — it runs `freebsd-update` inside the jail
  root to move a base system to a newer release.
- **`PortForwardProvider`** is QEMU-only because QEMU expresses port forwards as
  `hostfwd` rules on its user-mode NAT. Jails and bhyve route port forwarding
  through PF instead (the `firewall` API), not through this interface.
- **`ReconfigureProvider`** is jail-only. A `PATCH` on a bhyve/QEMU/Podman
  instance therefore updates the datastore record and nothing on the provider
  side.

Some implemented capabilities have no CLI command and are reachable over the
API only: QEMU clone and exec, Podman pause/resume and export/import, bhyve
exec and health check.

---

## The provider registry

Providers are discovered through the registry in
[`pkg/provider/registry.go`](https://github.com/hospitus/hospitus/blob/main/pkg/provider/registry.go):

```go
type Registry struct {
    mu        sync.RWMutex
    providers map[string]Provider
}

func NewRegistry() *Registry
func (r *Registry) Register(p Provider) error   // keyed by p.Metadata().Name; rejects empty/duplicate
func (r *Registry) Get(name string) (Provider, error)
func (r *Registry) List() []ProviderInfo         // runs HealthCheck() to fill Available
func (r *Registry) IsAvailable(name string) bool
```

The registry is a plain `map[string]Provider` behind a `sync.RWMutex`. There is
also a package-level global registry with `RegisterBuiltin(p)` and
`GetGlobalRegistry()` for the built-in providers.

`List()` calls each provider's `HealthCheck()` so `ProviderInfo.Available`
reflects whether the backend actually works on this host right now (for example,
the bhyve provider reports unavailable if the `vmm` kernel module is not loaded).

`hospitusd` registers providers at startup based on `runtime.GOOS` and tool
availability (`registerProviders` in `cmd/hospitusd/main.go`): jail and bhyve are
registered **only on FreeBSD** — off FreeBSD they are absent from the registry
entirely, not merely unavailable — QEMU and Podman everywhere, and vfkit plus
Apple `container` additionally on macOS.

---

## Writing a new provider

The following walks through adding a hypothetical `myprovider`. The mechanics
mirror the existing backends.

### 1. Create the package and struct

```go
// pkg/provider/myprovider/myprovider.go
package myprovider

import (
    "context"
    "log/slog"
    "sync"

    "github.com/hospitus/hospitus/pkg/provider"
)

type MyProvider struct {
    mu        sync.RWMutex
    instances map[string]*instanceState
    dataDir   string
    stateDir  string
    logger    *slog.Logger
}

func NewMyProvider(dataDir, stateDir string, logger *slog.Logger) *MyProvider {
    return &MyProvider{
        instances: make(map[string]*instanceState),
        dataDir:   dataDir,
        stateDir:  stateDir,
        logger:    logger,
    }
}

// Compile-time proof that MyProvider satisfies the interface.
// This line fails to build the moment a method is missing or its
// signature drifts — the cheapest test you will ever write.
var _ provider.Provider = (*MyProvider)(nil)
```

The `var _ provider.Provider = (*MyProvider)(nil)` assertion is the idiom every
built-in backend uses. Add one such line per optional interface you implement.

### 2. Implement `Metadata`, `Capabilities`, and the lifecycle methods

```go
func (p *MyProvider) Metadata() provider.ProviderMetadata {
    return provider.ProviderMetadata{
        Name:        "myprovider",
        Type:        provider.ProviderTypeVM,
        Version:     "1.0.0",
        Description: "My example provider",
    }
}

func (p *MyProvider) Capabilities() provider.ProviderCapabilities {
    return provider.ProviderCapabilities{ /* set the booleans you support */ }
}

func (p *MyProvider) HealthCheck(ctx context.Context) error {
    // Return an error if the backend tool/kernel module is missing.
    // registry.List() surfaces this as Available=false.
    return nil
}

func (p *MyProvider) CreateInstance(ctx context.Context, spec provider.InstanceSpec) (provider.InstanceHandle, error) {
    p.mu.Lock()
    defer p.mu.Unlock()
    // 1. validate spec, 2. create storage, 3. configure networking,
    // 4. persist config, 5. return a handle.
    return provider.InstanceHandle{ID: spec.Name, Provider: "myprovider"}, nil
}

// ... StartInstance, StopInstance, DeleteInstance, GetInstanceState, etc.
```

Implement the full mandatory interface. For features you do not support, return
a sentinel error from `pkg/provider/errors.go` (e.g. `provider.ErrUnsupportedOperation`)
rather than panicking — but prefer simply *not* implementing the optional
interface, which gives the API a clean `501`.

### 3. Opt into capability interfaces

Add the methods and a matching assertion. Splitting them across files (as the
built-ins do — `snapshot.go`, `clone.go`, `console.go`, …) keeps each file small
and focused:

```go
// pkg/provider/myprovider/snapshot.go
var _ provider.SnapshotProvider = (*MyProvider)(nil)

func (p *MyProvider) CreateSnapshot(ctx context.Context, h provider.InstanceHandle, name string) (provider.SnapshotHandle, error) {
    // ...
}
// DeleteSnapshot, RestoreSnapshot, ListSnapshots ...
```

### 4. Register it in the daemon

In `cmd/hospitusd/main.go`, register the provider (guarded by platform checks if it
is OS-specific):

```go
registry.Register(myprovider.NewMyProvider(dataDir, stateDir, logger))
```

`List()` will run its `HealthCheck()`, so a provider that registers but is not
usable on the current host still shows up as `available: false` instead of
breaking startup.

### 5. Wire the CLI and API

A new provider is only useful once users can reach it. See
[Contributing](contributing.md) for the two end-to-end recipes:

- **Add a CLI command** — create `cmd/hospitus-cli/cmd/<provider>/<command>.go`,
  return a `*cobra.Command`, register it in the parent command, and extend the
  `APIClientInterface` in `cmd/hospitus-cli/internal/cmdutil/client.go` if a new API
  call is needed.
- **Add an API endpoint** — add the client method in `internal/client/client.go`,
  add the handler in `internal/api/server.go` with a Go 1.22+ route pattern
  (`s.mux.HandleFunc("POST /api/v1/...", s.handleX)`), and read path parameters
  with `r.PathValue("id")`.

---

## Testing a provider

Follow the project rule: **no OS calls in unit tests** (see
[Testing](testing.md)). Pure logic — spec validation, command-line building,
parameter conversion — is unit-tested with `go test -short`; anything that shells
out to `jail(8)`, `bhyve(8)`, `zfs`, or `pfctl` belongs in `test/integration/`
and runs only on FreeBSD with root.

```go
func TestCreateInstance_Validation(t *testing.T) {
    p := NewMyProvider(t.TempDir(), t.TempDir(), slog.Default())
    _, err := p.CreateInstance(context.Background(), provider.InstanceSpec{ /* invalid */ })
    require.Error(t, err)
}
```

The most valuable test is free: the `var _ provider.Provider = (*MyProvider)(nil)`
assertion makes the package fail to compile the instant your implementation
drifts from the interface.

---

## See also

- [Architecture Overview](architecture.md) — where providers sit in the system
- [Technical Deep Dive](technical-deep-dive.md) — package-by-package internals
- [REST API](api-reference.md) — the HTTP surface that dispatches to providers
- [Testing](testing.md) — the unit/integration split providers must respect
