package scrub

import (
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"
)

// TS-27-54: scrub replaces secret and environment values, removes URL user
// information, and only then truncates.
// Requirement: 27-REQ-9.1
func TestScrub_ReplacesRemovesAndTruncates_TS_27_54(t *testing.T) {
	t.Run("a secret and URL user information", func(t *testing.T) {
		got := Text("pull https://u:tok@reg failed: -e K=s3cret", "s3cret")
		if want := "pull https://reg failed: -e K=[redacted]"; got != want {
			t.Errorf("scrub = %q; want %q", got, want)
		}
	})

	t.Run("an environment value", func(t *testing.T) {
		got := Text("starting with SECRET_ENV=envval now", "s3cret", "envval")
		if want := "starting with SECRET_ENV=[redacted] now"; got != want {
			t.Errorf("scrub = %q; want %q", got, want)
		}
	})

	t.Run("every occurrence, and the longer value first", func(t *testing.T) {
		got := Text("abcdef abc abcdef", "abc", "abcdef")
		if want := "[redacted] [redacted] [redacted]"; got != want {
			t.Errorf("scrub = %q; want %q", got, want)
		}
	})

	t.Run("empty values change nothing", func(t *testing.T) {
		const text = "an engine error with no secret in it"
		if got := Text(text, ""); got != text {
			t.Errorf("scrub with an empty value = %q; want the text unchanged", got)
		}
		if got := Text(text); got != text {
			t.Errorf("scrub with no values = %q; want the text unchanged", got)
		}
	})

	t.Run("a long text is cut to at most 1024 bytes", func(t *testing.T) {
		got := Text(strings.Repeat("x", 5000))
		if len(got) != 1024 {
			t.Errorf("len(Text(5000 bytes)) = %d; want 1024", len(got))
		}
	})

	t.Run("a value on the cut is replaced before the cut", func(t *testing.T) {
		const secret = "topsecretvalue"
		text := strings.Repeat("x", 1020) + secret + strings.Repeat("y", 100)
		got := Text(text, secret)
		if strings.Contains(got, secret) || strings.Contains(got, "topsec") {
			t.Errorf("the output holds the value or a part of it: ...%s", got[len(got)-30:])
		}
		if !strings.HasPrefix(got, strings.Repeat("x", 1020)+"[red") {
			t.Errorf("the value was not replaced by the marker before the cut: ...%s", got[1000:])
		}
	})

	t.Run("the cut keeps whole runes", func(t *testing.T) {
		for pad := 1020; pad <= 1024; pad++ {
			got := Text(strings.Repeat("a", pad) + "日本語🙂")
			if !utf8.ValidString(got) || len(got) > 1024 {
				t.Errorf("pad %d: valid UTF-8 = %v, len = %d; want valid and at most 1024", pad, utf8.ValidString(got), len(got))
			}
		}
		if got := Text(strings.Repeat("a", 1023) + "日"); got != strings.Repeat("a", 1023) {
			t.Errorf("a rune that straddles byte 1024 was kept in part: len %d", len(got))
		}
	})

	t.Run("user information removed from a join cannot restore a value", func(t *testing.T) {
		// "ps://reg" is not in the text, but it is once the user information is gone.
		got := Text("fetch https://u@reg failed", "ps://reg")
		if want := "fetch htt[redacted] failed"; got != want {
			t.Errorf("scrub = %q; want %q: the value is replaced where taking out the user information made it appear", got, want)
		}
	})
}

// TS-27-55: For any text and any set of non-empty values, the output is valid
// UTF-8 of at most 1024 bytes and holds none of the values.
//
// A value that is part of the marker itself (a single "e", say) stays in the
// output as a part of "[redacted]", so the generated values use an alphabet
// without the characters of the marker. Real values are not that short.
// Requirement: 27-REQ-9.2
func TestScrub_Property_TS_27_55(t *testing.T) {
	const (
		valueAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789fghijklmnopqsuvwxyz-_.:/+=@é日🙂"
		iterations    = 3000
	)
	pieces := []string{"the quick brown fox ", "failed to start container ", "https://user:pw@registry.example/", "日本語", "é", "🙂", "\xff\xfe", "-e KEY=", "\n"}
	runes := []rune(valueAlphabet)

	rng := rand.New(rand.NewPCG(27, 55))
	randomValue := func() string {
		var b strings.Builder
		for range 1 + rng.IntN(40) {
			b.WriteRune(runes[rng.IntN(len(runes))])
		}
		return b.String()
	}

	for i := range iterations {
		values := make([]string, 1+rng.IntN(4))
		for j := range values {
			values[j] = randomValue()
		}
		var text strings.Builder
		for target := rng.IntN(3000); text.Len() < target; {
			if rng.IntN(3) == 0 {
				text.WriteString(values[rng.IntN(len(values))])
			} else {
				text.WriteString(pieces[rng.IntN(len(pieces))])
			}
		}
		in := text.String()
		if rng.IntN(2) == 0 && len(in) > 0 {
			// Put a value across the 1024-byte boundary.
			v := values[rng.IntN(len(values))]
			at := min(max(1024-rng.IntN(len(v)+1), 0), len(in))
			in = in[:at] + v + in[at:]
		}

		out := Text(in, values...)
		if !utf8.ValidString(out) {
			t.Fatalf("iteration %d: the output is not valid UTF-8: %q", i, out)
		}
		if len(out) > 1024 {
			t.Fatalf("iteration %d: len(output) = %d; want at most 1024", i, len(out))
		}
		for _, v := range values {
			if strings.Contains(out, v) {
				t.Fatalf("iteration %d: the output holds the value %q", i, v)
			}
		}
	}
}
