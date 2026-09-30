# Define Sandbox Configuration

## Intent

A sandbox (`docs/prd/18-run-agents-in-ephemeral-sandboxes.md`) is launched
from a description: which image, on which provider and gateway, with what
resources, network policy, environment, secrets, workload and timeouts.
That description should be one document, reviewable as a whole, rather than
a set of flags spread over config keys, workspace variables and request
bodies.

This PRD defines that document, `sandbox.json`, and where it comes from: a
hub-wide default file next to `config.toml`, and an optional per-workspace
document stored on the hub through the API that replaces the default
entirely. It defines validation, optional operator ceilings, and the
resolution step that turns a document plus a workspace into the concrete
environment a sandbox will receive (plain values, selected variables,
named secrets). Nothing in this PRD launches anything; it is complete when
the API can store, validate and return effective configurations and hub
code can ask for the resolved environment of a workspace.

## Goals

- One JSON schema for sandbox configuration, versioned.
- A hub default loaded from `$XDG_CONFIG_HOME/sandbox.json`, with a
  built-in fallback so the hub starts without the file.
- A per-workspace document managed through `GET`/`PUT`/`DELETE` on the
  workspace, replacing the default wholesale.
- Optional ceilings in `config.toml` that no document can exceed.
- A Go API that returns the effective document for a workspace and
  resolves its environment from the secrets and variables stores.
- `sandboxes:read` and `sandboxes:write` permission scopes, registered
  here because the configuration endpoints are the first to need them.

## Non-goals

- Reading configuration from the repository (`.af/sandbox.json` or
  similar). Configuration lives on the hub so that a push cannot change the
  next sandbox's image or resources.
- Field-level merging between the default and the workspace document.
- Per-organisation documents.
- Enforcing anything at runtime. Launch-time checks are the lifecycle
  PRD's; this PRD validates documents and ceilings.

## Current behaviour (for reference)

- Server configuration is `config.toml` loaded by `apikit.LoadConfig()`
  from `$XDG_CONFIG_HOME` (`docs/configuration.md`).
- Secrets and variables exist at user, org and workspace tiers;
  `GET /workspaces/:slug/vars/resolved` merges variables workspace > org >
  user; `secrets.Store.GetSecretValue` reads one secret at one tier;
  `ResolveCloneAuth` shows the tiered lookup pattern for secrets.
- Permission scopes are registered by passing `apikit.Permission` values
  to `MountWorkspaceHandlers`.

## Functional Requirements

### Schema

- **SC-1.** Document schema, version 1:

  ```json
  {
    "version": 1,
    "provider": "container",
    "gateway": "local",
    "image": "quay.io/agentfox/agents:latest",
    "workdir": "/opt/app-root/workspace",
    "command": ["claude", "-p", "Implement the spec in .specs/"],
    "on_exit": "stop",
    "resources": { "cpu": "2", "memory": "4Gi", "pids": 512, "disk": "10Gi" },
    "network": { "egress": "all", "allow": [] },
    "env": { "AGENT_LOG_LEVEL": "info" },
    "vars": "all",
    "secrets": ["ANTHROPIC_API_KEY"],
    "timeouts": { "boot": "5m", "max": "8h" }
  }
  ```

  | Field | Rules |
  |---|---|
  | `version` | required, `1` |
  | `provider` | `container` (default), `microvm`, `pod` |
  | `gateway` | name, default `local`; existence is checked at launch, not here |
  | `image` | required; OCI reference |
  | `workdir` | optional absolute path; default: the image's working directory |
  | `command` | optional argv array; the workload the outpost starts; absent means idle |
  | `on_exit` | `stop` (default) or `keep` |
  | `resources.cpu`, `.memory` | Kubernetes quantity syntax; defaults `2`, `4Gi` |
  | `resources.pids` | integer, default 512 |
  | `resources.disk` | optional quantity; advisory |
  | `network.egress` | `all` (default) or `restricted`; with `restricted`, `allow` lists `host[:port]` |
  | `env` | map of plain strings; keys match `^[A-Za-z_][A-Za-z0-9_]*$` and MUST NOT start with `AF_` |
  | `vars` | `"all"` (default) or a list of variable keys |
  | `secrets` | list of secret keys; default empty |
  | `timeouts.boot`, `.max` | Go durations; defaults `5m`, `8h`; `max` ≥ `boot` |

  Unknown fields are rejected (`400 sandbox_config_invalid` naming the
  field) so that typos do not silently do nothing.
- **SC-2.** The built-in default is the example above without `command`
  and with `secrets: []`.

### Sources

- **SC-3.** At startup the hub SHALL load `[sandbox] config_path`
  (default `$XDG_CONFIG_HOME/sandbox.json`). A missing file falls back to
  the built-in default with a log line; a file that fails validation or a
  ceiling aborts startup. The file is read once; changing it requires a
  restart.
