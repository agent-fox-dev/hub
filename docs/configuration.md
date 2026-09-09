# Server Configuration

This document is the configuration reference for **af-hub** (API server) and
**afc** (CLI client). Both use TOML configuration files.

## Config File Location

The server loads its configuration from a file named `config.toml`. The search
order is:

1. `$XDG_CONFIG_HOME/config.toml` (when `XDG_CONFIG_HOME` is set).
2. `./config.toml` (current working directory, used when `XDG_CONFIG_HOME` is
   unset).

The configuration is loaded by `apikit.LoadConfig()`.

## Example config.toml

```toml
[server]
port          = 8080
bind          = "0.0.0.0"
external_url  = "http://localhost:8080"
mount_point   = "/api/v1"
max_body_size = "1MB"

[database]
path = "afhub.db"

[logging]
level = "info"

[workspace]
workers = 4              # path omitted; resolved at runtime (see defaults below)

[[oauth.providers]]
name          = "github"
client_id     = "${GITHUB_CLIENT_ID}"
client_secret = "${GITHUB_CLIENT_SECRET}"
```

## Server Configuration Sections

### [server]

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `port` | integer | `8080` | Listen port. `0` selects an ephemeral port. Range: 0--65535. |
| `bind` | string | `"0.0.0.0"` | Bind address. |
| `external_url` | string | `""` (unset) | External URL used for OAuth redirect validation and for constructing git clone URLs (`hub_url` in workspace responses). When empty, `hub_url` in workspace responses is null. |
| `mount_point` | string | `"/api/v1"` | API route prefix for all REST endpoints. |
| `max_body_size` | string | `"1MB"` | Maximum request body size. Format: `<int><KB|MB|GB>`. |

### [database]

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `path` | string | `"./data/apikit.db"` | SQLite database file path. When omitted, defaults to `./data/apikit.db` (or `$XDG_DATA_HOME/apikit.db` when `XDG_DATA_HOME` is set). Bare filenames are resolved relative to `$XDG_DATA_HOME/` when that variable is set, or relative to the working directory otherwise. All shipped `config.toml` examples override this to `"afhub.db"`. |

### [logging]

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `level` | string | `"info"` | Log level. Valid values: `trace`, `debug`, `info`, `warn`, `error`, `fatal`, `panic`. |
| `log_health_probes` | boolean | `false` | When `false`, health probe requests (`/healthz`, `/readyz`) are suppressed from access logs. |

### [workspace]

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `path` | string | `./data/workspaces` | Root directory for workspace local clones. Each workspace gets a subdirectory: `<path>/<slug>/trunk/`. Bare names are resolved relative to `$XDG_DATA_HOME/` when set. |
| `workers` | integer | `4` | Number of concurrent clone worker goroutines. |

### [[oauth.providers]]

This section is repeatable (TOML array of tables). Add one entry per OAuth
provider.

| Key | Type | Description |
|-----|------|-------------|
| `name` | string | Provider name (e.g. `"github"`, `"google"`). Required. |
| `client_id` | string | OAuth client ID. Supports env var substitution via `${VAR}` syntax. Required. |
| `client_secret` | string | OAuth client secret. Supports env var substitution via `${VAR}` syntax. Required. |
| `authorize_url` | string | Override the built-in OAuth authorization endpoint URL. Optional; used for custom or self-hosted providers. |
| `token_url` | string | Override the built-in OAuth token endpoint URL. Optional; used for custom or self-hosted providers. |
| `userinfo_url` | string | Override the built-in OAuth userinfo endpoint URL. Optional; used for custom or self-hosted providers. |

## Environment Variables

