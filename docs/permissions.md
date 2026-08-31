# Permissions Reference

This document lists every permission scope known to **hub** and **apikit**,
the credential types that interact with them, and the authorization rules
that govern access.

## Permission Format

Permissions follow the pattern `resource_type:action` where both parts are
non-empty strings containing only lowercase ASCII letters, digits, and
underscores (regex `^[a-z0-9_]+$`).

The permission registry in apikit is pre-populated with 6 built-in scopes.
Consuming projects (like hub) register additional scopes by passing
`apikit.Permission` structs to `Server.MountHandlers()`. PAT creation
validates every requested scope against this registry and rejects unknown
scopes with HTTP 400.

---

## Credential Types

| Type | Format | Permission Model |
|------|--------|-----------------|
| Admin Token | `af_admin_<64-hex>` | Implicit full access. Bypasses all permission and ownership checks. Cannot create workspaces (no user identity). |
| API Key | `af_<key_id>_<secret>` | Implicit full access to the owning user's resources. Admin-role API keys also pass admin-level checks. |
| PAT | `af_pat_<token_id>_<secret>` | Explicit scope-based access. Restricted to the permission list granted at creation. Never treated as admin, even if the user has admin role. |

Admin tokens and API keys bypass all `RequirePermission` checks — scopes
only constrain PATs.

---

## API Key Authorization in Detail

API keys are the primary credential for interactive users and CLI callers.
They carry no explicit permission list — their access is governed by
ownership and role instead.

### Lifecycle

**Creation.** API keys are created exclusively through the OAuth login
flow (`POST /auth/callback`). The CLI runs `afc login`, opens a browser
for GitHub OAuth, receives an authorization code, and exchanges it with
the hub. The hub upserts the user record, revokes all existing API keys
for that user, generates a new key (`af_<key_id>_<secret>`), and returns
it once. The plaintext secret is never stored server-side — only the
SHA-256 hash is persisted. Default expiry is 90 days (allowed: 0, 30, 60,
or 90).

**Refresh.** `POST /user/keys/:key_id/refresh` generates a new secret
while preserving the key_id and recalculates expiry. This endpoint
rejects PAT authentication — API key auth is required.

**Revocation.** Keys can be revoked three ways:
- Self-service: `DELETE /user/keys/:key_id` (self-revocation of the
  authenticating key is allowed).
- Admin: `DELETE /users/:id/keys/:key_id`.
- Automatic: re-login via OAuth mass-revokes all existing keys for the
  user.

**Blocked users.** Blocking a user disables all their API keys at
authentication time without individually revoking them. Unblocking
immediately re-enables all non-revoked, non-expired keys.

### Authentication Flow

REST API requests pass the API key as `Authorization: Bearer <key>`. Git
operations pass it as the HTTP Basic password (username is ignored). Both
paths validate the credential independently:

1. Parse the token format to identify it as an API key.
2. Look up the `key_id` in the `api_keys` table.
3. Check `revoked_at` is NULL.
4. Check `expires_at` is not in the past.
5. SHA-256 hash the secret and constant-time compare against the stored
   hash.
6. (REST only) Check the owning user's status is not `blocked`.

### Authorization Model

API key authorization rests on three principles:

**1. Permission bypass.** `RequirePermission` returns nil immediately for
API keys without inspecting any scope list. The `AuthInfo` struct for API
keys has `Permissions` set to nil. In the workspace subsystem,
`hasWriteAccess` and `hasDeleteAccess` also return true unconditionally
for API keys. In the git server, `requireGitScope` returns nil for API
keys — they have implicit `git:read` + `git:write`.

**2. Ownership enforcement.** Despite bypassing scope checks, API keys
are strictly scoped to resources they own:
- REST `/user/*` endpoints query `WHERE user_id = ?` using the
  authenticated user's ID.
- Core workspace CRUD handlers (create, list, get, update, archive,
  reactivate, delete) check `ws.OwnerID == auth.UserID` and return 404 on
  mismatch (anti-enumeration). List endpoints filter by `owner_id`.
- Git server checks `info.UserID != ws.OwnerID` and returns a pkt-line
  404 on mismatch.
