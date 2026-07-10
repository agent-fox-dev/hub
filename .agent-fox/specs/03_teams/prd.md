---
spec_id: '03'
spec_name: teams
title: Teams
status: draft
created_at: '2026-07-10T15:01:11.889182+00:00'
updated_at: '2026-07-10T15:10:21.851506+00:00'
owner: ''
source: docs/01_prd.md
schema_version: 1
---
# Team Management

## Background

Teams are being introduced now as the foundational organizational primitive for af-hub. They provide a lightweight grouping mechanism for users that will later underpin role-based access control (RBAC) and workspace organization. The immediate need is to establish the team entity, its lifecycle, and membership data model before layering permissions on top.

RBAC is deliberately deferred because this incremental approach reduces risk and allows the team schema and API to stabilize before authorization complexity is added. The no-permissions design was chosen so that teams can ship quickly and be validated by admins before the permission model is fully designed. Once both the teams entity and the workspace entity are stable, a future spec will handle the workspace-to-team relationship and RBAC.

This builds on the server foundation (spec 1), which provides the database connection, migration runner, auth middleware, admin middleware, and error handling infrastructure. The team-specific schema (tables and migrations) is owned by this spec.

## Intent

Implement lightweight team management for af-hub. Teams serve as an organizational grouping for users, with no permission implications in this iteration. This provides the foundation for future RBAC work.

This builds on the server foundation (spec 1), which provides auth middleware, admin middleware, database infrastructure (connection and migration runner), and error handling.

## Goals

- Implement team CRUD endpoints (admin only): create, get by ID, list (with archive filtering), archive, reactivate, delete.
- Enforce team lifecycle state machine: active → archived → deleted (terminal).
- Implement team membership endpoints (admin only): add member, list members.
- Validate team slugs (lowercase alphanumeric + hyphens, 3-64 chars, starts with letter, no trailing hyphen; consecutive hyphens are allowed).
- Validate team URLs (must have http/https scheme and host).
- Validate team names (non-empty after trimming, max 255 characters, any Unicode characters allowed).
- Enforce uniqueness on team name and slug among non-deleted teams (HTTP 409 on duplicates), backed by a DB-level partial UNIQUE index.
- Define and own the `teams` and `team_members` database table migrations.

## Non-goals

