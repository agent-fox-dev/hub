---
spec_id: '19'
spec_name: sessions_metrics_retention
title: Sessions Metrics Retention
status: draft
created_at: '2026-09-01T13:16:23.055309+00:00'
updated_at: '2026-09-01T14:13:45.610168+00:00'
owner: ''
source: docs/prd/prd14.md
schema_version: 1
---
# Agent Sessions, Metrics, and Retention

## Source

File: `docs/prd/prd14.md` (split 3 of 3)

## Intent

With audit storage (spec 17) and hub emission/query (spec 18) in place, the
audit subsystem can ingest, store, and query both agent and hub events. This
spec completes the audit subsystem with three remaining capabilities: agent
session lifecycle tracking, Prometheus metrics for operational monitoring,
and retention policies to prevent unbounded database growth.

Agent sessions track the lifecycle of an agent's interaction with the hub
through explicit open/close API calls with token usage reporting. Prometheus
metrics expose request counters, latency histograms, active session gauges,
and queue depth for scraping. The retention worker runs hourly to enforce
time-based and run-count limits.

## Goals

- Track agent sessions with explicit open/close lifecycle and usage reporting.
- Force-close active sessions when a workspace is archived or deleted.
- Provide a cost summary endpoint aggregating token usage per workspace.
- Expose a Prometheus-compatible `/metrics` endpoint with request counters,
  latency histograms, active session gauges, and job queue depth.
- Enforce retention policies via a background worker to prevent unbounded
  database growth.
- Handle workspace lifecycle events (archive, delete) consistently for audit
  data.

## Non-Goals

- DuckDB setup or schema (spec 17: audit_storage_ingestion). The `agent_sessions`
  and `token_usage` table DDL is fully defined in spec 17, which defines all
  nine audit table schemas. This spec adds only queries and business logic.
- Agent data ingestion endpoints (spec 17: audit_storage_ingestion).
- Hub-internal audit emission (spec 18: hub_audit_query).
- Unified audit query or SSE streaming (spec 18: hub_audit_query).
- Real-time alerting rules or thresholds.
- OTel integration.
- Dollar-cost computation (hub doesn't know model pricing).
- Scrape authentication for `/metrics` — permanently unauthenticated by design;
  network isolation is handled at the infrastructure level. If a deployment
  requires scrape auth, operators can add it via a reverse proxy.
- Modifying `internal/audit/permissions.go` — all four permission scopes
  (`audit:read`, `audit:write`, `sessions:read`, `sessions:write`) were created
  in spec 17. This spec only references the already-defined scopes.

## Background

Specs 17 and 18 established the audit subsystem's storage layer (DuckDB schema,
ingestion endpoints, Store) and query/streaming layer (hub-internal event
emission, SSE, query API). Three capabilities were deferred to this spec to
keep each unit small and independently reviewable:

1. **Agent sessions** — Agents need a structured lifecycle (open → usage reports
   → close) separate from raw audit event ingestion, with token usage aggregation
   and a cost summary surface for operators.
2. **Prometheus metrics** — Operations teams need a scrape endpoint to monitor
   hub health: request rates, latency, active sessions, queue depth, and audit
   table sizes. Introducing Prometheus in this spec rather than earlier avoids
   a transitive dependency chain across specs 17 and 18.
3. **Retention** — Without pruning, the DuckDB audit database grows unboundedly.
   Retention was deferred until all table types were defined so a single worker
   can cover all tables consistently.

## Tech Stack

- Go 1.26+
- Echo v4
- apikit
- `github.com/prometheus/client_golang` (new dependency)
- DuckDB (via foundation from spec 17)
- **Test tooling:** Go standard `testing` package only (no testify). Tests use
  a real DuckDB instance created against a temp file in `t.TempDir()`, matching
  the conventions established in specs 17 and 18.

## Functional Requirements

### Agent Session Lifecycle API

#### POST /api/v1/sessions

Open a new agent session.

Request body:

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `id` | No | Generated UUID | Idempotency key |
| `workspace_slug` | **Yes** | — | Workspace this session operates on |
| `run_id` | No | `""` | Agent run ID |
| `node_id` | No | `""` | Task graph node identifier |
| `archetype` | No | `""` | Agent archetype |
| `model` | No | `""` | Model identifier |
| `metadata` | No | `null` | Arbitrary JSON metadata |

The `credential_id` and `credential_type` are extracted from the authenticated
request's `apikit.AuthInfo` — agents do not self-report their credentials.

**Validation failure (400 Bad Request):** If `workspace_slug` is missing or
empty, the handler returns 400 using `apikit.WriteAPIError(c, 400, message)`,
which produces the standard hub error envelope:
```json
{ "error": "workspace_slug is required", "status": 400 }
```
This is consistent with the established error-response pattern used across all
hub handlers.

Response (201 Created):
```json
{
  "id": "string",
  "workspace_slug": "string",
  "credential_id": "string",
  "credential_type": "string",
  "status": "active",
  "started_at": "2026-09-01T13:00:00Z"
}
```
Duplicate `id` returns 200 with the existing record (same shape as 201).

Permission: `sessions:write`.

#### POST /api/v1/sessions/:id/complete

Close an active session with final status and usage data.

