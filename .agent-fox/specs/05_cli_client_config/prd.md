---
spec_id: '05'
spec_name: cli_client_config
title: CLI Client Configuration
status: draft
created_at: '2026-07-09T13:00:51.358711+00:00'
updated_at: '2026-07-09T13:02:06.111543+00:00'
owner: ''
source: .agent-fox/specs/cr1.md
schema_version: 1
---
# CLI Client Configuration

## Intent

Enable `afc` CLI users to persist hub URL and API key credentials locally so
they do not need to supply them on every command invocation.

## Background

The `afc` CLI has historically required `--hub-url` and `--api-key` flags (or
their corresponding environment variables `AF_HUB_URL` and `AF_HUB_API_KEY`)
on every command invocation. This is impractical for daily interactive use.

TOML was chosen as the config file format because `github.com/BurntSushi/toml`
is already a project dependency and TOML is human-readable and hand-editable
without tooling. The design follows conventions established by `gh` (GitHub CLI)
and `kubectl` for storing per-user credentials in a dotfile under the user's
home directory. OS keychain integration was considered but deferred due to
cross-platform complexity and the single-user, interactive nature of the CLI.

## Problem

The `afc` CLI requires `--hub-url` and `--api-key` flags (or their
corresponding environment variables `AF_HUB_URL` and `AF_HUB_API_KEY`) on
every command invocation. This is impractical for daily interactive use.

## Solution

Introduce a persistent client configuration file at `$HOME/.af/config.toml`
that stores the default hub URL and all known API key tokens. Command-line
flags and environment variables continue to override config file values.

Commands that create, update, or revoke keys automatically update the config
file. A new `afc keys default` command lets the user choose which stored key
is active.

## Goals

- **Zero mandatory flags** for authenticated commands once the config file is
  populated — users who have run `afc login` or `afc keys create` need not
  pass `--hub-url` or `--api-key` on subsequent invocations.
- **Automatic config file creation** on first run — `$HOME/.af/config.toml` is
  created with no user intervention required.
- **Fully configured CLI after `afc login`** — after a successful login, the
  CLI is ready for daily use with no manual file editing.

## Non-Goals

The following are explicitly out of scope for this feature:

- **OS keychain or secret store integration** — tokens are stored in plaintext
  with restricted file permissions (`0600`) only; keychain/secret store support
  is not provided.
- **Multi-profile or named-context support** — only one active hub URL and one
  default API key are tracked at a time.
- **Encrypted config file storage** — the file is not encrypted at rest beyond
  OS-level file permission restrictions.
- **Windows-specific path conventions** — `%APPDATA%` or other Windows path
  idioms are not supported; the feature targets Unix-like systems using `$HOME`.
- **Remote or shared configuration** — the config file is local to the user's
  machine and is not synced or shared.

## Config File Structure

```toml
# Client configuration for afc.
# See docs/configuration.md for the full reference.

hub_url = "https://hub.example.com"
api_key = "my-project"

[keys.my-project]
key_id = "a1b2c3d4e5f6"
token = "af_a1b2c3d4e5f6_deadbeef..."
label = "dev laptop"

[keys.staging]
key_id = "f7e8d9c0b1a2"
token = "af_f7e8d9c0b1a2_cafebabe..."
label = "ci runner"

[keys._login]
key_id = "0011aabbccdd"
token = "af_0011aabbccdd_aabbccdd..."
label = "login"
```

### Field Definitions

**Top-level fields:**

| Field | Type | Description |
|-------|------|-------------|
| `hub_url` | string | Default hub URL. Used when `--hub-url` and `AF_HUB_URL` are absent. |
| `api_key` | string | Workspace slug referencing a `[keys.<workspace_slug>]` section. Determines which token is used when `--api-key` and `AF_HUB_API_KEY` are absent. The reserved slug `_login` references the token obtained via `afc login`. |

**`[keys.<workspace_slug>]` sections:**

Each section stores one API key for a workspace. The section name is the
workspace slug — the short handle returned by `afc workspace create` and used
in `afc keys create --workspace <workspace_slug>`. The reserved slug `_login`
is used for the token obtained via `afc login`.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `key_id` | string | yes | The hex key identifier (used by `keys refresh` and `keys revoke` commands). |
| `token` | string | yes | Full plaintext API key (e.g. `af_<key_id>_<secret>`). |
| `label` | string | no | Human-readable label. |

## Behavior

### Auto-Creation

On startup, if `$HOME/.af/config.toml` does not exist, `afc` creates:

