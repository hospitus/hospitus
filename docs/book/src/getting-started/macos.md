# Running Hospitus on macOS

macOS runs four providers. Two are cross-platform: **QEMU**, accelerated through
Hypervisor.framework, and **Podman**, through `podman machine`. Two are
macOS-only and sit directly on Apple's Virtualization.framework: **vfkit** for
virtual machines and **container** for Linux containers. Jails and bhyve are
FreeBSD kernel features and are not available — `hospitus init --check` reports them
as skipped rather than missing.

| Provider | Runs | Needs |
|----------|------|-------|
| `vfkit` | arm64 VMs, natively accelerated | `brew install vfkit` |
| `container` | OCI containers, one VM each | `brew install container`, macOS 26+, Apple silicon |
| `qemu` | VMs of any architecture (foreign ones emulated) | `brew install qemu` |
| `podman` | OCI containers | `brew install podman`, a running `podman machine` |

Nothing here needs root. QEMU with HVF and a user-owned `podman machine` both
run unprivileged, so the daemon, its data directory, the API key and the VM
disks all stay owned by your user.

---

## Prerequisites

```sh
brew install qemu podman vfkit container

podman machine init      # first time only
podman machine start

container system start                    # starts the background service
container system kernel set --recommended # first time only
```

`vfkit` and `container` are optional: without them the two cross-platform
providers still work, and the doctor reports them as warnings rather than
failures.

Go 1.25+ is needed to build from source.

Once the binaries are installed — the next section does that — check the host
with:

```sh
hospitus init --check
```

Expect `0 failed`; the skipped entries are the FreeBSD-only checks.

---

## Running the daemon as a service

macOS has no rc.d. `scripts/hospitus-macos-service.sh` is the counterpart of the
FreeBSD `rc.d` script: it installs the binaries, creates the directories,
generates an API key, renders a launchd agent and loads it.

```sh
make build
sh scripts/hospitus-macos-service.sh install

sh scripts/hospitus-macos-service.sh status
sh scripts/hospitus-macos-service.sh key
```

| What | Where |
|------|-------|
| Binaries | `~/.local/bin/hospitusd`, `~/.local/bin/hospitus` |
| Data, database, API key | `~/Library/Application Support/hospitus` |
| Logs and audit log | `~/Library/Logs/hospitus` |
| launchd agent | `~/Library/LaunchAgents/io.github.hospitus.hospitus.plist` |

The agent is a **LaunchAgent**, not a LaunchDaemon, and it copies the binaries
out of your checkout on install. Both details matter: launchd refuses to execute
a binary on a volume it has no access to — a build on an external disk hangs in
dyld with an empty log — and a copy means rebuilding the repository does not
swap the running daemon's binary underneath it.

Reinstalling is idempotent. To remove it:

```sh
sh scripts/hospitus-macos-service.sh uninstall      # keeps your data
sh scripts/hospitus-macos-service.sh uninstall -p   # also removes data and key
```

`-p` removes the data directory, and with it the database and every QEMU VM.
Podman containers live in Podman's own store, not in that directory, so they
survive: remove them with `podman rm -f <name>` if you want a clean slate.
Hospitus notices containers deleted behind its back and does not hold their names.

Point the CLI at the daemon:

```sh
hospitus context add local --url http://127.0.0.1:8080 \
	--api-key "$(sh scripts/hospitus-macos-service.sh key)"
hospitus context use local
```

The agent listens on `127.0.0.1:8080` without TLS, matching the FreeBSD service
default. Exposing it on the network means editing the plist to add
`--tls-cert` / `--tls-key` and changing `--addr`.

---

## Containers with Podman

```sh
hospitus podman create web --image docker.io/library/nginx:alpine -p 8081:80 --start
hospitus podman list
hospitus podman logs web
hospitus podman destroy -f -y web  # -f stops it first, -y skips the prompt
```

Not port 8080: the daemon has it. Podman publishes through `gvproxy`, which
binds every interface, so a container on 8080 does not collide outright — it
just answers on `localhost:8080` while the daemon answers on `127.0.0.1:8080`,
and a CLI pointed at `localhost` gets the container's HTTP error page instead
of the API.

Port mappings are published by Podman itself, and networking is handled by
`podman machine`. Hospitus does not manage the host firewall on macOS: the
bridge, NAT and port-forwarding operations that back the jail provider on
FreeBSD return an explicit "not supported on this platform" error here rather
than pretending to succeed.

