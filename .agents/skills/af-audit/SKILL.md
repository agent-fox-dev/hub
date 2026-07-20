---
name: af-audit
description: Full-repo quality audit against active specifications -- checks spec conformance, code quality, and test adequacy.
argument-hint: "[spec-number-or-name]"
---

# af-audit -- Specification Compliance Audit

If `.agent-fox/steering.md` exists in the project root, read it and follow any
directives found there before proceeding.

You are a senior quality engineer who audits implementations against their
specifications. You cross-reference requirements, acceptance criteria, and test
contracts with the actual code and tests to surface gaps, deviations, and quality
issues. You are thorough, precise, and evidence-based -- every finding cites the
specific requirement ID and the code location it concerns.

**Audit scope:** This skill performs a full quality audit covering three
dimensions: (1) spec conformance, (2) code quality, and (3) test adequacy.
Only active specs are audited -- specs under `.agent-fox/specs/archive/` are
ignored.

---

## Step 1: Discover Active Specs

List all directories in `.agent-fox/specs/` excluding `archive/`. Each directory
is a spec with a numeric prefix and snake_case name (e.g.,
`01_afaudit_package`).

For each spec directory, read the `prd.md` frontmatter to extract `spec_id`,
`spec_name`, `title`, and `status`.

**Scoping rules:**

- If `$ARGUMENTS` names a specific spec (by number, name, or partial match),
  audit only that spec.
- If `$ARGUMENTS` is empty, audit all active specs.
- If no active specs exist, report that and stop.

Print a summary of specs to be audited:

```
[af-audit] Found N active spec(s):
  - 01 afaudit_package: "Afaudit Package"
  ...
```

---

## Step 2: Read Project Context

Before diving into specs, orient yourself in the codebase:

1. Read `README.md` for project overview and structure.
2. Read `CLAUDE.md` or `AGENTS.md` for agent instructions and conventions.
3. Read `.agent-fox/steering.md` for project-level directives.
4. Run `git log --oneline -10` and `git status --short --branch` for recent
   context.

Identify the main package structure, test framework (pytest, etc.), and coding
conventions. This context informs how you evaluate code quality and test
adequacy.

**Important:** Only read files tracked by git. Skip anything matched by
`.gitignore`. When in doubt, run `git ls-files` to see what's tracked.

---

## Step 3: Per-Spec Deep Read

For each spec being audited, read all five artifacts in this order:

### 3a. PRD (`prd.md`)

Read the full PRD. Extract:
- **Goals** -- what the spec aims to achieve.
- **Scope** -- what is in scope and out of scope.
- **Files involved** -- which source files the spec touches.
- **Key design decisions** -- constraints, patterns, or approaches mandated.

### 3b. Requirements (`requirements.json`)

Parse the JSON. For each requirement, note:
- `id` (e.g., `01-REQ-1.1`)
- `title` and `user_story`
- Each `acceptance_criteria` entry with its `id` and `action`
- Each `edge_cases` entry

Also read `correctness_properties` and `execution_paths` if present -- these
describe invariants and expected behavior paths that the implementation must
satisfy.

### 3c. Test Spec (`test_spec.json`)

Parse the JSON. Catalog:
- `test_cases` -- each with `id`, `requirement_id`, `kind` (unit/integration/
  property), `description`, `assertion_pseudocode`
- `property_tests` -- hypothesis/fuzzing contracts
- `edge_case_tests` -- boundary and error path tests
- `smoke_tests` -- end-to-end validation tests
- `coverage` -- the spec's own coverage mapping

### 3d. Tasks (`tasks.json`)

Read the task plan. Note:
- Which tasks are marked done vs. pending.
- Which files each task modifies.
- The implementation order and dependencies.

### 3e. Implementation Files

From the PRD, requirements, and tasks, compile the list of source files the
spec touches. Read each file. Also identify the corresponding test files
(typically in a `tests/` directory mirroring the source structure).

---

## Step 4: Evaluate Spec Conformance

For each requirement in `requirements.json`, evaluate whether the
implementation satisfies it. Work through every acceptance criterion
systematically:

### 4a. Acceptance Criteria Check

For each acceptance criterion (`XX-REQ-N.M`):

1. Locate the code that implements this criterion.
2. Verify the implementation matches the specified behavior.
3. Classify as:
   - **Satisfied** -- implementation clearly matches the criterion.
   - **Partially satisfied** -- implementation exists but is incomplete or
     deviates in a minor way.
   - **Not satisfied** -- no implementation found, or implementation contradicts
     the criterion.
   - **Cannot determine** -- criterion is ambiguous or requires runtime testing
     to verify.

### 4b. Edge Case Coverage

For each edge case in the requirements:
- Is there explicit handling in the code?
- Is there a test that exercises this edge case?

### 4c. Correctness Properties

If `correctness_properties` exist in the requirements:
- Does the implementation maintain the stated invariants?
- Are there property tests that verify them?

### 4d. Execution Paths

If `execution_paths` exist:
- Can you trace each path through the code?
- Are all branches reachable?

Record findings with specific requirement IDs and file locations.

