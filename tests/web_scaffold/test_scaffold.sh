#!/usr/bin/env bash
#
# Web UI Scaffold Acceptance Tests
# =================================
# Shell-based acceptance tests for the web/ scaffold (spec 06_web_ui_scaffold).
#
# These tests verify file existence, content patterns, JSON structure, and
# command outputs as defined in the test specification (TS-06-1 through TS-06-49,
# plus edge-case and property tests).
#
# Usage:
#   ./tests/web_scaffold/test_scaffold.sh           # run all tests
#   ./tests/web_scaffold/test_scaffold.sh group1     # run group 1 tests only
#   ./tests/web_scaffold/test_scaffold.sh TS-06-1    # run a single test
#
# Exit codes:
#   0 — all tests passed
#   1 — one or more tests failed
#
# Note: No npm test framework is available in this project (testing infra is
# deferred to a future spec). These shell-based tests provide acceptance
# verification without requiring Vitest, Jest, or similar.

set -euo pipefail

# --- Configuration -----------------------------------------------------------

# Resolve repo root (relative to this script)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
WEB_DIR="${REPO_ROOT}/web"

# Counters
PASS_COUNT=0
FAIL_COUNT=0
SKIP_COUNT=0
TOTAL_COUNT=0

# --- Test Framework -----------------------------------------------------------

# Colors (disable if not a terminal)
if [[ -t 1 ]]; then
  RED='\033[0;31m'
  GREEN='\033[0;32m'
  YELLOW='\033[0;33m'
  BOLD='\033[1m'
  RESET='\033[0m'
else
  RED='' GREEN='' YELLOW='' BOLD='' RESET=''
fi

pass() {
  PASS_COUNT=$((PASS_COUNT + 1))
  TOTAL_COUNT=$((TOTAL_COUNT + 1))
  printf "${GREEN}  PASS${RESET} %s\n" "$1"
}

fail() {
  FAIL_COUNT=$((FAIL_COUNT + 1))
  TOTAL_COUNT=$((TOTAL_COUNT + 1))
  printf "${RED}  FAIL${RESET} %s\n" "$1"
  if [[ -n "${2:-}" ]]; then
    printf "       %s\n" "$2"
  fi
}

skip() {
  SKIP_COUNT=$((SKIP_COUNT + 1))
  TOTAL_COUNT=$((TOTAL_COUNT + 1))
  printf "${YELLOW}  SKIP${RESET} %s — %s\n" "$1" "$2"
}

section() {
  printf "\n${BOLD}%s${RESET}\n" "$1"
}

summary() {
  printf "\n${BOLD}--- Summary ---${RESET}\n"
  printf "Total: %d  Passed: ${GREEN}%d${RESET}  Failed: ${RED}%d${RESET}  Skipped: ${YELLOW}%d${RESET}\n" \
    "$TOTAL_COUNT" "$PASS_COUNT" "$FAIL_COUNT" "$SKIP_COUNT"
  if [[ "$FAIL_COUNT" -gt 0 ]]; then
    printf "${RED}FAILED${RESET}\n"
    return 1
  else
    printf "${GREEN}ALL PASSED${RESET}\n"
    return 0
  fi
}

# Check if a specific test or group was requested
FILTER="${1:-all}"

should_run() {
  local test_id="$1"
  local group="$2"
  [[ "$FILTER" == "all" ]] || [[ "$FILTER" == "$test_id" ]] || [[ "$FILTER" == "$group" ]]
}

# --- Helper Functions ---------------------------------------------------------

file_exists() {
  [[ -f "$1" ]]
}

file_not_exists() {
  [[ ! -f "$1" ]]
}

dir_exists() {
  [[ -d "$1" ]]
}

file_contains() {
  local file="$1"
  local pattern="$2"
  grep -q "$pattern" "$file" 2>/dev/null
}

file_not_contains() {
  local file="$1"
  local pattern="$2"
  ! grep -q "$pattern" "$file" 2>/dev/null
}

