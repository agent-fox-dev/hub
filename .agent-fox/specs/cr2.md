# Rename "Workspace" to "Team" and Introduce True Workspaces

## Problem

The current implementation uses "workspace" to mean a multi-tenant
organizational boundary (comparable to a GitHub Organization), but the
architecture docs define "workspace" as a task-scoped execution context tied
to a git repo. This naming collision will block the coordination layer: when
task-scoped workspaces are built, there will be two unrelated concepts sharing
the same name across the codebase, database schema, API surface, CLI, and
documentation.

## Changes

### 1. Rename the current "workspace" to "team"

Every occurrence of the current organizational-boundary concept must be renamed
from "workspace" to "team" across:

- **Database schema**: table `workspaces` → `teams`, `workspace_members` →
  `team_members`, all FK column names (`workspace_id` → `team_id`).
- **Go types**: `Workspace` struct → `Team`, `WorkspaceMember` →
  `TeamMember`, all store methods (`CreateWorkspace` → `CreateTeam`, etc.).
- **REST API**: `/api/v1/workspaces` → `/api/v1/teams`,
  `/api/v1/workspaces/:id/members` → `/api/v1/teams/:id/members`. API key
  responses change `workspace_id` to `team_id`.
- **CLI**: `afc keys create --workspace` → `afc keys create --team`. The
  config file `[keys.<workspace_slug>]` sections remain keyed by slug but
  the conceptual label changes (this is cosmetic — the TOML keys are
  user-chosen slugs, not the word "workspace").
- **Documentation**: `docs/api.md`, `docs/cli.md`, `docs/configuration.md`,
  `docs/architecture.md`, `README.md`.
- **Tests**: all test files referencing workspace types, endpoints, or flags.

This is a mechanical rename. No behavioral change. The "team" entity retains
its existing schema fields (`id`, `name`, `slug`, `url`, `status`,
`created_at`, `created_by`), lifecycle (active → archived → deleted),
RBAC model (admin/editor/reader membership), and API key scoping.

**Note:** This change request does not retroactively modify previous spec
packages (01–04). Those specs are historical records of what was built. The
rename and all code changes described here are implemented as new work in a
future spec derived from this PRD.

### 2. Introduce the "workspace" concept aligned with architecture

A workspace is the context in which work is done: implementing a spec package,
fixing a GitHub issue, or interactive agent work. Each workspace maps to one
git repository. Work inside a workspace is started in isolated sandbox
containers (OpenShell).

#### Workspace entity (minimal v1)

| Field | Type | Description |
|-------|------|-------------|
| `id` | UUID | Primary key. |
| `slug` | string | Unique human-readable identifier (e.g. `my-api`). Same format rules as team slugs. |
| `git_url` | string | Git remote URL for the repository (e.g. `https://github.com/org/repo.git`). |
| `owner_id` | FK → users | The user who created the workspace. |
| `team_id` | FK → teams | The team this workspace belongs to. |
| `branch` | string | Git branch name or revision to track. Nullable; null means the repo's default branch. |
| `status` | string | `active` or `archived`. Default `active`. |
| `created_at` | timestamp | Auto-set on creation. |

**Uniqueness constraint:** `(slug)` is globally unique. The same `git_url` may
appear in multiple workspaces with different slugs (e.g. one workspace per
feature branch, or one per developer).

#### New CLI command: `afc workspace create`

```
afc workspace create --git-url <url> --slug <slug> [--branch <branch-or-rev>] [--team <team-slug>]
```

| Flag | Required | Description |
|------|----------|-------------|
| `--git-url` | yes | Git remote URL for the repository. |
| `--slug` | yes | Human-readable workspace slug. Must be unique. |
| `--branch` | no | Git branch name or revision to track (e.g. `main`, `feature/dark-mode`, a commit SHA). Defaults to the repository's default branch if omitted. |
| `--team` | no | Team slug to associate the workspace with. If omitted, the workspace is personal (team_id is null). |

The authenticated user becomes the owner (`owner_id`).

On success, prints the created workspace object as JSON to stdout.

#### New REST endpoint

`POST /api/v1/workspaces` — create a workspace.

Request body:

```json
{
  "git_url": "https://github.com/org/repo.git",
  "slug": "my-api",
  "branch": "main",
  "team_id": "optional-team-uuid"
}
```

`branch` is optional; omit or set to `null` to use the repo's default branch.

Response: HTTP 201 with the workspace object.

Auth: any authenticated user can create a workspace. If `team_id` is provided,
the user must be a member of that team.

#### Database migration

Add a `workspaces` table (now free since the old one was renamed to `teams`)
with the schema above. FK to `users` for `owner_id`, FK to `teams` for
`team_id` (nullable).

### 3. Update architecture documents

The documents in `architecture/` must be updated to reflect the rename and
the refined workspace definition:

- **Rename references:** any use of the term "workspace" that actually refers
  to the organizational/team concept must be updated to "team". Review all
  files in `architecture/` and `docs/` for this.
- **Workspace definition:** update the workspace description in the
  architecture docs to reflect the v1 entity introduced here — a workspace
  maps to a git repo (+ optional branch), is owned by a user, and optionally
  belongs to a team. Sandbox/OpenShell provisioning and the full coordination
  layer remain as future work described in the architecture but not yet
  implemented.
- **Schema alignment:** update the operational store schema in
  `architecture/services-architecture.md` (or equivalent) to match the new
  `teams` and `workspaces` table definitions introduced by this change.

### 4. Scope and non-goals

This change request covers:

- The mechanical rename of the current "workspace" concept to "team".
- The introduction of the new workspace entity, table, API endpoint, and CLI
  command with the minimal schema above.
- Updating the architecture documents in `architecture/` and `docs/` to
  reflect the rename and refined workspace definition.

Previous spec packages (01–04) are not modified. They are historical records.
All changes are implemented as new work in a future spec.

It does NOT cover:

- Sandbox/OpenShell container provisioning.
- Spec package storage or lifecycle within a workspace.
- Agent orchestration, runs, or activity logs.
- Campaign management.
- Git branch management or cloning.
- Workspace lifecycle beyond create (no archive/delete/list yet).

These will be addressed in follow-up specs as the coordination layer is built.
