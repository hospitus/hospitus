# Networking Problems

Hospitus automates FreeBSD networking for VNET jails: it creates epair interfaces,
attaches them to a bridge (default `hospitus0`), allocates IPs, and installs NAT and
port-forward rules under a dedicated PF anchor (`hospitus`). When something breaks,
the fault is almost always in one of those layers. This chapter is organized by
layer, with a diagnostic first and a fix second.

> **Let Hospitus own its rules.** Hospitus regenerates its PF anchor and bridge/epair
> setup from its own state. Do **not** hand-inject rules into the `hospitus`
> anchor with `pfctl -f` — you will overwrite what Hospitus manages and the next
> daemon restart will undo your edit anyway. Manage forwards with
> `hospitus jail expose`, and reload rules by restarting the service. There is no
> `hospitus firewall reload` command.

## One-shot diagnostic

Run this on the host to snapshot every layer at once:

```sh
#!/bin/sh
echo "== bridge =="            ; ifconfig hospitus0 2>/dev/null || echo "hospitus0 missing"
echo "== ip forwarding =="     ; sysctl net.inet.ip.forwarding
echo "== PF status =="         ; doas pfctl -s info | head -3
echo "== hospitus NAT rules =="   ; doas pfctl -a 'hospitus' -s nat  2>/dev/null || echo "none"
echo "== hospitus rdr/filter =="  ; doas pfctl -a 'hospitus' -s rules 2>/dev/null || echo "none"
echo "== epair members =="     ; ifconfig | grep -E '^epair|member'
```

## No internet access from a jail

**Diagnose** from inside the jail:

```sh
hospitus jail exec web ifconfig            # does it have an IP?
hospitus jail exec web netstat -rn | grep default   # is there a default route?
hospitus jail exec web cat /etc/resolv.conf         # is DNS configured?
hospitus jail exec web ping -c3 10.0.0.1            # can it reach the gateway (bridge IP)?
hospitus jail exec web ping -c3 8.8.8.8             # can it reach the internet by IP?
```

Work outward: gateway first, then internet-by-IP, then DNS.

**Fixes by cause:**

1. **No default route.** The jail should route via the bridge IP. If it is
   missing, the jail was created without a gateway; recreate it with the correct
   network settings, or set it temporarily:

   ```sh
   hospitus jail exec web route add default 10.0.0.1
   ```

2. **NAT is not translating.** Confirm the NAT rule exists under the anchor:

   ```sh
   doas pfctl -a 'hospitus' -s nat
   ```

   If it is empty, Hospitus did not install NAT. Ensure PF is enabled and restart
   the daemon so it re-applies its rules:

   ```sh
   doas sysrc pf_enable=YES
   doas service pf start
   doas service hospitus restart
   ```

3. **IP forwarding is off** (required for NAT):

   ```sh
   doas sysctl net.inet.ip.forwarding=1
   echo 'net.inet.ip.forwarding=1' | doas tee -a /etc/sysctl.conf
   ```

4. **DNS only.** If ping-by-IP works but names don't resolve, fix resolv.conf:

   ```sh
   hospitus jail exec web sh -c 'printf "nameserver 8.8.8.8\nnameserver 1.1.1.1\n" > /etc/resolv.conf'
   ```

## Jails can't reach each other

**Diagnose:**

```sh
hospitus jail list                       # note each jail's IP
hospitus jail exec web ping -c3 10.0.0.3 # from one jail to another
ifconfig hospitus0                       # both epairs should be members
```

**Fixes:**

1. **Different bridges.** Jails only talk directly when on the same bridge.
   Confirm both epair interfaces appear as `member:` of `hospitus0`.

2. **An epair is down.** Only the a-side is on the host — the b-side was moved
   into the jail's VNET and is not visible or configurable from here. Bring the
   host side up (Hospitus normally does this):

   ```sh
   ifconfig epair0a
   doas ifconfig epair0a up
   ```

   To check the jail's side, look from inside it:
   `hospitus jail exec web ifconfig`.

3. **PF is blocking inter-jail traffic.** Test by briefly disabling PF — only on
   a host you control:

   ```sh
   doas pfctl -d          # disable
   hospitus jail exec web ping -c3 10.0.0.3
   doas pfctl -e          # re-enable
   ```

   If traffic flows only with PF off, your host ruleset (not the `hospitus`
   anchor) is blocking the jail subnet. Add a pass rule for it in
   `/etc/pf.conf`.

## Bridge `hospitus0` not found

**Cause** — the bridge was not created, or the `if_bridge` module is not loaded.
Hospitus auto-creates its bridge, but only if bridge auto-creation is enabled and
the module is present.

**Fix:**

