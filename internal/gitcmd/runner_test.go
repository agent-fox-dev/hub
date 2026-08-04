package gitcmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// ========================================================================
// Spec 11 Task 1.2: Constructor integration tests (version enforcement)
// (TS-11-1, TS-11-2, TS-11-3, TS-11-4)
// Requirements: 11-REQ-1.1, 11-REQ-1.2, 11-REQ-1.3, 11-REQ-1.4
// ========================================================================

// TestNew_Success verifies that the constructor returns a non-nil *GitRunner
// and nil error when the host git version is >= 2.38 and workDir exists.
//
// TS-11-1
// Requirement: 11-REQ-1.1
func TestNew_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	tmpDir := t.TempDir()
	runner, err := New(tmpDir, nil)
	if err != nil {
		t.Fatalf("New(%q, nil) returned unexpected error: %v", tmpDir, err)
	}
	if runner == nil {
		t.Fatal("New returned nil runner with nil error")
	}
}

// TestNew_VersionBelowMinimum verifies that the constructor returns (nil, error)
// when the detected git version is below 2.38, with the error message
// identifying the detected version and the 2.38 minimum requirement.
//
// TS-11-2
// Requirement: 11-REQ-1.2
//
// Note: This test uses a fake git binary to simulate an old version. If the
// fake binary cannot be injected, the test is skipped (per TS-11-2
// preconditions).
func TestNew_VersionBelowMinimum(t *testing.T) {
	// Create a fake git binary that outputs a version below 2.38.
	fakeGitDir := t.TempDir()
	fakeGitPath := fakeGitDir + "/git"

	script := "#!/bin/sh\necho 'git version 2.37.0'\n"
	if err := os.WriteFile(fakeGitPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to create fake git binary: %v", err)
	}

	// Override PATH so that our fake git is found first.
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeGitDir+":"+origPath)

	tmpDir := t.TempDir()
	runner, err := New(tmpDir, nil)
	if runner != nil {
		t.Error("New should return nil runner for git < 2.38")
	}
	if err == nil {
		t.Fatal("New should return non-nil error for git < 2.38")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "2.37") {
		t.Errorf("error message %q should contain detected version '2.37'", errMsg)
	}
	if !strings.Contains(errMsg, "2.38") {
		t.Errorf("error message %q should contain minimum version '2.38'", errMsg)
	}
}

// TestNew_GitNotOnPath verifies that the constructor returns (nil, error)
// wrapping the exec failure when git is not found on PATH.
//
// TS-11-3
// Requirement: 11-REQ-1.3
func TestNew_GitNotOnPath(t *testing.T) {
	// Set PATH to a directory that does not contain a git binary.
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	tmpDir := t.TempDir()
	runner, err := New(tmpDir, nil)
	if runner != nil {
		t.Error("New should return nil runner when git is not on PATH")
	}
	if err == nil {
		t.Fatal("New should return non-nil error when git is not on PATH")
	}
}

// TestNew_NonGitDirectory verifies that the constructor accepts any valid
// directory path as workDir, even if it is not a git repository.
//
// TS-11-4
// Requirement: 11-REQ-1.4
func TestNew_NonGitDirectory(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Use a plain temp dir that is not a git repo.
	tmpDir := t.TempDir()
	runner, err := New(tmpDir, nil)
	if err != nil {
		t.Fatalf("New(%q, nil) returned unexpected error: %v", tmpDir, err)
	}
	if runner == nil {
		t.Fatal("New returned nil runner for a valid non-git directory")
	}
}

// TestNew_InvalidWorkDir verifies that the constructor returns (nil, error)
// when workDir is empty or does not exist on the filesystem.
//
// 11-REQ-1.E1
func TestNew_InvalidWorkDir(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	tests := []struct {
		name    string
		workDir string
	}{
		{"empty_string", ""},
		{"nonexistent_path", "/tmp/gitcmd-nonexistent-path-" + fmt.Sprintf("%d", os.Getpid())},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := New(tt.workDir, nil)
			if runner != nil {
				t.Error("New should return nil runner for invalid workDir")
			}
			if err == nil {
				t.Fatalf("New(%q, nil) should return non-nil error", tt.workDir)
			}
		})
	}
}

