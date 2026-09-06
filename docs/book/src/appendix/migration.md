# Migration from Other Tools

This guide helps you migrate workloads from other FreeBSD virtualization
managers — CBSD, iocage, ezjail, Bastille, Pot, vm-bhyve/iohyve — onto Hospitus. It
maps each tool's commands to their Hospitus equivalents and gives scripts that
copy the data and recreate the definition.

> **Two conventions used throughout:**
>
> - **`hospitus apply` takes the manifest path as a positional argument** — there is
>   no `-f` flag. Write `hospitus apply web.toml`, not `hospitus apply -f web.toml`.
> - The TOML manifest snippets below are **illustrative equivalents**, not
>   exhaustive schema. For the authoritative UWM keys and their exact spelling,
>   see the [UWM Specification](../uwm/spec.md) and the
>   [Examples & Template Catalog](../uwm/examples.md). Validate any manifest you
>   author with `hospitus validate <file>.toml` before applying it.
>
> Live migration of running instances between hosts is **not** a feature;
> the workflows here stop the source workload, copy its data, and recreate it on
> Hospitus.

## Migrating from CBSD

### Overview

CBSD and Hospitus share similar concepts but differ in implementation:

| CBSD | Hospitus | Notes |
|------|-------|-------|
| `cbsd jcreate` | `hospitus jail create` | Similar syntax |
| `cbsd jstart` | `hospitus jail start` | Same |
| `cbsd jstop` | `hospitus jail stop` | Same |
| `cbsd jremove` | `hospitus jail destroy` | Different command |
| `cbsd jls` | `hospitus jail list` | Same |
| `cbsd jconfig` | `hospitus jail info` | Different command |
| Shell configs | TOML manifests | Different format |
| `nodeippool` | `ip_pool` | Same format, different config |

### IP Pool Migration

CBSD's `nodeippool` format is directly compatible:

```bash
# CBSD: the nodeippool setting (cbsd initenv)
nodeippool="192.168.1.0/24 10.0.0.0/24"

# Hospitus: /usr/local/etc/hospitus/hospitusd.conf
ip_pool = "192.168.1.0/24 10.0.0.0/24"
```

All formats work:
- CIDR: `192.168.1.0/24`
- Range: `192.168.1.10-50`
- Single IP: `192.168.1.100`

### Exporting CBSD Jails

```sh
#!/bin/sh
# Export CBSD jail to Hospitus manifest

CBSD_JAIL="$1"
OUTPUT="${2:-$CBSD_JAIL.toml}"

# Get CBSD jail info
CBSD_DATA=$(cbsd jget jname="$CBSD_JAIL" 2>/dev/null)

# Extract values. ip4_addr carries a prefix length, which the manifest needs.
IP=$(echo "$CBSD_DATA" | grep "ip4_addr=" | cut -d= -f2)

cat > "$OUTPUT" << EOF
[workload]
name = "$CBSD_JAIL"

[provider]
type = "jail"

[image]
source = "freebsd:14.3-RELEASE"

[[networks]]
name = "public"
type = "bridge"
bridge = "hospitus0"

[networks.ip]
mode = "static"
address = "$IP"

[lifecycle.autostart]
enabled = true
EOF

echo "Exported to $OUTPUT"
```

### Migrating Jail Data

```sh
#!/bin/sh
# Migrate CBSD jail to Hospitus

CBSD_JAIL="$1"
CBSD_ROOT="/usr/jails/jails/$CBSD_JAIL"
HOSPITUS_ROOT="/zroot/hospitus/jails/$CBSD_JAIL"

# Stop CBSD jail
cbsd jstop jname="$CBSD_JAIL"

# Create Hospitus dataset
zfs create -p zroot/hospitus/jails/"$CBSD_JAIL"

# Copy data (preserving attributes)
rsync -avxHAX "$CBSD_ROOT/root/" "$HOSPITUS_ROOT/root/"

# Create Hospitus jail from manifest
hospitus apply "$CBSD_JAIL.toml"

# Start
hospitus jail start "$CBSD_JAIL"
```

## Migrating from iocage

### Command Mapping

