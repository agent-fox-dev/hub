# Create and lease sandboxes

Status: **proposed** (2026-10-19). Nothing here is implemented. This is
item 1 of what the agent-fox service PRD asks of hub
([agent-fox `docs/prd/01`](https://github.com/agent-fox-dev/agent-fox/blob/main/docs/prd/01-triage-and-fix-forge-issues-as-a-service.md),
§9), written to its decisions D4 (hub owns the sandbox and injects the
secrets), D8 (every brain runs inside the box) and D10 (one provider per
hub deployment). Details left to settle at spec time are marked *spec
time*. Next step: this document becomes the `prd.md` of a spec package and
the two AgentKit changes in §8 are filed there.

## Intent

Hub holds everything an agent run needs except the place to run it: the
workspace and its clone, the credentials for the forge and the git remotes,
the secrets a team stores at user, org and workspace scope, the sessions
that account for cost, the audit stream. The agent-fox service, which runs
the triage and fix agents, is built so that it holds none of those: it has
a hub credential and asks hub for the rest. The one thing it cannot ask
for today is a sandbox.

This PRD gives hub **sandboxes**: isolated, ephemeral execution
environments created from AgentKit's `sandbox` module on the one provider
a deployment is configured with, shaped by a **profile** hub holds, with
the secrets the profile names resolved by hub and placed in the box's
environment, and handed to a caller as a **lease**: a URL, a token good for
that one sandbox, and an expiry. The caller attaches with AgentKit's
gateway client and uses the box exactly as it would use one it created
itself; it never sees the provider, the provider's credentials, or the
secret values that went in.

The design rule: **a secret enters a sandbox through hub and never through
a caller.** A caller says which profile; hub says what is in it.

## Goals

1. A caller with a hub credential and the right scope can create a
   sandbox for a workspace from a named profile, in one request, and
   receives a lease it can attach to with `gateway.Dial` from
   `agentkit-go/sandbox/gateway`, with no other configuration.
2. The box's environment holds the profile's secrets, resolved workspace
   over org over the workspace owner's user scope, and nothing the caller
   supplied beyond what the profile allows. No API response, log line or
   audit event carries a secret value.
3. One provider per deployment, in hub's configuration: Podman or Docker
   on hub's host, or an `agentkit-gateway` elsewhere by URL and token. The
   same API serves both, and a profile the provider cannot honour is
   refused at create, naming the field.
4. A sandbox is bounded in time by hub and by the provider: the lease's
   expiry is the box's TTL, hub destroys what expires, and a crashed
   caller leaves nothing behind that a reap does not find.
5. A person sees and manages what is running: `afc sandbox` lists a
   workspace's sandboxes with their profile, owner, age and expiry, and
   can destroy one.
6. Everything is testable offline: the provider is AgentKit's `local` in
   tests, the gateway is `gatewayd.Server` in process, and the API is
   driven with `httptest` as every other hub handler is.

## Non-goals

- **Running anything inside the sandbox.** Hub creates it and hands it
  over. What runs is the caller's business; hub never execs into a box it
  leased.
- **A sandbox API for people's interactive use.** `afc sandbox create` is
  for operators and tests; the shell into a box is the provider CLI's.
- **More than one provider at a time**, or per-workspace providers. A
  deployment chooses once (agent-fox PRD D10).
- **Images.** Hub names an image in a profile and does not build, pull or
  verify it beyond what the provider does.
- **Persistent or shared sandboxes.** One box, one lease, one caller, then
  destroyed. A box outliving its lease is a bug.
- **Changing AgentKit's sandbox protocol.** The two additions §8 asks of
  AgentKit are to the gateway server's configuration, not to any wire.

## Background: what exists

| Need | Exists | Where |
|---|---|---|
| The sandbox contract | `sandbox.Spec`, `Provider`, `Sandbox`, `CheckSpec`; `Spec.Env` is the box's whole environment | agentkit-go `sandbox` |
| Providers | `podman.New` (Podman or Docker), `gateway.Dial` (a remote gateway), `local.Unisolated` (tests) | agentkit-go `sandbox/podman`, `sandbox/gateway`, `sandbox/local` |
| A gateway server as a library | `gatewayd.New(Config{Backend, Tokens, TLS, NetworkModes, MaxSandboxes, MaxTTL})`, `Serve(net.Listener)`, `Shutdown`; a sandbox belongs to the token that created it | agentkit-go `sandbox/gatewayd` |
| The client every caller has | `gateway.Dial(url, Options{Token})` returns a `sandbox.Provider`; `Create`, `List`, `Destroy` | agentkit-go `sandbox/gateway` |
| Secrets with scopes and a reserved-name convention | `secrets.Store`: `GetSecretValue(ownerType, ownerID, key)`, `ResolveVariables`; values never returned by the API | hub `internal/secrets` |
| Workspace ownership and scopes | `wsaccess.AuthorizeWorkspace`; `apikit.Permission` registration through `MountHandlers`; PATs checked with `hasScope` | hub `internal/workspace`, `internal/wsaccess` |
| Configuration | `apikit.LoadConfig` reads `config.toml`; hub's own sections are decoded from the same file (`BurntSushi/toml` is a direct dependency) | hub `cmd/af-hub`, apikit |
| Audit events and SSE | `audit.Emitter`, `HubEvent{EventType, Workspace, Metadata}`, `GET /api/v1/events` | hub `internal/audit` |
| A durable queue for the reaper | `jobqueue` (to become `aflib/jobqueue`, hub PRD 02's sibling) | hub `internal/jobqueue` |

What does not exist: any notion of a sandbox in hub, a profile, a lease, a
token hub mints for a gateway, a gRPC listener in hub, and the two
AgentKit seams §8 names.

## 1. Vocabulary

| Term | Meaning |
|---|---|
| **Provider** | The one backend hub creates sandboxes on: `podman` on hub's host, or `gateway` to a remote `agentkit-gateway`. Configured once. |
| **Profile** | A named sandbox shape in hub's configuration: image, resources, network, TTL ceiling, the secret names to inject, fixed environment. |
| **Lease** | Hub's record of one sandbox a caller may create and use: the workspace, the profile, the owner, a token, an expiry, and the sandbox id once created. |
| **Lease token** | A random bearer token, minted per lease, that hub's gateway accepts for exactly that lease's sandbox. |
| **Hub's gateway** | The `gatewayd.Server` hub embeds and serves on its own gRPC listener, whose backend is the provider, whose tokens are the lease tokens. |
| **Caller** | Whoever holds the lease: the agent-fox service's worker, an operator with `afc`, a test. |

## 2. The shape

```
 caller (agent-fox worker, afc, a test)
   │  POST /api/v1/workspaces/:slug/sandboxes {profile}        hub credential, sandboxes:write
   ▼
 hub REST ──── the lease: {id, gateway_url, token, expires_at, profile, image, network}
   │
   │  gateway.Dial(gateway_url, token) ; Create(spec) ; Sync ; Tools ; Export ; Close
   ▼
 hub's gateway (gatewayd.Server on [sandbox].listen)
   │  authenticates the lease token; on Create: looks the lease up, builds the spec
   │  hub decided (profile + resolved secrets), creates one box on the provider,
   │  binds it to the lease; relays the Agent protocol afterwards
   ▼
 provider (podman on this host · gateway.Dial to a remote agentkit-gateway)
   ▼
 box: image from the profile · Env = profile.env + resolved secrets · network, resources, TTL from the profile
```

What the picture decides:

- **The lease is a one-shot right to create one sandbox with hub's spec.**
  The caller's `Create` carries a spec, as the client API requires, but
  hub's gateway takes from it only `Labels` and `Workdir`; image,
  environment, network, resources and TTL are the lease's, computed when
  the lease was issued. A second `Create` under the same token is refused.
  This is what keeps the secrets out of every response: the only place
  they are assembled is inside hub, on the way to the provider.
- **The token is the principal.** Hub's gateway authenticates each RPC by
  the lease token and binds the created sandbox to it; the relayed Agent
  RPCs name the sandbox by id and are allowed only for the token that
  created it, as `gatewayd` already enforces. A lease token sees one
  sandbox and nothing else on the gateway.
- **Hub is a gateway over a gateway** when the provider is remote: hub's
  backend is then `gateway.Dial` to the deployment's `agentkit-gateway`
  with the one token hub holds, and every lease shares it. The caller
  never sees that token. On a single host the backend is `podman.New`.

## 3. Configuration

In `config.toml`, decoded by hub from the same file apikit loads:

```toml
[sandbox]
provider      = "podman"                 # podman | gateway; required to enable the feature
listen        = "0.0.0.0:8443"           # hub's gateway listener (gRPC); loopback by default
external_url  = "https://hub.example.com:8443"   # what a lease's gateway_url says; required when listen is not loopback
tls_cert      = "/etc/hub/gateway.pem"   # both or neither; plaintext only on loopback
tls_key       = "/etc/hub/gateway-key.pem"
max_sandboxes = 16
max_ttl       = "4h"
lease_timeout = "5m"                     # a lease with no Create by then expires
network_modes = ["none", "unrestricted"] # the ceiling; a profile may not exceed it
reap_interval = "10m"

# provider = "podman"
engine_binary = "podman"                 # podman.Options.Binary; "docker" works too
runtime       = ""                       # "" or "krun"
userns        = ""

# provider = "gateway"
# gateway_url   = "https://gw.example.com:8443"
# gateway_token = "${AGENTKIT_GATEWAY_TOKEN}"

[[sandbox.profiles]]
name      = "tools"                      # the default when a request names none
default   = true
image     = "quay.io/agentfox/tools:latest"
cpus      = 2
memory_mb = 4096
pids      = 512
disk_mb   = 0                            # 0: the provider's default
network   = "unrestricted"
ttl       = "1h"                         # the lease's expiry and the box's TTL; at most max_ttl
secrets   = ["ANTHROPIC_API_KEY"]        # resolved workspace > org > owner user; refused if missing
optional_secrets = ["GITHUB_TOKEN"]      # injected when present, skipped when not
env       = { GOFLAGS = "-mod=mod", HOME = "/opt/app-root/src" }
require_landlock = false

[[sandbox.profiles]]
name    = "triage"
image   = "quay.io/agentfox/tools:latest"
cpus    = 1
memory_mb = 2048
network = "unrestricted"
ttl     = "20m"
secrets = ["ANTHROPIC_API_KEY"]
```

Rules:

- `[sandbox]` absent: the feature is off, the routes answer 404 and
  `afc sandbox` says so. `provider` set: hub validates the section at
  start, dials or probes the provider once, and refuses to start on a
  provider it cannot reach, a profile outside the provider's
  capabilities (`sandbox.CheckSpec` against `Capabilities()`), a
  `network` outside `network_modes`, a `ttl` over `max_ttl`, two
  profiles named alike, or two marked `default`.
- `${VAR}` expansion applies to every string, as it does for the OAuth
  section.
- Secret names are validated against hub's secret key rules at start; a
  profile may name a reserved git secret (`GIT_PAT`) and gets it like any
  other, which is how a box that needs to push gets a credential a team
  chose to give it. Nothing is injected that the profile did not name.
- *Spec time:* whether the section lives in apikit's `Config` (as
  `[workspace]` does) or in a hub-owned struct decoded beside it. The
  recommendation is hub-owned: the sandbox is hub's domain and apikit
  should stop growing hub sections.

## 4. The API

Under `/api/v1`, hub authentication, in `openapi.yaml`. Two new scopes:
`sandboxes:read` and `sandboxes:write`; API keys and admin tokens have
both. Ownership as everywhere: a workspace the caller does not own
answers 404.

```
POST   /workspaces/:slug/sandboxes            {profile?, purpose?, ttl?, labels?}   → 201 the lease
GET    /workspaces/:slug/sandboxes            the workspace's leases, live and recent
GET    /workspaces/:slug/sandboxes/:id        one lease (never the token again)
DELETE /workspaces/:slug/sandboxes/:id        destroy the box, revoke the token  → 204
GET    /sandbox-profiles                      the profiles: name, image, resources, network, ttl, secret names; never values
GET    /sandboxes                             admin token only: every lease on the deployment
```

The lease, as `POST` returns it (the token appears here and nowhere
else):

```jsonc
{
  "id": "sbx_7f3a…",
  "workspace": "widgets",
  "profile": "tools",
  "purpose": "fix",                       // free text the caller set; shown in lists
  "state": "leased",                      // leased | active | destroyed | expired | failed
  "gateway_url": "https://hub.example.com:8443",
  "token": "sbl_…",                       // 48 printable ASCII characters; shown once
  "expires_at": "2026-10-19T16:12:05Z",   // created_at + ttl
  "lease_expires_at": "2026-10-19T15:17:05Z",   // created_at + lease_timeout, until Create
  "image": "quay.io/agentfox/tools:latest",
  "network": "unrestricted",
  "resources": { "cpus": 2, "memory_mb": 4096, "pids": 512 },
  "secrets": ["ANTHROPIC_API_KEY"],       // names only
  "created_by": "usr_…",
  "created_at": "…",
  "sandbox_id": null,                     // the provider's id, set at Create
  "labels": { "job": "job_…" }
}
```

| Request field | Meaning |
|---|---|
| `profile` | a profile name; the default profile when absent; 400 naming the profiles when unknown |
| `purpose` | up to 64 characters, for lists and audit |
| `ttl` | a duration at most the profile's `ttl`; shorter is allowed |
| `labels` | up to 16 string pairs, forwarded to the provider as `Spec.Labels` under a `hub.` prefix plus `hub.lease=<id>` and `hub.workspace=<slug>` |

**Errors:** 400 (unknown profile, a `ttl` over the profile's, bad labels),
403 (PAT without `sandboxes:write`), 404 (workspace, or feature off), 409
(`error_type: sandbox_capacity`: `max_sandboxes` reached, counting leases
not yet destroyed), 422 (`error_type: secret_missing`, naming the secret
the profile requires and the scopes searched, never a value), 502 (the
provider refused or could not be reached; the provider's message with any
credential removed).

**States.** `leased` from `POST` until the caller's `Create`; `active`
from `Create` to destroy; `destroyed` by `DELETE`, by the caller's
`Close`, or by hub at `expires_at`; `expired` when `lease_timeout` passed
with no `Create`; `failed` when the provider's `Create` failed (the
message is on the lease, credential-free). A lease stays listable for 24
hours after its terminal state (*spec time*: the retention), so `afc
sandbox list` explains what happened to a box that is gone.

## 5. The gateway hub serves

Hub embeds `gatewayd.Server` and serves it on `[sandbox].listen`, TLS
when configured, from the start of the process to its shutdown
(`Shutdown` closes every box). Its `Backend` is hub's own
`sandbox.Provider`, a thin wrapper over the configured provider:

```
Create(ctx, spec)   lease := the lease bound to the authenticated token (§8: the principal in ctx)
                    refuse: no lease, lease not `leased`, lease expired (FailedPrecondition, naming the state)
                    s := lease.spec                 image, Env (profile.env + secrets, resolved now, never stored),
                                                    Network, Resources, TTL = time until expires_at, RequireLandlock
                    s.Labels = spec.Labels + hub.lease, hub.workspace ; s.Workdir = spec.Workdir
                    sb, err := provider.Create(ctx, s)   (CheckSpec runs in the provider as always)
                    on success: lease.state = active, sandbox_id = sb.ID(); audit hub.sandbox.create
                    on failure: lease.state = failed with the message; audit hub.sandbox.failed
Reap(ctx)           provider.Reap, then every lease whose expires_at or lease_expires_at passed and
                    is not terminal: destroy if active, mark expired otherwise; audit hub.sandbox.expire
Capabilities()      the provider's, with NetworkModes cut to network_modes
```

Secrets are resolved at `Create`, not at `POST`: a value rotated between
the lease and the box's start is the new one, and a value is held in
memory for the length of one provider call. `GetSecretValue` is called
for the workspace scope, then the workspace's org, then the workspace
owner's user scope, first found wins; a required name found nowhere fails
the `Create` with `secret_missing` and the lease records it. *Spec time:*
whether to resolve at `POST` too, so the 422 arrives before a caller has
dialed; the recommendation is yes, as a check only, with the values
discarded.

**Tokens.** `sbl_` plus 44 characters from `crypto/rand` in a URL-safe
alphabet; hub stores the SHA-256 and the lease id; the gateway's
authenticator (§8) looks the hash up and returns the lease id as the
principal, refusing a lease in a terminal state. `DELETE` and expiry
revoke the token; a relayed RPC on a revoked token is `Unauthenticated`,
which the client reports as `sandbox_unavailable`, the right answer for a
box that is gone.

**Limits.** `max_sandboxes` counts leases in `leased` and `active`;
`gatewayd`'s own `MaxSandboxes` is set to the same number. `max_ttl`
bounds profiles at start; `gatewayd.MaxTTL` is set to it.

## 6. The CLI

```
afc sandbox create <slug> [--profile tools] [--purpose text] [--ttl 30m] [--label k=v]   prints the lease; --token-file writes the token there and prints the rest
afc sandbox list <slug> [--all]           live leases; --all includes terminal ones within retention
afc sandbox get <slug> <id>
afc sandbox destroy <slug> <id>
afc sandbox profiles                      the deployment's profiles, secret names only
```

Output is one JSON document on stdout, as every `afc` command; exit codes
are apikit's. `afc sandbox create` without `--token-file` prints the token
in the JSON once and warns on stderr that it will not be shown again.

## 7. Audit and observability

| Event | When | Metadata |
|---|---|---|
| `hub.sandbox.lease` | `POST` succeeded | `lease_id`, `profile`, `purpose`, `ttl`, `secret_names` |
| `hub.sandbox.create` | the provider created the box | `lease_id`, `sandbox_id`, `image`, `network` |
| `hub.sandbox.failed` | the provider refused | `lease_id`, `error` (credential-free) |
| `hub.sandbox.destroy` | `DELETE`, the caller's `Close`, or shutdown | `lease_id`, `sandbox_id`, `reason` (`client` \| `api` \| `shutdown`) |
| `hub.sandbox.expire` | the reaper destroyed or expired it | `lease_id`, `state` |

All carry `workspace`, so the agent-fox service sees them on its stream.
Metrics: `afhub_sandboxes_active` (gauge, by workspace), `afhub_sandbox_creates_total`
(counter, by result), `afhub_sandbox_lease_seconds` (histogram, lease to
destroy).

## 8. Dependencies on AgentKit

Two additions to `sandbox/gatewayd`, both small, neither touching a wire:

1. **A token callback.** `gatewayd.Config` gains `Authenticate func(ctx
   context.Context, token string) (principal string, ok bool)`, used in
   place of `Tokens` when set: the server calls it on every RPC, keys
   ownership by `principal` instead of by the token's hash, and logs the
   principal. Hub's authenticator maps a lease token to its lease id.
   Static `Tokens` stays for the command.
2. **The principal in the backend's context.** `gatewayd` puts the
   authenticated principal on the context it passes to
   `Backend.Create`, `Reap` and the relayed calls, readable with
   `gatewayd.PrincipalFromContext(ctx)`. Without it hub's backend cannot
   know which lease a `Create` belongs to, and a caller holding one lease
   could name another's id in a label.

Both are filed as one AgentKit spec; this PRD's spec depends on it. The
versions are pinned in hub's `go.mod` (the sandbox module and the
gatewayd module, `replace` to a sibling checkout while they move, as the
other repositories do).