Request body:

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `status` | No | `""` | Terminal status: `completed`, `failed`, `timeout` |
| `input_tokens` | No | `0` | Input tokens consumed |
| `output_tokens` | No | `0` | Output tokens consumed |
| `cache_read_input_tokens` | No | `0` | Cached input tokens read |
| `cache_creation_input_tokens` | No | `0` | Cached input tokens created (session-level summary; see note below) |
| `duration_ms` | No | `0` | Session duration in milliseconds |
| `error_message` | No | `null` | Error message if failed |

**Note on `cache_creation_input_tokens`:** This field is intentionally present
only on the complete endpoint, not on the incremental usage report. Cache
creation spans multiple API calls and is most accurately known at session end,
so it is reported as a session-level summary field rather than per-call.

Valid client-settable terminal statuses: `completed`, `failed`, `timeout`. The
`terminated` status is set exclusively by the force-close logic during workspace
archive/delete and cannot be submitted via this endpoint.

**Idempotency on terminal sessions:** Calling this endpoint on a session that
is already in any terminal state (`completed`, `failed`, `timeout`, or
`terminated`) returns **200 OK** with the existing session record. The response
body includes the actual `status` field so the caller can distinguish a
force-close (`terminated`) from a self-close (`completed`, `failed`, `timeout`).
This treats all terminal states uniformly — an agent that receives 200 knows
the session is closed, regardless of who closed it.

Only the session owner (same `credential_id`) or an admin can complete a
session. **Admin status is determined by checking `auth.CredentialType ==
"admin_token"`,** matching the existing pattern in `internal/workspace/auth.go`.
The three credential types defined in the steering directives are `admin_token`,
`api_key`, and `pat`. Ownership is enforced at the handler level: after loading
the session record, compare `credential_id` against the request's
`apikit.AuthInfo`; return 403 if neither matches and the caller is not an admin
token.

Permission: `sessions:write`.

#### POST /api/v1/sessions/:id/usage

Report incremental token usage for an active session. May be called multiple
times. Each call creates a new `token_usage` record.

**Ownership:** Only the session owner (same `credential_id`) or an admin
(`auth.CredentialType == "admin_token"`) can report usage on a session. The
handler loads the session record, compares `credential_id` against the request's
`apikit.AuthInfo`, and returns 403 if neither matches and the caller is not an
admin token. This is identical to the ownership enforcement on
`POST /api/v1/sessions/:id/complete` and prevents cross-session token pollution
by other credentials.

Request body:

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `id` | No | Generated UUID | Idempotency key |
| `model` | **Yes** | — | Model identifier |
| `input_tokens` | No | `0` | Input tokens consumed |
| `output_tokens` | No | `0` | Output tokens consumed |
| `cache_read_tokens` | No | `0` | Cached tokens read |

The `workspace_slug` is resolved from the session record.

**Error conditions:**
- Session not found: 404 Not Found.
- Caller is not the session owner and is not an admin: 403 Forbidden.
- Session status is not `active` (i.e., is `completed`, `failed`, `timeout`, or
  `terminated`): 409 Conflict with body `{"error": "session is not active"}`.
  This prevents stale agents from accidentally reporting usage after a session
  has been closed or force-terminated.

Response (201 Created): the full `token_usage` record as stored:
```json
{
  "id": "string",
  "session_id": "string",
  "workspace_slug": "string",
  "model": "string",
  "input_tokens": 0,
  "output_tokens": 0,
  "cache_read_tokens": 0,
  "reported_at": "2026-09-01T13:00:00Z"
}
```

Permission: `sessions:write`.

#### GET /api/v1/sessions

List agent sessions with filtering.

**Access scoping:** Results are workspace-scoped based on the caller's identity.
Non-admin credentials (API keys and PATs with `sessions:read`) only see sessions
for workspaces they have access to: for API keys, the key owner must be a member
of the org that owns the workspace; for PATs, the PAT must have been granted
access to the workspace's org. Admin tokens see sessions across all workspaces.
This matches the workspace attribution auth model established in spec 17.

**Explicit workspace filter and access control:** When a non-admin caller
supplies a `workspace_slug` query parameter for a workspace they cannot access,
the endpoint returns **403 Forbidden** — consistent with `GET /api/v1/sessions/:id`
and `GET /api/v1/workspaces/:slug/cost`. Returning an empty list would
incorrectly imply the workspace has no sessions; returning 403 surfaces the
access failure explicitly. When no `workspace_slug` filter is provided, the
endpoint simply excludes inaccessible workspaces from results (no 403 is raised
for the filter-absent case).

Query parameters:

| Parameter | Type | Description |
|---|---|---|
| `workspace_slug` | string | Filter by workspace (403 if caller cannot access this workspace) |
| `run_id` | string | Filter by run ID |
| `status` | string | Filter: `active`, `completed`, `failed`, `timeout`, `terminated` |
| `credential_type` | string | Filter by credential type |
| `since` | string (ISO 8601) | Sessions started after |
| `order` | string | Sort by `started_at`: `asc` or `desc` (default `desc`) |
| `limit` | integer | Page size, default 50, max 500 |
| `cursor` | string | Opaque pagination cursor (see below) |

**Cursor pagination:** Keyset-based. The cursor encodes a `(started_at, id)`
tuple, URL-safe base64-encoded (RFC 4648 §5, no padding), matching the cursor
strategy used in spec 17. For `desc` order, the filter is
`WHERE (started_at, id) < (cursor_started_at, cursor_id)`; for `asc` order,
`WHERE (started_at, id) > (cursor_started_at, cursor_id)`. Cursors are opaque
to clients and must not be constructed or parsed by callers.

