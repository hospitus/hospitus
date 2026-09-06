# REST API

Hospitus is API-first: the CLI and every integration talk to `hospitusd`
over the same HTTP/JSON REST API. This page is the human-readable tour. The
**authoritative, machine-readable contract** is the OpenAPI 3.0.3 document at
[`docs/api/openapi.yaml`](https://github.com/hospitus/hospitus/blob/main/docs/api/openapi.yaml);
the routes themselves are registered in
[`internal/api/server.go`](https://github.com/hospitus/hospitus/blob/main/internal/api/server.go)
(`registerRoutes()`).

---

## Conventions

- **Base URL** — `http://127.0.0.1:8080` by default (loopback). The CLI reads
  `HOSPITUS_API_URL` to override it.
- **Versioning** — all resource routes are prefixed `/api/v1`. `/health`,
  `/metrics`, `/metrics/prometheus`, and `/security/status` sit outside the
  prefix.
- **Authentication** — send `X-API-Key: <key>` on every request except
  `/health`. See [Authentication](authentication.md).
- **Routing** — Hospitus uses the Go 1.22+ `net/http.ServeMux` pattern router. Some
  routes encode the method in the pattern (`"POST /api/v1/instances/{id}/exec"`);
  most register a base path and dispatch on `r.Method` inside the handler. Path
  parameters are read with `r.PathValue("id")`.
- **Synchronous by default** — instance create answers `201` (or streams, with
  `Accept: text/plain`), image fetch `200`, stack deploy `201`, import `201`,
  backup `201`. Only handlers that submit to the job manager return a
  [Job](#jobs); polling `GET /api/v1/jobs/{id}` for the others waits on an ID
  that was never issued.

### Response and error shape

Successful responses are JSON with a conventional status code. Errors return:

```json
{
  "error": "Failed to create snapshot",
  "code": "optional_machine_code",
  "detail": "exit status 125: repository name must be lowercase"
}
```

`error` is a fixed sentence for a given failure, so a client can match on it.
`detail` appears whenever the daemon has a cause to report, and carries the part
you can act on — that a container is still running, that an image does not
exist, that the tool behind the provider rejected an argument. It is a single
line, truncated at 2048 characters; the full text is always in the hospitusd log.
Do not match on `detail`: its wording comes from whatever failed underneath.

Every route that answers this way requires authentication, which is why the
cause is reported whatever its origin — a provider message and "database is
locked" are equally useful to the operator who has to act on it.

| Status | Meaning in Hospitus |
|--------|------------------|
| `200 OK` | Success with a body |
| `201 Created` | Resource created synchronously |
| `202 Accepted` | Work accepted as an async job |
| `204 No Content` | Success, no body (deletes) |
| `400 Bad Request` | Malformed body or parameters |
| `401 Unauthorized` | Missing / invalid API key |
| `403 Forbidden` | Key lacks the required permission |
| `404 Not Found` | Unknown instance / resource |
| `409 Conflict` | e.g. instance already exists / already running |
| `429 Too Many Requests` | Rate limit or auth-attempt limit |
| `501 Not Implemented` | Provider does not support this capability |

`501` is a first-class case here: because the same handler serves every backend,
an endpoint like snapshots returns `501` for a provider that does not implement
the corresponding [optional interface](providers.md#optional-capability-interfaces).

### Working with the OpenAPI spec

```bash
# Interactive Swagger UI
docker run -p 8081:8080 -e SWAGGER_JSON=/spec/openapi.yaml \
  -v "$(pwd)/docs/api:/spec" swaggerapi/swagger-ui

# Static Redoc preview / lint
npx @redocly/cli preview-docs docs/api/openapi.yaml
npx @redocly/cli lint docs/api/openapi.yaml
```

---

## System

| Method & path | Description |
|---------------|-------------|
| `GET /health` | Liveness. **No auth.** Returns `status` and `time`; a `503` adds `error`. |
| `GET /metrics` | JSON summary (instance/job counts). Public only with `--metrics-public`. |
| `GET /metrics/prometheus` | Prometheus text format. Public only with `--metrics-public`. |
| `GET /security/status` | Security health: `score`, `status`, `recommendations`, and a `checks` object (`authentication_enabled`, `tls_enabled`, `rate_limiting_enabled`, `auth_success_rate`, `request_block_rate`, `seconds_since_incident`). |

---

## Providers

| Method & path | Description |
|---------------|-------------|
| `GET /api/v1/providers` | List registered providers with `available` and `capabilities`. |
| `GET /api/v1/providers/{name}` | Details for one provider. |

`available` reflects a live `HealthCheck()` — a provider can be registered but
unavailable (e.g. bhyve with `vmm` not loaded).

---

## Instances

The core lifecycle. `{id}` is the instance id (`name` or `name@provider`).

| Method & path | Description |
|---------------|-------------|
| `GET /api/v1/instances` | List. Filters: `?provider=`, `?state=`, `?label=key=value`. |
| `POST /api/v1/instances` | Create. Body is an `InstanceSpec`. Always synchronous: `201` (or streams progress with `Accept: text/plain`). |
| `GET /api/v1/instances/{id}` | The full stored instance record (spec, handle, state, labels…). |
| `PATCH /api/v1/instances/{id}` | Update. Body `{"spec": {...}, "provider_config": {...}}`. |
| `DELETE /api/v1/instances/{id}` | Delete. `?force=true` to delete a running instance. `204`. |
| `POST /api/v1/instances/{id}/start` | Start. Streams with `Accept: text/plain`; a provider failure is a `500` (there is no already-running `409`). |
| `POST /api/v1/instances/{id}/stop` | Stop. `?force=true` for a hard stop; the graceful timeout is fixed at 30 s. There is no request body. |
| `POST /api/v1/instances/{id}/restart` | Restart. |
| `GET /api/v1/instances/{id}/health` | Health check (`InstanceHealthCheckProvider`). |
| `GET /api/v1/instances/{id}/metrics` | CPU / memory / network usage. |
| `GET /api/v1/instances/{id}/events` | Event history for the instance. |
| `POST /api/v1/instances/{id}/exec` | Run a command inside. Body is an `ExecRequest`. |
| `GET  /api/v1/instances/{id}/console` | Console info; upgrades to a WebSocket when `Upgrade: websocket` (or `/console/ws`). The WebSocket console is **jail-only**: any other provider gets a `400`. |

### Creating an instance

```bash
curl -X POST -H "X-API-Key: $HOSPITUS_API_KEY" -H "Content-Type: application/json" \
  'http://127.0.0.1:8080/api/v1/instances?provider=jail' \
  -d '{
        "name": "web01",
        "image": "14.3-RELEASE-amd64",
        "cpus": 2,
        "memory_mb": 1024,
        "provider_config": { "vnet": true }
      }'
```

The provider is chosen with `?provider=jail` or with a `provider` label, not with
a field of the body: the body is an `InstanceSpec`, and decoding rejects a field
the struct does not have.

`InstanceSpec`: `name` is required; the common fields are `image`, `cpus`,
`memory_mb`, `os_type`, `os_version`, `arch`, `disks[]`, `networks[]`,
`cloud_init`, `labels`, `annotations`, and `provider_config` for anything
specific to one provider. `InstanceState` is one of `unknown`, `creating`,
`stopped`, `starting`, `running`, `paused`, `stopping`, `migrating`,
`deleting`, `error`.

### Executing a command

```json
POST /api/v1/instances/web01/exec
{ "command": "/bin/sh", "args": ["-c", "uname -a"], "user": "root", "working_dir": "/" }
```

Returns `{ "exit_code", "stdout", "stderr" }`.

### Optional-capability sub-resources

These dispatch to the [optional provider interfaces](providers.md); a provider
that does not implement the interface returns `501`.

| Method & path | Backing interface |
|---------------|-------------------|
| `GET/POST/DELETE /api/v1/instances/{id}/snapshots` | `SnapshotProvider` |
| `POST /api/v1/instances/{id}/snapshots/{name}/restore` | `SnapshotProvider` |
| `POST /api/v1/instances/{id}/clone` | `CloneProvider` |
| `POST /api/v1/instances/{id}/export` | `ExportImportProvider` |
| `GET/POST/DELETE /api/v1/instances/{id}/media` | `MediaProvider` (bhyve/QEMU) |
| `GET/POST /api/v1/instances/{id}/boot-order` | `MediaProvider` |
| `POST /api/v1/instances/{id}/checkpoint`, `GET .../checkpoints` | `CheckpointProvider` (bhyve) |
| `POST /api/v1/instances/{id}/checkpoint/{name}/restore`, `DELETE .../checkpoint/{name}` | `CheckpointProvider` |
| `POST /api/v1/instances/{id}/pause`, `.../resume` | `PauseProvider` |
| `POST /api/v1/instances/{id}/rename` | `RenameProvider` |
| `POST /api/v1/instances/{id}/upgrade` | `UpgradeProvider` (jail) |
| `GET/POST/DELETE /api/v1/instances/{id}/port-forwards` | `PortForwardProvider` (QEMU) |
| `GET/POST/DELETE /api/v1/instances/{id}/interfaces` | network attach/detach |
| `GET/POST /api/v1/instances/{id}/services` | in-instance rc services (jail) |
| `GET /api/v1/instances/{id}/volumes`, `POST/DELETE .../volumes/{volume}` | attach/detach volumes (jail only; `400` otherwise) |
| `GET/POST /api/v1/instances/{id}/backups` | per-instance backups |
| `GET/PUT/DELETE /api/v1/instances/{id}/backups/config` | per-instance backup schedule/retention config |

### FreeBSD-specific (jails)

| Method & path | Description |
|---------------|-------------|
| `GET/POST/DELETE /api/v1/instances/{id}/freebsd/rctl` | Read/set/clear RCTL resource limits. |
| `GET /api/v1/instances/{id}/freebsd/vnet` | VNET status. |
| `POST /api/v1/instances/{id}/freebsd/vnet/enable`, `/disable` | Toggle VNET. |

---

## Images

| Method & path | Description |
|---------------|-------------|
| `GET /api/v1/images` | List downloaded images. (No query filters — a `?provider=` is ignored.) |
| `POST /api/v1/images/fetch` | Download an image, synchronously: `200` when the download finishes. Body `{ "version": "14.3-RELEASE-amd64" }`; any other field is rejected with `400`. |
| `POST /api/v1/images/refresh` | Refresh the catalog from upstream. |
| `DELETE /api/v1/images/{name}` | Delete a downloaded image. |

See [Base Image Sets](sets.md) for what image names mean and how they are stored.

---

## Jobs

The async work queue. See the [`JobManager`](technical-deep-dive.md#39-goroutines-and-the-job-system).

| Method & path | Description |
|---------------|-------------|
| `GET /api/v1/jobs` | List. Filter `?status=` (there is no `?type=` filter). |
| `GET /api/v1/jobs/stats` | Queue counts by status. |
| `GET /api/v1/jobs/{id}` | Job status and progress. |
| `POST /api/v1/jobs/{id}/cancel` | Cancel a running job. `409` if already terminal. |
| `DELETE /api/v1/jobs/{id}` | Delete a completed/failed job. |

A `Job` carries `id` (UUID-ish), `type`, `description`, `status`
(`pending|running|completed|failed|canceled`), `progress` (0.0–1.0), `message`,
`result`, `error`, and timestamps.

---

## Auto-start

| Method & path | Description |
|---------------|-------------|
| `GET /api/v1/autostart` | List auto-start entries across all providers. |
| `POST /api/v1/autostart` | Trigger auto-start now: start every configured instance, in priority order. |
| `GET/PUT/DELETE /api/v1/autostart/{provider}/{id}` | Read / set (`{"enabled": true, "priority": 50, "delay_ms": 0}`) / disable auto-start for an instance. |

---

## Firewall (port forwarding)

PF-backed NAT rules for jail/bhyve.

| Method & path | Description |
|---------------|-------------|
| `POST /api/v1/firewall/expose` | Add a rule. Body `{"instance", "provider", "protocol", "host_port", "target_port", "target_ip"}`; `target_ip` is filled in from the instance when omitted. `201`. |
| `GET /api/v1/firewall/expose/{instance}` | List an instance's rules. |
| `DELETE /api/v1/firewall/expose` | Remove a rule. JSON body `{"instance", "host_port", "protocol"}` — not query parameters. `204`. |
| `POST /api/v1/firewall/nat` | Set up NAT. Body `{"instance", "provider", "source_network", "out_interface"}`. `201`. |
| `DELETE /api/v1/firewall/nat` | Remove an instance's NAT rule. Body `{"instance"}`. `204`. |

---

## Storage: volumes, backups, media

| Method & path | Description |
|---------------|-------------|
| `GET/POST /api/v1/volumes` | List / create a volume. `{"name": "data", "size": "10G"}` — `size` is a ZFS size **string** (becomes the dataset's `refquota`); `quota`, `reservation`, `compression`, `description` are optional. |
| `GET/DELETE /api/v1/volumes/{name}` | Volume details / delete. |
| `GET /api/v1/backups` | List every backup, across instances. |
| `POST /api/v1/instances/{id}/backups` | Create a backup of one instance. Body `{"type": "snapshot"}`, or `"full"` or `"incremental"`; an absent body means a snapshot. `201`. |
| `GET /api/v1/instances/{id}/backups` | List that instance's backups. |
| `GET/DELETE /api/v1/backups/{id}` | Backup details / delete. |
| `POST /api/v1/backups/{id}/verify`, `/restore` | Verify or restore a backup. |

---

## Stacks (multi-instance orchestration)

`hospitus manifest apply` and `hospitus apply` do **not** use these endpoints: the CLI
creates each instance directly, in dependency order. Stack records, health
conditions and the stack lifecycle below therefore apply to the API only.

| Method & path | Description |
|---------------|-------------|
| `GET/POST /api/v1/stacks` | List / deploy a stack. Deploy is synchronous: `201` with the stack record. |
| `GET/DELETE /api/v1/stacks/{name}` | Stack details / destroy (synchronous `204`). |
| `POST /api/v1/stacks/{name}/start`, `/stop` | Start/stop all instances in a stack. |
| `GET /api/v1/stacks/{name}/services` | Service discovery for the stack. |

See [Unified Workload Manifests](../uwm/overview.md) for the manifest format.

---

## Networking & console sessions

| Method & path | Description |
|---------------|-------------|
| `GET /api/v1/network/bridges` | List host bridges and their members. |
| `GET /api/v1/console/sessions` | List active console sessions (WebSocket-backed). |

---

## Import / export

| Method & path | Description |
|---------------|-------------|
| `POST /api/v1/import` | Import an instance from an archive, synchronously: `201` with the stored instance. Body `{"import_path", "provider"}` (both required) plus optional `new_name`, `reset_mac`, `new_ip`, `start_after_import`. |

Export is per-instance and synchronous: `POST /api/v1/instances/{id}/export`
with body `{"export_path": "..."}` (required; confined to the data directory)
plus optional `compress`, `stop_instance`, `include_snapshots` answers `200`
when the archive is written.

---

## Auth key management

Admin-only (`admin`/`*` permission). Covered in [Authentication](authentication.md).

| Method & path | Description |
|---------------|-------------|
| `GET /api/v1/auth/keys` | List key metadata. |
| `POST /api/v1/auth/keys` | Mint a scoped key (`{name, permissions[], ttl_days}`); plaintext returned once. |
| `DELETE /api/v1/auth/keys/{id}` | Revoke a key. |

---

## Middleware pipeline

Every request flows through this chain (outermost first), configured in
`internal/api/middleware.go`:

```
recovery → CORS → security headers → [rate limit] → body-size limit (32 MB) → logging → [auth] → handler
```

Rate limiting and auth are conditional (see [Authentication](authentication.md)).
The body-size cap is 32 MB and is skipped for WebSocket upgrades.

---

## Clients

- **Go client** — [`internal/client`](https://github.com/hospitus/hospitus/blob/main/internal/client/client.go)
  is the official client and the foundation of the CLI:
  ```go
  c := client.NewClient("http://127.0.0.1:8080")
  providers, err := c.ListProviders(ctx)
  ```
- **CLI** — the `hospitus` command wraps that client. See the
  [CLI Reference](../user-guide/cli-reference.md).

---

## See also

- [`docs/api/openapi.yaml`](https://github.com/hospitus/hospitus/blob/main/docs/api/openapi.yaml) — the source of truth
- [The Provider Interface](providers.md) — what the `501`s mean
- [Authentication & Permissions](authentication.md)
