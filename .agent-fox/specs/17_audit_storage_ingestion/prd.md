---
spec_id: '17'
spec_name: audit_storage_ingestion
title: Audit Storage Ingestion
status: draft
created_at: '2026-09-01T13:16:15.479326+00:00'
updated_at: '2026-09-01T13:32:43.449638+00:00'
owner: ''
source: docs/prd/prd14.md
schema_version: 1
---
# Audit Storage and Agent Telemetry Ingestion

## Source

File: `docs/prd/prd14.md` (split 1 of 3)

## Intent

The hub coordinates workspaces, merges, patches, and git operations for
multiple agents and operators. None of these operations leave a structured
trail. Nightshift agents running afaudit produce rich structured telemetry
with no destination beyond local JSONL files.

This spec defines the foundation layer of the audit subsystem: a DuckDB-backed
storage engine and REST API endpoints for ingesting agent telemetry. It
introduces the `internal/audit` package with schema initialization, a data
store, an Emitter interface, type definitions, validation helpers, and
permission scopes. Agent-submitted data — audit events, session outcomes, tool
calls, tool errors, trace events, and postmortem reports — flows in through
REST endpoints that accept the exact JSON shapes produced by afaudit.

## Goals

- Accept audit events from nightshift agents in the exact JSON format produced
  by afaudit's `event_to_json` serializer, requiring zero changes to the
  afaudit data model.
- Accept session outcomes, tool calls, tool errors, agent trace events, and
  postmortem reports via dedicated REST endpoints.
- Provide batch ingestion endpoints for bulk upload of existing JSONL files
  (events and traces only — see Non-Goals for exclusions).
- Use client-generated UUIDs as idempotency keys so retried submissions produce
  no duplicates.
- Store all ingested data in DuckDB with columnar storage optimized for
  analytical queries.
- Provide per-run query endpoints for all ingested data types with cursor-based
  pagination, per-endpoint filter parameters (including time-range filters on
  all GET endpoints), and configurable sort order on all GET endpoints.
- Preserve the existing single-binary deployment model (accepting CGo for
  DuckDB as the sole exception).
- Define an `Emitter` interface that downstream specs use for hub-internal
  audit emission.
- Define four PAT permission scopes: `audit:read`, `audit:write`,
  `sessions:read`, `sessions:write`.

## Non-Goals

- Hub-internal audit event emission (spec 18: hub_audit_query).
- Unified cross-source audit query API (spec 18: hub_audit_query).
- SSE streaming endpoint (spec 18: hub_audit_query).
- Agent session lifecycle management (spec 19: sessions_metrics_retention).
- Prometheus metrics endpoint (spec 19: sessions_metrics_retention).
- Retention policies and background workers (spec 19: sessions_metrics_retention).
- Transcript reconstruction (spec 18: hub_audit_query).
- Server-side postmortem computation — only pre-computed ingestion.
- Modifying afaudit's data model or event type enum.
- OTel integration — deferred. Schema includes nullable `trace_id` for
  forward-compatibility only.
- Batch ingestion endpoints for `sessions/outcomes`, `tools/calls`, and
  `tools/errors` — these record types are low-volume (one per session or tool
  invocation) and afaudit JSONL bulk-upload files contain events and traces
  only. Batch variants for the remaining types can be added in a future spec
  if needed.
- Application-level rate limiting on audit ingestion endpoints. Write
  contention is handled by DuckDB's internal write serialization and the HTTP
  503/`Retry-After: 5` mechanism. Proactive rate limiting is deferred to the
  reverse proxy or infrastructure layer.

## Tech Stack

- Go 1.26+
- DuckDB via `github.com/duckdb/duckdb-go` (official DuckDB-maintained Go driver,
  CGo required, `database/sql` compatible driver)
- Echo v4 (existing HTTP framework)
- apikit (auth, error responses, timestamps)
- **Test tooling:** Standard `testing` package only (no third-party assertion
  libraries; the codebase explicitly avoids testify). Tests use a real
  temp-file DuckDB instance per test, created in `t.TempDir()`. The `Emitter`
  interface is tested with a mock implementation defined inline in `_test.go`
  files.

## Functional Requirements

### DuckDB Storage Layer

The audit subsystem uses DuckDB for all audit and telemetry data, separate from
the hub's operational SQLite database. DuckDB's columnar storage provides
10-100x faster analytical queries (aggregations, time-range scans, group-by)
compared to SQLite for the write-once, read-many audit workload.

The DuckDB file lives alongside the SQLite database at a configurable path
(`AF_AUDIT_DB_PATH`, default: `<data_dir>/audit.duckdb`). `<data_dir>` is
resolved from the existing hub convention: it is the directory containing the
SQLite database file, derived from the `database.path` field in `config.toml`.
Implementers should follow the existing config resolution logic in
`cmd/af-hub/main.go` to obtain this directory. If `AF_AUDIT_DB_PATH` is
explicitly set, it overrides this default entirely.

Before opening the DuckDB connection, the hub creates any missing parent
directories in the resolved path via `os.MkdirAll`. This aligns with the hub's
existing behavior for the SQLite database path and simplifies container
deployments where the data directory is a mounted volume. If `os.MkdirAll`
fails (e.g., due to permission errors), hub startup aborts with an error.

The hub opens the DuckDB connection at startup via `audit.OpenDB(path)` and
passes it to the `internal/audit` package. Schema is initialized via
`audit.InitSchema(db *sql.DB)` using `CREATE TABLE IF NOT EXISTS` followed by
`ALTER TABLE ADD COLUMN IF NOT EXISTS` migrations (see Schema Migration below).

#### DuckDB Driver Usage and Constraints

The hub uses `github.com/duckdb/duckdb-go` as a `database/sql`-compatible
driver. The DSN is a plain file path (e.g., `/data/audit.duckdb`). An
in-memory DSN (`:memory:`) is used in tests via `t.TempDir()`-based temp
files to keep test isolation without pure in-memory state sharing.

