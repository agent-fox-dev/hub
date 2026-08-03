---
spec_id: '10'
spec_name: gitcmd
title: Gitcmd
status: draft
created_at: '2026-08-03T12:18:04.539508+00:00'
updated_at: '2026-08-03T12:28:27.709801+00:00'
owner: ''
source: docs/prd/prd8.md
schema_version: 1
---
# Hardened Git Subprocess Runner

## Intent

The hub needs to execute git commands (rebase, merge-tree, ls-remote,
fast-forward push) as subprocesses for merge queue and campaign operations.
Every git subprocess call must enforce safety defaults to prevent protocol
injection, interactive prompt hangs, and system config interference.

This spec provides `internal/gitcmd/`, a small foundational package that wraps
`exec.Command` with security defaults and structured error handling. It is used
by the merge queue, continuous rebase, and any future git operations that
require the git CLI.

## Goals

- Provide a `GitRunner` struct that wraps `exec.Command` with safety
  environment variables applied to every invocation.
- Enforce `GIT_ALLOW_PROTOCOL=file:https:ssh` to prevent `ext::` and other
  dangerous protocol handlers.
- Enforce `GIT_TERMINAL_PROMPT=0` to prevent interactive credential prompts
  from hanging the hub process.
- Enforce `GIT_CONFIG_NOSYSTEM=1` to ignore system-level git config that
  could alter behavior.
- Provide three-way exit code discrimination for remote queries using
  `git ls-remote --exit-code`: exit 0 = branch exists, exit 2 = branch
  missing, any other exit code (1, 128, etc.) = network/auth/system failure.
- Provide uniform error formatting with command, exit code, and stderr
  captured, using the format: `git <args> exited with code <N>: <stderr>`.
- Provide a `CheckGitVersion(ctx context.Context) error` function that
  validates the host git binary meets the minimum version requirement at
  startup.

## Non-Goals

- **Git operations themselves.** This package provides the runner, not the
  merge/rebase/push logic. Those belong in `internal/mergequeue/` and
  `internal/campaign/`.
- **go-git integration.** This package wraps the git CLI only. Clone and push
  operations are handled by go-git (as implemented in `workspace_checkout`);
  `gitcmd` handles only operations go-git does not support: rebase,
  merge-tree, and ls-remote with exit-code semantics.
- **Windows support.** The hub runs on Linux in production and macOS for
  development. Windows is explicitly out of scope.
- **Stdout/stderr size limits.** Commands used by this package (merge-tree,
  rebase, ls-remote) produce small output; unbounded in-memory buffering is
  acceptable.
- **Configurable protocol allowlist.** The `GIT_ALLOW_PROTOCOL` value is a
  security invariant and is never caller-configurable. If a future use case
  requires a different protocol set, it must go through a design review and
  PRD update.
- **Observability and logging.** The package emits no structured logs. All
  observability (logging the full command, duration, exit code, etc.) is
  entirely the caller's responsibility. The package is thin infrastructure;
  logging belongs at the call site where business context is available.
- **Git binary path override.** The package always resolves the git binary via
  PATH. Supporting a configurable binary path is out of scope for this spec and
  can be added later if needed. PATH resolution is standard and sufficient for
  production and development environments.
- **Zero-args guard.** Calling `Run` or `RunExitCode` with no arguments is not
  guarded by the package. The resulting `GitError` wrapping git's own stderr
  (a usage message) is clear enough. No additional programmer-mistake guard is
  warranted.

## Functional Requirements

### GitRunner struct

```go
type GitRunner struct {
    workDir string
    env     []string // caller-supplied additional env vars; safety defaults always appended last
}
```

`NewRunner(workDir string, env ...string)` creates an immutable `GitRunner`
with the working directory set and any additional environment variables
supplied at construction time. The variadic `env` parameter accepts key=value
strings (e.g. `"GIT_AUTHOR_NAME=Bot"`).

The runner is intended to be constructed once per workspace and shared across
goroutines. Because all environment configuration is fixed at construction
time, the runner is safe for concurrent use: multiple goroutines may call
`Run` or `RunExitCode` simultaneously without data races.

**`AddEnv` is not provided.** Callers must supply all additional environment
variables at construction time via the `env ...string` parameter. This design
eliminates the possibility of concurrent mutation of the env slice after
construction.

**Working directory validation:** `NewRunner` performs no validation that
`workDir` exists at construction time. If `workDir` is absent when `Run` or
`RunExitCode` is called, the raw `os/exec` error is returned as-is. The
workspace root is validated at server boot; directories beneath it are created
by the clone queue. No additional validation within this package is warranted.

### Safety environment defaults

