# afc CLI Reference

`afc` is the command-line client for af-hub. It supports OAuth authentication
and API key management for programmatic access to hub workspaces.

## Global Flags

| Flag | Environment Variable | Description |
|------|---------------------|-------------|
| `--hub-url` | `AF_HUB_URL` | Hub server base URL. The flag takes precedence over the environment variable. |

When neither `--hub-url` nor `AF_HUB_URL` is provided, commands that require
the hub URL will exit with a descriptive error.

## Output Conventions

- **JSON output** (machine-readable) is written to **stdout**.
- **Status messages and errors** (human-readable) are written to **stderr**.
- On failure, `afc` exits with a **non-zero exit code** and prints an error
  message to stderr.

When the hub returns a non-2xx HTTP response, `afc` attempts to parse the
error envelope and display a human-readable message. If the response body
cannot be parsed, `afc` falls back to printing the raw HTTP status code and
response body.

---

## Commands

### login

Authenticate with the hub via the OAuth authorization code flow. The command
fetches available providers from the hub, starts a local callback server,
opens the provider's authorization URL in the default browser, waits for the
OAuth redirect, exchanges the authorization code with the hub, and prints the
returned user object as JSON to stdout.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--provider` | `github` | OAuth provider name. |
| `--hub-url` | | Hub server base URL (or set `AF_HUB_URL`). |

**Behavior:**

1. Fetches the provider list from `GET /api/v1/auth/providers`.
2. Validates that the requested provider exists (exits with an error listing
   available providers if not found).
3. Starts a local HTTP callback server on a random available port.
4. Opens the authorization URL in the default browser (`open` on macOS,
   `xdg-open` on Linux).
5. Waits for the OAuth redirect (times out after 5 minutes).
6. Sends the authorization code to `POST /api/v1/auth/callback` on the hub.
7. Prints the returned user object as JSON to stdout.
8. Shuts down the callback server and releases the port.

The callback server is always shut down on exit, whether the flow succeeds,
times out, encounters an error, or is interrupted by SIGINT/SIGTERM.

**Example:**

```bash
afc login --provider github --hub-url https://hub.example.com
```

**Error conditions:**

- Hub unreachable: prints connection error with the attempted hub URL to stderr.
- Provider not found: prints error listing available providers to stderr.
- Callback timeout (5 minutes): prints timeout error with retry suggestion.
- OAuth provider error: prints the provider's error description to stderr.
- Hub returns HTTP error on callback: parses the error envelope and prints it.
- SIGINT/SIGTERM: shuts down cleanly with no orphaned ports.

---

### keys create

Create a new API key scoped to a workspace. On success, the full key object
(including the plaintext secret) is printed as JSON to stdout exactly once.
The plaintext secret is never logged or re-displayed.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--workspace` | *(required)* | Workspace ID to scope the key to. |
| `--label` | | Human-readable label for the key. |
| `--expires` | `30` | Key expiry in days (0, 30, 60, or 90). |
| `--api-key` | | API key for authentication (or set `AF_HUB_API_KEY`). |
| `--hub-url` | | Hub server base URL (or set `AF_HUB_URL`). |

**Example:**

```bash
afc keys create --workspace ws-123 --label ci-bot --expires 30 \
  --api-key <your-api-key> --hub-url https://hub.example.com
```

**Error conditions:**

- Missing `--workspace` flag: prints usage error to stderr.
- Missing credentials (`--api-key` / `AF_HUB_API_KEY`): prints credential error.
- User not a workspace member: prints membership error from the hub to stderr.

---

### keys list

List all API keys for the authenticated user. Prints a JSON array to stdout
where each element contains `key_id`, `label`, `workspace_id`, `expires_at`,
`created_at`, and `revoked` fields.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--api-key` | | API key for authentication (or set `AF_HUB_API_KEY`). |
| `--hub-url` | | Hub server base URL (or set `AF_HUB_URL`). |

**Example:**

```bash
afc keys list --api-key <your-api-key> --hub-url https://hub.example.com
```

**Error conditions:**

- Hub returns HTTP error (e.g. 401 unauthorized): parses error envelope and
  prints the message to stderr.

---

### keys refresh

Rotate the secret of an existing API key by key ID. The old secret is
invalidated and a new plaintext secret is returned in the updated key object
on stdout as JSON.

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<key-id>` | The ID of the API key to refresh (required). |

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--api-key` | | API key for authentication (or set `AF_HUB_API_KEY`). |
| `--hub-url` | | Hub server base URL (or set `AF_HUB_URL`). |

**Example:**

```bash
afc keys refresh key-abc-123 \
  --api-key <your-api-key> --hub-url https://hub.example.com
```

**Error conditions:**

- Missing `<key-id>` argument: prints usage error to stderr.
- Key not found or not owned by user: parses error envelope from hub and
  prints to stderr.

---

### keys revoke

Permanently revoke an existing API key by key ID. On success, a confirmation
message is printed to stderr. No JSON is printed to stdout.

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<key-id>` | The ID of the API key to revoke (required). |

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--api-key` | | API key for authentication (or set `AF_HUB_API_KEY`). |
| `--hub-url` | | Hub server base URL (or set `AF_HUB_URL`). |

**Example:**

```bash
afc keys revoke key-abc-123 \
  --api-key <your-api-key> --hub-url https://hub.example.com
```

**Error conditions:**

- Missing `<key-id>` argument: prints usage error to stderr.
- Key not found or not owned by user: parses error envelope from hub and
  prints to stderr.

---

## Environment Variables

| Variable | Description |
|----------|-------------|
| `AF_HUB_URL` | Hub server base URL. Overridden by `--hub-url` flag. |
| `AF_HUB_API_KEY` | API key for authentication on key management commands. Overridden by `--api-key` flag. |

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Command completed successfully. |
| `1` | Command failed (error details on stderr). |
