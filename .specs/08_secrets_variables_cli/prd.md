---
spec_id: 08
spec_name: secrets_variables_cli
title: Secrets Variables Cli
status: draft
created_at: '2026-07-29T10:18:44.193617+00:00'
updated_at: '2026-07-29T10:24:49.543757+00:00'
owner: ''
source: interactive
schema_version: 1
---
# Secrets and Variables CLI Commands

## Intent

The `afc` CLI needs commands for managing secrets and variables against the
REST API defined in spec `07_secrets_variables`. Users interact with secrets
and variables through `afc secrets` and `afc vars` command groups, each
providing create, list, update, and delete subcommands with ownership
selectors for targeting user, organization, or workspace scopes.

This spec covers only the CLI layer — the thin Cobra command definitions that
parse arguments and flags, call the hub REST API via `apikit.CLIClient`, and
print results. The API endpoints, authorization model, and store logic are
defined in spec `07_secrets_variables`.

## Goals

- Add `afc secrets` command group with `create`, `list`, `update`, and
  `delete` subcommands.
- Add `afc vars` command group with `create`, `list`, `update`, `delete`,
  and `resolve` subcommands.
- Support ownership selectors (`--org`, `--workspace`, `--user`) on all
  mutation and list commands.
- Follow the existing `afc workspace` CLI patterns: Cobra commands,
  `apikit.CLIClientFromCmd`, `apikit.CLIPrintResult`, `apikit.CLIHandleError`.
- Register both command groups in `BuildRootCommand`.

## Non-goals

- **REST API endpoints, authorization, or store logic.** Covered by spec
  `07_secrets_variables`.
- **Permission scope registration.** Covered by spec `07_secrets_variables`.
- **Interactive prompts or confirmation dialogs.** All commands are
  non-interactive.
- **Output formatting options** (e.g. `--format table`). The CLI outputs
  JSON via `apikit.CLIPrintResult`, matching the existing workspace commands.
- **Batch update or batch delete.** Only create supports multiple entries.

## Background

The `afc` CLI is the primary command-line interface for the agent-fox
platform. It follows a consistent pattern of thin Cobra command groups that
parse arguments and flags, then delegate to the hub REST API. The existing
`afc workspace` commands (`internal/cli/workspace_cmd.go`) serve as the
canonical implementation reference for flag binding, client instantiation
via `apikit.CLIClientFromCmd`, result printing via `apikit.CLIPrintResult`,
and error handling via `apikit.CLIHandleError`.

Secrets and variables are key/value stores scoped to users, organizations,
and workspaces, defined in spec `07_secrets_variables`. Secrets are sensitive
(values never returned to clients); variables are non-sensitive (values
readable). Both share the same CRUD API shape and ownership-selector model.

The `GET /api/v1/workspaces/:slug/vars/resolved` endpoint — used by
`afc vars resolve` — is defined in spec `07_secrets_variables` as
requirement 07-REQ-16 and returns a merged, precedence-ordered variable set
for a workspace.

Both new command groups (`SecretsCmd()` and `VarsCmd()`) are registered in
`internal/cli/workspace_cmd.go` alongside the existing workspace commands,
following the established file convention for CLI command registration in
this project. The command implementations themselves live in two new
dedicated files: `internal/cli/secrets_cmd.go` and
`internal/cli/vars_cmd.go`.

## Functional Requirements

### Command Group: `afc secrets`

#### `afc secrets create <key=value>[,<key=value>...]`

Create one or more secrets at the specified ownership scope(s).

- **Arguments:** One or more `key=value` pairs, comma-separated.
- **Flags:**
  - `--org <slug>` — target an organization scope.
  - `--workspace <slug>` — target a workspace scope.
  - `--user` — target the authenticated user's scope (boolean flag, takes
    no argument; defined as `BoolVar` with default `false`).
- **Default scope:** If no flag is provided at all (none of `--user`,
  `--org`, `--workspace`), runtime logic defaults to user scope. This is
  detected at runtime by checking whether all three flag variables remain at
  their zero values, not by setting the `--user` BoolVar default to `true`.
  This mirrors how the absence of `--org` or `--workspace` already works and
  avoids ambiguity between an explicit `--user` and the implicit default.
