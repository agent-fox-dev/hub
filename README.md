# agent-fox hub

A headless harness for spec-driven, multi-agent software development. The
harness gives each unit of work an isolated workspace with its own branch,
files, and agents, and coordinates those agents through a validated
specification package rather than ad-hoc chat.

The design is inspired by [Intent](https://www.intentapp.dev) from Augment
Code but diverges intentionally: headless instead of desktop, coordination
rebuilt on a structured spec package that freezes on approval, and all
grounding unified under a single Context abstraction.

## Architecture

The project produces two binaries:

| Binary | Purpose |
|--------|---------|
| `hub` | API server -- owns user identity, OAuth, workspaces, git hosting, merge queue, carry-patch workflows, secrets, and access control |
| `afc` | CLI client -- authenticates with the hub and manages resources |

Primary data is stored in an embedded SQLite database (modernc.org/sqlite,
pure Go). Audit logs use an embedded DuckDB database (requires CGo).
Authentication supports three credential types: admin tokens, user API keys,
and personal access tokens (PATs). A PAT can be bound to a single workspace,
which confines it to that workspace (see `docs/permissions.md`).

The hub binary includes a built-in git smart HTTP server that exposes
workspace repositories at `/git/<org>/<slug>.git` for clone, fetch, and push
operations.

Carry-patch workspaces (a fork that carries patches on top of an upstream, see
[Carry-Patch Workflow](docs/carry_patch_workflow.md)) are served by a Go module
of its own, [`carrypatch`](carrypatch/), nested in this repository. It imports
nothing of the hub; the hub binds it to its locks, access rule, audit log and
git server in `internal/cphost` (see
[ADR 05](docs/adr/05-move-carry-patch-to-its-own-module.md)).

With a `[sandbox]` section in `config.toml` the hub also hands out sandboxes. A
caller gets a lease over the REST API (`afc sandbox create`) and creates the one
sandbox it allows by connecting an AgentKit client to the **sandbox gateway**,
which the hub serves on a second port with the lease's token. The gateway
listens on `127.0.0.1:8443` unless `[sandbox].listen` says otherwise, and only
loopback may be plaintext; every lease shows the address as `gateway_url`. See
[Server Configuration](docs/configuration.md#the-sandbox-gateway).

The hub is also the forge client of its workspaces: a caller reads a workspace's
repository, issues and pull requests on GitHub or GitLab through the hub
(`afc forge status`, `afc issue list|get`, `afc pr get|state|files|checks|reviews`),
and the hub uses the git credential it already holds for the workspace, never the
caller's. The forge can tell the hub what happens on the repository in return:
`afc forge webhook set` returns the URL and the secret to enter in the forge's webhook
settings, and the hub relays the verified deliveries as `hub.forge.*` events on its event
stream. See [Forge Endpoints](docs/api.md#forge-endpoints).

## Getting Started

### Prerequisites

- Go 1.27+
- CGO enabled (`CGO_ENABLED=1`) — required by the DuckDB audit storage driver
- Sibling checkouts of [apikit](https://github.com/txsvc/apikit) at `../apikit`,
  [agentkit-go](https://github.com/agent-fox-dev/agentkit-go) at
  `../agentkit-go` and [aflib](https://github.com/agent-fox-dev/aflib) at
  `../aflib` (all three are `replace` targets in `go.mod`)
- A GitHub OAuth application (for user login)

### Build

```sh
make build
```

This builds the `hub` binary into `bin/` and installs `afc` into `$GOBIN`
(or `$GOPATH/bin`).

### Configure the server

Create a `config.toml` in the working directory (or set `XDG_CONFIG_HOME` to
control the config file location):

```toml
[server]
port = 8080
external_url = "http://localhost:8080"

[database]
path = "afhub.db"

[logging]
level = "info"

[[oauth.providers]]
name = "github"
client_id = "${GITHUB_CLIENT_ID}"
client_secret = "${GITHUB_CLIENT_SECRET}"
```

OAuth credentials support `${VAR}` env-var substitution. See
[Server Configuration](docs/configuration.md) for the full reference.

### First boot

The server can start without any flags:

```sh
bin/hub
```

To bootstrap an admin account, pass `--admin-email` on first boot. The server
generates an admin token, writes the plaintext to `admin_token` in the config
directory, and exits immediately:

```sh
bin/hub --admin-email=admin@example.com
```

Save the token, then restart without the flag:

```sh
export ADMIN_TOKEN=$(cat admin_token)
bin/hub
```

To rotate the admin token later, use `--reset-admin-token` (same
generate-and-exit behaviour).

### Authenticate with the CLI

```sh
afc login
```

This opens a browser for GitHub OAuth, exchanges the code, and saves
credentials to `~/.af/config.toml`. From here you can create workspaces,
manage API keys, and issue workspace tokens.

### Verify the server is running

```sh
curl http://localhost:8080/healthz
# {"status": "ok"}

curl http://localhost:8080/readyz
# {"status": "ready"}
```

## Documentation

| Document | Description |
|----------|-------------|
| [OpenAPI Description](docs/openapi.yaml) | Machine-readable OpenAPI 3.1 description of the full HTTP surface |
| [API Reference](docs/api.md) | REST API endpoints, authentication, request/response schemas |
| [CLI Reference](docs/cli.md) | `afc` commands, flags, and configuration |
| [Server Configuration](docs/configuration.md) | `config.toml` reference and environment variables |
| [Permissions](docs/permissions.md) | Permission model, scopes, and access control |
| [Carry-Patch Workflow](docs/carry_patch_workflow.md) | Guide to maintaining fork patches with automated rebuild |
| [Product Requirements](docs/prd/) | PRDs: sandboxes and leases (01), the forge API and webhook ingress (02) |

## Development

```sh
make help             # list all targets
make check            # fmt, vet, lint and test
make test             # tests only
make fmt              # gofmt in place
make vet              # go vet
make lint             # golangci-lint, if installed
make doc              # Go package docs into docs/generated
make build-containers # build the container images via podman
make hub-reset        # reset data and first-boot
make hub-run          # run the server locally
make hub-runc         # run the hub container
make clean            # remove build output
```

## License

See [LICENSE](LICENSE) for details.
