# Spec Format Specification

Version 1.3 — Draft

## 1. Scope

This document defines the on-disk format for a specification
package. A specification package (hereafter "spec") is the durable artifact
that captures design intent, acceptance criteria, verification contracts, and
implementation plans for one cohesive feature or concern.

A spec consists of four required artifacts and one optional artifact:

| File | Format | Required | Purpose |
|---|---|---|---|
| `prd.md` | Markdown with YAML frontmatter | yes | Narrative intent — the "why" and "what" |
| `requirements.json` | JSON (schema-validated) | yes | What the system must do, must guarantee, and how it wires together |
| `test_spec.json` | JSON (schema-validated) | yes | How each requirement is verified |
| `tasks.json` | JSON (schema-validated) | yes | What work to do, in what order, with what dependencies |
| `architecture.md` | Markdown (free-form, no schema) | no | Architectural context — modules, interfaces, data models, technology choices |

There is no `design.md`. Content that previously lived in a design document
is distributed as follows:

| Former design.md section | New location |
|---|---|
| Correctness Properties | `requirements.json` — `correctness_properties` |
| Execution Paths | `requirements.json` — `execution_paths` |
| Error Handling | `requirements.json` — `error_handling` |
| Architecture / Module Responsibilities | `architecture.md` (optional) |
| Components and Interfaces | `architecture.md` (optional) |
| Data Models | `architecture.md` (optional) |
| Technology Stack | `architecture.md` (optional) |
| Definition of Done | Project-level steering (not per-spec) |
| Testing Strategy | Project-level steering (not per-spec) |

---

## 2. Terminology

| Term | Definition |
|---|---|
| Spec | A package of four required artifacts (and one optional) representing one feature or concern |
| Spec root | The directory containing the spec artifacts, e.g. `<spec_root>/specs/05_my_feature/` |
| Operator | A human who authors PRDs, reviews specs, and approves plans |
| Coordinator | The agent role that drafts and mutates JSON artifacts from PRD input |
| Archetype | An agent role that executes tasks; may only update its own task state |
| EARS | Easy Approach to Requirements Syntax — a pattern language for writing testable requirements |
| Spec ID | The numeric prefix of a spec folder, e.g. `"05"` |

---

## 3. Folder layout and naming

Specs live under the project's spec root directory (default: `<spec_root>/specs/`).

```
<spec_root>/specs/
  05_my_feature/
    prd.md
    requirements.json
    test_spec.json
    tasks.json
    architecture.md      # optional
  archive/
    03_old_feature/      # superseded specs
```

### 3.1 Naming convention

Format: `{NN}_{snake_case_name}`

- `NN` is a monotonically increasing integer (no leading zeros required past
  two digits). The next number is `max(existing) + 1`.
- `snake_case_name` is a short, descriptive slug.
- Collisions on `NN` are rejected at creation time.

### 3.2 Completeness

A spec root **must** contain all four required files (`prd.md`,
`requirements.json`, `test_spec.json`, `tasks.json`). A directory missing
any required file is not a valid spec. The optional `architecture.md` may
be present or absent without affecting validity.

### 3.3 Bootstrap

