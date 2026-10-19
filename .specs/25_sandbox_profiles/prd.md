---
spec_id: "25"
spec_name: "sandbox_profiles"
title: "Sandbox provider and profiles"
status: "active"
created_at: "2026-10-19T16:20:27.48165Z"
updated_at: "2026-10-19T16:20:27.48165Z"
intent_hash: "9f095130b0883559770d29a33eeb94e78533b0c4fc92b7491027f92652db372b"
schema_version: 2
source: "docs/prd/01-create-and-lease-sandboxes.md"
---
## Intent

Give hub a validated, deployment-wide definition of where sandboxes run and what shape they take: one provider chosen in hub's configuration and a set of named profiles (image, resources, network, lifetime, the names of the secrets to inject and fixed environment), checked against what the provider can honour before hub serves a request, and published read-only to callers so they can choose a profile without ever seeing a secret value. Every later sandbox capability (leases, hub's gateway, expiry, a remote provider) builds on this definition.

## Goals

1. With a valid `[sandbox]` section and a reachable Podman (or Docker) engine, hub starts, and `GET /api/v1/sandbox-profiles` returns every configured profile with its image, resources, network, TTL, the names of its secrets and the names of its fixed environment variables, and no secret or environment value.
2. Each configuration fault listed under Requirements (unknown key, missing provider, unsupported provider, duplicate profile name, two default profiles, a `network` outside `network_modes`, a `ttl` over `max_ttl`, an invalid secret name, a profile the provider's capabilities reject, an unreachable engine) stops hub at start with a message that names the profile and the field.
3. Without a `[sandbox]` section hub starts exactly as it does today, and the sandbox routes answer 404 with `error_type: sandboxes_disabled`.
4. A PAT without `sandboxes:read` or `sandboxes:write` gets 403 from the profile listing. API keys and admin tokens are allowed. Both scopes can be granted to PATs from this scope on.
5. `afc sandbox profiles` prints the deployment's profiles as one JSON document.
6. `make test` covers all of this offline. Tests inject AgentKit's `local` provider or a test double that implements `sandbox.Provider`, so no container engine is needed.

## Non-goals

- **Leases and the lease REST API** (`POST/GET/DELETE /workspaces/:slug/sandboxes`, `GET /sandboxes`, lease tokens, `max_sandboxes`, the `hub.sandbox.lease` event, `afc sandbox create|list|get|destroy`). These are in the spec `sandbox_leases`.
- **Hub's embedded gateway** (`[sandbox].listen`, `external_url`, TLS, the wrapping backend, secret injection when the box is created, the `hub.sandbox.create`, `hub.sandbox.failed` and `hub.sandbox.destroy` events). These are in the spec `sandbox_gateway`, which depends on the two AgentKit `gatewayd` additions.
- **Expiry and reaping** (`lease_timeout`, `reap_interval`, destroying boxes at expiry and at shutdown, keeping terminal leases visible, metrics). These are in the spec `sandbox_expiry`.
- **The remote `gateway` provider** (`gateway_url`, `gateway_token`). This is in the spec `sandbox_gateway_provider`.
- **The AgentKit changes themselves** (`gatewayd.Config.Authenticate`, `gatewayd.PrincipalFromContext`). They belong to the agentkit-go repository and this scope does not need them.
- Creating, listing or destroying any sandbox. This scope builds the provider and validates against it, but never asks it to create a box.
- Defining profiles through the API, per-workspace or per-org profiles, or reloading the configuration while hub runs. Profiles change only when the file is edited and hub is restarted.
- Building, pulling or checking images.
- Showing the provider's name, its options or deployment-level limits through the API.
- Conditional GET or ETags on the profile listing.

## Background

The agent-fox service runs triage and fix agents. It holds only a hub credential and gets everything else from hub. The one thing it cannot get today is a place to run. The source PRD (`docs/prd/01-create-and-lease-sandboxes.md`) asks hub to create sandboxes on one configured provider, using shapes that hub controls, and to inject the secrets itself. That PRD covers five scopes that can be built and tested separately. This spec is the first. It establishes the configuration, the provider and the profiles, and the other four scopes depend on it.

What exists in the code today:

- **Configuration.** `cmd/af-hub/main.go` calls `apikit.LoadConfig()`. The result has the `Server`, `Database`, `Workspace` and OAuth sections. hub has no configuration section of its own. Per `docs/configuration.md`, apikit reads `$XDG_CONFIG_HOME/config.toml` when `XDG_CONFIG_HOME` is set and `./config.toml` otherwise. `github.com/BurntSushi/toml` v1.6.0 is already a direct dependency. Its only current use is in `internal/cli/credential_cmd.go` (`toml.Decode(os.ExpandEnv(...), &cfg)`).
- **Secret key rules.** `internal/secrets/validation.go` exports `ValidateKey(key string) error`: the key is non-empty, at most 255 characters, contains only alphanumerics and `_`, and does not start with a digit. Secret keys are case-insensitive in the store (`GetSecretValue` compares `UPPER(key)`).
- **Permissions.** Each hub package exports a `Permissions()` (or `XxxPermissions()`) function that returns `[]apikit.Permission{{Resource, Action}}`. `main.go` collects these into `extraPerms` and passes them to `workspace.MountWorkspaceHandlers`, which registers them through `apikit.Server.MountHandlers`. Following the steering, only PATs are scope-checked (`slices.Contains(auth.Permissions, "resource:action")`), using standalone helpers that take `*apikit.AuthInfo` (see `internal/workspace/auth.go`).
- **Errors.** Handlers reply with `apikit.WriteAPIError(c, code, msg)` or, when there is a machine-readable type, `apikit.WriteAPIErrorWithType(c, code, msg, errorType)`. The known types are listed in `docs/api.md` under "Error Response Schema".
- **CLI.** `internal/cli/workspace_cmd.go` `BuildRootCommand()` adds each command group (`WorkspaceCmd()`, `RerereCmd()`, …). Commands call `apikit.CLIClientFromCmd`, `client.DoRequest(ctx, method, apiPath(...), body)` and `apikit.CLIPrintResult`. `internal/cli` imports no hub `internal/` package, and `afc` is built with `CGO_ENABLED=0`.
- **Sibling dependency pattern.** apikit is pulled in with `replace github.com/txsvc/apikit => ../apikit`. `containers/hub/Containerfile` copies it from a `--build-context apikit=../apikit`, the `build-hub-container` target in the `Makefile` passes that context, and `.github/workflows/inc-build-hub.yaml` and `build-hub-cli.yaml` clone apikit next to the repository.
- **AgentKit.** agentkit-go is not in `go.mod`, and nothing in the repository refers to it. This scope adds it.
- **Existing sandbox code.** The repository has no sandbox code. `containers/sandbox/Containerfile` builds a development image that a profile may name. It is unrelated code.

## Requirements

**R1. Reading the section.** At start, hub reads a `[sandbox]` table, and any `[[sandbox.profiles]]` tables, from the same `config.toml` apikit loads. The path is `$XDG_CONFIG_HOME/config.toml` when that variable is set, otherwise `./config.toml`. If the file is missing or has no `[sandbox]` table, the feature is off: hub starts normally and logs one info line saying sandboxes are disabled. In every string value of the section, `${NAME}` is replaced by the value of environment variable `NAME`, or by an empty string when it is unset. A bare `$` is left as it is. A key under `[sandbox]` or `[[sandbox.profiles]]` that hub does not know refuses start, and the message names the full key path, for example `sandbox.profiles[1].memroy_mb`. A file that cannot be read or decoded refuses start with the decode error. Hub validates this section after `apikit.Bootstrap`, so first-boot admin-token generation never depends on the sandbox configuration, and before any queue, worker or HTTP route is set up.

**R2. Deployment keys.** This scope recognises these keys under `[sandbox]`:
- `provider` is required when the section is present. `"podman"` is the only value accepted in this scope. `"gateway"` refuses start with a message saying the gateway provider is not supported by this build. Any other value refuses start and lists the accepted values.
- `engine_binary` defaults to `"podman"`; `"docker"` also works. `runtime` and `userns` default to `""`. Hub passes all three to the Podman provider unchanged. If the provider rejects a value, hub refuses start with the provider's error prefixed by `sandbox:`.
- `max_ttl` is a Go duration string. It defaults to `"4h"` and must be greater than zero.
- `network_modes` is a list of non-empty, unique strings. It defaults to `["none"]`.

**R3. Profile validation.** At least one `[[sandbox.profiles]]` entry is required. Every rule below is checked for every profile. A failure names the profile (by `name`, or by its index when `name` is missing) and the field.
- `name` is required, matches `^[a-z0-9][a-z0-9_-]{0,62}$`, and is unique across profiles.
- `default` is a boolean. At most one profile may set it. Having no default profile is valid.
- `image` is required and must not be empty.
- `cpus` is a number of 0 or more. `memory_mb`, `pids` and `disk_mb` are integers of 0 or more. In each case 0 means the provider's default.
- `network` defaults to `"none"` and must be one of `network_modes`.
- `ttl` is required. It is a Go duration string, must be greater than zero, and must be no more than `max_ttl`.
- Every entry of `secrets` and of `optional_secrets` passes `secrets.ValidateKey`. Names compared without regard to case must not repeat within a list or across the two lists. Reserved names such as `GIT_PAT` are allowed.
- Every key of `env` passes `secrets.ValidateKey`. No `env` key may equal, ignoring case, a name in that profile's `secrets` or `optional_secrets`.
- `require_landlock` is a boolean and defaults to `false`.

Hub collects every validation failure and reports them together in one start-up error, not just the first one.

**R4. Provider construction and capability check.** When the static validation passes, hub builds the Podman provider from `engine_binary`, `runtime` and `userns` and probes it once by calling `List` with a 10-second timeout. If construction or the probe fails, hub refuses start, naming `sandbox.provider` and `sandbox.engine_binary` and including the provider's error. Every `network_modes` entry must be one of the network modes in the provider's capabilities. Each profile is turned into a `sandbox.Spec`: image, the profile's fixed `env` only, network, resources, `TTL` equal to the profile's `ttl`, and `require_landlock`. That spec must pass `sandbox.CheckSpec` against the provider's capabilities. A refusal names the profile and includes the error text. If AgentKit's CPU field is a whole number, a fractional `cpus` value refuses start, naming the field. Hub keeps the provider and the validated profiles in memory for the later scopes. This scope never creates a sandbox.

**R5. Permission scopes.** Hub registers `sandboxes:read` and `sandboxes:write` with apikit on every start, whether or not the feature is on, so PATs can be issued with them ahead of time. For PATs, `sandboxes:write` implies `sandboxes:read`. API keys and admin tokens are not scope-checked. This scope only reads, so only the read check is applied. `sandboxes:write` is used from `sandbox_leases` on.

**R6. Profile listing.** `GET /api/v1/sandbox-profiles` checks in this order:
1. No credential: 401.
2. Feature off: 404 with `error_type: sandboxes_disabled` and the message `sandboxes are not enabled on this hub`.
3. A PAT with neither `sandboxes:read` nor `sandboxes:write`: 403.

Otherwise it returns 200 with `{"profiles": [...]}`, sorted by name. Each element has `name`, `default`, `image`, `network`, `ttl_seconds` (integer), `resources` (`cpus`, `memory_mb`, `pids`, `disk_mb`), `secrets`, `optional_secrets`, `env_keys` and `require_landlock`. The three name lists are sorted in ascending order. The response is built from the validated profiles in memory, with no provider call and no database read, so it keeps working if the engine becomes unreachable after start. The listing does not depend on a workspace: every authorised caller sees the same list.

**R7. No values leave hub.** The profile listing, start-up log lines and validation errors never contain a secret value or an `env` value. This scope never reads a secret value. Errors about `env` name the key only. The start-up log line for an enabled feature gives the provider name and the profile names, and nothing else from the section.

**R8. `afc sandbox profiles`.** A new `afc sandbox` command group, added in `BuildRootCommand`, has one subcommand in this scope: `profiles`, which takes no arguments. It sends `GET /sandbox-profiles` and prints the response as one JSON document on stdout using `apikit.CLIPrintResult`. Errors, including the disabled 404, go through `apikit.CLIHandleError` and exit with apikit's codes. `internal/cli` does not import the new sandbox package.

**R9. Dependency and build.** `go.mod` requires the agentkit-go sandbox module, with a `replace` to a sibling checkout at `../agentkit-go`, following the apikit pattern. `containers/hub/Containerfile` copies it from an `agentkit` build context. The `build-hub-container` target in the `Makefile` passes `--build-context agentkit=../agentkit-go`. `.github/workflows/inc-build-hub.yaml` and `.github/workflows/build-hub-cli.yaml` clone it next to the repository, as they do for apikit, and `inc-build-hub.yaml` passes it as a build context. `afc` stays a static `CGO_ENABLED=0` binary and does not link agentkit.

**R10. Documentation.** In the same change, following the steering:
- `docs/configuration.md` documents `[sandbox]` and `[[sandbox.profiles]]` with every key, type, default and rule from R1–R4 that this scope implements.
- `docs/api.md` and `docs/openapi.yaml` document `GET /sandbox-profiles`, its schema and errors, and add `sandboxes_disabled` to the known error types.
- `docs/cli.md` documents `afc sandbox profiles`.
- `docs/permissions.md` adds the two scopes to the registry tables, the implied-permission table and the complete list.
- `docs/architecture.md` describes the new package and how the provider is built at start.
- `README.md` lists the `../agentkit-go` sibling checkout as a build prerequisite.
- `make doc` is run. No tests are written about the docs.

## Design Decisions

1. **Split the source PRD into five specs: `sandbox_profiles`, `sandbox_leases`, `sandbox_gateway`, `sandbox_expiry` and `sandbox_gateway_provider`, in that order.** The source covers configuration, a lease API, a gRPC listener with a wrapping backend, a reaper and a second provider. Each can be built and tested on its own, and together they are far beyond one spec's 10 requirements. Leases are separated from the gateway because only the gateway depends on the pending AgentKit seams.
2. **The configuration lives in a struct owned by hub, decoded from the same file, not in apikit's `Config`.** The source recommends this, and the sandbox belongs to hub's domain. It also keeps the sibling apikit unchanged.
3. **The config path is found with apikit's documented search order: `$XDG_CONFIG_HOME/config.toml`, else `./config.toml`.** The hub code calls no apikit function that exposes the resolved path. The path search is the only part re-derived, and it follows `docs/configuration.md` exactly.
4. **A missing file or a missing `[sandbox]` table turns the feature off; a table without `provider` refuses start.** A table with no provider is far more likely to be a mistake than a deliberate choice.
5. **`provider = "gateway"` is refused in this scope with a "not supported by this build" message.** Building and probing a remote gateway is `sandbox_gateway_provider`'s job. Refusing clearly is safer than accepting keys that do nothing.
6. **Unknown keys refuse start.** A mistyped resource or secret key in a security boundary must not be silently dropped. Later scopes add their own keys (`listen`, `max_sandboxes`, `lease_timeout`, …) to the recognised set.
7. **`${NAME}` is expanded in each string value after decoding, and a bare `$` is literal.** This matches the `${VAR}` syntax already documented for OAuth. Expanding the whole file the way `credential_cmd.go` does could also change other sections, or break TOML when a value contains quotes.
8. **Durations are decoded as strings and parsed with `time.ParseDuration`.** The error can then name the exact field, and the behaviour does not depend on the TOML library's duration support.
9. **`network_modes` defaults to `["none"]` and a profile's `network` defaults to `"none"`.** A sandbox has no network unless the operator explicitly allows it.
10. **`max_ttl` defaults to 4h and `ttl` is required on every profile.** A deployment-wide ceiling is safe to default. Each profile's lifetime is a deliberate choice, so the operator must state it.
11. **Having no default profile is valid; having two is an error.** The source only forbids two defaults. With no default, later scopes require the request to name a profile.
12. **Resource values of 0 mean the provider's default.** The source uses this for `disk_mb`. Applying it to every resource keeps the behaviour consistent.
13. **Secret and `env` names reuse `secrets.ValidateKey`, and collisions are compared ignoring case.** Profile secret names must be keys the secrets store can hold, and the store compares keys without regard to case.
14. **The startup probe is a single `List` call with a 10-second timeout, run after static validation.** It shows the engine is reachable without creating anything. Static errors are reported first, without waiting on the engine.
15. **Validation runs after `apikit.Bootstrap`.** Generating the admin token on first boot exits the process, and must keep working when podman is missing or the sandbox section is wrong.
16. **All validation failures are reported together.** An operator fixing a profile file should see every problem in one restart.
17. **The listing shows only `env` key names, never values, and does not show the provider.** `${VAR}` expansion can put an operator's credential into `env`, and the source says callers never see the provider.
18. **`ttl_seconds` is an integer.** JSON clients can read it without parsing a duration.
19. **The new package is `internal/sandboxes`.** This avoids clashing with AgentKit's `sandbox` package name, which hub imports alongside it, and follows the plural resource naming of `internal/secrets`.
20. **The routes are always registered, and answer 404 `sandboxes_disabled` when the feature is off.** Callers and `afc` can tell "not enabled" apart from a mistyped path. Later scopes reuse the same response.
21. **When the feature is off, the disabled 404 comes before the PAT scope check.** The answer is the same for every caller and points the operator at the real cause. 401 still comes first.
22. **The two scopes are registered even when the feature is off.** PATs can be issued before an operator enables sandboxes, and the permission registry does not change with configuration.
23. **AgentKit comes in with a sibling `replace`, as the source specifies.** The build, container and CI changes follow the existing apikit pattern step for step.
24. **Tests inject the provider.** Production builds only the Podman provider from configuration. `local` is never selectable in configuration, because it runs without isolation on hub's host.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `07_secrets_variables` (archived) | depends on | Profile secret and `env` names are validated with `secrets.ValidateKey`. Later scopes resolve values with `secrets.Store.GetSecretValue`. |
| agentkit-go `sandbox` module (external repository) | depends on | `sandbox.Spec`, `sandbox.Provider`, `sandbox.CheckSpec`, capabilities, `podman.New` and, in tests, `local`. These symbols exist upstream according to the source. The AgentKit `gatewayd` additions are not needed by this scope. |

## Verified External API

**agentkit-go**: not in `go.mod` and not vendored. Every symbol below is **unverified** and taken from the source PRD. The module path is assumed to be `github.com/agent-fox-dev/agentkit-go`, with `replace => ../agentkit-go`.

| Symbol | Assumed shape | Status |
|---|---|---|
| `sandbox.Spec` | struct with `Image string`, `Env` (the box's whole environment, assumed `map[string]string`), `Network` (mode name), `Resources` (CPUs, memory MB, PIDs, disk MB), `TTL time.Duration`, `RequireLandlock bool`, `Labels map[string]string`, `Workdir string` | unverified |
| `sandbox.Provider` | interface including `Create(ctx, Spec) (Sandbox, error)`, `List(ctx) (…, error)`, `Destroy(ctx, id) error`, `Capabilities() Capabilities`, `Reap(ctx) …` | unverified |
| `sandbox.Capabilities` | struct including `NetworkModes` (list of mode names) | unverified |
| `sandbox.CheckSpec` | `func CheckSpec(Spec, Capabilities) error`, returning an error that names the offending field | unverified |
| `podman.New` | `func New(podman.Options) (sandbox.Provider, error)`; `Options{Binary, Runtime, UserNS string}` | unverified |
| `local.Unisolated` | constructor for an unisolated test provider, used only in tests | unverified |

**apikit** (sibling `../apikit`, outside the repository). The following were verified from existing hub call sites:
- `apikit.LoadConfig()`, whose result has `.Database.Path` and `.Workspace.Path`.
- `apikit.Permission{Resource, Action string}`.
- `apikit.GetAuthInfo(echo.Context) *apikit.AuthInfo`, with fields `CredentialType`, `UserID`, `Permissions []string`, `KeyID`.
- `apikit.SetAuthInfo(c, *AuthInfo)`.
- `apikit.WriteAPIError(c, code int, msg string) error` and `apikit.WriteAPIErrorWithType(c, code int, msg, errorType string) error`.
- `(*apikit.Server).APIGroup() *echo.Group` and `MountHandlers(db, perms...)`.
- CLI: `apikit.CLIClientFromCmd(cmd)`, `client.DoRequest(ctx, method, path, body)`, `apikit.CLIPrintResult(cmd, result)`, `apikit.CLIHandleError(cmd, err)`, `apikit.NewCLIError(code, msg)`.
- An apikit function that exposes the resolved config file path: **NOT FOUND** in hub's call sites. The documented search order is assumed. If apikit does export such a function, use it instead (steering: never reimplement apikit).

**github.com/BurntSushi/toml v1.6.0**:
- `toml.Decode(data string, v any) (toml.MetaData, error)`: verified by its use in `internal/cli/credential_cmd.go`.
- `toml.MetaData.Undecoded() []toml.Key` and `toml.Key.String()`: the library's standard v1 API, not used in the repository.
