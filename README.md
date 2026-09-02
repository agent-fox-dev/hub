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
and workspace-scoped tokens.

The hub binary includes a built-in git smart HTTP server that exposes
workspace repositories at `/git/<org>/<slug>.git` for clone, fetch, and push
operations.

## Getting Started

### Prerequisites

- Go 1.26+
- CGO enabled (`CGO_ENABLED=1`) — required by the DuckDB audit storage driver
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
| [API Reference](docs/api.md) | REST API endpoints, authentication, request/response schemas |
| [CLI Reference](docs/cli.md) | `afc` commands, flags, and configuration |
| [Server Configuration](docs/configuration.md) | `config.toml` reference and environment variables |
| [Permissions](docs/permissions.md) | Permission model, scopes, and access control |
| [Carry-Patch Workflow](docs/carry_patch_workflow.md) | Guide to maintaining fork patches with automated rebuild |

## Development

```sh
make check            # lint + tests
make test             # tests only
make lint             # go vet
make build-container  # build container image via podman
make hub-reset        # reset data and first-boot
make hub-run          # run the server locally
make hub-runc         # run the af-hub container
make clean            # remove build binaries and container image
```

The web UI scaffold (Vite + React + TypeScript) lives in `web/`:

```sh
make web-dev    # start dev server with API proxy
make web-build  # production build
make web-lint   # lint frontend
```

## License

See [LICENSE](LICENSE) for details.
