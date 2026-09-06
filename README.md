# Hospitus

![version: 1.0.0](https://img.shields.io/badge/version-1.0.0-blue.svg)
![go: 1.26+](https://img.shields.io/badge/go-1.26%2B-blue.svg)
![license: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)

> Automated tests cover the API, the CLI, the providers and the manifest
> layer, and the example manifests are deployed on a FreeBSD host. Hospitus
> does not have CBSD's years in the field. See [Status](#status).

Hospitus is an API-first manager for virtual machines and containers. Jails and
bhyve on FreeBSD, QEMU and Podman on FreeBSD, Linux and macOS, vfkit and
Apple's container runtime on macOS — all behind one REST API and one CLI, two
Go binaries with no runtime dependency beyond the base system and the
hypervisors themselves.

In spirit it is **CBSD's problem space in Go**: managing FreeBSD jails and
bhyve VMs, reworked around a client/server REST architecture and declarative
TOML manifests instead of shell-driven flavour files.

## Status

The API, the CLI, the providers and the manifest layer are covered by tests, and the example manifests are deployed against a real FreeBSD
host rather than asserted from a desk. Production history is limited.

What it does not have is time. [CBSD](https://github.com/cbsd/cbsd) has been
managing jails and bhyve VMs in production for over a decade, and no amount of
testing substitutes for that many people having hit that many edges. If you
want the option with the longest record behind it, use CBSD.

Two things are worth saying plainly. Hospitus was written with heavy AI
assistance; if that is not something you want to run, CBSD covers the same
ground on FreeBSD and is the honest recommendation. And on macOS there is no
equivalent — CBSD is FreeBSD-only, so the comparison does not apply there.

## What it does

- **Providers** — jails (`jail(8)`, ZFS, VNET, PF), bhyve VMs, QEMU VMs and
  Podman/OCI containers on FreeBSD; vfkit VMs and Apple's container runtime on
  macOS. All six behind one `Provider` interface.
- **REST API** (`hospitusd`) — `/api/v1/`, 50 routes, async jobs, WebSocket
  console, bcrypt-hashed API keys (`X-API-Key`).
- **CLI** (`hospitus`) — ~180 commands across the six providers, 33 CBSD-style
  aliases (`jls`, `bstart`, …), and shell completion.
- **Storage** — ZFS datasets, snapshots, and clones; the parent dataset is
  configurable (`HOSPITUS_ZFS_PARENT`, default `zroot/hospitus`).
- **Networking** — VNET/epair with an auto-created bridge; NAT and
  port-forwarding through a dedicated PF anchor (`hospitus`). The host's
  `/etc/pf.conf` is never modified.
- **Manifests** — declarative workloads and stacks in TOML, rendered through a
  Go-template layer with variables and generated secrets.

## Platform

FreeBSD is where the whole surface lives: jails and bhyve, with ZFS storage and
a PF anchor. QEMU and Podman also run on Linux and macOS; macOS adds vfkit and
Apple's container runtime. The firewall backend follows the host — PF on
FreeBSD, nftables or iptables on Linux — and on macOS QEMU forwards ports
itself, so none is needed.

CI builds and runs the unit tests on all three. The integration suite, which
creates real jails and VMs, runs on FreeBSD.

## Build

```sh
make build     # hospitusd + hospitus
make test      # unit tests
make lint      # golangci-lint
```

Requires Go 1.25+.

## Quick start

```sh
# daemon, loopback dev mode (no auth, no TLS)
doas ./hospitusd --data-dir /var/lib/hospitus --state-dir /var/lib/hospitus/state \
              --db /var/lib/hospitus/hospitus.db --allow-no-auth --allow-insecure-tls

# create and start a jail
./hospitus jail create web --image 14.3-RELEASE-amd64 --vnet
./hospitus jail start web
```

Per-provider prerequisites (ZFS, PF, RCTL, `vmm`, Podman) are documented under
`docs/`.

## Layout

```
cmd/hospitusd/          API server daemon
cmd/hospitus-cli/       CLI client (cobra)
internal/api/        HTTP routes, auth, middleware
internal/datastore/  SQLite persistence
pkg/provider/        provider interface + jail/bhyve/qemu/podman
pkg/manifest/        manifest parser and templating
pkg/image/           image catalog and download
docs/book/           user documentation (mdBook)
examples/manifests/  example workload/stack templates
```

## Provenance & development

The architecture and design are my own. The implementation was written with
substantial help from LLM/coding agents, which also drove several later
improvements (test coverage, a command-runner abstraction for testability, an
audit-driven cleanup). AI output is treated as a proposal, not as
authoritative: it is reviewed, tested, and rewritten where needed, and the
result is my responsibility.

The git history is short and linear. That reflects when this implementation
was committed, not when the design work began — read it as an assisted rewrite
over a pre-existing design, not the whole system conjured from a blank page.

## License

Apache-2.0 — see [`LICENSE`](LICENSE). Commits are signed off
(`Signed-off-by`).
