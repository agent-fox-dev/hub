# The Issue Service: A Tracker-Neutral Issue API

**Author:** [Platform Engineering]
**Date:** 2026-09-07
**Status:** Draft
**Version:** 1.3.0 — workspace-addressed, nightshift-only
**Implemented by:** `hub`
**Consumer:** `nightshift`

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Problem Statement](#2-problem-statement)
3. [Goals and Non-Goals](#3-goals-and-non-goals)
4. [Consumers](#4-consumers)
5. [Concepts](#5-concepts)
6. [API Specification](#6-api-specification)
7. [Capability Model](#7-capability-model)
8. [Implementations](#8-implementations)
9. [Non-Functional Requirements](#9-non-functional-requirements)
10. [Conformance Suite](#10-conformance-suite)
11. [Delivery Plan](#11-delivery-plan)
12. [Open Questions](#12-open-questions)
13. [Appendix A: Mapping from `afissues.protocol`](#appendix-a-mapping-from-afissuesprotocol)
14. [Appendix B: Tracker Divergences](#appendix-b-tracker-divergences)

---

## 1. Executive Summary

The Issue Service API is one HTTP contract for reading and writing issues,
labels, comments and pull requests. Agent tooling speaks it; a server
implements it; the server owns whatever tracker sits behind it.

It exists so that automated agents stop carrying tracker code. Today the
`nightshift` daemon links `afissues` — 1,879 lines implementing GitHub, GitLab
and Gitea three times over — and every quirk of those three APIs is a quirk
inside the process that is supposed to be fixing bugs. Moving that behind a
service means one client, one set of semantics, and a new tracker becomes a
new server rather than a fourth implementation in the daemon.

**`hub` is the implementation.** The issue API is part of hub, served on
hub's existing `/api/v1` surface with hub's existing bearer credentials,
scopes, workspace ACL and audit taxonomy (§8.1). Issues are addressed by
workspace slug — `/api/v1/workspaces/{slug}/issues` — so they sit alongside
`patches`, `merges`, `secrets` and `runs` on the same resource, under the same
`{slug}` path parameter every other workspace sub-resource already uses. There
is no second service to deploy and no second credential to issue: a consumer
already talking to hub is already talking to the issue service.

There is no second server, in production or otherwise. The GitHub, GitLab and
Gitea logic that `afissues` carries moves into hub, and the conformance suite
(§10) is what keeps hub's implementation matching this document rather than
the other way round.

The contract is derived from `afissues.protocol.PlatformProtocol` — the same
fifteen operations — with five gaps closed (§6.3, Appendix A).

### What this is not

It is not a general tracker abstraction competing with the GitHub API. It
covers exactly what agent workflows need: find work, report progress, record
outcome. Milestones, tracker-side board features, assignees, reactions and
attachments are out of scope (§3), and adding one requires an amendment.

---

## 2. Problem Statement

### The current shape

`afissues` is a Python package implementing one `Protocol` three times:

```
afissues/protocol.py    371 lines   PlatformProtocol + 6 DTOs + NullPlatform
afissues/github.py      779 lines
afissues/gitlab.py      362 lines
afissues/gitea.py       506 lines
```

Every consumer that wants issues links all of it, along with an HTTP client, an
SSRF guard, and the credential handling for three vendors. The `nightshift`
daemon imports it, holds `GITHUB_PAT` / `GITLAB_TOKEN` / `GITEA_TOKEN`, and
constructs a platform through a 279-line factory that branches on config.

Four problems follow, and they are the requirements for this document.

**P-1 — Tracker quirks live in the agent.** GitLab numbers issues by `iid`,
returns `"opened"` where GitHub returns `"open"`, and closes with
`{"state_event": "close"}` rather than `{"state": "closed"}`. Those three facts
are currently inside the daemon's dependency tree. They are tracker facts and
belong to whatever talks to the tracker.

**P-2 — The protocol has gaps that its consumers work around.** `IssueResult`
carries no `state`, so a consumer checking whether an issue was closed reads
`getattr(fresh, "state", "open")` and silently always gets `"open"`.
`get_issue_timeline` is not on the protocol at all and is reached by
`getattr(platform, "get_issue_timeline", None)`. List operations do not
paginate. Each gap is invisible at the call site.

**P-3 — Credentials spread.** Every host running an agent needs a tracker
token with write access to issues. A service concentrates that into one place
with one audit trail.

**P-4 — There is no contract to test against.** `PlatformProtocol` is a Python
`Protocol`; conformance is whatever the three implementations happen to agree
on. There is no suite a fourth implementation could run.

### Why a service rather than a shared library

A library still links tracker code into every consumer, still spreads
credentials, and still requires every consumer to be written in the library's
language. The `nightshift` rewrite is in Go and `afissues` is Python; a service
is the boundary that makes that irrelevant.

---

## 3. Goals and Non-Goals

### Goals

**G1** — One HTTP contract covering the operations agent workflows need:
create, read, list, update, close, comment on and label issues; read pull
request state, checks and reviews.

**G2** — Tracker-neutral semantics. A conforming server behaves identically
whether it is backed by GitHub, GitLab, Gitea, or its own database.

**G3** — Honest capability reporting. A server that cannot do pull requests
says so, and a consumer discovers that at startup rather than at first use.

**G4** — A conformance suite that any candidate implementation runs, covering
every endpoint, the idempotency rules, pagination, capabilities and each
documented error code.

**G5** — One implementation: hub. The spec is written to be implementable by
others, but none is planned and none is required.

**G6** — Close the five `PlatformProtocol` gaps (§6.3) rather than porting
them forward.

### Non-Goals

**NG1** — Not a general-purpose tracker API. Milestones, assignees,
tracker-side boards (GitHub Projects and equivalents), reactions,
attachments, issue templates and search are out of scope.

**NG2** — Not a write path for pull requests beyond creation. Merging,
closing, review submission and branch management are not exposed; code
operations are plain git against hub's built-in git server, and integration
work is hub's existing merge queue (`/api/v1/workspaces/{slug}/merges`) and
carry-patch rebuild endpoints — not this contract.

**NG3** — Not a sync engine. The service is a facade over a tracker or a
store; it does not reconcile two trackers or maintain a mirror.

**NG4** — Not a notification system in v1. Consumers poll. An event stream is
recorded as an open question (§12, Q-2).

**NG5** — Not an identity provider. Tokens are issued by the implementing
server's existing auth: hub PATs, API keys and admin tokens, with an
`issues:read` / `issues:write` scope pair registered the same way every other
hub scope is (`IssuesPermissions() []apikit.Permission`, collected into
`extraPerms` in `cmd/af-hub/main.go`).

---

## 4. Consumers

There is one consumer: **`nightshift`**.

| Consumer | Uses |
|---|---|
| **`nightshift` daemon** | poll by label, get, comment, label, close, PR state/checks/reviews, create PR |

`nightshift` is therefore not merely the first consumer but the whole of the
requirement set, and the contract is shaped by it end to end. In particular
`exclude_label` (§6.2) exists because its poll must drop terminal-state issues
server-side, and `Issue.state` is required because its dispatch guard depends
on it. Where this document says "a consumer", it means `nightshift`; where it
states a rule more generally, that is so a second consumer would not need the
contract changed, not a claim that one exists.

**Deployment shape.** A `nightshift` agent operates on exactly one workspace
and authenticates with a workspace-scoped PAT, the same credential it already
uses for telemetry ingestion (`.specs/17_audit_storage_ingestion/prd.md`,
"Workspace-Scoped Tokens"). That PAT carries `audit:write` + `sessions:write`
today and gains `issues:read` + `issues:write` for this contract. Because the
token is bound to the workspace, the `{slug}` in the path is not a free
parameter for it: a mismatch is `403` with `error_type: workspace_mismatch`,
hub's existing behaviour on every workspace-scoped route.

Any capability not on this table is not a requirement. No other tool, skill or
UI is a consumer of this contract, and none is planned; adding one is an
amendment to this section, not an assumed extension.

---

## 5. Concepts

**Workspace.** The unit of addressing. Hub has no concept of a "project": its
single central entity is the *workspace*, a git-repository-scoped execution
context owned by one user and associated with one organization
(`internal/workspace`, `docs/openapi.yaml` → `Workspace`). A workspace is
named by its `slug`, which is globally unique, immutable, and the primary key
of the `workspaces` table — the organization is a display and git-routing
namespace, not part of uniqueness. This contract adds an issue collection to
the workspace; it does not introduce a new addressable entity.

Every endpoint in §6 is therefore a workspace sub-resource under `{slug}`,
exactly like `patches`, `rebuilds`, `merges`, `secrets`, `vars` and `runs`
already are. Different workspaces may have their issues backed by different
trackers, which is why capabilities are reported per workspace (§7), not per
server.

**Workspace lifecycle.** A workspace has `status` (`active` | `archived`).
Issue writes against an archived workspace are refused with `409` and
`error_type: workspace_archived`, consistent with the rest of hub's API;
reads remain available. `clone_status` and `sync_status` describe the git
clone and do not gate issue operations — issues are tracker state, not
working-tree state, and must stay readable while a clone is failed or
syncing.

**Issue number.** Workspace-scoped and assigned by the backing tracker.
Consumers treat it as opaque within a workspace and never as globally unique.
Two workspaces backed by the same upstream repository will report the same
numbers; nothing in this contract deduplicates them.

**Capability.** A named feature a workspace may or may not have, reported by
`GET /workspaces/{slug}/capabilities` (§7). Two families exist. *Issue
capabilities* (`pull_requests`, `checks`, `reviews`, `relationships`) gate
endpoints in this contract: a gated operation returns `capability_unsupported`
when the capability is absent. *Workspace capabilities* (`carry_patch`,
`merge_queue`, `upstream_sync`, `git_hosting`, …) describe the rest of the
workspace and are reported here so a consumer can discover in one call what
the workspace can do; they are not gated by this contract and are enforced by
their own endpoints. §7 defines both.

**Trust boundary.** Issue titles, bodies and comments are attacker-controlled
in the general case — anyone who can file an issue can write them. The service
transports them verbatim and does not sanitize; sanitizing is the consumer's
responsibility at the point of use, because only the consumer knows whether the
text is about to become part of a model prompt. This is stated so no
implementer assumes the other side is doing it.

---

## 6. API Specification

Normative. "MUST", "SHOULD" and "MAY" carry their usual meaning.

Base path: `{endpoint_url}/api/v1`, hub's existing mount point
(`server.mount_point`, `containers/hub/config.toml`). Endpoint paths below are
written relative to it, so `/workspaces/{slug}/issues` is served at
`/api/v1/workspaces/{slug}/issues`. All request and response bodies are
`application/json; charset=utf-8` with snake_case keys. All timestamps are
RFC 3339 in UTC.

The path parameter is named `slug` and carries hub's existing `Slug` schema
(`docs/openapi.yaml` → `components.schemas.Slug`). There is no `/projects`
family and none is added: hub addresses workspaces flatly at
`/api/v1/workspaces/{slug}`, with tenancy expressed by the parallel
`/user/…`, `/orgs/{slug}/…` and `/workspaces/{slug}/…` prefix families
rather than by nesting an owner segment.

### 6.1 Transport, auth and versioning

- **REQ-IS-1.1** — Authentication is `Authorization: Bearer <token>`. A
  request without a valid token MUST return `401` with code `unauthorized`.
- **REQ-IS-1.2** — A token carries an authorization scope over workspaces. A
  request for a workspace the token cannot access MUST return `404`
  (`workspace_not_found`), never `403` — a distinguishable `403` reveals
  that a workspace exists. This is hub's existing anti-enumeration rule on
  workspace CRUD and applies unchanged.
- **REQ-IS-1.2a** — A *workspace-scoped* PAT is bound to one workspace. A
  request whose `{slug}` is a workspace the token is not bound to MUST return
  `403` with `error_type: workspace_mismatch`, matching hub's existing
  behaviour on workspace-scoped routes. The anti-enumeration rule of
  REQ-IS-1.2 does not apply here: the caller already knows which workspace it
  is bound to, so no existence is revealed.
- **REQ-IS-1.3** — Hub's existing unauthenticated `GET /version` (at the root,
  not under `/api/v1`) gains a `protocol` field:
  `{"protocol": "1.0", "implementation": "hub", "version": "…"}`. Consumers
  use `protocol` to refuse an incompatible server.
- **REQ-IS-1.4** — Hub's existing root-level `GET /healthz` (liveness) and
  `GET /readyz` (readiness) require no auth and return `200` or `503`.
  `readyz` gains reachability of the backing tracker as a readiness input.
- **REQ-IS-1.5** — TLS is required in any deployment reachable off-host.

### 6.2 Endpoints

Required operations first; capability-gated operations are marked.

---

#### `GET /workspaces`

- **REQ-IS-2.0** — This contract adds no workspace-listing endpoint. Hub
  already serves `GET /api/v1/workspaces` (scope `workspaces:read`), and a
  consumer discovers addressable workspaces there. Defining a second listing
  that returned a different set for the same token would be a bug with two
  places to fix.

---

#### `GET /workspaces/{slug}/capabilities`

**Response `200`** — see §7. Requires `workspaces:read`.

---

#### `POST /workspaces/{slug}/issues`

Create an issue.

**Headers** — `Idempotency-Key` (optional; see REQ-IS-3.3)
**Body** — `{"title": "…", "body": "…", "labels": ["af:fix"]}`
`title` is required and MUST be non-empty; `body` and `labels` are optional.

**Response `201`** — an `Issue` (§6.3).
**Errors** — `400 invalid_request`, `404 workspace_not_found`,
`409 workspace_archived`, `422 label_not_found`.

---

#### `GET /workspaces/{slug}/issues`

List issues.

**Query parameters**

| Name | Repeatable | Default | Meaning |
|---|---|---|---|
| `label` | yes | — | issue MUST carry every value given |
| `exclude_label` | yes | — | issue MUST carry none of the values given |
| `state` | no | `open` | `open` \| `closed` \| `all` |
| `sort` | no | `created` | `created` \| `updated` |
| `direction` | no | `asc` | `asc` \| `desc` |
| `limit` | no | `50` | 1–200 |
| `cursor` | no | — | opaque, from a prior response |

- **REQ-IS-2.1** — `label` and `exclude_label` MUST be applied server-side.
  Returning a superset for the client to filter is non-conforming: the whole
  point is that a consumer can exclude terminal-state issues without paying to
  fetch them.
- **REQ-IS-2.2** — Results MUST be ordered by `sort`/`direction` with issue
  number as a stable tiebreak, so a page boundary cannot drop or duplicate an
  issue.

**Response `200`** — `{"issues": [Issue], "next_cursor": "…"|null, "has_more": bool}`

---

#### `GET /workspaces/{slug}/issues/{number}`

**Response `200`** — an `Issue`. **Errors** — `404 issue_not_found`.

---

#### `PATCH /workspaces/{slug}/issues/{number}`

Update an issue. Body MAY contain `title`, `body`, `state`. Absent fields are
unchanged.

- **REQ-IS-2.3** — A server SHOULD support optimistic concurrency via
  `If-Unmodified-Since` against the issue's `updated_at`, returning `412
  precondition_failed` on conflict. A consumer that omits the header accepts
  last-write-wins.

**Response `200`** — the updated `Issue`. **Errors** — `404`, `412`, `400`.

---

#### `POST /workspaces/{slug}/issues/{number}/close`

Close an issue, optionally posting a comment in the same call.

**Body** — `{"comment": "…"}` (optional)

- **REQ-IS-2.4** — Closing an already-closed issue MUST return `200` and MUST
  NOT post the comment a second time when an `Idempotency-Key` is supplied.
  Without the key, the comment is posted; the close remains idempotent.

**Response `200`** — the `Issue`. **Errors** — `404 issue_not_found`.

---

#### `GET /workspaces/{slug}/issues/{number}/comments`

- **REQ-IS-2.5** — Comments MUST be returned in ascending chronological order.
  Consumers scan for the most recent machine-readable marker and depend on this.

**Query** — `limit`, `cursor`.
**Response `200`** — `{"comments": [Comment], "next_cursor": …, "has_more": bool}`

---

#### `POST /workspaces/{slug}/issues/{number}/comments`

**Headers** — `Idempotency-Key` (optional)
**Body** — `{"body": "…"}`, required and non-empty.
**Response `201`** — a `Comment`. **Errors** — `404`, `400 invalid_request`.

---

#### `PUT /workspaces/{slug}/issues/{number}/labels/{label}`

Add a label.

- **REQ-IS-2.6** — Idempotent: `204` whether or not the label was already
  present.
- **REQ-IS-2.7** — A label that does not exist on the workspace MUST return
  `422 label_not_found`. It MUST NOT be created implicitly — silent creation
  turns a typo into a permanent label.

**Response `204`.**

---

#### `DELETE /workspaces/{slug}/issues/{number}/labels/{label}`

Remove a label. **REQ-IS-2.8** — Idempotent: `204` whether or not the label
was present.

---

#### `GET /workspaces/{slug}/issues/{number}/relationships`

*Capability: `relationships`.*

Returns declared or inferred links between issues in this workspace.

**Response `200`** — `{"relationships": [Relationship]}`
**Errors** — `501 capability_unsupported`.

---

#### `GET /workspaces/{slug}/labels`

**Response `200`** — `{"labels": [Label]}`

---

#### `PUT /workspaces/{slug}/labels/{name}`

Create or update a label.

**Body** — `{"color": "12ec39", "description": "…"}`; `color` is six hex
characters without a leading `#`.

- **REQ-IS-2.9** — Idempotent. Creating a label that exists MUST return `200`
  and update colour/description; it MUST NOT error. This replaces the current
  practice of pattern-matching `"422"` or `"already exists"` out of an error
  string.

**Response `200`** (existed) or `201` (created).

---

#### `POST /workspaces/{slug}/pulls`

*Capability: `pull_requests`.*

**Body** — `{"title": "…", "body": "…", "head": "fix/42-slug", "base": "develop"}`, all required.
**Response `201`** — a `PullRequest`.
**Errors** — `501 capability_unsupported`, `422 invalid_ref`, `409 pull_request_exists`.

---

#### `GET /workspaces/{slug}/pulls/{number}`

*Capability: `pull_requests`.* **Response `200`** — a `PullRequest`.

---

#### `GET /workspaces/{slug}/pulls/{number}/checks`

*Capability: `checks`.* **Response `200`** — `{"checks": [Check]}`

---

#### `GET /workspaces/{slug}/pulls/{number}/reviews`

*Capability: `reviews`.* **Response `200`** — `{"reviews": [Review]}`

---

### 6.3 Schemas

```jsonc
// Issue
{
  "number": 42,
  "state": "open",                    // "open" | "closed"   REQUIRED
  "title": "…",
  "body": "…",                        // "" when empty, never null
  "labels": ["af:fix"],               // [] when none, never null
  "author": "octocat",
  "html_url": "https://…",            // "" when the backend has no web view
  "created_at": "2026-09-07T12:00:00Z",
  "updated_at": "2026-09-07T12:30:00Z"
}

// Comment
{ "id": "c_1", "body": "…", "author": "nightshift", "created_at": "…" }

// Label
{ "name": "af:fix", "color": "12ec39", "description": "…" }

// PullRequest
{ "number": 7, "state": "open", "merged": false,
  "head_ref": "fix/42-slug", "head_sha": "abc123…",
  "base_ref": "develop", "html_url": "https://…" }

// Check
{ "name": "test", "status": "completed",       // queued|in_progress|completed
  "conclusion": "failure",                     // null while not completed
  "output_title": "…", "output_summary": "…",  // "" when absent, never null
  "details_url": "https://…" }

// Review
{ "author": "reviewer",
  "state": "CHANGES_REQUESTED",   // APPROVED|CHANGES_REQUESTED|COMMENTED|DISMISSED|PENDING
  "body": "…", "submitted_at": "…" }

// Relationship
{ "kind": "blocked_by",           // blocked_by | blocks | duplicates | relates_to
  "from_issue": 41, "to_issue": 42,
  "source": "tracker" }           // tracker | body_reference

// Error
{ "error": { "code": "issue_not_found", "message": "…", "detail": {} } }
```

- **REQ-IS-3.1** — Optional string fields MUST be `""` rather than `null`, and
  optional arrays `[]` rather than `null`. A consumer must not have to
  distinguish absent from empty.
- **REQ-IS-3.2** — Unknown fields in a response MUST be ignored by consumers;
  servers MAY add fields without a version bump. Removing or retyping a field
  is a breaking change and requires a `protocol` major bump.

**Five deliberate changes against `PlatformProtocol`** (Appendix A has the
full mapping):

1. **`Issue.state` is required.** `IssueResult` has no such field, so a
   consumer's closed-check silently never fires (P-2).
2. **`Comment.id` is a string.** GitHub comment ids are global integers,
   GitLab's are note ids scoped to its own project entity; an opaque
   string is the only
   representation that survives both.
3. **List endpoints paginate.** `list_issues_by_label` returns everything, so
   a large backlog is one unbounded response.
4. **Relationships are a declared, capability-gated endpoint** rather than an
   undeclared method discovered by reflection.
5. **`Issue.updated_at` is present**, so a consumer can detect an edited issue
   without storing its body.

### 6.4 Cross-cutting semantics

- **REQ-IS-3.3 — Idempotency.** `POST` endpoints accept an `Idempotency-Key`
  header. A server MUST return the original response for a repeated key within
  a retention window of at least 24 hours, scoped to the workspace and
  endpoint. Note that this is a new convention for hub, which today expresses
  idempotency with a client-supplied body `id` or an `if_not_exists` flag and
  has no `Idempotency-Key` header anywhere; §12 Q-6 records the choice.
  `PUT` and `DELETE` on labels are idempotent by construction and need no key.
- **REQ-IS-3.4 — Pagination.** Cursors are opaque, single-use-forward, and
  MUST NOT be an offset: an issue closed between pages must not shift the
  window. A cursor a server did not issue MUST return `400 invalid_cursor`.
- **REQ-IS-3.5 — Rate limiting.** `429 rate_limited` MUST carry `Retry-After`
  in seconds. A server proxying a tracker SHOULD surface the tracker's own
  budget rather than exhausting it and returning `upstream_error`. Because a
  `nightshift` agent polls on a fixed interval, a `429` it cannot interpret
  becomes a hot loop against the tracker; `Retry-After` is what prevents that.
- **REQ-IS-3.6 — Upstream failure.** A tracker failure the server cannot
  classify MUST be `502 upstream_error` with the tracker's status in
  `error.detail`. It MUST NOT be flattened into `404` or an empty list — a
  consumer that cannot distinguish "no issues" from "could not ask" will treat
  an outage as an empty queue — for `nightshift`, an empty queue means "no
  work tonight", so a flattened error is silent inaction rather than a
  visible failure.

### 6.5 Error codes

These are the contract's machine-readable classifications. Hub carries them in
the `error_type` field of its existing apikit error envelope
(`{"error": {"code": <http status>, "message": "…", "error_type": "…"}}`,
`docs/openapi.yaml` → `components.schemas.Error`) — see §8.1 and §12 Q-7.

| Code | HTTP | Meaning |
|---|---|---|
| `unauthorized` | 401 | missing or invalid token |
| `workspace_not_found` | 404 | unknown workspace, or not visible to this token |
| `workspace_mismatch` | 403 | workspace-scoped token addressed a different workspace |
| `workspace_archived` | 409 | write attempted against an archived workspace |
| `issue_not_found` | 404 | unknown issue in this workspace |
| `pull_request_not_found` | 404 | unknown pull request |
| `label_not_found` | 422 | label does not exist on the workspace |
| `invalid_request` | 400 | malformed body or parameter |
| `invalid_cursor` | 400 | cursor not issued by this server |
| `invalid_ref` | 422 | `head`/`base` does not resolve |
| `pull_request_exists` | 409 | an open PR already exists for `head`→`base` |
| `precondition_failed` | 412 | `If-Unmodified-Since` conflict |
| `capability_unsupported` | 501 | operation gated behind an unadvertised capability |
| `rate_limited` | 429 | budget exhausted; `Retry-After` set |
| `upstream_error` | 502 | backing tracker failed |

---

## 7. Capability Model

A workspace does not have one uniform feature set. It is created in a
`workspace_mode` that is immutable thereafter, it may or may not be hosted on
hub's git server, its issues may be backed by a tracker that has no concept of
a check run. A consumer that has to discover each of these by making a call
and reading the failure is a consumer that discovers them in production.

`GET /workspaces/{slug}/capabilities` answers all of it in one call.

**REQ-IS-4.1** — The response has two blocks:

```json
{
  "workspace": {
    "slug": "acme-fix",
    "status": "active",
    "workspace_mode": "carry_patch",
    "capabilities": {
      "issues": true,
      "carry_patch": true,
      "merge_queue": true,
      "upstream_sync": true,
      "git_hosting": true,
      "secrets": true,
      "variables": true,
      "sessions": true,
      "audit": true
    }
  },
  "issues": {
    "pull_requests": true,
    "checks": true,
    "reviews": true,
    "relationships": false
  }
}
```

### 7.1 Issue capabilities

**REQ-IS-4.2** — The `issues` block gates endpoints in **this** contract.
`pull_requests`, `checks`, `reviews` and `relationships` are the only gated
operations; everything else in §6.2 is required.

**REQ-IS-4.3** — An operation gated behind an unadvertised capability MUST
return `501 capability_unsupported`, never a plausible empty result. An empty
`checks` array means "this PR has no checks"; it must not also mean "this
server cannot read checks".

**REQ-IS-4.4** — Everything not listed in `issues` is required. A server that
cannot create, list, get, update, close, comment on or label an issue is not
conforming and cannot claim partial compliance.

### 7.2 Workspace capabilities

**REQ-IS-4.5** — The `workspace.capabilities` block reports what the rest of
the workspace can do. It is **advertisement, not gating**: these capabilities
belong to hub's existing endpoints, which enforce them with their own errors,
and this contract does not put `501 capability_unsupported` in front of them.
Its purpose is that a consumer holding one workspace-scoped PAT can decide at
startup which of its workflows this workspace supports, instead of enqueuing a
rebuild against a `standard` workspace and reading a `400`.

**REQ-IS-4.6** — Every workspace capability is **derived** from state hub
already keeps, and then narrowed by the caller's own scopes (REQ-IS-4.9). None
of them is a new stored flag, and none is separately settable; a capability
that disagreed with the field it is derived from would be a second source of
truth. "Derived from" below gives the workspace-side condition; a capability is
reported `true` only when that condition holds **and** the credential can reach
its endpoints.

| Capability | Derived from | Gates | Failure without it |
|---|---|---|---|
| `issues` | this contract being served | `/workspaces/{slug}/issues/…` | — |
| `carry_patch` | `workspace_mode == "carry_patch"` | `/patches`, `/patch-status`, `/rebuild`, `/rebuilds`, `/rebuild-preview`, `/rerere`, carry-patch `/sync` | `400 workspace_mode_mismatch` |
| `merge_queue` | always true for an active workspace | `/merges`, `/rebase` | — |
| `upstream_sync` | `sync_mode != "disabled"` | `/sync`, `/reclone` | `400` on `/sync` |
| `git_hosting` | `hub_url != null` | `/git/{org}/{slug}.git/…` | clone URL unavailable |
| `secrets` | always true | `/secrets` | — |
| `variables` | always true | `/vars`, `/vars/resolved` | — |
| `sessions` | always true | `/sessions`, `/cost` | — |
| `audit` | always true | `/runs/{run_id}/…`, `/audit` | — |

**REQ-IS-4.7** — `workspace.workspace_mode` is reported verbatim as hub stores
it (`standard` | `carry_patch`) alongside the derived `carry_patch` boolean.
The mode is the fact; the boolean is the convenience. A consumer MAY branch on
either, and a server MUST NOT let them disagree.

**REQ-IS-4.8** — `capabilities` is an open map. A consumer MUST ignore keys it
does not recognise and MUST NOT treat an absent key as `false` — a key absent
because the server predates it is not the same as a capability that is off.
Adding a capability is therefore not a breaking change; removing one is.

**REQ-IS-4.9** — Workspace capabilities reflect what the caller's own
credential can reach. A capability whose endpoints the token has no scope for
is reported `false`, not `true`-and-then-`403`. A workspace-scoped `nightshift`
PAT holding `issues:*`, `audit:write` and `sessions:write` therefore sees
`secrets: false` and `variables: false`, which is the honest answer to "can I
do this?". Two credentials may thus see different `workspace.capabilities` for
the same workspace; this narrowing applies to the `workspace` block only, since
reaching the `issues` block at all already requires `issues:read`.

### 7.3 Reporting rules

**REQ-IS-4.10** — Capabilities are reported **per workspace**, because one
server fronts many workspaces with different modes and possibly different
backends. A server whose workspaces are uniform still answers per workspace.

**REQ-IS-4.11** — Issue capabilities are stable for the life of a workspace
and do not vary by credential. A server that loses one at runtime (its tracker
credential narrowed, a tracker feature disabled) SHOULD report it as lost
rather than failing individual calls.
Workspace capabilities derived from mutable state (`upstream_sync` from
`sync_mode`) change when that state changes; `carry_patch` cannot change,
because `workspace_mode` is immutable after creation.

**REQ-IS-4.12** — The endpoint MUST be cheap enough to call on every agent
start. It reads workspace row state and, for the `issues` block, the server's
own configuration for that workspace's backend; it MUST NOT probe the upstream
tracker on each call. A server that caches the `issues` block SHOULD bound the
staleness at 60 s.

---

## 8. Implementations

### 8.1 `hub` — the implementation

**REQ-IS-5.1** — Hub exposes the API under its existing `/api/v1` surface as a
workspace sub-resource at `/api/v1/workspaces/{slug}/issues`, authenticated by
its existing PATs and API keys with an `issues:read` / `issues:write` scope
pair. `{slug}` is the workspace slug — there is no separate identifier and no
mapping layer.

**REQ-IS-5.2** — Hub's authorization rules apply unchanged: a
workspace-scoped token may address only its own workspace (`403`,
`workspace_mismatch`), a workspace the token cannot see is `404` rather than
`403`, and an archived workspace refuses writes with `409`
(`workspace_archived`), consistent with the rest of hub's API.

**REQ-IS-5.2a** — The implementation follows hub's established module wiring:
a new `internal/issues` package exposing `RegisterIssueRoutes(api *echo.Group,
cfg IssuesAPIConfig)` and `IssuesPermissions() []apikit.Permission`, with the
permissions collected into `extraPerms` in `cmd/af-hub/main.go` and the routes
registered alongside the carry-patch and merge modules. Scope checks are
inline handler guards (`isPAT` / `hasScope`), matching every other hub module;
there is no permission middleware in hub to hook into.

**REQ-IS-5.2b** — `GET /api/v1/workspaces/{slug}/capabilities` (§7) is served
by the workspace module, not the issues module, because it reports the whole
workspace. It requires `workspaces:read` and asks the issues module for the
`issues` block.

**REQ-IS-5.3** — Issue mutations emit hub audit events under the existing
`hub.*` event taxonomy, so issue activity appears in the unified audit query
alongside everything else.

**REQ-IS-5.4** — Whether hub stores issues itself or proxies a tracker is an
implementation choice behind the same contract; capabilities report the
difference. The choice is made per workspace, so one hub may proxy GitHub for
one workspace and serve stored issues for another.

**REQ-IS-5.5** — Where hub proxies GitHub, its backend is a port of
`afissues/github.py`'s request shaping and response mapping, not a fresh
implementation. That code is proven against the real API and already handles
the cases Appendix B records; rewriting it from the GitHub docs would
rediscover them. The port normalises to this document's schemas (§6.3) — most
visibly `Issue.state`, which the Python DTO does not carry at all.

**REQ-IS-5.6** — Hub retains `afissues`' SSRF guard on any configured tracker
URL.

### 8.2 GitLab and Gitea

**REQ-IS-6.1** — Not supported in v1. Supporting GitLab or Gitea means hub
growing a backend for it. `afissues/gitlab.py` and `gitea.py` remain available
in af-python as the basis for that work, and Appendix B records the
divergences it must absorb. This is a scope decision, not a technical
obstacle.

---

## 9. Non-Functional Requirements

- **NFR-IS-01 — Latency.** A read served from the server's own store SHOULD
  complete within 100 ms at p95. A read proxied to a tracker is bounded by the
  tracker; the server MUST NOT add more than 50 ms at p95 over the upstream
  call.
- **NFR-IS-02 — Caching.** A proxying server SHOULD cache `GET` responses
  briefly (5–30 s) to absorb a polling consumer, and MUST NOT serve cached
  data to a read that follows its own write within the same workspace.
- **NFR-IS-03 — Rate-limit safety.** A proxying server MUST track upstream
  budget and return `429` with `Retry-After` before exhausting it, rather than
  passing through an upstream failure.
- **NFR-IS-04 — Concurrency.** Safe for concurrent requests across workspaces;
  writes to one issue are serialized.
- **NFR-IS-05 — Observability.** Structured logs with workspace slug,
  endpoint, status and upstream latency; Prometheus metrics for request
  count, latency, error codes and upstream budget remaining.
- **NFR-IS-06 — No secret leakage.** Tracker tokens never appear in responses,
  logs or `error.detail`.
- **NFR-IS-07 — Payload bounds.** Issue and comment bodies are accepted up to
  a documented limit (default 256 KiB); exceeding it is `400 invalid_request`,
  not a truncated write.
- **NFR-IS-08 — Scope.** These bind hub, the implementation. The in-process
  fake of REQ-IS-8.4 must pass the §10 conformance suite and nothing else;
  holding a test double to latency and rate-limit targets would be effort
  spent on something that never serves a request.

---

## 10. Conformance Suite

**REQ-IS-8.1** — A black-box suite runs against any candidate server given a
base URL, a token and a disposable workspace. It is the definition of
conformance; "implements the spec" means "passes the suite".

**REQ-IS-8.2** — Coverage:

| Area | Cases |
|---|---|
| Auth | missing token → 401; invisible workspace → 404 not 403; workspace-scoped PAT on a foreign slug → 403 `workspace_mismatch` |
| Issue CRUD | create → get → patch → close round trip; body/labels empty-not-null |
| Listing | `label` AND semantics; `exclude_label` NONE semantics; `state` filter; ordering; stable tiebreak |
| Pagination | full walk equals unpaginated set; forged cursor → 400; issue mutated mid-walk neither dropped nor duplicated |
| Comments | chronological order; create → list round trip |
| Labels | add/remove idempotent (204 twice); unknown label → 422; `PUT /labels` idempotent |
| Capabilities | every gated `issues` capability returns 501 when unadvertised and works when advertised; `workspace.capabilities` matches the workspace row (`carry_patch` ⇔ `workspace_mode`, `upstream_sync` ⇔ `sync_mode`); unknown keys ignored, absent key ≠ false |
| Idempotency | repeated `Idempotency-Key` returns the original response, creates nothing new |
| Errors | every code in §6.5 is reachable and carries the right HTTP status and `error_type` |
| Lifecycle | write to an archived workspace → 409 `workspace_archived`; reads still succeed |
| Upstream failure | injected tracker failure surfaces as 502, never as 404 or an empty list |

**REQ-IS-8.3** — The suite ships in the same repository as the spec and runs
in CI against hub on every change to either.

It also runs against the REQ-IS-8.4 fake, and that is not redundant. A suite
developed against exactly one implementation drifts into testing that
implementation's habits rather than the contract, and there is no way to notice
from inside. The fake has to exist anyway for consumers to test against, it is
written from this document rather than from hub's code, and it is the cheapest
second reader the contract can have. It is not a server and is not deployed;
if it and hub ever disagree, the disagreement is a defect in one of them or in
this specification, and finding out which is the point.

**REQ-IS-8.4** — An in-process fake satisfying the suite ships for
`nightshift` to test against without a network.

---

## 11. Delivery Plan

**Phase 1 — Spec, suite and fake.** This document, an OpenAPI 3.1 description
generated from it, the §10 conformance suite, and the in-process fake it and
consumers both run against. The fake is written from this document, not from
hub's code (REQ-IS-8.3), and it lets consumers build their clients while
Phase 2 is in flight.

**Phase 2 — hub.** Implement the contract on hub's existing auth, storage and
audit as `internal/issues` (REQ-IS-5.2a), extend the workspace module with
`GET /workspaces/{slug}/capabilities` (§7), and get the suite green against
hub. **This is the deliverable that unblocks the consumer.** `nightshift`
cannot poll until it lands, because hub is the implementation it is configured
against.

**Phase 3 — Consumer cutover.** `nightshift` switches to the client and
`afissues` is removed from the Python daemon's dependency set. Its GitHub logic
has by then been ported into hub, which is the one place it now lives.

---

## 12. Open Questions

**Q-1 — Does the service own issue storage, or only proxy?** Hub could store
issues natively, which would let a workspace have issues without any external
tracker. That is a larger product decision than this contract needs; the
contract works either way and capabilities report the difference.
*Recommendation: proxy first, revisit natively-stored issues after Phase 3.*

**Q-2 — Should there be an event stream?** The consumer polls today. Hub
already runs an SSE endpoint at `GET /api/v1/events` carrying `hub.*` events,
so the additive move is an issue event type on that stream rather than a new
endpoint — it would let `nightshift` react to a new `af:fix` label instead of
waiting out its poll interval, and would cut upstream rate consumption. It does
not change the polling contract. *Recommendation: defer to v1.1, design it as
an additional event type on the existing stream.*

**Q-3 — How are cross-workspace relationships expressed?** `Relationship`
carries issue numbers, which are workspace-scoped, so a dependency on an issue
in another workspace is currently inexpressible. `nightshift` only builds
graphs within one batch, and each agent is bound to a single workspace by its
PAT, so this is not blocking. *Recommendation: leave it; widen to a
slug-qualified reference only when a consumer needs it.*

**Q-4 — Should hub support GitLab and Gitea backends?** The Python
implementations exist and Appendix B records what a port must absorb; nobody
is asking for it today. *Recommendation: no in v1; the contract makes it a
contained change later, and the capability model already lets a GitLab-backed
workspace report `checks` and `reviews` as unsupported rather than
approximating them.*

**Q-5 — With one implementation and one consumer, what stops the spec becoming
a description of hub?** Nothing structural. The mitigations are the fake being
written from this document rather than hub's code (REQ-IS-8.3), and the rule
that a behaviour change lands here before it lands in hub. Both depend on
discipline rather than on a mechanism. *Recommendation: accept it. A second
server built solely to hold the spec honest serves no user and costs real
maintenance; if the spec does start drifting, that will show up as the consumer
finding hub's behaviour surprising, and a second implementation can be
reconsidered then.*

**Q-6 — Is `Idempotency-Key` the right idempotency mechanism for hub?** Hub
has no such header today: it expresses idempotency with a client-supplied
body `id` that turns a `201` into a `200`, or with an `if_not_exists` flag.
REQ-IS-3.3 introduces a header convention that would be hub's first, and it
needs a store with a 24-hour retention window that nothing in hub currently
has. *Recommendation: keep the header — it is the only mechanism that also
covers `POST /issues/{n}/close`, which has no natural client-supplied id — but
decide in Phase 2 whether the retention store is a DuckDB table or a SQLite
one, and record the divergence from hub's existing convention in
`docs/errata/`.*

**Q-7 — How do §6.5's codes fit hub's error envelope?** Hub's envelope is
`{"error": {"code": <http status int>, "message": "…", "error_type": "…"}}`,
where `code` is the numeric status and the machine-readable classification is
`error_type`. §6.3's schema shows `error.code` holding the string classifier
and adds an `error.detail` object hub does not have. These cannot both be
right. *Recommendation: hub's envelope wins — it is implemented, documented in
`docs/openapi.yaml`, and already carries `workspace_mismatch`,
`workspace_archived` and `workspace_mode_mismatch`. Fold §6.5's codes into
`error_type`, drop `error.detail` or add it to the shared envelope for every
hub endpoint rather than only this one, and amend §6.3 accordingly.*

**Q-8 — Should the capability endpoint live in this contract at all?**
`GET /workspaces/{slug}/capabilities` (§7) reports the whole workspace, most of
which predates this document. Specifying it here means an issue-service
amendment is needed to add a capability for an unrelated hub feature.
*Recommendation: implement it in the workspace module (REQ-IS-5.2b) and, once
this document is accepted, move §7.2 into the workspace spec and leave §7.1
here. The open-map rule (REQ-IS-4.8) is what makes that split safe.*

---

## Appendix A: Mapping from `afissues.protocol`

Source: `agent-fox-dev/af-python`, `packages/afissues/afissues/protocol.py`.

| `PlatformProtocol` method | Endpoint | Change |
|---|---|---|
| `create_issue` | `POST /issues` | + `Idempotency-Key` |
| `list_issues_by_label` | `GET /issues` | + `exclude_label`, + pagination |
| `get_issue` | `GET /issues/{n}` | — |
| `update_issue` | `PATCH /issues/{n}` | + `title`, `state`; + optimistic concurrency |
| `close_issue` | `POST /issues/{n}/close` | — |
| `list_issue_comments` | `GET /issues/{n}/comments` | + pagination; order now normative |
| `add_issue_comment` | `POST /issues/{n}/comments` | + `Idempotency-Key` |
| `assign_label` | `PUT /issues/{n}/labels/{l}` | idempotency now normative |
| `remove_label` | `DELETE /issues/{n}/labels/{l}` | idempotency now normative |
| `create_label` | `PUT /labels/{name}` | idempotent; no error-string matching |
| `create_pr` | `POST /pulls` | capability-gated |
| `get_pr_state` | `GET /pulls/{n}` | + `head_ref`, `base_ref`, `html_url` |
| `get_pr_checks` | `GET /pulls/{n}/checks` | + `details_url`; capability-gated |
| `get_pr_reviews` | `GET /pulls/{n}/reviews` | capability-gated |
| `close` | — | connection lifecycle is the client's |
| *(none — `getattr` probe)* | `GET /issues/{n}/relationships` | now declared and gated |
| *(none)* | `GET /workspaces/{slug}/capabilities` | replaces `getattr` sniffing; also reports workspace capabilities (§7.2) |
| `NullPlatform` | — | replaced by an unconfigured endpoint on the consumer side |

DTO changes: `IssueResult` → `Issue` (+ `state`, `author`, `created_at`,
`updated_at`); `IssueComment` → `Comment` (`id` int → string, `user` →
`author`); `PrState` → `PullRequest` (+ `head_ref`, `base_ref`, `html_url`);
`PrResult` folded into `PullRequest`; `CheckResult` → `Check` (+
`details_url`); `ReviewComment` → `Review` (`user` → `author`).

---

## Appendix B: Tracker Divergences

What a bridge must absorb so consumers never see it. Verified against the
`afissues` implementations at `agent-fox-dev/af-python@faedf22`.

**Issue state vocabulary.** GitLab uses `opened`/`closed`; GitHub and Gitea use
`open`/`closed`. `gitlab.py:28` carries `_STATE_MAP = {"open": "opened"}` and
applies it when *sending* a filter (`gitlab.py:115`, `:307`) — but never maps
the returned value back. A bridge MUST normalise in both directions.

**Issue identity.** GitLab addresses issues by `iid`, an id internal to
GitLab's own project entity (`gitlab.py:35`, `:207`), not the global `id`.
Merge requests likewise (`gitlab.py:271`, `:296`). The service's `number` is
that tracker-scoped value, surfaced under the hub workspace the tracker backs.
"Project" here is GitLab's term for the repository it hosts; it is not a hub
concept and does not appear in this contract's addressing.

**Close semantics.** GitLab closes with `{"state_event": "close"}`
(`gitlab.py:162`); GitHub and Gitea with `{"state": "closed"}`
(`github.py:744`, `gitea.py:267`).

**Comment identity.** GitLab comments are notes with ids scoped to the GitLab
project (`gitlab.py:197`); GitHub comment ids are global. Hence `Comment.id` is
an opaque string.

**Pull requests vs merge requests.** GitLab's merge requests differ in naming
and in the `state` vocabulary (`"opened"` again, `gitlab.py:276`). A bridge
presents them as `PullRequest`.

**Checks and reviews.** GitHub check-runs may return `output: null`, which
`afissues` maps to empty strings rather than nulls — behaviour this spec makes
normative (REQ-IS-3.1). GitLab pipelines and approvals do not map cleanly onto
check-runs and reviews; a GitLab bridge would most likely report `checks` and
`reviews` as unsupported rather than approximate them, which is exactly what
the capability model is for.

**Relationships.** Only GitHub's timeline API is implemented today, and only as
an undeclared method. A bridge without an equivalent reports
`relationships: false`.

**Capabilities are per workspace, not per hub.** Two workspaces on the same hub
may be backed by different trackers, so §7's `issues` block can legitimately
differ between them. Nothing in this contract lets a consumer cache one
workspace's capabilities and apply them to another.
