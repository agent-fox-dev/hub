---
spec_id: '02'
spec_name: web_scaffold
title: Web Scaffold
status: draft
created_at: '2026-07-20T16:18:57.919676+00:00'
updated_at: '2026-07-20T16:28:38.028850+00:00'
owner: ''
source: docs/prd.md
schema_version: 1
---
# af-hub Web UI Scaffold

## Background

af-hub is being developed as the coordination hub for the agent-fox platform. Until now, no frontend project exists — the repository contains only the Go backend. This spec establishes the frontend foundation so that future specs (such as workspace management pages, login flows, and dashboards) can add functional UI incrementally without revisiting build toolchain decisions. The scaffold is intentionally minimal: it proves the development pipeline works end-to-end and nothing more.

## Intent

Set up the frontend project for af-hub — the web interface that will eventually provide login flows, dashboards, and workspace management pages. This iteration delivers only the project scaffold and build toolchain so that future specs can add functional pages incrementally without worrying about the initial setup.

## Goals

- Initialize a cleanly separated frontend project at `web/` with its own `package.json`.
- Set up the development toolchain: Vite + React + TypeScript + Tailwind CSS + shadcn/ui.
- Configure a dev proxy so frontend development can talk to the Go backend without CORS issues.
- Add make targets for frontend build operations.
- Ship a single placeholder route to verify the scaffold works end-to-end.

## Non-goals

- **Functional UI pages.** No login flow, no dashboard, no settings pages. Just the scaffold.
- **Authentication or session management in the frontend.** Future work.
- **Server-side rendering.** The frontend is a client-side SPA.
- **Backend changes.** This spec touches only the `web/` directory and the root `Makefile`.
- **Production serving of the built SPA.** How `web/dist/` is served in production (e.g., `go:embed`, a separate static file server, or CDN deployment) is explicitly out of scope for all scaffold-related specs. Deployment strategy will be addressed in a dedicated future spec.
- **CI integration.** No CI pipeline exists yet. Wiring `make web-build` and `make web-lint` into a CI gate is a separate concern deferred to a future spec.
- **Cross-spec dependency declarations.** The `workspaces` spec and other future specs will build on top of this scaffold, but that relationship is implicit; no formal spec-level dependency is declared here. The web scaffold is a standalone build toolchain setup.
- **Vite config file structure.** The location and internal structure of the Vite configuration file (including the proxy block) are left to the implementer. The required proxy behavior is fully specified; how it is expressed in configuration is an implementation detail.
- **ESLint config file format.** Whether the ESLint configuration uses the flat config format (`eslint.config.js`, default in ESLint 9+) or the legacy format (`.eslintrc.cjs`) is left to the implementer based on the installed Vite/ESLint version.
- **TypeScript type-check command structure.** Whether `tsc --noEmit` is invoked as a separate `npm run typecheck` script or bundled into `npm run lint` is left to the implementer. What matters is the outcome: `make web-lint` exits with code 0 and reports no errors.
- **`.gitignore` contents.** Standard developer hygiene (e.g., excluding `node_modules/` and `web/dist/`) is assumed and does not need to be specified here.
- **Dev server port and host.** The Vite dev server port and host are left to Vite's defaults. The implementer may configure them; the spec does not prescribe a specific URL.
- **Makefile `web-install` target and top-level target integration.** Whether a `make web-install` or `make web-setup` target is added, and whether existing top-level targets such as `make all` or `make build` are updated to include the frontend, are left to the implementer.

## Functional Requirements

### Project initialization

