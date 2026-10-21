# Errata: spec 32, forge webhook ingress

Where the delivered code differs from `.specs/32_forge_webhook_ingress` (the spec is not
edited), and what the code does instead. Each entry names the code and the test of the
delivered behaviour. The first entries are the ones a reviewer should decide on; the later
ones are details the spec left open.

## Differences a reviewer should look at

### 1. apikit's body size limit is not on the Echo root, so the 1 MiB cap is the only limit

The spec's background says apikit's `max_body_size` is installed with `e.Use` and applies to
every route of the instance, and that the check "runs before the handler". In the installed
apikit (`../apikit/server.go`) the body size limit and the content-type enforcement are
scoped to the API group, "so that non-API routes mounted on Echo()" can use their own sizes
and types. `POST /hooks/forge/:slug` is subject to neither (the comment on `gitBodyPassthrough`
in `internal/gitserver/handlers.go` still describes the old placement; it was left alone).

The ingress therefore refuses what it reads beyond 1 MiB itself, always with
`413` and `error_type: body_too_large` (`readDelivery`, `internal/forge/ingress.go:195`): a
body whose `Content-Length` is over the cap is refused without being read, and any other is
read through a limited reader. A limit installed on the whole instance, as the spec assumes,
is honoured too: apikit's own `413` (no `error_type`) when it knows the length, and the
handler's `body_too_large` when it does not. TS-32-20 runs under no global limit, under the
default 1 MiB and under a raised one (`TestIngressBodyLimit_TS_32_20`). A GitHub delivery's
body is read before its signature is checked, as the spec orders it, so an unauthenticated
caller can make the hub read up to 1 MiB per request.

### 2. `Config.Secrets` is an interface

The spec's task 1 adds `Secrets *secrets.Store` to `forge.Config`. TS-32-4, 6, 10 and 13 need
a store that records its calls and fails on demand, so the field is the interface
`SecretStore` (`internal/forge/config.go:53`) with the four methods the routes use;
`*secrets.Store` satisfies it and `main.go` passes the same `store`. A nil `Secrets` is a
misconfiguration: the webhook routes, the ingress and the `webhook` block of `GET /forge`
answer 500 and log it (`secretsStore`, `internal/forge/webhook_handlers.go:41`;
`TestWebhookRoutes_WithoutASecretsStore`, `TestForgeStatusWebhookFailures_TS_32_13`).

### 3. Spec 30's tests of `GET /forge` changed

32-REQ-2.4 adds a `webhook` block to the body that spec 30 defined. TS-30-26 compared the
whole body and TS-30-29 its end (`"upstream":null}`); both now expect the block after the other
keys and keep every other assertion. The setup of the `afc forge status` smoke test (TS-30-55)
and `newEnv` give the routes a secrets store and the delivery log
(`internal/forge/handlers_forge_test.go`, `internal/cli/forge_smoke_test.go`).

### 4. Deliveries that are not relayed are bound to the workspace's repository too

The spec says to bind "a delivery of a relayed kind" and to record `target` for it. TS-32-24
expects the row of every delivery it posts to have target `origin`, among them one whose event
name is not one hub relays. The code binds every delivery whose payload names the workspace's
origin or upstream and records the target, and keeps the outcome `ignored` (`reduceDelivery`,
`internal/forge/reduce.go:49`). A delivery of a relayed kind about another repository is still
`repository_mismatch` with no target. `TestIngressRecordsARow_TS_32_24`,
`TestGitLabIssueUpdateWithoutChange_TS_32_40` (rows are `ignored` and bound).

### 5. The fields a relayed kind "needs" are its identity, not its description

The spec says a missing field the kind needs makes the delivery `malformed` and lists the
fields in the metadata. The code treats as needed the repository path, the number of an issue,
pull request or merge request (a whole number above zero), the name of a label and the ref of a
push (`need` in `internal/forge/reduce_github.go`, `reduce_gitlab.go`); the title, the URLs,
the branches, the labels and the actor default to empty, because an event with a number and no
title is still something a consumer can act on. `TestGitHubDeliveriesLackingTheirIdentityAreMalformed`,
`TestIngressMalformed_TS_32_29`.

### 6. A redelivered `malformed` delivery is a duplicate

The spec orders the steps as: reduce, then insert, and the insert decides. A `malformed`
delivery is recorded (the spec says so), and its redelivery therefore finds the row and is
answered `202 duplicate`, not `400` again (`TestIngressMalformed_TS_32_29`). The forge stops
retrying a 202.

## Details the spec left open

7. **The events of the webhook routes** (`hub.forge.webhook.set`, `.unset`) have the caller as
   actor, `ResourceType` `webhook`, `ResourceID` the workspace slug and `Action` `set` or
   `unset` (`webhookEvent`, `internal/forge/webhook_handlers.go:187`;
   `TestPutWebhook_EmitsAnEvent_TS_32_8`).
