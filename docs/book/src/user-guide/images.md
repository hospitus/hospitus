# Hospitus Image Management Guide

This guide covers managing images for jails and VMs with Hospitus.

---

## Table of Contents

- [Overview](#overview)
- [Image Categories](#image-categories)
- [Listing Available Images](#listing-available-images)
- [Downloading Images](#downloading-images)
- [Managing Downloaded Images](#managing-downloaded-images)
- [FreeBSD Jail Images](#freebsd-jail-images)
- [Linux Jail Images](#linux-jail-images)
- [VM Images](#vm-images)
- [Custom Image Catalog](#custom-image-catalog)
- [Command Reference](#command-reference)

---

## Overview

The centralized image catalog provides FreeBSD base sets, Linux rootfs images, and cloud VM images for all providers.

### Image Storage

Images are stored in `/var/lib/hospitus/images/` with subdirectories by category:

```
/var/lib/hospitus/images/
├── sets/      # Base system archives (.txz) for jails
├── iso/       # ISO images for VM installation
└── cloud/     # Cloud images (.qcow2, .raw) for VMs
```

You can override the image directory by setting the `HOSPITUS_IMAGE_DIR` environment variable.

---

## Image Categories

Hospitus supports three categories of images:

| Category | Description | Formats | Providers |
|----------|-------------|---------|-----------|
| `set` | Base system archives for jails | .txz, .tar.xz, .tar.gz | jail |
| `iso` | Installation ISO images for VMs | .iso | bhyve, qemu |
| `cloud` | Pre-built cloud images for VMs | .qcow2, .raw, .img, .vmdk | bhyve, qemu |

### Providers

- **jail**: FreeBSD jails (uses `set` images)
- **bhyve**: FreeBSD's native hypervisor (uses `iso` and `cloud` images)
- **qemu**: QEMU/KVM hypervisor (uses `iso` and `cloud` images)

---

## Listing Available Images

### List All Available Images

```bash
hospitus image available
```

**Output:**
```
NAME                             CATEGORY  OS       VERSION       ARCH     PROVIDERS
freebsd-14.3-RELEASE-amd64       set       freebsd  14.3-RELEASE  amd64    jail
freebsd-14.3-RELEASE-arm64       set       freebsd  14.3-RELEASE  arm64    jail
ubuntu-24.04-rootfs-amd64        set       linux    24.04         amd64    jail
alpine-3.20-rootfs-amd64         set       linux    3.20          amd64    jail
freebsd-14.3-RELEASE-amd64-dvd   iso       freebsd  14.3-RELEASE  amd64    bhyve,qemu
debian-12-amd64                  cloud     linux    12            amd64    bhyve,qemu

Total: 56 images available

To download an image, use:
  hospitus image fetch <name>
```

### Filter by Category

```bash
# List only jail base sets
hospitus image available --category set

# List only ISO images
hospitus image available --category iso

# List only cloud images
hospitus image available --category cloud
```

### Filter by Provider

```bash
# List images usable by jails
hospitus image available --provider jail

# List images usable by bhyve VMs
hospitus image available --provider bhyve

# List images usable by QEMU VMs
hospitus image available --provider qemu
```

### Filter by Operating System

```bash
# List all FreeBSD images
hospitus image available --os freebsd

# List all Linux images
hospitus image available --os linux

# List OpenBSD images
hospitus image available --os openbsd

# List NetBSD images
hospitus image available --os netbsd
```

### Combine Filters

```bash
# List Linux images for jails
hospitus image available --os linux --category set

# List FreeBSD cloud images
hospitus image available --os freebsd --category cloud
```

### JSON Output

```bash
# Output in JSON format for scripting
hospitus image available --output json
```

---

## Downloading Images

### Fetch an Image

```bash
# Download by image name
hospitus image fetch freebsd-14.3-RELEASE-amd64

# Download Linux rootfs for jails
hospitus image fetch ubuntu-24.04-rootfs-amd64
hospitus image fetch alpine-3.20-rootfs-amd64
```

**Output:**
```
Fetching image: freebsd-14.3-RELEASE-amd64
This may take a few minutes...
Download completed in 2s (avg 91.58 MB/s)
✓ Image freebsd-14.3-RELEASE-amd64 downloaded successfully

You can now create instances with:
  hospitus jail create <name> --image freebsd-14.3-RELEASE-amd64
```

The trailing hint names only the providers that can use the image, so a
cloud image lists `bhyve`/`qemu` instead.

### Verify Downloaded Images

When a SHA256 checksum is available in the catalog, Hospitus automatically verifies the downloaded file.

---

## Managing Downloaded Images

### List Downloaded Images

```bash
# List all downloaded images
hospitus image list
```

**Output:**
```
FILENAME                         CATEGORY  SIZE      PATH
freebsd-14.3-RELEASE-amd64.txz   set       200.6 MB  /var/lib/hospitus/images/sets/freebsd-14.3-RELEASE-amd64.txz
ubuntu-24.04-rootfs-amd64.tar.xz set       45.2 MB   /var/lib/hospitus/images/sets/ubuntu-24.04-rootfs-amd64.tar.xz

Total: 2 images downloaded
```

### Filter by Category

```bash
# List only jail sets
hospitus image list --category set

# List only ISO images
hospitus image list --category iso

# List only cloud images
hospitus image list --category cloud
```

### Delete Images

```bash
# Delete by filename
hospitus image delete freebsd-14.3-RELEASE-amd64.txz

# Alternative commands
hospitus image rm freebsd-14.3-RELEASE-amd64.txz
hospitus image remove freebsd-14.3-RELEASE-amd64.txz
```

---

## FreeBSD Jail Images

### Available FreeBSD Versions

| Image Name | Version | Architecture | Notes |
|------------|---------|--------------|-------|
| `freebsd-14.3-RELEASE-amd64` | 14.3-RELEASE | amd64 | Recommended |
| `freebsd-14.3-RELEASE-arm64` | 14.3-RELEASE | arm64 | For ARM64 systems |
| `freebsd-14.3-RELEASE-riscv64` | 14.3-RELEASE | riscv64 | For RISC-V systems |
| `freebsd-14.3-RELEASE-i386` | 14.3-RELEASE | i386 | 32-bit systems |
| `freebsd-14.2-RELEASE-amd64` | 14.2-RELEASE | amd64 | Previous stable |
| `freebsd-13.4-RELEASE-amd64` | 13.4-RELEASE | amd64 | Extended support |
| `freebsd-15.0-CURRENT-amd64` | 15.0-CURRENT | amd64 | Development |
| `freebsd-14-STABLE-amd64` | 14-STABLE | amd64 | Stable branch |

### Usage Example

```bash
# Download FreeBSD 14.3
hospitus image fetch freebsd-14.3-RELEASE-amd64

# Create a jail using this image
hospitus jail create webserver --image freebsd-14.3-RELEASE-amd64 --vnet --ip dhcp
```

---

## Linux Jail Images

FreeBSD supports running Linux applications in jails via the linux64 ABI compatibility layer. Hospitus provides rootfs images for popular Linux distributions.

### Available Linux Distributions

| Image Name | Distribution | Version | Notes |
|------------|--------------|---------|-------|
| `ubuntu-24.04-rootfs-amd64` | Ubuntu | 24.04 LTS | Noble Numbat |
| `ubuntu-22.04-rootfs-amd64` | Ubuntu | 22.04 LTS | Jammy Jellyfish |
| `alpine-3.20-rootfs-amd64` | Alpine | 3.20 | Minimal (~5MB) |
| `alpine-3.19-rootfs-amd64` | Alpine | 3.19 | Previous stable |
| `fedora-41-rootfs-amd64` | Fedora | 41 | Latest Fedora |
| `fedora-40-rootfs-amd64` | Fedora | 40 | Previous stable |
| `debian-12-rootfs-amd64` | Debian | 12 | Bookworm |

### Prerequisites for Linux Jails

Before creating Linux jails, ensure the Linux compatibility layer is enabled:

```bash
# Load linux64 kernel module
doas kldload linux64

# Enable at boot (add to /boot/loader.conf)
echo 'linux64_load="YES"' | doas tee -a /boot/loader.conf

# Mount linprocfs (required for Linux jails)
doas mount -t linprocfs linproc /compat/linux/proc
```

### Usage Example

```bash
# Download Alpine Linux rootfs
hospitus image fetch alpine-3.20-rootfs-amd64

# Create a Linux jail
hospitus jail create alpine-test \
    --image alpine-3.20-rootfs-amd64 \
    --vnet \
    --ip dhcp \
    --description "Alpine Linux test jail"

# Start and access
hospitus jail start alpine-test
doas hospitus jail console alpine-test
```

`--ip` is not optional here: a `--vnet` jail created without one gets a network
with no address, hospitus says so ("network has no address; no interface will be
created for it"), and the jail comes up unreachable.

Hospitus reads the userland from the image name, so no `--os-type` is needed —
`hospitus jail exec alpine-test uname -s` answers `Linux`.

### Linux Jail Limitations

- Network tools may have reduced functionality
- Some system calls are not supported
- Performance is native (no emulation overhead)
- `/proc` must be mounted for many Linux applications

---

## VM Images

### ISO Images for Installation

| Image Name | OS | Version | Use Case |
|------------|------|---------|----------|
| `freebsd-14.3-RELEASE-amd64-dvd` | FreeBSD | 14.3-RELEASE | Full installation |
| `freebsd-14.3-RELEASE-amd64-disc` | FreeBSD | 14.3-RELEASE | Minimal installation |
| `debian-12-netinst-amd64` | Debian | 12 | Network installation |
| `ubuntu-24.04-live-amd64` | Ubuntu | 24.04 | Live/Install media |
| `openbsd-7.6-amd64` | OpenBSD | 7.6 | OpenBSD install |
| `netbsd-10.0-amd64` | NetBSD | 10.0 | NetBSD install |

### Cloud Images (Pre-installed)

| Image Name | OS | Format | Features |
|------------|------|--------|----------|
| `freebsd-14.3-RELEASE-amd64-ufs` | FreeBSD | qcow2 | UFS filesystem |
| `freebsd-14.3-RELEASE-amd64-zfs` | FreeBSD | qcow2 | ZFS filesystem |
| `freebsd-14.3-RELEASE-amd64-raw` | FreeBSD | raw | Raw disk image |
| `debian-12-amd64` | Debian | qcow2 | Cloud-init enabled |
| `ubuntu-24.04-amd64` | Ubuntu | qcow2 | Cloud-init enabled |
| `alpine-3.20-amd64` | Alpine | qcow2 | Minimal VM |
| `rocky-9-amd64` | Rocky | qcow2 | RHEL compatible |
| `almalinux-9-amd64` | AlmaLinux | qcow2 | RHEL compatible |

### Usage Example (bhyve VM)

```bash
# Download a cloud image
hospitus image fetch debian-12-amd64

# Create a VM using the cloud image
hospitus bhyve create debian-vm \
    --image debian-12-amd64 \
    --cpus 2 \
    --memory 2048
```

---

## Custom Image Catalog

Hospitus ships with an embedded catalog. Your own profiles go in
`/var/lib/hospitus/config/catalog.json` (that is, `config/catalog.json` next to
the image directory). At startup hospitusd **merges** that file on top of the
embedded catalog: an entry whose `name` matches a built-in one replaces it, new
names are added, and the remaining embedded entries are kept. A stale or
tampered file therefore cannot drop trusted entries — and you cannot remove a
built-in image by leaving it out.

A catalog refresh (`POST /api/v1/images/refresh`) rewrites this same file from
the upstream catalog and its release probe, so hand edits to it can be
overwritten; keep a copy of your profiles elsewhere.

The file is a JSON **array** of profile objects. The field names match the
built-in catalog exactly:

```json
[
  {
    "name": "custom-freebsd-14.3",
    "display": "Custom FreeBSD 14.3",
    "description": "Custom FreeBSD build with extra packages",
    "category": "set",
    "format": "txz",
    "os_type": "freebsd",
    "os_version": "14.3-CUSTOM",
    "arch": "amd64",
    "providers": ["jail"],
    "url": "https://my-server.example.com/images/custom-14.3.txz",
    "sha256": "abc123..."
  }
]
```

| Field | Required | Notes |
|-------|----------|-------|
| `name` | yes | Identifier passed to `hospitus image fetch` and `--image` |
| `display` | yes | Human-readable label |
| `category` | yes | `set`, `iso`, or `cloud` |
| `format` | yes | e.g. `txz`, `qcow2`, `raw`, `iso` |
| `os_type` / `os_version` / `arch` | yes | Metadata used for filtering |
| `providers` | yes | Array of `jail`, `bhyve`, `qemu` |
| `url` | yes | Download location |
| `sha256` | no | Enables automatic checksum verification |

Your profiles then appear in `hospitus image available`.

---

## Command Reference

### hospitus image available

List available images from the catalog.

```
Usage:
  hospitus image available [flags]

Flags:
      --arch string       Filter by architecture (amd64, arm64, riscv64, i386)
  -c, --category string   Filter by category (set, iso, cloud)
  -h, --help              help for available
      --os string         Filter by OS type (freebsd, linux, openbsd, netbsd)
  -o, --output string     Output format (table, json) (default "table")
  -p, --provider string   Filter by provider (jail, bhyve, qemu)
```

### hospitus image list

List downloaded images.

```
Usage:
  hospitus image list [flags]

Flags:
  -c, --category string   Filter by category (set, iso, cloud)
  -h, --help              help for list
  -o, --output string     Output format (table, json) (default "table")
```

### hospitus image fetch

Download an image from the catalog.

```
Usage:
  hospitus image fetch <name> [flags]

Flags:
  -h, --help   help for fetch
```

### hospitus image delete

Delete a downloaded image.

```
Usage:
  hospitus image delete <filename> [flags]

Aliases:
  delete, rm, remove

Flags:
  -h, --help   help for delete
```

---

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `HOSPITUS_IMAGE_DIR` | Directory for storing images | `/var/lib/hospitus/images` |

---

## Next Steps

- [Jail User Guide](jails.md): Creating and managing jails
- [bhyve User Guide](bhyve.md): Managing bhyve VMs
- [QEMU User Guide](qemu.md): Managing QEMU VMs
