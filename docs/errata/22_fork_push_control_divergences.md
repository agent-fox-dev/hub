# Errata: Spec 22 — Fork Push Control Divergences

## Spec Expectation

Spec 22 (`22_fork_push_control`) proposed `PUSH_PATCHES_TO_ORIGIN`, the
pre-receive hook rejection, forwarding and mirror behaviours. The following
entries record where the delivered implementation diverges from the original
proposal (GitHub issue #35, "PRD17").

## Implementation Reality

1. **Delete rejection in `origin` mode.** Deletes of a registered patch
   branch are rejected in `origin` mode in both reject and forward modes.
   In forward mode the message is `branch is synced from origin; delete it
   on the fork instead`. The proposal did not specify delete handling. See
   `internal/carrypatch/push_control.go`, the `upd.New == plumbing.ZeroHash`
   check.

2. **`refs/hub/forward/<branch>` temporary ref with 120 s timeout and
   userinfo removal.** The forward goes through a temporary
   `refs/hub/forward/<branch>` ref so that the unreferenced commit is
   resolvable by go-git's push. The forward has a 120 s timeout
   (`forwardTimeout`). URL userinfo is removed from error messages with
   `sanitizeErrorText`, a pattern replacement over the whole message, so a
   URL embedded in a go-git error loses its userinfo and the rest of the
   text is not re-encoded. `stripUserinfo` is kept only for a single URL
   such as the workspace `git_url`. The proposal did not specify the
   forwarding mechanism. See `forwardToOrigin` in `push_control.go`.

3. **Accepted-only audit event and post-push hook; `head_sha` on every
   push.** Only accepted ref updates drive the `hub.git.push` audit event
   and the post-push hook. Rejected ref updates are excluded from both.
   `head_sha` is not gated on accepted refs: it is refreshed from the
   trunk HEAD after every push, including a push in which every ref was
   rejected (22-REQ-5.4). The proposal did not distinguish accepted from
   rejected updates. See `internal/gitserver/handlers.go`,
   `acceptedCommands` and the `22-REQ-5` comments.

4. **Control fails open on lookup failure.** The pre-receive hook
   fails open (fail open) on database lookup errors: if the workspace
   info or patch registration query fails, or the workspace is not found,
   the push is allowed rather than rejected and a warning is logged. The
   proposal did not specify fail-open behaviour. See `push_control.go`,
   the `loadWorkspaceInfo` and `isRegisteredPatchBranch` error paths.

   Variable lookups are not a fail-open path. A lookup error for
   `PUSH_PATCHES_TO_ORIGIN` or `PATCH_BRANCH_SOURCE` resolves to the
   documented default (forwarding disabled, source `hub`), as 22-REQ-1.1
   and 22-REQ-1.2 define, and nothing is logged. Failing open there would
   let an `origin`-mode push through unforwarded, so the hub and the fork
   would diverge.

5. **Mirror runs even when `AUTO_REBUILD_AFTER_PUSH=false`.** The mirror
   is called unconditionally in the post-push hook wrapper, after the
   rebuild enqueue function. The rebuild enqueue respects
   `AUTO_REBUILD_AFTER_PUSH=false` and returns early, but the mirror
   still runs. The proposal did not specify the interaction between the
   mirror and the rebuild variable. See `newPostPushRebuildHookWithLogger`
   in `internal/carrypatch/push_hook.go` and `mirrorBranches` in
   `push_mirror.go`.

6. **`refs/remotes/origin/<branch>` is updated by a forward or mirror.**
   The proposal and the design decisions say `refs/remotes/origin/*` is
   not updated after a forward or mirror. The push goes through go-git's
   `Remote.PushContext`, which writes the tracking ref for each pushed
   ref that matches the remote's fetch refspec once the push succeeds. The
   update is harmless: the tracking ref points at the commit the fork now
   has, which is what a sync would fetch. The push mechanism was not
   changed to avoid it. See `forwardToOrigin` in `push_control.go` and
   `mirrorBranchToOrigin` in `push_mirror.go`.

## Resolution

Each divergence is an implementation detail that improves safety or
robustness. The carry-patch workflow guide documents the delivered
behaviour. No proposal text is changed; the errata serve as the audit
trail.
