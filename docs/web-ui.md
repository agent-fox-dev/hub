# Frontend Development Guide

This guide covers setting up and working with the af-hub frontend, which lives
in the `web/` directory at the repository root.

## Prerequisites

- **Node.js 18** or later is required (Vite 6 and the project toolchain do not
  support older versions). If you are running an older version of Node, upgrade
  before running any commands — `npm install` or `vite` will fail with an
  incompatible-engine error.
- **npm** (ships with Node.js).

## Setup

Install project dependencies from the repository root:

```bash
npm install --prefix web
# or, from inside the web/ directory:
cd web && npm install
```

This populates `web/node_modules/` with all runtime and development
dependencies.

> **Tip:** The Makefile targets `make web-dev` and `make web-build` auto-install
> dependencies when `web/node_modules/` is absent, so you can skip this step if
> you use those targets.

## Dev Server

Start the Vite dev server with hot module replacement:

```bash
npm run dev --prefix web
# or from the repo root:
make web-dev
```

The dev server prints a local URL (typically `http://localhost:5173`) to stdout.
Open it in a browser to see the application.

### API Proxy

During development the Vite dev server proxies backend requests so the browser
sees a single origin (no CORS issues):

| Path       | Proxied to                        |
|------------|-----------------------------------|
| `/api/*`   | `http://localhost:8080/api/*`      |
| `/healthz` | `http://localhost:8080/healthz`    |
| `/readyz`  | `http://localhost:8080/readyz`     |

Start the Go backend (`make run` or `go run ./cmd/af-hub`) on port 8080 before
issuing API requests through the frontend. If the backend is not running, the
proxy returns a 502 error to the browser without crashing the dev server.

## Production Build

Compile and bundle the frontend for production:

```bash
npm run build --prefix web
# or from the repo root:
make web-build
```

This runs TypeScript type-checking (`tsc -b`) followed by the Vite bundler. The
output is written to `web/dist/` and contains `index.html` plus hashed JS/CSS
assets ready to be served by a static file server or embedded in the Go binary.

To preview the production build locally:

```bash
npm run preview --prefix web
```

## Linting

Run ESLint and TypeScript type checking:

```bash
npm run lint --prefix web
```

This executes `tsc -b --noEmit` (type check with project references) followed
by `eslint .` (flat-config ESLint 9+). The command exits with a non-zero code
if any errors are found.

## Project Structure

```
web/
├── components.json          # shadcn/ui configuration
├── package.json             # npm manifest (scripts, dependencies)
├── tsconfig.json            # TypeScript root config (project references)
├── tsconfig.app.json        # TypeScript config for app source
├── tsconfig.node.json       # TypeScript config for Node-side files
├── tsconfig.test.json       # TypeScript config for test files
├── vite.config.ts           # Vite + dev proxy configuration
├── vitest.config.ts         # Vitest test runner configuration
├── eslint.config.js         # ESLint flat config
├── index.html               # HTML entry point
├── src/
│   ├── main.tsx             # React entry — providers, router
│   ├── index.css            # Global styles (Tailwind CSS v4)
│   ├── vite-env.d.ts        # Vite client type declarations
│   ├── components/
│   │   └── ui/              # shadcn/ui component source files
│   │       └── button.tsx
│   ├── lib/
│   │   └── utils.ts         # Shared utilities (cn helper)
│   └── pages/
│       ├── HomePage.tsx     # Root route — Hello World placeholder
│       └── NotFound.tsx     # Catch-all 404 page
├── tests/
│   ├── project-init.test.ts
│   ├── proxy-makefile-scripts.test.ts
│   ├── route-docs-edge.test.ts
│   └── route-runtime.test.tsx
└── dist/                    # Production build output (git-ignored)
```

## Component Conventions (shadcn/ui)

This project uses [shadcn/ui](https://ui.shadcn.com/) for UI components.
Unlike typical npm packages, shadcn/ui components are **copied directly into
the project tree** at `web/src/components/ui/` — they are not installed as an
npm dependency.

### Adding a new component

```bash
cd web
npx shadcn add <component>
# Example:
npx shadcn add card
```

This copies the component source into `src/components/ui/` where you can
customise it freely. The `components.json` file at the web root controls paths,
aliases, and style settings used by the CLI.

### Styling

- **Tailwind CSS v4** — styles are defined via CSS-based configuration in
  `src/index.css` (no `tailwind.config.js`). Theme tokens are CSS custom
  properties.
- **class-variance-authority (cva)** — used by shadcn/ui components for
  variant-driven class composition.
- **tailwind-merge** — the `cn()` utility in `src/lib/utils.ts` merges
  Tailwind classes safely.

## Key Libraries

| Library                | Purpose                                |
|------------------------|----------------------------------------|
| React 19               | UI framework                           |
| React Router 7         | Client-side routing                    |
| TanStack Query 5       | Async state management / data fetching |
| Vite 6                 | Build tool and dev server              |
| Tailwind CSS 4         | Utility-first CSS framework            |
| shadcn/ui              | Accessible component primitives        |
| Vitest                 | Unit and integration testing           |
