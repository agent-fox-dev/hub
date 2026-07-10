# af-hub

## Intent

af-hub is the coordination hub for the agent-fox platform — the single stateful process that owns user identity, OAuth authentication, role-based authorization, multi-tenant team management, workspace registration, and programmatic access control.

This iteration delivers the foundation that everything else depends on: user identity via pluggable OAuth, organizational isolation via teams, git-repo-scoped workspaces as the unit of work, scoped API keys, a CLI with persistent configuration, and a web UI scaffold. Without this layer, no other platform capability — spec management, agent orchestration, sandbox provisioning — can operate securely. af-hub is the gate that every user and every agent passes through.

## Goals

- Provide a pluggable OAuth-based authentication system, shipping with GitHub as the first provider.
- Establish multi-tenant team isolation where organizational resources are scoped to a team.
- Introduce workspaces as the git-repo-scoped execution context where work is done.
- Enable programmatic access via scoped, revocable API keys.
- Deliver a CLI (`afc`) for operator-driven login, key management, workspace registration, and persistent client configuration.
- Set up the web UI toolchain and project scaffold, ready for future functional pages.
- Offer an admin bootstrap mechanism for initial system access on a fresh deployment, with support for token rotation.
- Ensure all API endpoints enforce role-based access control (RBAC).
- Ship comprehensive documentation as a first-class deliverable alongside every capability.

## Non-goals

- **Agent orchestration and spec-driven workflows.** af-hub provides auth, team management, and workspace registration only in this iteration; coordination, runtime, and agent lifecycle are separate platform layers built later.
- **Sandbox/OpenShell container provisioning.** Workspaces are metadata entities mapping to git repos; sandbox creation is future work.
- **Spec package storage or lifecycle within a workspace.** Future coordination-layer work.
- **Git branch management, cloning, or checkout.** Workspace `git_url` is stored as metadata, not validated for reachability.
- **Workspace lifecycle beyond creation.** List, archive, and delete operations for workspaces are future work.
- **Campaign management, agent runs, or activity logs.**
- **Rate limiting.** Not implemented in the first iteration.
- **CORS middleware.** Vite dev proxy handles CORS in development; production serves static assets from same origin.
- **Database migration tooling.** Schema is applied on boot via `CREATE TABLE IF NOT EXISTS`.
- **Additional OAuth providers beyond GitHub.** Google, GitLab, and Keycloak will be added later via the same provider interface.
- **Billing, metering, or usage tracking.**
- **Functional web UI pages.** This iteration scaffolds the web project only; login flows, dashboards, and settings pages are future work.
- **Session-based authentication.** Session tokens for web UI auth will be specced when the web UI gets real pages.
- **OS keychain or secret store integration.** CLI tokens are stored in plaintext with restricted file permissions only.
- **Multi-profile or named-context CLI support.** One active hub URL and one default API key at a time.
- **Windows-specific path conventions.** The CLI targets Unix-like systems using `$HOME`.

## Functional Requirements

### First boot and admin bootstrap

- On first boot (zero users in the database), the server automatically creates an admin user (`username: admin`, `email: admin@localhost`, `provider: local`, `provider_id: admin`) and generates a cryptographically random admin token in the format `af_admin_<64 hex chars>`.
- The SHA-256 hash of the token is stored in the database. The plaintext token is written to an `admin_token` file (mode 0600) next to `config.toml`.
- The server logs the file path at the `warn` level. The operator must save this token — it is the only credential with global admin access.
- On subsequent boots, the server reads `AF_HUB_ADMIN_TOKEN` from the environment, hashes it with SHA-256, and compares against the stored hash. The server refuses to start if the token is missing or does not match.

### Admin token rotation

- The server accepts a `--reset-admin-token` flag on boot.
- When set, it generates a new admin token (same flow as first boot: new random token, hash stored, plaintext written to `admin_token` file), invalidating the old token immediately.
- The operator must then update `AF_HUB_ADMIN_TOKEN` for subsequent boots.
- Normal server startup continues after the token is rotated.

### Authentication

