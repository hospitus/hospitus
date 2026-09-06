# Hospitus Podman User Guide

Managing Podman containers with Hospitus, from the command line and from a manifest.

---

## Table of Contents

- [Introduction](#introduction)
- [Imperative CLI](#imperative-cli)
- [UWM Manifests (Declarative)](#uwm-manifests-declarative)
- [Manifest Reference](#manifest-reference)

---

## Introduction

Podman is a daemonless OCI container runtime. Hospitus supports Podman containers through both imperative CLI commands (for quick operations) and declarative UWM manifests (for version-controlled infrastructure).

### How Podman Works in Hospitus

The imperative CLI (`hospitus podman create`, `hospitus podman start`, etc.) provides direct, script-friendly container management. For production and multi-container deployments, Unified Workload Manifests (UWM) offer declarative, GitOps-ready configuration.

### Prerequisites

- FreeBSD with Podman installed
- Hospitus daemon (`hospitusd`) running
- For macOS: `podman machine` (Linux VM) running

#### Install Podman on FreeBSD

```bash
# Install podman
doas pkg install podman

# Enable and start the podman service
doas sysrc podman_enable=YES
doas service podman start

# Verify installation (podman has no rootless mode on FreeBSD)
doas podman version
```

#### Configure podman-remote (Optional — connect to a Linux host)

```bash
# On Linux host: Enable podman socket
systemctl enable --now podman.socket

# On FreeBSD: Configure connection
podman-remote system connection add linux-host \
    ssh://user@linux-host/run/user/1000/podman/podman.sock
```

### Ensure Hospitusd is Running

```bash
# Check if the daemon is running (the rc.d service is named "hospitus")
service hospitus status

# If not running, start it
doas service hospitus start
```

---

## Linux images on FreeBSD

Podman on FreeBSD runs a Linux image through the Linux binary compatibility
layer, so what runs inside is a Linux program on a FreeBSD kernel. Most work.
Some start, hold their listening socket, and never serve a request.

nginx does exactly that, on FreeBSD 15.1 and 16-CURRENT alike. The master
process opens port 80, so the host sees a healthy-looking listener:

```sh
$ doas sockstat -l -j "$(doas jls -h jid name | awk 'NR>1 {print $1}')"
USER COMMAND      PID FD PROTO LOCAL ADDRESS         FOREIGN ADDRESS
0    nginx      36994  6 tcp4  *:80                  *:*
```

Every connection to it times out — from the host, from another container, and
from the container to its own address. The container log says why:

```
[alert] 36994#36994: worker process 37099 exited with fatal code 2 and cannot be respawned
```

The workers are what accept connections. They die at startup, the master keeps
the socket, and nothing answers. Nothing about the container or its networking
looks wrong, which is what makes it worth recognizing:

- `podman ps` shows the container up
- `sockstat` shows the port listening
- ICMP to the gateway works, and outbound TCP works
- a `nc -l` listener started in the same container answers on both its
  addresses

If all of that holds and one service still does not answer, read
`podman logs <name>` before suspecting the network. A health check in the
manifest turns this from an invisible failure into a reported one — which is
why every example ships with one.

### Published ports do not carry traffic

`--port 8081:80` is accepted, `podman ps` reports `0.0.0.0:8081->80/tcp`, and a
listener appears on the host:

```
root   conmon   2621  5 tcp4  *:8081   *:*
```

Pick a host port other than 8080 while you are at it: hospitusd listens on
127.0.0.1:8080, and a container published there shadows it, so the CLI's own
requests reach the container instead of the daemon.

Connections to a published port time out anyway. This is podman on FreeBSD, not
hospitus:
`podman run -d -p 8086:80 caddy:2` behaves the same with hospitus out of the
picture, on a host whose PF ruleset blocks nothing.

The container itself is reachable on its own address:

```sh
$ doas podman inspect web --format '{{.NetworkSettings.IPAddress}}'
10.88.0.66
$ fetch -qo- http://10.88.0.66/
<title>Caddy works!</title>
```

Use that address, or put the service in a jail, where hospitus does the port
forwarding itself through PF. `hospitus podman list` and `hospitus podman info`
report the same address, so podman only has to be asked when hospitusd is not
running.

## Imperative CLI

Hospitus provides imperative CLI commands for quick Podman container operations.

### Create a Container

```bash
# Create a web server with port forwarding
hospitus podman create web --image docker.io/library/caddy:2 --port 8081:80

# Create with environment variables and volume
hospitus podman create app --image myapp:1.0 \
    --port 8081:80 \
    --env APP_ENV=production \
    --env LOG_LEVEL=info \
    --volume /data/app:/app/data \
    --label tier=frontend

# Create and start immediately
hospitus podman create db --image postgres:16 \
    --env POSTGRES_PASSWORD=secret \
    --port 5432:5432 \
    --start
```

**Key flags**:

| Flag | Description |
|------|-------------|
| `--image` | Container image (required) |
| `-p, --port host:container` | Port forwarding (repeatable) |
| `-e, --env KEY=VALUE` | Environment variable (repeatable) |
| `-v, --volume host:container` | Volume mount (repeatable) |
| `--label KEY=VALUE` | Label (repeatable) |
| `--cmd` | Override container command |
| `--start` | Start after creation |
| `--description` | Human-readable description |
| `--auto-start` | Start the container when the host boots |
| `--auto-start-priority` | Boot order, 0–100 (default `50`) |
| `--auto-start-delay` | Milliseconds to wait after the previous container |

### Lifecycle

```bash
# List all podman containers
hospitus podman list

# Show container details
hospitus podman info web

# Start/stop/restart
hospitus podman start web
hospitus podman stop web
hospitus podman restart web

# Destroy a container
hospitus podman destroy web -y
```

### Exec, Console, Logs

```bash
# Execute a non-interactive command inside a running container
hospitus podman exec web -- caddy version

# Interactive shell (console)
hospitus podman console web

# View container logs (requires root)
doas hospitus podman logs web
```

### Stats and Monitoring

```bash
# Show resource usage
hospitus podman stats web

# JSON output for scripting
hospitus podman stats web --output json
```

Expect it to take about a minute. Podman's own `stats` call costs roughly 47
seconds per container on FreeBSD; jails and bhyve VMs answer immediately.

### Snapshots (podman commit)

Podman snapshots are implemented via `podman commit` — they capture the container's
current filesystem as a new image.

```bash
# Create a snapshot (container can be running or stopped)
hospitus podman snapshot create web before-config-change

# List snapshots
hospitus podman snapshot list web

# Restore from snapshot (stops and recreates the container)
hospitus podman snapshot restore web before-config-change

# Delete a snapshot
hospitus podman snapshot delete web old-snapshot -y
```

---

## UWM Manifests (Declarative)

For production deployments and multi-container stacks, use Unified Workload Manifests
for version-controlled, GitOps-ready configuration. See the [UWM Overview](../uwm/overview.md)
for complete reference.

### Scenario 1: Your First Container

**Goal**: Deploy a simple web-server container using a manifest.

### Step 1: Create the Manifest

Create a file `web.toml`. It runs Caddy, not nginx — see [Linux images on
FreeBSD](#linux-images-on-freebsd) above for why that matters here:

```toml
# web.toml - Simple Caddy container
[workload]
api_version = "hospitus.io/v1"
name = "my-web"

[workload.labels]
app = "caddy"
environment = "development"

[provider]
type = "podman"

[image]
source = "oci:caddy:2"

[resources]
cpu = 1
memory = "256Mi"

[[networks]]
name = "default"
type = "nat"

[[networks.ports]]
host = 8081
container = 80
protocol = "tcp"
```

### Step 2: Validate the Manifest

```bash
hospitus manifest validate web.toml
```

**Output**:
```
Manifest is valid
```

### Step 3: Preview the Deployment

```bash
hospitus apply --dry-run web.toml
```

**Output**:
```
Applying workload: my-web
  Provider: podman
  Image: oci:caddy:2
  CPUs: 1
  Memory: 256Mi
  Networks:
    - default (nat)

[Dry run] Would create workload - no changes made
```

### Step 4: Deploy the Container

```bash
hospitus apply --start web.toml
```

**Output**:
```
Applying workload: my-web
  Provider: podman
  Image: oci:caddy:2
  CPUs: 1
  Memory: 256Mi
  Networks:
    - default (nat)

Creating podman instance...
Created instance: my-web
Starting instance...
Instance started successfully
```

### Step 5: Verify the Container

```bash
# List containers
doas podman ps

# The published port does not carry traffic on FreeBSD, so reach the
# container on its own address (see "Published ports do not carry traffic").
addr=$(doas podman inspect my-web --format '{{.NetworkSettings.IPAddress}}')
fetch -qo- "http://$addr/"
```

### Step 6: Delete the Container

```bash
hospitus manifest delete -y web.toml
```

---

## Scenario 2: Web Application with Port Forwarding

**Goal**: Deploy a Node.js application with multiple ports.

### Step 1: Create the Manifest

Create `webapp.toml`:

```toml
# webapp.toml - Node.js web application
[workload]
api_version = "hospitus.io/v1"
name = "webapp"

[workload.labels]
app = "webapp"
tier = "frontend"

[provider]
type = "podman"

[image]
source = "oci:node:20-alpine"

[resources]
cpu = 2
memory = "512Mi"

# Environment variables reach the container as a plain table
[environment]
NODE_ENV = "production"
PORT = "3000"

[provider_overrides.podman]
command = ["node", "server.js"]

[[networks]]
name = "web"
type = "nat"

# HTTP port
[[networks.ports]]
host = 3000
container = 3000
protocol = "tcp"

# WebSocket port
[[networks.ports]]
host = 3001
container = 3001
protocol = "tcp"
```

### Step 2: Deploy with Volume Mount

Add a volume for your application code:

```toml
# Add to webapp.toml
[[storage.volumes]]
name = "code"
host_path = "/home/myuser/webapp"
mount_path = "/app"
read_only = true
```

### Step 3: Deploy

```bash
hospitus apply --start webapp.toml
```

### Step 4: View Logs

```bash
doas podman logs webapp
```

---

## Scenario 3: Database Container with Volumes

**Goal**: Deploy PostgreSQL with persistent storage.

### Step 1: Create the Manifest

Create `postgres.toml`:

```toml
# postgres.toml - PostgreSQL database
[workload]
api_version = "hospitus.io/v1"
name = "postgres-db"

[workload.labels]
app = "postgres"
tier = "database"

[provider]
type = "podman"

[image]
source = "oci:postgres:16-alpine"

[resources]
cpu = 2
memory = "1Gi"

# Environment configuration
[environment]
POSTGRES_USER = "myapp"
POSTGRES_PASSWORD = "{{ secret `pg_password` }}"
POSTGRES_DB = "myappdb"

# Persistent volume for data
[[storage.volumes]]
name = "pgdata"
mount_path = "/var/lib/postgresql/data"
size = "10Gi"

[[networks]]
name = "database"
type = "bridge"

[[networks.ports]]
host = 5432
container = 5432
protocol = "tcp"
```

### Step 2: Deploy

```bash
hospitus apply --start postgres.toml
```

### Step 3: Connect to Database

```bash
# Using psql client
psql -h localhost -U myapp -d myappdb

# Or exec into container
doas podman exec -it postgres-db psql -U myapp -d myappdb
```

### Step 4: Backup Database

```bash
# Dump database
doas podman exec postgres-db pg_dump -U myapp myappdb > backup.sql
```

---

## Scenario 4: Multi-Container Stack

**Goal**: Deploy a complete application with database, cache, and web server.

### Step 1: Create the Stack Manifest

Create `app-stack.toml`:

```toml
# app-stack.toml - Full application stack
[stack]
api_version = "hospitus.io/v1"
name = "myapp"

# PostgreSQL Database
[[instances]]
name = "db"
provider = "podman"

[instances.image]
source = "oci:postgres:16-alpine"

[instances.resources]
cpu = 2
memory = "2Gi"

[instances.environment]
POSTGRES_PASSWORD = "{{ secret `db_password` }}"

[[instances.networks]]
name = "internal"
type = "bridge"

# Redis Cache
[[instances]]
name = "cache"
provider = "podman"

[instances.image]
source = "oci:redis:7-alpine"

[instances.resources]
cpu = 1
memory = "512Mi"

[instances.depends_on]
services = ["db"]

[[instances.networks]]
name = "internal"
type = "bridge"

# Application
[[instances]]
name = "app"
provider = "podman"

[instances.image]
source = "oci:myapp:latest"

[instances.resources]
cpu = 2
memory = "1Gi"

[instances.depends_on]
services = ["db", "cache"]

[[instances.networks]]
name = "internal"
type = "bridge"

# Caddy reverse proxy
[[instances]]
name = "proxy"
provider = "podman"

[instances.depends_on]
services = ["app"]

[instances.image]
source = "oci:caddy:2"

[instances.resources]
cpu = 1
memory = "256Mi"

[[instances.networks]]
name = "internal"
type = "bridge"

[[instances.networks]]
name = "public"
type = "nat"

[[instances.networks.ports]]
host = 80
container = 80
protocol = "tcp"

[[instances.networks.ports]]
host = 443
container = 443
protocol = "tcp"
```

### Step 2: Deploy the Stack

```bash
# Preview deployment order
hospitus apply --dry-run app-stack.toml

# Deploy
hospitus apply --start app-stack.toml
```

Instances deploy sequentially in dependency order and are named
`<stack>_<instance>`:

**Output** (abridged — each instance repeats the same create/start block):
```
Applying stack: myapp
  Instances: 4

Deployment order:
  1. db (podman)
  2. cache (podman)
  3. app (podman)
  4. proxy (podman)

Creating instances...

Creating myapp_db...
Applying workload: myapp_db
  Provider: podman
  Image: oci:postgres:16-alpine
  CPUs: 2
  Memory: 2Gi
  Networks:
    - internal (bridge)

Creating podman instance...
Created instance: 9f2c1d4e8a7b6c5d4e3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a3b2c1d
Starting instance...
Instance started successfully
  Waiting for instance to stabilize...

Creating myapp_cache...
[...]

Stack myapp deployed successfully!
```

### Step 3: View Running Containers

The containers carry the stack-prefixed names (`myapp_db`, `myapp_cache`, …):

```bash
hospitus podman list

# Or ask podman directly
doas podman ps
```

---

## Scenario 5: Environment Variables and Secrets

**Goal**: Securely manage configuration and secrets.

### Environment Variables

Environment variables are a plain `[environment]` table:

```toml
[environment]
APP_ENV = "production"
LOG_LEVEL = "info"
```

Values go through the template layer first, so a variable
(`{{ .var }}`) or a generated secret can feed one. The `env` template
function reads the *daemon's* environment and only for an allow-list of
safe names (`HOME`, `USER`, `LANG`, …) — it is not a way to pass
arbitrary host variables through.

### Using Secrets Files

Mount a host file into the container and point the application at it:

```toml
[[storage.volumes]]
name = "db-password"
host_path = "/etc/secrets/db-password"
mount_path = "/run/secrets/db-password"
read_only = true

[environment]
DB_PASSWORD_FILE = "/run/secrets/db-password"
```

### Using Hospitus Secrets

`{{ secret `name` }}` in a manifest generates a random value on first
use and persists it in the Hospitus secret store, scoped to the workload
name:

```toml
[environment]
DB_PASSWORD = "{{ secret `db-password` }}"
```

```bash
# Inspect, rotate, or remove it later (scope = workload name)
hospitus secret get my-web db-password
hospitus secret rotate my-web db-password
hospitus secret rm my-web db-password
```

---

## Scenario 6: Resource Limits and Health Checks

**Goal**: Configure resource constraints and health monitoring.

### Resource Limits

`cpu` and `memory` are the two resource knobs a manifest takes:

```toml
[resources]
cpu = 2                    # Number of CPUs
memory = "1Gi"             # Memory limit
```

### Health Checks

Health checks live under `[lifecycle.health_check]` and run a command
inside the instance:

```toml
[lifecycle.health_check]
command = ["curl", "-f", "http://localhost:8080/health"]
interval = "30s"
timeout = "10s"
retries = 3
start_period = "5s"
```

There is no `restart_policy` key. In a stack deployment the
orchestrator restarts an instance whose health check fails; for start
at boot, use `[lifecycle.autostart]`.

### Complete Example

```toml
# production-app.toml
[workload]
api_version = "hospitus.io/v1"
name = "production-app"

[provider]
type = "podman"

[image]
source = "oci:myapp:v2.1.0"

[resources]
cpu = 4
memory = "4Gi"

[lifecycle.health_check]
command = ["/app/healthcheck"]
interval = "30s"
timeout = "5s"
retries = 3

[[networks]]
name = "production"
type = "bridge"

[[networks.ports]]
host = 8080
container = 8080
protocol = "tcp"
```

---

## Security Notes

### Containers run as root

Podman has no rootless mode on FreeBSD:

```console
$ podman version
Error: rootless mode is not supported on FreeBSD - run podman as root
```

Containers therefore run as root, and so does the hospitusd that invokes podman.
The rootless mode Podman's own documentation recommends applies to a Linux host
reached through `podman-remote`, and to the Linux VM behind `podman machine` on
macOS — not to containers hospitus runs on a FreeBSD host.

What is left to you:
- Pin image tags (`nginx:1.25`) rather than `latest` in production
- Pull from registries you trust
- Reach for a jail when the workload needs confining. It is the FreeBSD
  isolation primitive, and hospitus gives it firewall rules, resource limits and
  its own network stack.

### Image Security

```bash
# Verify image digest before use
doas podman pull nginx:alpine
doas podman inspect nginx:alpine | grep -i digest

# Use image signing if available
doas podman trust show
```

---

## Manifest Reference

### Workload Structure

```toml
[workload]
api_version = "hospitus.io/v1"    # optional; this is the only version
name = "container-name"         # Required: Container name

[workload.labels]
key = "value"                   # Optional: Labels

[provider]
type = "podman"                 # Required: Provider type

[image]
source = "oci:image:tag"        # Required: OCI image

[resources]
cpu = 1                         # Optional: CPU count
memory = "512Mi"                # Optional: Memory limit
```

### Image Sources

| Format | Description | Example |
|--------|-------------|---------|
| `oci:image:tag` | Docker Hub image | `oci:nginx:alpine` |
| `oci:registry/image:tag` | Custom registry | `oci:ghcr.io/org/app:v1` |

### Network Types

| Type | Description | Use Case |
|------|-------------|----------|
| `nat` | NAT with port forwarding | Default, simple access |
| `bridge` | Bridge network | Container-to-container |
| `none` | No network | Air-gapped containers |

### Port Forwarding

```toml
[[networks.ports]]
host = 8080                     # Host port
container = 80                  # Container port
protocol = "tcp"                # tcp or udp
```

### Volume Mounts

```toml
[[storage.volumes]]
name = "data"                   # Required: volume name
host_path = "/data/app"         # Host path (bind mount)
mount_path = "/app/data"        # Container path
read_only = false               # Optional: Read-only mount
```

### Environment Variables

```toml
[environment]
VAR_NAME = "value"
```

### Common Commands

| Command | Description |
|---------|-------------|
| `hospitus manifest validate <file>` | Validate manifest |
| `hospitus apply --dry-run <file>` | Preview changes |
| `hospitus apply --start <file>` | Deploy and start |
| `hospitus manifest delete -y <file>` | Delete resources |

---

## Troubleshooting

### Podman Service Not Running

**Error**: "connection refused" or "socket not found"

```bash
# Check podman service status
service podman status

# Start the service
doas service podman start

# Enable on boot
doas sysrc podman_enable=YES
```

### Container Won't Start

**Issue**: "Image not found"
```bash
# Pull image manually
doas podman pull nginx:alpine

# Or let Hospitus auto-pull
# (auto-pull is enabled by default)
```

**Issue**: "Port already in use"
- Change the host port in your manifest
- Check what's using the port: `sockstat -4 -l | grep <port>`

### Network Issues

**Issue**: Containers can't communicate
- Ensure containers are on the same network
- Use container names for DNS resolution

**Issue**: Can't access from host
- Verify port forwarding configuration
- Check firewall rules

### Permission Issues

**Issue**: "Permission denied"
- Podman runs as root on FreeBSD (no rootless mode — see
  [Security Notes](#security-notes)); make sure hospitusd runs as root
- Check volume mount permissions

### Performance Issues

**Tips for better performance**:
1. Use Alpine-based images for smaller size
2. Limit resources appropriately
3. Use read-only mounts where possible
4. Enable health checks for automatic recovery

---

## Next Steps

- **[Jail Guide](jails.md)**: FreeBSD native containers
- **[QEMU Guide](qemu.md)**: Virtual machines
- **[UWM Overview](../uwm/overview.md)**: Complete UWM reference
- **[API Reference](../developer-guide/api-reference.md)**: REST API documentation