- The `web/` directory at the repo root contains a standalone frontend project with its own `package.json`, cleanly separated from the Go backend.
- The project uses npm as the package manager. `package-lock.json` is committed.
- TypeScript is configured with strict mode enabled (see [TypeScript configuration](#typescript-configuration) below).

### Build toolchain

- Vite is the build tool and development server.
- React is the UI framework.
- Tailwind CSS provides utility-first styling.
- shadcn/ui provides component primitives (copied into the project tree, not used as a dependency). The scaffold must run `npx shadcn-ui init` (or the equivalent current CLI invocation) as part of setup and commit the resulting `components.json` config file. No individual components need to be added at scaffold time — only the init step and its config file are required.
- TanStack Query is installed for future API state management.
- React Router is installed for future client-side routing.

### TypeScript configuration

The `tsconfig.json` inside `web/` must include at minimum the following compiler options:

| Option | Required value |
|--------|---------------|
| `strict` | `true` |
| `moduleResolution` | `bundler` |
| `target` | `ESNext` |
| `jsx` | `react-jsx` |

These are the Vite-recommended defaults for a React + TypeScript project. No additional tsconfig options beyond these and the standard Vite scaffold output are required by this spec.

### Dev proxy

- The Vite dev server proxies requests matching `/api`, `/healthz`, and `/readyz` to the Go backend at `http://localhost:8080`.
- This eliminates CORS issues during development without requiring CORS middleware on the backend.
- The proxy target port `8080` is the Go backend's default listening port. No override mechanism is required for the scaffold phase.
- No specific error-handling behavior for the proxy is required by this spec. When the Go backend is not running, proxied requests will fail with a connection error at the network level; this is acceptable for the scaffold phase and does not require special handling.
- The implementer chooses the Vite configuration file name, location, and internal structure; the only requirement is that the proxy behavior described above is in effect when the dev server runs.

### ESLint configuration

- ESLint is configured using the Vite-generated default ESLint config as the starting point.
- The `@typescript-eslint/recommended` ruleset is extended on top of that default.
- The config file format (flat config `eslint.config.js` vs. legacy `.eslintrc.cjs`) is left to the implementer based on the installed Vite and ESLint versions.
- No additional plugins beyond those included in the Vite scaffold are required at this time; the configuration is intentionally minimal and can be extended in future specs.

### Make targets

- `make web-dev` starts the Vite dev server with hot reload.
- `make web-build` produces a production build to `web/dist/`.
- `make web-lint` runs ESLint and TypeScript type checking, exiting with code 0 only when there are no errors. The implementer decides whether TypeScript type checking (`tsc --noEmit` or equivalent) is a separate npm script or bundled into `npm run lint`.
- Additional Makefile targets (e.g., `make web-install`) and integration with existing top-level targets (e.g., `make all`, `make build`) are left to the implementer.

### Placeholder route and source layout

- A single route at `/` renders "af-hub" as an `<h1>` heading.
- No other pages, no navigation, no auth flow.
- The minimum required source files for the placeholder route are:
  - `web/index.html` — Vite's required HTML entry point.
  - `web/src/main.tsx` — application entry point (mounts the React app).
  - `web/src/App.tsx` — root component containing the placeholder `<h1>af-hub</h1>` heading.
- Internal directory structure beyond these files (e.g., `src/pages/`, `src/components/`, `src/lib/`) is left to the implementer and will be defined by future specs as functional pages are added.

### Development commands

- `npm run dev` starts the Vite dev server with hot reload (same as `make web-dev`).
- `npm run build` produces a production build to `web/dist/` (same as `make web-build`).
- `npm run lint` runs ESLint + TypeScript type checking (same as `make web-lint`).

## Acceptance Criteria

This spec is considered complete when **all** of the following are true:

1. `npm run build` (equivalently, `make web-build`) exits with code 0 and produces output in `web/dist/`.
2. `npm run lint` (equivalently, `make web-lint`) exits with code 0 with no errors.
3. A developer can run `make web-dev` and navigate to the Vite dev server URL (Vite's default, unless the implementer configures otherwise) to see an `<h1>` element containing "af-hub".
4. `web/src/main.tsx` and `web/src/App.tsx` exist and together implement the placeholder route.
5. `web/index.html` exists as the Vite HTML entry point.
6. `web/tsconfig.json` includes `strict: true`, `moduleResolution: bundler`, `target: ESNext`, and `jsx: react-jsx`.
7. `package-lock.json` is committed inside `web/`.
8. The Vite dev server proxies `/api`, `/healthz`, and `/readyz` to `http://localhost:8080` (verifiable by starting the Go backend and confirming a proxied request reaches it).
9. `web/components.json` exists and is committed, confirming that `npx shadcn-ui init` was run as part of the scaffold.

Package versions are not pinned in the spec — version selection is left to the implementer at install time. The committed `package-lock.json` locks all resolved versions after the initial `npm install`, ensuring reproducibility for all subsequent runs.

## Technical Boundaries

- **Language:** TypeScript (strict mode; see TypeScript configuration above)
- **Frontend stack:** React, Vite, Tailwind CSS, shadcn/ui, TanStack Query, React Router
- **Package manager:** npm

## Dependencies

| Package | Purpose |
|---------|---------|
| React | UI framework |
| Vite | Build tool and dev server |
| Tailwind CSS | Utility-first CSS |
| shadcn/ui + Radix UI | Component primitives (initialized via `npx shadcn-ui init`; `components.json` committed) |
| TanStack Query | API state management (installed, not yet used) |
| React Router | Client-side routing (installed, not yet used) |
| @typescript-eslint/recommended | TypeScript linting ruleset |

## Design Decisions

1. **npm as frontend package manager.** npm is the default for Vite scaffolding, requires no additional tooling, and produces a `package-lock.json` that is well-supported by CI systems.
2. **Minimal ESLint config.** The Vite-generated ESLint config extended with `@typescript-eslint/recommended` is sufficient for the scaffold phase. Stricter rules and additional plugins (e.g., `eslint-plugin-react-hooks`) can be layered in by future specs as the codebase grows. The ESLint config file format is left to the implementer based on the installed toolchain versions.
3. **No pinned package versions in the spec.** The `package-lock.json` committed after the initial install is the source of truth for reproducible builds. Pinning versions in the spec would create a maintenance burden without additional reproducibility benefit.
4. **Vite-recommended tsconfig defaults mandated.** Rather than leaving tsconfig fully open to the implementer, this spec mandates the four Vite-recommended compiler options (`strict`, `moduleResolution: bundler`, `target: ESNext`, `jsx: react-jsx`). This prevents incompatible settings that could cause build failures and avoids future specs having to correct a broken baseline.
5. **Minimal source layout.** Only `web/index.html`, `src/main.tsx`, and `src/App.tsx` are specified for the scaffold. Deeper directory conventions (`pages/`, `components/`, `lib/`) are deferred to the first spec that introduces a second route or shared component, keeping the scaffold free of premature structure.
6. **Vite config structure left to the implementer.** The proxy behavior is fully specified (paths, target port), but the Vite configuration file name, location, and internal structure are implementation details. This avoids over-specifying scaffolding conventions that Vite itself generates.
7. **TypeScript type-check invocation left to the implementer.** The acceptance criterion for `make web-lint` is outcome-based (exit code 0, no errors). Whether `tsc --noEmit` runs as part of `npm run lint` or as a separate script is an implementation detail that does not affect verifiability.
8. **Production serving deferred.** How `web/dist/` is served in production is explicitly out of scope. Deciding the serving strategy now would couple the scaffold to deployment concerns that are not yet finalized.
9. **CI integration deferred.** No CI pipeline exists at this time. When CI is introduced in a future spec, it will wire up `make web-build` and `make web-lint` as gates without any changes to this spec's deliverables.
10. **Specs remain independent.** The `workspaces` spec and other future UI specs will consume this scaffold, but those relationships are left implicit. The web scaffold is a self-contained build toolchain concern; future specs declare their own requirements against the scaffold as a given foundation.
11. **shadcn/ui init is part of the scaffold.** Running `npx shadcn-ui init` and committing `components.json` is explicitly required at scaffold time. This confirms that the shadcn/ui toolchain is wired up and ready for future specs to add components without re-running setup. No individual components are initialized at this stage.
12. **Dev server URL left to implementer.** Vite's default dev server URL is well-known (`http://localhost:5173` by default), but this spec does not prescribe a port or host. The implementer may leave Vite's defaults in place or configure them as needed.
13. **Makefile scope limited to three targets.** Only `make web-dev`, `make web-build`, and `make web-lint` are required. Additional targets (e.g., `make web-install`) and integration with top-level targets are left to the implementer to avoid over-specifying project-wide Makefile conventions at the scaffold stage.