8. **A scope-full error is told by its text.** The store gives the refusal to hold a 101st
   entry no type, so the 400 is chosen by comparing the message built from
   `secrets.MaxEntriesPerScope` (`isScopeFull`, `webhook_handlers.go:174`); every other failure
   is a 500 (`TestPutWebhook_StoreFailures_TS_32_6`).
9. **Repository binding uses `parseRemote`, not `describeRemote`.** `describeRemote` needs the
   caller's request, reads the credential and `FORGE_API_URL` and classifies the forge; the
   spec's own decision 5 keeps all of that off the ingress path (`bindRepository`,
   `reduce.go:114`; `TestIngressBindsToOrigin_TS_32_31`, `TestIngressBindsToUpstream_TS_32_32`).
   The host is not compared, only the path.
10. **An empty stored secret is no secret.** It would accept an empty GitLab token and sign
    with an empty key, so the ingress answers it 404 (`lookupWebhook`, `ingress.go:153`;
    `TestIngressRefusesAnEmptySecret`).
11. **A signature or token header given twice is invalid** (401), even with the secret among
    the values (`tokenMatches`, `signatureMatches`; `TestIngressGitHubSignatureGivenTwice`,
    `TestIngressGitLabToken_TS_32_18`). The GitHub signature must be lowercase hex, as
    TS-32-17 requires.
12. **The 202 answer carries `Content-Length`.** Without it the answer would be chunked and end
    only when the handler returns, after the events, which defeats flushing first
    (`writeOutcome`, `ingress.go:276`; `TestIngressAnswersBeforeEmitting_TS_32_47`).
13. **The delivery list breaks ties by recording order** (`ORDER BY received_at DESC, rowid
    DESC`, `deliveries_handler.go:74`), since `apikit.NowUTC` has whole seconds, and a `since`
    with a fraction of a second starts at the next second (`parseDeliveryQuery`, line 51;
    `TestListDeliveriesTiesNewestRecordedFirst`, `TestListDeliveriesSinceWithAFraction`).
14. **The id of a delivery** may contain the space and every printable ASCII character up to
    `~`, 255 characters at most (`printableASCII`, `ingress.go:241`).
15. **GitLab details.** The labels of a `labeled` or `unlabeled` event are the top-level
    `labels` and, when the payload has none, `changes.labels.current` (`reduce_gitlab.go:105`);
    a label listed twice is one event; `merged` is true for a `merge` action and for a state
    `merged`; a note is relayed when it has no action or `create` (`TestGitLabIssueUpdateLabelDetails`,
    `TestGitLabMergeRequestMergedFlag`, `TestGitLabNoteHook_TS_32_41`).
16. **A test seam.** TS-32-49 needs a delivery of three events and no real delivery makes three
    before the GitLab update of task 7, so `handlers.reduce` (`routes.go`, set to
    `reduceDelivery` by `MountWebhookIngress`, `ingress.go:59`) lets the test hand the ingress a
    reduction. `TestEmitErrorWithAnUpdateOfThreeEvents` checks the same against a real update.
17. **The retention interval is a variable** (`retentionEvery`, `retention.go:17`, one hour), so
    that a test can shorten it and prove `StartDeliveryRetention` uses it
    (`TestStartDeliveryRetentionTicks`). A pass that the shutdown cut short is not logged as
    a failure.
18. **Where the checks live.** TS-32-56 reads `cmd/af-hub/main.go` with `go/parser`
    (`cmd/af-hub/main_wiring_test.go`); TS-32-68 runs the real `af-hub` binary against a
    seeded database (`TestAfHubBinary_PrunesTheLogAndMountsTheIngress_TS_32_68`), which also
    sends a signed delivery to it. The smoke tests are in `internal/forge/smoke_webhook_test.go`
    (the spec's `touches` name `smoke_test.go`), and TS-32-63 is two tests because the one that
    runs `afc` changes `HOME`, under which the go tool must not run.

## Not handled

- **Rate limiting** of the ingress. An unauthenticated caller can write a warn line per request
  and, for a GitHub delivery, make the hub read up to 1 MiB.
- **GitHub deliveries as `application/x-www-form-urlencoded`**, GitHub's legacy
  `X-Hub-Signature`, Forgejo and Gitea headers and GitLab's signing-token header (non-goals):
  a form-encoded delivery with a valid signature is recorded `malformed`.
- **Secrets that `afc secrets create` makes with a value outside the rules of `PUT`** (shorter
  than 16 characters, with a space) are accepted by the store and used as they are: the rules
  are those of the `PUT` route only.
- **A host or a path in a payload's URL** is not checked: only the user information is removed.
- **A carry-patch fork whose upstream is the same repository as its origin** binds to the origin.
