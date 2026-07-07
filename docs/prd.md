# af-hub — Product Requirements Document

af-hub is the central API backend for the multi-tenant agent-fox platform. It provides user authentication, authorization, and workspace management.

A **Workspace** is the top-level organizational unit — comparable to a GitHub Organization. Users are members of one or more workspaces, and each workspace will eventually contain one or more repositories. In this first iteration, the workspace is the tenancy boundary; repository-level scoping within a workspace is a future concern.

## Spec pack index

The work described in this PRD is built from scratch across eight spec packs. Each builds a vertical slice of the system.

| Pack | Name | Scope | Depends on |
|------|------|-------|------------|
| **01** | Backend foundation | Go project layout (`cmd/af-hub`, `cmd/afc`, `internal/`). Config loading (`config.toml`). SQLite schema (all tables with workspace naming). Store layer (CRUD for all entities). Admin bootstrap flow. Health probes. Structured logging. Graceful shutdown. **Docs:** `README.md`, `docs/architecture.md`, `docs/configuration.md`. | — |
| **02** | Auth, RBAC & API endpoints | Pluggable OAuth provider registry (common interface for authorize, token exchange, user info) with GitHub as the first provider. Auth middleware (admin token + API key). RBAC enforcement. All HTTP handlers and route registration (users, workspaces, workspace members, API keys). Standardized error response envelope. **Docs:** `docs/api.md`. | 01 |
| **03** | Session auth & CLI | `sessions` table. `POST /api/v1/auth/session` (issue session tokens for web UI). `GET /api/v1/auth/me` (current user from any token type). Extend auth middleware to accept session tokens. `afc` CLI binary: `login` command (OAuth flow), `keys` subcommands (create/list/refresh/revoke). **Docs:** `docs/cli.md`, update `docs/api.md`. | 02 |
| **04** | Web UI scaffold | Initialize the `web/` project: Vite + React + TypeScript + Tailwind + shadcn/ui. Configure dev proxy to the Go backend. Add `make web-dev` and `make web-build` targets. No pages yet — just the toolchain and a "Hello world" route. **Docs:** `docs/web-ui.md`, update `README.md` and `docs/architecture.md`. | 01 |
| **05** | Public pages (landing + login) | Landing page with OAuth provider buttons. Login flow: browser redirect → provider → callback → session token. React Router setup with public/authenticated route split. | 03, 04 |
| **06** | Authenticated shell + dashboard | Sidebar layout (nav rail, workspace list, user avatar). Auth guard redirecting unauthenticated users. Dashboard placeholder with activity feed empty state, feature discovery cards, changelog section. | 05 |
| **07** | User settings + profile | Settings modal overlay (two-pane layout). General profile view/edit. Workspace membership list (read-only). | 06 |
| **08** | API key management UI | API key management within the settings modal: list all keys grouped by workspace, create new keys (workspace selector, label, expiry), refresh and revoke existing keys. Confirmation dialogs for destructive actions. | 07 |

The backend (01 → 02 → 03) and frontend scaffold (04) are independent tracks that can be worked in parallel. They converge at pack 05. Packs 05–08 are sequential.

```
01 ─→ 02 ─→ 03 ─→ 05 ─→ 06 ─→ 07 ─→ 08
 └────→ 04 ───┘
```

## Architecture overview

af-hub is a two-binary Go application:

- **af-hub** — the API server (Echo framework, embedded SQLite, structured JSON logging)
- **afc** — the CLI client (Cobra, OAuth login, API key management)

The project follows standard Go layout: `cmd/` for entry points, `internal/` for private packages. SQLite uses the pure-Go driver (`modernc.org/sqlite`) — no CGo required.

## Core requirements

### Authentication and authorization

The first priority — even before utility features — is a complete auth stack:

- User authentication via a pluggable OAuth provider registry. The first iteration ships with **GitHub** only; Google, GitLab, and Keycloak will be added later via the same provider interface.
- CLI-driven OAuth sign-up and login flow
- Admin token bootstrap for initial access
- API key management for programmatic access
- Role-based access control (RBAC) on all protected endpoints

