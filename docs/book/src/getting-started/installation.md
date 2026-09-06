# Installation

This guide covers installing Hospitus on FreeBSD — the primary supported platform.

---

## Prerequisites

### FreeBSD 14.0+

| Requirement | Notes |
|-------------|-------|
| FreeBSD 14.0 or later | Recommended: 14.2-RELEASE or newer |
| ZFS filesystem | Required for jail/bhyve storage and snapshots |
| Root access | `doas` or `sudo` must be configured |
| Go 1.25+ | Only needed if building from source |

Enable resource limits (for jail CPU/RAM caps):

```sh
# /boot/loader.conf
echo 'kern.racct.enable=1' >> /boot/loader.conf
```

A reboot is required after editing `loader.conf`.

For bhyve VMs:

```sh
# Load the bhyve kernel module
kldload vmm
echo 'vmm_load="YES"' >> /boot/loader.conf
```

---

## Installation

### From Source

```sh
# Clone the repository
git clone https://github.com/hospitus/hospitus.git
cd hospitus

# Install Go (if not already installed)
pkg install go

# Build all binaries
make build

# Or build individually
make build-daemon    # hospitusd (API server)
make build-cli       # hospitus (CLI client)
```

### Install Binaries System-Wide

Upgrading rather than installing for the first time? Stop the daemon before
copying. The kernel holds the executable of a running process, so the copy
fails with `cp: /usr/local/bin/hospitusd: Text file busy`:

```sh
doas service hospitus stop
```

```sh
# Copy binaries
doas cp hospitus hospitusd /usr/local/bin/
doas chmod 755 /usr/local/bin/hospitus /usr/local/bin/hospitusd
doas chown root:wheel /usr/local/bin/hospitus /usr/local/bin/hospitusd

# Create data and state directories
doas mkdir -p /var/lib/hospitus/state
doas chown root:wheel /var/lib/hospitus /var/lib/hospitus/state
doas chmod 750 /var/lib/hospitus
doas chmod 700 /var/lib/hospitus/state
```

---

## First-Run Setup (Secure)

Follow this section to run `hospitusd` by hand. If you are using the rc.d service
instead, skip steps 1 and 2: on first start `service hospitus start` writes a key
to `/usr/local/etc/hospitus/api.key` when that file does not yet exist, and reads
it on every start after that. Read it back with
`doas cat /usr/local/etc/hospitus/api.key`. A key you put there yourself is left
alone, so the two paths can be mixed.

### 1. Generate an API key

```sh
# Generate a random API key (save this — you'll need it for the CLI)
openssl rand -base64 32
```

Example output: `k9mX2cFqR7pLnT8aWdVoEbYjHsU3Gi4NzOe1Ph0CeK=`

### 2. Store the key in a file (avoids secrets in process list)

```sh
doas mkdir -p /usr/local/etc/hospitus
doas sh -c 'printf "%s\n" "k9mX2cFqR7pLnT8aWdVoEbYjHsU3Gi4NzOe1Ph0CeK=" > /usr/local/etc/hospitus/api.key'
doas chmod 600 /usr/local/etc/hospitus/api.key
doas chown root:wheel /usr/local/etc/hospitus/api.key
```

### 3. Generate a TLS certificate (self-signed for testing)

For production, use a certificate from Let's Encrypt or your internal CA.

```sh
doas mkdir -p /usr/local/etc/hospitus

# Self-signed certificate valid for 10 years
doas openssl req -x509 -newkey rsa:4096 -sha256 -days 3650 -nodes \
  -keyout /usr/local/etc/hospitus/hospitusd.key \
  -out    /usr/local/etc/hospitus/hospitusd.crt \
  -subj   "/CN=hospitusd" \
  -addext "subjectAltName=IP:127.0.0.1,IP:$(ifconfig | awk '/inet /&&!/127.0.0.1/{print $2;exit}')" \
  -addext "extendedKeyUsage=serverAuth"

doas chmod 600 /usr/local/etc/hospitus/hospitusd.key
doas chmod 644 /usr/local/etc/hospitus/hospitusd.crt
doas chown root:wheel /usr/local/etc/hospitus/hospitusd.key /usr/local/etc/hospitus/hospitusd.crt
```

### 4. Start the daemon

```sh
doas hospitusd \
  --tls-cert      /usr/local/etc/hospitus/hospitusd.crt \
  --tls-key       /usr/local/etc/hospitus/hospitusd.key \
  --api-key-file  /usr/local/etc/hospitus/api.key \
  --data-dir      /var/lib/hospitus \
  --state-dir     /var/lib/hospitus/state \
  --db            /var/lib/hospitus/hospitus.db
```