```sh
kldstat | grep if_bridge || doas kldload if_bridge
echo 'if_bridge_load="YES"' | doas tee -a /boot/loader.conf   # persist

# Let Hospitus recreate its bridge on restart.
doas service hospitus restart
```

If you prefer a manually managed, persistent bridge, define it in `rc.conf` and
tell Hospitus to use it (leave `auto_create_bridges` at its default and it will use
an existing bridge of the configured name):

```sh
doas sysrc cloned_interfaces+="bridge0"
doas sysrc ifconfig_bridge0_name="hospitus0"
doas sysrc ifconfig_hospitus0="inet 10.0.0.1/24 up addm epair..."   # members as needed
```

## Port forwarding doesn't work

**Manage forwards with the CLI**, never by editing the anchor:

```sh
hospitus jail expose add    web --port 8080:80        # tcp/8080:80 for an explicit protocol
hospitus jail expose list   web
hospitus jail expose remove web --port 8080 --protocol tcp
```

`expose add` has no `--protocol` flag — the protocol belongs in the port spec
(`tcp/8080:80`, `udp/53`), and TCP is the default. Only `remove` takes
`--protocol`.

**Diagnose a forward that isn't working:**

```sh
# 1. The rdr rule exists under the anchor. rdr rules are NAT rules: -s rules
#    lists the filter rules and will never show them.
doas pfctl -a 'hospitus' -s nat

# 2. The service is actually listening inside the jail.
hospitus jail exec web sockstat -4 -l | grep ':80'

# 3. Reach it from another host on the network.
curl -v http://<host-ip>:8080
```

**Fixes:**

1. **Nothing listening in the jail.** Start the service inside the jail; a
   forward to a closed port silently fails.

2. **rdr rule missing.** Re-add the expose entry, then confirm it appears. If the
   whole `hospitus` anchor is empty, PF may be disabled or the daemon has not
   installed rules — enable PF and restart the service (see below).

3. **Testing from the host itself.** By default a `rdr` rule does not apply to
   traffic that originates on the host. Test from another machine, or hit the
   jail's address directly:

   ```sh
   curl http://10.0.0.10:80        # instead of curl http://localhost:8080
   ```

## PF anchor is empty after enabling PF

**Symptom** — you just enabled PF and the `hospitus` anchor has no rules, so NAT
and forwards are dead.

**Cause** — Hospitus installs its rules when it initializes its firewall manager at
startup. If PF was enabled *after* the daemon started, the daemon never got to
install them.

**Fix — order matters:**

```sh
doas sysrc pf_enable=YES
doas service pf start
doas service hospitus restart      # daemon re-initializes and re-applies hospitus rules
```

Also confirm your `/etc/pf.conf` actually declares the Hospitus anchors — the
anchor rules do nothing unless the anchor is declared:

```sh
grep -n hospitus /etc/pf.conf
# Expected:
#   nat-anchor "hospitus"
#   rdr-anchor "hospitus"
#   anchor     "hospitus"
```

If they are missing, add them (see
[Host Setup](../getting-started/host-setup.md)) and reload:
`doas pfctl -f /etc/pf.conf`. Hospitus never adds these lines for you — `hospitusd`
only logs a warning when the anchor is absent — so a missing anchor means it was
never declared, or `pf.conf` was later replaced without it.

## IP allocation problems

### No IPs available / pool exhausted

**Diagnose:**

```sh
hospitus jail list                                   # see allocated addresses
grep ip_pool /usr/local/etc/hospitus/hospitusd.conf     # the configured pool
```

