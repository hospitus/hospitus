# Manifest Specification

This is the complete field reference for Unified Workload Manifests. Every field
below is backed by the manifest parser and validator in `pkg/manifest`. Fields that
are accepted and validated but **not yet applied** by the current converter are
marked *Reserved* — you may write them, but they have no effect in this release.

New to manifests? Start with the [Overview](overview.md); this page is a reference,
not a tutorial.

- [Manifest kinds](#manifest-kinds)
- [`[workload]`](#workload)
- [`[provider]`](#provider)
- [`[image]`](#image)
- [`[resources]`](#resources)
- [`[[networks]]`](#networks)
- [`[storage]`](#storage)
- [`[lifecycle]`](#lifecycle)
- [`[cloud_init]`](#cloud_init)
- [`[environment]`](#environment)
- [`[provider_overrides]`](#provider_overrides)
- [Stack fields](#stack-fields)
- [CLI reference](#cli-reference)
- [Full schema](#full-schema)

The current manifest API version is `hospitus.io/v1`. TOML (`.toml`) is the only
supported format.

---

## Manifest kinds

A manifest is exactly one of two kinds, detected from its top-level section:

- **Workload** — a single instance. Contains a `[workload]` section.
- **Stack** — several instances deployed together. Contains a `[stack]` section
  and one or more `[[instances]]`. See [Multi-Instance Stacks](stacks.md).

A file may not contain both sections.

---

## `[workload]`

Identity and metadata for a single workload.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | **Yes** | — | Unique instance name (see rules below) |
| `api_version` | string | No | `hospitus.io/v1` | Manifest API version. `hospitus.io/v1` is the only one this release understands; any other value is refused. |
| `description` | string | No | `""` | Free-text description; applied to the instance |
| `labels` | map[string]string | No | `{}` | Key/value labels for organization |
| `annotations` | map[string]string | No | `{}` | Arbitrary metadata annotations |

### Name rules

Validated against `^[a-z][a-z0-9-]*[a-z0-9]$` (single-letter names are also
allowed). The name is lowercased before checking, and the maximum length is 63
characters.

- Must start with a letter and end with a letter or digit.
- May contain lowercase letters, digits, and hyphens.
- No underscores, no leading digit, no trailing hyphen.

Valid: `web`, `my-app`, `database-01`, `a`
Invalid: `-app`, `my_app`, `app-`, `123app`

```toml
[workload]
api_version = "hospitus.io/v1"
name        = "production-web"
description = "Front-end web server for production traffic"

[workload.labels]
environment = "production"
team        = "platform"
```

---

## `[provider]`

Selects the virtualization backend.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `type` | string | **Yes** | — | `jail`, `bhyve`, `qemu`, or `podman` |

```toml
[provider]
type = "jail"
```

---

## `[image]`

The base system or image to boot from.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `source` | string | **Yes*** | — | Image in `type:reference` form |
| `arch` | string | No | `amd64` | `amd64`, `arm64`, `riscv64`, or `i386` |

\* `source` is required except for two cases: a Linux jail
(`provider_overrides.jail.os_type = "linux"`), which is populated with
debootstrap, and `source = "none"`, which boots a VM directly from a physical
disk.

### Source types and provider compatibility

The `source` prefix must be compatible with the provider, or validation fails:

| Prefix | Example | Valid providers |
|--------|---------|-----------------|
| `freebsd` | `freebsd:14.3-RELEASE` | jail, bhyve, qemu |
| `cloud` | `cloud:ubuntu-24.04-amd64` | bhyve, qemu |
| `iso` | `iso:FreeBSD-14.3-amd64-dvd1` | bhyve, qemu |
| `oci` | `oci:nginx:latest` | podman |

```toml
[image]
source = "freebsd:14.3-RELEASE"
arch   = "amd64"
```

---

## `[resources]`

Compute allocation.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `cpu` | int | No | `1` | CPU cores (0–256) |
| `memory` | string | No | `512Mi` | Memory with unit; minimum `64Mi` |

### Memory units

`K`/`M`/`G`/`T` are decimal (1000-based); `Ki`/`Mi`/`Gi`/`Ti` are binary
(1024-based). A bare number is bytes. Examples: `512Mi`, `2Gi`, `4G`.

```toml
[resources]
cpu    = 4
memory = "8Gi"
```

> There is no `[resources.limits]` sub-table — the strict parser rejects it as
> an unknown field. To cap a jail's resources, use jail resource controls at
> create time or ZFS quotas on its dataset.

---

## `[[networks]]`

An array of network interfaces. Use the double-bracket form for each entry.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | **Yes** | — | Interface name |
| `type` | string | No | `bridge` | `bridge`, `nat`, `macvlan`, `vxlan`, `none` |
| `bridge` | string | No | auto | Bridge to attach to (auto-created if absent) |
| `bridge_flags` | []string | No | `[]` | Bridge member flags, e.g. `["private"]` |
| `vlan` | int | No | — | VLAN tag |
| `mac` | string | No | auto | MAC address |
| `ip_pool` | string | No | — | IP pool for bridge auto-creation |
| `mtu` | int | No | — | Interface MTU |
| `ip` | table | No | — | IP configuration (see below) |
| `ports` | []table | No | `[]` | Port-forward rules (see below) |

> `type = "vnet"` is **not** a valid value. VNET is how jails implement the
> `bridge` and `nat` types under the hood — in the manifest, choose `bridge` or
> `nat`.

### `[networks.ip]`

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `mode` | string | No | `dhcp` | `dhcp`, `static`, or `none` |
| `address` | string | If `static` | — | CIDR, e.g. `10.0.0.50/24` |
| `gateway` | string | No | — | Default gateway (VMs only, see below) |
| `dns` | []string | No | `[]` | DNS server IPs (VMs only, see below) |

`gateway` and `dns` reach the guest through cloud-init, so they apply to bhyve
and QEMU instances and are **ignored by jails**. A jail takes its default route
from the daemon's `default_gateway` setting and copies the host's resolvers into
its `resolv.conf`. Setting either field on a jail network changes nothing, and
nothing reports it — so a jail that needs a route off its segment needs either
that daemon setting or a second interface on a network that has one.

### `[[networks.ports]]`

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | int | **Yes** | — | Host port (1–65535) |
| `container` | int | **Yes** | — | Instance port (1–65535) |
| `protocol` | string | No | `tcp` | `tcp` or `udp` |

`[[networks.ports]]` is acted on by **Podman**, whose runtime publishes the port
as the container starts, and by **QEMU** on `nat` networks, where each rule
becomes a user-mode `hostfwd` redirect (if the host port is taken, QEMU picks a
free one). bhyve and jail instances ignore it, and the reason is
not an oversight: a redirect needs the guest's address, and a DHCP guest has
none until it has booted. Declare it after the fact instead:

```sh
hospitus bhyve expose add myvm --port 2222:22 --target-ip 10.10.0.195
hospitus jail expose add myjail --port 8080:80
```

The command says so itself when the address is missing, rather than writing a
rule that points nowhere:

```
target_ip is required: this instance has no recorded address, which is normal
for a DHCP guest — pass the address it reports (hospitus <provider> expose add
--target-ip)
```

```toml
[[networks]]
name   = "public"
type   = "bridge"
bridge = "hospitus0"

[networks.ip]
mode    = "static"
address = "10.0.0.50/24"
gateway = "10.0.0.1"
dns     = ["1.1.1.1"]

[[networks.ports]]
host      = 8080
container = 80
protocol  = "tcp"
```

---

## `[storage]`

Root disk and additional volumes.

### `[storage.root_disk]`

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `size` | string | No | provider default | Disk size, e.g. `20Gi` |
| `type` | string | No | `auto` | `auto`, `zvol`, `qcow2`, `raw`, `physical` |
| `path` | string | If `physical` | — | Host device path, e.g. `/dev/ada2` |

The `path` requirement for `type = "physical"` is enforced by the provider at
create time, not by `hospitus validate` — a manifest that omits it validates
cleanly and fails at apply.

### `[[storage.volumes]]`

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | **Yes** | — | Volume name |
| `mount_path` | string | **Yes** | — | Absolute path inside the instance |
| `size` | string | If no `host_path` | — | Volume size |
| `host_path` | string | No | — | Host path for a bind/nullfs mount |
| `read_only` | bool | No | `false` | Mount read-only |
| `zfs` | table | No | — | ZFS options (see below) |

### `[storage.volumes.zfs]`

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `compression` | string | No | inherited | `lz4`, `gzip`, `zstd`, `lzjb`, `off` |
| `quota` | string | No | — | ZFS quota, e.g. `200Gi` |

```toml
[storage.root_disk]
size = "20Gi"
type = "zvol"

[[storage.volumes]]
name       = "data"
size       = "100Gi"
mount_path = "/var/lib/data"

[storage.volumes.zfs]
compression = "lz4"
quota       = "150Gi"

# Read-only host bind mount (nullfs)
[[storage.volumes]]
name       = "config"
host_path  = "/opt/app/config"
mount_path = "/etc/app"
read_only  = true
```

---

## `[lifecycle]`

Startup behavior and hooks.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `stop_timeout` | int | No | — | *Reserved* — seconds to wait for graceful stop |
| `autostart` | table | No | — | Boot-time autostart (see below) |
| `hooks` | table | No | — | Lifecycle hooks (see below) |
| `health_check` | table | No | — | Health-check definition (see below) |

### `[lifecycle.autostart]`

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `enabled` | bool | No | `false` | Start automatically on host boot |
| `priority` | int | No | `50` | 0–100; lower starts earlier |
| `delay` | string | No | `0s` | Delay after the previous instance |

### `[lifecycle.hooks]`

Two hook styles are supported. **Simple host hooks** are shell commands run on the
host at lifecycle points:

| Field | Type | Description |
|-------|------|-------------|
| `pre_start` | string | Runs on the host before the instance starts |
| `post_start` | string | Runs on the host after the instance starts |
| `pre_stop` | string | Runs on the host before the instance stops |

**Structured hooks** run one-time provisioning during creation. `pre_create` and
`post_create` are arrays of hook specs, but only `post_create` is executed —
`pre_create` hooks are accepted and stored with the instance, and nothing runs
them in this release:

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | Must be `exec` (run commands inside the instance); the validator refuses any other value |
| `commands` | []string | Commands to execute |
| `on_failure` | string | `stop` (default) or `continue` |

```toml
[lifecycle.autostart]
enabled  = true
priority = 20
delay    = "3s"

# Install and enable nginx the first time the jail is created.
[[lifecycle.hooks.post_create]]
type       = "exec"
on_failure = "stop"
commands = [
  "env IGNORE_OSVERSION=yes ASSUME_ALWAYS_YES=yes pkg install -y nginx",
  "sysrc ngihsp_enable=YES",
  "service nginx start",
]
```

Structured `post_create` hooks are the recommended way to provision a jail —
they avoid fragile shell heredocs and run inside the instance with a predictable
environment.

### `[lifecycle.health_check]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `command` | []string | — | Command run inside the instance; without it the check never runs |
| `interval` | string | `30s` | Time between checks |
| `timeout` | string | `10s` | Per-check timeout |
| `retries` | int | `3` | Consecutive failures before the instance is marked unhealthy |
| `start_period` | string | `0s` | Grace period before checks begin |

```toml
[lifecycle.health_check]
command  = ["service", "nginx", "status"]
interval = "30s"
timeout  = "5s"
retries  = 3
```

Health checks are wired up by **stack deploys** only: deploying a stack
registers the check and it drives `depends_on.condition = "healthy"`. On a
single-workload `hospitus apply`, the section is accepted and stored but nothing
acts on it in this release.

---

## `[cloud_init]`

First-boot provisioning for **bhyve and qemu only**. Applying a `[cloud_init]`
section to a jail or Podman workload is a validation error. Compatible with both
Linux cloud-init and FreeBSD's nuageinit (14.1+).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | auto | Generate a cloud-init ISO. Defaults to `true` when the section has any content (users, packages, runcmd, SSH keys, or user data); an explicit `enabled = false` is preserved |
| `hostname` | string | workload name | Guest hostname |
| `packages` | []string | `[]` | Packages to install on first boot |
| `runcmd` | []string | `[]` | Commands to run on first boot |
| `ssh_authorized_keys` | []string | `[]` | SSH keys for the default user |
| `user_data` | string | — | Raw cloud-config YAML (appended) |
| `user_data_file` | string | — | Path to an external user-data file |
| `users` | []table | `[]` | Users to create (see below) |

`user_data` and `user_data_file` are mutually exclusive.

### `[[cloud_init.users]]`

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Username (required) |
| `ssh_authorized_keys` | []string | SSH keys for this user |
| `sudo` | string | Sudo rule, e.g. `ALL=(ALL) NOPASSWD:ALL` (Linux) |
| `doas` | string | doas rule (nuageinit); `%u` is replaced with the username |
| `shell` | string | Login shell |
| `groups` | []string | Supplementary groups |
| `lock_passwd` | bool | Disable password login (Linux only) |
| `plain_text_passwd` | string | Plain-text password (nuageinit; console access) |

```toml
[provider]
type = "bhyve"

[image]
source = "cloud:ubuntu-24.04-amd64"

[cloud_init]
enabled  = true
hostname = "web01"
packages = ["nginx"]
runcmd   = ["systemctl enable --now nginx"]

[[cloud_init.users]]
name                = "deploy"
sudo                = "ALL=(ALL) NOPASSWD:ALL"
groups              = ["sudo"]
lock_passwd         = true
ssh_authorized_keys = ["ssh-ed25519 AAAA... deploy@ci"]
```

FreeBSD guests use nuageinit; prefer `doas` and `plain_text_passwd` over the
Linux-only `sudo`/`lock_passwd`, and use rc.d commands in `runcmd`:

```toml
[image]
source = "cloud:freebsd-14.1-cloud-amd64"

[cloud_init]
enabled = true
runcmd  = ["sysrc ngihsp_enable=YES", "service nginx start"]

[[cloud_init.users]]
name = "admin"
doas = "permit nopass %u as root"
groups = ["wheel"]
```

---

## `[environment]`

A top-level map of environment variables for the instance. Any key/value pairs
are accepted.

Only **Podman** acts on it: each entry becomes a `-e KEY=value` argument on the
container. The other providers store the map with the instance but do not
inject it.

```toml
[environment]
DATABASE_URL = "postgres://db:5432/app"
LOG_LEVEL    = "info"
```

---

## `[provider_overrides]`

Backend-specific settings, keyed by provider name. Only the section matching the
active provider is used.

### `[provider_overrides.jail]`

| Field | Type | Description |
|-------|------|-------------|
| `os_type` | string | `freebsd` (default) or `linux` |
| `os_version` | string | OS version override |
| `parameters` | map | Raw `jail.conf` parameters (see below) |

`parameters` accepts any `jail(8)` parameter as a key. Dotted keys must be quoted:

```toml
[provider_overrides.jail.parameters]
"allow.raw_sockets" = true
"allow.sysvipc"     = true
securelevel         = 2
devfs_ruleset       = 5
```

### `[provider_overrides.bhyve]`

| Field | Type | Description |
|-------|------|-------------|
| `bootloader` | string | Must be `uefi`; the provider refuses any other value |
| `console_type` | string | `nmdm`, `com1`, `virtio` |
| `disk_driver` | string | `virtio-blk` (default), `virtio-scsi`, `ahci-hd`, `nvme` |
| `passthrough` | []string | PCI devices, e.g. `["1/0/0"]` |
| `vnc` | string | `host:port` or a bare port; enables VNC at creation |
| `parameters` | map | Extra bhyve tunables, e.g. `ignore_msr = true` |

```toml
[provider_overrides.bhyve]
bootloader   = "uefi"
console_type = "nmdm"
disk_driver  = "nvme"

[provider_overrides.bhyve.parameters]
ignore_msr = true    # workaround for Windows-on-AMD MSR probing
```

### `[provider_overrides.qemu]`

| Field | Type | Description |
|-------|------|-------------|
| `machine` | string | Machine type, e.g. `q35`, `virt` |
| `cpu` | string | CPU model, e.g. `host`, `cortex-a72` |

### `[provider_overrides.podman]`

| Field | Type | Description |
|-------|------|-------------|
| `command` | []string | Override the container entrypoint/command |

---

## Stack fields

Stack manifests replace `[workload]` with `[stack]` plus repeated `[[instances]]`.
This is a summary; see [Multi-Instance Stacks](stacks.md) for the full treatment.

### `[stack]`

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | **Yes** | — | Stack name |
| `api_version` | string | No | `hospitus.io/v1` | API version |

### `[[instances]]`

Each instance mirrors a workload but drops the `[workload]` wrapper and inlines its
sections (`image`, `resources`, `networks`, `storage`, `lifecycle`, `cloud_init`,
`provider_overrides`), plus:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | **Yes** | Unique within the stack |
| `provider` | string | **Yes** | `jail`, `bhyve`, `qemu`, `podman` |
| `depends_on` | table | No | Start-ordering dependencies |

### `[instances.depends_on]`

| Field | Type | Description |
|-------|------|-------------|
| `services` | []string | Names of instances to start first |
| `condition` | string | `started` (default) or `healthy` |

The condition value is **not validated**: any value other than `healthy`
(including `completed`) is accepted and behaves as `started`. Dependency cycles
are rejected at validation time.

---

## CLI reference

### `hospitus validate`

Parse, render templates, and validate a manifest without touching the system.
Runs entirely in the client — no daemon required.

```bash
hospitus validate <manifest.toml> [-q|--quiet]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-q, --quiet` | `false` | Print only errors |

A valid manifest:

```text
Manifest type: Workload
Name: webserver
Provider: jail
Image: freebsd:14.3-RELEASE

Manifest is valid
```

An invalid one exits non-zero and lists each problem as `ERROR: [field] message`:

```text
Validation errors:
  ERROR: [workload.name] invalid name format (must be alphanumeric with hyphens, max 63 chars)
  ERROR: [provider.type] provider type is required
  ERROR: [networks[0].name] network name is required
```

`hospitus manifest validate` is an alias for `hospitus validate`.

### `hospitus apply`

Create (and optionally start) the workloads or stack described by a manifest.

```bash
hospitus apply <manifest.toml> [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Show what would be created; make no changes |
| `--start` | `false` | Start the instance after creation |
| `--pull` | `false` | Download a missing image instead of asking (required when not on a terminal) |
| `--keep-failed` | `false` | Leave the instances and volumes an apply created when it fails, to inspect them |
| `--timeout` | `30m` | Operation timeout (covers provisioning hooks) |
| `-n, --namespace` | — | Prefix applied to instance names |
| `--values` | — | TOML values file for template variables |
| `--var` | — | `key=value` variable (repeatable) |
| `--env-prefix` | `HOSPITUS_VAR_` | Prefix for environment-variable inputs |

There is no `-f` flag (the manifest is a positional argument) and no `--no-start`
flag (omit `--start` to create without starting). `hospitus manifest apply` is an
alias.

```bash
hospitus apply --dry-run webserver.toml
hospitus apply --start webserver.toml
hospitus apply -n staging --values staging.toml webserver.toml
```

### `hospitus manifest delete`

Delete the instances defined by a manifest, and the ZFS volumes it declared
for them. Pass the **manifest file** — Hospitus reads it to learn what to
remove. Aliases: `rm`, `destroy`.

A volume holds the workload's data and outlives its instance, so the command
names each one before asking for confirmation. `--keep-volumes` removes the
instances and leaves the data.

```bash
hospitus manifest delete <manifest.toml> [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--force` | `false` | Stop running instances before deleting |
| `-y, --yes` | `false` | Skip the confirmation prompt |
| `--keep-volumes` | `false` | Keep the ZFS volumes the manifest declares, with their data |
| `-n, --namespace` | — | Namespace prefix |
| `--timeout` | `5m` | Operation timeout |

```bash
hospitus manifest delete -y webserver.toml
hospitus manifest delete --force -y mystack.toml
```

---

## Full schema

A single-file reference showing every section. Reserved fields are commented.

```toml
[workload]                         # (workload manifests only)
api_version = "hospitus.io/v1"        # string, optional
name        = "instance-name"      # string, REQUIRED, 1–63 chars
description = "..."                # string, optional

[workload.labels]                  # map[string]string, optional
[workload.annotations]             # map[string]string, optional

[provider]
type = "jail"                      # REQUIRED: jail | bhyve | qemu | podman

[image]
source = "freebsd:14.3-RELEASE"    # REQUIRED (see exceptions): type:reference
arch   = "amd64"                   # optional: amd64 | arm64 | riscv64 | i386

[resources]
cpu    = 1                         # int, optional, 0–256
memory = "512Mi"                   # string, optional, min 64Mi

[[networks]]                       # array, optional
name         = "default"           # REQUIRED
type         = "bridge"            # bridge | nat | macvlan | vxlan | none
bridge       = "hospitus0"            # optional
bridge_flags = ["private"]         # optional
vlan         = 100                 # optional
mac          = "..."               # optional
ip_pool      = "..."               # optional
mtu          = 1500                # optional

[networks.ip]
mode    = "dhcp"                   # dhcp | static | none
address = "10.0.0.50/24"           # required if mode = static
gateway = "10.0.0.1"
dns     = ["1.1.1.1"]

[[networks.ports]]
host      = 8080                   # REQUIRED, 1–65535
container = 80                     # REQUIRED, 1–65535
protocol  = "tcp"                  # tcp | udp

[storage.root_disk]
size = "20Gi"
type = "auto"                      # auto | zvol | qcow2 | raw | physical
path = "/dev/ada2"                 # required if type = physical

[[storage.volumes]]
name       = "data"                # REQUIRED
mount_path = "/data"               # REQUIRED, absolute
size       = "100Gi"               # required unless host_path is set
host_path  = "/host/path"          # optional (bind/nullfs mount)
read_only  = false

[storage.volumes.zfs]
compression = "lz4"                # lz4 | gzip | zstd | lzjb | off
quota       = "200Gi"

[lifecycle]
# stop_timeout = 30                # RESERVED (parsed, not applied)

[lifecycle.autostart]
enabled  = false
priority = 50                      # 0–100
delay    = "0s"

[lifecycle.hooks]
pre_start  = "..."                 # host shell command
post_start = "..."
pre_stop   = "..."

[[lifecycle.hooks.post_create]]    # structured, runs at creation
type       = "exec"                # must be "exec" (runs in the instance)
commands   = ["env IGNORE_OSVERSION=yes ASSUME_ALWAYS_YES=yes pkg install -y nginx"]
on_failure = "stop"                # stop | continue

[lifecycle.health_check]           # acted on by stack deploys only
command      = ["service", "nginx", "status"]
interval     = "30s"
timeout      = "10s"
retries      = 3
start_period = "0s"

[environment]                      # env vars; acted on by Podman only
# KEY = "value"

# [cloud_init]                     # bhyve/qemu only — this example is a jail,
# enabled  = true                  # and the validator refuses the combination
# hostname = "host01"
# packages = ["nginx"]
# runcmd   = ["service nginx start"]
# ssh_authorized_keys = ["ssh-ed25519 AAAA..."]

# [[cloud_init.users]]
# name = "admin"

[provider_overrides.jail]
os_type = "freebsd"
[provider_overrides.jail.parameters]
"allow.raw_sockets" = false

[provider_overrides.bhyve]
bootloader   = "uefi"
console_type = "nmdm"
disk_driver  = "virtio-blk"
passthrough  = ["1/0/0"]
vnc          = "127.0.0.1:5900"
[provider_overrides.bhyve.parameters]
ignore_msr = false

[provider_overrides.qemu]
machine    = "q35"
cpu        = "host"

[provider_overrides.podman]
command = []
```

> **Reserved fields**: `lifecycle.stop_timeout` is accepted by the parser and
> validator but nothing acts on it in this release. On the single-workload apply
> path the same is true of `[lifecycle.health_check]` (stack deploys act on it)
> and of `pre_create` hooks (never executed).

---

## See Also

- [Overview](overview.md) — concepts and a first manifest
- [Variables & Secrets](variables.md) — parameterisation and credentials
- [Multi-Instance Stacks](stacks.md) — dependency-ordered deployments
- [Examples & Template Catalog](examples.md) — example manifests
