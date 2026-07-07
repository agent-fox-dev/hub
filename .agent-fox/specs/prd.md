# af-hub

## Intent

af-hub is the coordination hub for the agent-fox platform — the single stateful process that will eventually own spec management, context storage, agent orchestration, run management, and the full operator and agent API surface described in the platform architecture.

This first iteration delivers the foundation that everything else depends on: user identity, OAuth authentication, role-based authorization, multi-tenant workspace management, and programmatic access control. Without this layer, no other platform capability can operate securely. af-hub is the gate that every user and every agent passes through.

## Goals

- Provide a pluggable OAuth-based authentication system, shipping with GitHub as the first provider.
- Establish multi-tenant workspace isolation where all resources are scoped to a workspace.
- Enable programmatic access via scoped, revocable API keys.
- Deliver a CLI (`afc`) for operator-driven login and key management.
- Set up the web UI toolchain and project scaffold, ready for future functional pages.
- Offer an admin bootstrap mechanism for initial system access on a fresh deployment, with support for token rotation.
- Ensure all API endpoints enforce role-based access control (RBAC).
- Ship comprehensive documentation as a first-class deliverable alongside every capability.

## Non-goals

- **Agent orchestration and spec-driven workflows.** af-hub provides auth and workspace management only in this iteration; coordination, runtime, and agent lifecycle are separate platform layers built later.
- **Repository-level scoping within a workspace.** Workspaces are the tenancy boundary; repository management within a workspace is future work.
- **Rate limiting.** Not implemented in the first iteration.
- **CORS middleware.** Vite dev proxy handles CORS in development; production serves static assets from same origin.
- **Database migration tooling.** Schema is applied on boot via `CREATE TABLE IF NOT EXISTS`.
- **Additional OAuth providers beyond GitHub.** Google, GitLab, and Keycloak will be added later via the same provider interface.
- **Billing, metering, or usage tracking.**
- **Functional web UI pages.** This iteration scaffolds the web project only; login flows, dashboards, and settings pages are future work.
- **Session-based authentication.** Session tokens for web UI auth will be specced when the web UI gets real pages.

## Functional Requirements

### First boot and admin bootstrap

- On first boot (zero users in the database), the server automatically creates an admin user (`username: admin`, `email: admin@localhost`, `provider: local`) and generates a cryptographically random admin token in the format `af_admin_<64 hex chars>`.
- The SHA-256 hash of the token is stored in the database. The plaintext token is written to an `admin_token` file (mode 0600) next to `config.toml`.
- The server logs the file path. The operator must save this token — it is the only credential with global admin access.
- On subsequent boots, the server reads `AF_HUB_ADMIN_TOKEN` from the environment, hashes it with SHA-256, and compares against the stored hash. The server refuses to start if the token is missing or does not match.

### Admin token rotation

- The server accepts a `--reset-admin-token` flag on boot.
- When set, it generates a new admin token (same flow as first boot: new random token, hash stored, plaintext written to `admin_token` file), invalidating the old token immediately.
- The operator must then update `AF_HUB_ADMIN_TOKEN` for subsequent boots.
- Normal server startup continues after the token is rotated.

### Authentication

- All `/api/v1/*` endpoints (except `/api/v1/auth/*`) require a Bearer token in the `Authorization` header.
- Two token types are accepted: admin tokens and API keys.
- The admin token (`af_admin_...`) grants global admin access to all endpoints and all workspaces.
- API keys (`af_<key_id>_<secret>`) are scoped to a specific user and workspace. The user's role in that workspace determines permissions.
- Revoked keys and expired keys are rejected with HTTP 401.
- Blocked users are rejected with HTTP 403 on every authenticated request, regardless of token validity. API keys belonging to blocked users are effectively inert but not deleted — if the user is unblocked, their keys resume working.

### OAuth provider registry

- The system authenticates users via a pluggable OAuth provider registry. Each provider implements a common interface: authorize URL, token exchange, and user info extraction.
- The first iteration ships with GitHub only.
- Adding a new provider requires registering it in the registry with its URLs and field mappings — no changes to auth middleware or handlers.
- If a provider is removed from `config.toml`, existing users authenticated through that provider retain their API keys and can continue to use them. Those users cannot re-authenticate via OAuth until they authenticate through another configured provider.

### OAuth flow (CLI)

