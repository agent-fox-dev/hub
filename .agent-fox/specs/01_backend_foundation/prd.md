---
spec_id: '01'
spec_name: backend_foundation
title: Backend Foundation
status: draft
created_at: '2026-07-07T11:10:33.346069+00:00'
updated_at: '2026-07-07T11:10:33.346069+00:00'
owner: ''
source: ".agent-fox/specs/prd.md"
schema_version: 1
---
# Backend Foundation

## Intent

Establish the foundational Go project structure, configuration system, data storage layer, and operational infrastructure for af-hub — the coordination hub for the agent-fox platform. This spec builds the skeleton that all subsequent specs (auth, API endpoints, CLI, web UI) attach to.

The result is a running server binary that boots, loads configuration, initializes a SQLite database with the complete schema, performs admin bootstrap on first run, serves health probes, logs structured JSON, and shuts down gracefully. No HTTP API endpoints beyond health probes; no authentication middleware; no CLI client functionality — those are separate specs.

## Goals

- Create the Go project layout with two binary entry points: `af-hub` (server) and `afc` (CLI, stubbed).
- Implement TOML-based configuration loading with sensible defaults and validation.
- Initialize an embedded SQLite database with the complete schema for all entities (users, workspaces, workspace_members, api_keys, admin_tokens).
- Implement a store layer providing CRUD operations for all entities.
- Implement the admin bootstrap flow: detect first boot, create admin user, generate and persist admin token.
- Implement admin token rotation via `--reset-admin-token` flag.
- Implement admin token validation on subsequent boots via `AF_HUB_ADMIN_TOKEN`.
- Serve Kubernetes-compatible health probes at `/healthz` and `/readyz`.
- Implement structured JSON request logging.
- Implement graceful shutdown on SIGTERM/SIGINT with a 15-second drain timeout.
- Ship documentation: `README.md`, `docs/architecture.md`, `docs/configuration.md`.

## Non-goals

- HTTP API endpoints beyond health probes (covered by spec 02).
- Authentication middleware or RBAC enforcement (covered by spec 02).
- OAuth provider registry or callback handling (covered by spec 02).
- CLI login or key management commands (covered by spec 03).
- Web UI scaffolding (covered by spec 04).
- Database migration tooling — schema is applied on boot via `CREATE TABLE IF NOT EXISTS`.

## Functional Requirements

### Project structure

- The project follows standard Go layout: `cmd/af-hub/` for the server entry point, `cmd/afc/` for the CLI entry point (stubbed in this spec), and `internal/` for private packages.
- `make build` compiles both binaries to `bin/af-hub` and `bin/afc`.
- `make test` runs all tests.
- `make lint` runs `go vet`.

### Configuration

- The server loads `config.toml` from the current directory on startup.
- Configuration fields with defaults:
  - `[server] port` — integer, 1–65535, default 8080.
  - `[server] bind_address` — string, default `"0.0.0.0"`.
  - `[server] external_url` — string, optional, used for OAuth redirect URI derivation in production.
  - `[database] path` — string, default `"./data/af-hub.db"`.
  - `[logging] level` — string, one of `trace`/`debug`/`info`/`warn`/`error`/`fatal`/`panic`, default `"info"`.
  - `[[auth.oauth]]` — array of OAuth provider configs. Each entry: `provider` (string, required), `client_id` (string, required), `client_secret` (string, required), `authorize_url` / `token_url` / `userinfo_url` (strings, optional for providers with well-known URLs).
- The server refuses to start if `config.toml` is missing or contains invalid values.
- The data directory (parent of the database file) is created automatically if it does not exist.

### Database

- The server uses embedded SQLite with WAL mode for concurrent write safety, via the pure-Go driver `modernc.org/sqlite` (no CGo).
- On startup, the server creates all tables using `CREATE TABLE IF NOT EXISTS`:

**users** — `id` (TEXT PK, UUID), `username` (TEXT, UNIQUE NOT NULL), `email` (TEXT), `full_name` (TEXT), `provider` (TEXT, NOT NULL), `provider_id` (TEXT, NOT NULL), `status` (TEXT, DEFAULT 'active'), `created_at` (TEXT), `updated_at` (TEXT). Unique constraint on `(provider, provider_id)`.

**workspaces** — `id` (TEXT PK, UUID), `name` (TEXT, UNIQUE NOT NULL), `slug` (TEXT, UNIQUE NOT NULL), `url` (TEXT, UNIQUE NOT NULL), `status` (TEXT, DEFAULT 'active'), `created_at` (TEXT), `created_by` (TEXT, FK → users).

