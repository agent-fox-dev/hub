package scrub

import (
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// userinfoInURL finds the user information of a URL.
var userinfoInURL = regexp.MustCompile(`(?i)[a-z][a-z0-9+.\-]*://[^/\s?#]*@`)

// TS-30-45: Forwarded forge messages never contain the token or userinfo and stay
// within 1024 valid UTF-8 bytes.
// Requirement: 30-REQ-9.1
func TestScrubProperty_TS_30_45(t *testing.T) {
	rng := rand.New(rand.NewPCG(30, 45))
	const alphabet = "abcdefghijklmnopqrstuvwxyz ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.~:/@?#é世界😀\n\t"
	runes := []rune(alphabet)
	word := func(max int) string {
		out := make([]rune, rng.IntN(max))
		for i := range out {
			out[i] = runes[rng.IntN(len(runes))]
		}
		return string(out)
	}
	url := func(token string) string {
		scheme := []string{"https", "http", "ssh", "git+ssh", "ftp"}[rng.IntN(5)]
		user := word(8)
		if rng.IntN(3) == 0 {
			user += token
		}
		return scheme + "://" + user + ":" + word(8) + "@" + []string{"github.com", "h.example:8443"}[rng.IntN(2)] + "/x"
	}

	for i := range 3000 {
		token := "T" + word(30) + "z9"
		var parts []string
		for range 2 + rng.IntN(8) {
			switch rng.IntN(4) {
			case 0:
				parts = append(parts, token)
			case 1:
				parts = append(parts, url(token))
			case 2:
				parts = append(parts, string([]byte{0xff, 0xfe}))
			default:
				parts = append(parts, word(80))
			}
		}
		short := strings.Join(parts, " ")
		long := short + strings.Repeat(word(40)+token, 40)

		for name, msg := range map[string]string{"short": short, "long": long} {
			out := Text(msg, token)
			switch {
			case strings.Contains(out, token):
				t.Fatalf("%d %s: the output holds the token: %q", i, name, out)
			case userinfoInURL.MatchString(out):
				t.Fatalf("%d %s: the output holds userinfo: %q", i, name, out)
			case !utf8.ValidString(out):
				t.Fatalf("%d %s: the output is not valid UTF-8: %q", i, name, out)
			case len(out) > 1024:
				t.Fatalf("%d %s: the output is %d bytes", i, name, len(out))
			}
		}

		// Without a URL in it, a short text keeps the marker where the token was.
		var plain []string
		for range 2 + rng.IntN(6) {
			plain = append(plain, strings.ReplaceAll(word(80), ":", ""))
		}
		plain = append(plain, token)
		in := strings.Join(plain, " ")
		out := Text(in, token)
		if len(in) < 900 && (!strings.Contains(out, "[redacted]") || strings.Contains(out, token)) {
			t.Fatalf("%d: the token is not replaced with [redacted]: %q -> %q", i, in, out)
		}
	}
}

// scrub on cases that are easy to get wrong.
// Requirement: 30-REQ-9.1
func TestScrub(t *testing.T) {
	cases := []struct {
		name, text, token, want string
	}{
		{"token", "token=abc123def failed", "abc123def", "token=[redacted] failed"},
		{"every occurrence", "abc123def abc123def", "abc123def", "[redacted] [redacted]"},
		{"userinfo", "GET https://user:pass@github.com/x failed", "", "GET https://github.com/x failed"},
		{"token alone in userinfo", "https://abc123def@host/x", "abc123def", "https://host/x"},
		{"two urls", "ssh://a:b@h1/x and https://c@h2/y", "", "ssh://h1/x and https://h2/y"},
		{"at sign without a scheme", "mail bob@example.com", "", "mail bob@example.com"},
		{"at sign in the path", "https://host/a@b", "", "https://host/a@b"},
		{"empty token", "nothing to hide", "", "nothing to hide"},
		{"nothing", "", "abc123def", ""},
		{"invalid UTF-8", "bad \xff byte", "", "bad \uFFFD byte"},
		// Removing user information joins what was around it: here into the token.
		{"token formed by removing userinfo", "x://junk@y", "x://y", "[redacted]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text(tc.text, tc.token); got != tc.want {
				t.Errorf("Text(%q, %q) = %q; want %q", tc.text, tc.token, got, tc.want)
			}
		})
	}

	// Each replacement of the token a[ joins the marker to the a before it, which is the token
	// again. Within the passes the text settles around the marker; past them nothing but the
	// marker is returned. Either way the token is gone.
	t.Run("a token the marker exposes again", func(t *testing.T) {
		for _, text := range []string{"xaa[y", "xaaaa[y"} {
			within := Text(text, "a[")
			if strings.Contains(within, "a[") || within == "[redacted]" || !strings.HasPrefix(within, "x[redacted]") {
				t.Errorf("Text(%q) = %q; want the text kept around the marker, without the token", text, within)
			}
		}
		if past := Text("x"+strings.Repeat("a", 2*maxPasses)+"[y", "a["); past != "[redacted]" {
			t.Errorf("scrub past the passes = %q; want the marker alone", past)
		}
	})

	t.Run("cut on a rune boundary", func(t *testing.T) {
		text := strings.Repeat("é", 700) // 1400 bytes
		got := Text(text, "")
		if len(got) != 1024 || !utf8.ValidString(got) {
			t.Errorf("len %d, valid %v; want 1024 valid bytes", len(got), utf8.ValidString(got))
		}
		got = Text("a"+text, "") // the cut falls inside a rune
		if len(got) != 1023 || !utf8.ValidString(got) {
			t.Errorf("len %d, valid %v; want 1023 valid bytes", len(got), utf8.ValidString(got))
		}
	})

	t.Run("the cut does not leave part of the token", func(t *testing.T) {
		token := "SECRET-TOKEN-VALUE"
		text := strings.Repeat("x", 1020) + token + strings.Repeat("y", 50)
		got := Text(text, token)
		if len(got) > 1024 || strings.Contains(got, token) || strings.Contains(got, "SECRET") {
			t.Errorf("got %d bytes holding part of the token: ...%q", len(got), got[len(got)-30:])
		}
	})
}
