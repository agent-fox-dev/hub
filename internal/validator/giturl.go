package validator

// ValidateGitURL checks whether the given URL is a valid git remote URL.
// Accepts HTTPS (https://...) or SSH (git@host:path) formats only.
// Rejects http://, git://, ssh://, ftp://, local paths, and empty strings.
// Does not perform any network calls — validation is purely syntactic.
//
// Stub: real implementation in task group 6.
func ValidateGitURL(url string) bool {
	return false
}
