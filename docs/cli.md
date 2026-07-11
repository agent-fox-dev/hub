# CLI Reference

`afc` is the command-line client for the agent-fox hub. It authenticates with
the server, manages API keys, and creates and inspects workspaces.

## Installation

```sh
make build
```

The binary is written to `bin/afc`.

Print the version:

```sh
afc --version
# afc version 0.1.0
```

## Configuration

Credentials are stored in `~/.af/config.toml` (created with mode `0600` in a
directory with mode `0700`):

```toml
hub_url = "http://localhost:8080"
user_id = "uuid"
api_key = "af_abcd1234_secret"
key_id = "abcd1234"
```

Values are resolved in this order (first wins):

1. CLI flags (`--hub-url`, `--user-id`, `--api-key`)
2. Environment variables (`AF_HUB_URL`, `AF_HUB_USER_ID`, `AF_HUB_API_KEY`)
3. Config file (`~/.af/config.toml`)

## Global Flags

| Flag | Env var | Description |
|------|---------|-------------|
| `--hub-url <url>` | `AF_HUB_URL` | Hub server URL |
| `--user-id <id>` | `AF_HUB_USER_ID` | User ID |
| `--api-key <key>` | `AF_HUB_API_KEY` | API key |

---

## Commands

### `afc login`

Authenticate with the hub via GitHub OAuth. Opens a browser, starts a local
callback server, exchanges the authorization code, and saves credentials to the
config file.

```sh
afc login
afc login --provider github --expires 90
```

| Flag | Default | Description |
|------|---------|-------------|
| `--provider` | `github` | OAuth provider name |
| `--expires` | `90` | API key lifetime in days (0, 30, 60, or 90) |

---

### `afc keys list`

List API keys. Admins see all keys; users see their own.

```sh
afc keys list
```

Output: JSON array of key objects to stdout.

---

### `afc keys refresh`

Rotate the current API key. Generates a new secret while keeping the same key
ID. Updates the local config file with the new credentials.

```sh
afc keys refresh
```

Output: JSON key object (including new secret) to stdout.

---

### `afc keys revoke`

Revoke the current API key and clear local credentials (`api_key`, `key_id`,
`user_id`) from the config file.

```sh
afc keys revoke
```

---

### `afc workspace create`

Register a new workspace.

```sh
afc workspace create --slug my-workspace --git-url https://github.com/org/repo.git
afc workspace create --slug my-workspace --git-url https://github.com/org/repo.git --branch feature/work --team backend
```

| Flag | Required | Description |
|------|----------|-------------|
| `--slug` | Yes | Workspace slug (3-64 chars, lowercase alphanumeric + hyphens) |
| `--git-url` | Yes | Repository URL (HTTPS or SSH) |
| `--branch` | No | Git branch reference |
| `--team` | No | Team slug (resolved to UUID before sending) |

Output: JSON workspace object to stdout.

---

### `afc workspace list`

List all workspaces for the authenticated user.

```sh
afc workspace list
```

Output: JSON array of workspace objects to stdout.

---

### `afc workspace get <slug>`

Get a workspace by slug.

```sh
afc workspace get my-workspace
```

Output: JSON workspace object to stdout.

---

### `afc workspace token create`

Create a workspace token. The plaintext token is printed once and not saved to
the config file.

```sh
afc workspace token create --workspace my-workspace
afc workspace token create --workspace my-workspace --label ci-read --expires 30
```

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--workspace` | Yes | -- | Workspace slug |
| `--label` | No | -- | Token label (max 128 chars) |
| `--expires` | No | `30` | Lifetime in days (0, 30, 60, or 90) |

Output: JSON token object (including plaintext secret) to stdout.

---

### `afc workspace token list`

List workspace tokens (metadata only, no secrets).

```sh
afc workspace token list --workspace my-workspace
```

| Flag | Required | Description |
|------|----------|-------------|
| `--workspace` | Yes | Workspace slug |

Output: JSON array of token metadata to stdout.

---

### `afc workspace token revoke <token-id>`

Revoke a workspace token.

```sh
afc workspace token revoke abc12345 --workspace my-workspace
```

| Flag | Required | Description |
|------|----------|-------------|
| `--workspace` | Yes | Workspace slug |

The `token-id` is passed as a positional argument.
