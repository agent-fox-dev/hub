# Server Configuration

This document is for operators who run the **hub** server: where it reads its
configuration, every `config.toml` key with its default, the environment
variables it reads, the first boot, and how the container image and the
Kubernetes manifests are set up. A short section covers the configuration of
the **afc** client. The REST API is described in [api.md](api.md), the `afc`
commands in [cli.md](cli.md), credentials and scopes in
[permissions.md](permissions.md), and how the server works inside in
[architecture.md](architecture.md).

## Contents

- [Config file location](#config-file-location)
- [Minimal config.toml](#minimal-configtoml)
- [Example config.toml](#example-configtoml)
- [Key reference](#key-reference)
  - [`[server]`](#server)
  - [`[database]`](#database)
  - [`[logging]`](#logging)
  - [`[workspace]`](#workspace)
  - [`[[oauth.providers]]`](#oauthproviders)
  - [`[sandbox]`](#sandbox)
    - [Provider keys](#provider-keys)
    - [Limit keys](#limit-keys)
    - [Gateway listener keys](#gateway-listener-keys)
    - [`[[sandbox.profiles]]`](#sandboxprofiles)
    - [Start-up checks](#start-up-checks)
    - [The podman provider](#the-podman-provider)
    - [The remote gateway provider](#the-remote-gateway-provider)
    - [The sandbox gateway](#the-sandbox-gateway)
    - [Lease lifetime](#lease-lifetime)
    - [Sandbox example](#sandbox-example)
- [Forge Access](#forge-access)
- [Environment Variables](#environment-variables)
  - [Paths and deployment](#paths-and-deployment)
  - [Audit retention and limits](#audit-retention-and-limits)
  - [Git](#git)
  - [Forge client](#forge-client)
- [First Boot](#first-boot)
- [CLI Configuration (~/.af/config.toml)](#cli-configuration-afconfigtoml)
- [Container Configuration](#container-configuration)
  - [Volumes](#volumes)
  - [Exposed Ports](#exposed-ports)
- [Kubernetes Deployment](#kubernetes-deployment)
  - [Health Endpoints](#health-endpoints)

## Config file location

The server reads one file, `config.toml`:

1. `$XDG_CONFIG_HOME/config.toml` when `XDG_CONFIG_HOME` is set;
2. otherwise `./config.toml` in the working directory.

The file is optional: without it every key takes its default and sandboxes are
off. The configuration is read once, at start; a change takes effect when the
hub is restarted.

**Environment references.** For the sections from `[server]` to
`[[oauth.providers]]`, `$VAR` and `${VAR}` are replaced by the value of the
environment variable, which is how the shipped examples keep the OAuth secrets
out of the file. The replacement is made in the text of the whole file before it
is parsed, so a variable whose value contains a double quote breaks the parse
and stops the start, even when only the `[sandbox]` section references it. The
`[sandbox]` section is read with its own, stricter rule (see
[`[sandbox]`](#sandbox)).

## Minimal config.toml

Every key has a default. A hub that users can log in to with GitHub, that hands
out clone URLs and webhook URLs, needs an OAuth provider and its external URL:

```toml
[server]
external_url = "https://hub.example.com"

[database]
path = "afhub.db"

[[oauth.providers]]
name          = "github"
client_id     = "${GITHUB_CLIENT_ID}"
client_secret = "${GITHUB_CLIENT_SECRET}"
```

## Example config.toml

Every section, with the defaults written out. The `[sandbox]` section is
optional; leave it out to keep sandboxes off.

```toml
[server]
port          = 8080                       # listen port; 0 picks a free one
bind          = "0.0.0.0"                  # bind address
external_url  = "https://hub.example.com"  # clone URLs, webhook URLs, OAuth redirects
mount_point   = "/api/v1"                  # prefix of every REST route
max_body_size = "1MB"                      # <int><KB|MB|GB>

[database]
path = "afhub.db"                          # SQLite file; bare names go under $XDG_DATA_HOME

[logging]
level             = "info"                 # trace, debug, info, warn, error, fatal, panic
log_health_probes = false                  # log /healthz and /readyz requests

[workspace]
# path    = "./data/workspaces"            # root of the workspace clones
workers = 4                                # clones that run at once

[[oauth.providers]]
name          = "github"
client_id     = "${GITHUB_CLIENT_ID}"
client_secret = "${GITHUB_CLIENT_SECRET}"

# Optional: sandboxes for agents.
[sandbox]
provider = "podman"
listen   = "127.0.0.1:8443"                # the sandbox gateway

[[sandbox.profiles]]
name    = "agent"
default = true
image   = "registry.example.com/agent:1.4"
ttl     = "20m"
secrets = ["GIT_PAT"]
```

A larger sandbox section is shown under [Sandbox example](#sandbox-example).

## Key reference

### [server]

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `port` | integer | `8080` | Listen port, `0` to `65535`. `0` picks a free port. |
| `bind` | string | `"0.0.0.0"` | Bind address. |
| `external_url` | string | `""` | The hub's public URL. Used to validate OAuth redirects, to build the clone URL of a workspace (`hub_url`) and its webhook URL (`<external_url>/hooks/forge/<slug>`, shown by `afc forge webhook set` and `GET .../forge`). When empty, `hub_url` and the webhook `url` are null; a webhook secret can be set all the same. |
| `mount_point` | string | `"/api/v1"` | Prefix of every REST route. |
| `max_body_size` | string | `"1MB"` | Largest request body, as `<int><KB\|MB\|GB>`. The webhook ingress has its own limit (see [Forge Access](#forge-access)). |

### [database]

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `path` | string | `"./data/apikit.db"` | SQLite database file. Without the key the default is `./data/apikit.db`, or `$XDG_DATA_HOME/apikit.db` when `XDG_DATA_HOME` is set. A bare file name is resolved under `$XDG_DATA_HOME/` when it is set, else in the working directory. Every shipped `config.toml` sets `"afhub.db"`. The audit database is kept next to this file (see [`AF_AUDIT_DB_PATH`](#audit-retention-and-limits)). |

### [logging]

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `level` | string | `"info"` | Log level: `trace`, `debug`, `info`, `warn`, `error`, `fatal` or `panic`. |
| `log_health_probes` | boolean | `false` | When `false`, requests to `/healthz` and `/readyz` are left out of the access log. |

### [workspace]

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `path` | string | `"./data/workspaces"` | Root directory of the workspace clones; each workspace gets `<path>/<slug>/trunk/`. A bare name is resolved under `$XDG_DATA_HOME/` when it is set, so the default becomes `$XDG_DATA_HOME/workspaces`. Created at start if missing. |
| `workers` | integer | `4` | Number of workspace clones that run at once. |

### [[oauth.providers]]

Repeatable: one table per OAuth provider users may log in with.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `name` | string | *(required)* | Provider name, for example `"github"` or `"google"`. |
| `client_id` | string | *(required)* | OAuth client ID. Write it as `"${VAR}"` to keep it in the environment. |
| `client_secret` | string | *(required)* | OAuth client secret. Write it as `"${VAR}"`. |
| `authorize_url` | string | built in | Overrides the provider's authorization endpoint, for a custom or self-hosted provider. |
| `token_url` | string | built in | Overrides the provider's token endpoint. |
| `userinfo_url` | string | built in | Overrides the provider's userinfo endpoint. |

### [sandbox]

The `[sandbox]` table and its `[[sandbox.profiles]]` tables say where sandboxes
run and what shape they take. The section is optional. Without it sandboxes are
off: the hub starts as usual, logs one line saying sandboxes are disabled, and
every sandbox route answers `404` with `error_type: sandboxes_disabled`. The
`sandboxes:read` and `sandboxes:write` scopes are registered either way, so PATs
can carry them before sandboxes are turned on.

With the section present, a caller obtains a lease over the REST API
([Sandbox Lease Endpoints](api.md#sandbox-lease-endpoints)) and creates the one
sandbox the lease allows through the [sandbox gateway](#the-sandbox-gateway), a
second listener of the hub, with the lease's token. Callers read the profiles
with `GET /api/v1/sandbox-profiles` and `afc sandbox profiles`.

Rules for the whole section:

- **It is checked at start.** A faulty section stops the start (see
  [Start-up checks](#start-up-checks)). Profiles change only when the file is
  edited and the hub restarted.
- **Unknown keys stop the start.** The error names the full key path, for
  example `sandbox.profiles[1].memroy_mb`.
- **`${NAME}` is replaced** in every string value of the section, list entries
  and `env` values included, by the environment variable `NAME`, or by an empty
  string when it is unset. A bare `$` stays as it is, and a replacement is not
  expanded again.
- **All failures are reported together.** The start-up error lists every faulty
  key and profile, each naming the profile (by `name`, or as
  `sandbox.profiles[i]` when it has none) and the field.
- **Only `env` values and secrets are kept private.** Errors, the start-up log
  line and the profile listing never contain an `env` value or a secret value;
  secret values are read only when a sandbox is created, into that sandbox's
  environment. Everything else, as written after `${NAME}` expansion, can appear
  in errors or the listing (profile names, secret names, `image`, the
  deployment keys), so put a credential in no key but `env` and
  `gateway_token`.

#### Provider keys

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `provider` | string | *(required)* | Where sandboxes run: `"podman"`, a container engine on the hub's own host (Podman or Docker, see [The podman provider](#the-podman-provider)), or `"gateway"`, a remote `agentkit-gateway` (see [The remote gateway provider](#the-remote-gateway-provider)). Any other value stops the start; the error names `sandbox.provider` and lists both. |
| `engine_binary` | string | `"podman"` | The container engine: `"podman"`, `"docker"` or a path to one. A bare name is looked up in the hub's `PATH`. Provider `"podman"` only. |
| `runtime` | string | `""` | The engine's OCI runtime: empty for its default, or `"krun"` for a micro-VM (Podman only). The provider refuses what it does not support. Provider `"podman"` only. |
| `userns` | string | `""` | Passed to the engine as `--userns=<value>` when set. Provider `"podman"` only. |
| `gateway_url` | string | *(required with `"gateway"`)* | Address of the remote gateway: `https://host[:port]`, or `http://host[:port]` only when the host is `localhost` or a literal loopback IP address (no name is resolved). A port, if given, from `1` to `65535`; no user information, query or fragment; a path that is empty or `/`. Empty after `${NAME}` expansion counts as missing. Provider `"gateway"` only. |
| `gateway_token` | string | *(required with `"gateway"`)* | The bearer token the hub presents to the remote gateway: at least 16 characters, all printable ASCII without spaces (`0x21` to `0x7E`), so a tab, a trailing newline from a secret file and any non-ASCII byte are refused. Write it as `"${AGENTKIT_GATEWAY_TOKEN}"`. No error shows the token or any part of it. Provider `"gateway"` only. |

**Keys of the other provider stop the start.** Under `"gateway"` a non-empty
`engine_binary`, `runtime` or `userns`, and under `"podman"` a non-empty
`gateway_url` or `gateway_token`, is refused with one failure per key, naming
the key and the provider it applies to (for example
`sandbox.runtime applies only to sandbox.provider "podman"`). A key that would
do nothing is not ignored: an operator who sets `runtime = "krun"` would believe
in an isolation a remote gateway does not give.

#### Limit keys

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `max_ttl` | string | `"4h"` | The longest lifetime a profile may ask for, as a Go duration (`"90m"`, `"4h"`). Greater than zero. |
| `network_modes` | list of strings | `["none"]` | The network modes profiles may use. Entries are non-empty and unique, and each must be a mode the provider honours (see the provider sections). A sandbox has no network unless the operator allows one. An empty list is accepted but leaves no mode for any profile, so every profile is then refused. |
| `max_sandboxes` | integer | `16` | The most leases live at once, across all workspaces; a whole number of at least `1`. A lease counts while it is `leased` or `active` and before its `expires_at`; a `leased` lease stops counting at its `lease_expires_at`. At the limit `POST /api/v1/workspaces/:slug/sandboxes` answers `409` with `error_type: sandbox_capacity`. The gateway holds at most this many sandboxes too. |
| `lease_timeout` | string | `"5m"` | How long a lease may wait for its sandbox to be created, as a Go duration; at least `1s` and at most `max_ttl`. A lease still `leased` after that lapses (see [Lease lifetime](#lease-lifetime)). The default is not compared with `max_ttl`, so a deployment with a short `max_ttl` and no `lease_timeout` still starts; its leases are bounded by their `expires_at` all the same. |
| `reap_interval` | string | `"10m"` | How often the hub sweeps the leases, as a Go duration; at least `10s` and at most `1h`. Nothing depends on it for correctness: deadlines are enforced on every call, and a sweep only tidies up. An ended lease disappears between 24 hours and 24 hours plus `reap_interval` after it ended. |

#### Gateway listener keys

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `listen` | string | `"127.0.0.1:8443"` | Address of the sandbox gateway, `host:port`. The port is a number from `0` to `65535`; `0` picks a free port. |
| `tls_cert`, `tls_key` | string | `""` | Paths of the gateway's certificate and private key (PEM). Both are set or neither is; the error names the one that is missing. With neither the gateway speaks plaintext. The pair is loaded at start; a renewed certificate is picked up at the next start. |
| `external_url` | string | `""` | The address lease holders dial, when it is not the address the hub listens on, for example behind a proxy. An absolute `http` or `https` URL with a host and nothing else but an optional `/`: no user information, query, fragment or other path. |

**Plaintext only on loopback.** `listen` is on loopback when its host is
`localhost` or a literal loopback IP address; an empty host, `0.0.0.0`, `::` and
any other name are not, and no name is resolved. A `listen` address that is not
on loopback needs TLS (`tls_cert` and `tls_key`) and an `external_url`, because a
lease token sent in plaintext over a network would hand over a sandbox full of
secrets. The error for the first names `sandbox.listen` and says that a
plaintext gateway is allowed only on loopback; the error for the second names
`sandbox.external_url`.

**The address lease holders see.** Every lease shows a `gateway_url`: the
`external_url` when set, otherwise `https://` (with TLS) or `http://` followed by
the address the listener is really on, so `listen = "127.0.0.1:0"` shows the
port it got. The AgentKit client dials plaintext only to `localhost` and loopback
addresses, so an `http` `external_url` for any other host is accepted here but
refused by the client: use `https`.

#### [[sandbox.profiles]]

Repeatable; at least one profile is required. A lease copies the profile it is
issued from, so editing the file and restarting the hub changes only the leases
issued afterwards. The descriptions say what a sandbox of the profile gets.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `name` | string | *(required)* | The name callers choose. Matches `^[a-z0-9][a-z0-9_-]{0,62}$`; unique. |
| `default` | boolean | `false` | Marks the default profile. At most one profile may set it; none is valid. |
| `image` | string | *(required)* | The container image; not empty. The hub does not build, pull or check images. |
| `cpus` | number | `0` | CPU bound, `0` or more; a fraction such as `0.5` is allowed. |
| `memory_mb` | integer | `0` | Memory bound in MiB, `0` or more. |
| `pids` | integer | `0` | Process count bound, `0` or more. |
| `disk_mb` | integer | `0` | Disk bound in MiB, `0` or more. The podman provider cannot bound disk, so there a profile that sets it is refused at start. |
| `network` | string | `"none"` | The sandbox's network mode; one of `network_modes`. `"allowlist"` can be named only when the provider honours it, and its endpoints cannot be configured in this release. |
| `ttl` | string | *(required)* | The sandbox lifetime as a Go duration; greater than zero and at most `max_ttl`. |
| `secrets` | list of strings | `[]` | Names of the hub secrets put into the sandbox's environment. Issuing a lease is refused (`422`, `error_type: secret_missing`) while one of them exists nowhere, and so is creating the sandbox. Each is a valid secret key (letters, digits and `_`, not starting with a digit, at most 255 characters). Reserved names such as `GIT_PAT` are allowed. |
| `optional_secrets` | list of strings | `[]` | Names of secrets put into the environment when they exist. Same rules as `secrets`. |
| `env` | table of strings | `{}` | Fixed environment variables of the sandbox. Keys follow the secret key rules and must not equal, ignoring case, a name in `secrets` or `optional_secrets`. Values are never shown by the API or in logs. |
| `require_landlock` | boolean | `false` | Refuse to create a sandbox whose agent cannot confine itself with Landlock. |

Zero for `cpus`, `memory_mb`, `pids` and `disk_mb` means the provider's default.
Secret names are compared ignoring case, within `secrets`, within
`optional_secrets` and across the two.

#### Start-up checks

The hub checks the section at every start, after the first-boot admin token is
handled (so generating that token never depends on the sandbox configuration)
and before it serves anything. Any failure stops the start.

1. **The keys and profiles**, with all failures reported together, before the
   provider is contacted.
2. **The provider**, once, within 10 seconds (see
   [The podman provider](#the-podman-provider) and
   [The remote gateway provider](#the-remote-gateway-provider)). The error starts
   with `sandbox:` and names the keys involved.
3. **Every `network_modes` entry** must be a mode the provider honours.
4. **Every profile** must be one the provider can run (image, fixed `env`,
   network, resources, `ttl`, `require_landlock`); a refusal names the profile
   and says why.
5. **The gateway listener**: the TLS key pair is loaded (a failure reads
   `sandbox.tls_cert and sandbox.tls_key: cannot load the key pair:` and the
   cause) and `listen` is bound (`sandbox.listen "<address>": cannot bind the
   sandbox gateway:` and the cause).

The hub never creates a sandbox at start. After the checks it ends the leases a
crashed process left `active`, runs one sweep (see
[Lease lifetime](#lease-lifetime)) and only then lets the gateway accept
connections; what fails in this step is logged and does not stop the start. On
success it logs one line with the provider name and the profile names, and
nothing else from the section.

#### The podman provider

With `provider = "podman"` sandboxes run as containers of a container engine on
the hub's own host: Podman, or Docker with `engine_binary = "docker"`.

- **The engine must be there at start.** A missing engine binary, or an option
  the provider refuses (`runtime`, `userns`), stops the start with an error that
  names `sandbox.provider` and `sandbox.engine_binary`.
- **The start-up check cleans up.** The hub checks the engine by removing the
  containers and volumes that AgentKit labelled as sandboxes and whose own expiry
  has passed, whichever process created them. An engine that cannot be listed, and an expired leftover that
  cannot be removed, both stop the start (`failed its start-up check`).
- **Network modes:** `"none"` and `"unrestricted"`.
- **No disk bound:** a profile that sets `disk_mb` is refused.

#### The remote gateway provider

With `provider = "gateway"` sandboxes run on an `agentkit-gateway` elsewhere. The
hub holds one token for that gateway and puts its own
[sandbox gateway](#the-sandbox-gateway) in front of it, so callers keep the same
API, the same leases and the same guarantees and never see the remote's address
or token. Every other rule of the section (profiles, `max_ttl`,
`network_modes`, the scopes, the listing, the disabled state) applies unchanged.

```toml
[sandbox]
provider      = "gateway"
gateway_url   = "https://agentkit-gateway.example:8443"
gateway_token = "${AGENTKIT_GATEWAY_TOKEN}"
```

Keep the token in the environment, as `deploy/configmap.yaml` does for the OAuth
secrets: a token written into the file is readable by everyone who can read the
file.

**At start** the hub connects to the remote, reads its capabilities and lists
its sandboxes once, all within 10 seconds and under the hub's token; nothing is
created or destroyed. An unreachable address, a TLS failure, a remote that
refuses the token, and one that does not answer in time all stop the start with
an error that begins with `sandbox:`, names `sandbox.provider`,
`sandbox.gateway_url` and `sandbox.gateway_token`, and shows the token as
`[redacted]`. The network modes and profiles are checked against the
capabilities the remote reported. Those are read once: a change on the remote
takes effect when the hub is restarted.

What to know when you run it:

- **TLS uses the host's trust store.** For `https` the hub uses the system roots
  and TLS 1.2 or later. There is no setting for a custom CA, client certificates
  or a proxy.
- **The remote's own limits show up at create time.** The hub does not check the
  remote's maximum number of sandboxes or maximum TTL at start. When a create
  exceeds one, the remote refuses it and the lease is `failed`, with the remote's
  message.
- **The token and the address stay in the hub.** They are removed from every
  lease error, audit event, log line and API or gateway error made from the
  remote's errors, and from the capabilities the hub's gateway reports. A lease's
  `gateway_url` is always the hub's own gateway. One thing is out of the hub's
  hands: the error of a call relayed to a sandbox is handed to the holder as it
  is, and a TLS failure on `https` (for example a certificate not valid for the
  host name) names the host.
- **A remote that refuses the hub's token after start** (revoked or rotated) is
  answered to callers as `Unavailable`, not `Unauthenticated`, so nobody takes it
  for a refusal of their own lease token, and the hub logs one error naming
  `sandbox.gateway_token`.
- **The hub never retries a create on the remote**, since a retry after a lost
  answer could make a second sandbox. While the remote cannot be reached, leases
  are still issued and profiles listed, a create fails the lease, a `DELETE` of an
  `active` lease answers `502` and changes nothing, a holder's own destroy fails
  and leaves the lease `active`, and a sweep logs what it could not do. A failed destroy is tried again by the next `DELETE` or the next
  sweep. When the remote is back at the same address the next call works without
  a restart, though reconnecting can take a while: after each failed attempt the
  client waits longer, from about a second up to two minutes, and a call made in
  between fails at once.
- **Every sweep asks the remote** to remove the sandboxes whose own expiry has
  passed. If the remote cannot be reached that step is logged and the rest of the
  sweep still runs.
- **Changing `gateway_token` leaves sandboxes behind.** The remote ties each
  sandbox to the token that made it, so after a restart with another token the
  hub can no longer see or destroy the ones made under the old token. They live
  until the TTL the hub passed.
- **Network modes** are those the remote offers.

#### The sandbox gateway

The sandbox gateway is an AgentKit gateway (gRPC) that the hub serves on the
`listen` address whenever the `[sandbox]` section is present. A hub without the
section binds nothing. The REST API issues and ends leases; the gateway is where
a lease holder turns its lease into a sandbox and uses it.

The gateway accepts a lease token and nothing else; it has no static tokens. A
lease holder connects with the AgentKit client and asks for its sandbox:

```go
p, err := gateway.Dial(ctx, lease.GatewayURL, gateway.Options{Token: token})
sb, err := p.Create(ctx, sandbox.Spec{Image: lease.Image, TTL: time.Until(lease.ExpiresAt)})
```

The spec repeats the lease only so that the client's own checks pass; the hub
takes nothing from it but the working directory and the labels (see [The sandbox gateway](api.md#the-sandbox-gateway) in the API
reference, and the [`sandbox-lease` example](../examples/sandbox-lease/README.md)
for a complete program).

- **One sandbox per lease.** The first create under a token makes the lease
  `active`; a second one is refused. A token stops working on the very next call
  once its lease is `destroyed`, `expired` or `failed`, or past its `expires_at`,
  calls relayed to an existing sandbox included.
- **The lease decides what the sandbox is.** The image, network, resources,
  Landlock setting and fixed `env` are those of the lease, which copied them from
  the profile, and the TTL is the time the lease has left. Of the spec the caller
  sends, the hub reads only its `Workdir` and its labels; labels that start with
  `hub.` are dropped, and `hub.lease` and `hub.workspace` are set on the sandbox.
- **Secrets reach a sandbox only through the hub.** At create time the hub looks
  up each of the profile's `secrets` and `optional_secrets` in the workspace,
  then in the workspace's org (only while the workspace owner is a member of
  it), then in the owner's user scope, the first scope that has the name
  winning. A required secret that exists nowhere fails the lease with
  `secret_missing:`, the name and the scopes searched.
- **Nothing secret comes back.** The spec the gateway reports to the client has
  every environment value replaced by `[redacted]`, and a provider error is
  stored, logged, put in an event and returned only after every secret value,
  every fixed `env` value and the user information of every URL in it is
  removed.
- **The client checks first.** The AgentKit client and the gateway check the
  caller's TTL (one hour when empty) against `max_ttl` and its network mode
  (`none` when empty) against `network_modes` before the hub sees the request,
  although the hub then ignores both. With `max_ttl` under `1h`, or
  `network_modes` without `"none"`, a client must ask for a TTL and a network
  that fit. A client built from a development checkout of AgentKit has no default
  image and refuses an empty `Image`; the hub ignores whatever it is.
- **A `DELETE` on a lease destroys its sandbox.** If the provider cannot, the
  answer is `502` with `error_type: sandbox_provider_error`, the lease stays
  `active` and the call can be repeated (see
  [DELETE /api/v1/workspaces/:slug/sandboxes/:id](api.md#delete-apiv1workspacesslugsandboxesid)).
- **If the gateway stops serving** with an error, the hub logs it and exits with
  status 1, since it would otherwise go on issuing leases nobody can use.
- **Stopping the hub destroys the sandboxes it holds.** On SIGTERM or SIGINT the
  hub destroys the sandbox of every `active` lease and ends each lease with
  `end_reason: "shutdown"`, within 20 seconds, then stops the gateway (and closes
  its connection to a remote gateway). Kubernetes gives a pod 30 seconds after
  SIGTERM by default. Leases not ended when the 20 seconds run out stay `active`,
  and the next start ends them.
- **A crash runs none of this.** The next start ends every lease still `active`
  with `end_reason: "restart"`, but it can no longer reach the sandboxes of the
  process before it. Those live until the TTL the provider enforces, which is
  never later than their lease's `expires_at`, and the provider's own clean-up at
  the start of every sweep removes them after that.
- **Exposing the gateway is up to you.** The manifests in `deploy/` expose the
  REST port only; reaching the gateway from outside the pod (a second Service
  port, gRPC through a route) is a deployment decision.

#### Lease lifetime

Every lease ends: nothing a lease allows outlives its `expires_at`, and a lease
nobody uses lapses.

- **`lease_expires_at`, the deadline for a create.** Fixed when the lease is
  issued: the earlier of `created_at` plus `lease_timeout` and `expires_at`. A
  create for a `leased` lease that arrives at or after it, or with less than a
  second of the lease left, is refused (the client sees `Unavailable` with a
  message naming `expired`) and the lease is ended as `expired`. A lease from
  before this deadline existed has none of its own, and `expires_at` is used.
- **`expires_at`.** From this moment the lease's token is refused in every state.
- **The sweep.** At start and then every `reap_interval`, one at a time, the hub:
  asks the provider to remove its expired sandboxes; ends every `leased` lease
  past its deadline as `expired`; destroys the sandbox of every `active` lease
  past its `expires_at` and ends it as `destroyed`, both with `end_reason:
  "expired"`, whether or not the sandbox could be destroyed (a failure is
  logged); destroys every sandbox this process holds whose lease has ended
  (a sandbox it cannot match to an ended lease is left alone);
  deletes ended leases older than 24 hours; and recalibrates the
  `afhub_sandboxes_active` gauge. A step that fails is logged and the later ones
  still run. Each call to the provider is cut off after 30 seconds. The gateway's
  own periodic clean-up runs the same sweep; sweeps never overlap.
- **Retention.** An ended lease (`destroyed`, `expired` or `failed`) stays
  readable for 24 hours after it ended. The period is fixed.
- **Metrics** on `GET /metrics`, listed from the start:
  `afhub_sandboxes_active{workspace}`, the number of `active` leases;
  `afhub_sandbox_creates_total{result}`, the creates the gateway accepted for a
  `leased` lease, as `created` or `failed`; and `afhub_sandbox_lease_seconds`, a
  histogram of the time from issue to destruction of a lease that became
  `destroyed` (buckets 60, 300, 600, 1200, 1800, 3600, 7200, 14400 and 28800
  seconds).

#### Sandbox example

A Podman deployment whose gateway is reachable from other hosts, with two
profiles:

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

## Forge Access

The forge routes (see [Forge Endpoints](api.md#forge-endpoints)) need no key in
`config.toml`. The hub reaches a workspace's repository on its forge with the
git credential stored for the workspace (`GIT_PAT`, or `GIT_USERNAME` with
`GIT_PASSWORD`; `UPSTREAM_GIT_PAT` and its siblings for the upstream of a
`carry_patch` workspace). Two optional workspace settings give the API URL when
the host does not give it away:

| Workspace setting | Meaning |
|---|---|
| `forge_api_url` | The API URL of the forge behind the workspace's `git_url`: an absolute `https://` URL with a host, without user information, query or fragment |
| `upstream_forge_api_url` | The same for the `upstream_url` of a `carry_patch` workspace; it does not fall back to `forge_api_url` |

Both are listed in the [Workspace Settings Reference](api.md#workspace-settings-reference).
The environment variables the forge client reads are listed under
[Forge client](#forge-client).

**Webhooks.** The forge can tell the hub what happens on the repository by
posting deliveries to `POST /hooks/forge/<slug>`, which the hub always serves
(there is no key for it, and it takes no bearer token). A workspace accepts
deliveries once it has the workspace secret `FORGE_WEBHOOK_SECRET`
(`afc forge webhook set`); without it the endpoint answers `404`. The URL to
enter in the forge is `server.external_url` without a trailing slash, then
`/hooks/forge/<slug>`; a proxy in front of the hub must pass these requests on
unchanged. `server.max_body_size` does not apply: the endpoint accepts at most
1 MiB. The log of deliveries is kept for 7 days (not configurable). See
[The webhook ingress](api.md#the-webhook-ingress).

## Environment Variables

### Paths and deployment

| Variable | Description |
|----------|-------------|
| `XDG_CONFIG_HOME` | Directory of `config.toml` (`$XDG_CONFIG_HOME/config.toml`) and of the first-boot `admin_token` file. |
| `XDG_DATA_HOME` | Base directory of bare database and workspace paths (`$XDG_DATA_HOME/<name>`). |
| `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET` | The GitHub OAuth app's credentials, referenced from `config.toml` as `${GITHUB_CLIENT_ID}` and `${GITHUB_CLIENT_SECRET}`. Any variable can be referenced this way. |
| `AGENTKIT_GATEWAY_TOKEN` | The conventional variable for `sandbox.gateway_token`, referenced as `${AGENTKIT_GATEWAY_TOKEN}`. |
| `ADMIN_EMAIL` | Not read by the hub. The Kubernetes deployment passes it as `--admin-email` (see [Kubernetes Deployment](#kubernetes-deployment)); its value comes from the `af-hub-secrets` Secret. |
| `ADMIN_TOKEN` | Not read by the hub. The Makefile sets it from the saved admin token for operator commands such as `curl`. |

### Audit retention and limits

The hub applies the retention limits once at start and then every hour.

| Variable | Default | Description |
|----------|---------|-------------|
| `AF_AUDIT_DB_PATH` | `<directory of the SQLite database>/audit.duckdb` | Path of the audit database file. |
| `AF_AUDIT_MAX_AGE_DAYS` | `90` | Age in days after which agent and hub audit events are deleted. |
| `AF_AUDIT_MAX_RUNS` | `50` | Number of runs whose agent audit events are kept; the oldest runs (by their first event) are pruned first. |
| `AF_TRACE_MAX_AGE_DAYS` | `30` | Age in days after which agent traces are deleted. |
| `AF_SESSION_MAX_AGE_DAYS` | `90` | Age in days after which completed sessions are deleted. |
| `AF_SESSION_MAX_ACTIVE_AGE_DAYS` | `7` | Age in days after which a session that is still active is closed with status `timeout`. |
| `AF_POSTMORTEM_MAX_AGE_DAYS` | `180` | Age in days after which postmortems are deleted. |
| `AF_AUDIT_ORPHAN_RETENTION_DAYS` | `30` | Days the audit data of a deleted workspace is kept before it is removed. |
| `AF_AUDIT_WRITE_TIMEOUT_MS` | `2000` | Milliseconds an audit write may take before it is given up. |
| `AF_SSE_MAX_CONNECTIONS` | `100` | Most concurrent connections to the event stream (`GET /api/v1/events`). |

Every value but `AF_AUDIT_DB_PATH` must be a positive integer. Anything else,
`0` included, falls back to the default; for the `AF_*_DAYS`, `AF_AUDIT_MAX_RUNS` and
`AF_SSE_MAX_CONNECTIONS` variables the hub logs a warning.

### Git

| Variable | Description |
|----------|-------------|
| `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME`, `GIT_COMMITTER_EMAIL` | Identity of the commits the hub makes (carry-patch rebuilds, merge jobs). Default `af-hub <af-hub@localhost>`. |

The hub needs the `git` CLI, version 2.38 or later, on its `PATH` (the container
image includes it). Its git commands ignore the variables that would point git at
another repository or inject configuration (`GIT_DIR`, `GIT_WORK_TREE`,
`GIT_INDEX_FILE`, `GIT_CONFIG_PARAMETERS` and similar) and the system git
configuration, never prompt for credentials, and are stopped after 10 minutes
when they have no deadline of their own.

### Forge client

| Variable | Description |
|----------|-------------|
| `HTTPS_PROXY`, `NO_PROXY` | Proxy settings, followed by the forge client as by the hub's git transport. |
| `GITHUB_API_URL`, `GITLAB_API_URL` | Read when the forge client decides which forge serves a host. |
| `GITHUB_TOKEN`, `GH_TOKEN`, `GITLAB_TOKEN` | Never sent. The hub uses only the workspace's stored credential; a workspace without one sends no token. |

## First Boot

The server can start without `--admin-email`. Then no admin account is created
and the server serves requests at once:

```
bin/hub
```

To create the admin account, pass `--admin-email` on the first boot:

```
bin/hub --admin-email=admin@example.com
```

The server then generates an admin token, writes it in plaintext to the file
`admin_token` in the config directory, and **exits** without serving. The
operator:

1. saves the token from the `admin_token` file;
2. deletes the `admin_token` file;
3. starts the server again without `--admin-email`.

The server refuses to start while the `admin_token` file exists.

`--admin-email` matters only on the first boot: once an admin token exists, the
flag is ignored and the server starts normally. A start-up script may therefore
pass it on every start, as the [Kubernetes manifest](#kubernetes-deployment)
does.

To rotate the admin token, pass `--reset-admin-token`; the server generates a
new token and exits in the same way:

```
bin/hub --reset-admin-token
```

## CLI Configuration (~/.af/config.toml)

`afc login` stores the client's settings in `config.toml` in the client's
config directory: `~/.af`, or the directory `AF_CONFIG_DIR` names. The file has
mode `0600`, the directory `0700`:

| Key | Description |
|-----|-------------|
| `endpoint_url` | The hub's URL, for example `"https://hub.example.com"`. |
| `user_id` | The logged-in user's id. |
| `api_key` | The API key the login returned. |

`$VAR` and `${VAR}` in the file are replaced by environment variables when it is
read. Each setting can be overridden; the first of these that is set wins:

| Setting | Flag | Environment | File key |
|---------|------|-------------|----------|
| Hub URL | `--endpoint-url` | `AF_ENDPOINT_URL`, then `ENDPOINT_URL` | `endpoint_url` |
| API key | `--api-key` | `AF_API_KEY`, then `API_KEY` | `api_key` |
| User id | `--user-id` | `AF_USER_ID`, then `USER_ID` | `user_id` |

A command without a hub URL or an API key fails.

The git credential helper (`afc credential-helper`) reads only `ENDPOINT_URL`
and `API_KEY` from the environment, and otherwise `~/.af/config.toml`; it
ignores the `AF_` variables and `AF_CONFIG_DIR`. It answers git only for the
hub's own scheme and host:port; see
[Presenting a credential](permissions.md#presenting-a-credential).

## Container Configuration

The image sets:

- `XDG_CONFIG_HOME=/config/af-hub`
- `XDG_DATA_HOME=/data/af-hub`

A default config is installed at `/config/af-hub/config.toml`, so the server
finds it without overrides. It has only the `[server]`, `[database]` and
`[logging]` sections; the other sections take their defaults. Mount your own
`config.toml` at `/config/af-hub/config.toml` to replace it.

With the default config (`path = "afhub.db"`) the SQLite database is
`/data/af-hub/afhub.db`, the audit database `/data/af-hub/audit.duckdb`, and the
workspace clones are under `/data/af-hub/workspaces/`. If no config file is found
at all, the database is `/data/af-hub/apikit.db`.

The image includes the `git` CLI and `afc` (`/usr/bin/afc`). No git identity
needs to be configured: commits are made as `af-hub <af-hub@localhost>` unless the
`GIT_AUTHOR_*` and `GIT_COMMITTER_*` variables are set.

The image's command is `/usr/local/bin/run`, a shell script that runs
`/usr/bin/hub` without flags.

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

The sandbox gateway's port (`sandbox.listen`, `8443` by default) is not exposed
by the image.

## Kubernetes Deployment

The `deploy/` directory holds reference manifests. The deployment sets the same
`XDG_CONFIG_HOME` and `XDG_DATA_HOME` as the image, mounts the ConfigMap's
`config.toml` at `/config/af-hub/config.toml` and PVCs at `/config/af-hub` and
`/data/af-hub`, so the database is `/data/af-hub/afhub.db`.

| File | Description |
|------|-------------|
| `deploy/configmap.yaml` | `config.toml` with `[server]`, `[database]`, `[logging]` and a GitHub `[[oauth.providers]]` table; `[workspace]` is left out and takes its defaults (`$XDG_DATA_HOME/workspaces`, 4 workers). |
| `deploy/deployment.yaml` | The Deployment: volume mounts, health probes, resource limits, and the OAuth and admin variables from the Secret. |
| `deploy/pvc.yaml` | PersistentVolumeClaims for config (1Gi) and data (10Gi). |
| `deploy/service.yaml` | ClusterIP Service on port 8080. |
| `deploy/route.yaml` | OpenShift Route with TLS edge termination. |
| `deploy/secrets.yaml.example` | Template of the `af-hub-secrets` Secret: `admin-email`, `github-client-id`, `github-client-secret`. |

The deployment's start command handles the first boot: when
`/config/af-hub/admin_token` exists it prints the token to the container log and
renames the file to `token` on the config volume; otherwise it runs
`/usr/bin/hub --admin-email="$ADMIN_EMAIL"`.

### Health Endpoints

| Path | Purpose |
|------|---------|
| `/healthz` | Liveness probe; always answers ok. |
| `/readyz` | Readiness probe; pings the database. |
| `/metrics` | Prometheus metrics (see [GET /metrics](api.md#get-metrics)). |

All three are outside the mount point.
