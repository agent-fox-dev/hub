---
spec_id: '03'
spec_name: workspace_write_delete
title: Workspace Write Delete
status: draft
created_at: '2026-07-21T07:32:06.998437+00:00'
updated_at: '2026-07-21T07:32:06.998437+00:00'
owner: ''
source: docs/prd1.md
schema_version: 1
---
# Workspace Write and Delete Permission Scopes

## Intent

af-hub currently supports two workspace permission scopes for PATs: `workspaces:read` (list/view) and `workspaces:create` (create new workspaces). Lifecycle operations (archive, reactivate, delete) and property updates are restricted to the workspace owner's API key or admin tokens — there is no way to delegate these capabilities via PATs.

This adds two new permission scopes — `workspaces:write` and `workspaces:delete` — to enable fine-grained delegation of workspace mutation and deletion through PATs. It also introduces a workspace update endpoint and two new workspace metadata fields (`display_name`, `description`) so that `workspaces:write` has meaningful property-update operations to gate.

Additionally, this creates the missing API and CLI reference documentation (`docs/api.md`, `docs/cli.md`) referenced in `README.md`.

## Goals

- Register `workspaces:write` and `workspaces:delete` permission scopes with apikit's PAT permission registry.
- Allow PATs with `workspaces:write` to archive, reactivate, and update workspace properties for workspaces owned by the PAT's user.
- Allow PATs with `workspaces:delete` to delete archived workspaces owned by the PAT's user.
- Add `display_name` and `description` fields to the workspace schema.
- Introduce a workspace update API endpoint (`PATCH /api/v1/workspaces/:slug`) for changing mutable workspace properties.
- Add a corresponding `afc workspace update` CLI command.
- Update the access control matrix to reflect the new scopes while keeping existing scopes (`workspaces:read`, `workspaces:create`) unchanged.
- Create `docs/api.md` and `docs/cli.md` reference documentation covering all endpoints and commands (existing and new).

## Non-goals

- **Changing the behavior of existing scopes.** `workspaces:read` and `workspaces:create` continue to work exactly as they do today.
- **Slug changes via update.** The slug is the primary key and globally unique identifier — it is immutable after creation.
- **Modifying `git_url` or `branch` via update.** These are identity fields that define the workspace's git context. They are immutable after creation.
- **Ownership transfer.** Changing `owner_id` is not supported via the update endpoint.
- **Status changes via the update endpoint.** Lifecycle transitions (archive/reactivate/delete) use their existing dedicated endpoints, not the PATCH body.
- **Bulk updates or batch operations.** Update operates on a single workspace by slug.

## Functional Requirements

### New workspace fields

Two optional metadata fields are added to the workspace schema:

- `display_name` (optional at creation) — A human-readable name for the workspace. Free-form text up to 128 characters. No uniqueness constraint. When omitted at creation or cleared (set to `null` or empty string `""`), the server generates a default from the slug value. Always has a value in responses — never null.
- `description` (optional at creation) — A longer description of the workspace's purpose. Free-form text up to 1024 characters. When omitted at creation or cleared, defaults to an empty string. Always has a value in responses — never null.

Both fields are settable at creation time (optional) and updatable via the PATCH endpoint. Both appear in the workspace response object. The server always ensures a value: sending `null` or `""` triggers the server-side default, not a null in the database.

**Updated workspace table schema:**

```sql
CREATE TABLE IF NOT EXISTS workspaces (
    slug          TEXT PRIMARY KEY,
    git_url       TEXT NOT NULL,
    branch        TEXT,
    display_name  TEXT NOT NULL DEFAULT '',
    description   TEXT NOT NULL DEFAULT '',
    owner_id      TEXT NOT NULL,
    org_id        TEXT,
    status        TEXT NOT NULL DEFAULT 'active',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);
```

### New permission scopes

Hub registers two additional permission scopes with apikit's PAT permission registry via `apikit.Permission` in `MountHandlers`:

| Permission | Description |
|------------|-------------|
| `workspaces:write` | Archive, reactivate, and update properties of workspaces the PAT owner has access to |
| `workspaces:delete` | Delete archived workspaces the PAT owner has access to |

These are additive to the existing `workspaces:read` and `workspaces:create` scopes, which remain unchanged.

### Updated access control matrix

| Endpoint | Admin | Owner (API key) | PAT `workspaces:read` | PAT `workspaces:create` | PAT `workspaces:write` | PAT `workspaces:delete` |
|----------|-------|-----------------|-----------------------|------------------------|------------------------|------------------------|
| Create workspace | no\* | yes | no | yes | no | no |
| List workspaces | yes (all) | yes (own) | yes (own) | yes (own) | yes (own) | no |
| Get workspace | yes | yes | yes (own) | yes (own) | yes (own) | no |
| Update workspace | yes | yes | no | no | yes (own) | no |
| Archive workspace | yes | yes | no | no | yes (own) | no |
| Reactivate workspace | yes | yes | no | no | yes (own) | no |
| Delete workspace | yes | yes | no | no | no | yes (own) |

\*Admin tokens cannot create workspaces — a real user must be the owner.

