# Authentication & Permissions

Hospitus authenticates HTTP clients with **API keys**. Keys are stored
**bcrypt-hashed** — the plaintext is never persisted — and are sent on every
request in the `X-API-Key` header. This page documents the real model as
implemented in [`internal/auth/auth.go`](https://github.com/hospitus/hospitus/blob/main/internal/auth/auth.go),
[`internal/api/middleware.go`](https://github.com/hospitus/hospitus/blob/main/internal/api/middleware.go),
and [`cmd/hospitusd/main.go`](https://github.com/hospitus/hospitus/blob/main/cmd/hospitusd/main.go).

---

## The two auth providers

Both implement the `AuthProvider` interface:

```go
type AuthProvider interface {
    ValidateKey(key string) bool
    GetPermissions(key string) ([]string, bool)
}
```

The second return value of `GetPermissions` reports whether the key exists at
all, so a valid key with no permissions (`nil, true`) is distinguishable from
an invalid key (`nil, false`).

| Provider | When it is used | Storage |
|----------|-----------------|---------|
| `AuthManager` | Production — whenever any API key is configured | bcrypt-hashed keys, permissions, optional expiry |
| `SimpleAuthProvider` | Development/testing only | **plaintext keys in memory** — never for production |

`hospitusd` always constructs the server with `EnableAuth: true`. The *concrete*
behavior is then decided at startup from the flags you pass:

| Startup condition | Result |
|-------------------|--------|
| One or more API keys configured | `AuthManager` (bcrypt) — production mode |
| No keys **and** `--allow-no-auth` | Auth middleware disabled — open, loopback dev only |
| No keys **and** no `--allow-no-auth` | **Fail-closed**: an empty `SimpleAuthProvider` that rejects every request |

The fail-closed default is deliberate: a misconfigured daemon refuses all traffic
rather than silently running wide open.

---

## Supplying keys to the daemon

There is **no** `--api-keys` flag and **no** `HOSPITUS_API_KEYS` variable. Keys are
supplied through exactly three inputs, which accumulate:

```bash
# 1. Repeatable --api-key flag (once per key)
doas ./hospitusd --api-key "hsp_AbC..." --api-key "hsp_XyZ..."

# 2. A file with one key per line (blank lines and #-comments ignored)
doas ./hospitusd --api-key-file /usr/local/etc/hospitus/api.key

# 3. HOSPITUS_API_KEY environment variable (comma-separated)
HOSPITUS_API_KEY="hsp_AbC...,hsp_XyZ..." doas ./hospitusd
```

Example `/usr/local/etc/hospitus/api.key` (mode `0600`, owned by root):

```
# admin key
hsp_kV8x2Q3zR5p7N9mL4wY6tU1sA0dF3gH5
# ci key
hsp_MyReadOnlyKeyABCDEF0123456789ABCDEF
```

On startup each key is added to the `AuthManager` via `AddAPIKey`, which
**bcrypt-hashes it with `bcrypt.DefaultCost`** before storing. The plaintext
slices are zeroed afterwards. Keys loaded this way are granted the `*`
(all-access) permission.

### Key format

Programmatically generated keys use `GenerateAPIKey()`:

```
hsp_<base64url of 32 random bytes>
```

The `hsp_` prefix is a convention checked by the tests. Because keys are hashed on
load, any sufficiently random string works — you can mint one without the daemon:

```bash
# Bootstrap a key on any machine
echo "hsp_$(openssl rand -base64 32 | tr -d '=+/ ')"
# or
python3 -c "import secrets,base64;print('hsp_'+base64.urlsafe_b64encode(secrets.token_bytes(32)).decode().rstrip('='))"
```

---

## Using a key as a client

### CLI

```bash
export HOSPITUS_API_KEY="hsp_your-key-here"
hospitus jail list          # all commands now authenticate with this key
```

### HTTP

The middleware reads the key from the **`X-API-Key`** header:

```bash
curl -H "X-API-Key: hsp_your-key-here" http://127.0.0.1:8080/api/v1/instances
```

> `X-API-Key` is the only header the HTTP middleware reads — there is no
> `Authorization: Bearer` or query-parameter form.

The `/health` endpoint is always public. `/metrics` and `/metrics/prometheus`
are public **only** when `--metrics-public` is set (so a Prometheus scraper can
reach them without a key).

---

## Permissions

`AuthManager` keys carry a `Permissions []string`. Every request is mapped to a
required permission by `requiredPermission(method, path)`:

| Request | Required permission |
|---------|---------------------|
| Any path under `/api/v1/auth/keys` | `admin` |
| Path containing `/exec` or `/console` | `exec` |
| `GET` / `HEAD` / `OPTIONS` | `read` |
| Everything else (`POST`/`PUT`/`PATCH`/`DELETE`) | `write` |

A key holding `*` satisfies every check (`HasPermission` treats `*` as a
wildcard). Keys loaded from `--api-key` / `--api-key-file` / `HOSPITUS_API_KEY` all
receive `["*"]`, so they are effectively admin keys.

**Scoped, expiring keys** can only be minted through the management API (below),
which lets you set specific permissions and a TTL. There is no role/RBAC layer
beyond this permission-string check — for richer policy, front the daemon with a
reverse proxy.

### Managing keys at runtime

The admin-only key endpoints (require `admin`/`*`):

```bash
# List keys (metadata only — never the plaintext or hash)
curl -H "X-API-Key: $HOSPITUS_API_KEY" \
    http://127.0.0.1:8080/api/v1/auth/keys

# Create a scoped, 30-day key. The plaintext is returned exactly ONCE.
curl -X POST -H "X-API-Key: $HOSPITUS_API_KEY" -H "Content-Type: application/json" \
    -d '{"name":"ci-readonly","permissions":["read"],"ttl_days":30}' \
    http://127.0.0.1:8080/api/v1/auth/keys

# Revoke a key by ID
curl -X DELETE -H "X-API-Key: $HOSPITUS_API_KEY" \
    http://127.0.0.1:8080/api/v1/auth/keys/{id}
```

Runtime-created keys are persisted (hashed) in the datastore `api_keys` table and
rehydrated on the next daemon start. Expiry is enforced in `ValidateAPIKey`
(`ExpiresAt` in the past → `API key expired`).

---

## TLS

Hospitus **does** terminate TLS natively — you do not need a reverse proxy for
HTTPS. Point it at a PEM certificate and key:

```bash
doas ./hospitusd \
    --addr 0.0.0.0:8443 \
    --tls-cert /usr/local/etc/hospitus/tls/cert.pem \
    --tls-key  /usr/local/etc/hospitus/tls/key.pem
```

With both flags set, the server calls `ListenAndServeTLS` with a `tls.Config`
that pins `MinVersion: TLS 1.2` and prefers the X25519 / P-256 curves. It also
adds an HSTS header (`Strict-Transport-Security`) once TLS is active.

TLS is **fail-closed**: if you provide neither certificate nor `--allow-insecure-tls`,
the daemon refuses to start (`TLS required: configure certificates or set
allow_insecure_tls=true`). Use `--allow-insecure-tls` for plain HTTP in
development only.

You can still run behind nginx/Caddy/HAProxy if you want a shared TLS
terminator, request buffering, or path-based routing.

---

## Rate limiting

Two independent limiters live in the middleware, both keyed by client IP
(`golang.org/x/time/rate` token buckets):

- **Auth-attempt limiter** — always active when auth is enabled. Allows **5
  failed attempts per minute, burst 3**. It is consumed only on authentication
  *failures*, so a valid key is never throttled by its own traffic. Exceeding it
  returns `429` with `Retry-After: 60`. This is the brute-force guard.
- **General request limiter** — `rateLimitMiddleware`, wired when
  `ServerConfig.EnableRateLimit` is true. The shipped `hospitusd` enables it **by
  default**: `--rate-limit` defaults to true, with **10 requests/second
  sustained, burst 20** per client, tunable with `--rate-limit-rps` and
  `--rate-limit-burst` (or disabled with `--rate-limit=false`).

`X-Forwarded-For` / `X-Real-IP` are honored only for requests coming from a
configured `TrustedProxies` CIDR (empty by default), so spoofed forwarding
headers cannot dodge the limiter.

---

## Security headers

Every response passes through `securityHeadersMiddleware`, which sets:

```
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Content-Security-Policy: default-src 'none'; frame-ancestors 'none'
Referrer-Policy: no-referrer
Cache-Control: no-store, no-cache, must-revalidate, private
Pragma: no-cache
Strict-Transport-Security: max-age=31536000; includeSubDomains; preload   # only when TLS is active
```

The `Server` header is removed. CORS is off unless you configure
`AllowedOrigins` (a whitelist; `*` is accepted but must be set explicitly — the
daemon does not emit `Access-Control-Allow-Origin: *` by default).

---

## Audit logging

The logging middleware emits structured audit events for `401` (auth failure),
`429` (rate-limit), and `403` (insufficient permissions), and warns whenever the
daemon serves a request in no-auth mode. Ship `--log-format json` to a log
pipeline in production to track key usage and spot brute-force attempts.

---

## Checklist for production

1. Configure real API keys (`--api-key-file`, mode `0600`, root-owned). Never run
   production with `--allow-no-auth`.
2. Enable native TLS (`--tls-cert` / `--tls-key`) or terminate TLS at a proxy.
3. Prefer scoped, expiring keys (via `POST /api/v1/auth/keys`) for CI and
   read-only consumers instead of handing out `*` keys.
4. Bind to loopback (`--addr 127.0.0.1:8080`, the default) unless you have TLS +
   auth in place; only then expose `0.0.0.0`.
5. Ship JSON logs and watch for `401`/`429`/`403` audit events.

---

## See also

- [REST API](api-reference.md)
- [Production Security](../production/security.md)
- [Production Deployment](../production/deployment.md)
