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
