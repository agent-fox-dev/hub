# af-hub REST API Reference

This document covers all REST API endpoints provided by the af-hub service,
including workspace management endpoints and apikit-provided endpoints for
authentication, user management, API keys, tokens, organizations, and
administration.

## Authentication

All API endpoints require authentication via one of the following methods:

- **API Key**: A server-side credential associated with a user account. Passed
  in the `Authorization: Bearer <api-key>` header. API keys have full access
  to the owner's resources.
- **Personal Access Token (PAT)**: A scoped credential issued to a user for
  delegated API access. Passed in the `Authorization: Bearer <token>` header.
  PATs are restricted to the permission scopes granted at creation time.
- **Admin Token**: A special credential with cross-user administrative access.
  Passed in the `Authorization: Bearer <admin-token>` header.

Unauthenticated requests receive HTTP 401.

---

## Permission Scopes

PATs are granted one or more permission scopes that control which endpoints
they can access. The following scopes are available for workspace operations:

| Scope | Description | Authorized Endpoints |
|-------|-------------|---------------------|
| `workspaces:read` | List and view access to the PAT owner's workspaces | GET /api/v1/workspaces, GET /api/v1/workspaces/:slug, GET /api/v1/workspaces/:slug/rerere, DELETE /api/v1/workspaces/:slug/rerere/\*pathspec, GET /api/v1/workspaces/:slug/patch-status |
| `workspaces:create` | Create workspaces; implies read access | POST /api/v1/workspaces, GET /api/v1/workspaces, GET /api/v1/workspaces/:slug |
| `workspaces:write` | Update, archive, and reactivate workspaces; implies read access | PATCH /api/v1/workspaces/:slug, POST /api/v1/workspaces/:slug/archive, POST /api/v1/workspaces/:slug/reactivate, GET /api/v1/workspaces, GET /api/v1/workspaces/:slug |
| `workspaces:delete` | Delete archived workspaces owned by the PAT's user; does **not** imply read access | DELETE /api/v1/workspaces/:slug |
| `workspaces:sync` | Trigger upstream sync and reclone operations on workspaces | POST /api/v1/workspaces/:slug/sync, POST /api/v1/workspaces/:slug/reclone |
| `patches:read` | List and view patches for a workspace | GET /api/v1/workspaces/:slug/patches |
| `patches:write` | Add, remove, update, and reorder patches for a workspace; implies `patches:read` | POST /api/v1/workspaces/:slug/patches, PATCH /api/v1/workspaces/:slug/patches/:id, DELETE /api/v1/workspaces/:slug/patches/:id, POST /api/v1/workspaces/:slug/patches/reorder |
| `rebuilds:read` | View rebuild job status and history | GET /api/v1/workspaces/:slug/rebuilds, GET /api/v1/workspaces/:slug/rebuilds/:id |
| `rebuilds:write` | Submit rebuild jobs for carry-patch workspaces | POST /api/v1/workspaces/:slug/rebuild |
| `merges:read` | List and view merge job status | GET /api/v1/workspaces/:slug/merges, GET /api/v1/workspaces/:slug/merges/:id |
| `merges:write` | Submit and cancel merge jobs; trigger batch rebase | POST /api/v1/workspaces/:slug/merges, DELETE /api/v1/workspaces/:slug/merges/:id, POST /api/v1/workspaces/:slug/rebase |
| `git:read` | Clone and fetch access to workspace repositories via the git server | GET /git/:org/:slug.git/info/refs, POST /git/:org/:slug.git/git-upload-pack |
| `git:write` | Push access to workspace repositories via the git server; implies `git:read` | POST /git/:org/:slug.git/git-receive-pack (plus all `git:read` endpoints) |
| `secrets:manage` | Full CRUD access to secrets; implies `secrets:list`, `secrets:write`, and `secrets:delete` | POST, GET, PATCH, DELETE on /api/v1/user/secrets, /api/v1/orgs/:slug/secrets, /api/v1/workspaces/:slug/secrets |
| `secrets:list` | List access to secret names (values never returned) | GET /api/v1/user/secrets, GET /api/v1/orgs/:slug/secrets, GET /api/v1/workspaces/:slug/secrets |
| `secrets:write` | Update existing secret values | PATCH /api/v1/user/secrets/:key, PATCH /api/v1/orgs/:slug/secrets/:key, PATCH /api/v1/workspaces/:slug/secrets/:key |
| `secrets:delete` | Delete secrets | DELETE /api/v1/user/secrets/:key, DELETE /api/v1/orgs/:slug/secrets/:key, DELETE /api/v1/workspaces/:slug/secrets/:key |
| `vars:manage` | Full CRUD access to variables; implies `vars:read`, `vars:write`, and `vars:delete` | POST, GET, PATCH, DELETE on /api/v1/user/vars, /api/v1/orgs/:slug/vars, /api/v1/workspaces/:slug/vars |
| `vars:read` | Read and list variable values | GET /api/v1/user/vars, GET /api/v1/orgs/:slug/vars, GET /api/v1/workspaces/:slug/vars, GET /api/v1/workspaces/:slug/vars/resolved |
| `vars:write` | Update existing variable values; implies `vars:read` | PATCH + all `vars:read` endpoints |
| `vars:delete` | Delete variables | DELETE /api/v1/user/vars/:key, DELETE /api/v1/orgs/:slug/vars/:key, DELETE /api/v1/workspaces/:slug/vars/:key |

### Implied Permissions

- `workspaces:create` implies `workspaces:read` — a PAT with create scope can
  also list and view workspaces.
- `workspaces:write` implies `workspaces:read` — a PAT with write scope can
  also list and view workspaces.
- `workspaces:delete` does **not** imply read access — a PAT with only
  `workspaces:delete` cannot list or view workspaces.
- `patches:write` implies `patches:read` — a PAT with patches write scope can
  also list patches.
- `git:write` implies `git:read` — a PAT with git write scope can also clone
  and fetch.
- `secrets:manage` implies `secrets:list`, `secrets:write`, and
  `secrets:delete` — a PAT with manage scope has full CRUD access to secrets.
- `vars:manage` implies `vars:read`, `vars:write`, and `vars:delete` — a PAT
  with manage scope has full CRUD access to variables.
- `vars:write` implies `vars:read` — a PAT with write scope can also list and
  read variable values.

### Anti-Enumeration Policy

When a PAT lacks the required scope for an endpoint, or the requested
workspace is not owned by the PAT's user, the API returns HTTP 404 (not 403)
to avoid disclosing the existence of resources.

---

## Workspace Response Schema

All workspace endpoints that return workspace data use the following JSON
schema:

```json
{
  "slug": "my-project",
  "git_url": "https://github.com/user/repo.git",
  "hub_url": "http://localhost:8080/git/org-slug/my-project.git",
  "branch": "main",
  "owner_id": "uuid-string",
  "org_id": "uuid-string-or-null",
  "status": "active",
  "display_name": "My Project",
  "description": "A description of the workspace",
  "clone_status": "ready",
  "head_sha": "abc123def456...",
  "clone_error": null,
  "sync_mode": "pull_only",
  "sync_status": "idle",
  "upstream_head_sha": null,
  "last_sync_at": null,
  "sync_error": null,
  "workspace_mode": "standard",
  "upstream_url": null,
  "integration_branch": null,
  "created_at": "2024-01-01T00:00:00Z",
  "updated_at": "2024-01-01T00:00:00Z"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `slug` | string | Immutable globally unique URL-safe identifier |
| `git_url` | string | HTTPS or SSH URL of the git repository; immutable after creation |
| `hub_url` | string or null | Git clone URL on the hub built-in git server (e.g. `"http://localhost:8080/git/org-slug/workspace-slug.git"`). Null when external_url is not configured or the workspace has no organization. |
| `branch` | string or null | Git ref associated with the workspace; immutable after creation |
| `owner_id` | string (UUID) | User who owns the workspace |
| `org_id` | string (UUID) or null | Organization the workspace is associated with; nullable |
| `status` | string | Lifecycle state: `"active"` or `"archived"` |
| `display_name` | string | Human-readable label; defaults to slug value when not set; never null or empty |
| `description` | string | Free-form text describing the workspace; defaults to empty string; never null |
| `clone_status` | string | Clone lifecycle state: `"pending"`, `"cloning"`, `"ready"`, `"failed"`, or `"archived"` |
| `head_sha` | string or null | 40-character hex SHA of the HEAD commit. Set when clone_status is `"ready"` or `"archived"`. Null otherwise. |
| `clone_error` | string or null | Error message when clone_status is `"failed"`. Null otherwise. |
| `sync_mode` | string | Upstream sync mode: `"pull_only"` (default) or `"disabled"` |
| `sync_status` | string | Current sync state: `"idle"` (default), `"syncing"`, or `"error"` |
| `upstream_head_sha` | string or null | HEAD SHA of the upstream tracking branch at last fetch. Null until first sync. |
| `last_sync_at` | string (RFC 3339) or null | Timestamp of the last successful sync. Null until first sync. |
| `sync_error` | string or null | Error message from the most recent failed sync. Null when no error. |
| `workspace_mode` | string | Workspace operating mode: `"standard"` (default, single-remote) or `"carry_patch"` (dual-remote with patch list); immutable after creation |
| `upstream_url` | string or null | URL of the upstream repository for carry-patch workspaces. Null for standard workspaces; immutable after creation. |
| `integration_branch` | string or null | Name of the mechanically rebuilt branch that combines the upstream base with all carried patches. Defaults to `"deploy"` for carry-patch workspaces. Null for standard workspaces; immutable after creation. |
| `created_at` | string (RFC 3339) | Timestamp of workspace creation; immutable |
| `updated_at` | string (RFC 3339) | Timestamp of last modification |

### Error Response Schema

Error responses use the apikit error envelope format:

```json
{
  "error": {
    "code": 400,
    "message": "description of the error"
  }
}
```

---

## Workspace Endpoints

### POST /api/v1/workspaces

Create a new workspace.

**Authentication:** API Key, or PAT with `workspaces:create` scope.
Admin tokens are forbidden from creating workspaces (returns 403) because a
workspace requires a real user as owner.

**Request Body:**

```json
{
  "slug": "my-project",
  "git_url": "https://github.com/user/repo.git",
  "branch": "main",
  "org_id": "uuid-string",
  "display_name": "My Project",
  "description": "A description of the workspace",
  "sync_mode": "pull_only",
  "workspace_mode": "standard",
  "upstream_url": "https://github.com/upstream/repo.git",
  "integration_branch": "deploy",
  "git_pat": "ghp_xxxxxxxxxxxx",
  "git_username": "user",
  "git_password": "token-or-password"
}
```

| Field | Required | Type | Constraints |
|-------|----------|------|-------------|
| `slug` | yes | string | Globally unique, URL-safe identifier |
| `git_url` | yes | string | Valid HTTPS or SSH git URL |
| `branch` | no | string | Git ref; defaults to null |
| `org_id` | no | string (UUID) | Must reference an org the owner is a member of; when omitted or empty, the server auto-assigns the user's personal organization |
| `display_name` | no | string | Max 128 characters; defaults to slug value if omitted or empty |
| `description` | no | string | Max 1024 characters; defaults to empty string if omitted |
| `sync_mode` | no | string | Upstream sync mode: `"pull_only"` (default) or `"disabled"`; invalid values are rejected with HTTP 400 |
| `workspace_mode` | no | string | `"standard"` (default) or `"carry_patch"`; when `"carry_patch"`, `upstream_url` is required and `integration_branch` defaults to `"deploy"` |
| `upstream_url` | conditional | string | Required when `workspace_mode` is `"carry_patch"`; must be a valid HTTPS or SSH git URL; rejected for standard workspaces |
| `integration_branch` | no | string | Name of the integration branch; defaults to `"deploy"` for carry-patch workspaces; rejected for standard workspaces |
| `git_pat` | no | string | Personal access token for private repo auth; mutually exclusive with `git_username`/`git_password`; requires HTTPS `git_url` |
| `git_username` | no | string | Git username for HTTP basic auth; must be paired with `git_password`; mutually exclusive with `git_pat`; requires HTTPS `git_url` |
| `git_password` | no | string | Git password for HTTP basic auth; must be paired with `git_username`; mutually exclusive with `git_pat`; requires HTTPS `git_url` |

**Auto-Org Assignment:** When `org_id` is omitted or empty, the server
automatically assigns the workspace to the user's personal organization. If
the user has no personal organization, the server returns HTTP 400.

**Response:** HTTP 201 Created with workspace JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing required fields (`slug`, `git_url`), or `display_name` exceeds 128 characters, or `description` exceeds 1024 characters |
| 400 | `git_pat` and `git_username`/`git_password` provided together (mutually exclusive) |
| 400 | `git_username` provided without `git_password` or vice versa |
| 400 | Git credential values are empty strings |
| 400 | Git credentials provided with non-HTTPS `git_url` |
| 400 | Credential validation failed (ls-remote check fails) |
| 400 | User has no personal organization (when `org_id` is omitted) |
| 401 | Unauthenticated request |
| 403 | Admin token attempted to create a workspace; PAT lacks `workspaces:create` scope |
| 409 | A workspace with the given `slug` already exists |
| 500 | Internal server error (e.g., database error, org membership check failure, credential storage failure) |

---

### GET /api/v1/workspaces

List workspaces accessible to the authenticated user.

**Authentication:** API Key, or PAT with read access (`workspaces:read`,
`workspaces:create`, or `workspaces:write`). Admin tokens list all workspaces.

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `include_archived` | boolean | `false` | When `true`, includes archived workspaces in the listing |

**Response:** HTTP 200 OK with a JSON array of workspace objects.

- Non-admin users see only their own workspaces.
- Admin users see all workspaces.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 404 | PAT lacks read access (anti-enumeration) |

---

### GET /api/v1/workspaces/:slug

Get a single workspace by slug.

**Authentication:** API Key, or PAT with read access (`workspaces:read`,
`workspaces:create`, or `workspaces:write`). Admin tokens can access any
workspace.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to retrieve |

**Response:** HTTP 200 OK with workspace JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 404 | Workspace not found, or PAT lacks read access, or workspace not owned by the authenticated user (anti-enumeration) |

---

### PATCH /api/v1/workspaces/:slug

Update mutable properties of an existing workspace. This endpoint uses
**partial update semantics**: only fields included in the request body are
modified. Omitted fields retain their current values.

**Authentication:** API Key, or PAT with `workspaces:write` scope. Admin
tokens can update any workspace.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to update |

**Request Body:**

The request body is a JSON object containing one or more of the following
mutable fields. At least one field must be provided.

```json
{
  "display_name": "New Display Name",
  "description": "Updated description",
  "org_id": "uuid-string",
  "sync_mode": "disabled"
}
```

| Field | Type | Constraints | Null Behavior |
|-------|------|-------------|---------------|
| `display_name` | string or null | Max 128 characters | Setting to `null` clears the display name back to the workspace slug |
| `description` | string or null | Max 1024 characters | Setting to `null` clears the description to an empty string |
| `org_id` | string (UUID) or null | Must reference an org the owner is a member of | Setting to `null` removes the organization association |
| `sync_mode` | string | Must be `"pull_only"` or `"disabled"` | Setting to `null` is rejected with HTTP 400 |

**Partial Update Behavior:**

- Only explicitly provided fields are updated; omitted fields are not modified.
- Immutable fields (`slug`, `git_url`, `branch`, `owner_id`, `workspace_mode`,
  `upstream_url`, `integration_branch`) cannot be set via this endpoint.
  Including an immutable field in the request body returns HTTP 400.
- The `updated_at` timestamp is automatically advanced on every successful
  update.

**Response:** HTTP 200 OK with the full updated workspace JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Empty body (no fields provided); workspace is archived (must reactivate first); `display_name` exceeds 128 characters; `description` exceeds 1024 characters; request includes immutable fields; `sync_mode` value is invalid or null |
| 401 | Unauthenticated request |
| 403 | Workspace owner is not a member of the organization specified in `org_id` |
| 404 | Workspace not found; PAT lacks `workspaces:write` scope; workspace not owned by the authenticated user (anti-enumeration) |
| 500 | Internal server error (e.g., org membership check timeout or failure) |

---

### POST /api/v1/workspaces/:slug/archive

Archive a workspace. Archived workspaces are read-only; all state is
preserved and the workspace can be reactivated later.

**Authentication:** API Key, or PAT with `workspaces:write` scope. Admin
tokens can archive any workspace.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to archive |

**Response:** HTTP 200 OK with the updated workspace JSON (status = `"archived"`).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Workspace is already archived |
| 401 | Unauthenticated request |
| 404 | Workspace not found; PAT lacks `workspaces:write` scope; workspace not owned by the authenticated user (anti-enumeration) |
| 409 | Clone is in progress; archive is rejected until the clone completes or fails |

---

### POST /api/v1/workspaces/:slug/reactivate

Reactivate an archived workspace, restoring it to active status.

**Authentication:** API Key, or PAT with `workspaces:write` scope. Admin
tokens can reactivate any workspace.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to reactivate |

**Response:** HTTP 200 OK with the updated workspace JSON (status = `"active"`).

On reactivation, `clone_status` is reset to `"pending"` and a reclone job is
enqueued.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 404 | Workspace not found; PAT lacks `workspaces:write` scope; workspace not owned by the authenticated user (anti-enumeration) |
| 409 | Workspace is not archived |

---

### POST /api/v1/workspaces/:slug/sync

Trigger an upstream sync operation that fetches from the remote repository and
fast-forwards the local integration branch if possible. If a force-push is
detected (upstream history has diverged), the sync sets an error state with
instructions to use the `--reset-to-upstream` flag for recovery.

**Authentication:** API Key, or PAT with `workspaces:sync` scope. Admin
tokens can sync any workspace.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to sync |

**Preconditions:**

The sync operation requires all of the following:

- Workspace status is `"active"` (not archived)
- Clone status is `"ready"` (clone complete)
- Sync mode is not `"disabled"`
- Sync status is not `"syncing"` (no concurrent syncs)

**Sync Outcomes:**

| Outcome | Description | HTTP Status |
|---------|-------------|-------------|
| Up to date | Upstream HEAD equals local HEAD; `last_sync_at` is updated | 200 |
| Fast-forward | Upstream HEAD is a descendant of local HEAD; `head_sha` is advanced to upstream HEAD | 200 |
| Diverged | Upstream HEAD is not a descendant of local HEAD (force-push detected); `sync_status` set to `"error"` | 409 |
| Fetch failure | Network error, auth failure, or repo open failure; `sync_status` set to `"error"` | 502 |
| Context cancelled | Request cancelled mid-sync (timeout or client disconnect); `sync_status` set to `"error"` | 504 |

**Response (success):** HTTP 200 OK with updated workspace JSON. The response
includes updated `sync_status`, `head_sha`, `upstream_head_sha`, and
`last_sync_at` fields.

**State Machine:**

The `sync_status` field follows a strict state machine:

- `idle` → `syncing` → `idle` (successful sync)
- `idle` → `syncing` → `error` (failed sync)
- `error` → `syncing` → `idle` (recovery after operator resolution)

The handler guarantees that `sync_status` never remains stuck in `"syncing"`:
a deferred cleanup function transitions to `"error"` on context cancellation
or unexpected failures.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Workspace is not active; clone is not ready; sync is disabled |
| 401 | Unauthenticated request |
| 403 | PAT lacks `workspaces:sync` scope |
| 404 | Workspace not found |
| 409 | Sync already in progress (concurrent sync rejected); upstream history has diverged (force-push detected) |
| 502 | Upstream fetch failed (network, authentication, or repository error); credential resolution failed |
| 504 | Request context cancelled mid-sync (timeout or client disconnect) |

#### Carry-Patch Sync Extension

When the workspace is in `carry_patch` mode, the sync endpoint extends the
standard behavior with upstream merge detection and automatic rebuild
triggering. The response includes additional fields:

```json
{
  "patches_merged": ["feature/already-merged"],
  "rebuild_triggered": true
}
```

| Field | Type | Description |
|-------|------|-------------|
| `patches_merged` | string[] | Branch names of patches detected as merged upstream via ancestry check |
| `rebuild_triggered` | boolean | Whether a rebuild job was enqueued as a result of this sync |

**Carry-Patch Sync Flow:**

1. Resolve upstream credentials via `resolveUpstreamAuth`.
2. Fetch from the `upstream` remote (not `origin`).
3. Compare the new upstream HEAD against the stored `upstream_head_sha`.
4. If unchanged, return immediately with `patches_merged=[]` and
   `rebuild_triggered=false`.
5. For each active patch, check if its branch HEAD is an ancestor of the new
   upstream HEAD using `IsAncestor`. If true, transition the patch status to
   `merged_upstream`.
6. If `AUTO_REBUILD_AFTER_SYNC` is `true` (default) and the upstream ref
   advanced or patches were merged, enqueue a rebuild job. If a rebuild job
   is already queued or running, silently ignore the duplicate
   (`rebuild_triggered=false`).

**Standard Workspace Behavior:** When the workspace is in `standard` mode, the
carry-patch-specific fields (`patches_merged`, `rebuild_triggered`) are omitted
from the response.

---

### POST /api/v1/workspaces/:slug/reclone

Nuclear recovery operation that archives the workspace (pushing local commits
to upstream first), deletes the local clone directory, and re-clones from
upstream. The workspace status remains `"active"` throughout the entire reclone
lifecycle.

**Authentication:** API Key, or PAT with `workspaces:sync` scope. Admin
tokens can reclone any workspace.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to reclone |

**Preconditions:**

- Workspace must exist
- Clone status must not be `"pending"` or `"cloning"` (no concurrent reclone)

**Reclone Steps:**

1. Execute the archive flow: attempt to push local commits to upstream. If push
   fails (no credentials, remote rejection, or network error), a warning is
   logged and the reclone continues.
2. Delete the local clone directory at `<WORKSPACE_ROOT>/<slug>/`.
3. Atomically update database: `clone_status='pending'`, `sync_status='idle'`,
   clear `sync_error` and `upstream_head_sha`. Workspace `status` remains
   `'active'`.
4. Enqueue a clone job via the in-memory job queue. The clone lifecycle
   transitions `clone_status` from `'pending'` → `'cloning'` → `'ready'`.

**Response (success):** HTTP 200 OK with workspace JSON:

```json
{
  "slug": "my-workspace",
  "status": "active",
  "clone_status": "pending",
  "sync_status": "idle",
  "sync_error": null,
  "upstream_head_sha": null
}
```

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `workspaces:sync` scope |
| 404 | Workspace not found |
| 409 | Clone operation already in progress (`clone_status` is `"pending"` or `"cloning"`) |
| 500 | Failed to delete workspace directory; database update failed |

---

### DELETE /api/v1/workspaces/:slug

Permanently delete a workspace. Only archived workspaces can be deleted.

**Authentication:** API Key, or PAT with `workspaces:delete` scope. Admin
tokens can delete any workspace.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to delete |

**Response:** HTTP 204 No Content on successful deletion.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 404 | Workspace not found; PAT lacks `workspaces:delete` scope; workspace not owned by the authenticated user (anti-enumeration) |
| 409 | Workspace is not archived (must archive before deleting) |

---

## Patch Endpoints

Patch endpoints manage the ordered list of patch branches for carry-patch
workspaces. Patches represent git branches that are applied on top of the
upstream base when rebuilding the integration branch.

### Patch Response Schema

All patch endpoints that return patch data use the following JSON schema:

```json
{
  "id": "uuid-string",
  "workspace_slug": "my-workspace",
  "branch_name": "feature/auth-headers",
  "position": 1,
  "status": "active",
  "upstream_pr_url": "https://github.com/upstream/repo/pull/42",
  "description": null,
  "added_at": "2024-01-01T00:00:00Z",
  "updated_at": "2024-01-01T00:00:00Z"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `id` | string (UUID) | Unique identifier for the patch |
| `workspace_slug` | string | Slug of the workspace this patch belongs to |
| `branch_name` | string | Name of the git branch associated with this patch |
| `position` | integer | 1-based application order; lower values are applied first |
| `status` | string | Patch status: `"active"`, `"merged_upstream"`, `"conflict"`, or `"disabled"` |
| `upstream_pr_url` | string or null | URL of the corresponding upstream pull request |
| `description` | string or null | Free-form description of the patch |
| `added_at` | string (RFC 3339) | Timestamp of when the patch was added |
| `updated_at` | string (RFC 3339) | Timestamp of when the patch was last modified |

---

### POST /api/v1/workspaces/:slug/patches

Add a patch branch to a carry-patch workspace's patch list.

**Authentication:** API Key, or PAT with `patches:write` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to add the patch to |

**Request Body:**

```json
{
  "branch_name": "feature/auth-headers",
  "position": 2,
  "upstream_pr_url": "https://github.com/upstream/repo/pull/42",
  "description": "Add authentication headers to API"
}
```

| Field | Required | Type | Constraints |
|-------|----------|------|-------------|
| `branch_name` | yes | string | Must not be empty; must not equal the workspace's `integration_branch`; must not already exist in the patch list for this workspace |
| `position` | no | integer | 1-based insertion position; must be >= 1; values > (max position + 1) are clamped to append; when omitted, appends at the end |
| `upstream_pr_url` | no | string | URL of the upstream pull request |
| `description` | no | string | Free-form description |

**Response:** HTTP 201 Created with patch JSON.

**Behavior:**

- When `position` is omitted, the patch is appended at position = (current max
  position + 1).
- When `position` is specified, existing patches at that position or higher are
  shifted down by one to make room.
- Branch existence in the git repository is NOT validated at add time — that
  validation occurs at rebuild time.
- The patch is assigned a UUID, status `"active"`, and RFC 3339 timestamps.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | `branch_name` is absent or empty |
| 400 | `branch_name` equals the workspace's `integration_branch` |
| 400 | Workspace is in `standard` mode (not `carry_patch`) |
| 400 | Workspace does not exist or is not active |
| 400 | `position` is less than 1 |
| 401 | Unauthenticated request |
| 403 | PAT lacks `patches:write` scope |
| 409 | `branch_name` already exists in the patch list for this workspace |

---

### GET /api/v1/workspaces/:slug/patches

List all patches for a workspace, ordered by position ascending.

**Authentication:** API Key, or PAT with `patches:read` or `patches:write`
scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to list patches for |

**Response:** HTTP 200 OK with a JSON array of patch objects ordered by
position. Returns an empty array `[]` if no patches exist.

**Behavior:**

- For carry-patch workspaces, returns all patches in position order.
- For standard workspaces, returns an empty array (not an error).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `patches:read` scope |
| 404 | Workspace does not exist |

---

### PATCH /api/v1/workspaces/:slug/patches/:id

Update a patch's position, status, description, or upstream PR URL.

**Authentication:** API Key, or PAT with `patches:write` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |
| `:id` | The patch UUID to update |

**Request Body:**

```json
{
  "position": 1,
  "status": "disabled",
  "description": "Updated description",
  "upstream_pr_url": "https://github.com/upstream/repo/pull/99"
}
```

| Field | Required | Type | Constraints |
|-------|----------|------|-------------|
| `position` | no | integer | 1-based; must be >= 1 and <= total patch count for the workspace |
| `status` | no | string | Must be one of: `"active"`, `"merged_upstream"`, `"conflict"`, `"disabled"` |
| `description` | no | string | Free-form description |
| `upstream_pr_url` | no | string | URL of the upstream pull request |

**Response:** HTTP 200 OK with the updated patch JSON object.

**Behavior:**

- Only the provided fields are updated; omitted fields remain unchanged.
- `branch_name` is immutable after creation — if included in the request body,
  it is silently ignored.
- When `position` changes, other patches in the workspace are shifted to
  maintain a contiguous 1-based sequence with no gaps or duplicates.
- `updated_at` is set to the current RFC 3339 timestamp on every successful
  update.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | `status` is not one of the valid values |
| 400 | `position` is less than 1 or greater than the total number of patches |
| 401 | Unauthenticated request |
| 403 | PAT lacks `patches:write` scope |
| 404 | Patch ID does not exist for the given workspace |

---

### DELETE /api/v1/workspaces/:slug/patches/:id

Remove a patch from the workspace's patch list.

**Authentication:** API Key, or PAT with `patches:write` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |
| `:id` | The patch UUID to remove |

**Response:** HTTP 204 No Content (empty body).

**Behavior:**

- Deletes the patch row from the patches table.
- Compacts positions for remaining patches so there are no gaps — positions are
  reassigned as a contiguous 1-based sequence.
- The delete and position compaction are performed atomically within a single
  database transaction.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `patches:write` scope |
| 404 | Patch ID does not exist for the given workspace |
| 500 | Database transaction failure during delete and compaction (no partial state persisted) |

---

### POST /api/v1/workspaces/:slug/patches/reorder

Reorder the entire patch list by providing a complete ordered list of patch IDs.

**Authentication:** API Key, or PAT with `patches:write` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |

**Request Body:**

```json
{
  "patch_ids": ["uuid-3", "uuid-1", "uuid-2"]
}
```

| Field | Required | Type | Constraints |
|-------|----------|------|-------------|
| `patch_ids` | yes | string[] | Must contain exactly all patch IDs for the workspace — no missing, no extra, no duplicates |

**Response:** HTTP 200 OK with a JSON array of all patch objects in the new
position order.

**Behavior:**

- Assigns new 1-based positions to each patch based on array index (first
  element gets position 1, second gets position 2, etc.).
- Updates `updated_at` for each modified row.
- The entire operation is performed within a single database transaction.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | `patch_ids` is empty but the workspace has patches |
| 400 | `patch_ids` is missing one or more patch IDs that exist for the workspace |
| 400 | `patch_ids` contains duplicate IDs |
| 400 | `patch_ids` contains an ID that does not belong to the specified workspace |
| 401 | Unauthenticated request |
| 403 | PAT lacks `patches:write` scope |
| 500 | Database transaction failure during bulk position update (no positions changed) |

---

## Rerere Management Endpoints

Rerere endpoints let operators inspect and manage recorded git rerere
(reuse-recorded-resolution) entries for a carry-patch workspace. Resolutions
are stored in the `.git/rr-cache/` directory of the workspace clone.

### GET /api/v1/workspaces/:slug/rerere

List all recorded rerere resolutions for a workspace.

**Authentication:** API Key, or PAT with `workspaces:read` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |

**Response (success):** HTTP 200 OK

```json
{
  "resolutions": [
    {
      "path": "src/config.go",
      "recorded_at": "2024-06-15T10:30:00Z"
    },
    {
      "path": "pkg/handler.go",
      "recorded_at": "2024-06-14T08:15:00Z"
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `resolutions` | array | List of recorded rerere resolutions |
| `resolutions[].path` | string or null | File path derived from the preimage/postimage conflict marker |
| `resolutions[].recorded_at` | string (RFC 3339) or null | Timestamp derived from the file modification time of the rr-cache entry |

**Edge Cases:**

- If the `rr-cache` directory does not exist or is empty, returns `{"resolutions": []}`.
- Malformed rr-cache subdirectories (no preimage or postimage file) are silently skipped.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `workspaces:read` scope |
| 404 | Workspace not found |

---

### DELETE /api/v1/workspaces/:slug/rerere/\*pathspec

Forget a specific recorded rerere resolution by executing `git rerere forget <pathspec>`.

**Authentication:** API Key, or PAT with `workspaces:read` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |
| `*pathspec` | The file path to forget (wildcard parameter captures slashes, e.g. `src/config.go`) |

**Response (success):** HTTP 204 No Content

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Empty pathspec |
| 401 | Unauthenticated request |
| 403 | PAT lacks `workspaces:read` scope |
| 404 | Workspace not found; no recorded resolution for the given pathspec |

---

## Rebuild Endpoints

Rebuild endpoints allow operators to submit, list, and inspect rebuild jobs
for carry-patch workspaces. A rebuild reconstructs the integration branch
from upstream HEAD plus all active patches.

### POST /api/v1/workspaces/:slug/rebuild

Submit a new rebuild job for the workspace.

**Required scope:** `rebuilds:write`

**Preconditions:**

- Workspace must be in `carry_patch` mode
- Workspace status must be `active` with clone_status `ready`
- At least one patch must have status `active` or `conflict`
- No existing rebuild job may be queued or running for this workspace

**Response (202 Accepted):**

```json
{
  "id": "<uuid>",
  "type": "rebuild",
  "status": "queued",
  "key": "<workspace-slug>",
  "group_key": "<workspace-slug>:<integration-branch>",
  "payload": {
    "workspace_slug": "<slug>",
    "strategy": "rebase"
  }
}
```

| Status | Meaning |
|--------|---------|
| 202 | Job enqueued successfully |
| 400 | Workspace not in carry_patch mode, not active, clone not ready, or no active patches |
| 403 | Missing required scope `rebuilds:write` |
| 409 | A rebuild job is already queued or running for this workspace |

### GET /api/v1/workspaces/:slug/rebuilds

List rebuild jobs for a workspace, ordered by creation time descending.

**Required scope:** `rebuilds:read`

**Response (200 OK):**

```json
{
  "jobs": [
    {
      "id": "<uuid>",
      "status": "completed",
      "strategy": "rebase",
      "created_at": "<rfc3339>",
      "completed_at": "<rfc3339>",
      "error": null
    }
  ]
}
```

### GET /api/v1/workspaces/:slug/rebuilds/:id

Get a single rebuild job by ID, including `patch_results` for completed jobs.

**Required scope:** `rebuilds:read`

**Response (200 OK):**

```json
{
  "id": "<uuid>",
  "status": "completed",
  "strategy": "rebase",
  "created_at": "<rfc3339>",
  "completed_at": "<rfc3339>",
  "patch_results": [
    {
      "patch_id": "<uuid>",
      "branch_name": "feature/foo",
      "position": 1,
      "status": "success",
      "new_head_sha": "<sha>"
    }
  ]
}
```

| Status | Meaning |
|--------|---------|
| 200 | Job found |
| 403 | Missing required scope `rebuilds:read` |
| 404 | Job not found or does not belong to this workspace |

---

## Patch Status Dashboard Endpoint

The patch-status endpoint provides a comprehensive health summary of the
carry-patch stack for a workspace, aggregating workspace metadata, the most
recent rebuild result, per-patch status with rerere resolution counts, and
summary counts.

### GET /api/v1/workspaces/:slug/patch-status

Return a full status dashboard for the carry-patch stack.

**Authentication:** API Key, or PAT with `workspaces:read` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |

**Response (success):** HTTP 200 OK

```json
{
  "workspace_slug": "my-workspace",
  "workspace_mode": "carry_patch",
  "upstream_url": "https://github.com/upstream/repo.git",
  "upstream_head_sha": "abc123def456...",
  "integration_branch": "integration",
  "integration_head_sha": "def456abc123...",
  "last_sync_at": "2024-06-15T10:30:00Z",
  "last_rebuild": {
    "id": "uuid-string",
    "status": "completed"
  },
  "patches": [
    {
      "id": "uuid-string",
      "branch_name": "feature/patch-a",
      "position": 1,
      "status": "active",
      "last_rebuild_result": "success",
      "rerere_resolution_count": 0
    },
    {
      "id": "uuid-string",
      "branch_name": "feature/conflict",
      "position": 2,
      "status": "conflict",
      "last_rebuild_result": "conflict",
      "conflict_files": ["pkg/api.go"],
      "rerere_resolution_count": 1
    }
  ],
  "summary": {
    "total_patches": 2,
    "active": 1,
    "merged_upstream": 0,
    "conflict": 1,
    "disabled": 0
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `workspace_slug` | string | The workspace slug |
| `workspace_mode` | string | Always `"carry_patch"` |
| `upstream_url` | string | The upstream repository URL |
| `upstream_head_sha` | string | SHA of the upstream HEAD at last fetch |
| `integration_branch` | string | Name of the integration branch |
| `integration_head_sha` | string | SHA of the integration branch HEAD after last successful rebuild |
| `last_sync_at` | string (RFC 3339) or null | Timestamp of the last sync operation |
| `last_rebuild` | object or null | Most recent rebuild job record; null if no rebuild has been attempted |
| `last_rebuild.id` | string | Rebuild job ID |
| `last_rebuild.status` | string | Rebuild job status (`queued`, `running`, `completed`, `failed`) |
| `patches` | array | All patches ordered by position |
| `patches[].id` | string | Patch ID |
| `patches[].branch_name` | string | Git branch name for the patch |
| `patches[].position` | integer | 1-based position in the patch stack |
| `patches[].status` | string | Current patch status (`active`, `conflict`, `disabled`, `merged_upstream`) |
| `patches[].last_rebuild_result` | string or null | Per-patch result from most recent rebuild (`success`, `conflict`, `skipped`); null if no rebuild has been attempted |
| `patches[].conflict_files` | array of strings | File paths with unresolved conflicts (present only when `last_rebuild_result` is `conflict`) |
| `patches[].rerere_resolution_count` | integer | Count of recorded rerere resolutions relevant to files touched by this patch; 0 if rr-cache is inaccessible |
| `summary` | object | Aggregate counts derived from the patches array |
| `summary.total_patches` | integer | Total number of patches (equals length of `patches` array) |
| `summary.active` | integer | Count of patches with status `active` |
| `summary.merged_upstream` | integer | Count of patches with status `merged_upstream` |
| `summary.conflict` | integer | Count of patches with status `conflict` |
| `summary.disabled` | integer | Count of patches with status `disabled` |

**Consistency invariant:** `summary.total_patches` equals `len(patches)`, and
`summary.active + summary.merged_upstream + summary.conflict + summary.disabled`
equals `summary.total_patches`.

**Edge Cases:**

- If the workspace is not in `carry_patch` mode, returns HTTP 400.
- If no rebuild has been attempted, `last_rebuild` is null and all patches
  have `last_rebuild_result` set to null.
- If the patches table is empty, returns `patches: []` and all summary counts
  at zero.
- If the `rr-cache` directory is inaccessible, `rerere_resolution_count` is
  set to 0 for all patches rather than failing the request.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Workspace is not in `carry_patch` mode |
| 401 | Unauthenticated request |
| 403 | PAT lacks `workspaces:read` scope |
| 404 | Workspace not found |

---

## Merge Endpoints

Merge endpoints manage the integration of agent branches into target branches
using rebase-then-fast-forward semantics. Merge operations are executed as
background jobs via the durable job queue.

### Merge Job Response Schema

All merge job endpoints return the following JSON schema:

```json
{
  "id": "uuid-string",
  "workspace_slug": "my-workspace",
  "target_branch": "main",
  "source_ref": "feature/agent-1",
  "status": "queued",
  "base_sha": null,
  "merged_sha": null,
  "conflict_files": [],
  "check_output": null,
  "error": null,
  "retry_count": 0,
  "submitted_by": "alice",
  "created_at": "2024-01-01T00:00:00Z",
  "updated_at": "2024-01-01T00:00:00Z"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `id` | string (UUID) | Unique job identifier |
| `workspace_slug` | string | Workspace the merge belongs to |
| `target_branch` | string | Branch being merged into |
| `source_ref` | string | Branch being merged from |
| `status` | string | Job status: `"queued"`, `"running"`, `"completed"`, `"failed"`, `"dead_letter"`, or `"cancelled"` |
| `base_sha` | string or null | 40-char hex SHA of target branch HEAD before merge; null until completed |
| `merged_sha` | string or null | 40-char hex SHA of target branch HEAD after merge; null until completed |
| `conflict_files` | string[] | List of conflicting file paths; empty array if no conflicts |
| `check_output` | string or null | Output from the CHECK_COMMAND if it ran; null otherwise |
| `error` | string or null | Error details for failed jobs; null on success |
| `retry_count` | int | Number of times the job has been retried |
| `submitted_by` | string | Username of the authenticated user who submitted the job |
| `created_at` | string (RFC 3339) | Timestamp of job creation |
| `updated_at` | string (RFC 3339) | Timestamp of last modification |

---

### POST /api/v1/workspaces/:slug/merges

Submit a merge request to integrate a source branch into a target branch.

**Authentication:** API Key, or PAT with `merges:write` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |

**Request Body:**

```json
{
  "target_branch": "main",
  "source_ref": "feature/agent-1"
}
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `target_branch` | yes | string | The branch to merge into |
| `source_ref` | yes | string | The branch to merge from |

**Response:** HTTP 202 Accepted with the merge job record in queued status.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing `target_branch` or `source_ref`; malformed JSON; workspace is not active; clone is not ready; source or target branch does not exist |
| 401 | Unauthenticated request |
| 403 | PAT lacks `merges:write` scope |
| 404 | Workspace not found |
| 409 | A merge job for this source and target branch is already queued or running |

---

### GET /api/v1/workspaces/:slug/merges

List merge jobs for a workspace.

**Authentication:** API Key, or PAT with `merges:read` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |

**Response:** HTTP 200 OK with a JSON array of merge job records scoped to the
workspace. Returns an empty array `[]` when no merge jobs exist.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `merges:read` scope |

---

### GET /api/v1/workspaces/:slug/merges/:id

Get a single merge job by ID.

**Authentication:** API Key, or PAT with `merges:read` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |
| `:id` | The merge job UUID |

**Response:** HTTP 200 OK with the merge job record.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `merges:read` scope |
| 404 | Job not found, or job belongs to a different workspace |

---

### DELETE /api/v1/workspaces/:slug/merges/:id

Cancel a queued merge job.

**Authentication:** API Key, or PAT with `merges:write` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |
| `:id` | The merge job UUID |

**Response:** HTTP 200 OK with `{"status": "cancelled"}`.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `merges:write` scope |
| 404 | Job not found, or job belongs to a different workspace |
| 409 | Job cannot be cancelled (already running, completed, failed, or cancelled) |

---

### POST /api/v1/workspaces/:slug/rebase

Trigger a batch rebase of multiple branches onto a target ref. The operation
runs synchronously and returns per-branch results.

**Authentication:** API Key, or PAT with `merges:write` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |

**Request Body:**

```json
{
  "target_ref": "main",
  "branches": ["feature/a", "feature/b", "feature/c"]
}
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `target_ref` | yes | string | The ref to rebase onto |
| `branches` | yes | string[] | Non-empty list of branch names to rebase |

**Response:** HTTP 200 OK with per-branch results:

```json
{
  "results": [
    {
      "branch": "feature/a",
      "status": "ok",
      "new_head": "aaaa000000000000000000000000000000000001"
    },
    {
      "branch": "feature/b",
      "status": "conflict",
      "conflict_files": ["src/main.go", "src/util.go"]
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `results[].branch` | string | Branch name that was rebased |
| `results[].status` | string | `"ok"` on success, `"conflict"` on merge conflict, `"error"` on other failures |
| `results[].new_head` | string | 40-char hex SHA of the new HEAD after a successful rebase; omitted on conflict/error |
| `results[].conflict_files` | string[] | List of conflicting file paths; omitted on success |

A conflict on one branch does not prevent rebasing of subsequent branches.
All branches in the list are processed sequentially.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing `target_ref`; empty `branches` list; workspace is not active; clone is not ready |
| 401 | Unauthenticated request |
| 403 | PAT lacks `merges:write` scope |
| 404 | Workspace not found |

---

## Git Server Endpoints

The hub includes a built-in git smart HTTP server that exposes workspace
repositories for clone, fetch, and push operations. All git endpoints are
served under `/git/<org-slug>/<workspace-slug>.git/`.

### Git Server Authentication

All git server endpoints use HTTP Basic authentication. The username is
ignored; the password must be a valid hub credential (API key, PAT, or admin
token).

### Git Permission Scopes

| Scope | Description |
|-------|-------------|
| `git:read` | Clone and fetch access to workspace repositories |
| `git:write` | Push access to workspace repositories. Implies `git:read`. |

Admin tokens and API keys have implicit full access to all git operations.

### URL Format

```
<external_url>/git/<org-slug>/<workspace-slug>.git
```

The `.git` suffix is required. Requests without it receive HTTP 404.

---

### GET /git/:org/:slug.git/info/refs

Git ref advertisement endpoint (smart HTTP discovery).

**Authentication:** HTTP Basic (see Git Server Authentication above).

**Query Parameters:**

| Parameter | Required | Description |
|-----------|----------|-------------|
| `service` | yes | `"git-upload-pack"` (fetch/clone) or `"git-receive-pack"` (push) |

**Response:** HTTP 200 with `Content-Type: application/x-<service>-advertisement`.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Missing or invalid credentials |
| 403 | Invalid or missing `service` parameter, or PAT lacks the required git scope |
| 404 | Workspace not found, org mismatch, or clone not ready |

---

### POST /git/:org/:slug.git/git-upload-pack

Git fetch/clone data transfer.

**Authentication:** HTTP Basic. PATs require `git:read` scope.

**Response:** HTTP 200 with `Content-Type: application/x-git-upload-pack-result`.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Missing or invalid credentials |
| 403 | PAT lacks `git:read` scope |
| 404 | Workspace not found, org mismatch, or clone not ready |

---

### POST /git/:org/:slug.git/git-receive-pack

Git push data transfer. After a successful push, updates `head_sha` in the
database and resets the working tree to match the new HEAD.

**Authentication:** HTTP Basic. PATs require `git:write` scope.

**Response:** HTTP 200 with `Content-Type: application/x-git-receive-pack-result`.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Missing or invalid credentials |
| 403 | PAT lacks `git:write` scope |
| 404 | Workspace not found, org mismatch, or clone not ready |

---

## Secrets Endpoints

Secrets are scoped to a user, organization, or workspace. Secret values are
stored as base64-encoded strings and are **never returned by the API** -- only
key names and timestamps are included in responses.

### Secret Validation Rules

| Rule | Constraint |
|------|-----------|
| Key format | Alphanumeric characters and underscores only; cannot start with a digit |
| Key length | Max 255 characters |
| Value size | Max 256 KB (262,144 bytes) |
| Per-scope limit | 100 entries per (owner_type, owner_id) |
| Key lookup | Case-insensitive |

### Secret Response Schema

```json
{
  "key": "MY_SECRET",
  "created_at": "2024-01-01T00:00:00Z",
  "updated_at": "2024-01-01T00:00:00Z"
}
```

No `value` field is ever returned for secrets.

---

### POST /api/v1/user/secrets

Create one or more user-scoped secrets.

**Authentication:** API Key, or PAT with `secrets:manage` scope.

**Request Body:**

```json
{
  "entries": [
    {"key": "MY_SECRET", "value": "secret-value"},
    {"key": "ANOTHER_SECRET", "value": "another-value"}
  ]
}
```

**Response:** HTTP 201 Created with a JSON array of secret entries.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Validation error (empty entries, invalid key format, value too large, per-scope limit exceeded) |
| 401 | Unauthenticated request |
| 403 | PAT lacks `secrets:manage` scope |
| 409 | Duplicate key (case-insensitive) |

---

### GET /api/v1/user/secrets

List all user-scoped secrets.

**Authentication:** API Key, or PAT with `secrets:list` or `secrets:manage`
scope.

**Response:** HTTP 200 OK with a JSON array of secret entries, sorted by key
(case-insensitive).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |

---

### PATCH /api/v1/user/secrets/:key

Update the value of a user-scoped secret.

**Authentication:** API Key, or PAT with `secrets:write` or `secrets:manage`
scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:key` | The secret key to update (case-insensitive lookup) |

**Request Body:**

```json
{
  "value": "new-secret-value"
}
```

**Response:** HTTP 200 OK with the updated secret entry.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing `value` field |
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Key not found |

---

### DELETE /api/v1/user/secrets/:key

Delete a user-scoped secret.

**Authentication:** API Key, or PAT with `secrets:delete` or `secrets:manage`
scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:key` | The secret key to delete (case-insensitive lookup) |

**Response:** HTTP 204 No Content.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Key not found |

---

### POST /api/v1/orgs/:slug/secrets

Create one or more organization-scoped secrets.

**Authentication:** API Key, or PAT with `secrets:manage` scope. Requires
organization membership (admin bypasses membership check).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The organization slug |

**Request Body:** Same as `POST /api/v1/user/secrets`.

**Response:** HTTP 201 Created with a JSON array of secret entries.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Validation error |
| 401 | Unauthenticated request |
| 403 | PAT lacks `secrets:manage` scope |
| 404 | Organization not found or user is not a member |
| 409 | Duplicate key (case-insensitive) |

---

### GET /api/v1/orgs/:slug/secrets

List all organization-scoped secrets.

**Authentication:** API Key, or PAT with `secrets:list` or `secrets:manage`
scope. Requires organization membership (admin bypasses).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The organization slug |

**Response:** HTTP 200 OK with a JSON array of secret entries, sorted by key
(case-insensitive).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Organization not found or user is not a member |

---

### PATCH /api/v1/orgs/:slug/secrets/:key

Update the value of an organization-scoped secret.

**Authentication:** API Key, or PAT with `secrets:write` or `secrets:manage`
scope. Requires organization membership (admin bypasses).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The organization slug |
| `:key` | The secret key to update (case-insensitive lookup) |

**Request Body:** Same as `PATCH /api/v1/user/secrets/:key`.

**Response:** HTTP 200 OK with the updated secret entry.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing `value` field |
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Organization not found, user is not a member, or key not found |

---

### DELETE /api/v1/orgs/:slug/secrets/:key

Delete an organization-scoped secret.

**Authentication:** API Key, or PAT with `secrets:delete` or `secrets:manage`
scope. Requires organization membership (admin bypasses).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The organization slug |
| `:key` | The secret key to delete (case-insensitive lookup) |

**Response:** HTTP 204 No Content.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Organization not found, user is not a member, or key not found |

---

### POST /api/v1/workspaces/:slug/secrets

Create one or more workspace-scoped secrets.

**Authentication:** API Key, or PAT with `secrets:manage` scope. Requires
workspace ownership (admin bypasses ownership check).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |

**Request Body:** Same as `POST /api/v1/user/secrets`.

**Response:** HTTP 201 Created with a JSON array of secret entries.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Validation error |
| 401 | Unauthenticated request |
| 403 | PAT lacks `secrets:manage` scope |
| 404 | Workspace not found or not owned by the authenticated user |
| 409 | Duplicate key (case-insensitive) |

---

### GET /api/v1/workspaces/:slug/secrets

List all workspace-scoped secrets.

**Authentication:** API Key, or PAT with `secrets:list` or `secrets:manage`
scope. Requires workspace ownership (admin bypasses).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |

**Response:** HTTP 200 OK with a JSON array of secret entries, sorted by key
(case-insensitive).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Workspace not found or not owned by the authenticated user |

---

### PATCH /api/v1/workspaces/:slug/secrets/:key

Update the value of a workspace-scoped secret.

**Authentication:** API Key, or PAT with `secrets:write` or `secrets:manage`
scope. Requires workspace ownership (admin bypasses).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |
| `:key` | The secret key to update (case-insensitive lookup) |

**Request Body:** Same as `PATCH /api/v1/user/secrets/:key`.

**Response:** HTTP 200 OK with the updated secret entry.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing `value` field |
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Workspace not found, not owned by user, or key not found |

---

### DELETE /api/v1/workspaces/:slug/secrets/:key

Delete a workspace-scoped secret.

**Authentication:** API Key, or PAT with `secrets:delete` or `secrets:manage`
scope. Requires workspace ownership (admin bypasses).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |
| `:key` | The secret key to delete (case-insensitive lookup) |

**Response:** HTTP 204 No Content.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Workspace not found, not owned by user, or key not found |

---

## Variables Endpoints

Variables are scoped to a user, organization, or workspace. Unlike secrets,
variable values **are returned** by the API. Values are stored as
base64-encoded strings internally but are returned decoded.

### Variable Validation Rules

Same validation rules as secrets apply (see Secret Validation Rules above).

### Variable Response Schema

```json
{
  "key": "MY_VAR",
  "value": "my-value",
  "created_at": "2024-01-01T00:00:00Z",
  "updated_at": "2024-01-01T00:00:00Z"
}
```

---

### POST /api/v1/user/vars

Create one or more user-scoped variables.

**Authentication:** API Key, or PAT with `vars:manage` scope.

**Request Body:**

```json
{
  "entries": [
    {"key": "MY_VAR", "value": "my-value"},
    {"key": "ANOTHER_VAR", "value": "another-value"}
  ]
}
```

**Response:** HTTP 201 Created with a JSON array of variable entries.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Validation error (empty entries, invalid key format, value too large, per-scope limit exceeded) |
| 401 | Unauthenticated request |
| 403 | PAT lacks `vars:manage` scope |
| 409 | Duplicate key (case-insensitive) |

---

### GET /api/v1/user/vars

List all user-scoped variables.

**Authentication:** API Key, or PAT with `vars:read`, `vars:write`, or
`vars:manage` scope.

**Response:** HTTP 200 OK with a JSON array of variable entries, sorted by key
(case-insensitive).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |

---

### PATCH /api/v1/user/vars/:key

Update the value of a user-scoped variable.

**Authentication:** API Key, or PAT with `vars:write` or `vars:manage` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:key` | The variable key to update (case-insensitive lookup) |

**Request Body:**

```json
{
  "value": "new-value"
}
```

**Response:** HTTP 200 OK with the updated variable entry.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing `value` field |
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Key not found |

---

### DELETE /api/v1/user/vars/:key

Delete a user-scoped variable.

**Authentication:** API Key, or PAT with `vars:delete` or `vars:manage` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:key` | The variable key to delete (case-insensitive lookup) |

**Response:** HTTP 204 No Content.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Key not found |

---

### POST /api/v1/orgs/:slug/vars

Create one or more organization-scoped variables.

**Authentication:** API Key, or PAT with `vars:manage` scope. Requires
organization membership (admin bypasses membership check).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The organization slug |

**Request Body:** Same as `POST /api/v1/user/vars`.

**Response:** HTTP 201 Created with a JSON array of variable entries.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Validation error |
| 401 | Unauthenticated request |
| 403 | PAT lacks `vars:manage` scope |
| 404 | Organization not found or user is not a member |
| 409 | Duplicate key (case-insensitive) |

---

### GET /api/v1/orgs/:slug/vars

List all organization-scoped variables.

**Authentication:** API Key, or PAT with `vars:read`, `vars:write`, or
`vars:manage` scope. Requires organization membership (admin bypasses).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The organization slug |

**Response:** HTTP 200 OK with a JSON array of variable entries, sorted by key
(case-insensitive).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Organization not found or user is not a member |

---

### PATCH /api/v1/orgs/:slug/vars/:key

Update the value of an organization-scoped variable.

**Authentication:** API Key, or PAT with `vars:write` or `vars:manage` scope.
Requires organization membership (admin bypasses).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The organization slug |
| `:key` | The variable key to update (case-insensitive lookup) |

**Request Body:** Same as `PATCH /api/v1/user/vars/:key`.

**Response:** HTTP 200 OK with the updated variable entry.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing `value` field |
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Organization not found, user is not a member, or key not found |

---

### DELETE /api/v1/orgs/:slug/vars/:key

Delete an organization-scoped variable.

**Authentication:** API Key, or PAT with `vars:delete` or `vars:manage` scope.
Requires organization membership (admin bypasses).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The organization slug |
| `:key` | The variable key to delete (case-insensitive lookup) |

**Response:** HTTP 204 No Content.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Organization not found, user is not a member, or key not found |

---

### POST /api/v1/workspaces/:slug/vars

Create one or more workspace-scoped variables.

**Authentication:** API Key, or PAT with `vars:manage` scope. Requires
workspace ownership (admin bypasses ownership check).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |

**Request Body:** Same as `POST /api/v1/user/vars`.

**Response:** HTTP 201 Created with a JSON array of variable entries.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Validation error |
| 401 | Unauthenticated request |
| 403 | PAT lacks `vars:manage` scope |
| 404 | Workspace not found or not owned by the authenticated user |
| 409 | Duplicate key (case-insensitive) |

---

### GET /api/v1/workspaces/:slug/vars

List all workspace-scoped variables.

**Authentication:** API Key, or PAT with `vars:read`, `vars:write`, or
`vars:manage` scope. Requires workspace ownership (admin bypasses).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |

**Response:** HTTP 200 OK with a JSON array of variable entries, sorted by key
(case-insensitive).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Workspace not found or not owned by the authenticated user |

---

### PATCH /api/v1/workspaces/:slug/vars/:key

Update the value of a workspace-scoped variable.

**Authentication:** API Key, or PAT with `vars:write` or `vars:manage` scope.
Requires workspace ownership (admin bypasses).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |
| `:key` | The variable key to update (case-insensitive lookup) |

**Request Body:** Same as `PATCH /api/v1/user/vars/:key`.

**Response:** HTTP 200 OK with the updated variable entry.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing `value` field |
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Workspace not found, not owned by user, or key not found |

---

### DELETE /api/v1/workspaces/:slug/vars/:key

Delete a workspace-scoped variable.

**Authentication:** API Key, or PAT with `vars:delete` or `vars:manage` scope.
Requires workspace ownership (admin bypasses).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |
| `:key` | The variable key to delete (case-insensitive lookup) |

**Response:** HTTP 204 No Content.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Workspace not found, not owned by user, or key not found |

---

### GET /api/v1/workspaces/:slug/vars/resolved

Resolve variables for a workspace by merging values from user, organization,
and workspace tiers. Resolution order: workspace > org > user (workspace
values override org, org overrides user).

**Authentication:** API Key, or PAT with `vars:read`, `vars:write`, or
`vars:manage` scope. Requires workspace ownership (admin bypasses).

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |

**Response:** HTTP 200 OK with a JSON array of resolved variable entries,
sorted by key (case-insensitive).

```json
[
  {
    "key": "MY_VAR",
    "value": "workspace-override",
    "origin": "workspace",
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  }
]
```

The `origin` field indicates which tier the value came from: `"user"`,
`"org"`, or `"workspace"`.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Workspace not found or not owned by the authenticated user |
| 500 | Internal server error |

---

## Non-Workspace Endpoints (apikit-provided)

The following endpoints are provided by the `apikit` library and are available
alongside the workspace endpoints. All endpoints use the same authentication
mechanisms and error envelope format described above.

### Login

#### POST /login

Authenticate a user and obtain an API key.

**Authentication:** None (public endpoint).

**Request Body:**

```json
{
  "email": "user@example.com",
  "password": "secret"
}
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `email` | yes | string | User's email address |
| `password` | yes | string | User's password |

**Response:** HTTP 200 OK with a JSON object containing the API key and user
information.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing or invalid fields |
| 401 | Invalid credentials |

---

### User

#### GET /user

Get the profile of the authenticated user.

**Authentication:** API Key, PAT, or Admin Token.

**Response:** HTTP 200 OK with user profile JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |

#### PUT /user

Update the authenticated user's profile.

**Authentication:** API Key or Admin Token.

**Response:** HTTP 200 OK with updated user profile JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Invalid request body |
| 401 | Unauthenticated request |

---

### Keys

#### GET /user/keys

List API keys for the authenticated user.

**Authentication:** API Key or Admin Token.

**Response:** HTTP 200 OK with a JSON array of API key metadata.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |

#### POST /user/keys

Create a new API key for the authenticated user.

**Authentication:** API Key or Admin Token.

**Request Body:**

```json
{
  "description": "My API Key"
}
```

**Response:** HTTP 201 Created with the new API key (the full key value is
only returned once at creation time).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Invalid request body |
| 401 | Unauthenticated request |

#### DELETE /user/keys/:id

Revoke an API key.

**Authentication:** API Key or Admin Token.

**Response:** HTTP 204 No Content.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 404 | Key not found or not owned by user |

---

### Tokens

#### GET /user/tokens

List personal access tokens (PATs) for the authenticated user.

**Authentication:** API Key or Admin Token.

**Response:** HTTP 200 OK with a JSON array of token metadata, including
scopes granted to each token.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |

#### POST /user/tokens

Create a new personal access token with specific permission scopes.

**Authentication:** API Key or Admin Token.

**Request Body:**

```json
{
  "description": "CI token",
  "scopes": ["workspaces:read", "workspaces:create"]
}
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `description` | yes | string | Human-readable label for the token |
| `scopes` | yes | string[] | Permission scopes to grant (see Permission Scopes section) |

**Response:** HTTP 201 Created with the new token (the full token value is
only returned once at creation time).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Invalid request body, missing fields, or invalid scopes |
| 401 | Unauthenticated request |

#### DELETE /user/tokens/:id

Revoke a personal access token.

**Authentication:** API Key or Admin Token.

**Response:** HTTP 204 No Content.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 404 | Token not found or not owned by user |

---

### Orgs

#### GET /user/orgs

List organizations the authenticated user belongs to.

**Authentication:** API Key, PAT, or Admin Token.

**Response:** HTTP 200 OK with a JSON array of organization objects.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |

#### POST /orgs

Create a new organization.

**Authentication:** API Key or Admin Token.

**Request Body:**

```json
{
  "name": "My Organization",
  "slug": "my-org"
}
```

**Response:** HTTP 201 Created with the new organization JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Invalid request body or missing fields |
| 401 | Unauthenticated request |
| 409 | Organization slug already exists |

#### GET /orgs/:slug

Get organization details by slug.

**Authentication:** API Key, PAT, or Admin Token.

**Response:** HTTP 200 OK with organization JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 404 | Organization not found |

---

### Admin

#### GET /admin/users

List all users (admin only).

**Authentication:** Admin Token.

**Response:** HTTP 200 OK with a JSON array of user objects.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | Non-admin credential |

#### GET /admin/stats

Get system statistics (admin only).

**Authentication:** Admin Token.

**Response:** HTTP 200 OK with system statistics JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | Non-admin credential |

#### DELETE /admin/users/:id

Delete a user account (admin only).

**Authentication:** Admin Token.

**Response:** HTTP 204 No Content.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | Non-admin credential |
| 404 | User not found |