1. `$HOME/.af/` directory with permissions `0700`.
2. `$HOME/.af/config.toml` with permissions `0600`, containing:

```toml
# Client configuration for afc.
# See docs/configuration.md for the full reference.

hub_url = ""
```

If the directory or file already exists, no changes are made.

### Resolution Precedence

For **hub URL**:

1. `--hub-url` flag (highest priority)
2. `AF_HUB_URL` environment variable
3. `hub_url` from config file (empty string treated as unset)
4. Error with descriptive message (mentioning the config file as an option
   alongside flags and env vars)

For **API key**:

1. `--api-key` flag (highest priority)
2. `AF_HUB_API_KEY` environment variable
3. Config file: read `api_key` to get the workspace slug, then look up
   `[keys.<workspace_slug>].token`
4. Error with descriptive message (mentioning the config file as an option
   alongside flags and env vars)

### Config File Updates

The config file is only modified by these commands:

| Command | Config Change |
|---------|--------------|
| `afc login` | Stores the returned token as a `[keys._login]` entry with `key_id`, `token`, and `label = "login"`; sets `api_key` to `_login`. Also writes `hub_url` if not already set. |
| `afc keys create` | Adds a `[keys.<workspace_slug>]` section with `key_id`, `token`, and `label`. The workspace slug comes from the `--workspace` flag. |
| `afc keys refresh <key-id>` | Finds the `[keys.*]` section whose `key_id` matches, updates its `token`. |
| `afc keys revoke <key-id>` | Finds and removes the `[keys.*]` section whose `key_id` matches. If the revoked key's workspace slug was the default (`api_key`), clears `api_key` to an empty string and prints a warning to stderr. |
| `afc keys default <workspace-slug>` | Sets `api_key` to the specified workspace slug. |

No other command modifies the config file.

### Atomic Writes

All config file mutations use an atomic write strategy: the updated content is
written to a temporary file in the same directory (`$HOME/.af/`) using
`os.CreateTemp`, then renamed into place via `os.Rename`. The temp file is
cleaned up via defer on rename failure. No file locking is used. In the event
of concurrent config-mutating invocations, last writer wins. Concurrent config
mutations are not a supported use case for this single-user interactive CLI.

### New Command: `afc keys default <workspace-slug>`

Sets the default API key by updating `api_key` in the config file to point to
the specified workspace slug.