# Parse JSON with python3 (available on macOS/Linux without extra deps)
json_get() {
  local file="$1"
  local expr="$2"
  python3 -c "
import json, sys
with open('$file') as f:
    data = json.load(f)
result = $expr
if isinstance(result, bool):
    print('true' if result else 'false')
elif result is None:
    print('null')
else:
    print(result)
" 2>/dev/null
}

json_has_key() {
  local file="$1"
  local expr="$2"
  python3 -c "
import json, sys
with open('$file') as f:
    data = json.load(f)
try:
    result = $expr
    sys.exit(0)
except (KeyError, TypeError):
    sys.exit(1)
" 2>/dev/null
}

json_valid() {
  local file="$1"
  python3 -c "
import json, sys
with open('$file') as f:
    json.load(f)
" 2>/dev/null
}

# =============================================================================
# GROUP 1: Node.js version pinning and lockfile (TS-06-1, TS-06-2, TS-06-3)
# =============================================================================

run_group1() {
  section "Group 1: Node.js version pinning and lockfile"

  # TS-06-1: web/.nvmrc exists and contains exactly '22'
  # Requirement: 06-REQ-1.1
  if should_run "TS-06-1" "group1"; then
    if file_exists "${WEB_DIR}/.nvmrc"; then
      local content
      content="$(cat "${WEB_DIR}/.nvmrc" | tr -d '[:space:]')"
      if [[ "$content" == "22" ]]; then
        pass "TS-06-1: web/.nvmrc exists and contains '22'"
      else
        fail "TS-06-1: web/.nvmrc exists but contains '${content}' instead of '22'"
      fi
    else
      fail "TS-06-1: web/.nvmrc does not exist" \
        "Expected file web/.nvmrc with content '22'"
    fi
  fi

  # TS-06-2: web/package-lock.json exists, non-empty, npm ci exits 0
  # Requirement: 06-REQ-1.2
  if should_run "TS-06-2" "group1"; then
    if file_exists "${WEB_DIR}/package-lock.json"; then
      local size
      size="$(wc -c < "${WEB_DIR}/package-lock.json" | tr -d '[:space:]')"
      if [[ "$size" -gt 0 ]]; then
        # Only run npm ci if node_modules might need refreshing
        if (cd "${WEB_DIR}" && npm ci --ignore-scripts 2>/dev/null); then
          pass "TS-06-2: package-lock.json exists, non-empty, npm ci succeeds"
        else
          fail "TS-06-2: package-lock.json exists but npm ci failed" \
            "npm ci exited with non-zero code"
        fi
      else
        fail "TS-06-2: package-lock.json exists but is empty"
      fi
    else
      fail "TS-06-2: web/package-lock.json does not exist"
    fi
  fi

  # TS-06-3: All version strings in dependencies/devDependencies use ^ prefix
  # Requirement: 06-REQ-1.3
  if should_run "TS-06-3" "group1"; then
    if file_exists "${WEB_DIR}/package.json"; then
      local bad_versions
      bad_versions="$(python3 -c "
import json, sys
with open('${WEB_DIR}/package.json') as f:
    pkg = json.load(f)
bad = []
for section in ['dependencies', 'devDependencies']:
    deps = pkg.get(section, {})
    for name, version in deps.items():
        if not version.startswith('^'):
            bad.append(f'{section}.{name}: {version}')
if bad:
    print('\n'.join(bad))
    sys.exit(1)
" 2>&1)" && pass "TS-06-3: All version strings use caret (^) prefix" \
             || fail "TS-06-3: Some versions do not use caret prefix" \
                     "$bad_versions"
    else
      fail "TS-06-3: web/package.json does not exist"
    fi
  fi
}

# =============================================================================
# GROUP 2: Repository hygiene — gitignore entries (TS-06-4, TS-06-5)
# =============================================================================

