# CLI Reference

Complete reference for the `hospitus` command-line interface.

## Global Flags

These persistent flags are accepted by every command (they control how the CLI
connects to `hospitusd`):

| Flag | Description | Default |
|------|-------------|---------|
| `--api-url` | hospitusd URL (overrides context) | `http://127.0.0.1:8080` |
| `--api-key` | API key for authentication (overrides context) | |
| `--context` | Named context from the config file | active context |
| `--config` | Config file path | `~/.config/hospitus/config.yaml` |
| `--tls-ca` | CA certificate bundle (PEM) for custom/self-signed TLS | |
| `--tls-skip-verify` | Skip TLS certificate verification (insecure) | `false` |

Two other flags are **per-command**, not global:

- `-o`, `--output` — `table` or `json`, available on read commands
  (`list`, `info`, `health`, `stats`, …).
- `--timeout` — a duration flag on `manifest apply` / `manifest delete` for
  long-running provisioning. The general HTTP client timeout is set with the
  `HOSPITUS_TIMEOUT` environment variable, not a global flag.

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `HOSPITUS_API_URL` | hospitusd URL (used when no context/flag is set) | `http://127.0.0.1:8080` |
| `HOSPITUS_API_KEY` | API key (used when no context/flag is set) | |
| `HOSPITUS_TIMEOUT` | HTTP client timeout (e.g. `30s`, `5m`, `120`) | `30s` |

> Connection precedence, highest first: `--api-url`/`--api-key` flags → active
> context → `HOSPITUS_API_URL`/`HOSPITUS_API_KEY` → the local daemon's key file for a
> loopback URL only (`/usr/local/etc/hospitus/api.key`) → built-in default. There are no
> `HOSPITUS_TLS_*` variables; use `--tls-ca` / `--tls-skip-verify` or a context's
> `tls_ca_cert` field. See [Contexts](contexts.md).

### CBSD-Style Aliases

Short prefix aliases: `j` (jail), `b` (bhyve), `q` (QEMU), `p` (podman).

| Alias | Equivalent | Alias | Equivalent |
|-------|-----------|-------|-----------|
| `jcreate` / `jstart` / `jstop` / `jrestart` | `hospitus jail create` / `start` / `stop` / `restart` | `jdestroy` | `hospitus jail destroy` |
| `jls` / `jinfo` / `jexec` / `jlogin` | `hospitus jail list` / `info` / `exec` / `console` | | |
| `bcreate` / `bstart` / `bstop` / `brestart` | `hospitus bhyve create` / `start` / `stop` / `restart` | `bdestroy` | `hospitus bhyve destroy` |
| `bls` / `binfo` / `blogin` | `hospitus bhyve list` / `info` / `console` | | |
| `qcreate` / `qstart` / `qstop` / `qrestart` | `hospitus qemu create` / `start` / `stop` / `restart` | `qdestroy` | `hospitus qemu destroy` |
| `qls` / `qinfo` | `hospitus qemu list` / `info` | | |
| `pstart` / `pstop` / `prestart` / `pdestroy` | `hospitus podman start` / `stop` / `restart` / `destroy` | | |
| `pls` / `pinfo` / `pexec` / `plogin` / `plogs` | `hospitus podman list` / `info` / `exec` / `console` / `logs` | | |

---

## Jail Commands

```
hospitus jail autostart|create|list|info|start|stop|restart|destroy|rename|set|exec|console|health|stats|snapshot|clone|expose|tmux|volume|network|service|upgrade|export|import
```

### `hospitus jail create`

```
hospitus jail create <name> [flags]
```

**Resource & image flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--cpus` (`-c`) | `1` | Number of CPUs |
| `--memory` (`-m`) | `512` | Memory in MB |
| `--image` | | Image name (e.g. `freebsd-14.3-RELEASE-amd64`) |
| `-V` | | Version shortcut: `-V 14.3` → `freebsd-14.3-RELEASE-amd64` |
| `--arch` | `native` | `amd64`, `arm64`, `riscv64`, `i386` |
| `--start` | `false` | Start immediately after creation |
| `--pull` | `false` | Download a missing image instead of asking (required when not on a terminal) |
| `--os-type` | `freebsd` | `freebsd` or `linux`. Read from the image name when the image is a `-rootfs-` one, so Linux images need no flag |
| `--os-version` | | Distribution for a Linux jail built by debootstrap, e.g. `ubuntu-22.04` |
| `--description` | | Free text shown by `info` and `list` |

`hospitus jail create` also accepts `--bootloader`, `--sector-size` and `--disk`
from the flag set it shares with `bhyve` and `qemu`. A jail has no use for
them. `--cloud-init` is refused outright rather than ignored: provision a jail
with lifecycle hooks or with the commands a manifest runs after creation.

**Network flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--vnet` | `false` | Enable VNET virtual network stack |
| `--bridge` (`-b`) | | Bridge interface |
| `--ip` (`-i`) | | IPv4 with CIDR (e.g. `10.0.0.10/24` or `dhcp`) |
| `--vlan` | `0` | VLAN tag |
| `--bridge-flag` | | Bridge member flags (e.g. `private`) |

