---
spec_id: '06'
spec_name: git_server
title: Git Server
status: draft
created_at: '2026-07-27T15:20:42.704237+00:00'
updated_at: '2026-07-27T15:20:42.704237+00:00'
owner: ''
source: docs/prd/prd5.md
schema_version: 1
---
# Internal Git Server

## Intent

Hub exposes an internal git server over HTTP so that git clients can push to and pull from workspace repositories using standard git commands (`git clone`, `git push`, `git pull`). The server implements the git smart HTTP protocol on the same port as the REST API, authenticated via hub-managed API keys and PATs.

This gives agents and developers direct git access to workspace code without depending on the upstream remote, enabling local-loop workflows where changes are committed, pushed to hub, and picked up by other agents — all without leaving the platform.

## Goals

- Implement the git smart HTTP protocol (info/refs, git-upload-pack, git-receive-pack) using go-git's `transport/server` package.
- Serve workspace repositories at `/git/<org-slug>/<workspace-slug>.git/` on the existing HTTP server port.
- Authenticate git clients via HTTP Basic auth, mapping credentials to hub API keys, PATs, and admin tokens.
- Register new `git:read` and `git:write` PAT permission scopes for fine-grained git access delegation.
- Only serve workspaces with `clone_status = "ready"` — other states return appropriate errors.
- Update `head_sha` in the database after a successful push.

## Non-goals

- **SSH protocol.** SSH-based git access (key management, separate listener) is future work.
- **Git LFS.** go-git does not support LFS. Large file storage is deferred.
- **Upstream propagation.** Pushing to the internal git server updates only the local workspace clone. Propagation to the upstream remote is not performed (the archive flow in spec 05 handles upstream sync).
- **Anonymous access.** All repositories are private. Unauthenticated requests are rejected.
- **Dumb HTTP protocol.** Only the smart HTTP protocol is supported. Dumb protocol endpoints (loose objects, pack files) are not served.
- **Repository creation via git push.** Repositories are created via the workspace API, not via `git push` to a non-existent path.
- **Separate port or virtual host.** Git endpoints live on the same port and server instance as the REST API.

## Functional Requirements

### Git smart HTTP protocol endpoints

