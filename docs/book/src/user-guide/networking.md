# Networking

This guide explains how Hospitus connects jails and VMs to the network: the VNET
model, automatic bridges, PF-based NAT, port forwarding, and the DHCP-like IP
pool that hands out addresses.

## Table of Contents

- [The networking model](#the-networking-model)
- [VNET, epair, and bridges](#vnet-epair-and-bridges)
- [NAT and the PF anchor](#nat-and-the-pf-anchor)
- [Port forwarding](#port-forwarding)
- [IP address configuration](#ip-address-configuration)
- [IP pool configuration](#ip-pool-configuration)
- [Worked examples](#worked-examples)
- [Troubleshooting](#troubleshooting)
- [Migration from CBSD](#migration-from-cbsd)
- [Best practices](#best-practices)
- [See also](#see-also)

## The networking model

Hospitus gives each workload its own isolated network stack rather than sharing the
host's. On FreeBSD this is built from three native primitives:

| Primitive | Role |
|-----------|------|
| **VNET** | A per-jail virtual network stack — its own interfaces, routing table, and firewall state |
| **epair** | A virtual Ethernet cable: one end lives on the host bridge, the other inside the jail |
| **bridge** | A software switch that joins epair ends together and to the outside world |

Traffic from a jail leaves through its epair, crosses the bridge, and — for
private address ranges — is translated to the host's external IP by PF NAT. VMs
(bhyve, QEMU) follow the same bridge model, using `tap` interfaces instead of
epairs.

> **Prerequisites.** VNET networking needs a FreeBSD kernel with `VIMAGE`
> (standard in `GENERIC` on FreeBSD 13+). NAT and port forwarding need PF
> enabled: `sysrc pf_enable=YES && service pf start`.

## VNET, epair, and bridges

### Enabling VNET on a jail

Pass `--vnet` at creation time. Without it, the jail shares the host network
stack (IP-based, no isolation):

```bash
hospitus jail create web --image 14.3-RELEASE-amd64 --vnet --ip 10.0.0.10/24
```

When the jail starts, Hospitus:

1. Creates an `epair` interface pair.
2. Ensures the target bridge exists (see below), creating it if
   `auto_create_bridges` is enabled.
3. Adds the host-side epair end to the bridge.
4. Moves the jail-side epair end into the jail's VNET and assigns the IP.

### Automatic bridges

If you do not name a bridge with `--bridge`, Hospitus uses its default bridge
`hospitus0` and creates it on demand. The bridge prefix is configurable
(`bridge_prefix`, default `hospitus`), so the first auto-bridge is `hospitus0`.

```bash
# Uses the default bridge hospitus0
hospitus jail create app --image 14.3-RELEASE-amd64 --vnet --ip dhcp

# Attach to a specific, pre-existing bridge
hospitus jail create app --image 14.3-RELEASE-amd64 --vnet --bridge vmbr0 --ip dhcp
```

Custom bridges that Hospitus should *not* manage must be created beforehand:

```bash
doas ifconfig bridge create name vmbr0
doas ifconfig vmbr0 up
```

Verify the bridge and its members:

```bash
ifconfig hospitus0
# hospitus0: flags=8843<UP,BROADCAST,RUNNING,SIMPLEX,MULTICAST> ...
#         inet 10.0.0.1 netmask 0xffffff00 ...
#         member: epair0a ...
```

### Adding extra interfaces

A VNET jail can hold several interfaces — for example one on a public bridge and
one on an internal bridge. Use `hospitus jail network`:

```bash
# Add a second interface on an internal bridge
doas hospitus jail network add web \
  --bridge internal0 \
  --ipv4 172.16.0.10/24 \
  --name eth1

hospitus jail network list web
doas hospitus jail network remove web eth1
```

Each `network add` creates a new epair and attaches it to the named bridge,
creating the bridge if it is not there.

A jail has one default route however many interfaces it holds, and it goes to
the interface on the bridge Hospitus NATs from — `hospitus0` unless `bridge_prefix`
says otherwise. An interface on an internal segment carries traffic for that
segment and nothing else, which is what an internal segment is for. If none of
the jail's interfaces is on the NAT bridge, the first addressed one takes the
route.

IPv6 is supported via `--ipv6`, `--slaac`, and `--dhcpv6`.

## NAT and the PF anchor

For jails on private ranges (`10.x`, `172.16.x`, `192.168.x`) to reach the
Internet, their traffic must be NATed to the host's external interface. Hospitus
does this with PF, and it keeps **all** of its rules inside a dedicated PF
**anchor** named `hospitus` (configurable via `pf_anchor_name`).

### How Hospitus isolates its rules

Hospitus never mixes its NAT and redirect rules into your hand-written filter
rules. Instead it writes them to files under `/var/lib/hospitus/firewall/pf/` and
loads them into the `hospitus` anchor:

```
/var/lib/hospitus/firewall/pf/
├── nat.rules        # NAT (translation) rules
├── rdr.rules        # port-forwarding (rdr) rules
├── filter.rules     # the pass rules that let NATed traffic through
└── combined.rules   # the three above, in the order PF requires, loaded
                     # into the hospitus anchor
```

Each rule carries the instance it belongs to:

```
# ID: app-web-nat instance:app-web
nat on re0 inet from 10.50.0.0/24 to any -> (re0)
```

Two jails on one subnet therefore produce two identical `nat` lines, which
`pfctl -a hospitus -s nat` shows side by side. That is deliberate: destroying one
jail removes its own line and leaves the other's NAT working.

Your existing `pass`/`block`/`match` rules are left untouched.

### Bootstrapping the anchor into pf.conf

An anchor only takes effect once it is *declared* in the active ruleset. Hospitus
**never edits your `pf.conf`** — you add the declaration yourself, one time. On
startup hospitusd checks `pfctl -sn` for a `nat-anchor "hospitus"` line; if it is
missing, hospitusd logs the exact lines to add and keeps running, but NAT and port
forwarding stay inactive until you declare the anchor.

Add these three lines to `/etc/pf.conf`. PF requires translation anchors
(`nat-anchor`, `rdr-anchor`) to precede filter rules, so place them accordingly:

```pf
nat-anchor "hospitus"
rdr-anchor "hospitus"
anchor "hospitus"
```

Then reload PF:

```bash
doas pfctl -f /etc/pf.conf
```

hospitusd loads its rules into the anchor at runtime (`pfctl -a hospitus -f`), so you
do not add a `load anchor` line — declaring the anchor is all that is needed.

Inspect the live anchor at any time:

```bash
doas pfctl -a hospitus -s nat      # NAT rules Hospitus installed
doas pfctl -a hospitus -s rules    # rdr/filter rules in the anchor
```

### NAT configuration keys

NAT behavior is controlled by the daemon config file (see
[IP pool configuration](#ip-pool-configuration) for file locations). These are
the **only** networking keys hospitusd reads:

| Key | Default | Meaning |
|-----|---------|---------|
| `enable_nat` | `true` | Install NAT rules for private ranges |
| `enable_ip_forwarding` | `true` | Set `net.inet.ip.forwarding=1` |
| `external_interface` | auto-detect | Uplink interface for NAT (e.g. `em0`) |
| `nat_network` | `auto` | Network to NAT; `auto` derives it from the IP pool |
| `firewall_type` | `pf` | Firewall backend |
| `pf_anchor_name` | `hospitus` | Name of the PF anchor Hospitus owns |
| `default_gateway` | auto | Gateway advertised to workloads |
| `ip_pool` | `10.0.0.0/24` | DHCP-style address pool (see below) |
| `auto_create_bridges` | `true` | Create missing bridges automatically |
| `bridge_prefix` | `hospitus` | Prefix for auto-created bridges (`hospitus0`, …) |
| `enable_ipv6` | `false` | Enable IPv6 allocation |
| `ipv6_prefix` | `fd00::/48` | ULA prefix for jail IPv6 allocation |

> **bhyve NAT bridge.** For bhyve VMs using `type = "nat"`, hospitusd also creates a
> dedicated NAT bridge `hospitus-nat` (`10.10.0.1/24`) and requires `dnsmasq`
> for DHCP. See the [bhyve setup guide](../guides/bhyve-setup-guide.md).

## Port forwarding

Expose a service running inside a jail on a host port. This adds a PF `rdr` rule
to the `hospitus` anchor; Hospitus resolves the jail's current IP automatically.

```bash
# Forward host:80 → jail:8080 (TCP)
doas hospitus jail expose add web --port 80:8080

# Same port on host and jail
doas hospitus jail expose add web --port 443

# UDP
doas hospitus jail expose add dns --port udp/53

# Explicit protocol with mapping
doas hospitus jail expose add web --port tcp/80:8080
```

Port specification formats accepted by `--port`:

| Spec | Meaning |
|------|---------|
| `80` | Host 80 → jail 80, TCP |
| `80:8080` | Host 80 → jail 8080, TCP |
| `tcp/80:8080` | Explicit TCP |
| `udp/53` | Host 53 → jail 53, UDP |

List and remove rules:

```bash
hospitus jail expose list web
doas hospitus jail expose remove web --port 80 --protocol tcp
```

bhyve and QEMU VMs expose ports with the identical interface
(`hospitus bhyve expose …`, `hospitus qemu expose …`).

## IP address configuration

Hospitus provides a DHCP-like allocator compatible with CBSD's `nodeippool`,
alongside static assignment.

### Static IP

```bash
hospitus jail create myjail --image 14.3-RELEASE-amd64 --vnet --ip 192.168.1.100/24
```

### Automatic (DHCP-style) allocation

```bash
hospitus jail create myjail --image 14.3-RELEASE-amd64 --vnet --ip dhcp
```

With `dhcp`, Hospitus:

1. Finds the first free address in the configured IP pool(s).
2. Assigns it to the jail.
3. Persists the allocation, reusing the same IP on restart.

## IP pool configuration

### Pool formats

Hospitus accepts three formats, identical to CBSD:

```bash
# 1. CIDR — a whole network range
192.168.1.0/24    # 192.168.1.1 … 192.168.1.254
10.0.0.0/16       # 10.0.0.1 … 10.0.255.254

# 2. Range — last-octet span
192.168.1.10-50   # 192.168.1.10 … 192.168.1.50

# 3. Single address
192.168.1.100
```

Combine multiple pools by separating them with spaces. Hospitus tries each pool in
order and moves to the next when one is exhausted.

### Configuration sources and precedence

Pools can be set in three ways. Highest priority wins:

| Priority | Source |
|----------|--------|
| 1 (highest) | `HOSPITUS_IP_POOL` environment variable |
| 2 | Daemon config file (`ip_pool = "…"`) |
| 3 (lowest) | Built-in default `10.0.0.0/24` |

#### Config file (recommended for production)

hospitusd reads the first file found in this order:

- `/usr/local/etc/hospitus/hospitusd.conf` (FreeBSD)
- `/etc/hospitus/hospitusd.conf` (Linux)

Those two and no others, all root-owned: hospitusd runs as root, so a per-user or
working-directory config would let an unprivileged user steer the daemon. There
is no flag or environment variable to point it elsewhere.

The format is `key = value`, one per line; `#` starts a comment and quotes are
optional:

```conf
# /usr/local/etc/hospitus/hospitusd.conf
ip_pool = "192.168.1.0/24"

# Multiple pools, space-separated
# ip_pool = "192.168.1.10-50 10.0.0.0/24"

external_interface = "em0"
enable_nat = true
```

A complete annotated example ships as `etc/hospitus/hospitusd.conf.example`.

#### Environment variable (temporary override)

`HOSPITUS_IP_POOL` overrides the config file — handy for testing without editing
production files:

```bash
doas env HOSPITUS_IP_POOL="192.168.1.10-50 10.0.0.0/24" hospitusd
```

## Worked examples

### Home lab on `192.168.1.x`

```bash
cat > /usr/local/etc/hospitus/hospitusd.conf <<'EOF'
ip_pool = "192.168.1.100-200"
external_interface = "em0"
EOF

doas service hospitus start

hospitus jail create web   --image 14.3-RELEASE-amd64 --vnet --ip dhcp   # → 192.168.1.100
hospitus jail create db    --image 14.3-RELEASE-amd64 --vnet --ip dhcp   # → 192.168.1.101
hospitus jail create cache --image 14.3-RELEASE-amd64 --vnet --ip dhcp   # → 192.168.1.102
```

> **Do not point the pool at the network the host is already on.** Hospitus gives
> the bridge the first address of the pool's network, so `192.168.1.100-200`
> puts `192.168.1.1` on `hospitus0` — the address most home routers answer to. The
> host then has two interfaces on one subnet and routing breaks. Pick a range
> the LAN does not use (`10.0.0.0/24` is the default for that reason), or
> attach the jails to a bridge over the physical interface with `--bridge` and
> let the LAN's own DHCP server address them.

### Multiple segments with overflow

```bash
doas env HOSPITUS_IP_POOL="192.168.1.10-50 172.16.0.0/24" hospitusd

hospitus jail create dmz01 --image 14.3-RELEASE-amd64 --vnet --ip dhcp   # → 192.168.1.10
# When 192.168.1.10-50 is exhausted, allocation continues in 172.16.0.0/24
```

### Mixed static and dynamic

```bash
doas env HOSPITUS_IP_POOL="192.168.1.100-200" hospitusd

# Pin infrastructure to fixed addresses
hospitus jail create dns --image 14.3-RELEASE-amd64 --vnet --ip 192.168.1.53/24
# Everything else gets DHCP-style addresses
hospitus jail create dev1 --image 14.3-RELEASE-amd64 --vnet --ip dhcp    # → 192.168.1.100
```

## Troubleshooting

### "No available IP addresses in configured pools"

Every address in the pool is allocated.

- Free unused jails: `hospitus jail destroy oldjail -y`
- Add or widen pools: `HOSPITUS_IP_POOL="192.168.1.0/24 192.168.2.0/24"`
- Use a larger CIDR: `HOSPITUS_IP_POOL="10.0.0.0/16"`

### Jails have no Internet access

1. Confirm PF is enabled: `doas pfctl -s info | grep Status` → `Enabled`.
2. Confirm the anchor is wired in: `doas pfctl -sn | grep hospitus`.
3. Confirm NAT rules exist: `doas pfctl -a hospitus -s nat`.
4. Check IP forwarding: `sysctl net.inet.ip.forwarding` → `1`.
5. Confirm the uplink: set `external_interface` if auto-detection picked the
   wrong NIC.

### Anchor not loading

hospitusd never edits `pf.conf`; when the anchor is missing it logs what to add
and carries on:

```
Add these lines to /etc/pf.conf, then run: pfctl -f /etc/pf.conf
```

Add the anchor lines from the
[NAT section](#bootstrapping-the-anchor-into-pfconf), then
`doas pfctl -f /etc/pf.conf`. NAT and port forwarding stay inactive until you
do.

### IP conflict with an existing device

The pool overlaps addresses already in use on your LAN. Hospitus checks before
giving a bridge its gateway address and refuses rather than warning — the jail
fails to start with `IP conflict check failed for bridge …`. Choose a
non-overlapping range, e.g. `HOSPITUS_IP_POOL="192.168.100.0/24"`.

### Inspecting current state

```bash
hospitus jail list                 # allocated IPs per instance
hospitus jail info myjail           # detailed network view for one jail
ifconfig hospitus0                  # bridge + members
doas pfctl -a hospitus -s nat       # active NAT rules
```

## Migration from CBSD

The IP pool syntax is a drop-in match for CBSD's `nodeippool`:

```bash
# CBSD
nodeippool="192.168.1.0/24 10.0.0.0/24"

# Hospitus (env or config file)
ip_pool = "192.168.1.0/24 10.0.0.0/24"
```

CIDR, range, single-IP, and multi-pool forms all behave identically.

## Best practices

1. **Plan ranges by purpose** — reserve blocks for DMZ, internal, and dev.
2. **CIDR for scale, ranges for clarity** — `/24` gives 254 hosts; a `10-50`
   range reads better for a handful of jails.
3. **Set `external_interface` explicitly** on multi-NIC hosts to avoid
   mis-detected NAT uplinks.
4. **Pre-declare the PF anchor** in `pf.conf` if you manage the firewall by
   hand, so hospitusd never rewrites it.
5. **Keep pools off your physical LAN range** to avoid address conflicts.

## See also

- [Storage & Snapshots](storage.md)
- [CLI Reference](cli-reference.md)
- [Jail Networking Walkthrough](../guides/jail-networking-walkthrough.md)
- [bhyve Setup Guide](../guides/bhyve-setup-guide.md)
- [Remote Access & Multi-Server Management](remote.md)