| iocage | Hospitus |
|--------|-------|
| `iocage create` | `hospitus jail create` |
| `iocage start` | `hospitus jail start` |
| `iocage stop` | `hospitus jail stop` |
| `iocage destroy` | `hospitus jail destroy` |
| `iocage list` | `hospitus jail list` |
| `iocage exec` | `hospitus jail exec` |
| `iocage console` | `hospitus jail console` |
| `iocage snapshot` | `hospitus jail snapshot create` |
| `iocage rollback` | `hospitus jail snapshot restore` |

### Export Script

`cpuset` and `memoryuse` are `off` on a default iocage jail, and `cpuset` can
also be a CPU list (`1,2`) rather than a count — neither is a value
`[resources]` accepts, so the script emits the section only when both are
usable and leaves the resource limits to be set by hand otherwise.

```sh
#!/bin/sh
# Export iocage jail to Hospitus manifest

JAIL="$1"

# Get iocage properties
IP=$(iocage get ip4_addr "$JAIL" | cut -d'|' -f2)
CPUS=$(iocage get cpuset "$JAIL")
MEMORY=$(iocage get memoryuse "$JAIL")

cat > "$JAIL.toml" << EOF
[workload]
name = "$JAIL"

[provider]
type = "jail"

[image]
source = "freebsd:14.3-RELEASE"
EOF

# cpu must be a count and memory a size ("1Gi", "512Mi"): skip "off" and
# CPU lists such as "1,2".
case "$CPUS" in
    ''|off|*[!0-9]*) CPUS="" ;;
esac
case "$MEMORY" in
    ''|off) MEMORY="" ;;
esac
if [ -n "$CPUS" ] && [ -n "$MEMORY" ]; then
    cat >> "$JAIL.toml" << EOF

[resources]
cpu = $CPUS
memory = "$MEMORY"
EOF
else
    echo "note: cpuset/memoryuse not usable as limits; set [resources] by hand" >&2
fi

# The address needs its prefix length: iocage's ip4_addr carries one.
cat >> "$JAIL.toml" << EOF

[[networks]]
name = "public"
type = "bridge"
bridge = "hospitus0"

[networks.ip]
mode = "static"
address = "$IP"
EOF

echo "Exported: $JAIL.toml"
```

## Migrating from Docker/Podman

### For FreeBSD Users

If you're moving Docker workloads to FreeBSD jails:

```bash
# Docker
docker run -d --name web -p 8080:80 nginx

# Hospitus jail equivalent
hospitus jail create web --image 14.3-RELEASE-amd64 --vnet --ip dhcp --start
hospitus jail exec web env IGNORE_OSVERSION=yes ASSUME_ALWAYS_YES=yes pkg install -y nginx
hospitus jail exec web service nginx enable
hospitus jail exec web service nginx start
hospitus jail expose add web --port 8080:80
```

### For Linux Users (Podman Provider)

```toml
# docker-compose.yml equivalent
[workload]
name = "nginx"

[provider]
type = "podman"

[image]
source = "oci:nginx:alpine"

[resources]
cpu = 1
memory = "512Mi"

[[networks]]
name = "public"
type = "bridge"

[[networks.ports]]
host = 8080
container = 80
```

### Converting docker-compose

```sh
#!/bin/sh
# Basic docker-compose to Hospitus stack converter

COMPOSE_FILE="$1"
OUTPUT="${2:-stack.toml}"

echo "[stack]" > "$OUTPUT"
echo "name = \"$(basename "$(dirname "$COMPOSE_FILE")")\"" >> "$OUTPUT"
echo "" >> "$OUTPUT"

# Parse services (simplified - use yq for real conversion)
yq -r '.services | keys[]' "$COMPOSE_FILE" | while read service; do
    IMAGE=$(yq -r ".services.$service.image" "$COMPOSE_FILE")

    cat >> "$OUTPUT" << EOF
[[instances]]
name = "$service"
provider = "podman"

[instances.image]
source = "oci:$IMAGE"

EOF
done

echo "Converted to $OUTPUT"
echo "Note: Manual review required for complete conversion"
```

## Migrating from ezjail

### Command Mapping