- All `/api/v1/*` endpoints (except `/api/v1/auth/*`) require a Bearer token in the `Authorization` header.
- Two token types are accepted: admin tokens and API keys.
- The admin token (`af_admin_...`) grants global admin access to all endpoints and all teams.
- API keys (`af_<key_id>_<secret>`) are scoped to a specific user and team. The user's role in that team determines permissions.
- Revoked keys and expired keys are rejected with HTTP 401.
- Blocked users are rejected with HTTP 403 on every authenticated request, regardless of token validity. API keys belonging to blocked users are effectively inert but not deleted — if the user is unblocked, their keys resume working.

### OAuth provider registry

- The system authenticates users via a pluggable OAuth provider registry. Each provider implements a common interface: authorize URL construction, authorization code exchange for tokens, and user info extraction.
- The first iteration ships with GitHub only. GitHub's well-known URLs are built-in defaults; `authorize_url`, `token_url`, and `userinfo_url` in config are optional overrides.
- Adding a new provider requires registering it in the registry with its URLs and field mappings — no changes to auth middleware or handlers.
- If a provider is removed from `config.toml`, existing users authenticated through that provider retain their API keys and can continue to use them. Those users cannot re-authenticate via OAuth until they authenticate through another configured provider.

### OAuth flow (CLI)

- `afc login --provider github` fetches the provider list from the hub.
- The CLI opens the authorization URL in the user's browser.
- The CLI starts a local HTTP callback server on a random port.
- The CLI captures the authorization code and exchanges it with the hub via `POST /api/v1/auth/callback`.
- The hub exchanges the code with the identity provider, retrieves user info, and upserts the user: creates if new, updates username/email if existing. Blocked users are not re-activated on OAuth login.
- The hub returns the user object and an auto-generated API key to the CLI.
- The CLI stores the returned credentials in the persistent config file (see CLI client configuration).
- Admin-created users and OAuth-upserted users are the same population. If an admin creates a user with `provider: github, provider_id: 12345`, and that GitHub user later authenticates via OAuth, the existing record is matched and updated.

### Multi-tenancy and teams

- Resources are scoped to teams. A team is the top-level organizational unit, comparable to a GitHub Organization.
- Users are assigned roles per team via a membership table.
- API keys are scoped to a specific user + team pair.
- Both team names and slugs must be unique. Duplicate names or slugs return HTTP 409.
- Team slugs must be lowercase alphanumeric + hyphens, 3–64 characters, must start with a letter, and must not end with a hyphen.
- Team URLs must have a scheme (`http` or `https`) and a host at minimum.

### Team lifecycle

| State | Meaning | Allowed transitions |
|-------|---------|---------------------|
| **Active** | Default state. Resources and members are live. | → Archived |
| **Archived** | Read-only. All state preserved. Hidden from default listings. | → Active (reactivate), → Deleted |
| **Deleted** | Permanently removed. | Terminal |

- Only archived teams can be deleted. Attempting to delete an active team returns an error.
- Archiving preserves all state (members, API keys, associated data) and is fully reversible.
- Deleting a team permanently removes it along with its memberships and all API keys scoped to it.

### Workspaces

A workspace is the context in which work is done: implementing a spec package, fixing a GitHub issue, or interactive agent work. Each workspace maps to one git repository.

