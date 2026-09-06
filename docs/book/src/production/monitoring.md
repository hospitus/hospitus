# Monitoring

Hospitus exposes health and metrics over HTTP. This chapter documents the endpoints
that actually exist, the metric names the daemon actually emits, and how to wire
them into Prometheus, alerting, and log shipping.

> The daemon has no monitoring config file. Metric exposure is controlled by one
> flag — `--metrics-public` — and the log format by `--log-format`. There are no
> `metrics_enabled`/`metrics_path` config keys.

## Endpoints at a glance

| Endpoint | Auth | Content type | Purpose |
|----------|------|--------------|---------|
| `GET /health` | Always public | JSON | Liveness + database reachability. |
| `GET /metrics` | Key required, unless `--metrics-public` | JSON | Structured metrics for humans/scripts. |
| `GET /metrics/prometheus` | Key required, unless `--metrics-public` | text `0.0.4` | Prometheus exposition format. |
| `GET /security/status` | Key required | JSON | Security self-assessment + score. |

There is **no** `/api/v1/status` endpoint. Use `/health` for liveness and
`/metrics*` for everything else.

## Health checks

`/health` is unauthenticated so load balancers and probes can reach it without a
key. It also pings the database, so it reflects real readiness, not just "the
process is up".

```sh
curl -s http://127.0.0.1:8080/health
```

Use `https://` against a daemon holding a certificate, which a production one
does. Over `http://` it answers 400 and `Client sent an HTTP request to an HTTPS
server.`, so the liveness check below reports the daemon down while it is
running perfectly well.

Healthy:

```json
{ "status": "healthy", "time": "2026-07-26T10:30:00Z" }
```

Database unreachable (HTTP `503`):

```json
{ "status": "unhealthy", "error": "database unavailable", "time": "2026-07-26T10:30:00Z" }
```

Use the status code, not just the body, in probes:

```sh
# rc-friendly liveness check — use the scheme the daemon actually serves.
# TLS daemon (production):
curl -fsS --cacert /usr/local/etc/hospitus/ca.pem https://127.0.0.1:8443/health >/dev/null && echo up || echo down

# Plaintext daemon (--allow-insecure-tls, loopback only):
curl -fsS http://127.0.0.1:8080/health >/dev/null && echo up || echo down
```

Behind an HAProxy front end, health-check the same path:

```haproxy
backend hospitus
    option httpchk GET /health
    http-check expect status 200
    server hospitus1 127.0.0.1:8080 check
```

## Metrics

### Exposing metrics to Prometheus

Prometheus scrapes without sending an API key, so either mark the metrics
endpoints public or give Prometheus a key.

**Public metrics (simplest).** Start the daemon with `--metrics-public`. This
makes *only* `/metrics` and `/metrics/prometheus` unauthenticated; every other
route still requires a key.

```sh
doas sysrc hospitus_flags="--tls-cert /usr/local/etc/hospitus/hospitusd.crt --tls-key /usr/local/etc/hospitus/hospitusd.key --metrics-public"
doas service hospitus restart
```

Restrict scrape access at the network layer (PF) so "public" means "public to
the monitoring network", not the internet.

**Authenticated metrics.** Omit `--metrics-public` and have Prometheus send the
header. Prometheus 3.x can set it per scrape job with `http_headers`; older
releases cannot, and need a proxy that injects `X-API-Key` in front of the
daemon. Either way, `--metrics-public` behind a firewalled interface stays the
simplest arrangement.

### Prometheus scrape config

`/metrics/prometheus` returns the Prometheus exposition format:

```yaml
# prometheus.yml
scrape_configs:
  - job_name: 'hospitus'
    metrics_path: '/metrics/prometheus'
    scheme: https          # http if you did not enable TLS
    static_configs:
      - targets: ['hospitus.example.com:8443']
```

The plain `/metrics` endpoint returns the same **security and request** metrics
as JSON — convenient for `jq` and ad-hoc scripts, but not for Prometheus, and it
carries no jail series.

### Metrics the daemon emits

Hospitus exports two families of metrics. These are the real series names.

**Security and request metrics** (always present):

| Metric | Type | Meaning |
|--------|------|---------|
| `hospitus_auth_attempts_total` | counter | Total authentication attempts. |
| `hospitus_auth_success_total` | counter | Successful authentications. |
| `hospitus_auth_failures_total` | counter | Failed authentications. |
| `hospitus_auth_success_rate` | gauge | Success ratio. |
| `hospitus_rate_limit_violations_total` | counter | Requests rejected by rate limiting. |
| `hospitus_api_key_usage_total` | counter | API-key uses recorded. |
| `hospitus_suspicious_activity_total` | counter | Suspicious-activity detections. |
| `hospitus_security_events_total` | counter | Security events recorded. |
| `hospitus_security_seconds_since_last_event` | gauge | Seconds since the last security event. |
| `hospitus_requests_total` | counter | Total HTTP requests. |
| `hospitus_requests_blocked_total` | counter | Requests blocked by middleware. |
| `hospitus_requests_block_rate` | gauge | Blocked-request percentage. |

