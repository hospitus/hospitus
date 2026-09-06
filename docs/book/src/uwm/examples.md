# Examples & Template Catalog

This page is a library of working Unified Workload Manifests. The first half is a
set of hand-written examples you can copy and adapt; the second half is a catalog
of the ready-made templates that ship with Hospitus under `examples/manifests/`.

Every manifest here validates against the [Specification](spec.md). If you are new
to manifests, read the [Overview](overview.md) first.

## Applying any manifest

The workflow is the same for every example below:

```bash
# 1. Validate (parses and checks; no daemon needed)
hospitus validate myservice.toml

# 2. Preview
hospitus apply --dry-run myservice.toml

# 3. Create and start
hospitus apply --start myservice.toml

# 4. Inspect
hospitus jail info myservice        # or: hospitus bhyve info / podman info

# 5. Remove (pass the manifest file, not the instance name)
hospitus manifest delete -y myservice.toml
```

Templates that use variables accept `--var key=value` and `--values file.toml`; see
[Variables & Secrets](variables.md).

---

## Worked examples

### Web server (jail)

An nginx front-end with two forwarded ports and autostart.

```toml
# webserver.toml
[workload]
name        = "webserver"
description = "Production nginx web server"

[workload.labels]
tier        = "frontend"
environment = "production"

[provider]
type = "jail"

[image]
source = "freebsd:14.3-RELEASE"
arch   = "amd64"

[resources]
cpu    = 2
memory = "2Gi"

[[networks]]
name   = "public"
type   = "bridge"
bridge = "hospitus0"

[networks.ip]
mode = "dhcp"

[[networks.ports]]
host      = 80
container = 80
protocol  = "tcp"

[[networks.ports]]
host      = 443
container = 443
protocol  = "tcp"

[lifecycle.autostart]
enabled  = true
priority = 50

[[lifecycle.hooks.post_create]]
type     = "exec"
commands = [
  "env IGNORE_OSVERSION=yes ASSUME_ALWAYS_YES=yes pkg install -y nginx",
  "sysrc ngihsp_enable=YES",
  "service nginx start",
]

# nginx binds port 80, which a jail may not do without this. Without it the
# hook fails with "bind() to 0.0.0.0:80 failed (13: Permission denied)".
[provider_overrides.jail.parameters]
"allow.reserved_ports" = true
```

Jails ignore `[[networks.ports]]` — the two blocks above document intent but
create no redirects. Add them after the jail is running and has an address:

```bash
hospitus jail expose add webserver --port 80:80
hospitus jail expose add webserver --port 443:443
```

### Database (jail) with persistent storage

PostgreSQL on a dedicated, compressed ZFS volume so data survives jail recreation.

```toml
# database.toml
[workload]
name        = "postgres"
description = "PostgreSQL 16 with persistent storage"

[workload.labels]
tier = "database"

[provider]
type = "jail"

[image]
source = "freebsd:14.3-RELEASE"

[resources]
cpu    = 4
memory = "8Gi"

[[networks]]
name   = "internal"
type   = "bridge"
bridge = "hospitus-int"

[networks.ip]
mode    = "static"
address = "10.0.1.10/24"

[storage.root_disk]
size = "10Gi"

[[storage.volumes]]
name       = "pgdata"
size       = "100Gi"
mount_path = "/var/db/postgres"

[storage.volumes.zfs]
compression = "lz4"
quota       = "120Gi"

[provider_overrides.jail.parameters]
"allow.sysvipc" = true    # required by PostgreSQL
```

### Linux VM (bhyve) with cloud-init

An Ubuntu VM provisioned on first boot. Cloud-init is valid for bhyve and qemu
only.

The network is `nat`, which is what makes `dhcp` work here: hospitus runs a DHCP
server on its own bridge for NAT VMs and on no other. A bhyve VM on a `bridge`
network needs a DHCP server already on that bridge, or a static address.

```toml
# linux-vm.toml
[workload]
name        = "ubuntu-server"
description = "Ubuntu 24.04 LTS server VM"

[provider]
type = "bhyve"

[image]
source = "cloud:ubuntu-24.04-amd64"
arch   = "amd64"

[resources]
cpu    = 4
memory = "4Gi"

[[networks]]
name = "default"
type = "nat"

[networks.ip]
mode = "dhcp"

[storage.root_disk]
size = "50Gi"
type = "zvol"

[cloud_init]
enabled  = true
hostname = "ubuntu-server"
packages = ["nginx"]
runcmd   = ["systemctl enable --now nginx"]
ssh_authorized_keys = ["ssh-ed25519 AAAA... you@host"]

[provider_overrides.bhyve]
bootloader   = "uefi"
console_type = "nmdm"
```

