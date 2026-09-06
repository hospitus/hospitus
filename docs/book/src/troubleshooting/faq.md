# Frequently Asked Questions

Short, direct answers. Deeper material is linked from each entry.

## General

### What is Hospitus?

An API-first virtualization manager for FreeBSD. One daemon (`hospitusd`), one REST
API, and one CLI (`hospitus`) drive four workload types: FreeBSD **jails**, **bhyve**
VMs, **QEMU** VMs, and **Podman** containers — plus the experimental
**vfkit** and Apple **container** backends on macOS. Workloads can be declared
as TOML manifests (UWM) and applied reproducibly.

### Should I run this in production?

1.0.0 is released, it works, and it is tested — but it has no track record.
[CBSD](https://github.com/cbsd/cbsd) has been doing this job on FreeBSD for
over a decade; tests cover what someone thought to check, and ten years of
users cover what nobody did. If the safest option matters more than the shape
of the tool, use CBSD.

Hospitus was also written with heavy AI assistance. If you would rather not run
software built that way, CBSD covers the same ground on FreeBSD and is the
honest recommendation. On macOS there is no equivalent, so the choice does not
arise there.

### What does it run on?

- **FreeBSD 14.x** — jails, bhyve, QEMU and Podman (jail and bhyve are
  FreeBSD-only).
- **macOS** — QEMU, Podman, and the experimental vfkit and Apple container
  backends, for development; no jails or bhyve. Off FreeBSD the jail and bhyve
  providers are not registered at all, so `GET /api/v1/providers` does not list
  them.

Guest architectures for jails include AMD64 natively and ARM64/RISC-V via QEMU
user-mode emulation.

### How does Hospitus differ from CBSD?

CBSD is a mature, shell-based FreeBSD virtualization manager. Hospitus is API-first:
a Go daemon with a REST API, a client/daemon split (so one CLI drives local and
remote hosts), and TOML manifests instead of shell config. If you are coming from
CBSD, see [Migration from Other Tools](../appendix/migration.md).

## Installation

### What do I need to build and run it?

- FreeBSD 14.x with a ZFS pool.
- **Go 1.25+** to build from source.
- Root access (`doas`) — the daemon must run as root.

```sh
git clone https://github.com/hospitus/hospitus.git
cd hospitus && make build
doas install -m 0755 hospitusd /usr/local/bin/hospitusd
doas install -m 0755 hospitus  /usr/local/bin/hospitus
```

Then prepare the host with `hospitus init` — see
[Host Setup](../getting-started/host-setup.md).

### Do I need ZFS?

Yes, for the jail and bhyve providers. Hospitus stores jails and VM disks as ZFS
datasets and relies on ZFS snapshots and clones. Images are plain files under
`/var/lib/hospitus/images/`, not datasets. On macOS ZFS is not used.

## Configuring the daemon

### Is there a `hospitusd.conf` for the daemon?

Not for daemon settings. **`hospitusd` is configured entirely by command-line
flags** (and, under the rc.d service, by the `rc.conf` variables that build
them). There is **no** `--config` flag. Do not expect `listen_address`,
`log_level`, `data_dir`, `auth_enabled`, or `api_keys` keys — they do not exist.

The only file called `hospitusd.conf` that Hospitus reads is an optional **jail
network** config (`ip_pool`, `enable_nat`, `bridge_prefix`, …). See
[Deployment → network configuration](../production/deployment.md#6-optional-network-configuration-file).

### What's the default listen address, and how do I change it?

`127.0.0.1:8080` (loopback only). Change it with the `--addr` flag, or the
`hospitus_addr` rc.conf variable:

```sh
doas sysrc hospitus_addr="0.0.0.0:8443"
doas service hospitus restart
```

Exposing a routable address **requires** TLS — see
[Security → TLS](../production/security.md#tls).

### How does authentication work by default?

Authentication is **on and enforced** by default. The rc.d service generates a
random API key on first start (`/usr/local/etc/hospitus/api.key`) and passes it via
`--api-key-file`. If a daemon starts with **no** keys and without
`--allow-no-auth`, it **fails closed** and rejects every request. Hospitus is never
open-by-default. See [Security → Authentication](../production/security.md#authentication).

### How do I authenticate as a client?

With the **`X-API-Key` header** (the only accepted mechanism — no query
parameter, no `Bearer`). The CLI sends it for you when you provide the key:

```sh
export HOSPITUS_API_KEY="$(doas cat /usr/local/etc/hospitus/api.key)"
hospitus jail list

# or per-invocation:
hospitus --api-key "$KEY" --api-url https://hospitus.example.com:8443 jail list

# raw HTTP:
curl -H "X-API-Key: $KEY" https://hospitus.example.com:8443/api/v1/instances
```

### How do I point the CLI at a remote daemon?

Three ways, in increasing convenience:

```sh
# 1. Environment variables.
export HOSPITUS_API_URL="https://hospitus.example.com:8443"
export HOSPITUS_API_KEY="$KEY"

# 2. Global flags.
hospitus --api-url https://hospitus.example.com:8443 --api-key "$KEY" jail list

# 3. A saved context (~/.config/hospitus/config.yaml, managed by the CLI).
hospitus context add prod --url https://hospitus.example.com:8443 --api-key "$KEY"
hospitus context use prod
```

See [Remote Access](../user-guide/remote.md) and
[Contexts](../user-guide/contexts.md).

## Jails

### How do I create a basic jail?

```sh
hospitus image fetch 14.3-RELEASE-amd64
hospitus jail create web --image 14.3-RELEASE-amd64 --vnet --ip dhcp --start
```

`--start` starts the jail as part of creation; drop it and start the jail
yourself with `hospitus jail start web`.

### VNET vs non-VNET jails?

**VNET** jails get their own network stack and virtual interface — better
isolation, their own routing table; recommended for anything nontrivial.
**Non-VNET** jails share the host stack via IP aliases — simpler, less isolated.

### Can I run ARM64 jails on an AMD64 host?

Yes, via QEMU user-mode emulation:

```sh
doas pkg install qemu-user-static
doas tools/setup-binmiscctl.sh
hospitus image fetch 14.3-RELEASE-arm64
hospitus jail create arm-test --image 14.3-RELEASE-arm64 --vnet --ip dhcp
```

### How do I get a shell or run a command in a jail?

```sh
hospitus jail console web            # interactive shell
hospitus jail exec web ls -la /      # single command
```

### How do I install packages in a jail?

```sh
hospitus jail exec web env ASSUME_ALWAYS_YES=yes pkg bootstrap
hospitus jail exec web env IGNORE_OSVERSION=yes ASSUME_ALWAYS_YES=yes pkg install -y nginx
```

### How do I expose a port?

```sh
hospitus jail expose add    web --port 8080:80        # tcp/8080:80 for an explicit protocol
hospitus jail expose list   web
hospitus jail expose remove web --port 8080 --protocol tcp
```

`expose add` has no `--protocol` flag — the protocol is part of the port spec
(`tcp/8080:80`, `udp/53`), and TCP is the default. Only `remove` takes
`--protocol`.

Manage forwards only through these commands — do not hand-edit the PF anchor. See
[Networking Problems](networking.md#port-forwarding-doesnt-work).

## Storage

### Where does jail data live?

| Data | Default location |
|------|------------------|
| Jail roots | `/zroot/hospitus/jails/<name>/root/` |
| Images | `/var/lib/hospitus/images/` |
| Database & state | `/var/lib/hospitus/`, `/var/lib/hospitus/state/` |

### How do I snapshot and clone a jail?

```sh
hospitus jail snapshot create web pre-upgrade
hospitus jail snapshot list   web
hospitus jail stop web
hospitus jail snapshot restore web pre-upgrade
hospitus jail start web

hospitus jail clone web web-test                       # from current state
hospitus jail clone web web-test --snapshot pre-upgrade # from a snapshot
```

### How do I limit a jail's disk usage?

With a ZFS quota:

```sh
doas zfs set quota=10G zroot/hospitus/jails/web
```

## API & CLI

### Can I get machine-readable output?

Yes — pass `--output json` (not `--format`) to commands that support it:

```sh
hospitus jail list --output json | jq
hospitus jail info web --output json | jq
hospitus backup list --output json | jq
```

### Can I export a jail to a manifest file?

There is no jail-to-TOML export. What exists is an **instance archive** export
for backup/migration, which produces a tarball or ZFS stream — not a UWM
manifest:

```sh
hospitus jail export web /var/lib/hospitus/backups/web.tar.gz --compress --include-snapshots
```

The recommended way to keep a jail's *definition* is to author its UWM manifest
and keep it in source control, then recreate with `hospitus apply web.toml`. See
[Backup & Recovery](../production/backup.md).

### How do I apply a manifest?

```sh
hospitus validate web.toml     # check it first
hospitus apply web.toml        # create/update
```

See [Unified Workload Manifests](../uwm/overview.md).

## Troubleshooting

### How do I turn on debug logging?

Set the daemon's log level (flag or rc.conf), then restart:

```sh
doas sysrc hospitus_flags="--tls-cert ... --tls-key ... --log-level debug"
doas service hospitus restart
```

For structured logs, add `--log-format json`.

### Where are the logs?

Under the rc.d service, `daemon(8)` writes to `/var/log/hospitus/hospitusd.log`.

### How do I collect diagnostics for a bug report?

There is no `hospitus debug-info` command. Gather these by hand: `hospitus init
--check`, the tail of `/var/log/hospitus/hospitusd.log` (redact secrets), `uname -a`,
`zfs list -r zroot/hospitus`, and `doas jls`. See
[Common Issues → collecting diagnostics](common-issues.md#collecting-diagnostics-for-a-bug-report).

## See also

- [Common Issues](common-issues.md)
- [Networking Problems](networking.md)
- [Production Deployment](../production/deployment.md)
- [Glossary](../appendix/glossary.md)
