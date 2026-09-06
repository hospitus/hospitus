# Common Issues

Each entry follows the same shape: **symptom → cause → fix**. Commands assume
FreeBSD and the rc.d service (named `hospitus`, not `hospitusd`). For network-specific
problems see [Networking Problems](networking.md); for quick answers see the
[FAQ](faq.md).

Before anything else, run the prerequisite doctor — it catches most environment
problems in one shot:

```sh
hospitus init --check
```

## Seeing the exact command Hospitus ran

A jail or VM that starts and does nothing useful is usually doing exactly what
it was told. The argv Hospitus hands to `jail(8)` or `bhyve(8)` says what that was,
and it is logged at debug level — out of the way of everyday output, one flag
away when it matters:

```sh
doas service hospitus stop
doas hospitusd --log-level debug ...        # your usual flags
```

or, without restarting, read what a running daemon logged if it was started
that way:

```sh
doas grep "executing bhyve start" /var/log/hospitus/hospitusd.log | tail -1
doas grep "executing jail start"  /var/log/hospitus/hospitusd.log | tail -1
```

It is what tells a VM given a `virtio-blk` boot disk under UEFI — which OVMF
cannot read — from one whose guest simply never installed a driver, and it
names the PCI slot each passthrough device landed in.

## Daemon

### The daemon won't start

**Symptom** — `service hospitus start` fails, or `curl http://127.0.0.1:8080/health`
gives "connection refused".

**Causes and fixes:**

1. **Not running as root.** `hospitusd` requires root on FreeBSD and exits with
   `hospitusd requires root privileges on FreeBSD` otherwise. The rc.d service runs
   as root already; only by-hand invocations hit this. Use `doas hospitusd …`.

2. **Already running / port in use.** Check for an existing listener:

   ```sh
   pgrep -lf hospitusd
   sockstat -4 -l | grep 8080
   ```

   Stop the stray process or change `hospitus_addr`.

3. **Read the log — it names the reason.** Under the service, output goes to
   `/var/log/hospitus/hospitusd.log`:

   ```sh
   tail -n 100 /var/log/hospitus/hospitusd.log
   ```

