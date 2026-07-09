---
spec_id: '06'
spec_name: team_rename
title: Rename Workspace to Team
status: draft
created_at: '2026-07-09T13:58:16.658705+00:00'
updated_at: '2026-07-09T13:58:16.658705+00:00'
owner: ''
source: '.agent-fox/specs/cr2.md'
schema_version: 1
---

# Rename Workspace to Team

## Intent

Free the "workspace" name for the architecture's task-scoped concept by
renaming the current organizational-boundary entity from "workspace" to "team"
across the entire codebase.

## Background

The current implementation uses "workspace" to mean a multi-tenant
organizational boundary (comparable to a GitHub Organization), but the
architecture docs define "workspace" as a task-scoped execution context tied
to a git repo. This naming collision will block the coordination layer. A
follow-up spec (`workspace_entity`) will introduce the true workspace concept
once this rename clears the name.

## Problem

Two unrelated concepts share the name "workspace" — the organizational
container in the running code and the task-scoped sandbox in the architecture
docs. Before the coordination layer can be built, the name must be freed.

## Solution

Mechanically rename every occurrence of the organizational "workspace" concept
to "team". No behavioral change — only names change. The entity retains its
schema fields, lifecycle, RBAC model, and API key scoping.

Previous spec packages (01–04) are not modified. They are historical records.
All changes are implemented as new work in this spec.

## Goals

- **Consistent terminology**: after this spec, "team" means the organizational
  boundary everywhere — schema, types, API, CLI, docs, tests.
- **Zero behavioral change**: every operation works identically, just under the
  new name.
- **Free the "workspace" name**: the `workspaces` table name, Go types, API
  endpoints, and CLI flags no longer use "workspace" for the org concept.

## Non-Goals

- Introducing the new workspace entity (covered by the `workspace_entity` spec).
- Changing RBAC rules, lifecycle states, or any business logic.
- Modifying previous spec packages (01–04).

## Scope of Rename

### Database schema

Since this project is pre-production with no deployed users, the rename is
implemented as a fresh schema rebuild — drop and recreate with new names.
No data migration step is needed.

| Current | New |
|---------|-----|
| `workspaces` table | `teams` |
| `workspace_members` table | `team_members` |
| `workspace_id` FK columns (in `workspace_members`, `api_keys`) | `team_id` |

### Go types and store methods

| Current | New |
|---------|-----|
| `store.Workspace` struct | `store.Team` |
| `store.WorkspaceMember` struct | `store.TeamMember` |
| `CreateWorkspace`, `GetWorkspaceByID`, etc. | `CreateTeam`, `GetTeamByID`, etc. |
| `WorkspaceID` field on `APIKey` struct | `TeamID` |
| `workspace_handler.go` file | `team_handler.go` |
| `workspace_members.go` file | `team_members.go` |
| `workspaces.go` file | `teams.go` |
| `WorkspaceHandler` struct | `TeamHandler` struct |
| Handler methods (`CreateWorkspace`, `ListWorkspaces`, etc.) | `CreateTeam`, `ListTeams`, etc. |
| Auth context key `ContextKeyWorkspaceID` | `ContextKeyTeamID` |
| Route registrations `/api/v1/workspaces` | `/api/v1/teams` |
| Test files (`workspace_handler_test.go`, `workspace_edge_test.go`) | Renamed to `team_*` |

### REST API

| Current | New |
|---------|-----|
| `POST /api/v1/workspaces` | `POST /api/v1/teams` |
| `GET /api/v1/workspaces` | `GET /api/v1/teams` |
| `GET /api/v1/workspaces/:id` | `GET /api/v1/teams/:id` |
| `PUT /api/v1/workspaces/:id/archive` | `PUT /api/v1/teams/:id/archive` |
| `PUT /api/v1/workspaces/:id/reactivate` | `PUT /api/v1/teams/:id/reactivate` |
| `DELETE /api/v1/workspaces/:id` | `DELETE /api/v1/teams/:id` |
| `POST /api/v1/workspaces/:id/members` | `POST /api/v1/teams/:id/members` |
| `DELETE /api/v1/workspaces/:id/members/:user_id` | `DELETE /api/v1/teams/:id/members/:user_id` |
| JSON field `workspace_id` in API key responses | `team_id` |
| JSON field `workspace_id` in request/response bodies | `team_id` |

### CLI

| Current | New |
|---------|-----|
| `afc keys create --workspace` | `afc keys create --team` |

### Documentation

Update all occurrences in:

- `docs/api.md` — endpoint paths, request/response examples
- `docs/cli.md` — `--workspace` flag → `--team`
- `docs/architecture.md` — organizational references
- `docs/configuration.md` — if referencing workspace concept
- `docs/errata/*.md` — any errata referencing workspace concept
- `README.md` — project structure, quickstart

### Tests

All test files referencing workspace types, endpoints, flags, or URL paths
must be updated. Key files:

- `internal/integration/workspace_handler_test.go` → `team_handler_test.go`
- `internal/integration/workspace_edge_test.go` → `team_edge_test.go`
- `internal/store/workspaces.go` → `teams.go`
- `internal/store/workspace_members.go` → `team_members.go`
- All test helpers and assertions referencing workspace types

## Tech Stack

- Go 1.22+
- SQLite (schema DDL in `internal/db/db.go`)
- Echo HTTP framework
- Cobra CLI framework

## Design Decisions

1. **Fresh schema rebuild, no migration**: Since the project is pre-production
   with no deployed users, the rename is implemented by updating the DDL
   strings in `internal/db/db.go` directly. No ALTER TABLE or data migration
   needed. Rationale: simpler, no migration code to maintain, matches existing
   pattern where schema is defined as CREATE TABLE statements.

2. **Spec 05 (cli_client_config) interaction**: Spec 05 is unimplemented. Its
   `--workspace` flag reference and documentation will naturally use the
   correct `--team` terminology when implemented after this rename. The TOML
   config keys (`[keys.<slug>]`) are user-chosen slugs, not the literal word
   "workspace", so the config file structure is unaffected.

3. **File renames**: Source files like `workspace_handler.go` are renamed to
   `team_handler.go` for consistency. Git tracks these as renames.

