package gitcmd

import "context"

// IsAncestor determines whether commitA is an ancestor of commitB by running
// `git merge-base --is-ancestor <commitA> <commitB>` and performing
// three-way exit code discrimination:
//   - exit 0: commitA IS an ancestor → returns (true, nil)
//   - exit 1: commitA is NOT an ancestor → returns (false, nil)
//   - other: unexpected error → returns (false, *GitError)
//
// If either commitA or commitB is empty, returns (false, *GitError) without
// invoking the git subprocess.
func (r *GitRunner) IsAncestor(ctx context.Context, commitA, commitB string) (bool, error) {
	if commitA == "" || commitB == "" {
		return false, &GitError{
			Args:     []string{"merge-base", "--is-ancestor"},
			ExitCode: -1,
			Stderr:   "commitA and commitB must not be empty",
		}
	}

	args := []string{"merge-base", "--is-ancestor", endOfOptions, commitA, commitB}

	_, exitCode, stderr, err := r.runWithExitCode(ctx, args...)
	if err != nil {
		// Context cancellation or deadline exceeded.
		return false, err
	}

	// Exit codes 0 and 1 are the two answers; anything else is an error.
	switch exitCode {
	case 0:
		// commitA is an ancestor of commitB.
		return true, nil
	case 1:
		// commitA is not an ancestor of commitB.
		return false, nil
	default:
		// Unexpected error (e.g. invalid SHAs).
		return false, &GitError{
			Args:     args,
			ExitCode: exitCode,
			Stderr:   stderr,
		}
	}
}
