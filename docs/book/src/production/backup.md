# Backup & Recovery

A complete Hospitus backup has three independent layers, and you need all three to
recover cleanly:

1. **The database** — `hospitus.db` holds every instance definition, job record,
   stack, and secret reference. Lose it and the daemon no longer knows what it
   manages, even if the ZFS data survives.
2. **The workload data** — the ZFS datasets under your Hospitus parent hold jail
   roots, VM disks, and images.
3. **The configuration** — the API key file, TLS material, and any
   `hospitusd.conf` network settings.

This chapter covers Hospitus's built-in `hospitus backup` command for per-instance
backups, plus the host-level strategies (SQLite `.backup`, ZFS snapshots,
replication) that protect the whole deployment.

> **The service is named `hospitus`, not `hospitusd`.** Every stop/start below uses
> `service hospitus …`. Commands like `service hospitusd stop` will fail.

## What to back up

| Component | Location (default) | Criticality | Method |
|-----------|--------------------|-------------|--------|
| Database | `/var/lib/hospitus/hospitus.db` | Critical | SQLite `.backup` |
| State | `/var/lib/hospitus/state/` | High | file copy (daemon stopped) |
| Config | `/usr/local/etc/hospitus/` | Critical | file copy (mode 0600 preserved) |
| Jail roots / VM disks | `<pool>/hospitus/jails`, `<pool>/hospitus/bhyve` | High | ZFS snapshot + send |
| Images | `/var/lib/hospitus/images` (plain files) | Medium (re-downloadable) | file copy |
| Manifests | your source control | High | keep UWM files in git |

The single most important habit: **keep your UWM manifests in version control.**
With manifests plus the database, most instances can be rebuilt even after a
total loss.

## Per-instance backups: `hospitus backup`

Hospitus backs up any instance whose data lives on ZFS — a jail, or a VM whose
disks are ZFS volumes. A Podman container, or a VM on a file-backed image, is
refused by name rather than snapshotted somewhere it is not.

Backups are ZFS snapshots. A `snapshot` stays on the pool; a `full` or
`incremental` backup sends a stream to a destination you configure first, and
records survive a daemon restart so you can list, verify and restore by ID.

There is no encryption: hospitus has no key management, and accepting a key to send
plaintext would be worse than declining. Encrypt the dataset with ZFS native
encryption instead — a raw send of an encrypted dataset stays encrypted end to
end.

```sh
# A snapshot needs no configuration.
hospitus backup create web

# A full or incremental backup needs somewhere to go: a local directory, or
# host:path for a remote pool reached over ssh.
hospitus backup config web --destination /backup/hospitus --compression zstd
hospitus backup create web --type full
hospitus backup create web --type incremental

# Read the configuration back.
hospitus backup config web

# List backups — all instances, or one.
hospitus backup list
hospitus backup list --instance web
hospitus backup list --output json | jq

# Verify a stored backup's integrity.
hospitus backup verify <backup-id>

# Restore. By default it overwrites the source instance's current state.
hospitus backup restore <backup-id>

# Restore into a different instance instead of overwriting. The target has to
# exist already — restore fills an instance, it does not create one:
#   cannot locate the storage of instance web-clone: instance not found: web-clone
hospitus backup restore <backup-id> --target web-clone

# Delete a backup you no longer need.
hospitus backup delete <backup-id> -y
```

`hospitus backup list` prints `ID`, `INSTANCE`, `TYPE`, `STATUS`, `SIZE`, and
`CREATED`; grab the ID from there for verify/restore/delete. Restores prompt for
confirmation because they overwrite state — pass `-y` to skip the prompt in
scripts.

This is the recommended way to snapshot individual workloads before a risky
change:

```sh
hospitus backup create web            # safety net
hospitus jail exec web pkg upgrade -y # risky change
# if it goes wrong:
hospitus backup restore <id> -y
```

For scheduled per-instance backups, drive the CLI from `cron`:

```cron
# /etc/cron.d/hospitus-instance-backups
0 2 * * * root HOSPITUS_API_KEY=$(cat /usr/local/etc/hospitus/api.key) /usr/local/bin/hospitus backup create web >> /var/log/hospitus/backup.log 2>&1
```

