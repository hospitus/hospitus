# HOSPITUS Manifest Examples

This directory contains a collection of example manifest templates for various workloads, categorized by type.

## Structure

Every manifest is organized as follows:
`manifests/<category>/<workload>/template.toml`

Each is rendered, parsed and validated by `pkg/manifest/examples_test.go`, and
the integration suite deploys them against a real FreeBSD host.

## Quick Start

```bash
# Apply a template
hospitus apply --start manifests/jails/linux-ubuntu/template.toml

# Apply with custom variables
hospitus apply --start --var name=my-webserver manifests/containers/caddy/template.toml
```

## Available Templates

Twelve manifests, one directory each, all of them deployed and health-checked
by `pkg/manifest/examples_test.go` and the integration suite. The index used to
carry a hundred entries for a catalog that was never shipped: ninety-two of
its links pointed at directories that do not exist.

### Jails
| Workload | Description |
|----------|-------------|
| [freebsd-aarch64](manifests/jails/freebsd-aarch64/) | FreeBSD jail running ARM64 binaries under emulation |
| [freebsd-riscv64](manifests/jails/freebsd-riscv64/) | FreeBSD jail running RISC-V binaries under emulation |
| [linux-ubuntu](manifests/jails/linux-ubuntu/) | Ubuntu userland in a jail, serving over Apache |

### Virtual Machines
| Workload | Description |
|----------|-------------|
| [freebsd](manifests/vms/freebsd/) | FreeBSD 14.3 server with nuageinit provisioning |
| [gpu-passthrough](manifests/vms/gpu-passthrough/) | Debian 12 VM owning a PCI graphics card |
| [windows-physical-disk](manifests/vms/windows-physical-disk/) | Windows VM booting from a whole passed-through disk |

### Containers (Podman)
| Workload | Description |
|----------|-------------|
| [caddy](manifests/containers/caddy/) | Caddy serving static files from a Podman container |

### Developer Tools
| Workload | Description |
|----------|-------------|
| [ci-runner](manifests/dev-tools/ci-runner/) | Self-hosted CI/CD runner with Docker executor |

### Media
| Workload | Description |
|----------|-------------|
| [navidrome](manifests/media/navidrome/) | Music streaming server over a read-only library |

### Web Hosting
| Workload | Description |
|----------|-------------|
| [wordpress](manifests/web-hosting/wordpress/) | WordPress and MariaDB in two jails on a private segment |

### Stacks
| Workload | Description |
|----------|-------------|
| [nextcloud](manifests/stacks/nextcloud/) | Nextcloud split across four jails, one tier each |
| [qemu](manifests/stacks/qemu/) | Three QEMU VMs: database, backend, frontend |

## Testing

To deploy every manifest and run the health check each one declares:

```sh
doas make test-manifests
```

To run just one:

```sh
doas go test -v -run 'TestExampleManifests/caddy' ./test/integration/...
```
