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
| `af-hub` | API server -- owns user identity, OAuth, workspaces, and access control |
| `afc` | CLI client -- authenticates with the hub and manages resources |

Data is stored in an embedded SQLite database (pure Go, no CGo). Authentication
supports three credential types: admin tokens, user API keys, and
workspace-scoped tokens.

## Getting Started

### Prerequisites

- Go 1.26+
- A GitHub OAuth application (for user login)

### Build

```sh
make build
```

This compiles both binaries into `bin/`.

### Configure the server

Create a `config.toml` in the working directory (or pass `--config <path>`):

```toml
[server]
port = 8080

[database]
path = "./data/af-hub.db"

[log]
level = "info"

[[oauth.providers]]
name = "github"
client_id = "your-github-client-id"
client_secret = "your-github-client-secret"
```

### Start the server

```sh
bin/af-hub
```

On first boot the server generates an admin token and writes the plaintext to
`admin_token` (next to the config file). Save this value and export it for
subsequent starts:

```sh
export AF_HUB_ADMIN_TOKEN=$(cat admin_token)
bin/af-hub
```

### Authenticate with the CLI

```sh
bin/afc login
```

This opens a browser for GitHub OAuth, exchanges the code, and saves
credentials to `~/.af/config.toml`. From here you can create workspaces, manage
API keys, and issue workspace tokens.

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

## Development

```sh
make check    # lint + tests
make test     # tests only
make lint     # go vet
make clean    # remove bin/
```

The web UI scaffold (Vite + React + TypeScript) lives in `web/`:

```sh
make web-dev    # start dev server with API proxy
make web-build  # production build
make web-lint   # lint frontend
```

## License

See [LICENSE](LICENSE) for details.
