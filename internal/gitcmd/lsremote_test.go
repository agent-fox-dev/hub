package gitcmd

import (
	"context"
	"errors"
	"testing"
)

// ========================================================================
// Spec 11 Task 2.1: LsRemote three-way exit code discrimination tests
// (TS-11-11, TS-11-12, TS-11-13, TS-11-14)
// Requirements: 11-REQ-4.1, 11-REQ-4.2, 11-REQ-4.3, 11-REQ-4.4
// ========================================================================

// TestLsRemote_RefExists verifies that LsRemote returns trimmed stdout
// (containing the SHA and ref name) and nil error when git exits with
// code 0, indicating the queried ref exists on the remote.
//
// TS-11-11
// Requirement: 11-REQ-4.1
func TestLsRemote_RefExists(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	bareRepo := initBareTestRepo(t)
	tmpDir := t.TempDir()
	runner, err := New(tmpDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	remote := "file://" + bareRepo
	out, err := runner.LsRemote(ctx, remote, "refs/heads/main")
	if err != nil {
		t.Fatalf("LsRemote returned unexpected error: %v", err)
	}
	if out == "" {
		t.Fatal("LsRemote returned empty stdout for existing ref")
	}
	if !containsSubstring(out, "refs/heads/main") {
		t.Errorf("LsRemote stdout %q should contain 'refs/heads/main'", out)
	}
}

// TestLsRemote_RefNotFound verifies that LsRemote returns the
// ErrRefNotFound sentinel error when git exits with code 2,
// indicating the queried ref does not exist on the remote.
//
// TS-11-12
// Requirement: 11-REQ-4.2
func TestLsRemote_RefNotFound(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	bareRepo := initBareTestRepo(t)
	tmpDir := t.TempDir()
	runner, err := New(tmpDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	remote := "file://" + bareRepo
	out, err := runner.LsRemote(ctx, remote, "refs/heads/nonexistent-branch-xyz")
	if out != "" {
		t.Errorf("LsRemote should return empty stdout for missing ref, got %q", out)
	}
	if !errors.Is(err, ErrRefNotFound) {
		t.Fatalf("LsRemote error should be ErrRefNotFound, got: %v", err)
	}
}

// TestLsRemote_ErrRefNotFoundIsDistinct verifies that errors.Is correctly
// identifies ErrRefNotFound and does NOT match a generic *GitError, ensuring
// callers can reliably distinguish missing refs from network failures.
//
// TS-11-14 (errors.Is verification for exit-2 case)
// Requirement: 11-REQ-4.2
func TestLsRemote_ErrRefNotFoundIsDistinct(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	bareRepo := initBareTestRepo(t)
	tmpDir := t.TempDir()
	runner, err := New(tmpDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	remote := "file://" + bareRepo

	// Exit-2 case: ref not found.
	_, err = runner.LsRemote(ctx, remote, "refs/heads/nonexistent-branch-xyz")
	if !errors.Is(err, ErrRefNotFound) {
		t.Fatalf("exit-2 error should satisfy errors.Is(err, ErrRefNotFound), got: %v", err)
	}
	// ErrRefNotFound should NOT be extractable as *GitError — it's a distinct
	// sentinel for the missing-ref case.
	var ge *GitError
	if errors.As(err, &ge) {
		t.Errorf("ErrRefNotFound should not be unwrappable as *GitError, but got: %+v", ge)
	}
}

// TestLsRemote_NetworkFailure verifies that LsRemote returns a *GitError
// with ExitCode=1 and non-empty Stderr when git exits with code 1
// due to a network or authentication failure.
//
// Connection to https://localhost:1/nonexistent is used because port 1 (TCP
// Echo) is a privileged port that reliably refuses connections, causing git
// to exit with code 128 (which may be mapped to exit code 1 depending on
// git version). The key assertion is that the error is a *GitError (not
// ErrRefNotFound), distinguishing network failures from missing refs.
//
// TS-11-13
// Requirement: 11-REQ-4.3
// Edge case: 11-REQ-4.E1
func TestLsRemote_NetworkFailure(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	tmpDir := t.TempDir()
	runner, err := New(tmpDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	// localhost:1 is a privileged port that reliably refuses connections,
	// simulating a network/auth failure without requiring external resources.
	out, err := runner.LsRemote(ctx, "https://localhost:1/nonexistent", "refs/heads/main")
	if out != "" {
		t.Errorf("LsRemote should return empty stdout on network failure, got %q", out)
	}
	if err == nil {
		t.Fatal("LsRemote should return non-nil error on network failure")
	}

	// The error must NOT be ErrRefNotFound — this is a network failure, not
	// a missing ref.
	if errors.Is(err, ErrRefNotFound) {
		t.Fatal("network failure should not be ErrRefNotFound")
	}

	// The error should be a *GitError with a non-zero exit code and stderr.
	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("network failure error should be *GitError, got %T: %v", err, err)
	}
	if ge.ExitCode == 0 {
		t.Error("GitError.ExitCode should be non-zero for network failure")
	}
	if ge.Stderr == "" {
		t.Error("GitError.Stderr should be non-empty for network failure")
	}
}

// TestLsRemote_CommandArgsFormat verifies that LsRemote invokes git as
// "git ls-remote --exit-code <remote> <ref>" by inspecting GitError.Args
// on a known-failure path.
//
// TS-11-14
// Requirement: 11-REQ-4.4
func TestLsRemote_CommandArgsFormat(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	tmpDir := t.TempDir()
	runner, err := New(tmpDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	// Use an unreachable remote to trigger a *GitError so we can inspect Args.
	_, err = runner.LsRemote(ctx, "https://localhost:1/nonexistent", "refs/heads/main")
	if err == nil {
		t.Fatal("expected non-nil error for unreachable remote")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}

	// Verify the command used ls-remote --exit-code.
	if len(ge.Args) < 2 {
		t.Fatalf("GitError.Args has %d elements, want >= 2: %v", len(ge.Args), ge.Args)
	}
	if ge.Args[0] != "ls-remote" {
		t.Errorf("GitError.Args[0] = %q, want %q", ge.Args[0], "ls-remote")
	}
	if ge.Args[1] != "--exit-code" {
		t.Errorf("GitError.Args[1] = %q, want %q", ge.Args[1], "--exit-code")
	}
}
