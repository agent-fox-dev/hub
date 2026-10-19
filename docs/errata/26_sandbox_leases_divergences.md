# Errata: Spec 26 — Sandbox Leases Divergences

## Spec Expectation

Spec 26 (`26_sandbox_leases`) was written against an assumed layout of the code
that spec 25 (`25_sandbox_profiles`) delivered, and its tasks name files, types
and signatures. The following entries record where the delivered code differs
from the plan, with the code and the test of the delivered behaviour. None of
them changes a requirement of the spec.

## Implementation Reality

1. **The lease code lives in new files under new names.** The tasks plan
   `service.go`, `validate.go`, `handlers.go` and a lease type called `Service`
   in `internal/sandboxes`, and registration "through the spec 25
   `RegisterRoutes` seam". Spec 25 already uses those names for the provider
   `Service`, the configuration validation and the profile listing, and its
   tests (`validate_test.go`, `smoke_test.go`) use the test file names. The
   lease service is `LeaseService` in `lease_service.go`, the request
   validation is in `lease_validate.go`, the handlers are in `handlers_issue.go`,
   `handlers_read.go` and `handlers_delete.go`, the routes are mounted by their
   own `RegisterLeaseRoutes` (`routes.go`, line 15) and the two colliding test
   files are `lease_validate_test.go` and `lease_smoke_test.go`.
   `RegisterRoutes` and the profile listing are unchanged. Task 2 also adds
   `max_sandboxes` to spec 25's `validate.go` and `service.go` (the limit in
   `Validated` and `Service.MaxSandboxes`), which its `touches` list does not
   name; that code has no other home.

2. **`ttl` and the lease's clock are cut to whole seconds.** 26-REQ-1.6 says
   `expires_at` is `created_at` plus the TTL and that `expires_at` minus
   `created_at` equals `ttl_seconds`. `apikit.FormatUTC` writes timestamps with
   second precision, so a `ttl` such as `61.5s` would otherwise give an
   `expires_at` that is 61 or 62 seconds after `created_at`, depending on the
   clock's fraction. `Issue` truncates the clock and the TTL to the second
   (`lease_service.go`, lines 140 and 141), so `61.5s` is a lease of 61 seconds.
   See `TestIssue_SubSecondClockAndTTL` and `TestIssue_Timestamps_TS_26_6` in
   `internal/sandboxes/issue_test.go` (a clock at `03:04:05.9` and a `61.5s` ttl
   give 61 whole seconds) and `TestIssue_TTLAccepted_TS_26_19` in
   `internal/sandboxes/lease_validate_test.go`.

3. **`CheckRequiredSecrets` returns one error, not three values.** Task 3 plans
   `CheckRequiredSecrets(...) (missing, searched []string, err error)`. It returns
   `error`: nil, a `*MissingSecretsError` that carries the missing keys in
   profile order and the scope types searched, or the lookup error
   (`secretcheck.go`, line 72). The information is the same, and the handler
   maps the type to `422 secret_missing`.

4. **Spec 25's TS-25-52 was changed.** It asserted that `profiles` is the only
   subcommand of `afc sandbox`; 26-REQ-10 adds four. The test now asserts that
   `profiles` is among them (`TestBuildRootCommand_SandboxGroup_TS_25_52`), and
   `TestSandboxGroup_LeaseCommands_TS_26_55` pins the full set (`create`,
   `destroy`, `get`, `list`, `profiles`) in `internal/cli/sandbox_cmd_test.go`.

5. **`26-PATH-1` is a placeholder.** The spec's only execution path reads
   "x / 1. a b / 2. c d". The integration task traces the path the PRD and
   TS-26-57 describe instead: issue, read, look up the token, end, with the token
   revoked. `TestSmoke_IssueReadEndLease_TS_26_57` runs it over the production
   route registration with a real DuckDB audit emitter, and
   `TestAfHubBinary_Leases_PATH_1` (`internal/sandboxes/lease_smoke_binary_test.go`)
   runs it through the real `af-hub` binary.

6. **Labels over the limit are named by the first key past the sixteenth.**
   26-REQ-3.8 says a refusal names the offending label key. For more than 16
   labels no single key is at fault; the lowest key (in sorted order) beyond the
   sixteenth is named (`lease_validate.go`, line 240), so the answer does not
   depend on map order.

7. **Two lease-store seams have no production caller yet.** `LookupByToken`
   (`lease.go`, line 206) and `RevokeToken` (line 243) are exported for the
   sandbox gateway, which belongs to the `sandbox_gateway` spec; they are
   exercised by tests only. `ResolutionChain` is called by the presence check
   at issue (`secretcheck.go`, line 76) and is what the gateway will resolve
   values through. Everything else the spec has this one deliver is called from
   `cmd/af-hub/main.go`.

8. **The org tier does not follow `handleResolvedWorkspaceVars` for admin
   tokens.** Design Decision 6 calls the rule "the one
   `handleResolvedWorkspaceVars` already applies". That handler lets an admin
   token skip the membership test (`internal/secrets/handlers.go`, line 757:
   `orgID != "" && !isAdmin(auth) && !isOrgMember(...)`). The spec's criteria
   and TS-26-26 say the org scope requires the *workspace owner's* membership
   whoever the caller is, and the resolution chain implements that
   (`secretcheck.go`, `ResolutionChain`), so an admin token and the owner see the
   same scopes. See `TestResolutionChain_TS_26_26`.

9. **The request parser reads the body in two steps.** Go's JSON decoder matches
   field names without regard to case, so `{"PROFILE":"tools"}` would have been
   taken for `profile`. 26-REQ-3.3 refuses any field other than the four, so
   `parseIssueRequest` first decodes the body into a map and checks the names
   exactly, then decodes it into the request (`lease_validate.go`). A body that
   cannot be read answers 400, or 413 when it is over the server's limit, not 500.
   See `TestIssue_UnknownFieldsAndNonObjects_TS_26_17` and
   `TestIssue_UnreadableBody`.
