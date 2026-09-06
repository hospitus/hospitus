# bhyve Setup and Configuration Guide

This guide provides a complete walkthrough for using bhyve VMs with Hospitus, from initial setup and kernel module loading to advanced configurations including cloud images, VNC, PCI passthrough, and storage management.

## Table of Contents

- [bhyve Architecture Overview](#bhyve-architecture-overview)
- [Prerequisites](#prerequisites)
- [Initial Setup](#initial-setup)
  - [Loading Kernel Modules](#loading-kernel-modules)
  - [Verifying Hardware Support](#verifying-hardware-support)
  - [Setting Up Networking](#setting-up-networking)
- [Creating Your First VM](#creating-your-first-vm)
  - [Using Cloud Images](#using-cloud-images)
  - [Installing from ISO](#installing-from-iso)
- [VM Lifecycle Management](#vm-lifecycle-management)
  - [Starting and Stopping](#starting-and-stopping)
  - [Console Access](#console-access)
  - [VNC Access](#vnc-access)
- [Storage Management](#storage-management)
  - [ZVOL Disks](#zvol-disks)
  - [Disk Driver and Type](#disk-driver-and-disk-type)
  - [Additional Disks](#additional-disks)
  - [ISO Media Management](#iso-media-management)
- [Networking](#networking)
  - [Default Networking](#default-networking)
  - [Custom Bridges](#custom-bridges)
  - [Multiple Network Interfaces](#multiple-network-interfaces)
- [Advanced Configurations](#advanced-configurations)
  - [Cloud-Init](#cloud-init)
  - [PCI Passthrough](#pci-passthrough)
  - [UEFI Boot](#uefi-boot)
  - [CPU and Memory Allocation](#cpu-and-memory-allocation)
  - [I/O Limits](#io-limits)
- [Snapshots and Clones](#snapshots-and-clones)
  - [Creating Snapshots](#creating-snapshots)
  - [Restoring Snapshots](#restoring-snapshots)
  - [Cloning VMs](#cloning-vms)
- [Export and Import](#export-and-import)
- [Troubleshooting](#troubleshooting)
  - [VM Fails to Start](#vm-fails-to-start)
  - [No Network Access](#no-network-access)
  - [VNC Not Working](#vnc-not-working)
  - [Console Not Responding](#console-not-responding)
  - [PCI Passthrough Fails](#pci-passthrough-fails)
- [Quick Reference](#quick-reference)

---

## bhyve Architecture Overview

bhyve (BSD hypervisor) is FreeBSD's native hypervisor. Hospitus manages bhyve VMs through a provider that handles:

```
┌─────────────────────────────────────────────────────────┐
│                    Hospitus CLI / API                       │
│  hospitus bhyve create/start/stop/snapshot/clone/export     │
└────────────────────────┬────────────────────────────────┘
                         │
┌────────────────────────┴────────────────────────────────┐
│                 bhyve Provider                           │
│  - VM config management (JSON + text files)              │
│  - ZFS dataset/ZVOL management                           │
│  - Network interface (tap/bridge) setup                  │
│  - Cloud-init ISO generation                             │
│  - VNC configuration                                     │
│  - RCTL resource limits                                  │
└────────────────────────┬────────────────────────────────┘
                         │
┌────────────────────────┴────────────────────────────────┐
│                    FreeBSD Host                          │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────┐ │
│  │  vmm.ko     │  │ nmdm.ko     │  │ if_tap.ko       │ │
│  │ (hypervisor)│  │ (console)   │  │ (networking)    │ │
│  └─────────────┘  └─────────────┘  └─────────────────┘ │
│                                                          │
│  ┌─────────────────────────────────────────────────────┐ │
│  │  bhyve process (per VM)                              │ │
│  │  -s 0,hostbridge -s 1,lpc -s 2:0,virtio-blk,...     │ │
│  │  -l com1,/dev/nmdm{N}A -l bootrom,/path/to/UEFI.fd  │ │
│  └─────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
```

---

## Prerequisites

Before using bhyve with Hospitus, ensure:

1. **FreeBSD 13.2 or later** — bhyve is mature and well-supported
2. **Hardware virtualization support** — Intel VT-x or AMD-V
3. **ZFS available** — for VM storage, under the Hospitus parent dataset
   (default `zroot/hospitus`, overridden with `HOSPITUS_ZFS_PARENT`)
4. **Root access** — bhyve requires root privileges
5. **PF firewall** — required for NAT networking (`type = "nat"`)
6. **dnsmasq** — required for automatic IP assignment in NAT mode
7. **UEFI firmware** — bhyve loads a guest from it; without it every VM fails
   with `no bootrom was configured`

```bash
# Install dnsmasq for NAT networking, and the firmware guests boot from
doas pkg install dnsmasq edk2-bhyve

# Enable PF for NAT
doas sysrc pf_enable=YES
doas service pf start
```

---

## Initial Setup

### Loading Kernel Modules

bhyve requires several kernel modules. Load them manually or configure auto-loading:

```bash
# Load modules manually (for immediate use). tap is built into GENERIC on
# current FreeBSD, so there is no if_tap to load.
doas kldload vmm
doas kldload nmdm
doas kldload if_bridge

# Configure auto-loading at boot — add to /boot/loader.conf:
#   vmm_load="YES"
#   nmdm_load="YES"
#   if_bridge_load="YES"
# These entries only matter for the next boot; the kldloads above already
# cover this session.

# Verify modules are loaded
doas kldstat | grep -E 'vmm|nmdm|if_bridge'
```

### Verifying Hardware Support

Check if your CPU supports hardware virtualization:

These lines are printed once, at boot. Read them from `/var/run/dmesg.boot`
rather than from `dmesg`, whose buffer has wrapped on any host that has been up
for a while — the grep then finds nothing on a machine that runs bhyve perfectly
well.

```bash
# Intel CPUs
grep -i "VT-x" /var/run/dmesg.boot
# Should show: VT-x: PAT,HLT,MTF,...

# AMD CPUs
grep -i "SVM" /var/run/dmesg.boot
# Should show: SVM: AMD-V supported

# Alternative check: vmm(4) only attaches on a capable host, so its presence
# after kldload is itself the answer.
doas kldload vmm && sysctl hw.vmm
```

If neither shows output, your CPU may not support virtualization, or it's disabled in BIOS/UEFI.

### Setting Up Networking

bhyve VMs support two networking modes:

#### Bridge Mode (`type = "bridge"`)
The VM tap interface is attached to an existing bridge. Hospitus creates `hospitus0` automatically. For internet access, add your physical NIC to the bridge:

```bash
# Add physical NIC to default bridge (optional — gives VMs direct LAN access)
doas ifconfig hospitus0 addm em0
```

#### NAT Mode (`type = "nat"`) — Recommended for most VMs
Hospitus automatically creates and manages the NAT infrastructure:
- Bridge `hospitus-nat` (10.10.0.1/24)
- DHCP via dnsmasq (range: 10.10.0.10–10.10.0.200)
- PF masquerade (NAT) on the default interface

**Prerequisites for NAT mode:**
```bash
# PF firewall (required for NAT)
doas sysrc pf_enable=YES
doas service pf start

# dnsmasq DHCP server (required for automatic IP assignment)
doas pkg install dnsmasq
```

Hospitus handles all PF anchor rules, IP forwarding, and dnsmasq lifecycle automatically.

---

## Creating Your First VM

### Using Cloud Images

The easiest way to create a VM is using pre-built cloud images:

```bash
# List available cloud images
hospitus image available | grep cloud

# Fetch a cloud image
hospitus image fetch cloud:freebsd-14.1-cloud-amd64

# Create a VM with the cloud image (no bridge/IP flags → NAT mode by default)
hospitus bhyve create myvm --image cloud:freebsd-14.1-cloud-amd64 --cpus 2 --memory 2048

# Start the VM
hospitus bhyve start myvm

# Check status
hospitus bhyve info myvm
```

### Installing from ISO

For custom installations:

```bash
# Download a FreeBSD ISO
fetch https://download.freebsd.org/releases/amd64/14.1-RELEASE/FreeBSD-14.1-RELEASE-amd64-disc1.iso

# Create a VM with the ISO attached (NAT networking by default)
hospitus bhyve create installer-vm \
  --cpus 4 \
  --memory 4096

# Attach the ISO
hospitus bhyve media attach installer-vm --iso FreeBSD-14.1-RELEASE-amd64-disc1.iso

# Start the VM
hospitus bhyve start installer-vm

# Access the console to run the installer
hospitus bhyve console installer-vm
```

**After installation:**

```bash
# Detach the ISO
hospitus bhyve media detach installer-vm

# Reboot the VM
hospitus bhyve restart installer-vm
```

---

## VM Lifecycle Management

### Starting and Stopping

```bash
# Start a VM
hospitus bhyve start myvm

# Stop gracefully (ACPI shutdown)
hospitus bhyve stop myvm

# Force stop (kill bhyve process)
hospitus bhyve stop myvm --force

# Restart
hospitus bhyve restart myvm

# Check status
hospitus bhyve list
hospitus bhyve info myvm
```

### Console Access

bhyve VMs use `nmdm` (null modem) for serial console access:

```bash
# Connect to the console
hospitus bhyve console myvm

# The console uses a per-VM nmdm pair:
# /dev/nmdm-<vmname>A — bhyve's side
# /dev/nmdm-<vmname>B — your side (e.g. cu -l /dev/nmdm-myvmB)

# Disconnect: Ctrl+] then type "quit"
```

### VNC Access

For graphical console access:

```bash
# Enable VNC. This rewrites vm.conf only — a running VM must be restarted
# before the VNC server exists.
hospitus bhyve vnc myvm --enable --port 5900 --width 1024 --height 768
hospitus bhyve restart myvm

# Check VNC info
hospitus bhyve vnc myvm --info
# VNC console for myvm:
#   Host:       127.0.0.1   (default bind address; override with --host)
#   Port:       5900
#   Resolution: 1024x768
#   URI:        vnc://127.0.0.1:5900

# Connect with a VNC client
vncviewer <host-ip>:5900

# Disable VNC
hospitus bhyve vnc myvm --disable
```


---

## Storage Management

### ZVOL Disks

Hospitus uses ZVOLs (ZFS volumes) for VM disks by default:

```bash
# Check VM disk
zfs list -t volume -r zroot/hospitus/bhyve/myvm

# Output:
# NAME                        USED  AVAIL  REFER  MOUNTPOINT
# zroot/hospitus/bhyve/myvm/disk0  10G   100G    10G  -

# Check disk properties
zfs get volsize,volmode,compression zroot/hospitus/bhyve/myvm/disk0
```

**ZVOL advantages:**
- Thin provisioning (sparse allocation)
- ZFS snapshots for backup
- Native bhyve support via `virtio-blk`

### Disk driver and disk type

The `--disk-driver` flag selects the controller presented to the guest —
`virtio-blk` (default), `ahci-hd`, or `nvme`. `virtio-scsi` is not recognised:
it falls through to the default rather than being rejected.

```bash
hospitus bhyve create fast-vm --image cloud:freebsd-14.1-cloud-amd64 --disk-driver nvme
```

The disk *format* (ZVOL, raw, qcow2, or a physical device) is chosen in a manifest
via `storage.root_disk.type`; see the
[manifest spec](../uwm/spec.md#storage). For example, a raw-file root disk:

```toml
[storage.root_disk]
size = "20Gi"
type = "raw"
```

> When `bootloader = "uefi"` and no driver is given, the boot disk defaults to
> `ahci-hd`, because OVMF firmware cannot see virtio-blk devices. An explicit
> `--disk-driver virtio-blk` is still honored — the VM starts, sits in the UEFI
> shell and never boots, with only a log warning to say why.

### Additional Disks

There is no `disk` subcommand for adding a disk after the fact. Extra disks are
declared at create time with the repeatable `--disk <sizeGB>[:<name>]` flag, or
in a manifest under `[[storage.volumes]]`:

```bash
hospitus bhyve create myvm --image cloud:freebsd-14.1-cloud-amd64 \
  --disk 20:data --disk 50:backup
```

### ISO Media Management

```bash
# List the media currently attached to the VM
hospitus bhyve media list myvm

# Insert an ISO
hospitus bhyve media attach myvm --iso /path/to/installer.iso

# Eject an ISO
hospitus bhyve media detach myvm

# The ISO is attached as an AHCI CD-ROM — bhyve has no virtio-cdrom emulation:
# -s 4:0,ahci-cd,/path/to/installer.iso
```

---

## Networking

### Default Networking

Hospitus supports two networking modes for bhyve VMs: **NAT** (recommended) and **bridge**.

**NAT mode** (`type = "nat"`) is recommended for most VMs. Hospitus automatically creates the `hospitus-nat` bridge (10.10.0.1/24), runs dnsmasq for DHCP (10.10.0.10–10.10.0.200), and configures PF masquerade so VMs reach the internet through the host's default interface — no manual configuration needed.

**Bridge mode** (`type = "bridge"`) attaches the VM tap to an existing bridge (e.g., `hospitus0`). The VM receives an IP from the bridge's DHCP server (if any). The user is responsible for bridge connectivity and routing.

```bash
# NAT mode (default) — no bridge/IP flags; VM gets 10.10.0.x automatically
hospitus bhyve create nat-vm --image cloud:freebsd-14.1-cloud-amd64

# Bridge mode — attach the VM to an existing bridge with --bridge (and/or --ip)
hospitus bhyve create bridged-vm --image cloud:freebsd-14.1-cloud-amd64 --bridge hospitus0

# Check the tap interface. Taps are named per VM and NIC index, not tapN.
doas ifconfig | grep tap
# tap_nat-vm_0: flags=8943<UP,BROADCAST,RUNNING,PROMISC,SIMPLEX,MULTICAST>

# Check bridge membership (NAT mode)
doas ifconfig hospitus-nat
# Check bridge membership (bridge mode)
doas ifconfig hospitus0
```

**Inside the VM**, the network interface appears as `vtnet0` (virtio-net):

```bash
# Inside the VM (NAT mode)
ifconfig vtnet0
# inet 10.10.0.10 netmask 0xffffff00
```

### Custom Bridges

Create VMs on custom bridges for network segmentation:

```bash
# Create a DMZ bridge
doas ifconfig bridge create name dmz0
doas ifconfig dmz0 up
doas ifconfig dmz0 inet 172.16.0.1/24

# Create a VM on the DMZ bridge (passing --bridge selects bridge mode)
hospitus bhyve create dmz-vm \
  --image cloud:freebsd-14.1-cloud-amd64 \
  --bridge dmz0 \
  --ip 172.16.0.10/24
```

### Multiple Network Interfaces

For VMs that need multiple networks (e.g., firewalls, routers):

Declare one `[[networks]]` entry per interface in a manifest:

```bash
cat > router-vm.toml << 'EOF'
[workload]
name = "router-vm"

[provider]
type = "bhyve"

[image]
source = "cloud:freebsd-14.1-cloud-amd64"

[resources]
cpu    = 2
memory = "2Gi"

[[networks]]
name   = "lan"
type   = "bridge"
bridge = "hospitus0"

[networks.ip]
mode    = "static"
address = "10.0.0.1/24"

[[networks]]
name   = "dmz"
type   = "bridge"
bridge = "dmz0"

[networks.ip]
mode    = "static"
address = "172.16.0.1/24"
EOF

hospitus apply --start router-vm.toml
```

---

## Advanced Configurations

### Cloud-Init

Cloud-init automates VM provisioning on first boot. The recommended way to
configure it is a manifest `[cloud_init]` section, which gives you users,
packages, `runcmd`, and SSH keys with validation:

```toml
# cloud-vm.toml
[workload]
name = "cloud-vm"

[provider]
type = "bhyve"

[image]
source = "cloud:ubuntu-24.04-amd64"

[resources]
cpu    = 2
memory = "2Gi"

[cloud_init]
enabled  = true
hostname = "cloud-vm"
packages = ["nginx", "htop"]
runcmd   = ["systemctl enable --now nginx"]

[[cloud_init.users]]
name                = "admin"
sudo                = "ALL=(ALL) NOPASSWD:ALL"
ssh_authorized_keys = ["ssh-ed25519 AAAA... you@host"]
```

```bash
hospitus apply --start cloud-vm.toml
```

An existing cloud-config file goes in the manifest's `[cloud_init]` section.
There is no command-line equivalent: `hospitus bhyve create --cloud-init` is
refused, because the create request has no field to carry the file and it was
being discarded.

**What Hospitus does:**
1. Generates a cloud-init ISO with `user-data` and `meta-data`.
2. Attaches it as a CD-ROM device.
3. The guest OS cloud-init (or FreeBSD nuageinit) reads it on first boot.

See the [cloud_init spec](../uwm/spec.md#cloud_init) for every field, including
FreeBSD nuageinit differences.

### PCI Passthrough

Pass physical PCI devices directly to VMs for near-native performance. PCI passthrough is configured through the manifest or provider config, not via a separate `hospitus bhyve pci` subcommand.

**Prerequisites for PCI passthrough:**

1. **Enable VT-d/IOMMU in `/boot/loader.conf`:**

```bash
# Intel CPUs (VT-d)
echo 'hw.vmm.iommu.enable=1' >> /boot/loader.conf

# Reboot for changes to take effect
```

2. **Verify IOMMU is active after reboot:**
```bash
grep -i "vtd\|iommu" /var/run/dmesg.boot
# Should show: VTD: Intel VT-d enabled
# or: AMD-Vi: IOMMU enabled
```

3. **Find your PCI device slot:**
```bash
doas pciconf -lv
# Look for your device, e.g.:
# none0@pci0:9:0:0: class=0x028000 ... MediaTek MT7922 WiFi
# igc0@pci0:10:0:0: class=0x020000 ... Intel I225-V Ethernet
```

**Using manifests with PCI passthrough:**

```toml
[provider_overrides.bhyve]
# Format: "bus/slot/function"
passthrough = ["9/0/0"]  # MediaTek MT7922 WiFi
```

Or with variables:
```bash
hospitus apply --var pci_slot="9/0/0" template.toml
```

At create time you can also pass devices directly with the `--passthrough` flag
(bus/slot/func form, repeatable):

```bash
hospitus bhyve create gpu-vm --image cloud:ubuntu-24.04-amd64 --passthrough 9/0/0
```

The device has to be bound to `ppt(4)` before any of this works. Hospitus passes
the device to bhyve and does not bind it, so declare it in
`/boot/loader.conf` and reboot:

```
pptdevs="1/0/0 1/0/1"
```

(`ppt` is part of `vmm.ko`; there is no separate `ppt_load` module to set.)

A function of a multi-function device is claimed on its own, which is why the
example lists two: a GPU and the audio function beside it. After the reboot the
devices appear under their `ppt` names, and that is the check that they are
ready:

```bash
pciconf -l | grep '^ppt'
# ppt0@pci0:1:0:0: class=0x030000 ... vendor=0x10de device=0x1c03
# ppt1@pci0:1:0:1: class=0x040300 ... vendor=0x10de device=0x10f1
```

**Notes:**
- Hospitus records the devices in the VM's `vm.conf` as `passthrough=1/0/0,1/0/1`
- The device must not be in use by the host — binding it to `ppt` takes it away
  from its host driver

### UEFI Boot

For guests that require UEFI (Windows, modern Linux):

```bash
hospitus bhyve create uefi-vm \
  --image cloud:ubuntu-24.04-amd64 \
  --bootloader uefi \
  --cpus 4 \
  --memory 8192
```


### CPU and memory allocation

Set the guest's virtual CPU count and memory with `--cpus` (count) and `--memory`
(MiB):

```bash
hospitus bhyve create big-vm --image cloud:freebsd-14.1-cloud-amd64 --cpus 8 --memory 16384
```

### I/O limits

bhyve VMs can be throttled at create time with disk-throughput and process caps:

```bash
hospitus bhyve create limited-vm \
  --image cloud:freebsd-14.1-cloud-amd64 \
  --cpus 2 \
  --memory 2048 \
  --read-bps 50M \
  --write-bps 50M \
  --read-iops 2000 \
  --write-iops 2000 \
  --max-proc 512
```


---

## Snapshots and Clones

### Creating Snapshots

```bash
# Create a snapshot
hospitus bhyve snapshot create myvm pre-upgrade

# List snapshots
hospitus bhyve snapshot list myvm

# Output:
# NAME              CREATED             SIZE
# pre-upgrade       2025-01-15 14:30    -
# backup-20250110   2025-01-10 03:00    500 MB
```


### Restoring Snapshots

```bash
# Stop the VM first
hospitus bhyve stop myvm

# Restore the snapshot
hospitus bhyve snapshot restore myvm pre-upgrade

# Start the VM
hospitus bhyve start myvm
```


### Cloning VMs

```bash
# Clone from current state
hospitus bhyve clone myvm myvm-dev

# Clone from a specific snapshot
hospitus bhyve clone myvm myvm-test --snapshot pre-upgrade

# Space-efficient linked clone: shares blocks with the source
hospitus bhyve clone myvm myvm-dev --linked
```

By default the clone is a **full copy** (`zfs send | zfs receive`): it takes as
much space as the source and stays independent of it. `--linked` makes a
copy-on-write clone instead, which shares blocks with the source and grows only
as it diverges — at the cost of pinning the source snapshot for as long as the
clone exists.

---

## Export and Import

### Exporting a VM

```bash
# Export to a file
hospitus bhyve export myvm /var/lib/hospitus/bhyve/backups/myvm-export.tar.gz
```

**Export format** — a tar archive of the VM's directory, optionally gzipped:

```
myvm-export.tar.gz
├── vm.conf              # VM configuration
├── vm.state             # Recorded runtime state
├── cloud-init.iso       # Cloud-init ISO (if any)
└── disk0.img            # File-backed disks only
```

> **ZVOL-backed disks are not exported.** ZVOLs are the default for bhyve, and
> the archive contains only what lives in the VM directory — an export of a
> ZVOL-backed VM carries its configuration, not its data. Use
> `hospitus backup create <vm> --type full` (a ZFS send stream) to move the disks.
> `--include-snapshots` is read only by the jail provider; on a bhyve VM it
> changes nothing, snapshots included.

### Importing a VM

```bash
# Import from export file
hospitus bhyve import /var/lib/hospitus/bhyve/backups/myvm-export.tar.gz --name imported-vm

# Import with a new name, IP, and start immediately
hospitus bhyve import /var/lib/hospitus/bhyve/backups/myvm-export.tar.gz \
  --name imported-vm \
  --ip 172.16.0.20/24 \
  --start
```

---

## Troubleshooting

### VM Fails to Start

**Symptom:** `Failed to start VM: bhyve: invalid option`

**Diagnose:**
```bash
# Check if vmm module is loaded
doas kldstat | grep vmm

# Check if another process is using the VM name
doas bhyvectl --vm=myvm --get-stats 2>&1

# Check dmesg for bhyve errors
dmesg | tail -20
```

**Fix:**
```bash
# Load the vmm module
doas kldload vmm

# Destroy any stale bhyve state
doas bhyvectl --vm=myvm --destroy

# Retry
hospitus bhyve start myvm
```

---

### No Network Access

**Symptom:** VM boots but has no network connectivity.

**Diagnose — NAT mode (`type = "nat"`):**
```bash
# Verify NAT bridge exists with correct IP
ifconfig hospitus-nat

# Verify tap is member of NAT bridge
ifconfig hospitus-nat | grep member

# Verify dnsmasq is running
ps aux | grep dnsmasq | grep -v grep
cat /var/log/hospitus-bhyve-dnsmasq.log

# Verify PF NAT rule
doas pfctl -a hospitus -s nat

# Verify DHCP traffic reaches bridge (run, then boot VM via VNC)
doas tcpdump -i hospitus-nat -n port 67 or port 68

# Verify IP forwarding
sysctl net.inet.ip.forwarding
```

**Fix — NAT mode:**
```bash
# If dnsmasq not installed
doas pkg install dnsmasq

# If PF not enabled
doas sysrc pf_enable=YES && doas service pf start

# If IP forwarding disabled
doas sysctl net.inet.ip.forwarding=1

# Restart hospitusd to re-apply all NAT infrastructure
doas service hospitus restart
```

**Diagnose — Bridge mode (`type = "bridge"`):**
```bash
# Check tap interface is UP
doas ifconfig | grep tap

# Check bridge membership
doas ifconfig hospitus0
```

---

### VNC Not Working

**Symptom:** `VNC connection refused` or black screen

**Diagnose:**
```bash
# Check if VNC is enabled
hospitus bhyve vnc myvm --info

# Check if bhyve process has framebuffer
doas ps aux | grep bhyve | grep fbuf

# Check if port is listening
doas sockstat -4l | grep 5900
```

**Fix:**
```bash
# Disable and re-enable VNC
hospitus bhyve vnc myvm --disable
hospitus bhyve vnc myvm --enable --port 5901

# Check firewall
doas pfctl -s rules | grep 5901
```

---

### Console Not Responding

**Symptom:** `hospitus bhyve console myvm` connects but shows nothing

**Diagnose:**
```bash
# Check nmdm device
doas ls -la /dev/nmdm*

# Check if bhyve is using the correct nmdm
doas ps aux | grep bhyve | grep nmdm
```

**Fix:**
```bash
# The console may be waiting for input
# Try pressing Enter a few times

# If the guest OS doesn't have serial console configured:
# Edit /boot/loader.conf inside the VM:
# console="comconsole"
# boot_serial="YES"

# For Linux guests, add to kernel cmdline:
# console=ttyS0
```

---

### Windows 11: BSOD PHASE0_EXCEPTION (0x78) on AMD

**Symptom:** Windows 11 installer crashes immediately with BSOD code `0x78` on AMD hosts.

**Cause:** Windows HAL probes AMD-specific MSRs (performance counters) during phase 0
initialization. bhyve's SVM backend does not emulate them and injects a `#GP` fault.

**Fix:** Enable MSR-ignore in the VM's bhyve parameters via a manifest:

```toml
[provider_overrides.bhyve.parameters]
ignore_msr = true
```

This passes `-w` to bhyve, making it silently ignore unimplemented MSR accesses.
The setting is safe on Intel systems too.

---

### Windows 11: "This PC doesn't meet Windows 11 requirements" (TPM check)

**Symptom:** Installer shows TPM/SecureBoot hardware requirement error.

**Cause:** bhyve's TPM CRB emulation does not implement the cancellation register
(`tpm_crb_mem_handler: cancelling a TPM command is not implemented yet`). The installer's
hardware check probes this and may fail despite swtpm running correctly.

**Fix A — bypass ISO (recommended):**
Write an `autounattend.xml` that sets the LabConfig keys the installer reads,
then hand it to the VM on a second ISO. It drives no unattended install; the
graphical setup runs as usual, with the hardware check satisfied.

```xml
<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend">
  <settings pass="windowsPE">
    <component name="Microsoft-Windows-Setup"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral"
               versionScope="nonSxS"
               xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">
      <RunSynchronous>
        <RunSynchronousCommand wcm:action="add">
          <Order>1</Order>
          <Path>cmd /c reg add HKLM\SYSTEM\Setup\LabConfig /v BypassTPMCheck /t REG_DWORD /d 1 /f</Path>
        </RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add">
          <Order>2</Order>
          <Path>cmd /c reg add HKLM\SYSTEM\Setup\LabConfig /v BypassSecureBootCheck /t REG_DWORD /d 1 /f</Path>
        </RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add">
          <Order>3</Order>
          <Path>cmd /c reg add HKLM\SYSTEM\Setup\LabConfig /v BypassRAMCheck /t REG_DWORD /d 1 /f</Path>
        </RunSynchronousCommand>
      </RunSynchronous>
    </component>
  </settings>
</unattend>
```

```bash
doas pkg install cdrtools
mkdir -p /tmp/win11-bypass
# save the XML above as /tmp/win11-bypass/autounattend.xml
mkisofs -o /tmp/win11-bypass.iso -J -r /tmp/win11-bypass/
hospitus bhyve media attach win11 --iso /tmp/win11-bypass.iso
# Start the VM — the bypass runs on its own
```

**Fix B — manual registry (interactive):**
1. Press **Shift+F10** in the installer to open a command prompt
2. Run:
   ```bat
   reg add HKLM\SYSTEM\Setup\LabConfig /v BypassTPMCheck /t REG_DWORD /d 1 /f
   reg add HKLM\SYSTEM\Setup\LabConfig /v BypassSecureBootCheck /t REG_DWORD /d 1 /f
   ```
3. Close the prompt (`exit`) and click **Refresh**

**Note:** After installation, `tpm.msc` shows the TPM as "ready for use". The bypass
only applies to the installer's hardware check, not to the running OS.

---

### PCI Passthrough Fails

**Symptom:** `Failed to attach PCI device: Operation not permitted`

**Diagnose:**
```bash
# Is the device bound to ppt(4)? If it still answers to its host driver
# name — nvme0, vgapci0 — bhyve cannot have it.
pciconf -l | grep '^ppt'

# Check IOMMU support
doas sysctl hw.vmm.iommu.enable
```

**Fix:** claim the device at boot, which is the step that takes it away from its
host driver. Both go in `/boot/loader.conf`, and both need a reboot:

```
pptdevs="1/0/0 1/0/1"
hw.vmm.iommu.enable=1
```

Then confirm the device answers to a `ppt` name, and create the VM again —
passthrough is declared in the manifest or with `--passthrough`, not by a
separate command.

---

## Quick Reference

| Task | Command |
|------|---------|
| Load bhyve modules | `doas kldload vmm nmdm if_tap if_bridge` |
| Check VT-x/SVM | `grep -iE 'vt-x\|svm' /var/run/dmesg.boot` |
| Create VM (cloud image) | `hospitus bhyve create <name> --image cloud:<img> --cpus 2 --memory 2048` |
| Create VM (ISO install) | `hospitus bhyve create <name> --image iso:<name>` (or create, then `media attach <name> --iso <path>`) |
| Start VM | `hospitus bhyve start <name>` |
| Stop VM | `hospitus bhyve stop <name>` |
| Force stop | `hospitus bhyve stop <name> --force` |
| Console access | `hospitus bhyve console <name>` |
| Enable VNC | `hospitus bhyve vnc <name> --enable --port 5900` |
| Attach ISO | `hospitus bhyve media attach <name> --iso <path>` |
| Additional disks | `hospitus bhyve create <name> --disk 20:data` (repeatable) or `[[storage.volumes]]` in a manifest; there is no add-disk command |
| Create snapshot | `hospitus bhyve snapshot create <name> <tag>` |
| Restore snapshot | `hospitus bhyve snapshot restore <name> <tag>` |
| Clone VM | `hospitus bhyve clone <source> <new-name>` |
| Export VM | `hospitus bhyve export <name> <path>` |
| Import VM | `hospitus bhyve import <path> --name <name>` |
| PCI passthrough | Configured via manifest or provider config, not a separate command |
| List VMs | `hospitus bhyve list` |
| VM info | `hospitus bhyve info <name>` |
| Check ZVOL usage | `zfs list -t volume -r zroot/hospitus/bhyve/<name>` |
| Destroy stale VM | `doas bhyvectl --vm=<name> --destroy` |