- A workspace has: `slug` (globally unique), `git_url` (HTTPS or SSH format), optional `branch` (null means repo's default branch), `owner_id` (the creating user), optional `team_id` (team association), and `status` (active/archived, default active).
- Slug format: same rules as team slugs (lowercase alphanumeric + hyphens, 3–64 chars, starts with letter, no trailing hyphen).
- The same `git_url` may appear in multiple workspaces with different slugs (e.g. one workspace per feature branch, or one per developer).
- `git_url` accepts HTTPS (`https://...`) and SSH (`git@host:path`) formats. Not validated for reachability at creation time.
- Any authenticated user (with API key, not admin token) can create a workspace. If `team_id` is provided, the user must be a member of that team (any role).
- Admin tokens cannot create workspaces — a real user must be the owner.

### Roles and permissions

Three roles are implemented:

| Role | Scope | Description |
|------|-------|-------------|
| **admin** | Global | Full access to all endpoints and all teams |
| **editor** | Per-team | Read/write on resources within assigned teams |
| **reader** | Per-team | Read-only access within assigned teams |

Permission matrix:

| Endpoint | Admin | Editor | Reader |
|----------|-------|--------|--------|
| Create API key (per team) | yes | yes | no |
| List API keys | yes | yes | yes |
| Refresh API key | yes | yes | no |
| Revoke API key | yes | yes | no |
| Create / list / get / update user | yes | no | no |
| Create / list teams | yes | no | no |
| Archive / reactivate / delete team | yes | no | no |
| Add / list team members | yes | no | no |
| Create workspace | no | yes | yes |

Any authenticated user with an API key can create a workspace. Admin tokens cannot because workspaces require a real user as owner.

Exception: Any authenticated user can update their own `full_name` via `PUT /api/v1/users/:id`, but only admins can change `status`.

### API key management

- API keys use the opaque format `af_<key_id>_<secret>`, where `key_id` is a random 8-character alphanumeric identifier and `secret` is a random 32-character alphanumeric string. Only the SHA-256 hash of the secret is stored.
- Keys are scoped to a specific user and team. The user must be a member of the team to create a key, and the key inherits the user's role in that team.
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
- `POST /api/v1/auth/callback` — Exchange an OAuth authorization code for a user record and API key. Accepts `provider`, `code`, and `redirect_uri`. Creates or updates the user as needed. Returns:

```json
{
  "user": { ... },
  "api_key": {
    "key": "af_<key_id>_<secret>",
    "key_id": "<key_id>"
  }
}
```

#### User management (admin only)

- `POST /api/v1/users` — Create a user. Accepts `username`, `email`, `provider`, `provider_id`. Returns HTTP 201 with the created user. Returns HTTP 409 on duplicate username or duplicate `(provider, provider_id)`.
- `GET /api/v1/users` — List all users.
- `GET /api/v1/users/:id` — Get a user by ID, including team memberships and roles.
- `PUT /api/v1/users/:id` — Update a user's `full_name` or `status` (`active` | `blocked`).

#### Team management (admin only)

- `POST /api/v1/teams` — Create a team. Accepts `name`, `slug`, `url`. Returns HTTP 409 on duplicate name or slug.
- `GET /api/v1/teams` — List all teams. Archived teams excluded by default; include with `?include_archived=true`.
- `POST /api/v1/teams/:id/archive` — Archive a team.
- `POST /api/v1/teams/:id/reactivate` — Reactivate an archived team.
- `DELETE /api/v1/teams/:id` — Delete a team. Returns error if not archived. Cascades: deletes memberships and API keys.
- `POST /api/v1/teams/:id/members` — Add or update a user's role in a team. Accepts `user_id` and `role`.
- `GET /api/v1/teams/:id/members` — List all members of a team.

#### Workspace management (authenticated)

- `POST /api/v1/workspaces` — Create a workspace. Requires API key auth (not admin token). Accepts `slug`, `git_url`, `branch` (optional), `team_id` (optional). Returns HTTP 201 with the workspace object. Returns HTTP 409 on duplicate slug.

#### API key management (authenticated)

- `POST /api/v1/keys` — Create an API key scoped to a team. Accepts `team_id`, `label`, `expires` (0, 30, 60, or 90 days; default 30). Returns the full key including plaintext secret.
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

The CLI binary is `afc`. It uses persistent client configuration stored at `$HOME/.af/config.toml`.

#### Persistent client configuration

- On startup, if `$HOME/.af/config.toml` does not exist, `afc` creates `$HOME/.af/` (mode 0700) and `$HOME/.af/config.toml` (mode 0600) with `hub_url = ""`.
- Existing config files are not modified on startup.

**Config file structure:**

```toml
hub_url = "https://hub.example.com"
api_key = "my-project"

[keys.my-project]
key_id = "a1b2c3d4e5f6"
token = "af_a1b2c3d4e5f6_deadbeef..."
label = "dev laptop"

[keys._login]
key_id = "0011aabbccdd"
token = "af_0011aabbccdd_aabbccdd..."
label = "login"
```

**Resolution precedence** (for both hub URL and API key):
1. Command-line flag (`--hub-url`, `--api-key`) — highest priority
2. Environment variable (`AF_HUB_URL`, `AF_HUB_API_KEY`)
3. Config file value (empty string treated as unset)
4. Error with descriptive message

**Config-mutating commands:**

| Command | Config change |
|---------|---------------|
| `afc login` | Stores token as `[keys._login]`, sets `api_key = "_login"`, writes `hub_url` if empty |
| `afc keys create` | Adds `[keys.<team_slug>]` section |
| `afc keys refresh <key-id>` | Updates `token` in matching section |
| `afc keys revoke <key-id>` | Removes matching section; clears `api_key` if it was the default |
| `afc keys default <slug>` | Sets `api_key` to the specified slug |

All config mutations use atomic writes (write to temp file, rename into place).

#### Commands

- `afc login --provider <provider>` — Run the OAuth authorization code flow. Default provider: `github`. Stores returned credentials in config.
- `afc keys create --team <team-slug> [--label <label>] [--expires 0|30|60|90]` — Create an API key (default 30 days). Stores in config.
- `afc keys list` — List all keys for the authenticated user.
- `afc keys refresh <key-id>` — Refresh a key's secret. Updates config.
- `afc keys revoke <key-id>` — Revoke a key. Removes from config.
- `afc keys default <slug>` — Set the default API key by team slug.
- `afc workspace create --git-url <url> --slug <slug> [--branch <ref>] [--team <team-slug>]` — Register a workspace. The `--team` flag accepts a slug; the CLI resolves it to a UUID before the API call. On success, prints the workspace object as JSON.

All commands print JSON to stdout and human-readable messages to stderr.

### Web UI scaffold

- Initialize the `web/` project at the repo root with its own `package.json`, cleanly separated from the Go backend.
- Set up the toolchain: Vite + React + TypeScript + Tailwind CSS + shadcn/ui.
- Configure the Vite dev proxy to forward `/api`, `/healthz`, and `/readyz` requests to the Go backend.
- Add `make web-dev` and `make web-build` targets.
- Ship a single "Hello world" route — no pages, no auth flow, no functional UI.
- `npm run dev` starts the Vite dev server with hot reload.
- `npm run build` produces a production build to `web/dist/`.
- `npm run lint` runs ESLint + TypeScript type checking.

### Configuration

The server loads `config.toml` from the current directory (TOML format).

Required configuration:
- Server port (default 8080) and bind address (default `0.0.0.0`).
- `[server] external_url` — optional, used for OAuth redirect URI derivation in production.
- Database path (default `./data/af-hub.db`).
- Logging level (`trace`/`debug`/`info`/`warn`/`error`/`fatal`/`panic`; default `info`).
- OAuth provider configuration: provider name, client ID, client secret. Authorize URL, token URL, and userinfo URL are optional for providers with well-known URLs (e.g., GitHub).

Environment variables:
- `AF_HUB_ADMIN_TOKEN` — Required on subsequent boots (validated against stored hash).
- `AF_HUB_URL` — Default hub URL for CLI commands.
- `AF_HUB_API_KEY` — Default API key for CLI commands.

### Operational requirements

- Embedded SQLite with WAL mode for concurrent write safety.
- Structured JSON logging via logrus. Every request is logged with method, path, status, and duration.
- Graceful shutdown on SIGTERM/SIGINT with a 15-second drain timeout.
- Kubernetes-compatible health probes at `/healthz` and `/readyz`.

### Documentation

| Document | Location | Description |
|----------|----------|-------------|
| README.md | `/README.md` | Project overview, prerequisites, quickstart, project structure |
| Architecture | `/docs/architecture.md` | Two-binary design, project layout, SQLite storage, config loading, request lifecycle, team/workspace entity model |
| API reference | `/docs/api.md` | Complete REST API documentation: every endpoint with method, path, auth requirements, request/response bodies, status codes |
| CLI reference | `/docs/cli.md` | `afc` usage: all commands, flags, environment variables, config file interaction |
| Configuration | `/docs/configuration.md` | Server `config.toml` reference and client `$HOME/.af/config.toml` reference |
| Web UI development | `/docs/web-ui.md` | Frontend development guide |

## Technical Boundaries

- **Language (backend):** Go (1.22+)
- **Language (frontend):** TypeScript
- **Two-binary design:** `af-hub` (API server) and `afc` (CLI client)
- **HTTP framework:** Echo (`github.com/labstack/echo/v4`)
- **CLI framework:** Cobra (`github.com/spf13/cobra`)
- **Database:** Embedded SQLite with WAL mode, pure-Go driver (`modernc.org/sqlite`) — no CGo
- **Config format:** TOML (`config.toml` for server, `$HOME/.af/config.toml` for client)
- **Frontend stack:** React, Vite, Tailwind CSS, shadcn/ui (copied into tree), TanStack Query, React Router
- **Logging:** Structured JSON via logrus
- **Token hashing:** SHA-256 for admin tokens and API key secrets
- **Build:** `make build` compiles both binaries to `bin/`. `make test` runs all tests. `make lint` runs `go vet`.
- **Schema management:** `CREATE TABLE IF NOT EXISTS` on boot; no migration tooling
- **OAuth redirect URI (dev):** `http://localhost:5173/callback` (Vite default port). Production derives from `[server] external_url` in config.

## Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/labstack/echo/v4` | HTTP framework |
| `github.com/spf13/cobra` | CLI framework |
| `github.com/BurntSushi/toml` | Config file parsing (server and client) |
| `github.com/sirupsen/logrus` | Structured logging |
| `github.com/google/uuid` | UUID generation |
| `modernc.org/sqlite` | Pure-Go SQLite driver |
| React | UI framework |
| Vite | Build tool and dev server |
| Tailwind CSS | Utility-first CSS |
| shadcn/ui + Radix UI | Component primitives |
| TanStack Query | API state management |
| React Router | Client-side routing |

## Glossary

| Term | Definition |
|------|------------|
| **Team** | The top-level organizational unit, comparable to a GitHub Organization. The multi-tenancy boundary for users, roles, and API keys. (Previously called "workspace" in specs 01–04.) |
| **Workspace** | A git-repo-scoped execution context where work is done. Maps to one git repository and optional branch. Owned by a user, optionally associated with a team. |
| **API key** | A scoped, revocable credential in the format `af_<key_id>_<secret>`, tied to a user + team pair. |
| **Admin token** | A global credential in the format `af_admin_<64 hex>` that grants unrestricted access. |

## Design Decisions

1. **"Team" replaces "workspace" for the organizational concept.** The architecture defines "workspace" as a task-scoped execution context tied to a git repo. The original organizational entity is renamed to "team" to free the name. This is a terminology change only — the entity retains its schema, lifecycle, RBAC model, and API key scoping.
2. **Auth callback returns an API key alongside the user object.** This enables the CLI to be fully configured after a single `afc login` — no manual key creation step required for basic use.
3. **CLI uses persistent config at `$HOME/.af/config.toml`.** Eliminates the need for `--hub-url` and `--api-key` flags on every command. Tokens stored in plaintext with `0600` permissions; keychain integration deferred.
4. **Admin token rotation via boot flag.** `--reset-admin-token` reuses first-boot token generation logic. Chosen over a runtime CLI command because rotation should work even when the service is stopped.
5. **API key `expires: 0` means no expiry.** The `expires_at` field is nullable.
6. **Workspaces are user-owned, not admin-managed.** Any authenticated user (via API key) can create a workspace. Admin tokens cannot, since workspaces need a real user as owner.
7. **Workspace `git_url` not validated for reachability.** URL is stored as metadata for later use by sandbox provisioning. Network validation would add latency and fragility.
8. **Fresh schema rebuild for the rename.** Since the project is pre-production with no deployed users, the team rename is implemented by updating DDL directly — no ALTER TABLE or data migration.
9. **Config file atomic writes, no locking.** Last writer wins. Concurrent mutations are not a realistic scenario for a single-user interactive CLI.
10. **Blocked user handling at middleware level.** Keys are inert while user is blocked, functional again if unblocked. Keys are not deleted on block.
11. **Expired keys remain visible in listings.** For reference, but cannot authenticate.
