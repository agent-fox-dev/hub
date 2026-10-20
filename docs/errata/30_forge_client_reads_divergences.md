# Errata: Spec 30 — Forge Client Reads Divergences

## Spec Expectation

Spec 30 (`30_forge_client_reads`) was written before the `issuex` package could be read
in the repository (aflib was not in `go.mod`), and its "Verified External API" table lists
every `issuex` signature as unverified. It assumes `issuex.ParseRemote` reads any remote
URL, that `FORGE_API_URL` turns an unclassifiable host into a known forge, that the client
has an accessor for the forge and the API URL, that the list calls return bare slices, and
that `Retry-After` is the forge's value. The entries below record where the code that
landed differs, with the code and the test of the delivered behaviour. Entries 3, 4 and 5
are limits of `issuex` that aflib would have to lift.

## Implementation Reality

1. **issuex's signatures.** `Client` takes a `Repo` or an `IssueRef` for every call
   (`GetRepository(ctx, repo)`, `ListIssues(ctx, repo, IssueFilter)`,
   `ReadIssue(ctx, IssueRef)`, ...) and `Options` also has `UserAgent`, `NoOp` and `Dir`.
   `buildClient` (`client.go:69`) passes `BaseURL`, `Token`, `RemoteURL`, `Repo` and
   `HTTPClient`; the user agent is still set by a round-tripper (`httpclient.go:46`), which
   wins over the `agent-fox` default of issuex
   (`TestUserAgent_TS_30_23`, `TestCredentialKinds_TS_30_19`).

2. **`ParseRemote` reads only hosts that name a forge, so the hub splits the rest.**
   `issuex.ParseRemote` returns `(Repo, bool)` and refuses a host unless its name
   contains `github` or `gitlab` (or an environment API URL names it). `parseRemote`
   (`client.go:170`) tries it first and then splits the URL itself (`splitRemote`, `client.go:179`), so that a
   host that names neither forge is named in the answer
   (`TestUnclassifiableHostIsForgeUnknown_TS_30_18`) and `GET /forge` can show its
   repository (`TestDiagnosticReportsUnknown_TS_30_28`). The parsed repository is always
   handed to issuex: without one, a remote issuex cannot parse makes it run
   `git remote get-url origin` in the hub's working directory
   (`TestAmbiguousHostNeverAsksTheWorkingDirectory`).

3. **`FORGE_API_URL` does not classify a host by itself.** issuex tells GitHub from
   GitLab by the words `github` and `gitlab` in the host or in the API URL. A host that
   names neither, with an API URL that names neither, stays `forge_unknown`; the message
   says so and does not ask to set a variable that is set
   (`TestAPIURLThatNamesNoForgeSaysSo`, `ambiguousForge`, `client.go:145`). An API URL that
   contains one of the words classifies the host. A forge-kind variable, which would fix
   it, is a non-goal of the spec; an accessor or an option in issuex is the aflib change.

4. **issuex has no accessor for the forge or the API URL of a client.** `describeRemote`
   (`client.go:269`) takes the forge from the exported `issuex.DetectForge`, given the same
   options a read gets, and the API URL from the variable or from `defaultAPIURL` (`client.go:312`), which
   states issuex's default for the host (`api.github.com`, `<host>/api/v3`,
   `<host>/api/v4`) and does not see the `GITHUB_API_URL` or `GITLAB_API_URL` of the hub's
   environment that issuex would use (`TestDefaultAPIURL`,
   `TestDiagnosticReportsWithoutContactingTheForge_TS_30_26`).

5. **The list calls return structs, and their flags are not shown.** `ListIssues` returns
   `IssueList{issues, incomplete}` and `ListComments` `CommentList{comments, truncated}`.
   The spec asks for bare arrays, so the routes answer `.Issues` and `.Comments`
   (`handlers_read.go:66` and `:145`), and `incomplete` and the comment list's `truncated` are not
   exposed; `GET /issues/:n` keeps the thread's `truncated`. An issue thread without
   comments carries `[]`, not `null`
   (`TestReadRoutesReturnTheIssuexEncoding_TS_30_32`, `TestEmptyListsAreArrays_TS_30_33`).

6. **`Retry-After` is issuex's wait, which adds a second.** `HTTPError.RetryAfter()` adds
   one second to the forge's `Retry-After` header (and to the time until a reset), so
   `Retry-After: 1` gives `2`; the rounding to whole seconds is `retryAfterSeconds`
   (`errors.go:147`). issuex waits out a backoff of up to 120 seconds and tries once more, so
   a wait of 29 to 120 seconds meets the 30-second deadline first and is answered
   `504 forge_timeout`, not `429`; a longer one is `429` at once. TS-30-41's 1.2 seconds
   is tested on the rounding helper, the wire on `Retry-After: 1`
   (`TestForgeRateLimit_TS_30_41`, `TestRetryAfterSeconds`,
   `TestDeadlineCoversTheRateLimitWait`).

