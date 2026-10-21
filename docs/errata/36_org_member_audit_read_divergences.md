# Errata: Spec 36 — Org Member Audit Read Divergences

## Spec Expectation

Spec 36 (`36_org_member_audit_read`) lets members of a workspace's org read the workspace's
sessions, cost, per-run audit records and transcripts, and see its events in the unified audit
query and the SSE stream, through `wsaccess`. Opening and completing sessions, reporting usage and
ingesting audit data stay with the owner and admin tokens. Goal 5 says every existing test in
`internal/audit` passes with no assertion changed. The entries below record where the delivered
code or its tests differ from what the spec's text says, with the code and the test of what was
delivered.

## Implementation Reality

1. **TS-35-56 no longer builds its own org schema.** Spec 35's audit test
   (`internal/audit/org_member_unchanged_test.go`) added `workspaces.org_id` and created
   `org_members` itself. `openTestSQLiteDB` now has both (36-REQ-10.4,
   `TestOpenTestSQLiteDB_OrgSchema_TS_36_46`), so the test's `ALTER TABLE` and `CREATE TABLE`
   statements were removed; its seed rows are unchanged.

   TS-35-56 also asserted that a member gets the stranger's answer on every audit and session
   route, which is what spec 36 changes (Goal 5 does not name it). Each task that opens a route
   to members removed that route from the test's list, and the test keeps the routes on which a
   member's answer stays the stranger's. `POST /sessions` left it with task 2: a member now gets
   `403` with `error_type: forbidden` (36-REQ-6.2, `TestCreateSession_MemberForbidden_TS_36_28`).