| ezjail | Hospitus |
|--------|-------|
| `ezjail-admin create` | `hospitus jail create` |
| `ezjail-admin start` | `hospitus jail start` |
| `ezjail-admin stop` | `hospitus jail stop` |
| `ezjail-admin delete` | `hospitus jail destroy` |
| `ezjail-admin list` | `hospitus jail list` |
| `ezjail-admin console` | `hospitus jail console` |

### Migration Script

```sh
#!/bin/sh
# Migrate ezjail to Hospitus

EZJAIL_NAME="$1"
EZJAIL_ROOT="/usr/jails/$EZJAIL_NAME"

# Stop ezjail
ezjail-admin stop "$EZJAIL_NAME"

# Create Hospitus jail structure
zfs create -p "zroot/hospitus/jails/$EZJAIL_NAME"

# Copy jail content
rsync -avxHAX "$EZJAIL_ROOT/" "/zroot/hospitus/jails/$EZJAIL_NAME/root/"

# Get IP from ezjail config
IP=$(grep "jail_${EZJAIL_NAME}_ip=" /usr/local/etc/ezjail/* | cut -d= -f2 | tr -d '"')

# Create manifest
cat > "/tmp/$EZJAIL_NAME.toml" << EOF
[workload]
name = "$EZJAIL_NAME"

[provider]
type = "jail"

[image]
source = "freebsd:14.3-RELEASE"

[[networks]]
name = "public"
type = "bridge"
bridge = "hospitus0"

[networks.ip]
mode = "static"
address = "$IP"
EOF

# Import into Hospitus
hospitus apply "/tmp/$EZJAIL_NAME.toml"
```

## Migrating VMs

### From VirtualBox

There is no `file:` image source — the valid types are `freebsd`, `oci`,
`cloud`, `iso` and `none`. Create the VM with `source = "none"` and a raw root
disk of the right size, then overwrite that disk with the converted image.
Create first: the provider makes the disk with `truncate`, which would shorten
an image put in place beforehand.

```bash
# Export VirtualBox VM to raw disk
VBoxManage clonehd source.vdi target.raw --format RAW

# Create manifest
cat > myvm.toml << 'EOF'
[workload]
name = "myvm"

[provider]
type = "bhyve"

[image]
source = "none"

[resources]
cpu = 2
memory = "4Gi"

[storage.root_disk]
size = "20Gi"
type = "raw"

[[networks]]
name = "public"
type = "bridge"
EOF

hospitus apply myvm.toml

# Replace the empty root disk with the converted one. The VM directory lives
# under the daemon's --data-dir (default /var/lib/hospitus).
hospitus bhyve stop myvm 2>/dev/null
cp target.raw /var/lib/hospitus/bhyve/myvm/disk0.img
hospitus bhyve start myvm
```

`type = "raw"` matters: a root disk with no type becomes a ZVOL, and the disk
to overwrite is then `/dev/zvol/<parent>/myvm/disk0` instead.

### From VMware

```bash
# Convert VMDK to raw
qemu-img convert -f vmdk source.vmdk -O raw target.raw

# Proceed as with VirtualBox
```

## Migrating from Bastille

Bastille is a lightweight jail manager with a template system. Hospitus provides similar functionality with a REST API and richer resource management.

### Command Mapping

| Bastille | Hospitus | Notes |
|----------|-------|-------|
| `bastille create <name> <release> <ip>` | `hospitus jail create <name> --image <release> --ip <ip>` | Both create from base images |
| `bastille start <name>` | `hospitus jail start <name>` | Same |
| `bastille stop <name>` | `hospitus jail stop <name>` | Same |
| `bastille destroy <name>` | `hospitus jail destroy <name>` | Same |
| `bastille list` | `hospitus jail list` | Same |
| `bastille cmd <name> <cmd>` | `hospitus jail exec <name> <cmd>` | Same |
| `bastille console <name>` | `hospitus jail console <name>` | Same |
| `bastille cp <name> <src> <dst>` | Via `hospitus jail exec` with tar/rsync | Manual file copy |
| `bastille template <name> <tpl>` | UWM manifests | Hospitus uses TOML manifests |
| `bastille snapshot <name>` | `hospitus jail snapshot create <name> <snap>` | ZFS-based |
| `bastille clone <name> <new>` | `hospitus jail clone <name> <new>` | ZFS clone |
| `bastille rdr <name> tcp <host> <jail>` | `hospitus jail expose add <name> --port <host>:<jail>` | PF-based port forwarding |

