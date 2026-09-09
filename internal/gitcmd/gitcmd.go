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
		strings.Join(redactArgs(e.Args), " "), e.ExitCode, redactUserinfo(e.Stderr))
}

// redactArgs returns a copy of args with credentials embedded in URLs
// (https://user:token@host/...) replaced, so that error messages stored in
// job records, audit events, or logs never carry a token.
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = redactUserinfo(a)
	}
	return out
}

// redactUserinfo masks the userinfo part of any URL-looking token in s.
func redactUserinfo(s string) string {
	for _, scheme := range []string{"https://", "http://", "ssh://"} {
		idx := 0
		for {
			start := strings.Index(s[idx:], scheme)
			if start < 0 {
				break
			}
			start += idx + len(scheme)
			end := start
			for end < len(s) && s[end] != '/' && s[end] != ' ' && s[end] != '\n' && s[end] != '"' && s[end] != '\'' {
				end++
			}
			if at := strings.LastIndex(s[start:end], "@"); at >= 0 {
				s = s[:start] + "***@" + s[start+at+1:]
				end = start + len("***@")
			}
			idx = end
		}
	}
	return s
}

// DefaultTimeout bounds every git invocation whose context carries no
// deadline of its own. Job handlers normally pass a deadline-bearing context;
// this is the safety net for the ones that do not, so a wedged git process
// (stuck credential helper, hung remote) cannot pin a workspace lock forever.
var DefaultTimeout = 10 * time.Minute

// Default committer identity used when neither the process environment nor
// extraEnv provides one. Without it git refuses to create commits in
// containers whose hostname has no domain part ("unable to auto-detect
// email address"), which breaks carry-patch rebuilds and merge jobs.
const (
	DefaultIdentityName  = "af-hub"
	DefaultIdentityEmail = "af-hub@localhost"
)

// strippedEnvVars are inherited environment variables that redirect git to
// a different repository or inject configuration. They must never leak from
// the hub process (or a test harness) into the subprocesses that operate on
// workspace trunks.
var strippedEnvVars = map[string]bool{
	"GIT_DIR":                          true,
	"GIT_WORK_TREE":                    true,
	"GIT_INDEX_FILE":                   true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_COMMON_DIR":                   true,
	"GIT_NAMESPACE":                    true,
	"GIT_CONFIG_PARAMETERS":            true,
	"GIT_CONFIG_COUNT":                 true,
}