### Multi-tenancy

Resources are scoped to **workspaces**. Users are assigned roles per workspace via a membership table. API keys are scoped to a specific user + workspace pair.

## Data model

### Users

| Column | Type | Constraints |
|--------|------|-------------|
| id | TEXT | PRIMARY KEY (UUID) |
| username | TEXT | UNIQUE, NOT NULL |
| email | TEXT | |
| full_name | TEXT | |
| provider | TEXT | NOT NULL |
| provider_id | TEXT | NOT NULL |
| status | TEXT | DEFAULT 'active' |
| created_at | TEXT | |
| updated_at | TEXT | |

Unique constraint on `(provider, provider_id)`.

### Workspaces

| Column | Type | Constraints |
|--------|------|-------------|
| id | TEXT | PRIMARY KEY (UUID) |
| name | TEXT | NOT NULL |
| slug | TEXT | UNIQUE, NOT NULL |
| url | TEXT | UNIQUE, NOT NULL (validated URL with scheme + host) |
| created_at | TEXT | |
| created_by | TEXT | FK → users |

### Workspace members

| Column | Type | Constraints |
|--------|------|-------------|
| user_id | TEXT | FK → users |
| workspace_id | TEXT | FK → workspaces |
| role | TEXT | NOT NULL |
| created_at | TEXT | |
| granted_by | TEXT | FK → users |

Primary key: `(user_id, workspace_id)`.

### API keys

| Column | Type | Constraints |
|--------|------|-------------|
| id | TEXT | PRIMARY KEY (UUID) |
| key_id | TEXT | UNIQUE (lookup portion) |
| key_hash | TEXT | SHA-256 hash of secret |
| user_id | TEXT | FK → users |
| workspace_id | TEXT | FK → workspaces |
| label | TEXT | |
| expires_at | TEXT | nullable |
| revoked_at | TEXT | nullable |
| created_at | TEXT | |

### Sessions

| Column | Type | Constraints |
|--------|------|-------------|
| id | TEXT | PRIMARY KEY (UUID) |
| token_hash | TEXT | SHA-256 hash of session token |
| user_id | TEXT | FK → users |
| expires_at | TEXT | NOT NULL |
| created_at | TEXT | |

Session tokens use the format `af_session_<64 hex chars>`.

### Admin tokens

| Column | Type | Constraints |
|--------|------|-------------|
| id | TEXT | PRIMARY KEY |
| token_hash | TEXT | SHA-256 |
| created_at | TEXT | |

## Configuration

The server loads `config.toml` from the current directory (TOML format).

```toml
[server]
port = 8080                # 1–65535, default 8080
bind_address = "0.0.0.0"   # default "0.0.0.0"

[database]
path = "./data/af-hub.db"  # default "./data/af-hub.db"

[logging]
level = "info"              # trace/debug/info/warn/error/fatal/panic

[[auth.oauth]]
provider = "github"            # first iteration: GitHub only
client_id = "..."
client_secret = "..."
# authorize_url, token_url, userinfo_url — optional for providers with
# well-known URLs (e.g. GitHub); required for custom providers.
#
# The OAuth registry is pluggable: each provider implements a common
# interface (authorize URL, token exchange, user info extraction).
# Adding a new provider means registering it in the registry with its
# URLs and field mappings — no changes to auth middleware or handlers.
```

### Environment variables

| Variable | Purpose |
|----------|---------|
| `AF_HUB_ADMIN_TOKEN` | Required on subsequent boots (validated against stored hash) |
| `AF_HUB_URL` | Default hub URL for CLI commands |
| `AF_HUB_API_KEY` | Default API key for CLI key-management commands |

## First boot (admin bootstrap)

On first boot (zero users in the database), the server automatically:

1. Creates an admin user (`username: admin`, `email: admin@localhost`, `provider: local`).
2. Generates a cryptographically random admin token in the format `af_admin_<64 hex chars>`.
3. Stores the SHA-256 hash of the token in the `admin_tokens` table.
4. Writes the plaintext token to an `admin_token` file (mode 0600) next to `config.toml`.
5. Logs the file path. The operator must save this token — it is the only credential with global admin access.