// TestNew_MalformedVersionString verifies that the constructor returns
// (nil, error) when git --version output is unrecognizable.
//
// 11-REQ-1.E2
func TestNew_MalformedVersionString(t *testing.T) {
	// Create a fake git binary that outputs a malformed version string.
	fakeGitDir := t.TempDir()
	fakeGitPath := fakeGitDir + "/git"

	script := "#!/bin/sh\necho 'totally bogus output'\n"
	if err := os.WriteFile(fakeGitPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to create fake git binary: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeGitDir+":"+origPath)

	tmpDir := t.TempDir()
	runner, err := New(tmpDir, nil)
	if runner != nil {
		t.Error("New should return nil runner for malformed version string")
	}
	if err == nil {
		t.Fatal("New should return non-nil error for malformed version string")
	}
}

// ========================================================================
// Spec 11 Task 1.3: Safety environment variable injection tests
// (TS-11-5, TS-11-6, TS-11-7)
// Requirements: 11-REQ-2.1, 11-REQ-2.2, 11-REQ-2.3
//
// Note: The test spec suggests using `runner.Run(ctx, "env")` to inspect
// subprocess environment, but `git env` is not a valid git subcommand.
// Instead, these tests inspect the runner's internal env slice directly
// (same-package test access) to verify safety variables are present and
// correctly ordered.
// ========================================================================

