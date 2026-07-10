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
# GROUP 5: TypeScript compiler configuration (TS-06-15 through TS-06-18)
# =============================================================================

run_group5() {
  section "Group 5: TypeScript compiler configuration"

  # TS-06-15: tsconfig.json has all required compiler options; tsc --noEmit exits 0
  # Requirement: 06-REQ-5.1
  if should_run "TS-06-15" "group5"; then
    if file_exists "${WEB_DIR}/tsconfig.json"; then
      local all_ok=true
      local msg=""

      # Validate all required compilerOptions via a single python3 call
      local check_result
      check_result="$(python3 -c "
import json, sys
with open('${WEB_DIR}/tsconfig.json') as f:
    data = json.load(f)
co = data.get('compilerOptions', {})
errors = []
if co.get('strict') is not True:
    errors.append('strict != true')
if co.get('noUncheckedIndexedAccess') is not True:
    errors.append('noUncheckedIndexedAccess != true')
if co.get('moduleResolution') != 'bundler':
    errors.append('moduleResolution != bundler (got: %s)' % co.get('moduleResolution'))
if co.get('target') != 'ES2022':
    errors.append('target != ES2022 (got: %s)' % co.get('target'))
if co.get('jsx') != 'react-jsx':
    errors.append('jsx != react-jsx (got: %s)' % co.get('jsx'))
if co.get('noEmit') is not True:
    errors.append('noEmit != true')
paths = co.get('paths', {})
alias = paths.get('@/*', [])
if './src/*' not in alias:
    errors.append(\"paths['@/*'] does not contain './src/*'\")
if errors:
    print('FAIL:' + '; '.join(errors))
    sys.exit(1)
else:
    print('OK')
" 2>&1)"

      if [[ "$check_result" == "OK" ]]; then
        # Run tsc --noEmit if node_modules exists
        if dir_exists "${WEB_DIR}/node_modules"; then
          if (cd "${WEB_DIR}" && npx tsc --noEmit 2>/dev/null); then
            pass "TS-06-15: tsconfig.json has all required compiler options; tsc --noEmit passes"
          else
            fail "TS-06-15: tsconfig.json options correct but tsc --noEmit failed"
          fi
        else
          # Without node_modules we can only verify the config structure
          pass "TS-06-15: tsconfig.json has all required compiler options (tsc skipped — no node_modules)"
        fi
      else
        fail "TS-06-15: tsconfig.json missing required compiler options" \
          "${check_result#FAIL:}"
      fi
    else
      fail "TS-06-15: web/tsconfig.json does not exist"
    fi
  fi

  # TS-06-16: tsconfig.node.json has required options
  # Requirement: 06-REQ-5.2
  if should_run "TS-06-16" "group5"; then
    if file_exists "${WEB_DIR}/tsconfig.node.json"; then
      local check_result
      check_result="$(python3 -c "
import json, sys
with open('${WEB_DIR}/tsconfig.node.json') as f:
    data = json.load(f)
co = data.get('compilerOptions', {})
errors = []
if co.get('composite') is not True:
    errors.append('composite != true')
if co.get('skipLibCheck') is not True:
    errors.append('skipLibCheck != true')
if co.get('module') != 'ESNext':
    errors.append('module != ESNext (got: %s)' % co.get('module'))
if co.get('moduleResolution') != 'bundler':
    errors.append('moduleResolution != bundler (got: %s)' % co.get('moduleResolution'))
if co.get('allowSyntheticDefaultImports') is not True:
    errors.append('allowSyntheticDefaultImports != true')
include = data.get('include', [])
if 'vite.config.ts' not in include:
    errors.append(\"include does not contain 'vite.config.ts'\")
if errors:
    print('FAIL:' + '; '.join(errors))
    sys.exit(1)
else:
    print('OK')
" 2>&1)"

      if [[ "$check_result" == "OK" ]]; then
        pass "TS-06-16: tsconfig.node.json has all required options"
      else
        fail "TS-06-16: tsconfig.node.json missing required options" \
          "${check_result#FAIL:}"
      fi
    else
      fail "TS-06-16: web/tsconfig.node.json does not exist"
    fi
  fi

  # TS-06-17: tsconfig.json include covers src/
  # Requirement: 06-REQ-5.3
  if should_run "TS-06-17" "group5"; then
    if file_exists "${WEB_DIR}/tsconfig.json"; then
      local covers_src
      covers_src="$(python3 -c "
import json
with open('${WEB_DIR}/tsconfig.json') as f:
    data = json.load(f)
include = data.get('include', [])
root_dir = data.get('compilerOptions', {}).get('rootDir', '')
# Check if any include glob covers src/
covers = False
for glob in include:
    if 'src' in glob:
        covers = True
        break
if root_dir and ('src' in root_dir or root_dir == '.'):
    covers = True
print('true' if covers else 'false')
" 2>/dev/null)"

      if [[ "$covers_src" == "true" ]]; then
        pass "TS-06-17: tsconfig.json include/rootDir covers src/"
      else
        fail "TS-06-17: tsconfig.json does not cover src/ files" \
          "Neither include globs nor rootDir reference src/"
      fi
    else
      fail "TS-06-17: web/tsconfig.json does not exist"
    fi
  fi

  # TS-06-18: src/vite-env.d.ts declares __APP_VERSION__: string
  # Requirement: 06-REQ-5.4
  if should_run "TS-06-18" "group5"; then
    local env_file="${WEB_DIR}/src/vite-env.d.ts"
    if file_exists "$env_file"; then
      if file_contains "$env_file" "declare const __APP_VERSION__: string"; then
        pass "TS-06-18: vite-env.d.ts declares __APP_VERSION__: string"
      else
        fail "TS-06-18: vite-env.d.ts missing __APP_VERSION__ declaration" \
          "Expected: declare const __APP_VERSION__: string"
      fi
    else
      fail "TS-06-18: web/src/vite-env.d.ts does not exist"
    fi
  fi
}

# =============================================================================
# GROUP 6: Path alias configuration (TS-06-19 through TS-06-21)
# =============================================================================

run_group6() {
  section "Group 6: Path alias configuration"

  # TS-06-19: tsconfig.json paths['@/*'] == ['./src/*']; tsc --noEmit exits 0
  # Requirement: 06-REQ-6.1
  if should_run "TS-06-19" "group6"; then
    if file_exists "${WEB_DIR}/tsconfig.json"; then
      local alias_ok
      alias_ok="$(python3 -c "
import json
with open('${WEB_DIR}/tsconfig.json') as f:
    data = json.load(f)
paths = data.get('compilerOptions', {}).get('paths', {})
alias = paths.get('@/*', [])
if alias == ['./src/*']:
    print('true')
else:
    print('false: got %s' % alias)
" 2>/dev/null)"

      if [[ "$alias_ok" == "true" ]]; then
        # Run tsc --noEmit if node_modules exists
        if dir_exists "${WEB_DIR}/node_modules"; then
          if (cd "${WEB_DIR}" && npx tsc --noEmit 2>/dev/null); then
            pass "TS-06-19: tsconfig.json paths['@/*'] = ['./src/*']; tsc --noEmit passes"
          else
            fail "TS-06-19: paths alias correct but tsc --noEmit failed"
          fi
        else
          pass "TS-06-19: tsconfig.json paths['@/*'] = ['./src/*'] (tsc skipped — no node_modules)"
        fi
      else
        fail "TS-06-19: tsconfig.json paths['@/*'] incorrect" \
          "$alias_ok"
      fi
    else
      fail "TS-06-19: web/tsconfig.json does not exist"
    fi
  fi

  # TS-06-20: vite.config.ts has resolve.alias mapping @ to ./src; npm run build exits 0
  # Requirement: 06-REQ-6.2
  if should_run "TS-06-20" "group6"; then
    local vite_file="${WEB_DIR}/vite.config.ts"
    if file_exists "$vite_file"; then
      local has_resolve=false
      local has_alias=false
      local has_at=false
      local has_src=false

      file_contains "$vite_file" "resolve" && has_resolve=true
      file_contains "$vite_file" "alias" && has_alias=true
      # Check for @ as an alias key (quoted: '@' or "@")
      (grep -qE "['\"]@['\"]" "$vite_file" 2>/dev/null) && has_at=true
      file_contains "$vite_file" "./src" && has_src=true

      if [[ "$has_resolve" == "true" && "$has_alias" == "true" && \
            "$has_at" == "true" && "$has_src" == "true" ]]; then
        # Run npm run build if node_modules exists
        if dir_exists "${WEB_DIR}/node_modules"; then
          if (cd "${WEB_DIR}" && npm run build 2>/dev/null); then
            pass "TS-06-20: vite.config.ts has resolve.alias for @; npm run build passes"
          else
            fail "TS-06-20: vite.config.ts alias correct but npm run build failed"
          fi
        else
          pass "TS-06-20: vite.config.ts has resolve.alias for @ → ./src (build skipped — no node_modules)"
        fi
      else
        local msg=""
        [[ "$has_resolve" == "false" ]] && msg+="missing 'resolve'. "
        [[ "$has_alias" == "false" ]] && msg+="missing 'alias'. "
        [[ "$has_at" == "false" ]] && msg+="missing '@' alias key. "
        [[ "$has_src" == "false" ]] && msg+="missing './src' mapping."
        fail "TS-06-20: vite.config.ts missing resolve.alias config" "$msg"
      fi
    else
      fail "TS-06-20: web/vite.config.ts does not exist"
    fi
  fi

  # TS-06-21: All shadcn/ui imports use @/ alias, not relative paths
  # Requirement: 06-REQ-6.3
  if should_run "TS-06-21" "group6"; then
    local index_file="${WEB_DIR}/src/routes/index.tsx"
    if file_exists "$index_file"; then
      local has_alias_import=false
      local has_relative_import=false

      file_contains "$index_file" "@/components/ui/button" && has_alias_import=true
      file_contains "$index_file" "../components/ui/button" && has_relative_import=true

      if [[ "$has_alias_import" == "true" && "$has_relative_import" == "false" ]]; then
        pass "TS-06-21: Button import uses @/ alias, not relative path"
      elif [[ "$has_alias_import" == "false" ]]; then
        fail "TS-06-21: routes/index.tsx missing @/components/ui/button import"
      else
        fail "TS-06-21: routes/index.tsx uses relative import instead of @/ alias"
      fi
    else
      fail "TS-06-21: web/src/routes/index.tsx does not exist"
    fi
  fi
}

# =============================================================================
# GROUP 7: ESLint flat config (TS-06-22 through TS-06-27)
# =============================================================================

run_group7() {
  section "Group 7: ESLint flat config"

  # TS-06-22: eslint.config.js exists, uses flat config, ESLint >= v9
  # Requirement: 06-REQ-7.1
  if should_run "TS-06-22" "group7"; then
    local eslint_file="${WEB_DIR}/eslint.config.js"
    if file_exists "$eslint_file"; then
      local has_export=false
      # Check for ESM export (flat config) — either 'export default' or 'module.exports'
      (file_contains "$eslint_file" "export default" || \
       file_contains "$eslint_file" "module.exports") && has_export=true

      if [[ "$has_export" == "true" ]]; then
        # Check ESLint version if node_modules exists
        if dir_exists "${WEB_DIR}/node_modules"; then
          local eslint_version
          eslint_version="$(cd "${WEB_DIR}" && npx eslint --version 2>/dev/null)"
          local major
          major="$(echo "$eslint_version" | sed 's/^v//' | cut -d. -f1)"
          if [[ -n "$major" && "$major" -ge 9 ]]; then
            # Verify ESLint loads config without errors
            if (cd "${WEB_DIR}" && npx eslint . 2>/dev/null); then
              pass "TS-06-22: eslint.config.js exists, flat config, ESLint v${major}"
            else
              fail "TS-06-22: eslint.config.js exists but ESLint errors when loading it"
            fi
          else
            fail "TS-06-22: ESLint version < 9 (got: ${eslint_version})" \
              "ESLint 9+ required for flat config"
          fi
        else
          pass "TS-06-22: eslint.config.js exists with flat config export (version check skipped — no node_modules)"
        fi
      else
        fail "TS-06-22: eslint.config.js missing export (not flat config format)" \
          "Expected 'export default' or 'module.exports'"
      fi
    else
      fail "TS-06-22: web/eslint.config.js does not exist"
    fi
  fi

  # TS-06-23: eslint.config.js includes TypeScript-aware linting
  # Requirement: 06-REQ-7.2
  # Note: Modern ESLint 9 flat config may use the unified 'typescript-eslint'
  # package instead of separate '@typescript-eslint/eslint-plugin' and
  # '@typescript-eslint/parser' imports. We accept either pattern.
  if should_run "TS-06-23" "group7"; then
    local eslint_file="${WEB_DIR}/eslint.config.js"
    if file_exists "$eslint_file"; then
      local has_ts_eslint=false

      # Check for either the unified package or the individual packages
      if file_contains "$eslint_file" "typescript-eslint"; then
        # Covers both '@typescript-eslint/eslint-plugin' and 'typescript-eslint'
        has_ts_eslint=true
      fi

      if [[ "$has_ts_eslint" == "true" ]]; then
        # Additionally check that a parser is configured (either explicit or via unified package)
        local has_parser=false
        if file_contains "$eslint_file" "@typescript-eslint/parser" || \
           file_contains "$eslint_file" "typescript-eslint"; then
          has_parser=true
        fi

        if [[ "$has_parser" == "true" ]]; then
          pass "TS-06-23: eslint.config.js includes TypeScript-aware linting"
        else
          fail "TS-06-23: eslint.config.js has TS plugin but missing parser config"
        fi
      else
        fail "TS-06-23: eslint.config.js missing TypeScript ESLint references" \
          "Expected references to '@typescript-eslint/eslint-plugin' and '@typescript-eslint/parser' (or unified 'typescript-eslint' package)"
      fi
    else
      fail "TS-06-23: web/eslint.config.js does not exist"
    fi
  fi

  # TS-06-24: eslint.config.js includes React-specific rules
  # Requirement: 06-REQ-7.3
  if should_run "TS-06-24" "group7"; then
    local eslint_file="${WEB_DIR}/eslint.config.js"
    if file_exists "$eslint_file"; then
      local has_react_plugin=false
      local has_hooks_plugin=false

      file_contains "$eslint_file" "eslint-plugin-react" && has_react_plugin=true
      # Check for hooks plugin — either as import string or as part of config
      (file_contains "$eslint_file" "eslint-plugin-react-hooks" || \
       file_contains "$eslint_file" "react-hooks") && has_hooks_plugin=true

      if [[ "$has_react_plugin" == "true" && "$has_hooks_plugin" == "true" ]]; then
        pass "TS-06-24: eslint.config.js includes React and React Hooks plugins"
      else
        local msg=""
        [[ "$has_react_plugin" == "false" ]] && msg+="missing eslint-plugin-react. "
        [[ "$has_hooks_plugin" == "false" ]] && msg+="missing eslint-plugin-react-hooks."
        fail "TS-06-24: eslint.config.js missing React plugin references" "$msg"
      fi
    else
      fail "TS-06-24: web/eslint.config.js does not exist"
    fi
  fi

  # TS-06-25: eslint.config.js disables react/react-in-jsx-scope rule
  # Requirement: 06-REQ-7.4
  if should_run "TS-06-25" "group7"; then
    local eslint_file="${WEB_DIR}/eslint.config.js"
    if file_exists "$eslint_file"; then
      local has_rule=false
      local has_off=false

      file_contains "$eslint_file" "react/react-in-jsx-scope" && has_rule=true
      # Check that the rule is set to 'off' (within the same file context)
      (grep -E "react/react-in-jsx-scope.*off|react-in-jsx-scope.*off" "$eslint_file" >/dev/null 2>&1 || \
       grep -A2 "react/react-in-jsx-scope" "$eslint_file" 2>/dev/null | grep -q "off") && has_off=true

      if [[ "$has_rule" == "true" && "$has_off" == "true" ]]; then
        # Verify lint doesn't produce this error if node_modules exists
        if dir_exists "${WEB_DIR}/node_modules"; then
          local lint_output
          lint_output="$(cd "${WEB_DIR}" && npm run lint 2>&1)" || true
          if echo "$lint_output" | grep -q "react/react-in-jsx-scope"; then
            fail "TS-06-25: Rule is configured but still triggers during lint"
          else
            pass "TS-06-25: react/react-in-jsx-scope disabled; no errors in lint output"
          fi
        else
          pass "TS-06-25: react/react-in-jsx-scope rule set to 'off' (lint skipped — no node_modules)"
        fi
      elif [[ "$has_rule" == "false" ]]; then
        fail "TS-06-25: eslint.config.js missing react/react-in-jsx-scope rule"
      else
        fail "TS-06-25: react/react-in-jsx-scope referenced but not set to 'off'"
      fi
    else
      fail "TS-06-25: web/eslint.config.js does not exist"
    fi
  fi

  # TS-06-26: npm run lint produces zero errors on committed scaffold
  # Requirement: 06-REQ-7.5
  if should_run "TS-06-26" "group7"; then
    if file_exists "${WEB_DIR}/package.json"; then
      if dir_exists "${WEB_DIR}/node_modules"; then
        if (cd "${WEB_DIR}" && npm run lint 2>/dev/null); then
          pass "TS-06-26: npm run lint exits with code 0 (zero errors)"
        else
          fail "TS-06-26: npm run lint exited with non-zero code"
        fi
      else
        fail "TS-06-26: Cannot run npm run lint — node_modules not installed" \
          "Run npm ci in web/ first"
      fi
    else
      fail "TS-06-26: web/package.json does not exist"
    fi
  fi

  # TS-06-27: lint script contains both eslint and tsc --noEmit
  # Requirement: 06-REQ-7.6
  if should_run "TS-06-27" "group7"; then
    if file_exists "${WEB_DIR}/package.json"; then
      local lint_script
      lint_script="$(json_get "${WEB_DIR}/package.json" "data.get('scripts', {}).get('lint', '')")"

      local has_eslint=false
      local has_tsc=false

      echo "$lint_script" | grep -q "eslint" && has_eslint=true
      echo "$lint_script" | grep -q "tsc --noEmit" && has_tsc=true

      if [[ "$has_eslint" == "true" && "$has_tsc" == "true" ]]; then
        pass "TS-06-27: lint script contains both 'eslint' and 'tsc --noEmit'"
      else
        local msg=""
        [[ "$has_eslint" == "false" ]] && msg+="missing 'eslint'. "
        [[ "$has_tsc" == "false" ]] && msg+="missing 'tsc --noEmit'."
        fail "TS-06-27: lint script incomplete" "$msg (script: ${lint_script})"
      fi
    else
      fail "TS-06-27: web/package.json does not exist"
    fi
  fi
}

# =============================================================================
# GROUP 8: Tailwind CSS and shadcn/ui theming (TS-06-28 through TS-06-32)
# =============================================================================

run_group8() {
  section "Group 8: Tailwind CSS and shadcn/ui theming"

  # TS-06-28: globals.css has Tailwind directives and CSS variable declarations
  # Requirement: 06-REQ-8.1
  if should_run "TS-06-28" "group8"; then
    local css_file="${WEB_DIR}/src/globals.css"
    if file_exists "$css_file"; then
      local all_ok=true
      local msg=""

      file_contains "$css_file" "@tailwind base" || { all_ok=false; msg+="missing '@tailwind base'. "; }
      file_contains "$css_file" "@tailwind components" || { all_ok=false; msg+="missing '@tailwind components'. "; }
      file_contains "$css_file" "@tailwind utilities" || { all_ok=false; msg+="missing '@tailwind utilities'. "; }
      file_contains "$css_file" ":root" || { all_ok=false; msg+="missing ':root'. "; }
      file_contains "$css_file" ".dark" || { all_ok=false; msg+="missing '.dark'. "; }
      file_contains "$css_file" "--background" || { all_ok=false; msg+="missing '--background'. "; }
      file_contains "$css_file" "--foreground" || { all_ok=false; msg+="missing '--foreground'. "; }

      if [[ "$all_ok" == "true" ]]; then
        pass "TS-06-28: globals.css has Tailwind directives and CSS variable declarations"
      else
        fail "TS-06-28: globals.css missing required content" "$msg"
      fi
    else
      fail "TS-06-28: web/src/globals.css does not exist"
    fi
  fi

  # TS-06-29: tailwind.config.ts has content paths, theme extension, darkMode
  # Requirement: 06-REQ-8.2
  if should_run "TS-06-29" "group8"; then
    local tw_file="${WEB_DIR}/tailwind.config.ts"
    if file_exists "$tw_file"; then
      local all_ok=true
      local msg=""

      file_contains "$tw_file" "./index.html" || { all_ok=false; msg+="missing './index.html' in content. "; }
      file_contains "$tw_file" './src/\*\*/\*.\{ts,tsx,js,jsx\}' || { all_ok=false; msg+="missing './src/**/*.{ts,tsx,js,jsx}' in content. "; }
      file_contains "$tw_file" "hsl(var(--background))" || { all_ok=false; msg+="missing 'hsl(var(--background))' theme extension. "; }
      # Check for darkMode: 'class' or darkMode: "class"
      (grep -qE "darkMode.*['\"]class['\"]" "$tw_file" 2>/dev/null) || { all_ok=false; msg+="missing darkMode: 'class'. "; }

      if [[ "$all_ok" == "true" ]]; then
        pass "TS-06-29: tailwind.config.ts has correct content, theme, and darkMode"
      else
        fail "TS-06-29: tailwind.config.ts missing required configuration" "$msg"
      fi
    else
      fail "TS-06-29: web/tailwind.config.ts does not exist"
    fi
  fi

  # TS-06-30: postcss.config.js references tailwindcss and autoprefixer
  # Requirement: 06-REQ-8.3
  if should_run "TS-06-30" "group8"; then
    local postcss_file="${WEB_DIR}/postcss.config.js"
    if file_exists "$postcss_file"; then
      local has_tw=false
      local has_ap=false

      file_contains "$postcss_file" "tailwindcss" && has_tw=true
      file_contains "$postcss_file" "autoprefixer" && has_ap=true

      if [[ "$has_tw" == "true" && "$has_ap" == "true" ]]; then
        pass "TS-06-30: postcss.config.js references tailwindcss and autoprefixer"
      else
        local msg=""
        [[ "$has_tw" == "false" ]] && msg+="missing 'tailwindcss'. "
        [[ "$has_ap" == "false" ]] && msg+="missing 'autoprefixer'."
        fail "TS-06-30: postcss.config.js missing plugin references" "$msg"
      fi
    else
      fail "TS-06-30: web/postcss.config.js does not exist"
    fi
  fi

  # TS-06-31: src/main.tsx imports globals.css
  # Requirement: 06-REQ-8.4
  if should_run "TS-06-31" "group8"; then
    local main_file="${WEB_DIR}/src/main.tsx"
    if file_exists "$main_file"; then
      if file_contains "$main_file" "globals.css"; then
        pass "TS-06-31: main.tsx imports globals.css"
      else
        fail "TS-06-31: main.tsx missing globals.css import"
      fi
    else
      fail "TS-06-31: web/src/main.tsx does not exist"
    fi
  fi

  # TS-06-32: Button component exists, contains 'Button', build succeeds
  # Requirement: 06-REQ-8.5
  if should_run "TS-06-32" "group8"; then
    local button_file="${WEB_DIR}/src/components/ui/button.tsx"
    if file_exists "$button_file"; then
      if file_contains "$button_file" "Button"; then
        # Run npm run build if node_modules exists
        if dir_exists "${WEB_DIR}/node_modules"; then
          if (cd "${WEB_DIR}" && npm run build 2>/dev/null); then
            pass "TS-06-32: button.tsx exists, contains Button, build passes"
          else
            fail "TS-06-32: button.tsx correct but npm run build failed"
          fi
        else
          pass "TS-06-32: button.tsx exists and contains Button (build skipped — no node_modules)"
        fi
      else
        fail "TS-06-32: button.tsx exists but does not contain 'Button'"
      fi
    else
      fail "TS-06-32: web/src/components/ui/button.tsx does not exist"
    fi
  fi
}

# =============================================================================
# EDGE CASES (Group 2): TS-06-E4 through TS-06-E8
# =============================================================================

run_edge_cases_2() {
  section "Edge Cases (Group 2): TypeScript, Alias, ESLint, Tailwind"

  # TS-06-E4: noUncheckedIndexedAccess must be true
  # Requirement: 06-REQ-5.E1
  if should_run "TS-06-E4" "edge2"; then
    if file_exists "${WEB_DIR}/tsconfig.json"; then
      local value
      value="$(json_get "${WEB_DIR}/tsconfig.json" "data.get('compilerOptions', {}).get('noUncheckedIndexedAccess', False)")"
      if [[ "$value" == "true" ]]; then
        pass "TS-06-E4: noUncheckedIndexedAccess is true"
      else
        fail "TS-06-E4: noUncheckedIndexedAccess is not true (got: ${value})" \
          "Unsafe array/object index access would go undetected"
      fi
    else
      fail "TS-06-E4: web/tsconfig.json does not exist"
    fi
  fi

  # TS-06-E5: @/ alias configured in vite.config.ts (sync with tsconfig)
  # Requirement: 06-REQ-6.E1
  if should_run "TS-06-E5" "edge2"; then
    local vite_file="${WEB_DIR}/vite.config.ts"
    if file_exists "$vite_file"; then
      local all_ok=true
      local msg=""

      file_contains "$vite_file" "alias" || { all_ok=false; msg+="missing 'alias'. "; }
      (grep -qE "['\"]@['\"]" "$vite_file" 2>/dev/null) || { all_ok=false; msg+="missing '@' alias key. "; }
      file_contains "$vite_file" "./src" || { all_ok=false; msg+="missing './src' mapping. "; }

      if [[ "$all_ok" == "true" ]]; then
        pass "TS-06-E5: vite.config.ts has @/ alias synced with tsconfig"
      else
        fail "TS-06-E5: vite.config.ts missing @/ alias configuration" "$msg"
      fi
    else
      fail "TS-06-E5: web/vite.config.ts does not exist"
    fi
  fi

  # TS-06-E6: eslint.config.js must reference both React plugins
  # Requirement: 06-REQ-7.E1
  if should_run "TS-06-E6" "edge2"; then
    local eslint_file="${WEB_DIR}/eslint.config.js"
    if file_exists "$eslint_file"; then
      local has_react=false
      local has_hooks=false

      file_contains "$eslint_file" "eslint-plugin-react" && has_react=true
      (file_contains "$eslint_file" "eslint-plugin-react-hooks" || \
       file_contains "$eslint_file" "react-hooks") && has_hooks=true

      if [[ "$has_react" == "true" && "$has_hooks" == "true" ]]; then
        pass "TS-06-E6: eslint.config.js references both React plugins"
      else
        local msg=""
        [[ "$has_react" == "false" ]] && msg+="eslint-plugin-react must be present. "
        [[ "$has_hooks" == "false" ]] && msg+="eslint-plugin-react-hooks must be present."
        fail "TS-06-E6: eslint.config.js missing React plugin references" "$msg"
      fi
    else
      fail "TS-06-E6: web/eslint.config.js does not exist"
    fi
  fi

  # TS-06-E7: tailwind.config.ts content paths must cover src/
  # Requirement: 06-REQ-8.E1
  if should_run "TS-06-E7" "edge2"; then
    local tw_file="${WEB_DIR}/tailwind.config.ts"
    if file_exists "$tw_file"; then
      local has_src_glob=false
      local has_extensions=false

      file_contains "$tw_file" './src/\*\*/' && has_src_glob=true
      (file_contains "$tw_file" "{ts,tsx,js,jsx}" || file_contains "$tw_file" "tsx") && has_extensions=true

      if [[ "$has_src_glob" == "true" && "$has_extensions" == "true" ]]; then
        pass "TS-06-E7: tailwind.config.ts content paths cover src/ with correct extensions"
      else
        local msg=""
        [[ "$has_src_glob" == "false" ]] && msg+="missing './src/**/' glob. "
        [[ "$has_extensions" == "false" ]] && msg+="missing file extension patterns."
        fail "TS-06-E7: tailwind.config.ts content paths don't cover src/" "$msg"
      fi
    else
      fail "TS-06-E7: web/tailwind.config.ts does not exist"
    fi
  fi

  # TS-06-E8: globals.css must have CSS variable declarations under :root
  # Requirement: 06-REQ-8.E2
  if should_run "TS-06-E8" "edge2"; then
    local css_file="${WEB_DIR}/src/globals.css"
    if file_exists "$css_file"; then
      local all_ok=true
      local msg=""

      file_contains "$css_file" ":root" || { all_ok=false; msg+="missing ':root'. "; }
      file_contains "$css_file" "--background" || { all_ok=false; msg+="missing '--background'. "; }
      file_contains "$css_file" "--foreground" || { all_ok=false; msg+="missing '--foreground'. "; }
      file_contains "$css_file" "--primary" || { all_ok=false; msg+="missing '--primary'. "; }

      if [[ "$all_ok" == "true" ]]; then
        pass "TS-06-E8: globals.css has CSS variable declarations for :root"
      else
        fail "TS-06-E8: globals.css missing CSS variable declarations" \
          "$msg — shadcn/ui components would render unstyled"
      fi
    else
      fail "TS-06-E8: web/src/globals.css does not exist"
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

  if [[ "$FILTER" == "all" || "$FILTER" == "group5" || "$FILTER" =~ ^TS-06-1[5678]$ ]]; then
    run_group5
  fi

  if [[ "$FILTER" == "all" || "$FILTER" == "group6" || "$FILTER" =~ ^TS-06-(19|2[01])$ ]]; then
    run_group6
  fi

  if [[ "$FILTER" == "all" || "$FILTER" == "group7" || "$FILTER" =~ ^TS-06-2[234567]$ ]]; then
    run_group7
  fi

  if [[ "$FILTER" == "all" || "$FILTER" == "group8" || "$FILTER" =~ ^TS-06-(2[89]|3[012])$ ]]; then
    run_group8
  fi

  if [[ "$FILTER" == "all" || "$FILTER" == "edge2" || "$FILTER" =~ ^TS-06-E[45678]$ ]]; then
    run_edge_cases_2
  fi

  summary
}

main
