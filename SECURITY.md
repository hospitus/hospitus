# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 1.0.x | ✅ |
| earlier | ❌ |

Security fixes land on the current 1.0.x line. There is no long-term-support
branch for anything older.

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

### Preferred Method

Open a [GitHub Security Advisory](https://github.com/hospitus/hospitus/security/advisories/new)
(private disclosure via the **Security** tab → **Report a vulnerability**).

### What to Include

A useful report includes:

- A description of the vulnerability and its impact
- Steps to reproduce (minimal example, commands run, OS + FreeBSD version)
- The component affected (jail provider, bhyve provider, API server, CLI, auth, …)
- Any suggested fix or workaround you have identified

### Response Timeline

| Step | Target |
|------|--------|
| Acknowledgement | Within 48 hours |
| Initial assessment | Within 5 business days |
| Fix / patch | Depends on severity — critical issues are prioritised |
| Public disclosure | Coordinated with reporter after fix is available |

### Scope

The following are **in scope**:

- Remote code execution via the Hospitus API
- Authentication bypass or privilege escalation
- Path traversal or injection via instance names, hook scripts, or image paths
- Secrets exposure (API keys, bcrypt hashes) via API responses or log output
- PF firewall rule injection via instance parameters
- Jail escape via crafted configuration

The following are **out of scope**:

- Attacks that require root access on the host (Hospitus already requires root to manage jails/VMs)
- Denial of service against the local daemon
- Issues in third-party dependencies already tracked upstream

## Security Design Notes

- API keys are hashed with **bcrypt** in memory when the daemon loads them; the daemon
  itself never persists plaintext keys. Note that the packaged rc.d service, on first
  start, generates a random key and writes it to the credential file
  `%%PREFIX%%/etc/hospitus/api.key` (mode `0600`, e.g. `/usr/local/etc/hospitus/api.key`) so the
  local CLI can authenticate. Protect that file accordingly and rotate the key if it may
  have been exposed.
- Hook scripts are validated as **absolute paths to existing files** — shell strings are rejected
- Instance and snapshot names are validated against an allowlist regex before use in ZFS/jail commands
- PF anchor names and network interface names are validated to prevent injection
- The API server enforces per-IP rate limiting and standard security headers