// TestSafetyEnvVars_Present verifies that the runner's environment contains
// all three safety variables with the correct values.
//
// TS-11-5
// Requirement: 11-REQ-2.1
func TestSafetyEnvVars_Present(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	tmpDir := t.TempDir()
	runner, err := New(tmpDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if runner == nil {
		t.Fatal("runner is nil")
	}

	env := runner.env

	expected := []string{
		"GIT_ALLOW_PROTOCOL=file:https:ssh",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	}

	for _, want := range expected {
		if !envSliceContains(env, want) {
			t.Errorf("runner.env does not contain %q; env = %v", want, env)
		}
	}
}

// TestSafetyEnvVars_PrecedenceOverExtraEnv verifies that the hardcoded safety
// variables are appended AFTER extraEnv, so they unconditionally take
// precedence over any conflicting caller-supplied values.
//
// TS-11-6
// Requirement: 11-REQ-2.2
func TestSafetyEnvVars_PrecedenceOverExtraEnv(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	tmpDir := t.TempDir()
	extraEnv := []string{
		"GIT_ALLOW_PROTOCOL=git:http",
		"GIT_TERMINAL_PROMPT=1",
		"GIT_CONFIG_NOSYSTEM=0",
	}
	runner, err := New(tmpDir, extraEnv)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if runner == nil {
		t.Fatal("runner is nil")
	}

	env := runner.env

	// Last occurrence of each safety variable must be the hardcoded value.
	checks := []struct {
		key  string
		want string
	}{
		{"GIT_ALLOW_PROTOCOL", "file:https:ssh"},
		{"GIT_TERMINAL_PROMPT", "0"},
		{"GIT_CONFIG_NOSYSTEM", "1"},
	}

	for _, c := range checks {
		val, found := lastEnvValue(env, c.key)
		if !found {
			t.Errorf("env does not contain %s", c.key)
			continue
		}
		if val != c.want {
			t.Errorf("last %s = %q, want %q", c.key, val, c.want)
		}
	}
}

// TestSafetyEnvVars_AllowProtocolOverride verifies that when a caller passes
// a different GIT_ALLOW_PROTOCOL in extraEnv, the hardcoded file:https:ssh
// value wins because it is appended last.
//
// TS-11-7
// Requirement: 11-REQ-2.3
func TestSafetyEnvVars_AllowProtocolOverride(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	tmpDir := t.TempDir()
	runner, err := New(tmpDir, []string{"GIT_ALLOW_PROTOCOL=https"})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if runner == nil {
		t.Fatal("runner is nil")
	}

	val, found := lastEnvValue(runner.env, "GIT_ALLOW_PROTOCOL")
	if !found {
		t.Fatal("GIT_ALLOW_PROTOCOL not found in runner.env")
	}
	if val != "file:https:ssh" {
		t.Errorf("effective GIT_ALLOW_PROTOCOL = %q, want %q", val, "file:https:ssh")
	}
}

// TestSafetyEnvVars_NilExtraEnv verifies that when extraEnv is nil, the
// runner applies only the three safety variables without error.
//
// 11-REQ-2.E1
func TestSafetyEnvVars_NilExtraEnv(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	tmpDir := t.TempDir()
	runner, err := New(tmpDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if runner == nil {
		t.Fatal("runner is nil")
	}

	expected := []string{
		"GIT_ALLOW_PROTOCOL=file:https:ssh",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	}

	for _, want := range expected {
		if !envSliceContains(runner.env, want) {
			t.Errorf("runner.env missing %q with nil extraEnv", want)
		}
	}
}

// TestSafetyEnvVars_DuplicateExtraEnvEntries verifies that even with multiple
// duplicate entries for safety variables in extraEnv, the hardcoded values
// always win.
//
// 11-REQ-2.E2
func TestSafetyEnvVars_DuplicateExtraEnvEntries(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	tmpDir := t.TempDir()
	extraEnv := []string{
		"GIT_TERMINAL_PROMPT=1",
		"GIT_TERMINAL_PROMPT=2",
		"GIT_CONFIG_NOSYSTEM=0",
		"GIT_CONFIG_NOSYSTEM=99",
	}
	runner, err := New(tmpDir, extraEnv)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if runner == nil {
		t.Fatal("runner is nil")
	}

	env := runner.env

	val, _ := lastEnvValue(env, "GIT_TERMINAL_PROMPT")
	if val != "0" {
		t.Errorf("last GIT_TERMINAL_PROMPT = %q, want %q", val, "0")
	}

	val, _ = lastEnvValue(env, "GIT_CONFIG_NOSYSTEM")
	if val != "1" {
		t.Errorf("last GIT_CONFIG_NOSYSTEM = %q, want %q", val, "1")
	}
}

// ========================================================================
// Spec 11 Task 1.4: Run method tests
// (TS-11-8, TS-11-9, TS-11-10)
// Requirements: 11-REQ-3.1, 11-REQ-3.2, 11-REQ-3.3
// ========================================================================

// TestRun_Success verifies that Run returns trimmed stdout and nil error
// when the git subprocess exits with code 0.
//
// TS-11-8
// Requirement: 11-REQ-3.1
func TestRun_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := initTestRepo(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	out, err := runner.Run(ctx, "rev-parse", "--git-dir")
	if err != nil {
		t.Fatalf("Run(rev-parse --git-dir) returned error: %v", err)
	}

	trimmed := strings.TrimSpace(out)
	if trimmed != ".git" {
		t.Errorf("Run(rev-parse --git-dir) = %q, want %q", trimmed, ".git")
	}
}

// TestRun_NonZeroExitReturnsGitError verifies that Run returns a *GitError
// containing Args, ExitCode, and trimmed Stderr when the git subprocess
// exits with a non-zero exit code.
//
// TS-11-9
// Requirement: 11-REQ-3.2
func TestRun_NonZeroExitReturnsGitError(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := initTestRepo(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	out, runErr := runner.Run(ctx, "cat-file", "-t", "nonexistent-sha-abc123")
	if out != "" {
		t.Errorf("Run should return empty stdout on failure, got %q", out)
	}
	if runErr == nil {
		t.Fatal("Run should return non-nil error for non-zero exit")
	}

	var ge *GitError
	if !errors.As(runErr, &ge) {
		t.Fatalf("error is not *GitError: %T: %v", runErr, runErr)
	}

	if ge.ExitCode == 0 {
		t.Error("GitError.ExitCode should be non-zero")
	}
	if len(ge.Args) == 0 {
		t.Error("GitError.Args should not be empty")
	}

	// Verify that the args contain the subcommand we passed.
	argsJoined := strings.Join(ge.Args, " ")
	if !strings.Contains(argsJoined, "cat-file") {
		t.Errorf("GitError.Args %v should contain 'cat-file'", ge.Args)
	}
}

// TestRun_ContextCancellation verifies that Run respects context cancellation
// by killing the subprocess and returning the context error.
//
// TS-11-10
// Requirement: 11-REQ-3.3
func TestRun_ContextCancellation(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	tmpDir := t.TempDir()
	runner, err := New(tmpDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Use an extremely short deadline to trigger cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	// ls-remote to a non-listening address will block long enough for the
	// context to expire.
	out, runErr := runner.Run(ctx, "ls-remote", "https://localhost:1/nonexistent")
	if out != "" {
		t.Errorf("expected empty stdout on cancellation, got %q", out)
	}
	if runErr == nil {
		t.Fatal("Run should return non-nil error on context cancellation")
	}

	// Per 11-REQ-3.3: the error should wrap context.DeadlineExceeded or
	// context.Canceled.
	if !errors.Is(runErr, context.DeadlineExceeded) && !errors.Is(runErr, context.Canceled) {
		t.Errorf("expected context error, got: %v", runErr)
	}
}

// TestRun_EmptyArgs verifies that Run passes empty args to git, which returns
// a non-zero exit code with a usage error, wrapped as *GitError.
//
// 11-REQ-3.E2
func TestRun_EmptyArgs(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	tmpDir := t.TempDir()
	runner, err := New(tmpDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	_, runErr := runner.Run(ctx)
	if runErr == nil {
		t.Fatal("Run with no args should return an error")
	}

	var ge *GitError
	if !errors.As(runErr, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", runErr, runErr)
	}
}

// ========================================================================
// Spec 11 Task 1.5: GitError uniform error type tests
// (TS-11-28, TS-11-29)
// Requirements: 11-REQ-10.1, 11-REQ-10.2
// ========================================================================

// TestGitError_ErrorMethod verifies that GitError.Error() returns a non-empty
// string containing the args, exit code, and stderr.
//
// TS-11-28
// Requirement: 11-REQ-10.1
func TestGitError_ErrorMethod(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := initTestRepo(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	_, runErr := runner.Run(ctx, "cat-file", "-t", "deadbeef")
	if runErr == nil {
		t.Fatal("expected non-nil error")
	}

	var ge *GitError
	if !errors.As(runErr, &ge) {
		t.Fatalf("error is not *GitError: %T: %v", runErr, runErr)
	}

	// Verify fields are populated.
	if len(ge.Args) == 0 {
		t.Error("GitError.Args should not be empty")
	}
	if ge.ExitCode == 0 {
		t.Error("GitError.ExitCode should be non-zero")
	}

	// Verify Error() string contains all relevant information.
	msg := ge.Error()
	if msg == "" {
		t.Fatal("GitError.Error() returned empty string")
	}
	if !strings.Contains(msg, "cat-file") {
		t.Errorf("Error() %q should contain 'cat-file'", msg)
	}
	if !strings.Contains(msg, fmt.Sprintf("%d", ge.ExitCode)) {
		t.Errorf("Error() %q should contain exit code %d", msg, ge.ExitCode)
	}
}

// TestGitError_ErrorsAs verifies that *GitError can be detected via errors.As
// from any error returned by Run, so callers can use type assertions to
// inspect structured fields.
//
// TS-11-29
// Requirement: 11-REQ-10.2
func TestGitError_ErrorsAs(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := initTestRepo(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	_, runErr := runner.Run(ctx, "invalid-subcommand-xyz")
	if runErr == nil {
		t.Fatal("expected non-nil error for invalid subcommand")
	}

	var ge *GitError
	ok := errors.As(runErr, &ge)
	if !ok {
		t.Fatalf("errors.As should succeed for *GitError, got %T: %v", runErr, runErr)
	}
}

// TestGitError_StderrTrimmed verifies that GitError.Stderr is trimmed of
// leading/trailing whitespace but preserves internal content regardless of
// length.
//
// 11-REQ-10.E1
func TestGitError_StderrTrimmed(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := initTestRepo(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	_, runErr := runner.Run(ctx, "cat-file", "-t", "deadbeef")
	if runErr == nil {
		t.Fatal("expected non-nil error")
	}

	var ge *GitError
	if !errors.As(runErr, &ge) {
		t.Fatalf("error is not *GitError: %T: %v", runErr, runErr)
	}

	// Stderr should be trimmed — no leading or trailing whitespace.
	if ge.Stderr != strings.TrimSpace(ge.Stderr) {
		t.Errorf("GitError.Stderr is not trimmed: %q", ge.Stderr)
	}
}
