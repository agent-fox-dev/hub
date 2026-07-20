package workspace

import (
	"fmt"
	"regexp"
	"strings"
)

// slugPattern matches lowercase alphanumeric characters and hyphens, 3–64 chars.
var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}[a-z0-9]$`)

// httpsGitURLPattern matches https://<host>/<path> where host and path are non-empty.
var httpsGitURLPattern = regexp.MustCompile(`^https://([^/]+)/(.+)$`)

// sshGitURLPattern matches git@<host>:<path> where host and path are non-empty.
var sshGitURLPattern = regexp.MustCompile(`^git@([^:]+):(.+)$`)

// validateSlug checks that slug is 3–64 characters, lowercase alphanumeric
// plus hyphens, starts with a letter, has no trailing hyphen, and has no
// consecutive hyphens.
func validateSlug(slug string) error {
	if len(slug) < 3 {
		return fmt.Errorf("slug must be at least 3 characters, got %d", len(slug))
	}
	if len(slug) > 64 {
		return fmt.Errorf("slug must be at most 64 characters, got %d", len(slug))
	}

	// Must start with a letter.
	if slug[0] < 'a' || slug[0] > 'z' {
		return fmt.Errorf("slug must start with a lowercase letter")
	}

	// Must end with a letter or digit (no trailing hyphen).
	last := slug[len(slug)-1]
	if !((last >= 'a' && last <= 'z') || (last >= '0' && last <= '9')) {
		return fmt.Errorf("slug must not end with a hyphen")
	}

	// Check for consecutive hyphens.
	if strings.Contains(slug, "--") {
		return fmt.Errorf("slug must not contain consecutive hyphens")
	}

	// Must match the overall pattern: lowercase alphanumeric + hyphens only.
	if !slugPattern.MatchString(slug) {
		return fmt.Errorf("slug must contain only lowercase letters, digits, and hyphens")
	}

	return nil
}

// validateGitURL checks that git_url matches HTTPS format
// (https://<host>/<path>) or SSH format (git@<host>:<path>) where both host
// and path are non-empty. Validates by pattern matching only without DNS
// resolution or protocol negotiation.
func validateGitURL(url string) error {
	if url == "" {
		return fmt.Errorf("git_url must not be empty")
	}

	// Reject plain HTTP.
	if strings.HasPrefix(url, "http://") {
		return fmt.Errorf("git_url must use HTTPS or SSH, not plain HTTP")
	}

	// Try HTTPS pattern.
	if strings.HasPrefix(url, "https://") {
		matches := httpsGitURLPattern.FindStringSubmatch(url)
		if matches == nil {
			return fmt.Errorf("git_url HTTPS format must be https://<host>/<path> with non-empty host and path")
		}
		host := matches[1]
		path := matches[2]
		if host == "" {
			return fmt.Errorf("git_url must have a non-empty host")
		}
		if path == "" {
			return fmt.Errorf("git_url must have a non-empty path")
		}
		return nil
	}

	// Try SSH pattern.
	if strings.HasPrefix(url, "git@") {
		matches := sshGitURLPattern.FindStringSubmatch(url)
		if matches == nil {
			return fmt.Errorf("git_url SSH format must be git@<host>:<path> with non-empty host and path")
		}
		host := matches[1]
		path := matches[2]
		if host == "" {
			return fmt.Errorf("git_url must have a non-empty host")
		}
		if path == "" {
			return fmt.Errorf("git_url must have a non-empty path")
		}
		return nil
	}

	return fmt.Errorf("git_url must use HTTPS (https://) or SSH (git@) format")
}

// validateBranch checks that branch is non-empty and follows git ref naming
// rules: no ASCII control characters, no space, no ~, ^, :, ?, *, [, or \,
// no .. sequences, no trailing .lock, no trailing dot, no leading dot in any
// path component.
func validateBranch(branch string) error {
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

	return nil
}
