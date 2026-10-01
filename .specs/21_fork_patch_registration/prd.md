---
spec_id: "21"
spec_name: "fork_patch_registration"
title: "Register Patch Branches That Exist Only on the Fork"
status: "active"
created_at: "2026-10-01T14:28:07.234565Z"
updated_at: "2026-10-01T14:28:07.234565Z"
intent_hash: "568526339e89653bd9228b1295e17406998b5c4c3af7cb081d73d1c488d5dcbd"
schema_version: 2
source: "https://github.com/agent-fox-dev/hub/issues/35"
---
## Intent

A patch can only be rebuilt from `refs/heads/<branch>` in the workspace trunk, but a carry-patch workspace is a clone of a fork, so a branch that lives only on the fork exists in the trunk only as `refs/remotes/origin/<branch>`, or not at all. This spec makes patch registration (`POST /workspaces/:slug/patches`, single and batch, and `afc patch add` on top of it) find such a branch and give it a local ref, so that registering a fork branch no longer needs `skip_branch_check`. Registration looks at the local ref first, then the clone's remote-tracking ref, then, only when the fork is the authority for patch branches, the fork itself. How a branch was resolved is recorded in the audit trail.

## Goals

- `POST /workspaces/:slug/patches` accepts a branch that has no `refs/heads/<name>` but has `refs/remotes/origin/<name>`, in both `PATCH_BRANCH_SOURCE` modes. The local branch is created at the tracking tip before the patch row is inserted.
- When `PATCH_BRANCH_SOURCE` is `origin` and neither ref exists, registration fetches that one branch from the fork and retries the tracking-ref check. In `hub` mode it never contacts the fork.
- A branch found nowhere is rejected with `400` and the message `branch does not exist in repository or on origin`. In a batch the message is prefixed `patch[i]: ` as today.
- `skip_branch_check: true` keeps its meaning: no lookup, no fetch, no local branch created.
- A batch stays all-or-nothing for rows. Every element is resolved before any row is inserted.
- The single-patch `hub.patch.create` audit event records how the branch was resolved.
- `afc patch add` needs no new flags. Its help text, `docs/cli.md`, `docs/api.md` and `docs/openapi.yaml` describe the new behaviour.
- Existing request and response shapes are unchanged. Registration of a branch that already exists locally behaves as today, except as listed in Design Decision 3.

## Non-goals

This is the second of five scopes. The later scopes each get their own spec, and nothing below is built here.

- **`fork_push_control`**: `PUSH_PATCHES_TO_ORIGIN`, rejecting or forwarding hub pushes to registered patch branches, the hub-mode mirror, and `hub.patch.mirror_failed`.
- **`patch_divergence_recovery`**: `reset-to-origin`, `replaced_sha`, protecting `refs/hub/replaced/*`, and backup-ref cleanup.
- **`patch_authority_docs`**: `docs/carry_patch_workflow.md`, `docs/examples/AGENTS_carry_patch.md`, and ADR 01 status and errata.
- **Defining `PATCH_BRANCH_SOURCE`**, the origin fetch during sync, and the origin credential resolution. These come from `20_fork_patch_sync`. This spec reads the variable and reuses the credential resolution.
- **Keeping the branch current afterwards.** Registration creates the local ref once. Bringing it to the fork's tip on later syncs is `20_fork_patch_sync`.
- **Adding audit events to batch registration.** Batch registration emits no `hub.patch.create` events today, and that does not change here.
- **Changing validation that comes before the branch check**: branch-name syntax, rejection of the integration branch, position range, duplicate and already-registered checks, `if_not_exists`.
- **A fork branch filter, a registration-time schedule, or fetching more than the single requested branch.**
- **A new HTTP endpoint, a new permission scope, or a new CLI command.**

## Background

**Registration today.** `handleAddPatchSingle` and `handleAddPatchBatch` (`internal/workspace/patch_handlers.go`) call `branchCheckHook(slug, name)` unless `skip_branch_check` is true. A hook error answers `400 branch does not exist in repository` (batch: `patch[i]: branch does not exist in repository`). If no hook is registered, no check runs. The hook type `BranchCheckFunc func(slug, branchName string) error` and `RegisterBranchCheckHook` live in `internal/workspace/branch_check_hook.go`. Existing tests in `internal/workspace/carry_patch_patches_test.go` register stub hooks with this signature.