## Database backups

The database is small but critical. Use SQLite's online `.backup`, which is safe
while the daemon is running (it takes a consistent copy without blocking
writers). **Do not** just `cp hospitus.db` on a live daemon — you may capture a
torn write.

```sh
#!/usr/bin/env bash
# /usr/local/bin/hospitus-backup-db.sh
set -eu

BACKUP_DIR=/backup/hospitus/db
DATE=$(date +%Y%m%d_%H%M%S)
mkdir -p "$BACKUP_DIR"

# Online, consistent copy — safe while hospitusd runs.
sqlite3 /var/lib/hospitus/hospitus.db ".backup '$BACKUP_DIR/hospitus_$DATE.db'"

# Verify the copy before trusting it.
sqlite3 "$BACKUP_DIR/hospitus_$DATE.db" 'PRAGMA integrity_check;' | grep -qx ok \
  || { echo "integrity check FAILED" >&2; exit 1; }

gzip "$BACKUP_DIR/hospitus_$DATE.db"
find "$BACKUP_DIR" -name 'hospitus_*.db.gz' -mtime +30 -delete
echo "database backup ok: $BACKUP_DIR/hospitus_$DATE.db.gz"
```

```cron
# /etc/cron.d/hospitus-db-backup — daily at 02:00
0 2 * * * root /usr/local/bin/hospitus-backup-db.sh >> /var/log/hospitus/backup.log 2>&1
```

## ZFS snapshots

ZFS snapshots are instant and space-efficient, and they capture jail roots and
VM disks atomically. Snapshot the whole Hospitus tree recursively.

> Replace `zroot/hospitus` with your actual parent dataset. If you pointed Hospitus at
> a different pool via `HOSPITUS_ZFS_PARENT`, use that path here.

```sh
# One recursive snapshot of everything Hospitus owns.
doas zfs snapshot -r zroot/hospitus@backup-$(date +%Y%m%d-%H%M)

# List snapshots.
zfs list -t snapshot -r zroot/hospitus

# Snapshot a single jail before a risky change (ZFS-level, complements hospitus backup).
doas zfs snapshot zroot/hospitus/jails/web@pre-upgrade
```

A simple rolling snapshot schedule with `cron` (prune old ones with a helper or
a tool like `zfsnap`/`sanoid`):

```cron
# /etc/cron.d/hospitus-zfs-snapshots
0 * * * *  root  zfs snapshot -r zroot/hospitus@hourly-$(date +\%Y\%m\%d-\%H)
0 0 * * *  root  zfs snapshot -r zroot/hospitus@daily-$(date +\%Y\%m\%d)
0 0 * * 0  root  zfs snapshot -r zroot/hospitus@weekly-$(date +\%Y\%W)
```

> **Prefer `hospitus jail snapshot` for instances you manage.** The provider tracks
> snapshots it creates and integrates them with `hospitus jail clone` and restore.
> Use raw `zfs snapshot` for whole-tree/offsite backups, not for day-to-day
> per-jail rollbacks.

## Offsite replication with `zfs send`

For disaster recovery, replicate snapshots to another host. Incremental sends
keep it cheap after the first full transfer.

```sh
#!/usr/bin/env bash
# /usr/local/bin/hospitus-replicate.sh — incremental ZFS replication
set -eu

DATASET=zroot/hospitus
REMOTE=backup.example.com
REMOTE_POOL=tank/backup/hospitus

NEW="replicate-$(date +%Y%m%d-%H%M%S)"
doas zfs snapshot -r "$DATASET@$NEW"

LAST=$(ssh "$REMOTE" "zfs list -t snapshot -o name -H $REMOTE_POOL 2>/dev/null | tail -1" | cut -d@ -f2 || true)

if [ -z "$LAST" ]; then
    doas zfs send -R "$DATASET@$NEW" | ssh "$REMOTE" "zfs recv -F $REMOTE_POOL"
else
    doas zfs send -R -i "@$LAST" "$DATASET@$NEW" | ssh "$REMOTE" "zfs recv -F $REMOTE_POOL"
fi
```

