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
| REST endpoints added/changed/removed | `docs/api.md` **and** `docs/openapi.yaml` |
| Request/response schemas, status codes, or query parameters | `docs/api.md` **and** `docs/openapi.yaml` |
| CLI commands, subcommands, or flags added/changed/removed | `docs/cli.md` |
| Permission scopes added/changed | `docs/permissions.md` **and** the `x-required-scopes` of every affected operation in `docs/openapi.yaml` |
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

### OpenAPI Description

`docs/openapi.yaml` is an OpenAPI 3.1 description of the whole HTTP surface.
**Any change to the REST API requires a matching change to it, in the same
session.** It is a shipped artifact -- clients generate against it -- so a
stale entry is a broken contract, not a stale sentence.

**The handlers are the source of truth, not `docs/api.md`.** Write each entry
from the code you just changed: the route registration, the request and
response structs and their JSON tags (including `omitempty`, which decides
whether a field is omitted or sent as `null`), and every status code the
handler can return. Where the two documents disagree, fix `docs/api.md` too
rather than copying it.

Update all of the following when they change:

- **Paths and operations.** Echo route parameters (`:slug`) become OpenAPI
  template parameters (`{slug}`); a wildcard (`*`) becomes a named parameter
  that accepts slashes. Give every operation a unique `operationId`.
- **Schemas.** Add or amend the `components/schemas` entry rather than inlining
  a shape that already has a name. Match nullability to the Go type: a `*T`
  field with no `omitempty` is `type: [x, "null"]`; a field with `omitempty` is
  absent from `required` and is never sent as `null`.
- **`x-required-scopes`.** Every operation lists the PAT scopes it accepts. An
  empty list means no scope check runs. Record scopes that are tested
  literally, and any that are accepted only in certain modes, in the operation
  description -- the implication rules in `docs/permissions.md` do not apply
  everywhere.
- **Error responses.** Reuse `components/responses` where one fits. If the
  handler sets an `error_type`, add it to the `Error` schema's enum and name it
  in the response description.
- **`x-provider: apikit`.** Mark operations served by apikit rather than by hub
  code. These are mounted on the API group, so they live under the configured
  mount point (`/api/v1` by default) -- not at the server root. Only
  `/healthz`, `/readyz`, `/version`, `/metrics`, and `/git/...` sit outside it.

**Before committing, verify both of these:**

1. **The document is valid.** No validator runs in `make check`, because the
   repository carries no Python or Node toolchain:

   ```sh
   pip install openapi-spec-validator
   python3 -c "
   from openapi_spec_validator import validate
   from openapi_spec_validator.readers import read_from_filename
   validate(read_from_filename('docs/openapi.yaml')[0])
   print('valid')
   "
   ```

2. **Route coverage matches the code.** Extract every
   `(*echo.Group).VERB("path")` and `(*echo.Echo).VERB("path")` registration
   from the non-test sources of this repository and of apikit, and diff that
   set against the document's paths. It must match in **both** directions: a
   route missing from the document is an undocumented endpoint, and a path in
   the document with no registration is a phantom one.

If a divergence turns out to be a bug in the handler rather than in the
document, fix the handler -- and record the finding in `docs/errata/` so the
reasoning survives the diff.