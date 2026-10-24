// Package scrub makes text that may carry a credential safe to log, store in
// an audit event or return to a caller. One implementation serves the forge
// client, the sandboxes, the branch push and the carry-patch push control.
package scrub

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// Redacted takes the place of a secret value in scrubbed text.
	Redacted = "[redacted]"
	// MaxBytes is the longest text Text returns.
	MaxBytes = 1024
	// maxPasses bounds the passes that make sure no value is left.
	maxPasses = 8
)

// userinfo matches the scheme and the user information of a URL embedded
// anywhere in a string: a scheme (RFC 3986), "://", then everything up to the
// last "@" before the first "/", "?", "#" or whitespace. Matching up to the
// last "@" removes passwords that themselves contain an unescaped "@". Group 1
// is the scheme and "://", which is kept.
var userinfo = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://)[^/\s?#]*@`)

// Text replaces every non-empty value with [redacted] (the longest first),
// removes URL user information, and only then cuts the text to at most
// MaxBytes on a UTF-8 boundary, so that the cut can never split a value and
// leave a part of it in.
//
// Taking out user information can join the text on either side of it into a
// value that was not there before, so the replacing and the removing repeat
// until the text no longer changes. If a value is still in the text after the
// last pass, nothing but the marker is returned. A value that is part of the
// marker itself (a single "e", say) is replaced once and cannot be kept out of
// the output after that, because the marker holds it; credentials are not that
// short.
func Text(text string, values ...string) string {
	all := distinct(values)
	text = strings.ToValidUTF8(text, string(utf8.RuneError))
	text = pass(text, all)

	// The passes after the first leave the marker alone: they look for the
	// values that are not part of it.
	again := slices.DeleteFunc(slices.Clone(all), func(v string) bool { return strings.Contains(Redacted, v) })
	for range maxPasses {
		next := pass(text, again)
		if next == text {
			break
		}
		text = next
	}
	for _, v := range again {
		if strings.Contains(text, v) {
			return Redacted
		}
	}
	return TruncateUTF8(text, MaxBytes)
}

// Userinfo removes the user information (user:password@) of every URL in
// free-form text, such as an error message that embeds a remote URL, and
// returns everything else byte for byte. Addresses without a scheme
// (user@host:path, e-mail addresses) are left alone: they cannot be told apart
// from ordinary text, and an ssh login name is not a secret. It is idempotent.
func Userinfo(text string) string {
	return userinfo.ReplaceAllString(text, "${1}")
}

// TruncateUTF8 cuts s to at most limit bytes without splitting a rune. s must
// be valid UTF-8.
func TruncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	n := limit
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// pass replaces the values and removes the URL user information once.
func pass(text string, values []string) string {
	if len(values) > 0 {
		pairs := make([]string, 0, 2*len(values))
		for _, v := range values {
			pairs = append(pairs, v, Redacted)
		}
		text = strings.NewReplacer(pairs...).Replace(text)
	}
	return Userinfo(text)
}

// distinct returns the non-empty values once each, the longest first, so that
// a value that holds another is replaced as a whole.
func distinct(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	slices.SortStableFunc(out, func(a, b string) int { return len(b) - len(a) })
	return out
}