### Bastille Configuration to Hospitus Manifest

```bash
# Bastille jail.conf snippet:
# myjail {
#   host.hostname = "myjail.example.com";
#   ip4.addr = "192.168.1.10";
#   path = "/usr/local/bastille/jails/myjail/root";
# }

# Equivalent Hospitus manifest
cat > myjail.toml << 'EOF'
[workload]
name = "myjail"

[provider]
type = "jail"

[image]
source = "freebsd:14.3-RELEASE"

[resources]
cpu = 2
memory = "1Gi"

[[networks]]
name = "public"
type = "bridge"
bridge = "hospitus0"

[networks.ip]
mode = "static"
address = "192.168.1.10/24"

[lifecycle.autostart]
enabled = true

# host.hostname has no top-level key; set it as a jail parameter.
[provider_overrides.jail.parameters]
"host.hostname" = "myjail.example.com"
EOF

hospitus apply myjail.toml
```

### Migrating Bastille Jail Data

```sh
#!/bin/sh
# Migrate Bastille jail to Hospitus

BASTILLE_JAIL="$1"
BASTILLE_ROOT="/usr/local/bastille/jails/$BASTILLE_JAIL/root"

# Stop Bastille jail
bastille stop "$BASTILLE_JAIL"

# Create ZFS dataset for Hospitus
zfs create -p "zroot/hospitus/jails/$BASTILLE_JAIL"

# Copy data (preserving attributes and ACLs)
rsync -avxHAX "$BASTILLE_ROOT/" "/zroot/hospitus/jails/$BASTILLE_JAIL/root/"
chown -R root:wheel "/zroot/hospitus/jails/$BASTILLE_JAIL/root"

echo "Data migrated. Create and start with:"
echo "  hospitus jail create $BASTILLE_JAIL --image 14.3-RELEASE-amd64"
echo "  (then copy data back or hospitus apply <manifest>)"
```

### Bastille Templates → Hospitus Lifecycle Hooks