On subsequent boots, the server reads `AF_HUB_ADMIN_TOKEN` from the environment, hashes it, and compares against the stored hash. The server refuses to start if the token is missing or does not match.

## Authentication

All `/api/v1/*` endpoints (except `/api/v1/auth/*`) require a Bearer token in the `Authorization` header. Two token types are accepted:

**Admin token** — the bootstrap token (`af_admin_...`), granting global admin access to all endpoints and workspaces.

**API key** — opaque format `af_<key_id>_<secret>`:
- `key_id`: random 8-character alphanumeric identifier used for database lookup (no relationship to user or workspace data).
- `secret`: random 32-character alphanumeric string; only the SHA-256 hash is stored.
- Keys are scoped to a specific user and workspace. The user's role in that workspace determines permissions.
- Revoked keys and expired keys are rejected. Blocked users are rejected with HTTP 403.

### OAuth flow

The CLI-driven OAuth flow works as follows:

1. `afc login --provider github` fetches the provider list from the hub.
2. Opens the authorization URL in the user's browser.
3. Starts a local HTTP callback server on a random port.
4. Captures the authorization code, exchanges it with the hub via `POST /api/v1/auth/callback`.
5. The hub exchanges the code with the identity provider, retrieves user info, and upserts the user (creates if new, updates username/email if existing; does **not** re-activate blocked users).
6. Returns the user object to the CLI.

## Roles and permissions

Three roles are implemented:

| Role | Scope | Description |
|------|-------|-------------|
| **admin** | Global | Full access to all endpoints and all workspaces |
| **editor** | Per-workspace | Read/write on resources within assigned workspaces |
| **reader** | Per-workspace | Read-only access within assigned workspaces |

### Permission matrix

| Endpoint | Admin | Editor | Reader |
|----------|-------|--------|--------|
| `POST /api/v1/keys` (create key, per workspace) | yes | yes | no |
| `GET /api/v1/keys` (list keys) | yes | yes | yes |
| `POST /api/v1/keys/:key_id/refresh` | yes | yes | no |
| `DELETE /api/v1/keys/:key_id` (revoke) | yes | yes | no |
| `POST /api/v1/users` (create user) | yes | no | no |
| `GET /api/v1/users` (list users) | yes | no | no |
| `GET /api/v1/users/:id` (get user) | yes | no | no |
| `PUT /api/v1/users/:id` (update user) | yes | no | no |
| `POST /api/v1/workspaces` (create workspace) | yes | no | no |
| `GET /api/v1/workspaces` (list workspaces) | yes | no | no |
| `POST /api/v1/workspaces/:id/members` (add member) | yes | no | no |
| `GET /api/v1/workspaces/:id/members` (list members) | yes | no | no |

## CLI commands

The CLI binary is `afc`, built with Cobra.

### Login

```
afc login --provider <provider> [--hub-url URL]   # first iteration: github only
```

Runs the OAuth authorization code flow described above. The `--hub-url` flag (or `AF_HUB_URL` env var) specifies the hub server.

### Key management

All key commands accept `--hub-url` (or `AF_HUB_URL`) and `--api-key` (or `AF_HUB_API_KEY`).

```
afc keys create  --workspace <workspace-id> [--expires 0|30|60|90]   # default 30 days
afc keys list
afc keys refresh <key-id>
afc keys revoke  <key-id>
```

## API endpoints

### Health probes (public)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/healthz` | Liveness probe — always returns 200 |
| GET | `/readyz` | Readiness probe — pings the database, returns 200 or 503 |

### OAuth (public)

**GET /api/v1/auth/providers** — List configured OAuth providers (no secrets exposed).

```json
[{"name": "github", "authorize_url": "https://github.com/login/oauth/authorize"}]
```

**POST /api/v1/auth/callback** — Exchange an OAuth authorization code for a user record.

```json
{"provider": "github", "code": "abc123", "redirect_uri": "http://localhost:9999/callback"}
```

