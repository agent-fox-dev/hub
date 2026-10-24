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

A PAT can also be bound to one workspace. The binding narrows where its scopes
apply and never changes what they allow; see
[Workspace-Bound PATs](#workspace-bound-pats).

---

## API Key Authorization in Detail

API keys are the primary credential for interactive users and CLI callers.
They carry no explicit permission list — their access is governed by
ownership and role instead.

### Lifecycle

**Creation.** API keys are created exclusively through the OAuth login
flow (`POST /api/v1/auth/callback`). The CLI runs `afc login`, opens a browser
for GitHub OAuth, receives an authorization code, and exchanges it with
the hub. The hub upserts the user record, revokes all existing API keys
for that user, generates a new key (`af_<key_id>_<secret>`), and returns
it once. The plaintext secret is never stored server-side — only the
SHA-256 hash is persisted. Default expiry is 90 days (allowed: 0, 30, 60,
or 90).

**Refresh.** `POST /api/v1/user/keys/:key_id/refresh` generates a new secret
while preserving the key_id and recalculates expiry. This endpoint
rejects PAT authentication — API key auth is required.

**Revocation.** Keys can be revoked three ways:
- Self-service: `DELETE /api/v1/user/keys/:key_id` (self-revocation of the
  authenticating key is allowed).
- Admin: `DELETE /api/v1/users/:id/keys/:key_id`.
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

**2. Workspace access.** Despite bypassing scope checks, API keys
are strictly scoped to the resources they may reach:
- REST `/api/v1/user/*` endpoints query `WHERE user_id = ?` using the
  authenticated user's ID.
- Every REST route that names a workspace decides access through
  `wsaccess.AuthorizeWorkspace` (see [Workspace access
  levels](#workspace-access-levels); the session, cost and audit routes keep
  their own refusal answers): the owner and admin tokens may do
  everything, a member of the workspace's org may read, and everyone else
  gets 404 (anti-enumeration). Sync and reclone are writes like any other.
  The listing returns the caller's own workspaces plus those of the orgs
  the caller belongs to.
- The git server decides through `wsaccess.AuthorizeWorkspaceAt` with
  the read level for clone and fetch and the write level for push: the
  owner and admin tokens may do both, a member of the workspace's org may
  clone and fetch and gets a pkt-line 403 on push, and everyone else gets a
  pkt-line 404.

### Workspace access levels

`internal/wsaccess` decides a caller's access to a workspace, in this order,
and each REST endpoint states what it needs:

| Access | Who |
|--------|-----|
| `admin` | an admin token (`CredentialType == "admin_token"`); an admin-role API key is not one |
| `owner` | a credential whose user id is the workspace's `owner_id` |
| `member` | a credential whose user id has a row `(org_id, user_id)` in `org_members` for the workspace's `org_id` (whatever the org's status or kind); read on every request, so removing a member takes effect at once |
| none | everyone else, including a credential with no user id |

| Need | Admitted | A member gets | Anyone else gets |
|------|----------|---------------|------------------|
| read | member, owner, admin | the answer the owner gets | `404 workspace not found` |
| write | owner, admin | `403` `org members have read-only access to this workspace`, `error_type: forbidden` | `404 workspace not found` |
| private | owner, admin | `404 workspace not found`, as a stranger | `404 workspace not found` |

The checks run in this order: no credential is 401; an unknown slug and a
caller with no access are 404 (with no binding lookup); a PAT bound to another
workspace is 404 (spec 33) and the refusal is reported; then the need decides.
A database error is 500 and never a grant. The access step sits where the
ownership step always sat: after the PAT scope check (a PAT without the scope
gets the scope answer whatever its membership) and before status and
precondition checks, so a member's write to an archived workspace is 403, not
400. A scope 403 carries no `error_type`, so a client can tell it from a
member's `forbidden`.

Every workspace object carries `access` (the caller's level, `admin`, `owner`
or `member`, whatever the PAT's scopes) and `org_slug`.

| Routes | Need |
|--------|------|
| `GET /workspaces/:slug`, patch reads, rebuild reads, `rebuild-preview`, `patch-status`, `GET .../rerere`, merge reads, lease list and get, the forge reads and `forge/deliveries`, `GET .../vars`, `GET .../vars/resolved` | read |
| workspace update, archive, reactivate, delete, sync, reclone, branch push; patch writes; rebuild submit, cancel, requeue, rollback; `DELETE .../rerere/*`; merge submit, cancel, requeue, `rebase`; lease issue and delete; the forge writes and `PUT`/`DELETE .../forge/webhook`; variable create, update, delete | write |
| workspace secrets (all four routes); bound tokens (`POST`, `GET .../tokens`) | private |

The session, cost and audit routes decide through the same rule and keep their
own refusal answers:

| Routes | Need | A member gets | Anyone else gets |
|--------|------|---------------|------------------|
| per-run audit reads (`events`, `sessions/outcomes`, `tools/calls`, `tools/errors`, `traces`, `postmortem`) and the transcript | read, any workspace status | the owner's answer | `404 workspace not found` |
| `GET /sessions/:id`, `GET /sessions/:id/usage`, `GET /workspaces/:slug/cost`, `GET /sessions?workspace_slug=` | read, and the workspace must be active | the owner's answer | `403 workspace access denied` |
| `GET /sessions` | the active workspaces the caller may read | the sessions of owned and member workspaces | its own workspaces' sessions |
| `GET /audit`, `GET /events` | the workspaces of any status the caller may read (the stream computes the set when it opens) | the events of owned and member workspaces; never a hub event without a workspace | `404 workspace not found` for a `workspace` filter outside the set |
| `POST /sessions` | write | `403` `org members have read-only access to this workspace`, `error_type: forbidden` | `404 workspace not found` |
| `POST /sessions/:id/complete`, `POST /sessions/:id/usage` | the session's creator (owner or admin token) | `403 not session owner` | `403 not session owner` |
| the eight audit ingestion routes | owner (`checkWorkspaceAccess`) | `403 workspace_access_denied` (`409 workspace_archived` first on an archived workspace) | the same |

The git server uses the same levels: clone and fetch need read, push needs
write (a member's push is a pkt-line `403 org members have read-only access to
this workspace`); see [Git Endpoints](#git-endpoints-all-api-keys-access-enforced).

**3. Role-based escalation.** `IsAdmin()` returns true when
`CredentialType == "api_key"` AND `Role == "admin"`. This grants access
to `RequireAdmin`-gated endpoints (user management, org management).
However, admin-role API keys do NOT gain elevated access in two
subsystems:
- **Workspaces:** `convertApikitAuth` discards the `Role` field and maps
  `api_key` to `CredentialAPIKey` regardless of role. Admin-role API keys
  see only their own workspaces.
- **Git server:** `wsaccess.AuthorizeWorkspaceAt` treats only
  `CredentialType == "admin_token"` as admin, not `IsAdmin()`. An admin-role
  API key is owner, member or stranger like any other API key.

This creates an intentional asymmetry: an admin-role API key is admin for
REST user/org management but NOT for cross-tenant workspace or git access.
Only admin tokens bypass ownership.

### API Key vs PAT

| Aspect | API Key | PAT |
|--------|---------|-----|
| Permissions | Implicit full access (bypass all scope checks) | Must carry explicit scopes |
| Admin access | Admin-role keys pass `RequireAdmin` | Never admin, regardless of user role |
| Privilege escalation | Can create PATs with any registered permissions | New PAT must be a subset of creating PAT |
| Credential refresh | `POST /api/v1/user/keys/:key_id/refresh` (API key auth only) | Not supported |
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

All apikit endpoints below are mounted on the same API group as the workspace
endpoints, so they live under the configured mount point -- `/api/v1` by
default (`server.mount_point` in `config.toml`). Only `GET /healthz`,
`GET /readyz`, `GET /version`, hub's `GET /metrics`, and the `/git/...` routes
sit outside it.

#### Self-Service Endpoints (all API keys)

| Endpoint | Access | Ownership |
|----------|--------|-----------|
| `GET /api/v1/user` | allowed | own profile via `GetUserID` |
| `PATCH /api/v1/user` | allowed | own profile |
| `GET /api/v1/user/orgs` | allowed | own memberships |
| `GET /api/v1/user/keys` | allowed | own keys via `WHERE user_id = ?` |
| `POST /api/v1/user/keys/:key_id/refresh` | allowed | own key (404 on mismatch) |
| `DELETE /api/v1/user/keys/:key_id` | allowed | own key (self-revocation permitted) |
| `POST /api/v1/user/tokens` | allowed | creates PAT for own user (no escalation check) |
| `GET /api/v1/user/tokens` | allowed | own PATs |
| `GET /api/v1/user/tokens/:token_id` | allowed | own PAT (404 on mismatch) |
| `DELETE /api/v1/user/tokens/:token_id` | allowed | own PAT (404 on mismatch) |
| `PUT /api/v1/user/tokens/:token_id/permissions` | allowed | own PAT (requires `tokens:write` or `tokens:manage` scope) |
| `PATCH /api/v1/user/tokens/:token_id/permissions` | allowed | own PAT (requires `tokens:write` or `tokens:manage` scope) |
| `DELETE /api/v1/user/tokens/:token_id/permissions` | allowed | own PAT (requires `tokens:write` or `tokens:manage` scope) |

#### Workspace Endpoints (all API keys)

| Endpoint | Access | Ownership |
|----------|--------|-----------|
| `POST /api/v1/workspaces` | allowed | new workspace owned by caller |
| `GET /api/v1/workspaces` | allowed | own workspaces and those of the caller's orgs |
| `GET /api/v1/workspaces/:slug` | allowed | read: owner or org member (404 if neither) |
| `PATCH /api/v1/workspaces/:slug` | allowed | write: must own (org member 403) |
| `POST /api/v1/workspaces/:slug/archive` | allowed | write: must own (org member 403) |
| `POST /api/v1/workspaces/:slug/reactivate` | allowed | write: must own (org member 403) |
| `DELETE /api/v1/workspaces/:slug` | allowed | write: must own (org member 403), must be archived |
| `POST /api/v1/workspaces/:slug/sync` | allowed | write: must own (org member 403) |
| `POST /api/v1/workspaces/:slug/reclone` | allowed | write: must own (org member 403) |
| `POST /api/v1/workspaces/:slug/branches/:name/push` | allowed | write: must own (org member 403) |
| patches, merges, rebase, rebuilds, rerere, patch-status | allowed | reads: owner or org member; writes: must own (org member 403); 404 otherwise |
| sessions, cost, audit | allowed | reads: owner or org member (sessions and cost only on an active workspace); opening a session: must own (org member 403 `forbidden`); completing, usage reports and ingestion: owner only (org member 403) |

Admin-role API keys have the same access as user-role — they do NOT see
other users' workspaces beyond their orgs'.

#### Sandbox Endpoints (all API keys)

| Endpoint | Access | Ownership |
|----------|--------|-----------|
| `GET /api/v1/sandbox-profiles` | allowed | none; every authorised caller sees the same list |
| `POST /api/v1/workspaces/:slug/sandboxes` | allowed | write: must own the workspace (org member 403, 404 otherwise), which must be active |
| `GET /api/v1/workspaces/:slug/sandboxes`, `GET .../sandboxes/:id` | allowed | read: owner or org member (404 otherwise) |
| `DELETE /api/v1/workspaces/:slug/sandboxes/:id` | allowed | write: must own the workspace (org member 403, 404 otherwise) |
| `GET /api/v1/sandboxes` | **admin tokens only** (403 for an API key) | all workspaces |

#### Forge Endpoints (all API keys)

| Endpoint | Access | Ownership |
|----------|--------|-----------|
| `GET /api/v1/workspaces/:slug/forge` and the nine read routes (`.../forge/repository`, `.../issues`, `.../issues/:n`, `.../issues/:n/comments`, `.../pulls/:n`, `.../pulls/:n/state`, `.../pulls/:n/files`, `.../pulls/:n/checks`, `.../pulls/:n/reviews`) | allowed | read: owner or org member (404 otherwise); an admin token may read any workspace |
| the nine write routes (`POST .../issues`, `PATCH .../issues/:n`, `POST .../issues/:n/close`, `POST .../issues/:n/comments`, `POST .../issues/:n/labels`, `DELETE .../issues/:n/labels/:name`, `POST .../labels`, `POST .../pulls`, `POST .../pulls/:n/comments`) | allowed | write: must own the workspace (org member 403, 404 otherwise), which must be active (400 if not); an admin token may write to any workspace |
| `GET /api/v1/workspaces/:slug/forge/deliveries` | allowed | as the reads: owner or org member (404 otherwise); works on archived workspaces |
| `PUT /api/v1/workspaces/:slug/forge/webhook` | allowed | as the writes: must own the workspace (org member 403, 404 otherwise), which must be active (400 if not) |
| `DELETE /api/v1/workspaces/:slug/forge/webhook` | allowed | write: must own the workspace (org member 403, 404 otherwise); works on archived workspaces |
| `POST /hooks/forge/:slug` | **no credential** | none: outside the API group; the forge proves who it is with the HMAC signature or the token of the workspace's `FORGE_WEBHOOK_SECRET` |

For a read the checks run in this order: no credential is 401; a PAT without
`forge:read` or `forge:write` (and a credential type the hub does not know) is 403,
before the workspace is looked up; a workspace that is missing or that the caller may
not read is 404. For a write: no credential is 401; a PAT without `forge:write`,
`forge:read` alone included, is 403 (`PAT requires forge:write scope`), and so is an
unknown credential type, before the workspace is looked up; a workspace that is missing
or that the caller may not read is 404; a member of its org is 403 `forbidden`; one
that is not active is 400. The hub reads and writes the forge with the workspace's own
stored git credential, never with the caller's, a member's reads included; the
diagnostic names only the credential's kind.

The webhook ingress `POST /hooks/forge/:slug` is the one forge route that takes no bearer
credential and no scope: it is mounted on the server root beside `/git/...`, and a delivery
is accepted only when its signature (GitHub) or token (GitLab) matches the workspace's
`FORGE_WEBHOOK_SECRET`. A workspace without the secret and a slug that does not exist
answer the same `404`, so the endpoint does not tell which slugs exist. The secret is
managed with `forge:write` (`PUT`, `DELETE`) and its deliveries are read with `forge:read`.

#### Git Endpoints (all API keys, access enforced)

| Endpoint | Access | Ownership |
|----------|--------|-----------|
| `GET /git/:org/:slug.git/info/refs` | allowed | `service=git-receive-pack`: must own (org member pkt-line 403); otherwise owner or org member |
| `POST /git/:org/:slug.git/git-upload-pack` | allowed | owner or org member |
| `POST /git/:org/:slug.git/git-receive-pack` | allowed | must own (org member pkt-line 403) |

Anyone else gets a pkt-line 404. API keys have implicit `git:read` +
`git:write`. Admin-role API keys do NOT bypass the access rule in the git
server. Blocked users are rejected.

#### Admin Endpoints (admin-role API keys only)

User-role API keys receive 403 on all admin endpoints. Admin-role API
keys (`Role == "admin"`) pass `RequireAdmin` and gain access to:

| Endpoint | Description |
|----------|-------------|
| `POST /api/v1/users` | Create user |
| `GET /api/v1/users` | List all users |
| `GET /api/v1/users/:id` | Get any user |
| `PATCH /api/v1/users/:id` | Update any user |
| `POST /api/v1/users/:id/promote` | Promote to admin |
| `POST /api/v1/users/:id/demote` | Demote from admin |
| `POST /api/v1/users/:id/block` | Block user |
| `POST /api/v1/users/:id/unblock` | Unblock user |
| `GET /api/v1/users/:id/keys` | List any user's keys |
| `DELETE /api/v1/users/:id/keys/:key_id` | Revoke any user's key |
| `GET /api/v1/users/:id/tokens` | List any user's PATs |
| `DELETE /api/v1/users/:id/tokens/:token_id` | Revoke any user's PAT |
| `POST /api/v1/orgs` | Create org |
| `GET /api/v1/orgs` | List all orgs |
| `GET /api/v1/orgs/:id` | View any org (bypasses membership) |
| `PATCH /api/v1/orgs/:id` | Update any org |
| `DELETE /api/v1/orgs/:id` | Delete org |
| `POST /api/v1/orgs/:id/block` | Block org |
| `POST /api/v1/orgs/:id/unblock` | Unblock org |
| `GET /api/v1/orgs/:id/members` | List members (bypasses membership) |
| `PUT /api/v1/orgs/:id/members/:user_id` | Add member |
| `DELETE /api/v1/orgs/:id/members/:user_id` | Remove member |

### Non-Obvious Behaviors

- **OAuth re-login mass-revokes keys.** Logging in via OAuth revokes ALL
  existing API keys for the user, invalidating any keys used by automated
  systems.
- **Admin-role API keys are not admin everywhere.** They pass
  `RequireAdmin` for user/org management but are treated as regular users
  in workspace and git subsystems. Only admin tokens have cross-tenant
  workspace/git access.
- **Admin tokens cannot create workspaces.** They have no user identity,
  so workspace creation is rejected with 403. For the same reason they get
  403 on the user-scoped secrets and variables endpoints (`/api/v1/user/...`).
- **`vars:write` on a workspace is code execution on the hub.** The
  `CHECK_COMMAND` workspace variable is run with `sh -c` inside the trunk
  by merge jobs. Grant `vars:write`/`vars:manage` only to credentials you
  would trust with shell access to the hub process. The command runs with a
  minimal environment (PATH, HOME, TMPDIR, TZ, Go and git safety variables)
  plus the workspace's resolved variables; hub secrets are not exported.
- **API keys can create PATs with any permissions.** The privilege
  escalation check only applies when a PAT creates another PAT.
- **Self-revocation is allowed.** A key can revoke itself because the
  auth middleware validates the credential before the handler executes.
- **Config supports env var expansion.** `~/.af/config.toml` values
  containing `$VAR` are expanded via `os.ExpandEnv()` at load time.

---

## apikit Built-in Permissions

These 8 permissions are registered automatically by `apikit`'s
`NewPermissionRegistry()` in `apikit/internal/auth/permissions.go`.

### users:read

| | |
|---|---|
| **Source** | apikit |
| **Grants** | Read access to the authenticated user's own profile |
| **Endpoints** | `GET /api/v1/user`, `PATCH /api/v1/user` |

### orgs:read

| | |
|---|---|
| **Source** | apikit |
| **Grants** | Read access to the authenticated user's organization memberships |
| **Endpoints** | `GET /api/v1/user/orgs` |

### orgs:write

| | |
|---|---|
| **Source** | apikit |
| **Grants** | Reserved for PAT-scoped organization write operations |
| **Endpoints** | — (registered but not yet enforced by apikit handlers) |

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
| **Endpoints** | `DELETE /api/v1/user/keys/:key_id` |

### tokens:read

| | |
|---|---|
| **Source** | apikit |
| **Grants** | List and retrieve the authenticated user's PATs (metadata only) |
| **Endpoints** | `GET /api/v1/user/tokens`, `GET /api/v1/user/tokens/:token_id`; hub's `GET /api/v1/workspaces/:slug/tokens` (the workspace's owner) |

### tokens:manage

| | |
|---|---|
| **Source** | apikit |
| **Grants** | Create and revoke PATs, and modify PAT permissions. Privilege escalation is blocked: a new PAT's permissions must be a subset of the creating PAT's permissions. |
| **Endpoints** | `POST /api/v1/user/tokens`, `DELETE /api/v1/user/tokens/:token_id`, `PUT /api/v1/user/tokens/:token_id/permissions`, `PATCH /api/v1/user/tokens/:token_id/permissions`, `DELETE /api/v1/user/tokens/:token_id/permissions`; hub's `POST /api/v1/workspaces/:slug/tokens` (checked by apikit's route, which hub calls) and `GET /api/v1/workspaces/:slug/tokens` |

### tokens:write

| | |
|---|---|
| **Source** | apikit |
| **Grants** | Modify PAT permissions. A strict subset of `tokens:manage` by convention — a caller with `tokens:manage` can do everything `tokens:write` allows. |
| **Endpoints** | `PUT /api/v1/user/tokens/:token_id/permissions`, `PATCH /api/v1/user/tokens/:token_id/permissions`, `DELETE /api/v1/user/tokens/:token_id/permissions` |

---

## Hub Workspace Permissions

These 5 permissions are registered by `WorkspacePermissions()` in
`hub/internal/workspace/routes.go` and passed to `apikit.Server.MountHandlers()`
at startup.

### workspaces:read

| | |
|---|---|
| **Source** | hub |
| **Grants** | List and view the workspaces the authenticated user owns or whose org they belong to; list rerere resolutions; view patch-status dashboard |
| **Endpoints** | `GET /api/v1/workspaces`, `GET /api/v1/workspaces/:slug`, `GET /api/v1/workspaces/:slug/rerere`, `GET /api/v1/workspaces/:slug/patch-status` |
| **Implied by** | `workspaces:create`, `workspaces:write` -- but only for workspace CRUD endpoints (list, get). The rerere list and patch-status handlers check for the literal `workspaces:read` scope via `wsaccess.HasScope(auth, "workspaces:read")` and do NOT honor these implications. A PAT with `workspaces:create` or `workspaces:write` but without `workspaces:read` can list and get workspaces but cannot access rerere list or patch-status endpoints. |
| **Ownership** | Enforced through `wsaccess` with the read level: the workspace's owner, a member of its org and an admin token may read; anyone else gets `404` (anti-enumeration, see [Workspace access levels](#workspace-access-levels)). The listing returns the caller's own workspaces and those of the caller's orgs. |

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
| **Grants** | Update, archive, and reactivate workspaces owned by the authenticated user, and push one of their branches to the workspace's `origin`. Also grants access to the carry-patch sync endpoint (`POST /api/v1/workspaces/:slug/sync` when the workspace is in `carry_patch` mode), as an alternative to `workspaces:sync`. Grants access to forget (delete) rerere resolutions. |
| **Endpoints** | `PATCH /api/v1/workspaces/:slug`, `POST /api/v1/workspaces/:slug/archive`, `POST /api/v1/workspaces/:slug/reactivate`, `POST /api/v1/workspaces/:slug/branches/:name/push`, `POST /api/v1/workspaces/:slug/sync` (carry-patch sync only), `DELETE /api/v1/workspaces/:slug/rerere/*pathspec`, `GET /api/v1/workspaces`, `GET /api/v1/workspaces/:slug` |
| **Implies** | `workspaces:read` (workspace CRUD endpoints only -- list and get workspaces; does NOT grant access to rerere list or patch-status which require the literal `workspaces:read` scope) |

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
| **Endpoints** | `GET /api/v1/workspaces/:slug/patches`, `GET /api/v1/workspaces/:slug/patches/:id` |
| **Implied by** | `patches:write` |
| **Ownership** | Enforced through `wsaccess` with the read level: the workspace's owner, a member of its org and an admin token may read; anyone else gets `404` (anti-enumeration, see [Workspace access levels](#workspace-access-levels)). |

### patches:write

| | |
|---|---|
| **Source** | hub |
| **Grants** | Add, remove, update, restore, reorder, and reset patches for a workspace |
| **Endpoints** | `POST /api/v1/workspaces/:slug/patches`, `PATCH /api/v1/workspaces/:slug/patches/:id`, `DELETE /api/v1/workspaces/:slug/patches/:id`, `POST /api/v1/workspaces/:slug/patches/:id/restore`, `POST /api/v1/workspaces/:slug/patches/:id/reset-to-origin`, `POST /api/v1/workspaces/:slug/patches/reorder` |
| **Implies** | `patches:read` |
| **Ownership** | Enforced through `wsaccess` with the write level: the workspace's owner or an admin token; a member of its org gets `403` with `error_type: forbidden`; anyone else gets `404`. The check runs before any git operation. |

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
| **Ownership** | Enforced through `wsaccess` with the read level: the workspace's owner, a member of its org and an admin token may read; anyone else gets `404` (anti-enumeration, see [Workspace access levels](#workspace-access-levels)). |

### merges:write

| | |
|---|---|
| **Source** | hub |
| **Grants** | Submit merge requests, cancel queued merge jobs, requeue dead-lettered merge jobs, and trigger batch rebase operations |
| **Endpoints** | `POST /api/v1/workspaces/:slug/merges`, `DELETE /api/v1/workspaces/:slug/merges/:id`, `POST /api/v1/workspaces/:slug/merges/:id/requeue`, `POST /api/v1/workspaces/:slug/rebase` |
| **Ownership** | Enforced through `wsaccess` with the write level: the workspace's owner or an admin token; a member of its org gets `403` with `error_type: forbidden`; anyone else gets `404`. Submit and rebase then need an active workspace with a ready clone (400). |

---

## Hub Rebuild Permissions

These 2 permissions are registered by `CarryPatchPermissions()` of the
carry-patch module (`carrypatch/wire.go`) and passed to `apikit.Server.MountHandlers()`
at startup via the `extraPerms` parameter.

### rebuilds:read

| | |
|---|---|
| **Source** | hub |
| **Grants** | List and view rebuild job status and history for carry-patch workspaces; preview rebuild conflict predictions |
| **Endpoints** | `GET /api/v1/workspaces/:slug/rebuilds`, `GET /api/v1/workspaces/:slug/rebuilds/:id`, `GET /api/v1/workspaces/:slug/rebuild-preview` |
| **Ownership** | Enforced through `wsaccess` with the read level: the workspace's owner, a member of its org and an admin token may read; anyone else gets `404` (anti-enumeration, see [Workspace access levels](#workspace-access-levels)). |

### rebuilds:write

| | |
|---|---|
| **Source** | hub |
| **Grants** | Submit rebuild jobs, cancel queued rebuild jobs, requeue dead-lettered rebuild jobs, and roll back the integration branch to a previous rebuild state for carry-patch workspaces |
| **Endpoints** | `POST /api/v1/workspaces/:slug/rebuild`, `DELETE /api/v1/workspaces/:slug/rebuilds/:id`, `POST /api/v1/workspaces/:slug/rebuilds/:id/requeue`, `POST /api/v1/workspaces/:slug/rebuilds/:id/rollback` |
| **Ownership** | Enforced through `wsaccess` with the write level: the workspace's owner or an admin token; a member of its org gets `403` with `error_type: forbidden`; anyone else gets `404`. Rebuild handlers then check workspace mode, status and clone status. |

---

## Hub Audit and Session Permissions

These 4 permissions are registered by `Permissions()` in
`hub/internal/audit/permissions.go` and passed to `apikit.Server.MountHandlers()`
at startup via the `extraPerms` parameter.

### sessions:read

| | |
|---|---|
| **Source** | hub |
| **Grants** | Read agent session records, token usage records, and workspace cost summaries |
| **Endpoints** | `GET /api/v1/sessions`, `GET /api/v1/sessions/:id`, `GET /api/v1/sessions/:id/usage`, `GET /api/v1/workspaces/:slug/cost` |
| **Ownership** | Non-admin callers see only sessions of active workspaces they own or whose org they belong to; any other workspace is `403 workspace access denied`. Admin tokens see all workspaces. |

### sessions:write

| | |
|---|---|
| **Source** | hub |
| **Grants** | Open, close, and report token usage on agent sessions |
| **Endpoints** | `POST /api/v1/sessions`, `POST /api/v1/sessions/:id/complete`, `POST /api/v1/sessions/:id/usage` |
| **Ownership** | Opening a session needs write access to the workspace: its owner or an admin token (an org member gets `403 forbidden`). Completing a session and reporting usage enforce that the caller is the session owner or an admin token. |

### audit:read

| | |
|---|---|
| **Source** | hub |
| **Grants** | Query audit events, traces, session outcomes, tool calls, tool errors, postmortems, unified audit queries, transcripts, and SSE event streams |
| **Endpoints** | `GET /api/v1/workspaces/:slug/runs/:run_id/events`, `GET /api/v1/workspaces/:slug/runs/:run_id/traces`, `GET /api/v1/workspaces/:slug/runs/:run_id/sessions/outcomes`, `GET /api/v1/workspaces/:slug/runs/:run_id/tools/calls`, `GET /api/v1/workspaces/:slug/runs/:run_id/tools/errors`, `GET /api/v1/workspaces/:slug/runs/:run_id/postmortem`, `GET /api/v1/audit`, `GET /api/v1/workspaces/:slug/runs/:run_id/transcript`, `GET /api/v1/events` |
| **Ownership** | The workspace's owner, a member of its org and admin tokens read a workspace's runs and transcripts (`404 workspace not found` otherwise); `GET /api/v1/audit` and `GET /api/v1/events` return non-admins the events of the workspaces they own or share an org with, and hub events without a workspace to admin tokens only. |

### audit:write

| | |
|---|---|
| **Source** | hub |
| **Grants** | Ingest audit events, traces, session outcomes, tool calls, tool errors, and postmortems |
| **Endpoints** | `POST /api/v1/workspaces/:slug/runs/:run_id/events`, `POST /api/v1/workspaces/:slug/runs/:run_id/events/batch`, `POST /api/v1/workspaces/:slug/runs/:run_id/traces`, `POST /api/v1/workspaces/:slug/runs/:run_id/traces/batch`, `POST /api/v1/workspaces/:slug/runs/:run_id/sessions/outcomes`, `POST /api/v1/workspaces/:slug/runs/:run_id/tools/calls`, `POST /api/v1/workspaces/:slug/runs/:run_id/tools/errors`, `POST /api/v1/workspaces/:slug/runs/:run_id/postmortem` |

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

These 4 permissions are defined in
`hub/internal/secrets/permissions.go` and included in `secrets.Permissions()`
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

## Hub Sandbox Permissions

These 2 permissions are registered by `Permissions()` in
`hub/internal/sandboxes/permissions.go` and passed to `apikit.Server.MountHandlers()`
at startup via the `extraPerms` parameter. They are registered on every start,
whether or not a `[sandbox]` section is configured, so PATs can be issued with
them before an operator enables sandboxes.

### sandboxes:read

| | |
|---|---|
| **Source** | hub |
| **Grants** | List the deployment's sandbox profiles (names, images, resources, network, TTL, secret names and env key names, never a value), and list and read sandbox leases |
| **Endpoints** | `GET /api/v1/sandbox-profiles`, `GET /api/v1/workspaces/:slug/sandboxes`, `GET /api/v1/workspaces/:slug/sandboxes/:id` |
| **Implied by** | `sandboxes:write` |
| **Ownership** | Profiles: none, every authorised caller sees the same list. Leases: the workspace is looked up through `wsaccess.AuthorizeWorkspace` with the read level: its owner, a member of its org and an admin token may list and get leases, with the same bodies; anyone else gets `404`. The scope check comes first, so a PAT without the scope gets `403` even for a workspace that does not exist. |
| **Note** | API keys and admin tokens are not scope-checked. Without a `[sandbox]` section every sandbox route answers `404` with `error_type: sandboxes_disabled` to every authenticated caller, before the scope is checked. `GET /api/v1/sandboxes` (every workspace's leases) needs an admin token: a PAT holding both scopes gets `403`. |

### sandboxes:write

| | |
|---|---|
| **Source** | hub |
| **Grants** | Issue sandbox leases and end them. The lease token is returned only in the response that issues the lease. |
| **Endpoints** | `POST /api/v1/workspaces/:slug/sandboxes`, `DELETE /api/v1/workspaces/:slug/sandboxes/:id` |
| **Implies** | `sandboxes:read` |
| **Ownership** | `wsaccess` with the write level: the workspace's owner or an admin token; a member of its org gets `403` with `error_type: forbidden`; anyone else gets `404`. Issuing needs an active workspace; ending works on an archived one. |

---

## Hub Forge Permissions

These 2 permissions are registered by `Permissions()` in
`hub/internal/forge/permissions.go` and passed to `apikit.Server.MountHandlers()`
at startup via the `extraPerms` parameter, on every start.

### forge:read

| | |
|---|---|
| **Source** | hub |
| **Grants** | Read a workspace's repository, issues, comments and pull requests (state, files, CI checks, reviews) on its forge, see how the hub reaches the forge and whether its webhook is set up, and list the webhook deliveries the hub received. The hub uses the workspace's own stored git credential; the response shows its kind, never its value. |
| **Endpoints** | `GET /api/v1/workspaces/:slug/forge`, `GET .../forge/deliveries`, `GET .../forge/repository`, `GET .../issues`, `GET .../issues/:n`, `GET .../issues/:n/comments`, `GET .../pulls/:n`, `GET .../pulls/:n/state`, `GET .../pulls/:n/files`, `GET .../pulls/:n/checks`, `GET .../pulls/:n/reviews` |
| **Implied by** | `forge:write` |
| **Ownership** | The workspace is looked up through `wsaccess.AuthorizeWorkspace` with the read level: its owner, a member of its org and an admin token may read, with the workspace's own forge credential; anyone else gets `404`. |
| **Note** | API keys and admin tokens are not scope-checked. The scope is checked before the workspace is looked up, so a PAT without it gets `403` also for a slug that does not exist. Reads work on archived workspaces and on workspaces that are not cloned. |

### forge:write

| | |
|---|---|
| **Source** | hub |
| **Grants** | Change a workspace's repository on its forge: create, update and close issues, comment on issues and pull requests, add and remove issue labels, make sure a repository label exists, and open pull requests. Also set and remove the secret the forge signs its webhook deliveries with. The hub uses the workspace's own stored git credential, never the caller's. |
| **Endpoints** | `POST /api/v1/workspaces/:slug/issues`, `PATCH .../issues/:n`, `POST .../issues/:n/close`, `POST .../issues/:n/comments`, `POST .../issues/:n/labels`, `DELETE .../issues/:n/labels/:name`, `POST .../labels`, `POST .../pulls`, `POST .../pulls/:n/comments`, `PUT .../forge/webhook`, `DELETE .../forge/webhook`; and every `forge:read` endpoint |
| **Implies** | `forge:read` |
| **Ownership** | `wsaccess` with the write level: the workspace's owner or an admin token; a member of its org gets `403` with `error_type: forbidden`; anyone else gets `404`. A write also needs the workspace to be active: an archived one is `400 workspace is not active`, after the access check. This holds for the nine write routes and for `PUT .../forge/webhook`; `DELETE .../forge/webhook` works on a workspace of any status. |
| **Note** | `forge:read` does not grant a write: a PAT holding only `forge:read` gets `403 PAT requires forge:write scope` on every write route, before the workspace is looked up. API keys and admin tokens are not scope-checked. A write that reaches the forge is recorded as a `hub.forge.*` audit event naming the caller; a request the hub refuses on its own is not. `PUT` and `DELETE .../forge/webhook` record `hub.forge.webhook.set` and `.unset` (never the secret). Neither scope is needed for the ingress, which is authenticated by the signature. |

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
| `sandboxes:write` | `sandboxes:read` |
| `forge:write` | `forge:read` |

\* The `workspaces:create` and `workspaces:write` implication of
`workspaces:read` is implemented via `hasReadAccess()` in workspace CRUD
handlers only. It applies to `GET /api/v1/workspaces` and
`GET /api/v1/workspaces/:slug`. It does NOT apply to rerere
(`GET /api/v1/workspaces/:slug/rerere`) or patch-status
(`GET /api/v1/workspaces/:slug/patch-status`), which check for the literal
`workspaces:read` scope string.
The `DELETE /api/v1/workspaces/:slug/rerere/*`
endpoint checks for the literal `workspaces:write` scope string.

---

## Complete Permission List

All 37 registered permission scopes, sorted alphabetically:

| # | Scope | Source | Resource | Action |
|---|-------|--------|----------|--------|
| 1 | `audit:read` | hub | audit | read |
| 2 | `audit:write` | hub | audit | write |
| 3 | `forge:read` | hub | forge | read |
| 4 | `forge:write` | hub | forge | write |
| 5 | `git:read` | hub | git | read |
| 6 | `git:write` | hub | git | write |
| 7 | `keys:manage` | apikit | keys | manage |
| 8 | `keys:read` | apikit | keys | read |
| 9 | `merges:read` | hub | merges | read |
| 10 | `merges:write` | hub | merges | write |
| 11 | `orgs:read` | apikit | orgs | read |
| 12 | `orgs:write` | apikit | orgs | write |
| 13 | `patches:read` | hub | patches | read |
| 14 | `patches:write` | hub | patches | write |
| 15 | `rebuilds:read` | hub | rebuilds | read |
| 16 | `rebuilds:write` | hub | rebuilds | write |
| 17 | `sandboxes:read` | hub | sandboxes | read |
| 18 | `sandboxes:write` | hub | sandboxes | write |
| 19 | `secrets:delete` | hub | secrets | delete |
| 20 | `secrets:list` | hub | secrets | list |
| 21 | `secrets:manage` | hub | secrets | manage |
| 22 | `secrets:write` | hub | secrets | write |
| 23 | `sessions:read` | hub | sessions | read |
| 24 | `sessions:write` | hub | sessions | write |
| 25 | `tokens:manage` | apikit | tokens | manage |
| 26 | `tokens:read` | apikit | tokens | read |
| 27 | `tokens:write` | apikit | tokens | write |
| 28 | `users:read` | apikit | users | read |
| 29 | `vars:delete` | hub | vars | delete |
| 30 | `vars:manage` | hub | vars | manage |
| 31 | `vars:read` | hub | vars | read |
| 32 | `vars:write` | hub | vars | write |
| 33 | `workspaces:create` | hub | workspaces | create |
| 34 | `workspaces:delete` | hub | workspaces | delete |
| 35 | `workspaces:read` | hub | workspaces | read |
| 36 | `workspaces:sync` | hub | workspaces | sync |
| 37 | `workspaces:write` | hub | workspaces | write |

---

## Secrets and Variables Endpoint Access Matrix

All secrets and variables endpoints follow the same credential-type
authorization model. The scope checks (`wsaccess.IsPAT`, `wsaccess.IsAdmin`, `wsaccess.HasScope`) are enforced at
the handler level in `hub/internal/secrets/handlers.go`.

### Credential Access by Scope

| Scope | Admin Token | API Key | PAT |
|-------|-------------|---------|-----|
| User-scoped (`/api/v1/user/secrets`, `/api/v1/user/vars`) | Full access | Full access (own user) | Requires explicit scope |
| Org-scoped (`/api/v1/orgs/:slug/secrets`, `/api/v1/orgs/:slug/vars`) | Full access (bypasses membership) | Full access (requires org membership) | Requires explicit scope + org membership |
| Workspace secrets (`/api/v1/workspaces/:slug/secrets`) | Full access (bypasses ownership) | Full access (requires workspace ownership; a member of its org gets `404`) | Requires explicit scope + workspace ownership |
| Workspace variables (`/api/v1/workspaces/:slug/vars`) | Full access (bypasses ownership) | Read (list, resolved) for the owner and members of its org; writes for the owner (a member gets `403 forbidden`) | Requires explicit scope + the same access |

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
  operations require org membership; workspace-scoped operations go through
  `wsaccess`: workspace secrets are private to the owner, workspace
  variables are readable by members of the workspace's org and writable by
  the owner.
- **PATs** require explicit scopes as listed above. Additionally, org-scoped
  operations require org membership and workspace-scoped operations require
  the same access as for an API key.

Non-admin credentials that fail ownership or membership checks receive
HTTP 404 (not 403) consistent with the anti-enumeration policy; the workspace
routes answer `404 workspace not found` in the apikit envelope. The one 403 is
a member's variable write (`org members have read-only access to this
workspace`, `error_type: forbidden`). A member's `GET .../vars/resolved` is the
owner's document: the owner's user tier, the org tier while the owner is a
member of the org, and the workspace tier.

---

## Workspace-Bound PATs

Hub can bind a PAT to exactly one workspace `S`, through a row in its SQLite
table `token_workspace_bindings`, keyed by the PAT's token id. API keys and
admin tokens cannot be bound.

**Who may mint and list bound tokens.** `POST /api/v1/workspaces/:slug/tokens`
is open to the owner of an active workspace with an API key, or with a PAT
holding `tokens:manage`; the token is minted by apikit's own route, so a PAT can
grant only scopes it holds. Admin tokens cannot mint (a token needs a user as
its owner). `GET /api/v1/workspaces/:slug/tokens` is open to the owner (a PAT
needs `tokens:read` or `tokens:manage`) and to admin tokens. Both routes need
the private access level: a member of the workspace's org gets the stranger's
`404 workspace not found`. Deleting a workspace revokes its bound tokens.

For a bound PAT:

- **Workspace-scoped routes** (path template with `workspaces/:slug`) answer
  `404 workspace not found` for every slug other than `S`. The check runs
  before the handler, so it comes before any scope check: to a bound PAT,
  other workspaces do not exist. On `S` the route's own scope and ownership
  checks decide, as for an unbound PAT of the same user and scopes.
- **Cross-workspace routes** that are allowed (`GET /workspaces`, the session
  routes, `GET /audit`, `GET /events`, `GET /sandbox-profiles`) see only `S`.
  For any other workspace they give the answer a non-owner gets.
- **Every other route** is refused with `403` and `error_type: token_bound`.
  This includes `POST /workspaces`, user and org secrets and variables,
  `GET /sandboxes`, unknown paths, and every route apikit mounts. So
  `tokens:manage` on a bound PAT cannot be used to mint an unbound PAT through
  `POST /user/tokens`.
- **Git** serves `S`'s repository under the `git:read`/`git:write` scopes and
  the token user's access (a member's bound PAT still cannot push), and
  answers a pkt-line `404 repository not found` for every other repository.
- **`tokens:*` scopes are inert.** A bound PAT can neither mint nor list bound
  tokens: on its own workspace's `/tokens` routes it gets `403 token_bound`, on
  any other workspace's the guard's `404 workspace not found`. It cannot reach
  apikit's token routes either.
- **Refusals are audited** as `hub.token.bound_refused`, at most once per token
  per minute.

The routes allowed to bound tokens are marked where they are registered
(`wsaccess.AllowBoundTokens`). A route nobody marks is refused, which is the
safe default. See `docs/api.md` (Workspace-Bound Tokens) for every answer.

## Anti-Enumeration Policy

When a PAT lacks the required workspace scope, or when the caller may not
read a workspace (neither its owner, nor a member of its org, nor an admin
token), the workspace CRUD endpoints (list, get, update, archive, reactivate,
delete) return HTTP 404 (not 403) to avoid disclosing the existence of
resources. A member of the workspace's org can see it exists, so a member's
write is HTTP 403 with `error_type: forbidden`. The workspace create endpoint returns HTTP 403
(`"PAT requires workspaces:create scope to create workspaces"`) because
there is no existing resource to enumerate. The git server returns HTTP 404
to a caller who may not read the workspace (`authorizeGitAccess` writes a
pkt-line 404 `"repository not found"`) to prevent workspace slug enumeration;
a member's push, by someone who already knows the workspace exists, is a
pkt-line 403 `"org members have read-only access to this workspace"`.

All workspace-scoped endpoint groups (sync, reclone, branch push, patches, merges,
rebuilds, rerere, patch-status, leases, forge, secrets, variables, bound
tokens, sessions, audit) return HTTP 404 for a workspace the caller may not
see, matching the CRUD endpoints, with the same body as for a slug that does
not exist. A
workspace-bound PAT gets the answer a non-owner gets for every workspace other
than its own.

Other endpoint groups return HTTP 403 for missing PAT scopes:
git scope check returns 403 with `"insufficient git permissions"`;
sync handler returns 403 with `"PAT requires workspaces:sync scope to sync workspaces"`;
reclone handler returns 403 with `"PAT requires workspaces:sync scope to reclone workspaces"`;
branch push handler returns 403 with `"PAT requires workspaces:write scope to push branches"`, before the workspace is looked up;
carry-patch sync handler returns 403 with `"missing required scope: workspaces:sync"`;
secrets and variables handlers return 403 with `"insufficient permission scope"`;
merge handlers return 403 with `"PAT requires merges:write scope"` (or
`merges:read`); rebuild handlers return 403 with `"missing required scope:
rebuilds:write"` (or `rebuilds:read`); patch handlers return 403 with
`"PAT requires patches:read scope"` (or `patches:write`); rerere list
handler returns 403 for missing `workspaces:read` scope; rerere forget
handler returns 403 for missing `workspaces:write` scope; patch-status
handler returns 403 for missing `workspaces:read` scope.

## Hub Audit Permissions

The `audit:read`, `audit:write`, `sessions:read`, and `sessions:write`
permissions are registered by `Permissions()` in
`hub/internal/audit/permissions.go`. The complete endpoint lists for
`audit:read` and `audit:write` are documented in the
[Hub Audit and Session Permissions](#audit-read) section above.

**Ownership notes:**

- **audit:read**: Enforced through `wsaccess`. A non-admin caller reads the
  runs and transcripts of the workspaces it owns or whose org it belongs to,
  of any status (`404 workspace not found` otherwise), and `GET /audit` and
  `GET /events` return it only those workspaces' events. Admin tokens read
  everything, hub events without a workspace included.
- **audit:write**: PATs and API keys must own the workspace (checked via
  SQLite; an org member gets `403 workspace_access_denied`), and a PAT bound to
  another workspace gets `404`. Admin tokens bypass all workspace checks.
- **sessions:read**: Non-admin users are restricted to sessions of active
  workspaces they own or whose org they belong to (checked via SQLite).

### sessions:write

| | |
|---|---|
| **Source** | hub |
| **Grants** | Open agent sessions, complete sessions, and report incremental token usage |
| **Endpoints** | `POST /api/v1/sessions`, `POST /api/v1/sessions/:id/complete`, `POST /api/v1/sessions/:id/usage` |
| **Ownership** | Session completion and usage reporting require the caller to be the session owner or an admin. |

---

## Extending the Permission Registry

To add custom permissions from a new hub module:

1. Define a function returning `[]apikit.Permission` (see `GitPermissions()`
   or `WorkspacePermissions()` for examples).
2. Pass the permissions to `MountWorkspaceHandlers()` via the `extraPerms`
   variadic parameter, or call `Server.MountHandlers()` directly.
3. Check the scope in your handlers using `auth.RequirePermission(c, resource, action)`
   (apikit-level) or the workspace package's `hasPermission()` helper.
