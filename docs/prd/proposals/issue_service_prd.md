# The Issue Service: A Tracker-Neutral Issue API

**Author:** [Platform Engineering]
**Date:** 2026-09-07
**Status:** Draft
**Version:** 1.2.0 — hub is the only implementation
**Implemented by:** `hub`
**First consumer:** Night Shift (see `docs/proposals/nightshift_go_prd.md`, D-1)

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
Night Shift daemon links `afissues` — 1,879 lines implementing GitHub, GitLab
and Gitea three times over — and every quirk of those three APIs is a quirk
inside the process that is supposed to be fixing bugs. Moving that behind a
service means one client, one set of semantics, and a new tracker becomes a
new server rather than a fourth implementation in the daemon.

**`hub` is the implementation.** The issue API is part of hub, served on
hub's existing `/api/v1` surface with hub's existing bearer credentials,
scopes, workspace ACL and audit taxonomy (§8.1). There is no second service to
deploy and no second credential to issue: a consumer already talking to hub is
already talking to the issue service.

There is no second server, in production or otherwise. The GitHub, GitLab and
Gitea logic that `afissues` carries moves into hub, and the conformance suite
(§10) is what keeps hub's implementation matching this document rather than
the other way round.

The contract is derived from `afissues.protocol.PlatformProtocol` — the same
fifteen operations — with five gaps closed (§6.3, Appendix A).

### What this is not

It is not a general tracker abstraction competing with the GitHub API. It
covers exactly what agent workflows need: find work, report progress, record
outcome. Milestones, projects, assignees, reactions and attachments are out of
scope (§3), and adding one requires an amendment.

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
SSRF guard, and the credential handling for three vendors. The Night Shift
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
language. The Night Shift rewrite is in Go and `afissues` is Python; a service
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

**NG1** — Not a general-purpose tracker API. Milestones, assignees, projects,
reactions, attachments, issue templates and search are out of scope.

**NG2** — Not a write path for pull requests beyond creation. Merging,
closing, review submission and branch management are not exposed; code
operations are plain git (see D-1 in the Night Shift PRD).

**NG3** — Not a sync engine. The service is a facade over a tracker or a
store; it does not reconcile two trackers or maintain a mirror.

**NG4** — Not a notification system in v1. Consumers poll. An event stream is
recorded as an open question (§12, Q-2).

**NG5** — Not an identity provider. Tokens are issued by the implementing
server's existing auth: hub PATs, API keys and admin tokens, with an
`issues:read` / `issues:write` scope pair.

---

## 4. Consumers

| Consumer | Uses |
|---|---|
| **Night Shift daemon** | poll by label, get, comment, label, close, PR state/checks/reviews, create PR |
| **`af-issue` skill** | create issue, list labels |
| **`af-spec`** | get issue (as a spec source) |
| **hub web UI** | list, get, comment (when hub is the implementation) |

Night Shift is the demanding consumer and the reason for most of the
contract's shape. In particular `exclude_label` (§6.2) exists because its poll
must drop terminal-state issues server-side, and `Issue.state` is required
because its dispatch guard depends on it.

---

## 5. Concepts

**Project.** The unit of addressing: one issue collection, named by a slug.
Where hub implements the service, a project corresponds one-to-one with a hub
workspace, and the two names are the same string. A server may host many
projects with different backends and therefore different capabilities — which
is why capabilities are reported per project (§7), not per server.

**Issue number.** Project-scoped and assigned by the backing tracker.
Consumers treat it as opaque within a project and never as globally unique.

**Capability.** A named optional feature (`pull_requests`, `checks`,
`reviews`, `relationships`). Required operations are always present;
capability-gated operations return `capability_unsupported` when absent.

**Trust boundary.** Issue titles, bodies and comments are attacker-controlled
in the general case — anyone who can file an issue can write them. The service
transports them verbatim and does not sanitize; sanitizing is the consumer's
responsibility at the point of use, because only the consumer knows whether the
text is about to become part of a model prompt. This is stated so no
implementer assumes the other side is doing it.

---

## 6. API Specification

Normative. "MUST", "SHOULD" and "MAY" carry their usual meaning.

Base path: `{endpoint_url}/api/v1`. All request and response bodies are
`application/json; charset=utf-8`. All timestamps are RFC 3339 in UTC.

### 6.1 Transport, auth and versioning

- **REQ-IS-1.1** — Authentication is `Authorization: Bearer <token>`. A
  request without a valid token MUST return `401` with code `unauthorized`.