The daemon listens on `https://127.0.0.1:8080` by default.  
For remote access, add `--addr 0.0.0.0:8443` (see [Remote Access](../user-guide/remote.md)).

### 5. Configure the CLI

```sh
# Store the URL, API key, and CA cert as a named context for CLI use.
# --tls-ca points the CLI at your self-signed certificate so TLS verifies.
hospitus context add local \
  --url     https://127.0.0.1:8080 \
  --api-key k9mX2cFqR7pLnT8aWdVoEbYjHsU3Gi4NzOe1Ph0CeK= \
  --tls-ca  /usr/local/etc/hospitus/hospitusd.crt \
  --set-active

hospitus jail list
```

`--set-active` makes the new context current; you can also switch later with
`hospitus context use local`. Contexts are stored in `~/.config/hospitus/config.yaml`.

Or drive the CLI with environment variables and global flags instead of a context:

```sh
export HOSPITUS_API_URL=https://127.0.0.1:8080
export HOSPITUS_API_KEY=k9mX2cFqR7pLnT8aWdVoEbYjHsU3Gi4NzOe1Ph0CeK=
# The CA certificate has no environment variable — pass it with --tls-ca:
hospitus jail list --tls-ca /usr/local/etc/hospitus/hospitusd.crt
```

---

## Automated Setup (FreeBSD rc.d)

A setup script creates directories and installs the rc.d service (named `hospitus`):

```sh
doas ./scripts/setup-boot.sh
```

This script:
1. Creates `/var/lib/hospitus` and `/var/lib/hospitus/state` with restrictive permissions
2. Installs the rc.d service file to `/usr/local/etc/rc.d/hospitus`
3. Enables the service in `/etc/rc.conf` (`hospitus_enable="YES"`)
4. Starts the service, generating an API key on first start at
   `/usr/local/etc/hospitus/api.key` if one does not already exist

Configure the service in `/etc/rc.conf` (variables consumed by `etc/rc.d/hospitus`):

```sh
hospitus_enable="YES"
hospitus_addr="127.0.0.1:8080"
hospitus_data_dir="/var/lib/hospitus"
hospitus_state_dir="/var/lib/hospitus/state"
hospitus_db="/var/lib/hospitus/hospitus.db"
hospitus_key_file="/usr/local/etc/hospitus/api.key"
# Extra flags, e.g. enable TLS:
hospitus_flags="--tls-cert /usr/local/etc/hospitus/hospitusd.crt --tls-key /usr/local/etc/hospitus/hospitusd.key"
```

Then start:

```sh
doas service hospitus start
doas service hospitus status
```

### Point the CLI at the service

