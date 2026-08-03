package gitcmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// initGitRepo creates a temporary directory with an initialised git repository
// suitable for running git commands. Returns the repo path.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Use a temporary GitRunner to run git init. The runner is constructed
	// with the temp dir as workDir. We rely on NewRunner returning a
	// working *GitRunner for setup; if it doesn't, the test will fail
	// during setup, which is acceptable since the implementation doesn't
	// exist yet.
	runner := NewRunner(dir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}
	ctx := context.Background()
	_, _, err := runner.Run(ctx, "init")
	if err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	return dir
}

// fakeGitBin creates a temporary directory containing a shell script named
// "git" that prints the subprocess environment to stdout and exits 0. This
// allows tests to inspect the environment variables passed to git subprocesses
// without relying on `git env` (which is not a valid git subcommand).
//
// Returns the directory path containing the fake binary.
func fakeGitBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "git")
	// The script ignores all arguments and simply dumps its environment.
	err := os.WriteFile(script, []byte("#!/bin/sh\nenv\n"), 0o755)
	if err != nil {
		t.Fatalf("failed to create fake git script: %v", err)
	}
	return dir
}

// envLines parses output from the fake git binary (or `env` command) into
// individual environment variable lines.
func envLines(stdout []byte) []string {
	return strings.Split(string(stdout), "\n")
}

// envContains checks whether the env output contains a specific KEY=VALUE entry.
func envContains(stdout []byte, entry string) bool {
	for _, line := range envLines(stdout) {
		if strings.TrimSpace(line) == entry {
			return true
		}
	}
	return false
}

// envCount returns the number of lines starting with the given prefix (e.g.
// "GIT_ALLOW_PROTOCOL=") in the environment output.
func envCount(stdout []byte, prefix string) int {
	count := 0
	for _, line := range envLines(stdout) {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			count++
		}
	}
	return count
}

// ---------------------------------------------------------------------------
// Task 1.1: NewRunner construction and immutability
// ---------------------------------------------------------------------------

// TS-10-1: NewRunner returns a non-nil *GitRunner with the specified workDir
// and no error, performing no validation of workDir existence at construction
// time.
// Requirement: 10-REQ-1.1
func TestNewRunner_ReturnsNonNil(t *testing.T) {
	runner := NewRunner("/tmp/some/nonexistent/path", "GIT_AUTHOR_NAME=Bot")
	if runner == nil {
		t.Error("NewRunner returned nil; expected non-nil *GitRunner")
	}
}

// TS-10-1 (edge case E2): NewRunner with an empty workDir string succeeds at
// construction time; no panic.
// Requirement: 10-REQ-1.E2
func TestNewRunner_EmptyWorkDir(t *testing.T) {
	runner := NewRunner("")
	if runner == nil {
		t.Error("NewRunner('') returned nil; expected non-nil *GitRunner")
	}
}

// TS-10-1 (edge case E3): NewRunner with no additional env strings still
// returns non-nil.
// Requirement: 10-REQ-1.E3
func TestNewRunner_NoExtraEnv(t *testing.T) {
	runner := NewRunner("/tmp/test")
	if runner == nil {
		t.Error("NewRunner with no extra env returned nil; expected non-nil *GitRunner")
	}
}

// TS-10-1: NewRunner accepts variadic env strings.
// Requirement: 10-REQ-1.1
func TestNewRunner_AcceptsVariadicEnv(t *testing.T) {
	runner := NewRunner("/tmp/test",
		"GIT_AUTHOR_NAME=Bot",
		"GIT_COMMITTER_NAME=Bot",
		"CUSTOM_VAR=value",
	)
	if runner == nil {
		t.Error("NewRunner with multiple env strings returned nil")
	}
}

// TS-10-4: Multiple goroutines can call Run concurrently on the same
// *GitRunner without data races (verified by go test -race).
// Requirement: 10-REQ-1.4
func TestNewRunner_ConcurrentRunSafety(t *testing.T) {
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	const goroutines = 10
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			_, _, _ = runner.Run(ctx, "version")
		}()
	}
	wg.Wait()
	// The test passes if go test -race reports no data races.
}