| Variable | Description |
|----------|-------------|
| `XDG_CONFIG_HOME` | Base directory for config files. Server config is read from `$XDG_CONFIG_HOME/config.toml`. |
| `XDG_DATA_HOME` | Base directory for data files. Bare database and workspace paths are resolved relative to `$XDG_DATA_HOME/`. |
| `ADMIN_TOKEN` | Makefile convenience variable for operator use (e.g. in `curl` commands). Not read by the server binary. The bootstrap sequence always generates tokens on first boot (or rotation) and validates them at request time by SHA-256 hash comparison against the database. |
| `ADMIN_EMAIL` | Kubernetes deployment convenience variable. Not read by the server binary; the `deployment.yaml` init script passes its value as the `--admin-email` flag argument. Sourced from the `af-hub-secrets` Secret. |
| `GITHUB_CLIENT_ID` | GitHub OAuth app client ID (referenced in `config.toml` via `${GITHUB_CLIENT_ID}`). |
| `GITHUB_CLIENT_SECRET` | GitHub OAuth app client secret (referenced in `config.toml` via `${GITHUB_CLIENT_SECRET}`). |
| `AF_AUDIT_MAX_AGE_DAYS` | Maximum age in days for retained agent and hub audit event records. Default: `90`. |
| `AF_AUDIT_MAX_RUNS` | Maximum number of distinct run_ids to retain in agent_audit_events. Oldest runs (by MIN(timestamp)) are pruned first. Default: `50`. |
| `AF_TRACE_MAX_AGE_DAYS` | Maximum age in days for retained agent trace records. Default: `30`. |
| `AF_SESSION_MAX_AGE_DAYS` | Maximum age in days for retained completed session records. Default: `90`. |
| `AF_SESSION_MAX_ACTIVE_AGE_DAYS` | Maximum age in days for active (orphaned) sessions before the retention worker force-closes them with status `timeout`. Default: `7`. |
| `AF_POSTMORTEM_MAX_AGE_DAYS` | Maximum age in days for retained postmortem records. Default: `180`. |
| `AF_AUDIT_ORPHAN_RETENTION_DAYS` | Grace period in days before orphaned audit data (workspace no longer in SQLite) is deleted. Default: `30`. |
| `AF_AUDIT_DB_PATH` | Path to the DuckDB audit database file. Default: `<dir of SQLite database>/audit.duckdb`. |
| `AF_SSE_MAX_CONNECTIONS` | Maximum number of concurrent SSE connections accepted by the event streaming endpoint. Accepts positive integers; invalid or non-positive values fall back to the default with a warning log. Default: `100`. |
| `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME`, `GIT_COMMITTER_EMAIL` | Identity used for commits the hub creates (carry-patch cherry-picks and merges, merge jobs). Default: `af-hub <af-hub@localhost>` when unset. |

All `AF_*_DAYS` and `AF_AUDIT_MAX_RUNS` values must be positive integers;
anything else (including `0`) falls back to the default with a warning log.

The hub runs git with a hardened environment: `GIT_DIR`, `GIT_WORK_TREE`,
`GIT_INDEX_FILE`, `GIT_CONFIG_PARAMETERS` and similar repository-redirecting
variables are dropped, `LC_ALL=C`, `GIT_EDITOR=true`, `GIT_TERMINAL_PROMPT=0`
and `GIT_CONFIG_NOSYSTEM=1` are forced, each git invocation runs in its own
process group and is killed as a group on cancellation, and commands without
an explicit deadline are bounded to 10 minutes.

## First Boot

The server can start without `--admin-email`. In that case, no admin account
is created and the server begins accepting requests immediately:

```
bin/hub
```

To bootstrap the admin account, pass `--admin-email` on first boot:

```
bin/hub --admin-email=admin@example.com
```

When `--admin-email` is provided, the server generates an admin token, writes
the plaintext to `admin_token` in the config directory, and **exits
immediately** (the process terminates with `log.Fatal`). It does not start
serving. The operator must:

1. Save the token value from the `admin_token` file.
2. Delete the `admin_token` file.
3. Restart the server without `--admin-email`.

On subsequent boots the server refuses to start if the `admin_token` file
still exists (file-presence guard).