Returns HTTP 200 with the user object. Creates or updates the user as needed.

**POST /api/v1/auth/session** — Issue a short-lived session token for web UI use. Called by the SPA after a successful OAuth callback.

```json
{"provider": "github", "code": "abc123", "redirect_uri": "http://localhost:3000/callback"}
```

Returns HTTP 200 with the session token and user object:

```json
{"token": "...", "expires_at": "2025-01-16T10:30:00Z", "user": {...}}
```

**GET /api/v1/auth/me** — Return the current authenticated user (from session token, admin token, or API key). Used by the SPA on page load to restore the auth state without re-authenticating.

```json
{"id": "...", "username": "alice", "email": "alice@example.com", "workspaces": [{"id": "...", "name": "...", "role": "editor"}]}
```

### User management (admin only)

**POST /api/v1/users** — Create a user.

```json
{"username": "alice", "email": "alice@example.com", "provider": "github", "provider_id": "12345"}
```

Returns HTTP 201 with the created user object.

**GET /api/v1/users** — List all users (HTTP 200, JSON array).

**GET /api/v1/users/:id** — Get a user by ID, including workspace memberships and roles.

**PUT /api/v1/users/:id** — Update a user's `full_name` or `status` (`active` | `blocked`).

```json
{"status": "blocked"}
```

### Workspace management (admin only)

**POST /api/v1/workspaces** — Create a workspace.

```json
{"name": "My Workspace", "slug": "my-workspace", "url": "https://github.com/my-org"}
```

**GET /api/v1/workspaces** — List all workspaces.

**POST /api/v1/workspaces/:id/members** — Add or update a user's role in a workspace.

```json
{"user_id": "...", "role": "editor"}
```

**GET /api/v1/workspaces/:id/members** — List all members of a workspace.

### API key management (authenticated)

**POST /api/v1/keys** — Create an API key for the authenticated user, scoped to a specific workspace. The user must be a member of the workspace, and the key inherits the user's role in that workspace.

```json
{"workspace_id": "...", "label": "ci-key", "expires": 30}
```

Returns the full key (including plaintext secret) exactly once.

**GET /api/v1/keys** — List all keys for the authenticated user (across all workspaces).

**POST /api/v1/keys/:key_id/refresh** — Generate a new secret for an existing key (same key_id).

**DELETE /api/v1/keys/:key_id** — Permanently revoke a key.

## Web UI

### Tech stack

| Layer | Choice | Rationale |
|-------|--------|-----------|
| Language | TypeScript | Type safety across the frontend |
| Framework | React | Largest ecosystem, long-term maintainability |
| Build tool | Vite | Fast dev server, hot reload, zero-config for React/TS |
| Styling | Tailwind CSS | Utility-first, pairs with shadcn/ui |
| Components | shadcn/ui | Copied into the tree (not a dependency). Polished tables, forms, dialogs built on Radix primitives |
| API state | TanStack Query (React Query) | Handles caching, refetching, loading/error states for all API calls |
| Routing | React Router | Client-side routing for the SPA |

The web UI lives in `web/` at the repo root with its own `package.json`, cleanly separated from the Go backend. In production, `vite build` produces static assets that can be served by af-hub directly, from a CDN, or from any static file server.

### Layout

The authenticated UI uses a persistent shell inspired by GitHub Copilot's desktop layout (see `hack/screen1.png`, `hack/screen2.png` for reference):

- **Left sidebar** — fixed navigation rail with: Home, primary nav items, and a workspace list showing the user's workspace memberships. User avatar and a settings gear icon anchored at the bottom.
- **Main content area** — fills the remaining width. Content changes based on the current route.

#### Landing page (`/`)

Public page shown to unauthenticated visitors. No sidebar. Describes the platform and presents a "Sign in with GitHub" button (the provider list is dynamic from the API, so additional providers appear automatically as they are configured). Redirects to the dashboard if the user is already authenticated.

#### Login (`/login`)

