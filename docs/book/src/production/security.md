# Security Hardening

This chapter is the security reference for a production Hospitus deployment: how
authentication actually works, how to enable TLS, what the built-in protections
are, and how to lock down the host around the daemon.

Two facts shape everything below, so start here:

1. **`hospitusd` is configured with command-line flags, not a config file or a Go
   struct.** Ignore any snippet that hands `hospitusd` an `api.ServerConfig{…}` —
   that is an internal type for embedding the server as a library, not something
   an operator edits. You harden the daemon with flags (and, on FreeBSD, the
   rc.d variables that build them).
2. **Authentication is on by default and fails closed.** If the daemon starts
   with no keys and without `--allow-no-auth`, it rejects *every* request. This
   is why the rc.d script generates a key on first boot.

## Security checklist

Before a Hospitus host faces anything beyond loopback:

- [ ] The daemon loads at least one API key (`--api-key-file` or `--api-key`).
- [ ] `--allow-no-auth` is **not** set.
- [ ] TLS is enabled (`--tls-cert` + `--tls-key`) — or a TLS-terminating reverse
      proxy sits in front and the daemon stays on loopback.
- [ ] `--allow-insecure-tls` is **removed** once the listen address is routable.
- [ ] PF restricts the API port to a management network.
- [ ] The API key file is mode 0600 and owned by root.
- [ ] `--metrics-public` is used only if you accept unauthenticated `/metrics`.
- [ ] Log rotation and audit-log monitoring are in place.
- [ ] A tested backup exists (see [Backup & Recovery](backup.md)).

## Authentication

### How the daemon decides its auth mode

`hospitusd` always wires up the authentication layer. The concrete behavior is
derived at startup from the keys it loaded and the `--allow-no-auth` flag:

| Keys loaded? | `--allow-no-auth` | Resulting mode |
|--------------|-------------------|----------------|
| Yes | (ignored) | **Enforced** — every request needs a valid key (keys are bcrypt-hashed in memory). |
| No | `true` | **Disabled** — all requests pass. Dev only. |
| No | `false` (default) | **Fail-closed** — every request is rejected with `401`. |

The fail-closed default is intentional: a misconfigured daemon refuses traffic
rather than silently serving everything unauthenticated.

### Supplying keys

There are three ways to load keys. Prefer a key file so secrets never appear in
`ps(1)` output.

**A key file — `--api-key-file` (recommended).** One key per line; blank lines
and lines starting with `#` are ignored. Keep it mode 0600.

```sh
doas install -d -m 0750 /usr/local/etc/hospitus
umask 077
# Generate a strong random key from the base system (no openssl dependency):
jot -r -w '%02x' 32 0 255 | tr -d '\n' | doas tee /usr/local/etc/hospitus/api.key >/dev/null
echo | doas tee -a /usr/local/etc/hospitus/api.key >/dev/null
doas chmod 0600 /usr/local/etc/hospitus/api.key
```

With the rc.d service this is the `hospitus_key_file` variable (default
`/usr/local/etc/hospitus/api.key`), which the script generates for you on first
start. To use a different path:

```sh
doas sysrc hospitus_key_file="/usr/local/etc/hospitus/api.key"
```

**Repeatable flag — `--api-key`** (handy for one-off or dev use; visible in
`ps`, so avoid on shared hosts):

```sh
doas hospitusd --api-key "$KEY1" --api-key "$KEY2" ...
```

**Environment variable — `HOSPITUS_API_KEY`** (singular). A single key, or several
separated by commas:

```sh
export HOSPITUS_API_KEY="key1,key2"
```

Keys from all three sources are merged. There is **no** `--api-keys` flag and
**no** `HOSPITUS_API_KEYS` environment variable.

### Presenting a key as a client

Clients authenticate with the **`X-API-Key` HTTP header** — this is the only
accepted mechanism. There is no query-parameter and no `Authorization: Bearer`
form.

```sh
curl -H "X-API-Key: $KEY" https://hospitus.example.com:8443/api/v1/instances
```