The service starts on `127.0.0.1:8080` **over plain HTTP**: the default
`hospitus_flags` is `--allow-insecure-tls`, which is safe only because the daemon
is bound to the loopback. Use `http://` here — a context with `https://` fails
with `wrong version number`. To serve TLS instead, generate a certificate as in
[First-Run Setup](#3-generate-a-tls-certificate-self-signed-for-testing), put
`--tls-cert`/`--tls-key` in `hospitus_flags`, and use `https://`.

```sh
# The key the service generated on first start
doas cat /usr/local/etc/hospitus/api.key

hospitus context add local \
  --url     http://127.0.0.1:8080 \
  --api-key "$(doas cat /usr/local/etc/hospitus/api.key)" \
  --set-active

hospitus jail list
```

A fresh install answers `No instances found` — the daemon is reachable and
authenticated.

---

## All Daemon Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--addr` | API listen address | `127.0.0.1:8080` |
| `--data-dir` | Data storage directory | `/var/lib/hospitus` |
| `--state-dir` | Runtime state directory | `/var/lib/hospitus/state` |
| `--db` | SQLite database path | `/var/lib/hospitus/hospitus.db` |
| `--tls-cert` | TLS certificate file (PEM) | - |
| `--tls-key` | TLS private key file (PEM) | - |
| `--allow-insecure-tls` | Allow HTTP without TLS (**dev only**) | false |
| `--api-key` | API key (repeat for multiple; visible in ps) | - |
| `--api-key-file` | File with one API key per line (**recommended**) | - |
| `--allow-no-auth` | Disable authentication (**dev only**) | false |
| `--metrics-public` | Allow unauthenticated `/metrics` access (Prometheus) | false |
| `--log-level` | Log verbosity: debug, info, warn, error | `info` |
| `--log-format` | Log format: text or json | `text` |
| `--write-timeout` | HTTP write timeout | `30m` |
| `--read-timeout` | HTTP read timeout | `15s` |
| `--idle-timeout` | HTTP idle timeout | `60s` |
| `--rate-limit` | Per-client HTTP rate limiting | true |
| `--rate-limit-rps` | Sustained requests per second, per client | `10` |
| `--rate-limit-burst` | Burst size, per client | `20` |
| `--audit-log` | Security audit log file | `<data-dir>/logs/audit.log` |
| `--migrate` | Run database migrations and exit | false |
| `--migration-status` | Show migration status and exit | false |
| `--version` | Show version information and exit | false |

> **Security note:** Pass API keys via `--api-key-file` or the `HOSPITUS_API_KEY` environment
> variable rather than `--api-key` — command-line arguments are visible to all users via `ps(1)`.

### CLI Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `HOSPITUS_API_URL` | API server URL | `http://127.0.0.1:8080` |
| `HOSPITUS_API_KEY` | API key for authentication | - |
| `HOSPITUS_TIMEOUT` | HTTP client timeout | `30s` |

There is no environment variable for the CA certificate or TLS verification.
Pass those per invocation with the global `--tls-ca <file>` flag (or store them
in a context with `hospitus context add --tls-ca`). Use `--tls-skip-verify` only for
throwaway testing.

---

## Verify Installation

```sh
# Daemon health check
curl -k https://127.0.0.1:8080/health
# → {"status":"healthy","time":"..."}

# List jails (with auth)
hospitus jail list
```

---

## Uninstalling

```sh
doas sh scripts/hospitus-purge.sh
```

The script stops the instances Hospitus created, removes the daemon and its
rc.conf entry, flushes the `hospitus` PF anchor, and destroys the data directory
and the ZFS datasets beneath the parent. It never touches `/etc/pf.conf` — it
prints what to remove from it instead. Add `-k` to keep the data and remove
only the files.

Removing the directories by hand is a trap worth naming, because a firewall
that will not load is a poor way to learn it:

```sh
doas rm -rf /var/lib/hospitus /usr/local/etc/hospitus   # read the note below first
```

Both directories are places `pf.conf` may point at — `include
"/usr/local/etc/hospitus/pf.conf"`, or `load anchor "hospitus" from
"/var/lib/hospitus/firewall/pf/combined.rules"`. PF refuses an entire ruleset over
one missing file, so deleting either leaves the host without rules at its next
reload or reboot:

```
pfctl: cannot open the main config file!
```

The script checks for both and says so. If you do it by hand, remove the lines
from `pf.conf` first and confirm with `pfctl -n -f /etc/pf.conf`, which reports
without loading.

---

## Shell Completion

Enable tab completion for all hospitus commands and flags.

### bash

```sh
# Add to ~/.bashrc (or /usr/local/etc/bash_completion.d/hospitus)
hospitus completion bash > /usr/local/etc/bash_completion.d/hospitus
```

### zsh

```sh
# Add to ~/.zshrc
echo 'source <(hospitus completion zsh)' >> ~/.zshrc
# Enable completions if not already done
echo 'autoload -U compinit && compinit' >> ~/.zshrc
source ~/.zshrc
```

### fish

```sh
hospitus completion fish > ~/.config/fish/completions/hospitus.fish
```

### Verify

```sh
hospitus jail <TAB>    # should show: create destroy exec info list set start stop ...
hospitus --<TAB>       # should show global flags: --api-url --api-key --config --context --tls-ca ...
```

---

## Man Pages

Generate and install man pages for all hospitus commands:

```sh
# Generate into ./man/
hospitus man ./man

# Install system-wide
doas mkdir -p /usr/local/man/man1
doas cp ./man/*.1 /usr/local/man/man1/
doas makewhatis /usr/local/man

# Use them
man hospitus
man hospitus-jail-create
man hospitus-bhyve-create
```

---

## Next Steps

- [Quick Start](quick-start.md) — Create your first jail in minutes
- [Your First Jail](first-jail.md) — Step-by-step jail walkthrough
- [Contexts (Multi-host)](../user-guide/contexts.md) — Connect CLI to multiple servers
- [Remote Access](../user-guide/remote.md) — Connect CLI to a remote daemon securely
- [Security Hardening](../production/security.md) — Production security checklist