Response (200 OK):
```json
{
  "sessions": [
    {
      "id": "string",
      "workspace_slug": "string",
      "run_id": "string",
      "node_id": "string",
      "credential_id": "string",
      "credential_type": "string",
      "archetype": "string",
      "model": "string",
      "status": "active|completed|failed|timeout|terminated",
      "started_at": "2026-09-01T13:00:00Z",
      "ended_at": "2026-09-01T14:00:00Z",
      "duration_ms": 0,
      "metadata": null,
      "token_summary": {
        "total_input_tokens": 0,
        "total_output_tokens": 0,
        "total_cache_read_tokens": 0,
        "models_used": ["string"]
      }
    }
  ],
  "next_cursor": "string|null",
  "has_more": true
}
```

`token_summary` is computed by aggregating `token_usage` rows at query time.
`models_used` is the distinct list of model identifiers reported via usage
records for that session. No total count field is included.

Permission: `sessions:read`.

#### GET /api/v1/sessions/:id

Fetch a single session record by ID.

**Access scoping:** Non-admin callers can only retrieve sessions in workspaces
they have access to (same membership rules as `GET /api/v1/sessions`). If the
session exists but the caller lacks workspace access, return 403 Forbidden.
Admin tokens can retrieve any session.

Response (200 OK): the same session object shape as entries in the list response:
```json
{
  "id": "string",
  "workspace_slug": "string",
  "run_id": "string",
  "node_id": "string",
  "credential_id": "string",
  "credential_type": "string",
  "archetype": "string",
  "model": "string",
  "status": "active|completed|failed|timeout|terminated",
  "started_at": "2026-09-01T13:00:00Z",
  "ended_at": "2026-09-01T14:00:00Z",
  "duration_ms": 0,
  "metadata": null,
  "token_summary": {
    "total_input_tokens": 0,
    "total_output_tokens": 0,
    "total_cache_read_tokens": 0,
    "models_used": ["string"]
  }
}
```

**Error conditions:**
- Session not found: 404 Not Found.
- Session exists but caller cannot access its workspace: 403 Forbidden.

Permission: `sessions:read`.

#### GET /api/v1/sessions/:id/usage

Query token usage records for a session. Paginated via keyset cursor.

**Access scoping:** Non-admin callers can only query usage for sessions in
workspaces they have access to (same workspace-membership rules as
`GET /api/v1/sessions`). The handler loads the session record, checks workspace
access, and returns 403 if the caller cannot access that workspace. Admin tokens
can query usage for any session.

**Unbounded totals:** The `totals` field always aggregates **all** `token_usage`
records ever recorded for the session — there is no time-range filtering on this
endpoint. This endpoint answers "how much did this session cost in total?"; time-bounded
aggregation across sessions is the responsibility of `GET /api/v1/workspaces/:slug/cost`.

Query parameters:

| Parameter | Type | Description |
|---|---|---|
| `order` | string | Sort by `reported_at`: `asc` or `desc` (default `desc`) |
| `limit` | integer | Page size, default 100, max 1000 |
| `cursor` | string | Opaque pagination cursor (see below) |

**Cursor pagination:** Keyset-based. The cursor encodes a `(reported_at, id)`
tuple, URL-safe base64-encoded (RFC 4648 §5, no padding), matching the cursor
strategy used across all paginated endpoints in this subsystem. For `desc`
order, the filter is `WHERE (reported_at, id) < (cursor_reported_at, cursor_id)`;
for `asc` order, `WHERE (reported_at, id) > (cursor_reported_at, cursor_id)`.
Cursors are opaque to clients and must not be constructed or parsed by callers.

Response (200 OK):
```json
{
  "session_id": "string",
  "records": [
    {
      "id": "string",
      "session_id": "string",
      "workspace_slug": "string",
      "model": "string",
      "input_tokens": 0,
      "output_tokens": 0,
      "cache_read_tokens": 0,
      "reported_at": "2026-09-01T13:00:00Z"
    }
  ],
  "totals": {
    "total_input_tokens": 0,
    "total_output_tokens": 0,
    "total_cache_read_tokens": 0,
    "models_used": ["string"]
  },
  "next_cursor": "string|null",
  "has_more": true
}
```

`totals` reflects the aggregate across **all** usage records for the session
(not just the current page). `records` is the paginated subset for the current
page.

Permission: `sessions:read`.

### Cost Summary

#### GET /api/v1/workspaces/:slug/cost

Returns aggregated token usage for a workspace over a time period.

**Access scoping:** Non-admin credentials can only query cost data for workspaces
they have access to (same membership rules as `GET /api/v1/sessions`). Querying
a workspace slug the caller cannot access returns 403 Forbidden. Admin tokens
can query any workspace slug.

Query parameters:

| Parameter | Type | Description |
|-----------|------|-------------|
| `since` | string (ISO 8601) | Start of period (default: 24 hours ago) |
| `until` | string (ISO 8601) | End of period (default: now) |
| `group_by` | string | `day`, `session`, or `model` (default: `day`) |

**Parameter validation:** If `since >= until`, the endpoint returns
**400 Bad Request** with body `{"error": "since must be before until"}`.
Invalid time ranges are a client error and must be surfaced explicitly rather
than returning a misleading empty result set.

