package workspace

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// redactedMarker takes the place of the credential's secret in scrubbed text.
	redactedMarker = "[redacted]"
	// maxScrubbedBytes is the longest scrubbed text.
	maxScrubbedBytes = 1024
	// maxScrubPasses bounds the passes that make sure no secret is left.
	maxScrubPasses = 8
)

// userinfoPattern matches the scheme and the user information of a URL, and ${1}
// keeps the scheme. It is the pattern of internal/forge's and internal/sandboxes'
// userinfoPattern, repeated here because hub's packages do not import each other
// for it.
var userinfoPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://)[^/\s?#]*@`)

// scrubText makes text safe to return to a caller, to log and to record in an
// audit event: it replaces every occurrence of secret, the password or token of the
// workspace's git credential, with [redacted], removes URL user information, and
// cuts the text to at most 1024 bytes on a UTF-8 boundary only after that, so that
// the cut can never split the secret and leave a part of it in.
//
// Removing user information can join the text on either side of it into the
// secret, so the two steps repeat until the text no longer changes. If the secret
// is still in the text after the last pass, nothing but the marker is returned. A
// secret that is part of the marker itself cannot be kept out of the output;
// credentials are not that short.
func scrubText(text, secret string) string {
	text = strings.ToValidUTF8(text, string(utf8.RuneError))
	text = scrubTextPass(text, secret)
	if secret != "" && !strings.Contains(redactedMarker, secret) {
		for range maxScrubPasses {
			next := scrubTextPass(text, secret)
			if next == text {
				break
			}
			text = next
		}
		if strings.Contains(text, secret) {
			return redactedMarker
		}
	}
	return truncateScrubbed(text, maxScrubbedBytes)
}

// scrubTextPass replaces the secret and removes the URL user information once.
func scrubTextPass(text, secret string) string {
	if secret != "" {
		text = strings.ReplaceAll(text, secret, redactedMarker)
	}
	return userinfoPattern.ReplaceAllString(text, "${1}")
}

// truncateScrubbed cuts s to at most limit bytes without splitting a rune. s must
// be valid UTF-8.
func truncateScrubbed(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	n := limit
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
