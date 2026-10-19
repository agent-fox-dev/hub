---
spec_id: "27"
spec_name: "sandbox_gateway"
title: "Hub's embedded sandbox gateway"
status: "active"
created_at: "2026-10-19T16:40:37.774522Z"
updated_at: "2026-10-19T16:40:37.774522Z"
intent_hash: "3cd4ace871f4643b203967f3e6c776bbc4a494eaeeb506dc4551458e985029fc"
schema_version: 2
source: "docs/prd/01-create-and-lease-sandboxes.md"
---
## Intent

Make a lease usable. Hub serves an AgentKit gateway of its own on a second listener. The gateway accepts a lease token and nothing else. With that token it creates exactly one box, and it builds the box from the lease and the profile, never from what the caller asked for. Hub resolves the profile's secrets from the workspace's scopes and places them in the box's environment, so a secret enters a sandbox only through hub. The lease record shows whether the box was created, failed or destroyed, and every one of those outcomes is audited. Neither the caller, nor any response, log line or audit event, ever sees a secret value or the provider.

## Goals

1. A caller with an API key gets a lease from `POST /api/v1/workspaces/widgets/sandboxes` and reads the lease's `gateway_url`. It calls `gateway.Dial(gateway_url, token)` and then `Create(sandbox.Spec{})`, and gets a live sandbox. The spec the provider received has the image, network, resources and TTL that the lease and profile specify, whatever the caller put in its own spec. Its `Env` is the profile's fixed `env` plus the profile's secrets resolved from the workspace's scopes.
2. A second `Create` under the same token fails with `FailedPrecondition`. A token hub never minted fails with `Unauthenticated`, and so does any RPC on a lease that is `destroyed` or `failed`.
3. Suppose a required secret is deleted between `POST` and `Create`. The `Create` then fails with a `secret_missing` message that names the key and the scopes searched, the lease reads `failed` with that message, and one `hub.sandbox.failed` event is emitted.
4. `DELETE` on an `active` lease destroys the box: the provider's `List` no longer has it. A relayed RPC on the token now gets `Unauthenticated`, and the lease reads `destroyed` with `destroy_reason: "api"`. When the caller calls `Close` through the gateway, the box is destroyed the same way and the lease reads `destroyed` with `destroy_reason: "client"`.
5. A test plants a secret value and also makes the provider return an error that contains it. The value then appears in no HTTP response, no audit event, no log line, no gateway error message, no lease `error` field and no spec the gateway reports back.
6. Hub refuses to start if `[sandbox].listen` is not on loopback and has no TLS, if it is not on loopback and has no `external_url`, if only one of `tls_cert` and `tls_key` is set, if the key pair cannot be loaded, or if the address cannot be bound. Each message names the field.
7. `make test` covers all of this offline. Each test starts an in-process `gatewayd.Server` on `127.0.0.1:0` over AgentKit's `local` provider or a recording test double, and connects with the real `gateway.Dial` client.

## Non-goals

- **Issuing leases.** This belongs to `sandbox_leases`: minting and hashing tokens, `max_sandboxes` and the 409, validating profile, ttl and labels, the secret check at `POST`, the `hub.sandbox.lease` event, and `afc sandbox create|list|get|destroy`. This spec only reads leases, changes their state and extends `DELETE`.
- **Expiry and shutdown.** These belong to `sandbox_expiry`: `lease_timeout`, the reaper and the lease sweep, destroying boxes at `expires_at`, calling `gatewayd.Server.Shutdown` when the process stops, the `shutdown` and `expired` destroy reasons, reconciling `active` leases after a restart, retention of terminal leases, `hub.sandbox.expire`, and every metric.
- **The remote provider** (`provider = "gateway"`, `gateway_url`, `gateway_token`). This belongs to `sandbox_gateway_provider`.
- **The AgentKit changes themselves** (`gatewayd.Config.Authenticate` and `gatewayd.PrincipalFromContext`). They are filed and implemented in agentkit-go. This spec consumes them.
- Running anything inside a box. Hub never execs into a box it leased.
- Changes to `afc`. The new lease fields reach its output because it prints the JSON it receives.
- Changes to `deploy/` (a second Service port, gRPC through the OpenShift route), reloading the TLS certificate while hub runs, and client-certificate (mTLS) authentication on the gateway.
- A cache for token lookups.

