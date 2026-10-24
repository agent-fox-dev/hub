package gitcmd

import (
	"strings"
	"testing"
)

// The parser only extracts major.minor; whether the version is new enough is
// New's decision, so old versions parse too.
func TestParseGitVersion_ExtractsMajorMinor(t *testing.T) {
	tests := []struct {
		input        string
		major, minor int
	}{
		{"git version 2.39.1", 2, 39},
		{"git version 2.38.0", 2, 38},
		{"git version 2.37.0", 2, 37},
		{"git version 1.8.3", 1, 8},
		{"git version 3.0.0", 3, 0},
		{"git version 2.39", 2, 39},
		{"git version 2.39.1 (Apple Git-166)", 2, 39},
		{"git version 2.42.0.windows.1", 2, 42},
	}
	for _, tt := range tests {
		major, minor, err := parseGitVersion(tt.input)
		if err != nil || major != tt.major || minor != tt.minor {
			t.Errorf("parseGitVersion(%q) = %d, %d, %v; want %d, %d", tt.input, major, minor, err, tt.major, tt.minor)
		}
	}
}

// Malformed output is an error that quotes the raw input.
func TestParseGitVersion_RejectsMalformedOutput(t *testing.T) {
	for _, input := range []string{"", "   ", "not a git version string", "git version", "git version 2", "git version abc.def", "git version 2.x", "version 2.39.1"} {
		_, _, err := parseGitVersion(input)
		if err == nil {
			t.Errorf("parseGitVersion(%q) succeeded; want error", input)
		} else if !strings.Contains(err.Error(), input) {
			t.Errorf("parseGitVersion(%q) error %q does not quote the input", input, err)
		}
	}
}