- **Multi-scope create:** Multiple flags can be combined — each flag adds a
  scope and results in one API call per targeted scope. API calls are always
  made in a fixed order: **user → org → workspace**. For example,
  `--user --org myorg` makes two API calls (user scope first, then org
  scope), and `--user --org myorg --workspace myws` makes three (user, then
  org, then workspace). This order is deterministic and must be preserved in
  both the implementation and tests. If any call fails, the error is printed
  immediately as it occurs (not deferred) via `apikit.CLIHandleError`;
  already-completed calls are not rolled back.
- **Exit code for partial failure:** If any scope call fails, the command
  returns exit code 1 as a sentinel for partial failure — regardless of
  whether the final scope call succeeded. A zero exit code is only returned
  when all scope calls succeed. Exit code 1 is communicated by returning
  `apikit.NewCLIError(1, "")` from `RunE`, following the same pattern used
  in `workspace_cmd.go` where `apikit.CLIHandleError` propagates a
  `CLIError` type whose code is read by the Cobra error handler. The partial
  failure tracking variable (`hadError bool`) is set to `true` on any
  per-scope `apikit.CLIHandleError` call; after all scopes are processed,
  if `hadError` is `true` the function returns `apikit.NewCLIError(1, "")`.
- **Explicit `--user` with other flags:** When `--user` is provided
  alongside `--org` or `--workspace`, user scope is included as one of the
  targeted scopes. There is no conflict — each flag independently adds a
  scope to the set of API calls.
- **API call:** `POST /api/v1/{user/secrets | orgs/:slug/secrets | workspaces/:slug/secrets}`
  with body `{"entries": [{"key": "...", "value": "..."}]}`.
- **Output:** `CLIPrintResult` is called once per successful scope call with
  the full `DoRequest` response object as-is (e.g. the `{"entries": [...]}`
  wrapper object). The CLI does not reshape or unwrap the API response.
- **Errors:** Prints the API error envelope on failure per scope via
  `apikit.CLIHandleError`.

#### `afc secrets list`

List secret names (never values) at the specified ownership scope.

- **Flags:**
  - `--org <slug>` — list org-level secrets.
  - `--workspace <slug>` — list workspace-level secrets.
  - `--user` — list user-level secrets (boolean flag, `BoolVar` with default
    `false`).
  - No flag defaults to user-level (same runtime logic as `create`).
- **Selector rule:** Only one scope selector is allowed. Providing multiple
  selectors returns exit code 2 with an error message.
- **API call:** `GET /api/v1/{user/secrets | orgs/:slug/secrets | workspaces/:slug/secrets}`.
- **Output:** `CLIPrintResult` is called with the full `DoRequest` response
  as-is. The API returns a JSON array of `{key, created_at, updated_at}`
  objects.

#### `afc secrets update <key=value>`

Update a single secret's value at the specified ownership scope.

- **Arguments:** Exactly one `key=value` pair.
- **Flags:** Same as `list` (one scope selector, defaults to user via runtime
  logic; `--user` is a `BoolVar` with default `false`).
- **API call:** `PATCH /api/v1/{...}/secrets/:key` with body `{"value": "..."}`.
- **Output:** `CLIPrintResult` is called with the full `DoRequest` response
  as-is. The API returns `{key, created_at, updated_at}` (value omitted for
  secrets).

#### `afc secrets delete <key>`

Delete a single secret at the specified ownership scope.

- **Arguments:** The key name to delete.
- **Flags:** Same as `list` (one scope selector, defaults to user via runtime
  logic; `--user` is a `BoolVar` with default `false`).
- **API call:** `DELETE /api/v1/{...}/secrets/:key`. The hub returns
  `204 No Content` on success; `CLIClient.DoRequest` returns a nil body for
  204 responses.
- **Output:** Stdout is silent on success. A confirmation message is printed
  to stderr only, matching the workspace delete pattern exactly:
  ```go
  fmt.Fprintf(cmd.ErrOrStderr(), "Secret '%s' has been deleted.\n", key)
  ```
  `CLIPrintResult` is **not** called for delete — the CLI skips it entirely
  and goes straight to the stderr confirmation after a nil body is returned.

### Command Group: `afc vars`

#### `afc vars create <key=value>[,<key=value>...]`

Create one or more variables at the specified ownership scope(s).

- Same argument, flag, multi-scope, and error-reporting semantics as
  `afc secrets create`:
  - `--user` is a `BoolVar` with default `false`; no-flag case defaults to
    user scope via runtime logic.
  - API calls made in fixed order user → org → workspace.
  - Errors printed per-scope as they occur via `apikit.CLIHandleError`.
  - Exit code 1 returned via `apikit.NewCLIError(1, "")` if any scope
    failed; exit code 0 only if all succeeded.