- **Exception:** The sync (`POST /api/v1/workspaces/:slug/sync`) and
  reclone (`POST /api/v1/workspaces/:slug/reclone`) handlers do NOT
  enforce ownership. They look up the workspace by slug without checking
  `OwnerID`, so any authenticated API key (or admin token) can trigger
  sync or reclone on any workspace.

**3. Role-based escalation.** `IsAdmin()` returns true when
`CredentialType == "api_key"` AND `Role == "admin"`. This grants access
to `RequireAdmin`-gated endpoints (user management, org management).
However, admin-role API keys do NOT gain elevated access in two
subsystems:
- **Workspaces:** `convertApikitAuth` discards the `Role` field and maps
  `api_key` to `CredentialAPIKey` regardless of role. Admin-role API keys
  see only their own workspaces.
- **Git server:** `authorizeGitAccess` checks `CredentialType ==
  "admin_token"` directly, not `IsAdmin()`. Admin-role API keys must own
  the workspace.

This creates an intentional asymmetry: an admin-role API key is admin for
REST user/org management but NOT for cross-tenant workspace or git access.
Only admin tokens bypass ownership.

### API Key vs PAT

| Aspect | API Key | PAT |
|--------|---------|-----|
| Permissions | Implicit full access (bypass all scope checks) | Must carry explicit scopes |
| Admin access | Admin-role keys pass `RequireAdmin` | Never admin, regardless of user role |
| Privilege escalation | Can create PATs with any registered permissions | New PAT must be a subset of creating PAT |
| Credential refresh | `POST /user/keys/:key_id/refresh` (API key auth only) | Not supported |
| Intended use | Interactive / CLI | Programmatic / CI |

### CLI Usage

**Login:** `afc login --provider github --expires 90` opens a browser for
OAuth, receives an API key, and saves it to `~/.af/config.toml` (mode
0600 in a mode 0700 directory).

**Credential precedence:** (1) `--api-key` flag, (2) `API_KEY` env var,
(3) `~/.af/config.toml`, (4) error.

**HTTP requests:** The CLI sets `Authorization: Bearer <key>` on every
request. No client-side expiry check — all validation is server-side.

**Git credential helper:** `afc credential-helper get` reads
`~/.af/config.toml` and returns the API key as an HTTP Basic password
(`username=x-token-auth`), bridging REST Bearer auth and git HTTP Basic
auth. Configure with:
```
git config --global credential.<hub-url>.helper "!afc credential-helper"
```

### Endpoint Access Matrix

#### Self-Service Endpoints (all API keys)

| Endpoint | Access | Ownership |
|----------|--------|-----------|
| `GET /user` | allowed | own profile via `GetUserID` |
| `PATCH /user` | allowed | own profile |
| `GET /user/orgs` | allowed | own memberships |
| `GET /user/keys` | allowed | own keys via `WHERE user_id = ?` |
| `POST /user/keys/:key_id/refresh` | allowed | own key (404 on mismatch) |
| `DELETE /user/keys/:key_id` | allowed | own key (self-revocation permitted) |
| `POST /user/tokens` | allowed | creates PAT for own user (no escalation check) |
| `GET /user/tokens` | allowed | own PATs |
| `GET /user/tokens/:token_id` | allowed | own PAT (404 on mismatch) |
| `DELETE /user/tokens/:token_id` | allowed | own PAT (404 on mismatch) |

#### Workspace Endpoints (all API keys)

| Endpoint | Access | Ownership |
|----------|--------|-----------|
| `POST /api/v1/workspaces` | allowed | new workspace owned by caller |
| `GET /api/v1/workspaces` | allowed | filtered to own workspaces |
| `GET /api/v1/workspaces/:slug` | allowed | must own (404 if not) |
| `PATCH /api/v1/workspaces/:slug` | allowed | must own |
| `POST /api/v1/workspaces/:slug/archive` | allowed | must own |
| `POST /api/v1/workspaces/:slug/reactivate` | allowed | must own |
| `DELETE /api/v1/workspaces/:slug` | allowed | must own, must be archived |
| `POST /api/v1/workspaces/:slug/sync` | allowed | no ownership check |
| `POST /api/v1/workspaces/:slug/reclone` | allowed | no ownership check |

