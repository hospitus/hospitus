# Host Setup (`hospitus init`)

Hospitus drives native FreeBSD tools — ZFS, `jail(8)`, `bhyve(8)`, PF, kernel
modules — so the host needs a few prerequisites in place. `hospitus init` inspects
the host and, optionally, fixes what it safely can.

It has three modes, all built on the same prerequisite checks:

| Command | What it does |
|---------|--------------|
| `hospitus init --check` | **Doctor** — read-only. Reports every prerequisite and exits non-zero if a required one is missing. |
| `hospitus init` | **Wizard** — prompts before applying each fixable item (needs root). |
| `hospitus init --auto` | **Non-interactive** — applies every safe fix, idempotently (needs root). |

Limit the checks to the providers you care about with `--provider`:

```sh
hospitus init --check --provider jail,bhyve
```

On **FreeBSD** all providers are checked. On **macOS** the daemon registers
QEMU, Podman, vfkit, and Apple `container` (see [macOS](macos.md)); `hospitus init`
checks only the QEMU and Podman prerequisites. Jail and bhyve are FreeBSD-only
and their checks report `skip`.

## Doctor mode

Run it with `doas`. The check writes nothing either way, but several
prerequisites live where only root can look — pfctl answers nothing useful
unprivileged. Those checks report `skip` and say so rather than guess, so an
unprivileged run is honest but tells you less.


Run the read-only check first — it never changes anything:

```console
$ doas hospitus init --check
Hospitus prerequisite check
────────────────────────────────────────
[ OK ] Operating system             FreeBSD — all providers available
[ OK ] ZFS parent dataset           pool "zroot" exists (parent "zroot/hospitus") (required)
[FAIL] Data directory               /var/lib/hospitus missing or not writable (required)
        → mkdir -p /var/lib/hospitus (run as root)
[FAIL] net.inet.ip.forwarding       net.inet.ip.forwarding=0 (required)
        → sysctl net.inet.ip.forwarding=1 (and add it to /etc/sysctl.conf to persist)
[WARN] Hospitus PF anchor declared     cannot auto-verify; add the hospitus anchor to /etc/pf.conf yourself
        → add these lines to /etc/pf.conf (Hospitus never edits pf.conf for you):
        →     nat-anchor "hospitus"
        →     rdr-anchor "hospitus"
        →     anchor "hospitus"
────────────────────────────────────────
Summary: 24 OK, 0 fixed, 2 warnings, 2 failed, 0 skipped
```

The exit code is non-zero when any **required** prerequisite is missing, so
`hospitus init --check` is safe to run in CI or provisioning scripts.

## Fixing prerequisites

`hospitus init` (wizard) and `hospitus init --auto` apply the fixes and must run as
root:

```sh
doas hospitus init --auto
```

Auto-fixable items include: creating the data directory, loading kernel modules
(`vmm`, `nmdm`, `linux64`), setting runtime sysctls (`net.inet.ip.forwarding`),
and enabling PF (`sysrc pf_enable=YES && service pf start`). Fixes are
idempotent — a second run is a no-op.

Some prerequisites are **reported but never auto-fixed**, because doing so would
be unsafe or destructive:

- **The PF anchor.** Hospitus never edits your `/etc/pf.conf`. It manages only its
  own `hospitus` anchors; you add the anchor declarations yourself (the doctor
  prints the exact lines).
- **The ZFS pool.** Creating a pool is destructive. If your pool is not named
  `zroot`, point Hospitus at an existing one with `HOSPITUS_ZFS_PARENT` (e.g.
  `HOSPITUS_ZFS_PARENT=tank/hospitus`).
- **`kern.racct.enable`** for RCTL limits — requires a `/boot/loader.conf` entry
  and a reboot.

Kernel-module and sysctl fixes apply at runtime; the doctor's remediation text
tells you the `loader.conf` / `sysctl.conf` line to add so the change survives a
reboot.

## Recommended flow after install

```sh
doas hospitus init --auto      # prepare the host
hospitus init --check          # confirm everything is green
doas service hospitus start    # start the daemon
```
