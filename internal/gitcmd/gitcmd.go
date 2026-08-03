// Package gitcmd wraps exec.Command with hardened security defaults and
// structured error handling for executing git subprocesses within the af-hub
// platform. It provides the GitRunner struct, BranchExists method,
// CheckGitVersion function, and GitError type used by the merge queue,
// continuous rebase, and campaign operations.
package gitcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
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
// working directory, used to execute git subprocesses. It is safe for
// concurrent use by multiple goroutines after construction; all internal
// state is immutable.
type GitRunner struct {
	workDir string
	env     []string // caller-supplied additional env vars
}

// NewRunner creates an immutable *GitRunner with the given working directory
// and optional additional environment variable key=value strings. No
// validation of workDir existence is performed at construction time.
//
// Safety-default keys (GIT_ALLOW_PROTOCOL, GIT_TERMINAL_PROMPT,
// GIT_CONFIG_NOSYSTEM) passed in env are silently discarded during the
// deduplication step when building subprocess environments.
func NewRunner(workDir string, env ...string) *GitRunner {
	envCopy := make([]string, len(env))
	copy(envCopy, env)
	return &GitRunner{
		workDir: workDir,
		env:     envCopy,
	}
}

// buildEnv constructs the environment slice for a subprocess invocation by
// concatenating os.Environ(), caller-supplied env, then safety defaults,
// after removing ALL earlier occurrences of safety-default keys from both
// the inherited and caller-supplied entries. This guarantees the hardcoded
// safety values always win.
func (r *GitRunner) buildEnv() []string {
	inherited := os.Environ()

	// Pre-allocate: inherited + caller env + safety defaults
	combined := make([]string, 0, len(inherited)+len(r.env)+len(safetyDefaults))
	combined = append(combined, inherited...)
	combined = append(combined, r.env...)

	// Remove all occurrences of safety-default keys.
	result := make([]string, 0, len(combined)+len(safetyDefaults))
	for _, entry := range combined {
		isSafetyKey := false
		for _, key := range safetyKeys {
			if strings.HasPrefix(entry, key+"=") {
				isSafetyKey = true
				break
			}
		}
		if !isSafetyKey {
			result = append(result, entry)
		}
	}

	// Append safety defaults as the final entries.
	result = append(result, safetyDefaults...)
	return result
}

// Run executes git <args> in the runner's working directory and returns
// stdout, stderr, and a structured *GitError on non-zero exit. System-level
// failures (binary not found, context cancelled, signal termination) are
// returned as raw errors, never wrapped in *GitError.
func (r *GitRunner) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.workDir
	cmd.Env = r.buildEnv()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
			// Normal non-zero exit from git subprocess.
			return stdout.Bytes(), stderr.Bytes(), &GitError{
				Command:  strings.Join(args, " "),
				ExitCode: exitErr.ExitCode(),
				Stderr:   stderr.String(),
			}
		}
		// System-level error: binary not found, context cancelled,
		// signal termination (ExitCode == -1).
		return stdout.Bytes(), stderr.Bytes(), err
	}

	return stdout.Bytes(), stderr.Bytes(), nil
}

// RunExitCode executes git <args> and returns the raw exit code separately.
// It never produces a *GitError; non-zero exit codes carry specific
// semantics and are returned alongside nil error. System-level failures
// (binary not found, context cancelled) are returned as wrapped errors
// via fmt.Errorf with the %w verb for errors.As/errors.Is unwrapping.
func (r *GitRunner) RunExitCode(ctx context.Context, args ...string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.workDir
	cmd.Env = r.buildEnv()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode := exitErr.ExitCode()
			if exitCode >= 0 {
				// Normal non-zero exit — exit code carries semantics.
				return stdout.Bytes(), stderr.Bytes(), exitCode, nil
			}
			// Signal termination (e.g., SIGKILL from context cancellation).
			return stdout.Bytes(), stderr.Bytes(), exitCode, fmt.Errorf("git: %w", err)
		}
		// System-level error (binary not found, working dir invalid, etc.).
		return stdout.Bytes(), stderr.Bytes(), 0, fmt.Errorf("git: %w", err)
	}

	return stdout.Bytes(), stderr.Bytes(), 0, nil
}

// BranchExists checks whether a branch exists on a remote using
// git ls-remote --exit-code with three-way exit code discrimination:
//   - exit 0: branch exists, returns (true, nil)
//   - exit 2: branch missing, returns (false, nil)
//   - any other non-zero: network/auth failure, returns (false, error)
//
// The branch parameter must be a bare branch name (e.g. "main", not
// "refs/heads/main"); refs/heads/ is always prepended automatically.
func (r *GitRunner) BranchExists(ctx context.Context, remote, branch string) (bool, error) {
	ref := "refs/heads/" + branch
	_, stderr, exitCode, err := r.RunExitCode(ctx, "ls-remote", "--exit-code", remote, ref)
	if err != nil {
		// System-level failure (context cancelled, binary not found, signal kill).
		return false, err
	}
	switch exitCode {
	case 0:
		return true, nil
	case 2:
		return false, nil
	default:
		return false, fmt.Errorf("git ls-remote failed with exit code %d: %s",
			exitCode, strings.TrimSpace(string(stderr)))
	}
}

// minMajor and minMinor define the minimum git version required (2.38).
// These are deliberately unexported to prevent external dependence on the
// specific version threshold.
const (
	minMajor = 2
	minMinor = 38
)

// CheckGitVersion validates the host git binary meets the minimum version
// requirement (2.38) at startup. Returns an error with the format
// "requires git >= 2.38, found <version>" if below minimum, or the raw
// exec/parse error if git cannot be run or its output cannot be parsed.
func CheckGitVersion(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "git", "--version")
	output, err := cmd.Output()
	if err != nil {
		return err
	}

	version, err := parseGitVersion(string(output))
	if err != nil {
		return err
	}

	parts := strings.Split(version, ".")
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])

	if major < minMajor || (major == minMajor && minor < minMinor) {
		return fmt.Errorf("requires git >= %d.%d, found %s", minMajor, minMinor, version)
	}
	return nil
}

// parseGitVersion extracts the version string (major.minor.patch) from
// git --version output by taking the first three dot-separated numeric
// components and ignoring trailing tokens (Apple Git suffix, rc suffix, etc.).
// Returns an error if the output cannot be parsed into three numeric components.
func parseGitVersion(output string) (string, error) {
	const prefix = "git version "
	idx := strings.Index(output, prefix)
	if idx < 0 {
		return "", fmt.Errorf("cannot parse git version output: %q", output)
	}
	rest := strings.TrimSpace(output[idx+len(prefix):])

	// Take the first whitespace-delimited token (strips Apple Git suffix).
	tokens := strings.Fields(rest)
	if len(tokens) == 0 {
		return "", fmt.Errorf("cannot parse git version: empty version in %q", output)
	}
	versionToken := tokens[0]

	// Split by dots and require at least three numeric components.
	parts := strings.Split(versionToken, ".")
	if len(parts) < 3 {
		return "", fmt.Errorf("cannot parse git version: expected at least 3 components in %q", versionToken)
	}

	// Validate first three parts are numeric integers.
	for i := 0; i < 3; i++ {
		if _, err := strconv.Atoi(parts[i]); err != nil {
			return "", fmt.Errorf("cannot parse git version component %q in %q", parts[i], output)
		}
	}

	return parts[0] + "." + parts[1] + "." + parts[2], nil
}

// GitError is a structured error type returned by Run on non-zero git exit
// codes. Command holds the joined args without the 'git' binary prefix
// (e.g. "rebase origin/main"). RunExitCode never produces a GitError.
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
