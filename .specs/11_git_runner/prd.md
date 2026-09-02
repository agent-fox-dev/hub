---
spec_id: '11'
spec_name: git_runner
title: Git Runner
status: draft
created_at: '2026-08-04T09:46:30.215186+00:00'
updated_at: '2026-08-04T09:51:38.308892+00:00'
owner: ''
source: docs/prd/prd12.md
schema_version: 1
---
# Hardened Git Subprocess Runner

## Intent

The hub executes git CLI commands for operations that go-git cannot perform:
rebase, merge-tree conflict detection, and ls-remote with exit code
discrimination. Every such call must enforce protocol allowlists, suppress
interactive prompts, and ignore system-level config to prevent hangs, security
issues, and environment-dependent behavior.

This package provides a hardened subprocess runner for git CLI operations. It
is used alongside go-git — go-git handles transport-level operations (clone,
fetch, ref manipulation) while the runner handles operations that require the
git CLI (rebase, merge-tree, ls-remote exit codes).

## Goals

- Provide a hardened git subprocess runner that enforces safety defaults on
  every git CLI invocation within the hub.
- Apply safety environment variables (`GIT_ALLOW_PROTOCOL`,
  `GIT_TERMINAL_PROMPT`, `GIT_CONFIG_NOSYSTEM`) to every invocation.
- Provide uniform error formatting with command line, exit code, and stderr.
- Provide three-way exit code discrimination for `git ls-remote --exit-code`.
- Be usable by any hub package that needs git CLI operations.
- Enforce the git >= 2.38 version requirement at construction time, returning
  an error immediately if the constraint is not satisfied.
- Be safe for concurrent use — multiple goroutines may share a single instance.

## Non-Goals

- **Replacing go-git** for operations it handles well (clone, fetch, ref
  manipulation). The boundary: go-git for transport-level and ref operations;
  GitRunner for CLI-only operations (rebase, merge-tree, ls-remote exit codes).
- **High-level merge or sync operations.** Those are separate specs that
  consume this runner.
- **Retry or backoff logic.** Callers handle retries.
- **URL validation.** Remote URL validation is the responsibility of
  application-layer callers (e.g., workspace handlers) before passing URLs to
  GitRunner. The runner is a low-level internal tool and does not inspect or
  restrict remote URLs beyond the `GIT_ALLOW_PROTOCOL` environment variable.
- **Refactoring existing go-git callers.** The `workspace_checkout` and
  `git_server` specs use go-git exclusively (no direct CLI calls) and are not
  affected by the GitRunner mandate. GitRunner applies only to packages that
  need git CLI operations.

## Functional Requirements

### GitRunner

A `GitRunner` wraps git CLI subprocess calls with safety defaults and uniform
error handling. All hub packages that execute git commands via the CLI use this
runner — no direct `exec.Command("git", ...)` calls.

**Initialization:** the runner is created with a working directory and optional
additional environment variables. The constructor executes `git --version` and
returns an error if the detected git version is below 2.38, providing an
immediate, actionable failure rather than a cryptic runtime error when
`merge-tree --write-tree` is later invoked.

```go
func New(workDir string, extraEnv []string) (*GitRunner, error)
```

The `workDir` must be a valid directory path. For non-repo-scoped commands
(e.g., `ls-remote`), any valid directory is acceptable — callers may pass
`os.TempDir()` or a scratch directory. The constructor does not require `workDir`
to be a git repository.

**Goroutine safety:** `GitRunner` is safe for concurrent use by multiple
goroutines. The struct holds only the working directory path and a set of
environment variable strings — both immutable after construction. Each call to
`Run` or any convenience method creates an independent `exec.Cmd` with its own
stdout/stderr buffers. No synchronization primitives are required.

**Safety environment variables** applied to every invocation:

| Variable | Value | Purpose |
|----------|-------|---------|
| `GIT_ALLOW_PROTOCOL` | `file:https:ssh` | Permits local file, HTTPS, and SSH transports — the only transports that provide authentication (`ssh`) or encryption (`https`). `git://` (port 9418, unauthenticated) and `http://` (unencrypted) are intentionally excluded. `file:` is included for local repository operations; rejection of `file://` URLs where inappropriate is the responsibility of application-layer callers. |
| `GIT_TERMINAL_PROMPT` | `0` | Prevents interactive credential prompts from hanging the process. |
| `GIT_CONFIG_NOSYSTEM` | `1` | Ignores system-level git config that could alter behavior. |