**Precondition:** The workspace slug must reference an existing
`[keys.<workspace_slug>]` section in the config file. If not, the command exits
with an error and no changes are made.

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<workspace-slug>` | The workspace slug to set as the default (required). Use `_login` to default to the login token. |

### Login and Config

The `afc login` command currently returns a user object from the OAuth
callback, not an API key token. To support storing login credentials in the
config, the server-side auth callback endpoint (`POST /api/v1/auth/callback`)
must be extended to also return an API key in the response alongside the user
object.

The response schema change:

```json
{
  "user": { ... },
  "api_key": {
    "key": "af_<key_id>_<secret>",
    "key_id": "<key_id>"
  }
}
```

On successful login, the CLI:

1. Parses the `api_key` field from the response.
2. Writes a `[keys._login]` section with `key_id`, `token`, and
   `label = "login"`.
3. Sets `api_key` to `_login`.
4. Writes `hub_url` to the URL used for login (from the `--hub-url` flag or
   env var) if `hub_url` is currently empty.

### Rollout and Existing Users

No migration is required. Existing flags (`--hub-url`, `--api-key`) and
environment variables (`AF_HUB_URL`, `AF_HUB_API_KEY`) continue to work
unchanged with the same precedence as before. The config file is a purely
additive fallback layer. Users who continue to use flags or env vars will
experience no behavior change. When neither a flag, env var, nor populated
config file entry is present, the error message will mention the config file
as an option alongside flags and env vars, naturally guiding new users toward
the feature.

### Error Handling

| Condition | Behavior |
|-----------|----------|
| Config file exists but is malformed (invalid TOML, unexpected types) | `afc` exits with an error message identifying the parse issue. |
| `api_key` references a workspace slug with no matching `[keys.*]` section | Treated as if `api_key` is unset; falls through to error (or flag/env). |
| `afc keys default <workspace-slug>` with nonexistent slug | Error, no changes made. |
| `afc keys revoke` on the default key | Key section is removed, `api_key` is cleared, warning printed to stderr telling user to set a new default. |
| `$HOME/.af/` directory cannot be created (permission denied) | `afc` exits with error. |
| Missing hub URL or API key (no flag, env var, or config value) | `afc` exits with a descriptive error message referencing the config file, relevant flags, and env vars. |

### File Permissions

- `$HOME/.af/` directory: `0700` (owner-only access).
- `$HOME/.af/config.toml`: `0600` (owner-only read/write).
- Permissions are set on creation only; existing file permissions are not
  modified.

### Security Considerations

API key tokens are stored in plaintext inside `$HOME/.af/config.toml`. The file
is protected by OS-level permissions (`0600`), restricting access to the owning
user only. This approach was chosen for simplicity and cross-platform
consistency; OS keychain integration is explicitly out of scope for this
iteration. Users should be aware that any process running as the same user (or
as root) can read the config file. The threat model assumes a trusted
single-user workstation environment.

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 03_cli | 5 | 1 | Existing CLI commands, flag handling, and resolve functions from spec 03. |

## Tech Stack

- Go 1.22+
- Cobra CLI framework (`github.com/spf13/cobra`)
- `github.com/BurntSushi/toml` (already a project dependency)
- Standard library `os`, `path/filepath` for file and directory operations

## Documentation

- Update `docs/cli.md`: add the `keys default` command, document config file
  auto-creation, and note that `login`, `keys create`, `keys refresh`, and
  `keys revoke` update the config.
- Update `docs/configuration.md`: add a "Client Configuration" section
  describing the config file location, structure, precedence rules,
  auto-creation, atomic write behavior, file permissions, and security
  considerations.

## Design Decisions

1. **Config file TOML structure:** `api_key` is a workspace slug (pointer)
   into the `[keys.*]` sections, not the token itself. Keys use flat
   `[keys.<workspace_slug>]` table headers keyed by the workspace slug
   returned by `afc workspace create`. The reserved slug `_login` is used
   for the token obtained via `afc login`. Rationale: workspace slugs are
   human-readable and match the `--workspace` flag value, making the config
   file easy to understand and edit by hand.

2. **Precedence order:** `--flag` > env var > config file > error. Matches the
   existing flag > env pattern and adds config as the lowest-priority fallback.
   Rationale: env vars are more explicit than a file on disk, and flags are
   the most explicit.

3. **"keys default" semantics:** Sets which workspace slug is used as the
   bearer token for all commands when no flag or env var overrides. The config
   must contain the actual token in the matching `[keys.<workspace_slug>]`
   section.

4. **Initial config contents:** Just `hub_url = ""` with a comment pointing to
   docs. No key sections until the user creates or receives keys.

5. **What key commands write:** `keys create` stores key_id, token, and label.
   `keys refresh` updates only the token. `keys revoke` removes the entire
   key section.

6. **Revocation and default:** Revoking the default key clears `api_key` and
   warns the user. They must explicitly set a new default.

7. **Login and config:** Login stores the returned token under `[keys._login]`
   and sets it as default. Also writes `hub_url` if not already set. Requires
   a server-side change: the auth callback must return an API key alongside
   the user object.

8. **File permissions:** `0700` for directory, `0600` for config file. Matches
   the server's `admin_token` file convention.

9. **Malformed config:** `afc` exits with a parse error. No silent fallthrough.
   Rationale: a broken config file is a user error that should be surfaced
   immediately rather than silently ignored.

10. **Atomic writes, no locking:** Config mutations write to a temp file
    (via `os.CreateTemp`) then rename atomically. Temp file is cleaned up on
    rename failure. No file locking is used. Last writer wins in the unlikely
    event of concurrent invocations. Rationale: concurrent config mutations
    are not a realistic scenario for a single-user interactive CLI.

11. **Plaintext token storage:** Tokens are stored in plaintext protected by
    `0600` file permissions. OS keychain integration was considered but
    deferred due to cross-platform complexity. The threat model assumes a
    trusted single-user workstation.

12. **No migration strategy:** The config file is purely additive. Flags and
    env vars continue to work unchanged. Discovery is driven by improved
    error messages that reference the config file.

13. **Empty string is unset:** An empty string for `hub_url` or `api_key` in
    the config file is treated as if the field is absent — resolution falls
    through to the next precedence level.

14. **Spec 03 command ownership:** Spec 03 fully owns the CLI definition of
    `keys create`, `keys refresh`, `keys revoke`, and `keys list`. This PRD
    only describes the config-mutating side-effects added to those commands.

15. **Key lookup by key_id:** Each `[keys.*]` section includes a `key_id`
    field. Commands like `keys refresh <key-id>` and `keys revoke <key-id>`
    search all sections to find the one whose `key_id` matches the argument.
