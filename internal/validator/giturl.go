package validator

import (
	"regexp"
	"strings"
)

// sshGitURLRegexp matches SSH-format git remote URLs: git@<host>:<path>.
// The host must contain at least one character that is not a colon, and
// the path must start with a non-slash character and contain at least one
// more character.
var sshGitURLRegexp = regexp.MustCompile(`^git@[^:]+:[^/].+$`)

// ValidateGitURL checks whether the given URL is a valid git remote URL.
// Accepts HTTPS (https://...) or SSH (git@host:path) formats only.
// Rejects http://, git://, ssh://, ftp://, local paths, and empty strings.
// Does not perform any network calls — validation is purely syntactic.
func ValidateGitURL(rawURL string) bool {
	if rawURL == "" {
		return false
	}
	if strings.HasPrefix(rawURL, "https://") {
		return true
	}
	return sshGitURLRegexp.MatchString(rawURL)
}
