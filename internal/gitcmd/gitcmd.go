// Package gitcmd provides a hardened wrapper around git CLI subprocess calls
// with safety defaults, uniform error handling, and typed convenience methods.
package gitcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// GitError is a structured error type returned by GitRunner on command failure.
// It contains the full argument list, exit code, and trimmed stderr.
type GitError struct {
	// Args is the full command-line arguments of the failed git invocation
	// (e.g., ["rebase", "main"]).
	Args []string
	// ExitCode is the integer exit code returned by the failed git subprocess.
	ExitCode int
	// Stderr is the trimmed standard error output of the failed git subprocess.
	Stderr string
}

// Error implements the error interface, formatting Args, ExitCode, and Stderr
// into a human-readable message.
func (e *GitError) Error() string {
	return fmt.Sprintf("git %s: exit code %d: %s",
		strings.Join(e.Args, " "), e.ExitCode, e.Stderr)
}

// GitRunner wraps git CLI subprocess calls with safety defaults and uniform
// error handling. Use New to construct an instance.
//
// GitRunner holds only immutable state after construction (workDir and env).
// It is safe for concurrent use by multiple goroutines.
type GitRunner struct {
	workDir string
	env     []string
}

// New constructs a GitRunner that validates the working directory, checks that
// the host git version is >= 2.38, and returns a fully initialized *GitRunner.
//
// workDir must be an existing directory on the filesystem but is not required
// to be a git repository. extraEnv provides optional additional environment
// variables; the three safety variables (GIT_ALLOW_PROTOCOL, GIT_TERMINAL_PROMPT,
// GIT_CONFIG_NOSYSTEM) are always appended last and take precedence.
func New(workDir string, extraEnv []string) (*GitRunner, error) {
	// Validate workDir.
	if workDir == "" {
		return nil, fmt.Errorf("gitcmd: invalid working directory: empty path")
	}
	info, err := os.Stat(workDir)
	if err != nil {
		return nil, fmt.Errorf("gitcmd: invalid working directory %q: %w", workDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("gitcmd: invalid working directory %q: not a directory", workDir)
	}

	// Execute git --version with a 30-second timeout to avoid hanging
	// indefinitely (11-REQ-1.E3).
	versionCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Assemble the environment: os.Environ() + extraEnv + safety vars.
	// Safety variables are appended last so they always take precedence
	// over any conflicting values in extraEnv (11-REQ-2.2, 11-REQ-2.3).
	env := assembleEnv(extraEnv)

	cmd := exec.CommandContext(versionCtx, "git", "--version")
	cmd.Dir = workDir
	cmd.Env = env

	versionOut, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gitcmd: failed to run git --version: %w", err)
	}

	raw := strings.TrimSpace(string(versionOut))
	major, minor, err := parseGitVersion(raw)
	if err != nil {
		return nil, fmt.Errorf("gitcmd: %w", err)
	}

	if major < 2 || (major == 2 && minor < 38) {
		return nil, fmt.Errorf(
			"gitcmd: git version %d.%d is below the required minimum 2.38",
			major, minor)
	}

	return &GitRunner{
		workDir: workDir,
		env:     env,
	}, nil
}

// assembleEnv builds the full environment slice for git subprocesses.
// Order: os.Environ() + extraEnv + hardcoded safety variables.
func assembleEnv(extraEnv []string) []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+len(extraEnv)+3)
	env = append(env, base...)
	env = append(env, extraEnv...)
	env = append(env,
		"GIT_ALLOW_PROTOCOL=file:https:ssh",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	return env
}

// runWithExitCode executes a git command and returns the raw results without
// wrapping in *GitError. It returns the trimmed stdout (even on non-zero exit),
// the integer exit code, the trimmed stderr, and a non-nil error only for
// non-exec failures (context cancellation/timeout or binary-not-found).
//
// This helper enables callers like LsRemote to perform exit-code discrimination
// without re-implementing the subprocess boilerplate.
func (r *GitRunner) runWithExitCode(ctx context.Context, args ...string) (stdout string, exitCode int, stderr string, err error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.workDir
	cmd.Env = r.env

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()
	if runErr != nil {
		// Context cancellation/timeout takes priority (11-REQ-3.3).
		if ctx.Err() != nil {
			return "", -1, strings.TrimSpace(stderrBuf.String()), ctx.Err()
		}

		code := -1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code = exitErr.ExitCode()
		}

		return strings.TrimSpace(stdoutBuf.String()), code, strings.TrimSpace(stderrBuf.String()), nil
	}

	return strings.TrimSpace(stdoutBuf.String()), 0, strings.TrimSpace(stderrBuf.String()), nil
}