- **API call:** `POST /api/v1/{user/vars | orgs/:slug/vars | workspaces/:slug/vars}`
  with body `{"entries": [{"key": "...", "value": "..."}]}`.
- **Output:** `CLIPrintResult` is called once per successful scope call with
  the full `DoRequest` response object as-is (e.g. the `{"entries": [...]}`
  wrapper). Includes values for variables.

#### `afc vars list`

List variables (names and values) at the specified ownership scope.

- Same flag semantics as `afc secrets list` (`--user` is a `BoolVar` with
  default `false`; no-flag defaults to user scope via runtime logic).
- **API call:** `GET /api/v1/{user/vars | orgs/:slug/vars | workspaces/:slug/vars}`.
- **Output:** `CLIPrintResult` is called with the full `DoRequest` response
  as-is. The API returns a JSON array of `{key, value, created_at,
  updated_at}` objects.

#### `afc vars update <key=value>`

Update a single variable's value at the specified ownership scope.

- Same argument and flag semantics as `afc secrets update`.
- **API call:** `PATCH /api/v1/{...}/vars/:key` with body `{"value": "..."}`.
- **Output:** `CLIPrintResult` is called with the full `DoRequest` response
  as-is. The API returns `{key, value, created_at, updated_at}`.

#### `afc vars delete <key>`

Delete a single variable at the specified ownership scope.

- Same argument and flag semantics as `afc secrets delete`.
- **API call:** `DELETE /api/v1/{...}/vars/:key`. The hub returns
  `204 No Content` on success; `CLIClient.DoRequest` returns a nil body for
  204 responses.
- **Output:** Stdout is silent on success. A confirmation message is printed
  to stderr only:
  ```go
  fmt.Fprintf(cmd.ErrOrStderr(), "Variable '%s' has been deleted.\n", key)
  ```
  `CLIPrintResult` is **not** called for delete — the CLI skips it entirely
  and goes straight to the stderr confirmation after a nil body is returned.

#### `afc vars resolve <workspace-slug>`

Show the resolved/merged variable set for a workspace (spec `07_secrets_variables`,
requirement 07-REQ-16).

- **Arguments:** The workspace slug.
- **No ownership flags** — resolution is always workspace-scoped.
- **API call:** `GET /api/v1/workspaces/:slug/vars/resolved`.
- **Output:** `CLIPrintResult` is called with the full `DoRequest` response
  as-is. The API returns a JSON array of `{key, value, origin, created_at,
  updated_at}` objects, where `origin` indicates which tier the value came
  from (`user`, `org`, or `workspace`).

### Argument Parsing

**`key=value` parsing:** Split on the first `=` character. The key is
everything before the first `=`; the value is everything after it (may
contain `=` characters). A `key=value` argument without an `=` is an error
(exit code 2).

**Multiple entries:** For `create`, the argument is a comma-separated list
of `key=value` pairs (e.g. `KEY1=val1,KEY2=val2`). Values containing
commas must be quoted at the shell level (e.g. `'KEY=a,b,c'`).

**Empty or whitespace-only keys:** After comma-splitting, if any entry
produces an empty or whitespace-only key (e.g. `KEY=val,,KEY2=val2` splits
to an empty middle entry), the CLI exits with code 2 and a clear parse error
message (e.g. `"empty key in argument at position N"`). This is a
client-side guard for a client-side parsing artifact — the server is not
contacted.

**Key validation:** Beyond the empty/whitespace guard above, the CLI does
not validate key names — all other key validation is performed server-side
by the API. The CLI passes keys through as-is.

### Ownership Selector Resolution

For all commands, ownership scope is determined by flags:

| Flags provided | Scope | API path segment |
|----------------|-------|-----------------|
| (none) | user | `user/secrets` or `user/vars` |
| `--user` | user | `user/secrets` or `user/vars` |
| `--org <slug>` | org | `orgs/<slug>/secrets` or `orgs/<slug>/vars` |
| `--workspace <slug>` | workspace | `workspaces/<slug>/secrets` or `workspaces/<slug>/vars` |

**Default scope detection:** All three ownership flags (`--user`, `--org`,
`--workspace`) have zero-value defaults (`false` for `--user`, `""` for
`--org` and `--workspace`). Runtime logic checks: if all three remain at
their zero values, user scope is used. This is equivalent to `--user` being
set, but is detected explicitly in code rather than via a BoolVar default of
`true`. This approach keeps `--user` as a true opt-in flag and makes the
"no flags provided → user scope" rule transparent in the implementation.