---

## Virtual machines with QEMU

**Match the guest architecture to the Mac.** HVF runs guest instructions on the
host CPU, so it only accelerates a guest of the host's own architecture. On
Apple Silicon that means arm64 images; an amd64 image still runs, but under TCG
emulation, which is perhaps an order of magnitude slower.

The catalog carries arm64 cloud images for exactly this:

```sh
hospitus image available --arch arm64
hospitus image fetch alpine-3.20-arm64

hospitus qemu create alp --image alpine-3.20-arm64 --arch arm64 --cpus 2 --memory 1024
hospitus qemu start alp
```

Hospitus picks the right QEMU invocation from the architecture: `-machine virt` and
`-accel hvf -cpu host` for a native arm64 guest, `-machine q35` for x86, and
`-accel tcg` with an architecture-appropriate CPU model whenever the guest is
foreign. Check what a running VM actually got:

```sh
ps -o command= -p "$(pgrep -f 'qemu-system-aarch64 -name alp')" | tr ' ' '\n' | grep -A1 accel
```

UEFI firmware comes from the QEMU install (`/opt/homebrew/share/qemu` on Apple
Silicon, `/usr/local/share/qemu` on Intel). Each VM gets its own copy of the
variables store, so boot entries persist across restarts.

---

## Virtual machines with vfkit

`vfkit` puts a VM straight on Virtualization.framework, with no QEMU in the
middle. Only guests of the host's own architecture run — the framework executes
guest instructions on the host CPU and has no emulation fallback — so on Apple
silicon that means arm64.

```sh
hospitus image fetch alpine-3.20-arm64

hospitus vfkit create vm1 --image alpine-3.20-arm64 --arch arm64 --cpus 2 --memory 1024
hospitus vfkit start vm1
hospitus vfkit list
hospitus vfkit stats vm1
hospitus vfkit destroy -f -y vm1
```

Each VM is its own process with a pidfile and a REST control socket, so it
survives a daemon restart — reinstall the service while a VM runs and the same
process is still there afterwards. Disks are raw because the framework reads
nothing else, so a qcow2 from the catalog is converted with `qemu-img` at
creation.

What the framework does not offer, and this provider therefore refuses rather
than pretending: snapshots, migration, disk hotplug, and changing CPU or memory
on an existing VM.

---

## Containers with Apple's container tool

`container` runs each OCI container inside its own lightweight VM. It is
pre-1.0, so a minor upgrade can change its behavior.

```sh
container system start   # the provider refuses to register without it
```

Then the usual lifecycle:

```sh
hospitus container create c1 --image docker.io/library/alpine:latest --cpus 2 --memory 512
hospitus container list
hospitus container destroy -f -y c1
```

An image that exits as soon as it starts, like a bare `alpine`, needs a command
to hold it open. An instance spec has no field for one, so it travels in
provider config, which the CLI does not expose. Nor can a manifest: the
manifest validator accepts only jail, bhyve, qemu and podman, so the two
macOS providers are driven by the CLI or the API alone. For a command, that
means the API:

```sh
curl -X POST -H "X-API-Key: $KEY" -H 'Content-Type: application/json' \
	-d '{"name":"c1","image":"docker.io/library/alpine:latest","cpus":2,
	     "memory_mb":512,"provider_config":{"command":["sleep","120"]},
	     "labels":{"provider":"container"}}' \
	http://127.0.0.1:8080/api/v1/instances
```

---

## Limits worth knowing

**Unix socket paths.** QEMU's QMP, monitor and serial sockets live in the VM
directory, and a Unix socket path cannot exceed 104 bytes on macOS. The default
data directory leaves room for a normal instance name; a deep custom
`--data-dir` or a very long instance name does not. Hospitus checks this when the
VM is created and names the cause rather than letting QEMU fail later with a
path you never typed. The fix is a shorter `--data-dir`.

**Interactive consoles.** The WebSocket console is a jail feature and returns an
error on macOS. QEMU VMs expose a serial socket in the VM directory and a VNC
display; `hospitus qemu console <name>` prints the display actually allocated to
that VM.

**Storage.** ZFS-backed snapshots, clones and backups are FreeBSD features.
QEMU disk snapshots go through qcow2, and Podman through its own image store.