**Concurrency model:** DuckDB supports only one concurrent writer. Concurrent
write requests from the `database/sql` connection pool queue internally rather
than erroring immediately — DuckDB serializes writers at the storage level.
The hub does not add an application-level mutex. If a write times out under
sustained contention, the Store method returns an error and the handler
responds with HTTP 503 and a `Retry-After: 5` header.

**Known failure modes:**

| Failure | Behavior |
|---------|----------|
| File locked by another process | `OpenDB` returns error; hub startup aborts |
| Corrupted database file | DuckDB returns error on first query; logged and surfaced as HTTP 500 |
| Disk full during write | DuckDB returns error; surfaced as HTTP 500 (no partial commit) |
| Write timeout under contention | Store returns error; handler returns HTTP 503 with `Retry-After: 5` |
| Missing parent directory | `os.MkdirAll` called before `OpenDB`; startup aborts if directory creation fails |

**CGo build requirement:** The `duckdb-go` driver requires CGo. The hub binary
must be built with `CGO_ENABLED=1`. Cross-compilation without a matching C
toolchain is not supported for this package. All CI jobs that build or test
the `internal/audit` package must run on a host with CGo available.

Tables:

- `agent_audit_events` — afaudit AuditEvent records
- `hub_audit_events` — hub-internal audit events (schema defined here, populated by spec 18)
- `session_outcomes` — afaudit SessionOutcome records
- `tool_calls` — afaudit ToolCall records
- `tool_errors` — afaudit ToolError records
- `agent_traces` — AgentTraceSink records
- `postmortems` — pre-computed postmortem reports
- `agent_sessions` — hub-managed session lifecycle (schema defined here, populated by spec 19)
- `token_usage` — per-session token usage records (schema defined here, populated by spec 19)

All tables use DuckDB-native types: `VARCHAR`, `TIMESTAMPTZ`, `BOOLEAN`,
`JSON`, `VARCHAR[]`, `BIGINT`. No explicit indexes — DuckDB uses adaptive
zonemap-based min/max statistics.

### Database Schema

The following DDL is the canonical source of truth for all audit tables. All
nine tables are created during `audit.InitSchema(db *sql.DB)` using
`CREATE TABLE IF NOT EXISTS` statements executed in a single transaction.

```sql
-- Agent audit events (afaudit AuditEvent records)
CREATE TABLE IF NOT EXISTS agent_audit_events (
    id           VARCHAR PRIMARY KEY,
    run_id       VARCHAR NOT NULL,
    workspace    VARCHAR NOT NULL,
    event_type   VARCHAR NOT NULL,
    severity     VARCHAR NOT NULL,
    node_id      VARCHAR NOT NULL DEFAULT '',
    session_id   VARCHAR NOT NULL DEFAULT '',
    archetype    VARCHAR NOT NULL DEFAULT '',
    payload      JSON    NOT NULL DEFAULT '{}',
    trace_id     VARCHAR,          -- nullable, reserved for OTel forward-compat
    timestamp    TIMESTAMPTZ NOT NULL,
    ingested_at  TIMESTAMPTZ NOT NULL
);

-- Hub-internal audit events (populated by spec 18: hub_audit_query)
CREATE TABLE IF NOT EXISTS hub_audit_events (
    id            VARCHAR PRIMARY KEY,
    event_type    VARCHAR NOT NULL,
    actor_id      VARCHAR NOT NULL,
    actor_type    VARCHAR NOT NULL,   -- admin_token | api_key | pat | system
    resource_type VARCHAR NOT NULL,
    resource_id   VARCHAR NOT NULL,
    action        VARCHAR NOT NULL,
    workspace     VARCHAR NOT NULL DEFAULT '',
    metadata      JSON    NOT NULL DEFAULT '{}',
    trace_id      VARCHAR,            -- nullable, reserved for OTel forward-compat
    timestamp     TIMESTAMPTZ NOT NULL,
    ingested_at   TIMESTAMPTZ NOT NULL
);

-- Session outcomes (afaudit SessionOutcome records)
CREATE TABLE IF NOT EXISTS session_outcomes (
    id           VARCHAR PRIMARY KEY,
    run_id       VARCHAR NOT NULL,
    workspace    VARCHAR NOT NULL,
    session_id   VARCHAR NOT NULL,
    archetype    VARCHAR NOT NULL DEFAULT '',
    node_id      VARCHAR NOT NULL DEFAULT '',
    status       VARCHAR NOT NULL,
    exit_code    BIGINT,
    duration_ms  BIGINT,
    token_input  BIGINT,
    token_output BIGINT,
    error        VARCHAR,
    metadata     JSON    NOT NULL DEFAULT '{}',
    timestamp    TIMESTAMPTZ NOT NULL,
    ingested_at  TIMESTAMPTZ NOT NULL
);

-- Tool calls (afaudit ToolCall records)
CREATE TABLE IF NOT EXISTS tool_calls (
    id           VARCHAR PRIMARY KEY,
    run_id       VARCHAR NOT NULL,
    workspace    VARCHAR NOT NULL,
    session_id   VARCHAR NOT NULL DEFAULT '',
    node_id      VARCHAR NOT NULL DEFAULT '',
    tool_name    VARCHAR NOT NULL,
    input        JSON    NOT NULL DEFAULT '{}',
    output       JSON,
    duration_ms  BIGINT,
    success      BOOLEAN NOT NULL DEFAULT TRUE,
    timestamp    TIMESTAMPTZ NOT NULL,
    ingested_at  TIMESTAMPTZ NOT NULL
);

-- Tool errors (afaudit ToolError records)
CREATE TABLE IF NOT EXISTS tool_errors (
    id           VARCHAR PRIMARY KEY,
    run_id       VARCHAR NOT NULL,
    workspace    VARCHAR NOT NULL,
    session_id   VARCHAR NOT NULL DEFAULT '',
    node_id      VARCHAR NOT NULL DEFAULT '',
    tool_name    VARCHAR NOT NULL,
    error_code   VARCHAR NOT NULL DEFAULT '',
    error_msg    VARCHAR NOT NULL,
    input        JSON    NOT NULL DEFAULT '{}',
    timestamp    TIMESTAMPTZ NOT NULL,
    ingested_at  TIMESTAMPTZ NOT NULL
);

-- Agent trace events (AgentTraceSink records)
CREATE TABLE IF NOT EXISTS agent_traces (
    id           VARCHAR PRIMARY KEY,
    run_id       VARCHAR NOT NULL,
    workspace    VARCHAR NOT NULL,
    session_id   VARCHAR NOT NULL DEFAULT '',
    node_id      VARCHAR NOT NULL DEFAULT '',
    event_type   VARCHAR NOT NULL,   -- session.init | assistant.message | tool.use | tool.error | session.result
    role         VARCHAR,
    content      VARCHAR,
    tool_name    VARCHAR,
    input        JSON,
    output       JSON,
    duration_ms  BIGINT,
    token_input  BIGINT,
    token_output BIGINT,
    timestamp    TIMESTAMPTZ NOT NULL,
    ingested_at  TIMESTAMPTZ NOT NULL
);

-- Pre-computed postmortem reports
CREATE TABLE IF NOT EXISTS postmortems (
    run_id          VARCHAR PRIMARY KEY,
    workspace       VARCHAR NOT NULL,
    schema_version  BIGINT  NOT NULL DEFAULT 1,
    run_status      VARCHAR NOT NULL,   -- stalled | block_limit | cost_limit | session_limit
    started_at      TIMESTAMPTZ NOT NULL,
    completed_at    TIMESTAMPTZ NOT NULL,
    task_summary    JSON    NOT NULL,
    cost_summary    JSON    NOT NULL,
    blocked_tasks   JSON    NOT NULL DEFAULT '[]',
    session_history JSON    NOT NULL DEFAULT '[]',
    ingested_at     TIMESTAMPTZ NOT NULL
);

-- Hub-managed agent sessions (schema defined here, populated by spec 19: sessions_metrics_retention)
CREATE TABLE IF NOT EXISTS agent_sessions (
    id           VARCHAR PRIMARY KEY,
    run_id       VARCHAR NOT NULL,
    workspace    VARCHAR NOT NULL,
    node_id      VARCHAR NOT NULL DEFAULT '',
    archetype    VARCHAR NOT NULL DEFAULT '',
    status       VARCHAR NOT NULL,   -- active | completed | failed | stalled
    started_at   TIMESTAMPTZ NOT NULL,
    closed_at    TIMESTAMPTZ,
    metadata     JSON    NOT NULL DEFAULT '{}',
    ingested_at  TIMESTAMPTZ NOT NULL
);

-- Per-session token usage (schema defined here, populated by spec 19: sessions_metrics_retention)
CREATE TABLE IF NOT EXISTS token_usage (
    id           VARCHAR PRIMARY KEY,
    session_id   VARCHAR NOT NULL,
    run_id       VARCHAR NOT NULL,
    workspace    VARCHAR NOT NULL,
    model        VARCHAR NOT NULL DEFAULT '',
    input_tokens  BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    timestamp    TIMESTAMPTZ NOT NULL,
    ingested_at  TIMESTAMPTZ NOT NULL
);
```

