---
spec_id: "26"
spec_name: "sandbox_leases"
title: "Sandbox leases: issue, inspect and end a caller's right to one sandbox"
status: "active"
created_at: "2026-10-19T16:29:25.983184Z"
updated_at: "2026-10-19T16:29:25.983184Z"
intent_hash: "ab4ef4bf4c5b90ad5133cc62a6e1b215859c358cbad8273bca60cfd86b90a477"
schema_version: 2
source: "docs/prd/01-create-and-lease-sandboxes.md"
---
## Intent

Give hub a durable, auditable record of a caller's right to one sandbox, called a **lease**, and the REST and CLI surface to issue, inspect and end it. When hub issues a lease it fixes the sandbox's shape from a configured profile. It confirms that the secrets the profile requires exist for the workspace, without reading their values. It enforces the deployment's limit on sandboxes, and it gives the caller a bearer token that is shown exactly once and that hub keeps only as a hash. Hub's embedded gateway, lease expiry and remote providers all build on this record.

## Goals

1. A caller with an API key sends `POST /api/v1/workspaces/widgets/sandboxes` with `{"profile":"tools"}` and receives 201 and a lease. The lease is in state `leased` and carries a 48-character token starting `sbl_`, `expires_at` equal to `created_at` plus the profile's TTL, and the profile's image, network, resources and secret names.
2. The database holds only the SHA-256 of the token. No `GET` ever returns the token. After `DELETE`, the lease reads `destroyed`, and looking the token up in the lease store finds nothing.
3. Every refusal in the error table under Requirements returns the status and `error_type` listed there. A test covers each one.
4. Hub checks that required secrets exist without reading a secret value. A test plants a secret value and confirms it appears in no HTTP response, no audit event and no log line. The token appears in no response other than the `POST` that issued it, in no audit event and in no log line.
5. Each successful `POST` emits exactly one `hub.sandbox.lease` audit event, and the event carries the workspace.
6. `afc sandbox create|list|get|destroy` work against the API. `--token-file` writes the token to a file with mode 0600 and leaves it out of stdout.
7. `make test` covers all of this offline. No container engine, no provider and no gateway is needed.

## Non-goals

- **Creating, attaching to or destroying a box.** These belong to the `sandbox_gateway` spec: the `gateway_url` field of a lease, `[sandbox].listen` and `external_url`, the `Authenticate` callback, the `leased → active` and `leased → failed` transitions, a second `Create` being refused, `DELETE` and the client's `Close` destroying the box, and the `hub.sandbox.create`, `hub.sandbox.failed` and `hub.sandbox.destroy` events. This spec's `DELETE` ends the lease and revokes the token. It emits no audit event of its own.
- **Expiry.** These belong to the `sandbox_expiry` spec: `lease_timeout` and a `lease_expires_at` field, the `expired` state, the reaper, ending leases whose `expires_at` has passed, the 24-hour listing retention with pruning of old rows, and the sandbox metrics.
- **The remote `gateway` provider.** This belongs to the `sandbox_gateway_provider` spec.
- **The `[sandbox]` section, profile validation, the two scopes, `GET /sandbox-profiles` and `afc sandbox profiles`.** These are delivered by `sandbox_profiles`. This spec only consumes them.
- **What happens to leases when a workspace is archived or deleted.** Lease rows are not changed by either operation (see Design Decisions).
- **Changing a lease after it is issued**: no renewal, no `PATCH` and no re-issue of a token.
- Per-workspace or per-user quotas, list pagination cursors, conditional GET (ETag), and an `afc` command for the admin-only list.

## Background

The input PRD (`docs/prd/01-create-and-lease-sandboxes.md`) asks hub to hand callers a lease: a token good for one sandbox, whose contents hub decides. It has been split into five specs, and this is the second. `sandbox_profiles` delivers a validated deployment-wide profile set, a disabled state that makes the sandbox routes answer 404 with `error_type: sandboxes_disabled`, the scopes `sandboxes:read` and `sandboxes:write`, and an `afc sandbox` parent command. No lease, token or lease table exists yet.

