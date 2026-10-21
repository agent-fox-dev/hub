# Errata: Spec 35 — Org Member Workspace Access Divergences

## Spec Expectation

Spec 35 (`35_org_member_workspace_access`) lets members of a workspace's org read the
workspace across hub's REST API, keeps every change with the owner and admin tokens, and
decides both in `wsaccess`. The entries below record where the delivered code or its tests
differ from what the spec's text says, with the code and the test of what was delivered.

## Implementation Reality

1. **The refusal envelope is apikit's, nested.** 35-REQ-2.8 and TS-35-14 write the bodies as
   `{error: msg, error_type: "forbidden"}`. apikit's envelope is
   `{"error": {"code": <status>, "message": <msg>, "error_type": "forbidden"}}`
   (`apikit/errors.go`, `WriteAPIError` and `WriteAPIErrorWithType`). `wsaccess.WriteRefusal`
   (`internal/wsaccess/refusal.go:22`) delegates to them; `TestWriteRefusal_TS_35_14` asserts the
   nested fields and the absence of `error_type` on 404 and 500.

2. **A refused merge submit or batch rebase used to go on.** `validateWorkspaceForMerge`
   returned `apikit.WriteAPIError(...)`, which is nil once the response is written, and its two
   callers stopped only on a non-nil error. A caller it refused had the 404 written and then
   had the merge enqueued or the batch rebase run. 35-REQ-7.3 requires a member's write to
   enqueue and rebase nothing, so the helper now returns whether it refused
   (`internal/merge/api.go:270`). `TestMemberMergeWrites_TS_35_38` failed on its first run with
   "batch rebase ran 2 times" and passes now.

3. **A merge PAT without its scope used to get the 403 and the data.** `requireMergeReadScope` and
   `requireMergeWriteScope` had the same nil-error defect: a PAT without `merges:read` got the
   403 written, and then the merge list (or the workspace refusal) appended to the same
   response. 35-REQ-10.2 requires the scope refusal to come first and to be the answer, so both
   now return whether to go on (`internal/merge/api.go:228`, `:239`).
   `TestMemberMergeScopeAnswers_TS_35_53` found it.

4. **Workspace secrets and variables say `workspace not found`, and a database error is 500.**
   As Decision 20 says, the refusals of these routes moved from `internal/secrets`' own
   `{"error": {"code": 404, "message": "not found"}}` to `wsaccess`'s `workspace not found`. A
   failed workspace read, which `lookupWorkspaceOwner` reported as 404, is now the 500 every
   other caller of `AuthorizeWorkspace` gets (35-REQ-2.7). The helper is `authorizeWorkspace`
   (`internal/secrets/handlers.go:113`); `TestStrangerSecretsAndVars_TS_35_45` asserts the body.
   The existing secrets tests asserted only status codes and are unchanged.

5. **Two existing tests changed their call, not their claim.** Task 1 lists only some of the
   callers of the old `AuthorizeWorkspace(db, auth, slug)`. The calls in
   `internal/wsaccess/observer_test.go`, `internal/workspace/binding_test.go`,
   `internal/workspace/binding_property_test.go` and `internal/boundtoken/refusal_points_test.go`
   gained a `Need` argument. `lookupWorkspaceForAuth` now returns the caller's `Access` instead
   of an error that was always nil, so `TestLookupWorkspaceForAuth_Binding_TS_33_18` checks that
   the access is `owner` where it checked that the error was nil. No assertion was weakened.

6. **The row-vanishing hook of TS-35-32 is a package variable.** The test needs the workspace
   row deleted between the access decision and the full-row load. `lookupWorkspaceForAuth` calls
   `afterAccessDecision` (`internal/workspace/handlers.go:223`) when a test sets it; it is nil in
   production.

7. **The cross-package tests are split by package.** TS-35-36..38 and TS-35-40, 52, 53, 54 and 56
   cover routes in several packages. `internal/merge` imports `internal/workspace`, so a
   workspace test cannot mount the merge routes without an import cycle. Each package's routes
   are tested in that package (`org_member_access_test.go`, `org_member_unchanged_test.go`),
   under the same test id.