**Security flags:** `--allow-raw-sockets`, `--allow-sysvipc`, `--allow-mount`, `--allow-mount-devfs`, `--allow-mount-nullfs`, `--allow-mount-tmpfs`, `--allow-mount-zfs`, `--allow-vmm`, `--allow-mlock`, `--allow-reserved-ports`

**Lifecycle hooks:** `--exec-prestart`, `--exec-poststart`, `--exec-prestop`, `--exec-poststop`, `--exec-clean`

**Auto-start:** `--auto-start`, `--auto-start-priority` (0–100, default `50`), `--auto-start-delay` (ms)

**Behavior:** `--persist`, `--devfs-ruleset` (default `4`), `--children-max`, `--securelevel` (−1 to 3)

**Resource limits:** `--max-proc`, `--read-bps`, `--write-bps`, `--read-iops`, `--write-iops`

**Examples:**
```bash
hospitus jail create myjail
hospitus jail create web --vnet --ip 10.0.0.10/24 -V 14.3 --cpus 2 --memory 1024
hospitus jail create pgdb --allow-sysvipc -V 14.3 --cpus 4 --memory 4096
hospitus jail create arm-test --image freebsd-14.3-RELEASE-arm64 --vnet --ip dhcp
```

### `hospitus jail list`

```
hospitus jail list [--output json]       # Aliases: ls
```

### `hospitus jail info`

```
hospitus jail info <name> [--output json]    # Aliases: get, show
```

### `hospitus jail start` / `stop` / `restart`

```
hospitus jail start <name>
hospitus jail stop <name> [--force | -f]
hospitus jail restart <name>
```

### `hospitus jail destroy`

```
hospitus jail destroy [<name>...] [--force | -f] [--yes | -y] [--pattern | -x <glob>]
```

Supports multiple names and glob patterns. Aliases: `delete`, `rm`, `remove`.

### `hospitus jail set`

```
hospitus jail set <name> [--cpus <n>] [--memory <mb>] [--description <text>]
                      [--allow-raw-sockets] [--allow-sysvipc] [--allow-mlock]
                      [--allow-reserved-ports] [--allow-mount] [--persist]
                      [--auto-start] [--auto-start-priority <0-100>]
```

Some changes require a restart.

### `hospitus jail exec`

```
hospitus jail exec <name> <command> [args...] [-u <user>] [-w <workdir>] [-i]
```

Streams output via the API. Use `-i` for interactive TTY (runs locally via `jexec`). Flags after `-i` pass through to the command.

### `hospitus jail console`

```
hospitus jail console <name> [--shell <shell>]   # Aliases: login, attach
```

Opens an interactive shell via local `jexec`. Default shell: `/bin/sh`.

### `hospitus jail health`

```
hospitus jail health <name> [--output json]      # Aliases: check
```

Checks: jail process, ZFS dataset, VNET interface, command execution.

### `hospitus jail stats`

```
hospitus jail stats <name> [--output json]       # Aliases: metrics, top
```

CPU%, memory, disk I/O, network traffic. Requires `kern.racct.enable=1`.

### `hospitus jail snapshot`

```
hospitus jail snapshot create <name> <snapshot-name>
hospitus jail snapshot list [<name>] [--all | -a] [--pattern | -x <glob>] [--output json]
hospitus jail snapshot delete <name> <snapshot-name>
hospitus jail snapshot restore <name> <snapshot-name>
```

**⚠️ `restore`** discards all changes after the snapshot. Stop the jail first.

### `hospitus jail clone`

```
hospitus jail clone <source> <new-name> [--snapshot <name>] [--linked] [--reset-mac]
```

Uses ZFS copy-on-write. `--linked` creates a dependent clone. `--reset-mac` is on by default.

### `hospitus jail expose`

```
hospitus jail expose add <name> --port <spec>          # spec: 80, 80:8080, tcp/80:8080, udp/53
hospitus jail expose remove <name> --port <host-port> [--protocol tcp|udp]
hospitus jail expose list <name>
```