### Schema Migration

`audit.InitSchema(db *sql.DB)` runs in a single transaction and applies two phases:

1. **Table creation:** All nine `CREATE TABLE IF NOT EXISTS` statements are executed. For a fresh database, this creates all tables. For an existing database, these statements are no-ops.

2. **Column migrations:** After table creation, `ALTER TABLE ADD COLUMN IF NOT EXISTS` statements are run for any columns added in schema versions newer than the initial release. DuckDB natively supports `ADD COLUMN IF NOT EXISTS`, unlike SQLite. The initial schema version (this spec) has no `ALTER TABLE` migrations to run — the phase is a no-op but the pattern is established for future specs (18, 19) that may add columns to existing tables.

This approach ensures that operators upgrading a hub binary against an existing `audit.duckdb` file receive a transparent, non-destructive schema update with no manual intervention required.

### Store

The `Store` struct wraps `*sql.DB` (DuckDB) and provides CRUD methods for all
audit tables. Methods include insert (single and batch), query with filters,
and idempotent upsert via `INSERT OR IGNORE`.

### Emitter Interface

```go
type Emitter interface {
    Emit(ctx context.Context, event HubEvent) error
}
```

The `HubEvent` struct:

```go
type HubEvent struct {
    EventType    string
    ActorID      string
    ActorType    string         // one of: admin_token, api_key, pat, system
    ResourceType string
    ResourceID   string
    Action       string
    Workspace    string
    Metadata     map[string]any // free-form; per-event-type keys documented in spec 18
}
```

**`ActorType` values** correspond directly to apikit's `AuthInfo.CredentialType`:

| Value | Meaning |
|-------|---------|
| `admin_token` | Request authenticated with a hub admin token |
| `api_key` | Request authenticated with an API key |
| `pat` | Request authenticated with a Personal Access Token |
| `system` | Hub-internal background operations (no user credential) |

**`Metadata`** is a free-form `map[string]any`. The per-event-type metadata
keys are documented in the hub event emission points table in spec 18
(hub_audit_query). Schema validation is not applied to `Metadata` at ingestion
time. Downstream query consumers should treat unknown keys as additive.

The default `Emitter` implementation wraps the `Store` and writes hub events to
`hub_audit_events`. Emit failures are logged via slog and swallowed — audit
emission must never cause a handler to return an error to the client.

### afaudit Data Model Compatibility

The hub API accepts data in the exact shapes produced by the afaudit package.
No field renaming, no envelope wrapping, no schema transformation.

Field defaults and validation:

| Field | Required | Default if absent |
|---|---|---|
| `run_id` | Yes (from URL path) | — |
| `event_type` | Yes | — |
| `id` | No | Hub generates a UUID |
| `timestamp` | No | `apikit.NowUTC()` |
| `severity` | No | Per `default_severity_for()` mapping |
| `node_id` | No | `""` |
| `session_id` | No | `""` |
| `archetype` | No | `""` |
| `payload` | No | `{}` |