- `afc login --provider github` fetches the provider list from the hub.
- The CLI opens the authorization URL in the user's browser.
- The CLI starts a local HTTP callback server on a random port.
- The CLI captures the authorization code and exchanges it with the hub via `POST /api/v1/auth/callback`.
- The hub exchanges the code with the identity provider, retrieves user info, and upserts the user: creates if new, updates username/email if existing. Blocked users are not re-activated on OAuth login.
- The hub returns the user object to the CLI.
- Admin-created users and OAuth-upserted users are the same population. If an admin creates a user with `provider: github, provider_id: 12345`, and that GitHub user later authenticates via OAuth, the existing record is matched and updated.

### Multi-tenancy and workspaces

- Resources are scoped to workspaces. A workspace is the top-level organizational unit, comparable to a GitHub Organization.
- Users are assigned roles per workspace via a membership table.
- API keys are scoped to a specific user + workspace pair.
- Both workspace names and slugs must be unique. Duplicate names or slugs return HTTP 409.
- Workspace slugs must be lowercase alphanumeric + hyphens, 3–64 characters, must start with a letter, and must not end with a hyphen.
- Workspace URLs must have a scheme (`http` or `https`) and a host at minimum.

### Workspace lifecycle

| State | Meaning | Allowed transitions |
|-------|---------|---------------------|
| **Active** | Default state. Resources and members are live. | → Archived |
| **Archived** | Read-only. All state preserved. Hidden from default listings. | → Active (reactivate), → Deleted |
| **Deleted** | Permanently removed. | Terminal |

- Only archived workspaces can be deleted. Attempting to delete an active workspace returns an error.
- Archiving preserves all state (members, API keys, associated data) and is fully reversible.
- Deleting a workspace permanently removes it along with its memberships and all API keys scoped to it.

### Roles and permissions

Three roles are implemented:

| Role | Scope | Description |
|------|-------|-------------|
| **admin** | Global | Full access to all endpoints and all workspaces |
| **editor** | Per-workspace | Read/write on resources within assigned workspaces |
| **reader** | Per-workspace | Read-only access within assigned workspaces |

Permission matrix:

| Endpoint | Admin | Editor | Reader |
|----------|-------|--------|--------|
| Create API key (per workspace) | yes | yes | no |
| List API keys | yes | yes | yes |
| Refresh API key | yes | yes | no |
| Revoke API key | yes | yes | no |
| Create / list / get / update user | yes | no | no |
| Create / list workspaces | yes | no | no |
| Archive / reactivate / delete workspace | yes | no | no |
| Add / list workspace members | yes | no | no |

Exception: Any authenticated user can update their own `full_name` via `PUT /api/v1/users/:id`, but only admins can change `status`.

### API key management

- API keys use the opaque format `af_<key_id>_<secret>`, where `key_id` is a random 8-character alphanumeric identifier and `secret` is a random 32-character alphanumeric string. Only the SHA-256 hash of the secret is stored.
- Keys are scoped to a specific user and workspace. The user must be a member of the workspace to create a key, and the key inherits the user's role in that workspace.
- When creating a key, `expires` accepts 0 (no expiry), 30, 60, or 90 (days). Default is 30. Expiry is calculated as exactly `24h x N` from the creation timestamp.
- The full key (including plaintext secret) is returned exactly once at creation time.
- Refreshing a key generates a new secret for an existing key (same `key_id`).
- Revoking a key is permanent.
- `GET /api/v1/keys` returns all keys for the authenticated user, including expired ones (the `expires_at` field makes their status clear). Expired keys cannot authenticate but remain visible for reference.
- When authenticated with an admin token, `GET /api/v1/keys` lists ALL keys across all users. When authenticated with an API key, it lists only the authenticated user's keys.

### API endpoints

#### Health probes (public)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/healthz` | Liveness probe — always returns 200 |
| GET | `/readyz` | Readiness probe — pings the database, returns 200 or 503 |

#### OAuth (public)

- `GET /api/v1/auth/providers` — List configured OAuth providers (no secrets exposed). Returns provider name and authorize URL.
- `POST /api/v1/auth/callback` — Exchange an OAuth authorization code for a user record. Accepts `provider`, `code`, and `redirect_uri`. Returns the user object. Creates or updates the user as needed.

#### User management (admin only)

- `POST /api/v1/users` — Create a user. Accepts `username`, `email`, `provider`, `provider_id`. Returns HTTP 201 with the created user. Returns HTTP 409 on duplicate username or duplicate `(provider, provider_id)`.
- `GET /api/v1/users` — List all users.
- `GET /api/v1/users/:id` — Get a user by ID, including workspace memberships and roles.
- `PUT /api/v1/users/:id` — Update a user's `full_name` or `status` (`active` | `blocked`).