Manages PF port forwarding. The `add` command parses the jail's IP automatically.

### `hospitus jail tmux`

Persistent shell sessions inside jails (local `jexec`, not API). Needs `doas`.

```
doas hospitus jail tmux <name> [session]                  # attach / create
doas hospitus jail tmux list <name>                        # list sessions
doas hospitus jail tmux kill <name> <session>              # kill session
doas hospitus jail tmux send <name> <cmd> [-s <session>]   # send command
```

### `hospitus jail export` / `import`

```
hospitus jail export <name> <path> [--compress] [--stop] [--include-snapshots]
hospitus jail import <path> [--name <new-name>] [--reset-mac] [--ip <addr>] [--start]
```

### `hospitus jail volume` / `hospitus jail network` / `hospitus jail service`

Volume, network, and service management subcommands.

---

## bhyve Commands

```
hospitus bhyve create|list|info|start|stop|restart|destroy|rename|autostart|boot-order|checkpoint|clone|console|expose|media|pause|resume|snapshot|stats|vnc|export|import
```

### `hospitus bhyve create`

```
hospitus bhyve create <name> [flags]
```

Same create flags as jail (`--cpus`, `--memory`, `--image`, `--os-type`, `--description`, `--start`), plus `--bootloader` (only `uefi`, which is the default) and `--sector-size`. `--cloud-init` is refused: put the configuration in a manifest's `[cloud_init]` section.

### `hospitus bhyve list` / `info` / `start` / `stop` / `restart` / `destroy`

```
hospitus bhyve list [--output json]
hospitus bhyve info <name> [--output json]
hospitus bhyve start <name>
hospitus bhyve stop <name> [--force | -f]
hospitus bhyve restart <name>
hospitus bhyve destroy <name> [--force | -f] [--yes | -y]
```

### `hospitus bhyve autostart`

```
hospitus bhyve autostart enable <name> [--priority <0-100>] [--delay <ms>]
hospitus bhyve autostart disable <name>
hospitus bhyve autostart list
hospitus bhyve autostart status <name>
```

### `hospitus bhyve console`

Attach to the serial console of a running VM.

### `hospitus bhyve vnc`

Launch VNC viewer for graphical console access.

```
hospitus bhyve vnc <name> [--info] [--enable] [--disable]
                       [--port <n>] [--width <w>] [--height <h>]
                       [--host <addr>] [--wait] [--viewer <path>]
```

Auto-detects viewers, first match on `PATH` wins: `vncviewer`, `xvncviewer`,
`vinagre`, `remote-viewer`, `krdc`, `remmina`, `gtk-vnc-viewer`.

### `hospitus bhyve expose`

Same interface as `hospitus jail expose` (add/remove/list port forwarding rules).

### Other bhyve subcommands

`hospitus bhyve` also provides: `boot-order` (get/once/set), `checkpoint`
(create/delete/list/restore), `clone`, `export`, `import`, `media`
(attach/detach/list), `pause`, `resume`, `rename`, and `snapshot`
(create/delete/list/restore). Run `hospitus bhyve <command> --help` for details.

---

## QEMU Commands

```
hospitus qemu create|list|info|start|stop|restart|destroy|autostart|boot-order|console|expose|media|pause|resume|rename|snapshot|stats
```

### `hospitus qemu create`

```
hospitus qemu create <name> [flags]
```

Same create flags as bhyve. On macOS, uses HVF acceleration automatically.

### `hospitus qemu list` / `info` / `start` / `stop` / `restart` / `destroy`

Same patterns as bhyve equivalents.

### `hospitus qemu autostart` / `console` / `expose` / `stats`

Same patterns as bhyve equivalents.

### `hospitus qemu media`

Manage removable media (CD-ROM, ISO).

```
hospitus qemu media insert <name> --path <iso> [--readonly] [--bootable]
hospitus qemu media eject <name> <device-id>
hospitus qemu media list <name>
```

Boot device order is a separate top-level command (`get`/`set`/`once`), taking
positional device names (`disk`, `cdrom`, `network`, `floppy`, `usb`):

```
hospitus qemu boot-order set <name> disk cdrom
```

---

## Podman Commands

```
hospitus podman autostart|create|list|info|start|stop|restart|destroy|console|exec|logs|clone|snapshot|stats|health|rename
```

Podman commands delegate to `podman` on the host.

### `hospitus podman list` / `info` / `start` / `stop` / `restart` / `destroy`

```
hospitus podman list [--output json]
hospitus podman info <name>
hospitus podman start <name>
hospitus podman stop <name> [--force | -f]
hospitus podman restart <name>
hospitus podman destroy <name> [--force | -f] [--yes | -y]
```

