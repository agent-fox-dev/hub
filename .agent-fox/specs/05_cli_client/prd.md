---
spec_id: '05'
spec_name: cli_client
title: Cli Client
status: draft
created_at: '2026-07-10T15:01:12.469468+00:00'
updated_at: '2026-07-10T15:08:53.467738+00:00'
owner: ''
source: docs/01_prd.md
schema_version: 1
---
# CLI Client

## Intent

Implement the `afc` CLI binary for af-hub. The CLI provides operator-driven access to login, API key management, workspace registration, and workspace token management, with persistent configuration stored at `$HOME/.af/config.toml`.

This builds on all backend specs: server_foundation (spec 1), oauth_and_users (spec 2), teams (spec 3), and workspaces_and_tokens (spec 4).

## Background

The af-hub platform requires a first-class CLI tool for operators to interact with the hub server. The backend specs (1–4) define the full REST API surface; this spec defines the client-side binary that consumes those APIs. The CLI is the primary non-browser interface for authentication, key lifecycle management, and workspace administration. It targets operators running a self-hosted hub server and must work reliably across a range of network environments.

## Goals

- Build the `afc` CLI binary using Cobra with persistent client configuration.
- Initialize config directory and file on first run (`$HOME/.af/` mode 0700, `config.toml` mode 0600).
- Implement config resolution precedence: CLI flags > env vars > config file > error.
- Implement `afc login` with OAuth authorization code flow (CSRF state parameter, local callback server).
- Implement `afc keys list`, `afc keys refresh`, `afc keys revoke`.
- Implement `afc workspace create`, `afc workspace list`, `afc workspace get`.
- Implement `afc workspace token create`, `afc workspace token list`, `afc workspace token revoke`.
- All commands print JSON to stdout and human-readable messages to stderr.
- Config-mutating commands use atomic writes (write to temp file, rename into place).
- Expose `--version` flag for build-time version injection.

## Non-goals

- Web UI (web_ui_scaffold spec).
- Server binary (server_foundation spec).
- Multi-profile or named-context support.
- Windows path conventions (and Windows browser-open support).
- Workspace token persistence in config.
- HTTP client timeout user-configurability (a fixed 30 s default is used; see Technical Boundaries).
- TLS certificate verification overrides (`--insecure`, `--ca-cert`) — future enhancement.
- Retry behavior for transient network errors — future enhancement.
- File locking for concurrent CLI invocations — future enhancement (see Known Limitations).
- Client-side validation of `--git-url`, `--slug`, `--branch`, or `--label` field format/content (deferred to server).

## Functional Requirements

### Persistent client configuration

- Config path: `$HOME/.af/config.toml`
- On startup, if config doesn't exist, create directory (mode 0700) and file (mode 0600) with empty values.
- **Malformed config:** If the config file exists but contains unparseable TOML, the CLI prints a descriptive error to stderr (e.g., `Error: failed to parse config file: <parse error>`) and exits with code 1. The user must manually fix or delete the corrupted config file. The CLI does not silently overwrite or back up a malformed config.
- Config file structure: four fields — `hub_url`, `user_id`, `api_key`, `key_id`.
  - `hub_url`: base URL of the af-hub server.
  - `user_id`: UUID of the authenticated user.
  - `api_key`: the API key value (secret).
  - `key_id`: the UUID of the current API key, used by `keys refresh` and `keys revoke` without an extra API call.
- Resolution precedence: `--hub-url` / `--user-id` / `--api-key` flags > `AF_HUB_URL` / `AF_HUB_USER_ID` / `AF_HUB_API_KEY` env vars > config file > error with descriptive message.
- Note: `key_id` is only available from the config file; there is no corresponding flag or env var.

#### Atomic config writes

- Config-mutating commands write to a temporary file in the same directory as the config file (`$HOME/.af/`), then rename it into place.
- Temp file name: `config.toml.<random-suffix>` (e.g., using `os.CreateTemp` with the config directory and a fixed prefix pattern).
- No `fsync` before rename is required in this spec. Atomic rename (via `os.Rename`) is sufficient protection against partial writes.
- **Known limitation:** No file locking is implemented. Concurrent CLI invocations performing config mutations may race and produce a last-writer-wins outcome. Operators are expected to run one command at a time. File locking is a future enhancement.

#### Authentication

- All authenticated API requests include the header `Authorization: Bearer <api_key>`, where `<api_key>` is the stored API key value, consistent with the scheme defined in spec 2 (oauth_and_users).

#### Exit codes

- Exit code `0`: success.
- Exit code `1`: any error (API errors, network errors, missing config, validation failures).
- No other exit codes are defined in this spec.

#### Root command behavior

