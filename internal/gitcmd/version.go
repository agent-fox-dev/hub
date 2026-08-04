package gitcmd

import (
	"fmt"
	"strconv"
	"strings"
)

// parseGitVersion parses a raw "git --version" output string and returns the
// major and minor version numbers. It is a pure function with no side effects
// or subprocess calls.
//
// Expected input format: "git version X.Y[.Z] [suffix]"
// Examples:
//   - "git version 2.39.1"
//   - "git version 2.39.1 (Apple Git-166)"
//   - "git version 2.42.0.windows.1"
//   - "git version 2.39"
//
// Version comparison against the minimum (2.38) is the caller's
// responsibility; this function only parses.
func parseGitVersion(raw string) (major, minor int, err error) {
	const prefix = "git version "

	if !strings.HasPrefix(raw, prefix) {
		return 0, 0, fmt.Errorf("unrecognized git version format: %q", raw)
	}

	versionPart := strings.TrimPrefix(raw, prefix)

	// Drop platform suffixes separated by a space
	// (e.g., "(Apple Git-166)").
	if idx := strings.IndexByte(versionPart, ' '); idx >= 0 {
		versionPart = versionPart[:idx]
	}

	// Split into dot-separated components. We only need the first two
	// (major.minor). Extra components like patch or platform tags
	// (e.g., "0.windows.1") are ignored.
	parts := strings.SplitN(versionPart, ".", 3)
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("unrecognized git version format (need at least major.minor): %q", raw)
	}

	major, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("unrecognized git version format (invalid major): %q", raw)
	}

	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("unrecognized git version format (invalid minor): %q", raw)
	}

	return major, minor, nil
}
