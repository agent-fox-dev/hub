---
spec_id: '01'
spec_name: workspaces
title: Workspaces
status: draft
created_at: '2026-07-20T16:18:52.342339+00:00'
updated_at: '2026-07-20T16:22:16.640547+00:00'
owner: ''
source: docs/prd.md
schema_version: 1
---
# af-hub Workspaces

## Intent

af-hub is the coordination hub for the agent-fox platform — the stateful process that owns workspaces on top of the identity, authentication, and organization infrastructure provided by apikit.

A workspace is the unit of work in the agent-fox platform: a git-repo-scoped execution context where specs are implemented, issues are fixed, and agents operate. af-hub manages workspace lifecycle and ownership. Delegated access to workspaces is handled through apikit's PATs with hub-registered permission scopes. Without this layer, no other platform capability — spec management, agent orchestration, sandbox provisioning — can assign work to a specific repo and branch with controlled access.

af-hub is built as an apikit application. All user identity, OAuth authentication, API key management, PATs, organization management, and base server infrastructure are provided by `github.com/txsvc/apikit`. This spec covers only the workspace management capabilities that extend that foundation.

## Goals

- Establish workspaces as the git-repo-scoped execution context where work is done, with clear user ownership and optional organization association.
- Register workspace-related permission scopes with apikit's permission registry so PATs can be scoped to workspace operations.
- Deliver workspace-specific CLI commands in `afc` for workspace registration and management.
- Enforce workspace-level access control that integrates with apikit's authentication middleware.

## Non-goals

- **Agent orchestration and spec-driven workflows.** af-hub provides workspace ownership only in this iteration; coordination, runtime, and agent lifecycle are separate platform layers built later.
- **Sandbox/OpenShell container provisioning.** Workspaces are metadata entities mapping to git repos; sandbox creation is future work.
- **Spec package storage or lifecycle within a workspace.** Future coordination-layer work.
- **Git branch management, cloning, or checkout.** Workspace `git_url` is stored as metadata, not validated for reachability.
- **Workspace lifecycle beyond creation, archiving, and deletion.** Workspace update (changing git_url, branch, org_id) is future work.
- **Campaign management, agent runs, or activity logs.**
- **Web UI.** Covered by a separate spec. The active `web_scaffold` spec establishes the frontend project scaffold only; workspace management pages are explicitly deferred to a future spec and will consume the API defined here when that spec is written.
- **Authentication, OAuth, user management, organization management, API key management, PAT management, admin bootstrap.** All provided by apikit.
- **Pagination for workspace listings.** Data volume is expected to be small in this iteration; pagination will be introduced in a future iteration when workspace counts grow.
- **Schema migration tooling.** Pre-production; schema changes are handled as DDL updates. No migration framework is introduced in this iteration.

## Functional Requirements

### Workspaces

A workspace is the context in which work is done: implementing a spec package, fixing a GitHub issue, or interactive agent work. Each workspace maps to one git repository.

