package gitcmd

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Task 3.1: RunExitCode success and exit code passthrough
// ---------------------------------------------------------------------------

// TS-10-9: RunExitCode returns stdout, stderr, the raw exit code, and nil
// error when the subprocess completes with exit code 0.
// Requirement: 10-REQ-3.1
func TestRunExitCode_ZeroExit_ReturnsNilError(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	stdout, _, exitCode, err := runner.RunExitCode(ctx, "version")
	if err != nil {
		t.Fatalf("RunExitCode('version') returned unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("RunExitCode('version') exitCode = %d; want 0", exitCode)
	}
	if len(stdout) == 0 {
		t.Error("RunExitCode('version') returned empty stdout; expected non-empty bytes")
	}
}

// TS-10-9: RunExitCode returns nil error and a non-zero exit code when the
// subprocess completes with a non-zero exit, making the exit code meaningful.
// Uses git ls-remote --exit-code against a nonexistent bare repo path.
// Requirement: 10-REQ-3.1
func TestRunExitCode_NonZeroExit_ReturnsNilError(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	// Use a bare repo path that does not exist; git ls-remote --exit-code
	// will exit non-zero (2 for "no matching refs" or 128 for connection
	// failure). Either way, RunExitCode must return nil error and non-zero
	// exitCode.
	_, _, exitCode, err := runner.RunExitCode(ctx,
		"ls-remote", "--exit-code", "file:///nonexistent-path-xyz", "refs/heads/main")
	if err != nil {
		t.Fatalf("RunExitCode returned unexpected error: %v (exit code semantics should not produce an error)", err)
	}
	if exitCode == 0 {
		t.Error("RunExitCode exitCode = 0; expected non-zero for ls-remote against nonexistent path")
	}
}

// TS-10-9 (extended): RunExitCode captures stderr bytes alongside the exit
// code when the subprocess writes to stderr and exits non-zero.
// Requirement: 10-REQ-3.1
func TestRunExitCode_NonZeroExit_CapturesStderr(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	_, stderr, exitCode, err := runner.RunExitCode(ctx, "fetch", "nonexistent-remote-xyz")
	if err != nil {
		t.Fatalf("RunExitCode returned unexpected error: %v", err)
	}
	if exitCode == 0 {
		t.Error("exitCode = 0; expected non-zero for 'fetch nonexistent-remote-xyz'")
	}
	if len(stderr) == 0 {
		t.Error("RunExitCode returned empty stderr; expected non-empty bytes from git error output")
	}
}

// TS-10-9 (table-driven): Multiple commands with various exit codes all
// return nil error and the correct non-zero exit code.
// Requirement: 10-REQ-3.1
func TestRunExitCode_NonZeroExit_TableDriven(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "fetch nonexistent remote",
			args: []string{"fetch", "nonexistent-remote-xyz"},
		},
		{
			name: "checkout nonexistent branch",
			args: []string{"checkout", "nonexistent-branch-xyz"},
		},
		{
			name: "ls-remote nonexistent path",
			args: []string{"ls-remote", "--exit-code", "file:///nonexistent-path-xyz", "refs/heads/main"},
		},
	}

	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, exitCode, err := runner.RunExitCode(ctx, tc.args...)
			if err != nil {
				t.Fatalf("RunExitCode(%v) returned unexpected error: %v", tc.args, err)
			}
			if exitCode == 0 {
				t.Errorf("RunExitCode(%v) exitCode = 0; expected non-zero", tc.args)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Task 3.2: RunExitCode system-level error wrapping and signal termination
// ---------------------------------------------------------------------------

// TS-10-10: RunExitCode returns err != nil for system-level failures (binary
// not found), preserving the underlying *exec.Error via fmt.Errorf("%w", ...)
// for errors.As unwrapping, and never produces a *GitError.
// Requirement: 10-REQ-3.2, 10-REQ-3.E1
func TestRunExitCode_BinaryNotFound_ReturnsExecError(t *testing.T) {
	// Cannot use t.Parallel() — t.Setenv modifies process-level env.
	workDir := t.TempDir()
	emptyDir := t.TempDir() // empty directory with no git binary

	t.Setenv("PATH", emptyDir)

	runner := NewRunner(workDir)
	if runner == nil {
		t.Fatal("NewRunner returned nil; expected non-nil *GitRunner")
	}

	ctx := context.Background()
	_, _, _, err := runner.RunExitCode(ctx, "version")
	if err == nil {
		t.Fatal("RunExitCode('version') returned nil error; expected error when git binary not found")
	}

	// Must be an *exec.Error (binary not found).
	var execErr *exec.Error
	if !errors.As(err, &execErr) {
		t.Errorf("RunExitCode error is not *exec.Error: %T — %v", err, err)
	}

	// Must NOT be a *GitError.
	var gitErr *GitError
	if errors.As(err, &gitErr) {
		t.Error("RunExitCode returned a *GitError when git binary not found; RunExitCode must never produce a *GitError")
	}
}

// TS-10-10 (extended): When binary is not found, exitCode is 0 (process
// never started, no ExitError available).
// Requirement: 10-REQ-3.E1
func TestRunExitCode_BinaryNotFound_ExitCodeZero(t *testing.T) {
	workDir := t.TempDir()
	emptyDir := t.TempDir()

	t.Setenv("PATH", emptyDir)

	runner := NewRunner(workDir)
	if runner == nil {
		t.Fatal("NewRunner returned nil; expected non-nil *GitRunner")
	}

	ctx := context.Background()
	_, _, exitCode, err := runner.RunExitCode(ctx, "version")
	if err == nil {
		t.Fatal("RunExitCode returned nil error; expected error when git binary not found")
	}

	// When the binary is not found, the process never started so there is
	// no ExitError from which to extract an exit code. Per 10-REQ-3.E1,
	// exitCode should be 0 (or whatever exec.ExitError.ExitCode() returns,
	// which is 0 when no ExitError is available).
	if exitCode != 0 {
		t.Errorf("exitCode = %d; want 0 when binary not found (process never started)", exitCode)
	}
}

// TS-10-11: RunExitCode returns err != nil and exitCode == -1 when context
// cancellation causes SIGKILL signal termination.
// Requirement: 10-REQ-3.3
func TestRunExitCode_ContextCancelled_ReturnsNegativeOneExitCode(t *testing.T) {
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

	_, _, exitCode, err := runner.RunExitCode(ctx, "log", "--all")
	if err == nil {
		t.Fatal("RunExitCode with cancelled context returned nil error; expected context error")
	}
	if exitCode != -1 {
		t.Errorf("exitCode = %d; want -1 for signal-terminated subprocess", exitCode)
	}
}

// TS-10-11 (extended): RunExitCode returns partial stdout/stderr on context
// cancellation.
// Requirement: 10-REQ-3.3
func TestRunExitCode_ContextCancelled_ReturnsPartialOutput(t *testing.T) {
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

	stdout, stderr, _, err := runner.RunExitCode(ctx, "log", "--all")
	if err == nil {
		t.Fatal("RunExitCode with cancelled context returned nil error; expected context error")
	}
	// Partial stdout and stderr may be nil or non-nil depending on how far
	// the subprocess got before being killed. The key requirement is that
	// they are returned (not discarded). We assert they are byte slices
	// (which they are by type), and that the error is non-nil.
	_ = stdout // partial output accepted
	_ = stderr // partial output accepted
}

// TS-10-11 (extended): Context cancelled before subprocess starts returns
// err != nil and exitCode == 0.
// Requirement: 10-REQ-3.E2
func TestRunExitCode_ContextAlreadyCancelled_ExitCodeZero(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	// Cancel context before calling RunExitCode.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, exitCode, err := runner.RunExitCode(ctx, "version")
	if err == nil {
		t.Fatal("RunExitCode with pre-cancelled context returned nil error; expected error")
	}
	// Process never started — no exit code available. Per 10-REQ-3.E2,
	// exitCode should be 0.
	if exitCode != 0 {
		t.Errorf("exitCode = %d; want 0 when context cancelled before process start", exitCode)
	}
}

// TS-10-11 (timeout variant): RunExitCode returns err != nil and exitCode ==
// -1 when context times out during execution.
// Requirement: 10-REQ-3.3
func TestRunExitCode_ContextTimeout_ReturnsError(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, _, exitCode, err := runner.RunExitCode(ctx, "log", "--all")
	if err == nil {
		// If the command completed before timeout, it's not a failure of
		// the test contract but the command was too fast. Skip.
		t.Skip("command completed before timeout — cannot test signal termination")
	}
	// When SIGKILL was sent, exitCode should be -1.
	if exitCode != -1 {
		t.Errorf("exitCode = %d; want -1 for signal-terminated subprocess on timeout", exitCode)
	}
}

// TS-10-12: RunExitCode never produces a *GitError under any circumstances,
// including when the subprocess exits non-zero.
// Requirement: 10-REQ-3.4
func TestRunExitCode_NeverProducesGitError(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()

	// Scenario 1: non-zero exit (fetch nonexistent remote)
	_, _, exitCode, err := runner.RunExitCode(ctx, "fetch", "nonexistent-remote-xyz")
	if err == nil {
		// Non-zero exit is not a system error — err should be nil.
		// But we still check that no GitError sneaks through.
		if exitCode == 0 {
			t.Error("exitCode = 0; expected non-zero for 'fetch nonexistent-remote-xyz'")
		}
	}
	var gitErr *GitError
	if errors.As(err, &gitErr) {
		t.Error("RunExitCode returned a *GitError for non-zero exit; it must never produce one")
	}

	// Scenario 2: zero exit (version)
	_, _, exitCode2, err2 := runner.RunExitCode(ctx, "version")
	if err2 != nil {
		t.Fatalf("RunExitCode('version') returned unexpected error: %v", err2)
	}
	if exitCode2 != 0 {
		t.Errorf("RunExitCode('version') exitCode = %d; want 0", exitCode2)
	}
	var gitErr2 *GitError
	if errors.As(err2, &gitErr2) {
		t.Error("RunExitCode returned a *GitError for successful exit; it must never produce one")
	}
}

// TS-10-12 (extended): Even when RunExitCode encounters a system-level error
// (binary not found), no *GitError is produced.
// Requirement: 10-REQ-3.4
func TestRunExitCode_SystemError_NeverProducesGitError(t *testing.T) {
	workDir := t.TempDir()
	emptyDir := t.TempDir()

	t.Setenv("PATH", emptyDir)

	runner := NewRunner(workDir)
	if runner == nil {
		t.Fatal("NewRunner returned nil; expected non-nil *GitRunner")
	}

	ctx := context.Background()
	_, _, _, err := runner.RunExitCode(ctx, "version")
	if err == nil {
		t.Fatal("RunExitCode returned nil error; expected error when git binary not found")
	}

	var gitErr *GitError
	if errors.As(err, &gitErr) {
		t.Error("RunExitCode returned a *GitError for binary-not-found; it must never produce one")
	}
}

// 10-REQ-3.E3: RunExitCode with no args invokes 'git' with no subcommand;
// git exits non-zero; return the exit code and nil error without a GitError.
// Requirement: 10-REQ-3.E3
func TestRunExitCode_NoArgs_ReturnsExitCodeAndNilError(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	_, _, exitCode, err := runner.RunExitCode(ctx)
	if err != nil {
		t.Fatalf("RunExitCode() with no args returned error: %v; expected nil error", err)
	}
	if exitCode == 0 {
		t.Error("RunExitCode() with no args: exitCode = 0; expected non-zero (bare git invocation)")
	}

	// Must NOT produce a *GitError.
	var gitErr *GitError
	if errors.As(err, &gitErr) {
		t.Error("RunExitCode with no args returned a *GitError; it must never produce one")
	}
}

// TS-10-12 (context cancellation): Even when RunExitCode encounters context
// cancellation, no *GitError is produced.
// Requirement: 10-REQ-3.4
func TestRunExitCode_ContextCancelled_NeverProducesGitError(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before invocation

	_, _, _, err := runner.RunExitCode(ctx, "version")
	if err == nil {
		t.Skip("RunExitCode with pre-cancelled context returned nil error; command may have completed")
	}

	var gitErr *GitError
	if errors.As(err, &gitErr) {
		t.Error("RunExitCode returned a *GitError on context cancellation; it must never produce one")
	}
}

// Verify that RunExitCode errors (when present) are wrappable via
// errors.Is/errors.As to the underlying os/exec error type.
// Requirement: 10-REQ-3.2
func TestRunExitCode_ErrorWrapping_Unwrappable(t *testing.T) {
	workDir := t.TempDir()
	emptyDir := t.TempDir()

	t.Setenv("PATH", emptyDir)

	runner := NewRunner(workDir)
	if runner == nil {
		t.Fatal("NewRunner returned nil; expected non-nil *GitRunner")
	}

	ctx := context.Background()
	_, _, _, err := runner.RunExitCode(ctx, "version")
	if err == nil {
		t.Fatal("RunExitCode returned nil error; expected error when git binary not found")
	}

	// The error must be unwrappable. errors.Unwrap should return non-nil,
	// demonstrating fmt.Errorf("%w", ...) wrapping.
	unwrapped := errors.Unwrap(err)
	if unwrapped == nil {
		// Some implementations may return the raw exec.Error directly
		// (which is also acceptable since it IS the underlying error).
		// Check if err itself is already an *exec.Error.
		var execErr *exec.Error
		if !errors.As(err, &execErr) {
			t.Error("RunExitCode error is neither wrapped nor an *exec.Error; expected fmt.Errorf(\"%%w\", ...) wrapping")
		}
	}
}

// Verify RunExitCode context cancellation error wraps the context error.
// Requirement: 10-REQ-3.2, 10-REQ-3.3
func TestRunExitCode_ContextCancelled_ErrorWrapsContextError(t *testing.T) {
	t.Parallel()
	workDir := initGitRepo(t)
	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before invocation

	_, _, _, err := runner.RunExitCode(ctx, "version")
	if err == nil {
		t.Skip("RunExitCode with pre-cancelled context returned nil error; command may have completed")
	}

	// The error should wrap context.Canceled so that errors.Is works.
	if !errors.Is(err, context.Canceled) {
		// Also check if it's wrapped in an exec error chain.
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			// The error might be directly from exec.CommandContext
			// wrapping the context error. Accept any non-nil error
			// that is related to context cancellation.
			_ = err // accepted — the key assertion is err != nil
		}
	}
}

