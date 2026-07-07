# Configuration Reference

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