## Background

The source PRD (`docs/prd/01-create-and-lease-sandboxes.md`) is split into five specs, and this is the third. It is the first scope that creates a box.

What the earlier scopes provide (both are specified but not yet merged; check names against their code):

- `25_sandbox_profiles` adds the package `internal/sandboxes`. It holds the hub-owned `[sandbox]` config (`Config`, `ProfileConfig`, `LoadConfig`, `ConfigPath`), and `Validate` collects every failure with `errors.Join` and rejects unknown keys under `sandbox.*`. It also builds and probes the provider into a `Service` (`Service.Provider()`, `Service.Profiles()`), and adds `RegisterRoutes(api, svc)`, where a nil `svc` means the routes answer 404 `sandboxes_disabled`. `25-REQ-5.8` forbids that scope to call `Create` or `Destroy`. This scope is the first to call them.
- `26_sandbox_leases` adds the `sandbox_leases` table in SQLite. A lease has `id`, workspace, profile, purpose, a `state` from `leased | active | destroyed | expired | failed`, the token's SHA-256, `expires_at`, image, network, resources, required secret names, `created_by`, `created_at`, a nullable `sandbox_id` and labels. It also adds the `POST`/`GET`/`DELETE /workspaces/:slug/sandboxes` routes. Its `DELETE` ends the lease and revokes the token, and it emits no event. Its non-goals hand this spec the lease's `gateway_url` field, the `leased → active` and `leased → failed` transitions, refusing a second `Create`, box destruction on `DELETE` and `Close`, and the three events. Its `POST` builds the scope chain (workspace, then the workspace's org if the owner is a member, then the owner's user) and checks it with `ListSecrets`, without reading values.

What exists in hub today:

- `cmd/af-hub/main.go` wires everything and calls `server.Start()` last. `store := secrets.NewStore(database.SqlDB)` and `auditEmitter := audit.NewEmitterWithBroadcast(auditStore, sseMgr)` are built before the routes are registered.
- `internal/secrets/get_secret_value.go`: `(*Store).GetSecretValue(ownerType, ownerID, key string) (string, error)` matches `UPPER(key)` and returns `*secrets.NotFoundError` when the key is missing. Its doc comment says the value must never be exposed through the API.
- `internal/audit`: `Emitter.Emit(ctx, HubEvent) error` stores the event and broadcasts it on `GET /api/v1/events`. Event-type constants with their metadata live in `internal/audit/types.go`.
- Error-text scrubbing exists only as unexported helpers: `sanitizeErrorText` with `userinfoPattern` in `internal/carrypatch/push_control.go`, and `redactUserinfo` in `internal/gitcmd/gitcmd.go`.
- Hub has no gRPC dependency and no second listener. ADRs live in `docs/adr/` (`01-…`, `02-…`) in the Context / Decision / Consequences / Alternatives format.
- The deployment runs one replica (`deploy/deployment.yaml`: `replicas: 1`, `strategy: Recreate`), so an in-process lock is enough to serialise work on one lease.

## Requirements

**R1. Gateway configuration.** The `[sandbox]` table gains four keys. They are added to the set that `25_sandbox_profiles` recognises, are expanded for `${NAME}` like every other string, and are validated together with that scope's other failures.
- `listen` is a `host:port` address and defaults to `"127.0.0.1:8443"`. The port must be numeric, from 0 to 65535, and 0 picks a free port. The address is on loopback when the host is `localhost` or a literal IP address for which `IsLoopback()` is true. An empty host, `0.0.0.0`, `::` and any other hostname are not loopback.
- `tls_cert` and `tls_key` are file paths. Both are set or neither is, and a failure names the one that is missing.
- When `listen` is not on loopback, TLS must be configured. Otherwise start is refused with a message naming `sandbox.listen` and saying that a plaintext gateway is allowed only on loopback.
- `external_url` is required when `listen` is not on loopback. When set, it must be an absolute `http` or `https` URL with a host, no userinfo, no query, no fragment and no path other than `/`. A failure names `sandbox.external_url`.

After static validation passes, hub loads the key pair with `tls.LoadX509KeyPair`. A failure refuses start, names `sandbox.tls_cert` and `sandbox.tls_key`, and includes the loader's error.

