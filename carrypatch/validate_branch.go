package carrypatch

import (
	"fmt"
	"strings"

	"github.com/agent-fox-dev/carrypatch/gitcmd"
)

// validateBranchName checks that branch is non-empty and follows git ref naming
// rules: no ASCII control characters, no space, no ~, ^, :, ?, *, [, or \,
// no .. sequences, no trailing .lock, no trailing dot, no leading dot in any
// path component.
func validateBranchName(branch string) error {
	if branch == "" {
		return fmt.Errorf("branch must not be empty")
	}

	// Check for forbidden characters.
	for _, r := range branch {
		if r < 0x20 || r == 0x7f { // ASCII control characters
			return fmt.Errorf("branch must not contain ASCII control characters")
		}
		switch r {
		case ' ':
			return fmt.Errorf("branch must not contain spaces")
		case '~':
			return fmt.Errorf("branch must not contain '~'")
		case '^':
			return fmt.Errorf("branch must not contain '^'")
		case ':':
			return fmt.Errorf("branch must not contain ':'")
		case '?':
			return fmt.Errorf("branch must not contain '?'")
		case '*':
			return fmt.Errorf("branch must not contain '*'")
		case '[':
			return fmt.Errorf("branch must not contain '['")
		case '\\':
			return fmt.Errorf("branch must not contain '\\'")
		}
	}

	// No '..' sequences.
	if strings.Contains(branch, "..") {
		return fmt.Errorf("branch must not contain '..' sequences")
	}

	// No trailing '.lock'.
	if strings.HasSuffix(branch, ".lock") {
		return fmt.Errorf("branch must not end with '.lock'")
	}

	// No trailing dot.
	if strings.HasSuffix(branch, ".") {
		return fmt.Errorf("branch must not end with '.'")
	}

	// No leading dot in any path component.
	components := strings.Split(branch, "/")
	for _, comp := range components {
		if strings.HasPrefix(comp, ".") {
			return fmt.Errorf("branch must not have a leading dot in any path component")
		}
	}

	// Remaining git ref-format rules, including the rejection of names that
	// start with '-' (which git would otherwise parse as an option).
	if err := gitcmd.ValidateRefName(branch); err != nil {
		return fmt.Errorf("branch %s", strings.TrimPrefix(err.Error(), "ref name "))
	}

	return nil
}
