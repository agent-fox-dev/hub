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
| `external_url` | string | `""` (unset) | External URL used for OAuth redirect validation, for constructing git clone URLs (`hub_url` in workspace responses) and for the webhook URL of a workspace (`<external_url>/hooks/forge/<slug>`, shown by `afc forge webhook set` and `GET .../forge`). When empty, `hub_url` in workspace responses and the webhook `url` are null; a webhook secret can be set all the same. |
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
| `provider` | string | *(required)* | Where sandboxes run: `"podman"`, a container engine on the hub's own host (Podman or Docker), or `"gateway"`, a remote `agentkit-gateway` (see [The remote gateway provider](#the-remote-gateway-provider)). Any other value stops the start; the error names `sandbox.provider` and lists both. |
| `engine_binary` | string | `"podman"` | The container engine command, `"podman"` or `"docker"`, or a path to one. A bare name is looked up in the `PATH` of the hub process. Only with provider `"podman"`: with `"gateway"` a non-empty value stops the start. |
| `runtime` | string | `""` | The engine's OCI runtime: empty for its default, or `"krun"` for a micro-VM (podman only). Passed to the provider unchanged; the provider refuses what it does not support. Only with provider `"podman"`: with `"gateway"` a non-empty value stops the start. |
| `userns` | string | `""` | Passed to the engine as `--userns=<value>` when set. Only with provider `"podman"`: with `"gateway"` a non-empty value stops the start. |
| `gateway_url` | string | *(required with `"gateway"`)* | The address of the remote gateway: `https://host[:port]`, or `http://host[:port]` when the host is `localhost` or a loopback IP address. A host, a port from `1` to `65535` if there is one, no user information, query or fragment, and a path that is empty or `/`. Only with provider `"gateway"`: with `"podman"` a non-empty value stops the start. See [The remote gateway provider](#the-remote-gateway-provider). |
| `gateway_token` | string | *(required with `"gateway"`)* | The bearer token the hub presents to the remote gateway: at least 16 characters, all printable ASCII without spaces (`0x21` to `0x7E`). Write it as `"${AGENTKIT_GATEWAY_TOKEN}"`. Only with provider `"gateway"`: with `"podman"` a non-empty value stops the start. No error shows the token or any part of it. |
| `max_ttl` | string | `"4h"` | The longest lifetime a profile may ask for, as a Go duration (`"90m"`, `"4h"`). Must be greater than zero. |
| `network_modes` | list of strings | `["none"]` | The network modes profiles may use. Entries must be non-empty and unique, and each must be a mode the provider honours: `"none"` and `"unrestricted"` for Podman and Docker, and for a remote gateway the modes it offers. A sandbox has no network unless the operator allows one. An empty list is accepted but leaves no mode for any profile, so every profile is then refused. |
| `max_sandboxes` | integer | `16` | The most sandbox leases that may be live at once, across all workspaces. A lease counts while it is `leased` or `active` and its `expires_at` is in the future; a `leased` lease also stops counting when its `lease_expires_at` (see `lease_timeout`) is at or before now. At the limit `POST /api/v1/workspaces/:slug/sandboxes` answers `409` with `error_type: sandbox_capacity`. Must be a whole number of at least `1`. The gateway holds at most this many sandboxes too. |
| `lease_timeout` | string | `"5m"` | How long a lease may wait for its `Create`, as a Go duration. Every lease gets a `lease_expires_at`, the earlier of its `created_at` plus this and its `expires_at`; a lease that is still `leased` then lapses (see [Lease lifetime](#lease-lifetime)). At least `1s` and at most `max_ttl`. The default is not compared with `max_ttl`, so a deployment with a short `max_ttl` and no `lease_timeout` still starts; its leases are bounded by their `expires_at` all the same. |
| `reap_interval` | string | `"10m"` | How often the reaper sweeps the leases, as a Go duration. At least `10s` and at most `1h`. Nothing depends on it for correctness: the gateway checks the deadlines itself, and a sweep only tidies up. A lease that ended stays readable for 24 hours and is removed by the first sweep after that, so it disappears between 24 hours and 24 hours plus `reap_interval` after it ended. |
| `listen` | string | `"127.0.0.1:8443"` | The address of the sandbox gateway, `host:port`. The port is a number from `0` to `65535`, and `0` picks a free port. The address is on loopback when the host is `localhost` or a literal IP address that is a loopback address; an empty host, `0.0.0.0`, `::` and any other name are not. No name is resolved. |
| `tls_cert`, `tls_key` | string | `""` | Paths of the gateway's certificate and private key (PEM). Both are set or neither is; the message names the one that is missing. With neither the gateway speaks plaintext. |
| `external_url` | string | `""` | The address lease holders dial, when it is not the address hub listens on, for example behind a proxy. An absolute `http` or `https` URL with a host and nothing else but an optional `/`: no user information, query, fragment or other path. The AgentKit client dials plaintext only for `localhost` and loopback addresses, so an `http` URL for any other host is accepted here and refused by the client: use `https`. |

A `listen` address that is not on loopback needs TLS (`tls_cert` and `tls_key`)
and an `external_url`: a lease token sent in plaintext over a network would hand
over a sandbox full of secrets. The start-up error for the first names
`sandbox.listen` and says that a plaintext gateway is allowed only on loopback;
the second names `sandbox.external_url`. These failures are reported together
with every other failure of the section.

#### The remote gateway provider

With `provider = "gateway"` the sandboxes run on an `agentkit-gateway` elsewhere,
not on a container engine on the hub's host. The hub holds one token for that
gateway, checks at start that it answers and accepts the token, and then puts its
own gateway (see [The sandbox gateway](#the-sandbox-gateway)) in front of it, so a
caller keeps the same API, the same leases and the same guarantees, and the hub
works to keep the remote gateway, its address and its token from the caller (see
[below](#the-remote-gateway-provider) for the one place it cannot). Every other rule of the section
(profiles, `max_ttl`, `network_modes`, the scopes, the listing, the disabled state)
applies to both providers unchanged.

```toml
[sandbox]
provider = "gateway"
gateway_url = "https://agentkit-gateway.example:8443"
gateway_token = "${AGENTKIT_GATEWAY_TOKEN}"
```

Write the token as a `${NAME}` reference, as `deploy/configmap.yaml` does for the
OAuth secrets, and keep its value in the environment: a token written into the
file is readable by everyone who can read the file. Both keys are validated with
the other static checks and reported in the same joined error, before anything is
dialled:

- **`gateway_url` is required.** A value that is empty after `${NAME}` expansion
  counts as missing. It must be an absolute URL whose scheme is `https`, or `http`
  only when the host is `localhost` or a literal loopback IP address (no name is
  resolved), with a host, a port from `1` to `65535` if it has one, no user
  information, query or fragment, and a path that is empty or `/`. A failure names
  `sandbox.gateway_url` and the rule; it never repeats user information, and the
  plaintext rule shows the scheme and the host alone.
- **`gateway_token` is required.** It must be at least 16 characters, all in
  `0x21` to `0x7E`: a space, a tab, a trailing newline from a secret file and any
  non-ASCII byte are refused. A failure names `sandbox.gateway_token` and the rule
  and never repeats the value or any part of it.
- **The keys of the other provider are refused.** Under `"gateway"`,
  `engine_binary`, `runtime` and `userns`; under `"podman"`, `gateway_url` and
  `gateway_token`. There is one failure for each, naming the key and the provider it
  applies to (`sandbox.runtime applies only to sandbox.provider "podman"`). A key
  that would do nothing is not ignored, since an operator who sets
  `runtime = "krun"` would believe in an isolation a remote gateway does not give.

At start, after the static checks, the hub builds no Podman provider and runs no
engine binary. It dials the remote with `gateway.Dial`, which connects and reads
the remote's capabilities, and then makes one `List` call, all within 10 seconds
and under the hub's token. Nothing is created or destroyed on the remote. Any
failure stops the start with an error that begins with `sandbox:`, names
`sandbox.provider`, `sandbox.gateway_url` and `sandbox.gateway_token`, and carries
the client's own text with the token replaced by `[redacted]` and URL user
information removed: an unreachable address, a TLS failure, a remote that refuses
the token, and a remote that does not answer within the 10 seconds all end this
way. The remote's capabilities are read once and kept for the life of the process;
every `network_modes` entry and every profile is then checked against them with the
messages of the Podman provider, and a change on the remote takes effect when the
hub is restarted. The start-up info line is the one of every provider, with the
provider name and the profile names.

What to know when you run it:

- **TLS uses the host's trust store.** For `https` the hub dials with the client's
  defaults: the system roots and TLS 1.2 or later. There is no setting for a
  custom CA, client certificates or a proxy.
- **The remote's limits show up at `Create`.** The hub does not check the remote's
  `MaxSandboxes` or `MaxTTL` at start. When a `Create` exceeds one, the remote
  refuses it and the lease is `failed`, with the remote's message, scrubbed.
- **The token and the address stay in the hub.** The token, the `gateway_url`
  string, its host with the port and its host name alone are removed from every
  text produced from the remote's errors: the lease's `error`, the audit events,
  the log, and the errors returned by the gateway and the API. The same goes for
  the capabilities the hub's gateway reports, whose notes carry the remote's host
  and limits. The token is in no response, metric or log line. A lease's
  `gateway_url` is always the hub's own gateway, never the remote's. The hub dials
  the remote itself, so that the error of a call that fails because the remote
  cannot be reached (including a call relayed to a sandbox) says why
  (`connect: connection refused`, `no such host`) and not where. One thing is out
  of the hub's hands: gatewayd hands the error of a relayed call to the holder as it
  is, and a TLS failure on `https` (for example a certificate that is not valid for
  the host name) names the host.
- **A remote that refuses the hub's token after start** (it was revoked or
  rotated) is answered to the caller as `Unavailable` and not as the remote's
  `Unauthenticated`, so that nobody takes it for the hub refusing the caller's own
  lease token. The lease outcome is the usual one (a `Create` fails the lease, a
  destroy leaves it `active` and `DELETE` answers `502`), and the hub logs one
  error that names `sandbox.gateway_token`.
- **The hub never retries a `Create` on the remote** and does not redial or monitor
  it. A retry after a lost answer could make a second sandbox; a sandbox orphaned
  that way is bounded by the TTL the hub passes. While the remote cannot be
  reached, leases are issued and profiles listed, a `Create` fails the lease, a
  `DELETE` of an `active` lease answers `502` and changes nothing, a caller's
  `Close` fails and leaves the lease `active`, and a sweep logs what it could not
  do. A destroy that failed is tried again by id through the same client, by the
  next `DELETE` or the next sweep. When the remote is back at the same address the
  next call works without a restart, though the client may need time to reconnect:
  after each failed attempt gRPC waits longer before the next, from about a second up
  to two minutes, and a call made in between fails at once.
- **The sweep's `Reap` is a call to the remote.** The first step of every sweep
  (see [Lease lifetime](#lease-lifetime)) is the provider's `Reap`; for a remote
  gateway that is a `Reap` call on it, which has its backend remove the sandboxes
  whose own expiry has passed. If the remote cannot be reached the step fails, is
  logged, and the later steps still run.
- **Changing `gateway_token` leaves sandboxes behind.** The remote ties each
  sandbox to the token that made it, so after a restart with another token the hub
  can no longer see or destroy those made under the old one. They live until their
  TTL on the remote, which the TTL the hub passed bounds.
- **Stopping the hub closes the client last.** After the hub has destroyed the
  sandbox of every `active` lease through the client (see
  [The sandbox gateway](#the-sandbox-gateway)), it closes the client.

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
`engine_binary`, `runtime` and `userns` and checks it once. The steps below are
those of provider `"podman"`; provider `"gateway"` is built and probed as described
under [The remote gateway provider](#the-remote-gateway-provider), and then steps 3
and 4 apply to it unchanged:

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

After the checks above, the hub builds the gateway, before its HTTP server
serves anything and after the lease table exists. A hub without a `[sandbox]`
section binds nothing and does none of the work below.

1. If `tls_cert` and `tls_key` are set, it loads the key pair once. A failure
   stops the start with `sandbox.tls_cert and sandbox.tls_key: cannot load the
   key pair:` and the loader's error, before anything is bound. A certificate
   that is renewed is picked up at the next start.
2. It binds `listen`. A failure stops the start with `sandbox.listen "<address>":
   cannot bind the sandbox gateway:` and the bind error.
3. It ends the leases a crashed process left `active`, runs one sweep and
   starts the reaper (see [Lease lifetime](#lease-lifetime)). A failure in any of
   it is logged and never stops the start.
4. It serves an AgentKit gateway on the listener for as long as the process
   lives, over TLS when a key pair is set. The listener is bound at step 2 but
   serves only now, so no connection is accepted before step 3 has finished. If
   the gateway stops serving with an error, the hub logs it once and exits with
   status 1, since it would otherwise go on issuing leases nobody can use.

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
  is `destroyed`, `expired` or `failed`, or past its `expires_at`, on the very next call,
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
- **Stopping the hub destroys the sandboxes it holds.** When the HTTP server
  returns, which apikit does on SIGTERM or SIGINT, the hub destroys the sandbox of
  every `active` lease and ends each lease with `end_reason: "shutdown"`, within
  20 seconds and before the databases close, and then stops the gateway and, with a
  remote gateway, closes its client. Kubernetes
  gives a pod 30 seconds after SIGTERM by default. If the limit runs out the leases
  not yet ended stay `active`, and the next start ends them. A crash runs none of
  this: the next start ends every lease still `active` with `end_reason:
  "restart"`, but it no longer holds the sandboxes of the process before it, and
  AgentKit's provider has no way to list or destroy a sandbox by id. Those
  sandboxes live until the TTL the provider enforces, which is never later than
  their lease's `expires_at`, and the provider's own reap (the first step of every
  sweep) removes them after that.
- **A `DELETE` on a lease destroys its sandbox.** If the provider cannot, the
  answer is `502` with `error_type: sandbox_provider_error`, the lease stays
  `active` and the call can be repeated (see
  [DELETE /api/v1/workspaces/:slug/sandboxes/:id](api.md#delete-apiv1workspacesslugsandboxesid)).
- **The deployment is unchanged.** The manifests in `deploy/` expose the REST
  port only; reaching the gateway from outside the pod (a second Service port,
  gRPC through a route) is a deployment decision.

#### Lease lifetime

Every lease ends: nothing a lease allows outlives its `expires_at`, and a lease
nobody uses lapses.

- **The deadline for a `Create`.** A lease has a `lease_expires_at`, fixed when it
  is issued: the earlier of `created_at` plus `lease_timeout` and `expires_at`. A
  `Create` that arrives at or after it, or with less than a second of the lease
  left, for a lease still `leased`, is refused without a call to the provider (the
  backend answers `FailedPrecondition` naming `expired`, which gatewayd hands to
  the client as `Unavailable` with the same message), and the lease is ended as
  `expired` in the same call. A row from before the column has no
  deadline of its own, and `expires_at` is used.
- **`expires_at`.** From this moment the lease's token is refused in every state.
- **The sweep.** At start, then every `reap_interval`, the hub runs a sweep, one at
  a time, in this order: the provider's `Reap`; every `leased` lease past its
  deadline is ended as `expired`; every `active` lease past its `expires_at` has
  its sandbox destroyed and is ended as `destroyed`, both with `end_reason:
  "expired"`, whether or not the sandbox could be destroyed (the failure is
  logged); every sandbox the hub holds whose lease has ended is destroyed (see
  below); terminal leases that ended more than 24 hours ago are deleted; and the
  `afhub_sandboxes_active` gauge is recalibrated. A step that fails is logged and
  the later ones still run. Each call to the provider is cut off after 30 seconds.
  The gateway's own periodic reap, which it makes when it starts serving and every
  `reap_interval`, runs the same sweep; sweeps never overlap.
- **Sandboxes of ended leases.** The provider cannot list its sandboxes, so the sweep
  looks at the sandboxes this hub process holds a handle of: those it made and has
  not seen closed, among them one that could not be destroyed when its lease ended.
  A held sandbox whose id is the `sandbox_id` of an ended lease, or whose
  `hub.lease` label names one, is destroyed. A sandbox that cannot be matched to an
  ended lease is never touched.
- **Retention.** An ended lease (`destroyed`, `expired` or `failed`) stays readable
  for 24 hours after `ended_at` (`updated_at` when there is none). The period is
  fixed.
- **Metrics.** `afhub_sandboxes_active{workspace}` is the number of `active`
  leases, `afhub_sandbox_creates_total{result}` counts the `Create`s the gateway
  accepted for a `leased` lease as `created` or `failed`, and
  `afhub_sandbox_lease_seconds` is a histogram of the time from issue to
  destruction of a lease that became `destroyed` (buckets 60, 300, 600, 1200, 1800,
  3600, 7200, 14400 and 28800 seconds). They are listed on `GET /metrics` from the
  start.

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

## Forge Access

The forge routes (see [Forge Endpoints](api.md#forge-endpoints)) need no key in
`config.toml`. The hub reads a workspace's repository on its forge with the git
credential stored for the workspace (`GIT_PAT`, or `GIT_USERNAME` with `GIT_PASSWORD`;
`UPSTREAM_GIT_PAT` and its siblings for the upstream of a `carry_patch` workspace),
and two optional workspace settings set the API URL when the host does not give it
away:

| Workspace setting | Meaning |
|---|---|
| `forge_api_url` | The API URL of the forge behind the workspace's `git_url`: an absolute `https://` URL with a host, without userinfo, query or fragment |
| `upstream_forge_api_url` | The same for the `upstream_url` of a `carry_patch` workspace; it does not fall back to `forge_api_url` |

Both are listed in the [Workspace Settings Reference](api.md#workspace-settings-reference).

What the hub's environment changes:

- The HTTP client follows the proxy settings of the environment (`HTTPS_PROXY`,
  `NO_PROXY`), as the hub's git transport does.
- `GITHUB_API_URL` and `GITLAB_API_URL` are read by the forge client when it decides
  which forge serves a host. `GITHUB_TOKEN`, `GH_TOKEN` and `GITLAB_TOKEN` are never
  sent: a workspace without a credential sends no token.

The forge can also tell the hub what happens on the repository: it posts webhook deliveries
to `POST /hooks/forge/<slug>`, which the hub always mounts (there is no `config.toml` key for
it, and no bearer token is needed). A workspace accepts deliveries once it has the workspace
secret `FORGE_WEBHOOK_SECRET` (`afc forge webhook set`); without it the endpoint answers `404`.
The URL to enter in the forge is `server.external_url` without a trailing slash, then
`/hooks/forge/<slug>`; a proxy in front of the hub must pass these requests on as they are.
`server.max_body_size` does not apply to the endpoint, which accepts at most 1 MiB. The hub
keeps the log of the deliveries for 7 days (not configurable) and prunes it every hour. See
[The webhook ingress](api.md#the-webhook-ingress).

Building the hub needs the sibling checkout of
[aflib](https://github.com/agent-fox-dev/aflib) at `../aflib`, which `go.mod`
replaces (see the README's prerequisites); the image build passes it as the `aflib`
build context.

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
