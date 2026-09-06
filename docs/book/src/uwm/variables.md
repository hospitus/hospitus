# Variables & Secrets

A manifest is rendered through Go's `text/template` engine **before** the TOML is
parsed. This lets one manifest serve many environments — dev and prod, host A and
host B — without hardcoding IPs, sizes, or credentials. You supply the values at
apply time; Hospitus fills in the blanks, then validates and applies the result.

This page covers the template syntax, where values come from, the five template
functions Hospitus provides, and how persistent secrets work.

---

## Template Syntax

Variables use Go template `{{ .name }}` syntax and may appear anywhere inside a
TOML value:

```toml
[workload]
name = "{{ .app_name }}"

[resources]
memory = "{{ .memory | default "512Mi" }}"
```

When a variable is referenced but never supplied, it renders as the literal
string `<no value>` — not as an empty string — which usually makes the manifest
invalid in a confusing way. Guard against that with the `default` function (see
below); it treats `<no value>` as unset and substitutes the fallback.

> Variable names are **case-sensitive** and must be valid Go identifiers
> (`{{ .db_ip }}`, `{{ .DOMAIN }}`). The key you reference must match the key you
> supply exactly.

---

## Where Values Come From

At apply time, Hospitus merges variables from three sources. When the same key is set
in more than one place, the higher-priority source wins:

| Priority | Source | How to set it |
|----------|--------|---------------|
| 1 (highest) | `--var key=value` flag (repeatable) | `hospitus apply --var domain=my.tld app.toml` |
| 2 | `--values <file>` TOML file | `domain = "my.tld"` in the file |
| 3 (lowest) | `HOSPITUS_VAR_*` environment variables | `export HOSPITUS_VAR_domain=my.tld` |

The `default` function inside the template acts as the final fallback when a key is
absent from all three sources.

The environment-variable prefix defaults to `HOSPITUS_VAR_` and is configurable with
`--env-prefix`. The key is whatever follows the prefix, verbatim:
`HOSPITUS_VAR_domain` supplies `{{ .domain }}`, and `HOSPITUS_VAR_DB_IP` supplies
`{{ .DB_IP }}`.

```bash
# --var overrides everything for this run
hospitus apply --values prod.toml --var memory=4Gi app.toml
```

---

## Values File Format

A values file is plain TOML with flat string values (no nested tables):

```toml
# prod.toml
bridge        = "hospitus0"
domain        = "nas.example.com"
ext_interface = "em0"
db_ip         = "10.0.0.10"
app_ip        = "10.0.0.11"
memory        = "2Gi"
cpus          = "2"
```

Apply it with:

```bash
hospitus apply --values prod.toml app.toml
```

Templates are rendered over the raw file text *before* the TOML is parsed, so a
variable can fill a numeric field — but the placeholder must be left unquoted,
or TOML hands the parser a string:

```toml
[resources]
cpu = {{ .cpus | default "2" }}      # correct: renders to cpu = 2
# cpu = "{{ .cpus | default "2" }}"  # rejected: TOML value has type string;
                                     # destination has type integer
```

---

## Template Functions

Hospitus registers exactly **five** functions. No others are available.

### `default` — fallback value

Returns the fallback when the piped value is unset, empty, or a zero value.

```toml
memory = "{{ .memory | default "512Mi" }}"
bridge = "{{ .bridge | default "hospitus0" }}"
```

### `env` — read a host environment variable

Reads a variable from the daemon/CLI process environment. For security, `env` is
restricted to a fixed allow-list; any other name returns an empty string:

```
HOME  USER  LANG  LC_ALL  TZ  PWD
HOSPITUS_DATA_DIR  HOSPITUS_STATE_DIR  HOSPITUS_DB_PATH
```

```toml
[workload.annotations]
created_by = "{{ env "USER" }}"
```

To inject arbitrary values from the environment, use `HOSPITUS_VAR_*` variables and
reference them with `{{ .name }}` — that path is not allow-listed and is the
intended mechanism for parameterisation. Reserve `secret` (below) for anything
sensitive.