Every `exec.Command` created by the runner builds its environment using an
explicit deduplication step that makes the safety-default guarantee
implementation-owned rather than platform-assumed. The construction order is:

1. `os.Environ()` — the process's inherited environment
2. Any entries supplied via `NewRunner`'s `env` parameter — caller-supplied additions
3. Safety defaults — always appended last

Before passing the slice to `exec.Cmd.Env`, the runner removes any earlier
occurrences of the safety-default keys (`GIT_ALLOW_PROTOCOL`,
`GIT_TERMINAL_PROMPT`, `GIT_CONFIG_NOSYSTEM`) from steps 1 and 2. This
explicit deduplication ensures the safety defaults win regardless of
platform-specific `os/exec` duplicate-key resolution behavior, and makes the
guarantee independent of any Go runtime implementation detail.

| Variable | Value | Purpose |
|----------|-------|---------|
| `GIT_ALLOW_PROTOCOL` | `file:https:ssh` | Prevents `ext::` protocol handler injection. **This value is a security invariant and is hardcoded — it is not configurable at construction time or any other point.** |
| `GIT_TERMINAL_PROMPT` | `0` | Prevents interactive prompts from blocking the process |
| `GIT_CONFIG_NOSYSTEM` | `1` | Ignores system-level git config |

### Run method

`Run(ctx context.Context, args ...string) (stdout []byte, stderr []byte, err error)`

Executes `git <args>` in the runner's working directory. The command
environment is constructed as described above (inherited env → caller env →
safety defaults, with explicit deduplication of safety-default keys). Returns
stdout, stderr, and a structured `GitError` on non-zero exit.

If the git binary is not found on `PATH`, `Run` returns the raw `*exec.Error`
(or equivalent `os/exec` error) directly — this is a host misconfiguration,
not a git-level failure, and does not produce a `GitError`.

**Zero-args behavior:** If `Run` is called with no arguments, the package does
not guard against this. `os/exec` will invoke `git` with no subcommand; git
will print a usage message to stderr and exit non-zero. The resulting
`GitError` (with git's stderr included) is clear enough for callers to
diagnose the programmer mistake.

**Context cancellation:** If the context is cancelled or times out,
`exec.CommandContext` sends `SIGKILL` to the subprocess immediately (Go's
default behavior). No graceful shutdown window (SIGTERM) is provided — the
git operations used by this package (rebase, merge-tree, ls-remote) are
expected to be short-lived and idempotent or externally idempotent by their
callers. SIGKILL is therefore acceptable; partial output is returned for
debugging. Whatever stdout and stderr bytes were buffered before cancellation
are returned alongside the context error. Callers must therefore handle the
case where both `err != nil` and `len(stdout) > 0` simultaneously — this is
intentional and the merge queue callers already do so.

### RunExitCode method

`RunExitCode(ctx context.Context, args ...string) (stdout []byte, stderr []byte, exitCode int, err error)`

Same as `Run` but returns the raw exit code separately instead of wrapping it
in the error. Used for commands where non-zero exit codes carry specific
semantics (e.g. `ls-remote --exit-code`).

**Error contract:**
- `err == nil`: the subprocess ran and produced an exit code (including
  non-zero); `exitCode` is meaningful.
- `err != nil`: a system-level failure occurred (binary not found, context
  cancelled/timed out, etc.); `exitCode` is the raw value from
  `exec.ExitError.ExitCode()` and should be treated as secondary context only.

**Error type for system-level failures:** When `RunExitCode` returns
`err != nil` due to a system-level failure (binary not found, context
cancelled, etc.), the error is wrapped using `fmt.Errorf("%w", ...)`,
preserving the underlying `os/exec` error. Callers can therefore use
`errors.Is` and `errors.As` to unwrap to the original `*exec.Error` or
`*exec.ExitError` as needed. No `GitError` is produced by `RunExitCode` —
that type is exclusive to `Run`. This distinction allows upstream callers to
use `errors.As(err, &GitError{})` to unambiguously identify git-level
failures from `Run` versus system-level failures from either method.

**Signal termination and exit code -1:** When context cancellation causes
`SIGKILL`, Go's `exec.ExitError.ExitCode()` returns `-1` (signal termination,
no numeric exit code). `RunExitCode` returns this raw `-1` value directly and
documents it as meaning "signal termination." The merge queue and other callers
treat `err != nil` as the primary signal for failure; `exitCode == -1` provides
additional diagnostic context for logging. Callers must not treat `exitCode`
as meaningful when `err != nil`, except for logging purposes.

A context cancellation surfaces as `err != nil` with `exitCode == -1` (or
possibly `0` if the process had not yet started), consistent with
`exec.CommandContext` behavior. As with `Run`, partial stdout/stderr bytes
accumulated before cancellation are returned alongside the error.