**Safety variable precedence:** the three safety environment variables
(`GIT_ALLOW_PROTOCOL`, `GIT_TERMINAL_PROMPT`, `GIT_CONFIG_NOSYSTEM`) are
always appended to the process environment **after** `extraEnv`, so they
unconditionally take precedence. A caller cannot override them via `extraEnv`.
This is intentional — relaxing the protocol allowlist or re-enabling terminal
prompts would undermine the security guarantees that motivate this package.

**Context support:** every command execution accepts a `context.Context` for
cancellation and timeout control.

**Uniform error formatting:** every failed command returns a `*GitError`
containing:

- The full command line (as `[]string`)
- The exit code
- Stderr output (trimmed)

```go
type GitError struct {
    Args     []string
    ExitCode int
    Stderr   string
}

func (e *GitError) Error() string
```

**Three-way exit code discrimination** for remote queries using
`git ls-remote --exit-code`:

| Exit code | Meaning | Return |
|-----------|---------|--------|
| 0 | Branch/ref exists | Success with output |
| 2 | Branch/ref genuinely missing | `ErrRefNotFound` sentinel |
| 1 | Network/auth failure | `*GitError` with stderr |

This prevents misinterpreting a network timeout or auth failure as "branch
does not exist."

### Convenience Methods

The runner provides typed methods for common operations used by downstream
specs. All methods accept a `context.Context` as the first argument.

```go
// Run executes an arbitrary git subcommand with the given args.
// Returns trimmed stdout on success, or *GitError on failure.
func (r *GitRunner) Run(ctx context.Context, args ...string) (string, error)

// LsRemote runs `git ls-remote --exit-code <remote> <ref>`.
// Returns trimmed stdout on success (exit 0), ErrRefNotFound on exit 2,
// or *GitError on exit 1 (network/auth failure).
func (r *GitRunner) LsRemote(ctx context.Context, remote, ref string) (string, error)

// MergeTree runs `git merge-tree --write-tree <base> <head>` for read-only
// conflict detection. Returns the tree SHA string on a clean merge.
// On conflict, returns a non-nil MergeConflictError containing the list of
// conflicting file paths parsed from CONFLICT lines in stdout.
// Callers must use errors.As to extract the conflict paths:
//   var ce *MergeConflictError
//   if errors.As(err, &ce) { /* ce.ConflictingFiles */ }
func (r *GitRunner) MergeTree(ctx context.Context, base, head string) (string, error)

// Rebase runs `git rebase <onto>` on the current branch.
// On success, returns the new HEAD SHA (from git rev-parse HEAD).
// On conflict, calls git rebase --abort internally, then returns a non-nil
// RebaseConflictError containing the list of conflicting file paths.
// Callers must use errors.As to extract the conflict paths:
//   var ce *RebaseConflictError
//   if errors.As(err, &ce) { /* ce.ConflictingFiles */ }
func (r *GitRunner) Rebase(ctx context.Context, onto string) (string, error)

// RebaseAbort runs `git rebase --abort` for edge-case manual recovery —
// e.g., when a context cancellation interrupts a rebase mid-flight and
// the caller needs to clean up the dangling rebase state.
func (r *GitRunner) RebaseAbort(ctx context.Context) error

// RevParse resolves a ref to a SHA via `git rev-parse <ref>`.
func (r *GitRunner) RevParse(ctx context.Context, ref string) (string, error)

// UpdateRef updates a reference via `git update-ref <ref> <sha>`.
func (r *GitRunner) UpdateRef(ctx context.Context, ref, sha string) error
```

**Conflict error types:**

```go
// MergeConflictError is returned by MergeTree when the merge produces conflicts.
type MergeConflictError struct {
    ConflictingFiles []string
}

// RebaseConflictError is returned by Rebase when the rebase produces conflicts.
// Rebase calls git rebase --abort internally before returning this error.
type RebaseConflictError struct {
    ConflictingFiles []string
}
```

**Conflict file path parsing (MergeTree):** `git merge-tree --write-tree`
outputs conflict information to stdout with lines prefixed `CONFLICT`. A
representative line has the form:

```
CONFLICT (content): Merge conflict in path/to/file.go
```

