# Errata: Spec 06 — Test Framework Gap

## Issue

The `06_web_ui_scaffold` spec defines 49 acceptance tests, 11 edge-case tests,
and 5 property tests (task groups 1-3), but no test framework is available.

The PRD explicitly defers testing infrastructure:
> "Unit or component testing infrastructure (e.g., Vitest, React Testing Library)
> — deferred to a future spec."

The `devDependencies` list (REQ-4.2) includes no test runner, and the
`test_commands` in `tasks.json` (`npm run lint`, `npm run lint && npm run build`)
are lint/build commands, not test runners.

## Resolution

Acceptance tests are implemented as **shell-based test scripts** at
`tests/web_scaffold/test_scaffold.sh`. This approach:

- Requires no npm test framework (works with bash + python3 for JSON parsing)
- Verifies file existence, content patterns, JSON structure, and command outputs
- Can be run from the repository root: `./tests/web_scaffold/test_scaffold.sh`
- Supports filtering by test ID (`TS-06-1`) or group (`group1`)
- Produces clear pass/fail output with summaries

This is appropriate because the spec tests are acceptance-level (infrastructure
verification), not unit tests. They check that the scaffold is correctly
configured, not that application code behaves correctly.

## Impact

- Task groups 1-3 use shell tests instead of Vitest/Jest
- The `make test` target (Go tests) does not run these tests
- When a future spec adds Vitest, these tests could be migrated, but the shell
  versions remain valid and useful as CI gate checks