OAuth login flow. No sidebar. Displays the available identity providers (fetched from `GET /api/v1/auth/providers` — GitHub only in the first iteration). Clicking a provider initiates the OAuth authorization code flow via a browser redirect. On successful callback, the user session is established and the user is redirected to the dashboard. The login page renders provider buttons dynamically, so adding a new provider to `config.toml` requires no frontend changes.

#### Dashboard (`/dashboard`)

The authenticated home page within the sidebar shell. In this first iteration, the main content area contains:

- **Activity feed** — "Up next" style section showing recent activity across the user's workspaces (placeholder for future functionality). Empty state: "You're all caught up."
- **Feature discovery cards** — onboarding tiles introducing key platform capabilities (e.g., "Connect a workspace", "Create an API key"). Links to docs or triggers the relevant action.
- **What's new** — changelog section for platform updates.

The dashboard is intentionally a placeholder shell — real functionality will fill these sections in future iterations.

#### User settings / profile (`/settings`)

Opens as a modal overlay on top of the current page. Two-pane layout:

- **Left pane**: user identity block (name, handle) at the top, followed by a categorized settings navigation — General, Account, Themes/Appearance. Below the global settings, a "Workspaces" section lists the user's workspaces for per-workspace configuration.
- **Right pane**: context-sensitive content for the selected category. Form fields, toggles, and text areas as appropriate.

Settings categories for the first iteration:

- **General**: full name (editable), email (read-only), OAuth provider (read-only), account status, member since.
- **API keys**: list all keys (grouped by workspace), create new keys, refresh or revoke existing keys. Each key shows its label, workspace, expiry, and creation date.
- **Workspaces**: read-only list of workspace memberships with the user's role in each.

### Authentication in the web UI

The web UI uses the same OAuth providers as the CLI but with a browser-native redirect flow:

1. The user clicks a provider button on the landing or login page.
2. The browser redirects to the provider's authorization URL.
3. On callback, the SPA sends the authorization code to `POST /api/v1/auth/callback`.
4. The hub returns a **session token** (a short-lived opaque bearer token) alongside the user object.
5. The SPA stores the session token in memory (not localStorage — cleared on tab close for security). It is sent as a `Bearer` token in the `Authorization` header on all subsequent API requests.

The session token is distinct from API keys — it is short-lived (24 hours), non-renewable, and scoped to the web session. When it expires, the user is redirected to the login page to re-authenticate. This requires a new server endpoint:

**POST /api/v1/auth/session** — Issue a session token after a successful OAuth callback. Returns `{"token": "...", "expires_at": "..."}`. The token is a cryptographically random opaque string; the server stores its SHA-256 hash with an expiry timestamp.

### Development environment

- `npm run dev` — starts the Vite dev server with hot reload (proxies API requests to the Go backend)
- `npm run build` — production build to `web/dist/`
- `npm run lint` — ESLint + TypeScript type checking
- `npm run preview` — preview the production build locally

## Error responses

All API errors use a consistent JSON envelope:

```json
{"error": {"code": "<HTTP_STATUS>", "message": "Human-readable description"}}
```

| Status | Meaning |
|--------|---------|
| 400 | Bad request — malformed JSON, missing required fields, validation failure |
| 401 | Unauthorized — missing, invalid, or expired token |
| 403 | Forbidden — valid token but insufficient role, or user is blocked |
| 404 | Not found — resource does not exist |
| 409 | Conflict — unique constraint violation (duplicate username, slug, etc.) |
| 413 | Payload too large — request body exceeds limit |
| 500 | Internal server error |

## Operational details

- **Storage**: Embedded SQLite with WAL mode for concurrent write safety.
- **Logging**: Structured JSON logging via logrus. Every request is logged with method, path, status, and duration.
- **Graceful shutdown**: On SIGTERM/SIGINT with a 15-second drain timeout.
- **Health probes**: Kubernetes-compatible at `/healthz` and `/readyz`.
- **Build**: `make build` compiles both binaries to `bin/`. `make test` runs all tests. `make lint` runs `go vet`.

## Documentation

Documentation is a first-class deliverable, not an afterthought. Each spec pack must ship its documentation alongside the code it introduces.

