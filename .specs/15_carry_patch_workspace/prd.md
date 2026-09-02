---
spec_id: '15'
spec_name: carry_patch_workspace
title: Carry Patch Workspace
status: draft
created_at: '2026-08-16T15:54:27.201132+00:00'
updated_at: '2026-08-16T15:54:27.201132+00:00'
owner: ''
source: docs/prd/proposals/prd_carry_patch_workflow.md
schema_version: 1
---
# Carry-Patch Workspace and Patch List

## Source

docs/prd/proposals/prd_carry_patch_workflow.md (split 2 of 3)

## Intent

When the hub manages a workspace that is a fork of an upstream repository,
operators need to carry patches -- feature branches with pending upstream PRs --
and maintain an integration branch that combines the upstream base with all
carried patches. Today, the hub treats every workspace as a single-remote clone
with no concept of "upstream vs. fork" or "integration branch assembled from a
patch stack."

This spec adds the workspace-level foundations for the carry-patch workflow: a
new workspace mode, dual-remote clone setup, upstream credential management,
and a declarative ordered patch list per workspace. Together these form the
data model that the carry_patch_operations spec builds upon.

## Goals

- Allow workspaces to operate in a carry-patch mode with a declared fork URL
  (origin) and upstream URL, each with independent credentials.
- Extend workspace creation, schema, and response to include `workspace_mode`,
  `upstream_url`, and `integration_branch` fields.
- Configure carry-patch workspaces with a second remote (`upstream`) and
  `rerere.enabled=true` at clone time.
- Provide a declarative, ordered patch list per workspace that controls which
  branches are included in the integration branch and in what order.
- Expose patch list management via REST API and CLI.

## Non-Goals

- **Integration branch rebuild.** The rebuild algorithm, rerere replay during
  rebuild, and rebuild job queue integration are covered by the
  carry_patch_operations spec.
- **Carry-patch sync extensions.** Upstream merge detection and auto-rebuild
  after sync are covered by the carry_patch_operations spec.
- **Status dashboard.** The aggregated patch-status view is covered by the
  carry_patch_operations spec.
- **Periodic scheduling or webhook triggers.**

## Functional Requirements

### Workspace Mode and Schema Extension

A workspace can operate in one of two modes:

- `standard` (default): existing single-remote behavior. No upstream URL, no
  patch list, no rebuild operations. All existing workspace behavior is
  unchanged.
- `carry_patch`: dual-remote workspace with an upstream URL, a patch list, and
  rebuild operations.

#### Schema changes

Three new columns are added to the `workspaces` table via `ALTER TABLE`
(idempotent, same pattern as sync field migration):

| Column | Type | Default | Description |
|--------|------|---------|-------------|
| `workspace_mode` | TEXT NOT NULL | `'standard'` | Workspace mode |
| `upstream_url` | TEXT | NULL | Upstream repository URL |
| `integration_branch` | TEXT | NULL | Name of the mechanically rebuilt branch |

The `CREATE TABLE` DDL is also updated to include these columns for fresh
databases.

#### Creation validation

When creating a workspace in `carry_patch` mode:

- `upstream_url` is required and must pass the same URL validation as `git_url`
  (HTTPS or SSH format, non-empty host and path). HTTP 400 if missing or
  invalid.
- `git_url` represents the fork (origin remote).
- `upstream_url` represents the upstream repository.
- The existing `branch` field specifies which upstream branch to track. If
  null, the remote's default branch is used.
- `integration_branch` specifies the name of the mechanically rebuilt branch.
  Defaults to `deploy` if not provided.
- `workspace_mode`, `upstream_url`, and `integration_branch` are immutable
  after creation. PATCH requests that attempt to change these return HTTP 400.

When creating a `standard` workspace:

- Setting `upstream_url` or `integration_branch` returns HTTP 400.
- `workspace_mode` defaults to `standard` if omitted.

#### Response schema extension

The workspace response gains three new fields:

```json
{
  "workspace_mode": "standard | carry_patch",
  "upstream_url": "string | null",
  "integration_branch": "string | null"
}
```

For `standard` workspaces, `upstream_url` and `integration_branch` are null.

### Upstream Credentials

Carry-patch workspaces may need separate credentials for the upstream remote.

- Upstream credentials are stored as workspace secrets with the `UPSTREAM_`
  prefix: `UPSTREAM_GIT_PAT` or `UPSTREAM_GIT_USERNAME` /
  `UPSTREAM_GIT_PASSWORD`.
- A new `resolveUpstreamAuth` function resolves upstream credentials with
  fallback:
  1. Look up `UPSTREAM_GIT_PAT` -> BasicAuth with `x-token-auth` username.
  2. Look up `UPSTREAM_GIT_USERNAME` + `UPSTREAM_GIT_PASSWORD` -> BasicAuth.
  3. Fall back to `resolveCloneAuth` (origin credentials).
  This fallback handles the common case where the upstream uses the same PAT.
