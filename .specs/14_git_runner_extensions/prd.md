---
spec_id: '14'
spec_name: git_runner_extensions
title: Git Runner Extensions
status: draft
created_at: '2026-08-16T15:38:49.562149+00:00'
updated_at: '2026-08-16T15:38:49.562149+00:00'
owner: ''
source: docs/prd/proposals/prd_carry_patch_workflow.md
schema_version: 1
---
# Git Runner Extensions

## Source

docs/prd/proposals/prd_carry_patch_workflow.md (split 1 of 3)

## Intent

The carry-patch workflow (see carry_patch_workspace and carry_patch_operations
specs) requires git CLI operations that the current GitRunner (spec 11) does
not provide. This spec extends GitRunner with new convenience methods and
fixes a correctness bug in the sync fast-forward path.

The new methods follow the existing safety and error-handling patterns: typed
error returns, safety environment variables, concurrent-use safety, and
automatic cleanup on conflict (abort before returning error).

Additionally, the existing sync fast-forward (spec 13) updates the branch ref
via `Storer.SetReference` but does not update the working tree. This causes
on-disk files to remain at the old commit after a sync fast-forward. This spec
fixes that for all workspace modes.

## Goals

- Add checkout, branch creation, branch deletion, cherry-pick, config set,
  remote add, log, diff, merge --no-ff, and rebase continue methods to
  GitRunner.
- Provide typed conflict error types for cherry-pick and merge --no-ff,
  consistent with the existing RebaseConflictError pattern.
- Fix the sync working tree staleness bug by resetting the working tree after
  ref advancement during sync fast-forward.

## Non-Goals

- Modifying the sync algorithm beyond the working tree fix.
- Adding any carry-patch-specific logic (workspace mode, patches, rebuild).
- Adding push or remote fetch capabilities to GitRunner (handled by go-git).

## Functional Requirements

### New GitRunner Methods

All new methods follow the existing Run/runWithExitCode pattern. Each method
operates on the repository at the GitRunner's workDir.

#### Checkout

`Checkout(ctx context.Context, ref string) error`

Switches the working tree to an existing branch or detached HEAD via
`git checkout <ref>`. On failure, returns `*GitError`.

#### CreateBranch

`CreateBranch(ctx context.Context, name, startPoint string) error`

Creates a new branch at a specified commit or ref via
`git branch <name> <startPoint>`. On failure (e.g., branch already exists),
returns `*GitError`.

#### DeleteBranch

`DeleteBranch(ctx context.Context, name string) error`

Deletes a local branch via `git branch -D <name>`. Uses `-D` (force delete)
because the branch may not be fully merged. On failure, returns `*GitError`.

#### CherryPick

`CherryPick(ctx context.Context, sha string) (string, error)`

Applies a single commit via `git cherry-pick <sha>`. On success, returns the
new HEAD SHA. On conflict: automatically aborts (`git cherry-pick --abort`)
and returns `*CherryPickConflictError` with conflicting file paths. This
matches the Rebase auto-abort pattern (11-PROP-3).

On context cancellation, returns the context error without aborting (caller
must handle cleanup, same as Rebase).

#### ConfigSet

`ConfigSet(ctx context.Context, key, value string) error`

Sets a repository-local config value via `git config <key> <value>`. On
failure, returns `*GitError`.

#### RemoteAdd

`RemoteAdd(ctx context.Context, name, url string) error`

Adds a named remote with a URL via `git remote add <name> <url>`. On failure
(e.g., remote already exists), returns `*GitError`.

#### Log

`Log(ctx context.Context, args ...string) (string, error)`

Queries commit history via `git log <args...>`. Returns raw stdout. On
failure, returns `*GitError`. The caller controls the output format via args
(e.g., `--oneline`, `--format=%H`, `--reverse`).

#### Diff

`Diff(ctx context.Context, args ...string) (string, error)`

Compares trees or commits via `git diff <args...>`. Returns raw stdout. On
failure, returns `*GitError`.

#### MergeNoFF

`MergeNoFF(ctx context.Context, branch string) (string, error)`

