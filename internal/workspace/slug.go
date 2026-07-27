package workspace

import (
	"regexp"
	"strings"
)

// sanitizeInvalidChars matches any character not in [a-z0-9_-].
var sanitizeInvalidChars = regexp.MustCompile(`[^a-z0-9_-]`)

// sanitizeConsecutiveHyphens matches two or more consecutive hyphens.
var sanitizeConsecutiveHyphens = regexp.MustCompile(`-{2,}`)

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
	// Step 1: Lowercase the entire username string.
	s := strings.ToLower(username)

	// Step 2: Replace any character not in [a-z0-9_-] with a hyphen.
	s = sanitizeInvalidChars.ReplaceAllString(s, "-")

	// Step 3: Collapse consecutive hyphens into a single hyphen.
	s = sanitizeConsecutiveHyphens.ReplaceAllString(s, "-")

	// Step 4: Trim leading and trailing hyphens and underscores.
	s = strings.Trim(s, "-_")

	// Step 5: If the result is shorter than 2 characters, use fallback.
	// This check comes before the digit-prefix step because a single digit
	// (e.g. "7") must trigger the fallback (spec 04-REQ-5.E3), not get
	// the u- prefix which would make it "u-7" and bypass the fallback.
	if len(s) < 2 {
		uid := userID
		if len(uid) > 8 {
			uid = uid[:8]
		}
		return "u-" + uid
	}

	// Step 6: If the result starts with a digit, prepend "u-".
	if s[0] >= '0' && s[0] <= '9' {
		s = "u-" + s
	}

	return s
}
