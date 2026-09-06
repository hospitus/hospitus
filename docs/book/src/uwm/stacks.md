# Multi-Instance Stacks

A **stack** deploys several related workloads from one manifest, with declared
start-ordering between them. Where a workload manifest describes a single instance,
a stack describes a set — a database, a cache, an app, a front-end proxy — and the
dependencies that decide what starts first.

This page assumes you have read the [Overview](overview.md) and know the
single-workload [Specification](spec.md).

---

## What a stack looks like

```
  web-application-stack
  ┌──────────┐   ┌──────────┐   ┌──────────┐
  │ postgres │   │  redis   │   │   app    │   depends_on: [postgres, redis]
  │  (jail)  │   │  (jail)  │   │ (podman) │
  └────┬─────┘   └────┬─────┘   └────┬─────┘
       └──────────────┴──────────────┘
                      │
                 ┌────┴─────┐
                 │  nginx   │             depends_on: [app]
                 │  (jail)  │
                 └──────────┘
```

A stack manifest uses a `[stack]` header and repeated `[[instances]]` blocks
instead of a single `[workload]`. Each instance carries its own `provider`,
`image`, `resources`, `networks`, and so on, plus an optional `depends_on`.

---

## A complete stack manifest

```toml
# web-stack.toml
[stack]
name        = "web-application-stack"
api_version = "hospitus.io/v1"

# ── Database ────────────────────────────────
[[instances]]
name     = "postgres"
provider = "jail"

[instances.image]
source = "freebsd:14.3-RELEASE"

[instances.resources]
cpu    = 2
memory = "4Gi"

[[instances.networks]]
name   = "internal"
type   = "bridge"
bridge = "hospitus-int"

[instances.networks.ip]
mode    = "static"
address = "10.0.1.10/24"

[[instances.storage.volumes]]
name       = "pgdata"
size       = "50Gi"
mount_path = "/var/db/postgres"

# ── Cache ───────────────────────────────────
[[instances]]
name     = "redis"
provider = "jail"

[instances.image]
source = "freebsd:14.3-RELEASE"

[instances.resources]
cpu    = 1
memory = "1Gi"

[[instances.networks]]
name   = "internal"
type   = "bridge"
bridge = "hospitus-int"

[instances.networks.ip]
mode    = "static"
address = "10.0.1.20/24"

# ── Application ─────────────────────────────
[[instances]]
name     = "app"
provider = "podman"

[instances.image]
source = "oci:myapp:latest"

[instances.resources]
cpu    = 4
memory = "4Gi"

[[instances.networks]]
name   = "internal"
type   = "bridge"
bridge = "hospitus-int"

[instances.networks.ip]
mode    = "static"
address = "10.0.1.30/24"

[instances.depends_on]
services  = ["postgres", "redis"]
condition = "healthy"

# ── Front-end proxy ─────────────────────────
[[instances]]
name     = "nginx"
provider = "jail"

[instances.image]
source = "freebsd:14.3-RELEASE"

[instances.resources]
cpu    = 2
memory = "1Gi"

[[instances.networks]]
name   = "internal"
type   = "bridge"
bridge = "hospitus-int"

[instances.networks.ip]
mode    = "static"
address = "10.0.1.40/24"

[[instances.networks]]
name   = "external"
type   = "bridge"
bridge = "hospitus0"

[[instances.networks.ports]]
host      = 80
container = 80
protocol  = "tcp"

[[instances.networks.ports]]
host      = 443
container = 443
protocol  = "tcp"

[instances.depends_on]
services  = ["app"]
condition = "started"
```

---

## Dependencies and start order

Each instance may declare `depends_on` with a list of instance names and a
condition:

| Condition | Meaning |
|-----------|---------|
| `started` (default) | Wait until the dependency is running |
| `healthy` | Wait until the dependency passes its health check |

`healthy` is the only value that changes anything: the condition is not
validated, and every other value — including `completed` — is accepted and
behaves as `started`.