### `hospitus podman console`

```
hospitus podman console <name>
```

### `hospitus podman exec`

```
hospitus podman exec <name> <command> [args...]
```

Runs a single non-interactive command; it takes no flags of its own (`-u`,
`-w` and `-i` exist on `hospitus jail exec` only). For an interactive shell, use
`hospitus podman console <name>`.

### `hospitus podman logs`

```
hospitus podman logs <name>
```

---

## macOS Provider Commands

Two providers exist only on macOS, on Apple's Virtualization.framework:
`vfkit` for virtual machines and `container` for OCI containers. Both accept
the same verbs, and only those verbs — the framework offers no snapshots, no
migration, no disk hotplug and no resizing a running instance, so those
sub-commands are absent rather than failing at the daemon.

```
hospitus vfkit     create|list|info|start|stop|restart|destroy|stats
hospitus container create|list|info|start|stop|restart|destroy|stats
```

### `hospitus vfkit create` / `hospitus container create`

```
hospitus vfkit create <name> --image <image> [--arch arm64] [--cpus N] [--memory MB] [--start]
hospitus container create <name> --image <image> [--cpus N] [--memory MB] [--start]
```

vfkit runs only guests of the host's own architecture — arm64 on Apple
silicon — because the framework executes guest instructions on the host CPU
with no emulation fallback. For any other architecture use `hospitus qemu`.

A container whose image exits immediately needs a command to hold it open. An
instance spec has no field for one, so it travels in provider config, which
this command does not expose. Nor does a manifest — the validator accepts only
jail, bhyve, qemu and podman — so it has to go through the API, as described in
[Running on macOS](../getting-started/macos.md).

### `hospitus vfkit list` / `info` / `start` / `stop` / `restart` / `destroy` / `stats`

```
hospitus vfkit list [--output json]
hospitus vfkit info <name>
hospitus vfkit start <name>
hospitus vfkit stop <name> [--force | -f]
hospitus vfkit restart <name>
hospitus vfkit destroy <name>... [--force | -f] [--yes | -y]
hospitus vfkit stats <name>
```

`hospitus container` takes the same forms.

---

## Image Commands

```
hospitus image list|available|fetch|delete
```

### `hospitus image list`

```
hospitus image list [--category set|iso|cloud] [--output json]
```

List downloaded images.

### `hospitus image available`

```
hospitus image available [--category set|iso|cloud] [--provider jail|bhyve|qemu] [--os freebsd|linux|openbsd|netbsd] [--output json]
```

Search the image catalog. Categories: `set` (jail base), `iso` (VM installer), `cloud` (cloud images).

### `hospitus image fetch`

```
hospitus image fetch <version>
```

Takes the name `hospitus image available` lists, and a FreeBSD release may also be
named on its own:

```bash
hospitus image fetch 14.3-RELEASE-amd64          # same as freebsd-14.3-RELEASE-amd64
hospitus image fetch ubuntu-24.04-amd64          # the cloud image
hospitus image fetch alpine-3.20-rootfs-amd64    # a Linux jail rootfs
```

A `<category>:` prefix is accepted and ignored for the lookup, so
`cloud:ubuntu-24.04-amd64` fetches the same image as `ubuntu-24.04-amd64`. The
architecture is not optional in either spelling — `cloud:ubuntu-24.04` matches
no catalog entry:

```
Image "cloud:ubuntu-24.04" not found. Use 'hospitus image available' to see
available images, or name a FreeBSD release as VERSION-RELEASE-ARCH
(e.g. 14.3-RELEASE-amd64).
```

### `hospitus image delete`

```
hospitus image delete <image>     # Aliases: rm, remove
```

---

## Manifest Commands

```
hospitus manifest apply|validate|delete
```

Aliases: `m`. TOML-based declarative workload definitions.

### `hospitus manifest apply`

```
hospitus manifest apply <file.toml> [flags]
```

Creates or updates resources.

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Show what would be created without applying |
| `--start` | `false` | Start the instance after creation |
| `--pull` | `false` | Download a missing image instead of asking (required when not on a terminal) |
| `--keep-failed` | `false` | Leave the instances and volumes an apply created when it fails, to inspect them |
| `--timeout` | `30m` | Timeout for operations (includes provisioning hooks) |
| `-n`, `--namespace` | | Namespace prefix for instance names |
| `--var` | | Set variables (`key=value`, repeatable) |
| `--values` | | Path to a TOML file of variable values |
| `--env-prefix` | `HOSPITUS_VAR_` | Prefix for environment variables imported as manifest variables |

