package forge

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// redactedMarker takes the place of the token in scrubbed text.
	redactedMarker = "[redacted]"
	// maxMessageBytes is the longest scrubbed text.
	maxMessageBytes = 1024
	// maxScrubPasses bounds the passes that make sure no token is left.
	maxScrubPasses = 4
)

// userinfoPattern matches the scheme and the user information of a URL, and ${1} keeps
// the scheme. It is the pattern of internal/sandboxes' userinfoPattern, repeated here
// because hub's packages do not import each other for it.
var userinfoPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://)[^/\s?#]*@`)

// scrub makes the text of a forge failure safe to return to a caller and to log: it
// replaces every occurrence of the token with [redacted], removes URL user information,
// and only then cuts the text to at most 1024 bytes on a UTF-8 boundary, so that the cut
// can never split the token and leave a part of it in.
//
// Taking out user information can join the text on either side of it into the token, so
// the replacing and the removing repeat until the text no longer changes. If the token is
// still in the text after the last pass, nothing but the marker is returned. A token that
// is part of the marker itself (a single "e", say) cannot be kept out of the output,
// because the marker holds it; credentials are not that short.
func scrub(text, token string) string {
	text = strings.ToValidUTF8(text, string(utf8.RuneError))
	text = scrubPass(text, token)
	if token != "" && !strings.Contains(redactedMarker, token) {
		for range maxScrubPasses {
			next := scrubPass(text, token)
			if next == text {
				break
			}
			text = next
		}
		if strings.Contains(text, token) {
			return redactedMarker
		}
	}
	return truncateUTF8(text, maxMessageBytes)
}

// scrubPass replaces the token and removes the URL user information once.
func scrubPass(text, token string) string {
	if token != "" {
		text = strings.ReplaceAll(text, token, redactedMarker)
	}
	return userinfoPattern.ReplaceAllString(text, "${1}")
}

// truncateUTF8 cuts s to at most limit bytes without splitting a rune. s must be valid
// UTF-8.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	n := limit
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
