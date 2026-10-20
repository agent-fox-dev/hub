# Errata: Spec 29 — Remote Gateway Provider Divergences

## Spec Expectation

Spec 29 (`29_sandbox_gateway_provider`) was written before specs 25 to 28 landed
and says that no AgentKit symbol could be read. It assumes a lazy
`gateway.Dial(url, opts)` that returns a `sandbox.Provider`; a provider with
`List`, `Destroy(id)` and a not-found sentinel; a backend that a recording remote
sees `List` calls on; a "capability snapshot" wrapper around the provider; one
call-site that scrubs provider text; a sweep that leaves a lease `active` while the
remote is down; and a gateway over a gateway that works with AgentKit unchanged.
The entries below record where the code that landed differs, with the code and the
test of the delivered behaviour. Entries 9 and 10 are limits of AgentKit and say what
the hub could and could not do about them.

## Implementation Reality

1. **`gateway.Dial` takes a context and connects.** It is `Dial(ctx, url, Options)
   (*Provider, error)`: it checks the URL and the token, connects, and reads the
   remote's capabilities under `ctx` before it returns. `GatewayFactory` therefore
   takes the context, and `startRemote` (`service.go:83`) gives the factory and the
   probe one `probeTimeout` of 10 seconds between them, which is tighter than a
   separate 10 seconds for each. The production factory is `DialGateway`
   (`provider.go:55`). A remote that accepts the connection and never answers fails
   the start at 10 seconds (`TestStart_RemoteThatNeverAnswersFailsAtTheLimit_TS_29_22`,
   `TestStart_DialThatNeverReturnsFailsAtTheLimit`).

2. **The probe is the client's `List`, which the remote's backend never sees.**
   `sandbox.Provider` has `Capabilities`, `Create` and `Reap` only, and no not-found
   sentinel; `*gateway.Provider` also has `List`, an idempotent `Destroy(id)` and
   `Close`, which `RemoteClient` names (`provider.go`). gatewayd answers `List` from
   its own registry, so a recording backend cannot count it. TS-29-19 counts the
   `List` at the factory seam (`remoteFactory`, `remote_harness_test.go`), the
   remote's RPCs in its authenticator (Dial's capabilities and the `List`: two) and
   the backend's Creates, Reaps and closes (none)
   (`TestInit_ProbeIsOneListAndCreatesNothing_TS_29_19`).

3. **The seam is a `StartOption`.** `Start` and `Init` keep their `ProviderFactory`
   parameter and take `WithGatewayFactory(f)` as an option, with `DialGateway` the
   default, so that no existing caller and no Podman path changes (`provider.go`;
   `TestStart_GatewayFactoryReceivesURLAndToken_TS_29_18`,
   `TestStart_StaticFailureBuildsNothing_TS_29_20`,
   `TestValidate_UnknownProviderListsBoth_TS_29_1`, which counts the Podman factory
   and TS-29-20 the gateway factory).

4. **The capability snapshot is on the `Service`, for the remote only.** There is no
   wrapper type: `Service.Capabilities` (`service.go:322`) returns a copy of the
   value `startRemote` read once, and the backend and the gateway use it, so
   `Service.Provider()` stays the remote client (TS-29-2) and its `Destroy` and
   `Close` stay reachable. A provider that is not remote is asked every time, since
   spec 27's `TestBackendCreate_NetworkNotHonoured` changes a Podman provider's
   capabilities after start and expects the backend to follow
   (`TestStart_CapabilitiesAreReadOnce_TS_29_23`,
   `TestBackendCapabilities_ComeFromTheSnapshotWhileTheRemoteIsDown_TS_29_26`).

5. **A remote not-found is gatewayd's `ErrNotHeld` or a gRPC `NotFound`.** AgentKit
   has no sentinel, and the real client's `Destroy` answers OK for an id the remote
   does not have, so a not-found is rare; `isNotFound` (`errors_remote.go:18`)
   accepts the two forms wherever they sit in the chain. Hub destroys a box through
   its handle, not through a provider `Destroy(id)`, and every destroy (the caller's
   `Close`, `DELETE`, the sweep, the shutdown) ends in `closeProviderBox`
   (`handle.go:88`), which is where it is applied; `delete_destroy.go` and the
   sweeper needed no change (`TestRemoteNotFoundCountsAsDestroyed_TS_29_31`).