Admin-role API keys have the same access as user-role — they do NOT see
other users' workspaces.

#### Git Endpoints (all API keys, ownership enforced)

| Endpoint | Access | Ownership |
|----------|--------|-----------|
| `GET /git/:org/:slug.git/info/refs` | allowed | must own workspace |
| `POST /git/:org/:slug.git/git-upload-pack` | allowed | must own workspace |
| `POST /git/:org/:slug.git/git-receive-pack` | allowed | must own workspace |

API keys have implicit `git:read` + `git:write`. Admin-role API keys do
NOT bypass ownership in the git server.

#### Admin Endpoints (admin-role API keys only)

User-role API keys receive 403 on all admin endpoints. Admin-role API
keys (`Role == "admin"`) pass `RequireAdmin` and gain access to:

| Endpoint | Description |
|----------|-------------|
| `POST /users` | Create user |
| `GET /users` | List all users |
| `GET /users/:id` | Get any user |
| `PATCH /users/:id` | Update any user |
| `POST /users/:id/promote` | Promote to admin |
| `POST /users/:id/demote` | Demote from admin |
| `POST /users/:id/block` | Block user |
| `POST /users/:id/unblock` | Unblock user |
| `GET /users/:id/keys` | List any user's keys |
| `DELETE /users/:id/keys/:key_id` | Revoke any user's key |
| `GET /users/:id/tokens` | List any user's PATs |
| `DELETE /users/:id/tokens/:token_id` | Revoke any user's PAT |
| `POST /orgs` | Create org |
| `GET /orgs` | List all orgs |
| `GET /orgs/:id` | View any org (bypasses membership) |
| `PATCH /orgs/:id` | Update any org |
| `DELETE /orgs/:id` | Delete org |
| `POST /orgs/:id/block` | Block org |
| `POST /orgs/:id/unblock` | Unblock org |
| `GET /orgs/:id/members` | List members (bypasses membership) |
| `PUT /orgs/:id/members/:user_id` | Add member |
| `DELETE /orgs/:id/members/:user_id` | Remove member |

### Non-Obvious Behaviors

- **OAuth re-login mass-revokes keys.** Logging in via OAuth revokes ALL
  existing API keys for the user, invalidating any keys used by automated
  systems.
- **Admin-role API keys are not admin everywhere.** They pass
  `RequireAdmin` for user/org management but are treated as regular users
  in workspace and git subsystems. Only admin tokens have cross-tenant
  workspace/git access.
- **Admin tokens cannot create workspaces.** They have no user identity,
  so workspace creation is rejected with 403.
- **API keys can create PATs with any permissions.** The privilege
  escalation check only applies when a PAT creates another PAT.
- **Self-revocation is allowed.** A key can revoke itself because the
  auth middleware validates the credential before the handler executes.
- **Config supports env var expansion.** `~/.af/config.toml` values
  containing `$VAR` are expanded via `os.ExpandEnv()` at load time.

---

## apikit Built-in Permissions

These 6 permissions are registered automatically by `apikit`'s
`NewPermissionRegistry()` in `apikit/internal/auth/permissions.go`.

### users:read

| | |
|---|---|
| **Source** | apikit |
| **Grants** | Read access to the authenticated user's own profile |
| **Endpoints** | `GET /user`, `PATCH /user` |

### orgs:read

| | |
|---|---|
| **Source** | apikit |
| **Grants** | Read access to the authenticated user's organization memberships |
| **Endpoints** | `GET /user/orgs` |

### keys:read

| | |
|---|---|
| **Source** | apikit |
| **Grants** | Reserved for PAT-scoped read access to API key metadata |
| **Endpoints** | — (registered but not yet enforced by apikit handlers) |

### keys:manage

| | |
|---|---|
| **Source** | apikit |
| **Grants** | Reserved for PAT-scoped API key management (revoke) |
| **Endpoints** | `DELETE /user/keys/:key_id` |

### tokens:read