Response (200 OK):
```json
{
  "workspace": "string",
  "period": {
    "since": "2026-09-01T00:00:00Z",
    "until": "2026-09-02T00:00:00Z"
  },
  "totals": {
    "input_tokens": 0,
    "output_tokens": 0,
    "cache_read_tokens": 0,
    "sessions": 0
  },
  "breakdown": [
    {
      "<discriminator>": "string",
      "input_tokens": 0,
      "output_tokens": 0,
      "cache_read_tokens": 0,
      "sessions": 0
    }
  ]
}
```

**`sessions` count semantics:** The `sessions` field in both `totals` and each
`breakdown` entry counts the number of **distinct session IDs that have at
least one `token_usage` record** within the specified time period (i.e., where
`token_usage.reported_at` falls within `[since, until)`). Sessions that were
open during the period but reported no token usage are excluded. This reflects
actual token-consuming activity, which is the operationally meaningful metric
for cost tracking.

The `breakdown` array element contains a single discriminator field named after
the `group_by` dimension, plus the same token count fields as `totals`:

| `group_by` value | Discriminator field name | Value example |
|---|---|---|
| `day` | `date` | `"2026-09-01"` |
| `session` | `session_id` | `"uuid-string"` |
| `model` | `model` | `"claude-3-5-sonnet"` |

Token counts only — no dollar amounts (hub doesn't know model pricing).

Permission: `sessions:read`.

### Permissions

The `sessions:read` and `sessions:write` permission scopes were created in
spec 17 alongside `audit:read` and `audit:write` in
`internal/audit/permissions.go` via a `Permissions() []apikit.Permission`
function. All four scopes are already defined; this spec does not modify
`permissions.go`. Admin tokens and API keys receive implicit full access to all
scopes. PATs require explicit scope grants at issuance time.

### Session Force-Close on Workspace Lifecycle

#### Archived Workspaces

When a workspace is archived, the hub force-closes all active `agent_sessions`
for that workspace:
- Set `status` to `terminated`
- Set `ended_at` to the archive timestamp
- Set `error_message` to `"workspace archived"`
- Emit a `hub.session.force_closed` audit event for each (via `Emitter`)

This runs within the archive handler, after workspace status update and before
response.

#### Deleted Workspaces

Same force-close behavior before workspace row deletion. Happens within the
delete handler before cascade deletes. The `error_message` is set to
`"workspace deleted"`.

**Force-close audit event shape:** The force-close emits a `HubEvent` (defined
in spec 17) with the following fields for each affected session:

```
HubEvent{
  EventType:    "hub.session.force_closed",
  ActorType:    "system",
  ActorID:      "",               // empty string: no user actor for system-initiated events
  ResourceType: "session",
  ResourceID:   <session_id>,
  Action:       "terminate",
  Workspace:    <workspace_slug>,
  Metadata: map[string]any{
    "session_id": <session_id>,
    "reason":     "workspace archived" | "workspace deleted",
  },
}
```

`ActorID` is set to empty string for system-initiated events. Empty string is
the conventional "no actor" value for `ActorType == "system"`, consistent with
the `HubEvent` struct definition in spec 17. This follows the `HubEvent` struct
defined in spec 17 and uses the `Emitter` introduced in spec 18.

**Cross-database consistency:** Sessions are in DuckDB, workspace deletion is
in SQLite. True transactional atomicity is not possible. Approach: force-close
sessions in DuckDB first, then delete workspace in SQLite. If SQLite delete
fails after DuckDB close, sessions remain closed (safe). If DuckDB close fails,
log warning but proceed with workspace deletion.

**Audit data is retained, not cascade-deleted.** Audit data has forensic value
that outlives the workspace. Orphaned audit data is cleaned up by the retention
worker after a configurable grace period.

### Session Status Values

| Status | Set by | Description |
|---|---|---|
| `active` | `POST /sessions` | Session is open and accepting usage reports |
| `completed` | `POST /sessions/:id/complete` | Session ended successfully |
| `failed` | `POST /sessions/:id/complete` | Session ended with an error |
| `timeout` | `POST /sessions/:id/complete` | Session ended due to timeout |
| `terminated` | Force-close logic only | Session closed by workspace archive/delete |

`terminated` is an internal-only status: it cannot be submitted via the
complete endpoint but is valid as a `status` filter value in
`GET /api/v1/sessions`.

### Prometheus Metrics Endpoint

#### GET /metrics

Prometheus-compatible scrape endpoint using `promhttp` from
`github.com/prometheus/client_golang`. Metrics use a custom registry (not the
global default) to avoid conflicts.

The endpoint is **permanently unauthenticated** on the public port. The hub
runs as a single-process internal service; Prometheus scrape endpoints are
conventionally unauthenticated. Network isolation is handled at the
deployment/infrastructure level, not the application level. If a deployment
requires scrape authentication, operators can add it via a reverse proxy. The
endpoint is mounted on the raw Echo instance outside the API group to avoid
auth middleware, following the same pattern as `internal/gitserver`.

**HTTP path normalization:** The Prometheus middleware uses Echo's route template
(`c.Path()`) as the `path` label value rather than the raw request URI. Because
Echo resolves the matched route pattern before the response is sent (e.g.,
`/api/v1/sessions/:id` rather than `/api/v1/sessions/some-uuid`), no custom
normalization is required. This is the standard Echo idiom for Prometheus
middleware and prevents cardinality explosion from unique path parameter values
such as session IDs and workspace slugs.

Metrics exposed:

| Metric | Type | Labels | Description |
|---|---|---|---|
| `afhub_http_requests_total` | Counter | `method`, `path`, `status` | Total HTTP requests |
| `afhub_http_request_duration_seconds` | Histogram | `method`, `path` | Request latency |
| `afhub_audit_events_total` | Counter | `source`, `event_type` | Audit events ingested |
| `afhub_agent_sessions_active` | Gauge | `workspace` | Currently active sessions |
| `afhub_agent_tokens_total` | Counter | `workspace`, `model`, `direction` | Token usage (input/output/cache_read) |
| `afhub_jobqueue_depth` | Gauge | `type`, `status` | Job queue depth (instrumented by the durable job queue subsystem defined in spec `durable_job_queue`; this spec registers the metric definition but the job queue implementation updates the gauge and is responsible for initializing label combinations at startup) |
| `afhub_sse_connections` | Gauge | — | Active SSE connections |
| `afhub_audit_table_rows` | Gauge | `table` | Row count per audit table (sampled hourly by retention worker) |
| `afhub_retention_last_run_timestamp_seconds` | Gauge | — | Unix timestamp of the last successful complete retention run. Enables alerting on a stalled worker (e.g., alert if value is older than 2 hours). |
| `afhub_retention_errors_total` | Counter | `step` | Total retention step failures, labeled by step name (e.g., `"agent_records"`, `"run_count"`, `"hub_events"`, `"sessions"`, `"token_usage"`, `"traces"`, `"postmortems"`, `"orphans"`, `"gauge_update"`). Enables alerting on repeated retention failures. |

**`afhub_jobqueue_depth` initialization:** This metric is defined in this spec's
custom Prometheus registry but its label combinations and gauge values are
owned by the `durable_job_queue` subsystem. The job queue implementation is
responsible for initializing its known `(type, status)` label combinations when
it starts, ensuring the gauge appears in scrape output immediately. Until the
job queue subsystem is running, the metric may be absent from scrape output —
this is a known gap tracked in the `durable_job_queue` spec.

**`afhub_agent_sessions_active` update path:** This gauge is decremented inline
on every session status transition that moves a session out of `active` —
including `POST /sessions/:id/complete` (all client-settable terminal statuses)
and force-close (setting `terminated`). In addition, the retention worker
recalibrates the gauge on each hourly run via `COUNT(*) WHERE status='active'
GROUP BY workspace_slug` to correct for any drift caused by crashed handlers or
other edge cases. This keeps the gauge near-real-time under normal operation
while providing periodic self-healing.

**Cardinality note:** `afhub_agent_sessions_active` is labeled per workspace.
The expected workspace count is low (tens to hundreds), making per-workspace
granularity operationally valuable (e.g., identifying which workspace has stuck
sessions). If cardinality becomes a concern at a specific deployment scale,
operators can apply Prometheus metric relabeling rules without requiring
application changes.

HTTP latency histogram buckets: 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5,
1.0, 2.5, 5.0, 10.0 seconds.

Metrics collected via Echo middleware (HTTP metrics) plus direct counter
increments from the emitter and session tracker.

**`afhub_audit_table_rows` sampling:** The gauge is updated by the retention
worker on each hourly run via `COUNT(*)` per audit table. This approach
provides sufficient freshness for capacity planning dashboards while avoiding
unpredictable DuckDB load that would result from running `COUNT(*)` queries on
every Prometheus scrape.

### Retention Policies

Background goroutine running every hour.

**Startup behavior:** The retention worker runs immediately on startup, then
repeats every hour thereafter. This ensures stale data from a previous hub
instance is cleaned up promptly without waiting up to one hour. The immediate
startup run uses the same logic as subsequent hourly runs — no special-casing
is required.

**Lifecycle:** The retention worker is started in `main.go` via
`go audit.StartRetentionWorker(ctx, store, sqliteDB)`. After the initial
immediate run, it uses a `time.Ticker` for the hourly interval and selects on
`ctx.Done()` between ticks, enabling clean shutdown via context cancellation
(e.g., on `SIGTERM`). During an active retention run, each step checks
`ctx.Err()` before executing. If the context is cancelled mid-run, the current
step's DuckDB operation completes (mid-transaction cancellation is unsafe in
DuckDB) and the worker exits without starting remaining steps. This provides
step-boundary shutdown: at most one extra step completes after shutdown is
requested.

