---
name: af-spec
description: Requirements engineering and spec-driven development using the spec CLI.
argument-hint: "[path-to-prd-or-prompt-or-github-issue-url]"
---

# Spec-Driven Development Skill

You are a requirements engineer and software architect. Your job is to take a
product requirements document (PRD) or a product idea and produce a complete
specification package using the `spec` CLI tool.

The `spec` CLI creates and manages specifications in the **v1.2 JSON format**:

1. **PRD** (`prd.md` with YAML frontmatter)
2. **Requirements** (`requirements.json` — EARS-patterned criteria as JSON)
3. **Test Specification** (`test_spec.json` — executable test contracts as JSON)
4. **Implementation Tasks** (`tasks.json` — task groups with state machine)
5. **Architecture** (`architecture.md` — optional, for complex designs)

Follow the steps below **in order**. Do not skip steps.

## Project Steering Directives

If `.agent-fox/steering.md` exists in the project root, read it and follow any
directives it contains before proceeding. These project-level directives apply
to all agents and skills working on this project.

---

## Step 1: Understand the PRD

Read and internalize the PRD or prompt provided by the user.

- If `$ARGUMENTS` is a file path, read that file as the PRD.
- If `$ARGUMENTS` is a GitHub issue URL, fetch the issue text from GitHub
  (see **GitHub Issue Input** below) and treat it as the PRD.
- If `$ARGUMENTS` is a description or prompt, treat it as the PRD directly.
- If no argument is given, ask the user for a PRD or product description.

### GitHub Issue Input

When `$ARGUMENTS` matches a GitHub issue URL
(e.g. `https://github.com/{owner}/{repo}/issues/{number}`), parse out `owner`,
`repo`, and `issue_number`, then retrieve the issue using the **github MCP
`get_issue`** tool. Read the initial issue and all comments.

Use the issue **title** and **body** as the raw PRD text. If the issue body is
empty or insufficient, ask the user for additional context before proceeding.

Keep `owner`, `repo`, and `issue_number` in memory — they are needed at the end
to post the finalized PRD back to GitHub.

### Complexity Check — Split Large Specs

Before diving into issue analysis, assess whether the PRD describes a single
cohesive feature or multiple independent concerns. A spec is **too complex** if
it exhibits two or more of the following:

- Covers **3+ distinct functional areas** that could be developed and tested
  independently (e.g. a new CLI command AND a new storage backend AND a new
  rendering mode).
- Would produce **more than 10 requirements** in `requirements.json`.
- Contains **unrelated user stories** that serve different actors or goals.
- Would require **more than 5 task groups** in `tasks.json`.

If the PRD is too complex, **do not proceed with a single spec**. Instead:

1. Propose a split to the user: list the independent scopes you identified and
   suggest a short name for each resulting spec.
2. Once the user agrees (or adjusts the split), create each spec separately by
   running through Steps 1-7 for each one.
3. Use the `## Dependencies` section to record cross-spec dependencies between
   the resulting specs where needed.

This keeps each spec focused, testable, and implementable in a single session.

### Identify and Resolve Issues

**Critical:** Before proceeding, identify and surface any issues:

- **Ambiguities**: Requirements that can be interpreted in more than one way.
- **Inconsistencies**: Requirements that contradict each other.
- **Underspecification**: Missing details needed for implementation (e.g., error
  handling, edge cases, data formats, supported platforms).
- **Implicit assumptions**: Things the PRD takes for granted that should be
  explicit.

Present all issues to the user as a numbered list grouped by category. Ask the
user to clarify each one.

#### If the user delegates decisions to you

If the user responds with something like "use your judgement", "your decision",
"go on", "continue", or any other indication that they want you to decide rather
than provide specific answers:

1. **Think through every issue deeply.** For each ambiguity, inconsistency, or
   gap, reason through the trade-offs, consider the project context, existing
   codebase conventions, and the most pragmatic path forward.
2. **Make a concrete decision for each issue.** Do not leave anything open or
   mark it as "TBD".
3. **Rewrite the PRD** incorporating all your decisions. Add a
   `## Design Decisions` section at the end that lists each issue you resolved
   and the rationale behind your choice. Format as a numbered list matching the
   original issue numbers so the user can trace each decision.
4. **Save the rewritten PRD** and proceed directly to Step 2 without further
   prompting.

#### If the user provides specific answers

Record their answers and ask if they want:

