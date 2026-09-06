# Storage & Snapshots

Hospitus uses ZFS datasets for all workload storage. Every jail and VM gets a dedicated ZFS dataset under `zroot/hospitus/`.

## ZFS Integration

All Hospitus workloads use ZFS datasets for storage, enabling:

- **Copy-on-write**: Efficient cloning and snapshots
- **Compression**: Inherited from the pool; `hospitus jail volume create` defaults to LZ4
- **Quotas**: Per-workload storage limits
- **Checksumming**: Data integrity verification

## Dataset Structure

Jails store their filesystem as a directory tree on the ZFS dataset. bhyve VMs store a raw disk image file inside the dataset.

```
zroot/hospitus/
├── jails/
│   ├── webserver/
│   │   └── root/          # Jail filesystem (directory tree)
│   ├── database/
│   │   └── root/
│   └── cache/
│       └── root/
├── bhyve/
│   └── linux-vm/
│       └── disk0.img      # VM disk image (raw file)
└── images/
    ├── freebsd-14.3-RELEASE-amd64/
    └── freebsd-14.3-RELEASE-arm64/
```

> For detailed ZFS snapshot and clone workflows (rollback, cloning from snapshots, snapshot lifecycle), see the [Jails: Snapshots & Clones](jails.md#scenario-5-snapshots-clones-and-disaster-recovery) section. The ZFS features described there apply to both jails and bhyve VMs.

## Snapshots

### Create a Snapshot

```bash
hospitus jail snapshot create myjail backup-2025-01-15
```

### List Snapshots

```bash
hospitus jail snapshot list myjail
```

Output:
```
SNAPSHOT           CREATED              SIZE
backup-2025-01-15  2025-01-15 10:00:00  0B
before-upgrade     2025-01-14 15:30:00  50MB
```

### Restore from Snapshot

```bash
# Stop the jail first
hospitus jail stop myjail

# Restore
hospitus jail snapshot restore myjail backup-2025-01-15

# Start again
hospitus jail start myjail
```

### Delete a Snapshot

```bash
hospitus jail snapshot delete myjail before-upgrade
```

## Cloning

Create a new jail from an existing one:

```bash
hospitus jail clone webserver webserver-staging
```

Clone from a specific snapshot:

```bash
hospitus jail clone webserver webserver-test --snapshot backup-2025-01-15
```

## Storage Quotas

Set quotas on a jail's root dataset or volumes after creation:

```bash
# Set quota on the jail root dataset
doas zfs set quota=10G zroot/hospitus/jails/myapp

# Or create a ZFS-backed volume with a quota, then attach it to a jail
hospitus jail volume create data --size 10G --quota 10G --compression zstd
hospitus jail volume attach myapp data /var/data
```

`hospitus jail volume create <volume-name>` creates a standalone ZFS volume; use
`hospitus jail volume attach <jail-name> <volume-name> <mount-point>` to mount it
into a jail (the mount point is relative to the jail root). See
`hospitus jail volume --help` for `info`, `list`, `detach`, and `delete`.

Or in a UWM manifest. The root disk takes only a `size`; ZFS `quota` and
`compression` are available on `[[storage.volumes]]`:

```toml
[storage.root_disk]
size = "10Gi"

[[storage.volumes]]
name = "data"
size = "10Gi"
mount_path = "/var/data"

[storage.volumes.zfs]
quota = "10Gi"
compression = "lz4"
```

## Volumes

Mount additional storage into a jail:

```toml
[[storage.volumes]]
name = "data"
size = "100Gi"
mount_path = "/var/data"

[[storage.volumes]]
name = "config"
host_path = "/tank/configs/myapp"
mount_path = "/etc/myapp"
read_only = true
```

## Best Practices

1. **Regular Snapshots**: Create snapshots before upgrades or configuration changes
2. **Compression**: Set `compression=lz4` on the pool or on `zroot/hospitus` if not already enabled — instance datasets inherit it
3. **Quotas**: Always set quotas to prevent runaway storage usage
4. **Separate Datasets**: Use volumes for data that should persist across jail rebuilds

## See also

- [ZFS Deep Dive](../guides/zfs-deep-dive.md) — snapshot, clone, and rollback internals
- [Networking](networking.md)
- [CLI Reference](cli-reference.md) — full `hospitus jail snapshot` / `clone` / `volume` reference
- [Image Management](images.md)
