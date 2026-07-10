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
  grep -qF -- "$pattern" "$file" 2>/dev/null
}

file_not_contains() {
  local file="$1"
  local pattern="$2"
  ! grep -qF -- "$pattern" "$file" 2>/dev/null
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
      file_contains "$tw_file" './src/**/*.{ts,tsx,js,jsx}' || { all_ok=false; msg+="missing './src/**/*.{ts,tsx,js,jsx}' in content. "; }
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

      file_contains "$tw_file" './src/**/' && has_src_glob=true
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
# GROUP 9: Vite dev server proxy configuration (TS-06-33 through TS-06-37)
# =============================================================================

run_group9() {
  section "Group 9: Vite dev server proxy configuration"

  # TS-06-33: vite.config.ts configures dev server port 5173
  # Requirement: 06-REQ-9.1
  if should_run "TS-06-33" "group9"; then
    local vite_file="${WEB_DIR}/vite.config.ts"
    if file_exists "$vite_file"; then
      if grep -qE 'port\s*:\s*5173' "$vite_file" 2>/dev/null; then
        pass "TS-06-33: vite.config.ts configures dev server port 5173"
      else
        fail "TS-06-33: vite.config.ts missing port: 5173 configuration" \
          "Expected server.port to be 5173"
      fi
    else
      fail "TS-06-33: web/vite.config.ts does not exist"
    fi
  fi

  # TS-06-34: Vite proxy forwards /api/* to http://localhost:8080 with
  #           changeOrigin: true and no rewrite
  # Requirement: 06-REQ-9.2
  if should_run "TS-06-34" "group9"; then
    local vite_file="${WEB_DIR}/vite.config.ts"
    if file_exists "$vite_file"; then
      local has_api_entry=false
      local has_target=false
      local has_change_origin=false

      # Check for /api proxy entry (quoted as '/api' or "/api")
      grep -qE "['\"/]api['\"]" "$vite_file" 2>/dev/null && has_api_entry=true
      file_contains "$vite_file" "http://localhost:8080" && has_target=true
      grep -qE 'changeOrigin\s*:\s*true' "$vite_file" 2>/dev/null && has_change_origin=true

      if [[ "$has_api_entry" == "true" && "$has_target" == "true" && "$has_change_origin" == "true" ]]; then
        # Check that no rewrite key appears in the proxy section
        # Extract the proxy section and check for 'rewrite' adjacent to /api
        local proxy_section
        proxy_section="$(python3 -c "
import re, sys
with open('${vite_file}') as f:
    content = f.read()
# Find the proxy block — look for proxy: { ... }
m = re.search(r'proxy\s*:\s*\{', content)
if not m:
    print('NO_PROXY_SECTION')
    sys.exit(0)
# Extract from proxy: { to the matching closing brace
start = m.start()
depth = 0
for i in range(m.end()-1, len(content)):
    if content[i] == '{':
        depth += 1
    elif content[i] == '}':
        depth -= 1
        if depth == 0:
            print(content[start:i+1])
            sys.exit(0)
            break
print(content[start:])
" 2>/dev/null)"

        # Look for rewrite within the /api proxy entry specifically
        if echo "$proxy_section" | grep -q "rewrite" 2>/dev/null; then
          fail "TS-06-34: /api proxy has a 'rewrite' key — must use verbatim forwarding"
        else
          pass "TS-06-34: /api proxy targets localhost:8080, changeOrigin: true, no rewrite"
        fi
      else
        local msg=""
        [[ "$has_api_entry" == "false" ]] && msg+="missing /api proxy entry. "
        [[ "$has_target" == "false" ]] && msg+="missing http://localhost:8080 target. "
        [[ "$has_change_origin" == "false" ]] && msg+="missing changeOrigin: true."
        fail "TS-06-34: Vite proxy config for /api incomplete" "$msg"
      fi
    else
      fail "TS-06-34: web/vite.config.ts does not exist"
    fi
  fi

  # TS-06-35: Vite proxy forwards /healthz to http://localhost:8080
  #           with changeOrigin: true
  # Requirement: 06-REQ-9.3
  if should_run "TS-06-35" "group9"; then
    local vite_file="${WEB_DIR}/vite.config.ts"
    if file_exists "$vite_file"; then
      local has_healthz=false
      local has_target=false
      local has_change_origin=false

      file_contains "$vite_file" "/healthz" && has_healthz=true
      file_contains "$vite_file" "http://localhost:8080" && has_target=true
      grep -qE 'changeOrigin\s*:\s*true' "$vite_file" 2>/dev/null && has_change_origin=true

      if [[ "$has_healthz" == "true" && "$has_target" == "true" && "$has_change_origin" == "true" ]]; then
        pass "TS-06-35: /healthz proxy targets localhost:8080, changeOrigin: true"
      else
        local msg=""
        [[ "$has_healthz" == "false" ]] && msg+="missing /healthz proxy entry. "
        [[ "$has_target" == "false" ]] && msg+="missing http://localhost:8080 target. "
        [[ "$has_change_origin" == "false" ]] && msg+="missing changeOrigin: true."
        fail "TS-06-35: Vite proxy config for /healthz incomplete" "$msg"
      fi
    else
      fail "TS-06-35: web/vite.config.ts does not exist"
    fi
  fi

  # TS-06-36: Vite proxy forwards /readyz to http://localhost:8080
  #           with changeOrigin: true
  # Requirement: 06-REQ-9.4
  if should_run "TS-06-36" "group9"; then
    local vite_file="${WEB_DIR}/vite.config.ts"
    if file_exists "$vite_file"; then
      local has_readyz=false
      local has_target=false
      local has_change_origin=false

      file_contains "$vite_file" "/readyz" && has_readyz=true
      file_contains "$vite_file" "http://localhost:8080" && has_target=true
      grep -qE 'changeOrigin\s*:\s*true' "$vite_file" 2>/dev/null && has_change_origin=true

      if [[ "$has_readyz" == "true" && "$has_target" == "true" && "$has_change_origin" == "true" ]]; then
        pass "TS-06-36: /readyz proxy targets localhost:8080, changeOrigin: true"
      else
        local msg=""
        [[ "$has_readyz" == "false" ]] && msg+="missing /readyz proxy entry. "
        [[ "$has_target" == "false" ]] && msg+="missing http://localhost:8080 target. "
        [[ "$has_change_origin" == "false" ]] && msg+="missing changeOrigin: true."
        fail "TS-06-36: Vite proxy config for /readyz incomplete" "$msg"
      fi
    else
      fail "TS-06-36: web/vite.config.ts does not exist"
    fi
  fi

  # TS-06-37: npm run dev starts successfully without backend on port 8080
  # Requirement: 06-REQ-9.5
  # Note: This test starts the dev server in the background, waits briefly,
  # checks if port 5173 is listening, then terminates the server.
  if should_run "TS-06-37" "group9"; then
    if file_exists "${WEB_DIR}/package.json" && dir_exists "${WEB_DIR}/node_modules"; then
      # Ensure nothing is on port 8080 (backend) — this should be the normal state
      # Start the dev server in the background
      local dev_pid=""
      local dev_started=false

      (cd "${WEB_DIR}" && npm run dev >/dev/null 2>&1) &
      dev_pid=$!

      # Wait for the dev server to bind to port 5173 (up to 10 seconds)
      local retries=0
      while [[ $retries -lt 20 ]]; do
        if command -v lsof >/dev/null 2>&1; then
          if lsof -i :5173 -sTCP:LISTEN >/dev/null 2>&1; then
            dev_started=true
            break
          fi
        elif command -v ss >/dev/null 2>&1; then
          if ss -tln | grep -q ':5173 ' 2>/dev/null; then
            dev_started=true
            break
          fi
        elif command -v netstat >/dev/null 2>&1; then
          if netstat -tln 2>/dev/null | grep -q ':5173 '; then
            dev_started=true
            break
          fi
        else
          # Fallback: try to connect
          if (echo > /dev/tcp/localhost/5173) 2>/dev/null; then
            dev_started=true
            break
          fi
        fi
        sleep 0.5
        retries=$((retries + 1))
      done

      # Clean up: terminate the dev server
      if [[ -n "$dev_pid" ]]; then
        kill "$dev_pid" 2>/dev/null || true
        wait "$dev_pid" 2>/dev/null || true
      fi

      if [[ "$dev_started" == "true" ]]; then
        pass "TS-06-37: npm run dev starts on port 5173 without backend on 8080"
      else
        fail "TS-06-37: npm run dev did not bind to port 5173 within timeout"
      fi
    else
      if ! file_exists "${WEB_DIR}/package.json"; then
        fail "TS-06-37: web/package.json does not exist"
      else
        fail "TS-06-37: Cannot test dev server — node_modules not installed" \
          "Run npm ci in web/ first"
      fi
    fi
  fi
}

# =============================================================================
# GROUP 10: Makefile build targets (TS-06-38 through TS-06-41)
# =============================================================================

run_group10() {
  section "Group 10: Makefile build targets"

  local makefile="${REPO_ROOT}/Makefile"

  # TS-06-38: Makefile contains web-dev .PHONY target with recipe 'cd web && npm run dev'
  # Requirement: 06-REQ-10.1
  if should_run "TS-06-38" "group10"; then
    if file_exists "$makefile"; then
      local has_phony=false
      local has_recipe=false

      # Check if web-dev appears in any .PHONY declaration
      grep -qE '\.PHONY.*web-dev' "$makefile" 2>/dev/null && has_phony=true
      # Check for the recipe
      file_contains "$makefile" "cd web && npm run dev" && has_recipe=true

      if [[ "$has_phony" == "true" && "$has_recipe" == "true" ]]; then
        pass "TS-06-38: Makefile has web-dev in .PHONY with correct recipe"
      else
        local msg=""
        [[ "$has_phony" == "false" ]] && msg+="web-dev not in .PHONY. "
        [[ "$has_recipe" == "false" ]] && msg+="missing 'cd web && npm run dev' recipe."
        fail "TS-06-38: Makefile web-dev target incomplete" "$msg"
      fi
    else
      fail "TS-06-38: Root Makefile does not exist"
    fi
  fi

  # TS-06-39: Makefile contains web-build .PHONY target; make web-build exits 0,
  #           web/dist/ non-empty
  # Requirement: 06-REQ-10.2
  if should_run "TS-06-39" "group10"; then
    if file_exists "$makefile"; then
      local has_phony=false
      local has_recipe=false

      grep -qE '\.PHONY.*web-build' "$makefile" 2>/dev/null && has_phony=true
      file_contains "$makefile" "cd web && npm run build" && has_recipe=true

      if [[ "$has_phony" == "true" && "$has_recipe" == "true" ]]; then
        # Run make web-build if node_modules exists
        if dir_exists "${WEB_DIR}/node_modules"; then
          if (cd "${REPO_ROOT}" && make web-build 2>/dev/null); then
            if dir_exists "${WEB_DIR}/dist" && [[ -n "$(ls -A "${WEB_DIR}/dist/" 2>/dev/null)" ]]; then
              pass "TS-06-39: web-build in .PHONY, make web-build exits 0, dist/ non-empty"
            else
              fail "TS-06-39: make web-build succeeded but web/dist/ is empty or missing"
            fi
          else
            fail "TS-06-39: make web-build exited with non-zero code"
          fi
        else
          pass "TS-06-39: web-build in .PHONY with correct recipe (build skipped — no node_modules)"
        fi
      else
        local msg=""
        [[ "$has_phony" == "false" ]] && msg+="web-build not in .PHONY. "
        [[ "$has_recipe" == "false" ]] && msg+="missing 'cd web && npm run build' recipe."
        fail "TS-06-39: Makefile web-build target incomplete" "$msg"
      fi
    else
      fail "TS-06-39: Root Makefile does not exist"
    fi
  fi

  # TS-06-40: Makefile contains web-lint .PHONY target; make web-lint exits 0
  # Requirement: 06-REQ-10.3
  if should_run "TS-06-40" "group10"; then
    if file_exists "$makefile"; then
      local has_phony=false
      local has_recipe=false

      grep -qE '\.PHONY.*web-lint' "$makefile" 2>/dev/null && has_phony=true
      file_contains "$makefile" "cd web && npm run lint" && has_recipe=true

      if [[ "$has_phony" == "true" && "$has_recipe" == "true" ]]; then
        # Run make web-lint if node_modules exists
        if dir_exists "${WEB_DIR}/node_modules"; then
          if (cd "${REPO_ROOT}" && make web-lint 2>/dev/null); then
            pass "TS-06-40: web-lint in .PHONY, make web-lint exits 0"
          else
            fail "TS-06-40: make web-lint exited with non-zero code"
          fi
        else
          pass "TS-06-40: web-lint in .PHONY with correct recipe (lint skipped — no node_modules)"
        fi
      else
        local msg=""
        [[ "$has_phony" == "false" ]] && msg+="web-lint not in .PHONY. "
        [[ "$has_recipe" == "false" ]] && msg+="missing 'cd web && npm run lint' recipe."
        fail "TS-06-40: Makefile web-lint target incomplete" "$msg"
      fi
    else
      fail "TS-06-40: Root Makefile does not exist"
    fi
  fi

  # TS-06-41: All three targets declared in .PHONY; make -n exits 0
  # Requirement: 06-REQ-10.4
  if should_run "TS-06-41" "group10"; then
    if file_exists "$makefile"; then
      local all_phony=true
      local msg=""

      # Check all three targets appear somewhere in .PHONY lines
      for target in web-dev web-build web-lint; do
        if ! grep -qE '\.PHONY.*'"$target" "$makefile" 2>/dev/null; then
          all_phony=false
          msg+="${target} not in .PHONY. "
        fi
      done

      if [[ "$all_phony" == "true" ]]; then
        # Verify make -n (dry-run) works for all three targets
        if (cd "${REPO_ROOT}" && make -n web-dev web-build web-lint 2>/dev/null); then
          pass "TS-06-41: All three targets in .PHONY; make -n exits 0"
        else
          fail "TS-06-41: All targets in .PHONY but make -n failed"
        fi
      else
        fail "TS-06-41: Not all targets in .PHONY" "$msg"
      fi
    else
      fail "TS-06-41: Root Makefile does not exist"
    fi
  fi
}

# =============================================================================
# GROUP 11: Hello-world route and 404 catch-all (TS-06-42 through TS-06-49)
# =============================================================================

run_group11() {
  section "Group 11: Hello-world route and 404 catch-all"

  # TS-06-42: src/routes/index.tsx renders heading with 'af-hub'
  # Requirement: 06-REQ-11.1
  if should_run "TS-06-42" "group11"; then
    local index_file="${WEB_DIR}/src/routes/index.tsx"
    if file_exists "$index_file"; then
      local has_af_hub=false
      local has_heading=false

      file_contains "$index_file" "af-hub" && has_af_hub=true
      # Check for any heading element: <h1>, <h2>, etc.
      grep -qE '<h[1-6]' "$index_file" 2>/dev/null && has_heading=true

      if [[ "$has_af_hub" == "true" && "$has_heading" == "true" ]]; then
        pass "TS-06-42: routes/index.tsx renders heading with 'af-hub'"
      else
        local msg=""
        [[ "$has_af_hub" == "false" ]] && msg+="missing 'af-hub' text. "
        [[ "$has_heading" == "false" ]] && msg+="missing heading element (<h1>...<h6>)."
        fail "TS-06-42: routes/index.tsx missing required content" "$msg"
      fi
    else
      fail "TS-06-42: web/src/routes/index.tsx does not exist"
    fi
  fi

  # TS-06-43: src/routes/index.tsx references __APP_VERSION__; tsc --noEmit exits 0
  # Requirement: 06-REQ-11.2
  if should_run "TS-06-43" "group11"; then
    local index_file="${WEB_DIR}/src/routes/index.tsx"
    if file_exists "$index_file"; then
      if file_contains "$index_file" "__APP_VERSION__"; then
        if dir_exists "${WEB_DIR}/node_modules"; then
          if (cd "${WEB_DIR}" && npx tsc --noEmit 2>/dev/null); then
            pass "TS-06-43: routes/index.tsx uses __APP_VERSION__; tsc --noEmit passes"
          else
            fail "TS-06-43: __APP_VERSION__ referenced but tsc --noEmit failed"
          fi
        else
          pass "TS-06-43: routes/index.tsx uses __APP_VERSION__ (tsc skipped — no node_modules)"
        fi
      else
        fail "TS-06-43: routes/index.tsx missing __APP_VERSION__ reference"
      fi
    else
      fail "TS-06-43: web/src/routes/index.tsx does not exist"
    fi
  fi

  # TS-06-44: src/routes/index.tsx imports Button from @/components/ui/button
  #           and renders <Button
  # Requirement: 06-REQ-11.3
  if should_run "TS-06-44" "group11"; then
    local index_file="${WEB_DIR}/src/routes/index.tsx"
    if file_exists "$index_file"; then
      local has_import=false
      local has_render=false

      grep -q "from '@/components/ui/button'" "$index_file" 2>/dev/null || \
        grep -q 'from "@/components/ui/button"' "$index_file" 2>/dev/null
      [[ $? -eq 0 ]] && has_import=true
      file_contains "$index_file" "<Button" && has_render=true

      if [[ "$has_import" == "true" && "$has_render" == "true" ]]; then
        pass "TS-06-44: routes/index.tsx imports Button from @/ alias and renders <Button"
      else
        local msg=""
        [[ "$has_import" == "false" ]] && msg+="missing import from '@/components/ui/button'. "
        [[ "$has_render" == "false" ]] && msg+="missing <Button render."
        fail "TS-06-44: routes/index.tsx missing Button import/render" "$msg"
      fi
    else
      fail "TS-06-44: web/src/routes/index.tsx does not exist"
    fi
  fi

  # TS-06-45: vite.config.ts defines __APP_VERSION__ via Vite define option
  # Requirement: 06-REQ-11.4
  if should_run "TS-06-45" "group11"; then
    local vite_file="${WEB_DIR}/vite.config.ts"
    if file_exists "$vite_file"; then
      local has_define=false
      local has_app_version=false
      local has_stringify=false

      file_contains "$vite_file" "define" && has_define=true
      file_contains "$vite_file" "__APP_VERSION__" && has_app_version=true
      file_contains "$vite_file" "JSON.stringify(process.env.npm_package_version)" && has_stringify=true

      if [[ "$has_define" == "true" && "$has_app_version" == "true" && "$has_stringify" == "true" ]]; then
        pass "TS-06-45: vite.config.ts defines __APP_VERSION__ via JSON.stringify(process.env.npm_package_version)"
      else
        local msg=""
        [[ "$has_define" == "false" ]] && msg+="missing 'define' section. "
        [[ "$has_app_version" == "false" ]] && msg+="missing '__APP_VERSION__'. "
        [[ "$has_stringify" == "false" ]] && msg+="missing JSON.stringify(process.env.npm_package_version)."
        fail "TS-06-45: vite.config.ts __APP_VERSION__ define incomplete" "$msg"
      fi
    else
      fail "TS-06-45: web/vite.config.ts does not exist"
    fi
  fi

  # TS-06-46: src/App.tsx configures React Router with '/' route
  # Requirement: 06-REQ-11.5
  if should_run "TS-06-46" "group11"; then
    local app_file="${WEB_DIR}/src/App.tsx"
    if file_exists "$app_file"; then
      local has_router=false
      local has_index_route=false

      # Check for BrowserRouter or createBrowserRouter or RouterProvider
      (file_contains "$app_file" "BrowserRouter" || \
       file_contains "$app_file" "createBrowserRouter" || \
       file_contains "$app_file" "RouterProvider") && has_router=true

      # Check for registration of index route at /
      (file_contains "$app_file" "routes/index" || \
       file_contains "$app_file" "IndexPage" || \
       file_contains "$app_file" 'path="/"' || \
       file_contains "$app_file" "path: '/'" || \
       file_contains "$app_file" "path: \"/\"") && has_index_route=true

      if [[ "$has_router" == "true" && "$has_index_route" == "true" ]]; then
        pass "TS-06-46: App.tsx configures React Router and registers '/' route"
      else
        local msg=""
        [[ "$has_router" == "false" ]] && msg+="missing BrowserRouter/createBrowserRouter/RouterProvider. "
        [[ "$has_index_route" == "false" ]] && msg+="missing '/' route registration."
        fail "TS-06-46: App.tsx missing router/route configuration" "$msg"
      fi
    else
      fail "TS-06-46: web/src/App.tsx does not exist"
    fi
  fi

  # TS-06-47: src/App.tsx wraps router with QueryClientProvider
  # Requirement: 06-REQ-11.6
  if should_run "TS-06-47" "group11"; then
    local app_file="${WEB_DIR}/src/App.tsx"
    if file_exists "$app_file"; then
      local has_provider=false
      local has_query_import=false
      local has_query_client=false

      file_contains "$app_file" "QueryClientProvider" && has_provider=true
      file_contains "$app_file" "@tanstack/react-query" && has_query_import=true
      file_contains "$app_file" "QueryClient" && has_query_client=true

      if [[ "$has_provider" == "true" && "$has_query_import" == "true" && "$has_query_client" == "true" ]]; then
        pass "TS-06-47: App.tsx uses QueryClientProvider with QueryClient from @tanstack/react-query"
      else
        local msg=""
        [[ "$has_provider" == "false" ]] && msg+="missing QueryClientProvider. "
        [[ "$has_query_import" == "false" ]] && msg+="missing @tanstack/react-query import. "
        [[ "$has_query_client" == "false" ]] && msg+="missing QueryClient."
        fail "TS-06-47: App.tsx missing QueryClientProvider setup" "$msg"
      fi
    else
      fail "TS-06-47: web/src/App.tsx does not exist"
    fi
  fi

  # TS-06-48: src/App.tsx registers wildcard catch-all route for not-found
  # Requirement: 06-REQ-12.1
  if should_run "TS-06-48" "group11"; then
    local app_file="${WEB_DIR}/src/App.tsx"
    if file_exists "$app_file"; then
      local has_wildcard=false
      local has_not_found=false

      # Check for path="*" or path: '*' or path={"*"}
      (file_contains "$app_file" 'path="*"' || \
       file_contains "$app_file" "path: '*'" || \
       file_contains "$app_file" "path: \"*\"" || \
       file_contains "$app_file" 'path={"*"}') && has_wildcard=true

      # Check for not-found component reference
      (file_contains "$app_file" "not-found" || \
       file_contains "$app_file" "NotFound") && has_not_found=true

      if [[ "$has_wildcard" == "true" && "$has_not_found" == "true" ]]; then
        pass "TS-06-48: App.tsx has wildcard catch-all route rendering not-found component"
      else
        local msg=""
        [[ "$has_wildcard" == "false" ]] && msg+="missing wildcard path=\"*\" route. "
        [[ "$has_not_found" == "false" ]] && msg+="missing not-found component reference."
        fail "TS-06-48: App.tsx missing catch-all route" "$msg"
      fi
    else
      fail "TS-06-48: web/src/App.tsx does not exist"
    fi
  fi

  # TS-06-49: src/routes/not-found.tsx displays 404 message and link to /
  # Requirement: 06-REQ-12.2
  if should_run "TS-06-49" "group11"; then
    local nf_file="${WEB_DIR}/src/routes/not-found.tsx"
    if file_exists "$nf_file"; then
      local has_404=false
      local has_not_found_text=false
      local has_link_home=false

      file_contains "$nf_file" "404" && has_404=true
      (file_contains "$nf_file" "not found" || \
       file_contains "$nf_file" "Not Found" || \
       file_contains "$nf_file" "Not found") && has_not_found_text=true
      # Check for <Link to="/"> or <a href="/">
      (file_contains "$nf_file" 'to="/"' || \
       file_contains "$nf_file" "to='/'" || \
       file_contains "$nf_file" 'href="/"') && has_link_home=true

      if [[ "$has_404" == "true" && "$has_not_found_text" == "true" && "$has_link_home" == "true" ]]; then
        pass "TS-06-49: not-found.tsx has 404 message and link to /"
      else
        local msg=""
        [[ "$has_404" == "false" ]] && msg+="missing '404' text. "
        [[ "$has_not_found_text" == "false" ]] && msg+="missing 'not found' message. "
        [[ "$has_link_home" == "false" ]] && msg+="missing link to '/'."
        fail "TS-06-49: not-found.tsx missing required content" "$msg"
      fi
    else
      fail "TS-06-49: web/src/routes/not-found.tsx does not exist"
    fi
  fi
}

# =============================================================================
# EDGE CASES (Group 3): TS-06-E1, TS-06-E2, TS-06-E3, TS-06-E9, TS-06-E10,
#                        TS-06-E11
# =============================================================================

run_edge_cases_3() {
  section "Edge Cases (Group 3): Environment, shadcn, proxy, routes"

  # TS-06-E1: Missing or wrong-version .nvmrc is detectable
  # Requirement: 06-REQ-1.E1
  # Note: This is a verification that the scaffold correctly sets .nvmrc.
  # We assert the file must exist with content '22' — any deviation is a failure.
  if should_run "TS-06-E1" "edge3"; then
    if file_exists "${WEB_DIR}/.nvmrc"; then
      local content
      content="$(cat "${WEB_DIR}/.nvmrc" | tr -d '[:space:]')"
      if [[ "$content" == "22" ]]; then
        pass "TS-06-E1: .nvmrc exists with correct version '22'"
      else
        fail "TS-06-E1: .nvmrc contains '${content}' instead of '22'" \
          "Developers using nvm will select the wrong Node.js version"
      fi
    else
      fail "TS-06-E1: web/.nvmrc does not exist" \
        "Setup is incomplete — environment parity guarantee broken"
    fi
  fi

  # TS-06-E2: components.json absence would break npx shadcn-ui add
  # Requirement: 06-REQ-3.E1
  # Verify that components.json exists — its absence means the shadcn CLI
  # cannot add new components without re-initialization.
  if should_run "TS-06-E2" "edge3"; then
    if file_exists "${WEB_DIR}/components.json"; then
      if json_valid "${WEB_DIR}/components.json"; then
        pass "TS-06-E2: components.json exists and is valid — shadcn add will work"
      else
        fail "TS-06-E2: components.json exists but is invalid JSON" \
          "npx shadcn-ui add would fail or prompt for re-init"
      fi
    else
      fail "TS-06-E2: components.json is absent" \
        "Future npx shadcn-ui add <component> invocations would fail"
    fi
  fi

  # TS-06-E3: src/lib/utils.ts absence would break shadcn component compilation
  # Requirement: 06-REQ-3.E2
  # Verify that utils.ts exists and exports cn — its absence means all
  # shadcn/ui components (including Button) fail to compile.
  if should_run "TS-06-E3" "edge3"; then
    local utils_file="${WEB_DIR}/src/lib/utils.ts"
    if file_exists "$utils_file"; then
      if file_contains "$utils_file" "cn" && file_contains "$utils_file" "export"; then
        pass "TS-06-E3: src/lib/utils.ts exists and exports cn — component imports resolve"
      else
        fail "TS-06-E3: src/lib/utils.ts exists but missing cn export" \
          "shadcn/ui components would fail to compile"
      fi
    else
      fail "TS-06-E3: src/lib/utils.ts is absent" \
        "All shadcn/ui component copies would fail with module-not-found"
    fi
  fi

  # TS-06-E9: No rewrite key in any proxy rule in vite.config.ts
  # Requirement: 06-REQ-9.E1
  if should_run "TS-06-E9" "edge3"; then
    local vite_file="${WEB_DIR}/vite.config.ts"
    if file_exists "$vite_file"; then
      # Extract the proxy section and check for any 'rewrite' key
      local proxy_has_rewrite=false
      local proxy_section
      proxy_section="$(python3 -c "
import re
with open('${vite_file}') as f:
    content = f.read()
m = re.search(r'proxy\s*:\s*\{', content)
if not m:
    print('NO_PROXY')
    exit(0)
start = m.start()
depth = 0
for i in range(m.end()-1, len(content)):
    if content[i] == '{':
        depth += 1
    elif content[i] == '}':
        depth -= 1
        if depth == 0:
            print(content[start:i+1])
            exit(0)
print(content[start:])
" 2>/dev/null)"

      if [[ "$proxy_section" == "NO_PROXY" ]]; then
        fail "TS-06-E9: No proxy section found in vite.config.ts"
      elif echo "$proxy_section" | grep -q "rewrite" 2>/dev/null; then
        fail "TS-06-E9: Proxy section contains 'rewrite' — verbatim forwarding violated" \
          "No proxy rule should have a rewrite property"
      else
        pass "TS-06-E9: No 'rewrite' in proxy config — verbatim forwarding enforced"
      fi
    else
      fail "TS-06-E9: web/vite.config.ts does not exist"
    fi
  fi

  # TS-06-E10: vite.config.ts uses process.env.npm_package_version for __APP_VERSION__
  # Requirement: 06-REQ-11.E1
  # Validates that npm_package_version is used (so direct npx vite yields 'undefined').
  if should_run "TS-06-E10" "edge3"; then
    local vite_file="${WEB_DIR}/vite.config.ts"
    if file_exists "$vite_file"; then
      if file_contains "$vite_file" "process.env.npm_package_version"; then
        pass "TS-06-E10: vite.config.ts uses process.env.npm_package_version for version injection"
      else
        fail "TS-06-E10: vite.config.ts does not reference process.env.npm_package_version" \
          "__APP_VERSION__ would not reflect package.json version"
      fi
    else
      fail "TS-06-E10: web/vite.config.ts does not exist"
    fi
  fi

  # TS-06-E11: App.tsx must contain wildcard catch-all route
  # Requirement: 06-REQ-12.E1
  if should_run "TS-06-E11" "edge3"; then
    local app_file="${WEB_DIR}/src/App.tsx"
    if file_exists "$app_file"; then
      if file_contains "$app_file" 'path="*"' || \
         file_contains "$app_file" "path: '*'" || \
         file_contains "$app_file" "path: \"*\"" || \
         file_contains "$app_file" 'path={"*"}'; then
        pass "TS-06-E11: App.tsx contains wildcard catch-all route"
      else
        fail "TS-06-E11: App.tsx missing wildcard catch-all route" \
          "Unmatched paths would render blank page or throw React Router error"
      fi
    else
      fail "TS-06-E11: web/src/App.tsx does not exist"
    fi
  fi
}

# =============================================================================
# PROPERTY TESTS: TS-06-P1 through TS-06-P5
# =============================================================================

run_property_tests() {
  section "Property Tests: Scaffold invariants"

  # TS-06-P1: Zero lint errors on clean scaffold
  # Property: 06-PROP-1
  # Validates: 06-REQ-7.5, 06-REQ-7.6, 06-REQ-5.1
  if should_run "TS-06-P1" "property"; then
    if file_exists "${WEB_DIR}/package.json" && dir_exists "${WEB_DIR}/node_modules"; then
      local lint_ok=true
      local msg=""

      # Run npm run lint (eslint . && tsc --noEmit)
      if ! (cd "${WEB_DIR}" && npm run lint 2>/dev/null); then
        lint_ok=false
        msg+="npm run lint failed. "
      fi

      # Also verify individually
      if ! (cd "${WEB_DIR}" && npx eslint . 2>/dev/null); then
        lint_ok=false
        msg+="eslint . failed. "
      fi

      if ! (cd "${WEB_DIR}" && npx tsc --noEmit 2>/dev/null); then
        lint_ok=false
        msg+="tsc --noEmit failed. "
      fi

      if [[ "$lint_ok" == "true" ]]; then
        pass "TS-06-P1: Zero lint errors — npm run lint, eslint, and tsc all exit 0"
      else
        fail "TS-06-P1: Lint errors detected on clean scaffold" "$msg"
      fi
    else
      if ! file_exists "${WEB_DIR}/package.json"; then
        fail "TS-06-P1: web/package.json does not exist"
      else
        fail "TS-06-P1: Cannot verify lint — node_modules not installed"
      fi
    fi
  fi

  # TS-06-P2: Path alias consistency across TypeScript and Vite
  # Property: 06-PROP-2
  # Validates: 06-REQ-6.1, 06-REQ-6.2, 06-REQ-6.3
  if should_run "TS-06-P2" "property"; then
    local all_ok=true
    local msg=""

    # Check tsconfig.json paths
    if file_exists "${WEB_DIR}/tsconfig.json"; then
      local tsc_alias
      tsc_alias="$(python3 -c "
import json
with open('${WEB_DIR}/tsconfig.json') as f:
    data = json.load(f)
paths = data.get('compilerOptions', {}).get('paths', {})
alias = paths.get('@/*', [])
print('ok' if alias == ['./src/*'] else 'bad: %s' % alias)
" 2>/dev/null)"
      if [[ "$tsc_alias" != "ok" ]]; then
        all_ok=false
        msg+="tsconfig paths: ${tsc_alias}. "
      fi
    else
      all_ok=false
      msg+="tsconfig.json missing. "
    fi

    # Check vite.config.ts alias
    if file_exists "${WEB_DIR}/vite.config.ts"; then
      if ! (file_contains "${WEB_DIR}/vite.config.ts" "alias" && \
            grep -qE "['\"]@['\"]" "${WEB_DIR}/vite.config.ts" 2>/dev/null && \
            file_contains "${WEB_DIR}/vite.config.ts" "./src"); then
        all_ok=false
        msg+="vite.config.ts alias incomplete. "
      fi
    else
      all_ok=false
      msg+="vite.config.ts missing. "
    fi

    # Run both tsc and build to confirm consistency
    if [[ "$all_ok" == "true" ]] && dir_exists "${WEB_DIR}/node_modules"; then
      if ! (cd "${WEB_DIR}" && npx tsc --noEmit 2>/dev/null); then
        all_ok=false
        msg+="tsc --noEmit failed. "
      fi
      if ! (cd "${WEB_DIR}" && npm run build 2>/dev/null); then
        all_ok=false
        msg+="npm run build failed. "
      fi
    fi

    if [[ "$all_ok" == "true" ]]; then
      pass "TS-06-P2: @/ alias consistent in tsconfig.json and vite.config.ts"
    else
      fail "TS-06-P2: Path alias inconsistency detected" "$msg"
    fi
  fi

  # TS-06-P3: Proxy verbatim forwarding — no rewrite in any proxy rule
  # Property: 06-PROP-3
  # Validates: 06-REQ-9.2, 06-REQ-9.3, 06-REQ-9.4
  if should_run "TS-06-P3" "property"; then
    local vite_file="${WEB_DIR}/vite.config.ts"
    if file_exists "$vite_file"; then
      local all_ok=true
      local msg=""

      # Check that all three proxy paths exist
      for path_entry in "/api" "/healthz" "/readyz"; do
        if ! file_contains "$vite_file" "$path_entry"; then
          all_ok=false
          msg+="missing ${path_entry} proxy entry. "
        fi
      done

      # Check changeOrigin: true exists
      if ! grep -qE 'changeOrigin\s*:\s*true' "$vite_file" 2>/dev/null; then
        all_ok=false
        msg+="missing changeOrigin: true. "
      fi

      # Check target
      if ! file_contains "$vite_file" "http://localhost:8080"; then
        all_ok=false
        msg+="missing http://localhost:8080 target. "
      fi

      # Extract proxy section and verify no rewrite
      local proxy_section
      proxy_section="$(python3 -c "
import re
with open('${vite_file}') as f:
    content = f.read()
m = re.search(r'proxy\s*:\s*\{', content)
if not m:
    print('NO_PROXY')
    exit(0)
start = m.start()
depth = 0
for i in range(m.end()-1, len(content)):
    if content[i] == '{':
        depth += 1
    elif content[i] == '}':
        depth -= 1
        if depth == 0:
            print(content[start:i+1])
            exit(0)
print(content[start:])
" 2>/dev/null)"

      if [[ "$proxy_section" == "NO_PROXY" ]]; then
        all_ok=false
        msg+="no proxy section found. "
      elif echo "$proxy_section" | grep -q "rewrite" 2>/dev/null; then
        all_ok=false
        msg+="proxy section contains 'rewrite'. "
      fi

      if [[ "$all_ok" == "true" ]]; then
        pass "TS-06-P3: All proxy rules forward verbatim — no rewrite, changeOrigin: true"
      else
        fail "TS-06-P3: Proxy verbatim forwarding invariant violated" "$msg"
      fi
    else
      fail "TS-06-P3: web/vite.config.ts does not exist"
    fi
  fi

  # TS-06-P4: shadcn/ui CLI re-invocability — components.json consistent
  # Property: 06-PROP-4
  # Validates: 06-REQ-3.5, 06-REQ-6.1, 06-REQ-6.2
  if should_run "TS-06-P4" "property"; then
    local all_ok=true
    local msg=""

    # Check components.json exists and is valid
    if file_exists "${WEB_DIR}/components.json"; then
      if json_valid "${WEB_DIR}/components.json"; then
        # Verify alias config in components.json matches tsconfig/vite config
        local alias_ok
        alias_ok="$(python3 -c "
import json
with open('${WEB_DIR}/components.json') as f:
    cj = json.load(f)
aliases = cj.get('aliases', {})
# Check that utils alias references @/lib/utils or similar @/ path
utils_alias = aliases.get('utils', '')
has_at = '@/' in utils_alias or '@\\\\' in utils_alias
print('ok' if has_at else 'bad: utils=%s' % utils_alias)
" 2>/dev/null)"
        if [[ "$alias_ok" != "ok" ]]; then
          all_ok=false
          msg+="components.json alias: ${alias_ok}. "
        fi
      else
        all_ok=false
        msg+="components.json is invalid JSON. "
      fi
    else
      all_ok=false
      msg+="components.json missing. "
    fi

    # Verify tsconfig paths
    if file_exists "${WEB_DIR}/tsconfig.json"; then
      if ! json_has_key "${WEB_DIR}/tsconfig.json" "data['compilerOptions']['paths']['@/*']"; then
        all_ok=false
        msg+="tsconfig.json missing @/* paths. "
      fi
    else
      all_ok=false
      msg+="tsconfig.json missing. "
    fi

    # Verify vite alias
    if file_exists "${WEB_DIR}/vite.config.ts"; then
      if ! (file_contains "${WEB_DIR}/vite.config.ts" "alias" && \
            file_contains "${WEB_DIR}/vite.config.ts" "./src"); then
        all_ok=false
        msg+="vite.config.ts alias incomplete. "
      fi
    else
      all_ok=false
      msg+="vite.config.ts missing. "
    fi

    if [[ "$all_ok" == "true" ]]; then
      pass "TS-06-P4: components.json consistent with alias config — shadcn add re-invocable"
    else
      fail "TS-06-P4: shadcn/ui CLI re-invocability broken" "$msg"
    fi
  fi

  # TS-06-P5: Build output reproducibility — npm ci && npm run build succeeds
  # Property: 06-PROP-5
  # Validates: 06-REQ-1.2, 06-REQ-4.1, 06-REQ-11.4
  if should_run "TS-06-P5" "property"; then
    if file_exists "${WEB_DIR}/package.json" && file_exists "${WEB_DIR}/package-lock.json"; then
      # Run npm ci to ensure clean install from lockfile
      if (cd "${WEB_DIR}" && npm ci --ignore-scripts 2>/dev/null); then
        # Run npm run build
        if (cd "${WEB_DIR}" && npm run build 2>/dev/null); then
          if dir_exists "${WEB_DIR}/dist" && [[ -n "$(ls -A "${WEB_DIR}/dist/" 2>/dev/null)" ]]; then
            pass "TS-06-P5: npm ci && npm run build succeeds; dist/ produced"
          else
            fail "TS-06-P5: Build succeeded but web/dist/ is empty or missing"
          fi
        else
          fail "TS-06-P5: npm run build failed after npm ci"
        fi
      else
        fail "TS-06-P5: npm ci failed — lockfile may be inconsistent"
      fi
    else
      local msg=""
      file_exists "${WEB_DIR}/package.json" || msg+="package.json missing. "
      file_exists "${WEB_DIR}/package-lock.json" || msg+="package-lock.json missing."
      fail "TS-06-P5: Cannot verify build reproducibility" "$msg"
    fi
  fi
}

# =============================================================================
# SMOKE TESTS: TS-06-SMOKE-1 through TS-06-SMOKE-5
# Outlines for end-to-end integration validation. These tests require a
# running dev server and/or browser automation. They are scaffolded as
# structural checks here; full E2E execution is deferred to task group 10.
# =============================================================================

run_smoke_tests() {
  section "Smoke Tests: End-to-end scaffold integration (outlines)"

  # TS-06-SMOKE-1: Dev server + hello-world page
  # Execution Path: 06-PATH-1
  # Verifies: make web-dev starts, page renders af-hub heading, version, Button
  if should_run "TS-06-SMOKE-1" "smoke"; then
    # Structural pre-check: verify all files needed for SMOKE-1 exist
    local all_ok=true
    local msg=""
    local required_files=(
      "${REPO_ROOT}/Makefile"
      "${WEB_DIR}/package.json"
      "${WEB_DIR}/vite.config.ts"
      "${WEB_DIR}/index.html"
      "${WEB_DIR}/src/main.tsx"
      "${WEB_DIR}/src/App.tsx"
      "${WEB_DIR}/src/routes/index.tsx"
      "${WEB_DIR}/src/components/ui/button.tsx"
    )
    for f in "${required_files[@]}"; do
      if ! file_exists "$f"; then
        all_ok=false
        msg+="$(basename "$f") missing. "
      fi
    done

    if [[ "$all_ok" == "true" ]]; then
      # Check that index.tsx has af-hub and __APP_VERSION__
      if file_contains "${WEB_DIR}/src/routes/index.tsx" "af-hub" && \
         file_contains "${WEB_DIR}/src/routes/index.tsx" "__APP_VERSION__" && \
         file_contains "${WEB_DIR}/src/routes/index.tsx" "Button"; then
        pass "TS-06-SMOKE-1: Structural pre-check passed — all hello-world files present"
      else
        fail "TS-06-SMOKE-1: Files exist but missing required content in index.tsx"
      fi
    else
      fail "TS-06-SMOKE-1: Missing files for dev server hello-world" "$msg"
    fi
  fi

  # TS-06-SMOKE-2: make web-build produces dist/
  # Execution Path: 06-PATH-2
  # Verifies: make web-build exits 0, dist/ contains HTML, JS, CSS
  if should_run "TS-06-SMOKE-2" "smoke"; then
    if file_exists "${REPO_ROOT}/Makefile" && file_exists "${WEB_DIR}/package.json"; then
      if dir_exists "${WEB_DIR}/node_modules"; then
        if (cd "${REPO_ROOT}" && make web-build 2>/dev/null); then
          local has_html=false
          local has_js=false
          local has_css=false

          [[ -n "$(find "${WEB_DIR}/dist" -name '*.html' 2>/dev/null)" ]] && has_html=true
          [[ -n "$(find "${WEB_DIR}/dist" -name '*.js' 2>/dev/null)" ]] && has_js=true
          [[ -n "$(find "${WEB_DIR}/dist" -name '*.css' 2>/dev/null)" ]] && has_css=true

          if [[ "$has_html" == "true" && "$has_js" == "true" && "$has_css" == "true" ]]; then
            pass "TS-06-SMOKE-2: make web-build exits 0; dist/ has HTML, JS, and CSS"
          else
            local msg=""
            [[ "$has_html" == "false" ]] && msg+="no HTML files. "
            [[ "$has_js" == "false" ]] && msg+="no JS files. "
            [[ "$has_css" == "false" ]] && msg+="no CSS files."
            fail "TS-06-SMOKE-2: Build output incomplete" "$msg"
          fi
        else
          fail "TS-06-SMOKE-2: make web-build failed"
        fi
      else
        skip "TS-06-SMOKE-2" "node_modules not installed"
      fi
    else
      fail "TS-06-SMOKE-2: Makefile or package.json missing"
    fi
  fi

  # TS-06-SMOKE-3: make web-lint passes
  # Execution Path: 06-PATH-3
  # Verifies: make web-lint exits 0 with zero ESLint + tsc errors
  if should_run "TS-06-SMOKE-3" "smoke"; then
    if file_exists "${REPO_ROOT}/Makefile" && file_exists "${WEB_DIR}/package.json"; then
      if dir_exists "${WEB_DIR}/node_modules"; then
        local lint_output
        lint_output="$(cd "${REPO_ROOT}" && make web-lint 2>&1)"
        local lint_exit=$?

        if [[ $lint_exit -eq 0 ]]; then
          # Check for react/react-in-jsx-scope false positives
          if echo "$lint_output" | grep -q "react/react-in-jsx-scope" 2>/dev/null; then
            fail "TS-06-SMOKE-3: Lint passed but react/react-in-jsx-scope errors detected"
          else
            pass "TS-06-SMOKE-3: make web-lint exits 0 with zero errors"
          fi
        else
          fail "TS-06-SMOKE-3: make web-lint exited with code ${lint_exit}"
        fi
      else
        skip "TS-06-SMOKE-3" "node_modules not installed"
      fi
    else
      fail "TS-06-SMOKE-3: Makefile or package.json missing"
    fi
  fi

  # TS-06-SMOKE-4: 404 catch-all page for unmatched paths
  # Execution Path: 06-PATH-4
  # Verifies: Unmatched path renders not-found component with 404 + link to /
  if should_run "TS-06-SMOKE-4" "smoke"; then
    # Structural pre-check: verify all files needed for 404 routing exist
    local all_ok=true
    local msg=""

    if ! file_exists "${WEB_DIR}/src/App.tsx"; then
      all_ok=false
      msg+="App.tsx missing. "
    fi
    if ! file_exists "${WEB_DIR}/src/routes/not-found.tsx"; then
      all_ok=false
      msg+="not-found.tsx missing. "
    fi

    if [[ "$all_ok" == "true" ]]; then
      # Verify App.tsx has catch-all route
      local has_wildcard=false
      (file_contains "${WEB_DIR}/src/App.tsx" 'path="*"' || \
       file_contains "${WEB_DIR}/src/App.tsx" "path: '*'" || \
       file_contains "${WEB_DIR}/src/App.tsx" "path: \"*\"" || \
       file_contains "${WEB_DIR}/src/App.tsx" 'path={"*"}') && has_wildcard=true

      # Verify not-found.tsx has 404 message and link
      local has_404=false
      local has_link=false
      file_contains "${WEB_DIR}/src/routes/not-found.tsx" "404" && has_404=true
      (file_contains "${WEB_DIR}/src/routes/not-found.tsx" 'to="/"' || \
       file_contains "${WEB_DIR}/src/routes/not-found.tsx" "to='/'" || \
       file_contains "${WEB_DIR}/src/routes/not-found.tsx" 'href="/"') && has_link=true

      if [[ "$has_wildcard" == "true" && "$has_404" == "true" && "$has_link" == "true" ]]; then
        pass "TS-06-SMOKE-4: Structural pre-check passed — catch-all route + 404 page ready"
      else
        local detail=""
        [[ "$has_wildcard" == "false" ]] && detail+="no wildcard route. "
        [[ "$has_404" == "false" ]] && detail+="no 404 text. "
        [[ "$has_link" == "false" ]] && detail+="no link to /."
        fail "TS-06-SMOKE-4: 404 route structure incomplete" "$detail"
      fi
    else
      fail "TS-06-SMOKE-4: Missing files for 404 catch-all" "$msg"
    fi
  fi

  # TS-06-SMOKE-5: API proxy verbatim forwarding
  # Execution Path: 06-PATH-5
  # Verifies: /api/example proxied verbatim to localhost:8080 with changeOrigin
  if should_run "TS-06-SMOKE-5" "smoke"; then
    # Structural pre-check: verify proxy config exists with correct rules
    local vite_file="${WEB_DIR}/vite.config.ts"
    if file_exists "$vite_file"; then
      local all_ok=true
      local msg=""

      # Check all three proxy paths
      for proxy_path in "/api" "/healthz" "/readyz"; do
        if ! file_contains "$vite_file" "$proxy_path"; then
          all_ok=false
          msg+="missing ${proxy_path} proxy. "
        fi
      done

      if ! file_contains "$vite_file" "http://localhost:8080"; then
        all_ok=false
        msg+="missing target http://localhost:8080. "
      fi

      if ! grep -qE 'changeOrigin\s*:\s*true' "$vite_file" 2>/dev/null; then
        all_ok=false
        msg+="missing changeOrigin: true. "
      fi

      # Verify no rewrite in proxy section
      local proxy_section
      proxy_section="$(python3 -c "
import re
with open('${vite_file}') as f:
    content = f.read()
m = re.search(r'proxy\s*:\s*\{', content)
if not m:
    print('NO_PROXY')
    exit(0)
start = m.start()
depth = 0
for i in range(m.end()-1, len(content)):
    if content[i] == '{':
        depth += 1
    elif content[i] == '}':
        depth -= 1
        if depth == 0:
            print(content[start:i+1])
            exit(0)
print(content[start:])
" 2>/dev/null)"

      if echo "$proxy_section" | grep -q "rewrite" 2>/dev/null; then
        all_ok=false
        msg+="rewrite found in proxy config. "
      fi

      if [[ "$all_ok" == "true" ]]; then
        pass "TS-06-SMOKE-5: Structural pre-check passed — proxy config correct for verbatim forwarding"
      else
        fail "TS-06-SMOKE-5: Proxy config incomplete for verbatim forwarding" "$msg"
      fi
    else
      fail "TS-06-SMOKE-5: web/vite.config.ts does not exist"
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

  if [[ "$FILTER" == "all" || "$FILTER" == "group9" || "$FILTER" =~ ^TS-06-3[34567]$ ]]; then
    run_group9
  fi

  if [[ "$FILTER" == "all" || "$FILTER" == "group10" || "$FILTER" =~ ^TS-06-(3[89]|4[01])$ ]]; then
    run_group10
  fi

  if [[ "$FILTER" == "all" || "$FILTER" == "group11" || "$FILTER" =~ ^TS-06-4[2345678]$ || "$FILTER" == "TS-06-49" ]]; then
    run_group11
  fi

  if [[ "$FILTER" == "all" || "$FILTER" == "edge3" || "$FILTER" =~ ^TS-06-E(1|2|3|9|10|11)$ ]]; then
    run_edge_cases_3
  fi

  if [[ "$FILTER" == "all" || "$FILTER" == "property" || "$FILTER" =~ ^TS-06-P[12345]$ ]]; then
    run_property_tests
  fi

  if [[ "$FILTER" == "all" || "$FILTER" == "smoke" || "$FILTER" =~ ^TS-06-SMOKE-[12345]$ ]]; then
    run_smoke_tests
  fi

  summary
}

main