// TS-10-4 (extended): Multiple goroutines can call RunExitCode concurrently
// on the same *GitRunner without data races.
// Requirement: 10-REQ-1.4
func TestNewRunner_ConcurrentRunExitCodeSafety(t *testing.T) {
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	const goroutines = 10
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			_, _, _, _ = runner.RunExitCode(ctx, "version")
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Task 1.2: Safety environment defaults presence
// ---------------------------------------------------------------------------

// TS-10-3: Every git subprocess receives GIT_ALLOW_PROTOCOL='file:https:ssh',
// GIT_TERMINAL_PROMPT='0', and GIT_CONFIG_NOSYSTEM='1' as hardcoded entries,
// not overridable.
// Requirement: 10-REQ-1.3
//
// NOTE: This test uses a fake git binary that dumps env vars to stdout,
// because `git env` is not a valid git subcommand and could autocorrect
// destructively with help.autocorrect enabled. See reviewer finding.
func TestSafetyDefaults_PresentInSubprocess(t *testing.T) {
	fakeDir := fakeGitBin(t)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	workDir := t.TempDir()
	runner := NewRunner(workDir)
	if runner == nil {
		t.Fatal("NewRunner returned nil; expected non-nil *GitRunner")
	}

	ctx := context.Background()
	stdout, _, err := runner.Run(ctx, "version")
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}

	if !envContains(stdout, "GIT_ALLOW_PROTOCOL=file:https:ssh") {
		t.Error("subprocess env missing GIT_ALLOW_PROTOCOL=file:https:ssh")
	}
	if !envContains(stdout, "GIT_TERMINAL_PROMPT=0") {
		t.Error("subprocess env missing GIT_TERMINAL_PROMPT=0")
	}
	if !envContains(stdout, "GIT_CONFIG_NOSYSTEM=1") {
		t.Error("subprocess env missing GIT_CONFIG_NOSYSTEM=1")
	}
}

// TS-10-3 (alternate via buildEnv): Verify the environment slice built by the
// runner contains safety defaults when no extra env is provided.
// Requirement: 10-REQ-1.3
func TestSafetyDefaults_BuildEnv_NoExtraEnv(t *testing.T) {
	runner := NewRunner(t.TempDir())
	if runner == nil {
		t.Fatal("NewRunner returned nil; expected non-nil *GitRunner")
	}

	env := runner.buildEnv()
	if env == nil {
		t.Fatal("buildEnv returned nil; expected non-nil slice")
	}

	found := map[string]bool{
		"GIT_ALLOW_PROTOCOL=file:https:ssh": false,
		"GIT_TERMINAL_PROMPT=0":             false,
		"GIT_CONFIG_NOSYSTEM=1":             false,
	}
	for _, entry := range env {
		if _, ok := found[entry]; ok {
			found[entry] = true
		}
	}
	for entry, present := range found {
		if !present {
			t.Errorf("buildEnv missing safety default: %s", entry)
		}
	}
}

// TS-10-25: GIT_ALLOW_PROTOCOL is always set to 'file:https:ssh' on every
// invocation, never configurable.
// Requirement: 10-REQ-7.2
func TestSafetyDefaults_AllowProtocolHardcoded(t *testing.T) {
	fakeDir := fakeGitBin(t)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	// Without any extra env
	runner := NewRunner(t.TempDir())
	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}

	ctx := context.Background()
	stdout, _, err := runner.Run(ctx, "version")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !envContains(stdout, "GIT_ALLOW_PROTOCOL=file:https:ssh") {
		t.Error("GIT_ALLOW_PROTOCOL is not file:https:ssh with no extra env")
	}

	// With caller attempting to override
	runner2 := NewRunner(t.TempDir(), "GIT_ALLOW_PROTOCOL=ext::")
	if runner2 == nil {
		t.Fatal("NewRunner returned nil")
	}

	stdout2, _, err2 := runner2.Run(ctx, "version")
	if err2 != nil {
		t.Fatalf("Run error: %v", err2)
	}
	if !envContains(stdout2, "GIT_ALLOW_PROTOCOL=file:https:ssh") {
		t.Error("GIT_ALLOW_PROTOCOL should be file:https:ssh even when caller passes ext::")
	}
	if envContains(stdout2, "GIT_ALLOW_PROTOCOL=ext::") {
		t.Error("caller-supplied GIT_ALLOW_PROTOCOL=ext:: should have been stripped")
	}
}

