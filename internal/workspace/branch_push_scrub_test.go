package workspace

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// userinfoSegment matches a URL scheme followed by user information.
var userinfoSegment = regexp.MustCompile(`(?i)[a-z][a-z0-9+.\-]*://[^/\s?#]*@`)

// scrubPieces are the fragments the generated texts are made of: secrets,
// URLs with and without user information, multi-byte runes and the pieces of
// a split occurrence.
var scrubPieces = []string{
	"https://", "http://", "ssh://", "git@github.com:acme/w.git", "user:pw@", "u@", "@",
	"host.example/repo.git", " ", "/", "?", "#", ":", "日本", "é", "🦊", "\xff",
	"fatal: ", "push to ", "[redacted]", "tok", "en", "-", "x",
}

// genScrubCase builds a text and a secret from the pieces, placing the secret
// whole, split around user information, and inside user information.
func genScrubCase(r *rand.Rand) (string, string) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"
	secret := make([]byte, 6+r.Intn(30))
	for i := range secret {
		secret[i] = alphabet[r.Intn(len(alphabet))]
	}
	s := string(secret)
	// A secret holding "://" can be formed by removing user information: the
	// text "ab://u@cd" becomes "ab://cd".
	nested := ""
	if r.Intn(5) == 0 {
		cut := 1 + r.Intn(len(s)-1)
		s = "s" + s[:cut] + "://" + s[cut:]
		nested = s[:strings.Index(s, "://")+3] + "u:p@" + s[strings.Index(s, "://")+3:]
	}
	var b strings.Builder
	for n := r.Intn(60); n >= 0; n-- {
		if nested != "" && r.Intn(4) == 0 {
			b.WriteString(nested)
			continue
		}
		switch r.Intn(6) {
		case 0:
			b.WriteString(s)
		case 1:
			cut := 1 + r.Intn(len(s)-1)
			b.WriteString(s[:cut] + "https://u:p@" + s[cut:])
		case 2:
			b.WriteString("https://x-token-auth:" + s + "@host/r.git")
		default:
			b.WriteString(scrubPieces[r.Intn(len(scrubPieces))])
		}
	}
	if r.Intn(4) == 0 {
		b.WriteString(strings.Repeat("日本"+s, 200))
	}
	return b.String(), s
}

// TS-38-37: The scrub removes the secret and userinfo, is idempotent, valid
// UTF-8 and at most 1024 bytes.
// Requirement: 38-REQ-7.1
func TestScrubTextProperty_TS_38_37(t *testing.T) {
	r := rand.New(rand.NewSource(38))
	for i := 0; i < 2000; i++ {
		text, secret := genScrubCase(r)
		out := scrubText(text, secret)
		if strings.Contains(out, secret) {
			t.Fatalf("case %d: the output holds the secret %q: %.300q", i, secret, out)
		}
		if userinfoSegment.MatchString(out) {
			t.Fatalf("case %d: the output holds URL user information: %.300q", i, out)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("case %d: the output is not valid UTF-8: %.300q", i, out)
		}
		if len(out) > 1024 {
			t.Fatalf("case %d: the output is %d bytes; want at most 1024", i, len(out))
		}
		if again := scrubText(out, secret); again != out {
			t.Fatalf("case %d: scrubbing twice changed the text:\n%.300q\n%.300q", i, out, again)
		}
	}
}

// TS-38-37: the documented cases of the scrub, beside the generated ones.
// Requirement: 38-REQ-7.1
func TestScrubTextExamples_TS_38_37(t *testing.T) {
	for _, tc := range []struct{ text, secret, want string }{
		{"push to https://x-token-auth:tok-12345@github.com/a/b.git failed", "tok-12345",
			"push to https://github.com/a/b.git failed"},
		{"the forge said tok-12345", "tok-12345", "the forge said [redacted]"},
		{"tok-1https://u:p@2345 joined", "tok-12345", "tok-1https://2345 joined"},
		{"git@github.com:acme/widgets.git", "", "git@github.com:acme/widgets.git"},
	} {
		if got := scrubText(tc.text, tc.secret); got != tc.want {
			t.Errorf("scrubText(%q, %q) = %q; want %q", tc.text, tc.secret, got, tc.want)
		}
	}
	long := strings.Repeat("é", 600)
	if got := scrubText(long, ""); len(got) > 1024 || !utf8.ValidString(got) || len(got) != 1024 {
		t.Errorf("a 1200-byte text of two-byte runes was cut to %d bytes (valid %v); want 1024", len(got), utf8.ValidString(got))
	}
}