**R2. Gateway lifecycle and `gateway_url`.** When the feature is on, hub binds `listen` before the HTTP server starts serving. A bind failure refuses start with a message naming `sandbox.listen`. Hub builds a `gatewayd.Server` with these settings:
- `Backend`: hub's wrapping backend (R4–R7).
- `Authenticate`: hub's lease authenticator (R3). No static `Tokens`.
- `TLS`: the loaded key pair, or none.
- `NetworkModes`: `network_modes`.
- `MaxSandboxes`: `max_sandboxes`.
- `MaxTTL`: `max_ttl`.

Hub serves the gateway on the bound listener for the life of the process. If `gatewayd.New` fails, start is refused. If `Serve` returns unexpectedly, hub logs the error and exits non-zero, because leases would otherwise be issued that nobody can use. When the feature is off, nothing is bound.

The gateway URL is `external_url` when that is set. Otherwise it is `https://` when TLS is configured, or `http://` when it is not, followed by the listener's bound address, so port 0 resolves to the real port. Every lease representation (`POST`, `GET` one, the workspace list and the admin list) carries it as `gateway_url`.

**R3. Authenticating lease tokens.** gatewayd calls the authenticator on every RPC. The authenticator takes the SHA-256 of the presented token, the same way `sandbox_leases` does, and looks up the lease. It accepts the token, returning the lease id as the principal, only when the lease exists, is in `leased` or `active`, and has an `expires_at` in the future by hub's injected clock. Otherwise it refuses, and gatewayd answers `Unauthenticated`. Because the check reads the lease store every time, moving a lease to `destroyed` or `failed` revokes its token for the next RPC, including the relayed Agent calls on a box that already exists. The authenticator never logs the token or its hash.

**R4. Create builds hub's spec.** The backend's `Create` reads the principal from the context. It reads it through an injected function that defaults to `gatewayd.PrincipalFromContext`, so tests can supply one. Then it does the following, in order:
1. It claims the lease in an in-process set. If a `Create` for the same lease is already in flight, the call fails with `FailedPrecondition`.
2. It loads the lease. If the state is not `leased`, the call fails with `FailedPrecondition` naming the state. If `expires_at` has passed, the call fails with `FailedPrecondition` saying the lease has expired, and the state is not changed, because marking expiry belongs to `sandbox_expiry`.
3. It looks up the lease's profile among the validated profiles in memory. If the profile is no longer configured, the lease becomes `failed` with a message naming the profile.
4. It builds the spec:
   - Image, network, resources and required secret names come from the lease record.
   - The profile's fixed `env`, optional secret names, `require_landlock` and any resource field the lease does not record come from the profile.
   - `TTL` is the time left until `expires_at`, truncated to whole seconds.
   - `Workdir` is the caller's.
   - `Labels` are the caller's labels with every key that starts with `hub.` removed. Each lease label `k=v` is added as `hub.k=v`, and then `hub.lease=<lease id>` and `hub.workspace=<slug>` are added, overriding any earlier value.
   - Everything else in the caller's spec (image, env, network, resources, TTL, landlock) is ignored.
5. It resolves the secrets (R5) and calls the provider's `Create`. The provider runs `CheckSpec` as it always does.

The claim is released when the call returns, whatever the outcome.

**R5. Secret resolution when the box is created.** Hub resolves secret values during `Create`, not at `POST`, so a value rotated in between takes effect. Hub uses the same scope chain as the `POST` check of `sandbox_leases`: the workspace (`workspace`, slug), then the workspace's org (`org`, `workspaces.org_id`) only when it is set and the workspace owner is a member of it, then the owner's user scope (`user`, `workspaces.owner_id`). For each required and each optional name, hub calls `secrets.Store.GetSecretValue` tier by tier, and the first tier that has the name wins. A `*secrets.NotFoundError` moves on to the next tier.
- A required name found in no tier makes the lease `failed`. Its error reads `secret_missing:`, then the key, then the scopes searched (workspace, org, owner user). The call fails with `FailedPrecondition` and the same text.
- An optional name found in no tier is skipped.
- Any other store error fails the call with an internal error. The lease stays `leased` and its claim is released, so the caller can retry.

The injected variable uses the profile's spelling of the name. `Env` is the profile's `env` plus the resolved secrets. `25_sandbox_profiles` already forbids collisions between the two. Values live only in memory, for the length of the provider call. They are never written to the database.

