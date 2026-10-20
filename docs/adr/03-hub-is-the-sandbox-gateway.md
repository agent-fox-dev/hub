# ADR 03: Hub is the sandbox gateway

- **Status:** Accepted
- **Date:** 2026-10-20

## Context

A caller of the hub needs somewhere to run an agent, with the credentials the
agent needs and no more. The hub already holds those credentials (the secrets a
team stores at user, org and workspace scope) and already decides who may act on
a workspace. What it did not have was a place to run, and AgentKit has one:
`sandbox.Provider` creates an isolated sandbox on Podman, Docker or a remote
gateway, and `gatewayd.Server` is a gRPC server that lets authenticated clients
create, list and destroy sandboxes on a provider and relays their calls to them.
Its client, `gateway.Dial`, gives a caller a `sandbox.Provider` it uses exactly
as it would use one it created itself.

Two earlier specs gave the hub the sandbox definition (profiles) and the lease
(a caller's right to one sandbox, with a token). A lease is worth nothing until
something accepts its token and makes the sandbox. That something can be:

1. **A separate `agentkit-gateway` process** next to the hub, with the lease
   tokens handed to it.
2. **The caller itself**, which creates the sandbox with the provider's
   credentials and the secrets the hub sends it.
3. **A gateway inside the hub**, on a second listener, that checks the lease and
   creates the sandbox itself.

AgentKit's gateway can authenticate with a callback since the agentkit-go
authenticator change (`gatewayd.Config.Authenticate`), which names the principal
that owns the sandboxes a token creates, and `gatewayd.Server.Destroy`, which
lets the embedder end any sandbox the server holds.

## Decision

1. **The hub embeds `gatewayd.Server` and serves it on a second listener**
   (`[sandbox].listen`, default `127.0.0.1:8443`). It starts after the lease table
   exists and before the HTTP server serves, and a failure to load the key pair or
   to bind the address stops the start. If `Serve` returns an error the process
   exits, since leases would otherwise be issued that nobody can use.

2. **The gateway accepts a lease token and nothing else.** There are no static
   tokens. The authenticator looks the token's SHA-256 up in the lease table on
   every RPC and keeps nothing, so a lease that ends is refused on the next call.
   The principal is the lease id, so gatewayd already scopes `List` and `Destroy`
   to the lease.

3. **The hub's own `sandbox.Provider` wraps the real one.** Its `Create` builds
   the spec from the lease and never from the caller's, reads the secrets from the
   workspace's scopes at that moment, and moves the lease from `leased` to
   `active`, or to `failed`, with an audit event for each. Only one `Create` runs
   per lease, and every state change is a conditional update, so a lease yields at
   most one sandbox even when calls race and a `DELETE` arrives in between.

4. **The lease is one-shot.** A token creates one sandbox. The first `Create`
   takes the lease out of `leased`; a second one is refused, and a lease that has
   ended or failed is not authenticated at all.

5. **`DELETE` destroys the sandbox first.** An `active` lease is moved to
   `destroyed` only after the provider destroyed its sandbox, so the lease never
   reads `destroyed` while the sandbox runs. A failing provider answers 502 and
   changes nothing, which keeps the call retryable.

6. **Nothing secret leaves the hub.** The spec the gateway reports back has every
   environment value redacted, and provider error text is scrubbed of secret and
   fixed environment values and of URL user information, then truncated, before it
   is stored, logged, put in an event or returned.

## Consequences

- **A second listener, and a second port to publish.** The gateway is gRPC. The
  Kubernetes manifests expose the REST port only, and reaching the gateway from
  outside the pod is a deployment decision this change leaves to the operator. A
  plaintext gateway is allowed only on loopback, so a deployment that publishes it
  needs a certificate and an `external_url`. A certificate is read once, at start.
- **The hub holds the provider's credentials and the secrets.** The container
  engine socket or credentials sit with the process that also serves the REST API.
  This is the price of keeping secrets out of every caller: a secret enters a
  sandbox only through the hub. A compromise of the hub is a compromise of the
  provider, which a separate gateway process would limit.
- **One replica.** The claim that serialises `Create` for a lease is in the
  process, which is enough for the single-replica deployment and is not enough for
  a second one. The SQLite conditional updates would still keep the states
  right, but two replicas would each hold their own gatewayd registry.
- **The hub does not stop the sandboxes when it stops.** This change never calls
  `Shutdown`. A sandbox left behind is bounded by the TTL the provider enforces
  and removed by the reap the gateway runs at start and every ten minutes; ending
  boxes at shutdown and marking leases that run out are left to the expiry work.
  A restarted hub does not hold the boxes of the process before it either, so a
  `DELETE` of a lease that was `active` then ends the lease and cannot reach its
  box; reconciling such leases is also the expiry work.
- **gatewayd answers some failures its own way.** It hands a backend error that is
  no spec refusal to the client as gRPC `Unavailable` with the backend's text, so
  a second `Create` shows as `Unavailable` ("lease is active") and not as
  `FailedPrecondition`, and it checks the caller's TTL and network against the
  ceilings before the backend runs. The hub's backend returns the more exact
  codes, and the divergences are listed in
  `docs/errata/27_sandbox_gateway_divergences.md`.
- **`afc` is unchanged and stays static.** The gRPC stack comes in with the
  gateway; neither `cmd/afc` nor `internal/cli` imports it, and a test pins that.

## Alternatives considered

- **A separate `agentkit-gateway` process.** It isolates the provider's
  credentials from the REST API, which is attractive. It needs the lease tokens to
  reach it, either by a shared store (a second reader of the lease table, and the
  revocation question again) or by registering each token with it, and a second
  process to deploy, secure and keep in step with the hub's profiles. The hub would
  still have to resolve the secrets and put them in the sandbox, so it would send
  them to that process. Embedding trades the isolation for one fewer moving part
  and for revocation that needs no protocol: ending a lease clears one column.
- **The caller creates the sandbox.** The hub would have to give the caller the
  provider's credentials and the secret values, which is exactly what the design
  rule rules out: a secret enters a sandbox through the hub and never through a
  caller.
- **Cache the token lookups.** gatewayd asks for an authenticator that does not
  block and suggests an in-memory view. An indexed SQLite read costs microseconds,
  and a cache would delay revocation, which the lease promises takes effect on the
  next call. The authenticator reads the store every time.
