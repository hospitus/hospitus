# Introduction

**Hospitus** is an API-first manager for virtual machines and containers. It
unifies FreeBSD **jails**, **bhyve** virtual machines, **QEMU** virtual
machines, and **Podman** containers behind a single Go daemon, one RESTful API,
and one command-line interface. QEMU and Podman also run on Linux and macOS, and
macOS adds **vfkit** and Apple **container** — see
[macOS](getting-started/macos.md).

Instead of learning `jail(8)`, `bhyve(8)`, `qemu-system`, and `podman`
separately — each with its own configuration files, networking model, and
lifecycle quirks — you drive all of them the same way:

```bash
hospitus jail   create web  --image 14.3-RELEASE-amd64 --vnet --cpus 2 --memory 2048
hospitus bhyve  create db   --image ubuntu-24.04 --cpus 4 --memory 8192
hospitus podman create cache --image redis:7
```

## Who this manual is for

This is the **Administrator & User Manual**. It assumes you operate a FreeBSD
host and want to run and manage workloads on it. You do not need to read the Go
source to use Hospitus, but you should be comfortable with the FreeBSD command line
(`zfs`, `ifconfig`, `pfctl`, `service`, `sysrc`).

If you are extending Hospitus itself — writing a provider or calling the API
from code — see the separate **Developer Manual**.

## Status and scope

Hospitus is at **1.0.0**, targeting **FreeBSD 14.x**. The API, the CLI, the
providers and the manifest layer are covered by tests, and the example manifests
are deployed against a real host rather than asserted. Production history is
limited.

What it does not have is time in the field.
[CBSD](https://github.com/cbsd/cbsd) has been managing FreeBSD jails and bhyve
VMs in production for over a decade, and no amount of testing substitutes for
that many people having hit that many edges. If you want the option with the
longest record behind it, use CBSD.

Two things are worth saying plainly. Hospitus was written with heavy AI
assistance; if that is not something you want to run, CBSD covers the same
ground on FreeBSD and is the honest recommendation. And on macOS there is no
equivalent — CBSD is FreeBSD-only — so that comparison does not apply there.

Not every part is equally exercised:

| Surface | State |
|---------|-------|
| REST API, CLI, providers, manifests, jobs, image catalog | Covered by tests and exercised on a real host |

Where the manual describes an edge that is thinner than the rest, it says so.

## Why Hospitus?

Modern infrastructure spans several isolation technologies, each strong at
something different. Hospitus does not replace them — it gives them a common
control plane:

- **Unified CLI and REST API** — one grammar (`hospitus <provider> <verb>`) and one
  HTTP surface for every workload type.
- **Declarative manifests (UWM)** — describe workloads as TOML and apply them
  reproducibly; see the [Unified Workload Manifests](uwm/overview.md) chapter.
- **Native ZFS integration** — datasets, snapshots, and copy-on-write clones are
  first-class, not bolted on.
- **Automatic FreeBSD networking** — VNET epairs, bridges, and PF NAT anchors
  are created for you, without touching your existing `pf.conf`.
- **Cross-architecture jails** — run ARM64 or RISC-V jails on an AMD64 host via
  QEMU user-mode emulation.

## Core concepts

### Providers

Each virtualization technology is a **provider** behind a common interface. You
select one per workload with the CLI sub-command (`hospitus jail …`,
`hospitus bhyve …`) or the `type` field in a manifest.

| Provider | Backed by | Typical use |
|----------|-----------|-------------|
| **jail** | `jail(8)`, ZFS, VNET, PF | Lightweight FreeBSD services and containers |
| **bhyve** | `bhyve(8)`, ZFS, tap/bridge | Full VMs, including Linux and Windows guests |
| **qemu** | `qemu-system-*` | Cross-architecture and cross-platform VMs |
| **podman** | Podman / OCI | Docker-compatible Linux containers |

Jails and bhyve are FreeBSD-only. QEMU and Podman also run on macOS for
development, where the daemon additionally registers the **vfkit** and Apple
**container** providers ([macOS](getting-started/macos.md)). See the
per-provider guides under **Providers**.

### The daemon and the client

Hospitus is split into two binaries:

- **`hospitusd`** — the daemon. It holds all privileged logic (ZFS, `jail(8)`, PF,
  bhyve) and serves the REST API. It must run as **root** on FreeBSD and listens
  on `127.0.0.1:8080` by default.
- **`hospitus`** — the CLI client. It is a thin HTTP client that talks to `hospitusd`.
  It can target a local or a remote daemon (see
  [Remote Access](user-guide/remote.md) and [Contexts](user-guide/contexts.md)).

```
 hospitus (CLI) ──HTTP/JSON──► hospitusd (API server) ──► Provider interface
                                                          │
              ┌───────────────┬───────────────┬──────────┴──────────┐
              ▼               ▼               ▼                     ▼
        Jail provider   bhyve provider   QEMU provider       Podman provider
        jail(8), ZFS,   bhyve, ZFS,      qemu-system         OCI containers
        VNET, PF        tap/bridge
```

Because the client and daemon are separate, the same `hospitus` binary manages your
laptop's local daemon and a production host across the network — only the target
URL and API key change.

### Unified Workload Manifest (UWM)

UWM is Hospitus's infrastructure-as-code format: a TOML file that declares a
workload (or a multi-instance stack) so it can be created reproducibly with
`hospitus apply`. A minimal example:

```toml
[workload]
name = "webserver"

[provider]
type = "jail"

[image]
source = "freebsd:14.3-RELEASE"

[resources]
cpu = 2
memory = "2Gi"

[[networks]]
name = "public"
type = "bridge"
bridge = "hospitus0"
```

The full grammar, variable substitution, and secret handling are documented in
the [Unified Workload Manifests](uwm/overview.md) chapter.

## Where to go next

New to Hospitus? Follow the getting-started path in order:

1. [Installation](getting-started/installation.md) — build and install the binaries.
2. [Host Setup (`hospitus init`)](getting-started/host-setup.md) — prepare the FreeBSD host.
3. [Quick Start](getting-started/quick-start.md) — first daemon and first workload.
4. [Your First Jail](getting-started/first-jail.md) — a guided end-to-end tutorial.

Deploying Hospitus on a host? The [Production](production/deployment.md)
chapter covers deployment, [security hardening](production/security.md),
[monitoring](production/monitoring.md), and
[backup & recovery](production/backup.md).

## Getting help

- **CLI help** — `hospitus --help`, `hospitus <command> --help`, or `hospitus man`.
- **Prerequisite doctor** — `hospitus init --check` reports anything the host is
  missing.
- **Troubleshooting** — the [Troubleshooting](troubleshooting/common-issues.md)
  chapter and the [FAQ](troubleshooting/faq.md).
- **Issues** — [github.com/hospitus/hospitus/issues](https://github.com/hospitus/hospitus/issues).