### Documents

| Document | Location | Introduced in | Description |
|----------|----------|---------------|-------------|
| **README.md** | `/README.md` | Pack 01 | Project overview, prerequisites, quickstart (build, configure, run, first boot), project structure, link to other docs. Updated by every subsequent pack as new capabilities land. |
| **Architecture** | `/docs/architecture.md` | Pack 01 | System architecture: two-binary design, project layout, SQLite storage, config loading, request lifecycle. Updated when the web UI is added (pack 04). |
| **API reference** | `/docs/api.md` | Pack 02 | Complete REST API documentation: every endpoint with method, path, auth requirements, request/response bodies, status codes, and error format. Updated by pack 03 (session endpoints). |
| **CLI reference** | `/docs/cli.md` | Pack 03 | `afc` usage: all commands and subcommands, flags, environment variables, examples. |
| **Configuration** | `/docs/configuration.md` | Pack 01 | `config.toml` reference: all fields, defaults, validation rules, environment variable overrides. |
| **Web UI development** | `/docs/web-ui.md` | Pack 04 | Frontend development guide: setup, dev server, build, project structure, component conventions. |

### Principles

- **Write docs with the code.** A spec pack is not complete until its documentation is written or updated.
- **README is the entry point.** A new contributor should be able to clone, build, and run the system by following the README alone.
- **API docs are the contract.** Every endpoint is documented with request/response examples before the web UI consumes it.
- **Keep docs next to the code.** All documentation lives in the repo under `/docs/` (except `README.md` at the root). No external wikis or separate doc sites for the first iteration.

## Design Decisions

These decisions were made during requirements analysis to resolve ambiguities and underspecification in the original PRD.

1. **Sessions table schema**: `sessions(id TEXT PK UUID, token_hash TEXT, user_id TEXT FK→users, expires_at TEXT NOT NULL, created_at TEXT)`. Mirrors the admin_tokens pattern with expiry and user scoping.
2. **Session token format**: `af_session_<64 hex chars>` — follows the existing `af_admin_` prefix convention for token type identification in auth middleware.
3. **OAuth callback redirect URI**: Dev uses `http://localhost:5173/callback` (Vite default port). Production derives from a `[server] external_url` config field. The Go server validates redirect_uri against configured origins.
4. **API key `expires: 0`**: Means "no expiry" (never expires). The `expires_at` column is nullable, which aligns.
5. **User self-update**: `PUT /api/v1/users/:id` stays admin-only for `status` changes but allows any authenticated user to update their own `full_name`.
6. **Workspace URL validation**: Both `http://` and `https://` schemes allowed. Must have scheme + host at minimum.
7. **Slug validation rules**: Lowercase alphanumeric + hyphens, 3–64 characters, must start with a letter, must not end with a hyphen.
8. **API key list for admins**: Admin token lists ALL keys across all users. API key auth lists only the authenticated user's keys.
9. **Rate limiting**: Out of scope for the first iteration.
10. **CORS configuration**: Vite dev proxy handles CORS in development. No CORS middleware in Go — production serves static assets from same origin.
11. **Database migrations**: Schema applied on boot via `CREATE TABLE IF NOT EXISTS`. No migration tool for the first iteration.
12. **OAuth redirect URI registration**: Documented as a setup prerequisite — operator must register the callback URL with the OAuth provider.

## Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/labstack/echo/v4` | HTTP framework |
| `github.com/spf13/cobra` | CLI framework |
| `github.com/BurntSushi/toml` | Config file parsing |
| `github.com/sirupsen/logrus` | Structured logging |
| `github.com/google/uuid` | UUID generation |
| `modernc.org/sqlite` | Pure-Go SQLite driver |

### Web UI (web/)

| Package | Purpose |
|---------|---------|
| React | UI framework |
| TypeScript | Type-safe JavaScript |
| Vite | Build tool and dev server |
| Tailwind CSS | Utility-first CSS |
| shadcn/ui + Radix UI | Component primitives |
| TanStack Query | API state management |
| React Router | Client-side routing |
