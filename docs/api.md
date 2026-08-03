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
| `workspaces:read` | List and view access to the PAT owner's workspaces | GET /api/v1/workspaces, GET /api/v1/workspaces/:slug |
| `workspaces:create` | Create workspaces; implies read access | POST /api/v1/workspaces, GET /api/v1/workspaces, GET /api/v1/workspaces/:slug |
| `workspaces:write` | Update, archive, and reactivate workspaces; implies read access | PATCH /api/v1/workspaces/:slug, POST /api/v1/workspaces/:slug/archive, POST /api/v1/workspaces/:slug/reactivate, GET /api/v1/workspaces, GET /api/v1/workspaces/:slug |
| `workspaces:delete` | Delete archived workspaces owned by the PAT's user; does **not** imply read access | DELETE /api/v1/workspaces/:slug |
| `git:read` | Clone and fetch access to workspace repositories via the git server | GET /git/:org/:slug.git/info/refs, POST /git/:org/:slug.git/git-upload-pack |
| `git:write` | Push access to workspace repositories via the git server; implies `git:read` | POST /git/:org/:slug.git/git-receive-pack (plus all `git:read` endpoints) |

### Implied Permissions

- `workspaces:create` implies `workspaces:read` — a PAT with create scope can
  also list and view workspaces.
- `workspaces:write` implies `workspaces:read` — a PAT with write scope can
  also list and view workspaces.
- `workspaces:delete` does **not** imply read access — a PAT with only
  `workspaces:delete` cannot list or view workspaces.
- `git:write` implies `git:read` — a PAT with git write scope can also clone
  and fetch.

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
  "description": "A description of the workspace"
}
```

| Field | Required | Type | Constraints |
|-------|----------|------|-------------|
| `slug` | yes | string | Globally unique, URL-safe identifier |
| `git_url` | yes | string | Valid HTTPS or SSH git URL |
| `branch` | no | string | Git ref; defaults to null |
| `org_id` | no | string (UUID) | Must reference an org the owner is a member of |
| `display_name` | no | string | Max 128 characters; defaults to slug value if omitted or empty |
| `description` | no | string | Max 1024 characters; defaults to empty string if omitted |

**Response:** HTTP 201 Created with workspace JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing required fields (`slug`, `git_url`), or `display_name` exceeds 128 characters, or `description` exceeds 1024 characters |
| 401 | Unauthenticated request |
| 403 | Admin token attempted to create a workspace; PAT lacks `workspaces:create` scope |
| 409 | A workspace with the given `slug` already exists |
| 500 | Internal server error (e.g., database error, org membership check failure) |

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
  "org_id": "uuid-string"
}
```

| Field | Type | Constraints | Null Behavior |
|-------|------|-------------|---------------|
| `display_name` | string or null | Max 128 characters | Setting to `null` clears the display name back to the workspace slug |
| `description` | string or null | Max 1024 characters | Setting to `null` clears the description to an empty string |
| `org_id` | string (UUID) or null | Must reference an org the owner is a member of | Setting to `null` removes the organization association |

**Partial Update Behavior:**

- Only explicitly provided fields are updated; omitted fields are not modified.
- Immutable fields (`slug`, `git_url`, `branch`, `owner_id`) cannot be set
  via this endpoint. Including an immutable field in the request body returns
  HTTP 400.
- The `updated_at` timestamp is automatically advanced on every successful
  update.

**Response:** HTTP 200 OK with the full updated workspace JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Empty body (no fields provided); workspace is archived (must reactivate first); `display_name` exceeds 128 characters; `description` exceeds 1024 characters; request includes immutable fields |
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

## Campaign Endpoints

Campaigns are named sets of specs executed together against a shared
integration branch. A dependency DAG sequences and parallelizes spec work,
with cascading rebases after merges.

### Campaign Response Schema

