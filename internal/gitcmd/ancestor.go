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
	// TODO: implement in task group 10
	return false, nil
}