6. **A refused token is already `Unavailable` at the client; the hub says so
   itself.** The real client reports an `Unauthenticated` status as an error that
   matches `gateway.ErrUnauthenticated`, and wraps `PermissionDenied` as a status;
   `isRemoteRefusal` (`errors_remote.go:25`) takes both. gatewayd hands any backend
   error that is no refusal to the caller as `Unavailable`, so `Service.remoteFailure`
   (`errors_remote.go:45`) makes the code hold for the backend itself and logs the one
   error that names `sandbox.gateway_token`. For the same reason a second `Create`
   is `FailedPrecondition` at the backend and `Unavailable` with the message through
   the client, and a token hub never minted fails at `gateway.Dial` already
   (`TestRemoteRefusalReachesTheCallerAsUnavailable_TS_29_32`,
   `TestRemoteLease_RefusalsDoNotReachTheRemote_TS_29_29`).

7. **There is no single scrub call-site.** Nine sites turn provider text into
   output; each takes `Service.scrubValues` (`service.go:337`): the token, the
   `gateway_url` string and its host. `TestRemoteTextIsScrubbedAtEverySite` and
   `TestSweepAndDeleteScrubTheRemoteValuesThemselves` put a token-bearing error
   through all of them. A `Reap` failure belongs to no lease, so only the remote's
   values are scrubbed from it.

8. **While the remote is down, the sweep ends the lease and retries the box.**
   29-REQ-9.5 and TS-29-43 say the lease stays `active` until the remote is back.
   Spec 28's expiry path, tested and delivered, ends the lease first because
   revoking access must not wait for a provider call, and the orphan step retries
   the box on the same and the next sweeps. The delivered behaviour is the one of
   spec 28: the lease reads `destroyed` with `end_reason` `expired`, its token is
   refused, two error records are logged (the expiry's try and the orphan step's, none
   holding the remote), the hub keeps the box, and a sweep after the remote is back
   destroys it (`TestRemoteDown_SweepLogsAndTriesAgain_TS_29_43`).

9. **A retry of a failed destroy goes through the client's `Destroy` by id.**
   AgentKit's attached sandbox tears down once and its `Close` answers `nil` after
   the first, whatever became of the box. A `DELETE` or a sweep that tried again
   after an outage therefore reported the box destroyed while a remote that kept
   it (a cut network, not a restarted remote) still held it until its TTL.
   `destroyProviderBox` (`handle.go:107`) closes the box once and, after a failure,
   destroys it by id (`TestRemotePartition_TryingAgainDestroysTheBoxOnTheRemote`).
   Podman's sandboxes have the same `Close`; they are not changed here.

10. **The relay needed a change in AgentKit.** A gatewayd whose backend is a gateway
   client relayed every Agent RPC to the next gateway with no metadata, and the
   next gateway answered `the RPC carries no agentkit-sandbox-id`, so no `Create`
   through the hub could complete. The spec lists changes to AgentKit as a
   non-goal; with the user's approval it was fixed in agentkit-go pull request 150
   (the relay sends the backend sandbox's own outgoing metadata). The tests that
   create a box on the remote (TS-29-3, 27 to 30, 33, 44 and 47 to 51) fail with the
   handshake error against `agentkit-go` before that change.

11. **`Service.Close` is called from `Gateway.Shutdown`.** The plan has a
    `shutdown.go` and a deferred close in `main`. Hub's shutdown is
    `Gateway.Shutdown` (`gateway.go`), which main already runs after `server.Start`
    returns and before the databases close, so it calls `Service.Close`
    (`service.go:358`) last, bounded by the shutdown limit (`closeRemote`,
    `gateway.go:307`). `main` passes `WithGatewayFactory(DialGateway)` explicitly
    (`TestShutdown_ClosesTheRemoteClientAfterTheLastDestroy_TS_29_45`).

12. **Tests that differ from the plan.**
    - The in-process remote runs in gatewayd's callback mode and not in static mode,
      so that its authenticator can record every token (TS-29-28). A static token
      must have 32 characters; the hub's own minimum is 16.
    - The tests that wait for a remote that came back poll for up to 20 seconds:
      after a failed attempt the gRPC client waits a second or more before it
      reconnects, and the hub does not redial.
    - The tests of the validation (TS-29-5 to 13, 37), the probe (TS-29-2, 3, 18,
      21, 22) and the scrub (TS-29-34, 35) failed first; TS-29-11, 20, 36 and 38
      passed before the change they guard, as did TS-29-4, 24, 25, 26, 33 and
      TS-29-39 to 42 and 44 (the checks are shared between the providers, the real
      client answers `Capabilities` from its cache, and the remote's outage
      behaviour is that of spec 27 and the client), and TS-29-27 to 30 and
      TS-29-47 to 51 verify what tasks 3 to 9 and spec 27 built. Mutants of the
      production code each fail one of them.
    - Spec 25's `TestValidate_UnsupportedProviders_TS_25_10` asserted that provider
      `gateway` is refused as not supported by this build; it now asserts that a
      valid gateway configuration validates, as the spec says TS-25-9 and TS-25-10
      change.