For `create`: all three flags can be combined — each provided flag adds a
scope, resulting in one API call per scope. API calls are always executed in
fixed order: **user → org → workspace**. For example,
`--user --org myorg --workspace myws` produces three API calls in that
order. The command returns `apikit.NewCLIError(1, "")` if any scope call
fails, and exit code 0 only if all succeed.

For `list`, `update`, `delete`: exactly one selector is allowed (exit code
2 if multiple provided).

The `--user` flag is a boolean flag (takes no argument, defined as `BoolVar`
with default `false`) that uses the authenticated user's identity from the
API key or PAT.

### Error Handling

- Invalid arguments (missing `=`, wrong number of args): exit code 2 via
  `apikit.NewCLIError(2, message)`.
- Empty or whitespace-only key after comma-splitting in `create`: exit code
  2 with a descriptive parse error message via `apikit.NewCLIError(2, message)`.
- Multiple selectors on non-create commands: exit code 2 via
  `apikit.NewCLIError(2, message)`.
- API errors: printed as JSON error envelope via `apikit.CLIHandleError`.
- Multi-scope create partial failures: each per-scope error is printed
  immediately as it occurs (using `apikit.CLIHandleError`); a `hadError bool`
  tracking variable is set to `true`; the command continues executing
  remaining scopes in user → org → workspace order; after all scopes
  complete, if `hadError` is `true` the function returns
  `apikit.NewCLIError(1, "")` (exit code 1), otherwise returns `nil`
  (exit code 0). This pattern mirrors `workspace_cmd.go`.
- DELETE responses: the hub returns `204 No Content`; `CLIClient.DoRequest`
  returns a nil body; the CLI skips `CLIPrintResult` and prints only the
  stderr confirmation message.

### Registration

Both command groups are registered in `BuildRootCommand` in
`internal/cli/workspace_cmd.go` (the established file convention for CLI
command registration in this project):

```go
root.AddCommand(
    // ... existing commands ...
    SecretsCmd(),
    VarsCmd(),
)
```

The `SecretsCmd()` function is implemented in `internal/cli/secrets_cmd.go`
and `VarsCmd()` in `internal/cli/vars_cmd.go`.

## Tech Stack

- **Language:** Go
- **CLI framework:** Cobra via `github.com/spf13/cobra`
- **HTTP client:** `apikit.CLIClient` (via `apikit.CLIClientFromCmd`)
- **Output:** `apikit.CLIPrintResult` (JSON, passes full `DoRequest` response
  as-is) and `apikit.CLIHandleError`
- **Test tooling:** `go test` with `net/http/httptest`, mirroring the
  patterns in `internal/cli/workspace_cmd_test.go`. No external test
  framework — standard library only.

## Test Coverage

Test files (`secrets_cmd_test.go`, `vars_cmd_test.go`) must cover:

1. **Happy path** — each subcommand makes the correct API call(s) and
   prints the expected output (stdout for data commands, stderr-only for
   deletes).
2. **Argument validation errors** — missing `=` in a `key=value` argument,
   wrong number of positional arguments, and empty/whitespace-only keys
   after comma-splitting each produce exit code 2 with an appropriate
   message.
3. **Multi-scope create (required)** — at least one test must exercise a
   multi-scope `create` invocation (e.g. `--user --org myorg`) to verify
   that API calls are made in the correct user → org → workspace order and
   that both scope outputs are produced. This is a mandatory minimum given
   that multi-scope create is the most complex behavior in the spec
   (ordering, partial failure, exit code 1 sentinel).

Exhaustive edge-case coverage (e.g. every API error response variant, every
possible multi-scope combination beyond the one required test) is not
required. The three categories above define the minimum bar. Tests use
`net/http/httptest` to mock the hub REST API, consistent with
`workspace_cmd_test.go`.

## Implementation File Layout

| File | Contents |
|------|----------|
| `internal/cli/secrets_cmd.go` | `SecretsCmd()` and all `afc secrets` subcommand implementations |
| `internal/cli/vars_cmd.go` | `VarsCmd()` and all `afc vars` subcommand implementations |
| `internal/cli/workspace_cmd.go` | Registration of `SecretsCmd()` and `VarsCmd()` in `BuildRootCommand` (existing file) |
| `internal/cli/secrets_cmd_test.go` | Tests for `afc secrets` commands: happy path + argument validation errors + at least one multi-scope create test |
| `internal/cli/vars_cmd_test.go` | Tests for `afc vars` commands: happy path + argument validation errors + at least one multi-scope create test |

