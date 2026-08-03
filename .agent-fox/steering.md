# Steering Directives

## Authentication and Authorization

All HTTP handlers authenticate via apikit's middleware (mounted by apikit's
server bootstrap, not by handler packages), which validates Bearer tokens
and injects `*apikit.AuthInfo` into the request context.

**Core rules:**

- Call `apikit.GetAuthInfo(c)` to retrieve auth state (returns `nil` when
  unauthenticated). Never use `c.Get()` or define custom context keys.
- Use `*apikit.AuthInfo` everywhere -- in handler logic, function signatures,
  and tests. Never define local auth structs, credential-type constants, or
  wrapper functions.
- Credential types are `"admin_token"` (full access), `"api_key"` (full user
  access), and `"pat"` (scoped). Only PATs need permission checks via
  `slices.Contains(auth.Permissions, "resource:action")`.
- In tests, inject auth with `apikit.SetAuthInfo(c, &apikit.AuthInfo{...})`.
  Never use `c.Set()` with a custom key.
- Define standalone auth helpers (`isAdmin`, `isPAT`, `hasScope`, and
  domain-specific permission helpers) as functions taking `*apikit.AuthInfo`,
  not methods on local structs. See reference implementations for the pattern.

**Reference implementations:**

- `internal/workspace/auth.go` + `internal/workspace/handlers.go`

## Library Reuse

Before implementing any functionality, check whether it already exists:

1. **apikit** (`../apikit`) provides: server bootstrap and lifecycle, auth
   middleware, `WriteAPIError` error envelope, ETag support, timestamp
   utilities (`NowUTC`, `FormatUTC`, `ParseUTC`), database helpers
   (`(*DB).WithTx`, sentinel errors), CLI scaffolding (Cobra commands,
   `CLIClient`), SDK client with generics, and all shared domain types.
2. **go.mod dependencies** -- check existing imports before adding new ones.
3. **Standard library** -- prefer `net/http`, `slices`, `encoding/json`, etc.
   over third-party alternatives.

Never reimplement what apikit provides. Common violations to avoid:

- Use `apikit.WriteAPIError()`, not `c.JSON()` with a hand-built error map.
- Use `apikit.NowUTC()` / `apikit.FormatUTC()`, not `time.Now().UTC().Format(...)`.
- Use apikit's auth middleware, not custom credential resolution and hashing.
- Use `apikit.SetETag()` / `apikit.CheckETag()` for conditional GET support.

## Documentation Freshness

After implementing any spec, you **must** update all affected documentation
before the session is considered complete. Outdated docs are treated as
regressions.

**When to update:** Every time a spec implementation adds, changes, or removes
any of the items listed below, update the corresponding document in the same
session — not as a follow-up task.

| What changed | Update |
|---|---|
| REST endpoints added/changed/removed | `docs/api.md` |
| CLI commands, subcommands, or flags added/changed/removed | `docs/cli.md` |
| Permission scopes added/changed | `docs/permissions.md` |
| Config keys or env vars added/changed | `docs/configuration.md` |
| Architecture, package layout, or data flow changed | `docs/architecture.md` and/or relevant ADR |
| Setup, quickstart, or project overview changed | `README.md` |

**Instructions:**

1. Review the spec you just implemented and identify every user-facing or
   developer-facing surface that changed (API routes, request/response
   schemas, CLI parameters, environment variables, architectural decisions).
2. Open each affected doc and update it to reflect the new state. Do not
   leave placeholder text like "TODO" or "TBD" — write the actual content.
3. If a doc file listed above does not exist yet, create it with the correct
   content rather than skipping the update.
4. Run `make check` after doc updates to ensure nothing is broken.