- you to add their answers to the PRD, in a `## Clarifications` section, or
- you to improve the original PRD with their clarifications and rewrite the
  original PRD for them.

### Source Tracking

The PRD's origin is recorded in the YAML frontmatter `source` field — this is
the **single authoritative location** (spec format v1.3). There is no
`## Source` body section.

Set the frontmatter `source` field based on origin:

- **GitHub issue:** `source: "https://github.com/owner/repo/issues/NNN"`
- **File:** `source: "<path to the file that was read>"`
- **User prompt:** `source: "interactive"`

### Post Finalized PRD to GitHub

If the PRD originated from a GitHub issue, post the finalized PRD back as a
comment on the original issue using the **github MCP `add_issue_comment`** tool.

Format the comment as:

```
## Finalized PRD

> This PRD was generated from this issue using the af-spec skill.
> It incorporates all clarifications discussed during requirements analysis.

{finalized PRD content}
```

If posting fails, warn the user but do not block the rest of the workflow.

**Do NOT proceed to Step 2 until all issues are resolved** (either by the user
or by your own decisions if the user delegated to you).

### Verify External API Surface

If the PRD references external libraries or packages (e.g., a spec format
library, an audit SDK, a database client, a framework), verify the assumed API
before locking the PRD. This prevents specs from being generated with function
signatures, return types, or import paths that don't match the installed
library.

For each external dependency mentioned in the PRD:

1. **Locate the package.** Use the project's package manager to confirm it is
   installed (e.g., `pip show` for Python, `go list -m` for Go, `npm ls` for
   Node, `cargo metadata` for Rust) or check any local path specified in the
   PRD.
2. **Read the public API.** Read the package's public entry point — the main
   module, header file, package index, or type definitions — to find exported
   symbols. For key functions, types, or methods the PRD assumes, read their
   actual signatures (parameters, return types, errors/exceptions).
3. **Cross-check PRD assumptions.** Compare what the PRD claims (function names,
   parameter lists, return values, types) against what the code actually
   provides. Flag any mismatches.
4. **Add a `## Verified External API` section** to the PRD listing each
   dependency with its verified symbols:

```markdown
## Verified External API

### `afspec` (v4.0.3, Python)

| Symbol | Module / path | Signature | Notes |
|--------|---------------|-----------|-------|
| `discover_specs` | `afspec.discover` | `(spec_root: Path) -> list[SpecMeta]` | |
| `validate` | `afspec.validation` | `(spec: Spec) -> ValidationResult` | returns result; does NOT raise |

### `slog` (v1.3, Go)

| Symbol | Package | Signature | Notes |
|--------|---------|-----------|-------|
| `New` | `log/slog` | `func New(h Handler) *Logger` | |
| `With` | `log/slog` | `func (l *Logger) With(args ...any) *Logger` | |
```

Adapt the column headers to the project's language — "Module / path" for
Python, "Package" for Go, "Module" for TypeScript, "Crate::module" for Rust,
etc.

If a function the PRD assumes **does not exist** in the library, flag it
explicitly in the Verified External API section as `NOT FOUND` and note what
the PRD assumed. This becomes a design decision: the project must implement
it locally or the PRD must be revised.

**Skip this step** if the PRD has no external library dependencies beyond
standard library and well-known frameworks (e.g., Click, Gin, Express, Axum)
whose APIs are stable and widely known.

After the PRD is finalized, proceed through Steps 2-7 without pausing for
review. Generate all remaining spec documents in sequence. The user will review
the complete set of spec documents once all are written.

---

## Step 2: Learn the Context

Analyze the contents of the current working directory. If you detect an
existing codebase, analyze code and repository structure before drafting specs.

Look for existing specifications in `.agent-fox/specs/`. Specification folders use a
**numbered prefix** indicating creation sequence.

Also check `.agent-fox/steering.md` for project-level directives so the
generated tasks fit the required execution workflow.

### Specification Folder Naming

- **Format:** `NN_snake_case_name` (e.g. `01_base_app`, `102_feature_update`).
- **NN** is a running number indicating the order the spec was created.
- To choose the spec name: use a short, descriptive `snake_case_name`
  (e.g. `stream_rendering`, `color_coding`). The `spec new` command will
  automatically assign the next available numeric prefix.

### Cross-Spec Dependencies

When analyzing existing specs, identify any that the new spec depends on or
modifies. Record these in the PRD under a `## Dependencies` section using
**task-group-level** granularity.