// TS-10-E3: NewRunner with no additional env strings still applies all three
// safety defaults to every subprocess invocation (via buildEnv).
// Requirement: 10-REQ-1.E3
func TestSafetyDefaults_NoExtraEnv_StillApplied(t *testing.T) {
	fakeDir := fakeGitBin(t)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	runner := NewRunner(t.TempDir())
	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}

	ctx := context.Background()
	stdout, _, err := runner.Run(ctx, "version")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	for _, expected := range []string{
		"GIT_ALLOW_PROTOCOL=file:https:ssh",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	} {
		if !envContains(stdout, expected) {
			t.Errorf("subprocess env missing %s when no extra env provided", expected)
		}
	}
}

// TS-10-2: Environment concatenates os.Environ(), caller env, then safety
// defaults, with safety-default keys deduplicated.
// Requirement: 10-REQ-1.2
func TestSafetyDefaults_CallerEnvOverriddenByDefaults(t *testing.T) {
	fakeDir := fakeGitBin(t)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	runner := NewRunner(t.TempDir(), "GIT_TERMINAL_PROMPT=1")
	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}

	ctx := context.Background()
	stdout, _, err := runner.Run(ctx, "version")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// Safety default wins
	if !envContains(stdout, "GIT_TERMINAL_PROMPT=0") {
		t.Error("subprocess env should contain GIT_TERMINAL_PROMPT=0 (safety default)")
	}
	// Caller value stripped
	if envContains(stdout, "GIT_TERMINAL_PROMPT=1") {
		t.Error("subprocess env should NOT contain GIT_TERMINAL_PROMPT=1 (caller value should be stripped)")
	}
	// Other safety defaults also present
	if !envContains(stdout, "GIT_ALLOW_PROTOCOL=file:https:ssh") {
		t.Error("subprocess env missing GIT_ALLOW_PROTOCOL=file:https:ssh")
	}
	if !envContains(stdout, "GIT_CONFIG_NOSYSTEM=1") {
		t.Error("subprocess env missing GIT_CONFIG_NOSYSTEM=1")
	}
}

// ---------------------------------------------------------------------------
// Task 1.3: Safety default deduplication overriding caller-supplied values
// ---------------------------------------------------------------------------

// TS-10-24: The deduplication step removes all occurrences of safety-default
// keys from inherited os.Environ() and caller-supplied env entries before
// appending the hardcoded safety defaults as the final entries.
// Requirement: 10-REQ-7.1
func TestDeduplication_AllSafetyKeysOverridden(t *testing.T) {
	fakeDir := fakeGitBin(t)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	runner := NewRunner(t.TempDir(),
		"GIT_ALLOW_PROTOCOL=ext::",
		"GIT_TERMINAL_PROMPT=1",
		"GIT_CONFIG_NOSYSTEM=0",
	)
	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}

	ctx := context.Background()
	stdout, _, err := runner.Run(ctx, "version")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// Safety defaults must win
	if !envContains(stdout, "GIT_ALLOW_PROTOCOL=file:https:ssh") {
		t.Error("GIT_ALLOW_PROTOCOL should be file:https:ssh (safety default)")
	}
	if !envContains(stdout, "GIT_TERMINAL_PROMPT=0") {
		t.Error("GIT_TERMINAL_PROMPT should be 0 (safety default)")
	}
	if !envContains(stdout, "GIT_CONFIG_NOSYSTEM=1") {
		t.Error("GIT_CONFIG_NOSYSTEM should be 1 (safety default)")
	}

	// Each safety key must appear exactly once
	if c := envCount(stdout, "GIT_ALLOW_PROTOCOL="); c != 1 {
		t.Errorf("GIT_ALLOW_PROTOCOL appears %d times; want exactly 1", c)
	}
	if c := envCount(stdout, "GIT_TERMINAL_PROMPT="); c != 1 {
		t.Errorf("GIT_TERMINAL_PROMPT appears %d times; want exactly 1", c)
	}
	if c := envCount(stdout, "GIT_CONFIG_NOSYSTEM="); c != 1 {
		t.Errorf("GIT_CONFIG_NOSYSTEM appears %d times; want exactly 1", c)
	}
}

