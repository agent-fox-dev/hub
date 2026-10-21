# af-hub REST API Reference

This document covers all REST API endpoints provided by the af-hub service,
including workspace management endpoints and apikit-provided endpoints for
authentication, user management, API keys, tokens, organizations, and
administration.

A machine-readable OpenAPI 3.1 description of the same surface lives in
[`docs/openapi.yaml`](openapi.yaml). It is generated from the handler source
rather than from this document; where the two disagree, see
[`docs/errata/openapi_description.md`](errata/openapi_description.md).

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
| `workspaces:read` | List and view access to the PAT owner's workspaces | GET /api/v1/workspaces, GET /api/v1/workspaces/:slug, GET /api/v1/workspaces/:slug/rerere, GET /api/v1/workspaces/:slug/patch-status |
| `workspaces:create` | Create workspaces; implies read access | POST /api/v1/workspaces, GET /api/v1/workspaces, GET /api/v1/workspaces/:slug |
| `workspaces:write` | Update, archive, and reactivate workspaces; implies read access | PATCH /api/v1/workspaces/:slug, POST /api/v1/workspaces/:slug/archive, POST /api/v1/workspaces/:slug/reactivate, DELETE /api/v1/workspaces/:slug/rerere/\*pathspec, GET /api/v1/workspaces, GET /api/v1/workspaces/:slug |
| `workspaces:delete` | Delete archived workspaces owned by the PAT's user; does **not** imply read access | DELETE /api/v1/workspaces/:slug |
| `workspaces:sync` | Trigger upstream sync and reclone operations on workspaces | POST /api/v1/workspaces/:slug/sync, POST /api/v1/workspaces/:slug/reclone |
| `patches:read` | List and view patches for a workspace | GET /api/v1/workspaces/:slug/patches, GET /api/v1/workspaces/:slug/patches/:id |
| `patches:write` | Add, remove, update, restore, reorder, and reset patches for a workspace; implies `patches:read` | POST /api/v1/workspaces/:slug/patches, PATCH /api/v1/workspaces/:slug/patches/:id, DELETE /api/v1/workspaces/:slug/patches/:id, POST /api/v1/workspaces/:slug/patches/:id/restore, POST /api/v1/workspaces/:slug/patches/:id/reset-to-origin, POST /api/v1/workspaces/:slug/patches/reorder |
| `rebuilds:read` | View rebuild job status, history, and preview | GET /api/v1/workspaces/:slug/rebuilds, GET /api/v1/workspaces/:slug/rebuilds/:id, GET /api/v1/workspaces/:slug/rebuild-preview |
| `rebuilds:write` | Submit, cancel, requeue, and rollback rebuild jobs for carry-patch workspaces | POST /api/v1/workspaces/:slug/rebuild, DELETE /api/v1/workspaces/:slug/rebuilds/:id, POST /api/v1/workspaces/:slug/rebuilds/:id/requeue, POST /api/v1/workspaces/:slug/rebuilds/:id/rollback |
| `merges:read` | List and view merge job status | GET /api/v1/workspaces/:slug/merges, GET /api/v1/workspaces/:slug/merges/:id |
| `merges:write` | Submit, cancel, and requeue merge jobs; trigger batch rebase | POST /api/v1/workspaces/:slug/merges, DELETE /api/v1/workspaces/:slug/merges/:id, POST /api/v1/workspaces/:slug/merges/:id/requeue, POST /api/v1/workspaces/:slug/rebase |
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
| `sessions:read` | Read agent session records, token usage, and workspace cost summaries | GET /api/v1/sessions, GET /api/v1/sessions/:id, GET /api/v1/sessions/:id/usage, GET /api/v1/workspaces/:slug/cost |
| `sessions:write` | Open, close, and report token usage on agent sessions | POST /api/v1/sessions, POST /api/v1/sessions/:id/complete, POST /api/v1/sessions/:id/usage |
| `audit:read` | Query audit events, view transcripts, and connect to SSE stream | GET /api/v1/audit, GET /api/v1/workspaces/:slug/runs/:run_id/transcript, GET /api/v1/events |
| `audit:write` | Ingest agent audit events, traces, and session outcomes | POST endpoints under /api/v1/workspaces/:slug/runs/:run_id/ |
| `sandboxes:read` | List the deployment's sandbox profiles; list and read sandbox leases | GET /api/v1/sandbox-profiles, GET /api/v1/workspaces/:slug/sandboxes, GET /api/v1/workspaces/:slug/sandboxes/:id |
| `sandboxes:write` | Issue and end sandbox leases; implies `sandboxes:read` | POST /api/v1/workspaces/:slug/sandboxes, DELETE /api/v1/workspaces/:slug/sandboxes/:id (plus all `sandboxes:read` endpoints) |
| `forge:read` | Read a workspace's repository, issues and pull requests on its forge, and how the hub reaches it | GET /api/v1/workspaces/:slug/forge, GET .../forge/repository, GET .../issues, GET .../issues/:n, GET .../issues/:n/comments, GET .../pulls/:n, GET .../pulls/:n/state, GET .../pulls/:n/files, GET .../pulls/:n/checks, GET .../pulls/:n/reviews |
| `forge:write` | Change a workspace's issues, comments, labels and pull requests on its forge; implies `forge:read` | POST /api/v1/workspaces/:slug/issues, PATCH .../issues/:n, POST .../issues/:n/close, POST .../issues/:n/comments, POST .../issues/:n/labels, DELETE .../issues/:n/labels/:name, POST .../labels, POST .../pulls, POST .../pulls/:n/comments (plus all `forge:read` endpoints) |

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
- `sandboxes:write` implies `sandboxes:read` — a PAT with write scope can also
  list sandbox profiles and read leases. `sandboxes:read` does not allow
  issuing or ending a lease.
- `forge:write` implies `forge:read` — a PAT with write scope can also read a
  workspace's forge. `forge:read` does not allow a write: the write routes check
  `forge:write` itself.

### Scopes Checked Literally

Three handlers test for a scope string directly rather than through the
implications above. A PAT holding only `workspaces:create` or
`workspaces:write` can list and get workspaces but is rejected by:

| Endpoint | Requires the literal scope |
|----------|---------------------------|
| `GET /api/v1/workspaces/:slug/rerere` | `workspaces:read` |
| `GET /api/v1/workspaces/:slug/patch-status` | `workspaces:read` |
| `DELETE /api/v1/workspaces/:slug/rerere/*pathspec` | `workspaces:write` |

Conversely, `POST /api/v1/workspaces/:slug/sync` accepts **either**
`workspaces:sync` or `workspaces:write` when the workspace is in `carry_patch`
mode. The standard sync path and `POST /api/v1/workspaces/:slug/reclone`
require `workspaces:sync` exclusively.

### Workspace Ownership

Ownership is enforced on every workspace-scoped endpoint. A workspace is
visible only to its owner (the user behind the API key or PAT); admin tokens
bypass the check. Non-owners receive HTTP 404 (see the anti-enumeration
policy below), never 403, so slugs cannot be probed. This covers:

- workspace CRUD, sync, reclone, archive, reactivate
- all patch endpoints, rebuild, rebuild-preview, rollback, patch-status
- all merge endpoints and batch rebase
- rerere list and forget
- secrets and variables
- agent sessions (`POST /api/v1/sessions` returns 404 for a workspace the
  caller does not own) and every per-run audit endpoint
- the unified audit query and the SSE stream, which return only events of
  workspaces the caller owns (hub events without a workspace, such as user
  or credential changes, are visible to admin tokens only)
- the git server

Blocked users are rejected by the git server as well as by the API.

### Workspace-Bound Tokens

A personal access token can be bound to exactly one workspace. Hub keeps the
binding in its SQLite table `token_workspace_bindings` (`token_id` primary key,
`slug`, `created_at`), keyed by apikit's token id; a PAT without a row is
unbound. This release enforces bindings; no API writes them yet. A bound token
reaches its own workspace `S` under its scopes, with exactly the answers an
unbound PAT of the same user and scopes gets, and nothing else:

| Request by a token bound to `S` | Answer |
|---|---|
| Any route whose path template contains `workspaces/:slug`, for a slug other than `S` | `404` `workspace not found`, before the handler runs, so before any scope, existence or status check |
| The same routes for `S` | The route's usual answer |
| `GET /api/v1/workspaces` | At most `S`; the scope check and `include_archived` apply as usual |
| `POST /api/v1/sessions` with another `workspace_slug`, or replaying another workspace's session id | `404` `workspace not found` |
| `GET /api/v1/sessions/:id`, `GET /api/v1/sessions/:id/usage` or `GET /api/v1/sessions?workspace_slug=` outside `S` | `403` `workspace access denied`; `GET /api/v1/sessions` lists only `S`'s sessions |
| `POST /api/v1/sessions/:id/complete` or `POST /api/v1/sessions/:id/usage` for a session outside `S` | `403` `not session owner` |
| `GET /api/v1/audit`, `GET /api/v1/events` | Only `S`'s events; `GET /api/v1/audit?workspace=<other>` is `404` `workspace not found` |
| `GET /api/v1/sandbox-profiles` | As for an unbound PAT |
| Every other hub route: `POST /api/v1/workspaces`, `/api/v1/user/secrets`, `/api/v1/user/vars`, `/api/v1/orgs/:slug/secrets`, `/api/v1/orgs/:slug/vars`, `GET /api/v1/sandboxes`, a path under the mount point that matches no route, and any route added later | `403` with `error_type: token_bound` and the message `this token is bound to workspace <S>` |
| Every route apikit mounts (`/auth/...`, `/user`, `/user/keys`, `/user/tokens`, `/users`, `/orgs`) | `403` with `error_type: token_bound` and the message `this token is bound to a workspace`, answered before apikit authenticates the token, so the workspace is not named |
| The git server, for a repository other than `S`'s | pkt-line `404 repository not found` |

A token bound to an archived workspace still reaches it and gets its usual
`400`/`409` answers. Deleting the workspace revokes its live bound tokens and
removes their bindings in the same transaction, so such a token then gets `401`
everywhere; a binding whose workspace row vanished another way keeps its token
confined to nothing until the binding sweep revokes it. The binding guard answers a failed binding lookup
with `500 internal server error` (a pkt-line 500 in git), and no layer lets the
token through as unbound. API keys, admin tokens and unbound PATs are not affected,
and API keys and admin tokens never cause a binding lookup.

### Workspace Lock

Operations that modify a workspace clone (sync, reclone, archive, rebuild,
merge, batch rebase, rollback, rerere forget, and the post-push hook) are
serialised per workspace. HTTP handlers that cannot acquire the lock
immediately return `409` with `error_type: workspace_busy`; background jobs
wait for it.

A carry-patch rebuild applies patches in its own detached git worktree and
does not hold the lock for the whole run. It holds the lock only for the
upstream fetch and base resolution (the *fetch phase*) and for the final
integration-ref update, patch bookkeeping and optional push to `origin` (the
*final phase*). Sync, rollback, rerere forget, batch rebase and merge jobs
are therefore rejected (or made to wait) only while a rebuild is in its fetch
phase or final phase; during patch application they proceed normally.

Archive and reclone delete the directory the rebuild worktree lives in, so
they are guarded by a separate per-workspace rebuild-active flag instead: they
answer `409` with `error_type: workspace_busy` for the whole rebuild, from
before the fetch until the worktree has been removed, even while the lock
itself is free.

### Anti-Enumeration Policy

When a PAT lacks the required scope for an endpoint, or the requested
workspace is not owned by the PAT's user, the workspace CRUD endpoints (list,
get, update, archive, reactivate, delete) return HTTP 404 (not 403) to avoid
disclosing the existence of resources. The git server does the same, answering
a pkt-line 404 for non-owner access.

`POST /api/v1/workspaces` is the exception: it returns 403, because there is no
existing resource to enumerate. The other endpoint families also return 403 for
a missing scope.

---

## Error Response Schema

Error responses use the apikit error envelope format:

```json
{
  "error": {
    "code": 400,
    "message": "description of the error"
  }
}
```

Some endpoints include a machine-readable `error_type` field for programmatic
error classification. When present, the error envelope includes the additional
field:

```json
{
  "error": {
    "code": 409,
    "message": "a rebuild job is already queued or running for this workspace",
    "error_type": "concurrent_rebuild"
  }
}
```

The `error_type` field is omitted when not applicable. Known error types:

