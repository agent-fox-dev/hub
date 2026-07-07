# Architecture

This document covers the af-hub backend architecture: the two-binary design,
internal package layout, storage layer, configuration loading flow, and HTTP
request lifecycle.

For the broader three-layer system design (coordination, runtime, services),
see [docs/README.md](README.md).

## Two-binary design

The project produces two binaries from `cmd/`:

| Binary | Source | Purpose |
|--------|--------|---------|
| **af-hub** | `cmd/af-hub/` | The coordination hub — a single stateful HTTP server that owns user management, workspace management, API key authentication, and health probes. |
| **afc** | `cmd/afc/` | The CLI client — a command-line interface for interacting with the af-hub server. Currently a stub; full CLI functionality is deferred to later specs. |

Both binaries are compiled via `make build` and placed in `bin/`.

## Internal package layout

All private packages live under `internal/` and are not importable outside the
module. No Go source files outside `cmd/` and `internal/` contain
application logic.

```
internal/
├── auth/           OAuth provider registry, RBAC, auth middleware
├── bootstrap/      Admin user creation, token generation/validation/rotation
├── cli/            afc CLI commands (Cobra)
├── config/         TOML config loading, validation, data directory setup
├── db/             SQLite database open, WAL mode, schema initialization
├── handler/        HTTP route handlers (auth, users, workspaces, API keys)
├── integration/    Integration and smoke tests
├── logging/        Logrus JSON formatter setup and level configuration
├── middleware/     Echo middleware (structured request logger)
├── server/         Echo HTTP server initialization, health probe routes
└── store/          Typed CRUD operations — the ONLY package executing SQL
```

### Store layer exclusivity

The `store` package is the sole package that imports `database/sql` for query
execution and runs SQL statements (`db.Query`, `db.Exec`, `db.QueryRow`). All
other packages interact with data exclusively through the `Store` interface.
This invariant is enforced by a static-analysis test.

## SQLite storage

The hub uses **SQLite** via the pure-Go driver
[modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — no CGo
dependency required.

### WAL mode

On database open, the server immediately enables Write-Ahead Logging:

```sql
PRAGMA journal_mode=WAL
```

WAL mode allows concurrent readers while a single writer is active, which is
important for health probe pings occurring concurrently with write operations.
WAL mode is always enabled before any schema initialization.

### Schema

The database schema consists of five tables, all created with
`CREATE TABLE IF NOT EXISTS` on startup:

| Table | Primary key | Notable constraints |
|-------|-------------|---------------------|
| `users` | `id` (UUID TEXT) | UNIQUE on `username`; UNIQUE on `(provider, provider_id)` |
| `workspaces` | `id` (UUID TEXT) | UNIQUE on `name`, `slug`, `url` |
| `workspace_members` | `(user_id, workspace_id)` composite | FK to `users` and `workspaces` |
| `api_keys` | `id` (UUID TEXT) | UNIQUE on `key_id`; FK to `users` and `workspaces` |
| `admin_tokens` | `id` (UUID TEXT) | Stores SHA-256 hash of the admin bootstrap token |

All primary keys are UUID v4 TEXT values (generated via `github.com/google/uuid`).
All timestamps are stored as RFC 3339 TEXT strings.

## Config loading flow

The startup sequence in `cmd/af-hub/main.go` proceeds as follows:

```
1. Parse flags          --reset-admin-token
2. LoadConfig           Read config.toml (BurntSushi/toml), apply defaults
3. ValidateConfig       Port range, log level, OAuth provider fields
4. ConfigureLogging     Set logrus to JSON formatter + configured level
5. EnsureDataDir        os.MkdirAll for the database parent directory
6. OpenDatabase         Open SQLite, Ping, enable WAL mode
7. InitSchema           CREATE TABLE IF NOT EXISTS × 5
8. Bootstrap/Validate   First boot → RunAdminBootstrap
                        --reset-admin-token → RotateAdminToken
                        Otherwise → ValidateAdminToken (AF_HUB_ADMIN_TOKEN)
9. NewServer            Create Echo instance, register middleware + routes
10. Start               Bind to configured address:port
11. Signal handler      SIGTERM/SIGINT → graceful shutdown (15s drain)
```

Every step's error causes a `logrus.Fatal` exit before any port is bound,
ensuring the server never accepts traffic in a misconfigured state.

## Request lifecycle

```
Client request
  │
  ▼
Echo Router
  │
  ├── RequestLoggerMiddleware (logs method, path, status, duration as JSON)
  │
  ├── /healthz  →  200 {"status":"ok"}           (no DB interaction)
  ├── /readyz   →  db.PingContext (2s timeout)
  │                 success: 200 {"status":"ready"}
  │                 failure: 503 {"status":"not ready"}
  │
  └── (other routes via registered handlers)
         │
         ▼
      Store layer (typed Go functions)
         │
         ▼
      SQLite (WAL mode)
```

### Graceful shutdown

When the server receives SIGTERM or SIGINT:

1. Echo stops accepting new connections.
2. A 15-second drain timeout allows in-flight requests to complete.
3. If all connections drain in time, the database is closed and the process
   exits with code 0.
4. If the drain timeout expires, connections are force-closed, a warning is
   logged, and the process exits with a non-zero code.
5. The database close operation itself has a 5-second timeout to prevent
   indefinite blocking.