### OCI container (Podman)

An nginx container with a read-only host bind mount for the site content.

A container may only bind-mount a host path under `/var/lib/hospitus/volumes/` or
`/tmp/hospitus-`. Anywhere else is refused, so that a container cannot be pointed
at another user's data.

```toml
# nginx-container.toml
[workload]
name        = "nginx"
description = "nginx container from Docker Hub"

[provider]
type = "podman"

[image]
source = "oci:nginx:alpine"

[resources]
cpu    = 1
memory = "512Mi"

[[networks]]
name = "web"
type = "nat"

[[networks.ports]]
host      = 8080
container = 80
protocol  = "tcp"

[[storage.volumes]]
name       = "html"
host_path  = "/var/lib/hospitus/volumes/nginx-html"
mount_path = "/usr/share/nginx/html"
read_only  = true
```

### Cross-architecture jail (ARM64 on AMD64)

Runs an ARM64 FreeBSD jail on an AMD64 host via QEMU user-mode emulation. Requires
`qemu-user-static` and the `imgact_binmisc` module (see the project README for host
setup).

```toml
# arm64-jail.toml
[workload]
name        = "arm64-test"
description = "ARM64 jail on an AMD64 host"

[provider]
type = "jail"

[image]
source = "freebsd:14.3-RELEASE"
arch   = "arm64"

[resources]
cpu    = 2
memory = "1Gi"

[[networks]]
name   = "default"
type   = "bridge"
bridge = "hospitus0"

[networks.ip]
mode = "dhcp"
```

A network declared without an `ip` block gets no interface at all: the jail
starts, and hospitus warns that it left the network alone.

---
## Template catalog

Twelve manifests ship under `examples/manifests/`. Each one is applied and
checked on a FreeBSD host before release, and most carry a health check that
proves the service answers rather than merely that the instance started.

Deploy any of them with:

```bash
hospitus apply --start examples/manifests/<category>/<name>/template.toml
```

Override a default with `--var`:

```bash
hospitus apply --start --var bridge=lab0 examples/manifests/media/navidrome/template.toml
```

A manifest that generates a credential stores it as a
[secret](variables.md#secrets-cli) scoped to the instance name; read it back
with `hospitus secret get <scope> <name>`.

### Jails

| Manifest | What it runs |
|----------|--------------|
| `jails/freebsd-aarch64` | A FreeBSD jail executing ARM64 binaries through QEMU user-mode emulation. Its health check compares `uname -m` rather than merely running it, so an unemulated jail fails. |
| `jails/freebsd-riscv64` | The same for RISC-V. |
| `jails/linux-ubuntu` | An Ubuntu userland in a jail, served by Apache. Hospitus debootstraps the distribution; the binaries run through the Linux compatibility layer. |
| `media/navidrome` | A music streaming server in a jail, with its library on a separate dataset. |

### Containers

| Manifest | What it runs |
|----------|--------------|
| `containers/caddy` | Caddy serving static files from a Podman container. |

### Virtual machines

| Manifest | What it runs |
|----------|--------------|
| `vms/freebsd` | A FreeBSD server provisioned by nuageinit. |
| `vms/gpu-passthrough` | A Debian VM owning a PCI graphics card through `ppt(4)`. |
| `vms/windows-physical-disk` | A Windows VM booting an existing physical disk, untouched by hospitus. |
| `dev-tools/ci-runner` | A self-hosted CI runner in a bhyve VM, with a Docker executor. |

### Stacks

| Manifest | What it runs |
|----------|--------------|
| `stacks/nextcloud` | Nextcloud across four jails — database, cache, application, web — each with its own resources and dataset. Ships a `seed.sh` that proves a WebDAV round trip. |
| `stacks/qemu` | A three-VM development stack under QEMU: database, backend, frontend. |
| `web-hosting/wordpress` | WordPress across two jails, database and application. |

## See Also

- [Overview](overview.md) — manifest concepts and a first example
- [Specification](spec.md) — the complete field reference
- [Variables & Secrets](variables.md) — parameterise templates and manage credentials
- [Multi-Instance Stacks](stacks.md) — deploy several services together
