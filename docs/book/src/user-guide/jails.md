# Hospitus Jail User Guide

Managing FreeBSD jails with Hospitus, worked through as scenarios: a web server, a PostgreSQL database, a development environment.

---

## Table of Contents

- [Introduction](#introduction)
- [Scenario 1: Building Your First Web Server](#scenario-1-building-your-first-web-server)
- [Scenario 2: Database Server with PostgreSQL](#scenario-2-database-server-with-postgresql)
- [Scenario 3: Development Environment](#scenario-3-development-environment)
- [Scenario 4: Multi-Tier Application Stack](#scenario-4-multi-tier-application-stack)
- [Scenario 5: Snapshots, Clones and Disaster Recovery](#scenario-5-snapshots-clones-and-disaster-recovery)
- [Scenario 6: Production Deployment with Auto-Start](#scenario-6-production-deployment-with-auto-start)
- [Scenario 7: Cross-Architecture Jails](#scenario-7-cross-architecture-jails)
- [Scenario 8: Monitoring and Health Checks](#scenario-8-monitoring-and-health-checks)
- [Scenario 9: Tmux Sessions for Persistent Access](#scenario-9-tmux-sessions-for-persistent-access)
- [Command Reference Summary](#command-reference-summary)

---

## Introduction

Hospitus manages FreeBSD jails with ZFS storage, VNET networking, and resource limits via RCTL.

### Prerequisites

- FreeBSD 14.0 or later with ZFS
- Hospitus daemon (`hospitusd`) running
- Root or wheel group membership (most commands require `doas` or root)

### Managing Base Images

Before creating jails, you need base system images. Hospitus provides the `hospitus image` command to manage images from a built-in catalog. Images are stored in `/var/lib/hospitus/images/`.

#### View Available Images

```bash
# List all available images in the catalog
hospitus image available

# Filter by category (set = jail archives, iso = ISO images, cloud = VM images)
hospitus image available --category set

# Filter by provider (jail, bhyve, qemu)
hospitus image available --provider jail

# Filter by OS (freebsd, linux, openbsd, netbsd)
hospitus image available --os freebsd

# Filter Linux jail images only
hospitus image available --os linux --category set
```

#### Download Images

```bash
# Download a FreeBSD base image
hospitus image fetch freebsd-14.3-RELEASE-amd64

# Download a Linux rootfs for Linux jails
hospitus image fetch ubuntu-24.04-rootfs-amd64
hospitus image fetch alpine-3.20-rootfs-amd64
```

#### Manage Downloaded Images

```bash
# List downloaded images
hospitus image list

# Filter by category
hospitus image list --category set

# Delete an image you no longer need
hospitus image delete 14.3-RELEASE-amd64.txz
```

#### Supported Image Types

Hospitus supports three categories of images:

| Category | Format | Use Case | Providers |
|----------|--------|----------|-----------|
| `set` | .txz, .tar.xz, .tar.gz | Base system archives for jails | jail |
| `iso` | .iso | Installation images for VMs | bhyve, qemu |
| `cloud` | .qcow2, .raw, .img | Pre-built VM images | bhyve, qemu |

#### Available Jail Images

**FreeBSD:**
- `freebsd-14.3-RELEASE-amd64` - FreeBSD 14.3 (recommended)
- `freebsd-14.2-RELEASE-amd64` - FreeBSD 14.2 (previous stable)
- `freebsd-13.4-RELEASE-amd64` - FreeBSD 13.4
- `freebsd-14.3-RELEASE-arm64` - FreeBSD 14.3 for ARM64
- `freebsd-14.3-RELEASE-riscv64` - FreeBSD 14.3 for RISC-V

**Linux (via linux64 ABI):**
- `ubuntu-24.04-rootfs-amd64` - Ubuntu 24.04 LTS
- `ubuntu-22.04-rootfs-amd64` - Ubuntu 22.04 LTS
- `alpine-3.20-rootfs-amd64` - Alpine Linux 3.20 (minimal)
- `fedora-41-rootfs-amd64` - Fedora 41
- `debian-12-rootfs-amd64` - Debian 12 Bookworm

For complete documentation, see the [Image Management Guide](images.md).

### Ensure Hospitusd is Running

Before starting, verify the Hospitus daemon is running:

```bash
# Check if the daemon is running (the rc.d service is named "hospitus")
service hospitus status

# If not running, start it
doas service hospitus start
```

### Command Flag Ordering for `exec`

The `exec` command is designed to pass flags to commands inside the jail. This means Hospitus flags (`-u`, `-w`, `-i`) must come **before** the jail name:

```bash
# Correct: Hospitus flags before jail name
doas hospitus jail exec -u www -w /var/www myjail whoami

# This allows command flags to pass through to the jail command:
doas hospitus jail exec myjail env IGNORE_OSVERSION=yes ASSUME_ALWAYS_YES=yes pkg install -y nginx   # -y goes to pkg
doas hospitus jail exec myjail ls -la /var/log        # -la goes to ls
```

---

## Scenario 1: Building Your First Web Server

**Goal**: Create an isolated web server jail running nginx, expose port 80 to the internet, and manage its lifecycle.

### Step 1: Create the Jail

Create a jail for the web server with VNET networking, which gives it its own network stack:

```bash
# Create a jail with VNET networking and automatic IP assignment
hospitus jail create webserver \
    --image 14.3-RELEASE-amd64 \
    --cpus 2 \
    --memory 1024 \
    --vnet \
    --bridge hospitus0 \
    --ip dhcp \
    --description "Production web server running nginx"
```

**What happens**: Hospitus creates a new ZFS dataset, extracts the FreeBSD base system, configures VNET networking with an epair interface, and assigns an IP address from the configured pool.

**Output**:
```
Creating jail webserver...
Jail created: webserver (ID: webserver)
```

### Step 2: Start the Jail

```bash
hospitus jail start webserver
```

**Output**:
```
Jail started: webserver
```

### Step 3: Verify the Jail is Running

Use the `list` command to see all jails and their status:

```bash
hospitus jail list
```

**Output**:
```
NAME        STATE     IP ADDRESS      CPUs   MEMORY   PROVIDER   CREATED
webserver   running   10.0.0.2/24     2      1024MB   jail       2024-01-15 10:30
```

For detailed information about our new jail:

```bash
hospitus jail info webserver
```

**Output**:
```
Name:         webserver
State:        running
Provider:     jail
Description:  Production web server running nginx

Resources:
  CPUs:       2
  Memory:     1024 MB

Network:
  Type:       bridge
  Bridge:     hospitus0
  IPv4:       10.0.0.2/24

Image:        14.3-RELEASE-amd64
OS:           freebsd
ZFS Dataset:  zroot/hospitus/jails/webserver

Created:      2024-01-15T10:30:00+01:00
```

### Step 4: Install nginx Inside the Jail

Run commands inside the jail with `exec`:

> **Note**: `hospitus jail exec -i` (interactive) needs root on the client, because
> it attaches a terminal through `jexec(8)` locally. A plain `exec` goes through
> the daemon like any other command and needs no privilege of its own.

> **Note**: Fresh jails don't have the `pkg` tool bootstrapped. The first `pkg` command will automatically install it when run interactively. For non-interactive installation (like through `hospitus jail exec`), you need to set the `ASSUME_ALWAYS_YES` environment variable.

```bash
# Bootstrap pkg (first time only - required for fresh jails)
doas hospitus jail exec webserver env ASSUME_ALWAYS_YES=yes pkg bootstrap

# Update package repository
doas hospitus jail exec webserver pkg update

# Install nginx
doas hospitus jail exec webserver env IGNORE_OSVERSION=yes ASSUME_ALWAYS_YES=yes pkg install -y nginx

# Enable nginx to start on jail boot
doas hospitus jail exec webserver sysrc ngihsp_enable=YES
```

If `pkg update` stops on a version mismatch, add `IGNORE_OSVERSION=yes` to the
`env` above: `FreeBSD:14:amd64` is built against the newest supported 14
release, so its packages can carry a higher `__FreeBSD_version` than the image
the jail was created from.

#### Option A: nginx on Port 8080 (Recommended for Jails)

By default, jails cannot bind to privileged ports (< 1024). The simplest solution is to configure nginx to listen on port 8080:

```bash
# Configure nginx to listen on port 8080
doas hospitus jail exec webserver sed -i '' 's/listen       80;/listen       8080;/' \
    /usr/local/etc/nginx/nginx.conf

# Start nginx
doas hospitus jail exec webserver service nginx start
```

Then use port forwarding to expose port 80 on the host to port 8080 in the jail (see Step 5).

#### Option B: nginx on Port 80 (Requires --allow-reserved-ports)

If you need nginx to bind directly to port 80, the jail needs
`--allow-reserved-ports`. That is a create-time flag, so this is a choice made
back at Step 1 rather than something to apply to the `webserver` jail now — a
second `create webserver` fails on the name. Creating it that way instead:

```bash
# Instead of the Step 1 create, with reserved ports permission
hospitus jail create webserver \
    --image 14.3-RELEASE-amd64 \
    --cpus 2 \
    --memory 1024 \
    --vnet \
    --allow-reserved-ports \
    --description "Web server with reserved port access"

# nginx can then bind to port 80 directly, with its config left as shipped
doas hospitus jail exec webserver service nginx start
```

To switch an existing jail over, destroy and re-create it:

```bash
hospitus jail destroy -y webserver
```

### Step 5: Configure Port Forwarding

To make the web server accessible from outside, configure port forwarding. The command depends on which option you chose above:

#### For Option A (nginx on port 8080):

```bash
# Forward host port 80 to jail port 8080
doas hospitus jail expose add webserver --port 80:8080

# Verify the port forwarding rule
hospitus jail expose list webserver
```

**Output**:
```
Port forwarding rules for jail webserver:

PROTOCOL  HOST PORT  TARGET PORT  TARGET IP
tcp       80         8080         10.0.0.2
```

#### For Option B (nginx on port 80 with --allow-reserved-ports):

```bash
# Forward host port 80 to jail port 80
doas hospitus jail expose add webserver --port 80

# Verify the port forwarding rule
hospitus jail expose list webserver
```

**Output**:
```
Port forwarding rules for jail webserver:

PROTOCOL  HOST PORT  TARGET PORT  TARGET IP
tcp       80         80           10.0.0.2
```

Your web server is now accessible at `http://your-host-ip/`.

### Step 6: Access the Console for Debugging

When you need an interactive shell inside the jail:

```bash
doas hospitus jail console webserver
```

**Output**:
```
Attaching to jail webserver with /bin/sh
Type 'exit' to detach from the console.

root@webserver:/ #
```

You're now inside the jail. Check nginx is running:

```bash
root@webserver:/ # service nginx status
nginx is running as pid 1234.
root@webserver:/ # exit
```

### Step 7: Graceful Restart After Configuration Changes

After modifying nginx configuration:

```bash
# Edit nginx config interactively (requires -i flag for TTY)
doas hospitus jail exec -i webserver vi /usr/local/etc/nginx/nginx.conf

# Or use sed for non-interactive edits (better for scripts)
doas hospitus jail exec webserver sed -i '' 's/old_value/new_value/' /usr/local/etc/nginx/nginx.conf

# Restart the entire jail (useful when system-level changes are needed)
hospitus jail restart webserver
```

**Output**:
```
Jail restarted: webserver
```

### Step 8: Stop the Web Server (Maintenance)

For planned maintenance:

```bash
# Graceful stop (waits for processes to exit)
hospitus jail stop webserver

# Verify it stopped
hospitus jail list
```

**Output**:
```
Jail stopped: webserver
```

---

## Scenario 2: Database Server with PostgreSQL

**Goal**: Create a secure PostgreSQL database server that requires System V IPC, with proper resource limits.

### Step 1: Create a Jail with PostgreSQL Requirements

PostgreSQL requires `sysvipc` for shared memory. We also want memory locking for performance:

```bash
hospitus jail create postgres \
    --image 14.3-RELEASE-amd64 \
    --cpus 4 \
    --memory 4096 \
    --vnet \
    --bridge hospitus0 \
    --ip 10.50.0.10/24 \
    --allow-sysvipc \
    --allow-mlock \
    --description "PostgreSQL 16 database server"
```

**Key flags explained**:
- `--allow-sysvipc`: Enables System V IPC (shared memory, semaphores) required by PostgreSQL
- `--allow-mlock`: Allows memory locking for better database performance
- `--ip 10.50.0.10/24`: Static IP for reliable database connections

### Step 2: Start and Install PostgreSQL

```bash
# Start the jail
hospitus jail start postgres

# Bootstrap pkg (first time only - required for fresh jails)
doas hospitus jail exec postgres env ASSUME_ALWAYS_YES=yes pkg bootstrap

# Install PostgreSQL
doas hospitus jail exec postgres env IGNORE_OSVERSION=yes ASSUME_ALWAYS_YES=yes pkg install -y postgresql16-server

# Enable PostgreSQL
doas hospitus jail exec postgres sysrc postgresql_enable=YES

# Initialize the database cluster
doas hospitus jail exec postgres service postgresql initdb

# Start PostgreSQL
doas hospitus jail exec postgres service postgresql start
```

### Step 3: Verify PostgreSQL is Running

```bash
# Check status
doas hospitus jail exec postgres service postgresql status

# Connect to PostgreSQL as the postgres user
# Note: The -u flag must come BEFORE the jail name
doas hospitus jail exec -u postgres postgres psql -c "SELECT version();"
```

**Output**:
```
                                                       version
-----------------------------------------------------------------------------------------
 PostgreSQL 16.11 on amd64-portbld-freebsd14.3, compiled by clang version 19.1.7, 64-bit
(1 row)
```

### Step 3b: Create a Database and User

```bash
# Create a database
doas hospitus jail exec -u postgres postgres createdb myapp

# Create a user with password
doas hospitus jail exec -u postgres postgres psql -c "CREATE USER myappuser WITH PASSWORD 'secret';"

# Grant privileges
doas hospitus jail exec -u postgres postgres psql -c "GRANT ALL PRIVILEGES ON DATABASE myapp TO myappuser;"

# List databases
doas hospitus jail exec -u postgres postgres psql -c "\l"
```

### Step 3c: Connect to the Database

PostgreSQL runs inside the jail. There are several ways to connect:

**Option 1: Connect from inside the jail (recommended for admin tasks)**
```bash
# As postgres user
doas hospitus jail exec -u postgres postgres psql -d myapp

# As myappuser. exec is non-interactive, so there is no prompt to answer:
# supply the password through the environment instead of psql -W.
doas hospitus jail exec postgres env PGPASSWORD=secret psql -d myapp -U myappuser
```

**Option 2: Connect via TCP from the host**

First, configure PostgreSQL to accept TCP connections:

```bash
# Edit postgresql.conf to listen on all interfaces
doas hospitus jail exec postgres sed -i '' "s/#listen_addresses = 'localhost'/listen_addresses = '*'/" \
    /var/db/postgres/data16/postgresql.conf

# Edit pg_hba.conf to allow connections from host network
doas hospitus jail exec postgres sh -c "echo 'host all all 10.0.0.0/8 md5' >> /var/db/postgres/data16/pg_hba.conf"

# Restart PostgreSQL to apply changes
doas hospitus jail exec postgres service postgresql restart
```

Then connect from the host using the jail's IP address:
```bash
# Get the jail's IP address
hospitus jail info postgres | grep IPv4

# Connect via TCP (assuming jail IP is 10.50.0.10)
psql -h 10.50.0.10 -d myapp -U myappuser
```

**Option 3: Expose port to localhost via port forwarding**

```bash
# Add port forwarding from host to jail
doas hospitus jail expose add postgres --port 5432

# List current port forwarding
hospitus jail expose list postgres

# Now you can connect via localhost
psql -h localhost -p 5432 -d myapp -U myappuser
```

### Step 4: Detailed Status Check

```bash
# Get full information including resources
hospitus jail info postgres --output json
```

**Output** (JSON format):
```json
{
  "name": "postgres",
  "state": "running",
  "provider": "jail",
  "spec": {
    "cpus": 4,
    "memory_mb": 4096,
    "networks": [
      {
        "type": "bridge",
        "bridge": "hospitus0",
        "ipv4": "10.50.0.10/24"
      }
    ],
    "provider_config": {
      "allow.mlock": true,
      "allow.sysvipc": true,
      "vnet": true
    }
  }
}
```

---

## Scenario 3: Development Environment

**Goal**: Create a development jail with maximum flexibility for testing, including ZFS delegation and raw sockets for network debugging.

### Step 1: Create a Developer-Friendly Jail

```bash
hospitus jail create devbox \
    --image 14.3-RELEASE-amd64 \
    --cpus 2 \
    --memory 2048 \
    --vnet \
    --ip dhcp \
    --allow-raw-sockets \
    --allow-mount \
    --allow-mount-nullfs \
    --allow-mount-tmpfs \
    --allow-mount-zfs \
    --children-max 10 \
    --persist \
    --description "Development environment with full capabilities"
```

**Key flags for development**:
- `--allow-raw-sockets`: Enables `ping`, `traceroute`, and network debugging tools
- `--allow-mount*`: Allows mounting various filesystems for testing
- `--allow-mount-zfs`: Enables mounting ZFS datasets inside the jail
- `--children-max 10`: Allows creating nested jails for testing
- `--persist`: Keeps jail running even if all processes exit (prevents accidental shutdown)

### Step 2: Start and Access

```bash
hospitus jail start devbox

# Open an interactive shell using csh
doas hospitus jail console devbox --shell /bin/csh
```

### Step 3: Test Network Tools

Inside the jail, verify raw sockets work:

```bash
root@devbox:/ # ping -c 3 google.com
PING google.com (142.250.x.x): 56 data bytes
64 bytes from 142.250.x.x: icmp_seq=0 ttl=117 time=12.3 ms
...

root@devbox:/ # traceroute google.com
traceroute to google.com (142.250.x.x), 64 hops max, 40 byte packets
 1  10.50.0.1  0.234 ms  0.198 ms  0.187 ms
...
```

### Step 4: Run Commands with Specific User

Execute commands as a non-root user:

```bash
# Create a developer user first
doas hospitus jail exec devbox pw useradd developer -m -s /bin/sh

# Run commands as the developer user (note: -u flag BEFORE jail name)
doas hospitus jail exec -u developer devbox whoami
```

**Output**:
```
developer
```

```bash
# Run a shell as the developer user
doas hospitus jail exec -u developer devbox id
```

**Output**:
```
uid=1001(developer) gid=1001(developer) groups=1001(developer)
```

### Step 5: Run Commands in Specific Directory

```bash
# Run a command in a specific working directory (note: -w flag BEFORE jail name)
doas hospitus jail exec -w /var/log devbox ls -la

# Combine user and working directory flags
doas hospitus jail exec -u developer -w /home/developer devbox pwd
```

**Output**:
```
/home/developer
```

---

## Scenario 4: Multi-Tier Application Stack

**Goal**: Deploy a complete web application with separate jails for web, application, and database tiers, using boot priorities for ordered startup.

### Step 1: Create the Database Tier

```bash
hospitus jail create app-db \
    --image 14.3-RELEASE-amd64 \
    --cpus 2 \
    --memory 2048 \
    --vnet \
    --ip 10.50.0.20/24 \
    --allow-sysvipc \
    --auto-start \
    --auto-start-priority 10 \
    --description "Application database tier"
```

**Key configuration**:
- `--allow-sysvipc`: Required for PostgreSQL and other databases that use System V IPC
- `--auto-start-priority 10`: Lowest priority number starts first during boot

### Step 2: Create the Application Tier

```bash
hospitus jail create app-backend \
    --image 14.3-RELEASE-amd64 \
    --cpus 4 \
    --memory 4096 \
    --vnet \
    --ip 10.50.0.21/24 \
    --auto-start \
    --auto-start-priority 20 \
    --auto-start-delay 5000 \
    --description "Application backend tier"
```

**Key configuration**:
- `--auto-start-priority 20`: Starts after database (priority 10)
- `--auto-start-delay 5000`: Waits 5 seconds after previous tier starts, giving database time to initialize

### Step 3: Create the Web Tier

```bash
hospitus jail create app-web \
    --image 14.3-RELEASE-amd64 \
    --cpus 2 \
    --memory 1024 \
    --vnet \
    --ip 10.50.0.22/24 \
    --auto-start \
    --auto-start-priority 30 \
    --auto-start-delay 3000 \
    --description "Application web tier (nginx reverse proxy)"
```

**Key configuration**:
- `--auto-start-priority 30`: Starts last, after database and backend are ready
- `--auto-start-delay 3000`: Waits 3 seconds for backend to be ready

### Step 4: Start the Entire Stack

```bash
# Start in order (Hospitus respects priorities if using --auto-start)
hospitus jail start app-db
hospitus jail start app-backend
hospitus jail start app-web

# View all jails
hospitus jail list
```

**Output**:
```
NAME          STATE     IP ADDRESS      CPUs   MEMORY   PROVIDER   CREATED
app-db        running   10.50.0.20/24   2      2048MB   jail       2024-01-15 10:30
app-backend   running   10.50.0.21/24   4      4096MB   jail       2024-01-15 10:31
app-web       running   10.50.0.22/24   2      1024MB   jail       2024-01-15 10:32
```

### Step 5: Expose the Web Frontend

```bash
# Expose HTTP
doas hospitus jail expose add app-web --port 80:80

# Verify
hospitus jail expose list app-web
```

### Step 6: Verify Inter-Jail Communication

```bash
# Verify the jails can communicate with each other
doas hospitus jail exec app-web ping -c 2 10.50.0.21  # ping backend
doas hospitus jail exec app-backend ping -c 2 10.50.0.20  # ping database

# Verify external access
doas hospitus jail exec app-web ping -c 2 8.8.8.8
```

---

## Scenario 5: Snapshots, Clones and Disaster Recovery

**Goal**: Use ZFS snapshots for safe rollback points, efficiently clone jails for testing, and export/import jails for backup and disaster recovery.

This scenario covers the complete data protection workflow:
1. **Snapshots**: Instant rollback points before risky operations
2. **Clones**: Space-efficient copies for testing
3. **Export/Import**: Full backups for disaster recovery

### Part 1: Working with Snapshots

#### Step 1: Create a Snapshot Before Risky Operations

Before upgrading packages or making major changes, create a snapshot:

```bash
# Create a snapshot with a meaningful name
hospitus jail snapshot create app-db before-upgrade
```

**Output**:
```
Creating snapshot app-db@before-upgrade...
Snapshot created: app-db@before-upgrade
```

**What happens**: Hospitus creates an instant ZFS snapshot. Snapshots initially take no space—they only grow as the original data changes.

#### Step 2: List Available Snapshots

```bash
# View all snapshots for a jail
hospitus jail snapshot list app-db
```

**Output**:
```
SNAPSHOT        CREATED              SIZE
before-upgrade  2024-12-08 10:30:00  0B
daily-backup    2024-12-07 03:00:00  128MB
```

#### Step 3: Perform the Risky Operation

Now safely upgrade packages knowing you can rollback:

```bash
# Upgrade PostgreSQL
doas hospitus jail exec app-db pkg upgrade -y postgresql16-server

# If something goes wrong... you can restore
```

#### Step 4: Restore from Snapshot if Needed

If the upgrade breaks something:

```bash
# Stop the jail first (required for restore)
hospitus jail stop app-db

# Restore to the snapshot
hospitus jail snapshot restore app-db before-upgrade
```

**Output**:
```
Restoring jail app-db from snapshot before-upgrade...
WARNING: This will discard all changes made after the snapshot.
Snapshot restored: app-db rolled back to before-upgrade
```

```bash
# Start the jail again
hospitus jail start app-db
```

**Important**: Restoring a snapshot discards all changes made after it was created.

#### Step 5: Clean Up Old Snapshots

Remove snapshots you no longer need:

```bash
# Delete a specific snapshot
hospitus jail snapshot delete app-db old-backup
```

**Output**:
```
Deleting snapshot app-db@old-backup...
Snapshot deleted: app-db@old-backup
```

### Part 2: Working with Clones

Clones are space-efficient copies of jails, perfect for testing changes without affecting production.

#### Step 6: Clone a Jail for Testing

Create an instant copy of a production jail:

```bash
# Jail must be stopped before cloning current state
hospitus jail stop app-db
# Clone the current state
hospitus jail clone app-db app-db-test
# Restart the original jail if needed
hospitus jail start app-db
```

**Output**:
```
Cloning jail app-db to app-db-test...
Clone created: app-db-test
Instance app-db cloned successfully as app-db-test
Warning: app-db-test inherits the address 10.50.0.20/24 from its source.
  Running both at once conflicts. To give the clone another address:
    hospitus jail network remove app-db-test <interface>
    hospitus jail network add app-db-test --bridge <bridge> --ipv4 <address>
```

A clone is a copy of the source, address included, so the two cannot run at the
same time until one of them is given another. The commands above do that.

**What happens**: Hospitus uses ZFS clone which shares data with the source via copy-on-write. The clone initially consumes no additional space.

#### Step 7: Clone from a Specific Snapshot

Clone from a known-good state instead of the current state. Unlike cloning the current state, cloning from a snapshot does **not** require stopping the jail:

```bash
# Clone from snapshot instead of current state (jail can be running)
hospitus jail clone app-db app-db-staging --snapshot before-upgrade
```

**Output**:
```
Cloning jail app-db from snapshot before-upgrade to app-db-staging...
Clone created: app-db-staging
Snapshot before-upgrade cloned successfully as app-db-staging
```

#### Step 8: Start and Use the Clone

```bash
# Start the cloned jail
hospitus jail start app-db-test

# Verify it's running
hospitus jail list
```

**Output**:
```
NAME           STATE     IP ADDRESS      CPUs   MEMORY   PROVIDER   CREATED
app-db         running   10.50.0.20/24   2      2048MB   jail       2024-12-08 10:30
app-db-test    running   10.50.0.20/24   2      2048MB   jail       2024-12-08 11:00
```

Both show the same address, because a clone copies the spec of its source
unchanged. Nothing stops the two from running at once, and nothing reports it
either — traffic goes to whichever answered the last ARP request, and the only
trace is a line in the kernel log:

```
arp: 58:9c:fc:10:0a:e0 is using my IP address 10.50.0.20 on epair3b!
```

Give the clone its own address before starting it alongside the source:

```bash
hospitus jail network remove app-db-test <interface>
hospitus jail network add app-db-test --bridge hospitus0 --ipv4 10.50.0.23/24
```

#### Step 9: Clean Up Test Clones

When testing is complete:

```bash
# Destroy the test clone
hospitus jail destroy app-db-test -y
```

### Part 3: Export and Import for Disaster Recovery

For full backups that can be transferred to other systems, use export/import.

Both paths must sit inside the daemon's data directory, `/var/lib/hospitus` by
default; anywhere else is refused with

```
Invalid export path: path "/backup/app-db.tar.gz" is outside "/var/lib/hospitus"
```

The daemon writes the archive as root and unpacks one on import, so an
unrestricted path would let any caller read or overwrite whatever it named.
Export into the data directory, then move the archive wherever it belongs with
`mv` or `scp` — those run as you, not as the daemon.

#### Step 10: Export a Jail for Backup

```bash
# Export with compression
hospitus jail export app-db /var/lib/hospitus/backups/app-db-backup.tar.gz --compress
```

**Output**:
```
Exporting jail app-db to /var/lib/hospitus/backups/app-db-backup.tar.gz...
Export completed: Instance app-db exported successfully to /var/lib/hospitus/backups/app-db-backup.tar.gz
```

#### Step 11: Export with Stop for Consistency

For database jails, stop before export to ensure data consistency:

```bash
# Stop the jail before export to ensure data consistency
hospitus jail export app-db /var/lib/hospitus/backups/app-db-consistent.tar.gz \
    --compress \
    --stop \
    --include-snapshots
```

**What happens**: Hospitus stops the jail, creates a ZFS snapshot and exports it.
The jail stays **stopped** — start it again yourself when the export finishes:

```bash
hospitus jail start app-db
```

#### Step 12: Import on Another System

Transfer the backup to another FreeBSD host and import:

```bash
# Import with the original name
hospitus jail import /var/lib/hospitus/backups/app-db-backup.tar.gz
```

**Output**:
```
Importing jail from /var/lib/hospitus/backups/app-db-backup.tar.gz...
Import completed: Instance app-db imported successfully from app-db-backup.tar.gz
Jail name: app-db
```

Importing a name the host already carries changes nothing and says so:

```
Failed to import instance: jail app-db already exists (ZFS dataset)
```

Use `--name` for a copy alongside the original, as the next step shows.

#### Step 13: Import with a New Name

Create a staging copy from a backup:

```bash
# Import as a new jail with different name
hospitus jail import /var/lib/hospitus/backups/app-db-backup.tar.gz --name app-db-staging
```

**Output**:
```
Import completed: Instance app-db-staging imported successfully from app-db-backup.tar.gz
Jail name: app-db-staging
```

#### Step 14: Import with a New IP Address (Avoid Conflicts)

When importing a jail on the same system as the original, the imported jail will have the same IP address, causing network conflicts. Use `--ip` to assign a different IP:

```bash
# Import with a new IP to avoid conflicts
hospitus jail import /var/lib/hospitus/backups/app-db-backup.tar.gz --name app-db-staging --ip 10.50.0.30/24
```

**Output**:
```
Import completed: Instance app-db-staging imported successfully from app-db-backup.tar.gz
Jail name: app-db-staging
```

For complete network isolation (new MAC and new IP):

```bash
# Full network reset for staging environment
hospitus jail import /var/lib/hospitus/backups/app-db-backup.tar.gz \
    --name app-db-staging \
    --reset-mac \
    --ip 10.50.0.30/24
```

**Key options**:
- `--name`: Renames the imported jail
- `--reset-mac`: Generates new MAC addresses (important to avoid conflicts on same network)
- `--ip`: Assigns a new IP address (e.g., `10.0.0.100/24`) to avoid IP conflicts
- `--start`: Automatically starts the jail after import

#### Step 15: Verify and Start the Imported Jail

```bash
# Check the imported jail
hospitus jail info app-db-staging

# Start the imported jail
hospitus jail start app-db-staging
```

### Part 4: Automated Backup Strategy

A typical snapshot workflow for production:

```bash
#!/usr/bin/env bash
# Daily snapshot script

JAIL="app-db"
DATE=$(date +%Y%m%d)

# Create daily snapshot
hospitus jail snapshot create "$JAIL" "daily-$DATE"

# Delete snapshots older than 7 days
for snap in $(hospitus jail snapshot list "$JAIL" -o json | \
    jq -r '.[] | select(.Name | startswith("daily-")) | .Name'); do
    snap_date=${snap#daily-}
    if [[ "$snap_date" < "$(date -v-7d +%Y%m%d)" ]]; then
        hospitus jail snapshot delete "$JAIL" "$snap"
    fi
done
```

### Part 5: Clean Up

When you no longer need staging or test jails:

```bash
# Stop the jail
hospitus jail stop app-db-staging

# Destroy with confirmation skip
hospitus jail destroy app-db-staging -y
```

---

## Scenario 6: Production Deployment with Auto-Start

**Goal**: Configure jails to automatically start in the correct order when the host system boots.

### Step 1: Configure Auto-Start for Infrastructure Jails

```bash
# DNS server - starts first (priority 5)
hospitus jail create dns \
    --image 14.3-RELEASE-amd64 \
    --cpus 1 \
    --memory 512 \
    --vnet \
    --ip 10.50.0.2/24 \
    --auto-start \
    --auto-start-priority 5 \
    --allow-reserved-ports \
    --description "Internal DNS server"

# NFS server - starts second (priority 10)
hospitus jail create nfs \
    --image 14.3-RELEASE-amd64 \
    --cpus 2 \
    --memory 1024 \
    --vnet \
    --ip 10.50.0.3/24 \
    --auto-start \
    --auto-start-priority 10 \
    --auto-start-delay 2000 \
    --description "NFS file server"
```

### Step 2: View All Jails with Auto-Start

```bash
hospitus jail autostart list
```

**Output**:
```
NAME            PRIORITY  DELAY (MS)
----            --------  ----------
dns             5         0
app-db          10        0
app-backend     20        5000
app-web         30        3000
```

The list is ordered by priority, which is the order the jails come up in.

### Step 3: Force Stop a Misbehaving Jail

When a jail doesn't respond to graceful stop:

```bash
# Force stop (equivalent to kill -9)
hospitus jail stop problem-jail --force

# Or using short flag
hospitus jail stop problem-jail -f
```

### Step 4: Force Destroy a Stuck Jail

When normal destroy fails due to busy ZFS dataset:

```bash
# Force destroy even with busy resources
hospitus jail destroy stuck-jail --force --yes
# Or short form
hospitus jail destroy stuck-jail -f -y
```

---

## Scenario 7: Cross-Architecture Jails

**Goal**: Run ARM64 or RISC-V FreeBSD jails on an AMD64 host using QEMU user-mode emulation.

### Prerequisites

Cross-architecture jails require QEMU user-mode emulation configured via FreeBSD's `binmiscctl`:

```bash
# Run the setup script (requires root)
doas tools/setup-binmiscctl.sh

# Or manually configure for ARM64:
doas binmiscctl add aarch64 \
    --interpreter "/usr/local/bin/qemu-aarch64-static" \
    --magic "\x7f\x45\x4c\x46\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\xb7\x00" \
    --mask "\xff\xff\xff\xff\xff\xff\xff\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff\xff" \
    --size 20 \
    --set-enabled
```

Ensure QEMU static binaries are installed:

```bash
pkg install qemu-user-static
```

### Step 1: Download Cross-Architecture Images

```bash
# Download ARM64 FreeBSD image
hospitus image fetch freebsd-14.3-RELEASE-arm64

# Download RISC-V image
hospitus image fetch freebsd-14.3-RELEASE-riscv64
```

### Step 2: Create an ARM64 Jail

```bash
# Using --version and --arch flags (recommended)
# Builds image name automatically: freebsd-14.3-RELEASE-arm64
hospitus jail create arm-test \
    -V 14.3 --arch arm64 \
    --cpus 2 \
    --memory 1024 \
    --vnet \
    --ip dhcp \
    --description "ARM64 test jail via QEMU emulation"

# Or using explicit --image flag
hospitus jail create arm-test \
    --image freebsd-14.3-RELEASE-arm64 \
    --cpus 2 \
    --memory 1024 \
    --vnet \
    --ip dhcp \
    --description "ARM64 test jail via QEMU emulation"
```

**What happens**: Hospitus extracts the ARM64 FreeBSD base system and **automatically copies the appropriate QEMU static binary** (`qemu-aarch64-static`) into the jail. When you run binaries inside the jail, the kernel intercepts them and runs them through QEMU user-mode emulation.

### Step 3: Start and Verify Architecture

```bash
hospitus jail start arm-test

# Check the architecture inside the jail
doas hospitus jail exec arm-test uname -m
```

**Output**:
```
arm64
```

FreeBSD reports the machine as `arm64` and the processor architecture as
`aarch64`, so `uname -p` answers `aarch64` for the same jail. Package names and
the `--arch` flag follow the `arm64` spelling.

### Step 4: Install ARM64 Packages

Package management works normally—`pkg` fetches ARM64 packages:

```bash
# Bootstrap pkg (fetches ARM64 pkg binary)
doas hospitus jail exec arm-test env ASSUME_ALWAYS_YES=yes pkg bootstrap

# Install packages (ARM64 versions)
doas hospitus jail exec arm-test env IGNORE_OSVERSION=yes ASSUME_ALWAYS_YES=yes pkg install -y nginx
```

### Step 5: Create a RISC-V Jail

```bash
# Using --version and --arch flags (recommended)
# Builds image name automatically: freebsd-14.3-RELEASE-riscv64
hospitus jail create riscv-test \
    -V 14.3 --arch riscv64 \
    --cpus 1 \
    --memory 512 \
    --vnet \
    --description "RISC-V test jail"

# Or using explicit --image flag
hospitus jail create riscv-test \
    --image freebsd-14.3-RELEASE-riscv64 \
    --cpus 1 \
    --memory 512 \
    --vnet \
    --description "RISC-V test jail"

# Start and verify (QEMU binary is automatically copied)
hospitus jail start riscv-test

# Verify
doas hospitus jail exec riscv-test uname -m
```

**Output**:
```
riscv64
```

### Step 6: Performance Considerations

Emulated jails run slower than native jails:

- **CPU-bound tasks**: Expect 5-20x slowdown depending on workload
- **I/O-bound tasks**: Minimal overhead (filesystem is native)
- **Use cases**: Testing, CI/CD for cross-platform builds, development

For production ARM64/RISC-V workloads, run on native hardware.

### Step 7: Verify Emulation Status

Check that binmiscctl is properly configured:

```bash
# List configured interpreters
binmiscctl list
```

**Output**:
```
name: aarch64
interpreter: /usr/local/bin/qemu-aarch64-static
flags: ENABLED USE_MASK
magic size: 20
magic offset: 0
magic: 0x7f 0x45 0x4c 0x46  0x02 0x01 0x01 0x00  0x00 0x00 0x00 0x00
       0x00 0x00 0x00 0x00  0x02 0x00 0xb7 0x00
mask:  0xff 0xff 0xff 0xff  0xff 0xff 0xff 0x00  0xff 0xff 0xff 0xff
       0xff 0xff 0xff 0xff  0xfe 0xff 0xff 0xff

name: riscv64
interpreter: /usr/local/bin/qemu-riscv64-static
flags: ENABLED USE_MASK
...
```

`ENABLED` in the flags is what matters: an interpreter that is registered but
disabled leaves binaries failing with "Exec format error".

### Troubleshooting Cross-Architecture Jails

**Error: "jail: exec /bin/sh: No such file or directory"**
- Hospitus automatically copies the QEMU static binary into the jail during creation. If this error occurs:
  1. Ensure `qemu-user-static` package is installed: `pkg install qemu-user-static`
  2. Verify the binary exists: `ls /usr/local/bin/qemu-aarch64-static` (or `qemu-riscv64-static`)
  3. For existing jails created before automatic QEMU copy, manually copy the binary:
  ```bash
  JAIL_ROOT="/zroot/hospitus/jails/<jailname>"
  doas mkdir -p "$JAIL_ROOT/usr/local/bin"
  doas cp /usr/local/bin/qemu-aarch64-static "$JAIL_ROOT/usr/local/bin/"  # for ARM64
  # or qemu-riscv64-static for RISC-V
  ```

**Error: "Exec format error"**
- The interpreter is not configured. Run `doas tools/setup-binmiscctl.sh`
- Or manually add the interpreter with `binmiscctl add`

**Error: "No such file or directory" for qemu-*-static**
- Install QEMU: `pkg install qemu-user-static`

**Jail starts but commands hang**
- Check QEMU version compatibility
- Ensure enough memory is allocated (emulation uses more RAM)

---

## Scenario 8: Monitoring and Health Checks

**Goal**: Monitor jail resource usage and perform health checks to ensure jails are running correctly.

### Prerequisites

Resource monitoring requires RACCT (Resource ACCounting) to be enabled on FreeBSD:

```bash
# Check if RACCT is enabled
sysctl kern.racct.enable

# If not enabled, add to /boot/loader.conf and reboot:
# kern.racct.enable=1
```

### Step 1: View Resource Usage Statistics

The `stats` command shows real-time resource usage for a running jail:

```bash
# View human-readable stats
hospitus jail stats webserver
```

**Output**:
```
Resource Statistics for webserver
================================

CPU Usage:      5.23%
Memory:         128 MB / 265 MB
Disk Read:      1.2 MB
Disk Write:     3.5 MB
Network RX:     737.0 KB
Network TX:     31.6 MB

Timestamp: 2024-12-11T10:44:24+01:00
```

### Step 2: Get Stats in JSON Format

For scripting and monitoring tools:

```bash
# JSON output for automation
hospitus jail stats webserver --output json
```

**Output**:
```json
{
  "timestamp": "2024-12-11T10:44:24.085009561+01:00",
  "cpu_usage_percent": 5.23,
  "memory_used_mb": 128,
  "memory_total_mb": 265,
  "disk_read_bytes": 1258291,
  "disk_write_bytes": 3670016,
  "net_rx_bytes": 754688,
  "net_tx_bytes": 33128448
}
```

### Step 3: Perform Health Checks

The `health` command performs multiple checks to verify a jail is healthy:

```bash
# Check jail health
hospitus jail health webserver
```

**Output**:
```
Health Check for webserver
================================

Status: [OK] healthy
Message: all checks passed

Checks:
  [OK] jail_running    jail is running
  [OK] zfs_dataset     ZFS dataset exists
  [OK] network         VNET interface epair0a is up
  [OK] process_exec    jail is responsive

Timestamp: 2024-12-11T10:49:13+01:00
```

### Step 4: Health Check JSON Output

For monitoring integration:

```bash
hospitus jail health webserver --output json
```

**Output**:
```json
{
  "status": "healthy",
  "message": "all checks passed",
  "checks": [
    {
      "name": "jail_running",
      "status": "healthy",
      "message": "jail is running"
    },
    {
      "name": "zfs_dataset",
      "status": "healthy",
      "message": "ZFS dataset exists"
    },
    {
      "name": "network",
      "status": "healthy",
      "message": "VNET interface epair0a is up"
    },
    {
      "name": "process_exec",
      "status": "healthy",
      "message": "jail is responsive"
    }
  ],
  "timestamp": "2024-12-11T10:49:20.341895838+01:00"
}
```

### Step 5: Understanding Health Status

Health checks return one of four statuses:

| Status | Description |
|--------|-------------|
| `healthy` | All checks passed |
| `unhealthy` | Critical checks failed (jail not running, ZFS missing) |
| `degraded` | Non-critical checks failed (network issue, exec failed) |
| `unknown` | Unable to determine status |

### Step 6: Monitoring Script Example

Create a simple monitoring script:

```bash
#!/usr/bin/env bash
# jail-monitor.sh - Monitor all jails and alert on issues

JAILS=$(hospitus jail list --output json | jq -r '.[].name')

for jail in $JAILS; do
    status=$(hospitus jail health "$jail" --output json 2>/dev/null | jq -r '.status')

    case $status in
        healthy)
            echo "[OK] $jail is healthy"
            ;;
        degraded)
            echo "[WARN] $jail is degraded"
            # Send alert
            ;;
        unhealthy|"")
            echo "[FAIL] $jail is unhealthy or not responding"
            # Send critical alert
            ;;
    esac
done
```

### Step 7: Prometheus Integration Example

Export metrics for Prometheus:

```bash
#!/usr/bin/env bash
# Export jail metrics in Prometheus format

JAILS=$(hospitus jail list --output json | jq -r '.[] | select(.state=="running") | .name')

for jail in $JAILS; do
    metrics=$(hospitus jail stats "$jail" --output json 2>/dev/null)
    if [ -n "$metrics" ]; then
        cpu=$(echo "$metrics" | jq -r '.cpu_usage_percent')
        mem=$(echo "$metrics" | jq -r '.memory_used_mb')
        rx=$(echo "$metrics" | jq -r '.net_rx_bytes')
        tx=$(echo "$metrics" | jq -r '.net_tx_bytes')

        echo "hospitus_jail_cpu_percent{jail=\"$jail\"} $cpu"
        echo "hospitus_jail_memory_mb{jail=\"$jail\"} $mem"
        echo "hospitus_jail_network_rx_bytes{jail=\"$jail\"} $rx"
        echo "hospitus_jail_network_tx_bytes{jail=\"$jail\"} $tx"
    fi
done
```

---

## Scenario 9: Tmux Sessions for Persistent Access

**Goal**: Use tmux sessions inside jails for persistent shell access that survives disconnects and long-running provisioning tasks.

Tmux sessions are ideal for:
- Long-running package installations that may take minutes or hours
- Sessions that need to survive network disconnects
- Multi-window/pane workflows inside jails
- Automated provisioning scripts

### Prerequisites

Tmux commands run locally via `jexec` and require root privileges. The jail must be running.

### Step 1: Attach to a Tmux Session

The simplest way to get a persistent shell:

```bash
# Attach to default session (named after the jail)
doas hospitus jail tmux myjail
```

**Output**:
```
Creating new tmux session 'myjail'...
Note: tmux session runs locally via jexec (not through the API)
Attaching to tmux session 'myjail' in jail myjail
Detach with: Ctrl+b d
```

If tmux is not installed in the jail, you'll be prompted to install it.

### Step 2: Create Named Sessions

Use named sessions for different purposes:

```bash
# Create session for provisioning
doas hospitus jail tmux myjail provisioning

# Create session for monitoring
doas hospitus jail tmux myjail monitoring
```

### Step 3: List Active Sessions

```bash
# List all tmux sessions in a jail
doas hospitus jail tmux list myjail
```

**Output**:
```
Tmux sessions in jail myjail:

SESSION              WINDOWS    ATTACHED   CREATED
------------------------------------------------------------
myjail               1          no         2024-12-13 21:30
provisioning         2          yes        2024-12-13 21:35
```

### Step 4: Send Commands to Background Sessions

Run commands in a tmux session without attaching:

```bash
# Send a long-running command to a session
doas hospitus jail tmux send myjail "env IGNORE_OSVERSION=yes ASSUME_ALWAYS_YES=yes pkg install -y nginx postgresql16-server"

# Send to a specific session
doas hospitus jail tmux send myjail "make install" --session provisioning
```

**Output**:
```
Creating new tmux session 'myjail'...
Sent command to tmux session 'myjail' in jail myjail
Attach to view output: hospitus jail tmux myjail myjail
```

The session is created if it is not there yet, so this works on a jail nobody
has attached to. That first line is absent when the session already exists.

This is perfect for automated provisioning - the command runs in the background and you can check progress later.

### Step 5: Monitor Command Progress

Attach to see what's happening:

```bash
# Reattach to view progress
doas hospitus jail tmux myjail
```

Inside tmux, detach with `Ctrl+b d` to leave it running in the background.

### Step 6: Kill Sessions

Clean up sessions when done:

```bash
# Kill a specific session
doas hospitus jail tmux kill myjail provisioning
```

**Output**:
```
Killed tmux session 'provisioning' in jail myjail
```

### Step 7: Use Tmux for Long Provisioning

Example workflow for deploying a complex application:

```bash
# Create a jail
hospitus jail create appserver --image 14.3-RELEASE-amd64 --vnet --ip dhcp
hospitus jail start appserver

# Install tmux
doas hospitus jail exec appserver env ASSUME_ALWAYS_YES=yes pkg install -y tmux

# Start a provisioning session
doas hospitus jail tmux appserver provisioning

# Inside tmux, run your provisioning commands:
# pkg install -y python311 py311-pip nginx
# pip install gunicorn django
# ... (these may take a while)

# Detach with Ctrl+b d if you need to disconnect

# Later, reattach to check progress
doas hospitus jail tmux appserver provisioning
```

### Step 8: Automated Provisioning Script

Combine tmux with scripting for hands-off provisioning:

```bash
#!/usr/bin/env bash
# provision-appserver.sh

JAIL="appserver"

# Ensure jail is running
hospitus jail start "$JAIL" 2>/dev/null || true

# Install tmux if needed
doas hospitus jail exec "$JAIL" which tmux || \
    doas hospitus jail exec "$JAIL" env ASSUME_ALWAYS_YES=yes pkg install -y tmux

# Send provisioning commands to background session
doas hospitus jail tmux send "$JAIL" "pkg update && pkg upgrade -y"

echo "Provisioning started. Monitor with:"
echo "  doas hospitus jail tmux $JAIL"
```

### Tmux Quick Reference

| Key Combination | Action |
|-----------------|--------|
| `Ctrl+b d` | Detach from session (leaves it running) |
| `Ctrl+b c` | Create new window |
| `Ctrl+b n` | Next window |
| `Ctrl+b p` | Previous window |
| `Ctrl+b %` | Split pane vertically |
| `Ctrl+b "` | Split pane horizontally |
| `Ctrl+b o` | Switch to next pane |

### Why Use Tmux Instead of exec?

| Feature | `hospitus jail exec` | `hospitus jail tmux` |
|---------|-------------------|-------------------|
| Persistence | No - exits if connection drops | Yes - survives disconnects |
| Long commands | Risk of timeout | Perfect for hours-long tasks |
| Multiple sessions | No | Yes - multiple named sessions |
| Background execution | No | Yes - detach and reconnect |
| Provisioning | Via API (requires daemon) | Direct via jexec (local) |

Use `exec` for quick, one-off commands. Use `tmux` for interactive work, long-running tasks, or when you need session persistence.

---

## Command Reference Summary

### Lifecycle Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus jail create <name>` | Create a new jail | `--image`, `--cpus`, `--memory`, `--vnet`, `--ip` |
| `hospitus jail start <name>` | Start a stopped jail | - |
| `hospitus jail stop <name>` | Stop a running jail | `--force` / `-f` |
| `hospitus jail restart <name>` | Restart a jail | - |
| `hospitus jail destroy <name>` | Permanently delete a jail | `--force`, `--yes` / `-y` |

### Information Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus jail list` | List all jails | `--output json` |
| `hospitus jail info <name>` | Show detailed jail info | `--output json` |

### Monitoring Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus jail stats <name>` | Show resource usage (CPU, memory, network) | `--output json` |
| `hospitus jail health <name>` | Perform health check | `--output json` |

**Aliases**:
- `stats` can also be called as `metrics` or `top`
- `health` can also be called as `check`

### Interactive Commands (require root/doas)

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `doas hospitus jail exec [flags] <name> <cmd>` | Execute command in jail | `-u`, `-w`, `-i` |
| `doas hospitus jail console <name>` | Interactive shell | `--shell` |

### Tmux Session Commands (require root/doas)

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `doas hospitus jail tmux <name> [session]` | Attach to/create tmux session | - |
| `doas hospitus jail tmux list <name>` | List tmux sessions in jail | - |
| `doas hospitus jail tmux kill <name> <session>` | Kill a tmux session | - |
| `doas hospitus jail tmux send <name> <cmd>` | Send command to session | `--session` |

**Important**: For `exec`, flags must come **before** the jail name:
```bash
# Correct: flags before jail name
doas hospitus jail exec -u postgres postgres psql

# Wrong: flags after jail name (will be passed to the command)
doas hospitus jail exec postgres -u postgres psql  # -u goes to jexec, not hospitus
```

This design allows passing flags like `-y` to commands inside the jail:
```bash
doas hospitus jail exec myjail env IGNORE_OSVERSION=yes ASSUME_ALWAYS_YES=yes pkg install -y nginx  # -y goes to pkg
```

### Network Commands (require root/doas)

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `doas hospitus jail expose add <name>` | Add port forwarding | `--port` |
| `doas hospitus jail expose remove <name>` | Remove port forwarding | `--port`, `--protocol` |
| `hospitus jail expose list <name>` | List port forwards | `--output` |

### Snapshot Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus jail snapshot create <jail> <name>` | Create a ZFS snapshot | - |
| `hospitus jail snapshot list <jail>` | List jail snapshots | `--output json` |
| `hospitus jail snapshot delete <jail> <name>` | Delete a snapshot | - |
| `hospitus jail snapshot restore <jail> <name>` | Rollback to snapshot | - |

### Clone Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus jail clone <source> <name>` | Clone a jail | `--snapshot`, `--linked`, `--reset-mac` |

### Backup Commands

| Command | Description | Key Flags |
|---------|-------------|-----------|
| `hospitus jail export <name> <path>` | Export jail to tarball | `--compress`, `--stop`, `--include-snapshots` |
| `hospitus jail import <path>` | Import jail from tarball | `--name`, `--reset-mac`, `--ip`, `--start` |

### Security Flags (for `create`)

| Flag | Purpose |
|------|---------|
| `--allow-raw-sockets` | Enable ping, traceroute |
| `--allow-sysvipc` | Enable System V IPC (PostgreSQL) |
| `--allow-mount` | Allow filesystem mounting |
| `--allow-mount-zfs` | Allow mounting ZFS datasets |
| `--allow-mlock` | Allow memory locking |
| `--allow-reserved-ports` | Allow binding to ports < 1024 |
| `--allow-vmm` | Allow running bhyve inside jail |

### Behavior Flags (for `create`)

| Flag | Purpose |
|------|---------|
| `--auto-start` | Auto-start on system boot |
| `--auto-start-priority <n>` | Start order (0-100, lower first) |
| `--auto-start-delay <ms>` | Delay before starting |
| `--persist` | Keep running with no processes |
| `--devfs-ruleset <n>` | DevFS ruleset number |
| `--securelevel <n>` | Jail securelevel (-1 to 3) |
| `--children-max <n>` | Max nested jails |

### Lifecycle Hooks (for `create`)

| Flag | When Executed |
|------|---------------|
| `--exec-prestart <cmd>` | Before jail starts |
| `--exec-poststart <cmd>` | After jail starts |
| `--exec-prestop <cmd>` | Before jail stops |
| `--exec-poststop <cmd>` | After jail stops |
| `--exec-clean` | Run hooks in clean environment |

---

## Next Steps

- **[API Reference](../developer-guide/api-reference.md)**: REST API documentation for automation
- **[Networking Guide](networking.md)**: Advanced VNET and PF configuration
- **[Troubleshooting](../troubleshooting/faq.md)**: Common issues and solutions
- **[bhyve Guide](bhyve.md)**: Managing bhyve virtual machines

---

## Advanced Operations

### Rename a Jail

Rename a stopped jail. The ZFS dataset, jail.conf, and internal state are all updated.

```bash
# Jail must be stopped
hospitus jail stop old-name
hospitus jail rename old-name new-name
```

### Upgrade a Jail's Base System

Upgrade the FreeBSD base system inside a jail to a newer release using
`freebsd-update(8)`. The jail must be stopped. This is a long-running
operation submitted as an async job.

```bash
# Stop the jail first
hospitus jail stop myjail

# Submit upgrade job (returns immediately)
hospitus jail upgrade myjail --release 14.3-RELEASE

# Poll the returned job ID
hospitus job info <job-id>
```

Only the base system is upgraded. Packages are deliberately left alone — the
jail is stopped for the base upgrade, so `pkg` cannot run inside it. Upgrade
them after starting the jail again:

```bash
hospitus jail start myjail
doas hospitus jail exec myjail pkg upgrade -y
```

> **Note**: `freebsd-update` requires network access and a compatible release
> path. Only FreeBSD jails are supported (not Linux or cross-arch jails).

### IPv6 Support

A jail gets an IPv6 address when you give it one. Creating a jail with `--vnet`
assigns IPv4 only — `hospitus jail info` reports no IPv6, and inside the jail the
interface carries nothing beyond the link-local address:

```bash
hospitus jail network add myjail --bridge hospitus0 --ipv6 fd00::42/64
```

```
Host Interface: epair6a
Jail Interface: epair6b
IPv6:           fd00::42/64
```

Either add the address on a second interface as above, or pass `--ipv6` to
`hospitus jail network add` when attaching the first one.

> **Note**: hospitusd carries an `ipv6_prefix` setting (default `fd00::/48`) and
> the code to derive a stable ULA address per jail from it, but nothing calls
> that code yet — no jail is given an address automatically. Assign IPv6
> explicitly until this is wired up.
