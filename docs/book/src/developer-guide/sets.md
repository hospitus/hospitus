# Base Image Sets

In Hospitus, a **set** is a single root-filesystem archive — a FreeBSD `base.txz`,
or a Linux rootfs tarball — that becomes the userland of a **jail**. This is the
`set` *image category*, one of three the image catalog understands. This page
explains what a set really is in the code, how the jail provider consumes it, and
how to add a new architecture.

> **Terminology note.** In stock FreeBSD, "distribution sets" means the separate
> `base.txz`, `lib32.txz`, `kernel.txz`, `ports.txz`, and `src.txz` archives.
> Hospitus does **not** implement that multi-set model — it downloads and extracts
> `base.txz` only. "Set" in Hospitus means "a base rootfs image", not "one of the
> selectable FreeBSD distribution sets".

---

## The three image categories

The catalog in
[`pkg/image/catalog.go`](https://github.com/hospitus/hospitus/blob/main/pkg/image/catalog.go)
classifies every image into one `ImageCategory`:

```go
const (
    CategorySet   ImageCategory = "set"    // base-system rootfs archives (txz / tar.*) — jails
    CategoryISO   ImageCategory = "iso"    // installer discs — VMs
    CategoryCloud ImageCategory = "cloud"  // cloud images (qcow2/raw) — VMs
)
```

| Category | Formats | Consumed by | Stored under |
|----------|---------|-------------|--------------|
| `set` | `.txz`, `.tar.xz`, `.tar.gz`, `.tgz` | jail provider | `<imageDir>/sets/` |
| `iso` | `.iso` | bhyve / QEMU | `<imageDir>/iso/` |
| `cloud` | `.qcow2`, `.raw`, `.img`, `.vmdk` | bhyve / QEMU | `<imageDir>/cloud/` |

This page is about the `set` category. ISOs and cloud images are covered by the
[Images user guide](../user-guide/images.md).

The image directory defaults to `<data-dir>/images` and can be overridden with
the `HOSPITUS_IMAGE_DIR` environment variable.

---

## What is in a set

Every catalog entry is an `ImageProfile`:

```go
type ImageProfile struct {
    Name        string          // e.g. "freebsd-14.3-RELEASE-amd64"
    Display     string
    Description string
    Category    ImageCategory   // "set" here
    Format      ImageFormat     // txz, tar.xz, tar.gz, ...
    OSType      string          // "freebsd", "ubuntu", "alpine", ...
    OSVersion   string
    Arch        string          // amd64, arm64, riscv64, i386
    Providers   []ProviderType  // ["jail"] for sets
    URL         string
    SHA256      string
    Mirrors     []string
    // ... CloudInit, MinDiskGB, DefaultUser
}
```

The built-in catalog is embedded at build time from
[`pkg/image/catalog.json`](https://github.com/hospitus/hospitus/blob/main/pkg/image/catalog.json)
(`//go:embed catalog.json`). Its `set`-category entries include:

- **FreeBSD base sets** — `freebsd-14.3-RELEASE-amd64`, `-arm64`, `-riscv64`,
  `-i386`, plus `15.0-CURRENT` and `14-STABLE` snapshots.
- **Linux rootfs tarballs used as jail userlands** —
  `ubuntu-24.04-rootfs-amd64` (tar.xz), `alpine-3.20-rootfs-amd64` (tar.gz),
  `fedora-41-rootfs-amd64`, `debian-12-rootfs-amd64`. These require the FreeBSD
  Linux compatibility layer inside the jail.

All `set` profiles declare `Providers: ["jail"]`.

---

## Naming and selection

A set is referred to by its version/arch string, matching the catalog `Name`
minus the `freebsd-` prefix where applicable:

```bash
# Download a set into <imageDir>/sets/
hospitus image fetch 14.3-RELEASE-amd64

# Use it as a jail's base system
hospitus jail create web --image 14.3-RELEASE-amd64 --vnet --ip dhcp
```

In a [UWM manifest](../uwm/spec.md), a set is selected through the image
`source`, in `type:reference` form:

```toml
[image]
source = "freebsd:14.3-RELEASE"
arch   = "amd64"
```

There are **no** per-set flags (no `--lib32`, `--kernel`, `--ports`, `--src`) —
only the single base rootfs is fetched.

---

## How the jail provider consumes a set

The live code path is
[`pkg/provider/jail/image.go`](https://github.com/hospitus/hospitus/blob/main/pkg/provider/jail/image.go),
not the catalog. When a jail is created:

1. **Resolve the release** — `GetOSRelease(osType, osVersion, arch)` builds the
   download URL:
   ```
   https://download.freebsd.org/releases/<ARCH>/<VERSION>/base.txz
   ```
   For `CURRENT`/`STABLE` versions it uses the `snapshots/` path instead. Only
   `base.txz` is ever requested.
2. **Fetch the checksum** — `FetchManifestSHA256(arch, version)` downloads the
   FreeBSD `MANIFEST`, finds the `base.txz` line, and returns its SHA-256
   (best-effort; empty string if the network is unavailable).
3. **Download** — `DownloadBaseSystem` retrieves the archive with `fetch(1)`.
4. **Extract** — `extractBaseSystem` untars the archive into the jail root. It
   searches `<imageDir>/sets/` then `<imageDir>/`, trying the extensions
   `.txz .tar.gz .tar.xz .tar.bz2 .tgz` and auto-adding the `freebsd-` prefix and
   the `-amd64/-arm64/-i386/-riscv64` arch suffixes when matching a local file.

So a set downloaded once with `hospitus image fetch` is reused directly from
`<imageDir>/sets/` on subsequent jail creations — no re-download.

---

## Catalog refresh and discovery

`Catalog.Refresh(ctx)` keeps the catalog current from three sources:

1. **Remote catalog** — fetched from the repository's `catalog.json` on GitHub.
2. **Release probing** — `probeKnownFreeBSDReleases` issues `HEAD` requests against
   `download.freebsd.org` for known versions × architectures to discover which
   base sets actually exist upstream.
3. **Local cache** — the merged result is written to
   `<data-dir>/config/catalog.json` and reused on the next start.

`ListDownloaded()` scans the `sets/`, `iso/`, and `cloud/` subdirectories (plus a
legacy flat layout) and skips `.partial` files from interrupted downloads.

---

## Adding a new architecture

To support a new CPU architecture for jails (for example RISC-V `riscv64`):

1. **Catalog** — add `set`-category entries to `pkg/image/catalog.json` with the
   correct FreeBSD download URLs and (optionally) SHA-256 hashes for the new arch.
2. **QEMU user-mode mapping** — add the architecture to `qemuArchMap` in
   [`pkg/provider/jail/crossarch.go`](https://github.com/hospitus/hospitus/blob/main/pkg/provider/jail/crossarch.go),
   which maps a target arch to its `qemu-<arch>-static` binary name.
3. **binmiscctl** — register the ELF magic bytes for the new architecture in
   [`tools/setup-binmiscctl.sh`](https://github.com/hospitus/hospitus/blob/main/tools/setup-binmiscctl.sh)
   so the kernel routes foreign binaries to the QEMU interpreter.
4. **Test** — run the cross-architecture integration suite:
   ```bash
   HOSPITUS_CROSSARCH_TESTS=1 doas go test -v ./test/integration/... -run TestCrossArch
   ```

See [Testing](testing.md) for the prerequisites (`qemu-user-static`, the
`imgact_binmisc` kernel module) and how to run the cross-architecture suite.

---

## See also

- [Images (user guide)](../user-guide/images.md)
- [ZFS Deep Dive](../guides/zfs-deep-dive.md)
- [The Provider Interface](providers.md)
