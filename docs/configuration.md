# Server Configuration

The `af-hub` server reads its configuration from a TOML file. By default it
looks for `config.toml` in the working directory. Use `--config <path>` to
specify an alternative location.

A missing config file is non-fatal; all settings fall back to defaults.
Unrecognized keys are logged as warnings but do not prevent startup.

## Full Reference

```toml
[server]
port = 8080             # Listening port (default: 8080)
bind = "0.0.0.0"        # Bind address (default: "0.0.0.0")
external_url = ""       # Public URL, used for OAuth redirect URI allowlist in production

[database]
path = "./data/af-hub.db"   # SQLite database file path (default: "./data/af-hub.db")

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

Create a GitHub OAuth application at
`https://github.com/settings/applications/new`. Set the callback URL to match
where the CLI will listen during login:

- **Development:** `http://localhost:<port>/callback` (any localhost port is accepted)
- **Production:** Must match the `[server] external_url` value

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