## Verified External API

### `apikit` (local, via go.mod replace)

| Symbol | Package | Signature | Notes |
|--------|---------|-----------|-------|
| `RootCommand` | `apikit` | `func RootCommand() *cobra.Command` | |
| `CLIClientFromCmd` | `apikit` | `func CLIClientFromCmd(cmd *cobra.Command) (*CLIClient, error)` | |
| `CLIPrintResult` | `apikit` | `func CLIPrintResult(cmd *cobra.Command, v any) error` | Called with full `DoRequest` response as-is; not called for DELETE (nil body) |
| `CLIHandleError` | `apikit` | `func CLIHandleError(cmd *cobra.Command, err error) error` | |
| `NewCLIError` | `apikit` | `func NewCLIError(code int, message string) *CLIError` | Used for exit code 2 (arg errors) and exit code 1 (partial multi-scope failure) |
| `CLIClient.DoRequest` | `apikit` | `func (c *CLIClient) DoRequest(ctx context.Context, method, path string, body any) (any, error)` | Returns nil body for 204 No Content |

### Hub REST API (spec `07_secrets_variables`)

| Endpoint | Method | Notes |
|----------|--------|-------|
| `/api/v1/user/secrets` | POST, GET | User-scoped secrets |
| `/api/v1/user/secrets/:key` | PATCH, DELETE | User-scoped secret mutation; DELETE returns 204 |
| `/api/v1/orgs/:slug/secrets` | POST, GET | Org-scoped secrets |
| `/api/v1/orgs/:slug/secrets/:key` | PATCH, DELETE | Org-scoped secret mutation; DELETE returns 204 |
| `/api/v1/workspaces/:slug/secrets` | POST, GET | Workspace-scoped secrets |
| `/api/v1/workspaces/:slug/secrets/:key` | PATCH, DELETE | Workspace-scoped secret mutation; DELETE returns 204 |
| `/api/v1/user/vars` | POST, GET | User-scoped variables |
| `/api/v1/user/vars/:key` | PATCH, DELETE | User-scoped variable mutation; DELETE returns 204 |
| `/api/v1/orgs/:slug/vars` | POST, GET | Org-scoped variables |
| `/api/v1/orgs/:slug/vars/:key` | PATCH, DELETE | Org-scoped variable mutation; DELETE returns 204 |
| `/api/v1/workspaces/:slug/vars` | POST, GET | Workspace-scoped variables |
| `/api/v1/workspaces/:slug/vars/:key` | PATCH, DELETE | Workspace-scoped variable mutation; DELETE returns 204 |
| `/api/v1/workspaces/:slug/vars/resolved` | GET | Resolved/merged variable set (07-REQ-16) |

### Response Body Shapes

| Command | Response JSON shape |
|---------|-------------------|
| `secrets create` | `{"entries": [{"key": "...", "created_at": "...", "updated_at": "..."}]}` (values omitted; full object passed to `CLIPrintResult`) |
| `secrets list` | `[{"key": "...", "created_at": "...", "updated_at": "..."}]` (full response passed to `CLIPrintResult`) |
| `secrets update` | `{"key": "...", "created_at": "...", "updated_at": "..."}` (value omitted; full response passed to `CLIPrintResult`) |
| `secrets delete` | 204 No Content — no JSON body; stderr confirmation only; stdout silent; `CLIPrintResult` not called |
| `vars create` | `{"entries": [{"key": "...", "value": "...", "created_at": "...", "updated_at": "..."}]}` (full object passed to `CLIPrintResult`) |
| `vars list` | `[{"key": "...", "value": "...", "created_at": "...", "updated_at": "..."}]` (full response passed to `CLIPrintResult`) |
| `vars update` | `{"key": "...", "value": "...", "created_at": "...", "updated_at": "..."}` (full response passed to `CLIPrintResult`) |
| `vars delete` | 204 No Content — no JSON body; stderr confirmation only; stdout silent; `CLIPrintResult` not called |
| `vars resolve` | `[{"key": "...", "value": "...", "origin": "user|org|workspace", "created_at": "...", "updated_at": "..."}]` (full response passed to `CLIPrintResult`) |

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 07_secrets_variables | 8 | 1 | CLI calls the REST API endpoints mounted in group 8 |
