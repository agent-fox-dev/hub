// Package gitcmd provides a hardened wrapper around git CLI subprocess calls
// with safety defaults, uniform error handling, and typed convenience methods.
package gitcmd

import (
	"context"
	"errors"
	"fmt"
)

// GitError is a structured error type returned by GitRunner on command failure.
// It contains the full argument list, exit code, and trimmed stderr.
type GitError struct {
	// Args is the full command-line arguments of the failed git invocation.
	Args []string
	// ExitCode is the integer exit code returned by the failed git subprocess.
	ExitCode int
	// Stderr is the trimmed standard error output of the failed git subprocess.
	Stderr string
}

// Error implements the error interface.
func (e *GitError) Error() string {
	// TODO: implement — format Args, ExitCode, and Stderr into a human-readable message.
	return ""
}

// GitRunner wraps git CLI subprocess calls with safety defaults and uniform
// error handling. Use New to construct an instance.
type GitRunner struct {
	workDir string
	env     []string
}

// New constructs a GitRunner that validates the working directory, checks that
// the host git version is >= 2.38, and returns a fully initialized *GitRunner.
func New(_ string, _ []string) (*GitRunner, error) {
	return nil, fmt.Errorf("not implemented")
}

// Run executes an arbitrary git subcommand with the given args. It returns the
// trimmed stdout on success, or a *GitError on non-zero exit.
func (r *GitRunner) Run(_ context.Context, _ ...string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

// ErrRefNotFound is a sentinel error returned by LsRemote when git exits with
// code 2, indicating the queried ref does not exist on the remote.
var ErrRefNotFound = errors.New("ref not found on remote")

// MergeConflictError is returned by MergeTree when the dry-run merge detects
// conflicts. ConflictingFiles lists the file paths that have merge conflicts.
type MergeConflictError struct {
	ConflictingFiles []string
}

// Error implements the error interface.
func (e *MergeConflictError) Error() string {
	// TODO: implement — format conflicting files into a human-readable message.
	return ""
}

// RebaseConflictError is returned by Rebase when the rebase encounters
// conflicts. ConflictingFiles lists the file paths that have merge conflicts.
type RebaseConflictError struct {
	ConflictingFiles []string
}

// Error implements the error interface.
func (e *RebaseConflictError) Error() string {
	// TODO: implement — format conflicting files into a human-readable message.
	return ""
}

// LsRemote executes git ls-remote --exit-code with three-way exit code
// discrimination: exit 0 returns (stdout, nil), exit 2 returns
// ("", ErrRefNotFound), exit 1 returns ("", *GitError).
func (r *GitRunner) LsRemote(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

// MergeTree executes git merge-tree --write-tree for read-only conflict
// detection. Returns the tree SHA on a clean merge, or *MergeConflictError
// when conflicts are detected.
func (r *GitRunner) MergeTree(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

// Rebase executes git rebase <onto> with automatic abort on conflict.
// Returns the new HEAD SHA on success or *RebaseConflictError on conflict.
func (r *GitRunner) Rebase(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

// RebaseAbort executes git rebase --abort to clean up a failed rebase state.
func (r *GitRunner) RebaseAbort(_ context.Context) error {
	return fmt.Errorf("not implemented")
}

// RevParse executes git rev-parse to resolve a ref to its full SHA.
func (r *GitRunner) RevParse(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

// UpdateRef executes git update-ref to update a reference to point to a SHA.
func (r *GitRunner) UpdateRef(_ context.Context, _, _ string) error {
	return fmt.Errorf("not implemented")
}