### Three-way exit code discrimination

`BranchExists` is a method on `*GitRunner`, consistent with `Run` and
`RunExitCode`:

```go
func (r *GitRunner) BranchExists(ctx context.Context, remote string, branch string) (bool, error)
```

**Input contract:** The `branch` parameter must be a bare branch name (e.g.
`"main"`, `"feature/foo"`). The method always prepends `refs/heads/` before
passing to `git ls-remote --exit-code`. Callers **must not** pass a full ref
path (e.g. `"refs/heads/main"`) — doing so would produce
`refs/heads/refs/heads/main`, which would silently return `false`. The method
is named `BranchExists` and is scoped to branch refs only; for other ref
namespaces (tags, etc.) a separate method would be required.

Uses `git ls-remote --exit-code <remote> refs/heads/<branch>`:

| Exit code | Meaning | Return |
|-----------|---------|--------|
| 0 | Branch exists | `true, nil` |
| 2 | Branch genuinely missing | `false, nil` |
| 1, 128, or any other non-zero, non-2 code | Network/auth/system failure | `false, error` |

This prevents misinterpreting a network timeout, auth failure, or other
system-level git error as "branch does not exist." In practice, git returns
exit code 128 for many connection and authentication failures (in addition to
exit code 1), so the catch-all for non-zero, non-2 exit codes surfaces all
such failures as errors rather than silently treating them as branch-missing.

> **Caller guidance:** `BranchExists` may block indefinitely if the caller
> passes a non-deadline context against a slow or unreachable remote. Callers
> should set a deadline on the context before invoking this method; a timeout
> of **30 seconds** is recommended for typical network conditions.

### Git version validation

```go
func CheckGitVersion(ctx context.Context) error
```

Runs `git --version`, parses the version string, and returns an error if the
installed git version is below 2.38. The hub calls `CheckGitVersion` during
initialization, before starting the merge queue or any other consumer of this
package. This surfaces a misconfigured host environment early with a clear
error message rather than producing confusing failures during git operations.

**Error message format:** When the installed git version is below the minimum,
`CheckGitVersion` returns an error with the following format:

```
requires git >= 2.38, found <installed>
```

For example:

```
requires git >= 2.38, found 2.35.1
```

This concise, actionable format is consistent for log scanning and operator
remediation.

The minimum version floor (2.38) is required for `merge-tree --write-tree`
support, which is consumed by the merge queue package. This minimum version is
an unexported implementation detail of the package — it is not exposed as a
named constant in the public API, keeping the API surface minimal. The
requirement is documented in CI setup notes and host prerequisites outside the
package; the package itself enforces it at runtime via `CheckGitVersion`.

> **Caller guidance:** `CheckGitVersion` is called once at hub startup.
> Callers should set a short deadline on the context; a timeout of **5 seconds**
> is recommended, as `git --version` is a local binary invocation with no
> network dependency.

**Version string parsing:** `git --version` output is parsed by extracting
the first three dot-separated numeric components (major, minor, patch) and
ignoring any trailing tokens. This ensures compatibility with non-standard
version strings produced on supported platforms:

- Standard: `git version 2.39.1` → parsed as `2.39.1`
- Apple Git (macOS): `git version 2.39.3 (Apple Git-145)` → parsed as `2.39.3`
- Release candidates: `git version 2.38.0.rc1` → parsed as `2.38.0`

Any version string from which three numeric components cannot be extracted
returns a parse error from `CheckGitVersion`.

### Structured error type

```go
type GitError struct {
    Command  string // joined args without the binary prefix, e.g. "rebase origin/main"
    ExitCode int
    Stderr   string
}
```

Implements the `error` interface. The `Command` field holds only the joined
`args` string without the `git` binary prefix (e.g. `"rebase origin/main"`).
The `Error()` method prepends `"git "` when formatting, producing a string in
the exact format:

```
git <args> exited with code <N>: <stderr>
```

For example:

```
git rebase origin/main exited with code 1: error: could not apply abc1234...
```

This means:
- `GitError.Command` = `"rebase origin/main"` (what varies per invocation)
- `Error()` output = `"git rebase origin/main exited with code 1: ..."` (human-readable log line)

Keeping the binary prefix out of `Command` makes the field focused on the
varying part of each invocation and avoids redundancy for `errors.As`
consumers that want to inspect or match the subcommand and arguments
programmatically.

This format is consistent, human-readable, and easy to identify in logs.

