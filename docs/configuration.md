# Configuration Reference

This document covers two configuration files:

1. **Server configuration** (`config.toml` in the working directory) — used by
   `af-hub` to configure the server.
2. **Client configuration** (`$HOME/.af/config.toml`) — used by the `afc` CLI
   to store credentials and hub URL.

---

## Client Configuration

The `afc` CLI stores persistent credentials and connection settings in
`$HOME/.af/config.toml`. This file is auto-created on the first invocation of
any `afc` command.

### File location

| Path | Permissions | Description |
|------|-------------|-------------|
| `$HOME/.af/` | `0700` | Configuration directory. Created automatically on first run. |
| `$HOME/.af/config.toml` | `0600` | Configuration file (TOML format). Created automatically on first run. |

Permissions are set only at creation time. The CLI never modifies permissions
on paths that already exist.

### TOML structure

```toml
# Client configuration for afc
# See docs/configuration.md for details.

# Hub server URL. Overridden by --hub-url flag or AF_HUB_URL env var.
hub_url = "https://hub.example.com"

# Active API key workspace slug. Points to a [keys.<slug>] section below.
# Overridden by --api-key flag or AF_HUB_API_KEY env var.
api_key = "my-project"

# Login token (created by `afc login`)
[keys._login]
key_id = "0011aabb"
token = "af_0011aabb_secret789"
label = "login"

# Workspace-scoped key (created by `afc keys create --workspace my-project`)
[keys.my-project]
key_id = "a1b2c3d4"
token = "af_a1b2c3d4_secretabc"
label = "ci-bot"
```

### Field reference

#### Top-level fields

| Field | Type | Description |
|-------|------|-------------|
| `hub_url` | string | Default hub server URL. An empty string is treated as unset (resolution falls through to the next precedence level). |
| `api_key` | string | Workspace slug identifying the active `[keys.*]` section. An empty string is treated as unset. If it references a slug with no matching `[keys.*]` section, it is also treated as unset. |

#### `[keys.<workspace_slug>]` sections

Each section stores one API key. The section name is the workspace slug (or
ID) passed to `--workspace` during `afc keys create`, or the reserved slug
`_login` for credentials obtained via `afc login`.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `key_id` | string | **Yes** | Hex identifier for the API key. Used by `keys refresh` and `keys revoke` to locate entries. |
| `token` | string | **Yes** | Full plaintext API key string (format: `af_<key_id>_<secret>`). |
| `label` | string | No | Human-readable description of the key's purpose. |

### Resolution precedence

The hub URL and API key are resolved using the following precedence order
(first non-empty value wins):

**Hub URL:**

| Priority | Source | Description |
|----------|--------|-------------|
| 1 | `--hub-url` flag | Per-invocation override. |
| 2 | `AF_HUB_URL` env var | Environment override. |
| 3 | `hub_url` in config file | Persistent default (empty string treated as unset). |
| — | *(none found)* | Error with message referencing all three sources. |

**API key token:**

| Priority | Source | Description |
|----------|--------|-------------|
| 1 | `--api-key` flag | Per-invocation override (value used directly as token). |
| 2 | `AF_HUB_API_KEY` env var | Environment override (value used directly as token). |
| 3 | Config file lookup | Reads `api_key` slug → retrieves `token` from `[keys.<slug>]`. |
| — | *(none found)* | Error with message referencing all three sources. |

### Auto-creation behavior

On first run, if `$HOME/.af/config.toml` does not exist:

1. `$HOME/.af/` is created with permissions `0700`.
2. `$HOME/.af/config.toml` is created with permissions `0600` containing a
   comment header and `hub_url = ""`.
3. If creation fails partway, any partial file is cleaned up before the CLI
   exits with an error.

If both already exist, the CLI makes no changes to either path.

### Atomic writes

All config file mutations (login, keys create, keys refresh, keys revoke,
keys default) use atomic writes to prevent corruption:

1. Updated content is encoded to a temporary file in `$HOME/.af/` with
   permissions `0600`.
2. The temporary file is atomically renamed over `$HOME/.af/config.toml` via
   `os.Rename`.
3. If the rename fails, the temporary file is cleaned up and the original
   config file is left unchanged.

This ensures `$HOME/.af/config.toml` is always either the complete old
content or the complete new content — never a partial write.

### Commands that modify the config file

| Command | Config file effect |
|---------|--------------------|
| `afc login` | Writes `[keys._login]` section; sets `api_key = "_login"`; sets `hub_url` if empty. |
| `afc keys create` | Adds `[keys.<workspace>]` section with `key_id`, `token`, `label`. |
| `afc keys refresh <key-id>` | Updates `token` in the matching `[keys.*]` section. |
| `afc keys revoke <key-id>` | Removes the matching `[keys.*]` section; clears `api_key` if it was the default. |
| `afc keys default <slug>` | Sets `api_key` to the specified workspace slug. |

All other commands (e.g. `keys list`, `workspace list`) read the config file
but never modify it.

### Security considerations

- **Plaintext storage:** API key tokens are stored in plaintext in
  `$HOME/.af/config.toml`. The file is created with `0600` permissions
  (owner read/write only) to limit access on multi-user systems.