// Run executes an arbitrary git subcommand with the given args. It returns the
// trimmed stdout on success, or a *GitError on non-zero exit. If the context
// is cancelled or times out, Run returns the context error.
func (r *GitRunner) Run(ctx context.Context, args ...string) (string, error) {
	stdout, exitCode, stderr, err := r.runWithExitCode(ctx, args...)
	if err != nil {
		return "", err
	}
	if exitCode != 0 {
		return "", &GitError{
			Args:     args,
			ExitCode: exitCode,
			Stderr:   stderr,
		}
	}
	return stdout, nil
}

// ErrRefNotFound is a sentinel error returned by LsRemote when git exits with
// code 2, indicating the queried ref does not exist on the remote.
//
// The exit code 2 is produced by git ls-remote's --exit-code flag, which
// distinguishes a genuinely missing ref (exit 2) from connection or
// authentication failures (exit 1 or other non-zero). Callers should use
// errors.Is(err, ErrRefNotFound) to test for this condition.
var ErrRefNotFound = errors.New("ref not found on remote")

// MergeConflictError is returned by MergeTree when the dry-run merge detects
// conflicts. ConflictingFiles lists the file paths that have merge conflicts.
type MergeConflictError struct {
	ConflictingFiles []string
}

// Error implements the error interface.
func (e *MergeConflictError) Error() string {
	return fmt.Sprintf("merge conflict in %d file(s): %s",
		len(e.ConflictingFiles), strings.Join(e.ConflictingFiles, ", "))
}

// RebaseConflictError is returned by Rebase when the rebase encounters
// conflicts. ConflictingFiles lists the file paths that have merge conflicts.
type RebaseConflictError struct {
	ConflictingFiles []string
}

// Error implements the error interface.
func (e *RebaseConflictError) Error() string {
	return fmt.Sprintf("rebase conflict in %d file(s): %s",
		len(e.ConflictingFiles), strings.Join(e.ConflictingFiles, ", "))
}

// LsRemote executes git ls-remote --exit-code with three-way exit code
// discrimination: exit 0 returns (stdout, nil), exit 2 returns
// ("", ErrRefNotFound), exit 1 returns ("", *GitError).
func (r *GitRunner) LsRemote(ctx context.Context, remote, ref string) (string, error) {
	args := []string{"ls-remote", "--exit-code", remote, ref}

	stdout, exitCode, stderr, err := r.runWithExitCode(ctx, args...)
	if err != nil {
		return "", err
	}

	switch exitCode {
	case 0:
		return stdout, nil
	case 2:
		// Exit code 2: ref not found on remote.
		return "", ErrRefNotFound
	default:
		// All other non-zero exit codes: return *GitError.
		return "", &GitError{
			Args:     args,
			ExitCode: exitCode,
			Stderr:   stderr,
		}
	}
}

// MergeTree executes git merge-tree --write-tree for read-only conflict
// detection. Returns the tree SHA on a clean merge, or *MergeConflictError
// when conflicts are detected.
//
// CONFLICT line parsing rule:
// git merge-tree --write-tree (git 2.38+) emits conflict information to
// stdout. Lines starting with "CONFLICT" indicate merge conflicts. The file
// path is extracted from lines matching the format:
//
//	CONFLICT (content): Merge conflict in path/to/file.go
//
// The parser splits on "Merge conflict in " and takes the remainder as the
// file path. If a CONFLICT line does not match this format, the raw token
// after the last space is used as a best-effort file path.
func (r *GitRunner) MergeTree(ctx context.Context, base, head string) (string, error) {
	args := []string{"merge-tree", "--write-tree", base, head}

	stdout, exitCode, stderr, err := r.runWithExitCode(ctx, args...)
	if err != nil {
		return "", err
	}

	if exitCode != 0 {
		// Parse stdout for CONFLICT lines.
		conflictFiles := parseConflictFiles(stdout)
		if len(conflictFiles) > 0 {
			return "", &MergeConflictError{ConflictingFiles: conflictFiles}
		}

		// No CONFLICT lines: return *GitError (e.g., invalid SHA).
		return "", &GitError{
			Args:     args,
			ExitCode: exitCode,
			Stderr:   stderr,
		}
	}

	// Clean merge: first line of stdout is the tree SHA.
	lines := strings.SplitN(stdout, "\n", 2)
	if len(lines) == 0 || lines[0] == "" {
		return "", &GitError{
			Args:     args,
			ExitCode: 0,
			Stderr:   "empty stdout from git merge-tree",
		}
	}

	return strings.TrimSpace(lines[0]), nil
}