`GitError` is always produced by `Run` on non-zero exit. `RunExitCode` does
not produce a `GitError` on non-zero exit (the exit code is returned
directly); it only returns errors for system-level failures, wrapped via
`fmt.Errorf("%w", ...)` to preserve the underlying `os/exec` error for
`errors.Is`/`errors.As` inspection. Upstream callers can use
`errors.As(err, &GitError{})` to distinguish git-level failures (from `Run`)
from system-level failures.

## Observability

The `gitcmd` package emits **no logs**. All observability — including logging
the full command, working directory, duration, exit code, and stderr — is
entirely the caller's responsibility. This design is intentional: the package
is thin infrastructure, and callers have the business context (workspace ID,
operation name, trace ID, etc.) needed to produce meaningful log entries.
Callers should log at the call site using the structured `GitError` fields
(`Command`, `ExitCode`, `Stderr`) for rich diagnostics.

## Testing Strategy

The package uses **integration-style tests against a real git binary** only.
Mocking `exec.Cmd` is explicitly avoided — the value of this package lies in
correctly interfacing with the real git binary, and a mock would test the mock
rather than the safety guarantees.

**Expectations:**

- CI runners must have git >= 2.38 installed (consistent with the production
  requirement).
- Tests initialize a temporary git repository using `t.TempDir()` (which
  provides automatic cleanup at test teardown) and `git init` via the runner.
  Tests that are independent of shared state should call `t.Parallel()` to
  allow concurrent execution; tests that mutate a shared temporary repo must
  not call `t.Parallel()`.
- `BranchExists` is tested against a real local remote (a bare repo created
  in a `t.TempDir()` directory) to cover all three exit-code paths:
  - **Exit 0 (branch present):** push a branch to the bare repo and confirm `true, nil`.
  - **Exit 2 (branch missing):** query a branch name that does not exist on the bare repo and confirm `false, nil`.
  - **Exit 1 / 128 (network/auth failure):** pass an invalid/nonexistent path as the remote URL and confirm `false, error`. The exact exit code (1 or 128) is not asserted — the test asserts only that `err != nil`, consistent with the catch-all discrimination rule.
- `CheckGitVersion` is tested against the installed git binary; a parse error
  test uses a fake version string. Tests cover standard version strings,
  Apple Git vendor strings (e.g. `git version 2.39.3 (Apple Git-145)`), and
  release candidate strings (e.g. `git version 2.38.0.rc1`) to confirm
  correct parsing of the first three numeric components. A test also verifies
  that the error message for a below-minimum version matches the documented
  format `requires git >= 2.38, found <installed>`.
- Safety environment defaults are verified by running a git command that
  echoes or inspects environment variables to confirm the correct values are
  present and that caller-supplied values cannot override them. A test also
  verifies that a caller-supplied value for a safety-default key (e.g.
  `GIT_TERMINAL_PROMPT=1` passed via `NewRunner`'s `env` parameter) is
  stripped by the deduplication step and replaced by the safety default.
- `RunExitCode` signal-termination behavior is tested by cancelling a context
  mid-execution and asserting that `err != nil` and `exitCode == -1`.
- `RunExitCode` system-level error wrapping is tested by verifying that the
  returned `err` can be unwrapped via `errors.Is`/`errors.As` to the
  underlying `os/exec` error (e.g. triggering a "binary not found" scenario
  and asserting `errors.As(err, &*exec.Error{})` succeeds).
- Table-driven tests are used throughout for input/output coverage of error
  formatting and version parsing.

## Technical Boundaries

- **Language:** Go (1.26+, intentionally forward-looking; consistent with the
  project's `go.mod` version target across all specs. Will be updated to the
  stable release version when Go 1.26 ships.)
- **Package:** `internal/gitcmd/`
- **Dependencies:** Standard library only (`os/exec`, `context`, `bytes`,
  `fmt`).
- **Platforms:** Linux (production) and macOS (development). Windows is not in scope.
- **Requires:** git >= 2.38 on the host (for `merge-tree --write-tree`
  support needed by the merge queue, which consumes this package). Validated
  at hub startup via `CheckGitVersion`. This prerequisite must also be
  documented in CI setup notes and host environment prerequisites outside
  this package.

## Dependencies

This spec has no cross-spec dependencies. It is foundational infrastructure
used by the merge queue and campaign specs.

The boundary with existing git-related specs is as follows:
- `workspace_checkout` and `git_credentials` use go-git for clone and push
  operations; those specs are unaffected by and do not depend on `gitcmd`.
- `git_server` implements the smart HTTP git protocol layer; it does not invoke
  git subprocesses through `gitcmd`.
- `gitcmd` is the designated substrate for operations go-git does not support:
  rebase, merge-tree, and ls-remote with structured exit-code semantics.
