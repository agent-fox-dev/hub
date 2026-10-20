# Errata: Spec 27 — Sandbox Gateway Divergences

## Spec Expectation

Spec 27 (`27_sandbox_gateway`) was written before specs 25 and 26 were merged
and before the AgentKit gateway authenticator existed, and it marks every
AgentKit symbol it names as unverified. It assumes a `sandbox.Provider` with
`List` and `Destroy`, a provider "not found" sentinel, a `Close()` without a
context, gatewayd answering a backend's `FailedPrecondition` with that code, a
lease that records only part of its profile, and a client that makes
`Create(sandbox.Spec{})` work. The entries below record where the code that
landed differs, with the code and the test of the delivered behaviour. None of
them weakens a goal of the spec: a lease still gets one sandbox through hub's
gateway, built from the lease, with the secrets resolved by hub, and nothing
secret leaves hub.

## Implementation Reality

1. **`gatewayd` is a module of its own.** The spec expects the module that
   provides `sandbox/gatewayd` to be, perhaps, the sandbox module of spec 25.
   It is a third module, so `go.mod` requires
   `github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd` and replaces it with
   `../agentkit-go/sandbox/gatewayd`. `go mod tidy` drops an unused requirement,
   so `internal/sandboxes/imports_test.go` pins the gatewayd surface hub relies on
   (`Authenticator`, `Config.Authenticate`, `PrincipalFromContext`, `Serve`,
   `Destroy`, `Shutdown`, `ErrNotHeld`) at compile time, which also keeps the
   requirement. Both symbols the spec lists as "NOT FOUND" exist in agentkit-go.
   `Config.NetworkModes` is `[]sandbox.NetworkMode`, not strings: `gateway.go`
   (`gatewayNetworkModes`) converts through the provider's own list.

