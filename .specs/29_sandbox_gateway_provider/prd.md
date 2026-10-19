---
spec_id: "29"
spec_name: "sandbox_gateway_provider"
title: "Remote agentkit-gateway as hub's sandbox provider"
status: "active"
created_at: "2026-10-19T16:56:38.986406Z"
updated_at: "2026-10-19T16:56:38.986406Z"
intent_hash: "de21c54323aae9f41b6021ed93ca2460a87af09037d97fac0430b1a56ca86c3a"
schema_version: 2
source: "docs/prd/01-create-and-lease-sandboxes.md"
---
## Intent

Let a hub deployment run its sandboxes on a remote `agentkit-gateway` instead of a container engine on hub's own host. Hub holds a single credential for that gateway, checks at start that the gateway is reachable and accepts the credential, and then puts its own lease-enforcing gateway in front of it. Callers keep the same API, the same leases and the same guarantees: they never see the remote gateway, its address or its credential.

## Goals

1. With `provider = "gateway"`, a valid `gateway_url` and `gateway_token`, and a reachable agentkit-gateway that accepts that token, hub starts. The start-up probe makes exactly one `List` call to the remote under hub's token and creates nothing.
2. Over that remote gateway, the source's acceptance criteria 1, 2 and 5 hold unchanged:
   - A lease from `POST /api/v1/workspaces/widgets/sandboxes`, followed by `gateway.Dial(lease.gateway_url, token)` and `Create(sandbox.Spec{})`, produces a box on the remote gateway's backend. The box has the lease's image and TTL and the profile's `env` plus the resolved secrets.
   - A second `Create` under the same token is `FailedPrecondition`. A token hub never minted is `Unauthenticated`.
   - `DELETE`, and the caller's `Close`, destroy the box: the remote's `List` no longer has it. Both also revoke the lease token.
3. A test plants hub's remote token and makes the remote backend return an error that contains the token and the remote host. Neither then appears in any of these: a lease representation, an HTTP response body, an audit event, a log line, a gateway error message or a lease's `error` field.
4. Every configuration fault listed under Requirements stops hub at start with a message that names the field and never contains the token. The faults are: a missing or malformed `gateway_url`, a missing or malformed `gateway_token`, plaintext to a host that is not loopback, a key that belongs to the other provider, an unreachable remote, a remote that refuses the token, a `network_modes` entry the remote does not offer, and a profile the remote's capabilities reject.
5. If the remote gateway restarts on the same address, hub's next `Create` succeeds without hub being restarted. While the remote is down, a `Create` marks the lease `failed`, and a `DELETE` of an `active` lease answers 502 and changes nothing.
6. `make test` covers all of this offline. The remote is an in-process `gatewayd.Server` with a static token over AgentKit's `local` provider, on `127.0.0.1:0`. No container engine and no network access are needed.

## Non-goals

- **Everything the earlier scopes own.** That covers the `[sandbox]` section and profiles (`25_sandbox_profiles`), leases and their API (`26_sandbox_leases`), hub's embedded gateway, the wrapping backend, secret injection and scrubbing (`27_sandbox_gateway`), and expiry, the reaper, shutdown, restart recovery and metrics (`28_sandbox_expiry`). This spec only adds a second way to build the provider those scopes already use.
- More than one provider at a time, a provider per workspace, or switching providers while hub runs.
- A custom CA, client certificates (mTLS) or proxy settings for the connection to the remote. Hub uses the gateway client's defaults and the host's trust store.
- Hub-side redialling, health monitoring, readiness checks or circuit breaking for the remote. Hub relies on the client's own reconnection.
- Deploying, configuring or securing the remote agentkit-gateway, and changes under `deploy/`.
- Checking at start the remote's `MaxSandboxes` or `MaxTTL`, beyond what its capabilities report.
- Rotating `gateway_token` without a restart, reloading configuration, and reclaiming boxes created under an earlier token.
- New REST endpoints, lease fields, audit events, metrics or `afc` commands.
- Changes to AgentKit.

## Background

The source PRD (`docs/prd/01-create-and-lease-sandboxes.md`) is split into five specs, and this is the fifth. It asks for one provider per deployment: either Podman or Docker on hub's host, or "an `agentkit-gateway` elsewhere by URL and token", with "the same API" serving both. When the provider is remote, hub becomes "a gateway over a gateway". Its backend is `gateway.Dial` to the deployment's agentkit-gateway with the one token hub holds, every lease shares that token, and "the caller never sees that token".