Default severity mapping (Go port of afaudit's `default_severity_for()`):

| Event Type | Default Severity |
|---|---|
| `session.fail` | `error` |
| `run.limit_reached` | `warning` |
| `git.conflict` | `warning` |
| `harvest.empty` | `warning` |
| `review.parse_failure` | `warning` |
| All others | `info` |

Valid severity values: `info`, `warning`, `error`, `critical`.

### run_id Validation

Format: `YYYYMMDD_HHMMSS_6hexchars` (e.g., `20260704_143022_a1b2c3`).

The canonical validation regexp used in `internal/audit/validate.go` is:

```
^\d{8}_\d{6}_[0-9a-f]{6}$
```

This pattern enforces:
- Eight decimal digits (date: `YYYYMMDD`)
- Underscore separator
- Six decimal digits (time: `HHMMSS`)
- Underscore separator
- Six **lowercase** hexadecimal characters (matching afaudit's convention of
  generating the suffix via `os.urandom(3).hex()`, which always produces
  lowercase hex)

Uppercase hex characters in the suffix are **not** accepted. Invalid `run_id`
returns HTTP 400. The `run_id` in the URL path must match any `run_id` in the
request body; mismatch returns HTTP 400.

### Event Type Validation

Agent audit events: the hub accepts any `event_type` string. Unknown types are
accepted, stored, and logged at `warning` level. This decouples hub deployment
from afaudit releases.

Trace events: `event_type` must be one of: `session.init`, `assistant.message`,
`tool.use`, `tool.error`, `session.result`. Unknown trace types return HTTP 400.

### Idempotency

Client-generated UUIDs serve as idempotency keys. Any string matching the
standard UUID format (`8-4-4-4-12` hex digits with dashes) is accepted. No
UUID version enforcement — afaudit uses `uuid.uuid4()` but the hub validates
only format, not version bits. If a record with the same `id` exists, the hub
returns HTTP 200 (not 201) with the same minimal acknowledgement shape as the
original 201 response, and does not modify the stored record. Uses
`INSERT OR IGNORE` at the DuckDB level. Postmortems use `run_id` as the
uniqueness key.

### Workspace Attribution

All agent-submitted data is attributed to a workspace via the URL path
(`/api/v1/workspaces/:slug/runs/:run_id/...`). The workspace slug is stored
alongside every agent record.

Workspace resolution follows these paths:
- **Workspace-scoped tokens**: Token's workspace must match URL `:slug`.
  Mismatch returns HTTP 403 `workspace_mismatch`.
- **Generic tokens**: Hub verifies token owner has write access to workspace.
  No access returns HTTP 403 `workspace_access_denied`.
- **Admin tokens**: No workspace restrictions.

Archived workspaces reject ingestion with HTTP 409 `workspace_archived`.

### Agent Authentication

Nightshift agents submitting telemetry to ingestion endpoints may authenticate
using either of two credential types:

| Credential | Access |
|------------|--------|
| **PAT with `audit:write` + `sessions:write` scopes** | Recommended for least-privilege. Workspace-scoped PATs restrict the token to a single workspace; generic PATs require the token owner to have write access to the target workspace. |
| **API key** | Implicit full access (no scope grants required). Suitable for simpler operator setups. |

Admin tokens also have implicit full access but are not recommended for
automated agent use. The workspace attribution rules (scoped vs. generic token
vs. admin token) apply equally to both PAT and API key credentials, as defined
in the Workspace Attribution section above.

### Ingestion Endpoints

All endpoints use the path prefix `/api/v1/workspaces/:slug/runs/:run_id/`.

| Method | Path Suffix | Description |
|--------|------------|-------------|
| POST | `events` | Ingest single audit event |
| POST | `events/batch` | Ingest audit event batch (max 1000) |
| GET | `events` | Query events for a run |
| POST | `sessions/outcomes` | Record session outcome |
| GET | `sessions/outcomes` | Query session outcomes |
| POST | `tools/calls` | Record tool call |
| GET | `tools/calls` | Query tool calls |
| POST | `tools/errors` | Record tool error |
| GET | `tools/errors` | Query tool errors |
| POST | `traces` | Ingest single trace event |
| POST | `traces/batch` | Ingest trace event batch (max 1000) |
| GET | `traces` | Query traces for a run |
| POST | `postmortem` | Submit postmortem report |
| GET | `postmortem` | Retrieve postmortem report (single-record lookup by `run_id`; HTTP 404 if not found) |

There are no batch endpoints for `sessions/outcomes`, `tools/calls`, or
`tools/errors`. These record types are low-volume (one record per session or
tool invocation) and are not present in the afaudit JSONL files targeted by
bulk upload. See Non-Goals.

### Query Endpoint Filter Parameters

All GET query endpoints support cursor-based pagination (`cursor`, `limit`) and
the per-endpoint filter parameters listed below. All filter parameters are
optional query string values. All GET endpoints support time-range filtering
via `since` and `until`, and sort-order control via `order`.

**Note on GET `postmortem`:** `GET .../postmortem` is a single-record lookup
by the `:run_id` URL parameter, not a paginated list. A run has at most one
postmortem. Pagination, `since`/`until`, and `order` parameters do not apply.
If no postmortem exists for the run, the handler returns HTTP 404 with an
apikit error body.

#### GET `events` — Audit Events

| Parameter | Type | Description |
|-----------|------|-------------|
| `event_type` | string | Filter by exact event type |
| `severity` | string | Filter by severity (`info`, `warning`, `error`, `critical`) |
| `node_id` | string | Filter by node identifier |
| `since` | ISO 8601 string | Include only events with `timestamp >= since` |
| `until` | ISO 8601 string | Include only events with `timestamp <= until` |
| `order` | string | Sort order: `asc` or `desc` (default: `asc`) |
| `cursor` | string | Opaque base64 cursor from previous response |
| `limit` | integer | Max records to return (default 100, max 1000) |

#### GET `sessions/outcomes` — Session Outcomes

| Parameter | Type | Description |
|-----------|------|-------------|
| `node_id` | string | Filter by node identifier |
| `status` | string | Filter by outcome status |
| `since` | ISO 8601 string | Include only outcomes with `timestamp >= since` |
| `until` | ISO 8601 string | Include only outcomes with `timestamp <= until` |
| `order` | string | Sort order: `asc` or `desc` (default: `asc`) |
| `cursor` | string | Opaque base64 cursor from previous response |
| `limit` | integer | Max records to return (default 100, max 1000) |

#### GET `tools/calls` — Tool Calls

| Parameter | Type | Description |
|-----------|------|-------------|
| `node_id` | string | Filter by node identifier |
| `session_id` | string | Filter by session identifier |
| `tool_name` | string | Filter by tool name |
| `since` | ISO 8601 string | Include only tool calls with `timestamp >= since` |
| `until` | ISO 8601 string | Include only tool calls with `timestamp <= until` |
| `order` | string | Sort order: `asc` or `desc` (default: `asc`) |
| `cursor` | string | Opaque base64 cursor from previous response |
| `limit` | integer | Max records to return (default 100, max 1000) |

#### GET `tools/errors` — Tool Errors

| Parameter | Type | Description |
|-----------|------|-------------|
| `node_id` | string | Filter by node identifier |
| `session_id` | string | Filter by session identifier |
| `tool_name` | string | Filter by tool name |
| `since` | ISO 8601 string | Include only tool errors with `timestamp >= since` |
| `until` | ISO 8601 string | Include only tool errors with `timestamp <= until` |
| `order` | string | Sort order: `asc` or `desc` (default: `asc`) |
| `cursor` | string | Opaque base64 cursor from previous response |
| `limit` | integer | Max records to return (default 100, max 1000) |

#### GET `traces` — Agent Trace Events

| Parameter | Type | Description |
|-----------|------|-------------|
| `event_type` | string | Filter by trace event type (one of the five valid values) |
| `node_id` | string | Filter by node identifier |
| `since` | ISO 8601 string | Include only trace events with `timestamp >= since` |
| `until` | ISO 8601 string | Include only trace events with `timestamp <= until` |
| `order` | string | Sort order: `asc` or `desc` (default: `asc`) |
| `cursor` | string | Opaque base64 cursor from previous response |
| `limit` | integer | Max records to return (default 100, max 1000) |

### Response Body Shapes

#### POST Ingestion — Single Record (HTTP 201 created, HTTP 200 duplicate)

All single-record POST endpoints return a minimal acknowledgement. Duplicate
submissions (HTTP 200) return the same shape as the original 201 response.

**POST `events`:**
```json
{
  "id": "...",
  "run_id": "...",
  "event_type": "...",
  "severity": "...",
  "created_at": "2026-09-01T13:16:15Z"
}
```

**POST `sessions/outcomes`:**
```json
{
  "id": "...",
  "run_id": "...",
  "node_id": "...",
  "status": "...",
  "created_at": "2026-09-01T13:16:15Z"
}
```

**POST `tools/calls`:**
```json
{
  "id": "...",
  "run_id": "...",
  "tool_name": "...",
  "called_at": "2026-09-01T13:16:15Z"
}
```

**POST `tools/errors`:**
```json
{
  "id": "...",
  "run_id": "...",
  "tool_name": "...",
  "failed_at": "2026-09-01T13:16:15Z"
}
```

**POST `traces`:**
```json
{
  "id": "...",
  "run_id": "...",
  "event_type": "...",
  "timestamp": "2026-09-01T13:16:15Z"
}
```

**POST `postmortem`:**
```json
{
  "run_id": "...",
  "run_status": "...",
  "created_at": "2026-09-01T13:16:15Z"
}
```

#### POST Ingestion — Batch Endpoints (HTTP 200)

Batch endpoints always return HTTP 200 with a summary of the operation,
regardless of partial failures. Valid items are inserted even when some fail
validation.

```json
{
  "accepted": 42,
  "duplicates": 3,
  "errors": [
    { "index": 7, "id": "...", "message": "missing required field: event_type" }
  ]
}
```

`errors` is an empty array (`[]`) when all items were accepted or were
duplicates.

#### GET Query Endpoints — Response Envelope

All GET query endpoints (except `GET postmortem`) use a resource-named array
key with a consistent pagination envelope. No total count is included — cursor
pagination does not support efficient total counts on append-only tables.

**GET `events`:**
```json
{
  "events": [ /* array of agent_audit_event objects */ ],
  "next_cursor": "base64string or null",
  "has_more": true
}
```

**GET `sessions/outcomes`:**
```json
{
  "outcomes": [ /* array of session_outcome objects */ ],
  "next_cursor": "base64string or null",
  "has_more": false
}
```

**GET `tools/calls`:**
```json
{
  "calls": [ /* array of tool_call objects */ ],
  "next_cursor": "base64string or null",
  "has_more": false
}
```

**GET `tools/errors`:**
```json
{
  "errors": [ /* array of tool_error objects */ ],
  "next_cursor": "base64string or null",
  "has_more": false
}
```

**GET `traces`:**
```json
{
  "traces": [ /* array of agent_trace objects */ ],
  "next_cursor": "base64string or null",
  "has_more": false
}
```

**GET `postmortem`** (single-record lookup — no pagination envelope):
```json
{
  "run_id": "...",
  "workspace": "...",
  "schema_version": 1,
  "run_status": "...",
  "started_at": "...",
  "completed_at": "...",
  "task_summary": { /* task_summary object */ },
  "cost_summary": { /* cost_summary object */ },
  "blocked_tasks": [ /* array of {node_id, reason} */ ],
  "session_history": [ /* opaque array, stored as-is */ ],
  "ingested_at": "..."
}
```

If no postmortem exists for the given `run_id`, the handler returns HTTP 404
with an apikit error body (error type: `postmortem_not_found`).

The items in each paginated array correspond to the full stored record for that
table (all columns present), serialized to JSON using `snake_case` field names
matching the column names.

Each endpoint uses its own resource-named array key matching the URL path
segment: `events`, `outcomes`, `calls`, `errors`, `traces`. This is deliberate
— it prevents client-side ambiguity when handling responses from different
endpoints generically.

### Cursor-Based Pagination

All query endpoints (except `GET postmortem`) support cursor-based pagination.
The cursor is a base64-encoded JSON object with the following structure:

```json
{"ts": "<ISO 8601 timestamp>", "id": "<uuid>"}
```

Example decoded cursor:
```json
{"ts": "2026-09-01T14:30:22.123456Z", "id": "550e8400-e29b-41d4-a716-446655440000"}
```

The `ts` field is the `timestamp` column value of the last returned record; the
`id` field is the `id` column value. The JSON object is serialized to UTF-8,
then base64-encoded (standard encoding) to produce the opaque cursor string
returned to clients. Clients must not construct or parse cursors — they are
treated as opaque tokens.

**Cursor comparison semantics:**

- **`order=asc`:** `WHERE (timestamp, id) > (cursor.ts, cursor.id)` — returns
  records after the cursor position in ascending time order.
- **`order=desc`:** `WHERE (timestamp, id) < (cursor.ts, cursor.id)` — returns
  records before the cursor position in descending time order.

The `id` field acts as a tie-breaker when two records share the same
`timestamp`. Comparison is **lexicographic string order** on the UUID
`VARCHAR` column (standard DuckDB `VARCHAR` ordering). Since UUIDs are used
only for tie-breaking — not for chronological ordering — lexicographic string
comparison is sufficient to guarantee deterministic, stable pagination across
pages. Clients must not rely on any semantic ordering of UUIDs in the `id`
field.

**GET `postmortem` is excluded from cursor pagination.** It is a single-record
lookup by `:run_id` and returns the postmortem object directly (or HTTP 404 if
absent). No `next_cursor`, `has_more`, or pagination parameters apply.

Parameters: `limit` (default 100, max 1000), `cursor` (opaque string from
previous response). Response includes `next_cursor` (string or `null`) and
`has_more` (boolean).

### Postmortem Request Body Schema

The postmortem endpoint (`POST .../postmortem`) accepts the following JSON
body. `run_id` is taken from the URL path and must not conflict with any
`run_id` field in the body. Postmortems use `run_id` as the uniqueness key
(not a UUID `id` field).

| Field | Type | Required | Default | Notes |
|-------|------|----------|---------|-------|
| `schema_version` | integer | No | `1` | Only `1` is accepted; any other value returns HTTP 422 `unknown_schema_version` |
| `run_status` | string | Yes | — | One of: `stalled`, `block_limit`, `cost_limit`, `session_limit` |
| `started_at` | string (ISO 8601) | Yes | — | Run start time |
| `completed_at` | string (ISO 8601) | Yes | — | Run end time |
| `task_summary` | object | Yes | — | See sub-schema below |
| `cost_summary` | object | Yes | — | See sub-schema below |
| `blocked_tasks` | array | No | `[]` | Array of `{node_id: string, reason: string}` objects |
| `session_history` | array | No | `[]` | Opaque JSON array; stored as-is without field-level validation (see note below) |

**`task_summary` sub-schema:**

| Field | Type | Required |
|-------|------|----------|
| `total` | integer | Yes |
| `completed` | integer | Yes |
| `pending` | integer | Yes |
| `blocked` | integer | Yes |
| `failed` | integer | Yes |
| `in_progress` | integer | Yes |

**`cost_summary` sub-schema:**

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `total_cost_usd` | number | Yes | Pre-computed by client; hub does not validate pricing |
| `total_input_tokens` | integer | Yes | |
| `total_output_tokens` | integer | Yes | |
| `total_sessions` | integer | Yes | |

**`session_history` note:** The `session_history` array is treated as opaque
JSON. The hub stores the entire array as-is in the DuckDB `JSON` column without
field-level validation. This decouples hub releases from afaudit schema changes,
consistent with the afaudit compatibility goal. For implementer reference, the
afaudit-produced items contain fields such as `node_id`, `attempt`, `status`,
`archetype`, `model`, `duration_ms`, `cost`, `error_message`, `timestamp`,
`is_transport_error`, `is_budget_exhausted`, and `is_non_retryable`, but the
hub neither validates nor rejects unknown or missing fields within this array.

### Batch Semantics

Batch endpoints accept arrays of up to 1000 items. Batches are processed in a
single DuckDB transaction, but **invalid items are pre-filtered before the
transaction begins**:

1. **Pre-validation phase:** All items in the batch array are parsed and
   validated before any transaction is opened. Invalid items are collected into
   the `errors` array with their index and reason. Valid items proceed.
2. **Transaction phase:** A single DuckDB transaction is opened containing only
   the valid subset. Duplicates encountered via `INSERT OR IGNORE` are counted
   separately and silently skipped. The transaction is atomic — if the DB write
   fails (e.g., disk full), all valid items are rolled back and the handler
   returns HTTP 500. No partial commits occur within the transaction.

This approach avoids reliance on DuckDB savepoints (which are not available
for partial rollback within a single transaction) and ensures that valid items
are never silently dropped due to an adjacent invalid item.

Batches exceeding 1000 items return HTTP 413. The batch limit is fixed (not
configurable). Empty batch arrays return HTTP 400.

Batch ingestion is available only for `events` (`POST .../events/batch`) and
`traces` (`POST .../traces/batch`). No batch endpoints exist for
`sessions/outcomes`, `tools/calls`, or `tools/errors`.

### Request Body Size

No explicit per-endpoint body size limit is enforced within the audit package.
The hub relies on Echo's default body size limit (4 MB, configurable via
`echo.Echo.MaxRequestBodySize`). In practice, even postmortem payloads with
large `session_history` arrays remain well under 100 KB for typical runs. If
future workloads produce oversized payloads, Echo's body limit acts as the
safety net and returns HTTP 413 before the handler is invoked.

