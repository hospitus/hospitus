# PF Integration

Hospitus needs FreeBSD's Packet Filter (PF) to provide outbound NAT and inbound
port-forwarding for VNET jails and NAT-mode bhyve VMs. It does this **without ever
editing your `/etc/pf.conf`**: Hospitus manages a small set of rules inside its own
`hospitus` PF anchor, and you declare that anchor in your ruleset once. This guide
explains the model, how to wire it up with `hospitus init`, and how to verify it.

> **Design contract:** Hospitus never modifies `/etc/pf.conf`. It only reports the
> anchor lines you need to add, and manages the contents of the `hospitus` anchor at
> runtime. Your main ruleset stays under your control.

---

## The anchor model

An *anchor* is a named sub-ruleset that PF evaluates as part of your main policy.
Hospitus writes its NAT, redirect, and filter rules into files under
`/var/lib/hospitus/firewall/pf/` and loads them into the `hospitus` anchor. Because the
rules live in the anchor, Hospitus can add and remove them as jails and VMs start and
stop, and your hand-written `pf.conf` is never touched.

Hospitus maintains four rule files:

| File | Contents |
|------|----------|
| `nat.rules` | Outbound NAT rules (`nat on <iface> ...`) |
| `rdr.rules` | Port-forwarding redirect rules (`rdr pass ...`) |
| `filter.rules` | Filter `pass` rules that let jail/VM traffic through |
| `combined.rules` | The above concatenated in PF section order (translation, then filter) |

The `combined.rules` file is what the `hospitus` anchor loads.

---

## Declaring the anchor

You add three anchor declarations to your ruleset once. PF enforces strict
section ordering: translation rules (`nat-anchor`, `rdr-anchor`) must appear
**before** any filter rule (`pass`, `block`, `match`, `antispoof`); the filter
`anchor` goes in the filter section.

```pf
# --- Translation section: after scrub/normalization, before any pass/block ---
nat-anchor "hospitus"
rdr-anchor "hospitus"

# ... your existing filter rules ...

# --- Filter section: at the end ---
anchor "hospitus"
```

You do **not** need a `load anchor … from …` line: hospitusd loads its rules into
the anchor at runtime with `pfctl -a hospitus -f`. Declaring the anchor is enough.

## Rules of your own

The anchor belongs to the daemon. It rewrites it whenever it creates or destroys
an instance, and a rule added there by hand disappears without a message — the
symptom simply returns later, which makes it an unpleasant place to keep
anything.

For rules Hospitus does not generate — a network it does not manage, such as
Podman's — use a file of your own that pf.conf includes:

```pf
# --- Filter section: at the end ---
include "/usr/local/etc/hospitus/pf.conf"
anchor "hospitus"
```

**Create the file before adding that line.** PF refuses an entire ruleset whose
include names a file that is not there:

```
pfctl: pushfile: /usr/local/etc/hospitus/pf.conf: No such file or directory
/etc/pf.conf:33: failed to include file /usr/local/etc/hospitus/pf.conf
```

`service pf reload` then fails and the host keeps whatever was loaded before, so
the order matters:

```sh
doas install -m 0644 /dev/null /usr/local/etc/hospitus/pf.conf   # empty is fine
# ...now add the include line to pf.conf...
doas pfctl -n -f /etc/pf.conf                                 # check before loading
doas service pf reload
```

`hospitus init --check` reports the file's absence, and `hospitus init` creates it.
Hospitus never writes it afterwards: what goes in it is yours.

Then reload PF:

```bash
doas pfctl -f /etc/pf.conf
```

You do not have to memorise these lines — `hospitus init` prints them for your host.

---

## Wiring it up with `hospitus init`

`hospitus init` is the host doctor. It inspects everything Hospitus needs — ZFS, kernel
modules, sysctls, and PF — and, for PF, tells you exactly which anchor lines are
missing.

```bash
# Read-only report; exits non-zero if a required prerequisite is missing.
# Run it as root — the PF section needs root and is skipped otherwise
# ("rerun with doas").
doas hospitus init --check
```

If the `hospitus` anchor is not yet declared, the PF section of the report lists the
three anchor declarations above. Add them to `/etc/pf.conf` at the positions
shown, then reload PF. Because Hospitus never rewrites your ruleset, this is a
deliberate, one-time, you-controlled edit.

The interactive wizard (`hospitus init`) and the non-interactive `hospitus init --auto`
apply the *safe* fixes they can (loading modules, setting sysctls, and so on). PF
ruleset edits are left to you by design — run `hospitus init --check` to see what to
add, make the change, and reload.

> **What the daemon does at startup:** `hospitusd` creates the rule files and loads
> its anchor. If the `hospitus` anchor is not present in the active ruleset, it logs a
> warning naming the exact three anchor lines to add and continues without
> NAT/redirect support until you declare the anchor and reload PF.

---

## Filter rules and a `block all` policy

Many production rulesets use a default-deny policy:

```pf
block all                    # no "quick": the last matching rule wins
pass out on $egress inet keep state
```

Without explicit pass rules, jail and VM packets are dropped. Hospitus handles this
inside its anchor: when a VNET jail starts and a NAT rule is added, two companion
filter pass rules are written to `filter.rules` for the jail subnet:

```pf
pass in  inet from 10.0.0.0/24 keep state
pass out inet to   10.0.0.0/24 keep state
```

Because the `anchor "hospitus"` directive sits at the **end** of the filter section —
after a non-`quick` `block all` — these are the last matching rules and therefore
win. State tracking then handles return traffic automatically.

| Rule | Direction | Covers |
|------|-----------|--------|
| `pass in inet from 10.0.0.0/24` | Inbound at host | Jail → internet (and jail → host) |
| `pass out inet to 10.0.0.0/24` | Outbound from host | Host and forwarded clients → jail |

---

## Verifying the configuration

```bash
# 1. PF must be enabled
doas pfctl -s info | grep Status
# Status: Enabled ...

# 2. The hospitus nat-anchor should appear in the active ruleset
doas pfctl -sn | grep hospitus
# nat-anchor "hospitus" ...

# 3. Rules currently loaded in the anchor (one NAT rule per running VNET jail)
doas pfctl -a hospitus -s nat
doas pfctl -a hospitus -s rules

# 4. The generated rules file
cat /var/lib/hospitus/firewall/pf/combined.rules

# 5. End-to-end: reach the internet from inside a jail
doas jexec <jail> ping -c 3 1.1.1.1
```

If step 2 shows nothing, the anchor is not declared — run `hospitus init --check`,
add the reported lines, and `doas pfctl -f /etc/pf.conf`.

---

## Notes and limitations

- The filter pass rules use the whole Hospitus IP pool subnet (`10.0.0.0/24` by
  default) rather than per-jail addresses. This is scoped to the Hospitus pool and is
  intentional for a private deployment.
- IPv4 NAT is generated for VNET jails; IPv6 NAT for jails is not currently added.
  (bhyve NAT mode can enable IPv6 ULA NAT separately via `--ipv6`.)
- Hospitus only ever manages the `hospitus` anchor. Removing the anchor lines from
  `pf.conf` and reloading disables Hospitus NAT/port-forwarding cleanly, without
  leaving anything behind in your main ruleset.

---

## See Also

- [Port Forwarding](port-forwarding.md) — exposing jail services on the host
- [Jail Networking Walkthrough](jail-networking-walkthrough.md) — VNET, bridges, NAT end to end
- [Networking Problems](../troubleshooting/networking.md) — diagnosing connectivity issues