| | |
|---|---|
| **Source** | apikit |
| **Grants** | List and retrieve the authenticated user's PATs (metadata only) |
| **Endpoints** | `GET /user/tokens`, `GET /user/tokens/:token_id` |

### tokens:manage

| | |
|---|---|
| **Source** | apikit |
| **Grants** | Create and revoke PATs. Privilege escalation is blocked: a new PAT's permissions must be a subset of the creating PAT's permissions. |
| **Endpoints** | `POST /user/tokens`, `DELETE /user/tokens/:token_id` |

---

## Hub Workspace Permissions

These 5 permissions are registered by `WorkspacePermissions()` in
`hub/internal/workspace/routes.go` and passed to `apikit.Server.MountHandlers()`
at startup.

### workspaces:read

| | |
|---|---|
| **Source** | hub |
| **Grants** | List and view workspaces owned by the authenticated user; list and forget rerere resolutions; view patch-status dashboard |
| **Endpoints** | `GET /api/v1/workspaces`, `GET /api/v1/workspaces/:slug`, `GET /api/v1/workspaces/:slug/rerere`, `DELETE /api/v1/workspaces/:slug/rerere/*pathspec`, `GET /api/v1/workspaces/:slug/patch-status` |
| **Implied by** | `workspaces:create`, `workspaces:write` -- but only for workspace CRUD endpoints (list, get). The rerere and patch-status handlers check for the literal `workspaces:read` scope via `hasScope(auth, "workspaces:read")` and do NOT honor these implications. A PAT with `workspaces:create` or `workspaces:write` but without `workspaces:read` can list and get workspaces but cannot access rerere or patch-status endpoints. |
| **Ownership** | Enforced for workspace CRUD endpoints (list, get) via `lookupWorkspaceForAuth`. NOT enforced for rerere and patch-status endpoints -- these handlers check workspace existence but not ownership, so any authenticated user with `workspaces:read` can access rerere and patch-status for any workspace slug. |

### workspaces:create

| | |
|---|---|
| **Source** | hub |
| **Grants** | Create new workspaces. Admin tokens cannot create workspaces (HTTP 403) because a real user identity is required as owner. |
| **Endpoints** | `POST /api/v1/workspaces`, `GET /api/v1/workspaces`, `GET /api/v1/workspaces/:slug` |
| **Implies** | `workspaces:read` (workspace CRUD endpoints only -- list and get workspaces; does NOT grant access to rerere or patch-status which require the literal `workspaces:read` scope) |

### workspaces:write

| | |
|---|---|
| **Source** | hub |
| **Grants** | Update, archive, and reactivate workspaces owned by the authenticated user. Also grants access to the carry-patch sync endpoint (`POST /api/v1/workspaces/:slug/sync` when the workspace is in `carry_patch` mode), as an alternative to `workspaces:sync`. |
| **Endpoints** | `PATCH /api/v1/workspaces/:slug`, `POST /api/v1/workspaces/:slug/archive`, `POST /api/v1/workspaces/:slug/reactivate`, `POST /api/v1/workspaces/:slug/sync` (carry-patch sync only), `GET /api/v1/workspaces`, `GET /api/v1/workspaces/:slug` |
| **Implies** | `workspaces:read` (workspace CRUD endpoints only -- list and get workspaces; does NOT grant access to rerere or patch-status which require the literal `workspaces:read` scope) |

### workspaces:delete

| | |
|---|---|
| **Source** | hub |
| **Grants** | Permanently delete archived workspaces owned by the authenticated user |
| **Endpoints** | `DELETE /api/v1/workspaces/:slug` |
| **Does NOT imply** | `workspaces:read` — a PAT with only `workspaces:delete` cannot list or view workspaces |

### workspaces:sync

| | |
|---|---|
| **Source** | hub |
| **Grants** | Trigger upstream sync and reclone operations on workspaces. Includes fetching from upstream, fast-forwarding the integration branch, triggering reset-to-upstream recovery, and nuclear reclone (archive + re-clone). |
| **Endpoints** | `POST /api/v1/workspaces/:slug/sync`, `POST /api/v1/workspaces/:slug/reclone` |
| **Does NOT imply** | `workspaces:read` — a PAT with only `workspaces:sync` cannot list or view workspaces |
| **Note** | For carry-patch workspaces, the carry-patch sync handler also accepts `workspaces:write` as an alternative scope. The standard sync handler and reclone handler require `workspaces:sync` exclusively. |

