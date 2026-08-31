package gitcmd

import (
	"context"
	"strings"
)

// Cherry runs `git cherry <upstream> <head>` to identify commits in the
// <head> branch that have NOT been applied to <upstream>.
//
// git cherry outputs one line per commit:
//   - "+ <sha>" — commit has NOT been applied upstream (still needs cherry-pick)
//   - "- <sha>" — commit HAS been applied upstream (content-equivalent exists)
//
// Cherry returns two slices:
//   - applied: SHAs that have content-equivalent commits upstream ("- " lines)
//   - pending: SHAs that have NOT been applied upstream ("+ " lines)
//
// If upstream or head is empty, returns an error without invoking git.
func (r *GitRunner) Cherry(ctx context.Context, upstream, head string) (applied []string, pending []string, err error) {
	if upstream == "" || head == "" {
		return nil, nil, &GitError{
			Args:     []string{"cherry"},
			ExitCode: -1,
			Stderr:   "upstream and head must not be empty",
		}
	}

	stdout, runErr := r.Run(ctx, "cherry", upstream, head)
	if runErr != nil {
		return nil, nil, runErr
	}

	if stdout == "" {
		return nil, nil, nil
	}

	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 3 {
			continue
		}
		prefix := line[0]
		sha := strings.TrimSpace(line[2:])
		switch prefix {
		case '-':
			applied = append(applied, sha)
		case '+':
			pending = append(pending, sha)
		}
	}
	return applied, pending, nil
}
