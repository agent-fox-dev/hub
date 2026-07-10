package workspace

import (
	"fmt"
	"regexp"
	"strings"
)

// slugRegex matches valid workspace slugs:
// - 3 to 64 characters long
// - Starts with a lowercase letter
// - Contains only lowercase alphanumeric characters and hyphens
// - Does not end with a hyphen
//
// The regex is structured as:
//   - ^[a-z]          — must start with a lowercase letter
//   - [a-z0-9-]{1,62} — 1 to 62 middle characters (lowercase alnum + hyphen)
//   - [a-z0-9]$       — must end with lowercase letter or digit (no trailing hyphen)
//
// This yields a total length of 3 to 64 characters.
// For the 3-char boundary case, this is: letter + 1 middle + letter/digit = 3.
var slugRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}[a-z0-9]$`)

// ValidateSlug checks that a workspace slug conforms to the naming rules:
//   - 3 to 64 characters long
//   - Starts with a lowercase letter [a-z]
//   - Contains only lowercase alphanumeric characters [a-z0-9] and hyphens [-]
//   - Does not end with a hyphen
//
// Returns nil for valid slugs, a descriptive error otherwise.
func ValidateSlug(slug string) error {
	n := len(slug)
	if n < 3 || n > 64 {
		return fmt.Errorf("slug must be 3-64 characters long, got %d", n)
	}
	if slug[0] < 'a' || slug[0] > 'z' {
		return fmt.Errorf("slug must start with a lowercase letter")
	}
	if slug[n-1] == '-' {
		return fmt.Errorf("slug must not end with a hyphen")
	}
	if !slugRegex.MatchString(slug) {
		return fmt.Errorf("slug must contain only lowercase alphanumeric characters and hyphens")
	}
	return nil
}

// scpSSHRegex matches SCP-style SSH git URLs: git@<host>:<path>
// Examples: git@github.com:org/repo.git
var scpSSHRegex = regexp.MustCompile(`^git@[^:]+:.+$`)

// ValidateGitURL checks that a git remote URL is in an accepted format:
//   - HTTPS: starts with "https://"
//   - SCP-style SSH: matches git@<host>:<path>
//
// The URL must be at most 2048 characters. No reachability validation is performed.
// Returns nil for valid URLs, a descriptive error otherwise.
func ValidateGitURL(url string) error {
	if len(url) > 2048 {
		return fmt.Errorf("git_url must be at most 2048 characters, got %d", len(url))
	}
	if strings.HasPrefix(url, "https://") {
		return nil
	}
	if scpSSHRegex.MatchString(url) {
		return nil
	}
	return fmt.Errorf("git_url must start with https:// or match SCP-style SSH format (git@host:path)")
}

// ValidateBranch checks that an optional branch string:
//   - Is at most 255 characters
//   - Contains no ASCII whitespace (space 0x20, tab 0x09, newline 0x0A, carriage return 0x0D)
//
// A nil pointer means "omitted" and is always valid (stored as null).
// An empty string is valid per the rules (no whitespace, within length).
// Returns nil for valid branches, a descriptive error otherwise.
func ValidateBranch(branch *string) error {
	if branch == nil {
		return nil
	}
	if len(*branch) > 255 {
		return fmt.Errorf("branch must be at most 255 characters, got %d", len(*branch))
	}
	for _, c := range *branch {
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			return fmt.Errorf("branch must not contain ASCII whitespace characters")
		}
	}
	return nil
}
