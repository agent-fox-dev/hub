---
spec_id: '01'
spec_name: server_foundation
title: Server Foundation
status: draft
created_at: '2026-07-10T13:43:20.961756+00:00'
updated_at: '2026-07-10T14:10:46.875691+00:00'
owner: ''
source: docs/01_prd.md
schema_version: 1
---
# Server Foundation

## Intent

Establish the foundational server infrastructure for af-hub: configuration loading, database initialization, health probes, structured logging, graceful shutdown, admin bootstrap, and the authentication middleware that recognizes all three credential types (admin token, user API key, workspace token).

This spec delivers the base that all other af-hub specs depend on. Every API endpoint, every CLI command, and every workspace operation requires the server to be running, the database to be initialized, and the auth middleware to be in place.

> **Owner:** Not yet assigned — this is a greenfield project without team assignment. The owner field will be updated when team roles are established.

> **TLS:** TLS termination is always delegated to an upstream reverse proxy or load balancer. The server itself listens on plain HTTP. Native TLS support is explicitly out of scope for this iteration.

## Goals

- Load server configuration from `config.toml` (TOML format) with sensible defaults.
- Initialize embedded SQLite with WAL mode, creating all tables on boot via `CREATE TABLE IF NOT EXISTS`.
- Provide Kubernetes-compatible health probes at `/healthz` and `/readyz`.
- Implement structured JSON logging via logrus with per-request method, path, status, duration, and request ID.
- Support graceful shutdown on SIGTERM/SIGINT with a 15-second drain timeout.
- On first boot, auto-create an admin user and generate a cryptographically random admin token (`af_admin_<64 hex>`), storing the SHA-256 hash in the database and writing the plaintext to an `admin_token` file (mode 0600).
- On subsequent boots, validate `AF_HUB_ADMIN_TOKEN` against the stored hash; refuse to start on mismatch.
- Support admin token rotation via `--reset-admin-token` boot flag.
- Implement auth middleware that identifies credential type by prefix (`af_admin_`, `af_`, `af_wt_`), verifies the hashed secret, resolves the associated user (and workspace for workspace tokens), and rejects blocked users with HTTP 403 (admin token auth is exempt from this check).
- Enforce a 1 MB request body size limit (HTTP 413 on exceeds).
- Return all API errors in a consistent JSON envelope: `{"error": {"code": <integer>, "message": "..."}}`.

## Non-goals

- OAuth flow implementation (spec 2).
- User CRUD endpoints (spec 2).
- Team management (spec 3).
- Workspace and token endpoints (spec 4).
- CLI binary (spec 5).
- Web UI scaffold (spec 6).
- Rate limiting, CORS middleware, database migration tooling.
- Native TLS / HTTPS termination (delegated to reverse proxy).
- Validation of the `external_url` config field (deferred to spec 2).

## Functional Requirements

### Configuration loading

- The server loads `config.toml` from the current directory by default. An optional `--config <path>` flag overrides the config file location. The `admin_token` file is always written to the same directory as the resolved `config.toml`.
- **`admin_token` file location when no `--config` flag is provided:** The server resolves the config file path as `./config.toml` (current working directory). Regardless of whether this file actually exists, the `admin_token` file is always written to the current working directory (i.e., `./admin_token`). A missing `config.toml` is not fatal — all defaults apply — but the file output directory is always anchored to the current working directory in this case.
- Required settings with defaults: server port (8080), bind address (`0.0.0.0`), database path (`./data/af-hub.db`), log level (`info`).
- Optional: `[server] external_url` — parsed and stored as a string, but **not validated** in this spec. Validation is deferred to spec 2 (OAuth), which is the first consumer of this value.
- OAuth provider configuration: provider name, client ID, client secret. Authorize/token/userinfo URLs are optional for providers with well-known defaults (GitHub). These fields are parsed and stored but unused until spec 2.

#### config.toml parse error behavior

- **Missing file:** A missing `config.toml` at the resolved path is **not fatal**. All defaults apply and the server continues normally.
- **Invalid TOML syntax:** If `config.toml` exists but contains invalid TOML (e.g., malformed syntax, unclosed brackets), the server logs a fatal error with the parse failure reason and exits immediately (`os.Exit(1)`). A present-but-invalid config is a deployment mistake and must not be silently ignored.
- **Unrecognized fields:** If `config.toml` contains unrecognized fields or section headers (e.g., a typo like `[servr]` or an unknown key), the server operates in permissive mode: it logs a structured warning for each unknown field (at `warn` level, including the field name/path) and continues startup with defaults for any unrecognized keys. This catches typos without blocking startup during rapid iteration.
- **Invalid `log.level` value:** If `config.toml` specifies a `log.level` value that is not one of the recognized levels (`trace`, `debug`, `info`, `warn`, `error`, `fatal`, `panic`), the server logs a structured warning at `warn` level including the invalid value (e.g., `{"level":"warn","msg":"unrecognized log level, defaulting to info","invalid_value":"verbose"}`), then defaults to `info`. This is non-fatal — the server can function correctly with a reasonable fallback log level.