Configuration:

| Variable | Default | Description |
|----------|---------|-------------|
| `AF_AUDIT_MAX_AGE_DAYS` | `90` | Max age for audit events |
| `AF_AUDIT_MAX_RUNS` | `50` | Max distinct run_ids to retain |
| `AF_TRACE_MAX_AGE_DAYS` | `30` | Max age for trace data |
| `AF_SESSION_MAX_AGE_DAYS` | `90` | Max age for completed sessions |
| `AF_POSTMORTEM_MAX_AGE_DAYS` | `180` | Max age for postmortems |
| `AF_AUDIT_ORPHAN_RETENTION_DAYS` | `30` | Grace period after workspace deletion |

Retention logic (executed in order):
1. Delete agent records older than `max_age_days` from all agent tables.
2. If distinct `run_id` count exceeds `max_runs`, delete oldest runs until
   at or below limit. "Oldest" is determined by `MIN(timestamp)` from
   `agent_audit_events` grouped by `run_id` — i.e., the timestamp of the
   first event in each run. This matches the behavior of `afaudit`'s
   `enforce_file_retention`, which sorts audit files by their first event
   timestamp. Runs are deleted in ascending order of that minimum timestamp
   until the total distinct `run_id` count is at or below `max_runs`.
3. Delete `hub_audit_events` older than `max_age_days`.
4. Delete completed `agent_sessions` older than `session_max_age_days`.
5. Delete orphaned `token_usage` rows (session_id references deleted session).
6. Delete `agent_traces` older than `trace_max_age_days`.
7. Delete `postmortems` older than `postmortem_max_age_days`.
8. Delete orphaned audit data (workspace slug not in `workspaces` table) older
   than `orphan_retention_days`.
