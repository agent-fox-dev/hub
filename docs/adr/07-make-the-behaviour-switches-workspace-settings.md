# ADR 07: Make the behaviour switches workspace settings

- **Status:** Accepted
- **Date:** 2026-10-24

## Context

Eleven behaviours of a workspace were configured through workspace variables:
how a carry-patch workspace chooses the authority for patch branches, handles
divergence, forwards or mirrors pushes, rebuilds (strategy, fail mode,
auto-rebuild after sync and after push, push of the integration branch),
detects squash merges, and where its forge's API is. Variables are a free-form
key/value store meant for the values a workspace's jobs and sandboxes read.
Using it for the hub's own switches had three costs: the switches were
undiscoverable from the workspace object (a client had to know the key names
and read a second resource), untyped (`"true"` was on, `"yes"` was silently
off, a typo in a key or a value was accepted and ignored), and every reader
re-read the store per request with its own parsing.

## Decision

1. **The switches are a `settings` object of the workspace.** `Settings` in
   `internal/workspace` names each one with its type: five enumerations, four
   booleans and two URLs, each with a default. The workspace object shows every
   key with its effective value. `POST /api/v1/workspaces` takes `settings`;
   `PATCH /api/v1/workspaces/:slug` merges a `settings` object (a key given
   takes the value, `null` returns a key to its default, keys not given are
   kept). A write is validated: an unknown key or a value a setting cannot take
   is 400 with the key and its choices. `afc workspace create` and
   `afc workspace update` take `--setting key=value`.

2. **Stored as one JSON document** in `workspaces.settings`, defaults
   filled in. A document from before a setting existed lacks the key and
   gets the default on read.

3. **The readers keep their interface.** The carry-patch module and the forge
   client read a setting through the `GetVariable` function they already
   take, by the name the setting had as a variable (`PATCH_BRANCH_SOURCE`,
   `FORGE_API_URL`, ...). `workspace.SettingsReader` serves that function
   from the settings document, so the module, which the hub only embeds,
   needs no change, and the forge client's `*secrets.NotFoundError` contract
   for "not set" holds.

4. **Existing variables are migrated once.** `workspace.MigrateVariableSettings`
   runs at hub start: every workspace variable with one of the eleven names
   becomes the setting (a value the setting cannot take is dropped with a
   warning) and the variable row is deleted. Variables with other names are
   untouched; the variables API itself is unchanged and no longer changes hub
   behaviour.

## Consequences

- A client reads and sets a workspace's behaviour in one place, with the same
  validation the hub applies, and the documentation has one table of settings
  instead of a variables reference beside the workspace object.
- A deployment that set the switches through `afc vars` keeps them after one
  restart; scripts that still write them as variables have no effect from then
  on and must use `--setting`.
- Later, the carry-patch module can take a typed settings struct from its host
  instead of a string reader; this change keeps that door open without
  touching the module.
