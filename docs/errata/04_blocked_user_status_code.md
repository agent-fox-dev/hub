# Erratum: Blocked user workspace token rejection status code

**Spec:** 04 (workspaces_and_tokens)
**Requirement:** 04-REQ-12.4
**Date:** 2026-07-10

## Conflict

Spec 04 requirement 04-REQ-12.4 states:

> WHILE a request presents a workspace token belonging to a user whose account
> is blocked, THE auth middleware SHALL rejects the request with HTTP 401
> Unauthorized before the route handler runs.

However, the auth middleware is owned by spec 01 (server_foundation), which
specifies:

> Blocked users are rejected with HTTP 403.

The master PRD (`docs/01_prd.md`) confirms: "Blocked users are rejected with
HTTP 403."

## Resolution

Since spec 01 owns the auth middleware implementation, the actual behavior is
HTTP 403 for blocked users. Spec 04 integration tests (TS-04-52) assert
HTTP 403 instead of HTTP 401 to match the middleware's real behavior.

This divergence is intentional and correct: the middleware should use a
consistent status code for blocked users regardless of which spec defines the
endpoint being accessed.
