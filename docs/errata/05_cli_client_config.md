# Errata: Spec 05 — CLI Client Config

## E1: workspace_slug vs workspace_id in config file keys

**Severity:** Critical  
**Affected requirements:** 05-REQ-2.2, 05-REQ-6.1, 05-REQ-7.1, 05-REQ-8.1, 05-REQ-9.1

The spec uses `workspace_slug` as the TOML section key for `[keys.<workspace_slug>]`
entries and as the value passed via the `--workspace` flag. However, the existing
codebase uses `workspace_id` (a UUID-like string such as `ws-123`), not a slug:

- The `--workspace` flag in `keys.go` is documented as "Workspace ID" in `docs/cli.md`
- The CLI sends it as `workspace_id` in the HTTP request body
- The `Workspace` struct in `store.go` has separate `id` and `slug` fields

**Decision:** Use the `--workspace` flag value as-is for the TOML section key.
Whether the user passes an ID or a slug, it becomes the key in `[keys.<value>]`.
This is consistent with the existing wire format and avoids requiring a slug-to-id
lookup. The TOML sections may be less human-readable when IDs are used, but this
preserves backward compatibility with the existing `--workspace` flag semantics.

## E2: --api-key flag scoped to keys subcommand

**Severity:** Major  
**Affected requirements:** 05-REQ-3.2

The spec treats `--api-key` as globally available, but it is defined as a persistent
flag on the `keys` subcommand, not on the root command. The `PersistentPreRunE` on
the root command cannot eagerly resolve the API key for all commands.

**Decision:** The `PersistentPreRunE` only loads the config file. API key resolution
happens lazily in each command's `RunE` via the existing `resolveAPIKey(flagVal)`
function, which now delegates to `cliconfig.ResolveAPIKey`. Commands that don't need
an API key (like `login`) never call `resolveAPIKey`.

## E3: PersistentPreRunE does not eagerly resolve credentials

**Severity:** Major  
**Affected requirements:** 05-REQ-1.1, 05-REQ-3.1

The spec directs wiring config initialization into `PersistentPreRunE` with eager
resolution of both hub URL and API key. This would break `afc login` which doesn't
need an API key (the first-time user path, 05-PATH-1).

**Decision:** `PersistentPreRunE` only calls `EnsureConfigExists` and `LoadConfig`.
Credential resolution remains lazy — each command resolves hub URL and API key in its
own `RunE` as needed. The loaded config is stored in a package-level variable
(`loadedConfig`) accessible to the existing `resolveHubURL()` and `resolveAPIKey()`
functions.

## E4: Existing resolve function signatures preserved

**Severity:** Major  
**Affected requirements:** 05-REQ-3.1, 05-REQ-11.1

The spec describes new resolve functions with signatures
`resolveHubURL(flagVal, envVal string, cfg *Config)` in the `cliconfig` package.
The existing `cli` package has `resolveHubURL()` (no args, reads globals) and
`resolveAPIKey(flagVal string)`. Changing these signatures would break all existing
callers.

**Decision:** The `cliconfig` package exports `ResolveHubURL` and `ResolveAPIKey`
with the spec's signatures. The `cli` package's internal `resolveHubURL()` and
`resolveAPIKey()` functions are refactored to delegate to the `cliconfig` versions,
passing the package-level flag variables, `os.Getenv()` values, and loaded config.
No caller signatures change in the `cli` package.