**workspace_members** — `user_id` (TEXT, FK → users), `workspace_id` (TEXT, FK → workspaces), `role` (TEXT, NOT NULL), `created_at` (TEXT), `granted_by` (TEXT, FK → users). PK: `(user_id, workspace_id)`.

**api_keys** — `id` (TEXT PK, UUID), `key_id` (TEXT, UNIQUE), `key_hash` (TEXT), `user_id` (TEXT, FK → users), `workspace_id` (TEXT, FK → workspaces), `label` (TEXT), `expires_at` (TEXT, nullable), `revoked_at` (TEXT, nullable), `created_at` (TEXT).

**admin_tokens** — `id` (TEXT PK), `token_hash` (TEXT), `created_at` (TEXT).

### Store layer

- The store layer provides typed Go functions for CRUD operations on all entities.
- All IDs are UUIDs generated via `github.com/google/uuid`.
- Timestamps are stored as RFC 3339 strings.
- The store layer is the only code that touches the database directly.

### Admin bootstrap

- On first boot (zero users in the database), the server automatically:
  1. Creates an admin user: `username: admin`, `email: admin@localhost`, `provider: local`, `provider_id: admin`.
  2. Generates a cryptographically random admin token: `af_admin_<64 hex chars>`.
  3. Stores the SHA-256 hash of the token in the `admin_tokens` table.
  4. Writes the plaintext token to an `admin_token` file (mode 0600) next to `config.toml`.
  5. Logs the file path at the `warn` level.
- On subsequent boots, the server reads `AF_HUB_ADMIN_TOKEN` from the environment, hashes it with SHA-256, and compares against the stored hash. The server refuses to start if the variable is missing or the hash does not match.

### Admin token rotation

- The server accepts a `--reset-admin-token` flag on boot.
- When set, it generates a new admin token (same flow as first boot: new random token, store hash, write plaintext to `admin_token` file), invalidating the old token immediately.
- Normal server startup continues after the token is rotated.

### Health probes

- `GET /healthz` — liveness probe, always returns HTTP 200 with body `{"status": "ok"}`.
- `GET /readyz` — readiness probe, pings the database. Returns HTTP 200 with `{"status": "ready"}` on success, or HTTP 503 with `{"status": "not ready"}` on failure.

### Logging

- Structured JSON logging via `github.com/sirupsen/logrus`.
- Log level is configurable via `config.toml`.
- Every HTTP request is logged with: method, path, status code, and duration.

### Graceful shutdown

- On SIGTERM or SIGINT, the server stops accepting new connections and drains existing ones with a 15-second timeout.
- If connections do not drain within the timeout, the server force-closes them and exits.

### Documentation

- `README.md` at the project root: project overview, prerequisites (Go 1.22+), quickstart (build, configure, run, first boot), project structure overview, links to other docs.
- `docs/architecture.md`: two-binary design, project layout, SQLite storage, config loading flow, request lifecycle.
- `docs/configuration.md`: `config.toml` reference with all fields, their types, defaults, validation rules, and environment variable overrides.

## Technical Boundaries

- **Language:** Go (1.22+)
- **HTTP framework:** Echo (`github.com/labstack/echo/v4`)
- **Database:** Embedded SQLite with WAL mode, pure-Go driver (`modernc.org/sqlite`)
- **Config format:** TOML via `github.com/BurntSushi/toml`
- **Logging:** Structured JSON via `github.com/sirupsen/logrus`
- **UUID generation:** `github.com/google/uuid`
- **Build tooling:** Makefile with `build`, `test`, `lint` targets
- **Token hashing:** SHA-256
- **Schema management:** `CREATE TABLE IF NOT EXISTS` on boot

## Design Decisions

1. **Admin token validation on boot:** The server refuses to start without a valid `AF_HUB_ADMIN_TOKEN` on subsequent boots (not first boot). This prevents accidental exposure of an unprotected server.
2. **WAL mode:** Enabled on database open for concurrent read/write safety.
3. **Pure-Go SQLite:** `modernc.org/sqlite` avoids CGo dependency, simplifying cross-compilation and deployment.
4. **Config file requirement:** The server requires `config.toml` to exist. This is intentional — explicit configuration prevents silent misconfiguration.
5. **Timestamps as RFC 3339 strings:** Stored as TEXT in SQLite for human readability and portability. Parsed in Go using `time.RFC3339`.

