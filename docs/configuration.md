# Server Configuration

This document is the configuration reference for **af-hub** (API server) and
**afc** (CLI client). Both use TOML configuration files.

## Config File Location

The server loads its configuration from a file named `config.toml`. The search
order is:

1. `$XDG_CONFIG_HOME/config.toml` (when `XDG_CONFIG_HOME` is set).
2. `./config.toml` (current working directory, used when `XDG_CONFIG_HOME` is
   unset).

The configuration is loaded by `apikit.LoadConfig()`. hub reads its own
`[sandbox]` section from the same file (see [`[sandbox]`](#sandbox)).

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

# Optional: sandboxes for agents. Leave the whole section out to turn them off.
[sandbox]
provider = "podman"
listen   = "127.0.0.1:8443"   # the sandbox gateway (default); see [sandbox]

[[sandbox.profiles]]
name    = "agent"
default = true
image   = "registry.example.com/agent:1.4"
ttl     = "20m"
secrets = ["GIT_PAT"]
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

### [sandbox]

The `[sandbox]` table and its `[[sandbox.profiles]]` tables define where
sandboxes run and what shape they take. The section is optional. Without it
(or without a config file) sandboxes are off: the hub starts as usual, logs one
info line saying sandboxes are disabled, and `GET /api/v1/sandbox-profiles`
answers `404` with `error_type: sandboxes_disabled`.

With the section present the hub also serves a sandbox gateway on a second
listener (see [The sandbox gateway](#the-sandbox-gateway)): a caller obtains a
lease over the REST API, and creates the one sandbox the lease allows through
the gateway, with the lease's token.

With the section present, the hub validates it at start, right after the admin
bootstrap and before it sets up the workspace root, the job queues, the
workers or any route. The first-boot admin token therefore never depends on
the sandbox configuration. Profiles change only when the file is edited and
the hub is restarted.

- **Unknown keys refuse start.** The error names the full key path, for example
  `sandbox.profiles[1].memroy_mb`. Keys of the sections above are not checked
  here.
- **`${NAME}` is replaced** in every string value of the section, including
  list entries and `env` values, by the environment variable `NAME`, or by an
  empty string when it is unset. A bare `$` stays as it is, and a replacement
  is not expanded again. Note that apikit also expands `$VAR` and `${VAR}` in
  the whole file before it parses its own sections, so a variable whose value
  contains a double quote breaks the start even when only `[sandbox]`
  references it.
- **All failures are reported together.** A start-up error lists every faulty
  key and profile, each naming the profile (by `name`, or as
  `sandbox.profiles[i]` when the name is missing) and the field.
- **No `env` value is ever shown.** Validation errors, the start-up log line and
  the profile listing contain no `env` value and no secret value. The hub reads
  secret values only when the gateway creates a sandbox, and puts them in that
  sandbox's environment and nowhere else. Everything else in the section is shown as
  written after `${NAME}` expansion: profile names, secret names, `image` (the
  listing returns it as is) and the deployment keys appear in errors, so do not
  put a credential into any key but `env`.

#### Deployment keys

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `provider` | string | *(required)* | The provider that creates sandboxes. `"podman"` is the only accepted value; `"gateway"` is refused as not supported by this build. |
| `engine_binary` | string | `"podman"` | The container engine command, `"podman"` or `"docker"`, or a path to one. A bare name is looked up in the `PATH` of the hub process. |
| `runtime` | string | `""` | The engine's OCI runtime: empty for its default, or `"krun"` for a micro-VM (podman only). Passed to the provider unchanged; the provider refuses what it does not support. |
| `userns` | string | `""` | Passed to the engine as `--userns=<value>` when set. |
| `max_ttl` | string | `"4h"` | The longest lifetime a profile may ask for, as a Go duration (`"90m"`, `"4h"`). Must be greater than zero. |
| `network_modes` | list of strings | `["none"]` | The network modes profiles may use. Entries must be non-empty and unique, and each must be a mode the provider honours: `"none"` and `"unrestricted"` for Podman and Docker. A sandbox has no network unless the operator allows one. An empty list is accepted but leaves no mode for any profile, so every profile is then refused. |
| `max_sandboxes` | integer | `16` | The most sandbox leases that may be live at once, across all workspaces. A lease counts while it is `leased` or `active` and its `expires_at` is in the future. At the limit `POST /api/v1/workspaces/:slug/sandboxes` answers `409` with `error_type: sandbox_capacity`. Must be a whole number of at least `1`. The gateway holds at most this many sandboxes too. |
| `listen` | string | `"127.0.0.1:8443"` | The address of the sandbox gateway, `host:port`. The port is a number from `0` to `65535`, and `0` picks a free port. The address is on loopback when the host is `localhost` or a literal IP address that is a loopback address; an empty host, `0.0.0.0`, `::` and any other name are not. No name is resolved. |
| `tls_cert`, `tls_key` | string | `""` | Paths of the gateway's certificate and private key (PEM). Both are set or neither is; the message names the one that is missing. With neither the gateway speaks plaintext. |
| `external_url` | string | `""` | The address lease holders dial, when it is not the address hub listens on, for example behind a proxy. An absolute `http` or `https` URL with a host and nothing else but an optional `/`: no user information, query, fragment or other path. |

A `listen` address that is not on loopback needs TLS (`tls_cert` and `tls_key`)
and an `external_url`: a lease token sent in plaintext over a network would hand
over a sandbox full of secrets. The start-up error for the first names
`sandbox.listen` and says that a plaintext gateway is allowed only on loopback;
the second names `sandbox.external_url`. These failures are reported together
with every other failure of the section.

#### [[sandbox.profiles]]

This table is repeatable. At least one profile is required. The profiles are
published, and a lease copies the one it is issued from, so a sandbox is built
from the lease and not from the file: editing the file and restarting the hub
changes the leases issued afterwards. The descriptions below say what a sandbox
of the profile gets.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `name` | string | *(required)* | The profile name callers choose. Must match `^[a-z0-9][a-z0-9_-]{0,62}$` and be unique. |
| `default` | boolean | `false` | Marks the default profile. At most one profile may set it; having none is valid. |
| `image` | string | *(required)* | The container image. Must not be empty. The hub does not build, pull or check images. |
| `cpus` | number | `0` | CPU bound, `0` or more; a fraction such as `0.5` is allowed. |
| `memory_mb` | integer | `0` | Memory bound in MiB, `0` or more. |
| `pids` | integer | `0` | Process count bound, `0` or more. |
| `disk_mb` | integer | `0` | Disk bound in MiB, `0` or more. The Podman provider cannot bound disk, so a profile that sets it is refused at start. |
| `network` | string | `"none"` | The network mode of the sandbox. Must be one of `network_modes`. `"allowlist"` can be named only when the provider honours it, and this release cannot configure its endpoints. |
| `ttl` | string | *(required)* | The sandbox lifetime as a Go duration. Must be greater than zero and at most `max_ttl`. |
| `secrets` | list of strings | `[]` | Names of the hub secrets injected into the sandbox. Each name must be a valid secret key (alphanumeric and `_`, not starting with a digit, at most 255 characters). Reserved names such as `GIT_PAT` are allowed. |
| `optional_secrets` | list of strings | `[]` | Names of secrets injected when they exist. Same rules as `secrets`. |
| `env` | table of strings | `{}` | Fixed environment variables of the sandbox. Keys follow the secret key rules and must not equal, ignoring case, a name in `secrets` or `optional_secrets`. Values are never shown by the API or in logs. |
| `require_landlock` | boolean | `false` | Refuse to create a sandbox whose agent cannot confine itself with Landlock. |

Zero for `cpus`, `memory_mb`, `pids` and `disk_mb` means the provider's
default. Secret names are compared ignoring case, within `secrets`, within
`optional_secrets` and across the two lists.

#### Checks against the provider

When the keys and profiles pass, the hub builds the provider from
`engine_binary`, `runtime` and `userns` and checks it once:

1. It builds the Podman provider. A missing engine binary or an option the
   provider refuses stops the start with an error that starts with `sandbox:`
   and names `sandbox.provider` and `sandbox.engine_binary`.
2. It probes the engine with one call under a 10-second timeout. The call is
   the provider's `Reap`, the one provider operation that reaches the engine
   without creating a sandbox. It removes the containers and volumes that
   AgentKit labelled as sandboxes and whose own expiry has already passed,
   whichever process created them. An engine that cannot be listed, and an
   expired leftover that cannot be removed, both stop the start with the same
   kind of error (`failed its start-up check`).
3. Every `network_modes` entry must be a mode the provider honours.
4. Each profile is turned into a sandbox spec (image, the profile's fixed `env`
   only, network, resources, `ttl`, `require_landlock`) that AgentKit's
   `sandbox.CheckSpec` must accept against the provider's capabilities. A
   refusal names the profile and carries `CheckSpec`'s text.

The hub never creates a sandbox at start. Callers obtain leases with
`POST /api/v1/workspaces/:slug/sandboxes` (see
[Sandbox Lease Endpoints](api.md#sandbox-lease-endpoints)), and the gateway
creates the sandbox of a lease when its holder asks; the lease table is created
at every start, whether or not the section is present. After a successful start
it logs one info line with the provider name and the profile names, and nothing
else from the section.

#### The sandbox gateway

After the checks above, the hub starts the gateway, before its HTTP server
serves anything and after the lease table exists. A hub without a `[sandbox]`
section binds nothing.

1. If `tls_cert` and `tls_key` are set, it loads the key pair once. A failure
   stops the start with `sandbox.tls_cert and sandbox.tls_key: cannot load the
   key pair:` and the loader's error, before anything is bound. A certificate
   that is renewed is picked up at the next start.
2. It binds `listen`. A failure stops the start with `sandbox.listen "<address>":
   cannot bind the sandbox gateway:` and the bind error.
3. It serves an AgentKit gateway on the listener for as long as the process
   lives, over TLS when a key pair is set. If the gateway stops serving with an
   error, the hub logs it once and exits with status 1, since it would otherwise
   go on issuing leases nobody can use.

The address a lease holder dials is `external_url` when set, otherwise `https://`
(when TLS is configured) or `http://` followed by the address the listener is
really on, so that `listen = "127.0.0.1:0"` gives the port it got. Every lease
shows it as `gateway_url`.

The gateway accepts a lease token and nothing else: it has no static tokens. A
lease holder connects with the AgentKit client and asks for its sandbox:

```go
p, err := gateway.Dial(ctx, lease.GatewayURL, gateway.Options{Token: token})
sb, err := p.Create(ctx, sandbox.Spec{})
```

- **One sandbox per lease.** The first `Create` under a token makes the lease
  `active`; a second one is refused. A token stops working the moment its lease
  is `destroyed` or `failed`, or past its `expires_at`, on the very next call,
  the calls relayed to a sandbox that exists included.
- **The lease decides what the sandbox is.** The image, network, resources,
  Landlock setting and fixed `env` are those of the lease, which copied them from
  the profile, and the TTL is the time the lease has left. Of the spec the caller
  sends hub reads its `Workdir` and its labels only; labels that start with `hub.`
  are dropped, and `hub.lease` and `hub.workspace` are set on the sandbox.
- **Secrets reach a sandbox only through the hub.** At `Create` the hub resolves
  the profile's `secrets` and `optional_secrets` from the workspace, then (only
  if the workspace owner is a member of it) the workspace's org, then the owner's
  user scope, the first scope that has the name winning, and puts them in the
  sandbox's environment. A required secret that exists nowhere fails the lease
  with `secret_missing:`, the name and the scopes searched.
- **Nothing secret comes back.** The spec the gateway reports to the client has
  every environment value replaced by `[redacted]`, and an error from the provider
  is stored, logged, put in an event and returned only after every secret value,
  every fixed `env` value and the user information of every URL in it is removed.

Things to know when operating it:

- **The client checks first.** The AgentKit client and the gateway check the
  caller's TTL (one hour when it is empty) against `max_ttl` and its network mode
  (`none` when it is empty) against `network_modes` before the hub sees the
  request, although the hub then ignores both. With `max_ttl` under `1h`, or
  `network_modes` without `"none"`, a client has to ask for a TTL and a network
  that fit. A client built from a development checkout of AgentKit has no default
  image and refuses an empty `Image`; the hub ignores whatever it is.
- **Stopping the hub does not stop the sandboxes.** The hub does not shut the
  gateway down, so a sandbox that is left when the process exits lives until the
  TTL the provider enforces. The reap the gateway runs when the hub starts, and
  every ten minutes after, removes what has passed its own expiry.
- **A `DELETE` on a lease destroys its sandbox.** If the provider cannot, the
  answer is `502` with `error_type: sandbox_provider_error`, the lease stays
  `active` and the call can be repeated (see
  [DELETE /api/v1/workspaces/:slug/sandboxes/:id](api.md#delete-apiv1workspacesslugsandboxesid)).
- **The deployment is unchanged.** The manifests in `deploy/` expose the REST
  port only; reaching the gateway from outside the pod (a second Service port,
  gRPC through a route) is a deployment decision.

```toml
[sandbox]
provider      = "podman"
engine_binary = "podman"
max_ttl       = "2h"
network_modes = ["none", "unrestricted"]
listen        = "0.0.0.0:8443"
tls_cert      = "/etc/af-hub/gateway.crt"
tls_key       = "/etc/af-hub/gateway.key"
external_url  = "https://sandboxes.example.com"

[[sandbox.profiles]]
name             = "agent"
default          = true
image            = "registry.example.com/agent:1.4"
cpus             = 2
memory_mb        = 4096
pids             = 512
network          = "none"
ttl              = "20m"
secrets          = ["ANTHROPIC_API_KEY", "GIT_PAT"]
optional_secrets = ["GH_TOKEN"]
require_landlock = true

[sandbox.profiles.env]
LANG = "C.UTF-8"
TZ   = "UTC"

[[sandbox.profiles]]
name    = "build"
image   = "registry.example.com/build:3"
network = "unrestricted"
ttl     = "1h"
```

Callers read the validated profiles with `GET /api/v1/sandbox-profiles` and
`afc sandbox profiles`. The hub registers the `sandboxes:read` and
`sandboxes:write` PAT scopes on every start, whether or not the section is
present.

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
git for merge, batch rebase, every carry-patch operation (sync, rebuild,
preview, rerere), and for serving clone/fetch requests on the built-in git
server (`git upload-pack --stateless-rpc`). Pushes to the git server, initial
workspace clones, and the upstream fetch of carry-patch workspaces use go-git. No git identity needs
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