- Invoking `afc` with no subcommand prints help to stdout and exits with code 0 (Cobra's default behavior).
- `afc --help` and `afc help <command>` are supported out of the box via Cobra.
- `afc --version` prints `afc version <semver>` to stdout and exits with code 0. The version string is injected at build time via `-ldflags`. The initial version is `0.1.0`.

#### Flag validation

- **`--expires` flag:** Both `afc login` and `afc workspace token create` accept `--expires` with values restricted to `{0, 30, 60, 90}`. The CLI validates this client-side before making any network request. If an invalid value is provided (e.g., `45`), the CLI prints `Error: --expires must be one of: 0, 30, 60, 90` to stderr and exits with code 1.
- **Required non-empty string flags:** `--git-url` and `--slug` for `workspace create`, and `--workspace` for workspace token commands are checked for non-empty string values only. All format and content validation (URL format, slug character constraints, label length, etc.) is deferred to the server. The CLI prints the server's error response per the standard error handling rules.

#### Error handling for non-JSON server responses

- When the server returns a non-2xx response with a body that cannot be parsed as JSON (e.g., an HTML 502 Bad Gateway from a reverse proxy), the CLI prints `Error: unexpected response from server (HTTP <status>).` to stderr (without the raw body) and exits with code 1. This avoids dumping raw HTML to the terminal and keeps error output clean and predictable.

### Login command

- `afc login --provider <provider> [--expires 0|30|60|90]` — Default provider: `github`. Default expires: `90` days.
- Fetches provider list from hub (`GET /api/v1/auth/providers`).
- **Provider validation:** The CLI checks that the `--provider` value is present in the list returned by `GET /api/v1/auth/providers`. If the value is not found, the CLI prints `Error: unsupported provider: <value>. Available: <comma-separated list>` to stderr and exits with code 1. If the providers endpoint is unreachable, the network error is surfaced to stderr and the command exits with code 1 (the same standard network error behavior as all other commands).
- Generates cryptographically random state parameter for CSRF protection (minimum 16 bytes of entropy).
- Starts local HTTP callback server on a random available port (assigned by the OS via `:0` binding; no fixed range or fallback port is specified). The callback path is `/callback`, resulting in a `redirect_uri` of `http://localhost:<port>/callback`.
- Attempts to open the authorization URL in the user's default browser using `github.com/pkg/browser` (handles macOS via `open` and Linux via `xdg-open` automatically).
  - **Browser-open failure fallback:** The CLI always prints the full authorization URL to stderr regardless of whether the browser opened successfully, so the user can open it manually in headless or restricted environments.
- Waits up to **2 minutes** for the browser redirect callback. If the timeout elapses without receiving a callback, the CLI prints an error to stderr and exits with code 1. The timeout is not user-configurable in this spec.
- Receives callback, validates state, captures authorization code.
- **OAuth callback server HTTP response:** After receiving a valid callback (code + state), the server responds with HTTP 200 and a minimal HTML page containing a visible success message, e.g.:
  ```html
  <!DOCTYPE html>
  <html><body><p>Login successful! You may close this tab.</p></body></html>
  ```
  No redirects and no JavaScript are used. The browser tab can then be closed by the user.
- Exchanges code with hub (`POST /api/v1/auth/callback`) with the following JSON payload:
  ```json
  {
    "provider":     "<provider string, e.g. 'github'>",
    "code":         "<authorization code string>",
    "redirect_uri": "http://localhost:<port>/callback",
    "expires":      <integer: 0 for no expiry, or 30 | 60 | 90 for days>
  }
  ```
  - The `expires` field is always sent as an integer (number of days). When `--expires 0` is passed, the value `0` is sent in the payload (meaning no expiry). The server computes the expiry timestamp from the integer days value.
  - The server response contains a `user` object (with an `id` field) and an `api_key` object (with `id` and `key` fields).
- Stores `hub_url`, `user_id` (from `response.user.id`), `api_key` (from `response.api_key.key`), and `key_id` (from `response.api_key.id`) in config.
- **Already-logged-in behavior:** If the config already contains a non-empty `api_key`, silently overwrite with the new credentials (last login wins). The old API key is **not** automatically revoked. Optionally, a message to stderr noting that existing credentials were replaced is acceptable but not required. The user may manually revoke the old key via `afc keys revoke` if desired.
- Config mutation: sets `hub_url`, `user_id`, `api_key`, `key_id`.

### Keys commands

- **Missing `key_id` behavior:** `afc keys refresh` and `afc keys revoke` both require `key_id` to be present in config. If `key_id` is absent or empty, the CLI prints `Error: key_id is not set. Run "afc login" first.` to stderr and exits with code 1. This follows the same missing-config error pattern as other required config values.

- `afc keys list` — `GET /api/v1/keys`. Prints the raw API response body as-is to stdout, pretty-printed with 2-space indentation.
- `afc keys refresh` — `POST /api/v1/keys/:key_id/refresh` (using `key_id` from config). On success, updates both `api_key` and `key_id` in config with the values from the response. Prints the raw API response body (new key JSON) to stdout, pretty-printed with 2-space indentation.
- `afc keys revoke` — `DELETE /api/v1/keys/:key_id` (using `key_id` from config).
  - On success (2xx): clears `api_key`, `key_id`, and `user_id` from config, and prints `API key revoked.` to stderr.
  - On a 404 response (key already revoked or not found on the server): clears `api_key`, `key_id`, and `user_id` from config, and prints `API key not found on server. Local credentials cleared.` to stderr. The user's intent is to disconnect; the key being already gone server-side should not block local cleanup.
  - On any other non-2xx response: print the API error message to stderr and exit with code 1 (config is not modified).

### Workspace commands

- `afc workspace create --git-url <url> --slug <slug> [--branch <ref>] [--team <team-slug>]` — `POST /api/v1/workspaces`.
  - Request payload fields:
    - `git_url` (string, required): the git repository URL.
    - `slug` (string, required): the workspace slug.
    - `branch` (string, optional): the git branch/ref; omitted from payload if not provided.
    - `team_id` (string/UUID, optional): included only when `--team` is resolved to a UUID; omitted if `--team` is not provided.
  - The CLI only validates that `--git-url` and `--slug` are non-empty strings. All format and content validation is deferred to the server.
  - If `--team` is provided, resolve the team slug to a UUID via `GET /api/v1/teams` (search by slug field).
    - Zero matches: exit 1 with message `team not found: <slug>` to stderr.
    - Multiple matches: exit 1 with message `ambiguous team slug: <slug>` to stderr. (Team slugs are unique per spec 3; this case should not occur in practice but is handled defensively.)
  - Prints the raw API response body to stdout, pretty-printed with 2-space indentation.
- `afc workspace list` — `GET /api/v1/workspaces`. Prints the raw API response body to stdout, pretty-printed with 2-space indentation.
- `afc workspace get <slug>` — `GET /api/v1/workspaces/:slug`. Prints the raw API response body to stdout, pretty-printed with 2-space indentation.

### Workspace token commands

- `afc workspace token create --workspace <slug> [--label <label>] [--expires 0|30|60|90]` — `POST /api/v1/workspaces/:slug/tokens`. Default expires: `30` days.
  - Request payload fields:
    - `label` (string, optional): a human-readable label; omitted from payload if not provided.
    - `expires` (integer): always sent; `0` means no expiry, `30`/`60`/`90` means number of days. The server computes the expiry timestamp from the integer days value.
  - Prints the raw API response body (including the full token value) to stdout, pretty-printed with 2-space indentation. Token is **not** stored in config.
- `afc workspace token list --workspace <slug>` — `GET /api/v1/workspaces/:slug/tokens`. Prints the raw API response body to stdout, pretty-printed with 2-space indentation (metadata only, no secrets, as returned by the server).
- `afc workspace token revoke --workspace <slug> <token-id>` — `DELETE /api/v1/workspaces/:slug/tokens/:token_id`.
  - The `<token-id>` positional argument is required. The command is configured with Cobra's `ExactArgs(1)` validator; if the argument is omitted, Cobra prints its default usage error and exits with code 1.
  - On success (2xx), prints `Token <token-id> revoked.` to stderr.

### JSON output

- All commands that return data print the raw API response body as-is to stdout, pretty-printed with 2-space indentation.
- The CLI does not reshape, filter, or transform fields. Consumers parse the JSON as returned by the server.
- This approach ensures output remains stable relative to the server's API contract and avoids maintaining per-command output schemas.
- Human-readable status and error messages are always written to stderr, never stdout.

### Error handling

- API errors (non-2xx, except where noted for specific commands such as `keys revoke`) attempt JSON parse of the response body and print the error message from the JSON error envelope to stderr, then exit with code 1.
- **Non-JSON error bodies:** If the response body cannot be parsed as JSON (e.g., an HTML 502 from a proxy), the CLI prints `Error: unexpected response from server (HTTP <status>).` to stderr without the raw body, and exits with code 1.
- Network errors (connection refused, timeout) print a descriptive message to stderr and exit with code 1.
- Missing required config values print which value is missing and how to set it (flag, env var, or config file field). For `key_id` specifically: `Error: key_id is not set. Run "afc login" first.`
- Malformed config file: print `Error: failed to parse config file: <parse error>` to stderr and exit with code 1.
- Invalid `--expires` value: print `Error: --expires must be one of: 0, 30, 60, 90` to stderr and exit with code 1.
- Invalid `--provider` value: print `Error: unsupported provider: <value>. Available: <comma-separated list>` to stderr and exit with code 1.
- Exit code `0` on success; exit code `1` on any error.

## Acceptance Criteria / Testing Requirements

### Unit tests (required)

The following behaviors must have unit test coverage:

- **Config resolution precedence:** Verify that CLI flags override env vars, env vars override config file values, and config file values are used when no flag or env var is set. Verify that a missing required value produces a descriptive error. Cover all three sources for `hub_url`, `user_id`, and `api_key`.
- **Atomic write:** Verify that a config write produces a temp file in the config directory and renames it into place; verify the final file contents are correct.
- **CSRF state generation:** Verify that the generated state parameter is cryptographically random and of sufficient length (≥ 16 bytes of entropy).
- **Malformed config:** Verify that a config file with invalid TOML produces an error message to stderr and exit code 1 without modifying the file.
- **`--expires` validation:** Verify that values outside `{0, 30, 60, 90}` produce `Error: --expires must be one of: 0, 30, 60, 90` to stderr and exit code 1 for both `afc login` and `afc workspace token create`.
- **Missing `key_id`:** Verify that `afc keys refresh` and `afc keys revoke` produce `Error: key_id is not set. Run "afc login" first.` to stderr and exit code 1 when `key_id` is absent from config.
- **Provider validation:** Verify that an `--provider` value not in the providers list produces `Error: unsupported provider: <value>. Available: <comma-separated list>` to stderr and exit code 1.
- **Non-JSON error body:** Verify that a non-2xx response with a non-JSON body produces `Error: unexpected response from server (HTTP <status>).` to stderr and exit code 1.

### Integration tests (required)

Integration tests run against a local mock HTTP server and must cover:

- **Login flow:** Mock the `GET /api/v1/auth/providers` and `POST /api/v1/auth/callback` endpoints; verify config is populated correctly after login (hub_url, user_id, api_key, key_id); verify the 2-minute callback timeout results in exit code 1; verify the callback server returns a 200 HTML success page to the browser; verify that an unknown `--provider` value exits with code 1 and the correct error message.
- **Keys commands:** Mock `GET /api/v1/keys`, `POST /api/v1/keys/:key_id/refresh`, `DELETE /api/v1/keys/:key_id`; verify config mutations and stdout/stderr output for each; verify 404 on revoke is treated as success with the `API key not found on server. Local credentials cleared.` message to stderr; verify 2xx revoke prints `API key revoked.` to stderr; verify missing `key_id` in config produces the correct error for both refresh and revoke.
- **Workspace commands:** Mock `GET /api/v1/teams`, `POST /api/v1/workspaces`, `GET /api/v1/workspaces`, `GET /api/v1/workspaces/:slug`; verify team-slug resolution errors (zero matches, multiple matches) print the correct messages and exit code 1; verify correct request payload fields are sent; verify a non-JSON 502 response produces `Error: unexpected response from server (HTTP 502).` to stderr.
- **Workspace token commands:** Mock `POST /api/v1/workspaces/:slug/tokens`, `GET /api/v1/workspaces/:slug/tokens`, `DELETE /api/v1/workspaces/:slug/tokens/:token_id`; verify token value appears on stdout and is not written to config; verify `Token <id> revoked.` is printed to stderr on revoke success; verify omitting `<token-id>` produces a Cobra usage error and exit code 1.

## Technical Boundaries

- **Language:** Go (1.22+)
- **CLI framework:** Cobra (`github.com/spf13/cobra`)
- **Config:** TOML (`github.com/BurntSushi/toml`)
- **Browser open:** `github.com/pkg/browser` (handles macOS via `open`, Linux via `xdg-open`; Windows is out of scope).
- **HTTP client timeout:** 30 seconds (fixed default, not user-configurable in this spec).
- **OAuth callback server timeout:** 2 minutes (fixed, not user-configurable in this spec).
- **OAuth callback port:** Random available port assigned by the OS (`:0` binding). Callback path is `/callback`; `redirect_uri` sent to server is `http://localhost:<port>/callback`.
- **Authorization scheme:** `Authorization: Bearer <api_key>` for all authenticated requests (per spec 2).
- **JSON output:** Raw API response body, pretty-printed with 2-space indentation; no field reshaping.
- **`expires` field:** Sent as an integer (number of days) directly in API payloads; the server computes the expiry timestamp. Value `0` means no expiry. Allowed values: `{0, 30, 60, 90}`; validated client-side before any network request.
- **Version:** Cobra's built-in `Version` field; value injected at build time via `-ldflags`. Initial version: `0.1.0`. Output format: `afc version <semver>`.
- **Positional argument enforcement:** `afc workspace token revoke` uses Cobra's `ExactArgs(1)` validator.
- **Build:** `make build` compiles `afc` binary to `bin/`; version string injected via `-ldflags -X main.version=<semver>` (or equivalent package path).