7. **issuex's errors map as they are.** A 404 is `ErrNotFound` wrapping the `HTTPError`;
   a 401, another 403 and a 5xx are the bare `HTTPError`; a rate limit is `ErrRateLimited`
   wrapping it; a 405 or 409 is `ErrConflict` and is answered `forge_rejected`. Every error
   that comes from the HTTP client is a `*url.Error`, and is `forge_unreachable` unless it
   is the deadline or the refused redirect; an error in reading or decoding the body is
   `forge_error` (`mapForgeError`, `errors.go:77`; `isConnectionFailure`, `errors.go:141`; `TestMapForgeError`).

8. **The hub's environment is read by issuex.** Given no token, issuex uses
   `GITHUB_TOKEN`, `GH_TOKEN` or `GITLAB_TOKEN` of the hub's own environment, which would
   have been sent for a workspace with no credential. The call's context says which token
   the workspace has, and for none the transport removes `Authorization` and
   `PRIVATE-TOKEN` (`httpclient.go:46`, `TestNoCredentialSendsNoEnvironmentToken`). The
   tests clear these variables and `GITHUB_API_URL` and `GITLAB_API_URL`.

9. **The redirect policy also pins the scheme.** The spec refuses a redirect to another
   host; `refuseCrossHost` (`httpclient.go:70`) also refuses another port or scheme, so that a token never
   follows a redirect to plain http on the same host (`TestRedirectPolicy`).

10. **The error issuex carries for an issue's comments is scrubbed.** `ReadIssue` returns
    the issue with `CommentsErr` when only the comments failed; that error encodes as its
    fields (`HTTPError`) or as `{}`. `readIssue` (`handlers_read.go:120`) scrubs the message of
    an `HTTPError` and replaces any other with its scrubbed text
    (`TestIssueCommentsErrorIsScrubbed`).

11. **The API URL variable is trimmed, and an empty one is invalid.** A value is trimmed of
    white space before the check (`validAPIURL`, `client.go:155`), and a variable that is set to nothing answers
    `forge_api_url_invalid` (`TestInvalidAPIURLIsRefused_TS_30_17`).

12. **GitLab's API URL gets `/api/v4`.** `issuex.NewGitLab` appends `/api/v4` to a base URL
    that does not end with it, so a read under `FORGE_API_URL=https://git.example/gitlab` goes
    to `https://git.example/gitlab/api/v4/...`. `describeRemote` does the same for the
    `api_url` it shows (`client.go:301`), so that the diagnostic names
    the URL a read uses (`TestDiagnosticShowsTheGitLabAPIURLTheReadUses`).

13. **The message for an unreadable remote shows what follows the last `@`.** A password in a
    `git_url` that holds `/`, `?` or `#` ends the authority of the URL before its `@`, where
    `redactRemote`'s pattern stops; `validateGitURL` accepts such a URL, and it does not parse.
    `shownRemote` (`client.go:237`) cuts what is left up to the last `@`, so that `forge_unknown`
    never shows a password (`TestUnparseableRemoteIsForgeUnknown_TS_30_16`).

14. **No `canWriteForge`.** Task 2 lists `canReadForge` and `canWriteForge` among the auth helpers.
    No route of this spec needs the write check, and a function only a test calls is dead code,
    so `canWriteForge` is left for the spec that adds the first forge write. `forge:write` is
    registered and implies `forge:read` (`auth.go`, `TestCanReadForge`).

15. **Tests that differ from the plan.**
    - TS-30-12 runs on `GET /forge/repository` in task 3 and also on `/pulls/1/state`
      from task 4.
    - TS-30-7, 10, 21, 22, 43 and the checks that the transport and the scopes keep their
      behaviour passed on their first run, because earlier tasks built what they
      verify; TS-30-43 and TS-30-9 were made to require more (the forge's message; a route
      that exists) and failed first.
    - TS-30-37 checks that `internal/forge` does not import `internal/audit` directly:
      `internal/secrets`, which it needs for its not-found error, brings the audit package
      in beneath it.
    - TS-30-53's limit of 30 is applied by issuex after it has read the page, so the
      forge's request carries no `limit`; the filter is checked at the issuex call
      (`TestListQueryReachesIssuex_TS_30_36`).
    - The smoke tests that run `afc` live in `internal/cli/forge_smoke_test.go`, which
      imports `internal/forge`; no non-test file of `internal/cli` does.
    - Unlike spec 29's, no test runs the real `af-hub` binary against a forge; the binary
      was started by hand and the routes, the scopes and the 401 and 404 answers were seen
      there.