### Permissions

Four new PAT permission scopes:

| Scope | Description |
|-------|-------------|
| `audit:read` | Query audit events, traces, postmortems, tool calls/errors |
| `audit:write` | Submit audit events, traces, postmortems, tool calls/errors |
| `sessions:read` | Query agent sessions, token usage, cost summaries |
| `sessions:write` | Create/close agent sessions, report token usage |

Admin tokens and API keys have implicit full access. PATs require explicit
scope grants.

### Error Handling

All errors use `apikit.WriteAPIError()` / `apikit.WriteAPIErrorWithType()`.

| Condition | HTTP Status |
|-----------|-------------|
| Invalid `run_id` format | 400 |
| Missing required field | 400 |
| Body/URL `run_id` mismatch | 400 |
| Invalid severity | 400 |
| Invalid payload (not JSON object) | 400 |
| Batch exceeds 1000 | 413 |
| Unknown postmortem schema_version | 422 |
| Empty events array in batch | 400 |
| Unknown trace event_type | 400 |
| Duplicate id (idempotent) | 200 |
| Unauthenticated | 401 |
| Insufficient scope | 403 |
| Workspace not found | 404 |
| Postmortem not found | 404 (`postmortem_not_found`) |
| Workspace archived | 409 |
| Workspace-scoped token mismatch | 403 |
| Token owner lacks access | 403 |
| DuckDB write failure | 500 |
| DuckDB write timeout (sustained contention) | 503 with `Retry-After: 5` |

