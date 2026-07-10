# af-hub

## Intent

af-hub is the coordination hub for the agent-fox platform — the single stateful process that owns user identity, OAuth authentication, workspace ownership, and programmatic access control.

This iteration delivers the foundation that everything else depends on: user identity via pluggable OAuth, git-repo-scoped workspaces as the unit of work with clear ownership, delegated workspace access via scoped tokens, a CLI (`afc`) for operator-driven login, workspace registration, and token management, and a web UI scaffold. Without this layer, no other platform capability — spec management, agent orchestration, sandbox provisioning — can operate securely. af-hub is the gate that every user and every agent passes through.

## Goals

- Provide a pluggable OAuth-based authentication system, shipping with GitHub as the first provider.
- Establish workspaces as the git-repo-scoped execution context where work is done, with clear user ownership.
- Enable delegated workspace access via scoped, revocable workspace tokens for tools and agents.
- Deliver a CLI (`afc`) for operator-driven login, workspace registration, token management, and persistent client configuration.
- Set up the web UI toolchain and project scaffold, ready for future functional pages.
- Offer an admin bootstrap mechanism for initial system access on a fresh deployment, with support for token rotation.
- Ensure all API endpoints enforce access control based on credential type (admin token, user API key, or workspace token).
- Ship comprehensive documentation as a first-class deliverable alongside every capability.

## Non-goals

- **Agent orchestration and spec-driven workflows.** af-hub provides auth, workspace ownership, and access delegation only in this iteration; coordination, runtime, and agent lifecycle are separate platform layers built later.
- **Sandbox/OpenShell container provisioning.** Workspaces are metadata entities mapping to git repos; sandbox creation is future work.
- **Spec package storage or lifecycle within a workspace.** Future coordination-layer work.
- **Git branch management, cloning, or checkout.** Workspace `git_url` is stored as metadata, not validated for reachability.
- **Workspace lifecycle beyond creation and token management.** Archive and delete operations for workspaces are future work.
- **Team member removal.** Removing users from teams is deferred to a future iteration.
- **Team-based role-based access control (RBAC).** Teams exist as a lightweight organizational grouping only. Granular per-team roles (editor, viewer, etc.) are deferred to a future iteration.
- **Campaign management, agent runs, or activity logs.**
- **Rate limiting.** Not implemented in the first iteration.
- **CORS middleware.** Vite dev proxy handles CORS in development; production serves static assets from same origin.
- **Database migration tooling.** Schema is applied on boot via `CREATE TABLE IF NOT EXISTS`.
- **Additional OAuth providers beyond GitHub.** Google, GitLab, and Keycloak will be added later via the same provider interface.
- **Billing, metering, or usage tracking.**
- **Functional web UI pages.** This iteration scaffolds the web project only; login flows, dashboards, and settings pages are future work.
- **Session-based authentication.** Session tokens for web UI auth will be specced when the web UI gets real pages.
- **OS keychain or secret store integration.** CLI tokens are stored in plaintext with restricted file permissions only.
- **Multi-profile or named-context CLI support.** One active hub URL and one API key at a time.
- **Windows-specific path conventions.** The CLI targets Unix-like systems using `$HOME`.
- **Workspace token persistence in CLI config.** Workspace tokens are the user's responsibility to store securely.
- **Granular workspace token permissions.** Workspace tokens have read-only access in this iteration. Fine-grained permissions are future work.

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

- All `/api/v1/*` endpoints (except `/api/v1/auth/*`) require authentication via a Bearer token in the `Authorization` header.
- Three credential types are accepted:

| Credential | Format | Scope | Access level |
|------------|--------|-------|-------------|
| Admin token | `af_admin_<64 hex>` | Global | Full access to all endpoints and resources |
| User API key | `af_<key_id>_<secret>` | User-scoped | Full access to own resources; workspace owner access |
| Workspace token | `af_wt_<token_id>_<secret>` | Workspace-scoped | Read-only access to the specific workspace |

