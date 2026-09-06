# Hospitus bhyve User Guide

bhyve is FreeBSD's native hypervisor. Hospitus manages bhyve VMs with ZFS-backed storage, VNC console, and snapshot support.

---

## Table of Contents

- [Introduction](#introduction)
- [Scenario 1: Your First FreeBSD VM](#scenario-1-your-first-freebsd-vm)
- [Scenario 2: Linux VM with Cloud-Init](#scenario-2-linux-vm-with-cloud-init)
- [Scenario 3: Windows 11 VM](#scenario-3-windows-11-vm)
- [Scenario 4: Network Configuration](#scenario-4-network-configuration)
- [Scenario 5: Console Access](#scenario-5-console-access)
- [Scenario 5b: VNC Console Access](#scenario-5b-vnc-console-access)
- [Scenario 6: Production VM with Auto-Start](#scenario-6-production-vm-with-auto-start)
- [Scenario 7: Storage Configuration with ZFS](#scenario-7-storage-configuration-with-zfs)
- [Command Reference Summary](#command-reference-summary)

---

## Introduction

This guide walks you through managing bhyve virtual machines using Hospitus. bhyve is FreeBSD's native hypervisor, providing near-native performance through hardware virtualization extensions (Intel VT-x and AMD-V).

### Why bhyve?

bhyve is the ideal choice when you need:
- **Maximum performance**: Near-native speed with hardware virtualization
- **FreeBSD integration**: Native to FreeBSD, no additional software needed
- **ZFS integration**: Use ZVOLs for fast disk I/O and instant snapshots
- **Simple design**: Lightweight, BSD-licensed hypervisor

### bhyve vs QEMU

| Feature | bhyve | QEMU |
|---------|-------|------|
| Platform | FreeBSD only | Cross-platform |
| Performance | Near-native | Good (with KVM) |
| Cross-arch | No | Yes (emulation) |
| Live migration | No | Yes |
| Simplicity | Very simple | More complex |

### Prerequisites

- FreeBSD 14.x (the baseline for this manual)
- CPU with hardware virtualization (Intel VT-x or AMD-V)
- ZFS recommended for storage
- Hospitus daemon (`hospitusd`) running
- Root privileges
- **PF firewall** — required for NAT networking (`type = "nat"`)
- **dnsmasq** — required for automatic IP assignment in NAT mode

```bash
# Install dnsmasq for NAT networking
doas pkg install dnsmasq

# Enable PF for NAT
doas sysrc pf_enable=YES
doas service pf start
```

### Check Hardware Virtualization

The `hw.vmm.*` OIDs appear only once `vmm.ko` is loaded, and only for the
backend your CPU supports — so the check is which one exists:

```bash
doas kldload vmm

# Intel VT-x
sysctl hw.vmm.vmx.initialized

# AMD-V
sysctl hw.vmm.svm.features
```

A host with neither OID cannot run bhyve; `kldload vmm` itself fails on a CPU
without hardware virtualization.

### Managing VM Images

Before creating VMs, download disk images using Hospitus:

#### View Available Images

```bash
# List all available cloud images for VMs
hospitus image available --category cloud

# Filter for bhyve provider
hospitus image available --provider bhyve
```

#### Download Images

```bash
# Download FreeBSD cloud image
hospitus image fetch freebsd-14.3-RELEASE-amd64-zfs

# Download Ubuntu cloud image
hospitus image fetch ubuntu-24.04-amd64
```

#### Boot method

Hospitus boots every bhyve VM through UEFI, and `--bootloader` accepts no other
value. bhyve needs a bootrom, and UEFI is the one hospitus configures: asking for
`bhyveload` or `grub` produced a VM that bhyve refused to run at all, with "no
bootrom was configured" and exit status 4. Creating such a VM is now refused
outright.

UEFI boots FreeBSD, Linux and Windows guests alike, which is why it is the
default.

### Ensure Hospitusd is Running

```bash
# Check if the daemon is running (the rc.d service is named "hospitus")
service hospitus status

# If not running, start it
doas service hospitus start
```

---

## Scenario 1: Your First FreeBSD VM

**Goal**: Create a FreeBSD virtual machine with optimal performance.

### Step 1: Download FreeBSD Cloud Image

```bash
hospitus image fetch freebsd-14.3-RELEASE-amd64-zfs
```

### Step 2: Create the VM

```bash
hospitus bhyve create freebsd-vm \
    --image freebsd-14.3-RELEASE-amd64-zfs \
    --cpus 2 \
    --memory 2048 \
    --description "FreeBSD 14.3 server"
```

**What happens**: Hospitus creates:
- A ZFS ZVOL for the disk (fast, supports snapshots)
- VM configuration files
- TAP interface for networking

**Output**:
```
Creating bhyve VM freebsd-vm...
VM created: freebsd-vm (ID: freebsd-vm)
```

### Step 3: Start the VM

```bash
hospitus bhyve start freebsd-vm
```

**Output**:
```
VM started: freebsd-vm
```

### Step 4: Verify the VM is Running

```bash
hospitus bhyve list
```

**Output**:
```
NAME         STATE     IP ADDRESS   CPUs   MEMORY   PROVIDER   CREATED
freebsd-vm   running   10.10.0.43   2      2048MB   bhyve      2024-01-15 10:30
```

### Step 5: Get VM Details

```bash
hospitus bhyve info freebsd-vm
```

**Output**:
```
Name:         freebsd-vm
State:        running
Provider:     bhyve
Description:  FreeBSD 14.3 server

Resources:
  CPUs:       2
  Memory:     2048 MB

Network:
  Type:       nat
  Bridge:     hospitus-nat
  IPv4:       10.10.0.43
  MAC:        58:9c:fc:01:23:45

Image:        freebsd-14.3-RELEASE-amd64-zfs
ZFS Dataset:  zroot/hospitus/bhyve/freebsd-vm

Created:      2024-01-15T10:30:00+01:00
```

`info` reports what the datastore holds for the instance. Bootloader,
disk driver, VNC and console device are provider settings in `vm.conf`;
read them with `hospitus bhyve vnc <name> --info` or on the host under
`/var/lib/hospitus/bhyve/<name>/`. Add `-o json` for the full record.

### Step 6: Access the Console

```bash
doas hospitus bhyve console freebsd-vm
```

This opens the serial console via nmdm (null modem device).

### Step 7: Stop the VM

```bash
# Graceful shutdown (sends ACPI signal)
hospitus bhyve stop freebsd-vm

# Force stop if unresponsive
hospitus bhyve stop freebsd-vm --force
```

---

## Scenario 2: Linux VM with Cloud-Init

**Goal**: Create an Ubuntu VM with cloud-init for automated provisioning.

### Step 1: Download Ubuntu Cloud Image

```bash
hospitus image fetch ubuntu-24.04-amd64
```

### Step 2: Write the Manifest

Cloud-init is declared in the manifest itself — there is no separate
YAML file to write. Save this as `/tmp/ubuntu-server.toml`:

```toml
[workload]
api_version = "hospitus.io/v1"
name = "ubuntu-server"
description = "Ubuntu 24.04 production server"

[provider]
type = "bhyve"

[image]
source = "cloud:ubuntu-24.04-amd64"

[resources]
cpu = 4
memory = "4Gi"

[storage.root_disk]
size = "20Gi"
type = "zvol"

[cloud_init]
enabled = true
hostname = "ubuntu-server"
packages = ["nginx"]
runcmd = ["systemctl enable nginx", "systemctl start nginx"]

[[cloud_init.users]]
name = "admin"
groups = ["sudo"]
shell = "/bin/bash"
sudo = "ALL=(ALL) NOPASSWD:ALL"
ssh_authorized_keys = ["ssh-ed25519 AAAA... your-public-key"]
```

**What happens**: Hospitus:
1. Creates the VM with UEFI boot
2. Generates a cloud-init ISO from your configuration
3. Attaches the ISO as a secondary drive

> `hospitus bhyve create --cloud-init` is refused: the flag comes from the flag
> set shared with the other providers and was never wired to the API, which
> carries no cloud-init field. A manifest is the way in.

### Step 3: Create the VM

```bash
hospitus apply /tmp/ubuntu-server.toml
```

### Step 4: Start and Wait for Provisioning

```bash
hospitus bhyve start ubuntu-server

# Cloud-init runs on first boot
# Wait a few minutes for provisioning to complete
```

### Step 5: Verify Cloud-Init Completed

```bash
# Connect to console
doas hospitus bhyve console ubuntu-server

# Or SSH if network is configured
ssh admin@<vm-ip>
```

---

## Scenario 3: Windows 11 VM

**Goal**: Install Windows 11 under bhyve with TPM 2.0 emulation.

### Prerequisites

```bash
# TPM 2.0 emulation (required by Windows 11)
pkg install swtpm

# VNC viewer to drive the graphical installer
pkg install tigervnc-viewer

# Download Windows 11 ISO from Microsoft:
# https://www.microsoft.com/software-download/windows11
```

### Step 1: Create the VM

Windows needs an ahci-hd disk, an e1000 NIC, `ignore_msr`, a TPM and VNC. Only
the first two have create flags; `nic_driver`, `ignore_msr`, `tpm`, `vnc` and
`vnc_wait` are provider settings a manifest carries, so create the VM with
`hospitus apply`. Save this as `win11.toml`:

```toml
[workload]
api_version = "hospitus.io/v1"
name = "win11"

[workload.annotations]
description = "Windows 11"

[provider]
type = "bhyve"

[image]
# No image: Windows installs itself from the ISO attached in Step 2.
source = "none"

[resources]
cpu = 4
memory = "8Gi"

[storage.root_disk]
size = "64Gi"
type = "zvol"

[[networks]]
name = "public"
type = "nat"

[networks.ip]
mode = "dhcp"

[provider_overrides.bhyve]
bootloader  = "uefi"
disk_driver = "ahci-hd"       # WinPE ships no VirtIO disk driver
vnc         = "127.0.0.1:5911"

[provider_overrides.bhyve.parameters]
tpm        = true             # TPM 2.0, required by Windows 11
vnc_wait   = true             # hold the VM until a VNC client connects
nic_driver = "e1000"          # WinPE ships no VirtIO network driver
ignore_msr = true             # -w; required on AMD, harmless on Intel
```

```bash
doas hospitus apply win11.toml
```

To install onto a physical disk instead of a zvol, start from
`examples/manifests/vms/windows-physical-disk/template.toml`, which is the
same configuration with `[storage.root_disk] type = "physical"`. Passthrough
is default-deny — see [Physical Disk Passthrough](#physical-disk-passthrough).

### Step 2 & 3: Attach ISOs (automatic)

The `windows-prep` command generates the bypass ISO and attaches both in one step.
The VM must be **stopped** first (bhyve does not support hot-plug):

```bash
# Install ISO creation tool (one-time)
pkg install cdrtools       # provides mkisofs
# or: pkg install xorriso

hospitus bhyve windows-prep win11 --windows-iso ~/Downloads/Win11_25H2_x64.iso
```

This will:
1. Generate the bypass ISO (TPM/SecureBoot bypass) in a private temporary
   directory — pass `--output <path>` to choose the location
2. Attach the Windows ISO as `cdrom0` (bootable)
3. Attach the bypass ISO as `cdrom1` (picked up automatically by Windows Setup)

**Manual alternative** (generate, then attach each ISO separately):
```bash
hospitus bhyve windows-prep win11 --windows-iso ~/Downloads/Win11_25H2_x64.iso \
    --output /tmp/win11-bypass.iso --skip-attach

hospitus bhyve media attach win11 --iso ~/Downloads/Win11_25H2_x64.iso --bootable
hospitus bhyve media attach win11 --iso /tmp/win11-bypass.iso --device cdrom1
```

### Step 4: Start and connect via VNC

The manifest set `vnc_wait = true`, so the VM waits for a VNC connection before
booting. Start it in the background, then connect:

```bash
doas hospitus bhyve start win11 &
hospitus bhyve vnc win11
# or: vncviewer 127.0.0.1:5911
```

### Step 5: Complete installation

Follow the Windows 11 installer wizard. With the bypass ISO attached, hardware checks
are skipped automatically.

**Without the bypass ISO**, if you see "This PC doesn't meet Windows 11 requirements":
1. Press **Shift+F10** to open a command prompt
2. Run:
   ```bat
   reg add HKLM\SYSTEM\Setup\LabConfig /v BypassTPMCheck /t REG_DWORD /d 1 /f
   reg add HKLM\SYSTEM\Setup\LabConfig /v BypassSecureBootCheck /t REG_DWORD /d 1 /f
   ```
3. Close the prompt (`exit`) and click **Refresh**

### Step 6: Post-install

```bash
# Eject ISO(s)
# A bare detach ejects the first CD-ROM only, so name both slots
hospitus bhyve media detach win11 --device cdrom0
hospitus bhyve media detach win11 --device cdrom1

# Restart into installed Windows
hospitus bhyve restart win11
```

After booting into the Windows desktop, TPM is functional — `tpm.msc` reports
"The TPM is ready for use". The bypass only affects the installer's hardware check.

### Driver notes

Windows installer (WinPE) has no VirtIO drivers, so the template uses:
- **`ahci-hd`** for disk — Windows will find it without any extra drivers
- **`e1000`** for network — built into WinPE

After installation, install [VirtIO drivers for Windows](https://fedorapeople.org/groups/virt/virtio-win/)
to switch to `virtio-blk` and `virtio-net` for better performance.

### AMD-specific note: `-w` flag (`ignore_msr`)

On AMD systems, Windows HAL probes AMD-specific MSRs during early boot. bhyve's SVM
backend does not implement them; without `-w` this causes an immediate BSOD
(`PHASE0_EXCEPTION 0x78`). The template enables `ignore_msr = true` which passes `-w`
to bhyve. This flag is safe on Intel systems too.

---

## Scenario 4: Network Configuration

**Goal**: Configure VM networking for different use cases.

### Network Types

| Type | Description | Use Case |
|------|-------------|----------|
| NAT (default) | Private network behind the auto-created `hospitus-nat` bridge (`10.10.0.0/24`), with dnsmasq DHCP | Development, isolation |
| Bridge | VM joined to a host bridge, on the same L2 network as the host | Production, direct access |

When you pass no `--bridge`, Hospitus uses NAT: the VM attaches to the
`hospitus-nat` bridge that `hospitusd` creates automatically and receives a lease
from dnsmasq. This is why NAT mode needs `dnsmasq` and `pf` (see Prerequisites).

### Bridge Networking

To put a VM directly on the host network, attach it to a bridge. Hospitus creates
`hospitus0` automatically for bridge mode; you can also name any bridge you manage
yourself:

```bash
# Create VM on the default bridge hospitus0
hospitus bhyve create webserver \
    --image ubuntu-24.04-amd64 \
    --cpus 2 \
    --memory 2048 \
    --bridge hospitus0
```

The VM gets an IP on the same network as the host.

### Static IP Configuration

```bash
hospitus bhyve create database \
    --image ubuntu-24.04-amd64 \
    --cpus 4 \
    --memory 8192 \
    --bridge hospitus0 \
    --ip 192.168.1.100/24
```

### Port Forwarding

For NAT networks, configure port forwarding:

```bash
# Add port forwarding. The rule targets the address recorded for the VM, so a
# DHCP guest — which has none — needs --target-ip with the address the guest
# actually took.
doas hospitus bhyve expose add webserver --port 80:80 --target-ip 10.10.0.43
doas hospitus bhyve expose add webserver --port 443:443 --target-ip 10.10.0.43

# List port forwards
hospitus bhyve expose list webserver

# Remove port forward
doas hospitus bhyve expose remove webserver --port 80
```

A VM on the NAT bridge takes its address from dnsmasq rather than from hospitus.
The daemon reads it back from the lease file, or from the ARP table by the MAC it
assigned, so the command usually needs no address. A guest that has not asked for
one yet has none to find, and the command says so rather than guessing:

```
target_ip is required: this instance has no recorded address, which is normal
for a DHCP guest — pass the address it reports
```

Read the address inside the guest (`ip addr`, `ifconfig`), or give the VM a
static one at creation.

### Multiple Network Interfaces

A VM gets one interface per `[[networks]]` table in a manifest:

```toml
[[networks]]
name = "public"
type = "bridge"
bridge = "hospitus0"

[networks.ip]
address = "192.168.1.100/24"

[[networks]]
name = "internal"
type = "bridge"
bridge = "vm1"

[networks.ip]
address = "10.0.0.100/24"
```

With `auto_create_bridges` at its default of `true`, hospitusd creates the second
bridge (`vm1` here) if it is missing. Create it yourself
(`ifconfig bridge create name vm1`) when that setting is off, or when the
bridge needs host configuration Hospitus does not apply — a member interface, for
one.

There is no command-line equivalent: `hospitus bhyve create` takes a single
`--bridge` and `--ip`. A `--network` flag was accepted for a time and did
nothing at all, so it is gone rather than silently discarding what you type.

---

## Scenario 5: Console Access

**Goal**: Access VM console for debugging and management.

### Serial Console (Recommended for Servers)

bhyve uses nmdm (null modem) devices for serial console:

```bash
# Access serial console
doas hospitus bhyve console freebsd-vm
```

**Inside the console**:
- Press `Enter` to get a login prompt
- Type `~.` to disconnect

### Configure Serial Console in Guest

**FreeBSD**:
```bash
# /boot/loader.conf
console="comconsole"
comconsole_speed="115200"
```

**Linux (GRUB)**:
```bash
# /etc/default/grub
GRUB_CMDLINE_LINUX="console=ttyS0,115200"
sudo update-grub
```

### VNC Console (For Graphical VMs)

```bash
# Get VNC information
hospitus bhyve info windows-vm

# Connect via VNC client
vncviewer localhost:5900
```

---

## Scenario 5b: VNC Console Access

**Goal**: Configure and use VNC for graphical console access to bhyve VMs.

VNC (Virtual Network Computing) provides graphical console access, essential for:
- Installing operating systems with graphical installers (Windows, desktop Linux)
- Troubleshooting boot issues
- Managing VMs without network access
- Running graphical applications

### Understanding VNC in bhyve

bhyve uses the `fbuf` (framebuffer) device to provide VNC access. Hospitus manages this automatically through the `hospitus bhyve vnc` command.

**VNC Configuration Options:**

| Option | Description | Default |
|--------|-------------|---------|
| Port | VNC TCP port | 5900 |
| Host | Bind address | 127.0.0.1 |
| Width | Screen width | 1024 |
| Height | Screen height | 768 |
| Wait | Wait for VNC before boot | false |

### Enable VNC on a VM

VNC is not a `create` flag — it is configured with the `hospitus bhyve vnc` command
after the VM exists, then applied on the next start/restart. Create the VM first:

```bash
hospitus bhyve create windows-vm \
    --image /path/to/windows.iso \
    --cpus 4 \
    --memory 8192 \
    --bootloader uefi \
    --description "Windows VM with VNC"

# Turn on VNC (see the next section for options), then restart to apply
hospitus bhyve vnc windows-vm --enable --port 5901
hospitus bhyve restart windows-vm
```

### Enable VNC on Existing VM

```bash
# Enable VNC with default settings
hospitus bhyve vnc myvm --enable

# Enable with custom port and resolution
hospitus bhyve vnc myvm --enable --port 5902 --width 1920 --height 1080

# Enable with wait (VM waits for VNC connection before booting)
hospitus bhyve vnc myvm --enable --wait

# Restart VM to apply changes
hospitus bhyve restart myvm
```

### Connect to VNC Console

#### Using CLI (Recommended)

```bash
# Connect using auto-detected VNC viewer
hospitus bhyve vnc myvm

# Show connection info only (without connecting)
hospitus bhyve vnc myvm --info

# Use a specific VNC viewer
hospitus bhyve vnc myvm --viewer /usr/local/bin/tigervnc
```

**Output of `--info`:**
```
VNC console for myvm:
  Host:       127.0.0.1
  Port:       5901
  Resolution: 1024x768
  URI:        vnc://127.0.0.1:5901

Connect with:
  vncviewer 127.0.0.1:5901

For remote access, forward the port via SSH:
  ssh -L 5901:127.0.0.1:5901 user@host
  vncviewer 127.0.0.1:5901
```

With VNC off, the command fails with `VNC is not enabled for VM myvm`
rather than printing a disabled status.

#### Manual Connection

```bash
# TigerVNC
vncviewer 127.0.0.1::5901

# RealVNC
vncviewer localhost:5901

# Vinagre (GNOME)
vinagre vnc://127.0.0.1:5901

# Remmina
remmina -c vnc://127.0.0.1:5901
```

### Supported VNC Viewers

Hospitus auto-detects these VNC viewers (in order of preference):

1. `vncviewer` (TigerVNC, RealVNC)
2. `xvncviewer`
3. `vinagre` (GNOME)
4. `remote-viewer` (virt-viewer)
5. `krdc` (KDE)
6. `remmina`
7. `gtk-vnc-viewer`

Detection is by binary name on `PATH`. No FreeBSD port installs a
`gtk-vnc-viewer` binary (the `gtk-vnc` port ships `gvncviewer`), so that last
entry never matches in practice — point `--viewer` at the binary instead.

**Install a VNC viewer:**

```bash
# FreeBSD
doas pkg install tigervnc-viewer

# On your workstation (Debian/Ubuntu)
apt install tigervnc-viewer

# Any other viewer, named explicitly
hospitus bhyve vnc myvm --viewer /usr/local/bin/gvncviewer
```

### Disable VNC

```bash
# Disable VNC (requires restart)
hospitus bhyve vnc myvm --disable

# Restart to apply
hospitus bhyve restart myvm
```

### Remote VNC Access

By default, VNC binds to `127.0.0.1` for security. To access remotely:

**Option 1: SSH Tunnel (Recommended)**

```bash
# Create SSH tunnel from your workstation
ssh -L 5901:127.0.0.1:5901 user@freebsd-host

# Connect to localhost:5901 on your workstation
vncviewer localhost::5901
```

**Option 2: Bind to All Interfaces (Not recommended for production)**

```bash
# Allow remote connections (security risk!)
hospitus bhyve vnc myvm --enable --host 0.0.0.0 --port 5901

# Use firewall rules to restrict access
```

### VNC Command Reference

```bash
# Basic usage
hospitus bhyve vnc <vm-name> [flags]

# Flags
--info              Show VNC connection info without connecting
--enable            Enable VNC for the VM
--disable           Disable VNC for the VM
--port <port>       VNC port (default: 5900)
--host <address>    Bind address (default: 127.0.0.1)
--width <pixels>    Screen width (default: 1024)
--height <pixels>   Screen height (default: 768)
--wait              Wait for VNC connection before booting
--viewer <path>     Path to VNC viewer binary
```

### Troubleshooting VNC

**VNC not enabled:**
```console
$ hospitus bhyve vnc myvm
Error: VNC is not enabled for VM myvm

Enable VNC with: hospitus bhyve vnc myvm --enable
```

```bash
# Solution: enable VNC and restart
hospitus bhyve vnc myvm --enable
hospitus bhyve restart myvm
```

**No VNC viewer found** — the connection details are printed first, so you can
still connect by hand:
```console
$ hospitus bhyve vnc myvm
VNC console for myvm:
  Host:       127.0.0.1
  Port:       5901
  Resolution: 1024x768
  URI:        vnc://127.0.0.1:5901
[...]
Error: no VNC viewer found — install one with: pkg install tigervnc-viewer
```

**Connection refused:**
```bash
$ vncviewer localhost::5901
Connection refused

# Check VM is running
hospitus bhyve list

# Check VNC is enabled
hospitus bhyve vnc myvm --info

# Restart VM if needed
hospitus bhyve restart myvm
```

**Black screen:**
- Wait a few seconds (guest OS might be starting)
- Ensure guest OS has graphics drivers
- Try increasing resolution: `--width 1280 --height 1024`

---

## Scenario 6: Production VM with Auto-Start

**Goal**: Configure VMs to start automatically when the host boots.

### Step 1: Create VMs with Boot Order

```bash
# Database starts first (priority 10)
hospitus bhyve create prod-db \
    --image ubuntu-24.04-amd64 \
    --cpus 4 \
    --memory 8192 \
    --auto-start \
    --auto-start-priority 10 \
    --description "Production database"

# Application starts second (priority 20)
hospitus bhyve create prod-app \
    --image ubuntu-24.04-amd64 \
    --cpus 2 \
    --memory 4096 \
    --auto-start \
    --auto-start-priority 20 \
    --auto-start-delay 5000 \
    --description "Production application"

# Web server starts last (priority 30)
hospitus bhyve create prod-web \
    --image ubuntu-24.04-amd64 \
    --cpus 2 \
    --memory 2048 \
    --auto-start \
    --auto-start-priority 30 \
    --auto-start-delay 3000 \
    --description "Production web server"
```

### Step 2: Manage Auto-Start for Existing VMs

```bash
# Enable auto-start
hospitus bhyve autostart enable prod-db --priority 10

# Disable auto-start
hospitus bhyve autostart disable prod-web

# Check status
hospitus bhyve autostart status prod-db
```

### Step 3: View Boot Order

```bash
hospitus bhyve autostart list
```

---

## Scenario 7: Storage Configuration with ZFS

**Goal**: use ZFS for VM storage.

### ZFS ZVOL Benefits

- **Performance**: Block-level access, no filesystem overhead
- **Snapshots**: Instant, space-efficient snapshots
- **Compression**: Transparent compression
- **Cloning**: Instant clones from snapshots

### Default Storage

By default, Hospitus creates ZVOLs for VM disks:

```bash
# VM disk is stored at:
# /dev/zvol/zroot/hospitus/bhyve/<vm-name>/disk0
```

### Add Additional Disks

Disks are specified with `--disk`. Each value is either `<size>` GB, `<size>:<name>`
(size with a device label), or `physical:/dev/xxx` to pass through an existing
physical (external) disk.

```bash
# Create a VM with multiple data disks (size in GB, optional device name)
hospitus bhyve create database \
    --image ubuntu-24.04-amd64 \
    --cpus 4 \
    --memory 8192 \
    --disk 20:data \
    --disk 50:backup

# Attach an existing physical disk (e.g. boot an existing Windows install)
hospitus bhyve create dualboot --disk physical:/dev/ada1
```

### Physical Disk Passthrough

Passthrough is **default-deny**: with no allow-list configured, every physical
disk is refused with

```
physical disk "/dev/ada1" is not allow-listed for passthrough (name it in allowed_physical_disks in hospitusd.conf, then restart hospitusd)
```

Name the devices in the daemon config and restart hospitusd:

```conf
# /usr/local/etc/hospitus/hospitusd.conf
allowed_physical_disks = /dev/ada1 /dev/da5
```

Beyond the allow-list, the device must be a node under `/dev/`; Hospitus validates
that it exists and never destroys it on `delete` (only the VM's own ZFS
datasets are removed).

### ZFS Snapshots

```bash
# List ZFS datasets for a VM
zfs list -r zroot/hospitus/bhyve/database

# Create a snapshot
zfs snapshot zroot/hospitus/bhyve/database/disk0@before-upgrade

# Rollback
zfs rollback zroot/hospitus/bhyve/database/disk0@before-upgrade
```

### Compression

```bash
# Enable compression for VM storage
zfs set compression=lz4 zroot/hospitus/bhyve
```

---

## Command Reference Summary

### Lifecycle Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus bhyve create <name>` | Create a new VM | `--image`, `--cpus`, `--memory`, `--bootloader` |
| `hospitus bhyve start <name>` | Start a stopped VM | - |
| `hospitus bhyve stop <name>` | Stop a running VM | `--force` / `-f` |
| `hospitus bhyve restart <name>` | Restart a VM | - |
| `hospitus bhyve destroy <name>` | Delete a VM | `--force`, `--yes` / `-y` |

### Information Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus bhyve list` | List all VMs | `-o json` |
| `hospitus bhyve info <name>` | Show VM details | `-o json` |
| `hospitus bhyve stats <name>` | Show resource usage | `--watch`, `--interval` |

### Console Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `doas hospitus bhyve console <name>` | Serial console access | - |
| `hospitus bhyve vnc <name>` | Connect to VNC console | `--info`, `--viewer` |
| `hospitus bhyve vnc <name> --enable` | Enable VNC for a VM | `--port`, `--width`, `--height`, `--wait` |
| `hospitus bhyve vnc <name> --disable` | Disable VNC for a VM | - |

### Network Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `doas hospitus bhyve expose add <name>` | Add port forwarding | `--port host:guest` |
| `doas hospitus bhyve expose remove <name>` | Remove port forwarding | `--port` |
| `hospitus bhyve expose list <name>` | List port forwards | - |

### Auto-Start Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus bhyve autostart enable <name>` | Enable auto-start | `--priority`, `--delay` |
| `hospitus bhyve autostart disable <name>` | Disable auto-start | - |
| `hospitus bhyve autostart status <name>` | Check auto-start status | - |

### Create Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--image` | Base image or ISO | Required |
| `--cpus` / `-c` | Number of CPUs | 1 |
| `--memory` / `-m` | Memory in MB | 512 |
| `--bootloader` | Boot method; only `uefi` is accepted | uefi |
| `--os-type` | OS type (freebsd, linux, windows) | freebsd |
| `--bridge` / `-b` | Bridge interface (omit for NAT; `hospitus0` is the default bridge) | NAT |
| `--ip` / `-i` | IP address or 'dhcp' | dhcp |
| `--disk` / `-d` | Additional disk (`size`, `size:name`, or `physical:/dev/xxx`) | - |
| `--disk-driver` | Disk controller: virtio-blk, virtio-scsi, ahci-hd, nvme | virtio-blk |
| (cloud-init) | Not a create flag — `--cloud-init` is refused; use a manifest ([Scenario 2](#scenario-2-linux-vm-with-cloud-init)) | - |
| `--vlan` | VLAN tag for the interface | - |
| `--passthrough` | PCI device(s) to pass through (bus/slot/func, e.g. `1/0/0`) | - |
| `--usb-device` | PCI USB controller(s) to pass through (e.g. `0.14.0`) | - |
| `--usb-tablet` | Add a USB HID tablet for accurate pointer events | false |
| `--description` | VM description | - |
| `--auto-start` | Enable auto-start on boot | false |
| `--auto-start-priority` | Boot priority (0-100, lower first) | 50 |
| `--auto-start-delay` | Delay before starting (ms) | 0 |

> VNC has no `create` flags. Enable and size the framebuffer after creation with
> `hospitus bhyve vnc <name> --enable [--port --width --height --wait]`, then restart.

---

## Troubleshooting

### VM Won't Start

**Error**: "vmm device not available"
```bash
# Load vmm kernel module
kldload vmm

# Make it permanent
echo 'vmm_load="YES"' >> /boot/loader.conf
```

**Error**: "Hardware virtualization not supported"
- Ensure CPU has VT-x (Intel) or AMD-V
- Enable virtualization in BIOS

**Error**: `bhyve exited immediately: exit status 4 (bhyve: no bootrom was
configured)`

The UEFI firmware is a separate package from bhyve itself, so a host can have
bhyve, bhyvectl and the vmm module and still boot nothing:

```bash
pkg install edk2-bhyve
doas service hospitus restart
```

The restart matters: hospitusd resolves the firmware path once at startup.
`doas hospitus init` reports this before you meet it.

### Console Not Working

**Issue**: No output on serial console
- Ensure guest OS has serial console configured
- Check nmdm device exists: `ls /dev/nmdm*`

### Network Issues

**Issue**: VM can't reach network
```bash
# Check the bridge exists (hospitus0 for bridge mode, hospitus-nat for NAT)
ifconfig hospitus0

# Check the tap device is attached as a member
ifconfig hospitus0 | grep member
```

### Performance Issues

**Optimize for performance**:
1. Use ZVOLs for disk storage
2. Use VirtIO drivers in guest
3. Allocate enough memory
4. Don't overcommit CPUs

---

## Next Steps

- **[QEMU Guide](qemu.md)**: Cross-platform hypervisor
- **[Jail Guide](jails.md)**: FreeBSD jails for containers
- **[API Reference](../developer-guide/api-reference.md)**: REST API documentation

---

## Advanced Operations

### Checkpoints

Checkpoints freeze a running VM's CPU and memory state to disk, allowing
you to restore exactly from that point. Unlike snapshots (which are ZFS-only),
checkpoints capture the full bhyve execution state.

> **Needs a kernel built with `BHYVE_SNAPSHOT`.** GENERIC is not, so on a stock
> FreeBSD host this does not work at all: `bhyvectl` has no `--suspend` and
> Hospitus refuses the operation, naming the reason. Check with:
>
> ```sh
> bhyvectl --help | grep -- --suspend     # nothing means unsupported
> ```
>
> Enabling it means building a custom kernel with `options BHYVE_SNAPSHOT`.

```bash
# Create a checkpoint (VM must be running)
hospitus bhyve checkpoint create myvm before-upgrade

# List checkpoints
hospitus bhyve checkpoint list myvm

# Restore from a checkpoint (stops and restarts the VM)
hospitus bhyve checkpoint restore myvm before-upgrade

# Delete a checkpoint
hospitus bhyve checkpoint delete myvm before-upgrade
```

> **Note**: Checkpoints require bhyve checkpoint support and write state to
> `<datadir>/bhyve/<vmname>/checkpoints/`. The VM is stopped during restore.

### Pause / Resume

Pausing freezes a running VM without terminating it (SIGSTOP on the bhyve process).
This is useful for live inspection, temporarily freeing host CPU, or preparation
before a checkpoint.

```bash
hospitus bhyve pause myvm
# ... inspect or snapshot ...
hospitus bhyve resume myvm
```

### Rename

Rename a stopped VM. All files (ZFS dataset, UEFI vars, state) are updated atomically.

```bash
# VM must be stopped
hospitus bhyve stop myvm
hospitus bhyve rename myvm my-renamed-vm
```

### USB Controller Passthrough

Pass a host USB controller into a VM at the PCI level.  Identify the controller's
`bus.device.function` with `pciconf -l` (look for `xhci`/`ehci` entries).

```bash
# List PCI devices on the host
pciconf -l | grep -E 'xhci|ehci'
# → xhci0@pci0:0:20:0: ...

# Create a VM passing through the USB controller (bus.device.function)
hospitus bhyve create myvm --image freebsd-14.3 \
  --usb-device 0.20.0
```

> **Note**: The host must release the controller before the VM can claim it, and
> VT-d/IOMMU must be enabled in the BIOS.

### PCI / GPU Passthrough

Pass an arbitrary PCI device (GPU, NIC, storage or USB controller) into a VM.
This requires VT-d/AMD-Vi enabled in the BIOS and an IOMMU-capable device that is
not in use by the host.

```bash
# List PCI devices on the host (note the bus:slot:func, e.g. pci0:1:0:0)
pciconf -l

# Pass through device at bus 1, slot 0, function 0 (bhyve bus/slot/func form)
hospitus bhyve create gpuvm --image freebsd-14.3 --passthrough 1/0/0
```

When a VM has PCI passthrough devices, bhyve wires (locks) the guest's memory
automatically.  Use multiple `--passthrough` flags to pass several devices.

> **Note**: Passthrough requires `vmm` with IOMMU support. Reserve the device for
> passthrough by attaching it to `ppt` at boot if the host driver claims it.

### VLAN Networking

Attach a VM NIC to a specific VLAN (802.1Q tagging). The VLAN tag is set at
creation with `--vlan`:

```bash
# Create VM on VLAN 10
hospitus bhyve create myvm --image ubuntu-24.04 \
  --vlan 10
```

The host bridge must support VLANs (create a `vlan` interface before starting the VM):

```sh
ifconfig vlan10 create vlan 10 vlandev em0 up
```

### virtio-scsi Disk Controller

Use virtio-scsi for improved multi-disk performance and SCSI command support:

```bash
hospitus bhyve create myvm --image ubuntu-24.04 \
  --disk-driver virtio-scsi   # default: virtio-blk
```

virtio-scsi supports more than 1 disk on a single controller and
exposes proper SCSI semantics to the guest.

### Boot Order

Control which device the VM boots from:

```bash
# Set persistent boot order (disk first, then network)
hospitus bhyve boot-order set myvm disk network

# Get current boot order
hospitus bhyve boot-order get myvm

# Boot from cdrom once (next boot only), then revert to persistent order
hospitus bhyve boot-order once myvm cdrom
```

### Export / Import

Export a VM to a portable tar archive. The VM name and destination path are
positional; add `--stop` to guarantee a consistent on-disk state:

```bash
hospitus bhyve export myvm /var/lib/hospitus/bhyve/backups/myvm-backup.tar.gz --compress --stop
```

Import a VM from an archive (the archive path is positional):

```bash
hospitus bhyve import /var/lib/hospitus/bhyve/backups/myvm-backup.tar.gz --name restored-vm --reset-mac
```

Import flags: `--name` (rename), `--ip` (new address), `--reset-mac` (new MAC),
`--start` (boot after import). The export format includes the disk image, UEFI
vars, and metadata JSON.

