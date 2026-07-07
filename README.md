# agent-fox hub

A headless harness for spec-driven, multi-agent software development. The
harness gives each unit of work an isolated workspace with its own branch,
files, and agents, and coordinates those agents through a validated
specification package rather than ad-hoc chat. 

The design is inspired by [Intent](https://www.intentapp.dev) from Augment
Code but diverges intentionally: headless instead of desktop, coordination
rebuilt on a structured spec package that freezes on approval, and all
grounding unified under a single Context abstraction.

Start with the **[Architecture overview](docs/README.md)** for the
three-layer system design, the **[Spec Format overview](docs/specs/README.md)**
for the specification package that drives every unit of work, the
**[CLI Reference](docs/cli.md)** for the `afc` command-line client, or the
**[Frontend Guide](docs/web-ui.md)** for the React web UI.

## Frontend

The `web/` directory contains a React + TypeScript frontend built with Vite.
From the repo root:

- `make web-dev` — start the Vite dev server with hot reload (auto-installs
  dependencies if needed).
- `make web-build` — compile the production build into `web/dist/`.

See [docs/web-ui.md](docs/web-ui.md) for full setup, project structure, and
component conventions.