9. Update `afhub_audit_table_rows` gauge with current `COUNT(*)` per audit table.
   Also recalibrate `afhub_agent_sessions_active` gauge via `COUNT(*) WHERE
   status='active' GROUP BY workspace_slug`.
10. Record successful run completion: set `afhub_retention_last_run_timestamp_seconds`
    to the current Unix timestamp. Log rows deleted per table via slog.

**Retention worker failure handling:** Steps execute sequentially and
independently. Before each step, `ctx.Err()` is checked; if the context is
already cancelled, the worker exits immediately. If a step fails (DuckDB
error), the error is logged via slog (with the table name and error),
`afhub_retention_errors_total` is incremented with the appropriate `step` label,
and the worker proceeds to the next step. The worker does not attempt partial
rollback — each DELETE is a discrete operation. `afhub_retention_last_run_timestamp_seconds`
is only updated after all steps complete (step 10); a run that fails any step
does not update this gauge, enabling stale-worker alerting.

The orphan detection requires cross-database query. Approach: query workspace
slugs from SQLite, then delete DuckDB records whose `workspace` is not in that
set and whose ingestion timestamp exceeds the grace period. No DuckDB
`sqlite_scanner` extension is needed — this is a simple application-level join.

## Required Test Coverage

The following scenarios **must** have explicit test coverage. All tests use the
Go standard `testing` package with a real DuckDB instance in `t.TempDir()`.

| Scenario | Description |
|---|---|
| **(a) Session open/close idempotency** | `POST /sessions` with a duplicate `id` returns 200 with the existing record. `POST /sessions/:id/complete` on a session already in any terminal state (`completed`, `failed`, `timeout`, `terminated`) returns 200 with the actual status in the response body. |
| **(b) Force-close on archive/delete** | Force-closing all active sessions for a workspace sets `status = terminated`, `ended_at`, and `error_message` correctly. Sessions in non-active states are not modified. The `afhub_agent_sessions_active` gauge is decremented for each force-closed session. The emitted `HubEvent` has `EventType = "hub.session.force_closed"`, `ActorType = "system"`, `ActorID = ""`, `ResourceType = "session"`, `Action = "terminate"`, and `Metadata` containing `session_id` and `reason`. |
| **(c) Cursor pagination correctness** | `GET /api/v1/sessions` and `GET /api/v1/sessions/:id/usage` return correct results across multiple pages; cursors do not skip or duplicate records under concurrent inserts. |
| **(d) Each retention step** | Each of the 10 retention steps is tested independently: correct rows are deleted, rows outside the criteria are retained, and step failure (simulated error) does not prevent subsequent steps from running. `afhub_retention_errors_total` is incremented on step failure; `afhub_retention_last_run_timestamp_seconds` is updated only when all steps complete. |
| **(e) Prometheus metric values** | After performing operations (session open, usage report, session close, force-close), the corresponding metric counters and gauges reflect the expected values. Specifically: `afhub_agent_sessions_active` is decremented on close and force-close, and recalibrated on the retention run. `afhub_retention_last_run_timestamp_seconds` is set after a successful complete run. |
| **(f) Ownership enforcement on complete/usage** | `POST /sessions/:id/complete` and `POST /sessions/:id/usage` return 403 when called by a non-owner non-admin credential (`credential_type != "admin_token"` and `credential_id` does not match); return 200/201 when called by the session owner or a credential with `credential_type == "admin_token"`. |
| **(g) Workspace access scoping on list, single-fetch, usage, and cost** | `GET /api/v1/sessions` with an explicit `workspace_slug` filter for an inaccessible workspace returns 403. `GET /api/v1/sessions/:id` returns 403 for sessions in inaccessible workspaces. `GET /api/v1/sessions/:id/usage` returns 403 for sessions in inaccessible workspaces. `GET /api/v1/workspaces/:slug/cost` returns 403 for inaccessible workspace slugs. Admin tokens see all results. |
| **(h) Cost endpoint parameter validation** | `GET /api/v1/workspaces/:slug/cost` with `since >= until` returns 400 with body `{"error": "since must be before until"}`. |
| **(i) Single session fetch** | `GET /api/v1/sessions/:id` returns the correct session record with `token_summary` aggregated from usage rows. Returns 404 for unknown IDs. Returns 403 for accessible-workspace check failures by non-admin callers. |
| **(j) Retention worker startup run** | The retention worker executes an immediate run on startup before the first ticker fires. |
| **(k) Session open validation** | `POST /api/v1/sessions` with a missing or empty `workspace_slug` returns 400 with body `{"error": "workspace_slug is required", "status": 400}`. |
| **(l) Usage totals are unbounded** | `GET /api/v1/sessions/:id/usage` `totals` reflect all usage records ever for the session regardless of pagination page or any time range — confirmed by inserting records at varied timestamps and verifying totals remain consistent across pages. |

## New Files

