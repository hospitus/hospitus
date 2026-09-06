# Jail Networking Walkthrough

This guide covers jail networking in Hospitus: VNET, bridges, PF NAT, and port forwarding, from basic setup to advanced configurations.

## Table of Contents

- [Networking Architecture Overview](#networking-architecture-overview)
- [Prerequisites](#prerequisites)
- [Basic Networking Concepts](#basic-networking-concepts)
  - [Shared IP vs VNET](#shared-ip-vs-vnet)
  - [Epair Interfaces](#epair-interfaces)
  - [Bridges](#bridges)
  - [PF NAT](#pf-nat)
- [Step-by-Step Walkthroughs](#step-by-step-walkthroughs)
  - [1. Creating a Jail with Default Networking](#1-creating-a-jail-with-default-networking)
  - [2. Creating a Jail with VNET](#2-creating-a-jail-with-vnet)
  - [3. Configuring Static IP Addresses](#3-configuring-static-ip-addresses)
  - [4. Setting Up Port Forwarding](#4-setting-up-port-forwarding)
  - [5. Creating a Custom Bridge](#5-creating-a-custom-bridge)
  - [6. Multi-Jail Network Topology](#6-multi-jail-network-topology)
  - [7. Linux and Cross-Architecture Jail Networking](#7-linux-and-cross-architecture-jail-networking)
- [Advanced Configurations](#advanced-configurations)
  - [Multiple Network Interfaces](#multiple-network-interfaces)
  - [VLAN Tagging](#vlan-tagging)
  - [IPv6 Notes](#ipv6-notes)
  - [Network Isolation](#network-isolation)
- [Troubleshooting](#troubleshooting)
  - [Jail Has No Network Access](#jail-has-no-network-access)
  - [Port Forwarding Not Working](#port-forwarding-not-working)
  - [IP Address Conflicts](#ip-address-conflicts)
  - [Bridge Not Created](#bridge-not-created)
  - [PF Rules Not Loading](#pf-rules-not-loading)
- [Quick Reference](#quick-reference)

---

## Networking Architecture Overview

Hospitus manages jail networking through three layers:

```
┌─────────────────────────────────────────────────────────┐
│                    Jail (VNET)                          │
│  ┌─────────────┐    ┌─────────────┐                    │
│  │   epair0b   │    │   lo0       │                    │
│  │  10.0.0.2   │    │  127.0.0.1  │                    │
│  └──────┬──────┘    └─────────────┘                    │
└─────────┼──────────────────────────────────────────────┘
          │ epair (virtual cable)
┌─────────┼──────────────────────────────────────────────┐
│              Host Network                              │
│  ┌───────┴──────┐    ┌─────────────┐                   │
│  │   epair0a    │───►│  hospitus0     │◄── em0 (physical) │
│  │  (vnet side) │    │  (bridge)   │    192.168.1.100  │
│  └──────────────┘    └──────┬──────┘                   │
│                             │                           │
│                    ┌────────┴────────┐                  │
│                    │   PF NAT/RDR    │                  │
│                    │  anchor "hospitus" │                  │
│                    └─────────────────┘                  │
└────────────────────────────────────────────────────────┘
```

---

## Prerequisites

Before configuring jail networking, ensure:

1. **ZFS is available:**
   ```bash
   zfs list
   ```

2. **PF is enabled** (for NAT and port forwarding):
   ```bash
   sysrc pf_enable=YES
   service pf start
   ```

3. **Hospitus daemon is running:**
   ```bash
   doas service hospitus start
   # Or run in foreground for testing:
   doas ./hospitusd --data-dir /var/lib/hospitus --state-dir /var/lib/hospitus/state
   ```

4. **Base image is available:**
   ```bash
   hospitus image list
   # If empty, fetch one:
   hospitus image fetch 14.3-RELEASE-amd64
   ```

---

## Basic Networking Concepts

### Shared IP vs VNET

| Feature | Shared IP | VNET |
|---------|-----------|------|
| Network stack | Shared with host | Isolated per jail |
| IP address | Host IP | Dedicated IP |
| Ports | Shared (must not conflict) | Independent |
| Firewall | Host PF rules | Per-jail PF rules |
| Use case | Simple services, dev | Production, multi-tenant |

**Shared IP** (default): The jail shares the host's network stack. Services bind to host ports directly.

**VNET**: Each jail gets its own virtual network stack with dedicated interfaces, IP addresses, and routing tables.

### Epair Interfaces

An epair is a virtual Ethernet cable with two ends:
- `epairNa` — attached to the host bridge
- `epairNb` — placed inside the jail

Hospitus creates epairs automatically when you create a VNET jail.

### Bridges

A bridge connects multiple network interfaces at layer 2. Hospitus creates a default bridge (`hospitus0`) for VNET jails:

```bash
ifconfig hospitus0
# hospitus0: flags=8843<UP,BROADCAST,RUNNING,SIMPLEX,MULTICAST>
```

### PF NAT

Hospitus uses PF anchors to manage NAT rules without modifying your main `pf.conf`:

```bash
# View NAT rules
doas pfctl -a hospitus -s nat

# View redirect (port forwarding) rules
doas pfctl -a hospitus -s nat
```

---

## Step-by-Step Walkthroughs

### 1. Creating a Jail with Default Networking

This creates a jail with **shared IP** networking (no VNET):

```bash
hospitus jail create web-simple --image 14.3-RELEASE-amd64
```

**Verify:**
```bash
hospitus jail info web-simple
# Check "Networking" section — should show "shared" or empty

# Inside the jail, you'll see the host's interfaces:
doas hospitus jail exec web-simple ifconfig
```

**Use case:** Simple development jails, services that don't need network isolation.

---

### 2. Creating a Jail with VNET

This creates a jail with its **own network stack**:

```bash
hospitus jail create web-vnet --image 14.3-RELEASE-amd64 --vnet --ip dhcp
```

`--ip` is required. VNET gives the jail its own network stack, and without an
address hospitus creates no interface in it at all — `ifconfig -l` inside shows
only `lo0`. With `dhcp` the jail takes the next free address from the pool
(`10.0.0.0/24` by default), gets its own epair, and reaches the outside through
PF NAT.

**Verify:**
```bash
# Check jail networking
hospitus jail info web-vnet

# Should show something like:
# Network:
#   Type:       bridge
#   IPv4:       10.0.0.5/24

# Check the bridge on the host
doas ifconfig hospitus0
# Should list the jail's epair as a member:
#   member: epair3a flags=143<LEARNING,DISCOVER,AUTOEDGE,AUTOPTP>

# Check inside the jail
doas hospitus jail exec web-vnet ifconfig
# Should show the other half of that pair holding the address:
#   epair3b: flags=1008843<UP,BROADCAST,RUNNING,SIMPLEX,MULTICAST,LOWER_UP>
#           inet 10.0.0.5 netmask 0xffffff00 broadcast 10.0.0.255
#
# The number varies: each jail takes the next free epair.

# Test internet access from the jail
doas hospitus jail exec web-vnet ping -c 2 8.8.8.8
```

---

### 3. Configuring Static IP Addresses

By default, Hospitus auto-assigns IPs from `10.0.0.0/24`. You can specify a static IP:

```bash
hospitus jail create db-server --image 14.3-RELEASE-amd64 --vnet --ip 10.0.0.10/24
```

The prefix length is not optional: without it ifconfig falls back to the
classful mask, so `10.0.0.10` would configure a /8 and the jail would treat all
of `10.0.0.0/8` as directly reachable. Hospitus refuses an address written without
one.

**Verify:**
```bash
hospitus jail info db-server
# Should show IPv4: 10.0.0.10/24

doas hospitus jail exec db-server ifconfig
# Should show inet 10.0.0.10 on the jail's epair. Do not name the interface:
# each jail takes the next free pair, so "ifconfig epair0b" answers
# "interface epair0b does not exist" as often as not.
```

**IP Pool Management:**

Hospitus tracks allocated IPs to prevent conflicts. To see all allocated IPs:

```bash
# Check all jail IPs
hospitus jail list
# Or read one jail's stored network section:
doas jq .spec.networks /var/lib/hospitus/state/jails/db-server.json
```

**Changing a jail's IP:**

Stop the jail, modify the config, and restart:
```bash
hospitus jail stop db-server
# Edit the jail config:
doas vi /var/lib/hospitus/state/jails/db-server.json
# Change the IP in the networks section
hospitus jail start db-server
```

---

### 4. Setting Up Port Forwarding

Port forwarding exposes jail services on the host's IP using PF rdr rules.

**Forward host port 8080 to jail port 80:**

```bash
hospitus jail expose add web-vnet --port 8080:80
```

The `--port` spec is `host:jail`, defaulting to TCP. Prefix it with `tcp/` or
`udp/` to choose the protocol (`--port udp/53`). There is no `--protocol` flag on
`expose add`.

Hospitus adds a PF rdr rule under its anchor. PF prints a port by its service
name, so 8080 comes back as `http-alt`:

```
rdr pass on em0 inet proto tcp from any to any port = http-alt -> 10.0.0.2 port 80
```

**Verify:**
```bash
# A jail may not bind a port below 1024 until it is allowed to
doas hospitus jail set web-vnet --allow-reserved-ports
doas hospitus jail restart web-vnet

# Start a web server inside the jail. exec takes an argv, not a shell line: the
# validator rejects a command containing "&&", so run the two steps separately
# (or wrap them in /bin/sh -c '...', which the provider special-cases).
doas hospitus jail exec web-vnet -- env IGNORE_OSVERSION=yes ASSUME_ALWAYS_YES=yes pkg install -y nginx
doas hospitus jail exec web-vnet -- service nginx onestart

# Test from another machine on the network
curl http://<host-ip>:8080
# Should return the nginx welcome page
```

The host can test it too. A rdr rule only rewrites traffic arriving on the
external interface — the kernel handles loopback connections before PF sees
them — so hospitusd additionally runs a small TCP proxy on `127.0.0.1:<host port>`
that forwards to the jail. `curl http://127.0.0.1:8080` therefore reaches the
jail like any external client, unless something else on the host already owns
that port: hospitusd's own API port is the usual collision, and there the answer
comes from hospitusd rather than the jail.

**Multiple ports:**
```bash
# Forward HTTPS
hospitus jail expose add web-vnet --port 8443:443

# Forward UDP (e.g., DNS)
hospitus jail expose add dns-jail --port udp/53
```

**List exposed ports:**
```bash
hospitus jail expose list web-vnet
```

**Remove port forwarding:**
```bash
hospitus jail expose remove web-vnet --port 8080 --protocol tcp
```

---

### 5. Creating a Custom Bridge

By default, Hospitus creates `hospitus0`. You can create additional bridges for network segmentation:

Hospitus auto-creates a bridge it does not find (`auto_create_bridges` defaults to
true), but only the default bridge gets an address from the IP pool. A
segment with its own subnet therefore needs the bridge created by hand so it
has a gateway:

```bash
# Create a bridge for DMZ jails
doas ifconfig bridge create name dmz0
doas ifconfig dmz0 up

# Assign an IP to the bridge (gateway for DMZ jails)
doas ifconfig dmz0 inet 172.16.0.1/24
```

**Create a jail on the custom bridge:**

```bash
hospitus jail create dmz-web --image 14.3-RELEASE-amd64 --vnet --bridge dmz0 --ip 172.16.0.2/24
```

**Verify:**
```bash
hospitus jail info dmz-web
# Should show:
#   Network:
#     Type:       bridge
#     Bridge:     dmz0
#     IPv4:       172.16.0.2/24

doas ifconfig dmz0
# The bridge created above, now with the jail's epair as a member and
# 172.16.0.1/24 as the DMZ gateway
```

**Use case:** Isolating public-facing services from internal services.

---

### 6. Multi-Jail Network Topology

Create a realistic multi-jail setup with a reverse proxy, application server, and database:

```bash
# 1. Create the reverse proxy (public-facing)
hospitus jail create proxy --image 14.3-RELEASE-amd64 --vnet --ip 10.0.0.2/24
hospitus jail expose add proxy --port 80:80
hospitus jail expose add proxy --port 443:443

# 2. Create the application server (internal)
hospitus jail create app --image 14.3-RELEASE-amd64 --vnet --ip 10.0.0.3/24

# 3. Create the database (internal, isolated)
hospitus jail create db --image 14.3-RELEASE-amd64 --vnet --ip 10.0.0.4/24

# 4. Verify all jails are running
hospitus jail list
```

**Network topology:**
```
Internet ──► em0 (host) ──► hospitus0 (bridge)
                                    │
                    ┌───────────────┼───────────────┐
                    │               │               │
              proxy (10.0.0.2)  app (10.0.0.3)  db (10.0.0.4)
```

**Configure nginx as reverse proxy:**

Use a manifest for multi-jail deployments with service configuration. The [UWM manifest system](../uwm/overview.md) handles service installation, configuration files, and startup ordering declaratively — avoiding broken shell heredocs and escaped quoting.

---

### 7. Linux and Cross-Architecture Jail Networking

Linux jails and cross-architecture jails (ARM64 on AMD64) use the same epair/bridge/VNET infrastructure as native FreeBSD jails. The only difference is interface naming inside the jail (Linux may use `eth0` instead of `epair0b`).

Prerequisites for cross-architecture: `qemu-user-static` and `imgact_binmisc` kernel module.

---

## Advanced Configurations

### Multiple Network Interfaces

A jail can have multiple network interfaces for complex topologies:

Declare each interface as a `[[networks]]` entry in a manifest (jails implement
these as VNET interfaces internally — the manifest `type` is `bridge`, never
`vnet`):

```bash
cat > multi-net.toml << 'EOF'
[workload]
name = "multi-net-jail"

[provider]
type = "jail"

[image]
source = "freebsd:14.3-RELEASE"

[[networks]]
name   = "lan"
type   = "bridge"
bridge = "hospitus0"

[networks.ip]
mode    = "static"
address = "10.0.0.10/24"

[[networks]]
name   = "dmz"
type   = "bridge"
bridge = "dmz0"

[networks.ip]
mode    = "static"
address = "172.16.0.10/24"
EOF

hospitus apply --start multi-net.toml
```

### VLAN Tagging

For VLAN-segmented networks:

```bash
# Create a VLAN interface on the host
doas ifconfig em0.100 create vlan 100 vlandev em0
doas ifconfig em0.100 up

# Create a bridge for the VLAN
doas ifconfig bridge create name vlan100
doas ifconfig vlan100 addm em0.100
doas ifconfig vlan100 up

# Create a jail on the VLAN bridge
hospitus jail create vlan-jail --image 14.3-RELEASE-amd64 --vnet --bridge vlan100 --ip 192.168.100.2/24
```

### IPv6 Notes

The `hospitus jail create` command takes a single `--ip` value (IPv4 CIDR or `dhcp`);
there is no `--ipv6` flag for jails. Host-wide IPv6 behavior is governed by the
daemon's `enable_ipv6` configuration option. To give a jail a specific IPv6
address, assign it inside the jail after it starts:

```bash
# Read the jail's interface name from "hospitus jail exec ipv6-jail ifconfig"
# first — the epair number is whichever pair was free at start.
doas hospitus jail exec ipv6-jail -- ifconfig <jail-epair> inet6 2001:db8::10/64
doas hospitus jail exec ipv6-jail ifconfig
# Should show both inet and inet6 addresses
```

IPv6 addresses are globally routable and do not need NAT, but inbound
port-forwarding still uses the same `hospitus jail expose add` mechanism:

```bash
hospitus jail expose add ipv6-jail --port 80:80
```

### Network Isolation

To create a fully isolated jail (no external network access), you must create the isolated bridge **before** the jail:

```bash
# 1. Create the isolated bridge first (no physical interface member)
doas ifconfig bridge create name isolated0
doas ifconfig isolated0 inet 10.99.0.1/24
doas ifconfig isolated0 up

# 2. Create the jail on the isolated bridge
hospitus jail create isolated --image 14.3-RELEASE-amd64 --vnet --bridge isolated0 --ip 10.99.0.2/24
```

The jail can only communicate with other jails on the same isolated bridge. It has no route to the internet.

---

## Troubleshooting

### Jail Has No Network Access

**Symptom:** `ping: sendto: No route to host` or `ping: UDP connect: No route to host`

**Diagnose:**
```bash
# 1. Check if the jail is running
hospitus jail info <name>

# 2. Check if the epair exists
doas ifconfig | grep epair

# 3. Check if the epair is attached to the bridge
doas ifconfig hospitus0

# 4. Check inside the jail
doas hospitus jail exec <name> ifconfig
doas hospitus jail exec <name> netstat -rn
```

**Fix:**
```bash
# If epair is missing, restart the jail
hospitus jail restart <name>

# If bridge is missing, recreate it
doas ifconfig bridge create name hospitus0
doas ifconfig hospitus0 up
doas ifconfig hospitus0 inet 10.0.0.1/24
hospitus jail restart <name>

# If routing is missing inside the jail
doas hospitus jail exec <name> route add default 10.0.0.1
```

---

### Port Forwarding Not Working

**Symptom:** `curl: (7) Failed to connect to <host> port 8080: Connection refused`

**Diagnose:**
```bash
# 1. Check if PF is running
doas service pf status

# 2. Check if rdr rules are loaded
doas pfctl -a hospitus -s nat

# 3. Check if the service is listening inside the jail
doas hospitus jail exec <name> sockstat -4l

# 4. Read the anchor's rules with their hit counters — a rule at zero never
#    matched, which is usually the answer
doas pfctl -a hospitus -vvs rules
```

`-s` needs to be told what to show (`rules`, `nat`, `states`, `info`); on its
own it answers `option requires an argument -- s`.

**Fix:**
```bash
# If PF is not running
doas service pf start

# If rdr rules are missing, re-add them
hospitus jail expose remove <name> --port 8080 --protocol tcp
hospitus jail expose add <name> --port 8080:80

# If PF is blocking, confirm the hospitus anchor is declared in your ruleset
doas pfctl -sn | grep hospitus     # expect: nat-anchor "hospitus"
# If missing, run 'doas hospitus init --check' for the exact lines to add (the PF
# report needs root; unprivileged it skips), then:
# doas pfctl -f /etc/pf.conf
```

---

### IP Address Conflicts

**Symptom:** `arp: <ip> is on epairXb but should be on epairYb`

**Diagnose:**
```bash
# Check all jail IPs
hospitus jail list

# Check for duplicate IPs on the network
doas arp -a | grep <ip>
```

**Fix:**
```bash
# Assign a different IP
hospitus jail stop <name>
# Edit /var/lib/hospitus/state/jails/<name>.json to change the IP
hospitus jail start <name>
```

---

### Bridge Not Created

**Symptom:**
`failed to ensure bridge hospitus0: bridge hospitus0 does not exist and auto_create_bridges is disabled`

Rare on a default install: `auto_create_bridges` is true, so a missing bridge is
created on demand. You see this only on a host where the daemon config turns
that off.

**Fix:** either re-enable auto-creation in `hospitusd.conf`
(`auto_create_bridges = true`, then restart the daemon), or create the bridge
by hand:

```bash
doas ifconfig bridge create name hospitus0
doas ifconfig hospitus0 up
doas ifconfig hospitus0 inet 10.0.0.1/24

# Restart the jail
hospitus jail restart <name>
```

---

### PF Rules Not Loading

**Symptom:** `pfctl: Syntax error in config file` or rules not appearing

**Diagnose:**
```bash
# Check the combined rules file
cat /var/lib/hospitus/firewall/pf/combined.rules

# Try loading manually
doas pfctl -a hospitus -f /var/lib/hospitus/firewall/pf/combined.rules
```

**Fix:**
```bash
# If the rules file is corrupted, regenerate it
# Delete the PF state files and restart the daemon
doas rm /var/lib/hospitus/firewall/pf/*.rules
doas service hospitus restart
# Hospitus will regenerate the rules on jail start
```

---

## Quick Reference

| Task | Command |
|------|---------|
| Create jail with shared IP | `hospitus jail create <name> --image <img>` |
| Create jail with VNET | `hospitus jail create <name> --image <img> --vnet --ip dhcp` |
| Create jail with static IP | `hospitus jail create <name> --image <img> --vnet --ip 10.0.0.5/24` |
| Create jail on custom bridge | `hospitus jail create <name> --image <img> --vnet --bridge dmz0 --ip dhcp` |
| Add port forwarding | `hospitus jail expose add <name> --port 8080:80` |
| Remove port forwarding | `hospitus jail expose remove <name> --port 8080 --protocol tcp` |
| List exposed ports | `hospitus jail expose list <name>` |
| Check jail networking | `hospitus jail info <name>` |
| Check bridge members | `doas ifconfig hospitus0` |
| Check PF NAT rules | `doas pfctl -a hospitus -s nat` |
| Check PF rdr rules | `doas pfctl -a hospitus -s nat` |
| Create custom bridge | `doas ifconfig bridge create name <name>` |
| Test jail internet | `doas hospitus jail exec <name> ping -c 2 8.8.8.8` |
| Check inside jail network | `doas hospitus jail exec <name> ifconfig` |
| Check routing inside jail | `doas hospitus jail exec <name> netstat -rn` |
| Check listening ports in jail | `doas hospitus jail exec <name> sockstat -4l` |