**Scope behavior:**
- `workspaces:write` implies read access to the PAT owner's workspaces (a tool that modifies workspaces needs to verify its changes).
- `workspaces:delete` does **not** imply read access. The caller must know the workspace slug already or also hold `workspaces:read`.
- `workspaces:create` continues to imply read access (unchanged from current behavior).

### Workspace update endpoint

- `PATCH /api/v1/workspaces/:slug` — Update mutable workspace properties. Requires workspace ownership + user API key, admin token, or PAT with `workspaces:write`.
- Accepts a JSON body with optional fields: `display_name`, `description`, `org_id`. Only provided fields are updated; omitted fields are unchanged.
- Setting `display_name` or `description` to `null` or `""` clears it to the server-side default (slug value for display_name, empty string for description).
- Setting `org_id` to `null` removes the organization association.
- When updating `org_id` to a non-null value, the workspace owner must be a member of that organization (HTTP 403 otherwise).
- The update endpoint only operates on active workspaces. Attempting to update an archived workspace returns HTTP 400. The workspace must be reactivated first.
- An empty request body (no fields to update) returns HTTP 400.
- `display_name` must be 128 characters or fewer when provided (non-null/non-empty). `description` must be 1024 characters or fewer when provided (non-null/non-empty).
- Returns HTTP 200 with the updated workspace object.
- `updated_at` is set to the current time on any successful update.

### Updated response schema

```json
{
  "slug": "string",
  "git_url": "string",
  "branch": "string | null",
  "display_name": "string",
  "description": "string",
  "owner_id": "string (UUID)",
  "org_id": "string (UUID) | null",
  "status": "active | archived",
  "created_at": "string (RFC 3339)",
  "updated_at": "string (RFC 3339)"
}
```

`display_name` and `description` are always strings (never null) in responses.

The new fields appear in all workspace response objects (create, list, get, update, archive, reactivate).

### Updated create endpoint

`POST /api/v1/workspaces` accepts two additional optional fields in the request body: `display_name` and `description`. Both default to server-side values if omitted (slug for display_name, empty string for description).

### CLI changes

**New command:**
- `afc workspace update <slug> [--display-name <name>] [--description <text>] [--org <org-slug>] [--clear-display-name] [--clear-description] [--clear-org]` — Update workspace properties. At least one flag must be provided; otherwise the command prints a usage hint to stderr and exits 1. The `--clear-*` flags set the corresponding field to null. On success, prints the updated workspace object as JSON.

**Updated command:**
- `afc workspace create` gains optional `--display-name <name>` and `--description <text>` flags.

### Error responses

Additional error conditions for the update endpoint, using apikit's standard JSON envelope:

| Condition | HTTP Status |
|-----------|-------------|
| Empty update body (no fields to change) | 400 |
| Workspace is archived (must reactivate first) | 400 |
| `display_name` exceeds 128 characters | 400 |
| `description` exceeds 1024 characters | 400 |
| User not a member of the new organization | 403 |
| Workspace not found | 404 |
| Workspace exists but requester lacks access | 404 (anti-enumeration) |

### Documentation

- `docs/api.md` — REST API reference covering all endpoints (existing and new): authentication, request/response schemas, error codes, permission requirements. Covers the full workspace API surface, not just the additions in this PRD.
- `docs/cli.md` — CLI reference covering all `afc` commands (existing and new): workspace commands, apikit-provided commands (login, user, keys, tokens, orgs, admin), flags, exit codes. Covers the full CLI surface, not just the additions in this PRD.

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 01_workspaces | 8 | 1 | Requires fully implemented workspace infrastructure (table, handlers, routes, permissions, CLI, auth) |

## Technical Boundaries

- **Language:** Go (1.26+)
- **Foundation:** `github.com/txsvc/apikit` — permission registration via `apikit.Permission` in `MountHandlers` (same mechanism used for existing scopes).
- **Schema migration:** Pre-production; the new columns are added to the DDL directly. No migration framework.

## Design Decisions

1. **Archived workspaces are immutable.** The PATCH endpoint returns HTTP 400 for archived workspaces, consistent with the original spec's "Read-only. All state preserved." definition. Users must reactivate a workspace before updating its properties.
2. **Empty strings normalize to server defaults.** Sending `""` or `null` for `display_name` or `description` triggers a server-side default (slug for display_name, empty string for description) rather than storing null. This ensures response objects always have string values for these fields, simplifying client parsing.
3. **display_name defaults to slug.** When not provided at creation or cleared, the server derives display_name from the slug. This provides a human-readable label without requiring explicit input.
4. **Schema DDL update only.** New columns are added to the `CREATE TABLE IF NOT EXISTS` DDL. No ALTER TABLE migration logic. Existing pre-production databases may need to be recreated.
5. **`workspaces:delete` is a separate scope from `workspaces:write`.** Deletion is irreversible and physically removes the database row. Separating it from write prevents accidental delegation of destructive capability.
6. **`workspaces:delete` does not imply read.** A PAT with only `workspaces:delete` must know the slug already. This follows least-privilege: delete delegation does not grant browsing capability.

