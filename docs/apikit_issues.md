# apikit issues found during the September 2026 hub review

These were found while reviewing the hub and belong in `txsvc/apikit`. They
could not be filed from the review session (no write access to that
repository); each section is written as a ready-to-paste issue.

## A1. Body-size limit is installed globally and cannot be exempted

**Title:** Allow routes to opt out of the request body size limit

`NewServer` installs `bodySizeLimitMiddleware` with `e.Use`, so it applies to
every route, including ones an application registers outside the API group
(hub's git smart-HTTP endpoints under `/git/`). A `git push` whose packfile
exceeds `max_body_size` (default 1 MB) fails with 413 / `http: request body
too large`, and there is no supported way to exempt the route.

Hub currently works around this with an `e.Pre` middleware that swaps the
body out before the limiter runs. A proper fix would be one of:

- apply the limiter on the API group (`/api/v1`) instead of globally;
- accept a `Skipper func(echo.Context) bool` (or a list of path prefixes) in
  `ServerConfig`;
- expose the middleware so applications can install it themselves.

## A2. Export credential validation for non-REST entry points

**Title:** Expose a `ValidateCredential`/`ResolveAuthInfo` function

Applications that authenticate outside the echo middleware (hub's git server
uses HTTP Basic with the API key or PAT as password) have to re-implement
hashing, lookup and status checks against apikit's tables. Hub's copy did not
check `users.status = 'blocked'`, so a blocked user kept git access after
losing API access. Exporting the same function the middleware uses (token in,
`*AuthInfo` + error out, including blocked/expired/revoked handling) removes
the duplication and the drift.

## A3. Deletion hooks for users and organisations

**Title:** Provide before/after delete hooks for orgs and users

Already recorded in hub's `docs/errata/07_cascade_deletion_user_org.md`:
when apikit deletes an org or user, application-owned rows (secrets,
variables, workspaces) are orphaned because there is no hook to cascade.
`OnBeforeOrgDelete(ctx, orgID) error` / `OnBeforeUserDelete` callbacks in the
server config (or a `Deleter` interface) would allow applications to clean
up or veto.

## A4. SQLite connection pool is limited to a single connection

**Title:** Reconsider `SetMaxOpenConns(1)` for SQLite

With one connection every request is serialised behind the longest
transaction. In hub this includes clone bookkeeping and job-queue polling.
Enabling WAL, setting `busy_timeout`, and allowing a small pool (readers in
parallel, writes serialised by SQLite itself) would remove the latency cliff
without changing semantics.

## A5. CLI exit codes and error output

**Title:** `CLIExitCode` ignores the code carried by `CLIError`

`NewCLIError(code, msg)` accepts a code, but `ExitCode(err)` maps any API
error to 1 and everything else to 2 regardless of it, so applications cannot
distinguish usage errors from runtime failures as their documentation
promises. Related: `CmdHandleError` writes the JSON error envelope to
stdout, which mixes with successful JSON output when a command prints a
result before failing; consider stderr, or a documented single-document
guarantee.

## A6. Non-interactive login and configurable environment names

**Title:** Support `login --api-key` and namespaced environment variables

`afc login` is interactive only, and the recognised environment variables
are the generic `ENDPOINT_URL` / `API_KEY`, which collide with other tools
in CI images and devcontainers. Requests:

- a non-interactive `login --api-key <key> --endpoint-url <url>` (or reading
  the key from stdin);
- a per-application prefix for the variables (e.g. `AF_ENDPOINT_URL`) and
  the config directory (`~/.af` is hard-coded).

## A7. HTTP client has no timeout

**Title:** `CmdClient.DoRequest` should apply a default timeout

`DoRequest` uses the caller's context only; commands that do not set a
deadline hang forever on a stalled connection. A default per-request timeout
(overridable through the context) would make every CLI built on apikit
robust by default.
