---
spec_id: '07'
spec_name: workspace_entity
title: Workspace Entity
status: draft
created_at: '2026-07-09T13:58:20.824690+00:00'
updated_at: '2026-07-09T13:58:20.824690+00:00'
owner: ''
source: '.agent-fox/specs/cr2.md'
schema_version: 1
---

# Workspace Entity

## Intent

Introduce the workspace concept aligned with the system architecture — a
workspace maps to a git repository and is the context in which work is done.

## Background

After the `team_rename` spec frees the "workspace" name from the organizational
boundary concept, this spec introduces the true workspace entity as defined in
the architecture docs. A workspace is the context in which work is done:
implementing a spec package, fixing a GitHub issue, or interactive agent work.
Each workspace maps to one git repository (and optionally a branch). Work
inside a workspace will eventually be started in isolated sandbox containers
(OpenShell), but that is out of scope here — this spec establishes the entity,
storage, API, CLI, and architecture doc alignment.

## Problem

The system architecture defines workspaces as a core concept, but the codebase
has no implementation of the architecture's workspace entity. Only the
organizational container (now renamed to "team") exists. Before the
coordination layer can be built, the foundational workspace entity must exist.

## Solution

Add a minimal v1 workspace entity with database table, store CRUD, REST
endpoint for creation, CLI command, and architecture document updates.

Previous spec packages (01–04) are not modified. They are historical records.
All changes are implemented as new work in this spec.

## Goals

- **Workspace entity exists**: a `workspaces` table with slug, git_url,
  branch, owner, team association, and status.
- **Create via API and CLI**: `POST /api/v1/workspaces` and
  `afc workspace create` allow users to register workspaces.
- **Architecture alignment**: the `architecture/` docs reflect the team/workspace
  split and the v1 workspace schema.

## Non-Goals

- Sandbox/OpenShell container provisioning.
- Spec package storage or lifecycle within a workspace.
- Agent orchestration, runs, or activity logs.
- Campaign management.
- Git branch management, cloning, or checkout.
- Workspace lifecycle beyond create (no archive/delete/list yet).

## Workspace Entity

### Schema

| Field | Type | Constraints | Description |
|-------|------|-------------|-------------|
| `id` | TEXT (UUID) | PRIMARY KEY | Auto-generated UUID. |
| `slug` | TEXT | UNIQUE, NOT NULL | Human-readable identifier. Same format as team slugs: lowercase alphanumeric + hyphens, 3–64 chars, starts with a letter. |
| `git_url` | TEXT | NOT NULL | Git remote URL. Accepts HTTPS (`https://...`) and SSH (`git@host:path`) formats. Not validated for reachability. |
| `branch` | TEXT | nullable | Git branch name or revision. NULL means the repo's default branch. |
| `owner_id` | TEXT | NOT NULL, FK → users(id) | The user who created the workspace. |
| `team_id` | TEXT | nullable, FK → teams(id) | Optional team association. NULL means personal workspace. |
| `status` | TEXT | NOT NULL, DEFAULT 'active' | `active` or `archived`. |
| `created_at` | DATETIME | NOT NULL, DEFAULT CURRENT_TIMESTAMP | Auto-set. |

The same `git_url` may appear in multiple workspaces with different slugs.

### REST Endpoint

`POST /api/v1/workspaces` — create a workspace.

**Auth:** API key authentication required. Admin tokens are not allowed (a
real user must be the owner). If `team_id` is provided, the authenticated
user must be a member of that team (any role: reader, editor, or admin).

**Request body:**

```json
{
  "slug": "my-api",
  "git_url": "https://github.com/org/repo.git",
  "branch": "main",
  "team_id": "optional-team-uuid"
}
```

`branch` is optional; omit or set to `null` to use the repo's default branch.
`team_id` is optional; omit for a personal workspace.

**Success response:** HTTP 201 with the workspace object:

