# Quick Start Guide

Get up and running with Hospitus in under 10 minutes on FreeBSD.

## Prerequisites

- **FreeBSD 14.0+** with ZFS
- Go 1.25+ (to build from source)
- `doas` configured (or `sudo`)
- For bhyve: `vmm.ko` loaded (`kldload vmm`)
- For resource limits: `kern.racct.enable=1` in `/boot/loader.conf`

## Build and Install

```bash
git clone https://github.com/hospitus/hospitus.git
cd hospitus

make build

# Binaries are at ./hospitusd and ./hospitus — install them:
doas cp hospitusd hospitus /usr/local/bin/

# Verify
hospitusd --version
hospitus --version
```

## Start the Daemon

```bash
# Create data directory
doas mkdir -p /var/lib/hospitus/state

# Start in foreground (first run / testing)
doas hospitusd --data-dir /var/lib/hospitus --state-dir /var/lib/hospitus/state \
            --db /var/lib/hospitus/hospitus.db --allow-no-auth --allow-insecure-tls
```

Both `--allow-*` flags are for this first look only, and without them the daemon
does not start: it refuses to serve plaintext (`TLS required: configure
certificates or set allow_insecure_tls=true`) and, with authentication on and no
key configured, would reject every request. Past a first look, give it a
certificate and a key file instead — see [Installation](installation.md).

In another terminal, confirm it's running:

```bash
curl http://127.0.0.1:8080/health
```

For production, install the rc.d service with the provided script (the service
is named `hospitus`, not `hospitusd`):

```bash
doas ./scripts/setup-boot.sh
doas service hospitus start
```

## Your First Jail

Jails are the fastest way to start. They need only ZFS and do not require any VM hardware.

```bash
# List available FreeBSD images
hospitus image available

# Fetch a base image (downloads ~180 MB)
hospitus image fetch 14.3-RELEASE-amd64

# Create a jail with VNET networking. Without --ip the jail starts with no
# interface at all, and anything that reaches the network fails to resolve.
hospitus jail create web --image 14.3-RELEASE-amd64 --vnet --ip 10.0.0.10/24 \
     --cpus 2 --memory 512

# Start it
hospitus jail start web

# Check status
hospitus jail list
hospitus jail info web

# Run a command inside
hospitus jail exec web hostname

# Open an interactive shell
hospitus jail console web

# Stop and destroy
hospitus jail stop web
hospitus jail destroy -y web
```

## Your First bhyve VM

Requires `vmm.ko` and an image. Uses NAT networking with DHCP out of the box.

```bash
# Load the bhyve kernel module (add to /boot/loader.conf for persistence)
doas kldload vmm

# Fetch a cloud image. A VM boots from a disk image, not from the base set a
# jail is built out of: freebsd-14.3-RELEASE-amd64 is the latter, and the
# catalog lists it for jails only.
hospitus image fetch freebsd-14.3-RELEASE-amd64-zfs

# Create a VM
hospitus bhyve create myvm --image freebsd-14.3-RELEASE-amd64-zfs --cpus 2 --memory 2048

# Start it
hospitus bhyve start myvm

# Get its IP address (assigned via DHCP)
hospitus bhyve info myvm

# Enable a graphical console, then connect (VNC is off by default)
hospitus bhyve vnc myvm --enable
hospitus bhyve restart myvm
hospitus bhyve vnc myvm

# Stop and destroy
hospitus bhyve stop myvm
hospitus bhyve destroy -y myvm
```

## Your First QEMU VM

Works on FreeBSD without any kernel modules for QEMU system emulation.

```bash
hospitus image fetch ubuntu-24.04-amd64

hospitus qemu create ubuntu-test --image ubuntu-24.04-amd64 --cpus 2 --memory 2048
hospitus qemu start ubuntu-test
hospitus qemu info ubuntu-test
hospitus qemu stop ubuntu-test
hospitus qemu destroy -y ubuntu-test
```

## Your First Podman Container

Requires Podman installed (`pkg install podman`).

```bash
hospitus podman create caddy-test --image caddy:2
hospitus podman start caddy-test
hospitus podman list
hospitus podman info caddy-test
hospitus podman stop caddy-test
hospitus podman destroy -y caddy-test
```

## Useful Commands

```bash
# List downloaded images
hospitus image list

# List async jobs (for long operations)
hospitus job list
hospitus job info <job-id>

# Snapshots (jails and bhyve). A rollback rewrites the dataset under the
# running jail, so restore refuses one that is running.
hospitus jail snapshot create web snap1
hospitus jail snapshot list web
hospitus jail stop web
hospitus jail snapshot restore web snap1
hospitus jail start web

# Port forwarding (jails with VNET). Protocol is part of the port spec:
#   80        same port host/jail (TCP)   80:8080   host 80 -> jail 8080
#   udp/53    UDP                          tcp/80:8080  explicit TCP
hospitus jail expose add web --port 80:80
hospitus jail expose list web
```

## API Access

The daemon exposes a REST API on `http://127.0.0.1:8080`:

```bash
# Health check
curl http://127.0.0.1:8080/health

# List all instances across providers
curl http://127.0.0.1:8080/api/v1/instances

# List jails
curl http://127.0.0.1:8080/api/v1/instances?provider=jail
```

## Troubleshooting

| Problem | Solution |
|---------|----------|
| `Permission denied` | Only `hospitusd` needs root — the `hospitus` CLI is an HTTP client. Prefix `doas` on daemon, `kldload` and `zfs` commands, not on `hospitus` ones |
| `Missing API key` | Set up a context (`hospitus context`), or run the CLI as root on the daemon host so it can read `/usr/local/etc/hospitus/api.key` |
| `DHCP not working` in bhyve | Install dnsmasq: `pkg install dnsmasq`, then restart hospitusd |
| `vmm.ko not loaded` | Run `doas kldload vmm` or add `vmm_load="YES"` to `/boot/loader.conf` |
| `RCTL limits not applied` | Add `kern.racct.enable=1` to `/boot/loader.conf` and reboot |
| `ZFS dataset not found` | Verify `zroot/hospitus` exists: `zfs list \| grep hospitus` |
| No network in jail | Ensure VNET is available: `sysctl kern.features.vimage` |

## Next Steps

- **[Your First Jail](first-jail.md)** — Step-by-step jail tutorial
- **[CLI Reference](../user-guide/cli-reference.md)** — Complete command reference
- **[bhyve Setup Guide](../guides/bhyve-setup-guide.md)** — bhyve configuration and templates
- **[Jail Networking](../guides/jail-networking-walkthrough.md)** — VNET, bridges, port forwarding
- **[Manifests (UWM)](../uwm/)** — Declarative infrastructure with `hospitus apply`

