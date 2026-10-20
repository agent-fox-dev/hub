package sandboxes

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// redactedMarker takes the place of a secret value in scrubbed text.
	redactedMarker = "[redacted]"
	// maxScrubbedBytes is the longest scrubbed text.
	maxScrubbedBytes = 1024
	// maxScrubPasses bounds the passes that make sure no value is left.
	maxScrubPasses = 4
)

// userinfoPattern matches the scheme and the user information of a URL, and
// ${1} keeps the scheme. It is the pattern of carrypatch's userinfoPattern,
// repeated here because hub's packages do not import each other for it.
var userinfoPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://)[^/\s?#]*@`)

// scrub makes the text of a provider error safe to store, log, put in an audit
// event or return to a caller. It replaces every non-empty value with
// [redacted] (the longest first), removes URL user information, and only then
// cuts the text to at most 1024 bytes on a UTF-8 boundary, so that the cut can
// never split a value and leave a part of it unscrubbed.
//
// Taking out user information can join the text on either side of it into a
// value that was not there before, so the replacing and the removing repeat
// until the text no longer changes. If a value is still in the text after the
// last pass, nothing but the marker is returned.
//
// A value that is part of the marker itself (a single "e", say) is replaced
// once and cannot be kept out of the output after that, because the marker
// holds it. Secrets and environment values are not that short.
func scrub(text string, values []string) string {
	all := distinctValues(values)
	text = strings.ToValidUTF8(text, string(utf8.RuneError))
	text = scrubPass(text, all)

	// The passes after the first leave the marker alone: they look for the
	// values that are not part of it.
	again := slices.DeleteFunc(slices.Clone(all), func(v string) bool { return strings.Contains(redactedMarker, v) })
	for range maxScrubPasses {
		next := scrubPass(text, again)
		if next == text {
			break
		}
		text = next
	}
	for _, v := range again {
		if strings.Contains(text, v) {
			return redactedMarker
		}
	}
	return truncateUTF8(text, maxScrubbedBytes)
}

// scrubPass replaces the values and removes the URL user information once.
func scrubPass(text string, values []string) string {
	if len(values) > 0 {
		pairs := make([]string, 0, 2*len(values))
		for _, v := range values {
			pairs = append(pairs, v, redactedMarker)
		}
		text = strings.NewReplacer(pairs...).Replace(text)
	}
	return userinfoPattern.ReplaceAllString(text, "${1}")
}

// distinctValues returns the non-empty values once each, the longest first, so
// that a value that holds another is replaced as a whole.
func distinctValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	slices.SortStableFunc(out, func(a, b string) int { return len(b) - len(a) })
	return out
}

// truncateUTF8 cuts s to at most limit bytes without splitting a rune. s must be
// valid UTF-8.
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
