# Contexts — Managing Multiple hospitusd Hosts

A **context** is a named connection profile that stores a hospitusd URL, API key, and TLS
settings. Contexts let you manage multiple Hospitus hosts from a single CLI without re-typing
credentials on every command.

```
hospitus context add prod  --url https://hospitus.example.com:8443 --api-key sk-...
hospitus context use prod
hospitus jail list          # ← talks to production
```

## Context file

Contexts are stored in `~/.config/hospitus/config.yaml` (or `$XDG_CONFIG_HOME/hospitus/config.yaml`).

```yaml
current_context: prod
contexts:
  - name: local
    url: http://127.0.0.1:8080
  - name: prod
    url: https://hospitus.prod.example.com:8443
    api_key: hsp_my-production-key
    tls_ca_cert: /usr/local/etc/hospitus/ca.crt
  - name: staging
    url: https://staging.example.com:8443
    api_key: hsp_staging
    tls_skip_verify: false
```

You can edit this file directly or use the `hospitus context` subcommands.

> **Field names.** In the config file the CA path field is `tls_ca_cert`
> (matching the `--tls-ca` flag on the CLI). Other per-context fields are
> `name`, `url`, `api_key`, and `tls_skip_verify`.

## Commands

### Add a context

```bash
# Local development — no auth, plain HTTP
hospitus context add local --url http://127.0.0.1:8080

# Remote server with API key
hospitus context add prod \
  --url https://hospitus.prod.example.com:8443 \
  --api-key hsp_myprodkey

# Remote server with self-signed TLS certificate
hospitus context add lab \
  --url https://192.168.10.5:8443 \
  --api-key hsp_lab \
  --tls-ca /usr/local/etc/hospitus/ca.crt

# Add and immediately switch to it
hospitus context add staging \
  --url https://staging.example.com:8443 \
  --api-key hsp_staging \
  --set-active
```

### List contexts

```bash
hospitus context list
# ACTIVE  NAME     URL                                   AUTH  TLS
# *       prod     https://hospitus.prod.example.com:8443   key   custom-ca
#         local    http://127.0.0.1:8080                 none  default
#         staging  https://staging.example.com:8443      key   default
```

### Switch active context

```bash
hospitus context use local
# Switched to context "local" (http://127.0.0.1:8080)

hospitus context use prod
# Switched to context "prod" (https://hospitus.prod.example.com:8443)
```

### Show current context

```bash
hospitus context show
# Name:   prod
# URL:    https://hospitus.prod.example.com:8443
# Auth:   API key (set)
# TLS:    custom CA (/usr/local/etc/hospitus/ca.crt)
```

### Override context for one command

Pass `--context` to use a different context without switching:

```bash
# List jails on staging without switching the active context
hospitus --context staging jail list

# Create a jail on prod directly
hospitus --context prod jail create web01 --image 14.3-RELEASE-amd64 --vnet

# Or use --api-url and --api-key flags for one-off access
hospitus --api-url https://192.168.1.100:8080 --api-key hsp_key jail list
```

### Remove a context

```bash
hospitus context remove lab
# Removed context "lab"
```

## Setting up a remote hospitusd

### 1. Generate TLS certificate (production)

```bash
# On the remote server — generate a self-signed cert (valid 10 years)
openssl req -x509 -newkey rsa:4096 -sha256 -days 3650 -nodes \
  -keyout /usr/local/etc/hospitus/server.key \
  -out    /usr/local/etc/hospitus/server.crt \
  -subj   "/CN=hospitus-server" \
  -addext "subjectAltName=IP:192.168.1.100,DNS:hospitus.example.com"

chmod 600 /usr/local/etc/hospitus/server.key
chmod 644 /usr/local/etc/hospitus/server.crt
```

### 2. Configure hospitusd

`hospitusd` is configured with startup flags (there is no `hospitus.toml`, and no
`--hash-key` subcommand). Put your API keys — **in plaintext**, one per line —
in a key file; hospitusd bcrypt-hashes them internally on load. Blank lines and
lines starting with `#` are ignored.

```bash
install -m 0600 /dev/null /usr/local/etc/hospitus/api.key
printf '%s\n' "hsp_my-secret-key" > /usr/local/etc/hospitus/api.key
```

Set the listen address and TLS in `/etc/rc.conf` (the shipped `hospitus` service
resolves them into hospitusd flags):

```bash
doas sysrc hospitus_addr="0.0.0.0:8443"
doas sysrc hospitus_key_file="/usr/local/etc/hospitus/api.key"
doas sysrc hospitus_flags="--tls-cert /usr/local/etc/hospitus/server.crt --tls-key /usr/local/etc/hospitus/server.key"
```

