# Errata: workspace ownership is enforced on all workspace-scoped endpoints

## Previous Behaviour

Specs 12, 13, 15, 16, 17 and 18 describe the sync, reclone, patch, merge,
rebuild, rerere, patch-status, session-creation and audit endpoints in terms
of permission scopes only. The implementations looked the workspace up by
slug without checking `owner_id`, so any authenticated caller holding the
right scope (every API key has all scopes implicitly) could sync, rebuild,
merge, read audit data of, or push patches into any workspace on the hub.
The unified audit query and the SSE stream returned every workspace's
events to every `audit:read` holder.

## Current Behaviour

All of these endpoints now resolve the workspace through
`workspace.AuthorizeWorkspace`, which applies the same rule as the CRUD
endpoints: owner or admin token, otherwise `404` (anti-enumeration). The
unified audit query and SSE stream are restricted to the caller's own
workspaces; hub events without a workspace are admin-only. `POST
/api/v1/sessions` answers `404` for a workspace the caller does not own.

Tests that previously expected `400` for an unknown workspace on patch
creation now expect `404`, and admin tokens are rejected with `403` on the
user-scoped secrets and variables endpoints because they have no user
identity. See `docs/permissions.md` and `docs/api.md`.
