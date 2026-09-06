# Glossary

## A

### Anchor (PF)
A named collection of PF rules that can be loaded and manipulated independently from the main ruleset. Hospitus uses the `hospitus` anchor for its firewall rules.

### API (Application Programming Interface)
The HTTP REST interface that Hospitus exposes for programmatic control. All CLI commands communicate with the daemon via this API.

### API Key
The credential used to authenticate to the Hospitus API. Keys are supplied to the daemon with `--api-key-file`, `--api-key`, or the `HOSPITUS_API_KEY` environment variable, hashed with bcrypt in memory, and presented by clients in the `X-API-Key` header.

### ARC (Adaptive Replacement Cache)
ZFS's intelligent caching system that stores frequently accessed data in RAM for faster retrieval.

## B

### Base System
The core FreeBSD operating system files required to run a jail. Downloaded from FreeBSD mirrors as `base.txz`.

### bhyve
FreeBSD's native Type-2 hypervisor for running virtual machines. Supports FreeBSD, Linux, and Windows guests.

### Bridge
A network device that connects multiple network segments at the data link layer. Hospitus uses bridges (e.g., `hospitus0`) to connect jail VNET interfaces.

## C

### CIDR (Classless Inter-Domain Routing)
IP address notation that includes network prefix length, e.g., `10.0.0.0/24` represents addresses 10.0.0.0 through 10.0.0.255.

### Clone
A copy of a ZFS dataset or snapshot that shares blocks with the original, making it space-efficient. Used for quickly creating new jails from existing ones.

### Console
Interactive terminal access to a jail or VM. In jails, provided via `jexec`; in VMs, typically via serial or VNC.

### Container
A lightweight, isolated execution environment. In Hospitus, containers are managed via the Podman provider on FreeBSD (the primary platform) and macOS.

### Context (CLI)
A named connection profile in the CLI config file (`~/.config/hospitus/config.yaml`) that stores a daemon URL, API key, and TLS settings. Managed with `hospitus context` and selected with `hospitus context use`, so one workstation can target several daemons.

### Copy-on-Write (COW)
ZFS feature where modifications create new blocks rather than overwriting existing ones, enabling efficient snapshots and clones.

## D

### Daemon
A background process that runs continuously. `hospitusd` is the Hospitus daemon that provides the API server.

### Dataset
A ZFS filesystem or volume. Jails are stored in datasets like `zroot/hospitus/jails/myjail`.

### DHCP (Dynamic Host Configuration Protocol)
Protocol for automatic IP address assignment. Hospitus's "DHCP mode" allocates IPs from configured pools, though it's not a true DHCP server.

## E

### epair
FreeBSD virtual Ethernet interface pair. One end (`epair0a`) stays on the host; the other (`epair0b`) is moved into a VNET jail.

### Exec
Executing a command inside a running jail or container without entering a full shell.

## F

### Fail-closed authentication
Hospitus's default posture: if the daemon starts with no API keys and without `--allow-no-auth`, it rejects every request rather than serving them unauthenticated.

### Firewall
System for controlling network traffic. Hospitus uses PF on FreeBSD and manages rules via anchors.

### FreeBSD
A Unix-like operating system. Hospitus's primary platform, providing jails and bhyve.

## G

### Guest
The operating system running inside a VM or jail.

## H

### Handle
An identifier that uniquely references a Hospitus instance, consisting of provider name and instance ID.

### Host
The physical or primary system running Hospitus and its workloads.

### Hypervisor
Software that creates and manages virtual machines. bhyve is FreeBSD's native hypervisor.

## I

### Image
A template for creating instances. For jails, typically a FreeBSD base system; for VMs, a disk image; for containers, an OCI image.

### Instance
A running or stopped workload managed by Hospitus—a jail, VM, or container.

### IP Pool
A range of IP addresses available for automatic allocation to instances.

## J

### Jail
FreeBSD's OS-level virtualization technology. Provides isolated environments sharing the host kernel.

### jexec
FreeBSD command to execute commands inside a running jail.