Merges a branch with `--no-ff` via `git merge --no-ff <branch>`. On success,
returns the merge commit SHA. On conflict: automatically aborts
(`git merge --abort`) and returns `*MergeNoFFConflictError` with conflicting
file paths. Follows the same auto-abort pattern as Rebase.

On context cancellation, returns the context error without aborting.

#### MergeAbort

`MergeAbort(ctx context.Context) error`

Aborts a merge in progress via `git merge --abort`. Provided for edge-case
manual recovery after context cancellation during MergeNoFF, same as
RebaseAbort.

#### RebaseContinue

`RebaseContinue(ctx context.Context) (string, error)`

Continues a paused rebase via `git rebase --continue`. Used after rerere
auto-resolves conflicts. On success, returns the new HEAD SHA. On failure,
returns `*GitError`.

#### IsAncestor

`IsAncestor(ctx context.Context, commitA, commitB string) (bool, error)`

Checks whether commitA is an ancestor of commitB via
`git merge-base --is-ancestor <commitA> <commitB>`. Returns true if commitA
is an ancestor (exit code 0), false if not (exit code 1). Other exit codes
return `*GitError`.

### New Conflict Error Types

```go
type CherryPickConflictError struct {
    ConflictingFiles []string
}

type MergeNoFFConflictError struct {
    ConflictingFiles []string
}
```

Both implement the `error` interface with the same message format as
`RebaseConflictError`.

### Conflict file extraction

CherryPick and MergeNoFF parse conflicting file paths from git's stdout/stderr
output. The parsing reuses the existing `parseRebaseConflictFiles` function
(or equivalent) since git uses the same CONFLICT output format for cherry-pick,
merge, and rebase.

If conflict output cannot be parsed but the repository is in a conflicted state
(cherry-pick or merge in progress), a fallback entry `"(unresolved conflict)"`
is included — same pattern as Rebase.

### Sync Working Tree Fix

After advancing the branch ref during sync fast-forward (in
`defaultSyncUpdateLocalRefFn` or equivalent), the working tree must be reset
to match the new HEAD SHA. This applies to **both** standard and carry-patch
workspaces.

The reset is performed by opening the repository with go-git and calling
`worktree.Reset(&git.ResetOptions{Mode: git.HardReset})` after the ref
update. This matches the existing `updateHeadSHA` pattern in
`internal/gitserver/handlers.go`.

If the working tree reset fails, the sync logs an error but does not fail.
The ref update is the critical operation; the working tree will be corrected
on the next push via the git server or next sync.

## Technical Boundaries

- **Language:** Go (1.26+)
- **Package:** `internal/gitcmd` for GitRunner extensions
- **Package:** `internal/workspace` for sync working tree fix
- **Git requirement:** git >= 2.38 on the hub host (inherited from spec 11)
- **Testing:** Unit tests with real git repos in temp directories, following
  the existing test patterns in `internal/gitcmd/*_test.go`

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 11_git_runner | all | 1 | Extends existing GitRunner with new methods |
| 13_upstream_sync | all | last | Sync working tree fix modifies the sync fast-forward path |

## Design Decisions

1. **Cherry-pick returns new HEAD SHA.** Unlike Rebase which returns the final
   HEAD, CherryPick returns the HEAD after the single commit. This supports
   the carry-patch rebuild algorithm which cherry-picks commits one at a time.

2. **DeleteBranch uses -D (force).** The rebuild creates and deletes temporary
   branches that may not be fully merged. Using -d would fail in these cases.

3. **Log and Diff accept variadic args.** Rather than modelling every possible
   flag, these methods pass args through to git. The caller controls formatting.
   This is flexible and follows the Run pattern.

4. **IsAncestor as a dedicated method.** While this could be done via
   `Run("merge-base", "--is-ancestor", a, b)`, the three-way exit code
   discrimination (0=ancestor, 1=not ancestor, other=error) is non-trivial.
   A dedicated method with a boolean return makes the calling code cleaner.

5. **Working tree fix is best-effort.** The ref update is the authoritative
   state change. A failed working tree reset is logged but doesn't fail the
   sync, because the working tree will self-correct on the next mutation
   (push, rebuild, sync).