The `hospitus` CLI sends the header for you. Give it the key with the
`HOSPITUS_API_KEY` environment variable, the `--api-key` global flag, or a saved
[context](../user-guide/contexts.md):

```sh
export HOSPITUS_API_KEY="$KEY"
hospitus jail list

# or, without touching the environment:
hospitus --api-key "$KEY" --api-url https://hospitus.example.com:8443 jail list
```

`/health` is always reachable without a key (for load balancers and probes).
`/metrics` and `/metrics/prometheus` are unauthenticated only when the daemon
was started with `--metrics-public`; otherwise they require a key like any other
route.

### Key rotation and revocation

Keys live in the key file, so rotation is a file edit plus a restart:

1. Append a new key to `/usr/local/etc/hospitus/api.key`.
2. `doas service hospitus restart` (both old and new keys are now valid).
3. Move every client to the new key.
4. Remove the old key from the file and restart again.

To revoke a compromised key immediately, delete its line and restart. Because
keys are hashed in memory, there is no separate revocation store to clean up.

> **Fine-grained permissions and per-key management via `/api/v1/auth/keys` are
> part of the authentication subsystem but are not a stability-guaranteed
> operator surface.** Treat each key as full-access and
> issue one key per client so you can revoke them independently. See the
> Developer Manual's [Authentication](../developer-guide/authentication.md) page
> for the underlying model.

## TLS

`hospitusd` can terminate TLS itself. You do **not** need a reverse proxy — that is
one option, native TLS is another. Pick one; do not run plaintext on a routable
address.

### Option 1: native TLS in `hospitusd`

Point the daemon at a PEM certificate and key:

```sh
doas sysrc hospitus_addr="0.0.0.0:8443"
doas sysrc hospitus_flags="--tls-cert /usr/local/etc/hospitus/hospitusd.crt --tls-key /usr/local/etc/hospitus/hospitusd.key"
doas service hospitus restart
```

Setting `hospitus_flags` replaces the default `--allow-insecure-tls`, so plaintext
is no longer permitted — correct for an exposed daemon. There is no `--ca-cert`
flag; the daemon only needs its own certificate and key.

**Obtaining a certificate.** For a public name, use an ACME client such as
`acme.sh` or `certbot` to obtain `fullchain.pem` + `privkey.pem`, then point
`--tls-cert`/`--tls-key` at them and restart Hospitus on renewal:

```sh
# Example renewal hook — reload Hospitus after the cert is renewed.
0 3 * * * root certbot renew --quiet --deploy-hook 'service hospitus restart'
```

For an internal-only host, an internal CA is fine. A self-signed certificate
works but clients must trust it — either install the CA, or point the CLI at the
CA bundle:

```sh
hospitus --tls-ca /path/to/ca.pem --api-url https://hospitus.internal:8443 jail list
```

Use `hospitus --tls-skip-verify` only for throwaway testing; it disables
certificate verification entirely.

### Option 2: terminate TLS at a reverse proxy

Keep `hospitusd` on loopback (`127.0.0.1:8080`, default) and let nginx or HAProxy
handle TLS. The daemon still enforces API keys.

```nginx
server {
    listen 443 ssl http2;
    server_name hospitus.example.com;

    ssl_certificate     /usr/local/etc/letsencrypt/live/hospitus.example.com/fullchain.pem;
    ssl_certificate_key /usr/local/etc/letsencrypt/live/hospitus.example.com/privkey.pem;
    ssl_protocols       TLSv1.2 TLSv1.3;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        # Long-running exec/hooks: match the daemon's write timeout.
        proxy_read_timeout 30m;
    }
}
```

Because the proxy loopback-connects to the daemon, keep `--allow-insecure-tls`
(the loopback listener is plaintext) but ensure the port is not reachable from
elsewhere.

### TLS practices

- Serve TLS 1.2+ only; disable SSLv3/TLS 1.0/1.1.
- Automate renewal and restart Hospitus on deploy.
- Do not use `--tls-skip-verify` in production.

