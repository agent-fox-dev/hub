// Package gitcmd provides a hardened wrapper around git CLI subprocess calls
// with safety defaults, uniform error handling, and typed convenience methods.
package gitcmd

import (
	"context"
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