4. **Corrupt database.** If the log mentions the datastore, check integrity:

   ```sh
   sqlite3 /var/lib/hospitus/hospitus.db 'PRAGMA integrity_check;'
   ```

   If it is not `ok`, restore from backup (see
   [Backup & Recovery](../production/backup.md#database-backups)).

5. **Directory permissions.** The daemon needs to own its data and state dirs:

   ```sh
   ls -ld /var/lib/hospitus /var/lib/hospitus/state
   # Expected: root-owned. The daemon creates the data dir 0750 and the state
   # dir 0700; the rc.d script pre-creates both 0750, so 0750 on the state dir
   # on a service-managed host is normal, not a fault.
   ```

### The daemon refuses to start: another hospitusd owns the data directory

**Symptom** —

```
level=ERROR msg="Cannot start" err="another hospitusd already owns /var/lib/hospitus (pid 4916): stop it before starting this one"
```

**Cause** — a daemon is already running against that data directory. Most often
it is an older one that survived an upgrade: `service hospitus stop` only stops
what the rc script started, so a daemon launched by hand, or by an earlier
version with different flags, keeps running unnoticed.

**Why it matters** — two daemons on one data directory share every instance
record, provider state file and VM process, and each acts on what the other
changes. The older one kills processes the newer has no quarrel with and
restores records it removed. Neither log explains it, because each is behaving
correctly on its own terms.

**Fix** — find it and stop it:

```sh
ps -axo pid,lstart,command | grep '[n]exusd'
doas kill <pid>
```

Look at the start time: a daemon older than your last upgrade is the one to
stop. The lock is held on an open descriptor, so it clears by itself however
the daemon dies — a leftover `hospitusd.lock` file never blocks a start.

### Every API call returns 401 Unauthorized

**Symptom** — `hospitus` commands or `curl` calls fail with `401`, "Missing API
key", or "Invalid API key".

**Cause** — authentication is enforced (the default) and the client is not
sending a valid key. Hospitus authenticates only via the **`X-API-Key` header**;
there is no query-parameter or `Bearer` form.

**Fix:**

```sh
# Confirm the daemon loaded keys — look for this line in the log:
grep -i 'Loaded API keys' /var/log/hospitus/hospitusd.log

# Give the CLI the key.
export HOSPITUS_API_KEY="$(doas cat /usr/local/etc/hospitus/api.key)"
hospitus jail list

# Or test directly with the correct header.
curl -s -H "X-API-Key: $HOSPITUS_API_KEY" http://127.0.0.1:8080/api/v1/providers
```

If the daemon log shows no "Loaded API keys" line, no key file was supplied —
set `hospitus_key_file` (rc.d) or pass `--api-key-file`. See
[Security → Authentication](../production/security.md#authentication).

### Every API call returns 401 with no keys configured

**Symptom** — you started `hospitusd` by hand with no keys and now everything is
rejected.

**Cause** — with no keys and without `--allow-no-auth`, the daemon **fails
closed** and rejects all requests. This is deliberate.

**Fix** — supply a key (`--api-key-file`/`--api-key`), or, for local
development only, start with `--allow-no-auth`.

### The API hangs on long operations

**Symptom** — `hospitus jail exec` or `hospitus apply` with a long provisioning hook
times out or drops the connection.

**Cause** — the client or a reverse proxy timed out before the operation
finished. The daemon's own write timeout defaults to 30 minutes.

**Fix** — raise the client timeout and, if you front the daemon with a proxy,
raise its read timeout to match:

```sh
export HOSPITUS_TIMEOUT=60m
hospitus apply --start big-workload.toml
```

For genuinely long work, submit it as a job and poll instead of blocking:
`hospitus job list`, `hospitus job info <id>`.

## Jails

### A jail won't start

**Symptom** — `hospitus jail start web` fails; the jail stays stopped.

**Fixes:**

1. **Inspect what Hospitus knows:**

   ```sh
   hospitus jail info web
   ```

2. **Check the dataset exists and has a populated root:**

   ```sh
   zfs list zroot/hospitus/jails/web
   ls /zroot/hospitus/jails/web/root/
   ```

3. **Check VNET kernel modules:**

   ```sh
   kldstat | grep -E 'if_bridge|if_epair'
   doas kldload if_bridge if_epair   # if missing
   ```

4. **Look for an IP or bridge conflict** — see
   [Networking Problems](networking.md).

### RCTL resource limits not working

**Symptom** — a jail created with `--cpus`/`--memory` fails, or limits have no
effect; the daemon returns an error like `RACCT/RCTL is not enabled; resource
limits cannot be applied to jail "<name>" — add kern.racct.enable=1 to
/boot/loader.conf and reboot`.

**Cause** — FreeBSD's RACCT subsystem is off by default and can only be enabled
at boot.

**Fix:**

```sh
echo 'kern.racct.enable=1' | doas tee -a /boot/loader.conf
doas reboot
# After reboot, verify:
sysctl kern.racct.enable      # -> kern.racct.enable: 1
```

Resource-limited jails **require** RACCT; the daemon rejects them without it.

### `pkg` in a jail rejects the repository

**Symptom** — a post-create hook fails with one of:

```
pkg: repository FreeBSD contains packages for wrong OS version: FreeBSD:14:amd64
Newer FreeBSD version for package <name>:
- package: 1404000
- running userland: 1403000
Ignore the mismatch and continue? [y/N]:
```

**Causes and fixes:**

1. **The jail reports the host's version.** A jail inherits the host kernel's
   `osrelease`/`osreldate` unless told otherwise, so a 14.3 userland on a 15.1
   host claims to be 15.1 and `pkg` resolves ABI `FreeBSD:15:amd64` — then
   refuses the `FreeBSD:14` repository its base system actually needs.

   Hospitus derives these parameters from the image and sets them at creation, so
   this is fixed for jails it creates. Confirm on a running jail:

   ```sh
   doas jexec web uname -r          # -> 14.3-RELEASE, not the host's version
   doas jexec web pkg config abi    # -> FreeBSD:14:amd64
   ```

   Setting `osrelease` explicitly in the manifest's jail parameters overrides
   this — Hospitus never replaces a value you chose.

2. **A package is newer than the image.** The quarterly branch for `FreeBSD:14`
   is built against the newest supported point release, so individual packages
   can carry a higher `__FreeBSD_version` than a 14.3 image. `pkg` then stops
   and asks for confirmation, which a hook cannot answer. Tell `pkg` in the jail
   to proceed:

   ```sh
   doas jexec web sh -c 'echo IGNORE_OSVERSION=true >> /usr/local/etc/pkg.conf'
   ```

   The example manifests do this in their package-setup hook. Either accept the
   mismatch as above, or point the image at the release the packages are built
   for.

### A Linux jail won't populate

**Symptom** — creating a Debian or Ubuntu jail fails with one of:

```
failed to install Linux base system: debootstrap not found
failed to install Linux base system: debootstrap failed: exit status 1
  (output: E: none of sopv, sqv or gpgv2 are installed, but required for
  Release verification)
```

**Cause** — a Linux jail is populated by `debootstrap`, which is not part of
the base system, and which verifies the release signature before unpacking
anything. It looks for `sopv`, `sqv` or `gpgv2` by those exact names — FreeBSD's
`gnupg` package installs the verifier as `gpgv`, so having gnupg is not enough
on its own.

**Fix:**

```sh
doas pkg install debootstrap gnupg
doas ln -s /usr/local/bin/gpgv /usr/local/bin/gpgv2
hospitus init --check          # both now report OK
```

A third prerequisite follows: debootstrap verifies the base packages against
the distribution's **archive keyring**, and Hospitus refuses to bootstrap without
one rather than fetching packages unverified.

```
no archive keyring found at /usr/local/share/keyrings/debian-archive-keyring.gpg
or /usr/share/keyrings/debian-archive-keyring.gpg; refusing to bootstrap without
GPG verification (FreeBSD provides Ubuntu's as the ubuntu-keyring package;
Debian's is not packaged and has to be placed there by hand)
```

The two distributions are not equally available on FreeBSD:

- **Ubuntu** — `pkg install ubuntu-keyring` installs
  `/usr/local/share/keyrings/ubuntu-archive-keyring.gpg`. Ubuntu jails work
  after that.
- **Debian** — the archive keyring is not in ports. `debian-keyring` is a
  different thing (developer keys, not the archive), so Debian jails need the
  keyring placed at `/usr/local/share/keyrings/debian-archive-keyring.gpg` by
  hand, taken from a trusted Debian source.

`hospitus init --check` reports `debootstrap`, its verifier and the keyrings as
three separate checks, and names which distributions the host can currently
bootstrap — so a partially equipped host says so before you create a jail.

### `pkg` in a jail fails on address family or name resolution

**Symptom** — a hook fails part-way through `pkg bootstrap` or `pkg update`:

```
Address resolution failed for http://pkg.FreeBSD.org/FreeBSD:14:amd64/quarterly.
pkg: Error: Address family for host not supported
```

**Cause** — the mirror publishes AAAA records, and `pkg` will follow one even
on a host with no IPv6 route at all. The connection then fails at the socket,
which `pkg` reports as a resolution problem.

**Fix** — pin `pkg` to IPv4 inside the jail:

```sh
doas jexec web sh -c 'echo IP_VERSION=4 >> /usr/local/etc/pkg.conf'
```

The example manifests do this from their package-setup hook, but only when
`IP_VERSION` is not already set *and* a short IPv6 fetch of the package mirror
fails — so a jail with working IPv6 is left alone:

```sh
grep -qs IP_VERSION /usr/local/etc/pkg.conf ||
  fetch -6 -q -T 5 -o /dev/null http://pkg.FreeBSD.org/ 2>/dev/null ||
  echo IP_VERSION=4 >> /usr/local/etc/pkg.conf
```

To check a jail's IPv6 reachability by hand:

```sh
doas jexec web netstat -rn -f inet6 | grep '^default'
```

### Can't exec into a jail

**Symptom** — `hospitus jail exec web <cmd>` fails or reports "command not found".

**Fixes:**

1. **The jail must be running** — `hospitus jail list`, or `doas jls`.
2. **The command must exist in the jail root**, not the host:
   `hospitus jail exec web ls /bin`.
3. **Cross-architecture jail** — confirm the emulator is registered
   (`binmiscctl list`); see [Cross-architecture](#cross-architecture) below.

### Destroying a VM killed something else, or a stopped VM still reads as running

**Symptom** — `hospitus bhyve destroy` returns nothing useful and an unrelated
process on the host dies at the same moment (an ssh session, the very command
you ran). Or the opposite: a VM shut down from inside the guest keeps being
listed as `running`.

**Cause** — both come from trusting the PID recorded when the VM started. Once
bhyve exits, that number is free and the kernel gives it to whatever starts
next. A liveness probe then finds *a* process and calls the VM alive, and a
force stop signals whoever now holds the number.

**Fix** — this is fixed: Hospitus checks that the PID still belongs to the VM's
own bhyve process before signalling it or believing it. A VM whose PID has been
taken over now reports its process as no longer being the VM, and pause,
resume and stop refuse to signal a stranger.

If you are on an older build, confirm before acting on a recorded PID:

```sh
ps -p <pid> -o command=      # must be a bhyve process ending in the VM name
```

## Storage (ZFS)

### "dataset not found" / jail creation fails on storage

```sh
zpool status
zfs list -r zroot/hospitus
```

If the parent dataset is missing, `hospitus init --check` reports it. If your pool
is not `zroot`, point Hospitus at the right one with `HOSPITUS_ZFS_PARENT`
(for example `HOSPITUS_ZFS_PARENT=tank/hospitus`) — Hospitus never creates a pool for
you.

### Out of space (ENOSPC)

```sh
zfs list -o name,used,avail zroot/hospitus
# Snapshots are the usual culprit — list the biggest:
zfs list -t snapshot -o name,used -s used -r zroot/hospitus | tail
# Remove old ones you no longer need:
doas zfs destroy zroot/hospitus/jails/old@stale-snapshot
```

### Can't destroy a dataset that has clones

**Cause** — ZFS refuses to destroy a snapshot/dataset that a clone depends on.

**Fix** — promote the clone so it no longer depends on the origin, or destroy
the clone first:

```sh
zfs list -o name,origin | grep web
doas zfs promote zroot/hospitus/jails/web-clone   # clone becomes independent
```

## Networking (quick pointers)

Networking has its own chapter — [Networking Problems](networking.md) — but the
two most common one-liners:

```sh
# Bridge missing?
ifconfig hospitus0

# NAT/PF rules present under the hospitus anchor?
doas pfctl -a 'hospitus' -s nat
doas pfctl -a 'hospitus' -s rules
```

Hospitus regenerates its firewall rules at startup, so if the `hospitus` anchor is
empty, **restart the daemon** — there is no separate "reload firewall" command:

```sh
doas service hospitus restart
```

## Images

### Image download fails or hangs

**Symptom** — `hospitus image fetch 14.3-RELEASE-amd64` fails or stalls.

**Fixes:**

1. **Connectivity / DNS from the host:**

   ```sh
   host download.freebsd.org
   fetch -o /dev/null https://download.freebsd.org/ && echo reachable
   ```

2. **Disk space** in the image store: `df -h /var/lib/hospitus/images`.

3. **Retry a clean fetch.** If a partial download is wedged, delete and refetch:

   ```sh
   hospitus image delete 14.3-RELEASE-amd64
   hospitus image fetch  14.3-RELEASE-amd64
   ```

   List what the catalog offers and what you already have:

   ```sh
   hospitus image available
   hospitus image list
   ```

## Cross-architecture

### ARM64 jail won't start on AMD64 ("exec format error")

**Cause** — the QEMU user-mode emulator is not installed or not registered with
`binmiscctl`, so the kernel cannot run foreign binaries.

**Fix:**

```sh
doas pkg install qemu-user-static
doas tools/setup-binmiscctl.sh
binmiscctl list          # should list an aarch64 entry -> qemu-aarch64-static
```

Also confirm the emulator binary exists inside the jail root
(`/zroot/hospitus/jails/<name>/root/usr/local/bin/qemu-aarch64-static`); Hospitus
places it there during creation.

## Permissions

### "permission denied" / must run as root

Every privileged Hospitus operation runs inside the daemon, which is root. If you
see permission errors:

- **Running `hospitusd` by hand?** Prefix with `doas`.
- **`doas` not configured?** Add a rule:

  ```sh
  echo 'permit youruser as root' | doas tee -a /usr/local/etc/doas.conf
  ```

- **Data directory mis-owned?** `doas chown -R root:wheel /var/lib/hospitus`.

## Collecting diagnostics for a bug report

There is no single "debug-info" command; gather these by hand:

```sh
# Versions and prerequisite report
hospitus --version
hospitus init --check

# Daemon log (redact any secrets before sharing)
tail -n 500 /var/log/hospitus/hospitusd.log > /tmp/hospitus-log.txt

# Host facts
uname -a
zfs list -r zroot/hospitus
doas jls
```

Then open an issue at
[github.com/hospitus/hospitus/issues](https://github.com/hospitus/hospitus/issues)
with the OS version, the failing command, and the log excerpt.

## See also

- [Networking Problems](networking.md)
- [FAQ](faq.md)
- [Production Deployment](../production/deployment.md)
- [Security Hardening](../production/security.md)
