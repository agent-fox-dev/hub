package gitcmd

import (
	"testing"
)

// ========================================================================
// Spec 11 Task 1.1: Version string parser unit tests
// (TS-11-32, TS-11-33, TS-11-34)
// Requirements: 11-REQ-12.1, 11-REQ-12.2, 11-REQ-12.3
// ========================================================================

// TestParseGitVersion_Valid verifies that the version parser correctly extracts
// major and minor version numbers from valid "git --version" output strings.
//
// Per 11-REQ-12.E2, the parser must parse ALL valid version strings
// successfully, including versions below 2.38. Version comparison against
// the minimum is the constructor's responsibility, not the parser's.
//
// TS-11-32: Pure function returning (major, minor, error)
// TS-11-33: Parses "git version 2.39.1" -> major=2, minor=39
func TestParseGitVersion_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input string
		major int
		minor int
	}{
		// Standard three-component versions (above 2.38)
		{
			name:  "standard_2.39.1",
			input: "git version 2.39.1",
			major: 2,
			minor: 39,
		},
		{
			name:  "exactly_2.38.0",
			input: "git version 2.38.0",
			major: 2,
			minor: 38,
		},
		{
			name:  "major_version_3",
			input: "git version 3.0.0",
			major: 3,
			minor: 0,
		},
		{
			name:  "high_minor_version",
			input: "git version 2.55.0",
			major: 2,
			minor: 55,
		},

		// Versions below 2.38 — must parse successfully (11-REQ-12.E2)
		{
			name:  "below_minimum_2.37.0",
			input: "git version 2.37.0",
			major: 2,
			minor: 37,
		},
		{
			name:  "old_version_2.0.0",
			input: "git version 2.0.0",
			major: 2,
			minor: 0,
		},
		{
			name:  "version_1.8.3",
			input: "git version 1.8.3",
			major: 1,
			minor: 8,
		},

		// Two-component version (X.Y without .Z)
		{
			name:  "two_component_version",
			input: "git version 2.39",
			major: 2,
			minor: 39,
		},

		// Platform-specific suffixes (11-REQ-12.E3)
		{
			name:  "apple_git_suffix",
			input: "git version 2.39.1 (Apple Git-166)",
			major: 2,
			minor: 39,
		},
		{
			name:  "windows_suffix",
			input: "git version 2.42.0.windows.1",
			major: 2,
			minor: 42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			major, minor, err := parseGitVersion(tt.input)
			if err != nil {
				t.Fatalf("parseGitVersion(%q) returned unexpected error: %v", tt.input, err)
			}
			if major != tt.major {
				t.Errorf("parseGitVersion(%q) major = %d, want %d", tt.input, major, tt.major)
			}
			if minor != tt.minor {
				t.Errorf("parseGitVersion(%q) minor = %d, want %d", tt.input, minor, tt.minor)
			}
		})
	}
}

// TestParseGitVersion_Malformed verifies that the parser returns an error
// for inputs that do not match the expected "git version X.Y[.Z]" format.
//
// TS-11-34: Returns error with message indicating unrecognized format
// 11-REQ-12.E1: Empty string returns error
func TestParseGitVersion_Malformed(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "empty_string",
			input: "",
		},
		{
			name:  "arbitrary_text",
			input: "not a git version string",
		},
		{
			name:  "prefix_only_no_numbers",
			input: "git version",
		},
		{
			name:  "prefix_with_non_numeric",
			input: "git version abc.def",
		},
		{
			name:  "wrong_prefix",
			input: "version 2.39.1",
		},
		{
			name:  "only_whitespace",
			input: "   ",
		},
		{
			name:  "single_number_after_prefix",
			input: "git version 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseGitVersion(tt.input)
			if err == nil {
				t.Fatalf("parseGitVersion(%q) returned nil error, want non-nil", tt.input)
			}
			// TS-11-34: error message should reference the raw input
			errMsg := err.Error()
			if errMsg == "" {
				t.Errorf("parseGitVersion(%q) error message is empty", tt.input)
			}
		})
	}
}

// TestParseGitVersion_RawInputInError verifies that when the parser returns an
// error for an unrecognized format, the error message contains the raw input
// string so the caller can diagnose the issue.
//
// TS-11-34: "non-nil error containing 'not a git version string' or similar
// indication of the raw input"
func TestParseGitVersion_RawInputInError(t *testing.T) {
	raw := "not a git version string"
	_, _, err := parseGitVersion(raw)
	if err == nil {
		t.Fatal("parseGitVersion returned nil error for malformed input")
	}

	errMsg := err.Error()
	if !containsSubstring(errMsg, raw) {
		t.Errorf("error message %q does not contain raw input %q", errMsg, raw)
	}
}

// containsSubstring is a test helper to check substring presence.
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
