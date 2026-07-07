# agent-fox hub

A headless harness for spec-driven, multi-agent software development. The
harness gives each unit of work an isolated workspace with its own branch,
files, and agents, and coordinates those agents through a validated
specification package rather than ad-hoc chat.

The design is inspired by [Intent](https://www.intentapp.dev) from Augment
Code but diverges intentionally: headless instead of desktop, coordination
rebuilt on a structured spec package that freezes on approval, and all
grounding unified under a single Context abstraction.

## Prerequisites

- **Go 1.22+** (required for building server and CLI binaries)
- **Node.js 18+** (optional, for the web frontend)

## Quickstart

### Build

Compile both `af-hub` (server) and `afc` (CLI) binaries:

```bash
make build
```

This produces `bin/af-hub` and `bin/afc`.

### Configure

Create a `config.toml` file in the directory where you will run the server.
A minimal configuration:

```toml
[server]
port = 8080
bind_address = "0.0.0.0"

[database]
path = "./data/af-hub.db"

[logging]
level = "info"
```

See [docs/configuration.md](docs/configuration.md) for the full reference.

### Run

Start the server:

```bash
bin/af-hub
```

### First boot

On the first startup (when the database has no users), `af-hub` automatically:

1. Creates an `admin` user with provider `local`.
2. Generates a cryptographic admin token (format `af_admin_<64 hex chars>`).
3. Writes the plaintext token to an `admin_token` file (mode 0600) next to
   `config.toml` and logs its path at warn level.

On subsequent starts, set the `AF_HUB_ADMIN_TOKEN` environment variable to the
plaintext token before launching:

```bash
export AF_HUB_ADMIN_TOKEN=$(cat admin_token)
bin/af-hub
```

To rotate a compromised token:

```bash
bin/af-hub --reset-admin-token
```

## Project structure

```
cmd/
  af-hub/       Entry point for the af-hub server binary
  afc/          Entry point for the afc CLI binary
internal/
  auth/         OAuth provider registry and RBAC middleware
  bootstrap/    Admin bootstrap, token generation, validation, rotation
  config/       TOML configuration loading and validation
  db/           SQLite database initialization (WAL mode, schema)
  handler/      HTTP route handlers
  logging/      Structured JSON logging setup (logrus)
  middleware/   Echo middleware (request logger)
  server/       Echo HTTP server setup and health probes
  store/        Typed CRUD operations for all database entities
docs/           Documentation
web/            React + TypeScript frontend (Vite)
```

## Testing

```bash
make test     # run all Go tests
make lint     # run go vet
make check    # run lint + tests
```

## Documentation

### System architecture

- [System architecture](architecture/README.md) — three-layer system design.

### Implementation

- [REST API Reference](docs/api.md) — complete endpoint documentation with
  auth requirements, request/response examples, and error format.
- [Architecture overview](docs/architecture.md) — two-binary design,
  internal package layout, storage, config flow, request lifecycle.
- [Configuration reference](docs/configuration.md) — complete config.toml
  field reference with types, defaults, and validation rules.
- [CLI reference](docs/cli.md) — `afc` command-line client.
- [Frontend guide](docs/web-ui.md) — React web UI.

## Frontend

The `web/` directory contains a React + TypeScript frontend built with Vite.
From the repo root:

- `make web-dev` — start the Vite dev server with hot reload (auto-installs
  dependencies if needed).
- `make web-build` — compile the production build into `web/dist/`.

See [docs/web-ui.md](docs/web-ui.md) for full setup, project structure, and
component conventions.