// TS-10-24 (via buildEnv): Verify deduplication directly on the env slice.
// Requirement: 10-REQ-7.1
func TestDeduplication_BuildEnv_AllSafetyKeysOverridden(t *testing.T) {
	runner := NewRunner(t.TempDir(),
		"GIT_ALLOW_PROTOCOL=ext::",
		"GIT_TERMINAL_PROMPT=1",
		"GIT_CONFIG_NOSYSTEM=0",
	)
	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}

	env := runner.buildEnv()
	if env == nil {
		t.Fatal("buildEnv returned nil")
	}

	// Count occurrences of each safety key
	counts := map[string]int{
		"GIT_ALLOW_PROTOCOL=": 0,
		"GIT_TERMINAL_PROMPT=": 0,
		"GIT_CONFIG_NOSYSTEM=": 0,
	}
	for _, entry := range env {
		for prefix := range counts {
			if strings.HasPrefix(entry, prefix) {
				counts[prefix]++
			}
		}
	}

	for prefix, count := range counts {
		if count != 1 {
			t.Errorf("buildEnv: %s appears %d times; want exactly 1", prefix, count)
		}
	}

	// Verify safety defaults are the correct values
	envStr := strings.Join(env, "\n")
	for _, expected := range []string{
		"GIT_ALLOW_PROTOCOL=file:https:ssh",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	} {
		if !strings.Contains(envStr, expected) {
			t.Errorf("buildEnv missing safety default: %s", expected)
		}
	}

	// Verify caller values are NOT present
	for _, forbidden := range []string{
		"GIT_ALLOW_PROTOCOL=ext::",
		"GIT_TERMINAL_PROMPT=1",
		"GIT_CONFIG_NOSYSTEM=0",
	} {
		if strings.Contains(envStr, forbidden) {
			t.Errorf("buildEnv should not contain caller-supplied value: %s", forbidden)
		}
	}
}

// Test that GIT_TERMINAL_PROMPT=1 (caller-supplied) is stripped in favour of
// safety default GIT_TERMINAL_PROMPT=0.
// Requirement: 10-REQ-1.E1, 10-REQ-7.1
func TestDeduplication_TerminalPromptOverride(t *testing.T) {
	fakeDir := fakeGitBin(t)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	runner := NewRunner(t.TempDir(), "GIT_TERMINAL_PROMPT=1")
	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}

	ctx := context.Background()
	stdout, _, err := runner.Run(ctx, "version")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	if !envContains(stdout, "GIT_TERMINAL_PROMPT=0") {
		t.Error("GIT_TERMINAL_PROMPT should be 0 (safety default)")
	}
	if envContains(stdout, "GIT_TERMINAL_PROMPT=1") {
		t.Error("caller-supplied GIT_TERMINAL_PROMPT=1 should have been stripped")
	}
}

// Test that GIT_ALLOW_PROTOCOL=ext:: (caller-supplied) is stripped in favour
// of safety default GIT_ALLOW_PROTOCOL=file:https:ssh.
// Requirement: 10-REQ-7.1, 10-REQ-7.2
func TestDeduplication_AllowProtocolOverride(t *testing.T) {
	fakeDir := fakeGitBin(t)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	runner := NewRunner(t.TempDir(), "GIT_ALLOW_PROTOCOL=ext::")
	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}

	ctx := context.Background()
	stdout, _, err := runner.Run(ctx, "version")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	if !envContains(stdout, "GIT_ALLOW_PROTOCOL=file:https:ssh") {
		t.Error("GIT_ALLOW_PROTOCOL should be file:https:ssh (safety default)")
	}
	if envContains(stdout, "GIT_ALLOW_PROTOCOL=ext::") {
		t.Error("caller-supplied GIT_ALLOW_PROTOCOL=ext:: should have been stripped")
	}
}