#### config.toml structure

The following is the canonical annotated `config.toml` showing all supported fields, their TOML section headers, types, and default values. Implementers MUST use these exact field names and section headers when parsing configuration.

```toml
[server]
port = 8080              # integer; default: 8080
bind = "0.0.0.0"         # string;  default: "0.0.0.0"
external_url = ""        # string;  optional; used for OAuth redirect URI (spec 2); not validated here

[database]
path = "./data/af-hub.db"  # string; default: "./data/af-hub.db"

[log]
level = "info"  # string; one of: trace|debug|info|warn|error|fatal|panic; default: "info"
                # If an unrecognized value is provided, a warn-level log entry is emitted
                # and the level defaults to "info".

[[oauth.providers]]
name          = "github"                 # string; required per provider entry
client_id     = "your-github-client-id" # string; required per provider entry
client_secret = "your-github-client-secret" # string; required per provider entry
authorize_url = ""  # string; optional — defaults to provider well-known URL (e.g., GitHub)
token_url     = ""  # string; optional
userinfo_url  = ""  # string; optional
```

**Notes:**
- Multiple OAuth providers can be declared by repeating `[[oauth.providers]]` blocks.
- All fields have defaults where noted; the server starts successfully with an empty or minimal `config.toml`.
- If `config.toml` does not exist at the resolved path, the server applies all defaults and continues normally (a missing config file is not a fatal error).

### Startup sequence

The server initializes in the following strict order. Steps are not reordered or parallelized:

1. **Parse CLI flags** — `--config <path>` and `--reset-admin-token`.
2. **Load `config.toml`** — from the resolved path. Apply defaults for any missing fields. A missing config file is not fatal; all defaults apply. A present-but-invalid TOML file is fatal (log error, exit). Unrecognized fields produce a warn-level log entry per field and are otherwise ignored. An invalid `log.level` value produces a warn-level log entry and defaults to `info`.
3. **Initialize structured logging** — configure logrus with JSON formatter, log level from config, and **stdout** as the output destination. All subsequent log output uses structured JSON written to stdout.
4. **Open/initialize SQLite** — open or create the database at the configured path (creating parent directories automatically if needed), apply pragmas in order, create all tables via `CREATE TABLE IF NOT EXISTS`.
5. **Run admin bootstrap or token validation** — determine first boot vs. subsequent boot based on row count in `admin_tokens`; execute the appropriate flow (see [Admin bootstrap](#admin-bootstrap-first-boot) and [Admin token validation](#admin-token-validation-subsequent-boots)).
6. **Register HTTP routes and middleware** — attach middleware stack and route handlers to the Echo instance using the route group structure defined in [Route group structure](#route-group-structure). Register a global custom error handler on the Echo instance (see [Custom error handler](#custom-error-handler)).
7. **Log startup info** — emit a single structured log entry at `info` level immediately before the listener starts, with the following fields: `bind` (string), `port` (integer), `db_path` (string), `log_level` (string). Example: `{"level":"info","bind":"0.0.0.0","port":8080,"db_path":"./data/af-hub.db","log_level":"info","msg":"server starting"}`.
8. **Start HTTP listener** — begin accepting connections on the configured bind address and port.
9. **Begin listening for SIGTERM/SIGINT** — arm the graceful shutdown handler.

Health probe routes (`/healthz`, `/readyz`) are registered in step 6 and are not available before the HTTP listener starts in step 8. This means health probes are only reachable after the database is fully initialized and the admin bootstrap has completed.

### Database initialization

- Open or create the SQLite database at the configured path.
- **Missing parent directory:** If the directory portion of the configured database path does not exist, create it automatically (equivalent to `mkdir -p`) before opening the database. If directory creation fails, log a fatal error with the reason and exit immediately (`os.Exit(1)`).
- **Open failure:** If the database file cannot be opened or created (e.g., path unwritable, file corrupt), log a fatal error with the failure reason and exit immediately (`os.Exit(1)`) — no retries.
- Apply pragmas in this order immediately after opening the connection, before any table creation:
  1. `PRAGMA journal_mode = WAL` — enable WAL mode.
  2. `PRAGMA foreign_keys = ON` — enforce referential integrity (disabled by default in SQLite).
  3. `PRAGMA busy_timeout = 5000` — wait up to 5 seconds on write contention before returning a busy error.
- **PRAGMA failure handling:** If any of the three PRAGMA statements fails (e.g., driver-level error, read-only filesystem, corrupt DB file), the server treats this as a fatal startup error. It logs a structured error entry identifying the specific PRAGMA that failed and the reason, then exits immediately (`os.Exit(1)`). WAL mode failure in particular indicates the database is unusable. Silent continuation after a PRAGMA failure is not permitted.
- Create all tables via `CREATE TABLE IF NOT EXISTS` on boot. Full DDL including column types and indexes is specified below.

#### Connection model

The server uses Go's standard `database/sql` package with the pure-Go SQLite driver (`modernc.org/sqlite`). SQLite in WAL mode supports concurrent reads but serializes writes. The connection model follows standard `sql.DB` defaults:

- **Writes:** Serialized through a single writer connection. WAL mode ensures readers do not block writers and writers do not block readers.
- **Reads:** Handled via Go's `database/sql` connection pool (unbounded by default). Multiple concurrent read queries can proceed simultaneously under WAL mode without contention.
- No explicit `SetMaxOpenConns` or `SetMaxIdleConns` overrides are applied in this spec. The `busy_timeout = 5000` pragma handles transient write contention at the SQLite layer.

#### Write contention error handling

If a SQLite write operation exhausts the `busy_timeout` window (5 seconds of contention without acquiring a write lock), the driver returns an error. The server surfaces this to the HTTP caller as:

- **HTTP 503** with body `{"error": {"code": 503, "message": "service temporarily unavailable"}}`.

DB write contention is a transient condition. HTTP 503 communicates that the server is temporarily unable to process the request and the client may retry. This response uses the standard error envelope format.

#### Schema DDL

All UUID columns are stored as `TEXT`. All timestamp columns are stored as `TEXT` in ISO 8601 format (`YYYY-MM-DDTHH:MM:SS.sssZ`).

```sql
CREATE TABLE IF NOT EXISTS users (
    id          TEXT PRIMARY KEY,                -- UUID
    username    TEXT NOT NULL UNIQUE,
    email       TEXT NOT NULL,
    full_name   TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'active',  -- 'active' | 'blocked'
    provider    TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE (provider, provider_id)
);

CREATE TABLE IF NOT EXISTS admin_tokens (
    id          TEXT PRIMARY KEY,               -- UUID
    token_hash  TEXT NOT NULL,
    created_at  TEXT NOT NULL
);
-- At most one row at any time. Enforced by application logic only (delete before insert
-- during rotation). No DB-level constraint is required, as this table is managed by a
-- single, auditable code path.

CREATE TABLE IF NOT EXISTS api_keys (
    id          TEXT PRIMARY KEY,               -- UUID
    key_id      TEXT NOT NULL UNIQUE,           -- 8 alphanumeric chars
    secret_hash TEXT NOT NULL,                  -- SHA-256 of the raw 32-alphanumeric-char secret component only
    user_id     TEXT NOT NULL REFERENCES users(id),
    expires_at  TEXT,                           -- nullable
    created_at  TEXT NOT NULL,
    revoked_at  TEXT                            -- nullable
);

CREATE TABLE IF NOT EXISTS teams (
    id          TEXT PRIMARY KEY,               -- UUID
    name        TEXT NOT NULL UNIQUE,
    slug        TEXT NOT NULL UNIQUE,
    url         TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'active', -- 'active' | 'archived' | 'deleted'
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS team_members (
    team_id    TEXT NOT NULL REFERENCES teams(id),
    user_id    TEXT NOT NULL REFERENCES users(id),
    created_at TEXT NOT NULL,
    PRIMARY KEY (team_id, user_id)
);

CREATE TABLE IF NOT EXISTS workspaces (
    id         TEXT PRIMARY KEY,               -- UUID
    slug       TEXT NOT NULL UNIQUE,
    git_url    TEXT NOT NULL,
    branch     TEXT,                           -- nullable
    owner_id   TEXT NOT NULL REFERENCES users(id),
    team_id    TEXT REFERENCES teams(id),      -- nullable
    status     TEXT NOT NULL DEFAULT 'active', -- 'active' | 'archived'
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workspace_tokens (
    id           TEXT PRIMARY KEY,             -- UUID
    token_id     TEXT NOT NULL UNIQUE,         -- 8 alphanumeric chars
    secret_hash  TEXT NOT NULL,                -- SHA-256 of the raw 32-alphanumeric-char secret component only
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    user_id      TEXT NOT NULL REFERENCES users(id),
    label        TEXT,                         -- nullable
    expires_at   TEXT,                         -- nullable
    created_at   TEXT NOT NULL,
    revoked_at   TEXT                          -- nullable
);
```

#### Indexes

The following indexes are created on boot alongside the tables:

```sql
-- Auth hot-path lookups
CREATE INDEX IF NOT EXISTS idx_api_keys_key_id
    ON api_keys(key_id);

CREATE INDEX IF NOT EXISTS idx_workspace_tokens_token_id
    ON workspace_tokens(token_id);

-- OAuth provider lookup
CREATE INDEX IF NOT EXISTS idx_users_provider
    ON users(provider, provider_id);

-- Slug lookups
CREATE INDEX IF NOT EXISTS idx_workspaces_slug
    ON workspaces(slug);

CREATE INDEX IF NOT EXISTS idx_teams_slug
    ON teams(slug);
```

### Health probes

- `GET /healthz` — Always returns HTTP 200 with `{"status": "ok"}`.
- `GET /readyz` — Executes `SELECT 1` against the open database connection with a **2-second context timeout**. If the query does not return within 2 seconds, or if the query fails for any reason (connection error, driver error), the handler returns HTTP 503 with `{"status": "not ready"}`. Returns HTTP 200 with `{"status": "ready"}` on success. The `readyz` probe does **not** re-verify pragma settings — it is a liveness check on the connection only.
- A `503` from `/readyz` does **not** trigger any internal shutdown or self-termination. The server remains running indefinitely in a degraded state and retries the DB check on subsequent probe calls. This follows the standard Kubernetes pattern: the orchestrator (not the server) decides whether to restart the pod based on its own liveness/readiness probe configuration.
- **Degraded-state logging for `/readyz`:** To prevent log flooding in a degraded state (e.g., Kubernetes probing every 10 seconds), the following logging cadence applies:
  - The failure counter is a **package-level `int64` variable accessed exclusively via `sync/atomic`** to ensure goroutine safety under concurrent probe requests. No mutex is required.
  - The **first** consecutive DB check failure (counter transitions from 0 to 1) is logged at `error` level with the failure reason.
  - **Subsequent consecutive failures** (counter already ≥ 1 before the failing check) are logged at `debug` level only. This suppresses repetitive high-severity entries without silencing failures entirely.
  - When the DB check **recovers** (a previously failing probe returns success, i.e., counter was > 0), a single `info`-level log entry is emitted indicating recovery. The counter is atomically reset to 0 so that the next failure, if any, is again logged at `error` level.
  - **Concurrent recovery race:** In the rare case where two concurrent `/readyz` probes both succeed simultaneously after a degraded period (counter > 0), both may attempt to log the recovery message and reset the counter. This is acceptable — duplicate recovery log entries are benign and preferable to adding a mutex. The atomic reset is still correct because `sync/atomic` operations are sequentially consistent; at most a duplicate `info` log may appear. No special-casing is required.
- **Supported HTTP methods:** `GET` and `HEAD` are supported on both `/healthz` and `/readyz`. All other HTTP methods (e.g., POST, PUT, DELETE) return HTTP 405 (Method Not Allowed). Echo's default 405 behavior is used — no custom JSON envelope is required for these routes. Health probes are infrastructure endpoints, not API endpoints.

### Structured logging

- Use logrus v1.4.0 or later (required for native `trace` level support). The minimum version MUST be pinned in `go.mod` (e.g., `require github.com/sirupsen/logrus v1.4.0`). Because Go modules select the minimum version satisfying all constraints, any transitive dependency pulling in an older logrus version will be overridden by this explicit minimum pin. Build-time verification is provided by `go mod tidy` and `go mod verify`; if a dependency conflict prevents logrus from reaching v1.4.0, the build will fail with a clear module resolution error.
- **Output destination:** Always write to **stdout**. No file-based or stderr output. This follows the 12-factor app pattern and is compatible with container runtimes (Docker, Kubernetes) and systemd journal capture.
- Log every HTTP request with: `method`, `path`, `status` (integer), `duration_ms` (float), and `request_id` (UUID string).
- Support log levels: trace, debug, info, warn, error, fatal, panic. The `trace` level maps directly to logrus's native `TraceLevel` (available since logrus v1.4.0). No aliasing or fallback behavior is required.

### Request ID

- Generate a UUID (v4) per incoming request.
- If the incoming request carries an `X-Request-ID` header, apply the following validation before accepting it:
  - The value must be **non-empty**.
  - The value must consist entirely of **printable ASCII characters** (bytes 0x20–0x7E, inclusive).
  - The value must be **at most 128 characters** in length.
  - If the value fails any of these checks (empty, contains non-printable or non-ASCII bytes, or exceeds 128 characters), **discard it and generate a fresh UUID v4** instead. No error is returned to the client; the generated UUID is used transparently.
- Include the request ID in:
  - All structured log entries for that request (as the `request_id` field).
  - The `X-Request-ID` response header (always set, whether propagated from the client or generated fresh).
- The request ID is attached to Echo's context so downstream handlers can reference it.

### Graceful shutdown

- Listen for SIGTERM and SIGINT.
- On signal: stop accepting new connections and drain in-flight requests using `http.Server.Shutdown` with a 15-second context timeout. Echo's built-in `e.Shutdown(ctx)` wraps this behavior and is the recommended invocation.
- `http.Server.Shutdown` tracks active connections internally via Go's `net/http` connection state machine. The server does not maintain a separate in-flight counter — this is delegated entirely to the standard library.
- **If the 15-second drain timeout expires** before `Shutdown` returns: log a fixed warning message (e.g., `{"level":"warn","msg":"graceful shutdown timed out; some connections may have been dropped"}`) and exit. Because `http.Server.Shutdown` does not surface a precise count of dropped connections, no numeric count is included in this message.
- **If `Shutdown` completes within the timeout:** exit cleanly with an `info`-level log entry (e.g., `{"level":"info","msg":"server shutdown complete"}`). No warning is logged.
- **Streaming / WebSocket connections:** Go's `http.Server.Shutdown` closes hijacked connections (e.g., WebSocket upgrades) immediately without waiting for them to drain. This is standard Go behavior and is acceptable for this spec. No special handling is required.

### Admin bootstrap (first boot)

- **First boot** is defined as zero rows in the `admin_tokens` table at startup.
- **First boot also applies when `AF_HUB_ADMIN_TOKEN` is absent from the environment and `admin_tokens` has zero rows.** Zero rows in `admin_tokens` is the canonical and exclusive first-boot signal. A missing environment variable is ignored in this case — do not treat it as a fatal error.
- **`AF_HUB_ADMIN_TOKEN` present on first boot:** If `AF_HUB_ADMIN_TOKEN` is set in the environment but the `admin_tokens` table has zero rows (i.e., this is a first boot), the server logs a single structured notice at `info` level before proceeding with the normal first-boot flow: `{"level":"info","msg":"AF_HUB_ADMIN_TOKEN is set but will be ignored on first boot; a new token will be generated"}`. The env var value is never read or validated on first boot — the new token is always generated fresh regardless.
- On first boot:
  1. Create admin user row in `users`: `username = "admin"`, `email = "admin@localhost"`, `provider = "local"`, `provider_id = "admin"`, `status = "active"`. This row is created **only on first boot**. On subsequent boots, if the admin user row is missing (e.g., after a partial DB wipe) but the `admin_tokens` table has a row, the server continues normally — admin token auth verifies the token hash only and does not perform a user lookup. The admin user row is a convenience for API/database consistency, not a hard authentication dependency.
  2. Generate a cryptographically random admin token: `af_admin_<64 hex chars>`.
  3. Store the SHA-256 hash of the token in `admin_tokens`.
  4. Write the plaintext token to the `admin_token` file (mode 0600) in the same directory as the resolved `config.toml`. When no `--config` flag is provided, this file is written to the current working directory (`./admin_token`).
     - **If the file already exists at the target path:** overwrite it silently and log a structured warning at `warn` level with the following fields: `msg = "admin_token file already existed and was overwritten"`, `path = <absolute path of the file>`. This scenario most commonly occurs after a DB wipe (the `admin_tokens` table is emptied) while the old plaintext `admin_token` file remains on disk from a previous boot. The server treats this as a legitimate re-bootstrap and overwrites the stale file; the warning serves as an operator audit signal.
     - **If the file cannot be written** (e.g., filesystem permission error): log a fatal error with the reason and exit immediately (`os.Exit(1)`). The admin token is the only recovery mechanism on a fresh deployment; silently losing it is unacceptable.
  5. Log the absolute path of the written `admin_token` file at `warn` level.

### Admin token file persistence and security

- The `admin_token` plaintext file persists on disk indefinitely. Deletion is the operator's responsibility.
- **On every subsequent boot**, if the `admin_token` file is still present at the expected path (same directory as the resolved `config.toml`), the server logs a structured security warning at `warn` level with the following fields: `msg = "admin_token plaintext file still exists on disk; delete after securing the token"`, `path = <absolute path of the file>`. This warning is emitted regardless of whether the file content is valid.
- **If the `admin_token` file is absent on a subsequent boot** (i.e., the operator has already deleted it, which is the recommended security practice), the server remains completely silent — no log entry of any kind is emitted for the absent file.
- Admin tokens never expire. There is no maximum age or automatic expiry for admin tokens. Rotation is the only mechanism to invalidate an admin token, and rotation requires a server restart using the `--reset-admin-token` flag.

### Admin token validation (subsequent boots)

- **Subsequent boot** is defined as one or more rows present in the `admin_tokens` table at startup.
- Read `AF_HUB_ADMIN_TOKEN` from environment, hash with SHA-256, compare against stored hash. If the environment variable is absent or the hash does not match the stored hash, log a fatal error and refuse to start (`os.Exit(1)`).
- **Operator guidance:** After first boot, operators read the plaintext from the `admin_token` file (e.g., `cat admin_token`) and export it as the environment variable before subsequent starts (e.g., `export AF_HUB_ADMIN_TOKEN=$(cat admin_token)`). For production deployments, the value should be stored in a secrets manager (e.g., a Kubernetes Secret) and injected at runtime. This bootstrapping procedure is documented in the project quickstart guide.

### Admin token rotation

- `--reset-admin-token` is a **boot-time flag only** — it requires a server restart to take effect. There is no live rotation mechanism. Passing `--reset-admin-token` alongside any other valid boot flags (e.g., `--config <path>`) is fully supported; flags are processed independently and do not conflict.
- `--reset-admin-token` **bypasses `AF_HUB_ADMIN_TOKEN` validation entirely.** This is an intentional operator escape hatch: it is used precisely when the operator has lost access to the current token or needs to rotate it without knowing the previous value. Physical/infrastructure access to the server is the assumed authorization mechanism for this operation. The env var is neither read nor verified during a rotation boot.
- `--reset-admin-token` flag: generate new token using the same flow as first boot (generate, hash, write file, log path), replace the existing row in `admin_tokens` with the new hash (delete + insert), invalidate the old token. Continue normal startup.
- **File write failure during rotation is fatal**, identical to first-boot behavior: if the `admin_token` file cannot be written during rotation (e.g., filesystem permission error), log a fatal error with the reason and exit immediately (`os.Exit(1)`). The new token must be persisted to disk before the server continues; silently losing the rotated token is unacceptable.
- If `--reset-admin-token` is passed on a system with zero rows in `admin_tokens` (i.e., a never-booted system), the behavior is identical to a normal first boot — generate token, write file, continue. No special-casing is required.

### Route group structure

Routes are organized using Echo's route group mechanism to cleanly separate auth-protected and auth-excluded paths. The following group structure MUST be established in step 6 of the startup sequence so that spec 2 and later specs can register routes into the appropriate group without modifying this spec's code:

```
e (root Echo instance)
├── GET  /healthz          — no auth middleware
├── HEAD /healthz          — no auth middleware
├── GET  /readyz           — no auth middleware
├── HEAD /readyz           — no auth middleware
└── api  := e.Group("/api/v1")
    ├── auth := api.Group("/auth")   — NO auth middleware (public; for spec 2 OAuth routes)
    └── protected := api.Group("", authMiddleware)  — auth middleware applied
```

- Health probe routes are registered directly on the root Echo instance, outside all groups.
- `auth` group (`/api/v1/auth`) has no auth middleware. Spec 2 registers OAuth routes here.
- `protected` group (`/api/v1`) carries the auth middleware. All authenticated API endpoints in this and future specs register here.
- This structure means the auth middleware exclusion for `/api/v1/auth/*` is enforced structurally via Echo's group model, not via path-prefix string matching in the middleware itself.

### Custom error handler

A global custom error handler is registered on the Echo instance during step 6 of the startup sequence. Its responsibilities are:

- **For all routes except health probes** (`/healthz`, `/readyz`): translate any error — including Echo's built-in `*echo.HTTPError` (e.g., 404 for unmatched routes, 405 for method-not-allowed on API routes) — into the standard JSON error envelope format: `{"error": {"code": <integer>, "message": "<string>"}}`.
- **For health probe routes** (`/healthz`, `/readyz`): health probe handlers return their own plain JSON bodies (`{"status": "ok"}`, etc.) directly and do not produce errors routed through this handler. Echo's default 405 behavior for health probe routes (no custom envelope) remains unchanged.
- **Panic recovery** (from the Recover middleware) also surfaces errors through this handler, producing HTTP 500 with `{"error": {"code": 500, "message": "internal server error"}}`.
- The custom error handler replaces `e.HTTPErrorHandler` on the Echo instance. It inspects the error type:
  - If `*echo.HTTPError`: use its `Code` field as the HTTP status and its `Message` field (cast to string) as the envelope message.
  - For all other error types: HTTP 500, message `"internal server error"`.
- This ensures that no route inside `/api/v1/*` — whether matched or unmatched — returns a non-envelope JSON response.

### Token wire format

All token types follow a structured, underscore-delimited format parsed by the auth middleware:

| Type | Format | Components |
|---|---|---|
| Admin token | `af_admin_<64 hex chars>` | Opaque; entire suffix after `af_admin_` is the secret. |
| User API key | `af_<key_id>_<secret>` | `key_id`: 8 alphanumeric chars; `secret`: 32 alphanumeric chars. |
| Workspace token | `af_wt_<token_id>_<secret>` | `token_id`: 8 alphanumeric chars; `secret`: 32 alphanumeric chars. |

**Parsing rules:**
- Admin tokens: prefix `af_admin_` is stripped; the remaining 64 hex characters are the secret, hashed with SHA-256 for comparison against `admin_tokens`.
- User API keys: after confirming the `af_` prefix (and that the token does **not** begin with `af_admin_` or `af_wt_`), split the full token string on `_`. The resulting parts are `["af", "<key_id>", "<secret>"]` — use index 1 for `key_id` and index 2 for `secret` (0-indexed from the beginning of the full token string). **Structural validation before DB lookup:** the split MUST yield exactly 3 parts; `key_id` MUST be exactly 8 alphanumeric characters; `secret` MUST be exactly 32 alphanumeric characters. Any structural mismatch returns HTTP 401 immediately — no DB query is performed.
- Workspace tokens: after confirming the `af_wt_` prefix, strip the `af_wt_` prefix and split the remainder on `_`. The resulting parts are `["<token_id>", "<secret>"]` — use index 0 for `token_id` and index 1 for `secret`. **Structural validation before DB lookup:** the split MUST yield exactly 2 parts; `token_id` MUST be exactly 8 alphanumeric characters; `secret` MUST be exactly 32 alphanumeric characters. Any structural mismatch returns HTTP 401 immediately — no DB query is performed.
- Any token not matching a recognized prefix structure → HTTP 401.

**Structural validation summary:**

| Token type | Expected split parts | `key_id` / `token_id` | `secret` |
|---|---|---|---|
| Admin token | N/A (prefix strip only) | N/A | Exactly 64 hex chars |
| User API key | Exactly 3 (`af`, `key_id`, `secret`) | Exactly 8 alphanumeric chars | Exactly 32 alphanumeric chars |
| Workspace token | Exactly 2 (`token_id`, `secret`) | Exactly 8 alphanumeric chars | Exactly 32 alphanumeric chars |

All structural validation failures → HTTP 401 with `"missing or invalid authentication credentials"`. No DB queries are made for structurally invalid tokens.

### Secret hashing

The following table defines the canonical hashing input for each credential type. Implementers MUST use these exact inputs when computing hashes for storage and for verification at auth time.

| Token type | Column | Hash input | Algorithm |
|---|---|---|---|
| Admin token | `admin_tokens.token_hash` | The full 64 hex-char suffix after `af_admin_` | SHA-256 |
| User API key | `api_keys.secret_hash` | The raw 32-alphanumeric-char secret component only (not the full token string, not the `key_id`) | SHA-256 |
| Workspace token | `workspace_tokens.secret_hash` | The raw 32-alphanumeric-char secret component only (not the full token string, not the `token_id`) | SHA-256 |

At verification time, the auth middleware extracts the appropriate secret component from the parsed token, computes its SHA-256 hash, and compares it to the stored hash via constant-time comparison to prevent timing attacks.

### Middleware execution order

Echo middleware is registered and executes in the following order for all routes:

1. **Recover** — catches panics anywhere in the chain and converts them to HTTP 500 responses using the standard JSON error envelope format (see [Error envelope](#error-envelope)). This ensures no raw panic output reaches clients.
2. **Body-size limit** — enforces the 1 MB request body cap before any further processing. Requests exceeding this limit receive HTTP 413 immediately, preventing large payloads from consuming auth or handler processing time. For requests with no body (e.g., GET, HEAD), the middleware is a no-op at the application level — it wraps the response writer in all cases, but triggers only when a body is actually read and exceeds the limit. No 413 is issued for bodyless requests.
3. **Request logger** — generates or propagates the request ID, attaches it to context, and logs the completed request (method, path, status, duration, request_id). Runs after body-limit so oversized requests are also logged. A 413 response from the body-size limit middleware IS captured and logged by the request logger (status 413 will appear in the log entry for that request), because the request logger wraps the full middleware chain below it using Echo's response writer wrapping — the status code is captured after the inner middleware returns, regardless of whether the inner middleware short-circuited with an error.
4. **Auth** — extracts and verifies the Bearer token, resolves identity, and sets `AuthContext` in Echo's context. Only applied to routes registered under the `protected` group (see [Route group structure](#route-group-structure)).

Health probe routes (`/healthz`, `/readyz`) are registered outside the `/api/v1/*` group and bypass auth middleware entirely.

### Authentication middleware

- All routes registered under the `protected` group (`/api/v1` with auth middleware, excluding `/api/v1/auth/*`) pass through auth middleware.
- Extract Bearer token from `Authorization` header. The following cases all return HTTP 401 with the message `"missing or invalid authentication credentials"` — no additional detail is provided to prevent information leakage:
  - (a) `Authorization` header is absent entirely.
  - (b) `Authorization` header is present but does not use the `Bearer` scheme.
  - (c) `Authorization: Bearer ` header is present but the token value is empty or whitespace-only.
  - (d) Token is present but does not match any recognized prefix structure (`af_admin_`, `af_wt_`, or `af_`).
  - (e) Token matches a recognized prefix but fails structural validation (wrong part count, wrong `key_id`/`secret` length or character set).
- Identify credential type by prefix and parse per the token wire format above. Structural validation (part count, length, character set) is always performed before any database query.
- For `api_keys` and `workspace_tokens`: after verifying the secret hash, check `expires_at` and `revoked_at`. Token expiry is evaluated in UTC using an exclusive boundary: a token is expired if `expires_at IS NOT NULL AND expires_at < now() UTC`. A token whose `expires_at` equals the exact current timestamp is **not** considered expired — it remains valid until the expiry instant has passed. If the token is expired or revoked (`revoked_at IS NOT NULL`), return HTTP 401 with the message `"missing or invalid authentication credentials"`. No distinction is made between expired and revoked states in the response — this prevents information leakage about token state.
- After credential verification, check user status. Blocked users → HTTP 403. **Exception:** admin token authentication bypasses the blocked-user check entirely — admin token auth verifies the token hash only, with no user table lookup. This ensures emergency access is never inadvertently locked out.
- **`AuthContext.UserID` for admin token auth:** `UserID` is set to an empty string for admin token authentication (no user table lookup is performed). Downstream handlers that need to identify admin actions in audit logs or access control decisions MUST check `AuthContext.IsAdmin == true` rather than relying on `UserID`. The `IsAdmin` flag is the canonical signal for admin identity.
- Set the resolved identity as an `AuthContext` struct in Echo's context under the constant key `auth_context`. The struct is defined as:

```go
// ContextKey is a typed key for Echo context values to avoid collisions.
// All middleware in this project MUST use a typed ContextKey (not a bare string)
// to prevent key collisions across middleware layers.
type ContextKey string

const AuthContextKey ContextKey = "auth_context"

// CredentialType identifies the kind of credential used to authenticate.
type CredentialType string

const (
    CredentialTypeAdmin          CredentialType = "admin"
    CredentialTypeAPIKey         CredentialType = "api_key"
    CredentialTypeWorkspaceToken CredentialType = "workspace_token"
)

// AuthContext holds the resolved identity for an authenticated request.
// It is stored in Echo's context under AuthContextKey.
// For admin token auth, UserID is empty and IsAdmin is true.
// Handlers performing audit logging or admin-gated checks MUST use IsAdmin,
// not UserID, to identify admin requests.
type AuthContext struct {
    CredentialType CredentialType // "admin", "api_key", or "workspace_token"
    UserID         string         // UUID string of the authenticated user; empty for admin token auth
    WorkspaceID    string         // UUID string of the workspace; empty string if not a workspace token
    IsAdmin        bool           // true only for admin token credential type
}
```

- Downstream handlers retrieve the context via `c.Get(string(AuthContextKey)).(*AuthContext)`.
- **Convention for future middleware:** All middleware introduced in subsequent specs MUST follow the same `ContextKey` typed-key pattern to prevent collisions. Bare string keys are disallowed.

### Error envelope

- All API errors return `{"error": {"code": <integer>, "message": "<string>"}}`.
- The `code` field mirrors the HTTP status code (e.g., `404`, `401`, `500`). There is no separate application-level error code enumeration — the HTTP status code is the authoritative error identifier.
- Standard status codes: 400 (bad request), 401 (unauthorized), 403 (forbidden), 404 (not found), 409 (conflict), 413 (payload too large), 500 (internal server error), 503 (service temporarily unavailable — used for DB write contention).
- The recover middleware MUST wrap panics into this same envelope format. A panic that reaches the recover middleware produces an HTTP 500 response with `{"error": {"code": 500, "message": "internal server error"}}`.
- Echo's built-in error responses (e.g., 404 for unmatched API routes, 405 for method-not-allowed on API routes) are intercepted by the global custom error handler (see [Custom error handler](#custom-error-handler)) and rendered in this envelope format for all non-health-probe routes.
- Health probe routes (`/healthz`, `/readyz`) return plain JSON objects (`{"status": "ok"}`, `{"status": "ready"}`, `{"status": "not ready"}`) — not the error envelope format. HTTP 405 responses on these routes use Echo's default behavior (no custom envelope).

### Request body limit

- Enforce 1 MB maximum request body. Requests exceeding this → HTTP 413.
- For requests with no body (e.g., GET, HEAD), the body-size limit middleware is a no-op — no 413 is issued.

### Testing strategy

The spec CLI (`spec generate`) is a **hard prerequisite** for implementation and will be run immediately after this refinement step completes. The tool is already installed. The `spec generate` step produces a `test_spec.json` artifact that is the authoritative and exclusive acceptance criteria for this spec. Implementers MUST NOT begin implementation before running `spec generate` and obtaining the `test_spec.json` artifact. The artifact includes full HTTP scenario definitions with inputs, expected status codes, and expected response bodies.

## Technical Boundaries

- **Language:** Go (1.22+)
- **HTTP framework:** Echo (`github.com/labstack/echo/v4`)
- **Database:** Embedded SQLite with WAL mode, pure-Go driver (`modernc.org/sqlite`) — no CGo
- **Config:** TOML (`github.com/BurntSushi/toml`)
- **Logging:** Structured JSON via logrus v1.4.0+ (`github.com/sirupsen/logrus`), always written to **stdout**. Minimum version v1.4.0 is required for native `trace` level support and MUST be pinned in `go.mod` (e.g., `require github.com/sirupsen/logrus v1.4.0`). Go's minimum version selection (MVS) guarantees this pin is respected; transitive dependencies pulling in older versions are overridden automatically. Build-time verification is provided by `go mod tidy` and `go mod verify`.
- **Concurrency primitives:** `sync/atomic` for the `/readyz` failure counter.
- **UUID:** `github.com/google/uuid`
- **Token hashing:** SHA-256 (crypto/sha256 from standard library)
- **TLS:** Not handled at the server layer; expected to be terminated by an upstream reverse proxy.
- **Build:** `make build` compiles `af-hub` binary to `bin/`