#### Workspace management (admin only)

- `POST /api/v1/workspaces` — Create a workspace. Accepts `name`, `slug`, `url`. Returns HTTP 409 on duplicate name or slug.
- `GET /api/v1/workspaces` — List all workspaces. Archived workspaces are excluded by default; include them with a query parameter.
- `POST /api/v1/workspaces/:id/archive` — Archive a workspace.
- `POST /api/v1/workspaces/:id/reactivate` — Reactivate an archived workspace.
- `DELETE /api/v1/workspaces/:id` — Delete a workspace. Returns an error if the workspace is not archived.
- `POST /api/v1/workspaces/:id/members` — Add or update a user's role in a workspace. Accepts `user_id` and `role`.
- `GET /api/v1/workspaces/:id/members` — List all members of a workspace.

#### API key management (authenticated)

- `POST /api/v1/keys` — Create an API key scoped to a workspace. Accepts `workspace_id`, `label`, `expires` (0, 30, 60, or 90 days; default 30). Returns the full key including plaintext secret.
- `GET /api/v1/keys` — List all keys (scope depends on token type; see API key management section above).
- `POST /api/v1/keys/:key_id/refresh` — Generate a new secret for an existing key.
- `DELETE /api/v1/keys/:key_id` — Permanently revoke a key.

### Error handling

All API errors use a consistent JSON envelope: `{"error": {"code": "<HTTP_STATUS>", "message": "Human-readable description"}}`.

| Status | Meaning |
|--------|---------|
| 400 | Bad request — malformed JSON, missing required fields, validation failure |
| 401 | Unauthorized — missing, invalid, expired, or revoked token |
| 403 | Forbidden — valid token but insufficient role, or user is blocked |
| 404 | Not found — resource does not exist |
| 409 | Conflict — unique constraint violation (duplicate username, slug, name, etc.) |
| 413 | Payload too large — request body exceeds limit |
| 500 | Internal server error |

### CLI (`afc`)

The CLI binary is `afc`. All commands accept `--hub-url` (or `AF_HUB_URL` env var) to specify the hub server.

- `afc login --provider <provider>` — Run the OAuth authorization code flow. First iteration: GitHub only.
- `afc keys create --workspace <workspace-id> [--expires 0|30|60|90]` — Create an API key (default 30 days).
- `afc keys list` — List all keys for the authenticated user.
- `afc keys refresh <key-id>` — Refresh a key's secret.
- `afc keys revoke <key-id>` — Revoke a key.

All key commands also accept `--api-key` (or `AF_HUB_API_KEY`).

### Web UI scaffold

- Initialize the `web/` project at the repo root with its own `package.json`, cleanly separated from the Go backend.
- Set up the toolchain: Vite + React + TypeScript + Tailwind CSS + shadcn/ui.
- Configure the Vite dev proxy to forward API requests to the Go backend.
- Add `make web-dev` and `make web-build` targets.
- Ship a single "Hello world" route — no pages, no auth flow, no functional UI.
- `npm run dev` starts the Vite dev server with hot reload.
- `npm run build` produces a production build to `web/dist/`.
- `npm run lint` runs ESLint + TypeScript type checking.

### Configuration

The server loads `config.toml` from the current directory (TOML format).

Required configuration:
- Server port (default 8080) and bind address (default `0.0.0.0`).
- Database path (default `./data/af-hub.db`).
- Logging level (`trace`/`debug`/`info`/`warn`/`error`/`fatal`/`panic`; default `info`).
- OAuth provider configuration: provider name, client ID, client secret. Authorize URL, token URL, and userinfo URL are optional for providers with well-known URLs (e.g., GitHub); required for custom providers.

Environment variables:
- `AF_HUB_ADMIN_TOKEN` — Required on subsequent boots (validated against stored hash).
- `AF_HUB_URL` — Default hub URL for CLI commands.
- `AF_HUB_API_KEY` — Default API key for CLI key-management commands.

### Operational requirements

- Embedded SQLite with WAL mode for concurrent write safety.
- Structured JSON logging via logrus. Every request is logged with method, path, status, and duration.
- Graceful shutdown on SIGTERM/SIGINT with a 15-second drain timeout.
- Kubernetes-compatible health probes at `/healthz` and `/readyz`.

### Documentation

Documentation is a first-class deliverable. Each spec pack must ship its documentation alongside the code it introduces.