What this spec reuses from the code:

- **Workspace ownership.** `wsaccess.AuthorizeWorkspace(db, auth, slug)` in `internal/wsaccess/wsaccess.go` returns `WorkspaceAccess{Slug, OwnerID, Status, CloneStatus}`, or a status and a message. A workspace the caller does not own answers 404. Admin tokens may act on any workspace.
- **Auth helpers.** The steering reference is `internal/workspace/auth.go`: standalone `isAdmin`, `isPAT` and `hasScope` functions taking `*apikit.AuthInfo`. Only PATs are checked against scopes.
- **Handler and route style.** `internal/merge/api.go` registers `RegisterMergeRoutes(api *echo.Group, cfg MergeAPIConfig)` and checks the PAT scope (403) before looking up the workspace. A mutating endpoint on a non-active workspace gets 400 `workspace is not active`. Lists come back as a bare JSON array capped at 200 rows. `ResolveSubmittedBy` attributes admin tokens as `"admin"`.
- **Secrets.** `secrets.Store.ListSecrets(ownerType, ownerID)` returns key names and timestamps and never values. `GetSecretValue` compares keys with `UPPER(key)`. In `internal/secrets/handlers.go`, `handleResolvedWorkspaceVars` resolves workspace > org > user, with the workspace's `org_id` used only while the workspace owner is still a member of that org (`isOrgMember`).
- **Audit.** `audit.Emitter.Emit(ctx, HubEvent{...})` swallows store errors. Event-type constants live in `internal/audit/types.go`. A nil emitter skips emission (`docs/api.md`, Hub Audit Events).
- **Timestamps.** `apikit.NowUTC()` and `apikit.FormatUTC(t)` produce RFC 3339 UTC strings. These strings are compared in SQL elsewhere, for example `deleted_at < ?` in `internal/carrypatch/wire.go`.
- **Deployment.** `deploy/deployment.yaml` runs one replica with the `Recreate` strategy against one SQLite file, so a lock inside the process is enough to serialise lease issuance.

## Requirements

### 1. The lease record

Hub keeps leases in a new SQLite table, `sandbox_leases`, created at every start whether or not sandboxes are enabled, in the same way as `secrets.InitSchema`. A lease records:

- `id`: `sbx_` followed by 32 lowercase hex characters (16 bytes from `crypto/rand`).
- The workspace slug and the profile name.
- `purpose`.
- `state`: the column allows all five lifecycle states (`leased`, `active`, `destroyed`, `expired`, `failed`) so later specs need no migration. Only `leased` and `destroyed` are reachable in this spec.
- The token's SHA-256 as lowercase hex. It is unique and nullable.
- A snapshot of the profile taken at issue: image, network, cpus, memory_mb, pids, disk_mb, require_landlock, the required and optional secret names, and the profile's fixed environment.
- The caller's labels.
- The TTL in seconds and `expires_at`.
- `created_by`: the caller's user ID, or `"admin"` for an admin token.
- `created_at` and `updated_at`.
- Nullable `sandbox_id`, `error`, `ended_at` and `end_reason`.

Timestamps are written with `apikit.FormatUTC`. Hub never stores a secret value or a plaintext token. The fixed environment is stored for `sandbox_gateway` to build the box from, and no API response returns it.

### 2. Issuing a lease: `POST /api/v1/workspaces/:slug/sandboxes`

Checks run in this order:

1. Authentication.
2. The feature is enabled; if not, 404 `sandboxes_disabled`.
3. A PAT must hold `sandboxes:write`; if not, 403.
4. `AuthorizeWorkspace`; on failure, 404.
5. The workspace must be `active`; if not, 400.

