---
spec_id: '04'
spec_name: web_ui_scaffold
title: Web Ui Scaffold
status: draft
created_at: '2026-07-07T11:28:39.697158+00:00'
updated_at: '2026-07-07T11:28:39.697158+00:00'
owner: ''
source: ".agent-fox/specs/prd.md"
schema_version: 1
---
# Web UI Scaffold

## Intent

Initialize the frontend project for af-hub's web interface. This spec sets up the toolchain, frameworks, and build pipeline — nothing more. No functional pages, no authentication flow, no business logic. Just a working React + TypeScript + Tailwind + shadcn/ui project with a dev proxy to the Go backend and a "Hello world" route.

This foundation enables future specs to add real pages (login, dashboard, settings) without re-doing the toolchain setup.

## Goals

- Initialize the `web/` project at the repo root with its own `package.json`.
- Set up Vite + React + TypeScript + Tailwind CSS + shadcn/ui.
- Configure the Vite dev proxy to forward `/api` and `/healthz` and `/readyz` requests to the Go backend.
- Add `make web-dev` and `make web-build` targets to the project Makefile.
- Ship a single "Hello world" route at `/`.
- Ship frontend development documentation at `docs/web-ui.md`.

## Non-goals

- Login page or any authentication flow.
- Dashboard, settings, or any functional pages.
- API integration or data fetching.
- Session token handling.
- Production deployment configuration (CDN, static hosting).

## Functional Requirements

### Project initialization

- The web UI lives in `web/` at the repo root, cleanly separated from the Go backend.
- `web/` has its own `package.json` and `tsconfig.json`.
- The project uses Vite as the build tool and dev server.
- React with TypeScript for the UI framework.
- Tailwind CSS for styling.
- shadcn/ui components initialized (copied into the tree via `npx shadcn-ui@latest init`, not installed as a dependency).
- React Router configured for client-side routing.
- TanStack Query (React Query) installed and configured (provider wrapper in place, no queries yet).

### Dev proxy

- The Vite dev server proxies API requests to the Go backend:
  - `/api/*` → `http://localhost:8080`
  - `/healthz` → `http://localhost:8080`
  - `/readyz` → `http://localhost:8080`
- The proxy target port (8080) matches the Go server's default configuration.

### Build targets

- `make web-dev` — starts the Vite dev server with hot reload (runs `npm run dev` in `web/`).
- `make web-build` — runs the production build (runs `npm run build` in `web/`), outputting to `web/dist/`.
- Both targets handle `npm install` if `node_modules/` is missing.

### NPM scripts

- `npm run dev` — starts the Vite dev server with hot reload.
- `npm run build` — production build to `web/dist/`.
- `npm run lint` — runs ESLint + TypeScript type checking.
- `npm run preview` — preview the production build locally.

### Hello world route

- A single route at `/` that renders a minimal page with the text "af-hub" and a brief placeholder message.
- No navigation, no sidebar, no layout components beyond the minimum needed.

### Documentation

- `docs/web-ui.md` — Frontend development guide: prerequisites (Node.js version), setup (`npm install`), dev server, build, project structure, component conventions (shadcn/ui usage). Update `README.md` to link to it and mention the `make web-dev` / `make web-build` targets.

## Technical Boundaries

- **Language:** TypeScript
- **Framework:** React
- **Build tool:** Vite
- **CSS:** Tailwind CSS
- **Components:** shadcn/ui (copied into tree, built on Radix UI primitives)
- **API state:** TanStack Query (React Query) — installed and configured, no queries in this spec
- **Routing:** React Router — installed and configured with one route
- **Node.js:** Version compatible with Vite 5+ (Node 18+)

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 01_backend_foundation | 2 | 1 | Requires the Go server running with health probes for dev proxy verification |

## Design Decisions

1. **Separate `web/` directory:** Clean separation from Go backend. Own `package.json`, no monorepo tooling needed.
2. **shadcn/ui copied into tree:** Components are copied, not installed as a dependency. This gives full control over customization and avoids version lock-in.
3. **TanStack Query pre-configured:** Provider wrapper set up now so future specs adding API calls don't need boilerplate.
4. **React Router pre-configured:** Routing infrastructure in place for future page additions.
5. **No SSR:** Pure SPA. Vite builds static assets.

