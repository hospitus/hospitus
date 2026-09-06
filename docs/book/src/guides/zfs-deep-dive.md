# ZFS Deep Dive

Hospitus uses ZFS for all jail and VM storage. This guide explains the dataset layout
Hospitus creates, the ZFS features it relies on (thin clones, snapshots,
compression, quotas), and how to inspect, snapshot, clone, and migrate that storage
by hand when you need to.

- [Dataset layout](#dataset-layout)
- [How Hospitus uses ZFS](#how-hospitus-uses-zfs)
- [Features Hospitus relies on](#features-hospitus-relies-on)
- [Storage planning](#storage-planning)
- [Practical walkthroughs](#practical-walkthroughs)
- [Troubleshooting](#troubleshooting)
- [Quick reference](#quick-reference)

Commands that inspect or modify ZFS directly require root; use `doas`.

---

## Dataset layout

Hospitus keeps everything under a configurable parent dataset. By default the
jail parent is `zroot/hospitus/jails`:

```
zroot/hospitus/
├── jails/                          # One dataset per jail
│   ├── webserver                   #   jail root dataset
│   └── database
├── bhyve/                          # bhyve VM storage
│   └── ubuntu-vm/
│       └── disk0                   #   primary disk (ZVOL)
└── ...
```

Each jail is a **single dataset** with the base system extracted into it; there
is no separate `/root` child dataset. Extra sized volumes declared in a manifest
become their own datasets or disks.

### Naming conventions

| Component | Pattern | Example |
|-----------|---------|---------|
| Jail dataset | `{jails}/{name}` | `zroot/hospitus/jails/webserver` |
| VM disk | `{bhyve}/{name}/disk{N}` | `zroot/hospitus/bhyve/ubuntu-vm/disk0` |
| User snapshot | `{dataset}@{name}` | `zroot/hospitus/jails/webserver@pre-upgrade` |

---

## How Hospitus uses ZFS

### Jails

When you create a jail, Hospitus:

1. **Creates the jail's dataset** and reads back its mountpoint:

   ```bash
   zfs create zroot/hospitus/jails/webserver
   ```

2. **Extracts the base system** into that mountpoint, from the archive the image
   catalog downloaded.

3. **Runs the jail** from the mountpoint.

Each jail therefore owns its blocks outright. Cloning an existing jail, which
`hospitus jail clone --linked` does through a ZFS snapshot and clone, is the way
to get several jails sharing blocks.

### bhyve VMs

bhyve VMs use **ZVOLs** (ZFS block volumes) rather than filesystem datasets:

```bash
# Create a 20 GiB volume for the VM's primary disk
zfs create -V 20G zroot/hospitus/bhyve/ubuntu-vm/disk0

# Snapshot the disk for backup/restore
zfs snapshot zroot/hospitus/bhyve/ubuntu-vm/disk0@pre-upgrade
```

## Features Hospitus relies on

### Snapshots

ZFS snapshots are atomic, copy-on-write (they store only changed blocks), and can
be taken while the jail or VM is running. Manage them through the CLI:

```bash
hospitus jail snapshot create webserver pre-upgrade
hospitus jail snapshot list   webserver
hospitus jail snapshot restore webserver pre-upgrade
hospitus jail snapshot delete  webserver pre-upgrade
```

Under the hood a snapshot named `pre-upgrade` on jail `webserver` is:

```bash
zfs snapshot zroot/hospitus/jails/webserver@pre-upgrade
```

### Clones

A clone is a writable dataset created from a snapshot. `hospitus jail clone` can
give you either that or an independent copy, and the source must be stopped
either way:

```bash
hospitus jail stop webserver

# Independent copy, through zfs send | zfs receive
hospitus jail clone webserver webserver-dev

# Linked clone, through zfs clone
hospitus jail clone webserver webserver-dev --linked
```

Only the linked clone consumes **zero** extra space, growing as it diverges from
the source snapshot — and it keeps the source snapshot alive for as long as it
exists:

```
$ zfs list -o name,used,referenced zroot/hospitus/jails/webserver-dev
NAME                                 USED  REFER
zroot/hospitus/jails/webserver-dev        0B   531M

$ zfs get -H -o value origin zroot/hospitus/jails/webserver-dev
zroot/hospitus/jails/webserver@clone-source-1788098609956833372
```

Without `--linked` the copy stands on its own, reports no origin, and takes the
full 531M.

### Quotas and reservations

| Property | Effect | Example |
|----------|--------|---------|
| `refquota` | Cap space used by the dataset itself (excludes snapshots/children) | `zfs set refquota=10G zroot/hospitus/jails/web` |
| `quota` | Cap total space (includes children) | `zfs set quota=20G zroot/hospitus/jails/web` |
| `reservation` | Guarantee minimum space | `zfs set reservation=1G zroot/hospitus/jails/web` |

To cap a workload's disk from a manifest, set `storage.root_disk.size` or a volume
`size`/`zfs.quota`; see the [manifest spec](../uwm/spec.md#storage). There is no
`--storage` flag on `hospitus jail create` — apply limits with a manifest or with
`zfs set` after creation.

### Compression

Hospitus does **not** set compression on jail datasets: they are created with a
plain `zfs create` and inherit the setting from their parent. Only ZFS volumes
declared in a manifest default to `lz4`. Set it on the parent dataset (or on a
jail) yourself — lz4 is fast enough to be effectively free and typically cuts
disk usage by 30–70%:

```bash
zfs set compression=lz4 zroot/hospitus/jails/webserver
zfs get compressratio,compression zroot/hospitus/jails/webserver
```

Valid compression values in manifest ZFS volume options are `lz4`, `gzip`, `zstd`,
`lzjb`, and `off`.

### Thin provisioning

Thanks to clones, jails are thin-provisioned: a jail cloned from a 1.5 GB base
initially uses almost nothing, and grows only as packages and data are added.
Inspect real usage with:

```bash
zfs get referenced   zroot/hospitus/jails/webserver   # data referenced, after compression
zfs get used         zroot/hospitus/jails/webserver   # actual space consumed
zfs get usedbydataset zroot/hospitus/jails/webserver  # blocks unique to this dataset
```

---

## Storage planning

### Pool requirements

| Requirement | Minimum | Recommended |
|-------------|---------|-------------|
| Pool | Any pool holding the Hospitus parent dataset (default `zroot/hospitus`) | Dedicated pool for Hospitus |
| Free space | 2× largest base image | 10× largest base image |
| Compression | inherited from the parent | `lz4` or `zstd` set on the parent |

Creating a dedicated pool:

```bash
doas zpool create -o ashift=12 hospitus ada1
doas zfs set compression=lz4 hospitus
doas zfs set atime=off hospitus
```

### Capacity estimate

| Component | Size | Notes |
|-----------|------|-------|
| FreeBSD base | ~1.5 GB | Shared across all jails of that version |
| Linux base | ~300 MB | Shared across Linux jails |
| Empty jail | ~0 (thin) | Grows with installed packages |
| Typical web jail | 1–5 GB | nginx/PHP plus app |
| Database jail | 5–50 GB | Depends on data |
| bhyve VM disk | 10–100 GB | ZVOL, sparse or full |

```
Total ≈ BaseImages + (Jails × AvgGrowth) + (VMs × DiskSize) + 20% buffer
```

### Performance

- Prefer SSDs for pools hosting jails/VMs — random I/O dominates.
- `zfs set atime=off zroot/hospitus` avoids a write on every file read.
- Give ZFS's ARC enough RAM; check the cap with `sysctl vfs.zfs.arc_max`
  (25–50% of system RAM is a common target).

---

## Practical walkthroughs

### Inspect Hospitus datasets

```bash
zfs list -r zroot/hospitus
zfs list -r -o name,used,avail,refer,compression zroot/hospitus/jails
zfs list -r -t snapshot zroot/hospitus/jails/webserver
```

### Restore from a snapshot manually

Rollback discards everything written since the snapshot, so stop the jail first:

```bash
hospitus jail stop webserver
doas zfs rollback -r zroot/hospitus/jails/webserver@pre-upgrade
hospitus jail start webserver
```

For a safer path, clone the current state as a backup before rolling back:

```bash
hospitus jail stop webserver
doas zfs snapshot zroot/hospitus/jails/webserver@pre-rollback
doas zfs clone   zroot/hospitus/jails/webserver@pre-rollback zroot/hospitus/jails/webserver-backup
doas zfs rollback -r zroot/hospitus/jails/webserver@pre-upgrade
hospitus jail start webserver
```

### Clone a jail

Prefer the CLI, which also creates the jail configuration for the clone:

```bash
hospitus jail clone webserver webserver-dev
```

The raw ZFS equivalent (dataset only — you would still need to register the jail):

```bash
doas zfs snapshot zroot/hospitus/jails/webserver@clone-source
doas zfs clone    zroot/hospitus/jails/webserver@clone-source zroot/hospitus/jails/webserver-dev
```

### Migrate Hospitus to a new pool

```bash
# 1. Stop workloads and the daemon
hospitus jail list -o json | jq -r '.[].name' | xargs -I{} hospitus jail stop {}
doas service hospitus stop

# 2. Replicate every dataset to the new pool
doas zfs snapshot -r zroot/hospitus@migrate
doas zfs send -R zroot/hospitus@migrate | doas zfs receive -F newpool/hospitus

# 3. Point Hospitus at the new parent dataset and start the daemon. The only
#    mechanism is the HOSPITUS_ZFS_PARENT environment variable, which the daemon
#    reads at startup; with the shipped rc script, put it in hospitus_env:
doas sysrc hospitus_env="HOSPITUS_ZFS_PARENT=newpool/hospitus PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin"
doas service hospitus start

# 4. Verify
hospitus jail list
```

> Relocating the pool changes the dataset parent Hospitus expects. Set
> `HOSPITUS_ZFS_PARENT` in the daemon's environment before starting it, so it looks
> for jails under `newpool/hospitus/jails`. There is no configuration-file key or
> command-line flag for this.

---

## Troubleshooting

### Dataset already exists

`cannot create 'zroot/hospitus/jails/web': dataset already exists` — usually an
orphan left by a failed create.

```bash
zfs list zroot/hospitus/jails/web
doas zfs destroy -r zroot/hospitus/jails/web   # WARNING: destroys the data
hospitus jail create web --image 14.3-RELEASE-amd64
```

### Rollback refuses to run

`cannot rollback ... more recent snapshots or clones exist` — remove the newer
snapshots/clones first:

A clone is a filesystem, so `zfs list -t` has no `clone` type — it accepts
`filesystem`, `volume` and `snapshot`, and answers `invalid type 'clone'`
otherwise. Clones are the datasets with an origin:

```bash
zfs list -H -o name,origin -r zroot/hospitus | awk '$2 != "-"'
doas zfs destroy zroot/hospitus/jails/webserver-dev    # each clone listed above
doas zfs rollback -r zroot/hospitus/jails/webserver@pre-upgrade
```

### Out of space

```bash
zpool list
zfs list -r -o name,used,avail zroot/hospitus | sort -k2 -h | tail
zfs list -t snapshot -o name,used -r zroot/hospitus | sort -k2 -h | tail
```

Reclaim space by deleting old snapshots, then consider raising quotas or adding
vdevs:

```bash
hospitus jail snapshot list   webserver
hospitus jail snapshot delete webserver old-snapshot
doas zfs set refquota=20G zroot/hospitus/jails/webserver
```

### Pool health

```bash
doas zpool status -x            # "all pools are healthy" = good
doas zpool scrub zroot         # periodic integrity check
```

---

## Quick reference

| Task | Command |
|------|---------|
| List Hospitus datasets | `zfs list -r zroot/hospitus` |
| Jail disk usage | `zfs get used,referenced zroot/hospitus/jails/<name>` |
| Create snapshot | `hospitus jail snapshot create <name> <tag>` |
| Restore snapshot | `hospitus jail snapshot restore <name> <tag>` |
| Delete snapshot | `hospitus jail snapshot delete <name> <tag>` |
| Clone jail | `hospitus jail clone <source> <new-name>` |
| Compression ratio | `zfs get compressratio zroot/hospitus/jails/<name>` |
| Set disk cap | `zfs set refquota=10G zroot/hospitus/jails/<name>` |
| Pool health | `doas zpool status -x` |
| Scrub pool | `doas zpool scrub zroot` |

## See Also

- [Storage & Snapshots](../user-guide/storage.md) — day-to-day storage operations
- [Manifest Specification](../uwm/spec.md#storage) — declaring disks and volumes