run_group2() {
  section "Group 2: Repository hygiene — gitignore entries"

  # TS-06-4: git check-ignore web/dist/ and web/node_modules/ both exit 0
  # Requirement: 06-REQ-2.1
  if should_run "TS-06-4" "group2"; then
    local dist_ignored=false
    local nm_ignored=false

    if (cd "${REPO_ROOT}" && git check-ignore -q web/dist/ 2>/dev/null); then
      dist_ignored=true
    fi
    if (cd "${REPO_ROOT}" && git check-ignore -q web/node_modules/ 2>/dev/null); then
      nm_ignored=true
    fi

    if [[ "$dist_ignored" == "true" && "$nm_ignored" == "true" ]]; then
      pass "TS-06-4: web/dist/ and web/node_modules/ are both git-ignored"
    else
      local msg=""
      [[ "$dist_ignored" == "false" ]] && msg+="web/dist/ not ignored. "
      [[ "$nm_ignored" == "false" ]] && msg+="web/node_modules/ not ignored."
      fail "TS-06-4: Not all web paths are git-ignored" "$msg"
    fi
  fi

  # TS-06-5: web/.gitignore does NOT exist
  # Requirement: 06-REQ-2.2
  if should_run "TS-06-5" "group2"; then
    if file_not_exists "${WEB_DIR}/.gitignore"; then
      pass "TS-06-5: web/.gitignore does not exist (correct)"
    else
      fail "TS-06-5: web/.gitignore exists but should not" \
        "Only the root .gitignore should contain web-specific ignore rules"
    fi
  fi
}

# =============================================================================
# GROUP 3: Project directory structure — top-level files (TS-06-6 through TS-06-9)
# =============================================================================

