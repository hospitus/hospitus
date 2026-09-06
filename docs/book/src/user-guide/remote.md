# Remote Access & Multi-Server Management

Hospitus supports managing **multiple hospitusd instances** from a single CLI.  
This page covers:

- Securing `hospitusd` with API keys and TLS
- Connecting `hospitus` to a remote daemon
- Using *contexts* to switch between servers

---

## Concepts

| Term | Meaning |
|------|---------|
| **hospitusd** | The daemon that runs on the managed host |
| **hospitus** | The CLI client — can be anywhere |
| **context** | A named connection profile (URL + API key + TLS settings) |
| **active context** | The context used when no `--context` flag is given |

---

## Securing hospitusd

### Step 1 — Generate an API key

Any random string works.  Use `openssl` for a cryptographically-secure key:

```sh
openssl rand -hex 32
# → a3f8c21e9d4b07...  (copy this value)
```

### Step 2 — Start hospitusd with the key

```sh
doas hospitusd \
  --data-dir /var/lib/hospitus \
  --db /var/lib/hospitus/hospitus.db \
  --api-key a3f8c21e9d4b07...
```

> **Security note:** The `--api-key` flag is visible to all users via `ps(1)`.
> For production, store keys in a file and use `--api-key-file` instead:
>
> ```sh
> echo "a3f8c21e9d4b07..." | doas tee /usr/local/etc/hospitus/api.key > /dev/null
> doas chmod 600 /usr/local/etc/hospitus/api.key
> doas hospitusd --api-key-file /usr/local/etc/hospitus/api.key ...
> ```
>
> You can also set the `HOSPITUS_API_KEY` environment variable (comma-separated for multiple keys).

You can add multiple keys (e.g. one per client):

```sh
# One key per line in the file
doas sh -c 'printf "%s\n%s\n" "key-for-laptop" "key-for-ci" > /usr/local/etc/hospitus/api.key'
doas chmod 600 /usr/local/etc/hospitus/api.key
doas hospitusd --api-key-file /usr/local/etc/hospitus/api.key ...
```

Without `--allow-no-auth`, every request must include a valid `X-API-Key` header.  
`--allow-no-auth` is **only** for local development — never use it on a networked host.

### Step 3 — Enable TLS (recommended for remote access)

#### Option A: Self-signed certificate (quick)

```sh
# Generate a self-signed cert valid for 10 years
openssl req -x509 -newkey rsa:4096 -nodes \
  -keyout /usr/local/etc/hospitus/hospitusd.key \
  -out    /usr/local/etc/hospitus/hospitusd.crt \
  -days   3650 \
  -subj   "/CN=hospitus.example.com" \
  -addext "subjectAltName=IP:192.168.1.10,DNS:hospitus.example.com" \
  -addext "extendedKeyUsage=serverAuth"

chmod 600 /usr/local/etc/hospitus/hospitusd.key
```

`extendedKeyUsage=serverAuth` is what keeps the error message honest. Without
it a macOS client rejects the certificate as "not standards compliant", which
reads as though the file were malformed rather than merely untrusted.

```sh

doas hospitusd ... \
  --tls-cert /usr/local/etc/hospitus/hospitusd.crt \
  --tls-key  /usr/local/etc/hospitus/hospitusd.key
```

The daemon now listens on `https://`.

#### Option B: Let's Encrypt (public hostname only)

Use `acme.sh` or `certbot` to obtain a certificate, then point hospitusd at the
resulting `fullchain.pem` and `privkey.pem`.

### Step 4 — Bind to the right address

By default hospitusd listens on `127.0.0.1:8080`.  
For remote access add `--addr 0.0.0.0:8443` (or a specific interface IP):

```sh
doas hospitusd \
  --addr    0.0.0.0:8443 \
  --tls-cert  /usr/local/etc/hospitus/hospitusd.crt \
  --tls-key   /usr/local/etc/hospitus/hospitusd.key \
  --api-key   a3f8c21e9d4b07... \
  --data-dir  /var/lib/hospitus \
  --db        /var/lib/hospitus/hospitus.db
```

### Step 5 — Let the port through the host firewall

Binding to `0.0.0.0` is not enough on a host whose PF ruleset ends in a default
deny, which is the usual shape. The daemon listens, answers `https://` from the
host itself, and stays unreachable from anywhere else — and the CLI reports
`connection refused`, which reads as though the daemon were down rather than
filtered.

Check what the ruleset does with the port before suspecting the daemon:

```sh
doas pfctl -s rules | grep -E '^block|port = 8443'
```

If nothing passes it, add a rule to `/etc/pf.conf` scoped to the hosts that need
access — Hospitus never edits that file — and reload:

```
pass in on $ext_if inet proto tcp from 192.0.2.0/24 to any port 8443 keep state
```

```sh
doas pfctl -f /etc/pf.conf
```

An SSH tunnel avoids the question, and is the better answer when a single
operator needs access:

```sh
ssh -f -N -L 18080:127.0.0.1:8080 freebsd-host
hospitus context add remote --url http://127.0.0.1:18080 --api-key "$KEY"
```

The daemon stays on the loopback and SSH authenticates the operator, so no port
is opened at all.

The tunnel carries whatever the daemon speaks. `http://` is right for a daemon
started with `--allow-insecure-tls`; one holding a certificate needs `https://`
and its CA, even through the tunnel, and answers a plain `http://` URL with:

```
Error: failed to list jails: the daemon is using TLS but this URL is http://
  → check which URL is in use with 'hospitus context show'
```