// Test that GIT_CONFIG_NOSYSTEM=0 (caller-supplied) is stripped in favour of
// safety default GIT_CONFIG_NOSYSTEM=1.
// Requirement: 10-REQ-7.1
func TestDeduplication_ConfigNoSystemOverride(t *testing.T) {
	fakeDir := fakeGitBin(t)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	runner := NewRunner(t.TempDir(), "GIT_CONFIG_NOSYSTEM=0")
	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}

	ctx := context.Background()
	stdout, _, err := runner.Run(ctx, "version")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	if !envContains(stdout, "GIT_CONFIG_NOSYSTEM=1") {
		t.Error("GIT_CONFIG_NOSYSTEM should be 1 (safety default)")
	}
	if envContains(stdout, "GIT_CONFIG_NOSYSTEM=0") {
		t.Error("caller-supplied GIT_CONFIG_NOSYSTEM=0 should have been stripped")
	}
}

// TS-10-E21: When inherited process environment contains GIT_ALLOW_PROTOCOL
// set to a dangerous value (e.g. 'ext::'), the deduplication step strips it.
// Requirement: 10-REQ-7.E1
func TestDeduplication_InheritedEnvStripped(t *testing.T) {
	fakeDir := fakeGitBin(t)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
	t.Setenv("GIT_ALLOW_PROTOCOL", "ext::")

	runner := NewRunner(t.TempDir())
	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}

	ctx := context.Background()
	stdout, _, err := runner.Run(ctx, "version")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	if !envContains(stdout, "GIT_ALLOW_PROTOCOL=file:https:ssh") {
		t.Error("GIT_ALLOW_PROTOCOL should be file:https:ssh despite inherited ext::")
	}
	if envContains(stdout, "GIT_ALLOW_PROTOCOL=ext::") {
		t.Error("inherited GIT_ALLOW_PROTOCOL=ext:: should have been stripped")
	}
}

// TS-10-E22: When inherited process environment contains multiple entries for
// the same safety-default key, all are removed before the single hardcoded
// safety default is appended.
// Requirement: 10-REQ-7.E2
func TestDeduplication_MultipleDuplicatesStripped(t *testing.T) {
	fakeDir := fakeGitBin(t)
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
	// Set inherited env (one occurrence from process env)
	t.Setenv("GIT_ALLOW_PROTOCOL", "https:")

	// Pass two more occurrences via caller env
	runner := NewRunner(t.TempDir(),
		"GIT_ALLOW_PROTOCOL=ext::",
		"GIT_ALLOW_PROTOCOL=file:",
	)
	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}

	ctx := context.Background()
	stdout, _, err := runner.Run(ctx, "version")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// Only one occurrence of GIT_ALLOW_PROTOCOL, with the safety value
	if c := envCount(stdout, "GIT_ALLOW_PROTOCOL="); c != 1 {
		t.Errorf("GIT_ALLOW_PROTOCOL appears %d times; want exactly 1", c)
	}
	if !envContains(stdout, "GIT_ALLOW_PROTOCOL=file:https:ssh") {
		t.Error("GIT_ALLOW_PROTOCOL should be file:https:ssh")
	}
}

// TS-10-2 (via buildEnv): Verify safety defaults are last entries in the
// environment slice.
// Requirement: 10-REQ-1.2
func TestSafetyDefaults_BuildEnv_AreLastEntries(t *testing.T) {
	runner := NewRunner(t.TempDir(), "GIT_AUTHOR_NAME=Bot")
	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}

	env := runner.buildEnv()
	if env == nil {
		t.Fatal("buildEnv returned nil")
	}
	if len(env) < 3 {
		t.Fatalf("buildEnv returned %d entries; want at least 3 (safety defaults)", len(env))
	}

	// The last three entries should be the safety defaults (in any order
	// among themselves, but all three must be in the final three positions).
	last3 := env[len(env)-3:]
	last3Set := make(map[string]bool)
	for _, entry := range last3 {
		last3Set[entry] = true
	}

	for _, expected := range safetyDefaults {
		if !last3Set[expected] {
			t.Errorf("safety default %q is not among the last 3 entries of buildEnv", expected)
		}
	}
}
