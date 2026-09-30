# Erratum: `afc credential set` removed in favor of `afc secrets`

**Spec:** 15_carry_patch_workspace  
**Affected requirements:** 15-REQ-5.2, 15-REQ-5.3, 15-REQ-5.E3  
**Date:** 2026-09-30

## Problem

The spec defines an `afc credential set` command that stores upstream git
credentials. It was only a thin wrapper around the workspace secrets API that
wrote three fixed keys (`UPSTREAM_GIT_PAT`, `UPSTREAM_GIT_USERNAME`,
`UPSTREAM_GIT_PASSWORD`). It offered no list, update or delete, so managing
these credentials meant mixing two command groups over the same storage.

## Resolution

The `afc credential` command group was removed. Upstream credentials are
managed with `afc secrets create|list|update|delete --workspace <slug>`. The
three names above (and `GIT_PAT`, `GIT_USERNAME`, `GIT_PASSWORD` for `origin`)
are documented as reserved secret names in `docs/cli.md`. Server behavior
(15-REQ-5.1, 15-REQ-5.4, 15-REQ-5.E1, 15-REQ-5.E2) is unchanged. The hidden
`afc credential-helper` git helper is unrelated and stays.

The CLI no longer validates the username/password pairing at write time; the
server ignores an incomplete pair and falls back to the next credential source.