// defaultedEnvVars are appended only when absent from the inherited
// environment and extraEnv, so operators can still override them.
var defaultedEnvVars = []string{
	"GIT_AUTHOR_NAME=" + DefaultIdentityName,
	"GIT_AUTHOR_EMAIL=" + DefaultIdentityEmail,
	"GIT_COMMITTER_NAME=" + DefaultIdentityName,
	"GIT_COMMITTER_EMAIL=" + DefaultIdentityEmail,
	// Never open an editor for merge/cherry-pick messages.
	"GIT_EDITOR=true",
	// Never page output.
	"GIT_PAGER=cat",
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
// Order: filtered os.Environ() + extraEnv + identity/editor defaults (only
// when not already set) + hardcoded safety variables. The safety variables
// are last so they always take precedence (11-REQ-2.2, 11-REQ-2.3).
func assembleEnv(extraEnv []string) []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+len(extraEnv)+len(defaultedEnvVars)+4)
	present := make(map[string]bool, len(base)+len(extraEnv))
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		if strippedEnvVars[key] {
			continue
		}
		present[key] = true
		env = append(env, kv)
	}
	for _, kv := range extraEnv {
		key, _, _ := strings.Cut(kv, "=")
		present[key] = true
		env = append(env, kv)
	}
	for _, kv := range defaultedEnvVars {
		key, _, _ := strings.Cut(kv, "=")
		if !present[key] {
			env = append(env, kv)
		}
	}
	env = append(env,
		"GIT_ALLOW_PROTOCOL=file:https:ssh",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		// Parsers (conflict detection, rev-parse) rely on English output.
		"LC_ALL=C",
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
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && DefaultTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.workDir
	cmd.Env = r.env
	configureProcess(cmd)

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
	args := []string{"ls-remote", "--exit-code", endOfOptions, remote, ref}

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
// when conflicts are detected. Callers must use errors.As to extract
// *MergeConflictError from the returned error.
//
// On non-zero exit without CONFLICT lines in stdout (e.g. invalid SHA
// arguments), *GitError is returned instead.
//
// Note: merge-tree --write-tree requires git >= 2.38, which is enforced
// at construction time by New.
//
// See parseConflictFiles in conflict.go for the CONFLICT line parsing rule
// and representative sample output.
func (r *GitRunner) MergeTree(ctx context.Context, base, head string) (string, error) {
	args := []string{"merge-tree", "--write-tree", endOfOptions, base, head}

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

// Rebase executes git rebase <onto> with automatic abort on conflict.
// Returns the new HEAD SHA on success or *RebaseConflictError on conflict.
// Callers must use errors.As to extract *RebaseConflictError from the
// returned error.
//
// If the rebase encounters a conflict, git rebase --abort is called
// automatically before returning, so the repository is never left in
// rebase state when a *RebaseConflictError is returned (11-PROP-3).
//
// Important distinction:
//   - Conflict: returns *RebaseConflictError — repository is cleaned up
//     automatically via internal abort.
//   - Context cancellation/timeout: returns the context error — the
//     repository may still be in rebase state. The caller must invoke
//     RebaseAbort to clean up (11-REQ-6.E1).
//   - Abort failure after conflict: returns an error wrapping both the
//     conflict information and the abort failure (11-REQ-6.E2).
//   - Invalid onto ref: returns *GitError with exit code and stderr
//     (11-REQ-6.E3).
//   - rev-parse failure after successful rebase: returns *GitError
//     (11-REQ-6.E4).
func (r *GitRunner) Rebase(ctx context.Context, onto string) (string, error) {
	args := []string{"rebase", endOfOptions, onto}

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
//
// This method is intended for edge-case manual recovery when a context
// cancellation interrupts a Rebase call mid-flight, leaving the repository
// in an intermediate rebase state. In the normal conflict case, Rebase calls
// git rebase --abort internally before returning *RebaseConflictError, so
// callers do not need to invoke RebaseAbort themselves.
//
// Usage pattern after context cancellation:
//
//	sha, err := runner.Rebase(ctx, "main")
//	if errors.Is(err, context.DeadlineExceeded) {
//	    // Repository may be in rebase state — clean up.
//	    _ = runner.RebaseAbort(context.Background())
//	}
func (r *GitRunner) RebaseAbort(ctx context.Context) error {
	_, err := r.Run(ctx, "rebase", "--abort")
	return err
}

// RebaseContinue runs `git rebase --continue` to resume a paused rebase
// after conflicts have been resolved (e.g. by rerere).
//
// On success, returns (newHeadSHA, nil) where newHeadSHA is the 40-character
// hex SHA of the resulting HEAD commit.
//
// If git rebase --continue exits with a non-zero code, returns ("", *GitError)
// wrapping the exit code and stderr.
func (r *GitRunner) RebaseContinue(ctx context.Context) (string, error) {
	// Use -c core.editor=true to avoid editor prompts during rebase --continue.
	args := []string{"-c", "core.editor=true", "rebase", "--continue"}

	stdout, exitCode, stderr, err := r.runWithExitCode(ctx, args...)
	if err != nil {
		return "", err
	}

	if exitCode != 0 {
		return "", &GitError{
			Args:     args,
			ExitCode: exitCode,
			Stderr:   stderr + stdout,
		}
	}

	newSHA, err := r.RevParse(ctx, "HEAD")
	if err != nil {
		return "", err
	}

	return newSHA, nil
}

// RevParse executes git rev-parse --verify to resolve a single ref to its
// full SHA. --verify makes an unknown or option-like ref an error instead of
// echoing it back, and --end-of-options prevents ref from being parsed as an
// option.
func (r *GitRunner) RevParse(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", &GitError{Args: []string{"rev-parse"}, ExitCode: -1, Stderr: "ref must not be empty"}
	}
	return r.Run(ctx, "rev-parse", "--verify", endOfOptions, ref)
}

// UpdateRef executes git update-ref to update a reference to point to a SHA.
func (r *GitRunner) UpdateRef(ctx context.Context, ref, sha string) error {
	_, err := r.Run(ctx, "update-ref", endOfOptions, ref, sha)
	return err
}

// ResetInProgressState clears state a crashed or cancelled operation may
// have left in the working tree: an in-progress rebase, cherry-pick, or
// merge, plus uncommitted changes. Every step is best-effort; errors are
// ignored because the state simply may not exist.
func (r *GitRunner) ResetInProgressState(ctx context.Context) {
	_, _ = r.Run(ctx, "rebase", "--abort")
	_, _ = r.Run(ctx, "cherry-pick", "--abort")
	_, _ = r.Run(ctx, "merge", "--abort")
	_, _ = r.Run(ctx, "reset", "--hard", "HEAD", "--")
}