**R6. Create outcomes and races.** Every state change is a conditional update on the state hub expects to find.
- **Success.** The update `leased → active` also sets `sandbox_id` to the box's id. Hub emits one `hub.sandbox.create` event with `lease_id`, `sandbox_id`, `image` and `network`, and returns the wrapped sandbox (R7).
- **Lost race.** If the update finds the lease no longer `leased` (a `DELETE` ran during the provider call), hub destroys the new box on the provider and logs a warning with the lease id and the sandbox id. It emits no create event and fails the call with `FailedPrecondition` naming the current state.
- **Provider failure.** The update `leased → failed` records the provider's message, scrubbed (R9), as the lease's `error`. Hub emits one `hub.sandbox.failed` event with `lease_id` and the same `error`, and returns the scrubbed error to the caller.
- **Other failures.** A missing profile or a missing secret (R4, R5) records its message, emits `hub.sandbox.failed` and fails the call with `FailedPrecondition`.

`destroyed` and `failed` are terminal. A token cannot create twice: the first `Create` moves its lease out of `leased`, and every later `Create` fails in R4 step 2, or in R3 when the state is terminal.

**R7. The rest of the backend.**
- `Capabilities()` returns the provider's capabilities with `NetworkModes` narrowed to `network_modes`.
- `List`, when the context carries a principal, returns at most the one box bound to that principal's lease. Without a principal it returns the provider's list.
- `Destroy(id)`, and `Close` on the sandbox handle `Create` returned, form the client destroy path. When a principal is present, the lease it names must have `sandbox_id == id`, or the call fails as not found without touching the provider. Hub calls the provider's `Destroy`.
  - If the provider reports that the box does not exist, hub treats that as success.
  - Any other provider error is returned scrubbed, and the lease stays `active`.
  - On success, the update `active → destroyed` records `destroy_reason: "client"`, and hub emits one `hub.sandbox.destroy` event with `lease_id`, `sandbox_id` and `reason: "client"`.
- `Reap` passes straight through to the provider. The lease sweep belongs to `sandbox_expiry`.
- Every other method of the handle delegates to the provider's sandbox, except `Spec()`. The spec it reports keeps every `Env` key but replaces each value with `[redacted]`.

**R8. DELETE destroys the box.** This spec extends the `DELETE /workspaces/:slug/sandboxes/:id` of `sandbox_leases`. Its authentication, scope and ownership checks and its handling of terminal leases stay unchanged.
- **Lease `active`.** Hub first calls the provider's `Destroy(sandbox_id)`, bounded by 30 seconds. "Does not exist" counts as success. Any other failure answers 502 with `error_type: sandbox_provider_error` and the scrubbed message, and changes nothing: the lease stays `active` and the token stays valid, so the `DELETE` can be retried. On success, the update `active → destroyed` records `destroy_reason: "api"`.
- **Lease `leased`.** The `leased → destroyed` change of `sandbox_leases` applies, also recording `destroy_reason: "api"`. A `Create` still in flight loses its race (R6).

If the conditional update finds that the state changed under it, hub re-reads the lease once and acts on the new state. That happens, for example, when a `Create` finished in between and the box must now be destroyed.

Every change into `destroyed` made by `DELETE` emits exactly one `hub.sandbox.destroy` event with `lease_id`, `sandbox_id` (null when no box existed) and `reason: "api"`. The event's actor is the HTTP credential.

The lease record gains `error` (nullable text) and `destroy_reason` (nullable, `client` or `api` in this spec), and every lease representation shows both. If the schema of `sandbox_leases` lacks them, this spec adds them with an idempotent `ALTER TABLE … ADD COLUMN` in the style of `jobqueue.MigrateProgress`.

**R9. No secret or token leaves hub.**
- **Scrubbing.** Before any provider error text is stored on a lease, put in an audit event, logged or returned to a gateway or HTTP caller, hub scrubs it. It replaces every non-empty resolved secret value and every non-empty profile `env` value with `[redacted]`, and removes URL userinfo using the pattern of `carrypatch`'s `userinfoPattern`. Then it truncates the text to 1024 bytes on a UTF-8 boundary. Scrubbing comes first, so truncation can never split a value and leave part of it unscrubbed.
- **Logging.** Hub never logs a secret value, an `env` value, a lease token or a token hash.
- **Audit events.** Events carry secret names only. All three events set `Workspace` to the lease's slug, `ResourceID` to the lease id, and the same `ResourceType` that `sandbox_leases` uses for `hub.sandbox.lease`. Events raised through the gateway have `ActorType: "lease"` and `ActorID` set to the lease's `created_by`. A nil emitter skips emission, and an emit error is logged without changing the outcome.

