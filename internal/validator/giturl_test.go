package validator

import (
	"testing"
	"time"
)

// TS-07-29: Verifies the git_url validator accepts HTTPS URLs starting with
// https:// and SSH URLs matching git@host:path pattern.
//
// Requirement: 07-REQ-7.1
func TestValidateGitURL_ValidInputs(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"https_github", "https://github.com/org/repo.git"},
		{"https_gitlab", "https://gitlab.com/user/project"},
		{"ssh_github", "git@github.com:org/repo.git"},
		{"ssh_bitbucket", "git@bitbucket.org:user/repo.git"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !ValidateGitURL(tt.url) {
				t.Errorf("ValidateGitURL(%q) = false, want true", tt.url)
			}
		})
	}
}

// TS-07-30: Verifies the git_url validator rejects local filesystem paths,
// http://, git://, ssh:// schemes, and any other non-HTTPS/SSH format.
//
// TS-07-E12: Verifies the git_url validator returns false without panicking
// when an empty string is passed.
//
// Requirements: 07-REQ-7.2, 07-REQ-7.E1
func TestValidateGitURL_InvalidInputs(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"local_filesystem_path", "/local/path/repo"},
		{"http_non_tls", "http://github.com/repo.git"},
		{"git_protocol", "git://github.com/repo.git"},
		{"ssh_protocol", "ssh://git@github.com/repo.git"},
		{"ftp_protocol", "ftp://example.com/repo"},
		{"empty_string", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ValidateGitURL(tt.url) {
				t.Errorf("ValidateGitURL(%q) = true, want false", tt.url)
			}
		})
	}
}

// TS-07-31: Verifies the git_url validator performs no network calls —
// validation is purely syntactic. A syntactically valid but unreachable URL
// must return true in well under 1 millisecond.
//
// Requirement: 07-REQ-7.3
func TestValidateGitURL_NoNetworkCalls(t *testing.T) {
	start := time.Now()
	result := ValidateGitURL("https://unreachable.example.com/repo.git")
	elapsed := time.Since(start)

	if !result {
		t.Error("ValidateGitURL(\"https://unreachable.example.com/repo.git\") = false, want true (syntactically valid HTTPS URL)")
	}
	if elapsed > time.Millisecond {
		t.Errorf("ValidateGitURL took %v, expected < 1ms (may indicate network call)", elapsed)
	}
}

// TestValidateGitURL_EmptyStringNoPanic is an explicit edge case test ensuring
// that an empty string does not cause a panic.
//
// TS-07-E12: Requirement 07-REQ-7.E1
func TestValidateGitURL_EmptyStringNoPanic(t *testing.T) {
	// This test verifies that the function returns false and does not panic.
	// If the function panics, the test will fail with a panic trace.
	result := ValidateGitURL("")
	if result {
		t.Error("ValidateGitURL(\"\") = true, want false")
	}
}
