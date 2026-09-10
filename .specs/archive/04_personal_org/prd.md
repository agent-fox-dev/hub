---
spec_id: '04'
spec_name: personal_org
title: Personal Org
status: draft
created_at: '2026-07-27T12:28:13.625065+00:00'
updated_at: '2026-07-27T12:28:13.625065+00:00'
owner: ''
source: docs/prd/prd3.md
schema_version: 1
---
# Personal Organization Auto-Creation

## Intent

When a new user logs in for the first time (`afc login`) or when an admin creates a new user (`afc admin users create`), the system automatically creates a personal organization for that user. The personal organization is named after the user's username, and the user is its owner and sole member.

This ensures that every workspace gets a hierarchical namespace `<username>/<workspace_slug>` by guaranteeing that (1) every user has a personal org, and (2) every workspace is associated with an org.

## Goals

- Automatically create a personal organization when a new user is created, in both the OAuth login flow and the admin user creation flow.
- Introduce organization ownership via an `owner_id` column on the `orgs` table, so personal orgs can be identified and cannot be accidentally deleted or reassigned.
- Auto-associate workspaces with the creator's personal org when no `--org` is specified, ensuring all workspaces live within an org namespace.
- Add a hook mechanism to apikit so hub can inject post-user-creation logic without modifying apikit's handlers directly.

## Non-goals

- **Changing workspace slug format or uniqueness.** Workspace slugs remain globally unique. The `<username>/<workspace_slug>` namespace is a logical grouping via org association, not a change to slug uniqueness constraints.
- **Changing existing org admin management.** Admin-created orgs continue to work as before. The `owner_id` column is nullable for backward compatibility with existing orgs.
- **Modifying existing permission scopes.** `workspaces:read`, `workspaces:create`, `workspaces:write`, `workspaces:delete` are unchanged.
- **Allowing users to delete or rename their personal org.** Personal org lifecycle management is future work.
- **Org-scoped workspace slugs.** Slugs remain globally unique; two users cannot have workspaces with the same slug even if they are in different orgs.

## Functional Requirements

### Hook mechanism in apikit

apikit exposes a callback type and a registration method on `Server` so that downstream applications (hub) can inject logic that runs after a new user is created.

- **Callback type:** `AfterUserCreateFunc func(ctx context.Context, tx *sql.Tx, userID, username, email string) error`
  - Receives the transaction (`*sql.Tx`) from the enclosing user-creation operation so that the hook's side effects are atomic with the user insert.
  - Receives the new user's `id`, `username`, and `email`.
  - If the hook returns a non-nil error, the enclosing transaction rolls back and the user creation fails. The error is propagated as an HTTP 500 to the caller.

- **Registration:** `func (s *Server) OnAfterUserCreate(fn AfterUserCreateFunc)` on the apikit `Server` type. The registered function is stored on the server and called from:
  1. The OAuth callback handler (`handleCallback`) — in the new-user branch, after the user row is inserted but before the transaction commits.
  2. The admin user creation handler (`createUser`) — after the user row is inserted. This handler currently does not use a transaction; it must be wrapped in one so the hook runs atomically.

- Only one hook can be registered. Calling `OnAfterUserCreate` a second time replaces the previous hook. If no hook is registered, user creation proceeds without calling any hook (backward-compatible).

- The hook must be registered before `MountHandlers` is called, since `MountHandlers` wires up the OAuth and user handlers that call the hook.

### Organization ownership

The `orgs` table in apikit gains an `owner_id` column:

```sql
CREATE TABLE IF NOT EXISTS orgs (
    id         TEXT NOT NULL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    slug       TEXT NOT NULL UNIQUE,
    url        TEXT,
    owner_id   TEXT,
    status     TEXT NOT NULL DEFAULT 'active',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
```

- `owner_id` is nullable. Existing admin-created orgs have no owner (`NULL`). Personal orgs created by the hook always have `owner_id` set to the user's ID.
- The `owner_id` column has no foreign key constraint (to avoid circular dependency issues during bootstrap and physical deletes), but the application treats it as a reference to `users.id`.
- The `Organization` SDK type (`apikit.Organization`) gains an `OwnerID` field: `OwnerID *string json:"owner_id,omitempty"`.
- Existing org API endpoints (create, list, get, update) include `owner_id` in responses. The admin org creation endpoint (`POST /orgs`) accepts an optional `owner_id` field.

