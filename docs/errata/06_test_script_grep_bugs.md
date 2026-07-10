# Errata: Spec 06 — Test Script grep Bugs

## Issue

The shell-based test script `tests/web_scaffold/test_scaffold.sh` has two
classes of grep-related bugs that cause false negatives on macOS (BSD grep):

### 1. Double-dash patterns treated as grep flags

Tests TS-06-28 and TS-06-E8 use `file_contains "$file" "--background"` (and
`--foreground`, `--primary`). The `file_contains` function calls
`grep -q "$pattern" "$file"`, so grep receives `--background` as its first
argument and interprets it as an unrecognized option flag rather than a search
pattern.

**Fix:** Use `grep -qF -- "--background"` or `grep -q -e "--background"` to
force grep to treat the argument as a pattern.

**Affected tests:** TS-06-28, TS-06-E8

### 2. BRE interval expression in glob pattern

Test TS-06-29 uses `file_contains "$tw_file" './src/\*\*/\*.\{ts,tsx,js,jsx\}'`.
On BSD grep (macOS), `\{` opens a BRE interval expression (e.g., `\{n,m\}`).
The content `ts,tsx,js,jsx` is not a valid interval, so grep returns exit
code 2 (error).

**Fix:** Use `grep -qF './src/**/*.{ts,tsx,js,jsx}'` for a fixed-string match.

**Affected tests:** TS-06-29

### Impact

The implementation files (`globals.css`, `tailwind.config.ts`) contain the
correct content. The test failures are false negatives caused by grep
misinterpreting the search patterns. The implementation is spec-compliant.

### Workaround

Run tests on GNU/Linux where `grep` handles these patterns differently, or
manually verify file contents with `cat` or `grep -F`.