## 9. Order of work

1. **AgentKit:** the two `gatewayd` additions, with tests that run a
   `gatewayd.Server` over `local` with a callback authenticator.
2. **Configuration and profiles:** the `[sandbox]` section, validation at
   start, `GET /sandbox-profiles`, `afc sandbox profiles`. No boxes yet.
3. **Leases and the gateway:** the table, `POST` and `GET`, hub's gateway
   listener with the wrapping backend, secret resolution at `Create`, the
   token lifecycle, `DELETE`, `afc sandbox create|list|get|destroy`.
   Tests over `local` with an in-process `gatewayd.Server`, as
   `examples/gateway` does.
4. **Expiry and reaping:** `lease_timeout`, the reaper on `reap_interval`,
   destroy at `expires_at`, shutdown, the audit events and metrics.
5. **The `gateway` provider:** hub as a gateway over a remote
   `agentkit-gateway`, tested against an in-process one.
6. **Documentation:** `docs/api.md`, `openapi.yaml`, `docs/cli.md`,
   `docs/configuration.md`, `docs/permissions.md` (the two scopes),
   `docs/architecture.md` (the second listener and the wrapping backend),
   `README.md`.

Steps 2 and 3 can start against a `replace` of AgentKit while step 1 is
in review.

## 10. Acceptance criteria

