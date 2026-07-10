# Errata: Spec 02 (OAuth and Users) — User Management Divergences

This document records divergences between spec 02 (oauth_and_users) and the
implemented codebase or other specs, discovered during task group 2 (user
management tests).

## 1. team_memberships Missing `role` Column

**Spec 02 says (02-REQ-6.1):** GET /api/v1/users/:id returns
`team_memberships` array with `{team_id, team_name, role}` per entry.
Test TS-02-19 asserts `role == 'member'`.

**Reality:** The `team_members` table (defined in spec 01 DDL and owned by
spec 03) has only columns: `team_id`, `user_id`, `created_at`. There is no
`role` column. Spec 03 explicitly lists "Roles within teams" as
deferred/out-of-scope.

**Adaptation:** The handler must JOIN `team_members` with `teams` to populate
`team_id` and `team_name`. For the `role` field, the handler should return
`"member"` as a hardcoded default until spec 03 adds role support. The test
checks for the presence of the `role` field without asserting a specific
value, with a comment noting this limitation.

**Affected requirements:** 02-REQ-6.1, TS-02-19.

## 2. full_name NOT NULL Constraint vs Null Semantics

**Spec 02 says (02-REQ-7.1):** PUT /api/v1/users/:id should set `full_name`
to null if the supplied value is null or empty string.

**Spec 01 DDL:** `full_name TEXT NOT NULL DEFAULT ''` — the NOT NULL constraint
makes it impossible to store NULL in the `full_name` column.

**Adaptation:** When the request supplies null or an empty string for
`full_name`, the handler stores an empty string (`''`) instead of SQL NULL.
API responses return `""` (empty string) rather than JSON `null` for a
cleared full_name. Tests adapted to assert empty string instead of null.

**Affected requirements:** 02-REQ-7.1, TS-02-20.

## 3. Case-Insensitive Username Uniqueness

**Spec 02 says (02-REQ-12.2):** Username uniqueness is enforced by comparing
lowercased forms. Usernames are stored as-is (case preserved).

**Spec 01 DDL:** `username TEXT NOT NULL UNIQUE` — SQLite's UNIQUE constraint
on TEXT columns is case-sensitive by default.

**Adaptation:** The implementation must enforce case-insensitive uniqueness
at the application level before every INSERT/UPDATE. The handler should query
`SELECT id FROM users WHERE LOWER(username) = LOWER(?)` before inserting.
A future DDL migration could add `COLLATE NOCASE` to the column or create a
functional unique index on `LOWER(username)` for race-condition safety.

**Affected requirements:** 02-REQ-12.2, 02-REQ-4.4, TS-02-16, TS-02-38,
TS-02-E15.

## 4. Key Refresh Expiry Duration Calculation

**Spec 02 says (02-REQ-9.1, 02-REQ-9.4):** POST /api/v1/keys/:key_id/refresh
reuses the "original expiry duration" to recalculate expires_at.

**Problem:** The `api_keys` table has no `expires_in_days` column. The task
plan computes original duration as `ceil((expires_at - created_at) / 24h)`,
which only works on the first refresh. After refresh, `expires_at` is
updated but `created_at` remains the original value, causing the computed
duration to grow on each subsequent refresh.

**Resolution:** Added `expires_in_days INTEGER` column to the `api_keys`
table. The column stores the original N-day expiry duration at key creation
time (NULL for indefinite keys). The refresh handler reads this column
directly instead of computing from timestamps, eliminating the drift issue.

All test DDLs and INSERT helpers updated to include the column. The
production migration DDL (in `internal/db`, currently a stub) must also
include this column when implemented.

**Affected requirements:** 02-REQ-9.1, 02-REQ-9.4, TS-02-27, TS-02-28,
TS-02-30.
