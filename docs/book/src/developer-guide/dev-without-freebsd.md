# Developing Without FreeBSD

Hospitus targets FreeBSD, but a lot of it — the API server, CLI, datastore, job
manager, manifest engine, and the QEMU and Podman providers — is portable Go.
You can build, run, and test most of the project on **macOS** or **Linux** and
only reach for a FreeBSD box when working on jails or bhyve.

> **FreeBSD-only by design.** The jail and bhyve providers depend on kernel
> interfaces that exist only on FreeBSD (`jail(8)`, VNET/epair, `bhyve(8)`,
> `vmm`, RCTL, native ZFS). `registerProviders` gates them on `runtime.GOOS`,
> so off FreeBSD they are **never registered at all** — not registered and
> unavailable — and cannot be exercised there.

---

## What works where

| Component | macOS | Linux | FreeBSD |
|-----------|:-----:|:-----:|:-------:|
| `go build ./...` | ✅ | ✅ | ✅ |
| Unit tests (`go test -short ./...`) | ✅ | ✅ | ✅ |
| API server (`hospitusd`) | ✅ | ✅ | ✅ |
| CLI (`hospitus`) | ✅ | ✅ | ✅ |
| QEMU provider | ✅ (HVF) | ✅ (KVM) | ✅ |
| Podman provider | ✅ (podman machine) | ✅ | ✅ |
| Jail provider | ❌ | ❌ | ✅ |
| bhyve provider | ❌ | ❌ | ✅ |
| vfkit provider | ✅ | ❌ | ❌ |
| Apple `container` provider | ✅ | ❌ | ❌ |

The single binary registers the providers appropriate to the host. On macOS the
registry holds four: QEMU, Podman, vfkit and Apple `container` — jail and bhyve
are not registered, so they do not appear in `hospitus provider list` at all. A
registered provider whose tooling is missing still shows up, with
`available: false` from its failing `HealthCheck()`.

---

## macOS setup

### Prerequisites

```bash
brew install go            # Go 1.25+ (matches go.mod)
brew install qemu          # QEMU VM provider
brew install podman        # Podman container provider
brew install golangci-lint # linter

# Podman runs containers in a Linux VM — start it once
podman machine init
podman machine start

# Verify
qemu-system-x86_64 --version
podman version
```

### Build, test, run

```bash
git clone https://github.com/hospitus/hospitus
cd hospitus
make build                 # builds hospitusd + hospitus
go test -short ./...       # unit tests, no root needed

# Run the daemon for local development.
# --allow-insecure-tls: skip TLS (the daemon is otherwise fail-closed on TLS)
# --allow-no-auth:      skip API keys on loopback (otherwise it rejects all requests)
./hospitusd \
  --data-dir  ~/.hospitus/data \
  --state-dir ~/.hospitus/state \
  --db        ~/.hospitus/hospitus.db \
  --allow-insecure-tls \
  --allow-no-auth
```

Both flags matter: without `--allow-insecure-tls` the daemon refuses to start
without certificates, and without a key **or** `--allow-no-auth` it fails closed
and rejects every request. See [Authentication](authentication.md) for the full
matrix.

In another terminal:

```bash
./hospitus qemu list
./hospitus podman list
```

### How the providers behave on macOS

These behaviors are implemented in the QEMU/Podman providers and
`pkg/network/darwin.go`:

- **QEMU acceleration** — the provider detects HVF (Hypervisor.framework) and
  runs with `-accel hvf` for near-native speed; it falls back to software
  emulation (TCG) if HVF is unavailable. Firmware and binaries are located under
  both Homebrew prefixes: `/opt/homebrew/…` (Apple Silicon) and `/usr/local/…`
  (Intel).
- **QEMU networking** — user-mode NAT with `hostfwd` for port forwarding. No host
  network changes, no bridges, no PF.
- **Podman** — all container operations run through the `podman` CLI against the
  `podman machine` Linux VM. Unlike on FreeBSD, no `--os linux` flag is added
  because the VM is already Linux.
- **Network manager** — `pkg/network/darwin.go` is a minimal stub. Bridge/TAP
  operations are no-ops because QEMU and Podman handle their own networking; this
  is a deliberate simplification, not a gap to fill for VM/container development.

### macOS limitations

| Not available on macOS | Use instead |
|------------------------|-------------|
| Jails | Podman containers |
| bhyve VMs | QEMU VMs |
| VNET / epair interfaces | QEMU user-mode NAT |
| PF port forwarding | QEMU `hostfwd` / Podman port mapping |

---

## Linux setup

```bash
# Debian / Ubuntu
apt install golang qemu-system podman golangci-lint

# Fedora / RHEL
dnf install golang qemu podman golangci-lint
```

Build, test, and run exactly as on macOS. On Linux the QEMU provider uses KVM
when `/dev/kvm` is available. Jails and bhyve remain FreeBSD-only.

---

## Running unit tests without a daemon

The API, datastore, job manager, auth, manifest, and firewall packages are pure
Go and need no running daemon, no root, and no FreeBSD:

```bash
go test -short ./internal/api/...
go test -short ./internal/auth/...
go test -short ./internal/datastore/...
go test -short ./pkg/job/...
go test -short ./pkg/manifest/...
go test -short ./pkg/firewall/...
```

The project rule is **no OS calls in unit tests** — see [Testing](testing.md).
Anything that shells out to `jail(8)`, `zfs`, `bhyve`, or `pfctl` lives in
`test/integration/` and only runs on FreeBSD.

---

## The FreeBSD-only integration tests

Integration tests under `test/integration/` require FreeBSD with root:

```bash
# On FreeBSD, as root
doas go test -v ./test/integration/...

# Large-download tests (base.txz, cloud images, ...)
HOSPITUS_LONG_TESTS=1 doas go test -v ./test/integration/...

# The security suite is behind a build tag
doas go test -tags integration -v ./test/integration/...
```

They expect ZFS, PF (`sysrc pf_enable=YES && service pf start`), and RACCT
(`kern.racct.enable=1` in `/boot/loader.conf`, then reboot) — see the
[Host Setup guide](../getting-started/host-setup.md).

---

## Writing portable provider code

Guard FreeBSD-only behavior with `runtime.GOOS` or build tags so the package
still compiles everywhere:

```go
if runtime.GOOS != "freebsd" {
    return fmt.Errorf("jail provider is only supported on FreeBSD")
}
```

For larger platform-specific blocks, split them into `_freebsd.go` /
`_darwin.go` files with `//go:build` constraints, following the pattern in
`pkg/network/` (`freebsd.go` vs `darwin.go`).

---

## CI

One workflow, `.github/workflows/ci.yml`: `golangci-lint` first, then tests on
three platforms — Linux (`go vet` plus `go test -race`), macOS (`go test`), and
FreeBSD 14.3 and 15.1 (`go build`, `go vet`, `go test -short`) inside a
`vmactions/freebsd-vm` VM, because GitHub has no FreeBSD runner. A separate
`integration-freebsd` job then runs the integration suite in a FreeBSD VM
against a file-backed ZFS pool; bhyve stays out of it, since the VM has no
hardware virtualisation. A security scan and the cross-compilation matrix
follow.

---

## See also

- [Architecture Overview](architecture.md)
- [The Provider Interface](providers.md)
- [Testing](testing.md)
- [Contributing](contributing.md)
