package gitcmd

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Task 2.1: Run method success and stdout/stderr capture
// ---------------------------------------------------------------------------

// TS-10-5: Run returns stdout bytes, stderr bytes, and nil error when git
// exits with code 0.
// Requirement: 10-REQ-2.1
func TestRun_SuccessReturnsStdoutAndNilError(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	stdout, _, err := runner.Run(ctx, "version")
	if err != nil {
		t.Fatalf("Run('version') returned unexpected error: %v", err)
	}
	if len(stdout) == 0 {
		t.Error("Run('version') returned empty stdout; expected non-empty bytes")
	}
	if !strings.Contains(string(stdout), "git version") {
		t.Errorf("stdout does not contain 'git version': got %q", string(stdout))
	}
}

// TS-10-6: Run returns a *GitError with Command (joined args without 'git'
// prefix), ExitCode, and Stderr fields when git exits non-zero, and Error()
// produces the exact format string.
// Requirement: 10-REQ-2.2
func TestRun_NonZeroExitReturnsGitError(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	_, _, err := runner.Run(ctx, "fetch", "nonexistent-remote-xyz")
	if err == nil {
		t.Fatal("Run('fetch', 'nonexistent-remote-xyz') returned nil error; expected *GitError")
	}

	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("error is not *GitError: %T — %v", err, err)
	}

	if gitErr.Command != "fetch nonexistent-remote-xyz" {
		t.Errorf("GitError.Command = %q; want %q", gitErr.Command, "fetch nonexistent-remote-xyz")
	}
	if gitErr.ExitCode == 0 {
		t.Error("GitError.ExitCode is 0; expected non-zero")
	}
	if len(gitErr.Stderr) == 0 {
		t.Error("GitError.Stderr is empty; expected non-empty string from git")
	}

	// Verify Error() format: 'git <args> exited with code <N>: <stderr>'
	expected := fmt.Sprintf("git fetch nonexistent-remote-xyz exited with code %d: %s",
		gitErr.ExitCode, gitErr.Stderr)
	if gitErr.Error() != expected {
		t.Errorf("GitError.Error() = %q; want %q", gitErr.Error(), expected)
	}
}