```sh
hospitus --api-url https://127.0.0.1:18080 --api-key "$KEY" \
      --tls-ca /path/to/hospitusd.crt jail list
```

---

## Connecting the CLI to a remote daemon

### Quick one-off (flags)

```sh
hospitus --api-url https://192.168.1.10:8443 \
      --api-key a3f8c21e9d4b07... \
      --tls-ca  /path/to/ca.crt \
      jail list
```

For a self-signed cert, copy the server's `.crt` file to the client and pass it with
`--tls-ca`.  Alternatively (insecure, for a quick test only):

```sh
hospitus --api-url https://192.168.1.10:8443 \
      --api-key a3f8c21e9d4b07... \
      --tls-skip-verify \
      jail list
```

### Using environment variables

```sh
export HOSPITUS_API_URL=https://192.168.1.10:8443
export HOSPITUS_API_KEY=a3f8c21e9d4b07...
hospitus jail list
```

---

## Contexts: managing multiple servers

A *context* is a named profile stored in `~/.config/hospitus/config.yaml`.  
It bundles a URL, API key, and TLS options so you don't repeat flags.

### Create contexts

```sh
# Local dev — no auth, plain HTTP
hospitus context add local --url http://127.0.0.1:8080

# Home lab — self-signed cert
hospitus context add lab \
  --url    https://192.168.1.10:8443 \
  --api-key hsp_lab-key \
  --tls-ca  ~/.hospitus/lab-ca.crt

# Production — public TLS (system CAs trusted automatically)
hospitus context add prod \
  --url    https://hospitus.example.com:8443 \
  --api-key hsp_prod-key
```

### List and switch contexts

```sh
hospitus context list
# ACTIVE  NAME   URL                              AUTH  TLS
# *       local  http://127.0.0.1:8080            none  default
#         lab    https://192.168.1.10:8443         key   custom-ca
#         prod   https://hospitus.example.com:8443    key   default

hospitus context use prod
# Switched to context "prod" (https://hospitus.example.com:8443)

hospitus jail list    # ← now talks to prod
```

### One-off context override

```sh
hospitus --context lab jail list   # use lab for this command only
```

### Show active context

```sh
hospitus context show
# Name:   prod
# URL:    https://hospitus.example.com:8443
# Auth:   API key (set)
# TLS:    default (system CAs)
```

### Remove a context

```sh
hospitus context remove lab
```

---

## Config file format

`~/.config/hospitus/config.yaml` (created automatically, permissions 0600):

```yaml
current_context: prod
contexts:
  - name: local
    url: http://127.0.0.1:8080
  - name: lab
    url: https://192.168.1.10:8443
    api_key: hsp_lab-key
    tls_ca_cert: /home/user/.hospitus/lab-ca.crt
  - name: prod
    url: https://hospitus.example.com:8443
    api_key: hsp_prod-key
```

You can edit the file directly; `hospitus context` commands read and write it.

---

## Flag precedence

When multiple configuration sources provide the same setting, Hospitus uses the
**highest-priority** one and ignores the rest:

| Priority | Source |
|----------|--------|
| 1 (highest) | `--api-url` / `--api-key` / `--tls-*` flags |
| 2 | Active context from config file |
| 3 | `HOSPITUS_API_URL` / `HOSPITUS_API_KEY` environment variables |
| 4 | The local daemon's key file, **only** for a loopback URL (`/usr/local/etc/hospitus/api.key`, or `~/Library/Application Support/hospitus/api.key` on macOS) |
| 5 (lowest) | Default: `localhost:8080`, no auth |

Step 4 is what makes `doas hospitus …` work on the host running the daemon, where
`doas` clears the environment and the context belongs to your own user. It
never applies to a remote URL.

---

## rc.d service (FreeBSD)

The service is installed as `hospitus` when using `make install` or installing from ports. Example `/etc/rc.conf` snippet:

```sh
hospitus_enable="YES"
hospitus_flags="--addr 0.0.0.0:8443 --tls-cert /usr/local/etc/hospitus/hospitusd.crt --tls-key /usr/local/etc/hospitus/hospitusd.key --api-key-file /usr/local/etc/hospitus/api.key --data-dir /var/lib/hospitus --db /var/lib/hospitus/hospitus.db"
```

---

## Security checklist

- [ ] API key configured on hospitusd (`--api-key-file /usr/local/etc/hospitus/api.key`, not `--api-key`)
- [ ] `/usr/local/etc/hospitus/api.key` is `chmod 600` and owned by `root`
- [ ] TLS enabled (`--tls-cert` + `--tls-key`); minimum TLS 1.2 is enforced automatically
- [ ] hospitusd not listening on `0.0.0.0` unless required (default is `127.0.0.1`)
- [ ] Firewall rule limits port 8443 to trusted IPs only
- [ ] API key rotated if compromised (update file, restart hospitusd)
- [ ] `--allow-no-auth` **not** used in production
- [ ] `--allow-insecure-tls` **not** used in production
- [ ] `--tls-skip-verify` **not** used in production on CLI (use `--tls-ca` instead)
- [ ] `/metrics` endpoint protected (default); use `--metrics-public` only with Prometheus on a trusted network

---

## No central control plane

Hospitus manages multiple hosts through **contexts** — the CLI talks to each
`hospitusd` independently over HTTP/TLS, as described above. There is no central
control plane, no membership and no replication. For multi-host operation,
use contexts.

---

## See also

- [Contexts](contexts.md) — the full reference for named connection profiles
- [CLI Reference](cli-reference.md) — global flags and every command
- [Networking](networking.md) — bind address, PF, and firewall considerations