The runner scans stdout line-by-line, identifies lines starting with
`CONFLICT`, and extracts the file path from each such line. The parsing rule
is intentionally left to the implementer to verify against the installed git
version (2.38+) and to document in code comments, since the exact token
structure may vary across git versions. The resulting slice of paths is
returned in `MergeConflictError.ConflictingFiles`. The chosen parsing rule
(regex, prefix strip, or delimiter split) must be documented in the source
alongside the specific git output sample it was derived from.

**Rebase internal abort behavior:** When `Rebase` detects a conflict (non-zero
exit from `git rebase`), it immediately calls `git rebase --abort` internally
before returning a `RebaseConflictError`. `RebaseAbort` is exposed as a
standalone method only for edge-case manual recovery — for example, when a
`context.Context` cancellation interrupts a rebase mid-flight, leaving the
repository in an intermediate rebase state that the caller must explicitly
clean up.

### Package Location

`internal/gitcmd`

### Git Version Requirement

Requires git >= 2.38 on the host (for `merge-tree --write-tree` support used
by downstream consumers). **Enforcement:** the `GitRunner` constructor calls
`git --version`, parses the version string, and returns an error if the
version is below 2.38. This ensures the hub fails fast at startup rather than
producing a cryptic error at the point of first CLI use.

### Testing Strategy

`internal/gitcmd` is tested with **integration tests against a real git
binary**. Tests create temporary repositories, exercise the runner end-to-end,
and assert on outputs and error types. Mocking `exec.Command` is explicitly
avoided — the runner is a thin CLI wrapper and only real git behavior
constitutes a meaningful test. No additional test dependencies beyond the
standard library are required; `testing` and `os` (for temp dir management)
are sufficient.

**Version-check testing:** the version string parser (the function that accepts
a raw `git --version` output string and returns a parsed semver) is tested as
a **pure unit test** with table-driven cases covering valid versions above and
below 2.38, and malformed strings. The constructor integration test assumes the
host git is >= 2.38 and skips if it is not, rather than requiring a fake git
binary on `PATH` or mocking `exec.Command`.

**LsRemote exit-code-1 simulation:** integration tests produce a real exit-
code-1 scenario by pointing `LsRemote` at a syntactically valid but
non-existent remote URL (e.g., `https://localhost:1/nonexistent`). Git returns
exit code 1 on connection refused, exercising the network/auth failure path
without any live network dependency or mock.

Integration test coverage must include:

- Version string parser: pure unit test table covering versions above 2.38,
  below 2.38, and malformed strings.
- Constructor integration test: succeeds when host git is >= 2.38; skips
  otherwise.
- `LsRemote` three-way exit code discrimination: ref present (real local
  repo), ref absent (exit 2), network error simulation via
  `https://localhost:1/nonexistent` (exit 1).
- `MergeTree` clean merge (returns tree SHA) and conflicting merge (returns
  `MergeConflictError` with correct paths; callers use `errors.As` to
  inspect).
- `Rebase` success (returns new HEAD SHA) and conflict (returns
  `RebaseConflictError` with correct paths via `errors.As`; confirms
  `--abort` was called by verifying the repo is no longer in a rebase state).
- `RevParse` and `UpdateRef` round-trip correctness.
- Goroutine safety: concurrent calls from multiple goroutines complete without
  data races (verified with `-race`).
- Safety variable precedence: verify that passing a conflicting value in
  `extraEnv` (e.g., a broader `GIT_ALLOW_PROTOCOL`) does not override the
  hardcoded safety default.

## Technical Boundaries

- **Language:** Go (1.26+) — this is a future-dated spec; 1.26 is the
  project's target Go version as specified in `go.mod`.
- **Dependencies:** Standard library only (`os/exec`, `bytes`, `context`,
  `strings`, `fmt`, `strconv`)
- **Test tooling:** Standard library `testing` package; integration tests
  against a real git binary in a temporary repository. No mock frameworks or
  additional test dependencies.
- **No REST API or CLI surface** — pure internal library

## Design Decisions

This spec was split from a larger PRD (prd12.md — Git Operations Infrastructure)
that covered three independent functional areas: a hardened git subprocess
runner, merge operations, and upstream synchronization. This spec covers only
the git subprocess runner; the other two areas are covered by the
`merge_operations` and `upstream_sync` specs (planned; to be created
immediately after this spec).

1. **go-git / GitRunner boundary.** The hub uses go-git for transport-level
   operations (clone, fetch, ref manipulation) and GitRunner for CLI-only
   operations that go-git does not support (rebase, merge-tree conflict
   detection, ls-remote exit code discrimination). This maximizes library
   usage while providing CLI access where needed. Existing specs
   `workspace_checkout` and `git_server` use go-git exclusively and are not
   in scope for GitRunner adoption.