| Error Type | Endpoint | Description |
|------------|----------|-------------|
| `duplicate_merge` | POST /api/v1/workspaces/:slug/merges | A merge job for this source and target branch is already queued or running |
| `workspace_mode_mismatch` | POST /api/v1/workspaces/:slug/rebuild; every forge read route with `?target=upstream` | Workspace is not in `carry_patch` mode (a forge read with `target=upstream` also needs a non-empty `upstream_url`) |
| `no_active_patches` | POST /api/v1/workspaces/:slug/rebuild | No patches with status `active` or `conflict` |
| `concurrent_rebuild` | POST /api/v1/workspaces/:slug/rebuild | A rebuild job is already queued or running for this workspace |
| `origin_fetch_failed` | POST /api/v1/workspaces/:slug/sync, POST .../patches/:id/reset-to-origin | The origin fetch failed during a carry-patch sync with `PATCH_BRANCH_SOURCE=origin` or during a reset-to-origin. Distinguished from the upstream fetch failure by this error type |
| `missing_on_origin` | POST /api/v1/workspaces/:slug/patches/:id/reset-to-origin | The patch branch does not exist on the fork |
| `ref_changed` | POST /api/v1/workspaces/:slug/patches/:id/reset-to-origin | The patch branch was modified by another operation between the SHA resolution and the compare-and-swap write; retry |
| `workspace_busy` | archive, sync, reclone, rollback, batch rebase, rerere forget, reset-to-origin | Another operation currently holds the workspace lock; retry later. A rebuild holds the lock only in its fetch phase and final phase, so sync, rollback, rerere forget and batch rebase are rejected only then; archive and reclone are rejected for the whole rebuild |
| `token_bound` | every route a workspace-bound token may not call | 403. The token is bound to one workspace and the route is neither scoped to a workspace nor allowed for bound tokens. The message is `this token is bound to workspace <S>`, or `this token is bound to a workspace` on the routes apikit mounts (see [Workspace-Bound Tokens](#workspace-bound-tokens)) |
| `sandboxes_disabled` | GET /api/v1/sandbox-profiles and every sandbox lease route | The hub has no `[sandbox]` section in its `config.toml`, so sandboxes are not enabled. Answered with 404 to every authenticated caller before any scope is checked |
| `sandbox_capacity` | POST /api/v1/workspaces/:slug/sandboxes | The deployment's `max_sandboxes` limit on live leases is reached |
| `secret_missing` | POST /api/v1/workspaces/:slug/sandboxes | A secret the profile requires exists in no searched scope of the workspace |
| `sandbox_provider_error` | DELETE /api/v1/workspaces/:slug/sandboxes/:id | The provider could not destroy the sandbox of an `active` lease, or did not answer within 30 seconds. The message is the provider's, scrubbed; the lease is unchanged and the request can be repeated |
| `forge_api_url_invalid` | every forge route | `FORGE_API_URL` (`UPSTREAM_FORGE_API_URL` for the upstream) is set to something that is not an absolute `https://` URL with a host and without userinfo, query or fragment. 422; the message names the variable and the rule, not the value |
| `body_too_large` | every forge write route with a body-like field | An issue, pull-request or comment body, or the comment of a close, is longer than the forge accepts: 65,536 Unicode code points on GitHub, 1,000,000 on GitLab. 413; the message names the field, its length, the forge and the limit, and nothing is sent to the forge |
| `forge_unknown` | every forge route | The forge behind the remote URL cannot be told from its host or an API URL variable, or the remote URL names no repository. 422; nothing is sent to the forge |
| `forge_not_found` | every forge route | The forge answered 404. 404 |
| `forge_unauthenticated` | every forge route | The forge answered 401. 502 |
| `forge_forbidden` | every forge route | The forge answered 403 that is not a rate limit. 502 |
| `forge_rate_limited` | every forge route | A rate limit issuex could not wait out. 429, with `Retry-After` in whole seconds when known |
| `forge_rejected` | every forge route | The forge answered another 4xx. 422 |
| `forge_error` | every forge route | The forge answered 5xx, a response that could not be read, or a redirect to another host. 502. A write that issuex refuses because the workspace has no credential (`no authentication token configured`) is this too; it sends nothing and records no event |
| `forge_unreachable` | every forge route | The connection to the forge failed at DNS, TCP or TLS level. 502 |
| `forge_timeout` | every forge route | The 30-second deadline passed. 504 |

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
| `slug` | yes | string | Globally unique, URL-safe identifier. 3-64 characters; must start with a lowercase letter; must end with a lowercase letter or digit; only lowercase letters, digits, and hyphens allowed; no consecutive hyphens. |
| `git_url` | yes | string | Valid HTTPS or SSH git URL |
| `branch` | no | string | Git ref; defaults to null |
| `org_id` | no | string | Organization UUID or slug. Must reference an org the owner is a member of; when omitted or empty, the server auto-assigns the user's personal organization |
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
| `org_id` | string or null | Organization UUID or slug. Must reference an org the owner is a member of | Setting to `null` removes the organization association |
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

**Behavior:**

- For a workspace whose clone is `ready`, the hub first pushes all local
  branches to `origin` using the workspace's stored credentials (branches
  pushed through the hub's git server only exist in the hub's clone).
  If that push fails (or no credentials can be resolved) the workspace is
  still archived, but the local clone directory is kept and a warning is
  logged, so no commits are lost; reactivation reuses the retained clone.
- The archive acquires the workspace lock (see *Workspace Lock*). It is also
  rejected with `409` `workspace_busy` for the whole rebuild while a carry-patch
  rebuild is running, because the archive deletes the directory the rebuild
  worktree lives in.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Workspace is already archived |
| 401 | Unauthenticated request |
| 404 | Workspace not found; PAT lacks `workspaces:write` scope; workspace not owned by the authenticated user (anti-enumeration) |
| 409 | Clone is in progress; archive is rejected until the clone completes or fails. Another operation holds the workspace lock, or a rebuild is running (`error_type: workspace_busy`; archive is rejected for the whole rebuild, not only its fetch and final phases) |

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

Trigger a sync of the hub's local clone of the workspace
(`<workspace_root>/<slug>/trunk`) with a remote repository. **Which remote is
fetched depends on `workspace_mode`:**

| `workspace_mode` | Remote fetched | URL | Credentials | What is updated |
|---|---|---|---|---|
| `standard` | `origin` | `git_url` | `GIT_PAT`, or `GIT_USERNAME` + `GIT_PASSWORD` workspace secrets (none for public repos) | The workspace branch (`branch`, or the repository's default branch when unset) is fast-forwarded to `refs/remotes/origin/<branch>`; `head_sha` and `upstream_head_sha` advance |
| `carry_patch` | `upstream` (**not** `origin`) | `upstream_url` | `UPSTREAM_GIT_PAT`, or `UPSTREAM_GIT_USERNAME` + `UPSTREAM_GIT_PASSWORD`; falls back to the `origin` credentials when neither is set | All upstream branches are fetched into `refs/remotes/upstream/*` and the upstream default branch into `refs/remotes/upstream/HEAD`; `upstream_head_sha` advances, patches merged upstream are detected, and a rebuild of the integration branch is enqueued. `head_sha` and the integration branch are **not** touched by the sync itself -- the rebuild updates them |

By default, `origin` (the fork at `git_url`) is never fetched by sync: patch
branches are read from the hub's own clone, so they must be pushed to the
hub's git server (`/git/:org/:slug.git`), which also triggers an automatic
rebuild when a registered patch branch is pushed. The rebuilt integration
branch is only pushed back to `origin` when the workspace variable
`REBUILD_PUSH_INTEGRATION_BRANCH` is `"true"`.

When the workspace variable `PATCH_BRANCH_SOURCE` is set to `"origin"`, the
sync also fetches the `origin` remote and brings every registered patch
branch to the fork's tip. See
[Carry-Patch Sync Extension](#carry-patch-sync-extension) for details.

The rest of this section describes the **standard** sync path; see
[Carry-Patch Sync Extension](#carry-patch-sync-extension) below for the
carry-patch path. In standard mode, if a force-push is detected (the `origin`
branch has diverged from the local branch), the sync sets an error state with
instructions to use the `reset_to_upstream` query parameter for recovery. In
standard mode "upstream" in field and parameter names (`upstream_head_sha`,
`reset_to_upstream`) refers to `origin`.

**Authentication:** API Key, or PAT with `workspaces:sync` scope. The
workspace must be owned by the caller (admin tokens bypass); other
workspaces answer 404.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to sync |

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `reset_to_upstream` | boolean | `false` | Standard workspaces only. When `true`, force-resets the local workspace branch ref to the `origin` branch HEAD, ignoring ancestry. Used to recover from diverged (force-push) state. Ignored for carry-patch workspaces, whose integration branch is always recreated by a rebuild. |

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
| Reset to upstream | `reset_to_upstream=true`: fetches upstream, force-updates local ref to upstream HEAD regardless of ancestry, sets `head_sha` and `upstream_head_sha` to upstream HEAD | 200 |
| Diverged | Upstream HEAD is not a descendant of local HEAD (force-push detected); `sync_status` set to `"error"` | 409 |
| Fetch failure | Network error, auth failure, or repo open failure; `sync_status` set to `"error"` | 502 |
| Context cancelled | Request cancelled mid-sync (timeout or client disconnect); `sync_status` set to `"error"` | 504 |

**Response (success):** HTTP 200 OK with updated workspace JSON. The response
includes updated `sync_status`, `head_sha`, `upstream_head_sha`, and
`last_sync_at` fields.

**State Machine:**

The `sync_status` field follows a strict state machine:

- `idle` -> `syncing` -> `idle` (successful sync)
- `idle` -> `syncing` -> `error` (failed sync)
- `error` -> `syncing` -> `idle` (recovery after operator resolution)

The handler guarantees that `sync_status` never remains stuck in `"syncing"`:
a deferred cleanup function transitions to `"error"` on context cancellation
or unexpected failures.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Workspace is not active; clone is not ready; sync is disabled |
| 401 | Unauthenticated request |
| 403 | PAT lacks `workspaces:sync` scope |
| 404 | Workspace not found or not owned by the caller |
| 409 | Sync already in progress (concurrent sync rejected); another operation holds the workspace lock (`error_type: workspace_busy`; a rebuild holds it only during its fetch phase and final phase, not while it applies patches); upstream history has diverged (force-push detected) |
| 500 | Carry-patch `origin` mode: the patch refresh failed for a branch. The message is `failed to update patch branch <branch>` (ref write failed), `failed to compare patch branch <branch> with origin` (ancestry check failed) or `failed to reset working tree for patch branch <branch>` (ref moved, work-tree reset failed). See the Carry-Patch Sync Flow below |
| 502 | Upstream fetch failed (network, authentication, or repository error); credential resolution failed; origin fetch failed (`error_type: origin_fetch_failed`); failed to resolve origin credentials |
| 504 | Request context cancelled mid-sync (timeout or client disconnect) |

#### Carry-Patch Sync Extension

When the workspace is in `carry_patch` mode, the sync endpoint replaces the
standard fetch-and-fast-forward with a fetch of the **`upstream` remote**
(`upstream_url`) followed by an optional fetch of the **`origin` remote**
(when `PATCH_BRANCH_SOURCE` is `"origin"`), upstream merge detection, squash
merge detection, upstream force-push detection, patch branch refresh, and
automatic rebuild triggering.

Differences from the standard path:

- The preconditions above (active, clone ready, sync mode not `disabled`, no
  sync in progress) still apply, and the workspace lock is held for the
  duration (`409` `workspace_busy` while a merge or other sync runs, or
  while a rebuild is in its fetch phase or final phase; a rebuild applying
  patches in its worktree does not block it).
- `sync_status` is not moved to `syncing`/`error`; failures are reported by
  the HTTP status only (`502` when upstream credentials cannot be resolved or
  the upstream fetch fails, or when origin credentials cannot be resolved or
  the origin fetch fails; no workspace or patch state is modified).
- `head_sha` and the integration branch are not changed by the sync; they
  change when the enqueued rebuild completes (follow it via
  `GET /api/v1/workspaces/:slug/rebuilds/:id`).
- `reset_to_upstream` is ignored, and an upstream force-push is not an error:
  it is reported via `force_push_detected` and the rebuild re-applies the
  patches onto the rewritten upstream.
- `last_sync_at` is updated on every completed carry-patch sync, whether or
  not anything advanced, in both `hub` and `origin` modes.

The carry-patch fields are added **on top of** the standard workspace
JSON, not in place of it (16-REQ-5.1). The workspace record is re-read after
the carry-patch work completes, so `upstream_head_sha` and `last_sync_at`
reflect it:

```json
{
  "slug": "my-workspace",
  "status": "active",
  "clone_status": "ready",
  "sync_status": "idle",
  "workspace_mode": "carry_patch",
  "upstream_url": "https://github.com/upstream/repo.git",
  "integration_branch": "deploy",
  "upstream_head_sha": "abc123def456...",
  "last_sync_at": "2024-06-15T10:30:00Z",

  "patches_merged": ["feature/already-merged"],
  "rebuild_triggered": true,
  "rebuild_job_id": "d3b07384-d113-4ec5-8a4e-a12345678901",
  "force_push_detected": false,
  "origin_fetched": true,
  "patches_synced": [
    {
      "branch_name": "feature/auth-headers",
      "action": "fast_forwarded",
      "state": "in_sync",
      "local_sha": "abc123...",
      "origin_sha": "abc123..."
    }
  ],
  "patches_diverged": []
}
```

(Abbreviated -- every field from the Workspace Response Schema is present.)

| Field | Type | Description |
|-------|------|-------------|
| `patches_merged` | string[] | Branch names of patches detected as merged upstream (via ancestry check, content-based `git cherry` detection, or PR-number scanning) |
| `rebuild_triggered` | boolean | Whether a rebuild job was enqueued as a result of this sync |
| `rebuild_job_id` | string | ID of the enqueued rebuild job. Omitted entirely when no rebuild was enqueued -- it is never `null`. |
| `force_push_detected` | boolean | Whether the upstream HEAD is not a descendant of the previously stored upstream SHA, indicating a history rewrite. Informational only -- sync still proceeds. |
| `origin_fetched` | boolean | Always present. `true` when the fork (`origin`) was fetched in this sync (`PATCH_BRANCH_SOURCE=origin`), `false` otherwise. |
| `patches_synced` | array | Present only when `PATCH_BRANCH_SOURCE` is `"origin"` (empty `[]` when there are no candidates). Each element describes the per-branch outcome: `branch_name`, `action` (`none`, `created`, `fast_forwarded`, `replaced`), `state` (`in_sync`, `diverged`, `missing_on_origin`), `local_sha` (omitted when no local branch), `origin_sha` (omitted when fork has no such branch), and `replaced_sha` (present only for `replaced`). |
| `patches_diverged` | array of strings | Present only when `PATCH_BRANCH_SOURCE` is `"origin"`. Branch names whose state is `diverged`. Always `[]` under the `replace` policy. |

**Carry-Patch Sync Flow:**

1. Read `PATCH_BRANCH_SOURCE` and `PATCH_DIVERGENCE_POLICY` workspace
   variables. If `PATCH_BRANCH_SOURCE` is `"origin"`, resolve origin
   credentials (`GIT_PAT` / `GIT_USERNAME` + `GIT_PASSWORD`) before any
   fetch. A failure answers `502` with the message `failed to resolve origin credentials`.
2. Resolve upstream credentials via `resolveUpstreamAuth`: `UPSTREAM_GIT_PAT`,
   then `UPSTREAM_GIT_USERNAME` + `UPSTREAM_GIT_PASSWORD`, then the `origin`
   credentials (`GIT_PAT` / `GIT_USERNAME` + `GIT_PASSWORD`), then none.
3. Fetch from the `upstream` remote (not `origin`): every upstream branch into
   `refs/remotes/upstream/*` and the upstream default branch into
   `refs/remotes/upstream/HEAD`, which is the "upstream HEAD" used below and
   as the rebuild base.
4. If `PATCH_BRANCH_SOURCE` is `"origin"`, fetch from the `origin` remote
   with pruning (stale tracking refs are removed). A failure answers `502`
   with the message `origin fetch failed` and `error_type`
   `origin_fetch_failed`. The upstream tracking refs already updated are not
   rolled back.
5. Compare the new upstream HEAD against the stored `upstream_head_sha`.
6. If the stored SHA is non-empty and upstream advanced, check ancestry between
   the stored SHA and the new upstream HEAD. If the stored SHA is not an
   ancestor of the new HEAD, set `force_push_detected=true` (informational;
   sync continues). This comparison runs before the patch refresh.
7. If `PATCH_BRANCH_SOURCE` is `"origin"`, refresh each candidate patch
   branch (status `active`, `conflict` or `disabled`, excluding the
   integration branch) against the fork's tip, applying the first matching
   rule: fork missing, local missing (create), same commit, fast-forward, or
   diverged (replace or report, per `PATCH_DIVERGENCE_POLICY`). Ref writes
   use compare-and-swap (`git update-ref <ref> <new> <expected-old>`).
   Replaced branches are backed up under `refs/hub/replaced/<branch>`. Then
   persist each candidate patch's `origin_sync_state`, `origin_sha` and
   `origin_synced_at`, and clear those columns for `merged_upstream` and
   `deleted` rows. In `hub` mode, clear the three columns for every patch of
   the workspace instead. The refresh runs before the decision whether to
   look for merged patches, so a change that only moved patch branches (and
   did not advance upstream) still reaches step 8 and the rebuild decision in
   step 11.

   A refresh failure stops the refresh and ends the sync with `500` after
   step 7: outcomes already produced are persisted, a rebuild is enqueued for
   branches already moved (unless `AUTO_REBUILD_AFTER_SYNC` is `"false"`),
   audit events are emitted for the outcomes produced, and neither
   `upstream_head_sha` nor `last_sync_at` is written. The message tells the
   failures apart: `failed to update patch branch <branch>` (a ref write,
   including a lost compare-and-swap, failed), `failed to compare patch branch
   <branch> with origin` (the ancestry check failed; nothing was written) and
   `failed to reset working tree for patch branch <branch>` (the ref moved,
   so the branch counts as moved, but resetting the trunk work tree to the
   new tip failed).
8. Merge detection runs when upstream advanced or an `active` or `conflict`
   patch branch was moved by step 7. For each active patch, detect upstream
   merge using a multi-strategy approach controlled by the
   `SQUASH_MERGE_DETECTION` workspace variable:
   - **Ancestry check** (`git merge-base --is-ancestor`): detects standard merges
   - **Content-based detection** (`git cherry`): detects squash merges by comparing
     commit content (no pending commits = merged)
   - **PR-number scanning**: if `upstream_pr_url` is set, extracts the PR number
     and searches recent upstream commit messages for the GitHub squash-merge
     format `"Title (#NNN)"`
   - The `SQUASH_MERGE_DETECTION` variable accepts: `"ancestry_only"`,
     `"content_based"`, or `"both"` (default). Either ancestry or content-based
     signal is sufficient to mark a patch as `merged_upstream`.
9. Transition matched patches to `merged_upstream` status. In `origin` mode
   the origin columns of the patches newly marked merged are cleared again,
   because step 7 persisted them while the patch was still `active`. The
   response's `patches_synced` and `patches_diverged` keep describing the
   refresh in step 7.
10. Write `last_sync_at` and `updated_at` on every completed sync, whether
    or not anything advanced. `upstream_head_sha` is written only when
    upstream advanced.
11. If `AUTO_REBUILD_AFTER_SYNC` is not `"false"` (default is true) and the
    upstream ref advanced, a patch branch was moved (created, fast-forwarded
    or replaced for an `active` or `conflict` patch), or at least one patch
    was newly marked `merged_upstream`, enqueue a rebuild job. If a rebuild
    job is already queued or running, silently ignore the duplicate
    (`rebuild_triggered=false`).
12. Emit audit events (see [Hub Audit Events](#hub-audit-events) below).

**Standard Workspace Behavior:** When the workspace is in `standard` mode, the
carry-patch extension does not run at all -- the request takes the standard
sync path and the response is the plain workspace JSON, with none of
`patches_merged`, `rebuild_triggered`, `rebuild_job_id`, or
`force_push_detected` (16-REQ-5.E4).

---

### POST /api/v1/workspaces/:slug/reclone

Nuclear recovery operation that archives the workspace (pushing local commits
to upstream first), deletes the local clone directory, and re-clones from
upstream. The workspace status remains `"active"` throughout the entire reclone
lifecycle.

**Authentication:** API Key, or PAT with `workspaces:sync` scope. The
workspace must be owned by the caller (admin tokens bypass); other
workspaces answer 404. The archive-and-push step uses the workspace's stored
credentials, and the whole operation holds the workspace lock (`409`
`workspace_busy` while another operation runs). Reclone is also rejected with
`409` `workspace_busy` for the whole rebuild, because it deletes the directory
the rebuild worktree lives in.

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
   transitions `clone_status` from `'pending'` -> `'cloning'` -> `'ready'`.

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

## Prometheus Metrics Endpoint

### GET /metrics

Scrape operational metrics in Prometheus text exposition format.

**Authentication:** None. This endpoint is mounted outside the API auth
middleware group and does not require any Authorization header.

**Response:** HTTP 200 OK with `Content-Type: text/plain; version=0.0.4`.

The response includes all `afhub_*` metrics from the custom Prometheus registry:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `afhub_http_requests_total` | Counter | method, path, status | Total HTTP requests |
| `afhub_http_request_duration_seconds` | Histogram | method, path | HTTP request latency (buckets: 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0) |
| `afhub_agent_sessions_active` | Gauge | workspace | Currently active sessions |
| `afhub_agent_tokens_total` | Counter | workspace, model, direction | Token usage |
| `afhub_audit_events_total` | Counter | source, event_type | Audit events ingested |
| `afhub_audit_table_rows` | Gauge | table | Row count per audit table (sampled hourly) |
| `afhub_retention_errors_total` | Counter | step | Retention step failures |
| `afhub_retention_last_run_timestamp_seconds` | Gauge | — | Unix timestamp of last successful retention run |
| `afhub_sse_connections` | Gauge | — | Active SSE connections |
| `afhub_jobqueue_depth` | Gauge | type, status | Job queue depth (initialized by durable_job_queue) |
| `afhub_sandboxes_active` | Gauge | workspace | Sandbox leases in state `active`, per workspace: +1 when a lease becomes `active`, −1 when one ends, and recalibrated from the lease table at start and after every sweep. Listed from start with the `workspace=""` placeholder, which is also what remains while no lease is `active` |
| `afhub_sandbox_creates_total` | Counter | result | `Create`s the sandbox gateway accepted for a `leased` lease, by `result` (`created` when the lease became `active`, `failed` otherwise). A `Create` refused before that (unknown token, second `Create`, missed deadline, shutdown) is not counted |
| `afhub_sandbox_lease_seconds` | Histogram | — | Seconds from issue (`created_at`) to `ended_at` of a lease that became `destroyed`, whatever the reason (buckets: 60, 300, 600, 1200, 1800, 3600, 7200, 14400, 28800). A lease that lapsed to `expired` is not observed |

The `path` label uses the Echo route template (e.g. `/api/v1/sessions/:id`)
rather than the resolved URI to prevent cardinality explosion.

---

## Agent Session Endpoints

Agent session endpoints manage the lifecycle of agent sessions: opening,
closing, and reporting incremental token usage. Sessions track which
credentials and workspaces are active and accumulate token consumption for
cost analysis.

### POST /api/v1/sessions

Open a new agent session.

**Authentication:** API Key, PAT with `sessions:write` scope, or Admin Token.

**Request Body:**

```json
{
  "workspace_slug": "my-workspace",
  "run_id": "run-1",
  "node_id": "node-1",
  "archetype": "coder",
  "model": "claude-3-5-sonnet",
  "metadata": {"key": "value"}
}
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `id` | no | string | Client-provided UUID; generated if omitted |
| `workspace_slug` | yes | string | Target workspace |
| `run_id` | no | string | Run identifier for grouping |
| `node_id` | no | string | Task graph node identifier |
| `archetype` | no | string | Agent archetype label |
| `model` | no | string | Model name |
| `metadata` | no | object | Arbitrary JSON metadata |

**Response:** HTTP 201 Created with session JSON. HTTP 200 if `id` already
exists (idempotent).

```json
{
  "id": "uuid",
  "workspace_slug": "my-workspace",
  "credential_id": "cred-abc",
  "credential_type": "api_key",
  "status": "active",
  "started_at": "2026-09-01T00:00:00Z"
}
```

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | `workspace_slug` is missing or empty |
| 403 | Caller lacks `sessions:write` scope |
| 404 | Workspace does not exist or is not owned by the caller (admin tokens bypass) |

---

### POST /api/v1/sessions/:id/complete

Close an active session with a terminal status.

**Authentication:** Session owner or Admin Token. Requires `sessions:write`.

**Request Body:**

```json
{
  "status": "completed",
  "error_message": "",
  "duration_ms": 3000,
  "cache_creation_input_tokens": 100
}
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `status` | yes | string | Terminal status: `completed`, `failed`, or `timeout` |
| `error_message` | no | string | Error details (for `failed` status) |
| `duration_ms` | no | integer | Session duration in milliseconds |
| `cache_creation_input_tokens` | no | integer | Cache creation tokens |

**Response:** HTTP 200 with updated session JSON. Idempotent on already-terminal sessions.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | `status` is `terminated` (internal-only) or invalid |
| 403 | Caller is not the session owner and not admin |
| 404 | Session not found |
| 409 | Concurrent close attempt (session already transitioning) |

---

### POST /api/v1/sessions/:id/usage

Report incremental token usage for an active session.

**Authentication:** Session owner or Admin Token. Requires `sessions:write`.

**Request Body:**

```json
{
  "model": "claude-3-5-sonnet",
  "input_tokens": 200,
  "output_tokens": 80,
  "cache_read_tokens": 10
}
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `id` | no | string | Client-provided UUID; generated if omitted |
| `model` | yes | string | Model name |
| `input_tokens` | no | integer | Input tokens consumed (must be >= 0) |
| `output_tokens` | no | integer | Output tokens consumed (must be >= 0) |
| `cache_read_tokens` | no | integer | Cache read tokens consumed (must be >= 0) |

**Response:** HTTP 201 Created with token_usage record JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | `model` is missing or empty |
| 403 | Caller is not the session owner and not admin |
| 404 | Session not found |
| 409 | Session is not active |
| 422 | Any token count is negative |

---

### GET /api/v1/sessions

List agent sessions with filtering and cursor pagination.

**Authentication:** API Key, PAT with `sessions:read` scope, or Admin Token.

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `workspace_slug` | string | — | Filter by workspace |
| `run_id` | string | — | Filter by run |
| `status` | string | — | Filter by status (active, completed, failed, timeout, terminated) |
| `credential_type` | string | — | Filter by credential type |
| `since` | string (RFC 3339) | — | Filter sessions started after this time |
| `order` | string | `desc` | Sort order: `asc` or `desc` |
| `limit` | integer | `50` | Page size (max 500) |
| `cursor` | string | — | Keyset pagination cursor |

**Response:** HTTP 200 with paginated sessions including `token_summary`.

```json
{
  "sessions": [...],
  "next_cursor": "string|null",
  "has_more": true
}
```

Non-admin callers see only sessions in workspaces they own. Admin tokens see
all workspaces.

---

### GET /api/v1/sessions/:id

Fetch a single session by ID with token summary.

**Authentication:** API Key, PAT with `sessions:read` scope, or Admin Token.

**Response:** HTTP 200 with full session JSON including `token_summary`.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 403 | Non-admin caller cannot access the session's workspace |
| 404 | Session not found |

---

### GET /api/v1/sessions/:id/usage

Query paginated token usage records for a session.

**Authentication:** API Key, PAT with `sessions:read` scope, or Admin Token.

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `order` | string | `desc` | Sort order: `asc` or `desc` |
| `limit` | integer | `100` | Page size (max 1000) |
| `cursor` | string | — | Keyset pagination cursor |

**Response:** HTTP 200 with paginated records and unbounded totals.

```json
{
  "session_id": "sess-1",
  "records": [...],
  "totals": {
    "total_input_tokens": 500,
    "total_output_tokens": 200,
    "total_cache_read_tokens": 30,
    "models_used": ["claude-3-5-sonnet"]
  },
  "next_cursor": "string|null",
  "has_more": false
}
```

The `totals` field always reflects the aggregate of all token_usage records
for the session, independent of the current page.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Invalid cursor |
| 403 | Non-admin caller cannot access the session's workspace |
| 404 | Session not found |

---

### GET /api/v1/workspaces/:slug/cost

Retrieve aggregated token usage for a workspace over a time period.

**Authentication:** API Key, PAT with `sessions:read` scope, or Admin Token.

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `group_by` | string | `day` | Breakdown dimension: `day`, `session`, or `model` |
| `since` | string (RFC 3339) | 24h ago | Start of time window |
| `until` | string (RFC 3339) | now | End of time window |

**Response:** HTTP 200 with aggregated cost data.

```json
{
  "workspace": "my-workspace",
  "period": {"since": "...", "until": "..."},
  "totals": {
    "input_tokens": 1000,
    "output_tokens": 500,
    "cache_read_tokens": 100,
    "sessions": 3
  },
  "breakdown": [
    {"date": "2026-09-01", "input_tokens": 1000, "output_tokens": 500, "cache_read_tokens": 100, "sessions": 3}
  ]
}
```

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | `since >= until`; invalid `group_by` value |
| 403 | Non-admin caller cannot access the workspace |

---

## Patch Endpoints

Patch endpoints manage the ordered list of patch branches for carry-patch
workspaces. Patches represent git branches that are applied on top of the
upstream base when rebuilding the integration branch.

**Note:** Patch endpoints do not enforce workspace ownership. Any
authenticated user with an API key, or a PAT with the appropriate scope
(`patches:read` or `patches:write`), can read or modify patches for any
workspace.

### Patch Response Schema

All patch endpoints that return patch data use the following JSON schema:

```json
{
  "id": "uuid-string",
  "workspace_slug": "my-workspace",
  "branch_name": "feature/auth-headers",
  "position": 1,
  "status": "active",
  "conflict_files": ["src/config.go"],
  "upstream_pr_url": "https://github.com/upstream/repo/pull/42",
  "description": null,
  "deleted_at": null,
  "added_at": "2024-01-01T00:00:00Z",
  "updated_at": "2024-01-01T00:00:00Z",
  "origin_sync_state": "in_sync",
  "origin_sha": "abc123def456...",
  "origin_synced_at": "2024-06-15T10:30:00Z"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `id` | string (UUID) | Unique identifier for the patch |
| `workspace_slug` | string | Slug of the workspace this patch belongs to |
| `branch_name` | string | Name of the git branch associated with this patch |
| `position` | integer | 1-based application order; lower values are applied first |
| `status` | string | Patch status: `"active"`, `"merged_upstream"`, `"conflict"`, `"disabled"`, or `"deleted"` |
| `conflict_files` | string[] or absent | List of conflicting file paths; present only when the patch has unresolved conflicts |
| `upstream_pr_url` | string or null | URL of the corresponding upstream pull request |
| `description` | string or null | Free-form description of the patch |
| `deleted_at` | string (RFC 3339) or null | Timestamp of soft-deletion; null for non-deleted patches |
| `added_at` | string (RFC 3339) | Timestamp of when the patch was added |
| `updated_at` | string (RFC 3339) | Timestamp of when the patch was last modified |
| `origin_sync_state` | string or null | Relationship between the local patch branch and the fork after the last sync: `"in_sync"`, `"diverged"`, or `"missing_on_origin"`. Null when the workspace uses `hub` mode or the patch has not been synced. Omitted when null. |
| `origin_sha` | string or null | SHA of the fork tip for this patch branch at the last sync. Null when the fork has no such branch or the workspace uses `hub` mode. Omitted when null. |
| `origin_synced_at` | string (RFC 3339) or null | Timestamp of the last origin sync for this patch. Null when the workspace uses `hub` mode. Omitted when null. |

---

### POST /api/v1/workspaces/:slug/patches

Add one or more patch branches to a carry-patch workspace's patch list.
Supports both single-object and batch (JSON array) request bodies.

**Authentication:** API Key, or PAT with `patches:write` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to add the patch to |

#### Single Patch Request

```json
{
  "branch_name": "feature/auth-headers",
  "position": 2,
  "upstream_pr_url": "https://github.com/upstream/repo/pull/42",
  "description": "Add authentication headers to API",
  "skip_branch_check": false,
  "if_not_exists": false
}
```

| Field | Required | Type | Constraints |
|-------|----------|------|-------------|
| `branch_name` | yes | string | Must not be empty; must not equal the workspace's `integration_branch`; must not already exist in the patch list for this workspace |
| `position` | no | integer | 1-based insertion position; must be >= 1; values > (max position + 1) are clamped to append; when omitted, appends at the end |
| `upstream_pr_url` | no | string | URL of the upstream pull request |
| `description` | no | string | Free-form description |
| `skip_branch_check` | no | boolean | When `true`, skips validation that the branch exists in the git repository. Default `false`. |
| `if_not_exists` | no | boolean | When `true` and the branch already exists in the patch list, returns HTTP 200 with the existing record instead of HTTP 409. Default `false`. |

**Response:** HTTP 201 Created with patch JSON. When `if_not_exists` is `true`
and the branch already exists, returns HTTP 200 with the existing patch record.

#### Batch Patch Request

Send a JSON array of patch objects to insert multiple patches atomically:

```json
[
  {"branch_name": "feature/a", "position": 1},
  {"branch_name": "feature/b"},
  {"branch_name": "feature/c", "upstream_pr_url": "https://github.com/upstream/repo/pull/99"}
]
```

All patches in the batch are validated and inserted within a single database
transaction. If any patch fails validation, the entire batch is rejected and
no patches are inserted.

Every element is syntax-validated first, in array order (`branch_name`,
integration branch, `position`). Branch resolution starts only after all
elements pass, so a malformed later element is reported without fetching or
creating refs for the earlier ones. For example, a batch whose first element
names a branch that does not exist and whose second element has `position: 0`
answers `patch[1]: position must be >= 1`, not a missing-branch error for
element 0.

**Response:** HTTP 201 Created with a JSON array of patch objects.

**Behavior:**

- When `position` is omitted, the patch is appended at position = (current max
  position + 1).
- When `position` is specified, existing patches at that position or higher are
  shifted down by one to make room.
- The patch is assigned a UUID, status `"active"`, and RFC 3339 timestamps.
- In batch mode, duplicate `branch_name` values within the batch are rejected
  with HTTP 409.

**Branch Resolution:**

Unless `skip_branch_check` is `true`, the server resolves the branch name
using the following order. The first step that succeeds ends the resolution:

1. **`local`** — `refs/heads/<name>` exists in the trunk and resolves to a
   commit. No ref is written.
2. **`origin_tracking`** — `refs/remotes/origin/<name>` exists in the trunk
   and resolves to a commit. A local branch `refs/heads/<name>` is created at
   that commit.
3. **`origin_fetch`** — only when `PATCH_BRANCH_SOURCE` is `origin`. The
   branch is fetched from the fork's `origin` remote, and if
   `refs/remotes/origin/<name>` now resolves to a commit, the local branch is
   created at that commit.

Only fully qualified refs (`refs/heads/<name>`) are checked. Tags, bare SHAs
and remote-qualified names (e.g. `origin/x`) are no longer accepted as branch
names.

When `skip_branch_check` is `true`, no ref lookup, no fetch, no credential
resolution, no local branch creation and no workspace lock is performed for
that element. In a batch, `skip_branch_check` applies per element.

A batch resolves every element before inserting any row. The first failure
rejects the whole request. Local branches already created for earlier elements
stay in place (they are harmless refs found as `local` on the next attempt).

`PATCH_BRANCH_SOURCE` is read once per request. Every element of a batch is
resolved in the mode read for the request, even if the variable changes while
the request runs; the change takes effect on the next request.

**Audit Metadata:**

The single-patch `hub.patch.create` audit event includes a `branch_resolution`
metadata key with one of the values `local`, `origin_tracking`, `origin_fetch`
or `skipped` (when `skip_branch_check` was `true`). The key is omitted when no
branch-check hook is registered. Batch registration does not emit
`hub.patch.create` events.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | `branch_name` is absent or empty |
| 400 | `branch_name` equals the workspace's `integration_branch` |
| 400 | Workspace is in `standard` mode (not `carry_patch`) |
| 400 | Workspace does not exist or is not active |
| 400 | `position` is less than 1 |
| 400 | Branch does not exist in repository or on origin (when `skip_branch_check` is `false`). In a batch the message is prefixed `patch[i]: `. |
| 401 | Unauthenticated request |
| 403 | PAT lacks `patches:write` scope |
| 409 | `branch_name` already exists in the patch list for this workspace (unless `if_not_exists` is `true`) |
| 409 | Duplicate `branch_name` within a batch request |
| 409 | Another operation holds the workspace lock (`error_type: workspace_busy`). Returned when the resolver needs to write a ref but the trunk is locked by another operation. |
| 502 | Origin fetch failed (`error_type: origin_fetch_failed`). Returned when `PATCH_BRANCH_SOURCE` is `origin` and the fetch from the fork fails due to a network or authentication error. |
| 502 | Failed to resolve origin credentials. Returned when the credential resolver cannot produce credentials for the fork. |

---

### GET /api/v1/workspaces/:slug/patches

List all non-deleted patches for a workspace, ordered by position ascending.

**Authentication:** API Key, or PAT with `patches:read` or `patches:write`
scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to list patches for |

**Response:** HTTP 200 OK with a JSON array of patch objects ordered by
position. Returns an empty array `[]` if no patches exist. Soft-deleted
patches (status `"deleted"`) are excluded from the listing.

**Behavior:**

- Returns all non-deleted patches in position order for carry-patch workspaces.
- Returns an empty array `[]` if the workspace has no patches.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `patches:read` scope |
| 404 | Workspace does not exist |

**Note:** Standard (non-carry_patch) workspaces return HTTP 200 with an empty
array `[]` rather than an error.

---

### GET /api/v1/workspaces/:slug/patches/:id

Get a single patch by ID.

**Authentication:** API Key, or PAT with `patches:read` or `patches:write`
scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |
| `:id` | The patch UUID to retrieve |

**Response:** HTTP 200 OK with the patch JSON object.

The response is the standard patch object. When `refs/hub/replaced/<branch>`
resolves to a commit in the workspace trunk, the response also carries
`replaced_sha` -- the full 40-character SHA of the backup ref. The field is
omitted when the ref does not exist, when the workspace's trunk is not on disk
(for example an archived workspace), when no recovery hook is registered, and
when the lookup fails. A lookup failure is logged at warn level and never fails
the request.

To recover a discarded tip:

```
git fetch <hub_url> refs/hub/replaced/<branch>
```

A soft-deleted patch is returned with its `deleted` status.

**Error Codes:**

| Status | Condition |
|--------|----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `patches:read` scope |
| 404 | Patch ID does not exist for the given workspace; workspace not found or not owned by the caller |

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
| `status` | no | string | Must be one of: `"active"`, `"merged_upstream"`, `"conflict"`, `"disabled"`. `"deleted"` is a rebuild outcome and cannot be set here; use `DELETE` to remove a patch and `.../restore` to bring it back |
| `description` | no | string | Free-form description |
| `upstream_pr_url` | no | string | URL of the upstream pull request |

**Response:** HTTP 200 OK with the updated patch JSON object.

**Behavior:**

- Only the provided fields are updated; omitted fields remain unchanged.
- `branch_name` is immutable after creation -- if included in the request body,
  it is silently ignored.
- When `position` changes, other patches in the workspace are shifted to
  maintain a contiguous 1-based sequence with no gaps or duplicates.
- `updated_at` is set to the current RFC 3339 timestamp on every successful
  update.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | `status` is not one of the valid values, or is `"deleted"` |
| 400 | `position` is less than 1 or greater than the total number of patches |
| 401 | Unauthenticated request |
| 403 | PAT lacks `patches:write` scope |
| 404 | Patch ID does not exist for the given workspace; workspace not found or not owned by the caller |
| 409 | Patch is soft-deleted; restore it first |

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
- Compacts positions for remaining patches so there are no gaps -- positions are
  reassigned as a contiguous 1-based sequence.
- The delete and position compaction are performed atomically within a single
  database transaction.
- After the row is deleted, the backup ref `refs/hub/replaced/<branch>` is
  removed on a best-effort basis. A missing ref counts as success. A missing
  trunk (for example an archived workspace) is logged at info level. Any other
  failure is logged at warn level and does not change the response. No
  workspace lock is taken for the ref removal.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `patches:write` scope |
| 404 | Patch ID does not exist for the given workspace |
| 500 | Database transaction failure during delete and compaction (no partial state persisted) |

---

### POST /api/v1/workspaces/:slug/patches/:id/restore

Restore a soft-deleted patch back to active status.

**Authentication:** API Key, or PAT with `patches:write` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |
| `:id` | The patch UUID to restore |

**Response:** HTTP 200 OK with the restored patch JSON object.

**Behavior:**

- Transitions the patch from `"deleted"` status back to `"active"`.
- Clears the `deleted_at` timestamp.
- Assigns the patch a new position at the end of the list (max position + 1).
- `updated_at` is set to the current RFC 3339 timestamp.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Workspace does not exist or is not active; workspace is not in `carry_patch` mode; patch is not in `"deleted"` status |
| 401 | Unauthenticated request |
| 403 | PAT lacks `patches:write` scope |
| 404 | Patch ID does not exist for the given workspace |
| 500 | Internal server error |

---

### POST /api/v1/workspaces/:slug/patches/:id/reset-to-origin

Reset a patch branch to the fork's current tip, regardless of
`PATCH_DIVERGENCE_POLICY`. The endpoint fetches the branch from the fork,
compares the tips, and moves the local branch to the fork's tip. If the move
discards commits (diverged or the fork tip is behind the local tip), the old
tip is saved under `refs/hub/replaced/<branch>`, overwriting any earlier
backup. A fast-forward does not touch an existing backup.

**Authentication:** API Key, or PAT with `patches:write` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |
| `:id` | The patch UUID to reset |

**Request Body:** None.

**Response:** HTTP 200 OK with the patch JSON object as `GET` renders it
(including `replaced_sha` when a backup exists after the reset), plus:

| Field | Type | Description |
|-------|------|-------------|
| `replaced_sha` | string | Full 40-character SHA of the backup ref, when present |
| `rebuild_triggered` | boolean | Whether a rebuild job was enqueued |
| `rebuild_job_id` | string | ID of the enqueued rebuild job. Omitted when no rebuild was enqueued. |

To recover a discarded tip:

```
git fetch <hub_url> refs/hub/replaced/<branch>
```

**Preconditions (checked in order):**

1. Workspace must be active and in `carry_patch` mode.
2. Workspace `clone_status` must be `ready`.
3. Patch must exist.
4. Patch branch must not be the integration branch.
5. Patch status must be `active`, `conflict` or `disabled`.

**Reset rules:**

- *Local branch missing:* Create the branch at the fork tip. Action `created`.
  No backup.
- *Same commit:* Write nothing. Action `none`. Response is still `200`.
- *Fast-forward:* Move the local branch. Action `fast_forwarded`. No backup.
- *Anything else (diverged):* Save the local tip to
  `refs/hub/replaced/<branch>`, then move the local branch. Action `replaced`.

When `PATCH_BRANCH_SOURCE` is `origin`, a completed reset records the patch
as `in_sync` with the fork tip SHA. In `hub` mode, no origin state is written.

When the branch moved and the patch status is `active` or `conflict`, a
rebuild job is enqueued (same deduplication as sync), unless
`AUTO_REBUILD_AFTER_SYNC` is `"false"`. A `disabled` patch does not trigger
a rebuild.

When the moved branch is checked out in the trunk, the work tree follows it
(`git reset --hard`). A failure of that reset after the branch has moved does
not fail the request: the response, the recorded state, the rebuild and the
audit event all describe the move, and the failure is logged at error level.
The `hub.patch.reset` event (and `hub.patch.replace` for action `replaced`) is
emitted as soon as the reset succeeds, before the patch is read back for the
response.

**Error Codes:**

| Status | Condition |
|--------|----------|
| 400 | Workspace is not active; workspace is not in `carry_patch` mode; patch branch is the integration branch |
| 404 | Workspace not found or not owned by the caller; patch not found |
| 409 | Workspace clone is not ready; patch status cannot be reset (`merged_upstream` or `deleted`); branch does not exist on origin (`error_type: missing_on_origin`); patch branch changed during reset (`error_type: ref_changed`); another operation holds the workspace lock (`error_type: workspace_busy`) |
| 500 | Patch reset is not configured; failed to update patch branch (including a hub configured without its origin fetch or workspace lock); failed to fetch updated patch (the branch moved, but the patch row could not be read back; the audit event was still emitted) |
| 502 | Origin fetch failed (`error_type: origin_fetch_failed`); failed to resolve origin credentials |

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
| `patch_ids` | yes | string[] | Must contain exactly all patch IDs for the workspace -- no missing, no extra, no duplicates |

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
| 400 | Database transaction failure during bulk position update (all errors from the reorder function are returned as 400) |
| 401 | Unauthenticated request |
| 403 | PAT lacks `patches:write` scope |

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
      "id": "3f2a9c0e7b1d4c5a8e6f0b2d1c3a4e5f6a7b8c9d",
      "path": "src/config.go",
      "recorded_at": "2024-06-15T10:30:00Z",
      "resolved": true
    },
    {
      "id": "9d8c7b6a5f4e3d2c1b0a9f8e7d6c5b4a3f2e1d0c",
      "path": null,
      "recorded_at": "2024-06-14T08:15:00Z",
      "resolved": false
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `resolutions` | array | List of recorded rerere entries, newest first |
| `resolutions[].id` | string | The rr-cache entry name (git's conflict hash); stable identifier for `DELETE` |
| `resolutions[].path` | string or null | File path of the conflict. git only records the path while a conflict is in progress (`MERGE_RR`), so it is null for entries recorded by earlier rebuilds |
| `resolutions[].recorded_at` | string (RFC 3339) or null | Modification time of the rr-cache entry |
| `resolutions[].resolved` | boolean | `true` when the entry has a `postimage` (a recorded resolution that rerere will replay); `false` when only the `preimage` (conflict pattern) has been seen |

**Edge Cases:**

- If the `rr-cache` directory does not exist or is empty, returns `{"resolutions": []}`.
- Entries without a `preimage` file are skipped.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `workspaces:read` scope |
| 404 | Workspace not found |

---

### DELETE /api/v1/workspaces/:slug/rerere/\*pathspec

Forget a recorded rerere entry. The parameter is either an entry `id` from
the list endpoint (the rr-cache directory is removed) or a file path, in
which case `git rerere forget <path>` is executed; the latter only works
while that path is in a conflicted state, which is why the id form is
preferred for housekeeping.

**Authentication:** API Key, or PAT with `workspaces:write` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |
| `*pathspec` | Entry id (40 or 64 hex characters) or the file path to forget (wildcard parameter captures slashes, e.g. `src/config.go`). A path must not start with `-` |

**Response (success):** HTTP 204 No Content

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Empty or invalid pathspec |
| 401 | Unauthenticated request |
| 403 | PAT lacks `workspaces:write` scope |
| 404 | Workspace not found or not owned by the caller; no entry with the given id; no recorded resolution for the given path |
| 409 | Another operation holds the workspace lock (`error_type: workspace_busy`); a rebuild holds it only during its fetch phase and final phase, not while it applies patches |

---

## Rebuild Endpoints

Rebuild endpoints allow operators to submit, list, inspect, cancel, requeue,
rollback, and preview rebuild jobs for carry-patch workspaces. A rebuild
reconstructs the integration branch from upstream HEAD plus all active patches.

**Note:** Rebuild endpoints do not enforce workspace ownership. Any
authenticated user with an API key, or a PAT with the appropriate scope
(`rebuilds:read` or `rebuilds:write`), can view or submit rebuild jobs for
any workspace.

### Rebuild Job Response Schema

```json
{
  "id": "<uuid>",
  "status": "completed",
  "strategy": "rebase",
  "error": "",
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
  ],
  "integration_head_sha": "<sha>",
  "previous_integration_head_sha": "<sha>",
  "integration_branch_pushed": true
}
```

| Field | Type | Description |
|-------|------|-------------|
| `id` | string (UUID) | Unique job identifier |
| `status` | string | Job status: `"queued"`, `"running"`, `"completed"`, `"failed"`, `"dead_letter"`, or `"cancelled"` |
| `strategy` | string | Rebuild strategy: `"rebase"` or `"merge"` (omitted when empty) |
| `error` | string | Error details for failed jobs (omitted when empty) |
| `created_at` | string (RFC 3339) | Timestamp of job creation |
| `completed_at` | string (RFC 3339) or null | Timestamp of job completion; null for non-terminal states |
| `patch_results` | array or absent | Per-patch rebuild outcomes; present for completed jobs and for running jobs with intermediate progress |
| `integration_head_sha` | string | SHA of the integration branch HEAD after rebuild; empty for non-completed jobs |
| `previous_integration_head_sha` | string | SHA of the integration branch HEAD before rebuild; empty for first-ever rebuild or non-completed jobs |
| `upstream_head_sha` | string | SHA of the upstream base the rebuild started from (`refs/remotes/upstream/HEAD`, falling back to `refs/remotes/upstream/<workspace branch>` and then `FETCH_HEAD`) |
| `integration_branch_pushed` | boolean | `true` when the `REBUILD_PUSH_INTEGRATION_BRANCH` workspace variable is `"true"` and the rebuilt integration branch was force-pushed to `origin`; omitted otherwise |

Fields with `omitempty` tags (`strategy`, `error`, `patch_results`,
`integration_head_sha`, `previous_integration_head_sha`,
`integration_branch_pushed`) are omitted from the response when empty rather
than included as null.

#### Patch Result Schema

Each entry in the `patch_results` array:

| Field | Type | Description |
|-------|------|-------------|
| `patch_id` | string (UUID) | The patch ID |
| `branch_name` | string | The git branch name |
| `position` | integer | Position in the patch stack |
| `status` | string | Outcome: `"success"`, `"conflict"`, or `"skipped"` |
| `skipped_reason` | string | Reason for skipping: `"merged_upstream"`, `"disabled"`, `"deleted"`, or `"branch_not_found"` (omitted when not skipped) |
| `new_head_sha` | string or null | New HEAD SHA after successful application; null on conflict or skip |
| `conflict_files` | string[] | List of conflicting file paths (omitted when not conflicted) |
| `source_sha` | string | The commit the patch branch (`refs/heads/<branch_name>`) resolved to when patch application began; this is the tip the rebuild applied. Omitted for patches that were not attempted (skipped by status or `branch_not_found`) |

In the rebuild job objects this field is addressed as
`patch_results[].source_sha`; it also appears in the intermediate progress of a
running job. If a patch branch (or the upstream base) moves while a rebuild is
running, the run still applies the `source_sha` it started with and a
follow-up rebuild (submitted by `system:stale-snapshot`) is queued afterwards.

#### Rebuild Progress Tracking

For running rebuild jobs, the `patch_results` field exposes intermediate
progress as each patch completes. This allows clients to poll the job status
endpoint to see which patches have been processed so far without waiting for
the entire rebuild to finish.

### POST /api/v1/workspaces/:slug/rebuild

Submit a new rebuild job for the workspace.

**Required scope:** `rebuilds:write`

**Preconditions:**

- Workspace must be in `carry_patch` mode
- Workspace status must be `active` with clone_status `ready`
- At least one patch must have status `active` or `conflict`
- No existing rebuild job may be queued or running for this workspace

**Request Body (optional):**

```json
{
  "strategy": "rebase",
  "fail_mode": "fail_fast"
}
```

| Field | Required | Type | Constraints |
|-------|----------|------|-------------|
| `strategy` | no | string | `"rebase"` (default) or `"merge"`. Overrides the `REBUILD_STRATEGY` workspace variable when provided. |
| `fail_mode` | no | string | `"fail_fast"` (default) or `"continue"`. When `"continue"`, the rebuild skips conflicting patches and continues processing remaining patches instead of aborting. Overrides the `REBUILD_FAIL_MODE` workspace variable when provided. |

When the request body is omitted, `strategy` defaults to the `REBUILD_STRATEGY`
workspace variable (or `"rebase"` if unset), and `fail_mode` defaults to the
`REBUILD_FAIL_MODE` workspace variable. If neither the request body nor the
workspace variable provides a value, `fail_mode` is left empty at enqueue time
and the executor applies `"fail_fast"` as the runtime default.

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

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 202 | Job enqueued successfully |
| 400 | Workspace not in carry_patch mode (`error_type: workspace_mode_mismatch`), not active, clone not ready, or no active patches (`error_type: no_active_patches`); invalid `strategy` or `fail_mode` value |
| 401 | Unauthenticated request |
| 403 | Missing required scope `rebuilds:write` |
| 404 | Workspace not found |
| 409 | A rebuild job is already queued or running for this workspace (`error_type: concurrent_rebuild`) |
| 500 | Internal server error (database error, payload marshal failure, or job enqueue failure) |

#### Rebuild Algorithm

The rebuild executor processes patches in position order:

1. Fetch from the upstream remote and resolve the upstream base (under the
   workspace lock).
2. Create a detached git worktree at the upstream base under
   `<WORKSPACE_ROOT>/<slug>/rebuild/<job id>`; the workspace's trunk checkout
   is never changed. No lock is held while patches are applied.
3. Enable git rerere for conflict resolution replay and resolve each patch
   branch to its tip SHA (`patch_results[].source_sha`).
4. For each patch in position order, inside the worktree:
   - **Skip** patches with status `merged_upstream`, `disabled`, or `deleted`.
   - **Skip** patches whose branch does not exist in the repository.
   - **Apply** using the configured strategy (`rebase` = cherry-pick each
     unique commit; `merge` = `git merge --no-ff`).
   - On conflict, attempt rerere auto-resolution. If unresolved conflicts
     remain:
     - **fail_fast mode**: abort the rebuild immediately.
     - **continue mode**: mark the patch as `conflict`, reset to the
       pre-patch state, record the conflict, and continue with the next patch.
5. Re-acquire the workspace lock and force-update the integration branch ref
   (`update-ref`) to the worktree's final HEAD.
6. Soft-delete patches that were in `merged_upstream` status (sets status to
   `"deleted"` with a `deleted_at` timestamp rather than permanently removing
   the row).
7. Compact remaining patch positions, then remove the worktree. If a patch
   tip or the upstream base moved during the run, one follow-up rebuild is
   enqueued (`submitted_by: system:stale-snapshot`, audit event
   `hub.rebuild.followup`).

#### Auto-Trigger on Push

When a git push updates a branch that is a registered patch in a carry-patch
workspace, a rebuild is automatically enqueued (unless the
`AUTO_REBUILD_AFTER_PUSH` workspace variable is set to `"false"`). The push
response is not affected -- the rebuild runs asynchronously.

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
      "patch_results": [...],
      "integration_head_sha": "<sha>",
      "previous_integration_head_sha": "<sha>"
    }
  ]
}
```

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `rebuilds:read` scope |
| 500 | Internal server error (failed to list rebuild jobs) |

**Note:** Workspace existence is not validated. A query for a non-existent
workspace slug returns HTTP 200 with an empty jobs array rather than 404.

### GET /api/v1/workspaces/:slug/rebuilds/:id

Get a single rebuild job by ID, including `patch_results` for completed jobs
and intermediate progress for running jobs.

**Required scope:** `rebuilds:read`

**Response (200 OK):** Single rebuild job record (see Rebuild Job Response
Schema above).

| Status | Condition |
|--------|-----------|
| 200 | Job found |
| 401 | Unauthenticated request |
| 403 | Missing required scope `rebuilds:read` |
| 404 | Job not found or does not belong to this workspace |

### DELETE /api/v1/workspaces/:slug/rebuilds/:id

Cancel a queued rebuild job.

**Required scope:** `rebuilds:write`

**Response:** HTTP 200 OK with `{"status": "cancelled"}`.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | Missing required scope `rebuilds:write` |
| 404 | Job not found or does not belong to this workspace |
| 409 | Job cannot be cancelled (not in `queued` status) |

### POST /api/v1/workspaces/:slug/rebuilds/:id/requeue

Requeue a dead-lettered rebuild job.

**Required scope:** `rebuilds:write`

**Response:** HTTP 200 OK with the updated rebuild job record.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | Missing required scope `rebuilds:write` |
| 404 | Job not found or does not belong to this workspace |
| 409 | Job is not in `dead_letter` status, or an active rebuild job already exists for this workspace |

### POST /api/v1/workspaces/:slug/rebuilds/:id/rollback

Roll back the integration branch to the state before a completed rebuild by
resetting it to the `previous_integration_head_sha` stored in the rebuild
result.

**Required scope:** `rebuilds:write`

**Response (200 OK):**

```json
{
  "rolled_back_to": "<sha>"
}
```

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | Missing required scope `rebuilds:write` |
| 404 | Job not found or does not belong to this workspace |
| 409 | No previous integration head SHA available to roll back to (e.g. first-ever rebuild, or job has no result); another operation holds the workspace lock (`error_type: workspace_busy`; a rebuild holds it only during its fetch phase and final phase, not while it applies patches) |
| 500 | Failed to initialize git runner or failed to reset integration branch |

### GET /api/v1/workspaces/:slug/rebuild-preview

Perform a read-only conflict prediction for each active patch in position
order using `git merge-tree --write-tree`. This endpoint does not modify any
git refs, branches, or patch statuses.

**Required scope:** `rebuilds:read`

**Response (200 OK):**

```json
{
  "patch_results": [
    {
      "patch_id": "<uuid>",
      "branch_name": "feature/foo",
      "position": 1,
      "status": "would_succeed",
      "tree_sha": "<sha>",
      "conflict_files": []
    },
    {
      "patch_id": "<uuid>",
      "branch_name": "feature/bar",
      "position": 2,
      "status": "would_conflict",
      "conflict_files": ["src/main.go"]
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `patch_results[].patch_id` | string (UUID) | The patch ID |
| `patch_results[].branch_name` | string | The git branch name |
| `patch_results[].position` | integer | Position in the patch stack |
| `patch_results[].status` | string | Predicted outcome: `"would_succeed"` or `"would_conflict"` |
| `patch_results[].tree_sha` | string | The resulting tree SHA on success (omitted on conflict) |
| `patch_results[].conflict_files` | string[] | List of conflicting file paths; empty array on success |

**Behavior:**

- Patches with status `disabled` or `merged_upstream` are skipped.
- The preview is cumulative: each successfully predicted patch contributes to
  the base used for the next patch, simulating the actual rebuild order.
- A conflict on one patch does not prevent prediction of subsequent patches --
  they are tested against the last successfully merged base.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Workspace is not in `carry_patch` mode |
| 401 | Unauthenticated request |
| 403 | PAT lacks `rebuilds:read` scope |
| 404 | Workspace not found |
| 500 | Internal server error (failed to resolve upstream HEAD, list patches, or initialize git runner) |

---

## Patch Status Dashboard Endpoint

The patch-status endpoint provides a comprehensive health summary of the
carry-patch stack for a workspace, aggregating workspace metadata, the most
recent rebuild result, per-patch status, and summary counts including the
total rerere resolution count for the workspace.

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
  "status": "active",
  "clone_status": "ready",
  "clone_error": "",
  "sync_status": "idle",
  "sync_error": "",
  "sync_mode": "pull_only",
  "head_sha": "fedcba987654...",
  "git_url": "https://github.com/example/repo.git",
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
      "last_rebuild_result": "success"
    },
    {
      "id": "uuid-string",
      "branch_name": "feature/conflict",
      "position": 2,
      "status": "conflict",
      "last_rebuild_result": "conflict",
      "conflict_files": ["pkg/api.go"]
    }
  ],
  "summary": {
    "total_patches": 2,
    "active": 1,
    "merged_upstream": 0,
    "conflict": 1,
    "disabled": 0,
    "total_rerere_resolutions": 1
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `workspace_slug` | string | The workspace slug |
| `workspace_mode` | string | Always `"carry_patch"` |
| `status` | string | Workspace status (`active`, `archived`, etc.) |
| `clone_status` | string | Clone status (`pending`, `cloning`, `ready`, `failed`, `archived`) |
| `clone_error` | string | Clone error message; empty string if no error (omitted from JSON when empty) |
| `sync_status` | string | Sync status (`idle`, `syncing`, etc.) |
| `sync_error` | string | Sync error message; empty string if no error (omitted from JSON when empty) |
| `sync_mode` | string | Sync mode (`pull_only`, etc.) |
| `head_sha` | string | SHA of the workspace HEAD; empty string if not yet cloned |
| `git_url` | string | The workspace git repository URL |
| `upstream_url` | string | The upstream repository URL |
| `upstream_head_sha` | string | SHA of the upstream HEAD at last fetch |
| `integration_branch` | string | Name of the integration branch |
| `integration_head_sha` | string | SHA of the integration branch HEAD after last rebuild; derived from the rebuild result, empty string if no rebuild has been attempted |
| `last_sync_at` | string (RFC 3339) or null | Timestamp of the last sync operation |
| `last_rebuild` | object or null | Most recent rebuild job record; null if no rebuild has been attempted |
| `last_rebuild.id` | string | Rebuild job ID |
| `last_rebuild.status` | string | Rebuild job status (`queued`, `running`, `completed`, `failed`) |
| `patches` | array | All non-deleted patches ordered by position |
| `patches[].id` | string | Patch ID |
| `patches[].branch_name` | string | Git branch name for the patch |
| `patches[].position` | integer | 1-based position in the patch stack |
| `patches[].status` | string | Current patch status (`active`, `conflict`, `disabled`, `merged_upstream`) |
| `patches[].last_rebuild_result` | string or null | Per-patch result from most recent rebuild (`success`, `conflict`, `skipped`); null if no rebuild has been attempted |
| `patches[].conflict_files` | array of strings | File paths with unresolved conflicts (present only when `last_rebuild_result` is `conflict`) |
| `patches[].origin_sync_state` | string or null | Relationship between the local patch branch and the fork after the last sync: `"in_sync"`, `"diverged"`, or `"missing_on_origin"`. Null when the workspace uses `hub` mode or the patch has not been synced. Omitted when null. |
| `patches[].origin_sha` | string or null | SHA of the fork tip for this patch branch at the last sync. Null when the fork has no such branch or the workspace uses `hub` mode. Omitted when null. |
| `patches[].origin_synced_at` | string (RFC 3339) or null | Timestamp of the last origin sync for this patch. Null when the workspace uses `hub` mode. Omitted when null. |
| `summary` | object | Aggregate counts derived from the patches array |
| `summary.total_patches` | integer | Total number of non-deleted patches (equals length of `patches` array) |
| `summary.active` | integer | Count of patches with status `active` |
| `summary.merged_upstream` | integer | Count of patches with status `merged_upstream` |
| `summary.conflict` | integer | Count of patches with status `conflict` |
| `summary.disabled` | integer | Count of patches with status `disabled` |
| `summary.total_rerere_resolutions` | integer | Total count of recorded rerere resolutions for the workspace; 0 if rr-cache is inaccessible |
| `summary.patches_diverged` | integer | Count of patches whose `origin_sync_state` is `"diverged"`. Always present; `0` in `hub` mode. |
| `summary.patches_missing_on_origin` | integer | Count of patches whose `origin_sync_state` is `"missing_on_origin"`. Always present; `0` in `hub` mode. |

**Consistency invariant:** `summary.total_patches` equals `len(patches)`, and
`summary.active + summary.merged_upstream + summary.conflict + summary.disabled`
equals `summary.total_patches`.

**Edge Cases:**

- If the workspace is not in `carry_patch` mode, returns HTTP 400.
- If no rebuild has been attempted, `last_rebuild` is null and all patches
  have `last_rebuild_result` set to null.
- If the patches table is empty, returns `patches: []` and all summary counts
  at zero.
- If the `rr-cache` directory is inaccessible, `summary.total_rerere_resolutions`
  is set to 0 rather than failing the request.
- Soft-deleted patches (status `"deleted"`) are excluded from the dashboard.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Workspace is not in `carry_patch` mode |
| 401 | Unauthenticated request |
| 403 | PAT lacks `workspaces:read` scope |
| 404 | Workspace not found |
| 500 | Database error |

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
| 404 | Workspace not found or not owned by the caller |
| 409 | A merge job for this source and target branch is already queued or running (`error_type: duplicate_merge`) |

Branch names are validated with git's ref-name rules (no leading `-`, no
`..`, no control characters, etc.) and rejected with 400 otherwise. A
`CHECK_COMMAND` that exits non-zero fails the job with error class
`CheckFailed`; a missing `CHECK_COMMAND` skips the check, while any other
error resolving it is retried rather than treated as "no check".

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

**Note:** Workspace existence is not validated. A query for a non-existent
workspace slug returns HTTP 200 with an empty array rather than 404.

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

### POST /api/v1/workspaces/:slug/merges/:id/requeue

Requeue a dead-lettered merge job. Returns the updated merge job record on
success.

**Authentication:** API Key, or PAT with `merges:write` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |
| `:id` | The merge job UUID |

**Response:** HTTP 200 OK with the updated merge job record.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `merges:write` scope |
| 404 | Job not found, or job belongs to a different workspace |
| 409 | Job is not in `dead_letter` status, or an active merge job already exists for this source and target |

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

### Pre-Receive Hook (Push Control)

For carry-patch workspaces, the git server enforces a single-writer model for
registered patch branches. A pre-receive hook runs after the pack is unpacked
and before each ref is written. The behaviour depends on `PATCH_BRANCH_SOURCE`
and `PUSH_PATCHES_TO_ORIGIN`:

- **Reject mode** (`PATCH_BRANCH_SOURCE=origin`, `PUSH_PATCHES_TO_ORIGIN` not
  `"true"`): the update is rejected with the per-ref message
  `branch is synced from origin; push to <git_url> instead` (or
  `branch is synced from origin; delete it on the fork instead` for deletes).
  `<git_url>` is the workspace's clone URL with any userinfo removed.
- **Forward mode** (`PATCH_BRANCH_SOURCE=origin`, `PUSH_PATCHES_TO_ORIGIN=true`):
  the update is pushed to the fork's `origin` remote (non-force) before the
  hub writes its ref. If the forward fails, the hub ref is not written and the
  client sees `origin rejected push: <error>` or
  `failed to resolve origin credentials` as a per-ref error. Deletes are
  rejected as in reject mode.
- **Mirror mode** (`PATCH_BRANCH_SOURCE=hub`, `PUSH_PATCHES_TO_ORIGIN=true`):
  the push is accepted normally. After the push, each accepted registered
  patch branch is force-pushed to the fork, best effort. A mirror failure
  emits `hub.patch.mirror_failed` but does not fail the push or the rebuild.

Push control applies only to ref updates where: the workspace mode is
`carry_patch`, the ref is `refs/heads/<name>`, `<name>` matches a `patches`
row whose status is not `deleted`, and `<name>` is not the integration branch.
All other ref updates (unregistered branches, tags, standard workspaces) are
unaffected.

Rejections and forward failures appear as per-ref `ng` lines in the
report-status, which `git push` shows as `remote rejected` with the message.
Other refs in the same push are processed independently.

### Protected Namespace: `refs/hub/replaced/`

The git server never accepts a client push that creates, updates or deletes a
ref whose name starts with `refs/hub/replaced/`. The rule applies to every
workspace and every credential with `git:write`, and it does not depend on a
registered pre-receive hook or on the workspace mode. It is evaluated before
the pre-receive hook is consulted.

The client sees `unpack ok` and the per-ref status
`ng <ref> refs/hub/replaced/ is maintained by the hub and cannot be pushed to`.
Other refs in the same push are processed as usual. The rejected ref keeps its
old value.

Hub-internal writes (sync, reset, removal) do not pass through a client push
and are unaffected. Fetching is unchanged: `git ls-remote` and
`git fetch <hub_url> refs/hub/replaced/<branch>` work normally.

### Accepted Refs Only

Only ref updates that the hub actually accepted (report-status entry is `ok`)
drive side effects. A rejected ref does not appear in the `hub.git.push` audit
event's `refs_updated` list, does not trigger the post-push hook, and does not
enqueue a rebuild. If no update was accepted, no audit event is emitted and no
post-push hook runs. `head_sha` is the exception: it is refreshed from the
trunk HEAD after every push, including a push in which every ref was rejected.

### Post-Push Hooks

After a successful push, the git server:

1. Updates `head_sha` in the database.
2. Resets the working tree to match the new HEAD.
3. If a post-push hook is registered (carry-patch auto-rebuild), checks whether
   any accepted pushed branch is a registered patch in a carry-patch workspace
   and, if `AUTO_REBUILD_AFTER_PUSH` is not `"false"`, enqueues a rebuild job.
   The push response is not affected -- the hook runs asynchronously.
4. In hub mode with `PUSH_PATCHES_TO_ORIGIN=true`, each accepted registered
   patch branch is force-pushed (mirrored) to the fork. The mirror runs after
   the rebuild enqueue, whether or not a rebuild was enqueued. A mirror failure
   emits `hub.patch.mirror_failed` and is logged but does not fail the push or
   the rebuild.

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

Served by `git upload-pack --stateless-rpc` (the git CLI), which implements
the full stateless negotiation (`have`/`ACK`/`NAK`/`done`), so incremental
fetches into repositories that already hold part of the history transfer only
the missing objects. The ref advertisement for `service=git-upload-pack` is
produced by the same implementation. Protocol v0 is spoken; clients requesting
protocol v2 fall back to v0 automatically. Gzip-encoded request bodies
(`Content-Encoding: gzip`) are accepted.

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
database and resets the working tree to match the new HEAD. If the pushed
branch matches a registered patch in a carry-patch workspace, a rebuild may be
auto-triggered (see Post-Push Hooks above).

For carry-patch workspaces, a pre-receive hook may reject or forward updates
to registered patch branches depending on `PATCH_BRANCH_SOURCE` and
`PUSH_PATCHES_TO_ORIGIN` (see Pre-Receive Hook above). Only accepted ref
updates drive the `hub.git.push` audit event and the post-push hook.

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

**Authentication:** API Key, or PAT with `secrets:manage` scope. Admin
tokens have no user identity and receive `403` on every `/api/v1/user/secrets`
and `/api/v1/user/vars` endpoint.

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
`"org"`, or `"workspace"`. The org tier is only consulted when the workspace
owner is a member of the workspace's organization.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope |
| 404 | Workspace not found or not owned by the authenticated user |
| 500 | Internal server error |

---

## Workspace Variables Reference

The following workspace variables control carry-patch rebuild and sync
behavior and how the hub reaches a workspace's forge. They can be set via the
`POST /api/v1/workspaces/:slug/vars` endpoint.

| Variable | Values | Default | Description |
|----------|--------|---------|-------------|
| `REBUILD_STRATEGY` | `"rebase"`, `"merge"` | `"rebase"` | Strategy used to apply patches during rebuild. Can be overridden per-rebuild via the `strategy` field in the POST /rebuild request body. |
| `REBUILD_FAIL_MODE` | `"fail_fast"`, `"continue"` | `"fail_fast"` | Controls whether rebuild aborts on first conflict or skips conflicting patches and continues. Can be overridden per-rebuild via the `fail_mode` field in the POST /rebuild request body. |
| `AUTO_REBUILD_AFTER_SYNC` | `"true"`, `"false"` | `"true"` | When `"true"` (or unset), a carry-patch sync that detects upstream advancement or a patch branch change (created, fast-forwarded or replaced for an active or conflict patch) automatically enqueues a rebuild job. |
| `AUTO_REBUILD_AFTER_PUSH` | `"true"`, `"false"` | `"true"` | When `"true"` (or unset), a git push to a registered patch branch automatically enqueues a rebuild job. |
| `PATCH_BRANCH_SOURCE` | `"hub"`, `"origin"` | `"hub"` | Selects the authority for patch branches. `"hub"` (default): patch branches are read from the hub's local clone. `"origin"`: the fork is fetched during sync and every registered patch branch is brought to the fork's tip. Read on every sync; a change takes effect on the next sync without a restart. |
| `PATCH_DIVERGENCE_POLICY` | `"replace"`, `"report"` | `"replace"` | Controls what happens when the hub's copy and the fork's copy of a patch branch have diverged. `"replace"` (default): the hub's copy is replaced with the fork's tip (the old tip is saved under `refs/hub/replaced/<branch>`). `"report"`: the hub's copy is left unchanged and the divergence is reported. Ignored when `PATCH_BRANCH_SOURCE` is `"hub"`. |
| `PUSH_PATCHES_TO_ORIGIN` | `"true"` | (disabled) | Enables forwarding or mirroring of hub pushes to registered patch branches. Only the exact string `"true"` enables it. In `origin` mode (`PATCH_BRANCH_SOURCE=origin`): when enabled, a push to a registered patch branch is forwarded to the fork before the hub writes its ref; when disabled, the push is rejected. In `hub` mode: when enabled, accepted pushes to registered patch branches are mirrored (force-pushed) to the fork after the push is accepted. |
| `SQUASH_MERGE_DETECTION` | `"ancestry_only"`, `"content_based"`, `"both"` | `"both"` | Controls which strategies are used to detect upstream merges during carry-patch sync. `"ancestry_only"` uses only `git merge-base --is-ancestor`. `"content_based"` uses only `git cherry` and PR-number scanning. `"both"` (default) uses all strategies. |
| `CHECK_COMMAND` | any string | (unset) | Shell command executed via `sh -c` after a merge rebase to validate the result. If it exits non-zero, the rebase is rolled back. |
| `FORGE_API_URL` | an absolute `https://` URL with a host, without userinfo, query or fragment | (unset) | The API URL of the forge behind the workspace's `git_url`, for the [forge read routes](#forge-endpoints). Unset, the API is found from the host (`api.github.com` for `github.com`, `<host>/api/v3` or `<host>/api/v4` for another). A value that contains exactly one of `github` and `gitlab` also tells which forge it is, and a GitLab value gets `/api/v4` appended when it does not end with it. Any other value answers `422 forge_api_url_invalid`. |
| `UPSTREAM_FORGE_API_URL` | as `FORGE_API_URL` | (unset) | The same for the `upstream_url` of a `carry_patch` workspace (`?target=upstream`). It does not fall back to `FORGE_API_URL`: an upstream on another host must not inherit the fork's API URL. |

---

## Sandbox Profile Endpoints

A hub with a `[sandbox]` section in its `config.toml` publishes the sandbox
profiles of its deployment: the shapes a sandbox can take. A caller reads the
list to choose a profile by name. The profiles are defined by the operator (see
[Server Configuration](configuration.md#sandbox)), are checked against the
provider when the hub starts and change only when the file is edited and the
hub is restarted.

The listing never contains a secret value or an `env` value. It names the
secrets a profile injects and the keys of its fixed environment, and shows
nothing of the provider, its options or the deployment's limits.

### GET /api/v1/sandbox-profiles

List the deployment's sandbox profiles.

**Authentication:** API Key, Admin Token, or PAT with the `sandboxes:read` or
`sandboxes:write` scope.

The request is checked in this order:

1. No credential: 401.
2. The hub has no `[sandbox]` section: 404 with `error_type: sandboxes_disabled`,
   for every authenticated caller, including a PAT without sandbox scopes.
3. A PAT with neither `sandboxes:read` nor `sandboxes:write`: 403.

**Response (success):** HTTP 200 OK

```json
{
  "profiles": [
    {
      "name": "agent",
      "default": true,
      "image": "registry.example.com/agent:1.4",
      "network": "none",
      "ttl_seconds": 1200,
      "resources": {
        "cpus": 2,
        "memory_mb": 4096,
        "pids": 512,
        "disk_mb": 0
      },
      "secrets": ["ANTHROPIC_API_KEY", "GIT_PAT"],
      "optional_secrets": ["GH_TOKEN"],
      "env_keys": ["LANG", "TZ"],
      "require_landlock": true
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `profiles` | array | Every profile, sorted by `name` |
| `profiles[].name` | string | The profile name a caller chooses |
| `profiles[].default` | boolean | `true` for the one profile marked as the default; none may be marked |
| `profiles[].image` | string | The container image |
| `profiles[].network` | string | The network mode of a sandbox: `none`, `allowlist` or `unrestricted`, one of the deployment's allowed modes |
| `profiles[].ttl_seconds` | integer | The profile's lifetime in whole seconds, rounded down |
| `profiles[].resources` | object | `cpus` (number), `memory_mb`, `pids` and `disk_mb` (integers). `0` means the provider's default |
| `profiles[].secrets` | array of string | Names of the secrets the profile injects, sorted ascending; `[]` when there are none |
| `profiles[].optional_secrets` | array of string | Names of the secrets injected when they exist, sorted ascending; `[]` when there are none |
| `profiles[].env_keys` | array of string | Keys of the profile's fixed environment, sorted ascending; `[]` when there are none. Their values are never returned |
| `profiles[].require_landlock` | boolean | Whether a sandbox of this profile must confine its agent with Landlock |

The response is built from the profiles the hub validated at start. It makes
no provider call and reads no database, so it keeps answering after the
container engine becomes unreachable. Every authorised caller gets the same
list, whatever their workspaces, user or organisation.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT has neither `sandboxes:read` nor `sandboxes:write` scope |
| 404 | The hub has no `[sandbox]` section (`error_type: sandboxes_disabled`, message `sandboxes are not enabled on this hub`) |

---

## Sandbox Lease Endpoints

A **lease** is a caller's right to one sandbox in a workspace. When a lease is
issued the hub fixes the sandbox's shape from a configured profile (see
[Sandbox Profile Endpoints](#sandbox-profile-endpoints)), confirms that the
secrets the profile requires exist for the workspace, enforces the deployment's
limit on live leases and hands back a **token**, a bearer credential for that one
sandbox. The token is returned once, in the response that issues the lease, and
the hub keeps only its SHA-256. The routes below record the lease; the holder
creates the one sandbox it allows through the hub's sandbox gateway, with the
token (see [The sandbox gateway](#the-sandbox-gateway)), and ending a lease
destroys that sandbox.

A lease is in state `leased` (issued, no sandbox yet), `active` (its sandbox was
created), `destroyed`, `expired` (it got no `Create` before its `lease_expires_at`,
or its time ran out first; see [Lease lifetime](configuration.md#lease-lifetime))
or `failed` (its sandbox could not be created). `destroyed`, `expired` and `failed`
are the **terminal** states: a lease that has reached one stays readable through
`GET`, `?all=true` and the admin list for 24 hours after it ended and is then
removed, after which `GET` answers 404. A lease never carries its token, the
token's hash or the profile's environment values in any response but the issuing
`POST`, which carries the token only.

The lease routes check a request in this order, and the first failing check
answers:

1. No credential: 401.
2. The hub has no `[sandbox]` section: 404 with `error_type: sandboxes_disabled`,
   for every authenticated caller.
3. A PAT without the scope the route needs: 403, before the workspace is looked
   up. `POST` and `DELETE` need `sandboxes:write`; the `GET` routes need
   `sandboxes:read` or `sandboxes:write`.
4. A workspace that does not exist or that the caller does not own: 404
   `workspace not found`, the same for both. Admin tokens may act on any existing
   workspace.
5. `POST` only: a workspace that is not active: 400 `workspace is not active`.

### The lease representation

Every response that shows a lease has exactly these fields.

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | `sbx_` and 32 lowercase hex characters |
| `workspace` | string | The workspace slug |
| `profile` | string | The profile the lease was issued from |
| `purpose` | string | The caller's purpose text, `""` when none |
| `state` | string | `leased`, `active`, `destroyed`, `expired` or `failed` |
| `gateway_url` | string | Where the holder dials the sandbox gateway with the lease's token: `[sandbox].external_url` when set, otherwise `http://` or `https://` and the address the gateway listens on. The same on every lease, in every state |
| `expires_at` | string (RFC 3339 UTC) | `created_at` plus the lease's TTL; the sandbox's lifetime ends here, and the lease's token is refused from this moment in every state |
| `lease_expires_at` | string (RFC 3339 UTC) or `null` | The deadline for the lease's `Create`: the earlier of `created_at` plus `[sandbox].lease_timeout` and `expires_at`. It never changes. A lease that is still `leased` at this moment lapses to `expired`. `null` for a lease issued before the field existed, which lapses at `expires_at` |
| `image` | string | The profile's image, as it was at issue |
| `network` | string | The profile's network mode |
| `resources` | object | `cpus` (number), `memory_mb`, `pids`, `disk_mb` (integers); `0` means the provider's default |
| `require_landlock` | boolean | Whether the sandbox must confine its agent with Landlock |
| `secrets` | array of string | Names of the secrets that were required; `[]` when none |
| `optional_secrets` | array of string | Names of the optional secrets; `[]` when none |
| `labels` | object | The caller's labels exactly as given; `{}` when none |
| `created_by` | string | The caller's user ID, or `admin` for an admin token |
| `created_at`, `updated_at` | string (RFC 3339 UTC) | When the lease was issued and last changed |
| `sandbox_id`, `error`, `ended_at`, `end_reason` | string or `null` | `null` until set. `sandbox_id` is the id of the sandbox once the lease is `active`. `error` is the reason a `failed` lease failed; a message that comes from the provider has secret values, fixed environment values and URL user information removed and is cut to 1024 bytes. `end_reason` says why the lease ended: `client` (its holder closed its sandbox through the gateway), `api` (`DELETE`), `expired` (time: a lease that got no `Create` in time, and an `active` lease whose sandbox was destroyed at `expires_at`; `state` tells the two apart), `shutdown` (the hub shut down: the shutdown itself ended the lease, or `DELETE` or the holder's close did while it was shutting down) or `restart` (the hub found the lease `active` after a crash). A `failed` lease has no `end_reason` |

The profile is copied onto the lease when it is issued, so editing the
configuration and restarting the hub does not change a lease that exists.

### POST /api/v1/workspaces/:slug/sandboxes

Issue a lease in a workspace.

**Authentication:** API Key, Admin Token, or PAT with `sandboxes:write`.

**Request body:** a JSON object, optional as a whole (an empty body counts as
`{}`). A field other than these four is refused, so that a caller cannot believe
it set an image or an environment. Field names are matched exactly (`PROFILE` is
not `profile`), and anything after the object is refused.

| Field | Type | Description |
|-------|------|-------------|
| `profile` | string | Profile name, matched exactly and case-sensitively. Defaults to the profile marked `default` in the configuration |
| `purpose` | string | At most 64 characters and no control character. Defaults to `""` |
| `ttl` | string | A Go duration such as `30m`: at least `1m` and at most the profile's TTL. Defaults to the profile's TTL. Fractions of a second are cut off |
| `labels` | object | At most 16 pairs of strings. A key is 1 to 63 characters from `A-Za-z0-9._-`, starts and ends with a letter or digit, and is not `lease` or `workspace` in any case. A value has at most 256 bytes and no control character |

**Response (success):** HTTP 201 Created, the lease representation plus `token`.

```json
{
  "id": "sbx_3f2a9c0e7b1d4c5a8e6f0b2d1c3a4e5f",
  "workspace": "widgets",
  "profile": "tools",
  "purpose": "triage",
  "state": "leased",
  "gateway_url": "https://sandboxes.example.com",
  "expires_at": "2030-01-02T04:04:05Z",
  "lease_expires_at": "2030-01-02T03:09:05Z",
  "image": "registry.example.com/tools:1",
  "network": "none",
  "resources": {"cpus": 1, "memory_mb": 512, "pids": 100, "disk_mb": 0},
  "require_landlock": true,
  "secrets": ["GIT_PAT"],
  "optional_secrets": [],
  "labels": {"team": "red"},
  "created_by": "user-1",
  "created_at": "2030-01-02T03:04:05Z",
  "updated_at": "2030-01-02T03:04:05Z",
  "sandbox_id": null,
  "error": null,
  "ended_at": null,
  "end_reason": null,
  "token": "sbl_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
}
```

`token` is `sbl_` and 44 characters (33 random bytes in unpadded URL-safe base64),
48 characters in all.

**Required secrets.** Before the lease is stored the hub confirms that every
secret the profile requires exists in at least one scope of the workspace's
resolution chain, in order: the workspace, the workspace's org (only when the
workspace has one and its owner is a member of it), and the workspace owner's
user scope, never the caller's. Names match ignoring case. The hub reads key
names only and never a secret value. Optional secrets are not checked. When
some are missing the answer is 422 `secret_missing` and no lease is stored; its
message names every missing key in the profile's order and the scope types
searched, and no owner or org identifier, for example
`required secrets not found: A_KEY, C_KEY (searched scopes: workspace, user)`.

**Capacity.** `[sandbox].max_sandboxes` (default 16) limits the leases that are
live at once. A lease counts while it is `leased` or `active` and its `expires_at`
is in the future, in every workspace; a `leased` lease whose `lease_expires_at` is
at or before now no longer counts, whether or not a sweep has ended it yet. At the limit the answer is 409 `sandbox_capacity`,
for example `sandbox capacity reached: at most 2 leases may be live at once
(max_sandboxes)`. The count and the insert are serialised, so concurrent
requests never exceed the limit.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Malformed JSON, a body that cannot be read, or a body that is not an object; an unknown field (the message names it); an unknown profile, or no profile when none is the default (the message lists the profiles in configuration order); a `ttl` that does not parse, is under `1m` or is over the profile's TTL (the message names that TTL); a bad `purpose`; bad `labels` (the message names the label key); a workspace that is not active |
| 401 | Unauthenticated request |
| 403 | PAT lacks `sandboxes:write` scope |
| 404 | Sandboxes are not enabled (`error_type: sandboxes_disabled`); the workspace does not exist or is not owned by the caller |
| 409 | `max_sandboxes` is reached (`error_type: sandbox_capacity`) |
| 413 | The body is larger than the server accepts |
| 422 | A required secret exists in no searched scope (`error_type: secret_missing`) |
| 500 | A database or random-source failure; nothing is stored |

### GET /api/v1/workspaces/:slug/sandboxes

List the workspace's leases, newest `created_at` first, at most 200, as a bare
JSON array (`[]` when there are none). Only live leases (`leased` or `active`)
are returned unless `?all=true` also asks for those that have ended; a terminal
lease is listed for 24 hours after it ended. `all` accepts what Go's `strconv.ParseBool` accepts (`1`, `t`, `T`, `TRUE`,
`true`, `True`, `0`, `f`, `F`, `FALSE`, `false`, `False`); anything else, an empty
value included, is 400. Works on an archived workspace.

**Authentication:** API Key, Admin Token, or PAT with `sandboxes:read` or
`sandboxes:write`.

**Error Codes:** 400 (`all` cannot be parsed), 401, 403 (PAT without either
scope), 404 (`sandboxes_disabled`, or workspace not found).

### GET /api/v1/workspaces/:slug/sandboxes/:id

Return one lease of the workspace, in any state. A terminal lease can be read for 24
hours after it ended and is then removed: `sandbox lease not found`. Works on an
archived workspace.

**Authentication:** as for the list.

**Error Codes:** 401, 403, 404 (`sandboxes_disabled`; workspace not found; or
`sandbox lease not found` for an `id` that is malformed, unknown or belongs to
another workspace, without saying which).

### DELETE /api/v1/workspaces/:slug/sandboxes/:id

End a lease, destroy its sandbox and revoke its token. The lease becomes
`destroyed`, with `end_reason` `api` (`shutdown` while the hub is shutting down) and `ended_at` and `updated_at` set to now,
and its token stops working with the very next call to the gateway, the calls
relayed to a sandbox that exists included. Answers 204 with no body. Works on an
archived workspace.

- **A `leased` lease** has no sandbox. It is moved to `destroyed` without a call to
  the provider. If its holder's `Create` is in flight, the sandbox that call makes
  is destroyed the moment it exists, and the call fails.
- **An `active` lease** has its sandbox destroyed first, within 30 seconds, and only
  when that worked is the lease moved to `destroyed`. A sandbox that is already
  gone counts as destroyed. The hub reaches only the sandboxes its own process
  made: after the hub has been restarted it no longer holds the sandbox of a lease
  that was `active` before, so a `DELETE` ends that lease without destroying the
  sandbox, which then lives until the TTL the provider enforces (a lease that was
  `active` at a crash is ended by the next start in any case, with `end_reason`
  `restart`). If the provider fails, or does not answer in time, the
  answer is 502 with `error_type: sandbox_provider_error` and the provider's
  message with secret values, fixed environment values and URL user information
  removed. Nothing has changed: the lease is still `active`, its token still
  works, no event was emitted, and the same `DELETE` can be sent again.
- **A lease that has ended** (`destroyed`, `expired` or `failed`) is left alone, for
  the 24 hours it is retained (after that it is gone and the answer is 404
  `sandbox lease not found`): 204, no call to the provider, no event, and the original `ended_at` and
  `end_reason` stay. A retry or a clean-up script is harmless.

If the lease changes state between the hub reading it and updating it (its
`Create` finished, or its holder closed the sandbox), the hub reads it once more
and acts on the new state.

Each change into `destroyed` that `DELETE` makes emits one `hub.sandbox.destroy`
event with `reason: "api"` (or `"shutdown"`), the id of the sandbox or `null` when the lease had
none, and the actor of the calling credential (see
[Hub Audit Events](#hub-audit-events)).

**Authentication:** API Key, Admin Token, or PAT with `sandboxes:write`.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `sandboxes:write` scope |
| 404 | `sandboxes_disabled`; workspace not found; or `sandbox lease not found`, as above |
| 500 | A database failure |
| 502 | The provider could not destroy the sandbox (`error_type: sandbox_provider_error`); the lease is unchanged |

### The sandbox gateway

The hub serves an AgentKit sandbox gateway on its own listener (`[sandbox].listen`,
see [Server Configuration](configuration.md#the-sandbox-gateway)). It is a gRPC
service and not part of the REST API, and it is reached with AgentKit's client at
the lease's `gateway_url`:

```go
p, err := gateway.Dial(ctx, gatewayURL, gateway.Options{Token: token})
sb, err := p.Create(ctx, sandbox.Spec{})
```

**Authentication.** The gateway accepts a lease token and nothing else. Every call,
`Dial` included, carries the token as a bearer credential. The hub looks the
token's SHA-256 up in the lease table on every call and accepts it only for a
lease that is `leased` or `active` and whose `expires_at` is in the future. A
token that hub never issued, and the token of a lease that is `destroyed`,
`expired`, `failed` or past its time, is refused with gRPC `Unauthenticated`,
without saying which, on the very next call.

**Creating the sandbox.** The lease is the principal of the call, and the hub
builds the sandbox from the lease and not from the spec the caller sends:

| Of the sandbox | Comes from |
|----------------|------------|
| Image, network, resources, Landlock setting, fixed environment | The lease (copied from the profile when it was issued) |
| Secrets | The profile's `secrets` and `optional_secrets`, read when the sandbox is created: the workspace, then the workspace's org (only if its owner is a member), then the owner's user scope; the first scope that has the name wins. They are put in the sandbox's environment under the profile's spelling of the name |
| TTL | The time the lease has left, in whole seconds |
| Working directory | The caller's `Workdir` |
| Labels | The caller's labels without every key that starts with `hub.`, the lease's labels as `hub.<key>`, and `hub.lease` and `hub.workspace` |

The caller's image, environment, network, resources, TTL and Landlock setting are
not read. The spec the gateway reports back has every environment value replaced
by `[redacted]`.

A `Create` changes the lease as follows. Only one `Create` can be under way for a
lease, and only a `leased` lease can create:

| Outcome | Lease | Event |
|---------|-------|-------|
| The provider made the sandbox | `leased` to `active`, `sandbox_id` set | `hub.sandbox.create` |
| The profile is no longer configured; a required secret exists in no scope; the spec cannot be built; the provider failed | `leased` to `failed`, `error` set to the message | `hub.sandbox.failed` |
| The lease's `lease_expires_at` (or its `expires_at`) is at or before now; nothing is created | `leased` to `expired`, `end_reason` `expired`, in the same call | `hub.sandbox.expire` |
| A `DELETE` ended the lease while the provider worked, or a sweep expired it | keeps its terminal state and `end_reason`; the new sandbox is destroyed | none |
| The secrets store failed | stays `leased`, so the call can be repeated | none |

The gateway hands a failure of `Create` to the client as gRPC `Unavailable` with
the hub's message: `lease <id> is <state>: only a leased lease can create its
box`, `lease <id> has expired`, `a Create for lease <id> is already in progress`,
`profile "<name>" is no longer configured`, `secret_missing: <KEYS> (searched
scopes: workspace, org, user)` (the scope types actually searched), `hub is
shutting down and creates no more sandboxes` (gRPC `Unavailable`, from the moment
the hub starts to shut down), or the provider's error text, scrubbed as under
`error` above. A `Create` the gateway accepts for a `leased` lease, that is, one
that gets as far as making the sandbox, is counted once on
`afhub_sandbox_creates_total`, as `created` or `failed`. A second `Create` under
the same token therefore fails while the lease is `active`, and is
`Unauthenticated` once it has ended or failed.

**Using and ending the sandbox.** The gateway relays the holder's calls to its
sandbox, lists only the sandbox of the holder's lease, and answers a request to
destroy a sandbox that is not the holder's lease's as it answers an unknown id,
without touching anything. Its `Reap` call, which the gateway makes for itself when
it starts and at an interval, runs the hub's sweep (see
[Lease lifetime](configuration.md#lease-lifetime)); when a lease holder makes it,
it does nothing: starting sweeps is not a holder's to ask, and the answer would
show other leases' sandbox ids and the provider's error text. When the holder
closes its sandbox the hub destroys it and moves the lease from `active` to
`destroyed` with `end_reason` `client`, emitting one `hub.sandbox.destroy`. If the
provider cannot destroy it, the client gets the scrubbed error and the lease stays
`active`. Closing a sandbox in the last two seconds of the lease's time, or after
it, closes it and changes nothing else: the gateway closes a sandbox when its TTL
ends, a moment before the lease does, and the next sweep ends the lease as
`expired`.

### GET /api/v1/sandboxes

List the leases of every workspace, in the same representation, order, cap and
`?all=true` behaviour as the workspace list.

**Authentication:** Admin Token only.

**Error Codes:** 400 (`all` cannot be parsed), 401, 403 (any credential but an
admin token, including an API key or a PAT holding both scopes), 404
(`sandboxes_disabled`).

---

## Forge Endpoints

A workspace's repository lives on a forge, GitHub or GitLab. These endpoints let
a caller read the repository, its issues and its pull requests through the hub,
using the credential the hub already holds for the workspace and never one of
the caller's own. For every request the hub builds a fresh client for the
workspace itself, from the workspace's own record. A read takes nothing from the
request but which remote to read (`target`) and its own parameters; a write sends
the fields of its body (a title, a comment, labels) and nothing else. Neither the
request's `Authorization` header nor the body as it came reaches the forge, and no
response shows the credential, only its kind.

The ten read endpoints need `forge:read`. They work on `active` and `archived`
workspaces alike, take no workspace lock, need no clone (`clone_status` does not
matter) and emit no audit event. The nine write endpoints
([The write routes](#the-write-routes)) need `forge:write`, work on `active`
workspaces only, take no lock and need no clone either, and record every write that
reaches the forge as a `hub.forge.*` audit event. Everything below that is not
about the write routes holds for both. The webhook routes
([The webhook routes](#the-webhook-routes)) are of their own kind: the list of
deliveries is a read (`forge:read`, a workspace of any status), `PUT .../forge/webhook`
a write (`forge:write`, an `active` workspace) and `DELETE .../forge/webhook` needs
`forge:write` and works on a workspace of any status.

A forge can also tell the hub what happened on the repository, so that a consumer
learns of an issue, a label, a comment, a pull request or a push without polling. The
forge posts a *delivery* to a public endpoint of the hub
([The webhook ingress](#the-webhook-ingress)); the hub checks it against a secret it holds
for the workspace, records it once and relays it as a `hub.forge.*` audit event, which
reaches the SSE stream and the audit query. Three routes manage the secret and read the
log of deliveries ([The webhook routes](#the-webhook-routes)). The hub only verifies,
records and relays: it never acts on a delivery and never sends a request to the forge
for one.

### How the hub reaches the forge

| | origin (the default) | upstream |
|---|---|---|
| Remote URL | the workspace's `git_url` | the `upstream_url` of a `carry_patch` workspace |
| Credential | `GIT_PAT`, or `GIT_USERNAME` with `GIT_PASSWORD` (the resolver the clone uses) | `UPSTREAM_GIT_PAT`, or `UPSTREAM_GIT_USERNAME` with `UPSTREAM_GIT_PASSWORD`, else the origin's (the resolver the carry-patch sync uses) |
| API URL variable | `FORGE_API_URL` | `UPSTREAM_FORGE_API_URL`, with no fallback to `FORGE_API_URL` |

- **Repository.** It is read from the remote URL: `https://host/owner/name`,
  `ssh://`, or `git@host:owner/name`, with `.git` and userinfo removed. GitLab
  subgroup paths are kept: for `https://gitlab.example.com/group/subgroup/widgets`
  the owner is `group/subgroup` and the name `widgets`.
- **Which forge.** The forge is GitHub or GitLab. A host whose name contains
  `github` or `gitlab` says which; so does an API URL variable that contains one
  of the two words. The API URL, when no variable sets it, is `https://api.github.com`
  for `github.com`, `https://<host>/api/v3` for another GitHub host and
  `https://<host>/api/v4` for a GitLab host. A host that names neither forge, with
  no API URL variable that does, cannot be told: the answer is `422 forge_unknown`
  and nothing is sent. The classification is that of the aflib `issuex` package;
  the hub's own environment variables `GITHUB_API_URL` and `GITLAB_API_URL` are
  read by it too.
- **API URL variables.** `FORGE_API_URL` and `UPSTREAM_FORGE_API_URL` are workspace
  variables (see the [Workspace Variables Reference](#workspace-variables-reference)).
  A value that is set must be an absolute `https://` URL with a host and without
  userinfo, query or fragment; any other is `422 forge_api_url_invalid`, with a
  message that names the variable and the rule and not the value. Surrounding
  white space and trailing slashes are ignored. For GitLab, issuex puts `/api/v4`
  after a value that does not end with it, and the diagnostic shows the URL with it.
- **Credential kind.** `pat` when the resolver returns the username `x-token-auth`
  (a `GIT_PAT`); `basic` for any other username, in which case the password is sent
  to the forge as the token; `none` when there is no credential, in which case the
  client is unauthenticated and sends no token header, whatever token the hub's own
  environment (`GITHUB_TOKEN`, `GH_TOKEN`, `GITLAB_TOKEN`) holds.
- **No caching.** A client is built for each request, so a rotated secret or a
  changed variable applies to the next call.
- **Every request** to the forge carries `User-Agent: af-hub/<version>`. The HTTP
  client follows the environment's proxy settings, as the hub's git transport does,
  and refuses a redirect to another host, port or scheme (a GitLab token would
  otherwise follow it): the answer is `502 forge_error`. A call, the retries issuex
  makes inside it included, has 30 seconds, after which the answer is
  `504 forge_timeout`.

### Order of checks

Every forge route checks, in this order:

1. No credential: 401.
2. A PAT without `forge:read` (or `forge:write`, which implies it): 403, with the
   message `PAT requires forge:read scope`. A credential type the hub does not know
   is 403 too. This comes before the workspace is looked up, so it is the answer
   for a slug that does not exist as well.
3. A workspace that does not exist or is not the caller's: 404 `workspace not found`
   (admin tokens may read any workspace).
4. The `target` parameter (all routes but `GET .../forge`): 400.
5. The path and query parameters: 400, naming the parameter.
6. Building the client: 422 or 500.

API keys and admin tokens hold `forge:read` and `forge:write` implicitly.

A write route checks, in this order, and answers with the first that fails:

1. No credential: 401.
2. A PAT without `forge:write`, `forge:read` alone included: 403, with the message
   `PAT requires forge:write scope`. A credential type the hub does not know is 403
   too. This comes before the workspace is looked up.
3. A workspace that does not exist or is not the caller's: 404 `workspace not found`.
4. A workspace whose status is not `active`: 400 `workspace is not active`.
5. The request itself: 400. This is the `target` (a bad one, and `upstream` on a
   workspace that is not a `carry_patch` fork with an `upstream_url`, which is
   `workspace_mode_mismatch`), a number in the path that is not a positive integer,
   a body that is not JSON of the shape the route takes, and the field rules of the
   route.
6. Building the client: 422 or 500, as for a read. A request this forge cannot take
   is refused here: an update that sets only `assignees` on GitLab is
   `400 nothing to update`.
7. A body-like field over the forge's limit: 413 `body_too_large`. The limit depends
   on the forge, so this check follows the client: a forge the hub cannot tell is
   422 even for an oversized body.

A request refused at any of these steps sends nothing to the forge and records no
audit event.

### The target

Every route but `GET /api/v1/workspaces/:slug/forge` takes `?target=`:

| Value | Reads |
|---|---|
| absent, or `origin` | the repository at the workspace's `git_url` |
| `upstream` | the repository at the `upstream_url`; allowed only when `workspace_mode` is `carry_patch` and `upstream_url` is not empty, else `400` with `error_type: workspace_mode_mismatch` |

Any other value (`ORIGIN`, an empty value, a repeated parameter) is `400` with the
message `target must be "origin" or "upstream"`.

### GET /api/v1/workspaces/:slug/forge

Describe how the hub reaches the forge, without sending any request to it. A person
runs it when the other routes answer `forge_unknown`.

**Authentication:** API Key, Admin Token, or PAT with the `forge:read` or
`forge:write` scope.

**Response (success):** HTTP 200 OK

```json
{
  "forge": "github",
  "api_url": "https://api.github.com",
  "repository": { "host": "github.com", "owner": "acme", "name": "widgets" },
  "credential": "pat",
  "upstream": null,
  "webhook": {
    "configured": true,
    "url": "https://hub.example.com/hooks/forge/widgets",
    "last_delivery_at": "2026-10-21T09:12:03Z"
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `forge` | string | `github`, `gitlab` or `unknown` |
| `api_url` | string or null | The API URL the hub sends its requests under (for GitLab, with `/api/v4`); `null` when the forge is `unknown`. It is the variable's value or the default for the host, so a `GITHUB_API_URL` or `GITLAB_API_URL` in the hub's own environment, which issuex would honour, is not shown |
| `repository` | object or null | `host`, `owner` (the full namespace path) and `name`; `null` when the remote URL names no repository |
| `credential` | string | `pat`, `basic` or `none`: the kind, never a value |
| `upstream` | object or null | The same four fields for the `upstream_url`, computed with the upstream resolver and `UPSTREAM_FORGE_API_URL`; `null` unless the workspace is `carry_patch` with a non-empty `upstream_url` |
| `webhook` | object | The state of the workspace's webhook, described below. It never holds the secret |

The `webhook` block:

| Field | Type | Description |
|-------|------|-------------|
| `configured` | boolean | `true` when the workspace has the secret `FORGE_WEBHOOK_SECRET` (workspace scope; a secret of an org or a user is never read) |
| `url` | string or null | The URL the forge is to post to: `server.external_url` without its trailing `/`, then `/hooks/forge/<slug>`; `null` when `external_url` is not set |
| `last_delivery_at` | string or null | The time the hub received the latest delivery of this workspace, of any outcome, as a UTC timestamp; `null` when it has none |

A host that cannot be classified is reported as `"forge": "unknown"` with
`api_url: null` and is not an error. The repository is still shown when the URL
names one.

**Errors:**

| Status | Condition |
|--------|-----------|
| 401, 403, 404 | As in [Order of checks](#order-of-checks) |
| 422 | `FORGE_API_URL`, or `UPSTREAM_FORGE_API_URL` for the upstream block, is invalid (`error_type: forge_api_url_invalid`) |
| 500 | The secrets or variables store failed, or the delivery log could not be read (no partial `webhook` block is answered) |

### The read routes

Each route calls one issuex method and returns its result as issuex encodes it. A
list is a bare JSON array, and an empty list is `[]`, never `null`.

| Route | issuex method | Response |
|---|---|---|
| `GET /api/v1/workspaces/:slug/forge/repository` | `GetRepository` | the repository |
| `GET /api/v1/workspaces/:slug/issues` | `ListIssues` | an array of issues |
| `GET /api/v1/workspaces/:slug/issues/:n` | `ReadIssue` | the issue with its comments |
| `GET /api/v1/workspaces/:slug/issues/:n/comments` | `ListComments` | an array of comments |
| `GET /api/v1/workspaces/:slug/pulls/:n` | `ReadPullRequest` | the pull request |
| `GET /api/v1/workspaces/:slug/pulls/:n/state` | `GetPRState` | the state of the pull request |
| `GET /api/v1/workspaces/:slug/pulls/:n/files` | `ReadChangedFiles` | an array of changed files |
| `GET /api/v1/workspaces/:slug/pulls/:n/checks` | `GetCIChecks` | an array of CI check runs |
| `GET /api/v1/workspaces/:slug/pulls/:n/reviews` | `GetPRReviews` | an array of reviews |

**Authentication:** API Key, Admin Token, or PAT with the `forge:read` or
`forge:write` scope. All of them take `?target=`.

`:n` must be a positive decimal integer (`400`, `n must be a positive decimal
integer`); on GitLab it is the project's `iid`. On `GET .../issues` the query is:

| Parameter | Values | Passed to the forge |
|-----------|--------|---------------------|
| `state` | `open`, `closed`, `all` | when given |
| `labels` | comma-separated; empty items are dropped | when any remain |
| `assignee` | free text | when given |
| `sort` | `created`, `updated` | when given |
| `direction` | `asc`, `desc` | when given |
| `limit` | an integer from 1 to 100; default 30 | always |

A parameter that is absent is not passed on, and issuex uses its own default. A bad
value, an empty one or a repeated one answers `400` with a message that starts with
the parameter's name (`state must be one of: open, closed, all`, `limit must be an
integer from 1 to 100`). The check happens before a client is built.

Result types, as issuex encodes them (fields marked `?` are omitted when empty):

| Type | Fields |
|---|---|
| repository | `full_name`, `default_branch`, `private`, `archived`, `permissions` (`push`, `pull`, `admin`), `allow_merge_commit?`, `allow_squash_merge?`, `allow_rebase_merge?` |
| issue | `number`, `title`, `body`, `state`, `url?`, `html_url?`, `author` (`login`), `labels?`, `created_at`, `updated_at`, `closed_at?`, `is_pr` |
| issue thread | `issue`, `comments`, `truncated`, `comments_err?` |
| comment | `id?`, `body`, `user` (`login`), `created_at`, `updated_at?`, `url?`, `html_url?` |
| pull request | `number`, `title`, `body`, `state`, `url?`, `html_url?`, `draft`, `merged`, `head_sha`, `head_branch`, `base_branch`, `author`, `created_at`, `updated_at`, `closed_at?`, `merged_at?` |
| pull request state | `number`, `state`, `merged`, `head_sha` |
| changed file | `filename`, `status`, `additions`, `deletions` |
| check run | `name`, `status`, `conclusion`, `summary?`, `url?` |
| review | `author`, `state`, `body`, `submitted_at` |

`GET .../issues/:n` answers an issue thread: the issue, its comments and `truncated`,
issuex's flag that more comments exist than it read. When the issue was read and its
comments could not be, the thread carries the error as `comments_err`, with the token
and any URL userinfo taken out of its text, and the answer is still 200.

issuex returns `incomplete` with the issue list and `truncated` with the comment
list; the list routes answer the bare array and do not show those two flags.

**Example:** `GET /api/v1/workspaces/widgets/issues?labels=af:triage&state=open`

```json
[
  {
    "number": 12,
    "title": "Widgets break",
    "body": "it breaks",
    "state": "open",
    "html_url": "https://github.com/acme/widgets/issues/12",
    "author": { "login": "alice" },
    "labels": ["af:triage"],
    "created_at": "2026-01-02T03:04:05Z",
    "updated_at": "2026-01-02T03:04:05Z",
    "is_pr": false
  }
]
```

### The write routes

| Route | Body | Success |
|---|---|---|
| `POST /api/v1/workspaces/:slug/issues` | `{title, body?, labels?, assignees?}` | 201, the issue |
| `PATCH /api/v1/workspaces/:slug/issues/:n` | `{title?, body?, state?, labels?, assignees?}` | 200, the issue |
| `POST /api/v1/workspaces/:slug/issues/:n/close` | `{comment?}` or no body | 200, the issue |
| `POST /api/v1/workspaces/:slug/issues/:n/comments` | `{body}` | 201, `{"url": …}` |
| `POST /api/v1/workspaces/:slug/issues/:n/labels` | `["name", …]` | 204, no body |
| `DELETE /api/v1/workspaces/:slug/issues/:n/labels/:name` | none | 204, no body |
| `POST /api/v1/workspaces/:slug/labels` | `{name, color?, description?}` | 201, the label; 200 when it exists |
| `POST /api/v1/workspaces/:slug/pulls` | `{title, body?, head, base, draft?}` | 201, the pull request |
| `POST /api/v1/workspaces/:slug/pulls/:n/comments` | `{body}` | 201, `{"url": …}` |

**Authentication:** API Key, Admin Token, or PAT with the `forge:write` scope.
Every route takes `?target=origin|upstream`, with the default and the meaning given
in [The target](#the-target). Issues, pull requests and labels in the answers are
the types listed under [The read routes](#the-read-routes), as issuex encodes them
(a label: `name`, `color?`, `description?`; GitHub gives the color without a `#`,
GitLab with one). Fields the hub does not know are ignored. `:n` is the issue
number, on GitLab the project's `iid`.

- **`POST .../issues`.** `title` is required and must not be blank; `body` may be
  empty; `labels` and `assignees` are at most 100 names each, none blank. The
  credential the forge sees is the workspace's, never the caller's bearer. On GitLab
  the assignees are accepted and not sent: issuex sends none, because GitLab takes
  user ids (`docs/errata/31_forge_writes_divergences.md`).
- **`PATCH .../issues/:n`.** Only the fields the request sets change. A field that is
  absent or `null` is left alone; `title` must not be blank; `body: ""` clears the
  body; `state` is `open` or `closed`; `labels` and `assignees` replace the issue's
  whole set, and `[]` removes every entry. A body that sets none of them is
  `400 nothing to update`, and so, on GitLab, is one that sets only `assignees`, which
  the hub does not send there (an update that sets other fields records only those
  as changed). The answer is the issue as the forge returns it.
- **`POST .../issues/:n/close`.** An empty body, `{}` or a comment that is blank
  closes the issue. A comment that is not blank is posted first, and the issue is
  closed only when that succeeded: a comment the forge refuses leaves the issue open
  and is answered with its error; a close that fails after the comment leaves the
  comment, is answered with the close's error, and the event holds the comment's URL.
  Closing a closed issue is 200. The answer is the issue as the forge reports it
  after the close.
- **`POST .../issues/:n/comments`.** `body` is required and must not be blank. The
  answer is `{"url": "<the comment's URL>"}`.
- **`POST .../issues/:n/labels`.** The body is a bare JSON array of 1 to 100 names,
  none blank (an object, an empty array or a non-string entry is 400). The labels
  are added and the issue keeps its others. A name the repository does not have is
  for the forge to deal with, and its answer is passed through.
- **`DELETE .../issues/:n/labels/:name`.** `:name` is the last path segment,
  percent-decoded exactly once: `type%2Fbug` removes `type/bug`, `af:fix` is
  unchanged; a label named `.` or `..` is sent to the forge as `%2E` or `%2E%2E`, so that
  no dot segment is left in the path. The answer is 204 also when the issue never had
  the label. GitHub
  answers not found to such a removal and GitLab does not, so after a not found from
  the forge the hub reads the issue once: if it is there the answer is 204, and if
  not it is 404 `forge_not_found`.
- **`POST .../labels`.** `name` is required and must not be blank; `color` is six hex
  digits, with or without a leading `#` (anything else is 400), and defaults to
  `ededed`; `description` is optional. The label is looked up by name first. One that
  exists is answered 200 as the forge holds it, and is never changed, whatever color
  and description the request carries: making sure of a label twice is harmless. One
  that does not is created and answered 201. A create the forge refuses, as when a
  concurrent call created the same label, is followed by exactly one more lookup,
  which answers 200 when the label is there and the create's error when it is not.
- **`POST .../pulls`.** `title`, `head` and `base` are required and must not be
  blank; `body` is optional; `draft` must be a boolean and is `false` when absent.
  `head` is sent as given, so GitHub's `owner:branch` opens a pull request from a
  fork. With `?target=upstream` on a `carry_patch` workspace it is opened on the
  `upstream_url` repository with the upstream credential. A pull request that
  exists for `head` and `base` is refused by the forge and answered through the
  mapping (422 `forge_rejected`); the hub keeps no idempotency key, checks for no
  duplicate, and a retried create can create twice.
- **`POST .../pulls/:n/comments`.** `body` is required and must not be blank. The
  comment goes to the conversation of the pull request (on GitLab, the merge
  request's notes), and the answer is `{"url": …}`.

**Body limits.** These fields are measured, in Unicode code points and after the
client is built: the `body` of an issue (create and update), the `comment` of a
close, the `body` of a comment on an issue or a pull request, and the `body` of a pull
request.

| Forge | Limit |
|---|---|
| GitHub | 65,536 |
| GitLab | 1,000,000 |

A field over the limit is `413` with `error_type: body_too_large` and a message such
as `body is 70000 characters; github accepts at most 65536`; nothing is sent. A field
of exactly the limit is sent, whatever the bytes of each code point. Titles, `head`,
`base`, label names, label descriptions and assignee names are not measured: the
forge rejects them, and its rejection is mapped like any other. A request body
larger than the server's `max_body_size` is refused earlier, by the server, with its
own 413.

**How a write is sent.**

- The requests go to the API root of the workspace's `FORGE_API_URL` (or
  `UPSTREAM_FORGE_API_URL`), or to the one the hub states for the host; unlike a read,
  a write does not look at the `GITHUB_API_URL` or `GITLAB_API_URL` of the hub's own
  environment.
- Creating an issue, commenting, adding labels and opening a pull request are issuex's
  calls. issuex cannot send the rest the way these routes need, so the hub sends them
  itself, through the same HTTP client, with the same token rules, user agent,
  redirect policy and deadline: an issue update (issuex sends only non-empty fields,
  so it cannot clear a body or a set), the label lookup and the label create (issuex
  has no lookup, and its create returns no label and counts an existing label as a
  success), the removal of a label on GitHub (issuex counts GitHub's 404 as a
  success), and the read of the issue after a not found. Those requests are not
  retried at all, a rate limit included; issuex retries its own once after the forge's
  `Retry-After`, as for a read.
- The hub adds no retry of its own. A create that times out or cannot reach the forge
  may have happened, and the hub reports the mapped error without checking.
- A write the caller walks away from is not cancelled: it runs on the request's
  context with the cancellation detached, so that a write which left the hub is
  finished and recorded.
- A write that takes several requests (a close with a comment, a label lookup and
  create, a removal and its read) gives every request the whole 30 seconds.
- A write needs no clone and takes no workspace lock.

**Audit.** A write that sent at least one request to the forge records exactly one
event, whether it succeeded or failed, before the answer is written. See
[Hub Audit Events](#hub-audit-events).

### The webhook routes

| Route | Body | Success |
|---|---|---|
| `PUT /api/v1/workspaces/:slug/forge/webhook` | `{"secret"?: string}`, or no body | 200, `{"configured": true, "url": …, "secret"?: …}` |
| `DELETE /api/v1/workspaces/:slug/forge/webhook` | none | 204, no body |
| `GET /api/v1/workspaces/:slug/forge/deliveries?since=&limit=` | none | 200, a bare array |

**Authentication:** the `PUT` and the `DELETE` need an API Key, an Admin Token or a PAT
with the `forge:write` scope; the `GET` a PAT with `forge:read` or `forge:write`. None of
the three takes `target`, sends a request to the forge, takes a workspace lock or needs a
clone. A forge's webhook is registered by a person: the hub returns the URL and the secret
to enter in the forge's webhook settings and does not call the forge's hook API.

The secret is the workspace secret `FORGE_WEBHOOK_SECRET`. `PUT` is one way to set it;
`afc secrets create FORGE_WEBHOOK_SECRET --workspace <slug>` is another. The hub reads it
again for every delivery and caches nothing, so a rotated secret applies from the next
delivery. Its value is in the response of the `PUT` that generated it and nowhere else: not
in another response, a log line, an audit event or the delivery log.

#### PUT /api/v1/workspaces/:slug/forge/webhook

Set the webhook secret of the workspace. The checks run in this order, and the first that
fails answers:

1. No credential: 401.
2. A PAT without `forge:write`: 403, `PAT requires forge:write scope`.
3. A workspace that does not exist or is not the caller's: 404 `workspace not found`.
4. A workspace that is not `active`: 400 `workspace is not active`.
5. The body.

The body is an optional JSON object. With no body, `{}` or `{"secret": null}` the hub
generates a secret: 32 bytes from `crypto/rand`, as 64 lowercase hex characters. A secret
the caller supplies must be 16 to 255 characters, each a printable ASCII character from `!`
to `~` (no space and no control character, so that it is safe in the header GitLab sends it
in). Any other body is 400, and the message names `secret` and the rule and never repeats the
value:

| Message | When |
|---|---|
| `the request body must be a JSON object with an optional "secret" string` | the body is not one JSON object |
| `secret must be a string` | `secret` is not a string or `null` |
| `secret must be 16 to 255 characters long` | the length is wrong |
| `secret must only have the printable ASCII characters from ! to ~, so no space and no control character` | a character is outside the range |

The hub updates the secret that is there and creates one that is not; a create that loses a
race to a concurrent request is followed by one more update. A workspace scope that is full
answers 400 with the store's message (`maximum of 100 entries per scope exceeded`); any other
failure of the store is 500 `internal server error`, and neither emits an event.

**Response (success):** HTTP 200 OK

```json
{
  "configured": true,
  "url": "https://hub.example.com/hooks/forge/widgets",
  "secret": "5d0c…64 hex characters…"
}
```

`url` is `null` when `server.external_url` is not set; the secret is stored all the same.
`secret` is present only when the hub generated it, and no later response shows it again.
The hub emits `hub.forge.webhook.set` with the metadata `{"generated": <bool>}`.

#### DELETE /api/v1/workspaces/:slug/forge/webhook

Remove the webhook secret, so that the hub refuses the forge's deliveries (404) from then
on. It checks the same as the `PUT` up to the workspace's ownership and not its status, so it
works on an archived workspace too. It answers 204 whether or not there was a secret, and
emits `hub.forge.webhook.unset` with `{"existed": <bool>}`. The rows of the delivery log stay
until they are pruned. A failure of the store is 500.

#### GET /api/v1/workspaces/:slug/forge/deliveries

List the deliveries the hub recorded for the workspace, newest first, whatever the status of
the workspace. It checks as `GET .../forge` does (401, 403 naming `forge:read`, 404).

| Parameter | Description |
|---|---|
| `since` | An RFC 3339 timestamp; only deliveries received at that time or later (inclusive). The log keeps whole seconds, so a `since` with a fraction starts at the next second. Offsets are normalised to UTC |
| `limit` | An integer from 1 to 100; default 30 |

A value that is not valid, a repeated parameter included, is 400 with a message that names the
parameter (`since must be an RFC 3339 timestamp, for example 2026-10-21T09:00:00Z`, `limit must
be an integer from 1 to 100`, `since must be given once`).

**Response (success):** HTTP 200 OK with a bare array, `[]` when there is none. Rows of the
same second come last recorded first.

```json
[
  {
    "delivery_id": "72d3162e-cc78-11e3-81ab-4c9367dc0958",
    "forge": "github",
    "event": "issues",
    "action": "labeled",
    "target": "origin",
    "outcome": "relayed",
    "relayed": ["hub.forge.issue.labeled"],
    "received_at": "2026-10-21T09:12:03Z"
  }
]
```

| Field | Type | Description |
|-------|------|-------------|
| `delivery_id` | string | The forge's id of the delivery (see below) |
| `forge` | string | `github` or `gitlab` |
| `event` | string | The forge's event name (`X-GitHub-Event`, `X-Gitlab-Event`), cut to 64 bytes |
| `action` | string | The action in the payload; `""` when it has none |
| `target` | string or null | `origin` or `upstream`, the repository of the workspace the delivery is about; `null` when it is about neither |
| `outcome` | string | `relayed`, `ignored`, `repository_mismatch` or `malformed` |
| `relayed` | array of strings | The event types handed to the emitter, in order; `[]` for any outcome but `relayed` |
| `received_at` | string | When the hub received it, as a UTC timestamp |

### The webhook ingress

`POST /hooks/forge/:slug` receives the deliveries of a workspace's forge. It is mounted on
the Echo root, beside `/git/...`, **outside the API group and its bearer authentication**: the
forge proves who it is with the signature of the workspace's secret. It takes no workspace
lock and never contacts the forge. The body size limit and the content-type check of the API
group do not apply to it; the ingress has a limit of its own.

**Steps**, in this order:

1. **Lookup.** The hub reads the workspace and its secret for every request. An unknown slug
   and a workspace without the secret (or with an empty one) answer **404 with the same body**
   (the message `not found`), before the body is read. A failure of the database or the
   secrets store is 500. The status of the workspace does not matter: an archived workspace
   that has a secret still relays.
2. **The scheme**, by header. `X-Hub-Signature-256` present is GitHub's; else `X-Gitlab-Token`
   present is GitLab's; with neither the answer is 401, before the body is read. A header
   given twice is not valid.
3. **GitLab's token** is compared with the secret in constant time before the body is read.
4. **The body** is read up to 1 MiB (1,048,576 bytes). More is 413 with
   `error_type: body_too_large`, without the rest being read when the length was announced.
5. **GitHub's signature** must be `sha256=` and the 64 lowercase hex digits of the HMAC-SHA256
   of the raw body under the secret, compared with `hmac.Equal`.

A missing, malformed or wrong signature or token is **401 `delivery signature missing or
invalid`** whatever is wrong. Such a request writes no row and emits no event; it is logged at
warn level with the slug and the scheme (`github`, `gitlab` or `none`) and never with a header
value or the body.

**Identity.** After the verification the hub reads the delivery id and the event name from
the headers:

| Forge | Delivery id | Event name |
|---|---|---|
| GitHub | `X-GitHub-Delivery` | `X-GitHub-Event` |
| GitLab | `Idempotency-Key`, else `X-Gitlab-Event-UUID` | `X-Gitlab-Event` |

A delivery without either, or with an id of more than 255 characters or with a character
outside printable ASCII (space to `~`), is **400** and nothing is recorded.

**Deduplication.** Deliveries are recorded in the table `forge_deliveries`, keyed by the workspace
and the delivery id, with an `INSERT … ON CONFLICT DO NOTHING`. A delivery whose key is there
already is a **duplicate**: it is answered `202 {"outcome":"duplicate"}`, emits nothing and adds
no row, and so are all but one of the copies of a delivery that arrive together. If the insert
fails the answer is 500 and nothing is emitted, so that the forge's redelivery is processed in
full.

**Answer, then relay.** An accepted delivery is answered **202** with `{"outcome": …}`; the
answer is written and flushed before the first event is handed to the emitter, so that a slow
audit store cannot hold the forge's connection open:

| Outcome | Meaning |
|---|---|
| `relayed` | At least one event was handed to the emitter |
| `ignored` | The event and action are not ones the hub relays, or the payload says nothing to relay (a note on a commit, an update that changes nothing), whichever repository it names |
| `repository_mismatch` | A delivery that has events to relay about a repository that is neither the workspace's origin nor, on a `carry_patch` workspace with an `upstream_url`, its upstream (or the workspace's remote cannot be read) |
| `duplicate` | The delivery id was recorded before |

A body that is not one JSON object, or lacks the field that says what the delivery is about
(the repository, the number of an issue, a pull request or a merge request, the name of a label,
the ref of a push), is recorded as `malformed` and answered **400**. The descriptive fields
(title, URL, branches, labels) default to empty instead. The events are emitted in order through
the audit emitter on a context the end of the request does not cancel; an emit error is logged
and changes nothing about the answer, and so is an answer that cannot be written (the forge has
gone): the delivery is recorded, so its events are relayed all the same. The payload is never
stored and never logged.

**Repository binding.** For a kind the hub relays, the payload's repository path (GitHub
`repository.full_name`, GitLab `project.path_with_namespace`) is compared, ignoring case, with
the origin's `owner/name` (the full namespace path on GitLab) and, on a `carry_patch` workspace
with an `upstream_url`, with the upstream's. A match with the origin is `target: origin`, one
only with the upstream `target: upstream`. A delivery of any other kind whose repository is the
workspace's is bound the same way and recorded with its target; it is ignored all the same.

**Mapping.**

| GitHub (`X-GitHub-Event`, `action`) | Hub event(s) |
|---|---|
| `issues` opened, edited, closed, reopened | `hub.forge.issue.opened`, `.edited`, `.closed`, `.reopened` |
| `issues` labeled, unlabeled | `hub.forge.issue.labeled`, `.unlabeled` |
| `issue_comment` created | `hub.forge.comment.created` (`pull_request: true` when the issue has a `pull_request` object) |
| `pull_request` opened, reopened, synchronize | `hub.forge.pull_request.opened`, `.reopened`, `.synchronize` |
| `pull_request` closed | `.merged` when `pull_request.merged` is true, else `.closed` |
| `push` | `hub.forge.push` |

| GitLab (`X-Gitlab-Event`, `object_attributes.action`) | Hub event(s) |
|---|---|
| `Issue Hook` open, close, reopen | `hub.forge.issue.opened`, `.closed`, `.reopened` |
| `Issue Hook` update | `.edited` when `changes` has `title` or `description`; then one `.unlabeled` for each label in `changes.labels.previous` and not in `current`; then one `.labeled` for each label in `current` and not in `previous`; each group sorted by name. An update with none of these changes is ignored. At most 100 label events are relayed for one delivery (the first, `unlabeled` before `labeled`, with a warning in the log), and an event lists at most 100 labels |
| `Note Hook` on an `Issue` or a `MergeRequest`, no action or `create` | `hub.forge.comment.created` (`pull_request: true` for a merge request) |
| `Merge Request Hook` open, close, reopen, merge | `hub.forge.pull_request.opened`, `.closed`, `.reopened`, `.merged` |
| `Merge Request Hook` update | `.synchronize` when the payload has `oldrev`; an update without it is ignored |
| `Push Hook`, `Tag Push Hook` | `hub.forge.push` |

Everything else is ignored, for example GitHub `ping`, `pull_request` labeled and `issue_comment`
edited, GitLab's confidential hooks, and notes on commits or snippets. The events and their
metadata are in [Hub Audit Events](#hub-audit-events).

**The delivery log** is pruned by the hub every hour, starting when it starts: rows more than 7
days old and the rows of workspaces that no longer exist are deleted.

**Not supported:** Forgejo and Gitea deliveries, GitLab's signing-token header, GitHub's legacy
SHA-1 `X-Hub-Signature` and form-encoded payloads (such a delivery with a valid signature is
recorded `malformed`), a webhook secret of an org or a user, and rate limiting of the endpoint.

### Failures of the forge

Every failure of a call to the forge is answered by one of these, whatever the route:

| Condition | Status | `error_type` |
|---|---|---|
| `target=upstream` on a workspace that is not a `carry_patch` fork with an `upstream_url` | 400 | `workspace_mode_mismatch` |
| an invalid `target`, `:n` or query parameter, or a write body that breaks a rule of its route | 400 | |
| a write on a workspace that is not `active` | 400 | |
| a body-like field over the forge's limit (writes) | 413 | `body_too_large` |
| `FORGE_API_URL` / `UPSTREAM_FORGE_API_URL` invalid | 422 | `forge_api_url_invalid` |
| the forge cannot be told from the host, or the remote URL names no repository | 422 | `forge_unknown` |
| the forge answered 404 | 404 | `forge_not_found` |
| the forge answered 401 | 502 | `forge_unauthenticated` |
| the forge answered 403 that is not a rate limit | 502 | `forge_forbidden` |
| a rate limit issuex could not wait out | 429 | `forge_rate_limited` |
| the forge answered another 4xx | 422 | `forge_rejected` |
| the forge answered 5xx, a response that cannot be read, or a redirect to another host | 502 | `forge_error` |
| the connection failed (DNS, TCP, TLS) | 502 | `forge_unreachable` |
| the 30-second deadline passed | 504 | `forge_timeout` |
| the secrets or variables store failed | 500 | |

- The forge's own message is in `message` as `the forge answered <status>: <message>`,
  after three steps: the resolved token is replaced with `[redacted]`, URL userinfo is
  removed, and the text is cut to 1024 bytes on a UTF-8 boundary. A `401` for a
  `basic` credential adds `The forge API needs a token: store one as GIT_PAT, or as
  GIT_PASSWORD.`
- `forge_rate_limited` carries `Retry-After` in whole seconds, rounded up, when issuex
  knows how long to wait. issuex adds one second to the forge's own value
  (`Retry-After: 1` gives `2`) and itself waits out a backoff of up to 120 seconds
  and tries once more; the 30-second deadline cuts a wait of 29 to 120 seconds
  short, which is answered `504 forge_timeout`.
- This table holds for the write routes too. A write the forge refuses is answered
  like a read it refuses, and the event it records says the same in `error_type`.
- Each 5xx and 429 answer caused by the forge is logged once, as `forge: request
  failed` with the workspace (`slug`), the `target`, the `operation` (the issuex
  method, for example `ListIssues`), the status the forge gave (`forge_status`; 0
  when it gave none), the hub's `status` and `error_type`. The line never holds the
  token, a message, a request body or a response body. A store failure is logged
  with the workspace, the target and the type of the error, never its text.
- No response and no log line contains the credential or the userinfo of a URL.

---

## Hub Audit Events

The hub emits structured audit events for significant operations. These events
are stored in `hub_audit_events` and are queryable through the unified audit
query endpoint (`GET /api/v1/audit`). Each event has an `event_type`, an actor
(the calling credential), a `resource_type`, and metadata.

| Event Type | Resource Type | When Emitted | Metadata |
|------------|---------------|--------------|----------|
| `hub.patch.create` | `patch` | A single patch is registered via `POST /workspaces/:slug/patches` (not emitted for batch or `if_not_exists` hits) | `branch_name`, `position`, `branch_resolution` (`local`, `origin_tracking`, `origin_fetch` or `skipped`) |
| `hub.rebuild.followup` | `rebuild` | A follow-up rebuild is enqueued because a patch tip or upstream base moved during a rebuild run | `submitted_by`, workspace slug |
| `hub.patch.sync` | `patch` | A carry-patch sync completes in `origin` mode (`resource_id` is the workspace slug, `action` is `sync`) | `origin_fetched` (bool), `created` (branch names), `fast_forwarded` (branch names), `replaced` (branch names), `diverged` (branch names), `missing_on_origin` (branch names) |
| `hub.patch.replace` | `patch` | A patch branch is replaced during an `origin`-mode sync (one event per replaced branch; `resource_id` is the branch name, `action` is `replace`, as for the reset-triggered event) | `branch_name`, `replaced_sha`, `origin_sha` |
| `hub.patch.mirror_failed` | `patch` | A hub-mode mirror of a registered patch branch to the fork failed (one event per failed branch) | `branch_name`, `error` (with any URL userinfo removed) |
| `hub.patch.reset` | `patch` | A patch branch is reset to the fork's tip via `POST /workspaces/:slug/patches/:id/reset-to-origin` | `branch_name`, `action` (`none`, `created`, `fast_forwarded` or `replaced`), `local_sha` (omitted when no local branch), `origin_sha`, `replaced_sha` (present only for `replaced`) |
| `hub.sandbox.lease` | `sandbox` | A sandbox lease is issued via `POST /workspaces/:slug/sandboxes` (one event per successful request; `resource_id` is the lease ID, `action` is `lease`; ending a lease emits `hub.sandbox.destroy` or `hub.sandbox.expire` and no event of this kind) | `lease_id`, `profile`, `purpose`, `ttl` (a Go duration string such as `1h0m0s`), `secret_names`, `optional_secret_names`. Never a secret value, an environment value or the lease token |
| `hub.sandbox.create` | `sandbox` | The sandbox gateway made the sandbox of a lease, which became `active` (`resource_id` is the lease ID, `action` is `create`; actor type `lease` and actor ID the lease's `created_by`) | `lease_id`, `sandbox_id`, `image`, `network` |
| `hub.sandbox.failed` | `sandbox` | A lease became `failed` because its sandbox could not be created (`action` is `fail`; actor type `lease` and actor ID the lease's `created_by`) | `lease_id`, `error` (scrubbed: no secret value, fixed environment value or URL user information, at most 1024 bytes) |
| `hub.sandbox.destroy` | `sandbox` | A lease became `destroyed`: its holder closed the sandbox through the gateway (actor type `lease`, actor ID the lease's `created_by`), `DELETE` ended it (the calling credential is the actor), or the hub ended it when it shut down or started after a crash (actor type `system`, empty actor ID). One event per change (`action` is `destroy`) | `lease_id`, `sandbox_id` (`null` when the lease never had a sandbox), `reason` (`client`, `api`, `shutdown` or `restart`; a Close or a `DELETE` while the hub shuts down carries `shutdown`) |
| `hub.sandbox.expire` | `sandbox` | Time ended a lease: a `leased` lease got no `Create` before its deadline (a late `Create`, or a sweep), or an `active` lease passed its `expires_at` and a sweep destroyed its sandbox. One event per lease, and no `hub.sandbox.destroy` (`action` is `expire`; actor type `system`, empty actor ID) | `lease_id`, `state` (`expired` for a lease that never had a sandbox, `destroyed` for one whose sandbox was destroyed), `sandbox_id` (only when a sandbox was destroyed) |
| `hub.forge.issue.create` | `issue` | `POST /workspaces/:slug/issues` sent a request to the forge, whether it succeeded or not (`resource_id` is the new issue's number, empty on a failure; `action` is `create`). The actor is the caller. All nine `hub.forge.*` events share the metadata under the table | none of its own |
| `hub.forge.issue.update` | `issue` | `PATCH /workspaces/:slug/issues/:n` (`resource_id` is `n`, `action` is `update`) | `fields`: the names of the fields the request changed, sorted |
| `hub.forge.issue.close` | `issue` | `POST /workspaces/:slug/issues/:n/close` (`resource_id` is `n`, `action` is `close`). One event stands for a close with a comment too; there is no separate comment event | `comment_url`: the URL of the comment, when one was posted, also when the close then failed |
| `hub.forge.comment.create` | `comment` | `POST /workspaces/:slug/issues/:n/comments` (`resource_id` is the issue number `n`, `action` is `create`) | none of its own; `url` is the comment's |
| `hub.forge.label.add` | `label` | `POST /workspaces/:slug/issues/:n/labels` (`resource_id` is `n`, `action` is `add`) | `labels`: the names the request gave |
| `hub.forge.label.remove` | `label` | `DELETE /workspaces/:slug/issues/:n/labels/:name` (`resource_id` is `n`, `action` is `remove`) | `label`: the name |
| `hub.forge.label.create` | `label` | `POST /workspaces/:slug/labels`, whether it created the label or found it (`resource_id` is the label name, `action` is `create`) | `label`: the name; `created`: true only when this call created the label, false when it existed and when the call failed |
| `hub.forge.pull_request.create` | `pull_request` | `POST /workspaces/:slug/pulls` (`resource_id` is the new number, empty on a failure; `action` is `create`) | `head`, `base`, `draft` |
| `hub.forge.pull_request.comment` | `pull_request` | `POST /workspaces/:slug/pulls/:n/comments` (`resource_id` is `n`, `action` is `comment`) | none of its own; `url` is the comment's |
| `hub.forge.issue.opened` | `issue` | A forge reported that an issue was opened on a workspace's repository (GitHub `issues` opened, GitLab `Issue Hook` open). Relayed by the webhook ingress after it answered the forge; `resource_id` is the issue number (the `iid` on GitLab), `action` is `opened`. The actor type is `forge` and the actor ID empty; the login is in `actor`. All thirteen delivery events share the metadata under the table | `number`, `title` (cut to 256 bytes), `url`, `labels` (the issue's label names, the first 100) |
| `hub.forge.issue.edited` | `issue` | The title or body of an issue changed (`action` is `edited`) | as `hub.forge.issue.opened` |
| `hub.forge.issue.closed` | `issue` | An issue was closed (`action` is `closed`) | as `hub.forge.issue.opened` |
| `hub.forge.issue.reopened` | `issue` | An issue was reopened (`action` is `reopened`) | as `hub.forge.issue.opened` |
| `hub.forge.issue.labeled` | `issue` | A label was put on an issue; a GitLab update that adds several is one event for each, sorted by name (`action` is `labeled`) | `number`, `label`, `labels` (the issue's labels now, the first 100), `url` |
| `hub.forge.issue.unlabeled` | `issue` | A label was taken off an issue; on GitLab these come before the `labeled` events of the same update (`action` is `unlabeled`) | as `hub.forge.issue.labeled` |
| `hub.forge.comment.created` | `comment` | A new comment on an issue or a pull request or merge request (`resource_id` is its number, `action` is `created`). The text of the comment is not in the event | `number`, `comment_url`, `pull_request` (true for a pull request or merge request) |
| `hub.forge.pull_request.opened` | `pull_request` | A pull request or merge request was opened (`resource_id` is its number, `action` is `opened`) | `number`, `url`, `head`, `base` (branch names), `merged` (bool) |
| `hub.forge.pull_request.closed` | `pull_request` | One was closed without being merged (`action` is `closed`) | as `hub.forge.pull_request.opened` |
| `hub.forge.pull_request.merged` | `pull_request` | One was merged; such a delivery is never also `closed` (`action` is `merged`) | as `hub.forge.pull_request.opened` |
| `hub.forge.pull_request.reopened` | `pull_request` | One was reopened (`action` is `reopened`) | as `hub.forge.pull_request.opened` |
| `hub.forge.pull_request.synchronize` | `pull_request` | Commits were pushed to its branch (`action` is `synchronize`) | as `hub.forge.pull_request.opened` |
| `hub.forge.push` | `repository` | Commits or a tag were pushed (`resource_id` is the ref, `action` is `push`) | `ref`, `before`, `after` |
| `hub.forge.webhook.set` | `webhook` | `PUT /workspaces/:slug/forge/webhook` stored the secret (`resource_id` is the workspace slug, `action` is `set`). The actor is the caller | `generated`: true when the hub made the secret up. Never the secret |
| `hub.forge.webhook.unset` | `webhook` | `DELETE /workspaces/:slug/forge/webhook` (`resource_id` is the slug, `action` is `unset`; also when there was no secret). The actor is the caller | `existed`: false when there was no secret to remove |

A nil audit emitter skips emission. An emit error is logged without affecting
the response. In `hub` mode no `hub.patch.sync` or `hub.patch.replace` events
are emitted. On the ref-write failure path, events are still emitted for the
outcomes already produced.

**The `hub.forge.*` events.** One event is recorded for each write request that sent
at least one request to the forge, whether the write succeeded or failed, and it is
recorded before the answer is written, on a context the caller's disconnect does not
cancel. A request the hub refuses on its own (401, 403, 404, 400, 413, 422 or a
credential error) sends nothing and records nothing. The actor is the caller
(`actor_id` its user, `actor_type` its credential type) and `workspace` the slug.
Common metadata: `target` (`origin` or `upstream`); `ok`; `error_type` only when `ok`
is false, the `error_type` the hub answered with; `number`, a JSON number, when
known; and `url`, the issue, pull-request or comment URL the forge returned, with any
userinfo removed. The metadata holds names, numbers and URLs and never a credential,
URL userinfo, a title, a body, a comment or a message from the forge. These events
say who asked the hub to write; the events a forge reports about a repository, when a
webhook relays them, are named in the past tense and are a different thing.

**The delivery events** (`hub.forge.issue.opened` to `hub.forge.push`, thirteen of them) are
relayed by the [webhook ingress](#the-webhook-ingress), one for each event the delivery comes to
and in the order the mapping makes them (a GitLab update: `edited`, then `unlabeled`, then
`labeled`), after the 202 answer has been flushed. `workspace` is the
slug; the actor is the forge, not a hub identity (`actor_type` `forge`, `actor_id` empty).
Common metadata: `forge` (`github` or `gitlab`), `target` (`origin` or `upstream`),
`delivery_id` (the forge's id) and `actor` (the forge login; `""` when the payload has none),
and the keys of the table for the kind, no other field of the payload. URLs have their user
information removed. A consumer reads them on `GET /api/v1/events?workspace=<slug>` and
`GET /api/v1/audit?workspace=<slug>`. `hub.forge.webhook.set` and `.unset` say who changed
the secret and never hold it.

A reset with action `replaced` also emits `hub.patch.replace` with
`branch_name`, `replaced_sha`, `origin_sha` and an added `trigger` key set to
`reset_to_origin`. Events emitted by sync carry no `trigger` key; a consumer
reads its absence as `sync`.

---

## Identity and Access Endpoints (apikit-provided)

The following endpoints are provided by the `apikit` library. They are mounted
on the **same API group** as the workspace endpoints, so they live under the
configured mount point -- `/api/v1` by default (`server.mount_point` in
`config.toml`). They use the same authentication mechanisms and error envelope
described above.

The only routes registered outside the API group are `GET /healthz`,
`GET /readyz`, `GET /version`, and hub's own `GET /metrics`, `/git/...` and
`POST /hooks/forge/:slug` endpoints.

### Server Information

#### GET /version

Return build information and the configured API mount point.

**Authentication:** None. Registered at the server root, outside the API auth
group. Cached publicly.

**Response:** HTTP 200 OK.

```json
{
  "go_version": "1.26.5",
  "build_time": "2026-09-01T00:00:00Z",
  "commit": "abc1234",
  "mount_point": "/api/v1"
}
```

---

### Authentication

#### GET /api/v1/auth/providers

List the configured OAuth providers and the authorize URL for each.

**Authentication:** None. `RegisterOAuthHandlers` runs before the auth
middleware is attached to the API group, and Echo captures a group's middleware
when a route is added, so both `/auth` routes are public. Cached publicly for
five minutes.

**Response:** HTTP 200 OK with a JSON array.

```json
[
  {
    "name": "github",
    "authorize_url": "https://github.com/login/oauth/authorize?client_id=..."
  }
]
```

---

#### POST /api/v1/auth/callback

Exchange an OAuth authorization code for a user record and an API key. This is
the **only** way an API key is created -- there is no password login endpoint.

**Authentication:** None (public endpoint).

**Request Body:**

```json
{
  "provider": "github",
  "code": "authorization-code-from-provider",
  "redirect_uri": "http://localhost:8080/callback",
  "expires": 90
}
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `provider` | yes | string | Provider name, as listed by `GET /api/v1/auth/providers` |
| `code` | yes | string | Authorization code returned by the OAuth provider |
| `redirect_uri` | no | string | Validated against the provider's redirect allowlist |
| `expires` | no | integer | API key lifetime in days; must be 0, 30, 60, or 90. Defaults to 90. `0` means no expiry. |

**Behavior:** In a single transaction the hub upserts the user record, revokes
**all** existing API keys for that user, and generates a new key. The plaintext
key is returned once; only its SHA-256 hash is persisted. On a new user, hub's
`OnAfterUserCreate` hook runs inside the same transaction and creates the
user's personal organization.

**Note:** Because re-login mass-revokes keys, any automation still holding an
older key for the same user stops working after a login.

**Response:** HTTP 200 OK.

```json
{
  "user": {
    "id": "uuid-string",
    "username": "alice",
    "email": "alice@example.com",
    "full_name": "Alice Example",
    "status": "active",
    "role": "user",
    "provider": "github",
    "provider_id": "12345",
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  },
  "api_key": {
    "key": "af_<key_id>_<secret>",
    "key_id": "<key_id>",
    "expires_at": "2024-04-01T00:00:00Z"
  }
}
```

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing or invalid fields; `redirect_uri` not in the allowlist |
| 401 | The authorization code is invalid or expired |
| 403 | The matched user is blocked |
| 409 | The user already exists under a conflicting identity |

---

### User

#### GET /api/v1/user

Get the profile of the authenticated user.

**Authentication:** API Key, PAT with `users:read` scope, or Admin Token.

**Response:** HTTP 200 OK with the user object (see the callback response
above for the field set).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 404 | User not found |

#### PATCH /api/v1/user

Update the authenticated user's profile. `full_name` is the only mutable
field, and it is **required** -- a missing or null value is rejected.

**Authentication:** API Key, PAT with `users:read` scope, or Admin Token.

**Request Body:**

```json
{
  "full_name": "Alice Example"
}
```

**Response:** HTTP 200 OK with the updated user object.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Invalid request body, or `full_name` is missing |
| 401 | Unauthenticated request |
| 404 | User not found |

---

### Keys

API keys are created only by the OAuth callback. These endpoints list, rotate,
and revoke them; none of them creates a key.

#### GET /api/v1/user/keys

List API keys for the authenticated user. Metadata only -- secret hashes,
expiry-day settings, and plaintext secrets are never returned. Supports
conditional GET via ETag.

**Authentication:** API Key, PAT with `keys:read` scope, or Admin Token.

**Response:** HTTP 200 OK with a JSON array.

```json
[
  {
    "key_id": "<key_id>",
    "created_at": "2024-01-01T00:00:00Z",
    "expires_at": "2024-04-01T00:00:00Z",
    "revoked_at": null
  }
]
```

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 304 | The ETag matched; the listing is unchanged |
| 401 | Unauthenticated request |

#### POST /api/v1/user/keys/:key_id/refresh

Rotate an API key's secret, preserving `key_id` and recomputing the expiry
from the key's stored lifetime.

**Authentication:** API Key only. **PAT authentication is rejected.**

**Response:** HTTP 200 OK. The plaintext key is returned only here.

```json
{
  "key": "af_<key_id>_<new-secret>",
  "key_id": "<key_id>",
  "expires_at": "2024-07-01T00:00:00Z"
}
```

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | The key is revoked, or already expired |
| 401 | Unauthenticated, or authenticated with something other than an API key |
| 404 | Key not found or not owned by the user |

#### DELETE /api/v1/user/keys/:key_id

Revoke an API key. Self-revocation is permitted: the auth middleware validates
the credential before the handler runs and does not re-validate mid-flight.

**Authentication:** API Key, PAT with `keys:manage` scope, or Admin Token.

**Response:** HTTP 204 No Content.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 404 | Key not found or not owned by the user |

---

### Tokens

#### GET /api/v1/user/tokens

List personal access tokens (PATs) for the authenticated user, including the
permissions granted to each.

**Authentication:** API Key, PAT with `tokens:read` scope, or Admin Token.

**Response:** HTTP 200 OK with a JSON array.

```json
[
  {
    "token_id": "<token_id>",
    "name": "CI token",
    "permissions": ["workspaces:read", "workspaces:create"],
    "expires_at": "2024-04-01T00:00:00Z",
    "created_at": "2024-01-01T00:00:00Z",
    "revoked_at": null
  }
]
```

#### POST /api/v1/user/tokens

Create a personal access token with specific permissions.

**Authentication:** API Key, PAT with `tokens:manage` scope, or Admin Token.

**Request Body:**

```json
{
  "name": "CI token",
  "permissions": ["workspaces:read", "workspaces:create"],
  "expires": 90
}
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `name` | yes | string | Human-readable label; max 255 characters |
| `permissions` | yes | string[] | Permission scopes to grant (see Permission Scopes) |
| `expires` | no | integer | Token lifetime in days; must be 0, 30, 60, or 90. `0` means no expiry. |

**Privilege escalation:** A PAT creating another PAT may only grant permissions
it already holds; exceeding them returns 403. API keys are not subject to this
restriction and may mint a PAT with any registered permission.

**Response:** HTTP 201 Created. The plaintext token is returned only here.

```json
{
  "token_id": "<token_id>",
  "name": "CI token",
  "token": "af_pat_<token_id>_<secret>",
  "permissions": ["workspaces:read", "workspaces:create"],
  "expires_at": "2024-04-01T00:00:00Z",
  "created_at": "2024-01-01T00:00:00Z"
}
```

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Invalid body; `name` missing or over 255 characters; `permissions` empty; a permission is malformed or unknown; `expires` not 0, 30, 60, or 90 |
| 401 | Unauthenticated request |
| 403 | Caller lacks `tokens:manage`, or a PAT tried to grant a permission it does not hold |

#### GET /api/v1/user/tokens/:token_id

Get a single PAT belonging to the authenticated user.

**Authentication:** API Key, PAT with `tokens:read` scope, or Admin Token.

**Response:** HTTP 200 OK with the token metadata.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 404 | Token not found or not owned by the user |

#### DELETE /api/v1/user/tokens/:token_id

Revoke a personal access token.

**Authentication:** API Key, PAT with `tokens:manage` scope, or Admin Token.

**Response:** HTTP 200 OK with the revoked token's metadata (not 204).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | The token is already revoked |
| 401 | Unauthenticated request |
| 404 | Token not found or not owned by the user |

#### PUT / PATCH / DELETE /api/v1/user/tokens/:token_id/permissions

Modify a PAT's permissions. `PUT` replaces the list, `PATCH` adds to it, and
`DELETE` removes from it.

**Authentication:** API Key, PAT with `tokens:write` or `tokens:manage` scope,
or Admin Token.

**Request Body:**

```json
{
  "permissions": ["workspaces:read"]
}
```

An absent or null `permissions` field is always an error. On `PUT`, an explicit
empty array clears every permission and auto-revokes the token; on `PATCH` and
`DELETE` an empty array is rejected.

**Response:** HTTP 200 OK with the token's metadata after the change.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Invalid body; `permissions` absent; empty on `PATCH`/`DELETE`; token is revoked |
| 401 | Unauthenticated request |
| 403 | Caller lacks the required scope, or a PAT tried to grant a permission it does not hold |
| 404 | Token not found or not owned by the user |

---

### Orgs

#### GET /api/v1/user/orgs

List organizations the authenticated user belongs to.

**Authentication:** API Key, PAT with `orgs:read` scope, or Admin Token.

**Response:** HTTP 200 OK with a JSON array of organization objects.

```json
[
  {
    "id": "org-id",
    "name": "My Organization",
    "slug": "my-org",
    "url": "https://example.com",
    "owner_id": "uuid-string",
    "status": "active",
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  }
]
```

#### GET /api/v1/orgs/:id

Get organization details. Non-admin callers must be a member; admin
credentials bypass the membership check. Supports conditional GET via ETag.

**Note:** apikit addresses organizations by **id**. Hub's org-scoped secrets
and variables endpoints (`/api/v1/orgs/:slug/secrets`,
`/api/v1/orgs/:slug/vars`) address them by **slug**. Echo stores parameter
names per route, so both resolve correctly, but a client cannot assume one
identifier works for both.

**Authentication:** API Key, PAT with `orgs:read` scope, or Admin Token.

**Response:** HTTP 200 OK with the organization object.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 304 | The ETag matched; the record is unchanged |
| 401 | Unauthenticated request |
| 403 | Caller is not a member of the organization |
| 404 | Organization not found |

---

### Administration

Administration endpoints require an admin token or an admin-role API key.
There is no `/admin` path prefix.

**Note the asymmetry:** an admin-role API key passes `RequireAdmin` here, but
is treated as an ordinary user by the workspace and git subsystems, which check
for the admin token credential type directly. Only admin tokens have
cross-tenant workspace and git access.

#### Users

| Endpoint | Description |
|----------|-------------|
| `POST /api/v1/users` | Create a user. Body: `{username, email, provider, provider_id}`. Returns 201, or 409 if the username or provider identity exists. |
| `GET /api/v1/users` | List all users |
| `GET /api/v1/users/:id` | Get any user. Supports ETag; returns 304 when unchanged. |
| `PATCH /api/v1/users/:id` | Update any user. Body: `{full_name}`, required. |
| `POST /api/v1/users/:id/promote` | Promote to admin. Idempotent. |
| `POST /api/v1/users/:id/demote` | Demote from admin. Returns 409 for the last remaining admin. |
| `POST /api/v1/users/:id/block` | Block a user |
| `POST /api/v1/users/:id/unblock` | Unblock a user |
| `GET /api/v1/users/:id/keys` | List any user's API keys. Entries include `user_id`. |
| `DELETE /api/v1/users/:id/keys/:key_id` | Revoke any user's API key. Returns 204. |
| `GET /api/v1/users/:id/tokens` | List any user's PATs. Entries include `user_id`. |
| `DELETE /api/v1/users/:id/tokens/:token_id` | Revoke any user's PAT. Returns 204. |

Blocking a user disables all their API keys at authentication time without
individually revoking them; unblocking re-enables every non-revoked, unexpired
key.

#### Organizations

| Endpoint | Description |
|----------|-------------|
| `POST /api/v1/orgs` | Create an org. Body: `{name, slug, url, owner_id}`. Returns 201, or 409 if the name or slug exists, or 400 for an invalid slug format. |
| `GET /api/v1/orgs` | List all organizations |
| `PATCH /api/v1/orgs/:id` | Update an org. Body: `{name, url}`; at least one required. Returns 409 on a name collision. |
| `DELETE /api/v1/orgs/:id` | Delete an org. Returns 204. |
| `POST /api/v1/orgs/:id/block` | Block an org |
| `POST /api/v1/orgs/:id/unblock` | Unblock an org |
| `GET /api/v1/orgs/:id/members` | List members. Bypasses the membership check. |
| `PUT /api/v1/orgs/:id/members/:user_id` | Add a member. Returns the membership record. |
| `DELETE /api/v1/orgs/:id/members/:user_id` | Remove a member. Returns 204. |

A membership record has the shape
`{org_id, user_id, username, email, role, created_at}`.

All administration endpoints return 401 when unauthenticated and 403 for a
non-admin credential.

---

## Audit Ingestion Endpoints

The audit ingestion subsystem provides DuckDB-backed storage and REST API
endpoints for ingesting structured telemetry produced by nightshift agents.
All ingestion endpoints live under `/api/v1/workspaces/:slug/runs/:run_id/`.

POST endpoints require the `audit:write` permission scope. GET endpoints
require `audit:read`. Admin tokens and API keys have implicit access; PATs
require explicit scope grants.

All POST endpoints enforce workspace access control: an unknown workspace
returns `404`, a workspace other than a bound token's own returns `404`
`workspace not found`, archived workspaces return `409`, and the caller must
own the workspace (`403 workspace_access_denied`). A refused request stores
nothing. All use `INSERT OR IGNORE` for idempotent ingestion — duplicates
return `200` instead of `201`.

The `run_id` URL parameter must match the format `YYYYMMDD_HHMMSS_6hexchars`
(e.g. `20260704_143022_a1b2c3`).

Client-supplied timestamps (`timestamp`, `started_at`, `completed_at`) must be
RFC 3339; offsets are accepted and normalised to UTC. Malformed values are
rejected with `400` (per item, in the batch endpoints). GET endpoints under
the same prefix require the caller to own the workspace (404 otherwise).

### POST /api/v1/workspaces/:slug/runs/:run_id/events

Ingest a single audit event.

**Authentication:** API Key, or PAT with `audit:write` scope.

**Request Body:**

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `id` | no | string | Client-provided UUID; generated if omitted |
| `event_type` | yes | string | Event type identifier |
| `severity` | no | string | One of: `info`, `warning`, `error`, `critical`; defaults via `defaultSeverityFor` |
| `node_id` | no | string | Task graph node identifier |
| `session_id` | no | string | Session identifier |
| `timestamp` | no | string | RFC 3339 timestamp; defaults to current UTC |
| `payload` | no | object | Arbitrary JSON object |

**Response:** `201 Created` with `{id, run_id, event_type, severity, created_at}`.
`200 OK` if duplicate by `id`.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing `event_type`, invalid `run_id`, or `run_id` mismatch between URL and body |
| 403 | Insufficient scope, or the caller does not own the workspace (`workspace_access_denied`) |
| 404 | Unknown workspace, or a workspace other than a bound token's own (`workspace not found`) |
| 409 | Workspace archived (`workspace_archived`) |

---

### GET /api/v1/workspaces/:slug/runs/:run_id/events

Query audit events for a run with cursor-based pagination.

**Authentication:** API Key, or PAT with `audit:read` scope.

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `event_type` | string | (all) | Filter by event type |
| `severity` | string | (all) | Filter by severity |
| `node_id` | string | (all) | Filter by node ID |
| `session_id` | string | (all) | Filter by session ID |
| `since` | string | — | RFC 3339 lower bound (inclusive) |
| `until` | string | — | RFC 3339 upper bound (exclusive) |
| `order` | string | `desc` | Sort order: `asc` or `desc` |
| `limit` | int | 50 | Page size (max 500) |
| `cursor` | string | — | Opaque base64url pagination token |

**Response:** `200 OK`

```json
{
  "events": [...],
  "next_cursor": "string or null",
  "has_more": true
}
```

---

### POST /api/v1/workspaces/:slug/runs/:run_id/events/batch

Bulk-ingest up to 1000 audit events in a single request.

**Authentication:** API Key, or PAT with `audit:write` scope.

**Request Body:** JSON array of event objects (same schema as single POST).

**Constraints:** Array must have 1–1000 items. Items exceeding 1000 return
`413`. Empty arrays return `400`.

**Response:** `200 OK`

```json
{
  "accepted": 95,
  "duplicates": 3,
  "errors": [
    {"index": 7, "id": "...", "message": "missing event_type"}
  ]
}
```

---

### POST /api/v1/workspaces/:slug/runs/:run_id/sessions/outcomes

Ingest a single session outcome record.

**Authentication:** API Key, or PAT with `audit:write` scope.

**Request Body:**

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `id` | no | string | Client-provided UUID; generated if omitted |
| `session_id` | yes | string | Session identifier |
| `node_id` | no | string | Task graph node identifier |
| `status` | yes | string | Outcome status |
| `timestamp` | no | string | RFC 3339 timestamp |
| `duration_ms` | no | int | Duration in milliseconds (non-negative) |
| `token_usage` | no | object | Arbitrary JSON object |

**Response:** `201 Created` with `{id, run_id, node_id, status, created_at}`.
`200 OK` if duplicate.

---

### GET /api/v1/workspaces/:slug/runs/:run_id/sessions/outcomes

Query session outcomes for a run. Supports the same pagination parameters as
GET events (`session_id`, `status`, `node_id`, `since`, `until`, `order`,
`limit`, `cursor`).

**Response:** `200 OK` with `{outcomes: [...], next_cursor, has_more}`.

---

### POST /api/v1/workspaces/:slug/runs/:run_id/tools/calls

Ingest a single tool call record.

**Authentication:** API Key, or PAT with `audit:write` scope.

**Request Body:**

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `id` | no | string | Client-provided UUID |
| `tool_name` | yes | string | Name of the tool invoked |
| `node_id` | no | string | Task graph node identifier |
| `session_id` | no | string | Session identifier |
| `timestamp` | no | string | RFC 3339 timestamp |
| `duration_ms` | no | int | Call duration in milliseconds |
| `input` | no | object | Tool input payload |
| `output` | no | object | Tool output payload |

**Response:** `201 Created` with `{id, run_id, tool_name, called_at}`.
`200 OK` if duplicate.

---

### GET /api/v1/workspaces/:slug/runs/:run_id/tools/calls

Query tool calls for a run. Supports `tool_name`, `session_id`, `node_id`,
`since`, `until`, `order`, `limit`, `cursor`.

**Response:** `200 OK` with `{calls: [...], next_cursor, has_more}`.

---

### POST /api/v1/workspaces/:slug/runs/:run_id/tools/errors

Ingest a single tool error record.

**Authentication:** API Key, or PAT with `audit:write` scope.

**Request Body:**

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `id` | no | string | Client-provided UUID |
| `tool_name` | yes | string | Name of the tool that errored |
| `node_id` | no | string | Task graph node identifier |
| `session_id` | no | string | Session identifier |
| `error_code` | no | string | Machine-readable error code |
| `error_msg` | yes | string | Human-readable error message |
| `timestamp` | no | string | RFC 3339 timestamp |

**Response:** `201 Created` with `{id, run_id, tool_name, failed_at}`.
`200 OK` if duplicate.

---

### GET /api/v1/workspaces/:slug/runs/:run_id/tools/errors

Query tool errors for a run. Supports `tool_name`, `session_id`, `node_id`,
`since`, `until`, `order`, `limit`, `cursor`.

**Response:** `200 OK` with `{errors: [...], next_cursor, has_more}`.

---

### POST /api/v1/workspaces/:slug/runs/:run_id/traces

Ingest a single trace event.

**Authentication:** API Key, or PAT with `audit:write` scope.

**Request Body:**

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `id` | no | string | Client-provided UUID |
| `event_type` | yes | string | One of: `session.init`, `assistant.message`, `tool.use`, `tool.error`, `session.result` |
| `node_id` | no | string | Task graph node identifier |
| `session_id` | no | string | Session identifier |
| `sequence` | no | int | Event sequence number |
| `timestamp` | no | string | RFC 3339 timestamp |
| `data` | no | object | Arbitrary JSON object |

**Response:** `201 Created` with `{id, run_id, event_type, timestamp}`.
`200 OK` if duplicate.

---

### GET /api/v1/workspaces/:slug/runs/:run_id/traces

Query trace events for a run. Supports `event_type`, `session_id`, `node_id`,
`since`, `until`, `order`, `limit`, `cursor`.

**Response:** `200 OK` with `{traces: [...], next_cursor, has_more}`.

---

### POST /api/v1/workspaces/:slug/runs/:run_id/traces/batch

Bulk-ingest up to 1000 trace events. Same constraints and response format as
`POST events/batch`.

**Response:** `200 OK` with `{accepted, duplicates, errors}`.

---

### POST /api/v1/workspaces/:slug/runs/:run_id/postmortem

Submit a postmortem report for a run. Only one postmortem per `run_id`.

**Authentication:** API Key, or PAT with `audit:write` scope.

**Request Body:**

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `schema_version` | no | int | Must be `1` if provided (422 otherwise) |
| `run_status` | yes | string | One of: `stalled`, `block_limit`, `cost_limit`, `session_limit` |
| `started_at` | yes | string | RFC 3339 timestamp |
| `completed_at` | yes | string | RFC 3339 timestamp |
| `task_summary` | yes | object | Must contain a `total` field |
| `cost_summary` | yes | object | Aggregated token and cost totals |
| `blocked_tasks` | no | array | Objects with `node_id` and `reason` |
| `session_history` | no | array | Opaque JSON array stored as-is |

**Response:** `201 Created` with `{run_id, run_status, created_at}`.
`200 OK` if duplicate.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing required fields or invalid `run_status` |
| 422 | Unsupported `schema_version` (`unknown_schema_version`) |

---

### GET /api/v1/workspaces/:slug/runs/:run_id/postmortem

Retrieve the postmortem report for a run.

**Authentication:** API Key, or PAT with `audit:read` scope.

**Response:** `200 OK` with full postmortem record including `run_id`,
`workspace`, `schema_version`, `run_status`, `started_at`, `completed_at`,
`task_summary`, `cost_summary`, `blocked_tasks`, `session_history`, and
`ingested_at`.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 404 | No postmortem exists for this `run_id` (`postmortem_not_found`) |

---

## Audit Query Endpoints

The audit subsystem provides a unified query API that merges hub-internal and
agent-submitted audit events, a conversation transcript reconstruction
endpoint, and a real-time SSE streaming endpoint.

All audit query endpoints require the `audit:read` permission scope. Admin
tokens and API keys have implicit access; PATs require an explicit
`audit:read` grant.

### GET /api/v1/audit

Unified audit event query that merges events from `hub_audit_events` and
`agent_audit_events` into a single chronological result set using UNION ALL.

**Authentication:** API Key, or PAT with `audit:read` scope.

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `source` | string | (all) | Filter by event source: `"hub"` or `"agent"` |
| `workspace` | string | (all) | Filter by workspace slug |
| `event_type` | string | (all) | Filter by exact event type |
| `event_type_prefix` | string | (all) | Filter by event type prefix (e.g., `"hub.workspace"` matches all workspace events) |
| `severity` | string | (all) | Filter by severity level |
| `actor_id` | string | (all) | Filter by actor ID (hub events only; excludes agent events) |
| `actor_type` | string | (all) | Filter by actor type (hub events only; excludes agent events) |
| `resource_type` | string | (all) | Filter by resource type (hub events only; excludes agent events) |
| `action` | string | (all) | Filter by action (hub events only; excludes agent events) |
| `run_id` | string | (all) | Filter by agent run ID (agent events only; excludes hub events) |
| `since` | string (RFC 3339) | (none) | Inclusive lower bound on event timestamp |
| `until` | string (RFC 3339) | (none) | Exclusive upper bound on event timestamp |
| `limit` | integer | 100 | Maximum number of events to return (clamped to 1000) |
| `cursor` | string | (none) | Opaque pagination cursor from a previous response |

Non-admin callers only receive events of workspaces they own; hub events
that carry no workspace (user, org, and credential changes) are returned to
admin tokens only. Timestamps of both sources are compared as instants, so
hub events (recorded with microsecond precision) and agent events interleave
correctly; `timestamp` is rendered as RFC 3339 UTC with fractional seconds
where present.

**Response:** HTTP 200 OK

```json
{
  "events": [
    {
      "id": "uuid-string",
      "event_type": "hub.workspace.create",
      "source": "hub",
      "timestamp": "2026-09-01T12:00:00Z",
      "severity": "info",
      "workspace": "my-workspace",
      "actor_id": "user-uuid",
      "actor_type": "api_key",
      "resource_type": "workspace",
      "resource_id": "my-workspace",
      "action": "create",
      "run_id": null,
      "node_id": null,
      "session_id": null,
      "archetype": null
    }
  ],
  "next_cursor": "base64-encoded-cursor-or-null",
  "has_more": false
}
```

| Field | Type | Description |
|-------|------|-------------|
| `events` | array | Unified audit events ordered by timestamp descending |
| `events[].id` | string | Unique event identifier |
| `events[].event_type` | string | Dot-notation event type (e.g., `hub.workspace.create`) |
| `events[].source` | string | Event source: `"hub"` or `"agent"` |
| `events[].timestamp` | string (RFC 3339) | Event timestamp |
| `events[].severity` | string | Severity level |
| `events[].workspace` | string | Workspace slug |
| `events[].actor_id` | string or null | Actor who performed the action (hub events only) |
| `events[].actor_type` | string or null | Credential type of the actor (hub events only) |
| `events[].resource_type` | string or null | Category of affected resource (hub events only) |
| `events[].resource_id` | string or null | Identifier of affected resource (hub events only) |
| `events[].action` | string or null | Action performed (hub events only) |
| `events[].run_id` | string or null | Agent run identifier (agent events only) |
| `events[].node_id` | string or null | Agent node identifier (agent events only) |
| `events[].session_id` | string or null | Agent session identifier (agent events only) |
| `events[].archetype` | string or null | Agent archetype (agent events only) |
| `next_cursor` | string or null | Opaque cursor for fetching the next page |
| `has_more` | boolean | Whether more results exist beyond this page |

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Invalid cursor, invalid since/until timestamp, or since >= until |
| 401 | Unauthenticated request |
| 403 | PAT lacks `audit:read` scope |
| 500 | Internal server error |

---

### GET /api/v1/workspaces/:slug/runs/:run_id/transcript

Reconstruct a conversation transcript from agent trace data for a specific
run and node. Maps agent trace event types to conversation roles.

**Authentication:** API Key, or PAT with `audit:read` scope.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug |
| `:run_id` | The agent run identifier |

**Query Parameters:**

| Parameter | Required | Type | Description |
|-----------|----------|------|-------------|
| `node_id` | yes | string | The node identifier within the run |

**Response:** HTTP 200 OK

```json
{
  "run_id": "run-uuid",
  "node_id": "node-uuid",
  "messages": [
    {
      "role": "system",
      "content": "System initialization message",
      "tool_name": null,
      "timestamp": "2026-09-01T12:00:00Z"
    },
    {
      "role": "assistant",
      "content": "Assistant response",
      "tool_name": null,
      "timestamp": "2026-09-01T12:00:01Z"
    },
    {
      "role": "tool_use",
      "content": "Tool output",
      "tool_name": "bash",
      "timestamp": "2026-09-01T12:00:02Z"
    }
  ]
}
```

**Role Mapping:**

| Trace Event Type | Transcript Role |
|-----------------|----------------|
| `session.init` | `system` |
| `assistant.message` | `assistant` |
| `tool.use` | `tool_use` |
| `tool.error` | `tool_error` |

Unrecognized trace event types are silently skipped.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing required `node_id` query parameter |
| 401 | Unauthenticated request |
| 403 | PAT lacks `audit:read` scope |
| 500 | Internal server error |

---

### GET /api/v1/events

Server-Sent Events (SSE) streaming endpoint delivering real-time audit events
to connected clients. Uses the `text/event-stream` content type.

**Authentication:** API Key, or PAT with `audit:read` scope.

**Query Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `workspace` | string | Filter events by workspace slug |
| `run_id` | string | Accepted for forward compatibility but currently ignored: streamed hub events carry no run id |
| `category` | string | Filter events by category: `hub` or `agent` |

Non-admin callers only receive events of workspaces they own; a `workspace`
filter naming another user's workspace answers 404.

**Response:** HTTP 200 OK with `Content-Type: text/event-stream`

The endpoint streams SSE frames in the following formats:

**Heartbeat frame** (sent every 30 seconds):
```
event: heartbeat
data: {"timestamp":"2026-09-01T12:00:00Z"}
```

**Audit event frame:**
```
event: audit_event
data: {"id":"uuid","event_type":"hub.workspace.create","timestamp":"...","workspace":"..."}
```

**Connection Limits:**

The maximum number of concurrent SSE connections is controlled by the
`AF_SSE_MAX_CONNECTIONS` environment variable (default: 100). When the limit
is reached, new connections receive HTTP 503 with an error message.

**Connection Lifecycle:**

- An initial heartbeat frame is sent immediately to confirm the connection.
- Heartbeat frames are sent every 30 seconds to prevent stale connection
  detection.
- Connections with no reads for 60 seconds are automatically closed.
- Per-client event buffers hold up to 256 events; when full, the oldest event
  is drained before inserting a new one.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | PAT lacks `audit:read` scope |
| 503 | Maximum SSE connection limit reached |
