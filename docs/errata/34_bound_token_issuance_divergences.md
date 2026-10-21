# Errata: Spec 34 — Bound Token Issuance Divergences

## Spec Expectation

Spec 34 (`34_bound_token_issuance`) issues, lists and retires workspace-bound personal access
tokens on top of spec 33's binding. The entries below record where the delivered behaviour
differs from a criterion, a test or spec 33's own tests, with the code and the test of what was
delivered.

## Implementation Reality

1. **Deleting a workspace now retires its bindings, which spec 33's tests said it would not.**
   33-REQ-1.6 says a deletion leaves the binding rows of the slug unchanged ("in this spec"),
   and 34-REQ-5.1 replaces that: `deleteWorkspaceRevoked` (`internal/workspace/store.go`) calls
   `wsaccess.RevokeAndUnbindSlug` (`internal/wsaccess/binding_store.go`) in its transaction.
   Two spec 33 tests encoded the old behaviour and were changed to the new one:
   `TestArchiveDelete_KeepBindings_TS_33_6` (archive still changes nothing; delete now leaves
   only the other workspace's binding and revokes the deleted workspace's tokens) and
   `TestBoundToken_DeletedWorkspaceFailsClosed_TS_33_55`, whose workspace row now vanishes by
   another path than `deleteWorkspace` (the case the binding sweep cleans up), so it still
   proves that a binding whose workspace is gone reaches nothing.

2. **Behind spec 33's guard, a bound PAT on another workspace's `/tokens` gets `404`.** 34-REQ-1.3
   and 34-REQ-4.2 answer a bound PAT with `403 token_bound` for any slug. In the running hub,
   spec 33's binding guard runs before both handlers, and `/workspaces/:slug/tokens` is
   workspace-scoped. For a slug other than its own, the token gets the guard's `404 workspace not
   found`, which the guard reports. The handlers' `403 token_bound` (`refuseBoundToken`,
   `internal/boundtoken/boundtoken.go`) answers on the token's own slug, and wherever the
   handlers run without the guard. `TestMint_BoundPAT_TS_34_3` and `TestList_BoundPAT_TS_34_22`
   test the handlers; the guard's answer is spec 33's `TestBindingGuard_OtherSlugIs404_TS_33_13`.
   Either way the token mints and lists nothing, and the refusal is reported once.

3. **The explicit owner check of the mint handler cannot be told apart by a test yet.**
   TS-34-6 asks for a caller that `wsaccess.AuthorizeWorkspace` allows but who is not the owner
   (org-member read access, hub PRD 04). `AuthorizeWorkspace` allows only the owner and admin
   tokens today, and admin tokens are refused earlier. The check `ws.OwnerID != auth.UserID`
   (`internal/boundtoken/mint.go`) is in place, and `TestMint_NotFound_TS_34_6` covers
   non-owners and a missing workspace.

4. **The refusal limiter keeps an entry that holds back a count until its token's next
   refusal.** 34-REQ-10.6 requires each event's `suppressed` to equal the refusals since the
   token's previous event. 34-REQ-10.7 drops "entries older than the window". Dropping an entry
   that still holds back a count would make the token's next event report 0, against 10.6. So
   `Reporter.Report` (`internal/boundtoken/reporter.go`) drops an entry whose window has passed
   only when it holds nothing back. An entry with a count stays until the token is refused again,
   which records the count. Memory is bounded by the tokens refused within the last minute plus
   the tokens with a pending count, at most one entry per bound token
   (`TestReporter_BoundedMemory_TS_34_62`, `TestReporter_PropertyWindow_TS_34_61`).
