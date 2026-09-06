# Production Deployment

This chapter walks through a production deployment of Hospitus on FreeBSD: install
the binaries, prepare the host, run `hospitusd` as an rc.d service, expose it
safely, and verify the result.

Everything here targets **FreeBSD 14.x**: the REST API, the CLI, the
providers, jobs, images and manifests.

> **`hospitusd` is configured by command-line flags, not a config file.** The
> daemon does **not** read a daemon config file — there is no `--config` flag.
> Its behavior comes entirely from the flags below (and, on FreeBSD, from the
> rc.d variables that build them). The only file called `hospitusd.conf` that Hospitus
> reads is an optional **network** config used by the jail provider; see
> [Network configuration file](#6-optional-network-configuration-file).

## 1. System requirements

| Component | Minimum | Recommended |
|-----------|---------|-------------|
| FreeBSD | 14.0-RELEASE | 14.x-RELEASE, latest patch level |
| CPU | 2 cores | 8+ cores (bhyve/QEMU are CPU-bound) |
| RAM | 4 GB | 32+ GB (VMs reserve guest RAM) |
| Storage | ZFS pool, 50 GB free | ZFS on NVMe, 1+ TB |
| Privileges | root (`doas`) | root, dedicated admin account |

ZFS is required for the jail and bhyve providers. Hospitus stores jails, VM disks,
and images as ZFS datasets and relies on ZFS snapshots and clones.

## 2. Install the binaries

Hospitus ships two binaries: `hospitusd` (the daemon) and `hospitus` (the CLI).

### From source

```sh
git clone https://github.com/hospitus/hospitus.git
cd hospitus
make build

# Both go in ${PREFIX}/bin: the shipped rc.d script looks for hospitusd at
# ${hospitus_localbase}/bin/hospitusd and will not find it anywhere else.
doas install -m 0755 hospitusd /usr/local/bin/hospitusd
doas install -m 0755 hospitus  /usr/local/bin/hospitus
```

Confirm the versions match:

```sh
hospitus --version
doas hospitusd --version
```

## 3. Prepare the host with `hospitus init`

Do **not** hand-roll host preparation. Hospitus ships a prerequisite doctor that
checks and (optionally) fixes ZFS, kernel modules, sysctls, and PF for the
providers you intend to use. It is fully documented in
[Host Setup (`hospitus init`)](../getting-started/host-setup.md).

```sh
# Read-only report — safe to run anywhere, non-zero exit if a required item is missing.
hospitus init --check

# Apply every safe fix (kernel modules, sysctls, data dir, enable PF). Needs root.
doas hospitus init --auto

# Re-check until everything required is green.
hospitus init --check
```

`hospitus init` deliberately **does not** create the ZFS pool: creating a pool is
destructive. If your pool is not named `zroot`, point Hospitus at an existing
dataset with `HOSPITUS_ZFS_PARENT` (for example `HOSPITUS_ZFS_PARENT=tank/hospitus`).

### How Hospitus interacts with `pf.conf`

Hospitus keeps **all** of its NAT, `rdr`, and filter rules isolated inside its own
`hospitus` PF anchor (rule files under `/var/lib/hospitus/firewall/pf/`). It never
mixes rules into your own filter ruleset.

For those anchor rules to take effect, `/etc/pf.conf` must **declare** the
anchor. Hospitus never edits `/etc/pf.conf` for you — you add the three lines the
doctor prints once, then reload PF:

```pf
nat-anchor "hospitus"
rdr-anchor "hospitus"
anchor     "hospitus"
```

```sh
doas pfctl -f /etc/pf.conf
```

`hospitus init --check` reports whether the anchor is declared. If it is missing
when `hospitusd` starts, the daemon logs a warning with these exact lines and keeps
running, but NAT and port-forwarding stay inactive until you declare the anchor
and reload PF. This keeps the daemon predictable on a host whose firewall you
manage by hand or via configuration management.

For RCTL resource limits (`--cpus`, `--memory` on jails), enable RACCT — this
needs a reboot:

```sh
echo 'kern.racct.enable=1' | doas tee -a /boot/loader.conf
doas reboot
```

## 4. Run `hospitusd` as a service

Hospitus ships an rc.d script (`etc/rc.d/hospitus` in the source tree, installed as
`/usr/local/etc/rc.d/hospitus`). **The service name is `hospitus`, not `hospitusd`.** Use
the shipped script — do not write your own, and do not invent flags: `hospitusd`
does not accept `--config` or `--pid-file`.

### Enable and start

```sh
doas sysrc hospitus_enable=YES
doas service hospitus start
```

On first start the script creates the data, state, and log directories, and — if
no key file exists yet — generates a random API key at
`/usr/local/etc/hospitus/api.key` (mode 0600). Read it with:

```sh
doas cat /usr/local/etc/hospitus/api.key
```

### rc.conf variables

The script builds the `hospitusd` command line from these `rc.conf` variables
(defaults shown). Override with `sysrc`:

| Variable | Default | Maps to |
|----------|---------|---------|
| `hospitus_enable` | `NO` | (enables the service) |
| `hospitus_user` | empty | empty means run as root directly, which jail/bhyve/ZFS need; set a name only to drop privileges |
| `hospitus_addr` | `127.0.0.1:8080` | `--addr` |
| `hospitus_data_dir` | `/var/lib/hospitus` | `--data-dir` |
| `hospitus_state_dir` | `/var/lib/hospitus/state` | `--state-dir` |
| `hospitus_db` | `/var/lib/hospitus/hospitus.db` | `--db` |
| `hospitus_localbase` | `user.localbase` sysctl, else `/usr/local` | where the script looks for `hospitusd` and the default key file |
| `hospitus_key_file` | `${hospitus_localbase}/etc/hospitus/api.key` | `--api-key-file` |
| `hospitus_log_dir` | `/var/log/hospitus` | `daemon(8)` log destination **and** `--audit-log <dir>/audit.log` |
| `hospitus_env` | a PATH covering base and `${hospitus_localbase}` | environment for the daemon; without it qemu/podman look unavailable |
| `hospitus_flags` | `--allow-insecure-tls` | extra flags appended verbatim |

`hospitusd` runs in the foreground; the script wraps it in `daemon(8)`, which
backgrounds it, writes the pidfile (`/var/run/hospitus.pid`), and redirects output
to `${hospitus_log_dir}/hospitusd.log`.

> **About the default `hospitus_flags`.** The shipped default is
> `--allow-insecure-tls`, which permits plaintext HTTP. That is safe **only**
> because the default `hospitus_addr` is loopback (`127.0.0.1`). The moment you
> change `hospitus_addr` to a routable address, you **must** replace
> `--allow-insecure-tls` with real TLS — see the next section.

### All `hospitusd` flags

For reference (and for the systemd unit below), these are the real daemon flags:

| Flag | Default | Purpose |
|------|---------|---------|
| `--addr` | `127.0.0.1:8080` | Listen address. `0.0.0.0:PORT` exposes remotely. |
| `--data-dir` | `/var/lib/hospitus` | Persistent data directory. |
| `--state-dir` | `/var/lib/hospitus/state` | Runtime state (mode 0700). |
| `--db` | `/var/lib/hospitus/hospitus.db` | SQLite database path. |
| `--api-key-file` | — | File of API keys, one per line. |
| `--api-key` | — | An API key; repeat the flag for several keys. |
| `--allow-no-auth` | `false` | Run unauthenticated (dev only). |
| `--tls-cert` | — | TLS certificate (PEM). |
| `--tls-key` | — | TLS private key (PEM). |
| `--allow-insecure-tls` | `false` | Permit plaintext (dev / loopback only). |
| `--metrics-public` | `false` | Serve `/metrics` without auth (Prometheus). |
| `--audit-log` | `<data-dir>/logs/audit.log` | Security audit log file. The rc.d script passes it on every start. |
| `--rate-limit` | `true` | Per-client HTTP rate limiting. |
| `--rate-limit-rps` | `10` | Sustained requests per second per client. |
| `--rate-limit-burst` | `20` | Burst size per client. |
| `--write-timeout` | `30m` | HTTP write timeout (long exec/hooks). |
| `--read-timeout` | `15s` | HTTP read timeout. |
| `--idle-timeout` | `60s` | Keep-alive idle timeout. |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error`. |
| `--log-format` | `text` | `text` or `json` (use `json` for log shipping). |
| `--migrate` / `--migration-status` | — | Run/inspect DB migrations and exit. |
| `--version` | — | Print version information and exit. |

There is **no** `--ca-cert` flag and **no** `HOSPITUS_TLS_CA` environment variable.
Client-side CA trust is a CLI concern (`hospitus --tls-ca …`), not a daemon flag.

## 5. Expose the API safely

By default Hospitus listens on loopback only, which is the safest posture: only
processes on the host (including the local `hospitus` CLI) can reach it. To manage
Hospitus from other machines you must both bind a routable address **and** turn on
TLS and authentication.

### Option A — native TLS (recommended)

`hospitusd` terminates TLS itself. Point it at a certificate and key and bind a
routable address:

```sh
doas sysrc hospitus_addr="0.0.0.0:8443"
doas sysrc hospitus_flags="--tls-cert /usr/local/etc/hospitus/hospitusd.crt --tls-key /usr/local/etc/hospitus/hospitusd.key"
doas service hospitus restart
```

Setting `hospitus_flags` this way **replaces** the default
`--allow-insecure-tls`, which is what a daemon on the network needs.
Authentication is already on because the rc.d script always passes
`--api-key-file`.

For obtaining and renewing certificates (Let's Encrypt or an internal CA), and
for the reverse-proxy alternative, see the
[Security Hardening](security.md#tls) chapter.

### Option B — reverse proxy terminating TLS

You may instead keep `hospitusd` on loopback and terminate TLS at nginx/HAProxy on
the same host. Hospitus still needs authentication; keep the API key in place. This
is a valid choice, not a requirement — native TLS (Option A) removes the extra
moving part. The proxy configuration is in
[Security Hardening](security.md#option-2-terminate-tls-at-a-reverse-proxy).

### Firewall posture

Whichever option you choose, restrict who can reach the API port with PF on the
host:

```pf
# /etc/pf.conf — allow the management network to the Hospitus API, block the rest.
admin_net = "10.0.1.0/24"
pass in quick on egress proto tcp from $admin_net to (egress) port 8443 keep state
block in  quick on egress proto tcp to (egress) port 8443
```

Never expose port 8080/8443 to the public internet without both TLS and a
restricted source address.

## 6. Optional network configuration file

Hospitus reads an **optional** configuration file: mostly the jail provider's
automatic bridge/NAT/IP-pool behavior, plus which host disks a VM may be
given raw access to. This is the only `hospitusd.conf` Hospitus consumes, and it is
loaded from the first path that exists:

```
/usr/local/etc/hospitus/hospitusd.conf   # FreeBSD
/etc/hospitus/hospitusd.conf             # Linux
```

Those two and no others, both root-owned on purpose: hospitusd runs as root, so
reading configuration from a home directory or the working directory would let
an unprivileged user influence the daemon. There is no flag or environment
variable that points the daemon at another path — a file kept elsewhere is
simply not read.

If no file exists, sensible defaults apply (IP forwarding, NAT, and bridge
auto-creation are enabled). The recognized keys are exactly:

```conf
# /usr/local/etc/hospitus/hospitusd.conf — jail network configuration
ip_pool             = 10.0.0.0/24
enable_ip_forwarding = true
enable_nat          = true
external_interface  = auto        # or an interface name, e.g. em0
firewall_type       = pf
default_gateway     = auto
auto_create_bridges = true
nat_network         = auto
bridge_prefix       = hospitus
pf_anchor_name      = hospitus
enable_ipv6         = false
ipv6_prefix         = fd00::/48

# Host devices a VM may be given raw access to, space-separated. Default-deny:
# an empty list permits nothing, and the daemon must be restarted to pick up a
# change.
allowed_physical_disks = /dev/nda0
```

Do not put `listen_address`, `log_level`, `data_dir`, or `database` here — those
are **daemon** settings and belong on the `hospitusd` command line (rc.d
variables). They are not valid keys and are ignored.

## 7. Verify the deployment

```sh
# 1. The service is running and holds a pid.
doas service hospitus status

# 2. Health endpoint (always unauthenticated) returns healthy.
curl -s http://127.0.0.1:8080/health
# {"status":"healthy","time":"..."}

# 3. An authenticated call succeeds with the key.
KEY=$(doas cat /usr/local/etc/hospitus/api.key)
curl -s -H "X-API-Key: $KEY" http://127.0.0.1:8080/api/v1/providers | jq

# 4. The same call WITHOUT a key is rejected (auth is enforced).
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/api/v1/providers
# 401

# 5. The CLI can talk to the daemon.
export HOSPITUS_API_KEY="$KEY"
hospitus jail list
```

If step 4 returns `200` instead of `401`, authentication is **not** enforced —
you are running with `--allow-no-auth` or no keys were loaded. Fix this before
exposing the daemon; see [Security Hardening](security.md#authentication).

## 8. Log rotation

`daemon(8)` writes to `/var/log/hospitus/hospitusd.log`. Add a `newsyslog` entry so it
does not grow without bound:

```
# /usr/local/etc/newsyslog.conf.d/hospitus.conf
/var/log/hospitus/hospitusd.log  root:wheel  640  7  *  @T00  GB
```

Rotation alone is not enough: `daemon(8)` holds the log file open and keeps
writing to the renamed inode, and the entry above names no pidfile or signal —
there is nothing safe to signal, since `hospitusd` exits on SIGHUP and the rc.d
script does not start `daemon(8)` with `-H`. Restart the service after a
rotation (`doas service hospitus restart`, e.g. from a nightly cron job) so the
daemon reopens the new file.

If you set `--log-format json` (via `hospitus_flags`), the same file contains
line-delimited JSON suitable for shipping to a log aggregator — see
[Monitoring](monitoring.md#logs).

## Common deployment problems

**The daemon exits immediately with "requires root privileges".** `hospitusd` must
run as root on FreeBSD. The rc.d service already does this; if you run it by hand
use `doas hospitusd …`.

**Every API call returns 401, even with a key.** The key you are sending does
not match a loaded key. Confirm the daemon loaded keys (`Loaded API keys from
file` appears in the log) and that you are sending the header `X-API-Key`
(not `Authorization`).

**Port already in use.** Another process holds the port:
`sockstat -4 -l | grep 8080`. Change `hospitus_addr` or stop the conflicting
process.

**Jails fail to start with an RCTL error.** RACCT is not enabled — see
[Common Issues → RCTL](../troubleshooting/common-issues.md#rctl-resource-limits-not-working).

## See also

- [Security Hardening](security.md) — TLS, API keys, firewalling, audit logging.
- [Monitoring](monitoring.md) — health checks and Prometheus metrics.
- [Backup & Recovery](backup.md) — the database, ZFS snapshots, and `hospitus backup`.
- [Host Setup (`hospitus init`)](../getting-started/host-setup.md) — prerequisite doctor.