---

## Hub Patch Permissions

These 2 permissions are registered by the workspace package in
`hub/internal/workspace/routes.go` and passed to `apikit.Server.MountHandlers()`
at startup. They control access to the carry-patch patch list endpoints.

### patches:read

| | |
|---|---|
| **Source** | hub |
| **Grants** | List and view patches for a workspace |
| **Endpoints** | `GET /api/v1/workspaces/:slug/patches` |
| **Implied by** | `patches:write` |
| **Ownership** | Not enforced. Any authenticated user with the required scope can list patches for any workspace slug. |

### patches:write

| | |
|---|---|
| **Source** | hub |
| **Grants** | Add, remove, update, and reorder patches for a workspace |
| **Endpoints** | `POST /api/v1/workspaces/:slug/patches`, `PATCH /api/v1/workspaces/:slug/patches/:id`, `DELETE /api/v1/workspaces/:slug/patches/:id`, `POST /api/v1/workspaces/:slug/patches/reorder` |
| **Implies** | `patches:read` |
| **Ownership** | Not enforced. Patch handlers check workspace existence and status but do NOT verify ownership. Any authenticated user with the required scope can manage patches for any workspace slug. |

---

## Hub Merge Permissions

These 2 permissions are registered by `MergePermissions()` in
`hub/internal/merge/api.go` and passed to `apikit.Server.MountHandlers()`
at startup.

### merges:read

| | |
|---|---|
| **Source** | hub |
| **Grants** | List and view merge job status for workspaces |
| **Endpoints** | `GET /api/v1/workspaces/:slug/merges`, `GET /api/v1/workspaces/:slug/merges/:id` |
| **Ownership** | Not enforced. Any authenticated user with the required scope can list/view merge jobs for any workspace slug. |

### merges:write

| | |
|---|---|
| **Source** | hub |
| **Grants** | Submit merge requests, cancel queued merge jobs, and trigger batch rebase operations |
| **Endpoints** | `POST /api/v1/workspaces/:slug/merges`, `DELETE /api/v1/workspaces/:slug/merges/:id`, `POST /api/v1/workspaces/:slug/rebase` |
| **Ownership** | Not enforced. Merge handlers check workspace existence, active status, and ready clone status but do NOT verify `ws.OwnerID == auth.UserID`. Any authenticated user with the required scope can access these endpoints for any workspace slug. |

---

## Hub Rebuild Permissions

These 2 permissions are registered by `CarryPatchPermissions()` in
`hub/internal/carrypatch/wire.go` and passed to `apikit.Server.MountHandlers()`
at startup via the `extraPerms` parameter.

### rebuilds:read

| | |
|---|---|
| **Source** | hub |
| **Grants** | List and view rebuild job status and history for carry-patch workspaces |
| **Endpoints** | `GET /api/v1/workspaces/:slug/rebuilds`, `GET /api/v1/workspaces/:slug/rebuilds/:id` |
| **Ownership** | Not enforced. Any authenticated user with the required scope can list/view rebuild jobs for any workspace slug. |

### rebuilds:write

| | |
|---|---|
| **Source** | hub |
| **Grants** | Submit rebuild jobs for carry-patch workspaces |
| **Endpoints** | `POST /api/v1/workspaces/:slug/rebuild` |
| **Ownership** | Not enforced. Rebuild handlers check workspace mode, status, and clone status but do NOT verify ownership. Any authenticated user with the required scope can submit rebuilds for any workspace slug. |

---

## Hub Git Permissions

These 2 permissions are registered by `GitPermissions()` in
`hub/internal/gitserver/permissions.go` and passed to
`apikit.Server.MountHandlers()` at startup via the `extraPerms` parameter of
`MountWorkspaceHandlers()`.

### git:read