// TS-10-6 (table-driven): Multiple git commands that exit non-zero produce
// *GitError with correct fields and Error() format.
// Requirement: 10-REQ-2.2
func TestRun_NonZeroExit_TableDriven(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	tests := []struct {
		name            string
		args            []string
		expectedCommand string
	}{
		{
			name:            "fetch nonexistent remote",
			args:            []string{"fetch", "nonexistent-remote-xyz"},
			expectedCommand: "fetch nonexistent-remote-xyz",
		},
		{
			name:            "checkout nonexistent branch",
			args:            []string{"checkout", "nonexistent-branch-xyz"},
			expectedCommand: "checkout nonexistent-branch-xyz",
		},
		{
			name:            "merge nonexistent ref",
			args:            []string{"merge", "nonexistent-ref-xyz"},
			expectedCommand: "merge nonexistent-ref-xyz",
		},
	}

	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runner.Run(ctx, tc.args...)
			if err == nil {
				t.Fatalf("Run(%v) returned nil error; expected *GitError", tc.args)
			}

			var gitErr *GitError
			if !errors.As(err, &gitErr) {
				t.Fatalf("error is not *GitError: %T — %v", err, err)
			}

			if gitErr.Command != tc.expectedCommand {
				t.Errorf("GitError.Command = %q; want %q", gitErr.Command, tc.expectedCommand)
			}
			if gitErr.ExitCode == 0 {
				t.Error("GitError.ExitCode is 0; expected non-zero")
			}
			if len(gitErr.Stderr) == 0 {
				t.Error("GitError.Stderr is empty; expected non-empty")
			}

			// Verify Error() format
			expected := fmt.Sprintf("git %s exited with code %d: %s",
				gitErr.Command, gitErr.ExitCode, gitErr.Stderr)
			if gitErr.Error() != expected {
				t.Errorf("GitError.Error() = %q; want %q", gitErr.Error(), expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Task 2.2: GitError structured error type
// ---------------------------------------------------------------------------

// TS-10-17: GitError.Error() returns a string in the exact format
// 'git <Command> exited with code <N>: <stderr>'.
// Requirement: 10-REQ-5.1
func TestGitError_ErrorFormat(t *testing.T) {
	t.Parallel()

	gitErr := &GitError{
		Command:  "rebase origin/main",
		ExitCode: 1,
		Stderr:   "error: could not apply abc1234...",
	}

	expected := "git rebase origin/main exited with code 1: error: could not apply abc1234..."
	if gitErr.Error() != expected {
		t.Errorf("GitError.Error() = %q; want %q", gitErr.Error(), expected)
	}
}

// 10-REQ-5.E2: GitError.Error() with empty Stderr produces the format
// 'git <Command> exited with code <N>: ' with an empty suffix after the
// colon.
func TestGitError_ErrorFormat_EmptyStderr(t *testing.T) {
	t.Parallel()

	gitErr := &GitError{
		Command:  "status",
		ExitCode: 128,
		Stderr:   "",
	}

	expected := "git status exited with code 128: "
	if gitErr.Error() != expected {
		t.Errorf("GitError.Error() = %q; want %q", gitErr.Error(), expected)
	}
}

// TS-10-18: GitError stores Command as joined args without the 'git' binary
// prefix, ExitCode as integer, and Stderr as captured stderr string.
// Requirement: 10-REQ-5.2
func TestGitError_FieldsFromRun(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	_, _, err := runner.Run(ctx, "fetch", "nonexistent-remote-xyz")
	if err == nil {
		t.Fatal("Run('fetch', 'nonexistent-remote-xyz') returned nil error; expected *GitError")
	}

	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("error is not *GitError: %T — %v", err, err)
	}

	// Command should NOT start with "git "
	if strings.HasPrefix(gitErr.Command, "git ") {
		t.Errorf("GitError.Command starts with 'git ': %q", gitErr.Command)
	}
	if gitErr.Command != "fetch nonexistent-remote-xyz" {
		t.Errorf("GitError.Command = %q; want %q", gitErr.Command, "fetch nonexistent-remote-xyz")
	}
	if gitErr.ExitCode == 0 {
		t.Error("GitError.ExitCode is 0; expected non-zero")
	}
	if len(gitErr.Stderr) == 0 {
		t.Error("GitError.Stderr is empty; expected non-empty string")
	}
}

// TS-10-19: GitError is produced exclusively by Run on non-zero exit;
// RunExitCode must never produce a GitError.
// Requirement: 10-REQ-5.3
func TestGitError_ExclusiveToRun_NotRunExitCode(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()

	// Run with a failing command -> should produce *GitError.
	_, _, runErr := runner.Run(ctx, "fetch", "nonexistent-remote-xyz")
	var gitErr *GitError
	if !errors.As(runErr, &gitErr) {
		t.Error("Run('fetch', 'nonexistent-remote-xyz') did not return a *GitError")
	}

	// RunExitCode with the same failing command -> must NOT produce *GitError.
	_, _, exitCode, runExitErr := runner.RunExitCode(ctx, "fetch", "nonexistent-remote-xyz")
	if runExitErr != nil {
		var gitErr2 *GitError
		if errors.As(runExitErr, &gitErr2) {
			t.Error("RunExitCode returned a *GitError; it must never produce one")
		}
	}
	if exitCode == 0 {
		t.Error("RunExitCode exit code is 0; expected non-zero for 'fetch nonexistent-remote-xyz'")
	}
}

// 10-REQ-5.E1: errors.As allows the caller to extract *GitError and access
// Command, ExitCode, and Stderr fields programmatically.
func TestGitError_ErrorsAs(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	_, _, err := runner.Run(ctx, "fetch", "nonexistent-remote-xyz")
	if err == nil {
		t.Fatal("expected non-nil error from Run")
	}

	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Fatal("errors.As(err, &gitErr) returned false; expected true")
	}

	// Verify programmatic field access works after extraction.
	if gitErr.Command == "" {
		t.Error("GitError.Command is empty after errors.As extraction")
	}
	if gitErr.ExitCode == 0 {
		t.Error("GitError.ExitCode is 0 after errors.As extraction")
	}
	if gitErr.Stderr == "" {
		t.Error("GitError.Stderr is empty after errors.As extraction")
	}
}

// ---------------------------------------------------------------------------
// Task 2.3: Run method binary-not-found and context cancellation
// ---------------------------------------------------------------------------

// TS-10-7: Run returns the raw *exec.Error (not a *GitError) when the git
// binary is not found on PATH.
// Requirement: 10-REQ-2.3
func TestRun_BinaryNotFound_ReturnsExecError(t *testing.T) {
	// Cannot use t.Parallel() — t.Setenv modifies process-level env.
	workDir := t.TempDir()
	emptyDir := t.TempDir() // empty directory with no git binary

	t.Setenv("PATH", emptyDir)

	runner := NewRunner(workDir)
	if runner == nil {
		t.Fatal("NewRunner returned nil; expected non-nil *GitRunner")
	}

	ctx := context.Background()
	_, _, err := runner.Run(ctx, "version")
	if err == nil {
		t.Fatal("Run('version') returned nil error; expected error when git binary not found")
	}

	// Must NOT be a *GitError.
	var gitErr *GitError
	if errors.As(err, &gitErr) {
		t.Error("Run returned a *GitError when git binary not found; expected raw *exec.Error")
	}

	// Must be an *exec.Error.
	var execErr *exec.Error
	if !errors.As(err, &execErr) {
		t.Errorf("Run did not return *exec.Error; got %T: %v", err, err)
	}
}

// TS-10-8: Run sends SIGKILL on context cancellation and returns partial
// stdout/stderr alongside the context error; both err != nil and
// len(stdout) > 0 may be true simultaneously.
// Requirement: 10-REQ-2.4
func TestRun_ContextCancellation_ReturnsError(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, _, err := runner.Run(ctx, "log", "--all")
	if err == nil {
		t.Error("Run with cancelled context returned nil error; expected context error")
	}
	// Note: both err != nil and len(stdout) >= 0 are valid simultaneously
	// per 10-REQ-2.E3 — we only assert that err is non-nil.
}

// 10-REQ-2.E1: Run with no args invokes 'git' with no subcommand; git prints
// a usage message to stderr and exits non-zero; return the resulting GitError
// without any additional programmer-mistake guard.
func TestRun_NoArgs_ReturnsGitError(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	_, _, err := runner.Run(ctx)
	if err == nil {
		t.Fatal("Run() with no args returned nil error; expected *GitError")
	}

	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("error is not *GitError: %T — %v", err, err)
	}

	// git with no subcommand should exit non-zero and print usage to stderr.
	if gitErr.ExitCode == 0 {
		t.Error("GitError.ExitCode is 0; expected non-zero for bare 'git' invocation")
	}
	if len(gitErr.Stderr) == 0 {
		t.Error("GitError.Stderr is empty; expected git usage message")
	}
}

// 10-REQ-2.E2: Run when workDir does not exist returns the raw os/exec
// working-directory error, not a *GitError.
func TestRun_WorkDirNotExists_ReturnsRawError(t *testing.T) {
	t.Parallel()
	runner := NewRunner("/nonexistent/path/that/does/not/exist")
	if runner == nil {
		t.Fatal("NewRunner returned nil; expected non-nil *GitRunner")
	}

	ctx := context.Background()
	_, _, err := runner.Run(ctx, "version")
	if err == nil {
		t.Fatal("Run with non-existent workDir returned nil error; expected raw os/exec error")
	}

	// Must NOT be a *GitError.
	var gitErr *GitError
	if errors.As(err, &gitErr) {
		t.Error("Run returned a *GitError for non-existent workDir; expected raw os/exec error")
	}
}
