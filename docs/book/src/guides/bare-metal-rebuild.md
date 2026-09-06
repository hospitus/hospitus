# Rebuilding a FreeBSD Host from Scratch

This walkthrough takes a FreeBSD machine that runs something else — here CBSD —
and turns it into a Hospitus host, then shows how to remove Hospitus completely and
build it again. Every step is idempotent: running it twice changes nothing the
second time, so the procedure doubles as a repair when a host has drifted.

It was written against a real rebuild of a FreeBSD 15.1-RELEASE host with two
ZFS pools, and every command below was executed there.

> **Destructive.** Steps 1 and 6 destroy jails, VMs and ZFS datasets. Read what
> each one removes before running it.

---

## Overview

| Step | What it does | Idempotent |
|------|--------------|------------|
| 1 | Remove the previous manager (CBSD) | yes |
| 2 | Hand PF back to the host | yes |
| 3 | Install prerequisites | yes |
| 4 | Build and install Hospitus | yes |
| 5 | Start the daemon and create a workload | yes |
| 6 | Remove Hospitus entirely | yes |

---

## 1. Remove the previous manager

Skip this step on a host that never ran CBSD.

CBSD owns a jail workdir, a bridge, a PF ruleset, an rc.conf block and a system
user. Remove them in dependency order — a running jail holds its datasets, and a
mounted nullfs holds them even after the jail stops.

```sh
# Stop every jail, then the CBSD services.
doas jls                                  # note the JIDs
doas jail -r <jid>                        # repeat per jail
doas service cbsdd stop
doas service cbsdrsyncd stop

# Release the mounts a stopped jail leaves behind, deepest first.
mount -p | awk '{print $2}' | grep '^/usr/jails' | sort -r |
	while read -r mp; do doas umount -f "$mp"; done

# Remove the package, its rc.conf entries and its sudoers drop-in.
doas pkg delete -y cbsd
for v in cbsdd_enable cbsdrsyncd_enable cbsdrsyncd_flags cbsd_workdir; do
	doas sysrc -x "$v" 2>/dev/null || true
done
doas rm -f /usr/local/etc/sudoers.d/cbsd_sudoers

# Destroy the bridge, the datasets and the system user.
doas ifconfig cbsdbr0 destroy
doas chflags -R noschg /usr/jails          # base systems ship immutable files
doas zfs destroy -r zroot/jails
doas rm -rf /usr/jails /cbsd /var/mail/cbsd
doas pw userdel cbsd
```

CBSD's dependencies (`sudo`, `rsync`, `jq`, `sqlite3`, `pkgconf`, …) stay
installed. `pkg autoremove -n` lists them; review that list before removing
anything, since other tooling may rely on `sudo` in particular.

---

## 2. Hand PF back to the host

CBSD starts PF itself from `/usr/jails/etc/pf.conf`, so a host that ran it
usually has **no `/etc/pf.conf` at all** and `pf_enable="NO"` in `rc.conf`, even
though PF is loaded and filtering. Once CBSD is gone, PF must be driven by rc(8)
again.

Hospitus never edits `pf.conf`. It loads its NAT and redirect rules into an anchor
named `hospitus`, which the host ruleset has to declare. Write a ruleset that keeps
the machine's current posture and adds those three lines:

```sh
doas tee /etc/pf.conf >/dev/null <<'EOF'
# Host firewall. Hospitus manages only the rules inside the "hospitus" anchor.
ext_if = "re0"

set skip on lo0

# Translation rules managed by Hospitus (jail/bhyve NAT and port forwarding).
nat-anchor "hospitus"
rdr-anchor "hospitus"

# Filter rules managed by Hospitus.
anchor "hospitus"

# No filtering: this host sits on a trusted LAN. Tighten to taste.
pass all
EOF

doas pfctl -n -f /etc/pf.conf     # validate before loading
doas sysrc pf_enable=YES
doas pfctl -f /etc/pf.conf
```

Order matters inside `pf.conf`: translation anchors (`nat-anchor`, `rdr-anchor`)
must appear before filter rules.

Verify:

```sh
doas pfctl -s Anchors      # -> hospitus
```

> **Careful with `hospitus init --auto` before this step.** Its PF fix runs
> `sysrc pf_enable=YES && service pf start`, which loads `/etc/pf.conf`. On a
> host where that file does not exist yet and PF is driven by another tool, run
> `hospitus init --check` and fix items by hand instead.

---

## 3. Install prerequisites

Jails and bhyve come from the base system. QEMU and Podman are ports:

```sh
doas pkg install -y qemu podman dnsmasq
```

`dnsmasq` is only needed for bhyve NAT networking; `swtpm` (Windows 11 guests)
and `debootstrap` (Debian/Ubuntu jails) are optional and reported as warnings
until installed.

Load the kernel modules Hospitus uses and make them persistent:

```sh
doas kldload vmm nmdm linux64 2>/dev/null || true
for m in vmm nmdm linux64; do
	grep -q "${m}_load" /boot/loader.conf 2>/dev/null ||
		echo "${m}_load=\"YES\"" | doas tee -a /boot/loader.conf >/dev/null
done
```

`kern.racct.enable=1` in `/boot/loader.conf` is required for per-jail CPU and
memory limits, and takes effect only after a reboot.

---

## 4. Build and install Hospitus

```sh
git clone https://github.com/hospitus/hospitus.git ~/src/hospitus
cd ~/src/hospitus
make build
doas make install
```

`make install` places `hospitusd` and `hospitus` in `/usr/local/bin`, the rc.d script
in `/usr/local/etc/rc.d/hospitus`, and a sample configuration in
`/usr/local/etc/hospitus`. Re-running it overwrites the same files.

Check the host:

```sh
doas hospitus init --check
```

Aim for `0 failed`. Remaining warnings are optional features you have chosen not
to install.

---

## 5. Start the daemon and create a workload

```sh
doas sysrc hospitus_enable=YES
doas service hospitus start
doas service hospitus status
```

On first start the rc script generates an API key at
`/usr/local/etc/hospitus/api.key` (mode 0600). The daemon listens on
`127.0.0.1:8080` without TLS — the safe default for a local-only install. To
expose it on the network, set `hospitus_addr` **and** supply certificates through
`hospitus_flags`:

```sh
doas sysrc hospitus_addr="0.0.0.0:8080"
doas sysrc hospitus_flags="--tls-cert /path/cert.pem --tls-key /path/key.pem"
```

Point the CLI at the daemon and create a jail:

```sh
hospitus context add local --url http://127.0.0.1:8080 \
	--api-key "$(doas cat /usr/local/etc/hospitus/api.key)"
hospitus context use local

hospitus image fetch freebsd-14.3-RELEASE-amd64
hospitus jail create demo --image freebsd-14.3-RELEASE-amd64 --vnet --ip dhcp
hospitus jail start demo
```

> **`--vnet` needs an address.** A VNET jail created without `--ip` gets a
> virtual network stack and no interface in it. Pass `--ip dhcp` to take the
> next free address from the pool, or `--ip 10.0.0.10/24` for a static one.

Verify the whole path — interface, bridge membership, gateway, NAT:

```sh
hospitus jail list                                # IP ADDRESS column is populated
ifconfig hospitus0 | grep -E 'inet |member'       # gateway address and epair
hospitus jail exec demo ping -c 2 8.8.8.8         # NAT out through the anchor
doas pfctl -a hospitus -s nat                     # the rule Hospitus installed
```

---

## 6. Remove Hospitus entirely

`scripts/hospitus-purge.sh` undoes everything Hospitus put on the host, in the right
order: it stops instances, releases the mounts they leave behind, removes the
bridge and its epairs, flushes the `hospitus` PF anchor, deletes the binaries,
service script, configuration and data directory, and destroys the ZFS datasets
below the parent dataset.

```sh
doas sh scripts/hospitus-purge.sh          # prompts before destroying data
doas sh scripts/hospitus-purge.sh -y       # no prompt
doas sh scripts/hospitus-purge.sh -k       # files only; keep data and datasets
doas sh scripts/hospitus-purge.sh -p tank/hospitus   # non-default parent dataset
```

It never touches `/etc/pf.conf` — removing the anchor declarations is your call,
and the script prints the lines to drop. It refuses to operate on a bare pool
name, so `-p zroot` is rejected rather than destroying the system.

Running it on a host with no Hospitus installed succeeds and changes nothing, which
is what makes step 6 → step 4 a repeatable cycle:

```sh
doas sh scripts/hospitus-purge.sh -y   # tear down
cd ~/src/hospitus && doas make install # build back up
doas service hospitus start
```

Confirm the host is clean:

```sh
ifconfig -l                     # no hospitus0, no epair
zfs list -r zroot/hospitus         # dataset does not exist
ls /usr/local/bin/hospitus*        # no such file
```

---

## What to check when something does not work

| Symptom | Cause | Fix |
|---------|-------|-----|
| `service hospitus start` prints daemon(8) usage | An old rc script passing `hospitus_flags` to `daemon(8)` | Reinstall the rc script from this repository |
| `service hospitus status` says "not running" while `/health` answers | rc script without `procname` | Same |
| QEMU or Podman reported unavailable although installed | The service PATH lacks `/usr/local/bin` | Same; the rc script sets `hospitus_env` |
| Jail starts but has no interface | `--vnet` without `--ip` | Recreate with `--ip dhcp` |
| Jail has an address but no route | Bridge without its gateway address | Restart the jail; `EnsureBridge` applies the address to existing bridges too |
| `zfs destroy` reports "dataset is busy" | A devfs left by a stopped jail | The purge script unmounts it; otherwise `umount -f <path>/dev` |
