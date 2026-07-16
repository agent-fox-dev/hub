# Audit: OAuth Login Flow Failures

Three bugs prevented `afc login` from working end-to-end. All were simple
wiring or schema issues. This audit examines why each one was introduced and
why existing tests didn't catch it.

---

## Bug 1: Empty OAuth provider registry

**Symptom:** `unsupported provider: github. Available: ` (empty list).

**Root cause:** `cmd/af-hub/main.go` created `auth.NewRegistry()` but never
registered providers from `result.Config.OAuth.Providers`.

### Spec analysis

Spec 01 (server_foundation) PRD explicitly parses `[[oauth.providers]]` from
config.toml and notes: "These fields are parsed and stored but **unused until
spec 2**." Spec 02 (oauth_and_users) defines the provider registry abstraction
and `NewGitHubProvider(cfg)`, but never describes who calls
`registry.Register()` at startup. Spec 02 `tasks.json` task 4.2 creates the
provider constructor; task 4.3 creates the handler. No task in either spec
wires the config into the registry.

### Why it was missed

- **Spec gap: ownership handoff.** Both specs assume the other side handles
  registration. Spec 01 says "unused until spec 2." Spec 02 never picks up
  the handoff.
- **No integration test crosses the boundary.** Spec 02 tests use
  `registerGitHubProvider()` helpers that manually call `Register()` —
  they never exercise the real startup path.
- **Unit tests pass in isolation.** The registry works. The config parser
  works. Nothing tests that they're connected.

### Classification

**Spec defect:** missing cross-spec handoff requirement. Neither spec owns
"at startup, iterate config OAuth providers and register each."

---

## Bug 2: authorize_url missing client_id and scope

**Symptom:** GitHub returns 404 because the authorize URL has no `client_id`.

**Root cause:** `Registry.List()` returned the bare `authorize_url` from the
provider. The CLI's `BuildAuthorizationURL()` only appended `state` and
`redirect_uri`, not `client_id` or `scope`.

### Spec analysis

The specs are **clear but contradictory in practice:**

- **Spec 02 PRD** says the server returns a "**base** authorize URL (without a
  `state` parameter embedded)" and that "The CLI client is responsible for
  appending `state`, `client_id`, `redirect_uri`, and `scope` query parameters
  to the base URL."

- **Spec 05 (CLI)** execution path 05-PATH-1 step 3 says "opens authorization
  URL in browser" but never specifies which query params to append.

- **The implementation** (`BuildAuthorizationURL` in `internal/login/login.go`)
  has a comment: "Per the reviewer finding, the server's authorize_url already
  includes client_id and scope; the CLI only appends state and redirect_uri."
  This directly contradicts spec 02 PRD.

So the spec says the CLI must append `client_id` and `scope`. The
implementation was told (by a reviewer) that the server includes them. Neither
side did it.

### Why it was missed

- **Spec-implementation mismatch.** The spec assigns URL construction to the
  CLI. A reviewer overrode this and told the implementer the server handles it.
  Neither side was updated to actually do it.
- **The fix we applied puts it on the server side** (`Registry.List()` now
  bakes in `client_id` and `scope`). This contradicts spec 02 PRD's design
  but is the correct engineering choice — the server owns `client_id` and
  should not expose it to the CLI for manual reassembly.
- **No test opens a real authorize URL.** All tests mock GitHub and never
  validate that the constructed URL would actually work.

### Classification

**Spec-implementation conflict.** Spec 02 assigns the responsibility clearly,
but the implementation was steered by a reviewer comment in a different
direction, and neither path was completed. The spec's design (CLI appends
`client_id`) is also questionable — the `client_id` is a server secret that
the CLI shouldn't need to know.

---

## Bug 3a: GitHub private email not fetched

**Symptom:** `OAuth provider returned empty email; email is required`.

**Root cause:** GitHub's `/user` endpoint returns `null` for email when the
user has email privacy enabled. The `user:email` scope grants access to
`/user/emails`, but `GetUserInfo()` never called it.

### Spec analysis

Spec 02 PRD says: "Fail login if the provider returns a null or empty email."
It notes that `user:email` scope is "required because GitHub does not return a
user's email unless this scope is explicitly requested." This is factually
wrong — even with `user:email`, GitHub `/user` returns `null` for private
emails. The email is only available via the separate `/user/emails` endpoint.