Encrypt anything leaving the host. `age` is a good fit for backup archives:

```sh
age -r age1yourrecipientkey... -o hospitus_$DATE.db.gz.age hospitus_$DATE.db.gz
# recover: age -d -i /path/to/key.txt -o hospitus.db.gz hospitus_$DATE.db.gz.age
```

## Recovery procedures

### Restore the database

```sh
doas service hospitus stop

# Keep the current file aside before overwriting.
doas mv /var/lib/hospitus/hospitus.db /var/lib/hospitus/hospitus.db.bak

# Decompress if needed, then put the backup in place.
gunzip -c /backup/hospitus/db/hospitus_20260726_020000.db.gz | doas tee /var/lib/hospitus/hospitus.db >/dev/null

# Verify before starting.
sqlite3 /var/lib/hospitus/hospitus.db 'PRAGMA integrity_check;'

doas service hospitus start
hospitus jail list
```

On startup the daemon reconciles instance state from the providers, so if the
ZFS data still exists it will re-align running/stopped status with reality.

### Restore ZFS data

```sh
# Roll a single dataset back to a snapshot (destroys newer data in that dataset).
doas zfs rollback zroot/hospitus/jails/web@pre-upgrade

# Receive a replicated stream onto a recovery host.
ssh backup.example.com "zfs send -R tank/backup/hospitus@replicate-..." | doas zfs recv -F zroot/hospitus
```

### Restore a single instance

Preferred order, from most to least integrated:

```sh
# 1. From a Hospitus backup (tracks and verifies).
hospitus backup restore <backup-id>

# 2. From a provider snapshot (stop first if the provider requires it).
hospitus jail stop web
hospitus jail snapshot restore web pre-upgrade
hospitus jail start web

# 3. Re-create from the manifest (works even with no data backup, if the
#    manifest is in source control).
hospitus apply web.toml
```

### Full host recovery

1. Reinstall FreeBSD and the Hospitus binaries; run `doas hospitus init --auto`.
2. Restore `/usr/local/etc/hospitus/` (API key file, TLS material, `hospitusd.conf`),
   preserving mode 0600 on the key file.
3. Receive the ZFS data (`zfs recv`) onto the Hospitus parent dataset.
4. Restore `hospitus.db` (see above), then `doas service hospitus start`.
5. Verify: `curl -sk https://127.0.0.1:8080/health` and `hospitus jail list`.
   Use `http://` only for a daemon started with `--allow-insecure-tls`; one
   holding a certificate answers 400 there.
6. For anything missing, re-apply its manifest: `hospitus apply <file>.toml`.

## Verifying and testing backups

A backup you have never restored is a hypothesis. Verify integrity routinely and
test a real restore periodically.

```sh
# Hospitus-tracked backups: built-in integrity check.
hospitus backup verify <backup-id>

# Database dumps: SQLite integrity check. sqlite3 cannot read a database from a
# pipe — decompress to a real file first, or the check "passes" on any input.
gunzip -c hospitus_$DATE.db.gz > /tmp/hospitus-verify.db
sqlite3 /tmp/hospitus-verify.db 'PRAGMA integrity_check;'
rm -f /tmp/hospitus-verify.db

# ZFS: scrub the pool on a schedule so silent corruption surfaces early.
doas zpool scrub zroot
```

Once a month, receive the latest replicated stream onto a scratch host, restore
the database there, start the daemon, and confirm `hospitus jail list` matches
production.

## Backup policy checklist

- [ ] `hospitus backup create` before every risky per-instance change.
- [ ] Nightly `sqlite3 .backup` of the database, verified with
      `PRAGMA integrity_check`.
- [ ] Recursive ZFS snapshots on a rolling schedule, with pruning.
- [ ] Offsite `zfs send` replication, encrypted in transit and at rest.
- [ ] UWM manifests committed to source control.
- [ ] A restore rehearsed within the last month.
- [ ] `zpool scrub` scheduled.

## See also

- [Storage & Snapshots](../user-guide/storage.md) — ZFS and snapshot concepts.
- [Deployment](deployment.md) — directory layout and the service.
- [Monitoring](monitoring.md) — alert on database-health failures via `/health`.
