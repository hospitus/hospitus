# Your First Jail

This tutorial walks you through creating, managing, and destroying your first FreeBSD jail with Hospitus.

## Prerequisites

- Hospitus daemon running (`hospitusd`)
- FreeBSD 14.0+ host with ZFS
- Root access or `doas` configured

## Step 1: Check Available Images

First, see what FreeBSD images are available:

```bash
hospitus image available
```

Output:
```
NAME                        CATEGORY  OS       VERSION       ARCH     PROVIDERS
freebsd-14.3-RELEASE-amd64  set       freebsd  14.3-RELEASE  amd64    jail
freebsd-14.3-RELEASE-arm64  set       freebsd  14.3-RELEASE  arm64    jail
freebsd-14.2-RELEASE-amd64  set       freebsd  14.2-RELEASE  amd64    jail
freebsd-14.1-cloud-amd64    cloud     freebsd  14.1-RELEASE  amd64    bhyve,qemu

Total: 56 images available

To download an image, use:
  hospitus image fetch <name>
```

The table is abridged here; the command lists the whole catalog.

## Step 2: Fetch an Image

Download the FreeBSD 14.3 base image:

```bash
hospitus image fetch 14.3-RELEASE-amd64
```

Output:
```
Fetching image: 14.3-RELEASE-amd64
This may take a few minutes...
Download completed in 2s (avg 91.58 MB/s)
✓ Image 14.3-RELEASE-amd64 downloaded successfully

You can now create instances with:
  hospitus jail create <name> --image 14.3-RELEASE-amd64
```

Run it again and it says `Image already exists` instead of downloading: the
command is safe to repeat.

## Step 3: Create the Jail

Create a jail named "myjail" with VNET networking:

```bash
hospitus jail create myjail --image 14.3-RELEASE-amd64 --vnet --ip 10.0.0.10/24 \
  --cpus 2 --memory 1024
```

Output:
```
Creating jail myjail...
Jail created: myjail (ID: myjail)
```

What happened:
- `--image 14.3-RELEASE-amd64`: Uses the FreeBSD 14.3 base system
- `--vnet`: Creates isolated network stack with its own IP
- `--ip 10.0.0.10/24`: The address on that stack. Leave it out and the jail
  starts with no interface at all — Hospitus warns `network has no address` and
  anything that reaches the network, `pkg` included, fails to resolve. Use
  `--ip dhcp` where a DHCP server serves the bridge.
- `--cpus 2`: Limits to 2 CPU cores
- `--memory 1024`: Limits to 1024 MB (1 GB) RAM

## Step 4: Start the Jail

```bash
hospitus jail start myjail
```

Output:
```
Jail started: myjail
```

## Step 5: Verify the Jail is Running

```bash
hospitus jail list
```

Output:
```
NAME     STATE     IP ADDRESS     CPUs   MEMORY   PROVIDER   CREATED
myjail   running   10.0.0.10/24   2      1024MB   jail       2026-08-24 05:18
```

For scripting, prefer JSON output — the table layout is meant for humans:

```bash
hospitus jail list -o json | jq '.[].name'
```

## Step 6: Get Detailed Information

```bash
hospitus jail info myjail
```

Output:
```
Name:         myjail
State:        running
Provider:     jail

Resources:
  CPUs:       2
  Memory:     1024 MB

Network:
  Type:       bridge
  IPv4:       10.0.0.10/24

Image:        14.3-RELEASE-amd64
OS:           freebsd 14.3-RELEASE
ZFS Dataset:  zroot/hospitus/jails/myjail

Created:      2026-08-24T05:27:13+02:00
```

Add `-o json` to get the full machine-readable record.

## Step 7: Execute Commands Inside

Run commands inside the jail:

```bash
# Check the FreeBSD version
hospitus jail exec myjail freebsd-version
```

Output:
```
14.3-RELEASE
```

```bash
# Install a package
hospitus jail exec myjail env IGNORE_OSVERSION=yes ASSUME_ALWAYS_YES=yes \
  pkg install -y nginx
```

Those two variables are what makes this work unattended. `FreeBSD:14:amd64` is
built against the newest supported 14 release, so its `pkg` can carry a higher
`__FreeBSD_version` than a 14.3 image; pkg then stops to ask
`Ignore the mismatch and continue? [y/N]`, and `exec` has no terminal to answer
with. `IGNORE_OSVERSION` accepts the difference, `ASSUME_ALWAYS_YES` covers the
bootstrap prompt. To settle it once for the life of the jail instead:

```bash
hospitus jail exec myjail /bin/sh -c \
  'mkdir -p /usr/local/etc && echo IGNORE_OSVERSION=true >> /usr/local/etc/pkg.conf'
```

## Step 8: Get an Interactive Shell

Open a console session:

```bash
hospitus jail console myjail
```

This drops you into an interactive shell inside the jail:

```
root@myjail:~ # hostname
myjail
root@myjail:~ # exit
```

## Step 9: Stop the Jail

```bash
hospitus jail stop myjail
```

Output:
```
Jail stopped: myjail
```

## Step 10: Create a Snapshot

Before making changes, create a snapshot:

```bash
hospitus jail snapshot create myjail baseline
```

Output:
```
Creating snapshot myjail@baseline...
Snapshot created: myjail@baseline
```

List snapshots:

```bash
hospitus jail snapshot list myjail
```

Output:
```
SNAPSHOT  CREATED              SIZE
baseline  2026-08-24 05:28:04  0B
```

## Step 11: Restore from Snapshot (if needed)

If you need to rollback:

```bash
hospitus jail snapshot restore myjail baseline
```

## Step 12: Destroy the Jail

When you're done:

```bash
hospitus jail destroy myjail
```

You'll be prompted for confirmation. Answer `yes` (or `y`) — anything else, a
bare Return included, cancels:
```
WARNING: This will permanently destroy jail 'myjail' and all its data.
Are you sure? (yes/no): yes
Jail destroyed: myjail
```

Use `-y` to skip confirmation:
```bash
hospitus jail destroy myjail -y
```


## Next Steps

- [Working with Jails](../user-guide/jails.md) - Advanced jail configuration
- [Networking](../user-guide/networking.md) - Network configuration options
- [Storage & Snapshots](../user-guide/storage.md) - ZFS integration
- [UWM Manifests](../uwm/overview.md) - Declarative jail definitions