```json
{
  "id": "uuid-string",
  "workspace_slug": "ws-slug",
  "name": "sprint-42",
  "integration_branch": "main",
  "status": "active",
  "dag": {
    "specs": ["07", "09"],
    "edges": [{"from": "07", "to": "09", "relationship": "depends_on"}]
  },
  "specs": [
    {
      "campaign_id": "uuid-string",
      "spec_id": "07",
      "status": "active",
      "branch_name": "spec/07-secrets-variables",
      "branch_sha": "abcdef1234567890abcdef1234567890abcdef12",
      "conflict_details": null,
      "blocked_by_merge": "",
      "updated_at": "2024-01-01T00:00:00Z"
    }
  ],
  "warnings": [],
  "created_by": "user-id",
  "created_at": "2024-01-01T00:00:00Z",
  "updated_at": "2024-01-01T00:00:00Z"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `id` | string (UUID) | Unique campaign identifier |
| `workspace_slug` | string | Workspace this campaign belongs to |
| `name` | string | Human-readable campaign name; unique within a workspace |
| `integration_branch` | string | Shared git branch (e.g., `main`) that spec branches are rebased against |
| `status` | string | Lifecycle state: `pending`, `active`, `completed`, `failed`, or `cancelled` |
| `dag` | object | Spec dependency graph with `specs` array and `edges` array |
| `specs` | array | Per-spec status objects (see below) |
| `warnings` | array or null | Human-readable warnings for skipped specs during creation; omitted when empty |
| `created_by` | string | User or credential that created the campaign |
| `created_at` | string (RFC 3339) | Creation timestamp |
| `updated_at` | string (RFC 3339) | Last modification timestamp |

#### Spec Status Object

| Field | Type | Description |
|-------|------|-------------|
| `spec_id` | string | Spec identifier (e.g., `"07"`) |
| `status` | string | Per-spec state: `pending`, `active`, `blocked`, `merged`, `failed`, or `cancelled` |
| `branch_name` | string | Git branch name (e.g., `spec/07-secrets-variables`); empty for pending specs |
| `branch_sha` | string | 40-character hex SHA of branch HEAD; empty for pending specs |
| `conflict_details` | array or null | List of conflicting file paths when status is `blocked` |
| `blocked_by_merge` | string | UUID of the merge job that caused the conflict; empty when not blocked |

### Campaign Error Response Schema

Campaign endpoints use a flat JSON error format:

```json
{
  "error": "description of the error"
}
```

---

### POST /api/v1/workspaces/:slug/campaigns

Create a new campaign.

**Authentication:** API Key, Admin Token, or PAT with `campaigns:write` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | Workspace slug |

**Request Body:**

```json
{
  "name": "sprint-42",
  "spec_ids": ["07", "09"],
  "integration_branch": "main"
}
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `name` | yes | string | Campaign name; must be unique within the workspace |
| `integration_branch` | yes | string | Git branch to use as the integration target |
| `spec_ids` | no | string[] | Explicit list of spec IDs. When omitted, specs are discovered by scanning `tasks.json` files for pending subtasks. |

**Response:** HTTP 201 Created with campaign JSON (see Campaign Response Schema).

On creation, the handler computes the initial DAG frontier (specs with no
unmet dependencies), creates git branches for frontier specs, sets frontier
specs to `active`, and sets the campaign to `active`.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Invalid JSON in request body |
| 401 | Unauthenticated request |
| 403 | PAT lacks `campaigns:write` scope |
| 409 | An active campaign already exists for the integration branch; or campaign name already exists |
| 422 | Missing required fields (`name`, `integration_branch`); integration branch does not exist; no valid specs found |
| 500 | Git branch creation failure; database error |

---

### GET /api/v1/workspaces/:slug/campaigns

List campaigns for a workspace, optionally filtered by status.

**Authentication:** API Key, Admin Token, or PAT with `campaigns:read` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | Workspace slug |

**Query Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `status` | string | Filter by campaign status. Valid values: `pending`, `active`, `completed`, `failed`, `cancelled`. |

**Response:** HTTP 200 OK with a JSON array of campaign objects. Returns an
empty array `[]` when no campaigns match.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `campaigns:read` scope |
| 422 | Invalid status filter value |

---

### GET /api/v1/workspaces/:slug/campaigns/:id

Get a single campaign with full detail including DAG and per-spec status.