- The CLI accepts `--upstream-git-pat` and `--upstream-git-username` /
  `--upstream-git-password` flags on `afc credential set`.
- Upstream credentials follow the same storage, encryption, and access control
  rules as existing workspace credentials (spec 09).

### Carry-Patch Clone

When a `carry_patch` workspace is cloned, the clone process extends the
standard clone with additional setup:

1. The initial clone uses `git_url` (the fork) as origin, consistent with
   standard workspaces.
2. After clone completes, a second remote named `upstream` is added pointing
   to `upstream_url` (using GitRunner's new `RemoteAdd` method from the
   git_runner_extensions spec).
3. The repository is configured with `rerere.enabled=true` and
   `rerere.autoupdate=true` (using GitRunner's new `ConfigSet` method).
4. A local branch named per `integration_branch` (default `deploy`) is created
   at the upstream tracking branch HEAD. The integration branch starts
   identical to upstream and diverges only when the first rebuild applies
   patches.

If any of the post-clone setup steps fail (remote add, config set, branch
creation), the clone is still marked as `ready` since the core clone
succeeded. The failure is logged as a warning. The missing configuration can
be applied by the rebuild handler or manually.

### Patch List Management

Each carry-patch workspace has an ordered list of patches. A patch represents
a branch that should be applied on top of the upstream base when rebuilding
the integration branch.

#### Patches table

A new `patches` table is created:

```sql
CREATE TABLE IF NOT EXISTS patches (
    id              TEXT PRIMARY KEY,
    workspace_slug  TEXT NOT NULL,
    branch_name     TEXT NOT NULL,
    position        INTEGER NOT NULL,
    status          TEXT NOT NULL DEFAULT 'active',
    upstream_pr_url TEXT,
    description     TEXT,
    added_at        TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    UNIQUE(workspace_slug, branch_name),
    UNIQUE(workspace_slug, position)
);
```

#### Patch fields

| Field | Type | Description |
|-------|------|-------------|
| `id` | TEXT (UUID) | Unique patch identifier, generated on creation. |
| `workspace_slug` | TEXT | The workspace this patch belongs to. |
| `branch_name` | TEXT | The git branch containing this patch's commits. |
| `position` | INTEGER | Application order (1-based). Lower applied first. |
| `status` | TEXT | Patch lifecycle state (see below). |
| `upstream_pr_url` | TEXT (nullable) | URL of corresponding upstream PR. |
| `description` | TEXT (nullable) | Human-readable description. |
| `added_at` | TEXT (RFC 3339) | When the patch was added. |
| `updated_at` | TEXT (RFC 3339) | When the patch was last modified. |

#### Patch status lifecycle

| Status | Meaning |
|--------|---------|
| `active` | Included in the next rebuild. |
| `merged_upstream` | Detected as merged into upstream. Excluded from rebuild. Automatically removed after successful rebuild. |
| `conflict` | Most recent rebuild failed on this patch. Still included in rebuild attempts. |
| `disabled` | Operator has manually excluded this patch. |

The transition from `active` to `merged_upstream` is automatic (detected
during carry-patch sync, covered by carry_patch_operations spec). The
transition from `merged_upstream` to deletion is automatic (after successful
rebuild, covered by carry_patch_operations spec). All other transitions are
operator-initiated via the update endpoint.

#### Patch operations

- **Add patch:** Appends to the end by default. Optional `position` inserts at
  that position, shifting existing patches down. If the specified branch does
  not exist in the workspace repository, the add succeeds (validation happens
  at rebuild time; missing branches are skipped during rebuild). Adding a patch
  whose `branch_name` equals the workspace's `integration_branch` is rejected
  with HTTP 400.
- **Remove patch:** Removes a patch and compacts positions (no gaps).
- **Update patch:** Modify position, status, description, or
  `upstream_pr_url`. Changing position shifts other patches accordingly.
- **List patches:** Returns all patches for a workspace in position order.
- **Reorder patches:** Accepts a complete ordered list of patch IDs. Assigns
  new positions based on the provided order. All patch IDs for the workspace
  must be included; partial reorder is rejected with HTTP 400.

#### Constraints

- `(workspace_slug, branch_name)` is unique.
- `(workspace_slug, position)` is unique.
- Patches are only allowed on `carry_patch` workspaces. Attempting to add a
  patch to a `standard` workspace returns HTTP 400.

#### REST API

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/workspaces/:slug/patches` | Add a patch |
| GET | `/api/v1/workspaces/:slug/patches` | List patches |
| PATCH | `/api/v1/workspaces/:slug/patches/:id` | Update a patch |
| DELETE | `/api/v1/workspaces/:slug/patches/:id` | Remove a patch |
| POST | `/api/v1/workspaces/:slug/patches/reorder` | Bulk reorder |

**POST add request body:**

```json
{
  "branch_name": "feature/my-patch",
  "position": 3,
  "upstream_pr_url": "https://github.com/upstream/repo/pull/42",
  "description": "Add support for custom auth headers"
}
```

Only `branch_name` is required.

**POST reorder request body:**

```json
{
  "patch_ids": ["uuid-1", "uuid-3", "uuid-2"]
}
```

**Patch response schema:**

```json
{
  "id": "string (UUID)",
  "workspace_slug": "string",
  "branch_name": "string",
  "position": 1,
  "status": "active | merged_upstream | conflict | disabled",
  "upstream_pr_url": "string | null",
  "description": "string | null",
  "added_at": "string (RFC 3339)",
  "updated_at": "string (RFC 3339)"
}
```

#### CLI commands

```
afc patch add <workspace-slug> --branch <name> [--position <n>] [--upstream-pr <url>] [--description <text>]
afc patch remove <workspace-slug> <patch-id>
afc patch list <workspace-slug>
afc patch reorder <workspace-slug> <patch-id-1> <patch-id-2> ...
afc patch update <workspace-slug> <patch-id> [--position <n>] [--status <status>] [--description <text>] [--upstream-pr <url>]
```

#### Permissions

| Scope | Description |
|-------|-------------|
| `patches:read` | List and view patches |
| `patches:write` | Add, remove, update, and reorder patches |

Admin tokens and API keys have implicit full access. PATs require explicit
scope grants.

### Error Responses

All endpoints use the existing apikit `WriteAPIError` envelope.

| Condition | HTTP Status |
|-----------|-------------|
| Carry-patch operation on a `standard` workspace | 400 |
| Create `carry_patch` workspace without `upstream_url` | 400 |
| Set `upstream_url` on a `standard` workspace | 400 |
| Invalid `upstream_url` format | 400 |
| Set immutable fields (workspace_mode, upstream_url, integration_branch) | 400 |
| Patch `branch_name` already in patch list for this workspace | 409 |
| Patch `branch_name` equals `integration_branch` | 400 |
| Reorder with incomplete or duplicate patch ID list | 400 |
| Workspace not active or clone not ready | 400 |
| Patch not found | 404 |

### Cascade Delete

When a workspace is deleted (via `deleteWorkspace`), all associated patches
must also be deleted. The existing cascade delete transaction in
`store.go:deleteWorkspace` is extended to include
`DELETE FROM patches WHERE workspace_slug = ?`.

## Technical Boundaries

- **Language:** Go (1.26+)
- **Foundation:** `github.com/txsvc/apikit` for server framework, auth, CLI.
- **Database:** SQLite (pure Go, no CGo). Schema changes via `ALTER TABLE`
  and new table creation.
- **Credential storage:** Existing secrets/variables system (spec 07/09).
- **Git operations:** go-git for clone, GitRunner (spec 11, extended by
  git_runner_extensions spec) for remote-add, config-set, branch creation.

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 01_workspaces | all | 1 | Extended with workspace_mode, upstream_url, integration_branch |
| 05_workspace_checkout | all | 2 | Clone extended for dual-remote setup |
| 07_secrets_variables | all | 2 | Upstream credential storage |
| 09_git_credentials | all | 2 | resolveUpstreamAuth alongside resolveCloneAuth |
| 14_git_runner_extensions | 1 | 2 | Uses RemoteAdd, ConfigSet, CreateBranch |

## Design Decisions

1. **Workspace mode as a field, not a separate entity.** A carry-patch
   workspace IS a workspace -- it needs clone, git server, credentials, sync,
   archive, and all existing lifecycle operations. Adding a separate entity
   would duplicate the entire workspace management stack.

2. **`branch` tracks upstream, `integration_branch` is the derived output.**
   The workspace's `branch` field specifies which upstream branch to track.
   The `integration_branch` names the mechanically rebuilt branch. This
   separation is explicit: `branch` is input, `integration_branch` is output.

3. **Immutability of mode, upstream_url, and integration_branch.** These
   fields define the structural identity of a carry-patch workspace. Allowing
   changes would invalidate the patch list, rerere cache, and integration
   branch history. Converting requires creating a new workspace.

4. **Patch branch_name cannot equal integration_branch.** Applying the
   integration branch as a patch onto itself is nonsensical and would cause
   confusing git errors. Rejected at add time with HTTP 400.

5. **Missing branches accepted at add time.** Operators may define the patch
   list before all branches exist (e.g., planning future work). Validation
   happens at rebuild time; missing branches are skipped.

6. **Upstream credentials fall back to origin.** Many forks use the same PAT
   for both. Requiring separate credentials for every carry-patch workspace
   would be unnecessarily cumbersome.

7. **Post-clone setup failures are non-fatal.** The core clone is the critical
   operation. If remote-add or config-set fails, the workspace is still
   usable and the rebuild handler can set up missing configuration.