**Jail metrics** (emitted only when the jail provider is registered *and* at
least one jail exists — three series, no more):

| Metric | Type | Meaning |
|--------|------|---------|
| `hospitus_jails_total` | gauge | Jails known to the daemon. |
| `hospitus_jails_running` | gauge | Jails currently running. |
| `hospitus_jail_info{name,status}` | gauge | One sample per jail; `1` when running, `0` otherwise. |

There are **no per-jail resource series** on this endpoint: nothing named
`hospitus_jail_cpu_percent`, `hospitus_jail_memory_bytes`, `hospitus_jail_disk_*` or
`hospitus_jail_network_*` is exported, and neither is
`hospitus_api_requests_total`, a request-latency histogram, or any `hospitus_zfs_*` /
`hospitus_instances_total` series. Per-instance CPU, memory, disk and network
figures come from the instance metrics API instead:

```sh
curl -sk -H "X-API-Key: $KEY" \
  https://127.0.0.1:8443/api/v1/instances/web/metrics | jq
```

### Useful queries

```promql
# Authentication failure rate — spikes indicate credential probing.
rate(hospitus_auth_failures_total[5m])

# Rate-limit violations — abuse or a misbehaving client.
rate(hospitus_rate_limit_violations_total[5m])

# Running vs total jails.
hospitus_jails_running / hospitus_jails_total

# Jails that are not running.
hospitus_jail_info == 0
```

## Alerting

```yaml
# hospitus-alerts.yml
groups:
  - name: hospitus
    rules:
      - alert: HospitusDown
        expr: up{job="hospitus"} == 0
        for: 1m
        labels: { severity: critical }
        annotations:
          summary: "Hospitus daemon unreachable"

      - alert: HospitusAuthFailureSpike
        expr: rate(hospitus_auth_failures_total[5m]) > 1
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "Elevated authentication failures on Hospitus"

      - alert: HospitusJailDown
        expr: hospitus_jail_info == 0
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "Jail {{ $labels.name }} is not running"

      - alert: HospitusRateLimitAbuse
        expr: rate(hospitus_rate_limit_violations_total[5m]) > 5
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "Sustained rate-limit violations against Hospitus"
```

The `up` metric comes from Prometheus's own scrape success, so `HospitusDown` fires
even when the daemon cannot answer `/metrics`.

## Security status endpoint

`/security/status` returns a self-assessment: whether authentication and TLS are
on, whether rate limiting is active, recent auth success and request-block
rates, and an overall score. It requires a key. Use it as a post-deploy smoke
test — a low score usually means TLS or auth was left off.

```sh
curl -s -H "X-API-Key: $KEY" http://127.0.0.1:8080/security/status | jq '{score, status, checks}'
```

## Logs

`hospitusd` logs through structured `slog`. Choose the format with `--log-format`
and the verbosity with `--log-level` (`debug`, `info`, `warn`, `error`). For log
shipping, use JSON:

```sh
doas sysrc hospitus_flags="--tls-cert ... --tls-key ... --log-format json"
doas service hospitus restart
```

Under the rc.d service, `daemon(8)` writes to `/var/log/hospitus/hospitusd.log`. A
JSON line looks like:

```json
{"time":"2026-07-26T10:30:00Z","level":"INFO","msg":"Instance created","name":"web","provider":"jail"}
```

Ship it with any file-tailing agent (Promtail, Filebeat, Vector):

```yaml
# promtail example
scrape_configs:
  - job_name: hospitus
    static_configs:
      - targets: [localhost]
        labels:
          job: hospitus
          __path__: /var/log/hospitus/hospitusd.log
    pipeline_stages:
      - json:
          expressions: { level: level, msg: msg, provider: provider }
      - labels: { level: '' }
```

Rotate the file with `newsyslog` (see
[Deployment → Log rotation](deployment.md#8-log-rotation)).

## Per-instance stats without Prometheus

For quick checks you do not need a metrics stack. The CLI reads live stats
through the API:

```sh
hospitus jail stats web
hospitus bhyve stats db --watch --interval 2
hospitus qemu stats vm1 --watch
```

`--watch` refreshes on the given `--interval`, giving you a `top`-style view of a
single instance.

## See also

- [Deployment](deployment.md) — enabling `--metrics-public` and log rotation.
- [Security Hardening](security.md) — what the security metrics measure.
- [Backup & Recovery](backup.md) — protecting the state these metrics report on.