## Built-in request protections

Hospitus applies several protections in middleware, independent of any flag:

- **Authentication rate limiting.** Failed-auth attempts from an IP are
  throttled to blunt brute-force attacks; a client with a valid key is not
  throttled by its own successful traffic. A throttled client receives
  `429 Too Many Requests` with a `Retry-After` header.
- **Request rate limiting** governs overall throughput and also returns `429`
  when exceeded. It is **on by default** (10 requests/second sustained, burst
  20, per client) and is tunable: `--rate-limit=false` disables it,
  `--rate-limit-rps` and `--rate-limit-burst` change the budget.
- **Security headers and CORS** are set by the middleware.
- **Input validation at the API boundary** — for example, port-forward requests
  are validated (port 1–65535, protocol tcp/udp) before reaching a provider.

Apart from the three rate-limit flags, these use safe built-in defaults that no
`hospitusd` flag exposes; changing them requires embedding the server as a library.
There is no `--allowed-origins` flag.

## Secrets in manifests

Do not hardcode passwords or tokens in UWM manifests. Hospitus provides a secret
store and template functions so manifests reference secrets by name:

Secrets are addressed by scope (usually the workload or stack name) *and* name:

```sh
hospitus secret list [<scope>]
hospitus secret rotate <scope> <name>
hospitus secret rm <scope> <name>
```

Inside a manifest, the `secret` and `env` template functions pull values at
apply time instead of embedding them. See
[Variables & Secrets](../uwm/variables.md) for the full workflow.

## Host and network hardening

### Restrict the API port with PF

Only the management network should reach the API:

```pf
# /etc/pf.conf
admin_net = "10.0.1.0/24"
pass  in quick on egress proto tcp from $admin_net to (egress) port 8443 keep state
block in quick on egress proto tcp to (egress) port 8443
```

Hospitus keeps all of its own NAT, `rdr`, and filter rules isolated inside the
`hospitus` PF anchor, so they never mix into the host filter rules above. Hospitus
never edits `/etc/pf.conf`: you declare the `hospitus` anchor there once and Hospitus
only ever writes its own anchor files under `/var/lib/hospitus/firewall/pf/` — see
[Deployment → how Hospitus interacts with pf.conf](deployment.md#how-hospitus-interacts-with-pfconf).

### Run the daemon as root, deliberately

`hospitusd` must run as root to drive `jail(8)`, ZFS, bhyve, and PF. Reduce blast
radius around it instead of trying to drop privileges:

- Keep the data directory (`/var/lib/hospitus`, mode 0750) and state directory
  (`/var/lib/hospitus/state`, mode 0700) off world-readable paths — the daemon
  creates them with these modes.
- Restrict who can run the `hospitus` CLI and read the API key file.
- Administer over SSH or a VPN; do not expose the API to untrusted networks even
  with TLS.

### Keep the host patched

Track FreeBSD security advisories (`freebsd-update fetch install`) and rebuild
Hospitus from `main` when security fixes land.

## Auditing and monitoring

The daemon records authentication successes and failures and rate-limit
violations, and exposes them as metrics. Watch these signals:

- A sustained rise in `hospitus_auth_failures_total` — credential probing.
- `hospitus_rate_limit_violations_total` climbing — abuse or a misbehaving client.
- The `/security/status` endpoint returns a self-assessment (authentication on,
  TLS on, rate limiting on) plus a score — useful as a post-deploy smoke test:

  ```sh
  curl -s -H "X-API-Key: $KEY" http://127.0.0.1:8080/security/status | jq
  ```

Wiring these into Prometheus and alerts is covered in
[Monitoring](monitoring.md).

## See also

- [Deployment](deployment.md) — installing and running the service.
- [Monitoring](monitoring.md) — metrics, health, and alerting.
- [Backup & Recovery](backup.md) — protecting state and data.
- [Authentication & Permissions](../developer-guide/authentication.md) — the
  underlying auth model.