**The hook in production** is an inline closure in `cmd/af-hub/main.go`. It opens a runner on `<workspace_root>/<slug>/trunk` through `cpGitRunnerFactory` and runs `rev-parse --verify <branchName>`. That accepts any revision (a tag, a SHA, `origin/x`), not only a local branch. The rebuild uses only `refs/heads/<branch>`, so a patch registered through such a revision would later be skipped with `branch_not_found`.

**What the trunk holds.** The initial clone brings every fork branch as `refs/remotes/origin/*`, and only HEAD becomes a local branch. A branch pushed to the fork after the clone is not in the trunk at all until something fetches `origin`.

**Reusable pieces.**
- `carrypatch.GitRunner` and its factory `carrypatch.NewGitRunnerFactory()` provide `Run` and `UpdateRef`. `gitcmd.GitRunner.RevParse` runs `rev-parse --verify --end-of-options <ref>`.
- `upstream.Fetch` (`internal/upstream/upstream.go`) shows the go-git fetch pattern: `git.PlainOpen`, `repo.Remote`, `remote.FetchContext` with `RefSpecs`, `Auth` and `Tags: git.NoTags`, with `git.NoErrAlreadyUpToDate` treated as success.
- `workspace.ResolveCloneAuth(store, slug)` (`internal/workspace/clone_auth.go`) resolves origin credentials: `GIT_PAT`, then `GIT_USERNAME`/`GIT_PASSWORD`, then none.
- Workspace variables are read through `store.GetVariableValue(ownerType, ownerID, key)`. An unset variable returns an error, which callers treat as the default.
- `validateBranch` already rejects `:`, `*`, `?`, `[`, `\`, `^`, `~`, spaces, `..` and a leading dot. A validated name is therefore safe to put into a refspec.
- `wslock.TryLock(slug)` is how carry-patch handlers serialise on the trunk. A busy lock answers `409` with error type `workspace_busy` and the message `another operation is running on this workspace; retry later` (`internal/carrypatch/api.go`, `rerere_handlers.go`).
- Carry-patch hooks follow one pattern: the `workspace` package defines the hook type, `carrypatch` implements it, and `cmd/af-hub/main.go` registers it. The pattern exists because `workspace` and `carrypatch` do not import each other (`RegisterCarryPatchSyncHook` and `carrypatch.NewCarryPatchSyncHook` are an example).
- `emitHubAudit` and `audit.HubEvent` are used by the single-patch path for `hub.patch.create`.

**What `20_fork_patch_sync` provides** and this spec builds on: the `PATCH_BRANCH_SOURCE` variable (`hub` or `origin`, exact and case-sensitive match, anything else means `hub`), the origin credential resolver, the `502` message `origin fetch failed` with error type `origin_fetch_failed`, and the go-git origin fetch. Registration needs a single-branch variant of that fetch, not the wildcard-with-prune fetch.

## Requirements

### 1. Resolution order

For every non-skipped registration, a branch name is resolved in this order. The first step that succeeds ends the resolution.

1. **`local`**: `refs/heads/<name>` exists and resolves to a commit.
2. **`origin_tracking`**: `refs/remotes/origin/<name>` exists and resolves to a commit. The local branch `refs/heads/<name>` is created at that commit.
3. **`origin_fetch`**: only when `PATCH_BRANCH_SOURCE` is `origin`. The branch is fetched from the fork, and if `refs/remotes/origin/<name>` now resolves to a commit, the local branch is created at that commit.

Existence checks use fully qualified refs (`refs/heads/<name>^{commit}`), not bare names. A tag, a SHA or a remote-qualified name is therefore no longer accepted as a branch (Design Decision 3).

`PATCH_BRANCH_SOURCE` is read at the start of each registration request, so a change takes effect on the next request. The parsing rules are those of `20_fork_patch_sync`, and the same helper is used so the two cannot drift. An unset variable, a lookup error or an unrecognised value means `hub`.

When no branch-check hook is registered, nothing is resolved and registration accepts the branch, as today.

### 2. Creating the local branch

The local branch is created with `update-ref refs/heads/<name> <sha> <all-zero-sha>`, so the write fails if the ref appeared in the meantime. It never uses `checkout`, `branch` or `reset`, and it never touches the trunk's working tree or HEAD.

If the compare-and-swap create fails because the ref now exists (a hub push landed in between), resolution is re-run from step 1 and reports `local`. Any other failure to write the ref is an internal error: `500 internal server error`, with no patch row inserted. The error is logged with the slug and branch.

The new local branch points at the fork tip at that moment. Registration does not set upstream tracking configuration.

### 3. The single-branch fork fetch

Step 3 of the resolution runs a go-git fetch of the `origin` remote of the trunk with:

- the single refspec `+refs/heads/<name>:refs/remotes/origin/<name>`;
- no tags;
- no pruning;
- credentials from `workspace.ResolveCloneAuth` (`GIT_PAT`, then `GIT_USERNAME`/`GIT_PASSWORD`, then none), the same credentials the clone and the integration-branch push use.

An already-up-to-date result is success. The fetch never runs in `hub` mode, and origin credentials are never resolved in `hub` mode.

If the fork does not have the branch, the fetch fails with a "no matching ref" style error. That is classified as "branch not found on origin" and the request answers `400` (Requirement 4). Any other fetch failure is classified as an origin fetch failure: the request answers `502`, message `origin fetch failed`, error type `origin_fetch_failed`, the same message and type `20_fork_patch_sync` uses for sync. A failure to resolve origin credentials answers `502` with the message `failed to resolve origin credentials`. No patch row is inserted and no local branch is created for the failing element. The error text is logged and is not echoed into the response.

### 4. Errors for a branch found nowhere

When steps 1 to 3 (or steps 1 and 2 in `hub` mode) all fail to find the branch, the request answers `400` with the message `branch does not exist in repository or on origin`. In a batch the message is `patch[i]: branch does not exist in repository or on origin`, with `i` the index of the first failing element. The message is the same in both modes, even though `hub` mode never contacts the fork. The `400` and `502` messages are documented as above in `docs/api.md` and `docs/openapi.yaml`.

### 5. Skipping the check

`skip_branch_check: true` means none of Requirements 1 to 4 runs for that element: no ref lookup, no fetch, no credential resolution, no local branch, and no workspace lock taken. This holds in both modes and, in a batch, per element. A skipped element does not affect how its neighbours resolve.

### 6. Batch registration

Every element is validated and resolved before any row is inserted, in the order the elements appear in the array. The first failure (a `400` missing branch, a `502` fetch failure, a `409` busy workspace or a `500`) rejects the whole request, and no row is inserted.

Local branches already created for earlier elements stay in place when a later element fails. They are harmless refs, and the next registration finds them as `local`. Fork fetches already completed are likewise kept.

Duplicate-in-batch and already-registered checks keep their position after the resolution loop, so a batch that is going to answer `409` may still have created local branches. This keeps error precedence as it is today.

### 7. Concurrency

Step 1 (`local`) reads refs and takes no lock. Before step 2 or step 3 writes anything (a tracking-based branch create, or a fetch followed by a create), the handler takes the workspace lock with `wslock.TryLock(slug)` and holds it until that element's resolution finishes. After taking the lock it re-checks step 1, because a push may have created the branch while it waited. Sync holds the same lock while it fetches `origin`, so the two cannot write the same tracking refs at once.

If the lock is busy, the request answers `409` with error type `workspace_busy` and the message `another operation is running on this workspace; retry later`, the same as the other carry-patch handlers. No row is inserted. The lock is not held across the database insert, and it is not taken on the `local` path or for skipped elements.

Hub git pushes do not take the workspace lock. The compare-and-swap create of Requirement 2 is what makes a concurrent push safe.

### 8. Audit

The single-patch `hub.patch.create` event gains one metadata key, `branch_resolution`, next to the existing `branch_name` and `position`. Its value is `local`, `origin_tracking` or `origin_fetch`, or `skipped` when `skip_branch_check` was true. The key is omitted when no hook is registered. The event's other fields are unchanged. An `if_not_exists` hit that returns the existing record emits no event, as today.

### 9. Hook contract and wiring

The branch-check hook is replaced by a resolver hook defined in `internal/workspace/branch_check_hook.go`. It takes a context, the slug and the branch name, and returns the resolution method and a classified error. The errors distinguish at least: branch not found, origin fetch failed (including credential resolution failure), workspace busy, and any other failure. The handler maps them to `400`, `502`, `409` and `500` as described above.

The implementation lives in `internal/carrypatch` and is registered from `cmd/af-hub/main.go`, replacing the inline `rev-parse --verify` closure. It takes the git runner factory, the workspace root, the variable getter (`store.GetVariableValue`), the origin credential resolver of `20_fork_patch_sync`, and an injectable single-branch fetch function so tests can substitute a counting stub. The `workspace` package does not import `carrypatch`, and `carrypatch` does not import `workspace`.

Existing tests that register a `func(slug, branch string) error` stub are updated to the new hook type. The hook remains optional: when none is registered, registration accepts the branch.

### 10. Tests and documentation

Verification, using real git repositories (a bare fork, a clone as the trunk) for every case that touches refs:

- Single and batch registration, in both modes, for each resolution path: `local` (no ref written), `origin_tracking` (local ref created at the tracking tip), and `origin_fetch` (branch pushed to the fork after the clone, so only the fetch finds it).
- The `400` message in both modes, single and batch, with no row inserted and no local branch created for the failing branch.
- `hub` mode performs zero fetches and zero credential lookups, using a counting stub, including when the branch exists nowhere.
- `skip_branch_check` performs no lookup, no fetch and no ref write in both modes, and mixed batches treat each element separately.
- A batch whose second element is missing leaves the first element's local branch in place and inserts no rows.
- A bare name that is only a tag or a SHA is rejected.
- A fetch against a fork that lacks the branch gives `400`. A fetch against an unreachable fork gives `502` with error type `origin_fetch_failed`. A credential resolution failure gives `502`.
- A busy workspace lock gives `409 workspace_busy` on the fetch and create paths, and does not block the `local` and `skipped` paths.
- A branch that appears between the first check and the create is reported as `local`.
- `PATCH_BRANCH_SOURCE` changed between two requests takes effect on the second.
- The audit event carries `branch_resolution` for each path, including `skipped`.
- The CLI test for `afc patch add` still passes unchanged, since it sends the same body.

Documentation, per the project steering, in the same change:
- `docs/api.md`: the registration section (lines about `skip_branch_check`, the validation note and the `400` row) gains the resolution order, the new `400` message, the `409` and `502` responses and the audit metadata.
- `docs/openapi.yaml`: the `POST /workspaces/{slug}/patches` description, its `400` description and added `409`/`502` responses, and the `skip_branch_check` property description.
- `docs/cli.md`: the `afc patch add` entry and the `--skip-branch-check` flag text.
- `docs/carry_patch_workflow.md` is not changed here. `patch_authority_docs` rewrites it.

## Design Decisions

1. **The hook is replaced rather than a second hook added.** The input places PR-1 "in the branch-check hook". Two hooks (one boolean, one resolving) would leave two sources of truth. The cost is updating the four existing test stubs.
2. **The resolution logic lives in `carrypatch`, with the hook type in `workspace`.** This follows the sync-hook pattern, avoids an import cycle, and keeps git and go-git out of the workspace package.
3. **Existence is checked with `refs/heads/<name>`, not a bare name.** The input requires this order, and the rebuild only reads `refs/heads/*`. Today's `rev-parse --verify <name>` accepts tags, SHAs and `origin/x`, which the rebuild would later skip as `branch_not_found`. The side effect is that those inputs are now rejected at registration. This is a tightening, and `skip_branch_check` is still available.
4. **Fork-fetch failures other than "branch not found" answer `502`, not `400`.** The input says that if none of the paths succeeds, the request fails with `400`. Reporting an expired token or a network outage as "branch does not exist" would send an operator to the wrong place. `20_fork_patch_sync` already reserves `502 origin fetch failed` for the same condition. A genuinely missing branch still answers `400`.
5. **A busy lock answers `409` rather than waiting.** Every other carry-patch handler does the same, and a long sync should not hang a registration request.
6. **The lock is taken only when something will be written.** The common case (a branch already local) is a read and must not start failing with `409` during a sync. `hub`-mode registration with a branch in tracking refs does need the lock for the create, which is a small, brief hold.
7. **The local branch is created with a compare-and-swap `update-ref`.** Git-server pushes do not take the workspace lock, so a push can create the same branch concurrently. Losing the race is treated as `local`, because the pushed ref is the hub's own.
8. **The single-branch fetch does not prune and does not fetch tags.** Only one named tracking ref is of interest, and pruning is sync's job.
9. **A branch in `hub` mode is created from the tracking ref once, and from then on belongs to the hub.** The input states this, and `fork_push_control` and `20_fork_patch_sync` decide what happens next. This spec does not mark the patch with its origin.
10. **`origin_fetch` is only tried in `origin` mode.** A `hub`-mode workspace never contacts the fork outside clone, archive and the integration-branch push, which keeps today's network behaviour for existing workspaces.
11. **The `400` message is the same in both modes.** The input specifies one message. A mode-specific message would make clients branch on text.
12. **Batch registration does not gain audit events.** The input's PR-4 names `hub.patch.create`, which only the single path emits. Adding batch events changes the audit volume for an existing endpoint and is a separate decision.
13. **`skipped` is added as a fourth `branch_resolution` value.** The input lists three. Without a value for the skipped case, a reader of the audit trail could not tell "not checked" from "hook not registered".
14. **Resolution runs before the existence checks, as the branch check does today.** This keeps today's error precedence (`400` before `409`, and before `if_not_exists`). It means a request that later answers `409` may have created a local branch, which the input accepts.
15. **No new CLI flag.** The CLI sends the same body, and the server decides by mode. A flag would duplicate `PATCH_BRANCH_SOURCE`.

## Dependencies

| Spec | Relationship | Why |
|------|--------------|-----|
| `20_fork_patch_sync` | depends | It defines `PATCH_BRANCH_SOURCE` and its parsing rules, the origin credential resolver, the `502 origin fetch failed` message and `origin_fetch_failed` error type, and the origin go-git fetch pattern that the single-branch fetch is modelled on. |
| `15_carry_patch_workspace` (archived) | modifies | Patch registration in `internal/workspace/patch_handlers.go`, the branch-check hook, and the `hub.patch.create` audit metadata. |
| `09_git_credentials` (archived) | depends | `workspace.ResolveCloneAuth` for the fork credentials. |
| `18_hub_audit_query` (archived) | modifies | The metadata of the existing `hub.patch.create` event gains `branch_resolution`. |

## Verified External API

`github.com/go-git/go-git/v5` v5.19.1 (go.mod). Its source is outside the repository and could not be read. The signatures below come from call sites in `internal/upstream/upstream.go` and `internal/carrypatch/wire.go`.

| Symbol | Signature as used in this repo | Status |
|---|---|---|
| `git.PlainOpen` | `func PlainOpen(path string) (*Repository, error)` | verified by call site |
| `(*Repository).Remote` | `func (r *Repository) Remote(name string) (*Remote, error)` | verified by call site |
| `(*Remote).FetchContext` | `func (r *Remote) FetchContext(ctx context.Context, o *git.FetchOptions) error` | verified by call site |
| `git.FetchOptions` fields `RemoteName`, `RefSpecs []config.RefSpec`, `Auth transport.AuthMethod`, `Tags git.TagMode` | all set in `internal/upstream/upstream.go` | verified by call site |
| `git.NoTags`, `git.NoErrAlreadyUpToDate` | used in `upstream.go` and `wire.go` | verified by call site |
| `config.RefSpec` | `type RefSpec string` | verified by call site |
| The error go-git returns when a fetch refspec matches no remote ref | assumed to be `git.NoMatchingRefSpecError` (matched with `errors.As`) | **unverified.** It is not used anywhere in the repo. The integration test against a real bare fork that lacks the branch must confirm which error comes back and that it is distinguishable from a transport or auth failure. If it is not distinguishable by type, the implementation must instead decide "branch not found" by listing the remote's refs, and must not match on error text alone. |

Repository symbols this spec calls, all read in this repo: `carrypatch.NewGitRunnerFactory`, `carrypatch.GitRunner.Run`, `workspace.ResolveCloneAuth(store *secrets.Store, slug string) (transport.AuthMethod, error)`, `(*secrets.Store).GetVariableValue(ownerType, ownerID, key string) (string, error)`, `wslock.TryLock(slug) (unlock func(), ok bool)`, `apikit.WriteAPIError` and `apikit.WriteAPIErrorWithType`, `workspace.validateBranch`, and the `20_fork_patch_sync` helpers (the `PATCH_BRANCH_SOURCE` parser and the origin credential resolver, names fixed by that spec's implementation, not yet in the code).
