# Errata: Spec 33 — Token Workspace Binding Divergences

## Spec Expectation

Spec 33 (`33_token_workspace_binding`) binds a personal access token to one workspace and
enforces the binding in the REST API and the git server. Several of its criteria and tests
assume details the code does not have. The entries below record what the hub delivers
instead, with the code and the test of the delivered behaviour.

## Implementation Reality

1. **The PAT prefix is apikit's build-time prefix, not a literal `af_pat_`.** 33-REQ-2.2 and
   33-REQ-5.2 parse `Authorization: Bearer af_pat_<token_id>_<secret>`. apikit's prefix is
   the variable `apikit.TokenPrefix`. It is `ak` by default, and the Makefile and
   `containers/hub/Containerfile` set it to `af` with `-ldflags`. A `go test` binary and a
   plain `go build` therefore issue `ak_pat_…` tokens. `patTokenID`
   (`internal/wsaccess/guard.go`) parses `<apikit.TokenPrefix>_pat_<id>_…`, which is the
   form apikit itself accepts, so release builds parse `af_pat_` as the spec says. The tests
   build headers from `apikit.TokenPrefix` (`TestBindingGuard_DerivesTokenID_TS_33_9`,
   `TestAPIKitRouteGuard_*`). The git server keeps its own `af_` prefixes, and its
   `lookupPAT` sets `TokenID` itself.

2. **Without a read scope, the workspace handlers answer 404, not 403.** TS-33-14 and
   TS-33-34 call the scope-less answer of `GET /workspaces/:slug` and `GET /workspaces` "the
   handler's own 403". `handleGetWorkspace` and `handleListWorkspaces`
   (`internal/workspace/handlers.go`) answer a PAT without a read-implying scope with
   `404 workspace not found`. The tests assert what the criteria require: a bound token gets
   the same status and body as an unbound PAT with the same scopes
   (`TestBoundToken_OwnSlugEqualsUnbound_TS_33_14`, `TestListWorkspaces_BoundToken_TS_33_34`).

3. **A nil SQLite handle does not accept PATs on audit ingestion.** TS-33-50 expects "a PAT
   posts an event to any run → accepted" with the audit routes registered without a SQLite
   handle. Before this spec, `checkWorkspaceAccess` (`internal/audit/auth.go`) skipped the
   existence, archive and ownership checks there. It accepted API keys and admin tokens but
   refused every PAT that did not carry the retired `KeyID` slug, with
   `403 workspace_access_denied`. TS-17-52 encodes that refusal. With the `KeyID` branch
   removed (33-REQ-8.1), that is still the outcome, so 33-REQ-8.6 ("accepted as before this
   spec") holds for every credential. `TestIngestion_NilSQLiteSkipsChecks_TS_33_50` asserts
   201 for an API key and an admin token on a workspace that does not exist, and the unchanged
   403 for a PAT.

4. **`lookupWorkspaceForAuth` reports a refusal by its nil workspace.** 33-REQ-3.6 and
   TS-33-18/19 expect a non-nil error. On every refusal, `lookupWorkspaceForAuth` returns
   `(nil, respondError(...))`. The error is the result of writing the response, which is nil
   once the response is written, and all eight callers test the workspace for nil. The binding
   refusal follows the same convention. The tests assert the nil workspace and the written
   404/500 (`TestLookupWorkspaceForAuth_Binding_TS_33_18`, `TestBindingLookupFailure_Is500_TS_33_19`).

5. **A refused audit ingestion request no longer stores its event.** TS-33-47 requires that
   a bound token posting to another workspace's run writes no event. `checkWorkspaceAccess`
   returned the result of `apikit.WriteAPIError`, which is nil once the response is written,
   and every ingestion handler stops only on a non-nil error. So every refusal (unknown
   workspace, archived workspace, non-owner, a PAT without SQLite) wrote its status and error
   body, then carried on: it stored the event and appended the 201 body to the response. Each
   refusal now returns `errWorkspaceAccessDenied` after writing (`refused`,
   `internal/audit/auth.go`). The status codes and error bodies that unbound callers receive
   are unchanged. What changed is that the stray second body and the stored event are gone
   (`TestIngestion_UnboundNonOwner_TS_33_48`, `TestIngestion_ArchivedAndUnknown_TS_33_49`,
   `TestIngestion_BoundOtherWorkspace_TS_33_47`). This differs from 33-REQ-9.1 ("byte-identical")
   for those refused requests only, and the difference is the removal of a cross-workspace
   write.

6. **On audit ingestion, the binding is checked before the archive and ownership checks.**
   33-REQ-3.5 places the rule "after the existence and ownership checks" in
   `AuthorizeWorkspace`. `checkWorkspaceAccess` checks the archive status before ownership.
   The binding check sits right after the existence check, so a bound token gets
   `404 workspace not found` for every other workspace, archived or not owned. That is the
   answer the binding guard gives in front of these routes (TS-33-15) and the answer
   33-REQ-8.2 asks for.

7. **On its own deleted workspace, a bound token gets the route's own answer.** 33-REQ-9.5
   expects `404 workspace not found` on every workspace-scoped route once `S` is deleted. The
   guard refuses every slug other than `S` with that 404. For `S` itself, the guard lets the
   request through (33-REQ-3.2), and the route's own checks decide, exactly as for an unbound
   PAT. A handler that checks its scope before it looks the workspace up therefore answers
   the scope refusal first. For example, `GET /workspaces/S/patches` without `patches:read`
   answers `403 PAT requires patches:read scope`. With the scope it answers
   `404 workspace not found`. Nothing is reachable either way.
   `TestBoundToken_DeletedWorkspaceFailsClosed_TS_33_55` asserts the 404s and the equality
   with an unbound PAT. Answering 404 on `S` before the handler would need a second lookup
   (the workspace's existence) in the guard, which 33-REQ-2.4 does not allow.

8. **`GET /sessions` no longer lists every session to a caller without a readable workspace.**
   `getAccessibleWorkspaces` (`internal/audit/handlers.go`) returned a nil list when the caller
   owned no active workspace, and `ListSessions` (`internal/audit/store.go`) reads nil as "no
   restriction". So such a caller, and with this spec a token bound to an archived, deleted or
   foreign workspace, listed the sessions of every user. The list is now empty, never nil. The
   session listing of a user without an active workspace changes from all sessions to none,
   which differs from 33-REQ-9.1 for that case only, and removes a cross-tenant read
   (`TestSessions_NoAccessibleWorkspaceListsNothing`,
   `TestSessionsAudit_PropertyNeverWidens_TS_33_53`).