What the earlier scopes put in place. All four are specified, and none is implemented yet; `internal/sandboxes/` does not exist.

- `25_sandbox_profiles` decodes `[sandbox]` into a hub-owned `Config`, rejects unknown `sandbox.*` keys, expands `${NAME}` in every string, and collects static failures with `errors.Join`. It explicitly refuses `provider = "gateway"` with "not supported by this build" (25-REQ-2.2, its Design Decision 5) and defers it to this spec. Its provider seam is `ProviderFactory func(podman.Options) (sandbox.Provider, error)`, which is Podman-only. After the static checks pass, it probes the provider with exactly one `List` under a 10-second timeout (25-REQ-5.1). It then checks `network_modes` against `Capabilities().NetworkModes` and runs `sandbox.CheckSpec` per profile (25-REQ-5.4 to 5.7). Its start-up info line carries only the provider name and the profile names (25-REQ-8.2). Tests inject the provider, and `local` can never be selected in configuration.
- `27_sandbox_gateway` serves hub's own `gatewayd.Server` on `[sandbox].listen` with a lease authenticator. Its wrapping backend delegates to `Service.Provider()`, whatever that provider is. It scrubs provider error text of secret values, `env` values and URL userinfo before the text is stored, logged, emitted or returned (27-REQ-9.1, 9.3). It treats AgentKit's not-found sentinel as success when destroying a box (27-REQ-7.5, 8.2). It defines "loopback" as `localhost` or a loopback IP literal, with no DNS lookup (27-R1).
- `26_sandbox_leases` defines the lease representation. `28_sandbox_expiry` runs `provider.Reap`, the lease sweep, restart recovery and the shutdown destroy step. Its PRD says it needs no change when the remote provider arrives.

What exists in hub's code today:
- `cmd/af-hub/main.go` loads apikit's config, opens the database and runs `apikit.Bootstrap`. It builds the secrets store, the metrics and the audit emitter, registers routes and calls `server.Start()` last. Per 25 and 27, the sandbox service is built right after `Bootstrap`, and hub's gateway binds just before `server.Start()`.
- `go.mod` has no agentkit-go and no gRPC module yet. 25 and 27 add them through a sibling `replace => ../agentkit-go`.
- `internal/carrypatch/push_control.go` holds `userinfoPattern`, which 27 copies into its scrubber.
- `deploy/deployment.yaml` runs a single replica, and `deploy/configmap.yaml` already uses `${VAR}` for OAuth secrets. That is the convention `gateway_token = "${AGENTKIT_GATEWAY_TOKEN}"` follows.

## Requirements

**R1. Selecting the remote provider.** `[sandbox].provider` accepts `"podman"` and `"gateway"`. This replaces the refusal in 25-REQ-2.2. Any other value refuses start with a message that names `sandbox.provider` and lists both accepted values. With `"gateway"`, hub builds no Podman provider and never runs an engine binary. Everything else in `25_sandbox_profiles` applies to both providers unchanged: the profile rules, `max_ttl`, `network_modes`, the scopes, the profile listing and the disabled state.

**R2. The two connection keys.** `gateway_url` and `gateway_token` are added to the keys `25_sandbox_profiles` recognises under `[sandbox]`. Both are expanded for `${NAME}` like every other string. Both are validated with the other static checks and reported in the same joined error, before any provider is built.
- `gateway_url` is required when `provider = "gateway"`. It must parse as an absolute URL with:
  - scheme `https`, or `http` only when the host is loopback under 27's definition;
  - a non-empty host and, if a port is given, a numeric port from 1 to 65535;
  - no userinfo, no query, no fragment, and a path that is empty or `/`.

  A failure names `sandbox.gateway_url` and the rule it broke. A message that quotes the URL first removes any userinfo.
- `gateway_token` is required when `provider = "gateway"`. A value that is empty after expansion counts as missing. It must be at least 16 characters long and contain only printable ASCII without spaces (0x21–0x7E). A failure names `sandbox.gateway_token` and the rule, and never includes the value or any part of it.

**R3. Keys of the other provider.** When `provider = "gateway"`, a non-empty `engine_binary`, `runtime` or `userns` refuses start. The message names the key and says it applies only to provider `"podman"`. When `provider = "podman"`, a non-empty `gateway_url` or `gateway_token` refuses start in the same way, saying the key applies only to provider `"gateway"`, and never echoes the token. These failures are collected with the other static failures.