**Authentication:** API Key, Admin Token, or PAT with `campaigns:read` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | Workspace slug |
| `:id` | Campaign ID (UUID) |

**Response:** HTTP 200 OK with campaign JSON (see Campaign Response Schema),
including the `specs` array with `conflict_details` and `blocked_by_merge`.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `campaigns:read` scope |
| 404 | Campaign not found |

---

### DELETE /api/v1/workspaces/:slug/campaigns/:id

Cancel an active campaign. Sets the campaign and all specs to `cancelled`
status. Spec branches are left in place on the git server for debugging.

**Authentication:** API Key, Admin Token, or PAT with `campaigns:write` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | Workspace slug |
| `:id` | Campaign ID (UUID) |

**Response:** HTTP 200 OK with the updated campaign JSON reflecting
`cancelled` status for the campaign and all specs.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `campaigns:write` scope |
| 404 | Campaign not found |
| 409 | Campaign is already cancelled; or campaign is in `completed`/`failed` state |

---

### POST /api/v1/workspaces/:slug/campaigns/:id/specs/:spec_id/resolve

Resolve a rebase conflict on a blocked spec branch. The hub rebases the spec
branch onto the current integration branch HEAD and restores push access if
the rebase is clean.

**Authentication:** API Key, Admin Token, or PAT with `campaigns:write` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | Workspace slug |
| `:id` | Campaign ID (UUID) |
| `:spec_id` | Spec identifier (e.g., `"07"`) |

**Response (clean rebase):** HTTP 200 OK

```json
{
  "spec_id": "07",
  "status": "active",
  "branch_sha": "abcdef1234567890abcdef1234567890abcdef12"
}
```

**Response (already active/merged):** HTTP 200 OK with current status and
branch_sha (idempotent, no rebase performed).

**Response (still conflicting):** HTTP 409 Conflict

```json
{
  "conflict_details": ["file1.go", "file2.go"]
}
```

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `campaigns:write` scope |
| 404 | Campaign not found; or spec not found in campaign |
| 409 | Spec is in `pending`, `failed`, or `cancelled` status (wrong state for resolution); or rebase still has conflicts |
| 500 | Git rebase subprocess error or timeout |

---

## Merge Queue Endpoints

The merge queue serializes integration operations, providing a FIFO queue
with rebase-then-fast-forward semantics that prevents merge races and silent
work loss when multiple agents complete work on separate spec branches
concurrently.

### Merge Job Response Schema

```json
{
  "id": "uuid-string",
  "workspace_slug": "ws-slug",
  "target_branch": "main",
  "source_ref": "spec/07-secrets-variables",
  "status": "queued",
  "campaign_id": "uuid-string-or-null",
  "spec_id": "07-or-null",
  "rejection_reason": null,
  "retry_count": 0,
  "available_at": "2026-01-01T00:00:00Z",
  "base_sha": null,
  "merged_sha": null,
  "conflict_details": null,
  "check_output": null,
  "submitted_by": "uuid-string",
  "created_at": "2026-01-01T00:00:00Z",
  "updated_at": "2026-01-01T00:00:00Z"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | UUID of the merge job |
| `workspace_slug` | string | URL-safe workspace identifier |
| `target_branch` | string | Git branch to merge into (e.g. `main`) |
| `source_ref` | string | Git branch to merge from (e.g. `spec/07-secrets-variables`) |
| `status` | string | One of: `prepared`, `queued`, `running`, `merged`, `conflict`, `check_failed`, `cancelled`, `push_failed`, `dead_letter` |
| `campaign_id` | string\|null | UUID of the associated campaign, if any |
| `spec_id` | string\|null | Spec identifier inferred from `source_ref` |
| `rejection_reason` | string\|null | Why the merge was rejected (`BeforeDependency`, `WouldConflict`, `AlreadyMerged`, `BranchNotReady`, `SpecBlocked`) |
| `retry_count` | integer | Number of retries attempted |
| `available_at` | string | RFC 3339 timestamp when the job becomes eligible for processing |
| `base_sha` | string\|null | SHA of the target branch HEAD when the job started running |
| `merged_sha` | string\|null | SHA of the target branch HEAD after a successful merge |
| `conflict_details` | string[]\|null | Array of conflicting file paths (deserialized from stored JSON) |
| `check_output` | string\|null | Captured stdout/stderr from the post-rebase check command (included only in single-job responses, omitted from list) |
| `submitted_by` | string | UUID of the user or agent who submitted the job |
| `created_at` | string | RFC 3339 creation timestamp |
| `updated_at` | string | RFC 3339 last-update timestamp |

**Note:** The `nonce` field is never included in API responses. The
`check_output` field is included in single-job responses (GET by ID) but
omitted from list responses.

### Submit Merge Job

#### POST /api/v1/workspaces/:slug/merges

Submit a new merge job to the queue.

**Authentication:** API Key, Admin Token, or PAT with `merges:write` scope.

**Request Body:**

```json
{
  "target_branch": "main",
  "source_ref": "spec/07-secrets-variables"
}
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `target_branch` | yes | string | Git branch to merge into |
| `source_ref` | yes | string | Git branch to merge from |

