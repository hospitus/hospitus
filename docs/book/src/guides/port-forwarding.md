# Port Forwarding Walkthrough

This guide walks you through exposing jail services to the host network using
Hospitus port forwarding.

## Prerequisites

- A running Hospitus daemon (`hospitusd`)
- PF enabled: `doas sysrc pf_enable=YES && doas service pf start`
- IP forwarding enabled: `doas sysctl net.inet.ip.forwarding=1` (hospitusd also
  enables it at startup unless the config turns that off)
- A running VNET jail (jails without VNET cannot use port forwarding)

## Quick Start

```bash
# Create a VNET jail
hospitus jail create web --image 14.3-RELEASE-amd64 --vnet --ip dhcp

# Start it
hospitus jail start web

# Expose port 80 inside the jail as port 8080 on the host
hospitus jail expose add web --port 8080:80

# Check the rule is loaded. PF prints a port by its service name, so 8080
# appears as "http-alt" and 80 as "http".
doas pfctl -a hospitus -s nat | grep rdr

# Verify it works — from another machine, not from this host
curl http://<host-ip>:8080
```

## Step-by-Step Walkthrough

### 1. Create a VNET Jail

Port forwarding requires VNET networking. The `--vnet` flag enables it:

```bash
hospitus jail create nginx-jail --image 14.3-RELEASE-amd64 \
  --vnet \
  --ip dhcp \
  --cpus 1 \
  --memory 512
```

`--ip` is what makes the jail reachable. Without it the jail starts on a network
with no address — hospitusd says so, "network has no address; no interface will be
created for it" — and the next step cannot resolve a name, let alone install
anything.

### 2. Start the Jail and Install a Service

```bash
hospitus jail start nginx-jail

# Install nginx inside the jail
hospitus jail exec nginx-jail -- env IGNORE_OSVERSION=yes ASSUME_ALWAYS_YES=yes pkg install -y nginx

# A jail may not bind a port below 1024 until it is allowed to
hospitus jail set nginx-jail --allow-reserved-ports
hospitus jail restart nginx-jail

# Start nginx
hospitus jail exec nginx-jail -- service nginx onestart
```

Without that permission nginx installs and then refuses to serve:

```
nginx: [emerg] bind() to 0.0.0.0:80 failed (13: Permission denied)
```

`--allow-reserved-ports` is also a create flag, so a jail meant to serve on a
low port can be given it from the start.

### 3. Add Port Forwarding

Expose jail port 80 on host port 8080:

```bash
hospitus jail expose add nginx-jail --port 8080:80
```

The `--port` value encodes the host port, the jail port, and (optionally) the
protocol. The protocol is set with a `tcp/` or `udp/` prefix on `add` — there is
no `--protocol` flag on `expose add`:

| Spec | Meaning |
|------|---------|
| `80` | Host 80 → jail 80, TCP |
| `8080:80` | Host 8080 → jail 80, TCP |
| `tcp/8080:80` | Explicit TCP |
| `udp/53` | Host 53 → jail 53, UDP |

For UDP (e.g., DNS):

```bash
hospitus jail expose add dns-jail --port udp/53
```

### 4. Verify the Forwarding Rule

```bash
# List active port forwards for the jail
hospitus jail expose list nginx-jail

# Check PF rules directly
doas pfctl -a hospitus -s nat
```

Expected output from `expose list`:

```
Port forwarding rules for jail nginx-jail:

PROTOCOL  HOST PORT  TARGET PORT  TARGET IP
tcp       8080       80           10.0.0.5
```

### 5. Test Connectivity

```bash
# From another machine on the network
curl http://YOUR_HOST_IP:8080
```

The host can test it too. A `rdr` rule only rewrites traffic arriving on the
external interface — the kernel handles loopback connections before PF sees
them — so hospitusd additionally runs a small TCP proxy on `127.0.0.1:<host port>`
that forwards to the jail. `curl http://localhost:8080` therefore reaches the
jail like any external client, unless something else on the host already owns
that port: hospitusd's own API port is the usual collision, and there the answer
comes from hospitusd rather than the jail.

### 6. Remove Port Forwarding

```bash
# Remove the rule for host port 8080
hospitus jail expose remove nginx-jail --port 8080

# Remove a UDP rule
hospitus jail expose remove nginx-jail --port 53 --protocol udp
```

## Multiple Ports

You can expose multiple ports for the same jail:

```bash
hospitus jail expose add web-jail --port 80:80    # HTTP
hospitus jail expose add web-jail --port 443:443  # HTTPS
hospitus jail expose add web-jail --port 22:22    # SSH
```

List all:

```bash
hospitus jail expose list web-jail
```

## How It Works

Hospitus uses PF `rdr` (redirect) rules under the `hospitus` anchor:

```pf
# ID: <rule-id> instance:nginx-jail
rdr pass on em0 proto tcp from any to any port 8080 -> 10.0.0.5 port 80
```

Each rule in the file is preceded by exactly that `# ID:` comment, which is how
Hospitus finds and removes its own rules again. The anchor system ensures Hospitus
never modifies your main PF ruleset. Rules are automatically removed when the
jail is stopped or destroyed. Loopback traffic is handled by the localhost
proxy described above, not by a PF rule.

## Troubleshooting

### Port Forward Not Working

1. **Verify PF is running:**
   ```bash
   pfctl -s info | grep "Status:"
   # Should be "Status: Enabled"
   ```

2. **Check the rule was created:**
   ```bash
   doas pfctl -a hospitus -s nat
   ```

3. **Ensure IP forwarding is enabled:**
   ```bash
   sysctl net.inet.ip.forwarding
   # Should be 1
   ```

4. **Check the jail is running and has a VNET IP:**
   ```bash
   hospitus jail info nginx-jail
   # Look for the "IPv4:" line in the output
   ```

5. **Verify the service is listening inside the jail:**
   ```bash
   hospitus jail exec nginx-jail -- sockstat -l4 | grep 80
   ```

### "Firewall not configured" Error

PF must be enabled **before** hospitusd starts:

```bash
doas sysrc pf_enable=YES
doas service pf start
doas service hospitus restart
```

### Port Already in Use

Each host port can only be forwarded to one destination:

```bash
# Will fail if 8080 is already forwarded
hospitus jail expose add other-jail --port 8080:80
# Error: host port 8080/tcp is already in use by rule <rule-id>
```

List the forwards for a specific jail, or inspect the loaded PF rules directly:

```bash
hospitus jail expose list nginx-jail
doas pfctl -a hospitus -s nat
```

## See Also

- [Jail Networking Walkthrough](jail-networking-walkthrough.md)
- [PF Autoconfiguration](pf-autoconfiguration.md)
- [Troubleshooting Networking](../troubleshooting/networking.md)