## New Internal Package

| File | Contents |
|------|----------|
| `internal/audit/db.go` | `OpenDB(path) (*sql.DB, error)` — DuckDB connection; calls `os.MkdirAll` on parent directory before opening |
| `internal/audit/schema.go` | DDL constants, `InitSchema(db *sql.DB) error` (CREATE TABLE IF NOT EXISTS + ALTER TABLE ADD COLUMN IF NOT EXISTS migrations) |
| `internal/audit/store.go` | `Store` struct with CRUD methods |
| `internal/audit/emitter.go` | `Emitter` interface, default implementation |
| `internal/audit/handlers.go` | HTTP handler closures for ingestion and query |
| `internal/audit/routes.go` | `RegisterRoutes(api *echo.Group, store *Store, emitter Emitter)` |
| `internal/audit/permissions.go` | `Permissions() []apikit.Permission` |
| `internal/audit/auth.go` | Auth helpers for audit scopes |
| `internal/audit/types.go` | Request/response types, domain structs, `HubEvent` |
| `internal/audit/severity.go` | `defaultSeverityFor()` mapping |
| `internal/audit/validate.go` | `run_id` regexp (`^\d{8}_\d{6}_[0-9a-f]{6}$`, lowercase hex only), event type validation |

## Hub Integration

In `cmd/af-hub/main.go`:
- Open DuckDB connection via `audit.OpenDB(auditDBPath)` (which calls `os.MkdirAll` on the parent directory first)
- Call `audit.InitSchema(auditDB)`
- Create `audit.NewStore(auditDB)`
- Create `audit.NewEmitter(store)`
- Register `audit.RegisterRoutes(api, store, emitter)`
- Collect `audit.Permissions()` in `extraPerms`
- Close DuckDB connection on shutdown

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `AF_AUDIT_DB_PATH` | `<data_dir>/audit.duckdb` | DuckDB database file path. `<data_dir>` is the directory containing the hub's SQLite database file, resolved from `database.path` in `config.toml`. If `AF_AUDIT_DB_PATH` is set, it overrides this default entirely. Missing parent directories are created automatically via `os.MkdirAll` before opening DuckDB; startup aborts if directory creation fails. |

