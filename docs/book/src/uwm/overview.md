# Unified Workload Manifests — Overview

A **Unified Workload Manifest (UWM)** is a TOML file that describes a workload —
a jail, a bhyve or QEMU VM, or a Podman container — declaratively. Instead of
issuing a sequence of `hospitus` commands and remembering the exact flags, you write
down the desired end state once and let Hospitus create it.

This section teaches the format from the ground up:

- **This page** — what a manifest is, why to use one, and a working first example.
- [Specification](spec.md) — every field, its type, defaults, and validation rules.
- [Variables & Secrets](variables.md) — parameterise manifests and manage credentials.
- [Multi-Instance Stacks](stacks.md) — deploy several dependent workloads together.
- [Examples & Template Catalog](examples.md) — copy-and-adapt manifests for real services.

---

## Imperative vs Declarative

The imperative way — a jail created and exposed with individual commands:

```bash
hospitus jail create webserver --image freebsd-14.3-RELEASE-amd64 --vnet --ip dhcp --cpus 2 --memory 2048
hospitus jail start webserver
hospitus jail expose add webserver --port 80:80
hospitus jail expose add webserver --port 443:443
```

The declarative equivalent — one file that captures the same intent:

```toml
# webserver.toml
[workload]
name = "webserver"

[provider]
type = "jail"

[image]
source = "freebsd:14.3-RELEASE"

[resources]
cpu    = 2
memory = "2Gi"

[[networks]]
name = "public"
type = "bridge"
bridge = "hospitus0"

[[networks.ports]]
host      = 80
container = 80
protocol  = "tcp"

[[networks.ports]]
host      = 443
container = 443
protocol  = "tcp"
```

Apply it in a single step:

```bash
hospitus apply --start webserver.toml
```

> One caveat: **jails ignore `[[networks.ports]]`** (see
> [the spec](spec.md#networksports) for why). After applying, add the two
> forwards imperatively:
>
> ```bash
> hospitus jail expose add webserver --port 80:80
> hospitus jail expose add webserver --port 443:443
> ```

> `hospitus apply` takes the manifest path as a positional argument. There is no `-f`
> flag — write `hospitus apply webserver.toml`, not `hospitus apply -f webserver.toml`.

### Why declare instead of script?

- **Version control** — track infrastructure changes in Git alongside code.
- **Reproducibility** — the same manifest produces the same result every time.
- **Self-documenting** — the file *is* the description of the workload.
- **Reviewable** — changes show up as a readable TOML diff.
- **Portable** — the same shape targets four providers with minimal edits.

---

## Anatomy of a Manifest

Every workload manifest is built from a handful of sections. Only three are
required; the rest have sensible defaults.

| Section | Required | Purpose |
|---------|----------|---------|
| `[workload]` | **Yes** (`name`) | Identity: name, description, labels, annotations |
| `[provider]` | **Yes** (`type`) | Which backend: `jail`, `bhyve`, `qemu`, `podman` |
| `[image]` | **Yes** (`source`) | Base system or image to boot from |
| `[resources]` | No | CPU and memory allocation |
| `[[networks]]` | No | One or more network interfaces and port forwards |
| `[storage]` | No | Root disk and additional volumes |
| `[lifecycle]` | No | Autostart, hooks, health checks |
| `[cloud_init]` | No | First-boot provisioning (bhyve/qemu only) |
| `[provider_overrides.*]` | No | Backend-specific fine-tuning |

### Providers

The manifest describes *what* you want; the provider decides *how* it is realised.

| Provider | Platform | Best for |
|----------|----------|----------|
| `jail` | FreeBSD | Lightweight FreeBSD services, microservices |
| `bhyve` | FreeBSD | Full VMs, Linux and Windows guests |
| `qemu` | FreeBSD, macOS | Cross-architecture and legacy guests |
| `podman` | FreeBSD, macOS (Linux VM) | OCI/Docker-compatible containers |

### Image sources

The `[image] source` field uses a `type:reference` form, and each type is valid
only for certain providers (enforced at validation time):

```toml
[image]
source = "freebsd:14.3-RELEASE"   # jail, bhyve, qemu
# source = "cloud:ubuntu-24.04-amd64"   # bhyve, qemu
# source = "iso:FreeBSD-14.3-amd64-dvd1"  # bhyve, qemu
# source = "oci:nginx:latest"     # podman
```

---

## Your First Manifest

**1. Write the file.**

```bash
cat > myapp.toml <<'EOF'
[workload]
name = "myapp"

[provider]
type = "jail"

[image]
source = "freebsd:14.3-RELEASE"

[resources]
cpu    = 2
memory = "1Gi"

[[networks]]
name = "default"
type = "bridge"
bridge = "hospitus0"
EOF
```

**2. Validate before applying** — this parses, renders templates, and checks every
field without touching the system:

```bash
hospitus validate myapp.toml
```

**3. Preview, then apply.**

```bash
hospitus apply --dry-run myapp.toml   # show what would be created
hospitus apply --start myapp.toml     # create and start the jail
```

**4. Confirm it exists.**

```bash
hospitus jail info myapp
```

**5. Tear it down** when you are done. `delete` takes the **manifest
file**, not the instance name — Hospitus reads the file to know what to remove:

```bash
hospitus manifest delete -y myapp.toml
```

---

## Next Steps

- Read the full [Specification](spec.md) to learn every field.
- Parameterise your manifests with [Variables & Secrets](variables.md).
- Combine services into a [Multi-Instance Stack](stacks.md).
- Browse the [Examples & Template Catalog](examples.md) for example starting points.