- **REQ-IS-1.2** — A token carries an authorization scope over projects. A
  request for a project the token cannot access MUST return `404`
  (`project_not_found`), never `403` — a distinguishable `403` reveals that a
  project exists.
- **REQ-IS-1.3** — `GET /version` returns
  `{"protocol": "1.0", "implementation": "hub", "version": "…"}` and
  requires no auth. Consumers use `protocol` to refuse an incompatible server.
- **REQ-IS-1.4** — `GET /healthz` (liveness) and `GET /readyz` (readiness,
  including reachability of the backing tracker) require no auth and return
  `200` or `503`.
- **REQ-IS-1.5** — TLS is required in any deployment reachable off-host.

### 6.2 Endpoints

Required operations first; capability-gated operations are marked.

---

#### `GET /projects`

List projects the token may access.

**Response `200`** — `{"projects": [{"slug": "…", "name": "…"}], "next_cursor": null, "has_more": false}`

---

#### `GET /projects/{project}/capabilities`

**Response `200`** — see §7.

---

#### `POST /projects/{project}/issues`

Create an issue.

**Headers** — `Idempotency-Key` (optional; see REQ-IS-3.3)
**Body** — `{"title": "…", "body": "…", "labels": ["af:fix"]}`
`title` is required and MUST be non-empty; `body` and `labels` are optional.

**Response `201`** — an `Issue` (§6.3).
**Errors** — `400 invalid_request`, `404 project_not_found`, `422 label_not_found`.

---

#### `GET /projects/{project}/issues`

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

#### `GET /projects/{project}/issues/{number}`

**Response `200`** — an `Issue`. **Errors** — `404 issue_not_found`.

---

#### `PATCH /projects/{project}/issues/{number}`

Update an issue. Body MAY contain `title`, `body`, `state`. Absent fields are
unchanged.

- **REQ-IS-2.3** — A server SHOULD support optimistic concurrency via
  `If-Unmodified-Since` against the issue's `updated_at`, returning `412
  precondition_failed` on conflict. A consumer that omits the header accepts
  last-write-wins.

**Response `200`** — the updated `Issue`. **Errors** — `404`, `412`, `400`.

---

#### `POST /projects/{project}/issues/{number}/close`

Close an issue, optionally posting a comment in the same call.

**Body** — `{"comment": "…"}` (optional)

- **REQ-IS-2.4** — Closing an already-closed issue MUST return `200` and MUST
  NOT post the comment a second time when an `Idempotency-Key` is supplied.
  Without the key, the comment is posted; the close remains idempotent.

**Response `200`** — the `Issue`. **Errors** — `404 issue_not_found`.

---

#### `GET /projects/{project}/issues/{number}/comments`

- **REQ-IS-2.5** — Comments MUST be returned in ascending chronological order.
  Consumers scan for the most recent machine-readable marker and depend on this.

**Query** — `limit`, `cursor`.
**Response `200`** — `{"comments": [Comment], "next_cursor": …, "has_more": bool}`

---

#### `POST /projects/{project}/issues/{number}/comments`

**Headers** — `Idempotency-Key` (optional)
**Body** — `{"body": "…"}`, required and non-empty.
**Response `201`** — a `Comment`. **Errors** — `404`, `400 invalid_request`.

---

#### `PUT /projects/{project}/issues/{number}/labels/{label}`

Add a label.

- **REQ-IS-2.6** — Idempotent: `204` whether or not the label was already
  present.
- **REQ-IS-2.7** — A label that does not exist on the project MUST return
  `422 label_not_found`. It MUST NOT be created implicitly — silent creation
  turns a typo into a permanent label.

**Response `204`.**

---

#### `DELETE /projects/{project}/issues/{number}/labels/{label}`

Remove a label. **REQ-IS-2.8** — Idempotent: `204` whether or not the label
was present.

---

#### `GET /projects/{project}/issues/{number}/relationships`

*Capability: `relationships`.*

Returns declared or inferred links between issues in this project.

**Response `200`** — `{"relationships": [Relationship]}`
**Errors** — `501 capability_unsupported`.

---

#### `GET /projects/{project}/labels`

**Response `200`** — `{"labels": [Label]}`

---

#### `PUT /projects/{project}/labels/{name}`

Create or update a label.

**Body** — `{"color": "12ec39", "description": "…"}`; `color` is six hex
characters without a leading `#`.

- **REQ-IS-2.9** — Idempotent. Creating a label that exists MUST return `200`
  and update colour/description; it MUST NOT error. This replaces the current
  practice of pattern-matching `"422"` or `"already exists"` out of an error
  string.

