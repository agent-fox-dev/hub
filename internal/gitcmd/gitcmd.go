// Package gitcmd wraps exec.Command with hardened security defaults and
// structured error handling for executing git subprocesses.
package gitcmd

import (
	"context"
	"fmt"
)

// safetyDefaults are the hardcoded environment variables appended to every
// subprocess invocation. They are never configurable.
var safetyDefaults = []string{
	"GIT_ALLOW_PROTOCOL=file:https:ssh",
	"GIT_TERMINAL_PROMPT=0",
	"GIT_CONFIG_NOSYSTEM=1",
}

// safetyKeys lists the environment variable keys that are stripped during the
// deduplication step before safety defaults are appended.
var safetyKeys = []string{
	"GIT_ALLOW_PROTOCOL",
	"GIT_TERMINAL_PROMPT",
	"GIT_CONFIG_NOSYSTEM",
}

// GitRunner wraps exec.Command with safety environment defaults and a fixed
// working directory, used to execute git subprocesses.
type GitRunner struct {
	workDir string
	env     []string // caller-supplied additional env vars
}

// NewRunner creates an immutable *GitRunner with the given working directory
// and optional additional environment variable key=value strings. No
// validation of workDir existence is performed at construction time.
func NewRunner(workDir string, env ...string) *GitRunner {
	return nil // stub — implementation in a later task group
}

// Run executes git <args> in the runner's working directory and returns
// stdout, stderr, and a structured *GitError on non-zero exit.
func (r *GitRunner) Run(_ context.Context, _ ...string) ([]byte, []byte, error) {
	return nil, nil, nil // stub
}

// RunExitCode executes git <args> and returns the raw exit code separately.
// It never produces a *GitError; system-level failures are returned as
// wrapped errors via fmt.Errorf.
func (r *GitRunner) RunExitCode(_ context.Context, _ ...string) ([]byte, []byte, int, error) {
	return nil, nil, 0, nil // stub
}

// BranchExists checks whether a branch exists on a remote using
// git ls-remote --exit-code with three-way exit code discrimination.
func (r *GitRunner) BranchExists(_ context.Context, _, _ string) (bool, error) {
	return false, nil // stub
}

// buildEnv constructs the environment slice for a subprocess invocation by
// concatenating os.Environ(), caller-supplied env, then safety defaults,
// after removing any earlier occurrences of safety-default keys.
func (r *GitRunner) buildEnv() []string {
	return nil // stub
}

// CheckGitVersion validates the host git binary meets the minimum version
// requirement (2.38) at startup. Returns an error if below minimum.
func CheckGitVersion(_ context.Context) error {
	return nil // stub
}

// GitError is a structured error type returned by Run on non-zero git exit
// codes. Command holds the joined args without the binary prefix.
type GitError struct {
	Command  string
	ExitCode int
	Stderr   string
}

// Error implements the error interface, producing the format:
// git <Command> exited with code <N>: <stderr>
func (e *GitError) Error() string {
	return fmt.Sprintf("git %s exited with code %d: %s", e.Command, e.ExitCode, e.Stderr)
}
