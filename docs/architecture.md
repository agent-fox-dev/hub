# Architecture

This document describes the high-level architecture of af-hub.

## Git Server

The git server (`internal/gitserver`) provides smart HTTP git endpoints for
clone, fetch and push operations against workspace repositories. It is mounted
at `/git/<org>/<slug>.git/` and uses HTTP Basic authentication backed by the
hub's credential store.

### Receive-Pack Flow

The receive-pack handler (`handleReceivePack`) processes `git push` requests:

1. **Unpack.** The incoming packfile is decoded and its objects are written
   into the workspace trunk's object store via go-git's `ReceivePack`.
2. **Pre-receive hook.** A registered `PreReceiveHookFunc` is called once per
   ref update, after unpacking and before the hub writes the ref. The hook
   receives the request context, database handle, workspace slug, actor
   identity and the ref update (name, old hash, new hash). Returning a
   non-nil error rejects that single ref update; the error text becomes the
   per-ref status message the client sees (`ng <ref> <message>`). Other refs
   in the same push are processed independently.
3. **Ref write.** The trunk storer wrapper (`thinPackSafeStorer`) intercepts
   `SetReference`, `CheckAndSetReference` and `RemoveReference` to invoke the
   pre-receive hook before delegating to the underlying storer.
4. **Side effects.** Only accepted ref updates (report-status entry `ok`)
   drive `head_sha` updates, the `hub.git.push` audit event and the
   post-push hook. Rejected refs are excluded.

The pre-receive hook is registered via `gitserver.RegisterPreReceiveHook(fn)`,
following the same pattern as `RegisterPostPushHook`. The git server imports
no carry-patch code; the hook is wired in `cmd/af-hub/main.go`.

A `RecoveryHook` is registered via `workspace.RegisterRecoveryHook(fn)`,
following the same nil-default pattern. It carries three operations: reading
the replaced SHA for a slug and branch, removing the backup ref for a slug
and branch, and running a reset-to-origin for a patch. The workspace package
defines the hook type; `carrypatch` implements it; `cmd/af-hub/main.go` wires
them together. Neither package imports the other.

### Carry-Patch Push Control

The carry-patch package registers a pre-receive hook that enforces a
single-writer model for registered patch branches. The behaviour depends on
`PATCH_BRANCH_SOURCE` and `PUSH_PATCHES_TO_ORIGIN`:

- **Reject mode** (`origin` source, forwarding off): the push is refused.
- **Forward mode** (`origin` source, forwarding on): the push is forwarded to
  the fork before the hub writes its ref.
- **Mirror mode** (`hub` source, mirroring on): the push is accepted and then
  mirrored to the fork, best effort.

### Post-Push Hook

After the push, the post-push hook runs asynchronously. It checks whether any
accepted pushed branch is a registered patch and, if
`AUTO_REBUILD_AFTER_PUSH` is not `"false"`, enqueues a rebuild. In hub mode
with mirroring enabled, it also force-pushes each accepted registered patch
branch to the fork.
