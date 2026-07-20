# Erratum: apikit auth context helpers exported at package level

**Spec:** 01_workspaces  
**Affected requirements:** 01-REQ-4.1 through 01-REQ-4.8  
**Date:** 2026-07-20

## Problem

The spec and task plan assume hub handlers can call apikit's auth context
helpers (`GetAuthInfo`, `SetAuthInfo`, `GetUserID`, `IsAdmin`) to inspect
the authenticated credential. These functions are defined in apikit's
`internal/authctx` and `internal/auth` packages with an **unexported**
context key type (`contextKey`). Go visibility rules prevent hub from
importing any `internal/*` package from another module.

Without access to these helpers, hub cannot read the auth context set by
apikit's auth middleware, making production-path authorization impossible.

## Resolution

Added re-exports of the following symbols at the `github.com/txsvc/apikit`
package level in `apikit.go`:

- `type AuthInfo = authctx.AuthInfo`
- `func GetAuthInfo(c echo.Context) *AuthInfo`
- `func SetAuthInfo(c echo.Context, info *AuthInfo)`
- `func GetUserID(c echo.Context) string`

These are type aliases and thin delegation functions — no new logic.

The workspace package's `getAuth()` function now checks two sources in order:

1. Echo context `c.Get()` — used by test middleware (`X-Test-Auth` header).
2. Apikit auth context (request `context.Context`) — used in production
   where apikit's auth middleware injects `AuthInfo` via `context.WithValue`.

If neither source has auth info, the request is rejected with HTTP 401.

## Impact

- **apikit:** Four new exported symbols added to the public API surface.
  All are stable re-exports of existing internal types/functions.
- **hub:** `auth.go` now imports `apikit` to call `GetAuthInfo` for
  production auth bridging. Tests are unaffected (they continue using the
  echo context path).

## Spec divergence from error envelope (01-REQ-18.1)

The task plan instructs creating a custom `respondError` helper in
`internal/workspace/errors.go`. Instead, `respondError` in `handlers.go`
delegates to `apikit.WriteAPIError` which was already exported and produces
the exact JSON envelope format. No separate `errors.go` file was created.
