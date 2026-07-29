# Errata: 08_secrets_variables_cli

Divergences between spec 08 and the actual apikit/codebase behavior.
Tests in `secrets_cmd_test.go` follow the codebase reality, not the spec.

## 1. Error envelopes go to stdout, not stderr

**Spec says:** "stderr contains the JSON API error envelope" (08-REQ-2.E5,
08-REQ-3.E2, 08-REQ-4.E3, 08-REQ-5.E3, and all Error Handling table entries).

**Reality:** `CLIHandleError` calls `CmdPrintJSON` which writes to
`cmd.OutOrStdout()` — that is **stdout**. The existing `workspace_test.go`
confirms this pattern: `"stdout should contain error envelope via
CLIHandleError"` (line 858).

**Tests follow:** stdout for error envelopes, matching the existing workspace
test patterns and the actual `CLIHandleError` implementation.

## 2. Exit codes are always 1 for CLIError, not 2

**Spec says:** Argument validation errors return `NewCLIError(2, message)` and
"exits 2" (08-REQ-2.E1, 08-REQ-2.E2, 08-REQ-2.E3, 08-REQ-3.E1, 08-REQ-4.E1,
08-REQ-4.E2, 08-REQ-5.E1, 08-REQ-5.E2, etc.).

**Reality:** `ExitCode()` in `apikit/internal/cli/output.go:50` returns 1 for
any error satisfying the `apiErrorer` interface — and `CmdError` (the type
behind `NewCLIError`/`CLIError`) satisfies `apiErrorer` via `ErrorCode()` and
`ErrorMessage()` methods. The `2` in `NewCLIError(2, msg)` controls the JSON
envelope's `code` field, not the process exit code. Existing workspace tests
never reference exit code 2 — all error cases assert exit 1.

**Tests follow:** `err != nil` checks (which correspond to exit 1), not
exit code 2.

## 3. DoRequest prepends /api/v1 to paths

**Spec says:** Commands call `DoRequest` with full paths like
`/api/v1/user/secrets` (08-REQ-2.1, 08-REQ-3.1, etc.).

**Reality:** `DoRequest` (in `apikit/internal/cli/user.go:125`) already
constructs: `fullURL = endpointURL + "/api/v1" + path`. The existing
`workspace_cmd.go` passes un-prefixed paths like `/workspaces`, not
`/api/v1/workspaces`.

**Implementation must use:** Paths without the `/api/v1` prefix (e.g.,
`/user/secrets`, `/orgs/myorg/secrets`). The mock test server receives the
full path with prefix because apikit adds it.

## 4. Validation errors should use CLIHandleError wrapper

**Spec says:** Argument validation errors "return `NewCLIError(2, message)`
from `RunE` without calling `DoRequest`" — implying `NewCLIError` is returned
directly.

**Reality:** The existing workspace_cmd.go pattern wraps all validation errors
with `CLIHandleError`: `return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, ...))`.
Without `CLIHandleError`, the error would not be printed as a JSON envelope
until the top-level `CLIPrintError` handler, changing the output format.

**Implementation should follow:** The existing workspace pattern of wrapping
with `CLIHandleError`.

## 5. Create response format (server-side)

**Spec 08 says:** Create response is `{"entries":[...]}` (object with entries
key).

**Spec 07 says:** Server returns HTTP 201 with a JSON array (bare array).

**CLI impact:** None — the CLI passes through whatever `DoRequest` returns
unchanged via `CLIPrintResult` (08-PROP-7). Test assertions check for key
names in stdout without assuming the response wrapper format.