| Document | Location | Description |
|----------|----------|-------------|
| README.md | `/README.md` | Project overview, prerequisites, quickstart (build, configure, run, first boot), project structure |
| Architecture | `/docs/architecture.md` | System architecture: two-binary design, project layout, SQLite storage, config loading, request lifecycle |
| API reference | `/docs/api.md` | Complete REST API documentation: every endpoint with method, path, auth requirements, request/response bodies, status codes, error format |
| CLI reference | `/docs/cli.md` | `afc` usage: all commands and subcommands, flags, environment variables, examples |
| Configuration | `/docs/configuration.md` | `config.toml` reference: all fields, defaults, validation rules, environment variable overrides |
| Web UI development | `/docs/web-ui.md` | Frontend development guide: setup, dev server, build, project structure, component conventions |

Principles: docs ship with code, README is the entry point, API docs are the contract, docs live in `/docs/` (except README at root).

## Technical Boundaries

- **Language (backend):** Go
- **Language (frontend):** TypeScript
- **Two-binary design:** `af-hub` (API server) and `afc` (CLI client)
- **HTTP framework:** Echo (`github.com/labstack/echo/v4`)
- **CLI framework:** Cobra (`github.com/spf13/cobra`)
- **Database:** Embedded SQLite with WAL mode, pure-Go driver (`modernc.org/sqlite`) — no CGo
- **Config format:** TOML (`config.toml` from the current directory)
- **Frontend stack:** React, Vite, Tailwind CSS, shadcn/ui (copied into the tree), TanStack Query, React Router
- **Logging:** Structured JSON via logrus
- **Token hashing:** SHA-256 for admin tokens and API key secrets
- **Build:** `make build` compiles both binaries to `bin/`. `make test` runs all tests. `make lint` runs `go vet`.
- **Schema management:** `CREATE TABLE IF NOT EXISTS` on boot; no migration tooling
- **OAuth redirect URI (dev):** `http://localhost:5173/callback` (Vite default port). Production derives from `[server] external_url` in config.
- **OAuth prerequisite:** Operator must register the callback URL with the OAuth provider as a setup step.

## Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/labstack/echo/v4` | HTTP framework |
| `github.com/spf13/cobra` | CLI framework |
| `github.com/BurntSushi/toml` | Config file parsing |
| `github.com/sirupsen/logrus` | Structured logging |
| `github.com/google/uuid` | UUID generation |
| `modernc.org/sqlite` | Pure-Go SQLite driver |
| React | UI framework |
| Vite | Build tool and dev server |
| Tailwind CSS | Utility-first CSS |
| shadcn/ui + Radix UI | Component primitives |
| TanStack Query | API state management |
| React Router | Client-side routing |

## Design Decisions

1. **Admin token rotation mechanism:** The server accepts `--reset-admin-token` on boot to generate a new token and invalidate the old one. Chosen over a runtime CLI command because rotation should work even when the service is stopped, and the boot-time flow reuses the existing first-boot token generation logic.
2. **API key `expires: 0`:** Means "no expiry" (never expires). The `expires_at` field is nullable.
3. **Expiry calculation:** Exactly `24h x N` from the creation timestamp, where N is the number of days.
4. **User self-update:** `PUT /api/v1/users/:id` stays admin-only for `status` changes but allows any authenticated user to update their own `full_name`.
5. **Workspace URL validation:** Both `http://` and `https://` schemes allowed. Must have scheme + host at minimum.
6. **Slug validation:** Lowercase alphanumeric + hyphens, 3-64 characters, must start with a letter, must not end with a hyphen.
7. **API key list scope:** Admin token lists ALL keys across all users. API key auth lists only the authenticated user's keys.
8. **Expired keys in listings:** Expired keys remain visible in `GET /api/v1/keys` for reference but cannot authenticate.
9. **Blocked user handling:** Blocked users are rejected at the auth middleware level on every request. Their API keys are not deleted — just inert while blocked, functional again if unblocked.
10. **User population unity:** Admin-created users and OAuth-upserted users share one population. Matching is by `(provider, provider_id)`.
11. **Workspace name uniqueness:** Both `name` and `slug` must be unique across all workspaces.
12. **CORS:** Vite dev proxy handles CORS in development. No CORS middleware in Go — production serves static assets from same origin.
13. **Database schema application:** Applied on boot via `CREATE TABLE IF NOT EXISTS`. No migration tool for the first iteration.
14. **OAuth redirect URI registration:** Documented as a setup prerequisite — operator must register the callback URL with the OAuth provider.