Equivalently, to run it by hand:

```bash
doas hospitusd --addr 0.0.0.0:8443 \
    --tls-cert /usr/local/etc/hospitus/server.crt \
    --tls-key /usr/local/etc/hospitus/server.key \
    --api-key-file /usr/local/etc/hospitus/api.key \
    --data-dir /var/lib/hospitus --state-dir /var/lib/hospitus/state --db /var/lib/hospitus/hospitus.db
```

### 3. Start hospitus

```bash
doas sysrc hospitus_enable=YES
doas service hospitus start
```

### 4. Copy the certificate to your local machine

```bash
# From local machine
scp user@192.168.1.100:/usr/local/etc/hospitus/server.crt /usr/local/etc/hospitus/prod-ca.crt
```

### 5. Add the context

```bash
hospitus context add prod \
  --url https://192.168.1.100:8443 \
  --api-key hsp_my-secret-key \
  --tls-ca /usr/local/etc/hospitus/prod-ca.crt

hospitus context use prod
hospitus jail list
```

## Multiple hosts in scripts

```bash
#!/usr/bin/env bash
# deploy.sh — create the same jail on two hosts

for CTX in prod staging; do
  echo "=== Deploying to $CTX ==="
  hospitus --context "$CTX" jail create web01 \
    --image 14.3-RELEASE-amd64 \
    --vnet \
    --memory 1024 \
    --cpus 2
  hospitus --context "$CTX" jail start web01
done
```

## Environment variables and precedence

When several sources set the same value, Hospitus resolves it in this order
(highest priority first):

| Priority | Source |
|----------|--------|
| 1 (highest) | `--api-url` / `--api-key` flags (and `--context` selecting a profile) |
| 2 | Active context from `config.yaml` |
| 3 | `HOSPITUS_API_URL` / `HOSPITUS_API_KEY` environment variables |
| 4 | The local daemon's key file, **only** for a loopback URL (`/usr/local/etc/hospitus/api.key`, or `~/Library/Application Support/hospitus/api.key` on macOS) |
| 5 (lowest) | Built-in default `localhost:8080`, no auth |

Step 4 is why `doas hospitus …` works on the host running the daemon even though
`doas` clears the environment and the context belongs to your own user. A key
read that way is never sent to a remote URL.

Because the active context outranks the environment, `HOSPITUS_API_URL` /
`HOSPITUS_API_KEY` only take effect when **no** context is active and no `--api-url`
/ `--api-key` flag is given. They are most useful on a machine that has not run
`hospitus context use`:

```bash
export HOSPITUS_API_URL=https://staging.example.com:8443
export HOSPITUS_API_KEY=hsp_staging
hospitus jail list   # uses the env vars when no active context is set
```

To point at a server for one command regardless of context, prefer explicit
flags, which always win:

```bash
hospitus --api-url https://staging.example.com:8443 --api-key hsp_staging jail list
```

> Only `HOSPITUS_API_URL`, `HOSPITUS_API_KEY`, and `HOSPITUS_TIMEOUT` are read by the CLI.
> There are no `HOSPITUS_TLS_*` environment variables — configure TLS through the
> `--tls-ca` / `--tls-skip-verify` flags or the context's `tls_ca_cert` field.

## Troubleshooting

### "x509: certificate signed by unknown authority"

The server uses a self-signed certificate that your system doesn't trust.
Add `--tls-ca /path/to/server.crt` to the context, or use `--tls-skip-verify`
for testing (never in production).

```bash
hospitus context add lab \
  --url https://192.168.1.100:8443 \
  --tls-ca /path/to/server.crt
```

### "401 Unauthorized"

The API key is wrong or missing. Hospitus authenticates with the `X-API-Key`
header (not `Authorization: Bearer`). Verify with:

```bash
curl -H "X-API-Key: hsp_mykey" https://192.168.1.100:8443/api/v1/instances
```

The `/health` endpoint is unauthenticated, so use an API route like
`/api/v1/instances` to actually exercise the key.

### "connection refused"

hospitusd is not running or is listening on a different address. Check:

```bash
# On the remote server (the rc.d service is named "hospitus")
doas service hospitus status
doas sockstat -l | grep hospitusd
```

### Context file location

The config file defaults to `~/.config/hospitus/config.yaml` (or
`$XDG_CONFIG_HOME/hospitus/config.yaml`). Override it per command with the global
`--config` flag:

```bash
hospitus --config /path/to/other-config.yaml context list
```