The body is a JSON object with the optional fields `profile`, `purpose`, `ttl` and `labels`. An empty body counts as `{}`. A field outside these four is refused with 400 naming the field, so a caller cannot believe it set an image or an environment.

- **profile**: an exact, case-sensitive profile name. When the field is absent, hub uses the profile `sandbox_profiles` marks as default. An unknown name is refused with 400 listing the known profile names in configuration order. An absent profile when no default exists is also refused with 400 listing them.
- **ttl**: a Go duration string, defaulting to the profile's TTL. It must be at least 1 minute and at most the profile's TTL. Otherwise hub answers 400, and when the value is too long the message names the profile's TTL. `expires_at` is `created_at + ttl`.
- **purpose**: at most 64 characters (runes) with no control characters, defaulting to `""`. Otherwise 400.
- **labels**: an object of at most 16 string pairs. Each key has 1–63 characters from `[A-Za-z0-9._-]`, starts and ends with a letter or digit, and is not `lease` or `workspace` in any case. Each value has at most 256 bytes and no control characters. A violation answers 400 naming the label key. Labels are stored as given. Mapping them to provider labels is `sandbox_gateway`'s job.

On success hub answers 201. The body is the lease representation from Requirement 6 plus `token`. This is the only response that ever carries the token.

### 3. The required-secret check

Before the lease is stored, hub confirms that every name in the profile's required `secrets` exists in at least one scope of the workspace's resolution chain. The chain, in order:

1. The `workspace` scope (owner ID = slug).
2. The `org` scope (owner ID = the workspace's `org_id`), included only when `org_id` is set and the workspace owner is a member of that org (`org_members`).
3. The `user` scope of the workspace owner, never of the caller.

Names match case-insensitively, as `GetSecretValue` does. The check reads key names only, through `secrets.Store.ListSecrets`, and never calls `GetSecretValue`. Optional secrets are not checked.

If any required name is missing, hub answers 422 with `error_type: secret_missing`. The message names every missing key in profile order and lists the scope types actually searched. No lease is stored. A secrets-store error answers 500 and no lease is stored. The chain is computed by one helper so that `sandbox_gateway` resolves values in the same order.

### 4. Capacity: `max_sandboxes`

`[sandbox].max_sandboxes` is an integer of at least 1 and defaults to 16 when absent. Any other value stops hub at start with a message naming `[sandbox].max_sandboxes`. This spec owns the key: if `sandbox_profiles` does not already decode it, this spec adds it.

A lease counts against capacity while its state is `leased` or `active` and its `expires_at` is in the future. When the count has reached `max_sandboxes`, `POST` answers 409 with `error_type: sandbox_capacity`, naming the limit. The count and the insert happen under one lock inside the process and in one transaction. Concurrent `POST`s can therefore never exceed the limit.

### 5. The lease token

The token is `sbl_` followed by 44 characters: 33 bytes from `crypto/rand`, encoded with `base64.RawURLEncoding`, for 48 characters in all. Hub stores only the token's SHA-256. If token generation fails, hub answers 500 and stores nothing.

The lease store provides a lookup from a presented token to its lease. The lookup returns the lease only when the token is well formed, its hash matches, and the lease is in state `leased` or `active`. Every other case returns "not found". This is the seam `sandbox_gateway`'s authenticator uses.

Revoking a token means moving its lease to a terminal state and clearing the stored hash in the same update. After revocation, no lookup can succeed again.

### 6. Reading leases

- `GET /api/v1/workspaces/:slug/sandboxes` returns a JSON array of the workspace's leases, newest first, capped at 200. By default it returns leases in `leased` or `active`. `?all=true` includes terminal leases. A value of `all` that `strconv.ParseBool` cannot parse answers 400.
- `GET /api/v1/workspaces/:slug/sandboxes/:id` returns one lease. An ID that is malformed, unknown, or belongs to another workspace answers 404 `sandbox lease not found`.

Both endpoints need `sandboxes:read` or `sandboxes:write` for a PAT (403 otherwise) and apply the same disabled and ownership rules as `POST`. They work on archived workspaces.

The lease representation has these fields:

- `id`, `workspace`, `profile`, `purpose`, `state`
- `expires_at`
- `image`, `network`
- `resources`: `{cpus, memory_mb, pids, disk_mb}`
- `require_landlock`
- `secrets` and `optional_secrets`: arrays of names, always present
- `labels`: an object, always present
- `created_by`, `created_at`, `updated_at`
- `sandbox_id`, `error`, `ended_at`, `end_reason`: `null` until set

It never includes `token`, the token hash, or environment values.

### 7. Ending a lease: `DELETE /api/v1/workspaces/:slug/sandboxes/:id`

The endpoint applies the same disabled, scope (`sandboxes:write`) and ownership rules as `POST`, and works on archived workspaces. For a lease in `leased` or `active`, hub sets the state to `destroyed`, sets `ended_at` to now and `end_reason` to `api`, and clears the token hash, all in one conditional update. It answers 204.

For a lease already in a terminal state, hub answers 204 and changes nothing. An unknown ID, or an ID belonging to another workspace, answers 404.

All lease endings go through one function, so that `sandbox_gateway` can add box destruction and the destroy event there.

### 8. The admin list: `GET /api/v1/sandboxes`

This endpoint is for admin tokens only. Any other credential gets 403. It returns every workspace's leases in the shape of Requirement 6, newest first, capped at 200, with the same `?all=true` behaviour. When the feature is disabled it answers 404 `sandboxes_disabled`.

### 9. Audit and confidentiality

Each successful `POST` emits `hub.sandbox.lease` (constant in `internal/audit/types.go`) with these fields:

- `ResourceType`: `sandbox`
- `ResourceID`: the lease ID
- `Action`: `lease`
- `Workspace`: the slug
- `ActorID` and `ActorType`: taken from the credential, as merge events do
- `Metadata`: `lease_id`, `profile`, `purpose`, `ttl` (a Go duration string), `secret_names` and `optional_secret_names`

A nil emitter skips emission. A failed emit is logged with `slog.Error` and does not change the 201.

No response, event or log line carries a secret value or the fixed environment's values. The token appears only in the 201 body of the `POST` that issued it.

Errors:

| Status | `error_type` | When |
|---|---|---|
| 400 | — | malformed JSON, unknown body field, unknown or missing profile, bad `ttl`, `purpose` or `labels`, non-active workspace on `POST`, bad `all` |
| 401 | — | unauthenticated |
| 403 | — | PAT lacking the scope; a non-admin credential on `GET /sandboxes` |
| 404 | `sandboxes_disabled` | feature off |
| 404 | — | workspace missing or not owned; lease not found |
| 409 | `sandbox_capacity` | `max_sandboxes` reached |
| 422 | `secret_missing` | a required secret exists in no searched scope |
| 500 | — | database or token-generation failure |

### 10. CLI

These subcommands are added under the existing `afc sandbox` command. Each prints one JSON document on stdout and uses apikit's exit codes.

- `afc sandbox create <slug> [--profile P] [--purpose T] [--ttl D] [--label k=v]... [--token-file PATH]` sends the `POST`. `--label` may repeat. A label without `=`, or a repeated key, exits with code 2 before any request is sent. `--ttl` is passed to the server unparsed.
  - Without `--token-file`, the command prints the whole lease, token included, and warns on stderr that the token will not be shown again.
  - With `--token-file`, the command opens the file before the request: it is created or truncated, then set to mode 0600 with an explicit chmod. If the file cannot be opened, the command exits with code 2 and sends no request. After the `POST`, it writes the token with no trailing newline and prints the lease without `token`. If the write fails, it sends a best-effort `DELETE` for the new lease and exits non-zero with a message naming the file.
- `afc sandbox list <slug> [--all]` sends the `GET` list, adding `?all=true` with `--all`.
- `afc sandbox get <slug> <id>` sends the `GET` for one lease.
- `afc sandbox destroy <slug> <id>` sends the `DELETE` and prints `{"status":"destroyed","id":"<id>"}`, with a confirmation on stderr.

## Design Decisions

1. **The new code lives in `internal/sandboxes`, the package `sandbox_profiles` creates.** The lease is the same domain. A second package would need the profile set across a package boundary for no gain.
2. **The schema is created at every start, even when the feature is off.** This follows `secrets.InitSchema`. Turning sandboxes off and on again keeps the history, and the table costs nothing.
3. **The state column allows all five states from the start.** SQLite cannot alter a CHECK constraint, and the later specs need `active`, `failed` and `expired`.
4. **The profile is snapshotted onto the lease at issue, fixed environment included.** The input says the box's spec is "computed when the lease was issued", so a restart with an edited profile does not change an issued lease. The environment is operator configuration, not a secret, and no API returns it.
5. **The secret check at `POST` uses names only (`ListSecrets`) and never reads a value.** The check then cannot leak what it never holds, and it settles the input's spec-time question of a check at `POST` in favour of yes.
6. **The org tier is used only while the workspace owner is a member of the workspace's org, whoever the caller is.** This is the rule `handleResolvedWorkspaceVars` already applies, and secrets resolve by the owner's scopes (input §11).
7. **The 422 names every missing key and the scope types searched, not the owner's or the org's identifiers.** One round trip shows the whole problem without disclosing who the owner is.
8. **Leases past `expires_at` do not count against `max_sandboxes`.** Until `sandbox_expiry` lands, nothing moves such a lease out of `leased`, so a crashed caller would otherwise use up capacity for good. A lease past its expiry cannot be used, because the gateway refuses it and the box's TTL kills the box.
9. **Capacity is serialised with a lock inside the process plus one transaction.** Hub runs as one replica on one SQLite file, and a deferred SQLite transaction alone lets two concurrent requests both pass the count.
10. **Unknown body fields are refused.** "A caller says which profile; hub says what is in it": a silently ignored `image` or `env` would mislead the caller.
11. **`ttl` must be at least 1 minute.** A box cannot usefully start and run in less, and the floor rules out zero, negative and sub-second values in a single rule.
12. **The label keys `lease` and `workspace` are reserved.** The input forwards labels as `hub.` plus the key next to hub's own `hub.lease` and `hub.workspace`, and these two keys would collide with them.
13. **The token is 33 random bytes in RawURL base64, and only its SHA-256 is stored.** That produces exactly the input's `sbl_` plus 44 characters. With 264 bits of randomness, an unsalted hash is safe to store and quick to look up by index.
14. **Revocation clears the hash as well as ending the lease.** A future change to the lookup's state filter can then never revive a revoked token.
15. **Lease IDs use `crypto/rand` hex with an `sbx_` prefix, not UUIDs.** The input specifies the prefix. Hex is unambiguous in paths and labels.
16. **A `DELETE` on a terminal lease answers 204.** Retries and cleanup scripts are idempotent, and the original `end_reason` is kept.
17. **This spec's `DELETE` emits no audit event.** The split gives `hub.sandbox.destroy`, with its `reason` values and `sandbox_id`, to `sandbox_gateway`, which owns every destroy path.
18. **`gateway_url` and `lease_expires_at` are not part of this spec's lease.** Their configuration (`external_url`, `lease_timeout`) belongs to later specs, and a field that is always empty would mislead callers.
19. **`POST` on a non-active workspace answers 400 `workspace is not active`.** This matches merge, the closest mutating per-workspace endpoint. `GET` and `DELETE` still work, so an archived workspace's leases can be cleaned up.
20. **Archiving or deleting a workspace leaves its lease rows alone.** Deleting rows inside `deleteWorkspace` would orphan live boxes once `sandbox_gateway` exists, and it would break every workspace test schema. A slug reused after deletion shows the old leases until `sandbox_expiry` prunes them. This is accepted, because leases hold no secrets or tokens.
21. **`max_sandboxes` defaults to 16.** That is the input's example value, and a default keeps a minimal `[sandbox]` section valid.
22. **A PAT scope failure (403) is reported before workspace lookup (404), and a non-admin on `GET /sandboxes` gets 403.** This follows merge and the input's acceptance criterion 7. There is no resource to enumerate.
23. **`--token-file` opens the file before the `POST` and rolls the lease back if the write fails.** No lease is ever created whose token the caller has lost, and the token never falls back to stdout.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `sandbox_profiles` (`.specs/25_sandbox_profiles`) | depends on | Uses its validated profile set and default profile, its disabled state (404 `sandboxes_disabled`), the `sandboxes:read` and `sandboxes:write` scopes it registers, its `[sandbox]` config struct (to which `max_sandboxes` may be added), its route registration in `internal/sandboxes`, and its `afc sandbox` parent command |
| `07_secrets_variables` | depends on | Reads secret key names with `secrets.Store.ListSecrets`, and mirrors the org-membership rule used for workspace resolution |
| `01_workspaces` | depends on | Ownership through `wsaccess.AuthorizeWorkspace`, and workspace `status`, `owner_id` and `org_id` |
| `18_hub_audit_query` | modifies | Adds the `hub.sandbox.lease` event-type constant to `internal/audit/types.go` and emits it through `audit.Emitter` |

## Verified External API

apikit is a sibling checkout (`replace github.com/txsvc/apikit => ../apikit`) outside this repository, so its source could not be read. The signatures below come from call sites in hub and are **unverified**.

| Symbol | Signature as used | Call site |
|---|---|---|
| `apikit.AuthInfo` | struct with `CredentialType string`, `UserID string`, `Permissions []string` | `internal/merge/api_test.go:179` |
| `apikit.GetAuthInfo` | `func(c echo.Context) *AuthInfo` (nil when unauthenticated) | `internal/merge/api.go` |
| `apikit.SetAuthInfo` | `func(c echo.Context, info *AuthInfo)` | test middleware, errata `01_apikit_auth_exports.md` |
| `apikit.WriteAPIError` | `func(c echo.Context, code int, msg string) error` | `internal/merge/api.go:263` |
| `apikit.WriteAPIErrorWithType` | `func(c echo.Context, code int, msg, errorType string) error` | `internal/audit/auth.go:160`, `internal/merge/api.go` |
| `apikit.NowUTC` | `func() string` | `internal/audit/store.go:91` |
| `apikit.FormatUTC` | `func(t time.Time) string` (RFC 3339 UTC; compared as strings in SQL) | `internal/carrypatch/purge_refs.go:32`, `internal/carrypatch/wire.go:250` |
| `apikit.CLIClientFromCmd` | `func(cmd *cobra.Command) (client, error)` | `internal/cli/rerere_cmd.go` |
| `client.DoRequest` | `func(ctx, method, path string, body any) (any, error)`; result decodes to `map[string]any` or `[]any` | `internal/cli/merge_cmd.go`, `internal/cli/wait.go:132` |
| `apikit.CLIPrintResult` | `func(cmd *cobra.Command, result any) error` | `internal/cli/rerere_cmd.go` |
| `apikit.CLIHandleError` | `func(cmd *cobra.Command, err error) error` | `internal/cli/rerere_cmd.go` |
| `apikit.NewCLIError` | `func(code int, msg string) error` (code 2 for usage errors) | `internal/cli/cli_helpers.go` |

This spec calls no agentkit symbol. The standard library covers `crypto/rand`, `crypto/sha256`, `encoding/base64` (`RawURLEncoding`), `encoding/hex` and `encoding/json` (`Decoder.DisallowUnknownFields`).