Offline, with `local` as the provider and hub's gateway in process:

1. `POST /workspaces/widgets/sandboxes {profile: "tools"}` with an API key
   returns 201 with a token; `gateway.Dial(gateway_url, token)` then
   `Create(sandbox.Spec{})` returns a live sandbox whose `Spec().Env`
   holds `ANTHROPIC_API_KEY` from the workspace's secrets and the
   profile's `env`, and whose `Spec().Image` and `TTL` are the profile's,
   whatever the caller put in its spec.
2. A second `Create` under the same token is refused with
   `FailedPrecondition`; a `Create` under a token hub never minted is
   `Unauthenticated`; a lease in `destroyed` refuses too.
3. The secret value appears in no HTTP response, no audit event's
   metadata, and no log line, asserted by a test that greps all three
   with the value planted.
4. A profile requiring a secret the workspace, its org and its owner
   lack fails the `Create` with `secret_missing` naming the key and the
   three scopes; the lease reads `failed`.
5. `DELETE` destroys the box (the provider's `List` no longer has it) and
   revokes the token (a relayed `Call` answers `Unauthenticated`); the
   caller's `Close` does the same through the gateway and marks the lease
   `destroyed` with `reason: client`.
6. A lease with no `Create` by `lease_timeout` reads `expired` after the
   reaper runs; an `active` lease past `expires_at` is destroyed by the
   reaper and reads `destroyed` with `reason: expired`.
7. `max_sandboxes` reached answers 409 `sandbox_capacity` on `POST`; a
   `ttl` over the profile's answers 400; an unknown profile answers 400
   naming the known ones; a PAT without `sandboxes:write` answers 403; a
   workspace the caller does not own answers 404.
8. Hub refuses to start on a profile whose `network` is outside
   `network_modes`, whose `ttl` exceeds `max_ttl`, or whose name repeats,
   each with a message naming the profile and the field.
9. `afc sandbox create widgets --token-file t` prints the lease without
   the token and writes the token to `t` with mode 0600; `afc sandbox
   list widgets` shows it `active` after a `Create` and `destroyed` after
   `afc sandbox destroy`.
10. With `provider = "gateway"` against an in-process `agentkit-gateway`
    over `local`, criteria 1, 2 and 5 hold unchanged, and the remote
    gateway's token appears in no lease.

## 11. What is given up, stated plainly

- **Hub listens on a second port**, gRPC, with its own TLS. One more
  thing to deploy and to put behind a proxy, and the first non-HTTP
  surface hub has.
- **Hub holds the provider's credentials and the one remote-gateway
  token** for the whole deployment. That is the point (the caller holds
  nothing), and it makes hub the thing to protect.
- **Secrets are resolved by the workspace owner's scopes**, not the
  caller's. A PAT holder with `sandboxes:write` on a workspace gets a box
  with the owner's `ANTHROPIC_API_KEY` in it. That is what "the
  workspace's secrets" has meant for `GIT_PAT` all along, and the scope
  is the control; `docs/permissions.md` will say it in those words.
- **No per-workspace provider or image policy beyond profiles.** A team
  that needs an image hub does not list asks the operator for a profile.
- **A box's network is the profile's**, and on Podman that is none or
  everything (AgentKit PRD 11 §9). A brain that needs its vendor gets the
  whole network; a gateway with egress filtering is where that narrows.

## Documentation

| Change | Update |
|---|---|
| the endpoints, the lease schema, the errors | `docs/api.md`, `docs/openapi.yaml` |
| `afc sandbox` | `docs/cli.md` |
| `[sandbox]` and the profiles | `docs/configuration.md` |
| `sandboxes:read`, `sandboxes:write`, the owner-scope note | `docs/permissions.md` |
| the second listener, the wrapping backend, the lease lifecycle | `docs/architecture.md`, an ADR for "hub is the sandbox gateway" |
| the events and metrics | `docs/api.md` (Hub Audit Events, Prometheus) |
| the AgentKit version pins | `go.mod`, `README.md` (sibling checkouts) |
