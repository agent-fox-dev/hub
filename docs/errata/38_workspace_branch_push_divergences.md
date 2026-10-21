# Errata: Spec 38 — Workspace Branch Push Divergences

## Spec Expectation

Spec 38 (`38_workspace_branch_push`) adds `POST /api/v1/workspaces/:slug/branches/:name/push`,
which pushes one branch of a workspace's trunk to its `origin` through the push the archive
uses, and `afc workspace push`. The entries below record where the delivered code or its tests
differ from what the spec's text says, with the code and the test of what was delivered.

## Implementation Reality

1. **The shared push takes a progress writer.** Step 1.4 gives `pushToOrigin` the parameters
   `(ctx, repoPath, gitURL, auth, refSpecs, force)`. A forge's reason for refusing a ref (a
   hook's message, a protected-branch rule) reaches go-git only over the sideband, which go-git
   hands to `PushOptions.Progress`; the error itself says no more than `command error on
   refs/heads/<name>: pre-receive hook declined`. `pushToOrigin` (`internal/workspace/push_core.go`)
   therefore also takes `progress io.Writer`; the archive passes nil, and the branch push keeps
   the first 4 KiB, which `forge_rejected` carries scrubbed (R6).
   Test: `TestBranchPushForgeNotFoundAndRejected_TS_38_34`.

2. **go-git's fast-forward check is matched on its text.** The spec assumed the client-side
   check wraps `git.ErrNonFastForwardUpdate`. In go-git v5.19.3 it returns
   `fmt.Errorf("non-fast-forward update: %s", ...)` (`remote.go`), and `ErrNonFastForwardUpdate`
   is the worktree's pull error. `classifyPushError` (`internal/workspace/branch_push.go`) matches
   `non-fast-forward` and `fetch first` in the text, the fallback the spec asks for.
   Test: `TestBranchPushNonFastForward_TS_38_32`.

3. **No force hint when force was given.** R6 ends a `non_fast_forward` message with a force
   hint for any branch but the workspace's own. A forced push can still be refused as a
   non-fast-forward, by a forge that denies such updates (`receive.denyNonFastForwards`, a
   protected branch). Telling that caller to push with force would be wrong, so when `force` was
   given the message ends with "origin refuses non-fast-forward updates of it, also with force"
   (`nonFastForwardMessage`). The status, the error type, the commit and the single listing of
   origin's refs are as R6 says. Test: `TestBranchPushForgeNonFastForward_TS_38_32`.