The following endpoints implement the git smart HTTP protocol per the [git HTTP transport specification](https://git-scm.com/docs/http-protocol):

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/git/:org/:slug.git/info/refs?service=git-upload-pack` | GET | Ref advertisement for fetch/clone |
| `/git/:org/:slug.git/info/refs?service=git-receive-pack` | GET | Ref advertisement for push |
| `/git/:org/:slug.git/git-upload-pack` | POST | Handle fetch/clone data transfer |
| `/git/:org/:slug.git/git-receive-pack` | POST | Handle push data transfer |

- `:org` is the organization slug associated with the workspace.
- `:slug` is the workspace slug.
- The `.git` suffix is required (standard git URL convention).

The response content types follow the git smart HTTP protocol:
- `info/refs` for upload-pack: `application/x-git-upload-pack-advertisement`
- `info/refs` for receive-pack: `application/x-git-receive-pack-advertisement`
- `git-upload-pack` POST: `application/x-git-upload-pack-result`
- `git-receive-pack` POST: `application/x-git-receive-pack-result`

### Repository resolution

When a git request arrives, the server resolves the workspace:

1. Extract `:org` and `:slug` from the URL path.
2. Look up the workspace by slug in the database.
3. Verify the workspace's `org_id` matches an org with the given org slug. If not, return HTTP 404 (prevents enumeration).
4. Verify `clone_status = "ready"`. If not, return HTTP 404 with a descriptive error in the git protocol error format.
5. Open the repository at `<WORKSPACE_ROOT>/<slug>/trunk/` via `git.PlainOpen`.
6. Return the repository's storer to the go-git server transport.

### Authentication

Git clients authenticate via HTTP Basic auth. The server extracts credentials from the `Authorization: Basic` header:

- The **password** field contains the hub credential (API key, PAT, or admin token). The credential format (`af_key_...`, `af_pat_...`, `af_admin_...`) is self-identifying.
- The **username** field is ignored (git convention allows any value; common choices are the user's login or the literal `x-token-auth`).

If no `Authorization` header is present, the server returns HTTP 401 with the `WWW-Authenticate: Basic realm="af-hub"` header, prompting the git client for credentials.

Invalid credentials return HTTP 401. Valid credentials with insufficient permissions return HTTP 403.

### Authorization and permission scopes

Two new PAT permission scopes are registered with apikit:

| Permission | Description |
|------------|-------------|
| `git:read` | Clone and fetch from workspace repositories the PAT owner has access to |
| `git:write` | Push to workspace repositories the PAT owner has access to |

Access control matrix for git operations:

| Operation | Admin | Owner (API key) | PAT `git:read` | PAT `git:write` |
|-----------|-------|-----------------|-----------------|------------------|
| Clone/fetch | yes (any) | yes (own) | yes (own) | yes (own) |
| Push | yes (any) | yes (own) | no | yes (own) |

- `git:write` implies read access (a tool that pushes needs to fetch first).
- `git:read` and `git:write` are independent from workspace API scopes (`workspaces:read`, `workspaces:write`). A PAT can have git access without workspace API access, and vice versa.
- Non-owner, non-admin users cannot access workspace repos (same ownership model as the workspace API).

### Push behavior

When a git client pushes to a workspace repository:

1. The push updates the local clone at `<WORKSPACE_ROOT>/<slug>/trunk/`.
2. After a successful receive-pack, the server updates `head_sha` in the database to the new HEAD commit SHA.
3. No propagation to the upstream remote occurs. The local clone diverges from upstream until the next archive (which syncs to upstream per spec 05).

### Git URL format

Users clone workspace repos with:

```
git clone http://<hub-host>:<port>/git/<org-slug>/<workspace-slug>.git
```

With credentials:

```
git clone http://x-token-auth:<api-key>@<hub-host>:<port>/git/<org-slug>/<workspace-slug>.git
```

Or via git credential helper / interactive prompt.

### Error handling

| Condition | HTTP Status | Notes |
|-----------|-------------|-------|
| No `Authorization` header | 401 | `WWW-Authenticate: Basic realm="af-hub"` |
| Invalid credentials | 401 | |
| Valid credentials, no access to workspace | 404 | Anti-enumeration |
| Workspace not found | 404 | |
| Org slug does not match workspace's org | 404 | Anti-enumeration |
| `clone_status` is not `ready` | 404 | Workspace exists but is not servable |
| Push without `git:write` scope | 403 | |
| Invalid `service` query parameter on `info/refs` | 403 | Per git smart HTTP spec |
| Repository open fails (filesystem error) | 500 | |

Errors in the git protocol context use the git pkt-line error format where possible (e.g., `ERR <message>\n`) so git clients display meaningful messages.

### go-git server integration

The implementation uses go-git's `transport/server` package:

1. **Custom `Loader`**: Implements `server.Loader` to resolve workspace slugs to repository storers. The loader:
   - Receives a `transport.Endpoint` parsed from the request URL.
   - Extracts the workspace slug from the endpoint path.
   - Opens the repository via `git.PlainOpen`.
   - Returns `repo.Storer` (the `storer.Storer` interface).
   - Returns `transport.ErrRepositoryNotFound` if the workspace doesn't exist or isn't ready.

2. **Server transport**: `server.NewServer(loader)` creates a `transport.Transport` that handles upload-pack and receive-pack sessions.

3. **HTTP handlers**: Custom Echo handlers bridge HTTP requests to go-git sessions:
   - Parse the `service` query parameter from `info/refs` GET requests.
   - Create `UploadPackSession` or `ReceivePackSession` from the transport.
   - Stream request/response bodies between the HTTP connection and the go-git session.
   - Handle pkt-line framing for ref advertisement.

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 05_workspace_checkout | 6 | 1 | Requires local workspace clones and clone_status tracking |
| 04_personal_org | 3 | 1 | Requires every workspace to have an org association for URL namespacing |
| 01_workspaces | 8 | 1 | Requires workspace infrastructure |

## Technical Boundaries

- **Language:** Go (1.26+)
- **Foundation:** `github.com/txsvc/apikit` — auth middleware, permission registration.
- **Git library:** `github.com/go-git/go-git/v5` — already a dependency from spec 05.
- **Key packages:** `go-git/v5/plumbing/transport/server`, `go-git/v5/plumbing/transport`, `go-git/v5/plumbing/storer`.
- **Schema migration:** None. This spec adds no database columns. It reads existing workspace fields and updates `head_sha` on push.

## Design Decisions

1. **HTTP smart protocol only.** Hub already runs an HTTP server. Adding SSH would require a separate listener, key management, and a second authentication path — significant complexity for a secondary access method. HTTP is sufficient and is what GitHub, GitLab, and Gitea all support as a primary protocol.

2. **`/git/<org>/<slug>.git` path scheme.** GitHub-style namespacing under a `/git/` prefix keeps git endpoints cleanly separated from API endpoints. The org prefix provides natural namespacing and matches the mental model of `<owner>/<repo>`. The `/git/` prefix avoids ambiguity with existing API routes.

3. **Same port as REST API.** Running on the same port avoids firewall and config complexity. The `/git/` prefix ensures no route conflicts with `/api/v1/` endpoints.

4. **Push updates local clone only.** Propagating to upstream on every push would couple git server latency to upstream availability and add failure modes. The archive flow (spec 05) already handles upstream sync. Keeping push local enables fast local-loop workflows.

5. **Only `ready` workspaces are servable.** Serving workspaces that are still cloning, failed, or archived would expose incomplete or absent repositories. Returning 404 for non-ready workspaces is consistent with "the repo doesn't exist yet" from the git client's perspective.

6. **Separate `git:read` / `git:write` scopes.** Git access and workspace API access serve different purposes. A CI bot might need to push code (`git:write`) without needing to archive or update workspace metadata (`workspaces:write`). Separate scopes enable least-privilege delegation.

7. **`git:write` implies read.** A tool that pushes must first fetch to determine what to send. Requiring both `git:read` and `git:write` for a push-capable PAT would be a usability gap.

8. **HTTP Basic auth with credential as password.** This follows the GitHub/GitLab convention where PATs are passed as the password in HTTP Basic auth. The username is ignored (any value is accepted). This works with all git clients and credential helpers without configuration.

9. **No LFS.** go-git does not support Git LFS. Implementing a standalone LFS server (batch API, object storage) is a substantial feature orthogonal to the git transport. Deferred to a future spec.

10. **Anti-enumeration via 404.** When a workspace exists but the requester lacks access, or the org slug doesn't match, the server returns 404 instead of 403. This prevents attackers from discovering valid workspace slugs by probing — consistent with the workspace API's approach from spec 01.

