# Server Configuration

The `af-hub` server reads its configuration from a TOML file. Use
`--config <path>` to specify an explicit location.

When `--config` is not provided, the server uses XDG Base Directory paths:

| Item | Path | Fallback |
|------|------|----------|
| Config file | `$XDG_CONFIG_HOME/af-hub/config.toml` | `~/.config/af-hub/config.toml` |
| Database | `$XDG_DATA_HOME/af-hub/af-hub.db` | `~/.local/share/af-hub/af-hub.db` |
| Admin token | alongside config file | alongside config file |

When `--config` IS provided, the database defaults to `./data/af-hub.db`
(relative to the working directory) for backward compatibility. The
`database.path` setting in config.toml always takes precedence over both
defaults.

A missing config file is non-fatal; all settings fall back to defaults.
Unrecognized keys are logged as warnings but do not prevent startup.

## Full Reference

```toml
[server]
port = 8080             # Listening port (default: 8080)
bind = "0.0.0.0"        # Bind address (default: "0.0.0.0")
external_url = ""       # Public URL, used for OAuth redirect URI allowlist in production

[database]
path = "./data/af-hub.db"   # SQLite database file path (default depends on --config; see above)

[log]
level = "info"          # Log level: trace, debug, info, warn, error, fatal, panic (default: "info")

[[oauth.providers]]
name = "github"
client_id = ""          # GitHub OAuth app client ID
client_secret = ""      # GitHub OAuth app client secret
authorize_url = ""      # Override GitHub authorize URL (optional)
token_url = ""          # Override GitHub token URL (optional)
userinfo_url = ""       # Override GitHub user info URL (optional)
```

An invalid `log.level` defaults to `info` with a warning.

## OAuth Provider Setup

### 1. Create a GitHub OAuth App

Go to `https://github.com/settings/applications/new` and fill in:

| Field | Value |
|-------|-------|
| Application name | af-hub (or any name you prefer) |
| Homepage URL | `http://localhost:8080` (or your production URL) |
| Authorization callback URL | `http://localhost/callback` |

### 2. Authorization Callback URL

The `afc login` command starts a temporary local HTTP server on
`127.0.0.1` with a **random OS-assigned port**. The actual redirect URI looks
like `http://localhost:<random-port>/callback` — a different port each time.

GitHub treats `http://localhost` specially: when a registered callback URL
uses `localhost`, GitHub permits redirects to **any port** on localhost. This
is why you register `http://localhost/callback` (no port) — it covers every
`afc login` invocation regardless of the ephemeral port chosen.

### 3. Dev Mode vs Production

The hub server validates incoming redirect URIs against an allowlist:

| Mode | Condition | Allowed redirect URIs |
|------|-----------|----------------------|
| Development | `external_url` is empty (default) | Any `http://localhost:*/callback` |
| Production | `external_url` is set | Must match `external_url` exactly |

In production, set `[server] external_url` in your config and register the
matching callback URL in your GitHub OAuth app.

### 4. Configure the Hub

Copy the **Client ID** and **Client Secret** from GitHub into your
`config.toml`:

```toml
[[oauth.providers]]
name = "github"
client_id = "your_client_id"
client_secret = "your_client_secret"
```

The `authorize_url`, `token_url`, and `userinfo_url` fields default to
GitHub's standard endpoints and only need to be set for GitHub Enterprise or
other custom deployments.

## Environment Variables

| Variable | Used by | Description |
|----------|---------|-------------|
| `AF_HUB_ADMIN_TOKEN` | `af-hub` | Admin token for server validation on subsequent boots |
| `AF_HUB_URL` | `afc` | Default hub URL |
| `AF_HUB_USER_ID` | `afc` | Default user ID |
| `AF_HUB_API_KEY` | `afc` | Default API key |

## Admin Bootstrap

On **first boot**, the server creates an admin user and generates an admin
token. The plaintext token is written to a file called `admin_token` next to
the config file. Save this value somewhere secure.

On **subsequent boots**, set the `AF_HUB_ADMIN_TOKEN` environment variable to
the saved token. The server validates its SHA-256 hash on startup and refuses
to start if the value is missing or wrong.

To **reset the admin token**, start the server with `--reset-admin-token`. This
deletes all existing admin tokens and generates a fresh one.

## Database

The server uses an embedded SQLite database (pure Go, no CGo dependency). The
database directory is created automatically if it does not exist.

SQLite pragmas applied on startup:

| Pragma | Value |
|--------|-------|
| `journal_mode` | WAL |
| `foreign_keys` | ON |
| `busy_timeout` | 5000 |

## CLI Client Configuration

The `afc` CLI stores credentials in `~/.af/config.toml`:

```toml
hub_url = "http://localhost:8080"
user_id = "uuid"
api_key = "af_abcd1234_secret"
key_id = "abcd1234"
```

The file is created with mode `0600` (directory `0700`). Writes are atomic
(temp file + rename).

Values are resolved in order: CLI flags, environment variables, config file.
See the [CLI Reference](cli.md) for flag and env var details.
