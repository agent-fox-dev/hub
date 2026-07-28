# Server Configuration

This document is the configuration reference for **af-hub** (API server) and
**afc** (CLI client). Both use TOML configuration files.

## Config File Location

The server loads its configuration from a file named `config.toml`. The search
order is:

1. The path passed via `--config <path>` (highest priority).
2. `$XDG_CONFIG_HOME/af-hub/config.toml`
3. `./config.toml` (current working directory)

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
path    = "/var/lib/af-hub/workspaces"
workers = 4

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
| `external_url` | string | `"http://localhost:8080"` | External URL used for OAuth redirect validation and for constructing git clone URLs (`hub_url` in workspace responses). |
| `mount_point` | string | `"/api/v1"` | API route prefix for all REST endpoints. |
| `max_body_size` | string | `"1MB"` | Maximum request body size. Format: `<int><KB|MB|GB>`. |

### [database]

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `path` | string | `"afhub.db"` | SQLite database file path. Resolved relative to `$XDG_DATA_HOME/af-hub/` when running in a container, or relative to the working directory otherwise. |

### [logging]

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `level` | string | `"info"` | Log level. Valid values: `trace`, `debug`, `info`, `warn`, `error`, `fatal`, `panic`. |

### [workspace]

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `path` | string | -- | Root directory for workspace local clones. Each workspace gets a subdirectory: `<path>/<slug>/trunk/`. |
| `workers` | integer | -- | Number of concurrent clone worker goroutines. |

### [[oauth.providers]]

This section is repeatable (TOML array of tables). Add one entry per OAuth
provider.

| Key | Type | Description |
|-----|------|-------------|
| `name` | string | Provider name (e.g. `"github"`, `"google"`). |
| `client_id` | string | OAuth client ID. Supports env var substitution via `${VAR}` syntax. |
| `client_secret` | string | OAuth client secret. Supports env var substitution via `${VAR}` syntax. |

## Environment Variables

| Variable | Description |
|----------|-------------|
| `XDG_CONFIG_HOME` | Base directory for config files. Server config is read from `$XDG_CONFIG_HOME/af-hub/config.toml`. |
| `XDG_DATA_HOME` | Base directory for data files. Database path is resolved relative to `$XDG_DATA_HOME/af-hub/`. |
| `ADMIN_TOKEN` | Pre-existing admin token. When set, the server uses this token instead of generating a new one at boot. |
| `GITHUB_CLIENT_ID` | GitHub OAuth app client ID (referenced in `config.toml` via `${GITHUB_CLIENT_ID}`). |
| `GITHUB_CLIENT_SECRET` | GitHub OAuth app client secret (referenced in `config.toml` via `${GITHUB_CLIENT_SECRET}`). |

## First Boot

On first boot the server requires `--admin-email` to bootstrap the admin
account:

```
bin/af-hub --admin-email=admin@example.com
```

The server generates an admin token and writes the plaintext to `admin_token`
in the config directory. Save this value. On subsequent boots, pass it via the
`ADMIN_TOKEN` environment variable.

To rotate the admin token, pass `--reset-admin-token`:

```
bin/af-hub --reset-admin-token
```

## CLI Configuration (~/.af/config.toml)

After running `afc login`, credentials are stored in `~/.af/config.toml`:

| Key | Description |
|-----|-------------|
| `endpoint_url` | Hub server URL (e.g. `"http://localhost:8080"`). |
| `api_key` | API key obtained during login. |

The credential helper (`afc credential-helper`) reads from this file to supply
git credentials automatically.

## Container Configuration

The container image sets:

- `XDG_CONFIG_HOME=/config`
- `XDG_DATA_HOME=/data`

The default config path is `/config/af-hub/config.toml` (override by mounting
your own). The database is created at `/data/af-hub/afhub.db`.

### Volumes

| Mount Point | Purpose |
|-------------|---------|
| `/config` | Config directory. Mount a PVC or ConfigMap. |
| `/data` | Persistent data directory. Mount a PVC. |

### Exposed Ports

| Port | Description |
|------|-------------|
| 80 | HTTP (container default) |
| 8080 | HTTP (application default) |

## Kubernetes Deployment

The `deploy/` directory contains reference manifests:

| File | Description |
|------|-------------|
| `deploy/configmap.yaml` | ConfigMap with `config.toml`. |
| `deploy/deployment.yaml` | Deployment spec with volume mounts, health probes, and resource limits. |
| `deploy/pvc.yaml` | PersistentVolumeClaims for config and data. |
| `deploy/service.yaml` | ClusterIP Service on port 8080. |
| `deploy/route.yaml` | OpenShift Route (if applicable). |
| `deploy/secrets.yaml.example` | Secret template for `admin-email`, `github-client-id`, `github-client-secret`. |

### Health Endpoints

| Path | Purpose |
|------|---------|
| `/healthz` | Liveness probe (database ping). |
| `/readyz` | Readiness probe. |