2. **Convenience methods scope.** The runner provides typed methods (MergeTree,
   Rebase, LsRemote, etc.) for operations known to be needed by downstream
   specs. The method surface is derived from the parent PRD analysis; it will
   be extended when `merge_operations` and `upstream_sync` are formally
   specified. The generic `Run` method is always available for ad-hoc commands.

3. **GIT_ALLOW_PROTOCOL: included and excluded transports.** The allowlist
   `file:https:ssh` is deliberate. `https` and `ssh` are the only transports
   that offer both authentication and encryption, making them safe for
   production remote operations. `git://` (port 9418) is unauthenticated and
   excluded. `http://` is unencrypted and excluded. `file:` is included for
   local repository operations; enforcement of `file://` URL restrictions where
   inappropriate is the responsibility of application-layer callers (workspace
   handlers) that have the necessary business context. The `git_credentials`
   spec stores per-workspace credentials used by those callers.

4. **Goroutine safety via statelessness.** GitRunner is goroutine-safe because
   it is effectively stateless after construction — it holds only an immutable
   working directory path and an immutable environment variable slice. Each
   command execution allocates its own `exec.Cmd`, stdout buffer, and stderr
   buffer, so concurrent invocations cannot interfere with each other. No
   mutex or channel synchronization is needed.

5. **Rebase abort on conflict vs. standalone RebaseAbort.** `Rebase` calls
   `git rebase --abort` internally before returning `RebaseConflictError`,
   ensuring callers never receive a conflict error while the repository is in
   an intermediate rebase state. `RebaseAbort` is exposed separately only for
   the edge case where a context cancellation interrupts a rebase mid-flight
   — in that scenario, the `Rebase` call returns a context error (not a
   conflict error), and the repository may still be in a rebase state that
   requires explicit cleanup.

6. **MergeTree conflict parsing.** `git merge-tree --write-tree` emits
   human-readable `CONFLICT (...)` lines to stdout alongside the tree SHA.
   The runner scans for lines starting with `CONFLICT` and extracts the file
   path from each. The exact parsing rule is deferred to the implementer, who
   must verify the format against the installed git 2.38+ binary and document
   the chosen approach (regex, prefix strip, or delimiter split) in code
   comments alongside a representative sample line. This format is expected to
   be stable across git 2.38+ and is consistent with the version floor
   enforced at construction time.

7. **Working directory for non-repo-scoped commands.** `ls-remote` does not
   require a local repository. Callers constructing a `GitRunner` solely for
   remote queries may pass any valid directory (e.g., `os.TempDir()`). The
   constructor validates only that the path exists; it does not require the
   directory to be a git repository.

8. **Version check in constructor.** Executing `git --version` at construction
   time and returning an error immediately is preferred over documentation-only
   guidance. This provides a clear, actionable failure message and prevents
   silent failures deep in the call stack when `merge-tree --write-tree` is
   first invoked on a host with an older git. The version string parser is
   unit-tested independently; the constructor integration test skips on hosts
   with git < 2.38 rather than requiring a fake binary.

9. **Integration testing strategy.** Because GitRunner is a thin wrapper over
   the git CLI, unit tests with mocked `exec.Command` would verify Go wiring,
   not git behavior. Integration tests with a real git binary and temporary
   repositories provide meaningful coverage of the actual semantics (exit
   codes, output parsing, conflict detection) that the runner is responsible
   for interpreting correctly. The one exception is the version-string parser,
   which is a pure function and is unit-tested in isolation.

10. **Safety variable precedence via append order.** Safety environment
    variables are assembled by appending the three hardcoded values after
    `extraEnv`. Because Go's `exec.Cmd` (and the underlying OS) uses the
    last occurrence of a duplicate variable name, appending after `extraEnv`
    guarantees the safety defaults always win without requiring a scan-and-
    replace pass over the caller's environment. This is the simplest
    implementation that enforces the security invariant.

11. **LsRemote network-error simulation via localhost:1.** Pointing git at
    `https://localhost:1/nonexistent` is a reliable, hermetic way to produce
    exit code 1 (connection refused) without a live network or a mock server.
    Port 1 is privileged and universally unreachable from test processes,
    making this approach stable across CI environments.