- The admin token (`af_admin_...`) grants unrestricted access to all endpoints and all resources.
- User API keys identify a user. The server looks up the `key_id`, verifies the hashed secret, and resolves the associated user. The key is always associated with exactly one user.
- Workspace tokens grant read-only access to a single workspace, acting on behalf of the user who created them. The server looks up the `token_id`, verifies the hashed secret, and resolves the associated workspace and user.
- Expired or revoked credentials are rejected with HTTP 401.
- Blocked users are rejected with HTTP 403 on every authenticated request, regardless of credential validity. Workspace tokens created by blocked users are effectively inert — if the user is unblocked, their tokens resume working.

### OAuth provider registry

- The system authenticates users via a pluggable OAuth provider registry. Each provider implements a common interface: authorize URL construction, authorization code exchange for tokens, and user info extraction.
- The first iteration ships with GitHub only. GitHub's well-known URLs are built-in defaults; `authorize_url`, `token_url`, and `userinfo_url` in config are optional overrides.
- Adding a new provider requires registering it in the registry with its URLs and field mappings — no changes to auth middleware or handlers.
- If a provider is removed from `config.toml`, existing users authenticated through that provider retain their API keys and can continue to use them. Those users cannot re-authenticate via OAuth until they authenticate through another configured provider.

### OAuth flow (CLI)

- `afc login --provider github` fetches the provider list from the hub.
- The CLI opens the authorization URL in the user's browser, including a cryptographically random `state` parameter for CSRF protection.
- The CLI starts a local HTTP callback server on a random port.
- The CLI receives the callback, validates the `state` parameter matches the one it sent, and captures the authorization code.
- The CLI exchanges the code with the hub via `POST /api/v1/auth/callback`.
- The hub validates that `redirect_uri` matches the configured allowlist (in development: `http://localhost:*`; in production: derived from `[server] external_url`). Mismatched URIs are rejected with HTTP 400.
- The hub exchanges the code with the identity provider and retrieves user info. If the provider returns a null or empty email, the login fails with an error — email is a required field.
- The hub upserts the user: creates if new, updates username/email if existing. Blocked users are not re-activated on OAuth login.
- The hub generates a new user API key for the user (revoking any previously active key for that user) and returns the user object and API key to the CLI.
- The CLI stores the hub URL, user ID, and API key in the persistent config file (see CLI client configuration).
- Admin-created users and OAuth-upserted users are the same population. If an admin creates a user with `provider: github, provider_id: 12345`, and that GitHub user later authenticates via OAuth, the existing record is matched and updated.

### Teams

Teams are a lightweight organizational grouping mechanism. In this iteration, teams have no permission implications — they serve as a way to organize users and workspaces into logical groups for future use.

- A team has: `name` (unique), `slug` (unique), `url`, and `status`.
- Both team names and slugs must be unique. Duplicate names or slugs return HTTP 409.
- Team slugs must be lowercase alphanumeric + hyphens, 3–64 characters, must start with a letter, and must not end with a hyphen.
- Team URLs must have a scheme (`http` or `https`) and a host at minimum.
- Users can be associated with teams via a membership table. Membership has no permission implications in this iteration.

### Team lifecycle

| State | Meaning | Allowed transitions |
|-------|---------|---------------------|
| **Active** | Default state. | → Archived |
| **Archived** | Read-only. All state preserved. Hidden from default listings. | → Active (reactivate), → Deleted |
| **Deleted** | Permanently removed. | Terminal |

- Only archived teams can be deleted. Attempting to delete an active team returns an error.
- Archiving preserves all state (members, associated data) and is fully reversible.
- Deleting a team permanently removes it along with its memberships.

### Workspaces

A workspace is the context in which work is done: implementing a spec package, fixing a GitHub issue, or interactive agent work. Each workspace maps to one git repository.

