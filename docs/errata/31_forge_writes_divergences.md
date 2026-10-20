# Errata: Spec 31 — Forge Writes Divergences

## Spec Expectation

Spec 31 (`31_forge_writes`) lists the `issuex` write methods as unverified assumptions:
`CreateIssue`, `UpdateIssue`, `CloseIssue`, `PostComment`, `AddLabels`, `RemoveLabel`,
`GetLabel` (or `ListLabels`), `CreateLabel`, `ReadIssue`, `CreatePullRequest` and
`PostReviewComment`, with the shapes "under whatever names `issuex` uses". It assumes that an
update can send a body that is empty and a set of labels that is empty, that a label can be
looked up by name and that its creation returns it, that a label removal reports the forge's
not found, that a close returns the issue, and that `PostReviewComment` returns the comment's
URL. The real package, `../aflib/issuex`, has none of the four capabilities in the form the
spec needs, and the spec's non-goal "changing `issuex`" turns each gap into a finding for
aflib. The entries below record how the hub delivered the behaviour the criteria and tests
ask for, with the code and the test of the delivered behaviour. Entries 1 to 7 are limits of
`issuex` that aflib would have to lift; once it does, `write_raw.go` can shrink to
the calls of `issuex`.

## Implementation Reality

1. **An issue update that can clear.** `issuex.UpdateIssueRequest` has plain string and slice
   fields, and both adapters send a field only when it is not empty (`UpdateIssue` in
   `github_issues.go` and `gitlab_issues.go`), so `body: ""`, `labels: []` and
   `assignees: []` (31-REQ-2.3) cannot be sent. The hub sends the update itself:
   `forgeWriter.ChangeIssue` (`write_raw.go:57`) calls `rawForge.updateIssue`
   (`write_raw.go:193`) with an `issueUpdate` whose fields are pointers, so that a field that
   is set but empty is sent. It goes through the shared HTTP client (user agent, token rules,
   redirect policy, deadline) and fails as the `*issuex.HTTPError` that `mapForgeError`
   maps. GitLab's names are `description`, `state_event` (`close` and `reopen`) and a
   comma-joined `labels` that is `""` to clear (`gitlabPayload`, `write_raw.go:154`)
   (`TestUpdateIssue_SendsOnlyPresentFields_TS_31_11`,
   `TestIssueCreateAndUpdate_BothForges_TS_31_14`, `TestIssueUpdatePayloads`,
   `TestUpdateIssue_ForgeFailures`).

2. **A label lookup, and a create that returns the label.** `Client` has no lookup of a label,
   and `CreateLabel(ctx, repo, Label) error` returns no label and counts an existing label
   as a success (GitHub's 422 with `already_exists`, GitLab's 409). The 200 and 201 answers,
   the second lookup after a refused create and `created` (31-REQ-5.2 to 5.5) cannot be
   built on it. The hub sends both requests itself: `forgeWriter.GetLabel` and
   `forgeWriter.MakeLabel` (`write_raw.go:81`, `:92`), the color with a `#` on GitLab and
   without on GitHub (`TestEnsureLabel_ExistingIsReturnedUnchanged_TS_31_28`,
   `TestEnsureLabel_CreatesWithTheDefaultColor_TS_31_29`,
   `TestEnsureLabel_RefusedCreateLooksAgain_TS_31_30`, `TestEnsureLabel_Twice_TS_31_31`,
   `TestEnsureLabel_GitLab`). A label GitHub holds is answered as GitHub gives it, the color
   without a `#`, and as GitLab gives it, with one.

3. **A label removal that sees the forge's 404.** GitHub's `RemoveLabel` returns nil for a
   404, so a removal on an issue that does not exist would be 204 and the read-back of
   31-REQ-4.4 and 4.5 could never run. On GitHub the hub sends the removal itself
   (`forgeWriter.DropLabel`, `write_raw.go:65`); on GitLab issuex's `RemoveLabel` is used,
   whose request is a PUT of the issue and surfaces the 404 of a missing issue
   (`TestRemoveLabel_AbsentLabel_TS_31_24`, `TestRemoveLabel_MissingIssue_TS_31_25`,
   `TestLabels_BothForges_TS_31_26`).

4. **The read-back is one GET.** 31-REQ-4.4 names `ReadIssue`, which in issuex also lists the
   issue's comments, up to five pages. The hub needs to know that the issue is there, and
   sends one GET of it (`forgeWriter.CheckIssue`, `write_raw.go:76`); the "single issue read"
   the criterion and TS-31-24 count is that GET, and the comments are not requested.

5. **A close is a comment and an update.** `CloseIssue(ctx, ref, comment) error` returns no
   issue, and no URL for the comment it posts. A close is `AddComment`, which returns the
   URL for the event, when the comment is not blank, and then the update with the state
   `closed`, which returns the issue as the forge reports it (`prepareCloseIssue`,
   `write_issues.go:165`) (`TestCloseIssue_CommentBeforeClose_TS_31_16`,
   `TestCloseIssue_FailedCloseKeepsTheCommentURL_TS_31_18`, `TestCloseIssue_GitLab`).