**R4. Building and probing the remote client.** When static validation passes, hub builds the remote client through the provider factory seam of `25_sandbox_profiles`, which this spec extends with a gateway variant. In production the variant calls `gateway.Dial(gateway_url, gateway.Options{Token: gateway_token})`, and tests inject their own. Hub then runs 25's probe: exactly one `List` call under a 10-second timeout. When static validation fails, nothing is dialled and nothing is listed. If the client cannot be built, or the probe fails for any reason, hub refuses start. Reasons include an unreachable address, a TLS failure, the remote refusing the token and the timeout. The error:
- starts with `sandbox:`;
- names `sandbox.provider`, `sandbox.gateway_url` and `sandbox.gateway_token`;
- includes the client's error text with the token replaced by `[redacted]` and URL userinfo removed.

Hub creates nothing on the remote at start.

**R5. Remote capabilities.** After a successful probe, hub reads the remote's capabilities once and keeps that snapshot for the life of the process. Two existing checks run against the snapshot with their messages unchanged: 25's check that every `network_modes` entry is offered (25-REQ-5.4) and its per-profile `sandbox.CheckSpec` check (25-REQ-5.5 to 5.7). Hub's backend `Capabilities()` (27-REQ-7.1) narrows the same snapshot and makes no call to the remote. A change to the remote's capabilities takes effect when hub next restarts.

**R6. The same behaviour over the remote.** Hub's gateway, the lease API and the expiry work run on the remote client through the same `sandbox.Provider` seam they use for Podman, so a box is created on the remote under hub's single token. Lease tokens are never sent to the remote. A lease's `gateway_url` is always hub's own gateway URL (27-REQ-2.6), never the configured remote `gateway_url`. Three behaviours are specific to the remote provider:
- **Not found.** Wherever `27_sandbox_gateway` or `28_sandbox_expiry` treats a provider "not found" as success when destroying a box, a remote not-found counts too, whether the client reports it as AgentKit's not-found sentinel or as a gRPC `NotFound` status.
- **Remote refuses hub's token.** Sometimes a call fails after start because the remote refused hub's token: the remote's status is `Unauthenticated` or `PermissionDenied`. The error that reaches a caller of hub's gateway then carries `Unavailable` instead of the remote's code, with the scrubbed message. A caller therefore never mistakes the remote refusing hub for hub refusing its own lease token. The lease outcome follows 27 unchanged: a `Create` makes the lease `failed`, a `Destroy` leaves it `active`, and a `DELETE` answers 502. Each time this happens, hub logs one error that names `sandbox.gateway_token`, never its value.
- **No retries.** Hub never retries a remote `Create`, because a retry after a lost response could create a second box. The TTL that hub's backend passes bounds a box orphaned that way.

**R7. The remote token and address never leave hub.**
- While `provider = "gateway"`, the scrub set that 27-REQ-9.1 applies always contains three values besides the secret and `env` values: the token, the configured `gateway_url` string, and the URL's host as `url.Host` gives it. Every provider error text stored on a lease, emitted, logged, or returned to a gateway or HTTP caller is therefore free of them.
- The token appears in no lease representation, no HTTP response (including the profile listing and error bodies), no audit event, no log line, no metric label and no gateway error. The start-up info line still names only the provider and the profiles (25-REQ-8.2).
- After validation, hub keeps the token in a value whose `fmt` forms (`%v`, `%+v`, `%#v`, `%s`), `slog` form and JSON form all render `[redacted]`, so printing a struct cannot leak it.
- Start-up errors (R2 to R5) may quote `gateway_url` with its userinfo removed, but never the token.

**R8. Remote outages and shutdown.** Hub does not redial the remote and does not monitor it. While the remote cannot be reached:
- issuing a lease and listing profiles keep working, because neither calls the provider;
- a `Create` makes the lease `failed` with the scrubbed error (27-REQ-6.3);
- a `DELETE` of an `active` lease answers 502 `sandbox_provider_error` and changes nothing (27-REQ-8);
- a caller's `Close` returns an error and the lease stays `active`;
- the expiry sweep logs the failure and tries again on its next run.

When the remote is reachable again at the same address, hub's next provider call succeeds without hub being restarted. On shutdown, once the `28_sandbox_expiry` shutdown step has finished destroying active boxes, hub closes the remote client if the client implements `io.Closer`.

**R9. Documentation and build.** In the same change, after `make doc`:
- `docs/configuration.md` documents:
  - `provider = "gateway"`, `gateway_url` and `gateway_token`, with every rule from R2 and R3;
  - the recommended `gateway_token = "${AGENTKIT_GATEWAY_TOKEN}"`;
  - the trust store used for `https`;
  - that the remote's limits (`MaxSandboxes`, `MaxTTL`) show up as failed leases at `Create`;
  - that changing `gateway_token` leaves boxes created under the old token to the remote's TTL.