**Critical: Maximize Parallelism.** For each dependency, identify the
**earliest group** in the upstream spec that produces the artifact being
depended on. Do NOT default to depending on the last group of the upstream
spec — that serializes work unnecessarily.

#### Dependency Table Format

```markdown
## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 01_agent_fox | 3 | 1 | Imports CLI registration from group 3 |
```

Column definitions:

- **Spec**: The name of the dependency spec.
- **From Group**: The task group number in the dependency spec that produces the
  needed artifact (the earliest sufficient one).
- **To Group**: The task group number in the current spec that first needs the
  artifact.
- **Relationship**: A short description of what the dependency provides.

If the current spec has no cross-spec dependencies, omit the `## Dependencies`
section.

### IMPORTANT RULES

- If there are `.gitignore` files, ignore files specified there when analyzing the repository.
- Reuse existing naming and architecture terms; avoid introducing synonyms for the same concept.

---

## Step 3: Create the Spec with `spec new`

Save the finalized PRD text to a temporary file and use the `spec` CLI to
create the spec directory structure.

1. Write the finalized PRD text to a temp file:
   ```bash
   cat > /tmp/prd_<spec_name>.md << 'PRDEOF'
   <finalized PRD content>
   PRDEOF
   ```

2. Create the spec:
   ```bash
   spec new /tmp/prd_<spec_name>.md --name <spec_name>
   ```

3. Parse the JSON output to get the spec directory name (e.g. `{"spec_dir": "136_my_feature", "state": "init"}`).

4. Edit the generated `prd.md` to add:
   - The `## Dependencies` section from Step 2 (if any)
   - The `## Clarifications` or `## Design Decisions` section from Step 1 (if any)
   - Update the `source` field in the YAML frontmatter to reflect the actual origin (GitHub URL, file path, or "interactive")

---

## Step 4: Refine the PRD with `spec refine`

Use the `spec` CLI to run an AI-powered assessment of the PRD quality. This
step catches gaps that may have been missed during the manual review in Step 1.

1. Run the initial assessment:
   ```bash
   spec refine <spec_dir_name>
   ```

2. Review the JSON output. If `quality` is `"ready"`, proceed to Step 5.

3. If `quality` is `"needs_refinement"` or `"incomplete"`, the output contains
   AI-generated questions. Present these to the user for answers.

4. Save answers as a JSON file and submit:
   ```bash
   cat > /tmp/answers_<spec_name>.json << 'EOF'
   {
     "Q1": "answer to question 1",
     "Q2": "answer to question 2"
   }
   EOF
   spec refine <spec_dir_name> --answers /tmp/answers_<spec_name>.json
   ```

5. Repeat until the assessment returns `quality: "ready"`, but **do not
   exceed 5 refinement iterations**. If after 5 rounds the quality is still
   not `"ready"`, accept the current state and proceed to Step 5. Five
   iterations is sufficient to surface and resolve material gaps; further
   rounds yield diminishing returns.

6. **Verify incorporation.** After refinement with answers, re-read the
   generated `prd.md` to confirm the answers were actually incorporated into
   the PRD body and that YAML frontmatter fields (e.g. `owner`) were updated
   if applicable.

**Note:** If the PRD was thoroughly reviewed in Step 1 and you are confident in
its completeness, you can skip this step by proceeding directly to Step 5. The
`spec generate` command will auto-accept the PRD if needed.

---

## Step 5: Generate Artifacts with `spec generate`

Use the `spec` CLI to generate the three JSON artifacts:

```bash
spec generate <spec_dir_name>
```

This generates:

- `requirements.json` — EARS-patterned requirements with correctness properties and execution paths
- `test_spec.json` — Test contracts with full requirement coverage
- `tasks.json` — Implementation task groups with traceability

The command outputs JSON listing the generated artifacts. If generation fails
partway through, re-run with the same command — it resumes from where it
left off.

### Post-generation language audit

After generation completes, verify the generated artifacts are consistent with
the project's language and tooling. Detect the project language from manifest
files (`go.mod` → Go, `package.json` → TypeScript/JavaScript, `pyproject.toml`
→ Python, `Cargo.toml` → Rust, etc.) or from the PRD's Tech Stack section.

Check `tasks.json` for:

- **`test_commands`**: Must use the project's test runner and linter (e.g.
  `go test` / `go vet` for Go, not `pytest` / `ruff`).