The DuckDB connection pool size and query timeout are not configurable in this
spec. DuckDB's internal write serialization and the HTTP 503/`Retry-After: 5`
mechanism handle write contention without requiring operator-tunable pool
parameters.

## Dependencies

| Dependency | Relationship |
|------------|--------------|
| apikit | Auth middleware, `WriteAPIError`, `NowUTC`/`FormatUTC`, `GetAuthInfo` |
| `github.com/duckdb/duckdb-go` | New: DuckDB Go driver (official DuckDB-maintained, CGo, `database/sql` compatible driver). DSN is a plain file path. Requires `CGO_ENABLED=1` at build time. |
| `github.com/google/uuid` | Existing: UUID generation for default IDs |

**Downstream dependents** (specs that build on this spec's tables and interfaces):
- Spec 18 (`hub_audit_query`): adds hub-internal event emission, unified query API, and SSE streaming on top of tables defined here.
- Spec 19 (`sessions_metrics_retention`): populates `agent_sessions` and `token_usage` tables whose schemas are defined in this spec.

## Design Decisions

1. **Agent events keep bare types (no `agent.` prefix).** Maintaining afaudit
   compatibility is an explicit goal. The `source` field in unified query
   responses (spec 18) discriminates agent vs hub events.

2. **Batch limit fixed at 1000.** No environment variable configurability.
   Operators can request configurability in a future spec if needed.

3. **OTel deferred entirely.** No OTel code or imports. Schema includes nullable
   `trace_id` fields in `agent_audit_events` and `hub_audit_events` for
   forward-compatibility only.

4. **Server-side postmortem computation deferred.** Only pre-computed ingestion
   endpoint in scope.

5. **CGo accepted for DuckDB.** The analytical query advantages (10-100x for
   aggregations, time-range scans) justify the CGo dependency. DuckDB is
   isolated to the audit subsystem and does not affect the operational SQLite
   database. All CI jobs building or testing `internal/audit` must use a host
   with `CGO_ENABLED=1`.

6. **Cost summary returns tokens only, not USD.** The hub doesn't know model
   pricing. Dollar cost computation belongs in the client or a separate service.

7. **Write concurrency via DuckDB's internal serialization.** No application-level
   mutex. DuckDB serializes concurrent writers internally. Under sustained
   contention causing write timeouts, the hub returns HTTP 503 with
   `Retry-After: 5`. The 5-second value matches typical DuckDB write queue
   drain times under normal burst load; it is not configurable in this spec.

8. **No downstream-dependents section.** Cross-spec references in the Non-Goals
   section and dependency declarations in specs 18 and 19 are the authoritative
   record. This avoids a maintenance burden on this spec when new downstream
   specs are added.

9. **`ActorType` is a closed set matching apikit.** Valid values are
   `admin_token`, `api_key`, `pat`, and `system`. These are enforced by
   convention at emission points; no runtime validation is applied at ingestion
   for `hub_audit_events` rows written by the hub itself.

10. **Tests use real temp-file DuckDB instances.** Standard `testing` package
    only, no testify. Each test creates a fresh DuckDB file via `t.TempDir()`
    to avoid shared state and to exercise the actual DDL and driver.

11. **UUID format validation is permissive.** Any string matching the standard
    UUID format (`8-4-4-4-12` hex digits with dashes) is accepted as an
    idempotency key. No UUID version bits are enforced. This matches afaudit's
    use of `uuid.uuid4()` while remaining compatible with other UUID versions
    that clients may generate.

12. **`session_history` in postmortems is opaque.** The array is stored as-is
    without field-level validation, decoupling hub deployment from afaudit
    schema evolution. The hub is a data sink, not a postmortem engine.

13. **No per-endpoint body size limit.** Echo's default 4 MB body limit applies
    globally. Postmortem payloads in practice remain under 100 KB; Echo's limit
    is the safety net for pathological cases.

14. **POST responses return minimal acknowledgements.** Full record echo is not
    provided — clients already possess the submitted data. The minimal shape
    (id, run_id, key status field, timestamp) gives clients confirmation of
    ingestion and server-assigned defaults without over-specifying the response
    contract.

15. **Cursor encoding uses a JSON object wrapped in base64.** The cursor
    encodes `{"ts":"<ISO8601>","id":"<uuid>"}` — human-readable when decoded,
    self-documenting, and unambiguous across all endpoints. Base64 encoding
    makes it opaque to clients. The composite `(ts, id)` key provides stable,
    tie-breaking pagination even when multiple records share the same timestamp.
    Tie-breaking uses lexicographic string comparison on the `id` VARCHAR column
    (standard DuckDB ordering), which is deterministic and sufficient for
    pagination correctness — UUID ordering does not need to be chronological.
    GET `postmortem` is excluded — it is a single-record lookup with no
    pagination.

16. **Agent authentication allows PAT or API key.** Automated nightshift agents
    may use a workspace-scoped PAT with `audit:write` + `sessions:write` scopes
    (recommended for least-privilege) or an API key (simpler setup). The choice
    is left to the operator; both credential types are first-class.

17. **`since`/`until` and `order` apply uniformly to all paginated GET endpoints.**
    Time-range filtering and sort-order control are fundamental to audit system
    queries. Omitting them from any endpoint would force clients to fetch all
    records and filter client-side, defeating the server-side query API.
    All five paginated GET endpoints expose identical pagination and time-range
    parameters. GET `postmortem` is a single-record lookup and does not expose
    these parameters.

18. **Schema migration uses `ALTER TABLE ADD COLUMN IF NOT EXISTS`.** DuckDB
    natively supports this syntax. `InitSchema` runs migrations after table
    creation so that operators upgrading a hub binary against an existing
    `audit.duckdb` file receive transparent, non-destructive schema updates.
    The initial schema (this spec) has no `ALTER TABLE` statements to run, but
    the two-phase pattern is established for specs 18 and 19.

19. **Connection pool and query timeout are not operator-configurable.**
    DuckDB's internal write serialization and the HTTP 503/`Retry-After: 5`
    mechanism are sufficient for the anticipated write workload in this spec.
    Configurability may be added in a future spec if operational experience
    reveals the need.

20. **Batch pre-validation prevents silent data loss.** Invalid items are
    identified and collected before any DuckDB transaction is opened. The
    transaction contains only the valid subset and is fully atomic — a DB write
    failure rolls back all valid items rather than partially committing. This
    design avoids reliance on DuckDB savepoints (unavailable for partial rollback
    within a single transaction) and makes batch failure behavior deterministic.

21. **GET `postmortem` is a single-record lookup, not a paginated list.**
    The URL structure (`POST`/`GET .../postmortem`, not `.../postmortems`)
    reflects the one-per-run cardinality. A missing postmortem returns HTTP 404
    with error type `postmortem_not_found`. Cursor pagination, `since`/`until`,
    and `order` parameters do not apply.

22. **Each GET endpoint uses a distinct resource-named array key.** The five
    paginated endpoints use `events`, `outcomes`, `calls`, `errors`, and
    `traces` respectively — matching their URL path segments. This is deliberate:
    it prevents client-side ambiguity when handlers for different endpoints are
    written generically against the response shape.

23. **`AF_AUDIT_DB_PATH` default is resolved from `config.toml`.** The hub
    derives `data_dir` from the directory containing the SQLite database file
    (`database.path` in `config.toml`). The `audit.duckdb` file is placed in
    the same directory. Implementers should follow the existing config
    resolution in `cmd/af-hub/main.go`. An explicit `AF_AUDIT_DB_PATH`
    overrides this entirely.

24. **Missing parent directories are created automatically.** `audit.OpenDB`
    calls `os.MkdirAll` on the parent directory of the resolved DuckDB path
    before opening the connection. This aligns with the hub's existing behavior
    for the SQLite database path and simplifies container deployments where the
    data directory is a mounted volume. If `os.MkdirAll` fails (e.g., permission
    denied), hub startup aborts with an error rather than silently proceeding.

25. **`run_id` regexp enforces lowercase hex only.** The canonical pattern is
    `^\d{8}_\d{6}_[0-9a-f]{6}$`. Uppercase hex characters are not accepted,
    matching afaudit's convention of generating the suffix via
    `os.urandom(3).hex()` which always produces lowercase. This prevents
    ambiguous duplicate IDs (e.g., `a1b2c3` vs `A1B2C3`).

26. **No application-level rate limiting on ingestion endpoints.** Write
    contention is handled by DuckDB's internal serialization and the HTTP
    503/`Retry-After: 5` mechanism. Proactive per-client or per-workspace rate
    limiting is delegated to the reverse proxy or infrastructure layer. This
    keeps the audit package simple and avoids introducing in-process rate-limit
    state that would complicate multi-instance deployments.

27. **Batch ingestion is limited to events and traces.** Session outcomes, tool
    calls, and tool errors are low-volume record types (one per session or tool
    invocation) and are not present in the afaudit JSONL files targeted by bulk
    upload. Batch variants for these types can be added in a future spec if
    operational experience reveals the need.