| | |
|---|---|
| **Source** | hub |
| **Grants** | Clone and fetch access to workspace repositories via git smart HTTP |
| **Endpoints** | `GET /git/:org/:slug.git/info/refs?service=git-upload-pack`, `POST /git/:org/:slug.git/git-upload-pack` |
| **Implied by** | `git:write` |

### git:write

| | |
|---|---|
| **Source** | hub |
| **Grants** | Push access to workspace repositories via git smart HTTP |
| **Endpoints** | `POST /git/:org/:slug.git/git-receive-pack`, `GET /git/:org/:slug.git/info/refs?service=git-receive-pack` (plus all `git:read` endpoints) |
| **Implies** | `git:read` |

---

## Hub Secrets Permissions

These 4 permissions are registered by `Permissions()` in
`hub/internal/secrets/permissions.go` and passed to `apikit.Server.MountHandlers()`
at startup via the `extraPerms` parameter.

### secrets:manage

| | |
|---|---|
| **Source** | hub |
| **Grants** | Full CRUD access to secrets: create, list, update, and delete |
| **Endpoints** | `POST /api/v1/user/secrets`, `POST /api/v1/orgs/:slug/secrets`, `POST /api/v1/workspaces/:slug/secrets`, `GET /api/v1/user/secrets`, `GET /api/v1/orgs/:slug/secrets`, `GET /api/v1/workspaces/:slug/secrets`, `PATCH /api/v1/user/secrets/:key`, `PATCH /api/v1/orgs/:slug/secrets/:key`, `PATCH /api/v1/workspaces/:slug/secrets/:key`, `DELETE /api/v1/user/secrets/:key`, `DELETE /api/v1/orgs/:slug/secrets/:key`, `DELETE /api/v1/workspaces/:slug/secrets/:key` |
| **Implies** | `secrets:list`, `secrets:write`, `secrets:delete` |

### secrets:list

| | |
|---|---|
| **Source** | hub |
| **Grants** | List access to secret names (values are never returned) |
| **Endpoints** | `GET /api/v1/user/secrets`, `GET /api/v1/orgs/:slug/secrets`, `GET /api/v1/workspaces/:slug/secrets` |
| **Implied by** | `secrets:manage` |

### secrets:write

| | |
|---|---|
| **Source** | hub |
| **Grants** | Update existing secret values |
| **Endpoints** | `PATCH /api/v1/user/secrets/:key`, `PATCH /api/v1/orgs/:slug/secrets/:key`, `PATCH /api/v1/workspaces/:slug/secrets/:key` |
| **Implied by** | `secrets:manage` |

### secrets:delete

| | |
|---|---|
| **Source** | hub |
| **Grants** | Delete secrets |
| **Endpoints** | `DELETE /api/v1/user/secrets/:key`, `DELETE /api/v1/orgs/:slug/secrets/:key`, `DELETE /api/v1/workspaces/:slug/secrets/:key` |
| **Implied by** | `secrets:manage` |

---

## Hub Variables Permissions

These 4 permissions are registered by `VarsPermissions()` in
`hub/internal/vars/permissions.go` and included in `secrets.Permissions()`
for startup registration.

### vars:manage

| | |
|---|---|
| **Source** | hub |
| **Grants** | Full CRUD access to variables: create, list, read, update, and delete |
| **Endpoints** | `POST /api/v1/user/vars`, `POST /api/v1/orgs/:slug/vars`, `POST /api/v1/workspaces/:slug/vars`, `GET /api/v1/user/vars`, `GET /api/v1/orgs/:slug/vars`, `GET /api/v1/workspaces/:slug/vars`, `GET /api/v1/workspaces/:slug/vars/resolved`, `PATCH /api/v1/user/vars/:key`, `PATCH /api/v1/orgs/:slug/vars/:key`, `PATCH /api/v1/workspaces/:slug/vars/:key`, `DELETE /api/v1/user/vars/:key`, `DELETE /api/v1/orgs/:slug/vars/:key`, `DELETE /api/v1/workspaces/:slug/vars/:key` |
| **Implies** | `vars:read`, `vars:write`, `vars:delete` |

