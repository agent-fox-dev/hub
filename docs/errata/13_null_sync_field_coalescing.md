# Errata: 13-REQ-1.E2 NULL sync field coalescing test approach

## Spec Expectation

13-REQ-1.E2 states: "IF a workspace row in the database predates the schema
migration and has NULL for sync_mode or sync_status, THEN THE workspace API
response SHALL treat NULL sync_mode as 'pull_only' and NULL sync_status as
'idle'."

## Implementation Reality

The `sync_mode` and `sync_status` columns are defined with `NOT NULL DEFAULT`
constraints in both the `CREATE TABLE` DDL and the `ALTER TABLE` migration
statements. SQLite enforces `NOT NULL` on both `INSERT` and `UPDATE`,
preventing NULL values from ever existing in these columns through normal
database operations.

## Test Adaptation

The original Group 1 test `TestSyncSchema_NullSyncFieldsCoalesced` attempted
to set `sync_mode = NULL` and `sync_status = NULL` via `UPDATE`, which fails
with `NOT NULL constraint failed` error. The test was rewritten to simulate
the pre-migration scenario more realistically:

1. Creates a database with the old schema (no sync columns)
2. Adds sync columns WITHOUT `NOT NULL` (simulating a legacy or incomplete migration)
3. Inserts a workspace row where sync_mode and sync_status are NULL
4. Verifies the API response coalesces NULL to defaults

This approach tests the same coalescing contract while using a feasible
database state. The `scanWorkspaceRow` function scans `sync_mode` and
`sync_status` into `*string` temporaries and coalesces `nil` to defaults,
providing the defensive handling 13-REQ-1.E2 requires.

## Summary

The NOT NULL constraint (required by 13-REQ-1.1 / TS-13-1) and the NULL
coalescing edge case (13-REQ-1.E2) are inherently contradictory when tested
against a single database schema. The implementation satisfies both by:
- Enforcing NOT NULL at the DDL level for new and properly migrated databases
- Coalescing NULL at the scan layer for defensive handling of any legacy data