- `docs/architecture.md` describes the gateway-over-gateway layout: the one shared token, the capability snapshot, and where outages show up.
- If `docs/adr/03-hub-is-the-sandbox-gateway.md` from 27 does not cover the remote case, it gains a short paragraph under Consequences.

If the package that provides `gateway.Dial` is not yet required for non-test code, `go.mod` adds it under the existing `../agentkit-go` replace. `go list -deps ./cmd/afc` still lists no agentkit, gRPC or `internal/sandboxes` package (TS-25-54). No tests are written about the docs.

## Design Decisions

1. **The remote provider is only a second way to build the `sandbox.Provider` that 25 hands to everyone else.** Hub's backend, the lease API and the expiry work have no remote-specific path beyond the three behaviours in R6. The source says one API serves both providers, and 28's PRD already assumes this.
2. **The config keys keep the source's names, `gateway_url` and `gateway_token`, even though a lease also has a `gateway_url` field.** The split fixed these names. R6 states that the lease field is always hub's own URL, and a test asserts it differs from the configured remote URL.
3. **`https` is required, and `http` is allowed only to a loopback host, using 27's loopback rule.** The token gives access to every box hub runs, so it must not cross a network in plaintext. Loopback still allows a sidecar setup and the in-process test remote.
4. **`gateway_url` may not carry userinfo, a query, a fragment or a path.** Credentials belong in `gateway_token`. Userinfo in the URL would end up in error text and logs.
5. **`gateway_token` must have at least 16 characters of printable ASCII without spaces.** gRPC metadata accepts only printable ASCII, a trailing newline from a secret file is a common mistake, and a very short token is both weak and would make scrubbing mangle ordinary text.
6. **A key that belongs to the other provider refuses start instead of being ignored.** `runtime = "krun"` or `userns` silently ignored under a remote provider would make an operator believe in isolation that does not exist. This follows 25's rule of refusing any key that would do nothing.
7. **The probe is 25's single `List` with a 10-second timeout.** It proves that the remote is reachable and accepts hub's token without creating anything, and it reuses 25's tested start-up path.
8. **The remote's capabilities are read once at start and kept.** The assumed `Capabilities()` takes no context and returns no error. Reading the remote on every call could block gatewayd or report empty modes during an outage.
9. **Hub does not redial the remote. It relies on the gateway client to reconnect.** gRPC clients reconnect on their own, and the R8 test (restart the remote on the same address) catches a client that does not.
10. **A remote not-found is accepted in both forms, the sentinel and gRPC `NotFound`.** How the client maps the remote's status is unverified, and a box that is already gone must count as destroyed in either case.
11. **When the remote refuses hub's token, the caller gets `Unavailable` and the operator gets a log line.** Passing `Unauthenticated` through would tell the caller its lease token is bad, when the fault is in hub's configuration.
12. **The token is scrubbed from all text. The remote URL and host are scrubbed from all text produced after start. Start-up errors may show the URL.** The source says callers never see the provider. The scrubber already exists, so this costs three more values in its set. Start-up errors go only to the operator, who needs the URL to fix the problem.
13. **The start-up info line is unchanged and names only the provider and the profiles.** 25-REQ-8.2 fixes its attributes. The URL is in the operator's own configuration.
14. **The client is closed after 28's shutdown step, and only if it implements `io.Closer`.** Boxes must be destroyed through the client before the client is closed, and `sandbox.Provider` is not known to include `Close`.
15. **The factory seam gains a gateway variant. `gateway.Dial` is what production uses, and `local` can never be selected in configuration.** Tests need to inject a client pointing at the in-process remote, and 25 already keeps the unisolated provider out of configuration.
16. **The remote's `MaxSandboxes` and `MaxTTL` are not checked at start unless its capabilities report them.** Hub cannot read them through the assumed API. When they are exceeded, `Create` fails and the lease records it as `failed` with the error scrubbed, which is a visible, recorded outcome.
17. **Changing `gateway_token` leaves boxes made under the old token to the remote's TTL.** The remote ties each box to the token that created it, so hub cannot see those boxes any more. The TTL hub passes bounds them, and `docs/configuration.md` says so.
18. **No new ADR.** The decision that hub fronts a remote gateway belongs to ADR 03 "hub is the sandbox gateway", which 27 writes. This spec adds a paragraph there only if the paragraph is missing.
19. **Tests run the remote as `gatewayd.New` with a static token, over a recording wrapper around `local`, with hub's own gateway on a second `127.0.0.1:0` port.** This is how the source tests the remote case, and the recording wrapper shows the exact spec that reached the remote's backend.
20. **No test covers TLS to the remote. Validation covers the `https` rule.** Go loads the system trust store once per process, so a test cannot reliably trust a self-signed remote certificate. The client's TLS behaviour is AgentKit's to test.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `25_sandbox_profiles` | depends on, modifies | Replaces the refusal in 25-REQ-2.2. Adds `gateway_url` and `gateway_token` to `Config`, the recognised keys and `Validate`, plus the rule that refuses the other provider's keys. Extends the Podman-only `ProviderFactory` seam with a gateway variant. Reuses the probe and capability checks (25-REQ-5.1 to 5.7) with remote-specific field names in the errors. TS-25-9 and TS-25-10 change. |
| `26_sandbox_leases` | depends on | The no-leak tests inspect its lease representation. This spec makes no change to it. |
| `27_sandbox_gateway` | depends on, modifies | Its backend wraps the remote client. Its scrub set gains the token, the URL and the host. Its not-found handling also accepts a gRPC `NotFound`. A remote `Unauthenticated` or `PermissionDenied` reaches callers as `Unavailable`. Reuses its loopback rule and its in-process gatewayd test harness. |
| `28_sandbox_expiry` | depends on | Its sweep and shutdown step destroy boxes through the remote client, and hub closes that client after the shutdown step. Its not-found handling accepts the remote's form. |
| agentkit-go `sandbox/gateway` and `sandbox/gatewayd` (external) | depends on | `gateway.Dial` and `gateway.Options` in production. `gatewayd.New`, `Config.Tokens` and `local` for the in-process remote in tests. |