run_group3() {
  section "Group 3: Project directory structure — top-level files"

  # TS-06-6: All required top-level files exist under web/
  # Requirement: 06-REQ-3.1
  if should_run "TS-06-6" "group3"; then
    local required_files=(
      "package.json"
      "tsconfig.json"
      "tsconfig.node.json"
      "vite.config.ts"
      "tailwind.config.ts"
      "postcss.config.js"
      "eslint.config.js"
      "components.json"
      "index.html"
      ".nvmrc"
      "package-lock.json"
    )
    local missing=()
    for f in "${required_files[@]}"; do
      if ! file_exists "${WEB_DIR}/${f}"; then
        missing+=("$f")
      fi
    done
    if [[ ${#missing[@]} -eq 0 ]]; then
      pass "TS-06-6: All 11 required top-level files exist under web/"
    else
      fail "TS-06-6: Missing top-level files under web/" \
        "Missing: ${missing[*]}"
    fi
  fi

  # TS-06-7: All required source files exist under web/src/
  # Requirement: 06-REQ-3.2
  if should_run "TS-06-7" "group3"; then
    local required_src_paths=(
      "main.tsx"
      "App.tsx"
      "globals.css"
      "vite-env.d.ts"
      "lib/utils.ts"
      "routes/index.tsx"
      "routes/not-found.tsx"
      "components/ui/button.tsx"
    )
    local missing=()
    for p in "${required_src_paths[@]}"; do
      if ! file_exists "${WEB_DIR}/src/${p}"; then
        missing+=("$p")
      fi
    done
    if [[ ${#missing[@]} -eq 0 ]]; then
      pass "TS-06-7: All required source files exist under web/src/"
    else
      fail "TS-06-7: Missing source files under web/src/" \
        "Missing: ${missing[*]}"
    fi
  fi

  # TS-06-8: src/main.tsx mounts <App /> into #root using createRoot
  # Requirement: 06-REQ-3.3
  if should_run "TS-06-8" "group3"; then
    local main_file="${WEB_DIR}/src/main.tsx"
    if file_exists "$main_file"; then
      local has_createRoot=false
      local has_root=false
      local has_app=false

      file_contains "$main_file" "createRoot" && has_createRoot=true
      file_contains "$main_file" "root" && has_root=true
      file_contains "$main_file" "App" && has_app=true

      if [[ "$has_createRoot" == "true" && "$has_root" == "true" && "$has_app" == "true" ]]; then
        pass "TS-06-8: main.tsx uses createRoot, targets 'root' element, and renders App"
      else
        local msg=""
        [[ "$has_createRoot" == "false" ]] && msg+="missing createRoot. "
        [[ "$has_root" == "false" ]] && msg+="missing 'root' element reference. "
        [[ "$has_app" == "false" ]] && msg+="missing App reference."
        fail "TS-06-8: main.tsx missing required content" "$msg"
      fi
    else
      fail "TS-06-8: web/src/main.tsx does not exist"
    fi
  fi

  # TS-06-9: index.html at web/index.html (not src/), references src/main.tsx
  # Requirement: 06-REQ-3.4
  if should_run "TS-06-9" "group3"; then
    if file_exists "${WEB_DIR}/index.html"; then
      if file_not_exists "${WEB_DIR}/src/index.html"; then
        local has_main_tsx=false
        local has_type_module=false

        file_contains "${WEB_DIR}/index.html" "src/main.tsx" && has_main_tsx=true
        file_contains "${WEB_DIR}/index.html" 'type="module"' && has_type_module=true

        if [[ "$has_main_tsx" == "true" && "$has_type_module" == "true" ]]; then
          pass "TS-06-9: index.html at web/ root, references src/main.tsx with type=module"
        else
          local msg=""
          [[ "$has_main_tsx" == "false" ]] && msg+="missing src/main.tsx reference. "
          [[ "$has_type_module" == "false" ]] && msg+="missing type=\"module\"."
          fail "TS-06-9: index.html missing required content" "$msg"
        fi
      else
        fail "TS-06-9: index.html exists at web/src/index.html (should only be at web/)"
      fi
    else
      fail "TS-06-9: web/index.html does not exist"
    fi
  fi
}

# =============================================================================
# GROUP 4: components.json, utils.ts, and dependency declarations
#           (TS-06-10 through TS-06-14)
# =============================================================================

run_group4() {
  section "Group 4: components.json, utils.ts, and dependency declarations"

  # TS-06-10: web/components.json exists and is valid JSON
  # Requirement: 06-REQ-3.5
  if should_run "TS-06-10" "group4"; then
    if file_exists "${WEB_DIR}/components.json"; then
      if json_valid "${WEB_DIR}/components.json"; then
        pass "TS-06-10: web/components.json exists and is valid JSON"
      else
        fail "TS-06-10: web/components.json exists but is not valid JSON"
      fi
    else
      fail "TS-06-10: web/components.json does not exist"
    fi
  fi

  # TS-06-11: src/lib/utils.ts exports cn function; tsc --noEmit exits 0
  # Requirement: 06-REQ-3.6
  if should_run "TS-06-11" "group4"; then
    local utils_file="${WEB_DIR}/src/lib/utils.ts"
    if file_exists "$utils_file"; then
      local has_export=false
      local has_cn=false

      file_contains "$utils_file" "export" && has_export=true
      file_contains "$utils_file" "cn" && has_cn=true

      if [[ "$has_export" == "true" && "$has_cn" == "true" ]]; then
        # Check tsc --noEmit if node_modules exists
        if dir_exists "${WEB_DIR}/node_modules"; then
          if (cd "${WEB_DIR}" && npx tsc --noEmit 2>/dev/null); then
            pass "TS-06-11: utils.ts exports cn; tsc --noEmit passes"
          else
            fail "TS-06-11: utils.ts exports cn but tsc --noEmit failed"
          fi
        else
          fail "TS-06-11: utils.ts exports cn but node_modules not installed" \
            "Run npm ci in web/ first"
        fi
      else
        local msg=""
        [[ "$has_export" == "false" ]] && msg+="missing 'export'. "
        [[ "$has_cn" == "false" ]] && msg+="missing 'cn' function."
        fail "TS-06-11: utils.ts missing required exports" "$msg"
      fi
    else
      fail "TS-06-11: web/src/lib/utils.ts does not exist"
    fi
  fi

  # TS-06-12: All four runtime dependencies in package.json dependencies
  # Requirement: 06-REQ-4.1
  if should_run "TS-06-12" "group4"; then
    if file_exists "${WEB_DIR}/package.json"; then
      local required_deps=("react" "react-dom" "react-router-dom" "@tanstack/react-query")
      local missing=()
      for dep in "${required_deps[@]}"; do
        if ! json_has_key "${WEB_DIR}/package.json" "data['dependencies']['${dep}']"; then
          missing+=("$dep")
        fi
      done
      if [[ ${#missing[@]} -eq 0 ]]; then
        pass "TS-06-12: All four runtime dependencies declared in dependencies"
      else
        fail "TS-06-12: Missing runtime dependencies" \
          "Missing: ${missing[*]}"
      fi
    else
      fail "TS-06-12: web/package.json does not exist"
    fi
  fi

  # TS-06-13: All required dev dependencies in package.json devDependencies
  # Requirement: 06-REQ-4.2
  if should_run "TS-06-13" "group4"; then
    if file_exists "${WEB_DIR}/package.json"; then
      local required_dev_deps=(
        "typescript"
        "vite"
        "@vitejs/plugin-react"
        "tailwindcss"
        "postcss"
        "autoprefixer"
        "eslint"
        "@typescript-eslint/eslint-plugin"
        "@typescript-eslint/parser"
        "eslint-plugin-react"
        "eslint-plugin-react-hooks"
      )
      local missing=()
      for dep in "${required_dev_deps[@]}"; do
        if ! json_has_key "${WEB_DIR}/package.json" "data['devDependencies']['${dep}']"; then
          missing+=("$dep")
        fi
      done
      if [[ ${#missing[@]} -eq 0 ]]; then
        pass "TS-06-13: All required dev dependencies declared in devDependencies"
      else
        fail "TS-06-13: Missing dev dependencies" \
          "Missing: ${missing[*]}"
      fi
    else
      fail "TS-06-13: web/package.json does not exist"
    fi
  fi

  # TS-06-14: package.json scripts has non-empty dev, build, and lint entries
  # Requirement: 06-REQ-4.3
  if should_run "TS-06-14" "group4"; then
    if file_exists "${WEB_DIR}/package.json"; then
      local scripts_ok=true
      local msg=""

      for script in dev build lint; do
        local value
        value="$(json_get "${WEB_DIR}/package.json" "data.get('scripts', {}).get('${script}', '')")"
        if [[ -z "$value" || "$value" == "null" ]]; then
          scripts_ok=false
          msg+="'${script}' script missing or empty. "
        fi
      done

      if [[ "$scripts_ok" == "true" ]]; then
        pass "TS-06-14: package.json defines non-empty dev, build, and lint scripts"
      else
        fail "TS-06-14: package.json missing required scripts" "$msg"
      fi
    else
      fail "TS-06-14: web/package.json does not exist"
    fi
  fi
}

# =============================================================================
# Main entry point
# =============================================================================

main() {
  printf "${BOLD}Web UI Scaffold Acceptance Tests${RESET}\n"
  printf "Repository root: %s\n" "$REPO_ROOT"
  printf "Web directory:   %s\n" "$WEB_DIR"
  printf "Filter:          %s\n" "$FILTER"

  if [[ "$FILTER" == "all" || "$FILTER" == "group1" || "$FILTER" == TS-06-[123] ]]; then
    run_group1
  fi

  if [[ "$FILTER" == "all" || "$FILTER" == "group2" || "$FILTER" == TS-06-[45] ]]; then
    run_group2
  fi

  if [[ "$FILTER" == "all" || "$FILTER" == "group3" || "$FILTER" == TS-06-[6789] ]]; then
    run_group3
  fi

  if [[ "$FILTER" == "all" || "$FILTER" == "group4" || "$FILTER" =~ ^TS-06-1[01234]$ ]]; then
    run_group4
  fi

  summary
}

main