**Response `200`** (existed) or `201` (created).

---

#### `POST /projects/{project}/pulls`

*Capability: `pull_requests`.*

**Body** — `{"title": "…", "body": "…", "head": "fix/42-slug", "base": "develop"}`, all required.
**Response `201`** — a `PullRequest`.
**Errors** — `501 capability_unsupported`, `422 invalid_ref`, `409 pull_request_exists`.

---

#### `GET /projects/{project}/pulls/{number}`

*Capability: `pull_requests`.* **Response `200`** — a `PullRequest`.

---

#### `GET /projects/{project}/pulls/{number}/checks`

*Capability: `checks`.* **Response `200`** — `{"checks": [Check]}`

---

#### `GET /projects/{project}/pulls/{number}/reviews`

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
   GitLab's are per-project note ids; an opaque string is the only
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
  a retention window of at least 24 hours, scoped to the project and endpoint.
  `PUT` and `DELETE` on labels are idempotent by construction and need no key.
- **REQ-IS-3.4 — Pagination.** Cursors are opaque, single-use-forward, and
  MUST NOT be an offset: an issue closed between pages must not shift the
  window. A cursor a server did not issue MUST return `400 invalid_cursor`.
- **REQ-IS-3.5 — Rate limiting.** `429 rate_limited` MUST carry `Retry-After`
  in seconds. A server proxying a tracker SHOULD surface the tracker's own
  budget rather than exhausting it and returning `upstream_error`.
- **REQ-IS-3.6 — Upstream failure.** A tracker failure the server cannot
  classify MUST be `502 upstream_error` with the tracker's status in
  `error.detail`. It MUST NOT be flattened into `404` or an empty list — a
  consumer that cannot distinguish "no issues" from "could not ask" will treat
  an outage as an empty queue.

### 6.5 Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `unauthorized` | 401 | missing or invalid token |
| `project_not_found` | 404 | unknown project, or not visible to this token |
| `issue_not_found` | 404 | unknown issue in this project |
| `pull_request_not_found` | 404 | unknown pull request |
| `label_not_found` | 422 | label does not exist on the project |
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

**REQ-IS-4.1** — `GET /projects/{project}/capabilities` returns:

```json
{
  "pull_requests": true,
  "checks": true,
  "reviews": true,
  "relationships": false
}
```

**REQ-IS-4.2** — Capabilities are reported **per project**, because one server
may front several backends. A server whose projects are uniform still answers
per project.

**REQ-IS-4.3** — An operation gated behind an unadvertised capability MUST
return `501 capability_unsupported`, never a plausible empty result. An empty
`checks` array means "this PR has no checks"; it must not also mean "this
server cannot read checks".

**REQ-IS-4.4** — Capabilities are stable for the life of a project. A server
that loses a capability at runtime (a token narrowed, a tracker feature
disabled) SHOULD report it as lost rather than failing individual calls.

**REQ-IS-4.5** — Everything not listed as a capability is required. A server
that cannot create, list, get, update, close, comment on or label an issue is
not conforming and cannot claim partial compliance.

---

## 8. Implementations

### 8.1 `hub` — the implementation

**REQ-IS-5.1** — Hub exposes the API under its existing `/api/v1` surface,
authenticated by its existing PATs and API keys with an `issues:read` /
`issues:write` scope pair, and maps `{project}` to a workspace slug.

**REQ-IS-5.2** — Hub's authorization rules apply unchanged: a
workspace-scoped token may address only its own workspace, and an archived
workspace returns `409`, consistent with the rest of hub's API.

**REQ-IS-5.3** — Issue mutations emit hub audit events under the existing
`hub.*` event taxonomy, so issue activity appears in the unified audit query
alongside everything else.

**REQ-IS-5.4** — Whether hub stores issues itself or proxies a tracker is an
implementation choice behind the same contract; capabilities report the
difference.

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
  data to a read that follows its own write within the same project.
- **NFR-IS-03 — Rate-limit safety.** A proxying server MUST track upstream
  budget and return `429` with `Retry-After` before exhausting it, rather than
  passing through an upstream failure.
- **NFR-IS-04 — Concurrency.** Safe for concurrent requests across projects;
  writes to one issue are serialized.
- **NFR-IS-05 — Observability.** Structured logs with project, endpoint,
  status and upstream latency; Prometheus metrics for request count, latency,
  error codes and upstream budget remaining.
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
base URL, a token and a disposable project. It is the definition of
conformance; "implements the spec" means "passes the suite".