### vars:read

| | |
|---|---|
| **Source** | hub |
| **Grants** | List and read variable values |
| **Endpoints** | `GET /api/v1/user/vars`, `GET /api/v1/orgs/:slug/vars`, `GET /api/v1/workspaces/:slug/vars`, `GET /api/v1/workspaces/:slug/vars/resolved` |
| **Implied by** | `vars:write`, `vars:manage` |

### vars:write

| | |
|---|---|
| **Source** | hub |
| **Grants** | Update existing variable values |
| **Endpoints** | `PATCH /api/v1/user/vars/:key`, `PATCH /api/v1/orgs/:slug/vars/:key`, `PATCH /api/v1/workspaces/:slug/vars/:key` (plus all `vars:read` endpoints) |
| **Implies** | `vars:read` |
| **Implied by** | `vars:manage` |

### vars:delete

| | |
|---|---|
| **Source** | hub |
| **Grants** | Delete variables |
| **Endpoints** | `DELETE /api/v1/user/vars/:key`, `DELETE /api/v1/orgs/:slug/vars/:key`, `DELETE /api/v1/workspaces/:slug/vars/:key` |
| **Implied by** | `vars:manage` |

---

## Implied Permissions Summary

| Scope | Implies |
|-------|---------|
| `workspaces:create` | `workspaces:read` * |
| `workspaces:write` | `workspaces:read` * |
| `workspaces:sync` | *(nothing)* |
| `workspaces:delete` | *(nothing)* |
| `patches:write` | `patches:read` |
| `git:write` | `git:read` |
| `secrets:manage` | `secrets:list`, `secrets:write`, `secrets:delete` |
| `vars:manage` | `vars:read`, `vars:write`, `vars:delete` |
| `vars:write` | `vars:read` |

\* The `workspaces:create` and `workspaces:write` implication of
`workspaces:read` is implemented via `hasReadAccess()` in workspace CRUD
handlers only. It applies to `GET /api/v1/workspaces` and
`GET /api/v1/workspaces/:slug`. It does NOT apply to rerere
(`GET /api/v1/workspaces/:slug/rerere`,
`DELETE /api/v1/workspaces/:slug/rerere/*`) or patch-status
(`GET /api/v1/workspaces/:slug/patch-status`), which check for the literal
`workspaces:read` scope string.

---

## Complete Permission List

All 27 registered permission scopes, sorted alphabetically:

| # | Scope | Source | Resource | Action |
|---|-------|--------|----------|--------|
| 1 | `git:read` | hub | git | read |
| 2 | `git:write` | hub | git | write |
| 3 | `keys:manage` | apikit | keys | manage |
| 4 | `keys:read` | apikit | keys | read |
| 5 | `merges:read` | hub | merges | read |
| 6 | `merges:write` | hub | merges | write |
| 7 | `orgs:read` | apikit | orgs | read |
| 8 | `patches:read` | hub | patches | read |
| 9 | `patches:write` | hub | patches | write |
| 10 | `rebuilds:read` | hub | rebuilds | read |
| 11 | `rebuilds:write` | hub | rebuilds | write |
| 12 | `secrets:delete` | hub | secrets | delete |
| 13 | `secrets:list` | hub | secrets | list |
| 14 | `secrets:manage` | hub | secrets | manage |
| 15 | `secrets:write` | hub | secrets | write |
| 16 | `tokens:manage` | apikit | tokens | manage |
| 17 | `tokens:read` | apikit | tokens | read |
| 18 | `users:read` | apikit | users | read |
| 19 | `vars:delete` | hub | vars | delete |
| 20 | `vars:manage` | hub | vars | manage |
| 21 | `vars:read` | hub | vars | read |
| 22 | `vars:write` | hub | vars | write |
| 23 | `workspaces:create` | hub | workspaces | create |
| 24 | `workspaces:delete` | hub | workspaces | delete |
| 25 | `workspaces:read` | hub | workspaces | read |
| 26 | `workspaces:sync` | hub | workspaces | sync |
| 27 | `workspaces:write` | hub | workspaces | write |

---

## Secrets and Variables Endpoint Access Matrix

