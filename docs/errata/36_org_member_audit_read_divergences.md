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
   The per-run reads and the transcript left it with task 3: a member reads them
   (36-REQ-1.1, `TestRunReads_MemberReads_TS_36_1`). The session, usage, cost and
   `GET /sessions?workspace_slug=` reads left it with task 4 (36-REQ-2.1,
   `TestSessionReads_Member_TS_36_8`). `GET /audit?workspace=` and `GET /events?workspace=` left it
   with task 6, whose `readableWorkspaces` serves both (36-REQ-4.1 and 36-REQ-5.1,
   `TestAuditQuery_MemberWorkspaces_TS_36_18`). The test keeps completing a session and
   ingesting an event, on which a member's answer stays the stranger's (36-REQ-7).

2. **Admin tokens go through the rule on per-run reads, the transcript and `POST /sessions`.**
   Before this spec `requireWorkspaceRead` returned early for an admin token, so an admin naming a
   slug that names no workspace was served (an empty run, or a session opened in a workspace
   that does not exist). Steps 2.2 and 3.2 send admin tokens through `wsaccess.AuthorizeWorkspace`,
   and 36-REQ-1.4 and 36-REQ-6.3 answer an unknown slug with `404 workspace not found`, so an
   admin token now gets that 404 too (`internal/audit/auth.go`, `authorizeWorkspace`). An admin
   token still reads and opens sessions in every workspace that exists, whatever its status
   (`TestRunReads_OwnerAndAdmin_TS_36_2`, `TestCreateSession_OwnerAndAdminOpen_TS_36_27`).
   Two smaller consequences follow from the same change: an admin token replaying a session id
   whose session belongs to another workspace that no longer exists gets `404 workspace not
   found` instead of the record, and a failed workspace or binding lookup on these routes now
   answers an admin token `500`, as it answers everyone else (36-REQ-9.1).

3. **A failed lookup on the session list answers 500, with no session.** Step 5.2 says that
   `getAccessibleWorkspaces` returns an empty non-nil list on an error "so no session is
   returned"; 36-REQ-9.2 says the list "returns no session". `getAccessibleWorkspaces`
   (`internal/audit/handlers.go`) returns both, the empty list and the error, and
   `handleListSessions` keeps the `500 internal server error` it has always answered when that
   lookup fails, rather than a `200` with an empty list a client could take for "no sessions".
   No session is returned either way (`TestSessionReads_DatabaseFailure_TS_36_40`).

4. **The forge test fixtures gained `org_members`.** `GET /audit` and `GET /events` now take their
   set from `wsaccess.ReadableSlugs`, whose query reads `org_members` for every non-admin caller.
   Production always has that table (apikit creates it), but three forge tests mount the audit
   query routes on a fixture without it (TS-31-62, TS-32-48, TS-32-64), and the first run of
   task 6 failed them with 500. `createWorkspacesTable` (`internal/forge/testhelpers_test.go`) and
   `smokeDB` (`internal/forge/smoke_test.go`) now create the table, with the DDL `addOrgMember`
   already used. No assertion changed.