Spec 02 task 4.2 says `GetUserInfo()` "GETs userinfo_url with Bearer token;
parses JSON for login (username), email, id." No mention of `/user/emails`.

### Why it was missed

- **Spec factual error about GitHub API behavior.** The spec author assumed
  `user:email` scope causes `/user` to include the email. In reality, it only
  grants access to the `/user/emails` endpoint.
- **Tests use mocks that return whatever you tell them.** The mock server
  returns email when told to and null when told to — but never exercises the
  real GitHub API to discover the private-email behavior.

### Classification

**Spec factual error.** The spec made an incorrect assumption about GitHub API
behavior. No amount of testing against mocks would have caught this — only an
end-to-end test against real GitHub (or documented knowledge of the API quirk)
would surface it.

---

## Bug 3b: Missing expires_in_days column

**Symptom:** `failed to create API key` (INSERT fails on missing column).

**Root cause:** `keys.CreateKey()` inserts into an `expires_in_days` column
that doesn't exist in the `api_keys` CREATE TABLE DDL in `internal/db/db.go`.

### Spec analysis

This was already known. Errata file `docs/errata/02_user_management_divergences.md`
section 4 explicitly documents it:

> "Added `expires_in_days INTEGER` column to the `api_keys` table."
>
> "The production migration DDL (in `internal/db`, currently a stub) must also
> include this column when implemented."

The errata says the column was added to "test DDLs and INSERT helpers" but the
production schema in `internal/db/db.go` was never updated.

### Why it was missed

- **Errata documented, fix incomplete.** The divergence was identified and
  documented, but the errata's own resolution ("must also include this column")
  was never applied.
- **Tests use their own DDL.** Test setup code includes the column in inline
  schema creation, so all tests pass. The production schema in `db.go` is a
  different code path that is never exercised by tests.
- **No test creates a database using `db.InitDatabase()` and then exercises
  `CreateKey()` through it.** The integration tests use separate test-local
  schemas.

### Classification

**Known issue, incomplete fix.** The errata system worked as designed —
it caught the divergence. But the remediation step was never executed, and no
test validates that the production schema matches what the code expects.

---

## Systemic Issues

### 1. No cross-spec integration tests

Every spec has thorough unit and component tests, but no test exercises the
full path from config → server startup → CLI → OAuth → API key → authenticated
request. The three bugs above all live at boundaries between specs or between
components that are individually well-tested.

**Recommendation:** Add a smoke test that starts the real server from config,
runs `afc login` against it (with a mock OAuth provider), and verifies the
resulting config file contains valid credentials that authenticate successfully.

### 2. Test schemas diverge from production schemas

Test setup code defines its own inline CREATE TABLE statements. When the
production schema changes (or fails to change), tests continue to pass because
they use a different schema. Bug 3b is a direct consequence.

**Recommendation:** Tests should use `db.InitDatabase()` (or at minimum, the
same DDL source) to create their test databases. A shared schema source
eliminates the possibility of test/production divergence.

### 3. Spec handoff gaps

Specs 01 and 02 have a clear dependency (config parsing → provider
registration) but no explicit handoff requirement. The assumption that "the
next spec will wire it up" produced a gap that neither spec's tests cover.

**Recommendation:** When a spec explicitly defers work ("unused until spec N"),
spec N's requirements must include a requirement that picks up the deferred
item. The deferred item should be tracked as an explicit input dependency.

### 4. Mock-only OAuth testing

All OAuth tests mock the GitHub API. This is correct for unit testing but
insufficient for catching API behavior assumptions (like the private email
issue). The spec's factual error about GitHub's email behavior could only be
caught by testing against the real API or by an implementer who knows the
quirk.

**Recommendation:** Document known GitHub API behaviors (private email,
rate limiting, token formats) in a reference file. Spec requirements about
external API behavior should cite the provider's documentation, not assume.

### 5. Reviewer-driven spec overrides without updating specs

Bug 2 happened because a reviewer told the implementer to deviate from the
spec, but neither the spec nor the implementation was updated to reflect the
new design. The `BuildAuthorizationURL` comment says "Per the reviewer finding"
— an informal override that left both sides incomplete.

**Recommendation:** When a reviewer overrides a spec decision, the spec or
errata must be updated before the implementation is merged. Informal comments
in code are not a substitute for spec amendments.