- **SC-4.** A workspace document is stored in `workspace_sandbox_configs
  (workspace_slug PRIMARY KEY, document JSON, updated_at, updated_by)`.
  When present it is the effective document for that workspace, whole; the
  default is not consulted. Deleting the workspace deletes the row.
- **SC-5.** `Effective(slug) (Config, origin string, error)` SHALL return
  the workspace document with `origin = "workspace"` or the default with
  `origin = "default"`.

### Ceilings

- **SC-6.** `config.toml` MAY set:

  ```toml
  [sandbox.limits]
  max_cpu           = "8"
  max_memory        = "16Gi"
  max_pids          = 2048
  max_lifetime      = "24h"
  allowed_images    = ["quay.io/agentfox/*"]
  allowed_providers = ["container", "microvm"]
  allowed_gateways  = ["local"]
  ```

  None is set by default. A document that exceeds a set ceiling is invalid
  (`400 sandbox_config_invalid`, field named), at `PUT` time and at
  startup for the default file. `Effective` SHALL re-check the ceilings so
  a document stored before a ceiling was tightened is reported invalid
  (`ErrExceedsLimits`) rather than used.

### Environment resolution

- **SC-7.** `ResolveEnvironment(ctx, slug, cfg) (env map[string]string,
  secretKeys []string, warnings []string, error)` SHALL build the
  environment a sandbox receives: the document's `env`, then the
  workspace's resolved variables (all, or the listed keys) overriding
  `env`, then the named secrets (resolved workspace > org > user, the same
  order as variables) overriding both. A listed variable that does not
  resolve is a warning; a named secret that does not resolve is an error
  (`ErrSecretNotFound` naming the key). Any resulting key starting with
  `AF_` is dropped with a warning.
- **SC-8.** Resolution SHALL be a function of the current stores, not a
  snapshot: callers that need "what was launched" record the result
  themselves.

### API

- **SC-9.** Endpoints, requiring workspace ownership (admin bypasses):

  | Method and path | Scope | Effect |
  |---|---|---|
  | `GET /api/v1/workspaces/:slug/sandbox-config` | `sandboxes:read` | `{ "origin": "workspace" \| "default", "config": {…} }`; `?resolve=true` adds `"env_keys"`, `"secret_keys"` and `"warnings"` from SC-7 (names only, never values) |
  | `PUT /api/v1/workspaces/:slug/sandbox-config` | `sandboxes:write` | validate and store; `200` with the stored document |
  | `DELETE /api/v1/workspaces/:slug/sandbox-config` | `sandboxes:write` | remove; `204`; `404` if none |
  | `GET /api/v1/sandbox-config` | admin | the hub default and the ceilings |

  `sandboxes:write` implies `sandboxes:read`. `sandboxes:read` is
  registered as bindable in the sense of
  `docs/prd/20-issue-workspace-bound-tokens.md`; `sandboxes:write` is not.
- **SC-10.** Changes emit `hub.sandbox_config.update` and
  `hub.sandbox_config.delete` audit events (actor, workspace); the stored
  document is included with `secrets` as key names, which is all it ever
  contains.

### CLI

- **SC-11.** `afc sandbox config get <slug> [--resolve]`,
  `afc sandbox config set <slug> --file sandbox.json`,
  `afc sandbox config delete <slug>`, `afc sandbox config default`
  (admin).

### Testing

- Unit: schema validation table (every rule in SC-1, unknown fields,
  `AF_` keys), ceilings, `Effective` origins, `ResolveEnvironment`
  precedence and errors against the secrets and vars test helpers.
- Handler tests in the existing style (ownership, scopes, anti-enumeration,
  ETag on `GET`).

## Technical Boundaries

- Go; new package `internal/sandbox/config`. Quantities parsed with a
  small local parser (`k8s.io/apimachinery` is not worth the dependency
  for two fields).
- State in the existing SQLite database.
- Documentation: `docs/configuration.md` (`[sandbox] config_path`,
  `[sandbox.limits]`, the `sandbox.json` reference), `docs/api.md`,
  `docs/permissions.md` (two scopes), `docs/cli.md`.

## Dependencies

- `internal/secrets` for variable resolution and secret lookup.
- `internal/workspace` for ownership checks and the cascade on delete.

## Design Decisions

### 1. Whole-document replacement

A merge would be friendlier for one-field changes but makes the effective
configuration hard to reason about and to audit. A workspace that wants the
default plus one change copies the default. Ceilings are the one thing a
workspace cannot escape.

### 2. Stored on the hub, never read from the repository

The hub is the source of truth for workspace metadata (secrets and
variables already live there). Reading the document from the code would let
anyone who can push change resource limits, images and secret names.

### 3. Secrets by name, resolved late

The document names secrets; values are resolved when someone asks
(SC-7, SC-8). The document, its audit trail and its API responses never
contain a secret value.

## Open Questions

- Should `vars: "all"` remain the default, given it hands every workspace
  variable to every sandbox? The alternative default is an empty list with
  explicit opt-in per key.