**Fix** — widen the pool in the network config file, then restart the daemon
(this file is the jail-network config described in
[Deployment → network configuration](../production/deployment.md#6-optional-network-configuration-file)):

```conf
# /usr/local/etc/hospitus/hospitusd.conf
ip_pool = 10.0.0.0/16
```

```sh
doas service hospitus restart
```

Or free addresses by destroying jails you no longer need:

```sh
hospitus jail destroy old-jail -y
```

### IP conflicts

**Diagnose:**

```sh
hospitus jail list | awk 'NR>1 {print $3}' | sort | uniq -d    # duplicate IPs (column 3)
ifconfig | grep '10.0.0'                                    # host also using the range?
```

**Fix** — recreate the offending jail with an explicit, unused address rather
than editing internal state by hand:

```sh
hospitus jail create web --image 14.3-RELEASE-amd64 --vnet --ip 10.0.0.50/24
```

Keep provider subnets from overlapping (for example jails on `10.0.1.0/24`, VMs
on `10.0.2.0/24`).

## VNET / epair issues

**Diagnose:**

```sh
doas jls -v | grep web        # confirm the jail is VNET
hospitus jail exec web ifconfig  # the vnet interface inside the jail
ifconfig | grep epair         # epair created on the host
```

**Fix** — load the required modules and let Hospitus rebuild the interface by
restarting the jail:

```sh
doas kldload if_epair if_bridge
echo 'if_epair_load="YES"'  | doas tee -a /boot/loader.conf
echo 'if_bridge_load="YES"' | doas tee -a /boot/loader.conf
hospitus jail stop web && hospitus jail start web
```

## A Podman container starts but serves nothing

The container runs, the server inside it is listening, and every connection to
it times out — from another container, from the host, even from the container
to its own address:

```sh
$ hospitus podman exec web wget -T 5 -qO /dev/null http://127.0.0.1:80
wget: download timed out
$ doas fetch -T 5 -qo /dev/null http://10.88.0.4:80
fetch: http://10.88.0.4:80: Permission denied
```

Both symptoms point at the same thing, and `sockstat` confirms the server is
fine:

```sh
$ doas sockstat -l -j "$(doas jls -h jid name | awk 'NR>1 {print $1}')"
USER COMMAND     PID FD PROTO LOCAL ADDRESS         FOREIGN ADDRESS
0    nginx      7303  6 tcp4  *:80                  *:*
```

The cause is PF, and the giveaway is `Permission denied`: a local socket gets
EACCES when PF blocks the packet on the way out. A default-deny `pf.conf`
usually passes traffic on the interfaces it knows about:

```
pass out on em0 inet all flags S/SA keep state
pass out on igc0 inet all flags S/SA keep state
block return all
```

Podman's bridge is not among them, so container traffic falls through to
`block return all`.

Hospitus does not fix this for you. Its anchor covers the networks it creates for
jails and VMs; the Podman network belongs to Podman, and Hospitus never edits
`pf.conf` (see [PF Auto-Configuration](../guides/pf-autoconfiguration.md)).
Add the rule yourself, using whatever subnet `podman network inspect` reports
— `10.88.0.0/16` by default:

```
pass quick inet from 10.88.0.0/16 to any keep state
pass quick inet from any to 10.88.0.0/16 keep state
```

Then `service pf reload`. Check it took:

```sh
doas pfctl -sr | grep 10.88
```

Two ways to place the rule that look right and are not:

- **The `hospitus` anchor.** `pfctl -a hospitus -f` loads rules that last until the
  daemon next writes that anchor, which it does on every instance it creates or
  destroys and on every restart. The rule disappears with no message, and the
  symptom returns.
- **A nested anchor.** `pfctl -a hospitus/mine -f` loads rules PF never evaluates,
  because nothing declares `anchor "mine"` inside `hospitus`. The rules list
  correctly under `pfctl -a hospitus/mine -sr` and do nothing.

`pf.conf` is the durable place. Rather than putting the rules in it directly,
keep them in a file of your own that it includes — see
[Rules of your own](../guides/pf-autoconfiguration.md), which also covers the
ordering trap: pf refuses the whole ruleset if the included file does not exist
yet.

## A jail with two networks reaches the internet only sometimes

A jail on two networks — an internal segment and the NAT bridge, which is what
a stack tier usually needs — installs a default route for each:

```sh
$ hospitus jail exec db netstat -rn | grep default
default            10.31.0.1          UGS         epair0b
default            10.0.0.1           UGS         epair1b
```

FreeBSD hashes between them when `net.route.multipath` is 1, which is the
default:

```sh
$ sysctl net.route.multipath
net.route.multipath: 1
```

Hospitus writes its NAT rule for one of the instance's networks, so roughly half
the connections leave through a gateway that translates nothing and time out.
The symptom is intermittent by construction: the same manifest installs its
packages on one deployment and fails on the next, and `pkg` reports it as a
name resolution problem rather than a routing one:

```
pkg: Error: Address family for host not supported
Address resolution failed for http://pkg.FreeBSD.org/...
```

Check for it directly — two `default` lines is the whole diagnosis. Until a
release settles which network carries the default route, the way round it is to
give an instance one network with a route off it, and reach the other tiers by
address on a segment that has none.

## Tracing traffic

When the layers above look correct but packets still don't flow, watch them:

```sh
# On the bridge (jail-side traffic).
doas tcpdump -n -i hospitus0

# On the external interface. Outbound packets are already translated here, so
# filtering on the jail's address matches nothing — filter on what it talks to.
doas tcpdump -n -i em0 host 8.8.8.8

# PF state table for a jail.
doas pfctl -s state | grep 10.0.0.10
```

To see PF drops, enable logging on a rule and read `pflog0`:

```sh
doas tcpdump -n -e -ttt -i pflog0
```

## See also

- [Common Issues](common-issues.md)
- [Networking (user guide)](../user-guide/networking.md)
- [PF Auto-Configuration](../guides/pf-autoconfiguration.md)
- [Port Forwarding](../guides/port-forwarding.md)
- [Jail Networking Walkthrough](../guides/jail-networking-walkthrough.md)