### `secret` — persistent, auto-generated secret

Looks up a named secret scoped to the workload (or stack) name. If it does not yet
exist, Hospitus generates a strong random value, stores it on the host, and returns
it. On every subsequent apply the **same** value is returned, so credentials stay
stable across re-applies.

```toml
[[lifecycle.hooks.post_create]]
type = "exec"
commands = [
  "sh -c 'echo {{ secret "db_password" }} | pw usermod pgsql -h 0'",
]
```

Secrets change only when you explicitly rotate them (see
[Secrets CLI](#secrets-cli)). The scope is the manifest's `name`, so
`{{ secret "db_password" }}` in a workload named `postgres` resolves to the
`db_password` secret under the `postgres` scope.

### `randHex` — random hex string (not persistent)

Generates a random lowercase-hex string of length `n`. A **new** value is produced
on every apply, so use it only for throwaway values, never for anything that must
remain stable — use `secret` for that.

```toml
[workload.annotations]
build_nonce = "{{ randHex 16 }}"
```

### `randAlnum` — random alphanumeric string (not persistent)

Same as `randHex` but draws from `[a-zA-Z0-9]`.

```toml
[workload.labels]
run_id = "{{ randAlnum 8 }}"
```

> There is no `b64enc` and no `atoi` function. If you need base64 or numeric
> conversion, prepare the value before applying (for example, pass a
> pre-encoded string via `--var`).

---

## Worked Example

A single parameterised jail manifest that works for both dev and prod:

```toml
# web.toml
[workload]
name        = "{{ .app_name | default "web" }}"
description = "Parameterised web jail"

[provider]
type = "jail"

[image]
source = "freebsd:{{ .release | default "14.3-RELEASE" }}"

[resources]
cpu    = 2
memory = "{{ .memory | default "512Mi" }}"

[[networks]]
name   = "public"
type   = "bridge"
bridge = "{{ .bridge | default "hospitus0" }}"

[networks.ip]
mode    = "static"
address = "{{ .jail_ip | default "10.0.0.20/24" }}"
gateway = "10.0.0.1"

[[networks.ports]]
host      = 80
container = 80
protocol  = "tcp"

[lifecycle.autostart]
enabled  = true
priority = 50

# The password is generated once and reused on every apply.
[[lifecycle.hooks.post_create]]
type     = "exec"
commands = [
  "sh -c 'printf %s {{ secret "app_secret_key" }} > /usr/local/etc/app/secret.key'",
]
```

Deploy per environment:

```bash
# development
hospitus apply --var app_name=web-dev --var memory=256Mi web.toml

# production
hospitus apply --values prod.toml web.toml
```

---

## Secrets CLI

Secrets live on the host, one file per secret, and are managed with
`hospitus secret`. The **scope** is the workload or stack name.

```bash
# List every secret (optionally filter by scope)
hospitus secret list
hospitus secret list web

# Print a secret value
hospitus secret get web app_secret_key

# Rotate — generates a new value; the next apply picks it up
hospitus secret rotate web app_secret_key

# Delete permanently
hospitus secret rm web app_secret_key
```

Secret files are stored one per secret, mode `0600`, at
`<store>/<scope>/<name>`. Which store depends on who runs the command: the
daemon owns `/var/lib/hospitus/secrets`, so a stack deployed through the API keeps
its secrets there, while `hospitus apply` run by a user who cannot write that
directory keeps them in `~/.local/share/hospitus/secrets`, created `0700`.
`hospitus secret list` shows the ones belonging to whoever runs it.

Do not commit either directory to version control, and back it up as carefully
as any other credential store — rotating a secret invalidates anything already
provisioned with the old value.

---

## See Also

- [Overview](overview.md) — what a manifest is and why to use one
- [Specification](spec.md) — the complete field reference
- [Multi-Instance Stacks](stacks.md) — orchestrate several workloads together
- [Examples & Template Catalog](examples.md) — ready-to-adapt manifests