2. **`sandbox.Provider` has no `List`, no `Destroy` and no not-found sentinel.**
   It has `Capabilities`, `Create` and `Reap`, and `Sandbox.Close` takes a
   context. The wrapping backend implements those three methods
   (`backend.go`: `Capabilities`, `Reap`, `Create`). gatewayd answers `List` and a client's
   `Destroy` itself, from the sandboxes it holds for each principal, which is the
   lease; so the criteria that name a provider `List` or `Destroy` are verified
   through the real client over a real in-process gateway, and "the provider's
   `List`" in the tests is the recording provider's own record of live boxes:
   - 27-REQ-7.2 (`List` returns only the principal's box): gatewayd does it by
     owner, and `TestGateway_ListsOnlyTheLeasesBox_TS_27_42` checks it through
     `gateway.Provider.List`. "Without a principal the provider's list" has no
     meaning, as the backend has no `List`.
   - 27-REQ-7.4 (a `Destroy` of an id the lease does not own): gatewayd answers it
     like an id it never held, with `nil`, and touches nothing.
     `TestGateway_DestroyOfAnotherLeasesBox_TS_27_44` checks that the other box
     and both leases are unchanged. The backend has no `Destroy(id)` to add a
     second check to.
   - 27-REQ-7.3, 7.5, 7.6 (the client destroy path): the path is the handle's
     `Close(ctx)` (`handle.go:70`), which gatewayd calls. A provider "not found"
     does not exist; a box already gone at the provider closes without error
     (`TestGateway_CloseDestroysTheBox_TS_27_43` closes a box at the provider
     first).
   - 27-REQ-8.1 to 8.3 (`DELETE`): the provider "Destroy" is
     `gatewayd.Server.Destroy`, the embedder's call, with the hub-held handle as
     the fallback (entry 8). `gatewayd.ErrNotHeld` is the "not found" of this path.

3. **A lease already records the whole profile, and `end_reason` is the spec's
   `destroy_reason`.** Spec 26 snapshots the fixed environment, the optional
   secrets, `require_landlock` and every resource, and created the `error` and
   `end_reason` columns and fields. The spec's `ALTER TABLE` migration is therefore
   not needed and was not written, and `buildSpec` (`create_spec.go:27`) reads
   every field from the lease; the in-memory profile is only looked up to see that
   it still exists (27-REQ-4.5, `backend.go:175`). TS-27-25's "a resource field
   the lease lacks" cannot occur; `TestBackendCreate_BuildsHubsSpec_TS_27_25` asserts
   the lease's values, and `TestBackendCreate_TheLeaseWinsOverTheEditedProfile`
   restarts the hub with every copied profile field edited to show that they win. Design decision 12 ("fixed `env` values
   are never written to the database") does not hold: spec 26 stores them for the
   gateway and shows none of them in a response. The values the scrubber removes
   from error text are therefore the lease's fixed environment and the resolved
   secrets, and only the secrets are kept out of the database
   (`TestBackendCreate_NoSecretInTheDatabase_TS_27_34`).

4. **gatewayd hands the client `Unavailable`, not the backend's code.** For a
   backend `Create` error that is no spec refusal gatewayd answers gRPC
   `Unavailable` with `gatewayd: backend create: <message>`. The backend returns
   `FailedPrecondition`, `Internal`, `NotFound` and `Unauthenticated` through
   `GRPCStatus()` with a plain message (`statusError`, `backend.go:26`), and the tests of task 5
   and 6 assert those codes on the backend. Over the wire the tests assert
   `Unavailable` and the message: a second `Create` under an active lease
   (`TestGateway_SecondCreateIsRefused_TS_27_39`), a missing secret
   (`TestSmoke_SecretDeletedAfterPost_PATH_2_TS_27_62`). `Unauthenticated` is
   delivered as specified (`TestGateway_RevokedAndUnknownTokens_TS_27_18`). A store
   error that the spec wants as `Internal` reaches the client as `Unavailable`
   with the message `internal error`.

5. **`Create(sandbox.Spec{})` needs an image in a development build.** The spec's
   goal 1 and TS-27-61 call `Create(sandbox.Spec{})`. The AgentKit client fills an
   empty image with `sandbox.DefaultImage()` before it sends anything, which has
   an answer only for a released version and otherwise fails with
   `sandbox: no published image for this agentkit-go version ... set Spec.Image`.
   The tests therefore pass `sandbox.Spec{Image: "ignored"}` (`createFor`,
   `smoke_gateway_test.go`); hub ignores the image. The client and gatewayd also
   check the caller's TTL (one hour when empty) against `max_ttl` and its network
   mode (`none` when empty) against `network_modes` before the backend runs, so
   with `max_ttl` under `1h`, or `network_modes` without `"none"`, a client has to
   send a TTL and a network that fit even though hub reads neither. This is in
   `docs/configuration.md`. `gateway.Dial` also takes a context first:
   `Dial(ctx, url, Options)`.

6. **TS-27-18 cannot have an unknown token call `Create` through `Dial`.** `Dial`
   itself calls `Capabilities` with the token and is refused with
   `ErrUnauthenticated`. `Create` with an unknown token is made over a raw gRPC
   connection (`rawCall`, `gateway_grpc_test.go`), which gets `Unauthenticated`
   from gatewayd's tap before a message is read.

7. **`Close` is also what gatewayd calls at a sandbox's TTL, when it prunes a box
   that is no longer live, at shutdown and for the embedder's `Destroy`.** gatewayd has one path to the backend's sandbox, so
   the handle's `Close` cannot tell a client's destroy from the others by its
   arguments. Three rules keep the record true (`handle.go:70`, `:147`):
   - `DELETE` marks the lease as being ended by the API while it destroys the box
     (`endBox`), so the `Close` it causes only closes, and `DELETE` records the end
     with the reason `api` and its own actor
     (`TestDelete_ActiveLeaseDestroysTheBox_TS_27_48`).
   - A `Close` in the last two seconds of the lease's time, or after it, closes the
     box and changes nothing else: marking expiry is a later spec's job, as
     27-REQ-4.4 says of `Create`. The margin is there because the box's TTL is the
     time left cut down to whole seconds, so gatewayd closes a box at its TTL up to a
     second before `expires_at`; without it every natural expiry was recorded as a
     client destroy (`TestHandleClose_AtTheBoxsOwnTTLChangesNothing`,
     `TestHandleClose_AfterExpiryChangesNothing`).
   - Any other `Close` is the client's. A client that closes its box in the last two
     seconds of its lease leaves the lease `active` with no box until the expiry
     sweep ends it. A box that died on its own is pruned by gatewayd the next time
     it lists, relays to or reserves for that box's principal, through the same
     `Close`, and the lease is then recorded as destroyed by the client with one
     destroy event; the backend cannot tell it from a holder's `Close`.

8. **`DELETE` keeps the handle of every box until it is closed.** gatewayd removes
   an entry before it closes the box and keeps it removed when `Close` fails, so a
   retried `DELETE` would find `ErrNotHeld` and call the box destroyed while it
   runs. The backend keeps the handle of every box it made in a map by lease id
   (`backend.go:91`, `boxes`, held by `remember`, `backend.go:282`), and `endBox` (`handle.go:111`) closes it when the
   gateway no longer holds the box. When hub holds none, the box is taken to be gone.
   That is not true of a box made by a process that has since been restarted: the
   gateway and the map are gone with it, so a `DELETE` of a lease that was `active`
   before the restart ends the lease and leaves the box to the TTL the provider
   enforces. Reconciling such leases is the expiry spec's. The same map covers the
   window in which a lease is `active` but gatewayd has not yet recorded the box:
   the handle is held before the lease is marked `active`, so a `DELETE` that
   arrives while `Create` emits its create event still destroys the box
   (`TestGateway_DeleteBetweenActiveAndRegistered`; it failed, with a live box on a
   destroyed lease, before the handle was held that early). `TestDelete_ProviderFailureIsRetryable_TS_27_50` exercises it:
   its first failure removes the gateway's entry, and the retries go through the
   fallback.

9. **A Serve that returns `nil` is not a failure.** 27-REQ-2.4 ends the process when
   `Serve` returns. gatewayd returns `nil` only after `Shutdown`, which a later
   spec calls, so `nil` is an orderly stop (`gateway.go:138`) and only an error is
   logged and ends the process (`TestStartGateway_ServeReturns_TS_27_12`).

10. **The planned file names differ.** The tasks name `lease_store.go`, which does
    not exist: the lease store is `lease.go`, `store_query.go` and the new
    `lease_transitions.go`. Spec 26's guard that only `EndLease` clears a token hash
    stays true because `EndLease`, `FailLease` and `DestroyLease` share the one
    statement `endLease` (`lease.go:241`). `events.go` is created in task 5, not
    task 10, because TS-27-24 asserts the failed event, and `handle.go` arrives in
    task 7, where the handle first differs from the provider's box.

11. **Behaviours the spec leaves open.**
    - A lease with less than one second left counts as expired: its TTL cut to whole
      seconds would be zero, which a provider reads as an hour (`backend.go`, `creatable`;
      `TestBackendCreate_Expired_TS_27_23`).
    - The caller's label `agentkit.sandbox` is dropped with the `hub.*` keys: the
      provider refuses a spec that has it, which would let a caller fail its own
      lease (`create_spec.go`, `providerLabel`).
    - A lease whose network mode the provider no longer honours, or whose memory or
      disk size does not fit in bytes, fails with a message that names it, as a
      missing profile does (`TestBackendCreate_NetworkNotHonoured`).
    - A box the lease store cannot record is destroyed and the lease stays `leased`
      (`TestBackendCreate_RecordingFailureDestroysTheBox`); the box of a lease that
      a `DELETE` ended is destroyed with a warning that names both ids.
    - The handle keeps the secret values and the fixed environment values for as
      long as its box lives, to scrub the errors of `Close` and `DELETE`, where
      27-REQ-5.7 says "for the duration of the provider call". They are in the box's
      environment all that time, and are written nowhere.
    - The scrubber repeats until the text is stable, because taking out URL user
      information can join the text around it into a value that was not there, and a
      value that is part of the marker `[redacted]` (a single `e`, say) is replaced
      once and cannot be kept out of the output; the property test draws values
      from an alphabet without the marker's characters (`scrub.go:38`).

12. **Tests of specs 25 and 26 were changed.** Each is the same property restated
    for the new behaviour, with a mutant that still fails it:
    - `TestCheckRequiredSecrets_NamesOnly_TS_26_24` scanned every production file for
      `GetSecretValue`; the gateway reads values by design in `secret_resolve.go`,
      so the scan skips that one file and fails for any other.
    - `TestRender_ExactFields_TS_26_47` lists `gateway_url` among the fields of a
      lease (27-REQ-2.7).
    - `TestAudit_LeaseEvent_TS_26_53` expected one event after an issue and a
      `DELETE`; it counts `hub.sandbox.lease` events, one, and expects the
      `DELETE`'s `hub.sandbox.destroy` next to it (27-REQ-8.6).
    - The real `af-hub` binary tests give the `[sandbox]` section the address
      `127.0.0.1:0` (`newAfHubEnv`), since the gateway's default `127.0.0.1:8443`
      may be held by another process.

13. **TS-27-65 uses a free port, not 8443.** The unsafe configuration is
    `listen = "0.0.0.0:<port>"` with no TLS and no `external_url`; the test picks a
    free port so that "nothing is bound" can be checked without depending on
    whether 8443 is in use (`TestSmoke_UnsafeGatewayRefusesStart_PATH_5_TS_27_65`,
    and through the real binary in `TestAfHubBinary_UnsafeGatewayRefusesStart_PATH_5`).

14. **gatewayd's logger is not set.** 27-REQ-2.1 lists the fields of the gatewayd
    configuration and `Logger` is not among them, so gatewayd's own log lines
    (create, destroy, refused authentication, reap) are discarded. Hub logs its own
    events (`the sandbox gateway is serving`, the errors listed in `backend.go`
    and `gateway.go`); a refused authentication is not logged by hub, which the
    spec's "never logs a token or its hash" makes the safe default.

15. **`Reap` by a lease holder does nothing.** gatewayd answers a `Reap` call that a
    client makes with its token by running the backend's `Reap` with the lease as
    the principal and returning the ids and the error text to the caller. Passed
    straight to the provider (27-REQ-7.7) that would hand a lease holder the ids of
    other leases' sandboxes and the provider's unscrubbed error text (27-REQ-9.3,
    9.7), and let it make the provider work at will. `Backend.Reap`
    (`backend.go:355`) does nothing when the context carries a principal, and goes
    to the provider, with the error text scrubbed, when it does not, which is the
    gateway's own periodic call (`TestBackendReap_ScrubsTheProviderError`,
    `TestGateway_ReapByALeaseHolderDoesNothing`).

16. **Reserved labels.** The provider refuses every label under `agentkit.sandbox.`
    as well as the key `agentkit.sandbox`; `boxLabels` drops both
    (`create_spec.go`, `TestBackendCreate_DropsTheProvidersReservedLabels`). The
    entry on labels above (11) is this one's first half.

17. **Left as the spec has it, with the consequence.**
    - Any provider error at `Create` fails the lease (27-REQ-6.3), so a client whose
      context ends during a slow `Create`, or that sends a working directory the
      provider refuses (it wants an absolute, clean path outside `/`, `/tmp`,
      `/proc`, `/sys` and `/dev`), burns its one-shot lease. The error is returned as
      plain text, so gatewayd answers `Unavailable` and not the refusal a client could
      correct.
    - A failure to record the end of a lease after the box was destroyed
      (`endByClient`) is logged and not returned: the box is gone, the client cannot
      retry, and the lease stays `active` until a `DELETE` or the expiry sweep.
    - A workspace that no longer exists makes the secret lookup fail with an internal
      error on every attempt, and the lease stays `leased`.
    - The scrubber replaces exact substrings; a value an engine echoes quoted,
      escaped or encoded is not found.
    - The authenticator reads SQLite inside gRPC's tap handle, which gatewayd asks to
      stay quick (design decision 8).
    - An `http` `external_url` for a host that is not loopback validates (27-REQ-1.7)
      and is refused by the AgentKit client, which dials plaintext only for
      loopback.
