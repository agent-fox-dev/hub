# Errata: Spec 24 — Patch Authority Docs Divergences

## Spec Expectation

Spec 24 (`24_patch_authority_docs`), Requirement 5, listed the phases of the
carry-patch sync algorithm in this order: origin fetch; base resolution; the
patch refresh; force-push detection. Requirement 1 says the code wins where
the spec and the code disagree.

## Implementation Reality

1. **Base resolution and force-push detection run before the patch refresh.**
   The sync handler resolves the new upstream base and detects a force-push
   before it calls the patch refresh, so the guide's "Sync algorithm" section
   lists the phases as: fetch origin, resolve the base, detect force-push,
   refresh patch branches. See `internal/carrypatch/sync_handlers.go`,
   `runCarryPatchSync`.