**Response:** HTTP 202 Accepted with the merge job JSON object (status
will be `queued`).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing `target_branch` or `source_ref` |
| 401 | Unauthenticated request |
| 403 | PAT lacks `merges:write` scope |
| 404 | Workspace not found |
| 409 | Active merge job already exists for the same `(workspace_slug, source_ref)` pair. Response includes `existing_job_id`. |

### List Merge Jobs

#### GET /api/v1/workspaces/:slug/merges

List merge jobs for a workspace with optional filtering and cursor-based
pagination.

**Authentication:** API Key, Admin Token, or PAT with `merges:read` scope.

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `status` | string | *(none)* | Filter by a single status value |
| `limit` | integer | 50 | Maximum items per page (max 100) |
| `after` | string | *(none)* | UUID of the last job on the previous page (cursor) |

**Response:** HTTP 200 OK

```json
{
  "items": [ /* merge job objects (without check_output) */ ],
  "next_cursor": "uuid-or-null"
}
```

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Invalid `status`, `limit`, or `after` parameter |
| 401 | Unauthenticated request |
| 403 | PAT lacks `merges:read` scope |

### Get Single Merge Job

#### GET /api/v1/workspaces/:slug/merges/:id

Retrieve the full details of a single merge job, including `check_output`
and `conflict_details` deserialized as a native JSON array.

**Authentication:** API Key, Admin Token, or PAT with `merges:read` scope.

**Response:** HTTP 200 OK with the full merge job JSON object.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `merges:read` scope |
| 404 | Job not found, or job belongs to a different workspace (anti-enumeration) |

### Cancel Queued Merge Job

#### DELETE /api/v1/workspaces/:slug/merges/:id

Cancel a merge job that is in `queued` status. Jobs in any other status
cannot be cancelled.

**Authentication:** API Key, Admin Token, or PAT with `merges:write` scope.

**Response:** HTTP 204 No Content on successful cancellation.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `merges:write` scope |
| 404 | Job not found, or job belongs to a different workspace |
| 409 | Job is not in `queued` status. Response includes the current `status` value. Also returned if the job transitions from `queued` to `running` between the status check and the update (race condition). |

### Requeue Dead-Lettered Merge Job

#### POST /api/v1/workspaces/:slug/merges/:id/requeue

Create a new merge job from a dead-lettered job, giving it a fresh retry
budget. The original dead-lettered job is left unchanged for audit purposes.

**Authentication:** API Key, Admin Token, or PAT with `merges:write` scope.

**Response:** HTTP 202 Accepted with the new merge job JSON object. The new
job has a fresh UUID, `status=queued`, `retry_count=0`, `available_at=now()`,
and `submitted_by` set to the authenticated caller's UUID.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `merges:write` scope |
| 404 | Job not found, or job belongs to a different workspace |
| 409 | Job is not in `dead_letter` status; or an active job already exists for the same `(workspace_slug, source_ref)` pair (duplicate guard). When a duplicate exists, the response includes `existing_job_id`. |

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
