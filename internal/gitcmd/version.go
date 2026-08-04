package gitcmd

import "fmt"

// parseGitVersion parses a raw "git --version" output string and returns the
// major and minor version numbers. It is a pure function with no side effects.
// Version comparison against the minimum (2.38) is the caller's responsibility.
func parseGitVersion(_ string) (major, minor int, err error) {
	// TODO: implement — parse "git version X.Y[.Z]" format.
	return 0, 0, fmt.Errorf("not implemented")
}