- Team-based RBAC or permissions (deferred to future iteration).
- Team member removal (deferred).
- Roles within teams (deferred).
- Workspace-to-team association (deferred; a future spec will handle this relationship once both the teams and workspaces entities are stable).
- CLI (`afc`) commands for team management (deferred; teams are managed exclusively via the admin REST API in this iteration).
- Pagination or sorting for list endpoints (deferred; all list endpoints return full result sets, which is acceptable given expected small data volumes in this iteration).
- Rate limiting, request size limits, and abuse-prevention constraints (deferred; delegated to infrastructure-level concerns outside this spec's scope).

## Functional Requirements

### Team CRUD (admin only)

All team endpoints are gated by the admin middleware provided by server_foundation (spec 1). The middleware checks the authenticated user's role and returns HTTP 403 if the user is not an admin. This middleware is applied at the router level for all `/api/v1/teams` routes.

- `POST /api/v1/teams` — Create team. Accepts `name`, `slug`, `url` (optional). Leading and trailing whitespace is trimmed from `name` before validation. Validates slug format and URL format (if provided). Returns HTTP 201 with the created team object. Returns HTTP 409 on duplicate name or slug (uniqueness is checked only among non-deleted teams; deleted team names and slugs are released for reuse; distinct messages distinguish which field caused the conflict: `"team name already exists"` or `"team slug already exists"`). Unrecognized body fields are silently ignored (lenient parsing). Uniqueness is enforced at the database layer via a partial UNIQUE index (scoped to non-deleted teams) in addition to the application-layer check; if a concurrent insert causes a DB unique-constraint violation, the server maps it to HTTP 409.
- `GET /api/v1/teams` — List all active teams. Optional `?include_archived=true` query param to include archived teams. Deleted teams are never listed. Returns all matching records (no pagination), ordered by `created_at` ascending (oldest first). Returns HTTP 200 with an array of team objects.
- `GET /api/v1/teams/:id` — Fetch a single team by ID. Returns HTTP 200 with the team object for both active and archived teams. Returns HTTP 404 only if the team does not exist or has been deleted (deleted teams are treated as non-existent and their IDs are inaccessible across all endpoints). Returns HTTP 400 if the `:id` path parameter is not a valid UUID.
- `POST /api/v1/teams/:id/archive` — Archive an active team. No request body expected or required; any body present is silently ignored; `Content-Type` is not enforced. Returns HTTP 200 with the updated team object on success. Returns HTTP 409 if already archived. Returns HTTP 404 if the team does not exist or has been deleted.
- `POST /api/v1/teams/:id/reactivate` — Reactivate an archived team. No request body expected or required; any body present is silently ignored; `Content-Type` is not enforced. Returns HTTP 200 with the updated team object on success. Returns HTTP 409 if already active. Returns HTTP 404 if the team does not exist or has been deleted.
- `DELETE /api/v1/teams/:id` — Delete a team. The team must be archived first; active teams cannot be deleted directly. Returns HTTP 204 on success. Returns HTTP 409 if the team is active (with message `"team must be archived before deletion"`). Returns HTTP 404 if the team does not exist or has already been deleted (deleted teams are treated as non-existent). Cascades: deletes all memberships atomically within a single DB transaction. Terminal state — deleted teams cannot be recovered.

### Team lifecycle

| State    | Transitions                          |
|----------|--------------------------------------|
| active   | → archived                           |
| archived | → active (reactivate), → deleted     |
| deleted  | terminal (no further transitions)    |

Any lifecycle-mutating operation (archive, reactivate, delete) updates the team's `updated_at` timestamp. Since deletion physically removes the record, `updated_at` is not observable post-deletion.

Deleted teams are treated as non-existent for all purposes: they return HTTP 404 on any lookup or mutation attempt (including archive and reactivate), and their `name` and `slug` values are released — new teams may reuse a name or slug that previously belonged to a deleted team. Uniqueness checks for name and slug are scoped to non-deleted teams only. A deleted team's ID is also effectively inaccessible; no endpoint will return data for a deleted team.

### Transaction requirements

All multi-step write operations must be performed within a single database transaction to ensure data consistency:

- **Create team (uniqueness race):** The application-layer uniqueness check and insert must be performed within a transaction. Additionally, a partial UNIQUE index (`WHERE status != 'deleted'`) on `name` and `slug` is used at the database layer, so any concurrent insert that bypasses the application check will be caught by the DB constraint and mapped to HTTP 409.
- **Delete + cascade:** When a team is deleted, the deletion of the team record and all associated `team_members` rows must occur atomically within a single transaction. If any step fails, the entire operation is rolled back.
- **Membership add (idempotent):** The existence check and insert (or no-op) for membership must be performed within a transaction to prevent race conditions.

Single-row reads and single-step writes (e.g., archive, reactivate) do not require explicit transactions beyond what the database driver provides for individual statements.

### Team membership (admin only)

All team membership endpoints are gated by the same admin middleware as the team CRUD endpoints.

- `POST /api/v1/teams/:id/members` — Add user to team. Accepts `user_id`. Idempotent: adding an existing member is a no-op and returns HTTP 200. In both the no-op and the new-membership case, the response is produced by a fresh JOIN against the `users` table, ensuring the returned `email` and `name` always reflect current user data and `joined_at` always reflects the original membership creation timestamp. Returns HTTP 404 if the user does not exist or the team does not exist or has been deleted. Returns HTTP 409 if the team is archived. Returns HTTP 400 if `:id` or `user_id` is not a valid UUID.
- `GET /api/v1/teams/:id/members` — List all members of a team. Returns HTTP 200 with an array of member objects for both active and archived teams (membership is fully readable regardless of team status), ordered by `joined_at` (`team_members.created_at`) ascending (oldest member first). Returns HTTP 404 only if the team has been deleted or does not exist. Returns HTTP 400 if `:id` is not a valid UUID. Returns all records (no pagination).

### Response body shapes

All successful responses return JSON with `Content-Type: application/json`. Requests that include a body (POST endpoints that accept one) must use `Content-Type: application/json`; if the body is malformed JSON or the body cannot be parsed, the server returns HTTP 400 with the standard error envelope (e.g., `{"code": 400, "message": "invalid request body"}`). Archive and reactivate endpoints do not require or inspect any request body or `Content-Type` header. Error responses reuse the error envelope from server_foundation (see Error response format section).

#### Team object

Returned by create, get by ID, list teams, archive, and reactivate endpoints.

```json
{
  "id": "uuid-string",
  "name": "My Team",
  "slug": "my-team",
  "url": "https://example.com",
  "status": "active",
  "created_at": "2026-07-10T15:01:11.889182Z",
  "updated_at": "2026-07-10T15:01:11.889182Z"
}
```

| Field        | Type            | Notes                                          |
|--------------|-----------------|------------------------------------------------|
| id           | string (UUID)   | Team identifier                                |
| name         | string          | Unique team name (among non-deleted teams); stored after trimming leading/trailing whitespace |
| slug         | string          | Unique, validated slug (among non-deleted teams)|
| url          | string or null  | Optional; null if not set                      |
| status       | string          | One of: `active`, `archived`                   |
| created_at   | string (ISO8601)| UTC timestamp; formatted as RFC3339 with sub-second precision (e.g., `2026-07-10T15:01:11.889182Z`) |
| updated_at   | string (ISO8601)| UTC timestamp; same format as `created_at`; updated on any state change |

> **Note on `status` in responses:** Deleted teams are never returned by any endpoint, so the `status` field in a team object will only ever be `active` or `archived`.

> **Timestamp format:** All timestamps are stored and returned in UTC. The format is RFC3339 with microsecond precision, e.g., `2026-07-10T15:01:11.889182Z`. The trailing `Z` denotes UTC.

List response for `GET /api/v1/teams` is an array of team objects ordered by `created_at` ascending:

```json
[
  { "id": "...", "name": "...", "slug": "...", "url": null, "status": "active", "created_at": "...", "updated_at": "..." }
]
```

#### Member object

Returned by add member and list members endpoints. The `user_id` is the user's primary key. The `email` and `name` fields are sourced from the `users` table via a fresh JOIN on every response (including the idempotent no-op case), ensuring current user data is always returned. The `joined_at` field is the original timestamp when the membership was created (i.e., `team_members.created_at`), and is never updated on subsequent idempotent adds.

```json
{
  "user_id": "uuid-string",
  "team_id": "uuid-string",
  "email": "user@example.com",
  "name": "Jane Doe",
  "joined_at": "2026-07-10T15:01:11.889182Z"
}
```

| Field      | Type             | Notes                                                    |
|------------|------------------|----------------------------------------------------------|
| user_id    | string (UUID)    | The user's primary key (matches `users.id`)              |
| team_id    | string (UUID)    | The team's ID                                            |
| email      | string           | User's current email address (from `users.email` via fresh JOIN) |
| name       | string           | User's current display name (from `users.name` via fresh JOIN) |
| joined_at  | string (ISO8601) | Original membership creation timestamp; UTC, RFC3339 with microsecond precision |

List response for `GET /api/v1/teams/:id/members` is an array of member objects ordered by `joined_at` ascending (oldest member first):

```json
[
  { "user_id": "...", "team_id": "...", "email": "...", "name": "...", "joined_at": "..." }
]
```

### Validation rules

- **Name:** Any Unicode characters allowed. Leading and trailing whitespace is trimmed before validation. After trimming, must be non-empty and at most 255 characters. Returns HTTP 422 if empty after trimming or exceeds 255 characters. Returns HTTP 409 with message `"team name already exists"` if a duplicate exists among non-deleted teams.
- **Slug:** Lowercase alphanumeric + hyphens, 3-64 characters, must start with a letter, must not end with a hyphen. Consecutive hyphens (e.g., `a--b`) are explicitly allowed. Regex: `^[a-z][a-z0-9-]{1,62}[a-z0-9]$` (enforces minimum 3 characters: one leading letter, one or more middle characters, one trailing alphanumeric). Note: the middle group `{1,62}` requires at least one character, so the minimum valid slug length is 3. A slug of exactly 3 characters must be of the form `[a-z][a-z0-9-][a-z0-9]` — a hyphen is valid as the middle character (e.g., `a-b` is valid). Returns HTTP 422 on invalid slug. Returns HTTP 409 with message `"team slug already exists"` if a duplicate exists among non-deleted teams.
- **URL:** Optional. If provided, must have scheme (`http` or `https`) and a host at minimum. If omitted on create, the field is stored as null. Returns HTTP 422 on invalid URL format when provided.
- **Path parameters (`:id`, `user_id`):** Must be valid UUIDs. Returns HTTP 400 with a validation error message in the standard error envelope if a malformed or non-UUID value is provided. This check is performed before any database lookup.
- **Request body parsing:** If the request body is missing, malformed JSON, or cannot be decoded on an endpoint that requires a body, returns HTTP 400 with the message `"invalid request body"` in the standard error envelope. Unrecognized fields in the body are silently ignored. Endpoints that do not accept a body (archive, reactivate) ignore any body entirely.

### Error response format

All error responses reuse the existing error envelope defined in server_foundation. The `code` field in the JSON body is an integer that mirrors the HTTP status code. This ensures consistency across all API endpoints.

Example error envelope:

```json
{
  "code": 409,
  "message": "team slug already exists"
}
```

| Scenario                                                      | HTTP Status | Example message                              |
|---------------------------------------------------------------|-------------|----------------------------------------------|
| Duplicate name (among non-deleted teams)                      | 409         | `"team name already exists"`                 |
| Duplicate slug (among non-deleted teams)                      | 409         | `"team slug already exists"`                 |
| Invalid lifecycle transition — archive already-archived team  | 409         | `"team is already archived"`                 |
| Invalid lifecycle transition — reactivate already-active team | 409         | `"team is already active"`                   |
| Attempting to delete an active team                           | 409         | `"team must be archived before deletion"`    |
| Team is archived (membership add)                             | 409         | `"team is archived"`                         |
| Team not found or deleted                                     | 404         | `"team not found"`                           |
| User not found (membership add)                               | 404         | `"user not found"`                           |
| Invalid slug format                                           | 422         | `"invalid slug format"`                      |
| Invalid URL format                                            | 422         | `"invalid url format"`                       |
| Name is empty after trimming or exceeds 255 characters        | 422         | `"invalid team name"`                        |
| Missing required request body fields                          | 422         | `"missing required field"`                   |
| Malformed JSON or unparseable request body                    | 400         | `"invalid request body"`                     |
| Malformed or non-UUID path parameter                          | 400         | `"invalid id format"`                        |
| Caller is not an admin                                        | 403         | (defined by server_foundation middleware)    |

### `updated_at` semantics

The `updated_at` field is updated whenever the team record is mutated. This includes:
- Any lifecycle transition: archiving, reactivating (the status change updates `updated_at`).
- Deletion removes the record entirely (cascade delete within a transaction), so `updated_at` is not updated or observable post-deletion.
- Adding or removing members does **not** update the team's `updated_at`; membership changes are tracked via `team_members.created_at` (exposed as `joined_at` in the member response).

## Database Schema

This spec owns the `teams` and `team_members` table migrations. Server foundation (spec 1) provides the migration runner and database connection; this spec registers its own migration files.

### Migration files

Migration files follow the sequential integer prefix convention established in server_foundation. Files are embedded Go files using `embed.FS` and discovered and applied in order by the migration runner from server_foundation.

The numeric prefix for each migration file is assigned at integration time to avoid conflicts with migrations from other specs (e.g., server_foundation, oauth_and_users). Each spec uses descriptive base filenames (e.g., `create_teams.sql`, `create_team_members.sql`), and the numbering prefix is determined when the migrations are integrated into the unified project migration sequence. The migration runner handles ordering based on the numeric prefix and ensures each file is executed exactly once in ascending numeric order.

Example filenames (prefix assigned at integration):
- `NNN_create_teams.sql`
- `NNN_create_team_members.sql`

### `teams` table

| Column       | Type      | Notes                                                                      |
|--------------|-----------|----------------------------------------------------------------------------|
| id           | TEXT (PK) | UUID                                                                       |
| name         | TEXT      | Unique among non-deleted teams (enforced via partial UNIQUE index and application layer); stored after trimming; max 255 chars |
| slug         | TEXT      | Unique among non-deleted teams (enforced via partial UNIQUE index and application layer); validated format |
| url          | TEXT      | Nullable; optional on create; validated if present                         |
| status       | TEXT      | Enum: `active`, `archived`, `deleted`                                      |
| created_at   | DATETIME  | UTC; RFC3339 with microsecond precision                                    |
| updated_at   | DATETIME  | UTC; RFC3339 with microsecond precision; updated on any status change      |

> **Note on uniqueness:** Name and slug uniqueness among non-deleted teams is enforced via two complementary mechanisms:
> 1. **Application layer:** Before inserting a new team, the handler queries for any existing non-deleted team with the same name or slug and returns HTTP 409 if found.
> 2. **Database layer (partial UNIQUE index):** A partial UNIQUE index scoped to `WHERE status != 'deleted'` is created on each of `name` and `slug`. This catches concurrent race conditions that bypass the application-layer check, with the DB constraint violation mapped to HTTP 409. SQLite supports partial indexes and this is the preferred approach over a full-column UNIQUE constraint (which would prevent reuse of deleted teams' names/slugs).

### `team_members` table

| Column     | Type      | Notes                                                    |
|------------|-----------|----------------------------------------------------------|
| team_id    | TEXT (FK) | References `teams.id`, cascade delete                    |
| user_id    | TEXT (FK) | References `users.id`                                    |
| created_at | DATETIME  | Membership creation timestamp; UTC; exposed as `joined_at` |

Primary key: `(team_id, user_id)` — enforces idempotent membership adds at the database level. No surrogate primary key is needed; the composite key is sufficient.

The `users` table is owned by the oauth_and_users spec and provides the following columns used by this spec's member response JOIN: `id` (UUID, PK), `email` (TEXT), `name` (TEXT).

## Technical Boundaries

- **Language:** Go (1.22+)
- **HTTP framework:** Echo
- **Database:** SQLite
- **Schema ownership:** This spec defines and migrates the `teams` and `team_members` tables using sequentially-numbered SQL migration files embedded via `embed.FS`. Server foundation (spec 1) provides the DB connection and migration runner infrastructure. Migration file numeric prefixes are assigned at integration time to avoid cross-spec conflicts.
- **Admin enforcement:** All `/api/v1/teams` routes are protected by the admin middleware from server_foundation (spec 1). This middleware verifies the authenticated user's role and returns HTTP 403 if the user is not an admin.
- **Uniqueness enforcement:** Name and slug uniqueness is enforced at both the application layer (scoped to non-deleted teams, HTTP 409 on conflict) and the database layer via partial UNIQUE indexes (`WHERE status != 'deleted'`). DB-level constraint violations from concurrent inserts are caught and mapped to HTTP 409.
- **List ordering:** All list endpoints return results in `created_at` ascending order (oldest first). For `GET /api/v1/teams/:id/members`, ordering is by `team_members.created_at` (i.e., `joined_at`) ascending.
- **Archive/reactivate body handling:** The archive and reactivate endpoints (POST) do not require or inspect any request body. Any body present is silently ignored. `Content-Type` is not validated or required for these endpoints.
- **Path parameter validation:** All handlers validate UUID path parameters before performing any database operations, returning HTTP 400 on malformed input.
- **Request body parsing:** Malformed or undecodable JSON bodies return HTTP 400 with `"invalid request body"` on endpoints that accept a body. Unrecognized fields are silently ignored. Endpoints that accept no body (archive, reactivate) ignore any provided body entirely.
- **Transaction behavior:** Multi-step write operations (create team with uniqueness check, delete + cascade, idempotent membership add) are wrapped in a single database transaction. Single-step mutations (archive, reactivate) do not require explicit transactions.
- **Timestamp format:** All timestamps are stored and returned in UTC with RFC3339 microsecond precision (e.g., `2026-07-10T15:01:11.889182Z`).
- **Member response freshness:** The add-member endpoint always performs a fresh JOIN against `users` to return current `email` and `name`, even in the idempotent no-op case.
- **Rate limiting / request size limits:** Not addressed by this spec; delegated to infrastructure-level concerns.
- **Dependencies:** server_foundation (spec 1) for auth middleware, admin middleware, DB infrastructure, error envelope; oauth_and_users for the `users` table (columns: `id`, `email`, `name`) referenced by `team_members` and joined for member responses.