- **Single-user threat model:** The config file assumes a single-user system.
  On shared hosts, ensure `$HOME` is not world-readable.
- **Token rotation:** Use `afc keys refresh` to rotate a compromised key. The
  old secret is invalidated server-side and the config file is updated
  atomically.
- **Key revocation:** Use `afc keys revoke` to permanently invalidate a key
  and remove it from the config file.

### Malformed config file

If `$HOME/.af/config.toml` contains invalid TOML syntax or unexpected field
types, the CLI exits immediately with a non-zero status code and prints a
descriptive parse error message to stderr identifying the file path and the
nature of the failure. No API calls are made.

---

## Server Configuration

The af-hub server loads its configuration from a `config.toml` file in the
current working directory on startup. If the file is absent, the server logs
a fatal error and exits without binding any port.

## Complete example

```toml
[server]
port = 8080
bind_address = "0.0.0.0"
external_url = "https://hub.example.com"

[database]
path = "./data/af-hub.db"

[logging]
level = "info"

[[auth.oauth]]
provider = "github"
client_id = "your-github-client-id"
client_secret = "your-github-client-secret"
authorize_url = "https://github.com/login/oauth/authorize"
token_url = "https://github.com/login/oauth/access_token"
userinfo_url = "https://api.github.com/user"
```

## Field reference

### `[server]`

| Field | Type | Default | Validation | Description |
|-------|------|---------|------------|-------------|
| `port` | integer | `8080` | Must be in range 1–65535. Server refuses to start if out of range. | TCP port the HTTP server listens on. |
| `bind_address` | string | `"0.0.0.0"` | No validation beyond being a string. | Network address the server binds to. Use `"127.0.0.1"` to restrict to localhost. |
| `external_url` | string | _(none)_ | Optional. No validation beyond being a non-empty string when present. | Public-facing URL used for OAuth redirect URI derivation. Not required for local development. |

### `[database]`

| Field | Type | Default | Validation | Description |
|-------|------|---------|------------|-------------|
| `path` | string | `"./data/af-hub.db"` | No format validation. The server auto-creates the parent directory tree via `os.MkdirAll` if it does not exist. A fatal error is logged if the directory cannot be created (e.g. permission denied). | Filesystem path to the SQLite database file. |

### `[logging]`

| Field | Type | Default | Validation | Description |
|-------|------|---------|------------|-------------|
| `level` | string | `"info"` | Must be one of: `trace`, `debug`, `info`, `warn`, `error`, `fatal`, `panic`. Server refuses to start if the value is not in this set. | Minimum log level. All log output is emitted as structured JSON (logrus JSONFormatter). |

### `[[auth.oauth]]`

An array of tables, each defining one OAuth provider. All three required fields
must be non-empty strings; the server refuses to start if any are missing.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `provider` | string | **Yes** | Provider name (e.g. `"github"`). Used as the internal identifier. |
| `client_id` | string | **Yes** | OAuth client identifier from the provider. |
| `client_secret` | string | **Yes** | OAuth client secret credential from the provider. |
| `authorize_url` | string | No | OAuth authorization endpoint URL. Provider-specific defaults may apply. |
| `token_url` | string | No | OAuth token exchange endpoint URL. Provider-specific defaults may apply. |
| `userinfo_url` | string | No | User info endpoint URL for fetching profile data after authentication. |

## Environment variables

### `AF_HUB_ADMIN_TOKEN`

| Aspect | Detail |
|--------|--------|
| **Purpose** | Authenticates the admin credential on every non-first-boot startup. |
| **When required** | On every server start **after** the initial first boot. Not required on first boot (the token is generated automatically). Not required when `--reset-admin-token` is passed. |
| **Format** | The plaintext admin token string (e.g. `af_admin_<64 hex chars>`). |
| **Validation** | The server computes the SHA-256 hash of this value and compares it to the `token_hash` stored in the `admin_tokens` table. A mismatch causes a fatal error and the server refuses to start. |
| **If absent** | The server logs a fatal error indicating the missing environment variable and exits with a non-zero code before binding any port. |

### First boot token file

On first boot (zero users in the database), the server generates an admin token
and writes it to a file named `admin_token` in the same directory as
`config.toml` with file permissions `0600`. The absolute path is logged at
warn level. Read this file to obtain the token value for `AF_HUB_ADMIN_TOKEN`.

## Command-line flags

| Flag | Description |
|------|-------------|
| `--reset-admin-token` | Generates a new admin token, updates the database hash, overwrites the `admin_token` file (mode 0600), logs the file path at warn level, and continues normal startup. Skips `AF_HUB_ADMIN_TOKEN` validation. Use this to rotate a compromised or lost admin token. |

## Validation error behavior

When `config.toml` contains an invalid value, the server:

1. Logs a fatal error message that identifies the invalid field and the reason.
2. Exits with a non-zero code.
3. Never binds to any port.

Examples of validation failures:

- `port = 99999` → `"invalid config: port must be in range 1-65535, got 99999"`
- `level = "verbose"` → `"invalid config: level must be one of trace/debug/info/warn/error/fatal/panic, got \"verbose\""`
- `[[auth.oauth]]` entry with empty `client_id` → `"invalid config: auth.oauth[0].client_id must not be empty"`