Bastille templates run scripts inside jails during provisioning. The Hospitus
equivalent is a structured `post_create` hook — those commands run *inside* the
instance. The string hooks (`pre_start`, `post_start`, `pre_stop`) run on the
**host**, so they are not where in-jail provisioning belongs. See
[lifecycle hooks](../user-guide/jails.md#lifecycle-hooks-for-create):

```toml
# Bastille template equivalent
[lifecycle.autostart]
enabled = true

[[lifecycle.hooks.post_create]]
type = "exec"
commands = [
  "env IGNORE_OSVERSION=yes ASSUME_ALWAYS_YES=yes pkg install -y nginx",
  "service nginx enable",
]
on_failure = "stop"
```

`type` must be `exec`; the validator refuses any other value.

---

## Migrating from Pot

Pot is a FreeBSD jail manager with a focus on service mesh and multi-host networking (via Consul). Hospitus targets single-host deployment, with a simpler jail model.

### Command Mapping

| Pot | Hospitus | Notes |
|-----|-------|-------|
| `pot create -p <name> -t single -b <release>` | `hospitus jail create <name> --image <release>` | Single-layer jail |
| `pot start <name>` | `hospitus jail start <name>` | Same |
| `pot stop <name>` | `hospitus jail stop <name>` | Same |
| `pot destroy -p <name>` | `hospitus jail destroy <name>` | Same |
| `pot ls` | `hospitus jail list` | Same |
| `pot run -p <name> <cmd>` | `hospitus jail exec <name> <cmd>` | Same |
| `pot term <name>` | `hospitus jail console <name>` | Interactive shell |
| `pot snapshot -p <name>` | `hospitus jail snapshot create <name> <snap>` | ZFS snapshot |
| `pot clone -p <source> -P <dest>` | `hospitus jail clone <source> <dest>` | ZFS clone |
| `pot set-rss -p <name> -C <cpu>` | `hospitus jail set <name> --cpus <n>` | Resource limits |
| `pot set-rss -p <name> -M <mem>` | `hospitus jail set <name> --memory <n>` | Memory limits |

### Pot Configuration to Hospitus Manifest

```bash
# Pot uses /opt/pot/jails/<name>/conf/pot.conf
# Example pot.conf:
# pot_version=0.14.0
# network_type=public-bridge
# ip=192.168.0.10
# netmask=/24
# gateway=192.168.0.1
# cpu=2
# memory=1G

# Equivalent Hospitus manifest
cat > mypot.toml << 'EOF'
[workload]
name = "mypot"

[provider]
type = "jail"

[image]
source = "freebsd:14.3-RELEASE"

[resources]
cpu = 2
memory = "1Gi"

[[networks]]
name = "public"
type = "bridge"
bridge = "hospitus0"

[networks.ip]
mode = "static"
address = "192.168.0.10/24"
gateway = "192.168.0.1"
EOF
```

### Pot Flavours → Hospitus Hooks

Pot "flavours" are provisioning scripts. In Hospitus, use lifecycle hooks or UWM manifests:

```bash
# Pot: pot prepare-base -r 14.3 -t single -f myflavour
# Hospitus: use post-create hook or exec commands directly:
hospitus jail create mypot --image 14.3-RELEASE-amd64
hospitus jail exec mypot env IGNORE_OSVERSION=yes ASSUME_ALWAYS_YES=yes pkg install -y nginx
hospitus jail exec mypot service nginx enable
```

---

## Migrating from vm-bhyve / iohyve

vm-bhyve is the most popular bhyve management tool. Hospitus bhyve provider is feature-compatible and adds API-driven management.

### Command Mapping

| vm-bhyve | Hospitus | Notes |
|----------|-------|-------|
| `vm create <name>` | `hospitus bhyve create <name>` | Both create VM directories |
| `vm start <name>` | `hospitus bhyve start <name>` | Same |
| `vm stop <name>` | `hospitus bhyve stop <name>` | Graceful ACPI shutdown |
| `vm poweroff <name>` | `hospitus bhyve stop <name> --force` | Force kill |
| `vm destroy <name>` | `hospitus bhyve destroy <name>` | Same |
| `vm list` | `hospitus bhyve list` | Same |
| `vm console <name>` | `hospitus bhyve console <name>` | Serial/VNC console |
| `vm snapshot <name>@<snap>` | `hospitus bhyve snapshot create <name> <snap>` | ZFS snapshot |
| `vm rollback <name>@<snap>` | `hospitus bhyve snapshot restore <name> <snap>` | Restore snapshot |
| `vm clone <source> <dest>` | `hospitus bhyve clone <source> <dest>` | ZFS clone |
| `vm img` | `hospitus image list` | Cloud images |
| `vm iso` | `hospitus image list` | ISO images |

### vm-bhyve Configuration to Hospitus

vm-bhyve uses simple key=value `.conf` files in `/vm/<name>/<name>.conf`:

```bash
# Example vm-bhyve config: /vm/myvm/myvm.conf
# loader="grub"
# graphics="yes"
# graphics_port=5900
# cpu=2
# memory=2G
# network0_type="virtio-net"
# network0_switch="public"
# disk0_type="virtio-blk"
# disk0_name="disk0.img"

# Create equivalent in Hospitus. Disks are --disk <sizeGB>[:<name>]; there is no
# --disk-size and no --vnc flag (VNC comes from the manifest, or is turned on
# afterwards with `hospitus bhyve vnc`).
hospitus bhyve create myvm \
  --cpus 2 \
  --memory 2048 \
  --disk 20

# Or via manifest:
cat > myvm.toml << 'EOF'
[workload]
name = "myvm"

[provider]
type = "bhyve"

[image]
source = "none"

[resources]
cpu = 2
memory = "2Gi"

[storage.root_disk]
size = "20Gi"
type = "raw"

[[networks]]
name = "public"
type = "bridge"
bridge = "public"

# VNC is a bhyve provider override, not a top-level [vnc] table.
[provider_overrides.bhyve]
vnc = "5900"
EOF
```

The `loader="grub"` line has no equivalent: the bhyve provider accepts `uefi`
and refuses every other bootloader.

### Migrating vm-bhyve VM Disk

Create the VM first, then overwrite its root disk — as in the VirtualBox
section above, and for the same reason.

```sh
#!/bin/sh
# Migrate vm-bhyve VM to Hospitus

VM_NAME="$1"
VM_DIR="${VM_DIR:-/vm}"
# The VM directory sits under the daemon's --data-dir (default /var/lib/hospitus).
HOSPITUS_DIR="/var/lib/hospitus/bhyve"

# Stop vm-bhyve VM
vm stop "$VM_NAME"
sleep 2

# Create the Hospitus VM (raw root disk, sized for the image you are copying in)
hospitus apply "$VM_NAME.toml"
hospitus bhyve stop "$VM_NAME" 2>/dev/null

# Copy disk image(s) over the ones Hospitus created
i=0
for disk in "$VM_DIR/$VM_NAME/"*.img "$VM_DIR/$VM_NAME/"*.raw; do
    [ -f "$disk" ] || continue
    echo "Copying $disk -> disk$i.img"
    cp "$disk" "$HOSPITUS_DIR/$VM_NAME/disk$i.img"
    i=$((i + 1))
done

echo "VM disks migrated to $HOSPITUS_DIR/$VM_NAME/"
echo "Start with: hospitus bhyve start $VM_NAME"
```

### iohyve Migration

iohyve stored VMs as ZFS datasets, and so does Hospitus — but the two layouts are
not interchangeable, so a `zfs send | recv` of an iohyve dataset does not
produce a VM Hospitus can run. Copy the guest data, not the container: create the
VM in Hospitus, then write the iohyve disk over the ZVOL Hospitus made for it.

```sh
# iohyve stores VMs at iohyve/<vmname>
# Hospitus stores VM disks under <parent>/bhyve/<vmname> (default zroot/hospitus)

# Stop iohyve VM
iohyve stop myvm

# Create the VM in Hospitus first — this is what makes disk0
hospitus bhyve create myvm --cpus 2 --memory 2048 --disk 20

# Copy the guest disk into the ZVOL Hospitus created (a ZVOL is the default
# root-disk type; check the size matches before writing)
dd if=/dev/zvol/iohyve/myvm/disk0 of=/dev/zvol/zroot/hospitus/bhyve/myvm/disk0 bs=1M

hospitus bhyve start myvm
```

---

## Best Practices

### Pre-Migration Checklist

- [ ] Document current configuration
- [ ] List all IP addresses and ports
- [ ] Identify dependencies between jails/VMs
- [ ] Create backups/snapshots
- [ ] Plan maintenance window
- [ ] Test migration on non-production first

### During Migration

1. **Export configurations** before starting
2. **Migrate one workload at a time**
3. **Verify each migration** before proceeding
4. **Keep old system running** until verified
5. **Update DNS/load balancers** after verification

### Post-Migration

- [ ] Verify all services are running
- [ ] Test network connectivity
- [ ] Confirm data integrity
- [ ] Update monitoring
- [ ] Update backup scripts
- [ ] Document new configuration
- [ ] Remove old system only after confirmation

## Troubleshooting Migration Issues

### Network Configuration Differences

```bash
# Old system used direct IP aliases
# Hospitus uses VNET by default

# If applications expect specific interface names:
hospitus jail exec myjail ifconfig
# May show epair0b instead of expected interface

# Solution: Configure application to use new interface
# Or create alias in jail
```

### Path Differences

```bash
# CBSD: /usr/jails/jails/myjail/root
# iocage: /iocage/jails/myjail/root
# Hospitus: /zroot/hospitus/jails/myjail/root

# Update any hardcoded paths in scripts
```

### Permission Issues

```bash
# After rsync, fix permissions
chown -R root:wheel /zroot/hospitus/jails/myjail/root
chmod 755 /zroot/hospitus/jails/myjail/root
```

## See Also

- [Installation](../getting-started/installation.md)
- [Quick Start](../getting-started/quick-start.md)
- [UWM Specification](../uwm/spec.md)
