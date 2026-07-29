# Steering Directives

## Authentication and Authorization in API Handlers

All HTTP handlers in this project authenticate via apikit's middleware, which
validates Bearer tokens and injects `*apikit.AuthInfo` into the request
context. Handlers must use apikit's auth types directly — never define local
auth structs, credential-type constants, or bridge/wrapper functions.

### Rules

1. **Use `apikit.GetAuthInfo(c)` to retrieve auth state.** Returns `nil` when
   no valid credential is present. Never read auth from Echo's `c.Get()` or
   define your own context keys.

2. **Use `*apikit.AuthInfo` as-is.** Do not define package-local `AuthInfo`
   structs, `CredentialType` enums, or conversion functions. The canonical
   type is `apikit.AuthInfo` (aliased from `authctx.AuthInfo`).

3. **Check credential types with string constants on `auth.CredentialType`:**
   - `"admin_token"` — admin token (full access, no user identity)
   - `"api_key"` — user API key (implicit full access for the user)
   - `"pat"` — personal access token (scoped permissions)

4. **Check PAT permissions via `auth.Permissions` (a `[]string`).** Admin
   tokens and API keys have implicit full access — only PATs need scope
   checks. Use `slices.Contains` or a local `hasScope` helper.

5. **In tests, inject auth with `apikit.SetAuthInfo(c, &apikit.AuthInfo{...})`.**
   Never use `c.Set()` with a custom key. Test helper functions like
   `adminAuth()`, `userAuth()`, `patAuth()` must return `*apikit.AuthInfo`.

### Handler Pattern

Every authenticated handler follows this structure:

```go
func handleFoo() echo.HandlerFunc {
    return func(c echo.Context) error {
        auth := apikit.GetAuthInfo(c)
        if auth == nil {
            return respondError(c, http.StatusUnauthorized, "authentication required")
        }
        if isPAT(auth) && !canFooWrite(auth) {
            return respondError(c, http.StatusForbidden, "insufficient permission scope")
        }
        // ... handler logic using auth.UserID, isAdmin(auth), etc.
    }
}
```

### Permission Helper Pattern

Define standalone functions that take `*apikit.AuthInfo`, not methods on a
local struct. Group them at the top of your handlers file:

```go
func isAdmin(auth *apikit.AuthInfo) bool { return auth.CredentialType == "admin_token" }
func isPAT(auth *apikit.AuthInfo) bool   { return auth.CredentialType == "pat" }

func hasScope(auth *apikit.AuthInfo, scopes ...string) bool {
    for _, s := range scopes {
        if slices.Contains(auth.Permissions, s) {
            return true
        }
    }
    return false
}

func canFooRead(auth *apikit.AuthInfo) bool  { return hasScope(auth, "foo:read", "foo:manage") }
func canFooWrite(auth *apikit.AuthInfo) bool { return hasScope(auth, "foo:write", "foo:manage") }
```

### Reference implementations

- `internal/secrets/handlers.go` — secrets and variables handlers
- `internal/workspace/auth.go` + `internal/workspace/handlers.go` — workspace handlers