// parseConflictFiles scans git merge-tree output line-by-line for lines
// starting with "CONFLICT" and extracts file paths.
//
// Representative sample output from git 2.38+:
//
//	CONFLICT (content): Merge conflict in path/to/file.go
func parseConflictFiles(output string) []string {
	var files []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "CONFLICT") {
			continue
		}

		// Try to extract file path from "Merge conflict in <path>" format.
		const marker = "Merge conflict in "
		if idx := strings.Index(line, marker); idx >= 0 {
			path := strings.TrimSpace(line[idx+len(marker):])
			files = append(files, path)
			continue
		}

		// Best-effort: use the last space-delimited token as the path.
		parts := strings.Fields(line)
		if len(parts) > 0 {
			files = append(files, parts[len(parts)-1])
		} else {
			files = append(files, "")
		}
	}
	return files
}

// Rebase executes git rebase <onto> with automatic abort on conflict.
// Returns the new HEAD SHA on success or *RebaseConflictError on conflict.
// If the rebase encounters a conflict, git rebase --abort is called
// automatically before returning, so the repository is never left in
// rebase state when a *RebaseConflictError is returned.
//
// Note: This method captures stdout and stderr separately from Run because
// git rebase emits CONFLICT lines to stdout (not stderr), and Run discards
// stdout on error.
func (r *GitRunner) Rebase(ctx context.Context, onto string) (string, error) {
	args := []string{"rebase", onto}

	stdout, exitCode, stderr, err := r.runWithExitCode(ctx, args...)
	if err != nil {
		// Context cancellation: don't try to abort, just return.
		// The caller is responsible for calling RebaseAbort (11-REQ-6.E1).
		return "", err
	}

	if exitCode != 0 {
		// Parse conflict information from stdout (where git rebase writes
		// CONFLICT lines) and stderr.
		conflictFiles := parseRebaseConflictFiles(stdout)
		if len(conflictFiles) == 0 {
			conflictFiles = parseRebaseConflictFiles(stderr)
		}

		// Also detect conflicts by checking for rebase state directories.
		hasRebaseState := r.hasRebaseState()

		if hasRebaseState || len(conflictFiles) > 0 {
			// Attempt automatic abort.
			abortErr := r.RebaseAbort(ctx)
			if abortErr != nil {
				// 11-REQ-6.E2: wrap both the conflict info and abort failure.
				return "", fmt.Errorf(
					"rebase conflict (%s) and abort failed: %w",
					strings.Join(conflictFiles, ", "), abortErr)
			}

			// If we didn't find conflict files from the output, try to
			// extract them from the unmerged paths before aborting. Since
			// we already aborted, fall back to listing what we have.
			if len(conflictFiles) == 0 {
				// We know there was a conflict (rebase state existed), but
				// couldn't parse specific files. Include a generic entry.
				conflictFiles = []string{"(unresolved conflict)"}
			}

			return "", &RebaseConflictError{ConflictingFiles: conflictFiles}
		}

		// Not a conflict — return generic *GitError.
		return "", &GitError{
			Args:     args,
			ExitCode: exitCode,
			Stderr:   stderr,
		}
	}

	// Rebase succeeded: get the new HEAD SHA.
	sha, err := r.RevParse(ctx, "HEAD")
	if err != nil {
		return "", err
	}

	return sha, nil
}

// parseRebaseConflictFiles extracts conflicting file paths from git rebase
// stdout/stderr output. Lines containing "CONFLICT" with "Merge conflict in"
// are parsed similarly to merge-tree output.
func parseRebaseConflictFiles(output string) []string {
	var files []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "CONFLICT") {
			continue
		}
		const marker = "Merge conflict in "
		if idx := strings.Index(line, marker); idx >= 0 {
			path := strings.TrimSpace(line[idx+len(marker):])
			files = append(files, path)
			continue
		}
		// Best-effort fallback.
		parts := strings.Fields(line)
		if len(parts) > 0 {
			files = append(files, parts[len(parts)-1])
		}
	}
	return files
}

// hasRebaseState checks whether the repository is currently in a rebase state
// by looking for .git/rebase-merge or .git/rebase-apply directories.
func (r *GitRunner) hasRebaseState() bool {
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		path := r.workDir + "/.git/" + dir
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// RebaseAbort executes git rebase --abort to clean up a failed rebase state.
func (r *GitRunner) RebaseAbort(ctx context.Context) error {
	_, err := r.Run(ctx, "rebase", "--abort")
	return err
}

// RevParse executes git rev-parse to resolve a ref to its full SHA.
func (r *GitRunner) RevParse(ctx context.Context, ref string) (string, error) {
	return r.Run(ctx, "rev-parse", ref)
}

// UpdateRef executes git update-ref to update a reference to point to a SHA.
func (r *GitRunner) UpdateRef(ctx context.Context, ref, sha string) error {
	_, err := r.Run(ctx, "update-ref", ref, sha)
	return err
}
