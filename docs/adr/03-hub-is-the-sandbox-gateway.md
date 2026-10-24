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
  codes; see the divergences below.
- **The provider may be a remote gateway, which makes the hub a gateway over a
  gateway.** With `provider = "gateway"` the hub's backend is a client of an
  `agentkit-gateway` elsewhere, under one token the hub holds, and the hub's own
  gateway still stands in front of it. Callers see the same API, leases and
  guarantees and are kept from the remote, its address and its token: the token,
  the `gateway_url` and its host are scrubbed from the text of every provider error
  the hub stores, logs, emits or returns, and from the capability notes it reports,
  the hub dials the remote itself so that a failed call says why and not where, and
  a lease's `gateway_url` is the hub's own. What it cannot scrub is a TLS failure on
  `https` in a call gatewayd relays to the sandbox, which names the host. The cost is that the hub is a single point
  of the remote's credential, that every lease shares that credential on the remote
  (the remote cannot tell leases apart), and that the remote's limits and outages
  show up as failed leases and `502`s rather than as anything a caller could act on
  (`docs/configuration.md`, `docs/architecture.md`). The relay between the two
  gateways needs `agentkit-go` to pass the sandbox id on.
- **`afc` is unchanged and stays static.** The gRPC stack comes in with the
  gateway; neither `cmd/afc` nor `internal/cli` imports it, and a test pins that.

## Divergences from the specs

The sandbox specs were written before AgentKit's provider could be read and
assumed a provider with `List`, `Destroy` and a not-found sentinel. The code
follows AgentKit as it is, and the rules below are what it does instead.

**The provider.**

- `sandbox.Provider` has `Capabilities`, `Create` and `Reap`; `Sandbox.Close`
  takes a context. The start-up probe is one `Reap` under a 10-second limit,
  which also removes leftovers whose expiry label has passed and fails the
  start when a removal fails. A fractional `cpus` is accepted (the field is a
  float).
- The sweep, restart recovery and shutdown reach boxes through a `Boxes`
  interface (`List`, `Destroy`) that the gateway implements over the handles
  the hub holds; a box of an earlier process is out of reach and lives until
  the TTL the provider enforces. `gatewayd.ErrNotHeld` is the not-found.
- `Backend.Reap` runs the sweep when called without a lease principal (the
  gateway's periodic call, every `reap_interval`) and does nothing for a
  lease holder; the sweep logs each failing step with the provider's text
  scrubbed.

**Leases.**

- `ttl` and the clock are cut to whole seconds, so `expires_at` minus
  `created_at` equals `ttl_seconds`. A lease with less than a second left
  counts as expired. More than 16 labels are refused naming the lowest key
  past the sixteenth. The issue body is decoded into a map first so that field
  names match exactly; an unreadable body is 400 (413 over the server limit).
- The org tier of the secret resolution chain requires the workspace owner's
  membership whoever the caller is, so an admin token and the owner see the
  same scopes.
- The lease records the whole profile (fixed environment, optional secrets,
  landlock, resources), and `Create` builds the spec from the lease, looking
  the profile up only to see that it still exists. Fixed `env` values are in
  the database; only secret values are kept out of it.
- `lease_expires_at` is in the `CREATE TABLE` and added to an older table by
  a migration. `lease_timeout` and `max_ttl` are `*string` so that an empty
  value (an unset `${NAME}`) is refused rather than taken for absent; the
  lease deadline is `min(created_at + lease_timeout, expires_at)`.

**The gateway.**

- gatewayd hands a backend error that is no spec refusal to the client as
  `Unavailable` with the backend's text; the backend itself returns
  `FailedPrecondition`, `Internal`, `NotFound` and `Unauthenticated`. The
  hub makes a refused remote token `Unavailable` too, and logs the one error
  that names `sandbox.gateway_token`. gatewayd's own logger is not set.
- `Close` is also what gatewayd calls at a box's TTL, when pruning, at
  shutdown and for `Destroy`, so: `DELETE` marks the lease as ended by the API
  while it destroys the box; a `Close` in the last two seconds of the lease,
  or after it, changes nothing; any other `Close` is the client's.
- The backend keeps the handle of every box it made until it is closed, so a
  retried `DELETE` after a provider failure still reaches the box, and a
  `DELETE` that races the registration of a new box still destroys it.
- A `Serve` that returns nil is an orderly stop; only an error ends the
  process. Recovery and the first sweep run before the listener accepts.
- Reserved labels (`agentkit.sandbox` and everything under
  `agentkit.sandbox.`) are dropped with the `hub.*` keys. A box the lease
  store cannot record is destroyed and the lease stays `leased`. The handle
  keeps the secret and fixed environment values while its box lives, to scrub
  errors; the scrubber repeats until the text is stable.
- Any provider error at `Create` fails the one-shot lease, a context that
  ends during a slow `Create` included. A failure to record a client's
  destroy is logged and not returned.

**Expiry.**

- The expiry path ends the lease first and retries the box on later sweeps;
  while a remote is down the lease reads `destroyed`/`expired`, its token is
  refused, and the box is destroyed by a sweep after the remote is back.
- An ending whose context has ended (the shutdown limit) does not complete,
  and the lease is left for the next start. The lifecycle (`RecoverActive`,
  `StartReaper`, `RunStartup`, `Shutdown`) is on the `Sweeper`, driven by
  `Gateway.Run` and `Gateway.Shutdown`; the `Service` is read-only after start
  except for the shutdown flag.
- Metrics are recorded by the one statement that ends a lease and by
  `MarkActive`. `afhub_sandboxes_active` keeps its `workspace=""` placeholder
  while no lease is active so the metric is listed from start.

**The remote gateway provider.**

- `gateway.Dial(ctx, url, Options)` connects and reads capabilities; the
  factory and the probe share one 10-second limit. The factory is a
  `StartOption` (`WithGatewayFactory`), `DialGateway` by default. The
  capabilities of a remote are read once and served from the snapshot; a
  local provider is asked every time.
- Not-found is gatewayd's `ErrNotHeld` or a gRPC `NotFound`, applied in
  `closeProviderBox` where every destroy ends. After a failed destroy a retry
  goes through the client's `Destroy` by id, since an attached sandbox's
  `Close` answers nil after the first.
- Every site that turns provider text into output scrubs the remote's token,
  URL, host and bare host name; the capability notes are scrubbed once at
  start; the hub dials the remote through a dialer whose failures say why and
  not where. A TLS failure in a call gatewayd relays may still name the host.
- `Service.Close` runs last in `Gateway.Shutdown`, bounded by the shutdown
  limit. The relay between two gateways needed agentkit-go to pass the
  sandbox id on (agentkit-go pull request 150).

**Process and CLI.**

- A bootstrap run (`af-hub --admin-email`) writes the admin token and exits 1
  through `log.Fatal`; the sandbox section is validated after the bootstrap,
  so an invalid section never hides the token. `afc sandbox` reports every
  error as the JSON envelope on stdout, like every `afc` command, without the
  server's `error_type`.

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