**R10. Dependency, ADR and documentation.**
- **Dependency.** `go.mod` requires the agentkit-go module that provides `sandbox/gatewayd` at a version that has `Config.Authenticate` and `PrincipalFromContext`. If that is a separate module from the sandbox module `25_sandbox_profiles` added, it gets its own `replace` into the `../agentkit-go` sibling checkout. The gRPC packages it pulls in stay out of `cmd/afc`, and the `go list -deps ./cmd/afc` check of `25_sandbox_profiles` still passes.
- **Audit constants.** `internal/audit/types.go` gains `EventSandboxCreate`, `EventSandboxFailed` and `EventSandboxDestroy`, with doc comments that list their metadata.
- **ADR.** `docs/adr/03-hub-is-the-sandbox-gateway.md` records why hub embeds the gateway and what that costs: the second listener, hub holding the provider credentials, and the one-shot lease.
- **Documentation.** In the same change, after `make doc`:
  - `docs/configuration.md`: the four keys and their rules.
  - `docs/api.md` and `docs/openapi.yaml`: `gateway_url`, `error` and `destroy_reason` on the lease schema, the 502 `sandbox_provider_error` on `DELETE` and in the error-type table, and the three events in the Hub Audit Events table.
  - `docs/architecture.md`: the second listener, the wrapping backend and the lease state machine.
  - `docs/cli.md`: `afc sandbox destroy` now destroys the box.
  - `README.md`: the gateway port.

  No tests are written about the docs.

## Design Decisions