### jls
FreeBSD command to list running jails.

### Job
A long-running operation (instance creation, image fetch, backup) that the daemon runs asynchronously in a worker pool. Submitted operations return a job ID immediately; progress and result are polled with `hospitus job info <id>`.

## K

### Kernel
The core of the operating system. Jails share the host kernel; VMs have their own.

## L

### Lifecycle
The states an instance goes through: created → started → running → stopped → destroyed.

### Loader.conf
FreeBSD boot configuration file (`/boot/loader.conf`) for loading kernel modules and setting tunables.

## M

### Manifest
A TOML file describing a Hospitus workload or stack. See UWM.

### Metrics endpoint
The daemon's monitoring surface. `GET /metrics` returns structured JSON and `GET /metrics/prometheus` returns Prometheus exposition format; both require an API key unless the daemon was started with `--metrics-public`.

### Migration
In this manual, the process of moving workloads from another management tool (such as CBSD or Bastille) onto Hospitus — see [Migration from Other Tools](migration.md). Live migration of a running instance between hosts is not a feature.

## N

### NAT (Network Address Translation)
Modifying IP addresses in packet headers, typically to allow private network addresses to access the internet. Hospitus uses PF NAT for jail internet access.

### Hospitus
This project—a multi-platform virtualization management system.

### hospitusd
The Hospitus daemon—the API server that manages instances.

## O

### OCI (Open Container Initiative)
Standards for container formats and runtimes. Podman is OCI-compliant.

## P

### PF (Packet Filter)
FreeBSD's firewall system. Hospitus uses PF for NAT and port forwarding.

### Podman
A daemonless, Docker-compatible container engine. Hospitus's container provider, driven on FreeBSD (the primary platform) and macOS.

### Pool (ZFS)
A collection of storage devices managed by ZFS as a single entity.

### Port Forwarding
Redirecting network traffic from one port to another, typically from the host to a jail.

### Provider
A Hospitus plugin that implements a virtualization backend: jail, bhyve, qemu and podman, plus the experimental macOS-only vfkit and Apple container backends.

## Q

### QEMU
An open-source machine emulator and virtualizer. Hospitus uses QEMU for cross-architecture support and VM emulation.

### Quota
A limit on storage usage, enforced by ZFS at the dataset level.

## R

### RCTL (Resource Control)
FreeBSD kernel feature for setting resource limits on jails and processes.

### RDR (Redirect)
PF rule type for redirecting incoming connections to a different address/port.

### REST (Representational State Transfer)
Architectural style for web APIs. Hospitus provides a REST API.

### Root Filesystem
The base filesystem of a jail, containing the operating system and applications.

## S

### Snapshot
A point-in-time copy of a ZFS dataset. Space-efficient due to copy-on-write.

### Stack
A collection of related Hospitus instances defined in a single manifest with dependency management.

### State
The current status of an instance: running, stopped, creating, etc.

### Static IP
A manually assigned IP address that doesn't change.

## T

### TOML (Tom's Obvious Minimal Language)
A configuration file format used by Hospitus manifests.

### TLS (Transport Layer Security)
Cryptographic protocol for secure network communication.

### Type-2 Hypervisor
A hypervisor that runs on top of a host operating system (like bhyve), as opposed to Type-1 hypervisors that run directly on hardware.

## U

### UWM (Unified Workload Manifest)
Hospitus's declarative configuration format for defining workloads in TOML.

## V

### VM (Virtual Machine)
A complete emulated computer running its own operating system and kernel.

### VNET
FreeBSD virtual network stack that gives a jail its own network interfaces, routing tables, and firewall rules.

### Volume
Additional storage attached to an instance, separate from the root filesystem.

## Z

### ZFS (Zettabyte File System)
Advanced filesystem used by Hospitus for storage. Provides snapshots, clones, compression, and data integrity features.

### zvol
A ZFS volume—a block device backed by ZFS. Used for VM disk images.

## See Also

- [FAQ](../troubleshooting/faq.md)
- [UWM Specification](../uwm/spec.md)
