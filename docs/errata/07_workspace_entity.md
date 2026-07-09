# Errata: Spec 07 workspace_entity divergences

## Spec 06 test updates required by spec 07

**Spec 06 tests assert:** The `workspaces` table name, `workspace_members`,
and `workspace_id` should NOT appear in `internal/db/db.go` (TS-06-2) and
should not exist as runtime tables (TS-06-3).

**Spec 07 introduces:** A NEW `workspaces` table (git-repo-mapped entity)
with a completely different schema (slug, git_url, branch, owner_id, team_id).
This is a different entity from the organizational "workspace" that spec 06
renamed to "team."

**Resolution:** Spec 06 tests updated to only check for legacy naming
patterns (`workspace_members`, `workspace_id`) rather than the word
`workspaces` itself. The `assertTableNotExists(t, db, "workspaces")`
assertion was removed since the new workspaces table is now legitimate.
Table count tests updated from 5 to 6. These changes preserve the intent
of spec 06 (no legacy naming) while accommodating spec 07's new entity.

## PRAGMA foreign_keys=ON added to OpenDatabase

**Spec says (07-REQ-1.E2):** FK constraints on `workspaces.team_id` and
`workspaces.owner_id` must be enforced so that missing the teams table
dependency blocks startup.

**Pre-existing state:** `PRAGMA foreign_keys` was never set in
`internal/db/db.go`, meaning ALL FK references across the schema (teams,
team_members, api_keys, workspaces) were unenforced.

**Resolution:** Added `PRAGMA foreign_keys = ON` to `OpenDatabase()` so FK
enforcement applies to every connection. This is a system-wide behavioral
change that affects all FK references, not just the workspaces table. All
existing tests continue to pass with this change enabled.