During creation (via the library's `create()` flow), the four required
files come into existence sequentially. The library operates in bootstrap
mode during this process: cross-file validation is deferred until all four
required files are written. A partially-created spec directory is in an
"incomplete" state — not invalid, but not yet valid either.

The standalone `validate()` command always enforces completeness. A
directory missing any required file is reported as incomplete. A spec
cannot transition from `draft` to `active` while incomplete.
`architecture.md` may be added at any point — before, during, or after
the `draft` to `active` transition.

---

## 4. `prd.md`

The PRD is a prose markdown document with structured YAML frontmatter. It is
the only artifact authored primarily by humans. It captures narrative intent,
background, goals, non-goals, and any other context that does not fit into
the structured JSON artifacts.

### 4.1 Frontmatter

YAML frontmatter is mandatory. All fields shown are required unless marked
optional.

```yaml
---
spec_id: "05"
spec_name: "my_feature"
title: "Human-readable title for the feature"
status: "draft"                    # draft | active | sealed | superseded | archived
created_at: "2026-05-18T12:00:00Z"
updated_at: "2026-05-18T12:00:00Z"
owner: "author-name"
source: "https://github.com/org/repo/issues/42"  # origin of the PRD input
supersedes: []                     # list of spec_ids this replaces, e.g. ["03"]
tags: []                           # optional, free-form classification
intent_hash: null                  # SHA-256 of Intent section; set at draft→active transition
schema_version: 1
---
```

#### Field definitions

| Field | Type | Description |
|---|---|---|
| `spec_id` | string | Numeric prefix as string, e.g. `"05"`. Must match the folder prefix. |
| `spec_name` | string | Snake-case slug. Must match the folder suffix. |
| `title` | string | Human-readable title. |
| `status` | enum | Lifecycle state (see §9). |
| `created_at` | ISO 8601 datetime | When the spec was created. Immutable after creation. |
| `updated_at` | ISO 8601 datetime | Last modification timestamp. Updated on every save. |
| `owner` | string | Who owns this spec. |
| `source` | string | Where the PRD input came from: file path, GitHub issue URL, or free-text description. |
| `supersedes` | array of strings | Spec IDs this spec replaces. Empty array if none. |
| `tags` | array of strings | Optional. Free-form tags for classification. |
| `intent_hash` | string or null | SHA-256 hex digest of the `## Intent` section body (trimmed). Null while `status` is `draft`. Set automatically by the library at the `draft → active` transition. On every subsequent mutation, the library recomputes the hash and rejects the change if it differs. |
| `schema_version` | integer | Format version. Currently `1`. |

### 4.2 Body

The body is free-form markdown with one required section:

```markdown
# {title}

## Intent

{One paragraph — the operator's original goal, preserved verbatim.}
```

The `## Intent` section is the only machine-read part of the body. At the
`draft → active` transition, the library computes a SHA-256 hash of the
Intent section body (trimmed of leading/trailing whitespace) and stores it
in the frontmatter `intent_hash` field. On every subsequent mutation, the
library recomputes the hash and rejects the change if the Intent has been
altered.

The PRD origin is recorded exclusively in the frontmatter `source` field
(§4.1). There is no `## Source` body section — a single authoritative
location prevents sync drift.

All other sections are optional and operator-discretion: Goals, Non-goals,
Background, Design Decisions, Dependencies, Open Questions, etc. No machine
reads them — they exist for human reviewers and for context when the
coordinator drafts `requirements.json`. Architectural content (module
responsibilities, component interfaces, data models, technology stack)
belongs in `architecture.md` (§5), not in the PRD.

### 4.3 Optional sections of note

**Dependencies** — When a spec depends on other specs, the PRD may include a
`## Dependencies` section with a table declaring cross-spec edges at
task-group granularity. This table is informational in the PRD; the
machine-readable form lives in `tasks.json` (see §8.2).

**Design Decisions** — When the operator or coordinator resolves ambiguities
during PRD refinement, decisions and rationale can be recorded here. These
are human-oriented context, not machine-consumed structure.

---

## 5. `architecture.md`

The architecture document is an **optional** free-form markdown file that
provides architectural context for a spec. It captures the "how" — module
structure, interfaces, data models, and technology choices — that would
clutter the PRD's focus on intent.

A spec is valid without `architecture.md`. When present, the file has no
YAML frontmatter, no JSON schema, and no machine-validated structure. It is
authored by operators and coordinators, and consumed by human reviewers and
by agents that need implementation-level context.

### 5.1 Suggested sections

The following sections are recommended but not enforced. Authors may use any
markdown structure that serves the spec.

| Section | Purpose |
|---|---|
| Architecture Overview | High-level description and diagrams (Mermaid flowcharts, sequence diagrams) |
| Module Responsibilities | What each major module or component owns |
| Components and Interfaces | Type signatures, API surface, public contracts |
| Data Models | Schemas, configuration shapes, examples |
| Technology Stack | Languages, frameworks, libraries, infrastructure choices |
| Operational Readiness | Observability, deployment, compatibility, performance targets |

### 5.2 Authoring guidelines

1. **No frontmatter.** The file is pure markdown. Metadata lives in `prd.md`.
2. **No machine validation.** The library does not parse or validate
   `architecture.md` content. It checks only for file existence when present.
3. **Mermaid diagrams encouraged.** Use fenced `mermaid` code blocks for
   architecture diagrams, sequence diagrams, and data flow illustrations.
4. **Reference, don't duplicate.** If a concept is already defined in
   `requirements.json` (e.g., execution paths, correctness properties), reference
   it by ID rather than restating it.
5. **Implementation-level names are acceptable.** Unlike execution paths
   (which use logical actors), architecture.md may name concrete modules,
   classes, functions, and packages.

### 5.3 Lifecycle

`architecture.md` may be created, modified, or deleted at any point in the
spec lifecycle, including after the spec transitions to `active`. Because
the file has no machine-read content, changes to it do not trigger
cross-file validation or intent-hash checks.

When a spec is superseded or archived, `architecture.md` moves with the
rest of the spec directory.

---

## 6. `requirements.json`

The requirements file is the largest and most heavily mutated JSON artifact.
It captures what the system must do (EARS requirements), what invariants must
hold (correctness properties), how the system wires together (execution
paths), and how errors are handled.

### 6.1 Top-level schema

```json
{
  "$schema": "https://agent-fox.dev/schemas/requirements.v1.json",
  "spec_id": "05",
  "spec_name": "my_feature",
  "schema_version": 1,
  "introduction": "Brief description of the system being specified.",
  "glossary": {},
  "requirements": [],
  "correctness_properties": [],
  "execution_paths": [],
  "error_handling": []
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `$schema` | string | yes | Schema URI for validation. |
| `spec_id` | string | yes | Must match `prd.md` frontmatter. |
| `spec_name` | string | yes | Must match `prd.md` frontmatter. |
| `schema_version` | integer | yes | Currently `1`. |
| `introduction` | string | yes | Brief description of the system being specified. |
| `glossary` | object | yes | Map of term → definition. Every domain-specific term used in any requirement, property, or path must have an entry. |
| `requirements` | array | yes | Requirement objects (§6.2). |
| `correctness_properties` | array | yes | Correctness property objects (§6.3). May be empty. |
| `execution_paths` | array | yes | Execution path objects (§6.4). May be empty. |
| `error_handling` | array | yes | Error handling objects (§6.5). May be empty. |
| `external_apis` | array | no | Verified external API entries (§6.6). May be absent or empty. |

### 6.2 Requirements

Each requirement has a user story, acceptance criteria, and edge cases. IDs
are globally unique via the `{spec_id}-REQ-{N}` format.

```json
{
  "id": "05-REQ-1",
  "title": "Space lifecycle",
  "user_story": {
    "role": "operator",
    "goal": "create isolated workspaces for each task",
    "benefit": "agents don't trample each other's work"
  },
  "acceptance_criteria": [],
  "edge_cases": []
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Format: `{spec_id}-REQ-{N}`. N is a positive integer, sequential within the spec. |
| `title` | string | yes | Short descriptive title. |
| `user_story` | object | yes | Contains `role`, `goal`, `benefit` (all strings, all required). |
| `acceptance_criteria` | array | yes | Criterion objects (§6.2.1). At least one required. |
| `edge_cases` | array | yes | Criterion objects using the edge-case ID format. May be empty. |

**Scope limit:** A single spec should contain no more than 10 requirements
(excluding edge cases). Exceeding this suggests the spec should be split.

#### 6.2.1 Acceptance criterion

Each criterion is a discriminated union keyed on `ears_pattern`. The EARS
sentence is computed from the structured fields — the fields are the source
of truth, not a rendered string.

```json
{
  "id": "05-REQ-1.1",
  "ears_pattern": "event_driven",
  "trigger": "an operator invokes `space new`",
  "system": "the system",
  "action": "create a Space with a unique ID, a worktree, and a store",
  "return_contract": null
}
```

##### EARS patterns and required fields

| `ears_pattern` | Required fields | Rendered template |
|---|---|---|
| `ubiquitous` | `system`, `action` | THE {system} SHALL {action} |
| `event_driven` | `trigger`, `system`, `action` | WHEN {trigger}, THE {system} SHALL {action} |
| `complex_event` | `trigger`, `condition`, `system`, `action` | WHEN {trigger} AND {condition}, THE {system} SHALL {action} |
| `state_driven` | `state`, `system`, `action` | WHILE {state}, THE {system} SHALL {action} |
| `unwanted` | `error_condition`, `system`, `action` | IF {error_condition}, THEN THE {system} SHALL {action} |
| `optional` | `feature`, `system`, `action` | WHERE {feature}, THE {system} SHALL {action} |

Schema validation enforces the correct field set per pattern via a
discriminated `oneOf`.

##### Common fields (all patterns)

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Acceptance criteria: `{spec_id}-REQ-{N}.{C}`. Edge cases: `{spec_id}-REQ-{N}.E{C}`. |
| `ears_pattern` | enum | yes | One of the six patterns above. |
| `system` | string | yes | The system or component being specified. |
| `action` | string | yes | What the system must do. Must be testable and unambiguous. |
| `return_contract` | string or null | yes | What the function returns to callers. Null when no return value is relevant. Non-null signals pipeline participation. |

##### Pattern-specific fields

| Field | Patterns | Type | Description |
|---|---|---|---|
| `trigger` | `event_driven`, `complex_event` | string | The event that triggers the action. |
| `condition` | `complex_event` | string | An additional condition that must hold alongside the trigger. |
| `error_condition` | `unwanted` | string | The error condition that triggers the unwanted-behavior response. |
| `state` | `state_driven` | string | The state during which the action applies. |
| `feature` | `optional` | string | The feature or configuration flag that enables the action. |

#### 6.2.2 Requirement quality rules

These rules apply to all requirements. Each is tagged with its enforcement
mechanism:

1. **[schema-enforced]** Every criterion must contain a non-empty `action`
   field.
2. **[review guidance]** Every function whose output is consumed by another
   part of the system should have a non-null `return_contract`. (Cannot be
   schema-enforced — requires knowledge of callers that don't exist yet.)
3. **[generation instruction]** Edge cases must systematically address:
   empty/null input, boundary values, operation failure, authorization
   failure, and concurrent operations. This is not optional review
   guidance — the coordinator must generate edge cases covering these
   categories for every requirement where they apply.
4. **[generation instruction]** Defensive design edge cases. For any
   requirement that involves subprocesses, external commands, loops, retries,
   or external service calls, generate edge cases for:
   - **(a) Timeout / hang** — the subprocess or call does not return within
     a reasonable time.
   - **(b) Resource cleanup on failure** — partial state (temp files,
     worktrees, connections, child processes) must be released when the
     operation fails midway.
   - **(c) Unbounded iteration** — loops and retry paths must have a
     maximum iteration cap or be explicitly delegated to a named safety
     mechanism (e.g., a circuit breaker). If delegated, the edge case must
     state what happens when the safety mechanism is absent or disabled.
   - **(d) Library vs. application boundary** — library code must signal
     errors via return values or exceptions, never by terminating the
     process (e.g., calling `exit()`, `os.Exit()`, `process.exit()`). Only
     the top-level CLI or application entry point may terminate.
5. **[review guidance]** Prefer measurable constraints over qualitative
   language.

### 6.3 Correctness properties

Formal invariants that must hold across all valid executions. Each property
validates one or more acceptance criteria.

```json
{
  "id": "05-PROP-1",
  "title": "Spec mutation is auditable",
  "for_any": "spec mutation event",
  "invariant": "the ledger contains a corresponding entry committed in the same transaction",
  "validates": ["05-REQ-1.1", "05-REQ-2.3"]
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Format: `{spec_id}-PROP-{N}`. |
| `title` | string | yes | Short descriptive name. |
| `for_any` | string | yes | Universal quantifier — what the property ranges over. |
| `invariant` | string | yes | The condition that must hold for all values of the quantifier. |
| `validates` | array of strings | yes | Non-empty array of acceptance criterion IDs that this property validates. Every ID must exist in `requirements`. |

#### 6.3.1 Coverage rules

After all properties are written:

1. Every requirement's primary acceptance criterion should have at least one
   property that validates it.
2. There should be at least one property for the happy path, one for failure
   handling, and one for boundary conditions.
3. Properties must be testable — each maps to a property-based test.

### 6.4 Execution paths

Integration-level wiring requirements. Each path traces a user-visible
feature from entry point to observable side effect using logical actors
(not module or function names).

```json
{
  "id": "05-PATH-1",
  "title": "Operator creates a Space and dispatches the first task",
  "steps": [
    { "actor": "operator",     "action": "invoke `space new <prompt>`" },
    { "actor": "SpaceManager", "action": "allocate worktree, store, and ledger" },
    { "actor": "Coordinator",  "action": "draft requirements.json from the prompt" }
  ]
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Format: `{spec_id}-PATH-{N}`. |
| `title` | string | yes | Short description of the end-to-end scenario. |
| `steps` | array | yes | Ordered list of step objects. At least two steps required. |

Each step:

| Field | Type | Required | Description |
|---|---|---|---|
| `actor` | string | yes | Logical component name. Free-form string. |
| `action` | string | yes | What the actor does at this step. |

#### 6.4.1 Path rules

1. Every path must start at a user action, CLI command, API call, or
   scheduled trigger.
2. Every path must end at a concrete side effect (file written, API call
   made, value returned to caller, state change persisted).
3. Actors are logical components, not module names. The mapping from actor
   to code is a wiring-verification concern at implementation time.
4. Every execution path must have a corresponding smoke test in
   `test_spec.json`.
5. **[cross-spec]** When a PRD produces multiple specs with dependency
   edges, at least one spec (typically the last in the dependency chain)
   must include an execution path that traces the **full end-to-end user
   flow** across spec boundaries — from the user-facing entry point
   through every layer to the final observable side effect. This path
   must name actors from each upstream spec it depends on. Without such a
   path, no spec owns the integration glue and the wiring verification
   step cannot verify that the layers connect.

### 6.5 Error handling

Maps error conditions to system behavior, cross-referencing requirement IDs.

```json
{
  "id": "05-ERR-1",
  "condition": "Config file missing",
  "behavior": "Use defaults",
  "requirement_id": "05-REQ-2.E1"
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Format: `{spec_id}-ERR-{N}`. Allows test cases and other artifacts to reference specific error entries. |
| `condition` | string | yes | The error condition. |
| `behavior` | string | yes | What the system does in response. |
| `requirement_id` | string | yes | The requirement or edge case that specifies this behavior. Must exist in `requirements`. |

### 6.6 External APIs (optional)

Verified function/method signatures for external library dependencies. When a
spec depends on external packages (not stdlib, not well-known frameworks),
listing the verified API surface prevents requirements from being written
against assumed signatures that don't match reality.

This section is **optional**. Omit it when the spec has no external library
dependencies, or when all dependencies are well-known (e.g., Click, Gin,
Express, Axum). When present, the coding agent should treat this as the
authoritative reference for external API signatures.

```json
{
  "external_apis": [
    {
      "package": "afspec",
      "version": "4.0.3",
      "symbols": [
        {
          "name": "discover_specs",
          "import_path": "afspec.discover",
          "signature": "(spec_root: Path) -> list[SpecMeta]",
          "notes": ""
        },
        {
          "name": "validate",
          "import_path": "afspec.validation",
          "signature": "(spec: Spec) -> ValidationResult",
          "notes": "Returns result object; does NOT raise"
        }
      ]
    }
  ]
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `package` | string | yes | Package, module, or crate name as used by the project's import system. |
| `version` | string | yes | Version that was verified against. |
| `symbols` | array | yes | Verified symbols from this package. At least one required per entry. |

Each symbol:

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Function, method, class, type, or constant name. |
| `import_path` | string | yes | Qualified path to the symbol using the project language's module syntax (e.g., `afspec.validation` for Python, `log/slog` for Go, `@org/pkg` for TypeScript, `crate::module` for Rust). |
| `signature` | string | yes | Signature string using the project language's conventions — parameter names, types, and return type. |
| `notes` | string | no | Clarifications — e.g., "raises LoadError on missing files", "NOT FOUND in library", "returns error as second value". |

Symbols marked `NOT FOUND` in the notes indicate functions the PRD assumed
but the library does not provide. Requirements referencing these must
implement the functionality locally or be revised.

---

## 7. `test_spec.json`

The test specification translates every acceptance criterion, correctness
property, and execution path into a concrete, language-agnostic test
contract. It is derived from `requirements.json` — the coordinator generates
and patches it. Operators do not edit it directly.

### 7.1 Top-level schema

```json
{
  "$schema": "https://agent-fox.dev/schemas/test_spec.v1.json",
  "spec_id": "05",
  "spec_name": "my_feature",
  "schema_version": 1,
  "test_cases": [],
  "property_tests": [],
  "edge_case_tests": [],
  "smoke_tests": [],
  "coverage": {}
}
```

### 7.2 Test cases

One test case per acceptance criterion. The 1:1 mapping is enforced by
cross-file validation.

```json
{
  "id": "TS-05-1",
  "requirement_id": "05-REQ-1.1",
  "kind": "unit",
  "description": "Space creation produces all expected artifacts",
  "preconditions": [
    "clean git working tree",
    "no existing Space with the same slug"
  ],
  "input": {
    "prompt": "test feature"
  },
  "expected": {
    "space_id_format": "ULID",
    "worktree_exists": true,
    "store_exists": true
  },
  "assertion_pseudocode": "result = space_manager.create('test'); assert ULID.matches(result.id); assert exists(result.worktree)"
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Format: `TS-{spec_id}-{N}`. Running number. |
| `requirement_id` | string | yes | The acceptance criterion this test verifies. Must exist in `requirements.json`. |
| `kind` | enum | yes | `"unit"` or `"integration"`. |
| `description` | string | yes | One-sentence description of what is verified. |
| `preconditions` | array of strings | yes | System state required before test runs. May be empty. |
| `input` | object or null | no | Concrete input values or description of input shape. Free-form object. Null when inputs are fully described by preconditions or assertion pseudocode. |
| `expected` | object | yes | Concrete expected output, return value, side effect, or state change. Free-form object. |
| `assertion_pseudocode` | string | yes | Language-agnostic pseudocode for the assertion. May reference concrete module, function, and class names — test-spec pseudocode is the one place where implementation-level names are acceptable (contrast with execution paths, which use logical actors per §6.4). Must not use language-specific syntax. |

### 7.3 Property tests

One per correctness property.

```json
{
  "id": "TS-05-P1",
  "property_id": "05-PROP-1",
  "validates": ["05-REQ-1.1", "05-REQ-2.3"],
  "description": "Every mutation event has a corresponding ledger entry",
  "for_any_strategy": "valid spec mutation event sampled from event generators",
  "invariant_check": "ledger.has_entry(mutation.tx_id) is true"
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Format: `TS-{spec_id}-P{N}`. N matches the property number. |
| `property_id` | string | yes | The correctness property this test verifies. Must exist in `requirements.json`. |
| `validates` | array of strings | yes | Requirement IDs validated. Copied from the property for traceability. |
| `description` | string | yes | One-sentence description. |
| `for_any_strategy` | string | yes | How the property test generator should sample inputs. |
| `invariant_check` | string | yes | The invariant assertion in pseudocode. |

### 7.4 Edge case tests

Same structure as test cases (§7.2). One per edge case requirement
(`{spec_id}-REQ-{N}.E{C}`).

```json
{
  "id": "TS-05-E1",
  "requirement_id": "05-REQ-1.E1",
  "kind": "unit",
  "description": "Partial worktree creation rolls back cleanly",
  "preconditions": ["worktree creation will fail after directory creation"],
  "input": { "prompt": "test", "inject_failure": "after_mkdir" },
  "expected": { "no_partial_state": true, "error_reported": true },
  "assertion_pseudocode": "with mock_failure('after_mkdir'): result = space_manager.create('test'); assert result.is_error; assert not exists(partial_worktree)"
}
```

The `id` format is `TS-{spec_id}-E{N}` where N is a running number.

### 7.5 Smoke tests

One per execution path. These are integration tests that traverse a full
path from entry point to observable side effect.

```json
{
  "id": "TS-05-SMOKE-1",
  "execution_path_id": "05-PATH-1",
  "description": "End-to-end Space creation through first task dispatch",
  "trigger": "space_manager.create('test') followed by coordinator.dispatch()",
  "real_components": ["SpaceManager", "WorktreeManager", "Coordinator"],
  "mockable": ["filesystem (transient tmpdir)", "remote git (push only)"],
  "expected_effects": [
    "worktree directory exists with initialized git",
    "store contains the Space row",
    "requirements.json exists with at least one requirement"
  ]
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Format: `TS-{spec_id}-SMOKE-{N}`. |
| `execution_path_id` | string | yes | The execution path this test covers. Must exist in `requirements.json`. |
| `description` | string | yes | One sentence describing the end-to-end behavior verified. |
| `trigger` | string | yes | How the path is invoked. |
| `real_components` | array of strings | yes | Components that must NOT be mocked — the ones named in the execution path. |
| `mockable` | array of strings | yes | What may be stubbed (only external I/O). |
| `expected_effects` | array of strings | yes | Concrete observable outcomes. |

### 7.6 Coverage

Computed, not authored. Populated on every save by the validation library.

```json
{
  "coverage": {
    "requirements_covered": ["05-REQ-1.1", "05-REQ-1.2", "05-REQ-1.E1"],
    "properties_covered": ["05-PROP-1"],
    "paths_covered": ["05-PATH-1"],
    "gaps": []
  }
}
```

| Field | Type | Description |
|---|---|---|
| `requirements_covered` | array of strings | All requirement and edge case IDs that have a test case. |
| `properties_covered` | array of strings | All property IDs that have a property test. |
| `paths_covered` | array of strings | All execution path IDs that have a smoke test. |
| `gaps` | array of strings | IDs lacking coverage. Must be empty on save. |

---

## 8. `tasks.json`

The implementation plan. The most actively mutated file — task state changes
happen throughout execution.

### 8.1 Top-level schema

```json
{
  "$schema": "https://agent-fox.dev/schemas/tasks.v1.json",
  "spec_id": "05",
  "spec_name": "my_feature",
  "schema_version": 1,
  "test_commands": {
    "spec_tests": "pytest -q tests/spec/test_05.py",
    "all_tests": "pytest -q",
    "linter": "ruff check"
  },
  "dependencies": [],
  "task_groups": [],
  "traceability": []
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `test_commands` | object | yes | Commands for running tests. Contains `spec_tests`, `all_tests`, `linter` (all strings). |
| `dependencies` | array | yes | Cross-spec dependencies (§8.2). May be empty. |
| `task_groups` | array | yes | Ordered list of task group objects (§8.3). At least one required. |
| `traceability` | array | yes | Traceability entries (§8.5). |

### 8.2 Dependencies

Cross-spec dependencies at task-group granularity.

```json
{
  "depends_on_spec": "01",
  "from_group": 3,
  "to_group": 1,
  "relationship": "Imports CLI registration from group 3",
  "sentinel": false
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `depends_on_spec` | string | yes | The `spec_id` of the dependency (e.g. `"01"`, not the folder name). Consistent with all other cross-spec references. |
| `from_group` | integer | yes | Task group in the dependency spec that produces the needed artifact. `0` is a sentinel for "upstream not yet planned." |
| `to_group` | integer | yes | Earliest task group in this spec that needs the artifact. |
| `relationship` | string | yes | What the dependency provides and why `from_group` is the earliest sufficient one. |
| `sentinel` | boolean | yes | `true` when `from_group` is `0` (upstream spec unplanned). |

### 8.3 Task groups

```json
{
  "id": 1,
  "kind": "tests",
  "title": "Write failing spec tests",
  "subtasks": [],
  "verification": {}
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | integer | yes | Positive integer. Sequential within the spec. |
| `kind` | enum | yes | `"tests"`, `"standard"`, `"checkpoint"`, or `"wiring_verification"`. |
| `title` | string | yes | Short descriptive title. |
| `subtasks` | array | yes | Subtask objects (§8.3.1). Target 3–6 per group (excluding verification). |
| `verification` | object | yes | Verification subtask (§8.3.2). |

**Structural rules (schema-enforced):**

- Task group 1 must have `kind: "tests"`.
- The final task group must have `kind: "wiring_verification"`.
- No more than one `"wiring_verification"` group per spec.
- `"checkpoint"` groups may appear between implementation groups.
- Target 3–6 subtasks per group. More than 6 suggests splitting; fewer than
  2 suggests merging. These are guidelines, not hard limits.

#### 8.3.1 Subtask

```json
{
  "id": "2.1",
  "title": "Implement Space creation",
  "details": [
    "Allocate worktree from source branch",
    "Initialize SQLite store with schema"
  ],
  "test_spec_refs": ["TS-05-1", "TS-05-2"],
  "requirement_refs": ["05-REQ-1.1", "05-REQ-1.2"],
  "state": "pending",
  "optional": false
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Format: `{group_id}.{N}`. |
| `title` | string | yes | Short descriptive title in imperative form. |
| `details` | array of strings | yes | Implementation details as bullet points. |
| `test_spec_refs` | array of strings | yes | Test spec IDs this subtask should make pass. May be empty. |
| `requirement_refs` | array of strings | yes | Requirement IDs this subtask satisfies. May be empty. |
| `state` | enum | yes | Current state (see state machine below). |
| `optional` | boolean | yes | `true` for nice-to-have subtasks. Default `false`. |

##### Subtask state machine

```
                    ┌─────────────────────────────────┐
                    ▼                                  │
pending ──→ queued ──→ in_progress ──→ done ──→ pending_reevaluation
   │           │                                      │
   │           └──→ pending                           └──→ pending
   │                                                  └──→ dropped
   └──→ dropped
            ▲
            │
   queued ──┘
```

| State | Meaning | Allowed next states |
|---|---|---|
| `pending` | Not started | `queued`, `dropped` |
| `queued` | Selected for dispatch | `in_progress`, `pending`, `dropped` |
| `in_progress` | An archetype is executing it | `done`, `pending_reevaluation` |
| `done` | Verification passed | `pending_reevaluation` |
| `pending_reevaluation` | Upstream requirement changed; needs review | `pending`, `dropped` |
| `dropped` | Removed with explicit rationale | (terminal) |

Illegal transitions are rejected by the validation library.

**Runtime metadata (run ID, started_at, agent assignment) does not belong
here.** `tasks.json` is declarative planning state. Runtime state belongs in
the operational store.

#### 8.3.2 Verification subtask

Every task group has exactly one verification subtask.

```json
{
  "id": "2.V",
  "checks": [
    "Spec tests for this group pass: pytest -q tests/spec/test_05.py -k 'group2'",
    "All existing tests still pass: pytest -q",
    "No linter warnings introduced: ruff check",
    "Requirements 05-REQ-1.1, 05-REQ-1.2 acceptance criteria met"
  ]
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Format: `{group_id}.V`. |
| `checks` | array of strings | yes | Verification criteria. At least one required. Should include test commands. |

### 8.4 Wiring verification (final task group)

The last task group of every spec must be a wiring verification group. Its
purpose is to catch integration gaps. It cannot be satisfied by checking
components in isolation.

Required subtasks:

1. **Trace execution paths** — For each path in `requirements.json`, verify
   the entry point calls the next function in the chain. Confirm no stub
   remains unreplaced.
2. **Verify return value propagation** — For every function that returns
   data consumed by a caller, confirm the caller receives and uses it.
3. **Run smoke tests** — All `TS-{spec_id}-SMOKE-*` tests pass with real
   components.
4. **Stub/dead-code audit** — Search files for `return []`, `return None`
   on non-Optional returns, `pass` in non-abstract methods, `# TODO`,
   `NotImplementedError`. Each must be justified or replaced.
5. **Cross-spec entry point verification** — For paths whose entry point
   is owned by another spec, confirm the entry point is called from
   production code, not just tests.

**Hard rule:** An execution path that is not live in production code fails
the wiring verification. Errata or deferrals do not satisfy the check.

### 8.5 Traceability

Bidirectional links from requirements through test specs and tasks to
executable tests.

```json
{
  "requirement_id": "05-REQ-1.1",
  "test_spec_id": "TS-05-1",
  "task_id": "2.1",
  "test_path": "tests/spec/test_05.py::test_space_creation"
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `requirement_id` | string | yes | The requirement being traced. |
| `test_spec_id` | string | yes | The test spec entry for this requirement. |
| `task_id` | string | yes | The subtask that makes this specific test pass. |
| `test_path` | string or null | yes | Path to the actual test in the source tree. Null until the test is written. |

**Cardinality:** One entry per (`requirement_id`, `test_spec_id`) pair.
A requirement with both a unit test (`TS-05-1`) and a smoke test
(`TS-05-SMOKE-1`) gets two entries. A subtask that satisfies multiple
requirements appears in multiple entries (one per requirement it covers).
The pair is the primary key — duplicates are rejected.

`test_path` is the only field that references a file in the source tree.

---

## 9. Lifecycle

Lifecycle state is stored in `prd.md` frontmatter `status`. Transitions are
enforced by the library — free-form edits to the status field are rejected.

| State | Meaning | Mutations allowed |
|---|---|---|
| `draft` | Being authored | All, including Intent edits |
| `active` | Work in progress | All except Intent section and immutable frontmatter fields (`created_at`, `spec_id`, `spec_name`) |
| `sealed` | Complete; no further mutation | None |
| `superseded` | Replaced by another spec | None (deprecation banner applied automatically) |
| `archived` | Moved to `archive/` | None |

### 9.1 Superseding

When a new spec replaces an existing one:

1. The new spec sets `supersedes: ["03"]` in its frontmatter.
2. The library adds a deprecation banner to the top of the superseded
   spec's `prd.md`.
3. The superseded spec's status transitions to `superseded`.
4. The superseded spec folder is moved to `<spec_root>/specs/archive/`.

### 9.2 Archiving without superseding

A spec may be archived without being replaced — for example, a feature that
was prototyped but abandoned, or a spec that was completed and is no longer
actively referenced. The transition is `sealed → archived` (or
`draft → archived` for abandoned work). The library moves the folder to
`<spec_root>/specs/archive/` and sets `status: "archived"`. No deprecation
banner is added because there is no replacement spec to point to.

---

## 10. Validation

Two layers, both run on every mutation:

### 10.1 Schema validation

JSON Schema validation per file. Sub-millisecond. Rejects:

- Malformed structure or unknown fields.
- Missing required fields.
- EARS pattern field mismatches (wrong fields for the declared pattern).
- Illegal state transitions.
- Invalid ID formats.

`architecture.md` is exempt from schema validation — it is free-form
markdown with no required structure.

### 10.2 Cross-file integrity

Custom validation across the four required artifacts. Runs after schema
validation succeeds. `architecture.md` is not included in cross-file
integrity checks. Rules:

1. Every `requirement_id` referenced in `test_spec.json`, `tasks.json`
   traceability, and `error_handling` must exist in `requirements.json`.
2. Every requirement and edge case in `requirements.json` must be covered
   by a test case in `test_spec.json`.
3. Every correctness property must be referenced by a property test.
4. Every execution path must be referenced by a smoke test.
5. Every `test_spec_id` referenced in `tasks.json` must exist in
   `test_spec.json`.
6. Glossary cross-check (see below).
7. `spec_id` and `spec_name` must be consistent across all four required files.
8. No two entries in the `tasks.json` traceability array may share the
   same `(requirement_id, test_spec_id)` pair.

**Glossary cross-check (rule 6) details:**

Scope: the glossary covers domain terms in `requirements.json` only.
Fields checked: `action`, `trigger`, `condition`, `error_condition`,
`state`, `feature`, `for_any`, `invariant`. Terms in `test_spec.json` and
`tasks.json` are not glossary-checked because they may use
implementation-level vocabulary.

Term detection: any token wrapped in backticks (`` `SpaceManager` ``,
`` `LivingSpec` ``) within a checked field is treated as a domain term and
must have a glossary entry. Unquoted natural-language words are not checked.
This gives authors explicit control over what the validator flags: wrap a
term in backticks to declare it domain-specific, leave it unquoted to treat
it as common English.

A mutation that breaks integrity is rejected. The coordinator fixes
dependent files in the same transaction or does not make the change.

### 10.3 Standalone validation

The library exposes a standalone `validate()` for CI and pre-commit hooks.
This runs both layers without requiring a mutation.

---

## 11. Rendering

The library provides a renderer that produces markdown from JSON artifacts.
Rendering is deterministic: same JSON in, same markdown out, byte-for-byte.

### 11.1 Render targets

| Target | Description |
|---|---|
| Per-file | Markdown rendering of one JSON file |
| Combined | PRD markdown (as-is), then `architecture.md` (as-is, if present), followed by rendered JSON artifacts in order: requirements → test_spec → tasks |

### 11.2 EARS rendering

EARS sentences are rendered from decomposed fields using the templates in
§6.2.1. The rendered form is a derived view, never the source of truth.

---

## Appendix A: ID format summary

| Entity | Format | Example |
|---|---|---|
| Requirement | `{spec_id}-REQ-{N}` | `05-REQ-3` |
| Acceptance criterion | `{spec_id}-REQ-{N}.{C}` | `05-REQ-3.2` |
| Edge case | `{spec_id}-REQ-{N}.E{C}` | `05-REQ-3.E1` |
| Correctness property | `{spec_id}-PROP-{N}` | `05-PROP-2` |
| Execution path | `{spec_id}-PATH-{N}` | `05-PATH-1` |
| Error handling entry | `{spec_id}-ERR-{N}` | `05-ERR-1` |
| Test case | `TS-{spec_id}-{N}` | `TS-05-3` |
| Property test | `TS-{spec_id}-P{N}` | `TS-05-P2` |
| Edge case test | `TS-{spec_id}-E{N}` | `TS-05-E1` |
| Smoke test | `TS-{spec_id}-SMOKE-{N}` | `TS-05-SMOKE-1` |
| Task group | `{N}` (integer) | `3` |
| Subtask | `{group}.{N}` | `3.2` |
| Verification | `{group}.V` | `3.V` |

## Appendix B: JSON Schema file listing

The following schema files are required for validation:

| Schema | Validates |
|---|---|
| `requirements.v1.json` | `requirements.json` |
| `test_spec.v1.json` | `test_spec.json` |
| `tasks.v1.json` | `tasks.json` |
| `prd-frontmatter.v1.json` | YAML frontmatter of `prd.md` (parsed as JSON) |

Schemas are bundled with the library package and distributed alongside it.
The `$schema` URIs in JSON files (e.g.
`https://agent-fox.dev/schemas/requirements.v1.json`) are informational —
they enable editor autocompletion and hover documentation. The library
validates against its bundled copy, not the hosted URL.

`architecture.md` has no JSON schema. It is free-form markdown and is not
schema-validated.