To rotate the admin token, pass `--reset-admin-token`. The same
generate-and-exit behaviour applies:

```
bin/hub --reset-admin-token
```

## CLI Configuration (~/.af/config.toml)

After running `afc login`, credentials are stored in `~/.af/config.toml`:

| Key | Description |
|-----|-------------|
| `endpoint_url` | Hub server URL (e.g. `"http://localhost:8080"`). |
| `user_id` | Authenticated user ID. |
| `api_key` | API key obtained during login. |

The credential helper (`afc credential-helper`) reads from this file to supply
git credentials automatically.

## Container Configuration

The container image sets:

- `XDG_CONFIG_HOME=/config/af-hub`
- `XDG_DATA_HOME=/data/af-hub`

The bundled default config is installed at `/config/af-hub/config.toml`, so
the server finds it without any overrides. It contains only the `[server]`,
`[database]`, and `[logging]` sections; the `[workspace]` and
`[[oauth.providers]]` sections are omitted and fall back to programmatic
defaults. Mount your own `config.toml` at `/config/af-hub/config.toml` to
replace it.

With the bundled config (`path = "afhub.db"`) the SQLite database lives at
`/data/af-hub/afhub.db`, the DuckDB audit database at
`/data/af-hub/audit.duckdb`, and workspace clones under
`/data/af-hub/workspaces/`. If no config file is found at all, the
programmatic default database path is `/data/af-hub/apikit.db`.

The runtime image includes the `git` CLI (`git-core`). The hub shells out to
git for merge, batch rebase, and every carry-patch operation (sync, rebuild,
preview, rerere); clone, fetch, and push (including the upstream fetch of
carry-patch workspaces) use go-git and do not need it. No git identity needs
to be configured in the container: commits fall back to
`af-hub <af-hub@localhost>` unless the `GIT_AUTHOR_*` / `GIT_COMMITTER_*`
variables are set.

The container entrypoint is `/usr/local/bin/run`, a shell script that executes
`/usr/bin/hub` with no flags.

### Volumes

| Mount Point | Purpose |
|-------------|---------|
| `/config/af-hub` | Config directory. Mount a PVC or ConfigMap. |
| `/data/af-hub` | Persistent data directory. Mount a PVC. |

### Exposed Ports

| Port | Description |
|------|-------------|
| 80 | HTTP (container default) |
| 8080 | HTTP (application default) |

## Kubernetes Deployment

The Kubernetes `deployment.yaml` sets the same environment variables as the
image (`XDG_CONFIG_HOME=/config/af-hub` and `XDG_DATA_HOME=/data/af-hub`) and
mounts volumes at `/config/af-hub` and `/data/af-hub`. The deployment resolves
the config file at `/config/af-hub/config.toml` and the database at
`/data/af-hub/afhub.db`.

The `deploy/` directory contains reference manifests:

| File | Description |
|------|-------------|
| `deploy/configmap.yaml` | ConfigMap with `config.toml`. Includes `[server]`, `[database]`, `[logging]`, and `[[oauth.providers]]` sections. Omits the `[workspace]` section; omitted sections use programmatic defaults (`path` = `$XDG_DATA_HOME/workspaces`, `workers` = 4). |
| `deploy/deployment.yaml` | Deployment spec with volume mounts, health probes, and resource limits. |
| `deploy/pvc.yaml` | PersistentVolumeClaims for config (1Gi) and data (10Gi). |
| `deploy/service.yaml` | ClusterIP Service on port 8080. |
| `deploy/route.yaml` | OpenShift Route with TLS edge termination. |
| `deploy/secrets.yaml.example` | Secret template for `admin-email`, `github-client-id`, `github-client-secret`. |

### Health Endpoints

| Path | Purpose |
|------|---------|
| `/healthz` | Liveness probe (always returns ok). |
| `/readyz` | Readiness probe (database ping via HealthChecker). |