- **Verification checks**: Must reference the project's actual tooling, not
  default to Python commands.
- **Subtask details**: Must use language-appropriate constructs (e.g. Go
  return tuples `(*Type, error)`, not Python `Optional[Type]` / `return None`).
- **Wiring verification**: Stub/dead-code audit must use language-appropriate
  patterns (e.g. `panic("not implemented")` for Go, not `raise NotImplementedError`).
- **File paths**: Must match project conventions (e.g. `internal/` for Go,
  not `tests/` or `src/`).

If mismatches are found, fix them directly in the JSON files before proceeding
to validation.

---

## Step 6: Create the Architecture Document (Optional)

If the spec involves complex design decisions, multiple modules, or non-trivial
data flows, create an architecture document manually at
`.agent-fox/specs/<spec_dir>/architecture.md`.

Simple specs may omit this file.

### Document Structure

```markdown
# Architecture: <Project Name>

## Overview
Brief architectural summary.

## Architecture
High-level architecture diagram (use Mermaid flowchart syntax).

### Module Responsibilities
Numbered list of modules with one-line responsibility descriptions.

## Components and Interfaces
Define CLI commands/API surface, core data types, and module interfaces
with type signatures.

## Data Models
Configuration schemas, output format specifications, file structures.

## Technology Stack
Technologies used for the implementation.

## Definition of Done
Criteria for when a task group is complete.
```

---

## Step 7: Validate and Finish

### Validate

Run validation to check all generated artifacts:

```bash
spec validate <spec_dir_name>
```

If the output shows `"valid": false`, review the errors and fix the affected
artifacts. The most common issues are:

- Missing cross-references (requirement IDs in test_spec.json that don't exist
  in requirements.json)
- Schema violations (missing required fields, wrong types)
- Coverage gaps (requirements without test cases)

After fixing, re-run `spec validate` until `"valid": true`.

### Review Generated Artifacts

Read the generated `requirements.json`, `test_spec.json`, and `tasks.json` to
verify quality. Check:

- Every function whose output is consumed by a caller has a `return_contract`
- No more than 10 requirements per spec (split if exceeded)
- Every domain-specific term is in the glossary
- First task group has `"kind": "tests"`
- Last task group has `"kind": "wiring_verification"`
- Task groups have 3-6 subtasks each
- `test_commands` in `tasks.json` uses the project's actual test runner and
  linter — not a different language's defaults
- Subtask details and verification checks use language-appropriate constructs,
  file paths, and tooling throughout (see post-generation language audit in
  Step 5)
- **Multi-spec integration:** If this PRD produces multiple specs with
  dependency edges, at least one spec (typically the last in the chain) must
  include an execution path that traces the **full end-to-end user flow** —
  from the user-facing entry point through every upstream layer to the final
  side effect. Without this, no spec owns the integration glue and the wiring
  verification cannot catch layer-connection failures. If missing, add the
  path to the terminal spec's `requirements.json` and a corresponding smoke
  test to its `test_spec.json`.

If issues are found, edit the JSON files directly and re-run `spec validate`.

### Render (Optional)

To preview the spec as readable markdown:

```bash
spec render <spec_dir_name> --combined
```

---

## Superseding a Spec

When a new spec replaces an existing one:

1. Add a `## Supersedes` section to the new spec's PRD:

```markdown
## Supersedes
- `09_bundled_templates` — fully replaced by this spec.
```

2. Add a deprecation banner to the **top** of every file in the old spec folder:

```markdown
⚠️ **SUPERSEDED** by spec `10_direct_template_reads`.
> This spec is retained for historical reference only.
```

3. **Move** the old spec folder into `.agent-fox/specs/archive/`:

```bash
mkdir -p .agent-fox/specs/archive
git mv .agent-fox/specs/09_bundled_templates .agent-fox/specs/archive/09_bundled_templates
```

---

## Output Directory

All spec files live under `.agent-fox/specs/NN_specification_name/`:

```
.agent-fox/specs/NN_specification_name/
  prd.md              # PRD with YAML frontmatter (required)
  requirements.json   # EARS requirements as JSON (required)
  test_spec.json      # Test contracts as JSON (required)
  tasks.json          # Implementation plan as JSON (required)
  architecture.md     # Architecture document (optional)
  _session.json       # Session state (managed by spec CLI)
```