1. **The gateway lives in `internal/sandboxes`, beside the config, the service and the lease store.** It needs their unexported lease operations and the in-memory profiles. A second package would have to export the lease store only for this.
2. **`listen` defaults to `127.0.0.1:8443`, and port 0 is allowed.** The source says loopback by default and uses 8443 in its example. Port 0 lets tests and several hubs on one host run without colliding.
3. **Loopback means `localhost` or a literal loopback IP. No DNS lookup is done at validation.** Validation stays deterministic and offline. A hostname that might resolve to a public address is treated as public.
4. **A plaintext gateway is allowed only on loopback, and `external_url` is required off loopback.** The source states both rules. A lease token in plaintext on the network would hand over a box full of secrets.
5. **TLS is passed to gatewayd's `TLS` setting, from a key pair loaded once at start.** The source names that field, and loading at start turns a bad certificate into a start-up failure instead of a failure on the first RPC.
6. **`gateway_url` is `external_url`, or else a URL built from the scheme and the bound address.** With port 0 the bound address is the only correct one, and an operator behind a proxy sets `external_url`.
7. **`gateway_url` appears on every lease representation, not only on `POST`.** It is not secret, and a caller that lost the `POST` response still needs to know where to dial.
8. **The authenticator reads the lease store on every RPC, without a cache.** Revocation on `DELETE` and on `failed` has to take effect on the very next RPC, and an indexed SQLite lookup costs microseconds.
9. **The authenticator also refuses a lease whose `expires_at` has passed.** The lease's expiry is the box's TTL, so a box past it must not be usable while hub waits for the reaper of `sandbox_expiry`.
10. **The principal is read through an injected function that defaults to `gatewayd.PrincipalFromContext`.** Unit tests can then drive the backend without depending on how gatewayd stores the principal in its context.
11. **A lease gets one `Create` through an in-process claim plus conditional state updates.** Hub runs as a single replica. The claim stops two concurrent `Create`s from both reaching the provider, and the conditional update settles the race with `DELETE`.
12. **Fields recorded on the lease come from the lease. Everything else comes from the profile, and a profile that is gone fails the lease.** The lease keeps telling the truth about the box. Fixed `env` values, which `${VAR}` expansion can fill with operator credentials, are never written to the database.
13. **The caller's labels are kept, except keys beginning `hub.`. Lease labels become `hub.<k>`, and `hub.lease` and `hub.workspace` are set last.** The source keeps the caller's `Labels` and `Workdir` and prefixes lease labels. Dropping `hub.` keys stops a caller from forging another lease's id.
14. **`TTL` is the time left until `expires_at`, and a lease already past it is refused without a state change.** The box must not outlive the lease, and moving the lease to `expired` is the job of `sandbox_expiry`.
15. **Secrets are resolved at `Create` through the scope chain of the `POST` check, including the owner-membership rule for the org tier.** The source resolves at `Create` so rotations take effect. Using one chain means the 422 check at `POST` and the injection agree. The membership rule matches `handleResolvedWorkspaceVars`.
16. **A required secret missing at `Create` makes the lease `failed`. A store error leaves it `leased`.** A missing secret is a configuration fact that the caller cannot fix by retrying the same lease. A store error is transient.
17. **Provider error text is scrubbed of secret values, `env` values and URL userinfo, then truncated to 1024 bytes.** An engine can echo its command line, including `-e KEY=VALUE`, in an error. Scrubbing before truncating means no partial value survives.
18. **The handle's `Spec()` reports `Env` keys with values redacted.** The source's intent says the caller never sees the values that went in. Anything inside the box can still read them, but no RPC response carries them.
19. **A box created after its lease was deleted is destroyed at once.** "A box outliving its lease is a bug". The deleted lease must not be revived.
20. **The client destroy path checks that the principal's lease owns the sandbox id.** gatewayd already enforces ownership. Checking again in the backend costs one comparison and guards against a gatewayd that does not.
21. **`DELETE` on an `active` lease destroys the box first. A provider failure answers 502 `sandbox_provider_error` and changes nothing.** The lease never reads `destroyed` while its box still runs, and the `DELETE` stays retryable. A box already missing at the provider counts as destroyed.
22. **Every change into `destroyed` emits exactly one `hub.sandbox.destroy`, with `sandbox_id` null when no box existed.** `sandbox_leases` left its `DELETE` without an event so that this spec could add one, and every lease ending is then on the audit stream.
23. **Events raised through the gateway use `ActorType: "lease"` and `ActorID` = the lease's `created_by`.** The calling credential is the lease token, and `created_by` says who issued it. `actor_type` has no CHECK constraint.
24. **gatewayd error codes:** lease-state, secret and profile problems are `FailedPrecondition`, token problems are gatewayd's `Unauthenticated`, and provider errors are passed through scrubbed. This matches the source's acceptance criteria. The client reports `Unauthenticated` as `sandbox_unavailable`, which is the right answer for a box that is gone.
25. **If `Serve` returns unexpectedly, hub exits non-zero.** Otherwise the REST API would keep issuing leases that nobody can attach to.
26. **This scope never calls `gatewayd.Server.Shutdown`.** Destroying boxes at shutdown, with reason `shutdown`, belongs to `sandbox_expiry`. Until then, a box left behind when hub exits is bounded by the TTL the provider enforces.
27. **`error` and `destroy_reason` are added to the lease with an idempotent migration if `sandbox_leases` did not create them.** The source puts the failure message on the lease, and `ALTER TABLE ADD COLUMN` guarded by `PRAGMA table_info` is the repository's migration pattern.
28. **The ADR is `docs/adr/03-hub-is-the-sandbox-gateway.md`.** The source asks for it, and it is the next number.
29. **`deploy/` is not changed.** The feature is off unless `[sandbox]` is configured. Exposing a gRPC port through the OpenShift route is a deployment decision, not a code change.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `25_sandbox_profiles` | depends on, modifies | Uses `Service.Provider()`, the validated profiles, `network_modes` and `max_ttl`. Adds `listen`, `external_url`, `tls_cert` and `tls_key` to its recognised keys and to `Validate`. Starts the gateway only when its `Service` is non-nil. |
| `26_sandbox_leases` | depends on, modifies | Reads leases and token hashes, and reuses its token hashing, its scope chain and `max_sandboxes`. Adds the `leased → active` and `leased → failed` transitions, the `gateway_url`, `error` and `destroy_reason` fields, and box destruction plus the destroy event to its `DELETE`. |
| `07_secrets_variables` (archived) | depends on | `secrets.Store.GetSecretValue` and `*secrets.NotFoundError` resolve values tier by tier. |
| `18_hub_audit_query` (archived) | depends on | `audit.Emitter` with SSE broadcast carries the three events to `GET /api/v1/events`. |
| AgentKit `gatewayd` additions (agentkit-go, external) | depends on | `gatewayd.Config.Authenticate` and `gatewayd.PrincipalFromContext`. gatewayd must also hand the caller's spec to `Backend.Create` without rejecting an empty image or env first, so that `Create(sandbox.Spec{})` reaches hub's backend. |