| File | Contents |
|------|----------|
| `internal/audit/metrics.go` | Custom Prometheus registry, metric definitions, middleware factory |
| `internal/audit/retention.go` | Background retention worker |

## Modified Files

| File | Change |
|------|-------|
| `internal/audit/handlers.go` | Add session, usage, cost, single-session-fetch handlers |
| `internal/audit/store.go` | Add session CRUD, token usage, cost aggregation, retention methods |
| `internal/audit/routes.go` | Register session, usage, cost, metrics routes (including `GET /api/v1/sessions/:id`) |
| `internal/audit/permissions.go` | **Already created in spec 17.** All four scopes (`audit:read`, `audit:write`, `sessions:read`, `sessions:write`) are defined there. This spec does not modify this file — it is listed for reference only. |
| `internal/workspace/handlers.go` | Call session force-close in archive and delete handlers |
| `cmd/af-hub/main.go` | Start retention worker, mount `/metrics`, register Prometheus middleware |

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `AF_AUDIT_MAX_AGE_DAYS` | `90` | Max age for audit events |
| `AF_AUDIT_MAX_RUNS` | `50` | Max distinct run_ids to retain |
| `AF_TRACE_MAX_AGE_DAYS` | `30` | Max age for trace data |
| `AF_SESSION_MAX_AGE_DAYS` | `90` | Max age for completed sessions |
| `AF_POSTMORTEM_MAX_AGE_DAYS` | `180` | Max age for postmortems |
| `AF_AUDIT_ORPHAN_RETENTION_DAYS` | `30` | Grace period after workspace deletion |

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 17_audit_storage_ingestion | 1 | 1 | DuckDB connection, schema (including `agent_sessions` and `token_usage` DDL), Store, permissions, types |
| 18_hub_audit_query | 2 | 1 | Emitter (for session force-close audit events), SSE gauge metric |
| durable_job_queue | — | — | `afhub_jobqueue_depth` gauge is defined here but owned and initialized by the durable job queue subsystem |

## Design Decisions

1. **Cross-database workspace lifecycle: best-effort ordering.** Force-close
   sessions in DuckDB first, then delete workspace in SQLite. If DuckDB fails,
   log warning and proceed. If SQLite fails after DuckDB success, sessions
   remain safely closed.

2. **Cost summary returns tokens only, not USD.** The hub doesn't know model
   pricing. Dollar amounts can be computed client-side using a pricing table.

3. **Retention worker uses cross-database orphan detection.** Query workspace
   slugs from SQLite in-memory, then filter DuckDB records. No DuckDB
   `sqlite_scanner` extension needed — simple application-level join.

4. **Retention worker is fault-tolerant per step.** Each deletion step runs
   independently. Before each step, `ctx.Err()` is checked for cancellation.
   Failures are logged via slog and increment `afhub_retention_errors_total`
   (labeled by step); the worker continues with the remaining steps to maximize
   pruning coverage on each run.

5. **Run-count retention uses earliest-event timestamp.** Consistent with
   `afaudit`'s `enforce_file_retention`: `MIN(timestamp)` from
   `agent_audit_events` grouped by `run_id` determines run age. Oldest runs
   (smallest minimum timestamp) are deleted first.

6. **Session ownership enforced on both complete and usage endpoints.** Both
   `POST /sessions/:id/complete` and `POST /sessions/:id/usage` compare the
   request's `apikit.AuthInfo` credential_id against the stored session record.
   Admin status is determined by `auth.CredentialType == "admin_token"`,
   matching the pattern in `internal/workspace/auth.go`. Non-owners who are not
   admin tokens receive 403. This prevents cross-session token pollution and
   ensures usage data integrity.

7. **`/metrics` is permanently unauthenticated on the public port.** Network
   isolation is handled at the deployment/infrastructure level. This matches
   the standard Prometheus convention and the existing `internal/gitserver`
   pattern in the hub. If a deployment requires scrape auth, operators can add
   it via a reverse proxy. No future spec is expected to add application-level
   scrape authentication.

8. **Test tooling: Go standard `testing` package with real DuckDB.** No testify.
   Tests instantiate a real DuckDB database in `t.TempDir()`, consistent with
   the patterns established in specs 17 and 18. Twelve scenario categories
   (idempotency, force-close, pagination, retention steps, metrics values,
   ownership enforcement, workspace access scoping, cost validation,
   single-session fetch, retention startup run, session open validation,
   unbounded usage totals) require explicit test coverage.

9. **`terminated` is an internal-only session status.** It is set exclusively
   by force-close logic (workspace archive/delete) and cannot be submitted via
   `POST /sessions/:id/complete`. It is a valid filter value in
   `GET /api/v1/sessions` so operators can identify force-closed sessions.
   Calling `POST /sessions/:id/complete` on a `terminated` session returns 200
   (idempotent), treating all terminal states uniformly.

10. **`cache_creation_input_tokens` is session-level only.** Cache creation
    spans multiple API calls and is most accurately known at session end. It is
    reported as a summary field on the complete endpoint only, not on the
    incremental usage report. Incremental usage reports capture per-call
    consumption (input, output, cache reads).

11. **`afhub_audit_table_rows` is sampled hourly by the retention worker.**
    Running `COUNT(*)` at scrape time would add unpredictable DuckDB load on
    every Prometheus pull. Hourly sampling is sufficient for capacity planning
    dashboards.

