# Hospitus QEMU User Guide

Managing QEMU virtual machines with Hospitus, worked through as scenarios: a Linux VM, a Windows development environment, cross-architecture emulation.

---

## Table of Contents

- [Introduction](#introduction)
- [Scenario 1: Your First Linux VM](#scenario-1-your-first-linux-vm)
- [Scenario 2: Windows Development Environment](#scenario-2-windows-development-environment)
- [Scenario 3: Cross-Architecture Emulation](#scenario-3-cross-architecture-emulation)
- [Scenario 4: Network Configuration and Port Forwarding](#scenario-4-network-configuration-and-port-forwarding)
- [Scenario 5: VNC Console Access](#scenario-5-vnc-console-access)
- [Scenario 6: Production VM with Auto-Start](#scenario-6-production-vm-with-auto-start)
- [Scenario 7: Snapshots](#scenario-7-snapshots)
- [Scenario 8: ISO Media Management](#scenario-8-iso-media-management)
- [Stats and Monitoring](#stats-and-monitoring)
- [Command Reference Summary](#command-reference-summary)

---

## Introduction

QEMU provides cross-platform virtualization. Hospitus manages QEMU VMs on FreeBSD, macOS, and Linux.

### Why QEMU?

QEMU is the ideal choice when you need:
- **Cross-platform support**: Runs on FreeBSD, Linux, macOS, and Windows
- **Cross-architecture emulation**: Run ARM64 or RISC-V VMs on AMD64 hosts
- **Broad OS support**: Linux, Windows, FreeBSD, and exotic operating systems
- **Advanced features**: Snapshots, live migration, VNC console

### QEMU vs bhyve

| Feature | QEMU | bhyve |
|---------|------|-------|
| Platform | Cross-platform | FreeBSD only |
| Performance | Good (KVM on Linux) | Native |
| Cross-arch | Yes (emulation) | No |
| Live migration | Yes | No |
| Ease of setup | Easy | Requires FreeBSD |

### Prerequisites

- FreeBSD, Linux, or macOS
- QEMU installed:
  - **FreeBSD**: `doas pkg install qemu`
  - **Linux**: `apt install qemu-system-x86` / `dnf install qemu-kvm`
  - **macOS**: `brew install qemu` (uses HVF acceleration on Apple Silicon and Intel)
- Hospitus daemon (`hospitusd`) running
- Root privileges for networking features (`doas`)

### Managing VM Images

Before creating VMs, you need disk images. Hospitus provides the `hospitus image` command to manage images.

#### View Available Images

```bash
# List all available cloud images
hospitus image available --category cloud

# Filter by provider
hospitus image available --provider qemu

# Filter by OS
hospitus image available --os linux
```

#### Download Images

```bash
# Download Ubuntu cloud image
hospitus image fetch ubuntu-24.04-amd64

# Download FreeBSD cloud image
hospitus image fetch freebsd-14.3-RELEASE-amd64-zfs
```

#### Supported Image Formats

| Format | Extension | Description |
|--------|-----------|-------------|
| QCOW2 | .qcow2 | QEMU native, supports snapshots |
| Raw | .img, .raw | Simple, best performance |
| VHD | .vhd | Hyper-V compatible |
| VMDK | .vmdk | VMware compatible |

### Ensure Hospitusd is Running

```bash
# Check if the daemon is running (the rc.d service is named "hospitus")
service hospitus status

# If not running, start it
doas service hospitus start
```

> On macOS the daemon is not an rc.d service — start `hospitusd` directly (see the
> [Installation](../getting-started/installation.md) guide).

---

## Scenario 1: Your First Linux VM

**Goal**: Create an Ubuntu virtual machine with cloud-init configuration.

### Step 1: Download the Cloud Image

```bash
# Fetch Ubuntu cloud image
hospitus image fetch ubuntu-24.04-amd64
```

### Step 2: Create the VM

```bash
hospitus qemu create ubuntu-server \
    --image ubuntu-24.04-amd64 \
    --cpus 2 \
    --memory 2048 \
    --disk 20 \
    --os-type linux \
    --description "Ubuntu 24.04 development server"
```

**What happens**: Hospitus creates a new VM configuration with a QCOW2 disk based on the cloud image, ready for boot with cloud-init.

The QEMU provider is single-disk. `--disk <size>` sets the root disk size in GB (default 10 GB). To boot an existing physical/external disk directly, pass `--disk physical:/dev/xxx` — the device is used as-is and is never created or destroyed by Hospitus:

```bash
hospitus qemu create dualboot --disk physical:/dev/ada2
```

**Output**:
```
Creating QEMU VM ubuntu-server...
VM created: ubuntu-server (ID: ubuntu-server)
```

### Step 3: Start the VM

```bash
hospitus qemu start ubuntu-server
```

**Output**:
```
VM started: ubuntu-server
```

### Step 4: Verify the VM is Running

```bash
hospitus qemu list
```

**Output**:
```
NAME            STATE     IP ADDRESS   CPUs   MEMORY   PROVIDER   CREATED
ubuntu-server   running   -            2      2048MB   qemu       2024-01-15 10:30
```

### Step 5: Get VM Details

```bash
hospitus qemu info ubuntu-server
```

**Output**:
```
Name:         ubuntu-server
State:        running
Provider:     qemu
Description:  Ubuntu 24.04 development server

Resources:
  CPUs:       2
  Memory:     2048 MB

Image:        ubuntu-24.04-amd64
OS:           linux

Created:      2024-01-15T10:30:00+01:00
```

### Step 6: Access the Console

`hospitus qemu console` reports how to reach the VM's display; it does not open a
session itself.

```bash
doas hospitus qemu console ubuntu-server
```

```
Console information for VM ubuntu-server:

VNC access:
  Address: localhost:5900 (display :0)

Example clients:
  vncviewer localhost:0
  remote-viewer vnc://localhost:5900
```

### Step 7: Stop the VM

```bash
# Graceful shutdown (sends ACPI signal)
hospitus qemu stop ubuntu-server

# Force stop if needed
hospitus qemu stop ubuntu-server --force
```

---

## Scenario 2: Windows Development Environment

**Goal**: Create a Windows VM for development and testing.

### Step 1: Download Windows ISO

First, download a Windows ISO from Microsoft and place it in a known location.

### Step 2: Create the VM with ISO

```bash
hospitus qemu create windows-dev \
    --image /path/to/windows11.iso \
    --cpus 4 \
    --memory 8192 \
    --os-type windows \
    --description "Windows 11 development VM"
```

### Step 3: Configure VNC for Installation

Windows requires a graphical console for installation:

```bash
# Start VM with VNC enabled (default)
hospitus qemu start windows-dev
```

### Step 4: Connect via VNC

```bash
# Get VNC port
hospitus qemu info windows-dev --output json | jq '.spec.provider_config.vnc_port'

# Connect with VNC client
vncviewer localhost:5900
```

### Step 5: Complete Windows Installation

Follow the Windows installation process through VNC.

### Step 6: Install VirtIO Drivers

For best performance, install VirtIO drivers from the [Fedora VirtIO drivers ISO](https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/stable-virtio/).

---

## Scenario 3: Cross-Architecture Emulation

**Goal**: Run ARM64 or RISC-V VMs on an AMD64 host.

### Step 1: Download Cross-Architecture Image

```bash
# Download ARM64 Ubuntu image
hospitus image fetch ubuntu-24.04-arm64

```

The catalog carries no RISC-V cloud image, so the RISC-V example below needs
one you supply yourself — `hospitus image available --os linux --category cloud`
lists what ships with hospitus.

### Step 2: Create ARM64 VM

```bash
hospitus qemu create arm-test \
    --image ubuntu-24.04-arm64 \
    --arch arm64 \
    --cpus 2 \
    --memory 2048 \
    --description "ARM64 test VM via QEMU emulation"
```

**What happens**: Hospitus selects `qemu-system-aarch64` automatically based on the `--arch` flag.

### Step 3: Create RISC-V VM

```bash
hospitus qemu create riscv-test \
    --image /path/to/your-riscv64-cloud-image.qcow2 \
    --arch riscv64 \
    --cpus 1 \
    --memory 1024 \
    --description "RISC-V test VM"
```

### Step 4: Start and Verify

```bash
hospitus qemu start arm-test

# The emulator hospitus chose is the answer to what --arch did
doas pgrep -lf "name arm-test" | grep -o "qemu-system-[a-z0-9_]*"
# Output: qemu-system-aarch64

# For the guest's own view, reach it through the display
doas hospitus qemu console arm-test
```

### Performance Considerations

Cross-architecture emulation is slower than native execution:

| Scenario | Relative Speed |
|----------|----------------|
| Native (KVM on Linux) | 100% |
| Same-arch emulation | 50-80% |
| Cross-arch emulation | 5-20% |

Use cross-architecture emulation for:
- Testing ARM64 builds before deployment
- CI/CD for multi-architecture software
- Development of embedded systems

---

## Scenario 4: Network Configuration and Port Forwarding

**Goal**: Configure networking and expose services from the VM.

### Network Types

QEMU supports two network types:

| Type | Description | Use Case |
|------|-------------|----------|
| NAT (user) | Private network with NAT | Simple, no root needed |
| Bridge | Bridged to host network | Production, direct access |

### NAT Networking with Port Forwarding

NAT is the default and simplest option:

```bash
# Create VM with NAT (default)
hospitus qemu create webserver \
    --image ubuntu-24.04-amd64 \
    --cpus 2 \
    --memory 2048
```

#### Add Port Forwarding

```bash
# Forward host port 8080 to VM port 80
doas hospitus qemu expose add webserver --port 8080:80

# Forward SSH (host 2222 to VM 22)
doas hospitus qemu expose add webserver --port 2222:22

# List port forwarding rules
hospitus qemu expose list webserver
```

**Output**:
```
Port forwarding rules for VM webserver:

PROTOCOL  HOST PORT  GUEST PORT
tcp       8080       80
tcp       2222       22
```

#### Remove Port Forwarding

```bash
doas hospitus qemu expose remove webserver --port 8080
```

### Bridge Networking

For direct network access:

```bash
# Create VM with bridge networking
hospitus qemu create production-vm \
    --image ubuntu-24.04-amd64 \
    --cpus 4 \
    --memory 4096 \
    --bridge br0 \
    --ip 192.168.1.100/24
```

**Note**: Bridge networking requires:
- A bridge interface configured on the host
- Root privileges
- Proper firewall rules

---

## Scenario 5: VNC Console Access

**Goal**: Access VM graphical console via VNC.

### Default VNC Configuration

QEMU VMs automatically get a VNC server on an available port.

### Connect to VNC Console

```bash
# Get VM info including VNC port
hospitus qemu info webserver

# Connect with VNC client
vncviewer localhost:5900
```

### Secure VNC with Password

For production environments, configure VNC password in your VM creation or through QEMU options.

### Alternative: Serial Console

Hospitus gives every QEMU VM a serial port on a Unix socket, at
`/var/lib/hospitus/qemu/<name>/serial.sock`:

```bash
doas nc -U /var/lib/hospitus/qemu/webserver/serial.sock
```

It stays silent until the guest writes to it. A guest that boots through UEFI
uses its video console by default — the Ubuntu and FreeBSD cloud images both do
— so nothing reaches the socket until you say otherwise:

```bash
# Ubuntu: in /etc/default/grub, then update-grub
GRUB_CMDLINE_LINUX="console=ttyS0,115200"

# FreeBSD: in /boot/loader.conf
console="comconsole"
```

---

## Scenario 6: Production VM with Auto-Start

**Goal**: Configure VMs to automatically start when the host boots.

### Step 1: Create VM with Auto-Start

```bash
hospitus qemu create database \
    --image ubuntu-24.04-amd64 \
    --cpus 4 \
    --memory 8192 \
    --auto-start \
    --auto-start-priority 10 \
    --description "Production PostgreSQL database"

hospitus qemu create webserver \
    --image ubuntu-24.04-amd64 \
    --cpus 2 \
    --memory 4096 \
    --auto-start \
    --auto-start-priority 20 \
    --auto-start-delay 5000 \
    --description "Production web server"
```

**Key flags**:
- `--auto-start`: Enable auto-start on host boot
- `--auto-start-priority`: Lower numbers start first
- `--auto-start-delay`: Delay in milliseconds before starting

### Step 2: Configure Auto-Start for Existing VM

```bash
# Enable auto-start
hospitus qemu autostart enable webserver --priority 20 --delay 5000

# Disable auto-start
hospitus qemu autostart disable webserver

# Check auto-start status
hospitus qemu autostart status webserver
```

### Step 3: List All Auto-Start VMs

```bash
hospitus qemu autostart list
```

---

## Scenario 7: Snapshots

**Goal**: Save and restore VM disk state at specific points in time.

QEMU snapshots use `qemu-img` internally. The VM **must be stopped** before creating or restoring a snapshot.

### Create a Snapshot

```bash
# Stop the VM first
hospitus qemu stop myvm

# Create a snapshot
hospitus qemu snapshot create myvm before-upgrade
```

**Output**:
```
Creating snapshot myvm@before-upgrade...
Snapshot created: myvm@before-upgrade
```

### List Snapshots

```bash
hospitus qemu snapshot list myvm
```

**Output**:
```
NAME             CREATED               SIZE
before-upgrade   2024-06-01 10:00      512 MB
post-install     2024-05-15 08:30      480 MB
```

### Restore a Snapshot

```bash
# The VM must be stopped
hospitus qemu stop myvm

hospitus qemu snapshot restore myvm before-upgrade
```

> **Warning**: Restoring overwrites the current disk state. All changes since the snapshot will be lost.

### Delete a Snapshot

```bash
hospitus qemu snapshot delete myvm old-snapshot
# Confirmation required; use -y to skip
hospitus qemu snapshot delete myvm old-snapshot -y
```

---

## Scenario 8: ISO Media Management

**Goal**: Boot a VM from an ISO for OS installation, then switch to disk boot.

### Step 1: Download or Place an ISO

```bash
# Place your ISO in a known location
ls /var/lib/hospitus/images/iso/
# FreeBSD-14.2-RELEASE-amd64-disc1.iso
```

### Step 2: Create the VM

```bash
hospitus qemu create install-vm \
    --image /var/lib/hospitus/images/iso/FreeBSD-14.2-RELEASE-amd64-disc1.iso \
    --cpus 2 \
    --memory 2048 \
    --os-type freebsd
```

### Step 3: Insert ISO into Running VM

You can also insert media into an already-created VM:

```bash
hospitus qemu media insert install-vm \
    --path /var/lib/hospitus/images/iso/FreeBSD-14.2-RELEASE-amd64-disc1.iso

# List current media devices
hospitus qemu media list install-vm
```

**Output**:
```
DEVICE     TYPE    STATUS    PATH
ide0-cd0   cdrom   inserted  /var/lib/hospitus/images/iso/FreeBSD-14.2-RELEASE-amd64-disc1.iso
```

### Step 4: Set Boot Order to CD-ROM First

```bash
hospitus qemu boot-order set install-vm cdrom disk
```

### Step 5: Start VM and Install

```bash
hospitus qemu start install-vm
# Connect via VNC to complete installation
vncviewer localhost:5900
```

### Step 6: After Installation — Eject and Boot from Disk

```bash
# Eject the ISO
hospitus qemu media eject install-vm ide0-cd0

# Set boot order to disk
hospitus qemu boot-order set install-vm disk cdrom

# Restart into installed OS
hospitus qemu restart install-vm
```

---

## Stats and Monitoring

### Real-Time Resource Usage

```bash
hospitus qemu stats myvm
```

**Output**:
```
Name:    myvm
State:   running

CPU Usage:    12.4%
Memory:       1024 MB / 2048 MB (50%)
Disk Read:    4.2 MB/s
Disk Write:   1.1 MB/s
Net RX:       512 KB/s
Net TX:       128 KB/s
```

### Watching stats

```bash
hospitus qemu stats myvm --watch
hospitus qemu stats myvm --watch --interval 5
```

`stats` prints a fixed table; it has no JSON form. For machine-readable output,
read the metrics endpoint directly: `GET /api/v1/instances/{id}/metrics`.

### Monitor Multiple VMs

```bash
# List all VMs with state
hospitus qemu list

# Filter JSON for running VMs
hospitus qemu list --output json | grep -A5 '"state": "running"'
```

---

## Command Reference Summary

### Lifecycle Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus qemu create <name>` | Create a new VM | `--image`, `--cpus`, `--memory`, `--arch` |
| `hospitus qemu start <name>` | Start a stopped VM | - |
| `hospitus qemu stop <name>` | Stop a running VM | `--force` / `-f` |
| `hospitus qemu restart <name>` | Restart a VM | - |
| `hospitus qemu destroy <name>` | Delete a VM | `--force`, `--yes` / `-y` |

### Information Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus qemu list` | List all VMs | `--output json` |
| `hospitus qemu info <name>` | Show VM details | `--output json` |
| `hospitus qemu stats <name>` | Show resource usage | `--watch`, `--interval` |

### Console Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus qemu console <name>` | Where to reach the VM's display | - |

### Network Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus qemu expose add <name>` | Add port forwarding | `--port host:guest` |
| `hospitus qemu expose remove <name>` | Remove port forwarding | `--port`, `--protocol` |
| `hospitus qemu expose list <name>` | List port forwards | - |

### Auto-Start Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus qemu autostart enable <name>` | Enable auto-start | `--priority`, `--delay` |
| `hospitus qemu autostart disable <name>` | Disable auto-start | - |
| `hospitus qemu autostart status <name>` | Check auto-start status | - |

### Create Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--image` | Base image or ISO | Required |
| `--cpus` / `-c` | Number of CPUs | 1 |
| `--memory` / `-m` | Memory in MB | 512 |
| `--arch` | Architecture (amd64, arm64, i386, riscv64) | native |
| `--os-type` | OS type (linux, windows, freebsd) | freebsd |
| `--bridge` / `-b` | Bridge interface for networking | - |
| `--ip` / `-i` | IP address or 'dhcp' | dhcp |
| (cloud-init) | Not a create flag — `--cloud-init` is refused; use a manifest | - |
| `--description` | VM description | - |
| `--auto-start` | Enable auto-start on boot | false |
| `--auto-start-priority` | Boot priority (0-100, lower first) | 50 |
| `--auto-start-delay` | Delay before starting (ms) | 0 |

### Snapshot Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus qemu snapshot create <name> <snap>` | Create a disk snapshot (VM must be stopped) | - |
| `hospitus qemu snapshot list <name>` | List snapshots | `--output json` |
| `hospitus qemu snapshot restore <name> <snap>` | Restore to snapshot (VM must be stopped) | `-y` |
| `hospitus qemu snapshot delete <name> <snap>` | Delete a snapshot | `-y` |

### Media Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus qemu media insert <name>` | Insert ISO into CD-ROM | `--path`, `--readonly`, `--bootable` |
| `hospitus qemu media eject <name> <device>` | Eject media | - |
| `hospitus qemu media list <name>` | List media devices | - |
| `hospitus qemu boot-order get\|set\|once <name> [device...]` | Get/set boot order (top-level command; positional device names) | - |

### Stats Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus qemu stats <name>` | Show resource usage | `--watch`, `--interval` |

---

## Troubleshooting

### VM Won't Start

**Error**: "QEMU binary not found"
- Install QEMU: `doas pkg install qemu` (FreeBSD) or `apt install qemu-system` (Linux)
- On macOS: `brew install qemu`

**Error**: "KVM not available"
- On Linux, ensure `/dev/kvm` exists and you have permissions
- On FreeBSD, KVM is not available (QEMU runs in emulation mode)
- On macOS, HVF (Hypervisor.framework) is used automatically — no configuration needed

### No IP Address Displayed

`hospitus qemu info myvm` shows no IP even after the VM starts:

- The IP is reported via the **QEMU Guest Agent**. If it is not installed, Hospitus cannot discover the IP.
- Install the guest agent inside the VM:
  ```bash
  # Ubuntu/Debian
  apt install qemu-guest-agent
  systemctl enable --now qemu-guest-agent

  # FreeBSD (inside VM) — the rc script is qemu_guest_agent
  pkg install qemu-guest-agent
  sysrc qemu_guest_agent_enable=YES
  service qemu_guest_agent start
  ```
- After installing, restart the VM. The IP will appear in `hospitus qemu info`.

### Poor Performance

- Enable KVM on Linux for near-native performance
- Allocate more CPUs and memory
- Use VirtIO drivers in the guest
- Use QCOW2 format for better snapshot support

### Network Issues

- For NAT: Check port forwarding rules with `hospitus qemu expose list`
- For bridge: Ensure bridge interface exists and firewall allows traffic

### Console Not Responding

- `hospitus qemu console` prints VNC details; it does not open a session
- The serial socket carries nothing until the guest is told to use it — see
  [Alternative: Serial Console](#alternative-serial-console)

---

## Next Steps

- **[bhyve Guide](bhyve.md)**: FreeBSD native hypervisor
- **[Jail Guide](jails.md)**: FreeBSD jails for containers
- **[API Reference](../developer-guide/api-reference.md)**: REST API documentation