## Verified External API

agentkit-go is not vendored and lies outside the repository. `25_sandbox_profiles` adds it to `go.mod` as a sibling `replace`. No agentkit-go symbol could be read, so every row below is **unverified** and taken from the source PRD and the specs of the earlier scopes.

| Symbol | Assumed shape | Status |
|---|---|---|
| `gatewayd.New` | `func New(cfg gatewayd.Config) (*gatewayd.Server, error)` | unverified |
| `gatewayd.Config` | `{Backend sandbox.Provider; Tokens …; TLS *tls.Config; NetworkModes []string; MaxSandboxes int; MaxTTL time.Duration}` | unverified |
| `gatewayd.Config.Authenticate` | `func(ctx context.Context, token string) (principal string, ok bool)`, used instead of `Tokens` when set; ownership keyed by principal | NOT FOUND (pending AgentKit spec) |
| `gatewayd.PrincipalFromContext` | `func PrincipalFromContext(ctx context.Context) (string, bool)`; set on the context of `Backend.Create`, `Reap` and relayed calls | NOT FOUND (pending AgentKit spec); the return shape is assumed |
| `(*gatewayd.Server).Serve` | `func (s *Server) Serve(l net.Listener) error` | unverified |
| `(*gatewayd.Server).Shutdown` | `func (s *Server) Shutdown(ctx context.Context) error`; not called in this scope | unverified |
| `sandbox.Provider` | `Create(ctx, Spec) (Sandbox, error)`, `List(ctx) ([]Sandbox, error)`, `Destroy(ctx, id string) error`, `Capabilities() Capabilities`, `Reap(ctx) …` | unverified |
| `sandbox.Sandbox` | interface with at least `ID() string`, `Spec() Spec`, `Close() error` plus the Agent-protocol methods gatewayd relays | unverified |
| `sandbox.Spec` | `{Image string; Env map[string]string; Network string; Resources Resources; TTL time.Duration; RequireLandlock bool; Labels map[string]string; Workdir string}` | unverified |
| `sandbox.Capabilities` | struct with `NetworkModes []string` | unverified |
| a provider "not found" error | a sentinel such as `sandbox.ErrNotFound`, tested with `errors.Is` | unverified |
| gatewayd error-to-status mapping | how a backend error becomes `FailedPrecondition`: a gatewayd/sandbox sentinel, or a passed-through `google.golang.org/grpc/status` error | unverified |
| `gateway.Dial` (tests) | `func Dial(url string, opts gateway.Options) (sandbox.Provider, error)`; `Options{Token string}`; accepts `https://host:port`, and `http://host:port` for plaintext loopback is assumed | unverified |
| `local.Unisolated` (tests) | constructor for an unisolated `sandbox.Provider` | unverified |

Hub-internal and standard-library symbols, read in the repository:
- `func (s *secrets.Store) GetSecretValue(ownerType, ownerID, key string) (string, error)`, which returns `*secrets.NotFoundError{Key string}` (`internal/secrets/get_secret_value.go` and `store.go`). Verified.
- `audit.Emitter{ Emit(ctx context.Context, event audit.HubEvent) error }` and `audit.HubEvent{EventType, ActorID, ActorType, ResourceType, ResourceID, Action, Workspace string; Metadata map[string]any}` (`internal/audit/emitter.go`, `types.go`). Verified.
- `apikit.WriteAPIErrorWithType(c echo.Context, code int, msg, errorType string) error`, verified from call sites such as `internal/carrypatch/sync_handlers.go:213`, which already uses it with 502.
- Standard library: `crypto/tls.LoadX509KeyPair`, `net.Listen`, `net.SplitHostPort`, `net.ParseIP(...).IsLoopback()`, `net/url.Parse`, `crypto/sha256`.