## Verified External API

agentkit-go is not vendored and lies outside the repository. `25_sandbox_profiles` and `27_sandbox_gateway` add it to `go.mod` as a sibling `replace => ../agentkit-go`. No agentkit-go symbol could be read. Every row below comes from the source PRD and the earlier specs, and is **unverified**.

| Symbol | Assumed shape | Status |
|---|---|---|
| `gateway.Dial` | `func Dial(url string, opts gateway.Options) (sandbox.Provider, error)`, accepting `https://host[:port]` and, on loopback, `http://host:port`. Assumed not to block: the connection is made lazily, and the client reconnects after the remote restarts. | unverified |
| `gateway.Options` | `struct { Token string; ... }`, where the token is sent as the bearer credential on every RPC | unverified |
| value returned by `gateway.Dial` | may implement `io.Closer`, checked with a type assertion | unverified |
| `sandbox.Provider` | `Create(ctx, Spec) (Sandbox, error)`, `List(ctx) ([]Sandbox, error)`, `Destroy(ctx, id string) error`, `Capabilities() Capabilities`, `Reap(ctx) …` | unverified |
| `sandbox.Capabilities` | struct with `NetworkModes []string`, possibly with TTL or resource ceilings that `CheckSpec` uses | unverified |
| `sandbox.CheckSpec` | `func CheckSpec(Spec, Capabilities) error` | unverified |
| `sandbox.ErrNotFound` | not-found sentinel, tested with `errors.Is` (as 27 assumes) | unverified |
| `gatewayd.New` / `gatewayd.Config` (tests) | `func New(Config) (*Server, error)`; `Config{Backend sandbox.Provider; Tokens …; TLS *tls.Config; NetworkModes []string; MaxSandboxes int; MaxTTL time.Duration; Authenticate …}`. Static `Tokens` stays available for the remote under test. | unverified |
| `(*gatewayd.Server).Serve` / `Shutdown` (tests) | `Serve(net.Listener) error`, `Shutdown(ctx) error` | unverified |
| `local.Unisolated` (tests) | constructor for an unisolated `sandbox.Provider` | unverified |

The gRPC status API is `google.golang.org/grpc/status.Code(err error) codes.Code`, with `codes.NotFound`, `codes.Unauthenticated`, `codes.PermissionDenied` and `codes.Unavailable`. This is grpc-go's stable public API. It is not in `go.mod` yet and arrives with `27_sandbox_gateway`'s gatewayd dependency.

From the standard library: `net/url.Parse`, `(*url.URL).Hostname`/`Port`/`Host`/`User`, `net.ParseIP(...).IsLoopback()`, `io.Closer`, `log/slog.LogValuer`, `fmt.Formatter`/`fmt.GoStringer`, `encoding/json.Marshaler`.
