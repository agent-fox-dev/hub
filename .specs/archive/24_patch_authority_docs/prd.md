---
spec_id: "24"
spec_name: "patch_authority_docs"
title: "Document Patch Branch Authority in the Carry-Patch Guide, Agent Example and ADR 01"
status: "active"
created_at: "2026-10-01T14:51:21.710242Z"
updated_at: "2026-10-01T14:51:21.710242Z"
intent_hash: "a39d73b15fb11e54c6537e66c8b161efc50e0a4b8cdeff0c812d1556d983e6cc"
schema_version: 2
source: "https://github.com/agent-fox-dev/hub/issues/35"
---
## Intent

The carry-patch workflow guide, the agent-instructions example and ADR 01 must describe the two patch-branch authority models (`hub` and `origin`) accurately, once specs `20_fork_patch_sync`, `21_fork_patch_registration`, `22_fork_push_control` and `23_patch_divergence_recovery` have changed what the hub does. This spec rewrites those documents so that an operator or coding agent reads one consistent account of where patch branches live and what to do day to day. The fork-authoritative model is presented as the recommended path for teams that open upstream pull requests. It also records ADR 01 as accepted and files errata for every place the delivered behaviour differs from the original proposal. It changes no code behaviour.

## Goals

- `docs/carry_patch_workflow.md` is correct for both authority models. Its Remotes table no longer claims `origin` is where patch branches live without qualification. It has a "Where patch branches live" section that explains `PATCH_BRANCH_SOURCE`. Its Getting Started walkthrough shows the fork-authoritative flow as the primary path and the hub flow as the alternative for agent-driven workspaces.
- The guide's sync, conflict-resolution, patch-lifecycle, push, configuration, limitations and endpoint-summary text agrees with the behaviour specified by specs 20 to 23. No sentence in the guide contradicts them.
- The guide's "Resolving conflicts after a failed rebuild" section (and the matching Getting Started step) describes both modes. The `origin` mode instructions do not tell the reader to edit the trunk or push to the hub.
- `docs/examples/AGENTS_carry_patch.md` tells an agent to check `PATCH_BRANCH_SOURCE` before it decides where to push a patch branch, and its workflow, sync, conflict and quality-gate text works in both modes.
- ADR 01 has status `Accepted` and points to the errata.
- Errata under `docs/errata/` record each divergence of the delivered behaviour from the original proposal (GitHub issue #35, "PRD17").
- A documentation test in the repository's existing docs-test style fails if the key content above disappears. All existing docs tests keep passing.

## Non-goals

This is the fifth of five scopes. Specs 20 to 23 are written and own all behaviour. Nothing below changes code, routes, CLI commands, variables or schemas.

- **`fork_patch_sync`** (`20_fork_patch_sync`): the origin fetch, the patch refresh, the divergence policy and the sync response fields. This spec documents them.
- **`fork_patch_registration`** (`21_fork_patch_registration`): branch resolution at registration. This spec documents it.
- **`fork_push_control`** (`22_fork_push_control`): `PUSH_PATCHES_TO_ORIGIN`, the pre-receive hook and the mirror. This spec documents them.
- **`patch_divergence_recovery`** (`23_patch_divergence_recovery`): `reset-to-origin`, `replaced_sha`, protection of `refs/hub/replaced/*` and backup cleanup. This spec documents them.
- **Rewriting `docs/api.md`, `docs/openapi.yaml`, `docs/cli.md` and `docs/permissions.md`.** Specs 20 to 23 each update the reference text for what they add. This spec only reads them to keep the guide consistent and corrects them only where they contradict the guide after the rewrite (see Requirement 8).
- **Creating `docs/architecture.md`.** The file does not exist, and this spec changes no architecture. Specs 22 and 23 own any architecture text.
- **The four open questions of the proposal**: feeding manual conflict resolutions from a fork push into rerere, auto-disabling a `missing_on_origin` patch, a built-in sync schedule, and restricting the fork fetch to registered branch names. The guide states them as current limitations, not as planned features.
- **Scheduling the purge of soft-deleted patches.** The guide describes what the code does at implementation time (Requirement 3).
- **A new PRD file under `docs/prd/`.** The directory does not exist and the PRDs of this work live under `.specs/`.

## Background

**The guide today** (`docs/carry_patch_workflow.md`):

- The Remotes table says `origin` is "Where patch branches live; push target for local work". The code never reads patch branches from `origin` (ADR 01, Context).
- Getting Started steps 3 and 4 and "Resolving conflicts after a failed rebuild" tell the reader to resolve conflicts "in the workspace trunk (or via the hub's git server)". Step 4 shows a sync response with four extra fields and says sync returns immediately when upstream is unchanged.
- "How it works / Sync algorithm" lists five steps: fetch `upstream`, return if unchanged, force-push detection, merge detection, auto-rebuild.
- "Auto-rebuild on push" describes the push hook as the only push-time behaviour.
- The Patch list table has no sync-state fields. The patch endpoint table has no single-patch `GET` and no reset endpoint.
- The Configuration section documents `REBUILD_PUSH_INTEGRATION_BRANCH`, `REBUILD_STRATEGY`, `REBUILD_FAIL_MODE`, `AUTO_REBUILD_AFTER_SYNC`, `AUTO_REBUILD_AFTER_PUSH` and `SQUASH_MERGE_DETECTION`.
- The soft-delete text says a background purge removes expired patches after 7 days. Spec 23 found that nothing in production calls the purge routines (`PurgeDeletedPatches`, `PurgeExpiredDeletedPatches` in `internal/carrypatch`), so that statement does not match the code.
- Limitations include the lock note ("**Concurrent rebuild prevention.**") and the rebuild-algorithm text written for `01_rebuild_worktree`.

**What specs 20 to 23 will have already edited in the guide** (their own narrow edits, listed in their PRDs): Configuration entries for `PATCH_BRANCH_SOURCE`, `PATCH_DIVERGENCE_POLICY` and `PUSH_PATCHES_TO_ORIGIN`; a short interim note under the sync section about the two models and about hub pushes in `origin` mode; and two rows in the patch endpoint table. This spec reconciles those edits into the restructured guide. The interim note is replaced, not kept beside the new section.

**Existing docs tests** (`internal/carrypatch/docs_test.go`) assert content of the guide and the agent example. The guide must keep a `### Rebuild algorithm` section that mentions worktree, `update-ref` and follow-up, the phrases "does not change the trunk", "pre-rebuild integration branch", "follow-up rebuild" and "stale", the `**Concurrent rebuild prevention.**` note, and no `_rebuild_temp` outside a legacy note. The agent example must mark every `_rebuild_temp` mention as legacy. ADR 02 has a test too.

**The agent example** (`docs/examples/AGENTS_carry_patch.md`) tells agents to clone from the hub, create a patch branch, `git push origin patch/<name>` to the hub, register it, and run `afc rebuild submit`. In `origin` mode a hub push is refused (or forwarded when `PUSH_PATCHES_TO_ORIGIN=true`), and a rebuild alone does not fetch the fork, so the agent must push to the fork and run `afc workspace sync`.

**ADR 01** (`docs/adr/01-choose-the-authority-for-patch-branches.md`) is `Proposed`, dated 2026-09-29. Its Context describes the code before specs 20 to 23. ADR 02 shows the format for an accepted ADR (Status, Date).

**Errata conventions** (`AGENTS.md`, `docs/errata/16_rerere_entries_by_id.md`): files are `NN_snake_case_topic.md` where NN is the spec number; each file has the sections "Spec Expectation", "Implementation Reality" and "Resolution".

**Behaviour to document, taken from the PRDs of specs 20 to 23** (these PRDs describe it, but the code does not contain it yet; see Requirement 1 for how conflicts between a PRD and the final code are resolved):

- Variables: `PATCH_BRANCH_SOURCE` (`hub` default, `origin`), `PATCH_DIVERGENCE_POLICY` (`replace` default, `report`), `PUSH_PATCHES_TO_ORIGIN` (exact `true`).
- Sync in `origin` mode: upstream fetch, origin fetch with pruning, patch refresh with the outcomes `created`, `fast_forwarded`, `replaced` and the states `in_sync`, `diverged`, `missing_on_origin`; backup ref `refs/hub/replaced/<branch>`; a moved `active` or `conflict` patch triggers the same auto-rebuild as an upstream advance; `last_sync_at` is written on every completed sync; response fields `origin_fetched`, `patches_synced`, `patches_diverged`; `afc workspace sync --fail-on-diverged` exits 3; per-patch `origin_sync_state`, `origin_sha`, `origin_synced_at` and the dashboard counts.
- Registration: `refs/heads/<name>`, then `refs/remotes/origin/<name>`, then (`origin` mode only) a single-branch fork fetch; `400 branch does not exist in repository or on origin`; `skip_branch_check` unchanged.
- Pushes: in `origin` mode without forwarding, a push to a registered patch branch is rejected with `branch is synced from origin; push to <git_url> instead`; with `PUSH_PATCHES_TO_ORIGIN=true` it is forwarded without force before the hub accepts it; in `hub` mode with the variable `true`, accepted pushes are force-mirrored to the fork, best effort, with `hub.patch.mirror_failed` on failure.
- Recovery: `GET /workspaces/:slug/patches/:id` with `replaced_sha`; `POST .../patches/:id/reset-to-origin` and `afc patch reset-to-origin`; recovery of a replaced tip with `git fetch <hub_url> refs/hub/replaced/<branch>`; backup refs deleted by `afc patch remove` and by purge; client pushes to `refs/hub/replaced/` rejected.

## Requirements

### 1. Source of truth for what is documented

The text written by this spec describes the behaviour the code has when this spec is implemented. The implementer reads the shipped code of specs 20 to 23 (handlers, hooks, tests) and the docs those specs updated (`docs/api.md`, `docs/openapi.yaml`, `docs/cli.md`) before writing. Where the code and a spec PRD differ, the code wins and the difference is added to the errata (Requirement 7). Variable names, values, message strings, field names, endpoint paths, exit codes and event types are copied from the code and never paraphrased. A claim about behaviour that cannot be found in the code or in a test is not written.

Verification: a reviewer can trace every quoted message, field and command in the guide to the code or to `docs/api.md`/`docs/cli.md`. The documentation test of Requirement 9 checks the quoted strings against the guide.

### 2. Remotes table and "Where patch branches live"

The Remotes table is corrected: `origin` is the fork, the push target for the integration branch (`REBUILD_PUSH_INTEGRATION_BRANCH`) and, in `origin` mode, the authority for patch branches. The Purpose column no longer says patch branches live there in both modes. A third row or a note names the hub's own git server as the place patch branches are written in `hub` mode.

A new section "Where patch branches live" follows Concepts/Remotes. It states:

- A rebuild reads only `refs/heads/<branch>` in the workspace trunk. The initial clone brings the fork's branches in only as `refs/remotes/origin/*`.
- `PATCH_BRANCH_SOURCE` selects the writer: `hub` (default; patch branches are written by pushes to the hub's git server, sync never fetches the fork) or `origin` (sync fetches the fork and brings registered patch branches to the fork's tip).
- A table comparing the two models in these rows: who writes the branch, what sync does with it, what registration does, what a push to the hub does (reject, forward with `PUSH_PATCHES_TO_ORIGIN=true`, or accepted and optionally mirrored), what happens on divergence, where a person fixes a conflict, and which copy survives a lost hub data directory.
- Recommendation: teams that open upstream pull requests and review on the fork should use `origin` with `REBUILD_PUSH_INTEGRATION_BRANCH=true`, so people push only to GitHub and the hub is a rebuild engine. `hub` fits workspaces driven by agents that push to the hub and never open upstream PRs.
- The integration branch is always built by the hub and only pushed to the fork. Sync never fetches it from `origin`.
- The mode is read at the start of each operation, so changing `PATCH_BRANCH_SOURCE` takes effect on the next sync, registration or push. A short "switching modes" note says what carries over: switching to `origin` makes the next sync bring registered branches to the fork's tips (possibly replacing hub-side tips, which are kept under `refs/hub/replaced/<branch>`), and switching to `hub` stops fork fetches and clears persisted origin sync state at the next sync.
- A pointer to the Configuration section for the three variables.

Verification: the documentation test finds the heading, the three variable names and both values in the section. A manual read confirms the table rows.

### 3. Patch lifecycle, patch fields and soft-delete text

The guide's patch field table gains `origin_sync_state`, `origin_sha` and `origin_synced_at` (absent when null, and only written in `origin` mode). A short paragraph explains the three sync states and that `missing_on_origin` leaves the patch status alone: the rebuild keeps applying the hub's copy until an operator disables or removes the patch.

The registration text (Getting Started "Add patches" and "Day-to-day operations / Adding a new patch") describes the resolution order and the `400` message, says a fork-only branch no longer needs `--skip-branch-check`, and says `--skip-branch-check` is for branches that exist nowhere yet.

"Removing a patch" says that the backup ref `refs/hub/replaced/<branch>` is deleted with the patch row and that the branch itself is not deleted. The soft-delete text and the "Restoring a soft-deleted patch" text state the 7-day retention window. They state whether a scheduled purge exists by checking the code at implementation time (a search for callers of the purge routines). If nothing calls them in production, the guide says expired rows are only removed when a purge runs and does not promise automatic removal. It does not claim a scheduler that is not there.

Verification: tests (Requirement 9) check the new field names. The purge wording is checked in review against a search for callers of `PurgeExpiredDeletedPatches`.

### 4. Getting Started: fork-first walkthrough

The Getting Started walkthrough is restructured so that the primary path is fork-authoritative and uses the guide's existing example (`acme-oss/api-gateway`, fork `your-org/api-gateway-fork`). The order is:

1. Create the carry-patch workspace (as today).
2. Set upstream credentials (as today, unchanged).
3. Choose the authority: `afc vars create PATCH_BRANCH_SOURCE=origin --workspace api-gateway`, optionally with `REBUILD_PUSH_INTEGRATION_BRANCH=true`, and a note that a fork clone and a fork token with read access are needed on every sync (the sync fetches `origin` with the workspace's `GIT_PAT` or `GIT_USERNAME`/`GIT_PASSWORD`).
4. Create a patch branch in a normal clone of the fork, commit, and `git push` it to the fork (GitHub). Open the upstream PR from the same branch.
5. Register it with `afc patch add` (no `--skip-branch-check`). Existing registration examples (position, `--if-not-exists`, batch) stay.
6. Sync with `afc workspace sync api-gateway`. The text explains that in `origin` mode this, not `afc rebuild submit`, is what brings new fork commits into the hub, because a rebuild reads the hub's copy of each branch and does not fetch the fork. The sample response shows the new fields (`origin_fetched`, `patches_synced`, `patches_diverged`) next to the existing ones, and the text covers each outcome, the `--fail-on-diverged` flag with exit code 3, and `PATCH_DIVERGENCE_POLICY`.
7. The existing steps (preview, manual rebuild, dashboard, failed rebuild, rollback) follow, renumbered, with the dashboard step mentioning the sync-state fields and the new summary counts.

An "Alternative: hub-authoritative workspace" subsection (or a clearly separated walkthrough) shows the hub flow: leave `PATCH_BRANCH_SOURCE` unset, push the branch to the hub's git URL, register, let the push hook rebuild or run `afc rebuild submit`, and optionally set `PUSH_PATCHES_TO_ORIGIN=true` to mirror accepted pushes to the fork. It says this flow suits agent-driven workspaces.

All CLI commands in the walkthrough exist in `docs/cli.md`.

Verification: review confirms every command and flag in the walkthrough against `docs/cli.md`. The documentation test finds the fork-first and alternative headings.

### 5. Sync, push and rebuild-trigger text

The "Sync algorithm" section is rewritten to match the sync that spec 20 delivers. It covers, in order: credential resolution and the upstream fetch; the origin fetch in `origin` mode only (with pruning, and the separate `502 origin fetch failed` failure that leaves everything unchanged); base resolution; the patch refresh and its outcomes; force-push detection; merge detection (also run when only patch tips changed); the rebuild decision (upstream advanced, a patch moved, or a patch was newly merged, suppressed by `AUTO_REBUILD_AFTER_SYNC=false`); and the unconditional write of `last_sync_at`. Statements that sync "returns immediately" when upstream is unchanged are removed.

The `AUTO_REBUILD_AFTER_SYNC` entry says it covers patch changes as well as upstream advances. The "Auto-rebuild on push" section keeps the hook description and adds how the push is treated in each mode (rejection, forwarding, mirroring, and that only accepted ref updates trigger the hook). Any statement that is no longer true is removed rather than qualified.

The rebuild algorithm text written for `01_rebuild_worktree` is left as it is, apart from a sentence that the patch tips it snapshots are whatever the last sync (in `origin` mode) or push (in `hub` mode) left in the trunk.

Verification: the existing docs tests still pass. A review checks each sentence of the sync section against the spec 20 behaviour and the code.

### 6. Conflict resolution and divergence recovery in both modes

The "Resolving conflicts after a failed rebuild" section and Getting Started step "Handle a failed rebuild" each have a short common part (read `conflict_files` in `afc workspace patch-status`) and then one subsection per mode.

- **`hub` mode:** as today. Resolve in the trunk or by pushing a fixed branch to the hub's git server. Rerere records the resolution when the resolution happens during a rebuild. Then set the patch to `active` and rebuild. The text states what the code supports for manual rerere recording today.
- **`origin` mode:** fix the branch in a local clone (merge or rebase the latest upstream into the patch branch, resolve, commit), push it to the fork, run `afc workspace sync` (which brings the new tip and rebuilds), and set the patch back to `active` if its status is still `conflict` and the rebuild did not clear it. The text says not to edit the trunk or push to the hub, and why (a hub push is rejected, forwarded or replaced, and a trunk edit would be overwritten at the next sync). It states the limitation: a resolution made on the fork is not recorded in the hub's rerere cache, so the same conflict is resolved by hand again unless the rebuild itself resolves it. This is a known limitation, not a planned feature.

A new "Recovering a replaced or diverged patch branch" section covers: reading `origin_sync_state` and `replaced_sha`; fetching a replaced tip with `git fetch <hub_url> refs/hub/replaced/<branch>`; pushing it to the fork to keep it; `afc patch reset-to-origin <slug> <patch-id>` to move a `diverged` branch to the fork's tip on demand (and that it reports `rebuild_triggered`); that only one backup per branch exists and later replacements overwrite it; and what to do with `missing_on_origin`.

Verification: the documentation test finds both mode subsections in the conflict section and the recovery section. Review confirms the command names against `docs/cli.md`.

### 7. Configuration, limitations and endpoint summary

The Configuration section is reconciled so that `PATCH_BRANCH_SOURCE`, `PATCH_DIVERGENCE_POLICY` and `PUSH_PATCHES_TO_ORIGIN` each have exactly one entry, in the same style as the existing entries (values table, how to set it with `afc vars`, when it is read). Each entry states the default, the exact-match rule and whether it is ignored in the other mode (`PATCH_DIVERGENCE_POLICY` is ignored in `hub` mode; `PUSH_PATCHES_TO_ORIGIN` means forwarding in `origin` mode and mirroring in `hub` mode). A short table of recommended combinations is added: fork-first (`origin`, optional `REBUILD_PUSH_INTEGRATION_BRANCH=true`), fork-first with hub forwarding for tooling that only knows the hub URL, hub-only, and hub with a durable mirror.

The Limitations section gains: no fork webhooks and no built-in sync schedule (sync is triggered by `afc workspace sync`, an operator's scheduler or any client of `POST /workspaces/:slug/sync`); the sync fetches every fork branch; manual resolutions are not fed into rerere in `origin` mode; `missing_on_origin` does not disable the patch; and `origin` mode needs working origin credentials on every sync. The existing limitations, including the lock note, stay.

The "API reference summary" tables include the single-patch `GET`, the reset endpoint and their scopes, as spec 23 provides them, and the sync row describes both modes.

Verification: the documentation test checks that each of the three variables appears once as a Configuration heading and that each limitation phrase is present. Review checks the endpoint rows against `RegisterRoutes`.

### 8. Cross-document consistency

After the guide is rewritten, the implementer searches `docs/api.md`, `docs/cli.md`, `docs/openapi.yaml`, `docs/permissions.md` and `README.md` for statements about where patch branches live, what carry-patch sync fetches, when `last_sync_at` is written, and what a push to a patch branch does. Any statement that contradicts the guide or the shipped behaviour is corrected in the same change. The README is not otherwise changed. Its link to the guide stays valid. No document references a file that does not exist.

Verification: a recorded search (the commands and a summary of the hits, in the commit message or handoff note) shows no contradicting statement remains.

### 9. Agent example, ADR 01, errata and tests

**`docs/examples/AGENTS_carry_patch.md`.** A new step in "Understand Before You Code" tells the agent to read the workspace's `PATCH_BRANCH_SOURCE` with `afc vars list --workspace <workspace-slug>` (unset means `hub`) before anything else touches branches. "Decide Where Your Change Belongs", "Push to the Hub", "Register the Patch", "Trigger a Rebuild", "Upstream Sync", "Conflict Resolution", "Patch Management > Removing a Patch", "Commands Reference" and "Quality Gates" each get the mode split where it differs. In `origin` mode the agent clones the fork rather than the hub, pushes the patch branch to the fork, registers it, and runs `afc workspace sync` (not `afc rebuild submit`) to bring it in and rebuild. It never pushes a registered patch branch to the hub, because the hub refuses or replaces it. In `hub` mode today's instructions apply, and `PUSH_PATCHES_TO_ORIGIN=true` is explained as a mirror the agent does not need to do by hand. The text for `afc patch remove` states that the branch is not deleted but the backup ref is. The project context paragraph is made mode-neutral. The commands table gains `afc patch reset-to-origin`, `afc workspace sync --fail-on-diverged` and `afc vars list --workspace`. The existing `_rebuild_temp` legacy markers stay.

**ADR 01.** The status line becomes `Accepted`, and an `Accepted` date line is added using the date of the change. The proposal `Date` stays. A sentence at the start of Context says it describes the code before the decision was implemented. A short "Implementation notes" section lists the errata files of Requirement 9 and says the Decision section stands as written. The Decision text is not rewritten, and no consequences are removed.

**Errata.** One erratum file per delivering spec, named `docs/errata/NN_snake_case_topic.md` with NN the spec number, each in the existing three-section format: `20_fork_patch_sync_divergences.md`, `21_fork_patch_registration_divergences.md`, `22_fork_push_control_divergences.md`, `23_patch_divergence_recovery_divergences.md`. Each lists the points where delivered behaviour differs from the issue's proposal, one entry per divergence, with the reason. The starting list below comes from the PRDs and is checked against the code before it is written (Requirement 1). An entry the code does not bear out is dropped, and a divergence found in the code that is not listed is added.

- Spec 20: the origin fetch prunes stale tracking refs; origin credential failure answers `502 failed to resolve origin credentials` and the fetch failure carries error type `origin_fetch_failed`; patch ref writes are compare-and-swap, and a failed write answers `500` after persisting earlier outcomes; a moved `disabled` patch does not count as advanced; merge detection also runs when only patch tips changed and a newly merged patch triggers a rebuild; `origin_fetched` is always present while `patches_synced` and `patches_diverged` appear only in `origin` mode; `last_sync_at` is not written when the sync ends on a ref-write failure; workspace variables are documented in `docs/api.md` because `docs/configuration.md` does not list them.
- Spec 21: existence is checked on `refs/heads/<name>`, so a tag, SHA or remote-qualified name is no longer accepted; fork fetch failures other than a missing branch answer `502`, not `400`; a busy workspace answers `409 workspace_busy` when registration needs to write; `branch_resolution` has a fourth value `skipped`; batch registration emits no audit event.
- Spec 22: deletes of a registered patch branch are rejected in `origin` mode in both reject and forward modes; the forward goes through a temporary `refs/hub/forward/<branch>` ref with a 120 s timeout and URL userinfo is removed from messages; only accepted ref updates drive `head_sha`, the `hub.git.push` event and the post-push hook, for every push; control fails open on a lookup failure; the mirror runs even when `AUTO_REBUILD_AFTER_PUSH=false`.
- Spec 23: the single-patch `GET` route is added because it did not exist; the reset fetches the branch first, writes a backup only when it discards commits, enqueues a rebuild, and is limited to `active`, `conflict` and `disabled` patches; reset records sync state only in `origin` mode; a `hub.patch.reset` event is added; only the `refs/hub/replaced/` prefix is protected; the purge routine removes refs but is not scheduled.

**Tests.** A documentation test is added to `internal/carrypatch/docs_test.go`, reusing its `readDoc`, `requireContains` and `section` helpers. It asserts: the guide's "Where patch branches live" heading and the three variable names and both source values; one Configuration entry per variable; the fork-first and alternative walkthrough headings; both mode subsections in the conflict section; the recovery section; the Remotes table no longer containing the string "Where patch branches live; push target for local work"; the agent example mentioning `PATCH_BRANCH_SOURCE` and `afc workspace sync`; ADR 01 containing `Status:** Accepted` and no `Proposed`; and the four errata files existing and each containing the three section headings. The existing tests (`TS_01_69` to `TS_01_72`) pass unchanged. `make test` and `make lint` pass.

Verification: the new test passes and fails when any listed content is removed.

## Design Decisions

1. **Code wins over the spec PRDs when they differ.** Specs 20 to 23 are written but not implemented, and implementation can change details such as helper names, messages or defaults. A guide that repeats a PRD that the code then contradicts is worse than no guide. Each divergence goes into the errata, so the audit trail is kept.
2. **Fork-first walkthrough as the primary path, hub flow as an alternative.** ADR 01 point 6 says so, and the issue asks for it. The existing walkthrough already uses a GitHub fork and upstream PRs, so the fork-first path is the natural narrative.
3. **Sync, not rebuild, is the step after a fork push in `origin` mode.** A rebuild reads `refs/heads/<branch>` in the trunk and fetches only upstream, so a fork commit reaches the integration branch only through sync. Getting this wrong in the guide would make the recommended workflow silently rebuild stale tips.
4. **One erratum file per delivering spec, not one file for the whole issue.** The repository convention names an erratum after the spec number it relates to, and each divergence belongs to one spec. Four short files are easier to review than one long one.
5. **ADR 01's Decision is not rewritten.** The ADR records a decision. The delivered behaviour still matches it. The differences are implementation details, and they belong in errata. A short Implementation notes section links them.
6. **The guide states known limitations instead of promising the proposal's open questions.** Rerere feedback from fork pushes, auto-disabling `missing_on_origin`, a built-in schedule and a branch filter were deliberately left undecided. Documenting them as limitations tells operators what to expect without committing the project.
7. **The purge statement is checked against the code, not repeated.** The guide currently says a background purge removes expired patches. Spec 23 found no production caller. The guide must not describe a scheduler that does not exist, but if a later change wires one before this spec is implemented, the guide should say so.
8. **Docs tests in the existing style.** The repo already has `docs_test.go` that guards guide content. Extending it keeps this rewrite from decaying silently and costs a few string checks.
9. **No `docs/architecture.md` is created.** The steering says to create a missing file when architecture changes. This spec changes none, and the earlier specs that do change it (22 and 23) own that text.
10. **The interim sync note from spec 20 is removed, not kept.** It was written to describe the state between spec 20 and spec 22. Keeping it next to the full section would leave two descriptions that can disagree.
11. **`docs/configuration.md` is not changed.** It lists environment keys, not workspace variables. Spec 20 recorded this, and the variables are documented in `docs/api.md` and in the guide.
12. **Cross-document contradictions are fixed here, but reference docs are not rewritten.** Specs 20 to 23 own their reference text. This spec only closes contradictions that the guide's rewrite exposes, so it stays a documentation task of the size of one spec.

## Dependencies

| Spec | Relationship | Why |
|------|--------------|-----|
| `20_fork_patch_sync` | depends | Defines `PATCH_BRANCH_SOURCE`, `PATCH_DIVERGENCE_POLICY`, the origin fetch, the patch refresh, the sync response fields, `--fail-on-diverged`, the persisted sync state and the audit events that the guide documents. Its interim guide edits are reconciled here. |
| `21_fork_patch_registration` | depends | Defines the registration resolution order, the `400`, `409` and `502` messages and the `branch_resolution` audit value that the guide and agent example document. |
| `22_fork_push_control` | depends | Defines `PUSH_PATCHES_TO_ORIGIN`, the rejection, forwarding and mirror behaviours and their messages. Its edits to the guide's Configuration section and sync note are reconciled here. |
| `23_patch_divergence_recovery` | depends | Defines `reset-to-origin`, `replaced_sha`, backup-ref cleanup, the protected namespace and the purge behaviour that the recovery section documents. Its two rows in the endpoint table are kept. |
| `01_rebuild_worktree` | depends | The guide's rebuild-algorithm text, lock note and the docs tests (`TS_01_69` to `TS_01_72`) written for it must keep passing, and ADR 02 stays untouched. |
