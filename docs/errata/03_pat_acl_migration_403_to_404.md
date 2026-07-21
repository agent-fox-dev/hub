# Erratum: PAT lifecycle operation rejection changed from 403 to 404

**Spec:** 03_workspace_write_delete
**Affects:** 01-REQ-4.E1, 01-REQ-4.E2, 01-REQ-4.8, 01-REQ-8.3, 01-REQ-9.3, 01-REQ-10.3

## Change

Spec 01 originally required PATs attempting lifecycle operations (archive,
reactivate, delete) to receive HTTP 403 ("PATs cannot archive/delete
workspaces") — a blanket prohibition on all PATs regardless of scope.

Spec 03 introduces scope-based access control for PATs:

- `workspaces:write` grants archive and reactivate access on owned workspaces.
- `workspaces:delete` grants delete access on archived workspaces owned by the
  PAT's user.
- PATs lacking the required scope receive HTTP 404 (anti-enumeration) rather
  than 403, to avoid disclosing resource existence.

## Impact

The following spec 01 tests were updated to expect 404 instead of 403:

- `TestEdgeWorkspaceAuthz_PATReadCannotMutate` (TS-01-E6)
- `TestEdgeWorkspaceAuthz_PATCreateCannotMutate` (TS-01-E7)
- `TestWorkspaceAuthz_InsufficientScope` (TS-01-24)
- `TestWorkspaceArchive_PATForbidden` (TS-01-41)
- `TestWorkspaceReactivate_PATForbidden` (TS-01-45)
- `TestWorkspaceDelete_PATForbidden` (TS-01-49)

## Rationale

Anti-enumeration is a security best practice: returning 404 for any
unauthorized request prevents attackers from discovering valid workspace slugs
by probing with PATs that have insufficient permissions.