6. **A comment on a pull request is `AddComment`, not `PostReviewComment`.** In issuex
   `PostReviewComment(ctx, ref, body) error` returns no URL, and on GitHub it posts a review
   (`POST .../pulls/:n/reviews` with `event: COMMENT`). 31-REQ-6.6 and TS-31-37 need the
   comment's URL on the pull request's conversation. `AddComment` with `IsPullRequest` posts
   on the issue comments of the number on GitHub and the merge request's notes on GitLab,
   and returns the URL (`commentOnPullRequest`, `write_pulls.go:78`, which is
   `prepareComment(true)`, `write_issues.go:231`)
   (`TestCommentOnPullRequest_TS_31_37`, `TestPullRequests_GitLab`).

7. **GitLab assignees are accepted and not sent.** GitLab takes assignees as user ids. issuex
   sends none on create or update; the hub's own update has no names to ids lookup, which
   would be requests the spec does not ask for. The assignees are validated (at most 100,
   none blank) and left out of the request on GitLab, on create and on update
   (`gitlabPayload`, `write_raw.go:154`; `TestGitLabAssigneesAreAcceptedAndNotSent`). A
   caller who needs them on GitLab has to set them there.

8. **A write goes to the API root the hub states, not the one issuex would find.** A write
   sends some of its requests itself, beside issuex's, and both must go to one API root.
   A client built for a write is given `BaseURL` explicitly: the `FORGE_API_URL` value or
   the hub's statement for the host (`buildClient`, `client.go:79` and `:113`, `apiRoot`,
   `:134`). Unlike a read, a write does not read `GITHUB_API_URL` or `GITLAB_API_URL` of the
   hub's own environment (`TestWriteRequests_GoToOneAPIRoot`). A host whose forge only the
   environment can tell is `422 forge_unknown` for a write until `FORGE_API_URL` names the
   forge.

9. **The requests the hub sends are not retried at all.** issuex retries a rate-limited
   request once, after the forge's `Retry-After` plus a second (`forgeHTTP.do`,
   `transport.go`). The hub's own requests (`rawForge.do`, `write_raw.go:235`) are single
   attempts: a 429 on an issue update is answered at once, `Retry-After` passed through
   (`TestUpdateIssue_ForgeFailures/429`). 31-REQ-8.2 asks for no hub-side retry, so this is
   within it, but the two kinds of request differ.

10. **`Retry-After` is the forge's plus a second.** TS-31-43 says that the answer to a forge
    429 with `Retry-After: 30` carries `Retry-After 30`. issuex's `HTTPError.RetryAfter()`
    adds a second, and the hub passes that on (31), as for the reads (entry 6 of the erratum
    of spec 30) (`TestWriteFailures_UseTheSharedMapping_TS_31_43`).

11. **The GitLab limit is the spec's, not issuex's.** issuex exports `GitLabMaxBodyLength`
    1,048,576. The hub uses 1,000,000 (`write_limits.go:16`), the smaller of GitLab's limits
    on a note and on a description, as 31-REQ-7.2 says (`TestBodyLimit_GitLab_TS_31_39`).

12. **The label name is read from the escaped path.** echo's `:name` is still encoded for a
    path with a `%2F` and decoded for one without, so decoding it once would sometimes be a
    second decoding of a name that holds a `%`. `lastSegment` (`write_labels.go:64`) reads the
    last segment of `URL.EscapedPath()` and decodes it once
    (`TestRemoveLabel_NameIsDecodedOnce_TS_31_23`, with `%2F`, `:`, `%2525` and `%20`).

13. **The forge is told before the client is built.** The body limit depends on the forge,
    and an unknown forge must be 422 before 413, so `buildClient` runs `issuex.DetectForge`
    before `NewClient` (`client.go:112`). A fake client in a test needs a host that can be
    classified.

14. **A bare `user:pass@` is not URL userinfo.** TS-31-47 says that no response, log line or
    event holds "a `user:pass@` sequence". The scrubber of spec 30 removes the user
    information of a URL (`scheme://user:pass@host`) and the token wherever it is; a
    `user:pass@` outside a URL stays in a forge message. The test echoes the git URL, which
    is what a forge shows (`TestNoCredentialInResponsesLogsOrEvents_TS_31_47`).

15. **Spec 30's TS-30-37 changed.** It checked that `internal/forge` does not import
    `internal/audit`, which held while the package had no emitter. `forge.Config` now takes
    one (31-REQ-9.8), so the test wires a spy emitter into the routes and asserts that no
    read calls it, which is what the spec 30 entry describes
    (`TestReadsRecordNoAuditEvent_TS_30_37`).

16. **Tests that passed before their code, or differ from their entry.** TS-31-34, 38 to 52
    and 7 passed at their first run, as guards of the pipeline of tasks 1 and 2 and of the
    routes; mutants of that code show that each fails when its behaviour is broken (the
    commit messages of tasks 6 to 8 list them). TS-31-40 runs every field with every byte
    width on GitHub and, on GitLab, where a field is a megabyte of code points, with the
    1-byte and the 4-byte character. TS-31-43 expects `Retry-After: 31` (entry 10).

## Impact

The routes, statuses, `error_type`s and events are those of the spec. The differences that a
caller can see are entries 7 (GitLab assignees are not sent), 8 (a write looks at
`FORGE_API_URL` or the host, not the hub's environment), 9 and 10 (rate limits). Nothing in
`aflib` was changed. The findings for aflib are: a label lookup and a create that returns
the label; an update that can send an empty field; a label removal that does not hide a
missing issue; a close and a pull-request comment that return the issue and the URL; and
GitLab assignees.