### Personal org auto-creation (hub hook implementation)

Hub registers an `AfterUserCreateFunc` hook with apikit that creates a personal organization for every new user. The hook runs inside the user-creation transaction.

The hook performs these steps:

1. **Sanitize the username into an org slug.** Apply GitHub-style repository naming conventions, aligned with apikit's `validateSlug` pattern (`^[a-z0-9][a-z0-9_-]*[a-z0-9]$`):
   - Lowercase the entire string.
   - Replace any character that is not a lowercase letter, digit, hyphen, or underscore with a hyphen.
   - Collapse consecutive hyphens into a single hyphen.
   - Trim leading and trailing hyphens and underscores.
   - If the result starts with a digit, prepend `u-` (org slugs must start with a letter per apikit's `validateSlug`).
   - If the result is shorter than 2 characters after sanitization, use `u-<first 8 chars of userID>` as a fallback (the regex requires at least 2 characters for the general case).

2. **Handle slug collisions.** Query `orgs` for the candidate slug. If it already exists, append `-1`, `-2`, etc. until a unique slug is found. Cap at 10 attempts; if all collide, return an error (failing the user creation).

3. **Insert the org row.** Fields:
   - `id`: new UUID
   - `name`: the original username (not sanitized)
   - `slug`: the sanitized (and possibly suffixed) slug
   - `url`: empty string
   - `owner_id`: the new user's ID
   - `status`: `active`
   - `created_at`, `updated_at`: current time (RFC 3339)

4. **Insert the org_members row.** Link the new user as a member of the new org.

All operations use the `*sql.Tx` passed to the hook, so they are atomic with the user creation. If any step fails, the entire transaction (including user creation) rolls back.

### Workspace auto-association with personal org

When a workspace is created via `POST /api/v1/workspaces` and the request body does not include an `org_id` (or `org_id` is null), the server automatically sets `org_id` to the creating user's personal org.

- The personal org is identified by querying `SELECT id FROM orgs WHERE owner_id = ?` with the authenticated user's ID.
- If the user has no personal org (edge case — should not happen after this feature ships, but could occur for users created before the hook was deployed), the server returns HTTP 400 with the message `"user has no personal organization; contact an administrator"`.
- When `org_id` is explicitly provided in the request, the existing org membership check applies (unchanged behavior).
- The `--org` flag in `afc workspace create` remains optional. When omitted, the CLI does not send `org_id` in the request body, and the server defaults to the personal org.

### CLI changes

No new CLI commands are added. Existing commands behave as before with one implicit change:

- `afc workspace create --git-url <url> --slug <slug>` — When `--org` is omitted, the workspace is automatically associated with the user's personal org (server-side default). The CLI output (JSON) shows the populated `org_id`.

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 01_workspaces | 8 | 1 | Requires fully implemented workspace infrastructure |
| 03_workspace_write_delete | 6 | 1 | Requires update endpoint and new fields implemented |

## Technical Boundaries

- **Language:** Go (1.26+)
- **Repos affected:** Both `apikit` (hook mechanism, `owner_id` column, SDK type update) and `hub` (hook implementation, workspace auto-association).
- **Foundation:** `github.com/txsvc/apikit` — local replace at `../apikit`.
- **Schema migration:** Pre-production; schema changes are applied as DDL updates (no migration framework).

## Verified External API

### `github.com/txsvc/apikit` (v0.0.0, Go, local replace at `../apikit`)

| Symbol | Package | Signature | Notes |
|--------|---------|-----------|-------|
| `Server` | `apikit` | `type Server struct { ... }` | Unexported fields; extended with hook field |
| `NewServer` | `apikit` | `func NewServer(cfg *Config, checker HealthChecker) *Server` | |
| `Server.MountHandlers` | `apikit` | `func (s *Server) MountHandlers(database *DB, permissions ...Permission) error` | Wires OAuth, auth middleware, handlers |
| `Server.APIGroup` | `apikit` | `func (s *Server) APIGroup() *echo.Group` | |
| `Server.OnAfterUserCreate` | `apikit` | — | **NOT FOUND.** Must be added by this spec. |
| `AfterUserCreateFunc` | `apikit` | — | **NOT FOUND.** Must be added by this spec. |
| `Organization` | `apikit` | `type Organization struct { ID, Name, Slug, URL, Status string; ... }` | No `OwnerID` field yet; must be added. |
| `RegisterOAuthHandlers` | `oauth` (internal) | `func RegisterOAuthHandlers(group *echo.Group, registry *Registry, database *db.DB, externalURL string)` | Mounts `/auth/callback`; must receive hook reference |
| `handleCallback` | `oauth` (internal) | closure in `RegisterOAuthHandlers` | New-user branch at line 182-226; hook call inserted after INSERT |
| `createUser` | `handlers` (internal) | `func (h *userHandlers) createUser(c echo.Context) error` | No transaction today; must be wrapped in `WithTx` |
| `orgs` table | `db` (internal) | `CREATE TABLE IF NOT EXISTS orgs (id, name, slug, url, status, created_at, updated_at)` | No `owner_id` column yet; must be added |
| `org_members` table | `db` (internal) | `CREATE TABLE IF NOT EXISTS org_members (org_id, user_id, created_at)` | Used by hook to add membership |
| `validateSlug` | `handlers` (internal) | `func validateSlug(slug string) error` | Pattern: `^[a-z0-9][a-z0-9_-]*[a-z0-9]$`, 1-128 chars |

### `github.com/agent-fox-dev/hub` (this repo)

| Symbol | Package | Signature | Notes |
|--------|---------|-----------|-------|
| `MountWorkspaceHandlers` | `workspace` | `func MountWorkspaceHandlers(s *apikit.Server, db *apikit.DB) error` | Calls `s.MountHandlers`; hook must be registered before this call |
| `handleCreateWorkspace` | `workspace` | closure | Lines 160-166: org_id validation; must add auto-default logic |
| `checkOrgMembership` | `workspace` | `func checkOrgMembership(db *sql.DB, userID, orgID string) (int, string)` | Reused for explicit org_id validation |

## Design Decisions

1. **Hook mechanism in apikit, registered from hub (option b).** Rather than modifying apikit's OAuth and user-creation handlers directly, apikit exposes a generic `AfterUserCreateFunc` callback. Hub registers the org-creation logic as a hook. This keeps apikit generic and hub-specific logic in hub. The hook runs inside the transaction for atomicity.

2. **`owner_id` column on `orgs` table.** Organization ownership is modeled as a column on the `orgs` table rather than a role in `org_members`. This provides a clear, queryable way to identify personal orgs (`WHERE owner_id = ?`) and distinguishes personal orgs from admin-created ones (`owner_id IS NULL`).

3. **Username sanitized using GitHub conventions.** GitHub usernames are already mostly slug-compatible (alphanumeric + hyphens). The sanitization handles edge cases (uppercase, leading digits, special characters from non-GitHub providers) by lowercasing and replacing invalid characters with hyphens.

4. **Slug collision resolved with numeric suffix.** If the sanitized username collides with an existing org slug, append `-1`, `-2`, etc. This is simple, predictable, and avoids failing user creation due to a name collision. Capped at 10 attempts to prevent infinite loops.

5. **No workspace slug format change.** Workspace slugs remain globally unique flat identifiers. The `<username>/<workspace_slug>` namespace is a logical grouping via the org association, not a structural change to slugs. This avoids breaking changes to spec 01.

6. **"First time" detected by user not existing in DB.** The hook fires only on the new-user code path (not on returning-user login). This is inherent in the placement of the hook call — it runs after the INSERT, not the UPDATE branch.

7. **Org fields derived from username.** `name` = original username, `slug` = sanitized username, `url` = empty string, `status` = active. These are sensible defaults that require no user input.

8. **Workspace auto-associates with personal org.** When `org_id` is not provided in the create request, the server defaults to the user's personal org. This ensures every workspace has an org association without requiring CLI changes or breaking existing API clients (they just get auto-populated `org_id` in responses).