**REQ-IS-8.2** — Coverage:

| Area | Cases |
|---|---|
| Auth | missing token → 401; foreign project → 404 not 403 |
| Issue CRUD | create → get → patch → close round trip; body/labels empty-not-null |
| Listing | `label` AND semantics; `exclude_label` NONE semantics; `state` filter; ordering; stable tiebreak |
| Pagination | full walk equals unpaginated set; forged cursor → 400; issue mutated mid-walk neither dropped nor duplicated |
| Comments | chronological order; create → list round trip |
| Labels | add/remove idempotent (204 twice); unknown label → 422; `PUT /labels` idempotent |
| Capabilities | every gated endpoint returns 501 when unadvertised, and works when advertised |
| Idempotency | repeated `Idempotency-Key` returns the original response, creates nothing new |
| Errors | every code in §6.5 is reachable and carries the right HTTP status |
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

**REQ-IS-8.4** — An in-process fake satisfying the suite ships for consumers to
test against without a network (used by Night Shift's own suite, NFR-07 there).

---

## 11. Delivery Plan

**Phase 1 — Spec, suite and fake.** This document, an OpenAPI 3.1 description
generated from it, the §10 conformance suite, and the in-process fake it and
consumers both run against. The fake is written from this document, not from
hub's code (REQ-IS-8.3), and it lets consumers build their clients while
Phase 2 is in flight.

**Phase 2 — hub.** Implement the contract on hub's existing auth, storage and
audit; suite green against hub. **This is the deliverable that unblocks
consumers.** Night Shift cannot poll until it lands, because hub is the
implementation it is configured against.

**Phase 3 — Consumer cutover.** Night Shift switches to the client; the
`af-issue` skill drops `gh issue create`; `afissues` is removed from the
Python daemon's dependency set. Its GitHub logic has by then been ported into
hub, which is the one place it now lives.

---

## 12. Open Questions

**Q-1 — Does the service own issue storage, or only proxy?** Hub could store
issues natively, which would let a workspace have issues without any external
tracker. That is a larger product decision than this contract needs; the
contract works either way and capabilities report the difference.
*Recommendation: proxy first, revisit natively-stored issues after Phase 3.*

**Q-2 — Should there be an event stream?** Consumers poll today. A
server-sent-events endpoint mirroring hub's `/api/v1/events` would let Night
Shift react to a new `af:fix` label instead of waiting out its poll interval,
and would cut upstream rate consumption. It is additive and does not change the
polling contract. *Recommendation: defer to v1.1, design it as additive.*

**Q-3 — How are cross-project relationships expressed?** `Relationship`
carries issue numbers, which are project-scoped, so a dependency on an issue in
another project is currently inexpressible. Night Shift only builds graphs
within one batch, so this is not blocking. *Recommendation: leave it; widen to
a qualified reference only when a consumer needs it.*

**Q-4 — Should hub support GitLab and Gitea backends?** The Python
implementations exist and Appendix B records what a port must absorb; nobody
is asking for it today. *Recommendation: no in v1; the contract makes it a
contained change later, and the capability model already lets a GitLab-backed
project report `checks` and `reviews` as unsupported rather than
approximating them.*

**Q-5 — With one implementation, what stops the spec becoming a description
of hub?** Nothing structural. The mitigations are the fake being written from
this document rather than hub's code (REQ-IS-8.3), and the rule that a
behaviour change lands here before it lands in hub. Both depend on discipline
rather than on a mechanism. *Recommendation: accept it. A second server built
solely to hold the spec honest serves no user and costs real maintenance; if
the spec does start drifting, that will show up as consumers finding hub's
behaviour surprising, and a second implementation can be reconsidered then.*

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
| *(none)* | `GET /projects/{p}/capabilities` | replaces `getattr` sniffing |
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

**Issue identity.** GitLab addresses issues by project-internal `iid`
(`gitlab.py:35`, `:207`), not the global `id`. Merge requests likewise
(`gitlab.py:271`, `:296`). The service's `number` is the project-scoped value.

**Close semantics.** GitLab closes with `{"state_event": "close"}`
(`gitlab.py:162`); GitHub and Gitea with `{"state": "closed"}`
(`github.py:744`, `gitea.py:267`).

**Comment identity.** GitLab comments are notes with per-project ids
(`gitlab.py:197`); GitHub comment ids are global. Hence `Comment.id` is an
opaque string.

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