Hospitus computes the start order with a topological sort (Kahn's algorithm):

1. Instances with no dependencies come first.
2. Dependents follow their dependencies.
3. A dependency cycle is a validation error — the stack will not apply.

The sort produces a single ordered list, and both deploy paths walk it **one
instance at a time** — nothing is created in parallel, even where the graph
would allow it. For the manifest above the order is:

```
1. postgres, redis   (no dependencies — either order)
2. app               (after postgres and redis)
3. nginx             (after app)
```

Validation also rejects a `depends_on` that names an instance not present in the
stack, and an instance that depends on itself.

### What `hospitus apply` does with the condition today

`hospitus apply` uses `depends_on` for the order and **not** for the condition. It
creates each instance in dependency order and moves straight on to the next one
— with `--start` it pauses two seconds between instances, without it there is no
pause at all; `healthy` behaves like `started`. The stack API the daemon exposes
does honor the condition, but no CLI command reaches that path yet.

In practice the order is usually enough, because an instance's provisioning
hooks run to completion before the next instance is created — a database that
installs and starts inside its own hooks is accepting connections by the time
its dependent is built. It is not enough when a service keeps initializing after
its hooks return. Until the two paths agree, a manifest that must not race
should make its dependent wait for itself, in its own first hook, rather than
rely on the condition.

---

## Deploying and managing a stack

### Apply

`apply` handles a stack the same way as a workload — pass the file as a positional
argument. Instances are created in dependency order.

```bash
# Validate first
hospitus validate web-stack.toml

# Create every instance (omit --start to create without starting)
hospitus apply web-stack.toml

# Create and start; --timeout bounds the whole run, hooks included
hospitus apply --start web-stack.toml
```

### Inspect

Instances appear alongside standalone workloads in the provider list commands.
Filter by name or label to see just this stack:

```bash
hospitus jail list
hospitus podman list
```

### Stop and start individual instances

A stack instance is named `<stack>_<instance>`, so the instances of
`web-application-stack` are `web-application-stack_postgres` and friends — the
bare name in the manifest does not resolve.

Stop in reverse dependency order (front-end first, data tier last) so dependents
never lose their backends unexpectedly:

```bash
hospitus jail stop web-application-stack_nginx
hospitus podman stop web-application-stack_app
hospitus jail stop web-application-stack_redis
hospitus jail stop web-application-stack_postgres
```

### Delete

`manifest delete` reads the same file to know what to remove, and deletes in
reverse dependency order. Pass the **manifest file**, not the stack name:

```bash
hospitus manifest delete -y web-stack.toml
```

---

## Inter-service communication

Instances talk to each other over their shared internal bridge. Because each
service has a static IP, other services can reference it directly — for example,
the app connects to PostgreSQL at `10.0.1.10:5432` and Redis at `10.0.1.20:6379`.

Provisioning of service configuration (installing packages, writing config files,
setting credentials) is done per instance with `post_create` hooks or cloud-init,
exactly as for a single workload. See the [Specification](spec.md#lifecycle) for
the hook format and [Variables & Secrets](variables.md) for injecting values such
as database passwords with the `secret` function.

---

## Multi-host deployment

A stack manifest targets a single `hospitusd`. To spread services across physical
hosts, split the stack into per-host manifests and point each apply at a different
[CLI context](../user-guide/contexts.md).

### Define contexts

```bash
hospitus context add prod-db \
  --url https://db-host:8443 \
  --api-key "$(cat ~/.hospitus/db-key)"

hospitus context add prod-web \
  --url https://web-host:8443 \
  --api-key "$(cat ~/.hospitus/web-key)"
```

### Apply to each host

Select the target with the global `--context` flag:

```bash
# Database tier on prod-db
hospitus --context prod-db apply --start stack/database.toml

# Application tier on prod-web
hospitus --context prod-web apply --start stack/app.toml
```

### Cross-host networking

When instances span hosts, the internal bridge no longer connects them. Reference
remote services by a routable IP or over a VPN (WireGuard, Tailscale), and prefer
IP addresses over hostnames in your service configuration.

```bash
#!/usr/bin/env bash
# deploy-stack.sh — deploy a stack across two hosts
set -euo pipefail

echo "==> Database tier"
hospitus --context prod-db apply --start stack/database.toml

echo "==> Application tier"
hospitus --context prod-web apply --start stack/app.toml

echo "==> Done"
hospitus --context prod-db jail list
hospitus --context prod-web jail list
```

---

## See Also

- [Overview](overview.md) — manifest concepts
- [Specification](spec.md) — full field reference, including stack fields
- [Variables & Secrets](variables.md) — parameterisation and credentials
- [Examples & Template Catalog](examples.md) — ready-to-adapt manifests
- [Contexts](../user-guide/contexts.md) — managing multiple hospitusd hosts