### `hospitus manifest validate`

```
hospitus manifest validate <file.toml> [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-q`, `--quiet` | `false` | Only output errors |

### `hospitus manifest delete`

```
hospitus manifest delete <file.toml> [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--force` | `false` | Force delete (stop running instances first) |
| `-y`, `--yes` | `false` | Skip confirmation prompt |
| `--keep-volumes` | `false` | Keep the ZFS volumes the manifest declares, with their data |
| `--timeout` | `5m` | Timeout for operations (Linux jails may need longer) |
| `-n`, `--namespace` | | Namespace prefix for instance names |

---

## Secret Commands

Persistent secrets for manifest templates. Secrets are addressed by a `scope`
and a `name`.

There is no command to create one. A secret comes into being the first time a
manifest asks for it — `{{ secret `db_pass` }}` in a template generates a
random value, stores it, and renders it. Applying the same manifest again reads
back the value already stored, so the credentials inside an instance stay
valid.

```
hospitus secret get <scope> <name>
hospitus secret list [scope]
hospitus secret rm <scope> <name>
hospitus secret rotate <scope> <name>
```

The scope is the workload or stack name. `list` without one enumerates every
scope.

Where they are kept depends on who runs the command: the daemon owns
`/var/lib/hospitus/secrets`, and a caller who cannot write there gets
`~/.local/share/hospitus/secrets`, created `0700`. So a stack deployed through the
API and one applied from your shell keep their secrets in different places, and
`hospitus secret list` shows the ones belonging to whoever runs it.

---

## Management Commands

Top-level commands that operate across providers or on the daemon.

### `hospitus apply` / `hospitus validate`

Shortcuts for manifest workflows. `hospitus apply <file.toml>` is equivalent to
`hospitus manifest apply`; `hospitus validate <file.toml>` is equivalent to
`hospitus manifest validate`.

```
hospitus apply <file.toml> [--dry-run]
hospitus validate <file.toml>
```

### `hospitus init`

Prerequisite doctor. Checks and prepares the host for Hospitus (kernel modules,
ZFS, PF, required packages) and reports what is missing.

```
doas hospitus init
```

Reading the loaded PF ruleset needs root, so without `doas` the run stops part
way with "this command requires root privileges" after skipping those checks.
It ends with a count: `Summary: 33 OK, 0 fixed, 2 warnings, 0 failed, 2 skipped`.

### `hospitus context`

Manage named hospitusd connection profiles.

```
hospitus context add <name> --url <url> [--api-key <key>] [--tls-ca <pem>] [--tls-skip-verify] [--set-active]
hospitus context list
hospitus context show
hospitus context use <name>
hospitus context remove <name>
```

See [Contexts](contexts.md) for the full guide.

### `hospitus job`

Inspect and manage asynchronous jobs (long-running create/fetch operations).

```
hospitus job list [--status <state>]
hospitus job info <id>
hospitus job cancel <id>
hospitus job delete <id>
hospitus job stats
```

### `hospitus backup`

Create and restore instance backups.

```
hospitus backup create <name> [flags]
hospitus backup list
hospitus backup restore <backup> [flags]
hospitus backup verify <backup>
hospitus backup delete <backup>
```

### `hospitus man`

Generate man pages for the CLI. They are written to `./man` in the current
directory, one per command:

```
hospitus man
```

```
Generated 256 man page(s) in ./man
Install with:
  doas cp ./man/*.1 /usr/local/man/man1/
  doas makewhatis /usr/local/man
```

The count tracks the command tree, so it moves as commands are added.

---

## Examples

**Create and deploy a web jail:**

```bash
hospitus image fetch 14.3-RELEASE-amd64
hospitus jail create webserver -V 14.3 --vnet --ip 10.0.0.10/24 --cpus 2 --memory 1024
hospitus jail start webserver
doas hospitus jail expose add webserver --port 80:80
hospitus jail health webserver
```

**Snapshot workflow:**

```bash
hospitus jail snapshot create postgres before-upgrade
# Upgrade fails...
hospitus jail stop postgres
hospitus jail snapshot restore postgres before-upgrade
hospitus jail start postgres
```

**Clone for staging:**

```bash
hospitus jail clone prod-db staging-db --snapshot known-good
hospitus jail start staging-db
```

**Bulk destroy:**

```bash
hospitus jail destroy -x "test-*" -y
```

**Scripting with `jq`:**

```bash
hospitus jail list -o json | jq '.[] | {name, state}'
hospitus jail health myjail -o json | jq -e '.status == "healthy"' > /dev/null
```
