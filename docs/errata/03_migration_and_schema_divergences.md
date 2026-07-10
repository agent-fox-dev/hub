# Errata: Spec 03 (Teams) — Migration and Schema Divergences

This document records divergences between spec 03 (teams) and the implemented
codebase or other specs, discovered during task group 1 (migration tests).

## 1. No Migration Runner in Server Foundation

**Spec 03 says:** The migration runner from server_foundation (spec 01)
discovers and applies numbered SQL migration files in ascending order
(03-REQ-1.3, 03-REQ-1.E1).

**Reality:** Spec 01 explicitly lists "database migration tooling" as a
non-goal (spec 01 prd.md). The master PRD (docs/01_prd.md, line 32 and 386)
states: "Schema is applied on boot via `CREATE TABLE IF NOT EXISTS`; no
migration tooling."

**Adaptation:** `InitSchema(db *sql.DB) error` applies DDL using
`CREATE TABLE IF NOT EXISTS` and `CREATE UNIQUE INDEX IF NOT EXISTS`,
achieving the same idempotency guarantee. Migration files are still embedded
via `embed.FS` (exposed as `MigrationsFS`) for auditability and ordering,
but execution uses direct SQL rather than a migration tracking table.

**Affected requirements:** 03-REQ-1.3, 03-REQ-1.E1, TS-03-3, TS-03-E1.

## 2. Error Envelope Format Mismatch

**Spec 03 says:** Error responses use a flat envelope:
`{"code": <int>, "message": <string>}`.

**Spec 01 / master PRD says:** Error responses use a nested envelope:
`{"error": {"code": <integer>, "message": "<string>"}}`.

**Decision:** The implementation must use the spec 01 / master PRD nested
format since it is the canonical project-wide error envelope. Spec 03's
test assertions should be adjusted to expect the nested format. This
divergence affects all error handling test specs (TS-03-5 through TS-03-56)
and all requirements that reference the error envelope.

**Affected requirements:** 03-REQ-2.2 through 03-REQ-12.2.

## 3. Schema Conflict: `teams.url` Nullable vs NOT NULL DEFAULT ''

**Spec 01 DDL (in tasks):** Defines `teams.url` as `TEXT NOT NULL DEFAULT ''`.

**Spec 03 PRD:** Defines `url` as "TEXT, Nullable" with responses returning
`null` when unset.

**Decision:** Follow spec 03's definition (nullable). The teams table is
owned by spec 03, and JSON `null` for absent URLs is the correct API behavior
per the PRD response shape: `"url": "string or null"`.

## 4. Schema Conflict: Partial vs Full UNIQUE Constraints

**Spec 01 DDL (in tasks):** Uses full `UNIQUE` constraints on `teams.name`
and `teams.slug`.

**Spec 03 PRD:** Requires partial UNIQUE indexes scoped to
`WHERE status != 'deleted'` to allow reuse of deleted teams' names/slugs
(03-REQ-2.E2, 03-PROP-2).

**Decision:** Follow spec 03's partial UNIQUE indexes. Full UNIQUE
constraints would prevent name/slug reuse after team deletion, violating
a core spec 03 requirement.

## 5. Schema Conflict: Missing ON DELETE CASCADE for team_members FK

**Spec 01 DDL (in tasks):** Omits `ON DELETE CASCADE` on the `team_members`
foreign key to `teams(id)`.

**Spec 03 (03-REQ-1.2):** Requires `team_id TEXT FK referencing teams.id
with cascade delete`.

**Decision:** Follow spec 03's requirement and add `ON DELETE CASCADE`.
The cascade simplifies the delete-team transaction and is consistent with
the atomicity requirement (03-REQ-7.1).

## 6. Column Name Mismatch: users.full_name vs users.name

**Spec 03 PRD (line 249, dependencies section):** States the `users` table
provides columns: `id, email, name`.

**Spec 01/02 DDL:** The `users` table defines the column as `full_name`
(not `name`).

**Decision:** The store layer must JOIN on `users.full_name` and map it
to the `name` field in member response JSON. This is transparent to the
API consumer.

## 7. No Dedicated Admin Middleware in Server Foundation

**Spec 03 says:** All team routes are protected by "admin middleware from
server_foundation" that returns HTTP 403 for non-admin callers.

**Reality:** Spec 01 provides auth middleware that sets `AuthContext.IsAdmin`,
but no separate admin middleware. Spec 02 enforces admin access via
handler-level checks.

**Decision:** Implement a small admin-check middleware wrapper in the teams
package (or as shared middleware) that checks `AuthContext.IsAdmin` and
returns HTTP 403 if false. This middleware is applied at the router group
level for all `/api/v1/teams` routes.