- A workspace has: `slug` (globally unique), `git_url` (HTTPS or SSH format), optional `branch` (null means repo's default branch), `owner_id` (the creating user), optional `org_id` (organizational association via apikit organizations), and `status` (active/archived/deleted, default active).
- `branch`, when provided, must follow git ref naming rules: no ASCII control characters, no space, no `~`, `^`, `:`, `?`, `*`, `[`, `\`, no `..` sequences, no trailing `.lock`, no trailing dot, no leading dot in any path component, and must be non-empty.
- Slug format: lowercase alphanumeric + hyphens, 3–64 chars, starts with a letter, no trailing hyphen, no consecutive hyphens.
- The same `git_url` may appear in multiple workspaces with different slugs (e.g. one workspace per feature branch, or one per developer).
- `git_url` must be non-empty and match one of two formats: HTTPS (`https://<host>/<path>`) or SSH (`git@<host>:<path>`). Plain HTTP URLs are rejected. Both host and path segments must be non-empty. Format validation uses pattern matching only — the URL is not validated for DNS resolution or git protocol negotiation.
- Only users authenticated with a user API key (not admin token) can create a workspace. The creating user becomes the workspace owner.
- The workspace owner has full access to the workspace and its resources via their main API key.
- Admin tokens grant full access to any workspace but cannot create workspaces (workspaces require a real user as owner).
- When creating a workspace with an `org_id`, the server verifies the creating user is a member of that organization. If not, the request fails with HTTP 403.

#### Workspace lifecycle

| State | Meaning | Allowed transitions |
|-------|---------|---------------------|
| **Active** | Default state. Fully operational. | -> Archived |
| **Archived** | Read-only. All state preserved. Hidden from default listings. | -> Active (reactivate), -> Deleted |
| **Deleted** | Permanently removed. | Terminal |

- Only archived workspaces can be deleted. Attempting to delete an active workspace returns HTTP 400.
- Archiving preserves all state and is fully reversible.
- Deleting a workspace permanently removes it. This is irreversible. The database row is **physically deleted** — the row is removed from the `workspaces` table. The slug becomes available for reuse immediately.
- Only the workspace owner or admin can archive, reactivate, or delete a workspace.
- Requesting a transition that is not valid for the workspace's current state returns HTTP 400 with a descriptive error message. This applies to all invalid transitions including repeated ones (e.g., archiving an already-archived workspace, reactivating an already-active workspace). These are errors, not no-ops.

### Storage

Hub defines its own `workspaces` table using apikit's DB abstraction (`*apikit.DB`). The schema is applied via `CREATE TABLE IF NOT EXISTS` on boot, following the same pattern used by apikit internally. No migration tooling is used — pre-production schema changes are applied as DDL updates.

**Workspace table schema:**

```sql
CREATE TABLE IF NOT EXISTS workspaces (
    slug        TEXT PRIMARY KEY,
    git_url     TEXT NOT NULL,
    branch      TEXT,
    owner_id    TEXT NOT NULL,
    org_id      TEXT,
    status      TEXT NOT NULL DEFAULT 'active',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
```

- `slug` is the primary key (globally unique, enforces the 409 conflict response on duplicate).
- `branch` and `org_id` are nullable.
- `status` stores the string values `active` or `archived`. Deleted workspaces are physically removed; the `deleted` status value is never written to the table.
- `created_at` and `updated_at` are stored as RFC 3339 strings.

### Workspace permissions

Hub registers the following permission scopes with apikit's PAT permission registry via `apikit.Permission` in `MountHandlers`:

| Permission | Description |
|------------|-------------|
| `workspaces:read` | List and view workspaces the PAT owner has access to |
| `workspaces:create` | Create new workspaces |

PATs carrying these permissions can access workspace endpoints within the bounds of the owning user's access. A PAT with `workspaces:read` can read the same workspaces its owner could read with their API key.

### Workspace access control

| Credential | Workspace access |
|------------|-----------------|
| Admin token | Full access to all workspaces; cannot create workspaces |
| User API key | Full access to own workspaces; can create workspaces |
| PAT with `workspaces:read` | Read-only access to owner's workspaces |
| PAT with `workspaces:create` | Can create workspaces on behalf of owner and read own workspaces |

Permission matrix:

| Endpoint | Admin | Owner (API key) | PAT `workspaces:read` | PAT `workspaces:create` |
|----------|-------|-----------------|-----------------------|------------------------|
| Create workspace | no\* | yes | no | yes |
| List workspaces | yes (all) | yes (own) | yes (own) | yes (own) |
| Get workspace | yes | yes | yes (own) | yes (own) |
| Archive workspace | yes | yes | no | no |
| Reactivate workspace | yes | yes | no | no |
| Delete workspace | yes | yes | no | no |

\*Admin tokens cannot create workspaces — a real user must be the owner.

### API endpoints

All endpoints are mounted on apikit's API group (default `/api/v1`). Authentication is handled by apikit's auth middleware; hub adds workspace-specific authorization checks.

- `POST /api/v1/workspaces` — Create a workspace. Requires user API key or PAT with `workspaces:create`. Accepts `slug`, `git_url`, `branch` (optional), `org_id` (optional). Returns HTTP 201 with the workspace object. Returns HTTP 409 on duplicate slug. If `org_id` is provided, the creating user must be a member of that organization (HTTP 403 otherwise).
- `GET /api/v1/workspaces` — List workspaces. Admin: all workspaces. User (API key or PAT with `workspaces:read` or `workspaces:create`): own workspaces only. Archived workspaces are excluded by default; include with `?include_archived=true`. Returns an empty array when no workspaces match. Results are ordered by `created_at` descending. No pagination in this iteration; the result set is expected to remain small.
- `GET /api/v1/workspaces/:slug` — Get a workspace by slug. Requires workspace ownership, admin, or PAT with `workspaces:read` or `workspaces:create` scoped to the owning user.
- `POST /api/v1/workspaces/:slug/archive` — Archive a workspace. Requires workspace ownership or admin. Returns HTTP 200 with the updated workspace object (status: archived). Returns HTTP 400 if the workspace is already archived or is in the deleted state.
- `POST /api/v1/workspaces/:slug/reactivate` — Reactivate an archived workspace. Requires workspace ownership or admin. Returns HTTP 200 with the updated workspace object (status: active). Returns HTTP 400 if the workspace is not archived (i.e., already active or deleted).
- `DELETE /api/v1/workspaces/:slug` — Delete a workspace. Requires workspace ownership or admin. Returns HTTP 204 No Content with an empty body on success. Returns HTTP 400 if the workspace is not archived. Deletion is permanent, irreversible, and physically removes the row from the database. The slug becomes available for reuse.

### Response schema

A workspace object returned by all endpoints:

```json
{
  "slug": "string",
  "git_url": "string",
  "branch": "string | null",
  "owner_id": "string (UUID)",
  "org_id": "string (UUID) | null",
  "status": "active | archived",
  "created_at": "string (RFC 3339)",
  "updated_at": "string (RFC 3339)"
}
```

- `POST /api/v1/workspaces` returns a single workspace object (HTTP 201).
- `GET /api/v1/workspaces` returns `[workspace, ...]` (empty array when no workspaces match).
- `GET /api/v1/workspaces/:slug` returns a single workspace object (HTTP 200).
- `POST /api/v1/workspaces/:slug/archive` returns a single workspace object with `status: "archived"` (HTTP 200).
- `POST /api/v1/workspaces/:slug/reactivate` returns a single workspace object with `status: "active"` (HTTP 200).
- `DELETE /api/v1/workspaces/:slug` returns HTTP 204 No Content with an empty body.

### Error responses

All error responses use apikit's standard JSON envelope: `{"error": {"code": <HTTP_STATUS>, "message": "Human-readable description"}}`.

| Condition | HTTP Status |
|-----------|-------------|
| Missing or malformed request body | 400 |
| Slug validation failure (format, length, consecutive hyphens) | 400 |
| Invalid `git_url` format (not HTTPS or SSH, empty host/path) | 400 |
| Invalid `branch` format (does not follow git ref naming rules) | 400 |
| Referenced `org_id` does not exist | 400 |
| Delete or reactivate on workspace in wrong state | 400 |
| Archive on workspace already archived | 400 |
| Reactivate on workspace already active | 400 |
| Unauthenticated request (missing, invalid, or expired credential) | 401 |
| Insufficient permission (valid credential, wrong scope) | 403 |
| User not a member of the referenced organization | 403 |
| Workspace not found by slug | 404 |
| Duplicate slug | 409 |

When a workspace exists but the requester lacks access, return 404 (not 403) to prevent slug enumeration.

### CLI commands

`afc` is built on apikit's embeddable command tree. All apikit commands (login, user, keys, tokens, orgs, admin) are included via `apikit.RootCommand()`. Hub adds workspace-specific commands under `afc workspace`:

**Read/management commands:**
- `afc workspace create --git-url <url> --slug <slug> [--branch <ref>] [--org <org-slug>]` — Register a workspace. The `--org` flag accepts a slug; the CLI resolves it to a UUID before the API call (mechanism is left to the implementer's discretion — apikit SDK or a direct API call are both acceptable). On success, prints the workspace object as JSON.
- `afc workspace list [--include-archived]` — List the user's workspaces. The `--include-archived` flag passes `?include_archived=true` to the API, including archived workspaces in results. Prints JSON.
- `afc workspace get <slug>` — Get workspace details by slug. Prints JSON.

**Lifecycle commands:**
- `afc workspace archive <slug>` — Archive a workspace. On success, prints the updated workspace object as JSON.
- `afc workspace reactivate <slug>` — Reactivate an archived workspace. On success, prints the updated workspace object as JSON.
- `afc workspace delete <slug> --confirm` — Permanently delete an archived workspace. The `--confirm` flag is required; omitting it prints a usage hint to stderr and exits 1 without making an API call. On success, prints a confirmation message to stderr and exits 0.

All commands print JSON to stdout and human-readable messages to stderr. Exit code 0 on success, 1 on any error. When a required flag is missing, the command prints a usage hint to stderr and exits 1 without making an API call. When `--org` slug resolution fails (org not found), the command prints an error to stderr and exits 1.

## Technical Boundaries

- **Language:** Go (1.26+)
- **Foundation:** `github.com/txsvc/apikit` — provides server bootstrap, middleware, authentication, OAuth, user/org management, API keys, PATs, CLI framework, SDK, and SQLite storage.
- **Token prefix:** `af` — all credential types use the `af_` prefix to brand them as agent-fox platform credentials.
- **Health check:** Hub passes the database ping as its `HealthChecker` implementation for `apikit.NewServer`. This is already implemented in `cmd/af-hub/main.go`.

## Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/txsvc/apikit` | Server framework, authentication, user/org management, CLI framework, SDK |
| `github.com/labstack/echo/v4` | HTTP framework (via apikit, also used directly for hub route handlers) |

## Glossary

| Term | Definition |
|------|------------|
| **Workspace** | A git-repo-scoped execution context where work is done. Maps to one git repository and optional branch. Owned by a user, optionally associated with an organization. |
| **Slug** | A globally unique, URL-safe identifier for a workspace. Lowercase alphanumeric + hyphens, 3–64 chars, starts with a letter, no trailing hyphen, no consecutive hyphens. |
| **Organization** | An apikit-managed grouping of users. Workspaces can optionally be associated with an organization via `org_id`. |
| **PAT** | Personal Access Token. An apikit credential type (`af_pat_<token_id>_<secret>`) with fine-grained permission scopes. Hub extends the permission registry with workspace-specific scopes. |
| **apikit** | The foundation library (`github.com/txsvc/apikit`) that provides server bootstrap, authentication, OAuth, user management, organization management, API keys, PATs, and CLI infrastructure. |
| **Physical delete** | Removal of a database row entirely. Used for workspace deletion — the slug is freed for reuse and no audit record is retained. |

## Verified External API

### `github.com/txsvc/apikit` (v0.0.0, Go, local replace)

Failure modes for apikit functions are known to the implementation team and documented in apikit's own documentation. The table below captures verified happy-path signatures for reference.

| Symbol | Package | Signature | Notes |
|--------|---------|-----------|-------|
| `LoadConfig` | `apikit` | `func LoadConfig() (*Config, error)` | |
| `OpenDatabase` | `apikit` | `func OpenDatabase(path string) (*DB, error)` | |
| `Bootstrap` | `apikit` | `func Bootstrap(ctx context.Context, database *DB, opts BootstrapOptions) error` | |
| `NewServer` | `apikit` | `func NewServer(cfg *Config, checker HealthChecker) *Server` | |
| `Server.Start` | `apikit` | `func (s *Server) Start() error` | |
| `Server.APIGroup` | `apikit` | `func (s *Server) APIGroup() *echo.Group` | |
| `Server.MountHandlers` | `apikit` | `func (s *Server) MountHandlers(database *DB, permissions ...Permission) error` | apikit#33 |
| `Permission` | `apikit` | `type Permission struct { Resource, Action string }` | apikit#33 |
| `RootCommand` | `apikit` | `func RootCommand() *cobra.Command` | |
| `LoginCmd` | `apikit` | `func LoginCmd() *cobra.Command` | |
| `UserCmd` | `apikit` | `func UserCmd() *cobra.Command` | |
| `KeysCmd` | `apikit` | `func KeysCmd() *cobra.Command` | |
| `TokensCmd` | `apikit` | `func TokensCmd() *cobra.Command` | |
| `OrgsCmd` | `apikit` | `func OrgsCmd() *cobra.Command` | |
| `AdminCmd` | `apikit` | `func AdminCmd() *cobra.Command` | |
| `CLIExecute` | `apikit` | `func CLIExecute() error` | |
| `CLIPrintError` | `apikit` | `func CLIPrintError(err error)` | |
| `CLIExitCode` | `apikit` | `func CLIExitCode(err error) int` | |

## Design Decisions

1. **PATs replace workspace tokens.** apikit's permission-scoped PATs cover the delegation use case that workspace tokens were originally designed for. Hub registers `workspaces:read` and `workspaces:create` permissions via `apikit.Permission` in `MountHandlers` (apikit#33, merged). This eliminates a custom credential type, its management endpoints, and its CLI commands.
2. **PAT `workspaces:create` implies read access to own workspaces.** A tool that creates workspaces needs to verify its own creations. Denying read access to a create-only PAT would be a usability gap with no security benefit.
3. **Workspaces are user-owned, not admin-managed.** Any authenticated user (via API key) can create a workspace. Admin tokens cannot, since workspaces need a real user as owner.
4. **Workspace `git_url` not validated for reachability.** URL is stored as metadata for later use by sandbox provisioning. Network validation would add latency and fragility.
5. **Organizations replace teams.** The original hub PRD defined "teams" as a lightweight grouping. apikit provides full organization management. Hub uses apikit's organizations for workspace grouping (`org_id` on workspaces).
6. **404 over 403 for inaccessible workspaces.** When a workspace exists but the requester lacks access, the API returns 404 to prevent slug enumeration. This is a deliberate security-over-transparency tradeoff.
7. **Org membership required for workspace association.** When `org_id` is provided at creation, the user must be a member of that organization. This prevents users from associating workspaces with organizations they don't belong to.
8. **No pagination in this iteration.** The `GET /api/v1/workspaces` endpoint returns all matching workspaces without pagination. Data volume is expected to remain small. Pagination will be introduced in a future iteration when workspace counts grow to a scale that warrants it.
9. **Web scaffold decoupled from workspace API.** The active `web_scaffold` spec establishes a frontend project scaffold only; workspace management pages are explicitly deferred to a future spec. That future spec will declare a dependency on this workspace API. No cross-spec dependency is declared in either spec at this time.
10. **Physical delete for workspace removal.** Deleted workspaces are permanently removed from the database (row deleted). No audit log or soft-delete flag is retained. The slug is freed for reuse. This is consistent with the "permanent and irreversible" intent and avoids retaining stale data with no current consumer.
11. **Invalid lifecycle transitions are errors, not no-ops.** Requesting a transition that is not valid for the current state (including repeating a transition already applied) returns HTTP 400. This catches scripting mistakes early and makes CLI automation explicit about expected state.
12. **`afc workspace delete` requires `--confirm`.** Deletion is irreversible. A required confirmation flag prevents accidental permanent removal from the CLI without requiring an interactive prompt, keeping the command scriptable.
13. **Health check via database ping.** Hub implements `HealthChecker` using a database ping. This is sufficient for a single-node SQLite deployment and is already in place in `cmd/af-hub/main.go`.
14. **Org slug resolution mechanism left to implementer.** The `--org` flag accepts an org slug and the CLI resolves it to a UUID before the API call. Whether this uses the apikit SDK or a direct API call is left to implementer discretion — both approaches are acceptable and the choice does not affect the API contract.
