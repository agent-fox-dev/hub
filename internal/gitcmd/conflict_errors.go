package gitcmd

import (
	"fmt"
	"strings"
)

// CherryPickConflictError is returned by CherryPick when git cherry-pick
// exits with conflicts. ConflictingFiles lists the file paths that have
// merge conflicts, or ["(unresolved conflict)"] when parsing fails.
type CherryPickConflictError struct {
	ConflictingFiles []string
}

// Error implements the error interface. The format is consistent with
// RebaseConflictError's format.
func (e *CherryPickConflictError) Error() string {
	return fmt.Sprintf("cherry-pick conflict in %d file(s): %s",
		len(e.ConflictingFiles), strings.Join(e.ConflictingFiles, ", "))
}

// MergeNoFFConflictError is returned by MergeNoFF when git merge --no-ff
// exits with conflicts. ConflictingFiles lists the file paths that have
// merge conflicts, or ["(unresolved conflict)"] when parsing fails.
type MergeNoFFConflictError struct {
	ConflictingFiles []string
}

// Error implements the error interface. The format is consistent with
// RebaseConflictError's format.
func (e *MergeNoFFConflictError) Error() string {
	return fmt.Sprintf("merge --no-ff conflict in %d file(s): %s",
		len(e.ConflictingFiles), strings.Join(e.ConflictingFiles, ", "))
}
