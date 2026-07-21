# Erratum: DELETE /api/v1/workspaces/:slug returns 204, not 200

**Spec:** 03_workspace_write_delete
**Affected requirements:** 03-REQ-3.1, 03-REQ-3.2, 03-REQ-3.5
**Affected test specs:** TS-03-12, TS-03-13, TS-03-16

## Divergence

The spec states that a successful `DELETE /api/v1/workspaces/:slug` should
return HTTP 200. The existing handler (from spec 01) returns HTTP 204 No
Content via `c.NoContent(http.StatusNoContent)`.

## Decision

Tests use **HTTP 204** to match the existing codebase behavior. Changing the
delete handler to return 200 would be a breaking change that conflicts with
spec 01 tests and the established API contract.

## Impact

All test assertions for successful delete operations (TS-03-12, TS-03-13,
TS-03-16) check for status code 204 instead of the spec-stated 200.
