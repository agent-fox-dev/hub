# Errata: 04_web_ui_scaffold

## Tailwind CSS v4 configuration model

**Spec assumes:** Tailwind v3 with `tailwind.config.js`, `@tailwind base/components/utilities` directives, and `npx tailwindcss init -p`.

**Actual:** Tailwind CSS v4 uses CSS-based configuration with `@import "tailwindcss"` and no `tailwind.config.js`. The `npx shadcn@latest init` command handles Tailwind v4 setup automatically, including creating the CSS custom properties and theme configuration in `src/index.css`.

**Resolution:** Let shadcn handle Tailwind setup. No manual Tailwind configuration needed.

## shadcn CLI package name

**Spec assumes:** `npx shadcn-ui@latest init` throughout PRD, requirements, and tasks.

**Actual:** The `shadcn-ui` npm package (v0.9.5) is a legacy wrapper. The active CLI is `npx shadcn@latest init` (package `shadcn` at v4.13.0).

**Resolution:** Used `npx shadcn@latest init` with `-d -y` flags for non-interactive initialization.

## shadcn init does not copy component files

**Spec assumes:** Running `npx shadcn-ui@latest init` creates `web/src/components/ui/` with component files.

**Actual:** The `init` command only creates `components.json` configuration and sets up path aliases. An explicit `npx shadcn@latest add <component>` is required to populate `web/src/components/ui/`.

**Resolution:** The `init` command was run with `-d -y` flags which auto-added a default button component. Files were moved from the literal `@/` directory to `src/` since the path alias was not resolved during init.

## ESLint flat config

**Spec assumes:** ESLint with `.eslintrc.cjs` and `--ext .ts,.tsx` flag in the lint script.

**Actual:** ESLint 9+ uses flat config (`eslint.config.js`). The `--ext` flag is not supported in flat config mode.

**Resolution:** Created `eslint.config.js` with flat config syntax. Lint script uses `eslint .` instead of `eslint src --ext .ts,.tsx`.

## TypeScript project references and lint script

**Spec assumes:** `tsc --noEmit` for type checking in the lint script.

**Actual:** With project references (`tsconfig.json` using `references` to `tsconfig.app.json` and `tsconfig.node.json`), plain `tsc --noEmit` does not type-check source files because the root `tsconfig.json` has `files: []`.

**Resolution:** Lint script uses `tsc -b --noEmit` which follows project references and type-checks all included files. The test assertion (TS-04-14) checks for both `tsc` and `--noEmit` in the lint script, which this satisfies.

## shadcn added as dependency

**Spec assumes:** shadcn/ui components are copied into tree with no npm dependency.

**Actual:** `npx shadcn@latest init` adds `shadcn` (the CLI tool) to `dependencies`. This was moved to `devDependencies` since it's only used as a CLI tool for adding components. The test assertion (TS-04-3) checks for `shadcn-ui` and `@shadcn/ui` absence, which is satisfied.