12. **Keyset cursor pagination for `GET /api/v1/sessions` and `GET /api/v1/sessions/:id/usage`.**
    `GET /api/v1/sessions` encodes a `(started_at, id)` tuple; `GET /api/v1/sessions/:id/usage`
    encodes a `(reported_at, id)` tuple. Both use URL-safe base64 (RFC 4648 §5,
    no padding), matching the strategy from spec 17. Keyset pagination is correct
    under concurrent inserts, unlike offset-based approaches. Default/max limits
    are 50/500 for sessions and 100/1000 for usage records.

13. **`sessions` in cost summary counts distinct token-reporting session IDs.**
    The `sessions` field in `totals` and `breakdown` counts distinct session IDs
    with at least one `token_usage` record in the period (filtered by
    `token_usage.reported_at`). Sessions that were open but reported no tokens
    are excluded. This reflects actual cost-incurring activity.

14. **Retention worker uses step-boundary shutdown.** The worker selects on
    `ctx.Done()` between ticks and checks `ctx.Err()` before each step. If
    cancelled mid-run, the current DuckDB operation completes (safe), remaining
    steps are skipped, and the goroutine exits. This avoids unsafe mid-transaction
    cancellation while ensuring prompt shutdown after the current step.

15. **Prometheus HTTP middleware uses `c.Path()` for path labels.** Echo's
    `c.Path()` returns the matched route template (e.g., `/api/v1/sessions/:id`)
    rather than the raw URI, preventing cardinality explosion from unique
    path parameter values. No custom normalization layer is required.

16. **`afhub_agent_sessions_active` is decremented inline and recalibrated hourly.**
    Every session status transition out of `active` (complete, fail, timeout,
    force-close) decrements the gauge immediately for near-real-time accuracy.
    The retention worker recalibrates the gauge via `COUNT(*) WHERE status='active'
    GROUP BY workspace_slug` on each hourly run to self-heal any drift from
    crashed handlers or edge cases.

17. **`GET /api/v1/sessions`, `GET /api/v1/sessions/:id`, `GET /api/v1/sessions/:id/usage`,
    and the cost endpoint are all workspace-access-scoped.** Non-admin credentials
    see only sessions and cost data for workspaces in orgs they belong to. For
    `GET /api/v1/sessions`, an explicit `workspace_slug` filter targeting an
    inaccessible workspace returns 403 Forbidden (consistent with single-fetch
    and cost endpoints); when no filter is supplied, inaccessible workspaces
    are silently excluded from results. Admin tokens see all workspaces.

18. **`permissions.go` is owned by spec 17; not modified by this spec.** All
    four permission scopes (`audit:read`, `audit:write`, `sessions:read`,
    `sessions:write`) were defined in spec 17. This spec references those scopes
    but makes no changes to the file, preventing conflicting ownership between
    spec implementers.

19. **Admin status is determined by `credential_type == "admin_token"`.** This
    matches the pattern established in `internal/workspace/auth.go` (`isAdmin`
    check). The three credential types in the platform are `admin_token`,
    `api_key`, and `pat`. No additional role or scope check is needed.

20. **`hub.session.force_closed` event uses the `HubEvent` struct from spec 17.**
    The event is emitted via the `Emitter` (spec 18) with `ActorType = "system"`,
    `ActorID = ""` (empty string — the conventional "no actor" value for
    system-initiated events), `ResourceType = "session"`, `Action = "terminate"`,
    and `Metadata` containing `session_id` and `reason` (`"workspace archived"`
    or `"workspace deleted"`).

21. **Retention worker exposes `afhub_retention_last_run_timestamp_seconds`
    and `afhub_retention_errors_total` for operational observability.** The
    timestamp gauge is updated only after a fully complete run, enabling
    stale-worker alerting (e.g., alert if older than 2 hours). The error counter
    is labeled by step name, enabling per-step failure tracking. Both metrics
    are cheap to implement and provide high operational value.

22. **Retention worker runs immediately on startup.** The initial run fires
    before the first `time.Ticker` tick to promptly clean up stale data from
    a previous hub instance. Subsequent runs follow the hourly cadence. No
    special-casing is required — the startup run and hourly runs share identical
    logic.

23. **Cost endpoint returns 400 for invalid time ranges.** If `since >= until`,
    the endpoint returns 400 Bad Request with `{"error": "since must be before
    until"}`. This prevents misleading empty results and surfaces client errors
    explicitly.

24. **Session open validation uses `apikit.WriteAPIError`.** Missing or empty
    `workspace_slug` on `POST /api/v1/sessions` returns 400 via
    `apikit.WriteAPIError(c, 400, message)`, producing `{"error": "...", "status": 400}`.
    This is consistent with the error-response pattern used across all hub
    handlers.

25. **`afhub_jobqueue_depth` initialization is deferred to `durable_job_queue`.**
    This spec defines the metric in the custom Prometheus registry but does not
    initialize label combinations. The `durable_job_queue` subsystem owns gauge
    updates and must initialize its known `(type, status)` label combinations at
    startup to ensure the metric appears in scrape output before any jobs are
    processed. Until the job queue is running, the gauge may be absent from
    scrape output — this is a documented known gap.

26. **`GET /api/v1/sessions/:id/usage` `totals` are intentionally unbounded.**
    The endpoint aggregates all `token_usage` records ever recorded for the
    session — no time-range filtering is offered. This answers "how much did
    this session cost in total?" Time-bounded aggregation across sessions is
    the cost endpoint's responsibility. No `since`/`until` parameters will be
    added to this endpoint in future iterations without a separate spec.
