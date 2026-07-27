package workspace

// sanitizeSlug transforms a username into a valid organization slug following
// these rules:
//   - Lowercase the entire string
//   - Replace any character not in [a-z0-9_-] with a hyphen
//   - Collapse consecutive hyphens into one
//   - Trim leading and trailing hyphens and underscores
//   - If the result starts with a digit, prepend "u-"
//   - If the result is shorter than 2 characters, use fallback "u-<first 8 chars of userID>"
//
// The output always passes apikit's validateSlug (pattern ^[a-z0-9][a-z0-9_-]*[a-z0-9]$).
func sanitizeSlug(username, userID string) string {
	// Stub: return empty string so tests fail.
	// Implementation will be provided in task group 9.
	return ""
}