---

## Step 5: Evaluate Code Quality

Review the implementation files for quality issues that go beyond spec
conformance:

### 5a. Error Handling

- Are error cases from the spec handled with appropriate exceptions or error
  returns?
- Are error messages informative enough for debugging?
- Is there consistent error handling style (exceptions vs. return codes vs.
  Result types)?

### 5b. Architectural Fit

- Does the implementation follow patterns already established in the codebase?
- Are new abstractions justified, or do they add unnecessary complexity?
- Is the code consistent with the project's conventions (naming, module
  structure, import style)?

### 5c. Boundary Validation

- Is input validated at system boundaries (CLI args, config files, external
  API responses)?
- Are there assumptions about input format that could break silently?

### 5d. Unnecessary Complexity

- Dead code, unreachable branches, or over-engineering.
- Abstractions with only one implementation (premature generalization).
- Complex logic that could be simplified without losing correctness.

---

## Step 6: Evaluate Test Adequacy

Cross-reference the test spec with actual test files to find gaps and assess
coverage quality.

### 6a. Test Case Implementation

For each test case in `test_spec.json`:

1. Search for a corresponding test function or class in the test files.
   Match by test ID (e.g., `TS-01-1` in the docstring or test name) or by
   the requirement ID it covers.
2. Classify as:
   - **Implemented** -- a test exists that matches the contract.
   - **Partially implemented** -- a test exists but does not fully cover the
     assertion pseudocode.
   - **Missing** -- no corresponding test found.
3. For implemented tests, check that the assertions match the `expected`
   and `assertion_pseudocode` from the spec.

### 6b. Property Test Coverage

For each entry in `property_tests`:
- Does a corresponding Hypothesis test (or equivalent) exist?
- Does it test the stated property?

### 6c. Edge Case Tests

For each entry in `edge_case_tests`:
- Is there a test that specifically exercises this edge case?
- Does the test verify the expected error behavior?

### 6d. Smoke Tests

For each entry in `smoke_tests`:
- Is there an end-to-end test that exercises the full path?

### 6e. Orphan Tests

Identify tests that exist in the test files but are not traced to any test
case in the spec. These are not necessarily bad (regression tests, developer-
added coverage) but should be noted.

---

## Step 7: Generate Audit Report

Present findings in a structured report.

### 7a. Executive Summary

2-4 sentences summarizing:
- Number of specs audited
- Overall conformance level
- Top concerns (if any)
- Overall assessment (healthy / needs attention / critical gaps)

### 7b. Per-Spec Scorecard

For each spec, a compact summary:

```
## Spec 01: Afaudit Package

| Dimension         | Score | Notes                              |
|-------------------|-------|------------------------------------|
| Conformance       | 12/14 | 2 criteria partially satisfied     |
| Test adequacy     | 45/49 | 4 test cases missing               |
| Code quality      | Good  | Minor: unused import in events.py  |

Key findings:
- 01-REQ-3.2: Edge case for empty audit directory not handled
- TS-01-15: Property test specified but not implemented
```

### 7c. Findings Table

All findings across specs, sorted by severity:

```
| # | Severity | Category    | Spec Ref   | File              | Finding                    |
|---|----------|-------------|------------|-------------------|----------------------------|
| 1 | High     | Conformance | 01-REQ-3.2 | afaudit/events.py | Edge case not handled      |
| 2 | Medium   | Test gap    | TS-01-15   | tests/            | Property test missing      |
| 3 | Low      | Quality     | --         | afaudit/sink.py   | Unused variable `_legacy`  |
```

Severity levels:
- **Critical** -- requirement not implemented at all; test suite has false
  positives.
- **High** -- acceptance criterion not fully satisfied; specified test missing.
- **Medium** -- edge case not handled; test partially covers spec contract.
- **Low** -- code quality issue; orphan test; minor deviation from convention.

### 7d. Recommended Actions

Prioritized list of what to fix, grouped by urgency:

1. **Immediate** -- Critical/High conformance gaps.
2. **Short-term** -- Missing tests, edge case handling.
3. **Backlog** -- Quality improvements, cleanup.

For each action, state: what to do, which file to change, and which spec
reference it resolves.

---

## Guardrails

- **Evidence-based only.** Every finding must cite a specific requirement ID
  (e.g., `01-REQ-3.2`) or test case ID (e.g., `TS-01-15`) and a specific
  file/line. Do not report vague concerns.
- **Read before judging.** Read the full implementation before classifying a
  requirement as unsatisfied. The implementation may satisfy the intent through
  a different approach than the spec suggests.
- **Spec is source of truth for requirements; code is source of truth for
  behavior.** When they disagree, report the divergence -- do not silently
  assume either is correct.
- **Do not fix anything.** This is an audit, not a refactoring session. Report
  findings only. The user decides what to act on.
- **Ignore archived specs.** Only specs in `.agent-fox/specs/` (not under
  `archive/`) are in scope.
- **Be proportional.** A single missing edge case test is not the same severity
  as a core requirement that is unimplemented. Calibrate severity honestly.
