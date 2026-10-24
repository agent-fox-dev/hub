# Steering Directives

This file is kept the same across the agent-fox repositories. Anything that is
specific to one repository lives in the **Repository Specifics** section at the
end.

## Library Reuse

1. Before implementing any functionality, check whether it already exists in the codebase.
2. **go.mod dependencies** -- check existing imports before adding new ones.
3. **Standard library** -- prefer `net/http`, `slices`, `encoding/json`, etc. over third-party alternatives.

## Target Platforms

The software MUST run on Linux or macOS systems.
Support for Windows or other operating systems is "best effort", but not a requirement.

## Documentation Freshness

After implementing any spec, you **must** update all affected documentation
before the session is considered complete. Outdated docs are treated as
regressions.

Before updating the documentation, run `make doc` first, to refresh the generated Go docs (see below).

**When to update:** Every time a spec implementation adds, changes, or removes
any of the items listed below, update the corresponding document in the same
session — not as a follow-up task.

| What changed | Update |
|---|---|
| REST endpoints or the public Go API added/changed/removed | `docs/api.md` |
| CLI commands, subcommands, or flags added/changed/removed | `docs/cli.md` |
| Config keys or env vars added/changed | `docs/configuration.md` |
| Architecture, package layout, or data flow changed | `docs/architecture.md` and/or relevant ADR |
| Permission scopes added/changed | `docs/permissions.md` |
| Setup, quickstart, or project overview changed | `README.md` |

*Important:* Do NOT write tests for testing the presence or the content of the docs or PRDs.

**Instructions:**

1. Review the spec you just implemented and identify every user-facing or
   developer-facing surface that changed (API routes, request/response
   schemas, CLI parameters, environment variables, architectural decisions).
2. Open each affected doc and update it to reflect the new state. Do not
   leave placeholder text like "TODO" or "TBD" — write the actual content.
3. If a doc file listed above does not exist yet, create it with the correct
   content rather than skipping the update. A repository without a REST API or
   CLI keeps the file anyway and says in one line that it has none.
4. Run `make check` after doc updates to ensure nothing is broken.

## Tests and Spec Traceability

Specs are the input to an implementation, not the shape of its tests or its
comments. Once a spec is implemented, the code, its tests and the ADRs are
the record; the spec is archived.

- **Tests are organised by behaviour, not by spec.** A test file is named for
  the area of the production code it tests (`create_test.go`,
  `sync_test.go`, `access_test.go`), and a test for the behaviour it checks
  (`TestCreate_RejectsDuplicateSlug`). Do not create per-spec test files,
  smoke, property, phase or group files, or test names and comments that
  carry spec or test-case ids (`TS_01_17`, `01-REQ-4.1`). The variants of one
  behaviour are one table-driven test.
- **Every behaviour is tested once**, at the level where it lives: a handler
  through the router, a store function directly, a pure function directly.
  Keep end-to-end tests (a real server, real credentials) for what only that
  layer proves. The test code of a package stays near the size of its
  production code.
- **Use the shared helpers** (`internal/testutil` in this repository) for the
  database, credentials, requests, responses and seeds; a package keeps one
  `helpers_test.go` for what is specific to it. Create another package's
  tables with that package's schema function, never by copying its DDL.
- **Do not test artefacts**: no tests of an import graph, a build tag, the
  presence of a helper, a struct's fields, a documentation file's content, or
  that a package boundary holds.
- **Production comments give the reason, not a citation.** No spec or
  requirement ids in code comments; where a decision is the reason, cite the
  ADR.
- **Where the code diverges from a spec, record the decision in the ADR of
  the feature** (or a new ADR), as a rule the code follows, and update the
  reference docs. Do not write errata.

## Generated Docs

`make doc` writes `go doc -all` output for every publicly accessible package of
the repository (no `internal/` packages, no `examples/`) to `docs/generated/`,
one text file per package. The files are committed to git.

Their purpose is orientation: **when you start working on this repository, use
`docs/generated/` to find your way around.** It is the cheapest way to see which
packages exist, what each one is for and which types, functions and constants
it exports, before reading any source. Prefer it to scanning directories or
grepping for declarations.

- Never edit these files by hand; they are overwritten by `make doc`.
- Treat them as possibly stale: the code is authoritative. If a file disagrees
  with the source, regenerate it.
- Run `make doc` and commit the result whenever a change adds, removes or
  alters an exported identifier or a package doc comment.

## Repository Specifics

The hub is a REST service built on apikit (`docs/openapi.yaml` is the
machine-readable copy of `docs/api.md`).

### Reuse apikit

apikit (`../apikit`) provides: server bootstrap and lifecycle, auth
middleware, `WriteAPIError` error envelope, ETag support, timestamp
utilities (`NowUTC`, `FormatUTC`, `ParseUTC`), database helpers
(`(*DB).WithTx`, sentinel errors), CLI scaffolding (Cobra commands,
`CLIClient`), SDK client with generics, and all shared domain types.

Never reimplement what apikit provides. Common violations to avoid:

- Use `apikit.WriteAPIError()`, not `c.JSON()` with a hand-built error map.
- Use `apikit.NowUTC()` / `apikit.FormatUTC()`, not `time.Now().UTC().Format(...)`.
- Use apikit's auth middleware, not custom credential resolution and hashing.
- Use `apikit.SetETag()` / `apikit.CheckETag()` for conditional GET support.

### Authentication and Authorization

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