All secrets and variables endpoints follow the same credential-type
authorization model. The scope checks (`isPAT`, `isAdmin`) are enforced at
the handler level in `hub/internal/secrets/handlers.go`.

### Credential Access by Scope

| Scope | Admin Token | API Key | PAT |
|-------|-------------|---------|-----|
| User-scoped (`/api/v1/user/secrets`, `/api/v1/user/vars`) | Full access | Full access (own user) | Requires explicit scope |
| Org-scoped (`/api/v1/orgs/:slug/secrets`, `/api/v1/orgs/:slug/vars`) | Full access (bypasses membership) | Full access (requires org membership) | Requires explicit scope + org membership |
| Workspace-scoped (`/api/v1/workspaces/:slug/secrets`, `/api/v1/workspaces/:slug/vars`) | Full access (bypasses ownership) | Full access (requires workspace ownership) | Requires explicit scope + workspace ownership |

### PAT Scope Requirements by Operation

| Operation | Secrets Scope Required | Variables Scope Required |
|-----------|----------------------|------------------------|
| Create (POST) | `secrets:manage` | `vars:manage` |
| List (GET) | `secrets:list` or `secrets:manage` | `vars:read`, `vars:write`, or `vars:manage` |
| Update (PATCH) | `secrets:write` or `secrets:manage` | `vars:write` or `vars:manage` |
| Delete (DELETE) | `secrets:delete` or `secrets:manage` | `vars:delete` or `vars:manage` |
| Resolved (GET .../resolved) | — | `vars:read`, `vars:write`, or `vars:manage` |

### Ownership and Membership Rules

- **Admin tokens** bypass all ownership and membership checks. They can
  access secrets and variables for any user, org, or workspace.
- **API keys** have implicit full access but are constrained by ownership:
  user-scoped operations use the authenticated user's ID; org-scoped
  operations require org membership; workspace-scoped operations require
  workspace ownership.
- **PATs** require explicit scopes as listed above. Additionally, org-scoped
  operations require org membership and workspace-scoped operations require
  workspace ownership.

Non-admin credentials that fail ownership or membership checks receive
HTTP 404 (not 403) consistent with the anti-enumeration policy.

---

## Anti-Enumeration Policy

When a PAT lacks the required workspace scope, or when a workspace is
not owned by the caller, the workspace CRUD endpoints (list, get, update,
archive, reactivate, delete) return HTTP 404 (not 403) to avoid disclosing
the existence of resources. The workspace create endpoint returns HTTP 403
(`"PAT requires workspaces:create scope to create workspaces"`) because
there is no existing resource to enumerate. The git server returns HTTP 404
for non-owner access (`authorizeGitAccess` writes a pkt-line 404
`"repository not found"`) to prevent workspace slug enumeration.

Other endpoint groups return HTTP 403 for missing PAT scopes:
git scope check returns 403 with `"insufficient git permissions"`;
sync handler returns 403 with `"PAT requires workspaces:sync scope to sync workspaces"`;
reclone handler returns 403 with `"PAT requires workspaces:sync scope to reclone workspaces"`;
carry-patch sync handler returns 403 with `"missing required scope: workspaces:sync"`;
secrets and variables handlers return 403 with `"insufficient permission scope"`;
merge handlers return 403 with `"PAT requires merges:write scope"` (or
`merges:read`); rebuild handlers return 403 with `"missing required scope:
rebuilds:write"` (or `rebuilds:read`); patch handlers return 403 with
`"PAT requires patches:read scope"` (or `patches:write`); rerere and
patch-status handlers return 403 for missing `workspaces:read` scope.

## Extending the Permission Registry

To add custom permissions from a new hub module:

1. Define a function returning `[]apikit.Permission` (see `GitPermissions()`
   or `WorkspacePermissions()` for examples).
2. Pass the permissions to `MountWorkspaceHandlers()` via the `extraPerms`
   variadic parameter, or call `Server.MountHandlers()` directly.
3. Check the scope in your handlers using `auth.RequirePermission(c, resource, action)`
   (apikit-level) or the workspace package's `hasPermission()` helper.