- A workspace has: `slug` (globally unique), `git_url` (HTTPS or SSH format), optional `branch` (null means repo's default branch), `owner_id` (the creating user), optional `team_id` (organizational association), and `status` (active/archived, default active).
- Slug format: same rules as team slugs (lowercase alphanumeric + hyphens, 3–64 chars, starts with letter, no trailing hyphen).
- The same `git_url` may appear in multiple workspaces with different slugs (e.g. one workspace per feature branch, or one per developer).
- `git_url` accepts HTTPS (`https://...`) and SSH (`git@host:path`) formats. Not validated for reachability at creation time.
- Only users authenticated with a user API key (not admin token or workspace token) can create a workspace. The creating user becomes the workspace owner.
- The workspace owner has full access to the workspace and its resources via their main API key. This includes creating and managing workspace tokens.
- Admin tokens grant full access to any workspace but cannot create workspaces (workspaces require a real user as owner).

### Workspace tokens

Workspace tokens enable delegated access to a workspace. They are designed for tools, agents, programs, and other automated entities that need to interact with a workspace on behalf of the owner.

- Workspace tokens use the format `af_wt_<token_id>_<secret>`, where `token_id` is a random 8-character alphanumeric identifier and `secret` is a random 32-character alphanumeric string. Only the SHA-256 hash of the secret is stored.
- Each token is scoped to a single workspace and references the creating user (the workspace owner).
- Workspace tokens grant **read-only** access to the workspace by default. No other access levels are defined in this iteration.
- When creating a workspace token, `expires` accepts 0 (no expiry), 30, 60, or 90 (days). Default is 30. Expiry is calculated as exactly `24h × N` from the creation timestamp. The `expires_at` field is nullable (null when `expires` is 0).
- A workspace token can optionally carry a `label` (human-readable name, e.g. "ci-bot", "agent-1").
- Expired workspace tokens cannot authenticate but remain visible in listings for reference.
- Only the workspace owner can create, list, and revoke tokens for their workspace. Admin tokens can also manage tokens on any workspace.
- The full token (including plaintext secret) is returned exactly once at creation time. It is the user's responsibility to store it securely — workspace tokens are NOT persisted in the CLI config file.
- Revoking a workspace token is permanent.
- When a workspace token is used for authentication, API requests are scoped to the specific workspace with read-only access. The token holder cannot access other workspaces, create workspaces, or manage tokens.
- Tokens created by blocked users are inert. If the user is unblocked, the tokens resume working.

### Access control

Three access levels are implemented:

| Level | Scope | Description |
|-------|-------|-------------|
| **Admin** | Global | Full access to all endpoints and all resources |
| **Owner** | Per-workspace | Full access to owned workspaces, including token management |
| **Token holder** | Per-workspace | Read-only access to the specific workspace the token is scoped to |

Permission matrix:

| Endpoint | Admin | Owner (own workspace) | Token holder | Regular user |
|----------|-------|-----------------------|-------------|-------------|
| Create workspace | no\* | — | no | yes |
| List workspaces | yes (all) | yes (own) | no | yes (own) |
| Get workspace | yes | yes | yes (scoped) | no |
| Create workspace token | yes | yes | no | no |
| List workspace tokens | yes | yes | no | no |
| Revoke workspace token | yes | yes | no | no |
| List API keys | yes (all) | — | no | yes (own) |
| Refresh API key | yes | — | no | yes (own) |
| Revoke API key | yes | — | no | yes (own) |
| Create / list / get / update user | yes | no | no | no |
| Create / list teams | yes | no | no | no |
| Archive / reactivate / delete team | yes | no | no | no |
| Add / list team members | yes | no | no | no |

\*Admin tokens cannot create workspaces — a real user must be the owner.

Exception: Any authenticated user can update their own `full_name` via `PUT /api/v1/users/:id` — the middleware checks whether the requesting user's ID matches `:id` and permits the update if so. Only admins can change `status`.

### API key management

- User API keys use the opaque format `af_<key_id>_<secret>`, where `key_id` is a random 8-character alphanumeric identifier and `secret` is a random 32-character alphanumeric string. Only the SHA-256 hash of the secret is stored.
- Each user has one active API key at a time. A new login generates a new key, revoking the previous one.
- When creating a key (via login), `expires` accepts 0 (no expiry), 30, 60, or 90 (days). Default is 90. Expiry is calculated as exactly `24h × N` from the creation timestamp. The `expires_at` field is nullable (null when `expires` is 0).
- The full key (including plaintext secret) is returned at login and on refresh.
- Refreshing a key generates a new secret for an existing key (same `key_id`) and resets the expiry based on the original expiry duration.
- Revoking a key is permanent. The user must re-login to obtain a new key.
- Expired keys cannot authenticate but remain visible in listings for reference (the `expires_at` field makes their status clear).
- `GET /api/v1/keys` returns all keys across all users when authenticated with an admin token. When authenticated with a user API key, it returns only the authenticated user's key.

### API endpoints

#### Health probes (public)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/healthz` | Liveness probe — always returns 200 |
| GET | `/readyz` | Readiness probe — pings the database, returns 200 or 503 |

#### OAuth (public)

- `GET /api/v1/auth/providers` — List configured OAuth providers (no secrets exposed). Returns provider name and authorize URL.
- `POST /api/v1/auth/callback` — Exchange an OAuth authorization code for a user record and API key. Accepts `provider`, `code`, `redirect_uri`, and `expires` (0, 30, 60, or 90 days; default 90). Validates `redirect_uri` against the configured allowlist. Creates or updates the user as needed. Fails if the provider returns a null or empty email. Generates a new API key for the user (revoking any existing key). Returns:

```json
{
  "user": {
    "id": "<uuid>",
    "username": "<string>",
    "email": "<string>",
    "full_name": "<string>",
    "status": "active",
    "provider": "<string>",
    "provider_id": "<string>",
    "created_at": "<timestamp>",
    "updated_at": "<timestamp>"
  },
  "api_key": {
    "key": "af_<key_id>_<secret>",
    "key_id": "<key_id>"
  }
}
```

#### User management (admin only)

- `POST /api/v1/users` — Create a user. Accepts `username`, `email`, `provider`, `provider_id`. Returns HTTP 201 with the created user. Returns HTTP 409 on duplicate username or duplicate `(provider, provider_id)`.
- `GET /api/v1/users` — List all users.
- `GET /api/v1/users/:id` — Get a user by ID, including team memberships.
- `PUT /api/v1/users/:id` — Update a user's `full_name` or `status` (`active` | `blocked`).

#### Team management (admin only)

- `POST /api/v1/teams` — Create a team. Accepts `name`, `slug`, `url`. Returns HTTP 409 on duplicate name or slug.
- `GET /api/v1/teams` — List all teams. Archived teams excluded by default; include with `?include_archived=true`.
- `POST /api/v1/teams/:id/archive` — Archive a team.
- `POST /api/v1/teams/:id/reactivate` — Reactivate an archived team.
- `DELETE /api/v1/teams/:id` — Delete a team. Returns error if not archived. Cascades: deletes memberships.
- `POST /api/v1/teams/:id/members` — Add a user to a team. Accepts `user_id`.
- `GET /api/v1/teams/:id/members` — List all members of a team.

#### Workspace management (authenticated)

- `POST /api/v1/workspaces` — Create a workspace. Requires user API key auth (not admin token or workspace token). Accepts `slug`, `git_url`, `branch` (optional), `team_id` (optional). Returns HTTP 201 with the workspace object. Returns HTTP 409 on duplicate slug.
- `GET /api/v1/workspaces` — List workspaces. Admin: all workspaces. User (API key): own workspaces only. Workspace tokens: not allowed.
- `GET /api/v1/workspaces/:slug` — Get a workspace by slug. Requires workspace ownership, admin, or a valid workspace token scoped to this workspace.

#### Workspace token management (authenticated)

- `POST /api/v1/workspaces/:slug/tokens` — Create a workspace token. Requires workspace ownership or admin. Accepts `label` (optional) and `expires` (0, 30, 60, or 90 days; default 30). Returns the full token including plaintext secret.
- `GET /api/v1/workspaces/:slug/tokens` — List all tokens for a workspace (token_id, label, created_at — never the secret). Requires workspace ownership or admin.
- `DELETE /api/v1/workspaces/:slug/tokens/:token_id` — Permanently revoke a workspace token. Requires workspace ownership or admin.

#### API key management (authenticated)

- `GET /api/v1/keys` — List all keys (admin: all users; user API key: own key only).
- `POST /api/v1/keys/:key_id/refresh` — Generate a new secret for the authenticated user's key. Returns the full key with new secret.
- `DELETE /api/v1/keys/:key_id` — Permanently revoke a key.

### Error handling

All API errors use a consistent JSON envelope: `{"error": {"code": <HTTP_STATUS>, "message": "Human-readable description"}}`, where `code` is an integer (e.g. `409`, not `"409"`).

| Status | Meaning |
|--------|---------|
| 400 | Bad request — malformed JSON, missing required fields, validation failure |
| 401 | Unauthorized — missing, invalid, or revoked credential |
| 403 | Forbidden — valid credential but insufficient access, or user is blocked |
| 404 | Not found — resource does not exist |
| 409 | Conflict — unique constraint violation (duplicate username, slug, name, etc.) |
| 413 | Payload too large — request body exceeds limit |
| 500 | Internal server error |

### CLI (`afc`)

The CLI binary is `afc`. It uses persistent client configuration stored at `$HOME/.af/config.toml`.

#### Persistent client configuration

- On startup, if `$HOME/.af/config.toml` does not exist, `afc` creates `$HOME/.af/` (mode 0700) and `$HOME/.af/config.toml` (mode 0600) with empty values.
- Existing config files are not modified on startup.

**Config file structure:**

```toml
hub_url = "https://hub.example.com"
user_id = "550e8400-e29b-41d4-a716-446655440000"
api_key = "af_a1b2c3d4_deadbeef..."
```

Three fields only:
- `hub_url` — The hub's base URL.
- `user_id` — The authenticated user's UUID (received from OAuth callback).
- `api_key` — The user's main API key (received from OAuth callback).

**Resolution precedence** (for hub URL, user ID, and API key):
1. Command-line flag (`--hub-url`, `--user-id`, `--api-key`) — highest priority
2. Environment variable (`AF_HUB_URL`, `AF_HUB_USER_ID`, `AF_HUB_API_KEY`)
3. Config file value (empty string treated as unset)
4. Error with descriptive message

**Config-mutating commands:**

| Command | Config change |
|---------|---------------|
| `afc login` | Sets `hub_url`, `user_id`, and `api_key` |
| `afc keys refresh` | Updates `api_key` |
| `afc keys revoke` | Clears `api_key` and `user_id` |

All config mutations use atomic writes (write to temp file, rename into place).

#### Commands

- `afc login --provider <provider> [--expires 0|30|60|90]` — Run the OAuth authorization code flow. Default provider: `github`. Default key expiry: 90 days. Stores returned credentials in config.
- `afc keys list` — Show the current API key metadata (key_id, created_at). Admin: list all keys across users.
- `afc keys refresh` — Refresh the current API key (new secret, same key_id). Updates config.
- `afc keys revoke` — Revoke the current API key. Clears credentials from config. User must re-login to obtain a new key.
- `afc workspace create --git-url <url> --slug <slug> [--branch <ref>] [--team <team-slug>]` — Register a workspace. The `--team` flag accepts a slug; the CLI resolves it to a UUID before the API call. On success, prints the workspace object as JSON.
- `afc workspace list` — List the user's workspaces. Prints JSON.
- `afc workspace get <slug>` — Get workspace details by slug. Prints JSON.
- `afc workspace token create --workspace <slug> [--label <label>] [--expires 0|30|60|90]` — Create a workspace token (default 30 days). Prints the full token (including plaintext secret) to stdout. The token is NOT stored in the config file — it is the user's responsibility to store it securely.
- `afc workspace token list --workspace <slug>` — List workspace tokens (metadata only, no secrets). Prints JSON.
- `afc workspace token revoke --workspace <slug> <token-id>` — Permanently revoke a workspace token.

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
- `AF_HUB_USER_ID` — Default user ID for CLI commands.
- `AF_HUB_API_KEY` — Default API key for CLI commands.

### Operational requirements

- Embedded SQLite with WAL mode for concurrent write safety.
- Structured JSON logging via logrus. Every request is logged with method, path, status, and duration.
- Graceful shutdown on SIGTERM/SIGINT with a 15-second drain timeout.
- Request body size limit: 1 MB. Requests exceeding this limit are rejected with HTTP 413.
- Kubernetes-compatible health probes at `/healthz` and `/readyz`.

### Documentation

| Document | Location | Description |
|----------|----------|-------------|
| README.md | `/README.md` | Project overview, prerequisites, quickstart, project structure |
| Architecture | `/docs/architecture.md` | Two-binary design, project layout, SQLite storage, config loading, request lifecycle, workspace entity model |
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
- **Token hashing:** SHA-256 for admin tokens, API key secrets, and workspace token secrets
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
| **Team** | A lightweight organizational grouping of users and workspaces. No permission implications in this iteration. |
| **Workspace** | A git-repo-scoped execution context where work is done. Maps to one git repository and optional branch. Owned by a user, optionally associated with a team. |
| **User API key** | A user-scoped credential in the format `af_<key_id>_<secret>`, tied to a single user. One active key per user, created on login. |
| **Workspace token** | A workspace-scoped, read-only credential in the format `af_wt_<token_id>_<secret>`, created by the workspace owner for delegation to tools, agents, and programs. Not stored in CLI config. |
| **Admin token** | A global credential in the format `af_admin_<64 hex>` that grants unrestricted access. |

## Clarifications

1. **Error code type.** The `code` field in error responses is an integer (e.g. `409`), not a string.
2. **Self-update permission.** The `PUT /api/v1/users/:id` middleware checks whether the requesting user's ID matches `:id` — if so, `full_name` updates are permitted regardless of role. Only admins can change `status`.
3. **OAuth CSRF protection.** The CLI generates a cryptographically random `state` parameter and validates it on callback.
4. **Email required from OAuth provider.** If the identity provider returns a null or empty email, login fails with an error.
5. **Team member removal.** Deferred to a future iteration. Only adding members is supported.
6. **OAuth `redirect_uri` validation.** The hub validates `redirect_uri` against a configured allowlist (dev: `http://localhost:*`; production: derived from `[server] external_url`).
7. **Request body size limit.** 1 MB maximum. Requests exceeding this are rejected with HTTP 413.
8. **No pagination.** List endpoints return all results without pagination in this iteration.
9. **Workspace token expiry.** Tokens accept `expires` of 0 (indefinite), 30, 60, or 90 days. Default: 30 days.
10. **API key expiry.** Keys accept `expires` of 0 (indefinite), 30, 60, or 90 days. Default: 90 days.

## Design Decisions

1. **API keys are user-scoped, not team-scoped.** The original model required team context for key creation, but a user logging in for the first time has no team. Making keys user-scoped resolves this contradiction and simplifies the authentication model.
2. **Workspace tokens are a separate credential type.** Using a distinct format (`af_wt_...`) makes it trivial for the server to identify the credential type and apply the correct access rules. Workspace tokens always act on behalf of the creating user.
3. **Workspace tokens are read-only by default.** Since granular permissions haven't been designed yet, read-only is the safe default. Finer-grained access levels can be added in a future iteration without changing the token format.
4. **Workspace tokens are NOT stored in CLI config.** Workspace tokens are meant for external entities (tools, agents, CI systems) and may be stored in various secure locations (environment variables, secret managers, CI config). The CLI config only holds the user's own credentials.
5. **Teams are organizational only in this iteration.** Teams retain CRUD and lifecycle operations but have no permission implications. This keeps the first iteration simple while preserving the entity for future RBAC work.
6. **One active API key per user.** A new login replaces the existing key. This prevents key sprawl and simplifies the mental model.
7. **Auth callback generates a fresh key each login.** The previous key is revoked on re-login. This ensures the user always has a single, known credential.
8. **CLI uses persistent config at `$HOME/.af/config.toml`.** Three fields only: `hub_url`, `user_id`, `api_key`. Eliminates the need for flags on every command. Plaintext with `0600` permissions; keychain integration deferred.
9. **Admin token rotation via boot flag.** `--reset-admin-token` reuses first-boot token generation logic. Chosen over a runtime CLI command because rotation should work even when the service is stopped.
10. **Workspaces are user-owned, not admin-managed.** Any authenticated user (via API key) can create a workspace. Admin tokens cannot, since workspaces need a real user as owner.
11. **Workspace `git_url` not validated for reachability.** URL is stored as metadata for later use by sandbox provisioning. Network validation would add latency and fragility.
12. **Fresh schema rebuild for the rename.** Since the project is pre-production with no deployed users, the team rename is implemented by updating DDL directly — no ALTER TABLE or data migration.
13. **Config file atomic writes, no locking.** Last writer wins. Concurrent mutations are not a realistic scenario for a single-user interactive CLI.
14. **Blocked user handling at credential level.** API keys and workspace tokens are inert while user is blocked, functional again if unblocked. Credentials are not deleted on block.