```json
{
  "id": "uuid",
  "slug": "my-api",
  "git_url": "https://github.com/org/repo.git",
  "branch": "main",
  "owner_id": "user-uuid",
  "team_id": "team-uuid-or-null",
  "status": "active",
  "created_at": "2026-07-09T12:00:00Z"
}
```

**Error responses:**

| Condition | Status | Message |
|-----------|--------|---------|
| Missing `slug` or `git_url` | 400 | `missing required fields` |
| Invalid slug format | 400 | `invalid slug format` |
| Invalid `git_url` format | 400 | `invalid git_url format` |
| Slug already exists | 409 | `workspace slug already exists` |
| `team_id` provided but team not found | 404 | `team not found` |
| User not a member of the specified team | 403 | `not a member of this team` |
| Admin token used (no real user) | 403 | `workspace creation requires user authentication` |

All errors use the standard error envelope format:
`{"error": {"code": "<status>", "message": "<message>"}}`.

### CLI Command

```
afc workspace create --git-url <url> --slug <slug> [--branch <ref>] [--team <team-slug>]
```

| Flag | Required | Description |
|------|----------|-------------|
| `--git-url` | yes | Git remote URL. |
| `--slug` | yes | Workspace slug. Must be unique. |
| `--branch` | no | Git branch or revision. Omit for repo default. |
| `--team` | no | Team slug. If provided, resolves to team UUID before API call. |

The `--team` flag accepts a team slug. The CLI resolves it to a team UUID by
calling `GET /api/v1/teams?slug=<value>` (or a suitable lookup) before
passing `team_id` in the create request. If the team slug is not found, the
CLI prints an error and exits.

On success, prints the created workspace object as JSON to stdout.

### Architecture Document Updates

The documents in `architecture/` and `docs/` must be updated:

1. **Rename references**: any use of "workspace" that refers to the
   organizational/team concept must be updated to "team".
2. **Workspace definition**: update the workspace description to reflect the
   v1 entity — maps to a git repo (+ optional branch), owned by a user,
   optionally belongs to a team.
3. **Schema alignment**: update the operational store schema in
   `architecture/services-architecture.md` to include both the `teams` table
   (renamed from workspaces) and the new `workspaces` table.

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 06_team_rename | last | 1 | The `workspaces` table name must be freed (renamed to `teams`) before this spec can create the new `workspaces` table. |

## Tech Stack

- Go 1.22+
- SQLite (schema DDL in `internal/db/db.go`)
- Echo HTTP framework
- Cobra CLI framework

## Design Decisions

1. **`git_url` validation**: accepts HTTPS (`https://...`) and SSH
   (`git@host:path`) formats. Local filesystem paths are not supported — this
   is a server-hosted entity. The URL is not validated for reachability at
   creation time (no network call to the git remote). Rationale: validation
   for reachability would add latency and fragility; the URL is stored as
   metadata for later use by the sandbox provisioner.

2. **Duplicate slug → HTTP 409**: when a slug already exists, the endpoint
   returns 409 Conflict with the standard error envelope. Matches the
   project's existing convention for unique constraint violations.

3. **Team membership check**: any role (reader, editor, admin) is sufficient
   to associate a workspace with a team. Rationale: creating a workspace is a
   lightweight metadata operation; the team association just means "this
   workspace's work is related to this team." Access control for workspace
   operations can be refined later.

4. **Admin tokens cannot create workspaces**: workspace creation requires a
   real user as `owner_id`. Admin tokens have synthetic user context (the
   token record ID), which is not a real user. Rationale: workspaces are
   user-owned resources; admin tokens are for system administration.

5. **Team slug resolution in CLI**: the `--team` flag accepts a slug (not a
   UUID) for ergonomics. The CLI resolves it to a UUID before the API call.
   This requires the teams list/lookup endpoint to support slug-based
   filtering.

6. **No list/get/archive/delete yet**: this spec only implements workspace
   creation. CRUD operations beyond create will be added as the coordination
   layer matures. Rationale: the PRD explicitly scopes this to "minimal v1."

