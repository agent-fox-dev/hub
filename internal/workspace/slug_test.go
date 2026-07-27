package workspace

import (
	"regexp"
	"testing"
)

// apikitSlugPattern is the regex used by apikit's validateSlug to validate
// organization slugs: lowercase alphanumeric start and end, middle may
// include hyphens and underscores, length 1–128.
// Copied from apikit/internal/handlers/orgs.go for test-side verification.
var apikitSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*[a-z0-9]$`)

// apikitValidateSlug mirrors apikit's validateSlug for test assertions.
// This allows the hub tests to verify slug compliance without importing
// apikit's internal/handlers package.
func apikitValidateSlug(slug string) bool {
	if len(slug) == 0 || len(slug) > 128 {
		return false
	}
	// A single-character slug must be [a-z0-9].
	if len(slug) == 1 {
		c := slug[0]
		return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
	}
	return apikitSlugPattern.MatchString(slug)
}

// ---------------------------------------------------------------------------
// TS-04-17: Verify that the slug sanitizer lowercases the entire username
//           before further processing.
// Requirement: 04-REQ-5.1
// ---------------------------------------------------------------------------
func TestSanitizeSlug_Lowercase(t *testing.T) {
	slug := sanitizeSlug("Alice", "uid-001")
	if slug != "alice" {
		t.Errorf("sanitizeSlug(%q, %q) = %q; want %q", "Alice", "uid-001", slug, "alice")
	}
}

// ---------------------------------------------------------------------------
// TS-04-18: Verify that characters not in [a-z0-9_-] are replaced with
//           hyphens during slug sanitization.
// Requirement: 04-REQ-5.2
// ---------------------------------------------------------------------------
func TestSanitizeSlug_InvalidCharsReplacedWithHyphen(t *testing.T) {
	slug := sanitizeSlug("alice@example", "uid-001")
	if slug != "alice-example" {
		t.Errorf("sanitizeSlug(%q, %q) = %q; want %q", "alice@example", "uid-001", slug, "alice-example")
	}
}

// ---------------------------------------------------------------------------
// TS-04-19: Verify that consecutive hyphens are collapsed into a single
//           hyphen during sanitization.
// Requirement: 04-REQ-5.3
// ---------------------------------------------------------------------------
func TestSanitizeSlug_ConsecutiveHyphensCollapsed(t *testing.T) {
	slug := sanitizeSlug("alice--bob", "uid-001")
	if slug != "alice-bob" {
		t.Errorf("sanitizeSlug(%q, %q) = %q; want %q", "alice--bob", "uid-001", slug, "alice-bob")
	}
}

// ---------------------------------------------------------------------------
// TS-04-20: Verify that leading and trailing hyphens and underscores are
//           trimmed from the sanitized slug.
// Requirement: 04-REQ-5.4
// ---------------------------------------------------------------------------
func TestSanitizeSlug_LeadingTrailingTrimmed(t *testing.T) {
	slug := sanitizeSlug("-alice_", "uid-001")
	if slug != "alice" {
		t.Errorf("sanitizeSlug(%q, %q) = %q; want %q", "-alice_", "uid-001", slug, "alice")
	}
}

// ---------------------------------------------------------------------------
// TS-04-21: Verify that a sanitized slug starting with a digit gets the 'u-'
//           prefix prepended.
// Requirement: 04-REQ-5.5
// ---------------------------------------------------------------------------
func TestSanitizeSlug_DigitLeadingGetsPrefixed(t *testing.T) {
	slug := sanitizeSlug("42alice", "uid-001")
	if slug != "u-42alice" {
		t.Errorf("sanitizeSlug(%q, %q) = %q; want %q", "42alice", "uid-001", slug, "u-42alice")
	}
}

// ---------------------------------------------------------------------------
// TS-04-22: Verify that when the sanitized slug is shorter than 2 characters,
//           the fallback u-<first 8 chars of userID> is used.
// Requirement: 04-REQ-5.6
// ---------------------------------------------------------------------------
func TestSanitizeSlug_ShortFallback(t *testing.T) {
	slug := sanitizeSlug("a", "abcdefgh-1234")
	if slug != "u-abcdefgh" {
		t.Errorf("sanitizeSlug(%q, %q) = %q; want %q", "a", "abcdefgh-1234", slug, "u-abcdefgh")
	}
}

// ---------------------------------------------------------------------------
// TS-04-E6: Verify that a username consisting entirely of special characters
//           results in the fallback slug u-<first 8 chars of userID>.
// Requirement: 04-REQ-5.E1
// ---------------------------------------------------------------------------
func TestSanitizeSlug_AllSpecialCharsFallback(t *testing.T) {
	slug := sanitizeSlug("!!!", "abcdefgh-xyz")
	if slug != "u-abcdefgh" {
		t.Errorf("sanitizeSlug(%q, %q) = %q; want %q", "!!!", "abcdefgh-xyz", slug, "u-abcdefgh")
	}
}

// ---------------------------------------------------------------------------
// TS-04-E7: Verify that a username resulting in a single character after
//           sanitization triggers the fallback slug.
// Requirement: 04-REQ-5.E2
// ---------------------------------------------------------------------------
func TestSanitizeSlug_SingleCharAfterTrimFallback(t *testing.T) {
	slug := sanitizeSlug("a!", "abcdefgh-000")
	if slug != "u-abcdefgh" {
		t.Errorf("sanitizeSlug(%q, %q) = %q; want %q", "a!", "abcdefgh-000", slug, "u-abcdefgh")
	}
}

// ---------------------------------------------------------------------------
// TS-04-E8: Verify that a username that is a single digit triggers the
//           fallback slug.
// Requirement: 04-REQ-5.E3
// ---------------------------------------------------------------------------
func TestSanitizeSlug_SingleDigitFallback(t *testing.T) {
	slug := sanitizeSlug("7", "abcdefgh-111")
	if slug != "u-abcdefgh" {
		t.Errorf("sanitizeSlug(%q, %q) = %q; want %q", "7", "abcdefgh-111", slug, "u-abcdefgh")
	}
}

// ---------------------------------------------------------------------------
// TS-04-P5: Property test — for any username string, the slug sanitizer
//           always produces a result that matches the validateSlug regex
//           ^[a-z0-9][a-z0-9_-]*[a-z0-9]$ (minimum 2 chars).
// Property: 04-PROP-5
// Validates: 04-REQ-5.1, 04-REQ-5.2, 04-REQ-5.3, 04-REQ-5.4, 04-REQ-5.5,
//            04-REQ-5.6
// ---------------------------------------------------------------------------
func TestSanitizeSlug_PropertyAlwaysValid(t *testing.T) {
	// A fixed userID used across all property test cases. The first 8 chars
	// ("abcdef01") form a valid slug suffix for the fallback path.
	const userID = "abcdef01-2345-6789-abcd-ef0123456789"

	// Table-driven cases exercising arbitrary Unicode strings, edge cases,
	// control characters, emoji, mixed scripts, etc.
	cases := []struct {
		name     string
		username string
		userID   string
	}{
		// Empty and whitespace
		{"empty string", "", userID},
		{"single space", " ", userID},
		{"only whitespace", "   \t\n", userID},

		// Pure ASCII specials
		{"all punctuation", "!@#$%^&*()", userID},
		{"dots and slashes", "user.name/path\\back", userID},
		{"tildes and equals", "~~~===~~~", userID},

		// Single characters
		{"single letter", "x", userID},
		{"single digit", "5", userID},
		{"single underscore", "_", userID},
		{"single hyphen", "-", userID},
		{"single special", "@", userID},

		// Digit-leading usernames
		{"digit prefix", "1foo", userID},
		{"all digits", "123456789", userID},
		{"digit-hyphen-digit", "1-2-3", userID},

		// Unicode
		{"cyrillic", "Привет", userID},
		{"chinese", "你好世界", userID},
		{"arabic", "مرحبا", userID},
		{"emoji", "\U0001f600\U0001f680\U0001f30d", userID},
		{"mixed script latin+accents", "aliceéèê", userID},
		{"japanese katakana", "テスト", userID},
		{"devanagari", "नमस्ते", userID},

		// Mixed valid and invalid
		{"leading hyphens", "---alice", userID},
		{"trailing underscores", "alice___", userID},
		{"mixed prefix suffix", "__--alice--__", userID},
		{"consecutive specials", "a!!!b", userID},
		{"spaces in middle", "alice bob", userID},
		{"tabs in middle", "alice\tbob", userID},

		// Long strings
		{"200 char username", string(make([]byte, 200)), userID},
		{"long valid name", "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz", userID},

		// Short userIDs (exercise fallback truncation)
		{"short userID 8 chars", "!", "abcdefgh"},
		{"short userID exactly 8", "!", "12345678"},
		{"userID shorter than 8", "!", "abc"},

		// Case variations
		{"all uppercase", "ALICE", userID},
		{"mixed case", "AlIcE_BoB", userID},
		{"camelCase", "myUserName", userID},

		// Already valid slugs
		{"already valid 2-char", "ab", userID},
		{"already valid long", "alice-bob-charlie", userID},
		{"underscores valid", "alice_bob", userID},

		// Null bytes and control characters
		{"null byte", "alice\x00bob", userID},
		{"bell char", "alice\x07bob", userID},
		{"backspace", "alice\x08bob", userID},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slug := sanitizeSlug(tc.username, tc.userID)

			// Invariant 1: slug must be at least 2 characters.
			if len(slug) < 2 {
				t.Errorf("sanitizeSlug(%q, %q) = %q; length %d < 2",
					tc.username, tc.userID, slug, len(slug))
				return
			}

			// Invariant 2: slug must match apikit's validateSlug pattern.
			if !apikitValidateSlug(slug) {
				t.Errorf("sanitizeSlug(%q, %q) = %q; does not pass apikit validateSlug",
					tc.username, tc.userID, slug)
			}

			// Invariant 3: slug must contain only [a-z0-9_-] characters.
			for i, r := range slug {
				if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
					t.Errorf("sanitizeSlug(%q, %q) = %q; char at index %d is %q (not in [a-z0-9_-])",
						tc.username, tc.userID, slug, i, string(r))
				}
			}

			// Invariant 4: no consecutive hyphens.
			for i := 1; i < len(slug); i++ {
				if slug[i] == '-' && slug[i-1] == '-' {
					t.Errorf("sanitizeSlug(%q, %q) = %q; contains consecutive hyphens at index %d",
						tc.username, tc.userID, slug, i)
				}
			}

			// Invariant 5: must not start or end with hyphen or underscore.
			first := slug[0]
			if first == '-' || first == '_' {
				t.Errorf("sanitizeSlug(%q, %q) = %q; starts with %q",
					tc.username, tc.userID, slug, string(first))
			}
			last := slug[len(slug)-1]
			if last == '-' || last == '_' {
				t.Errorf("sanitizeSlug(%q, %q) = %q; ends with %q",
					tc.username, tc.userID, slug, string(last))
			}

			// Invariant 6: first character must be a letter (not a digit).
			if slug[0] >= '0' && slug[0] <= '9' {
				t.Errorf("sanitizeSlug(%q, %q) = %q; starts with digit %q (should have u- prefix or fallback)",
					tc.username, tc.userID, slug, string(slug[0]))
			}
		})
	}
}

// TestSanitizeSlug_PropertyFuzz exercises sanitizeSlug with programmatically
// generated inputs to catch invariant violations the table-driven cases miss.
// This complements the table-driven property test above.
func TestSanitizeSlug_PropertyFuzz(t *testing.T) {
	const userID = "abcdef01-2345-6789-abcd-ef0123456789"

	// Generate strings from various Unicode ranges.
	var inputs []string

	// All printable ASCII.
	for r := rune(0x20); r < 0x7f; r++ {
		inputs = append(inputs, string(r))
	}

	// Two-char combos that exercise edge transitions.
	edges := []rune{'-', '_', '0', '9', 'a', 'z', 'A', 'Z', '@', '.', ' ', '!'}
	for _, a := range edges {
		for _, b := range edges {
			inputs = append(inputs, string([]rune{a, b}))
		}
	}

	// Some multi-char patterns from various Unicode blocks.
	for i := 0; i < 50; i++ {
		var rs []rune
		for j := 0; j <= i; j++ {
			rs = append(rs, rune(0x100*i+j))
		}
		inputs = append(inputs, string(rs))
	}

	for _, input := range inputs {
		slug := sanitizeSlug(input, userID)

		if len(slug) < 2 {
			t.Errorf("sanitizeSlug(%q, %q) = %q; length %d < 2",
				truncateStr(input, 40), userID, slug, len(slug))
			continue
		}

		if !apikitValidateSlug(slug) {
			t.Errorf("sanitizeSlug(%q, %q) = %q; does not pass apikit validateSlug",
				truncateStr(input, 40), userID, slug)
		}
	}
}

// truncateStr shortens a string to maxLen runes for test output readability.
func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// TestSanitizeSlug_CombinedTransformations verifies that multiple
// sanitization rules compose correctly in a single input.
func TestSanitizeSlug_CombinedTransformations(t *testing.T) {
	tests := []struct {
		name     string
		username string
		userID   string
		want     string
	}{
		{
			name:     "uppercase + special char + consecutive hyphens",
			username: "Alice@@Bob",
			userID:   "uid-001",
			want:     "alice-bob",
		},
		{
			name:     "leading special + uppercase + trailing special",
			username: "!Alice!",
			userID:   "uid-001",
			want:     "alice",
		},
		{
			name:     "digit start after trim",
			username: "-42alice",
			userID:   "uid-001",
			want:     "u-42alice",
		},
		{
			name:     "all rules at once",
			username: "--9@A!!b--",
			userID:   "uid-001",
			want:     "u-9-a-b",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			slug := sanitizeSlug(tc.username, tc.userID)
			if slug != tc.want {
				t.Errorf("sanitizeSlug(%q, %q) = %q; want %q",
					tc.username, tc.userID, slug, tc.want)
			}
		})
	}
}

// TestSanitizeSlug_FallbackUserIDTruncation verifies that the fallback path
// correctly takes only the first 8 characters of the userID.
func TestSanitizeSlug_FallbackUserIDTruncation(t *testing.T) {
	tests := []struct {
		name     string
		username string
		userID   string
		want     string
	}{
		{
			name:     "standard UUID-style userID",
			username: "!",
			userID:   "abcdefgh-1234-5678",
			want:     "u-abcdefgh",
		},
		{
			name:     "exactly 8 char userID",
			username: "!",
			userID:   "abcdefgh",
			want:     "u-abcdefgh",
		},
		{
			name:     "long userID",
			username: "",
			userID:   "abcdefghijklmnopqrstuvwxyz",
			want:     "u-abcdefgh",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			slug := sanitizeSlug(tc.username, tc.userID)
			if slug != tc.want {
				t.Errorf("sanitizeSlug(%q, %q) = %q; want %q",
					tc.username, tc.userID, slug, tc.want)
			}
		})
	}
}

// TestSanitizeSlug_PreservesUnderscores verifies that underscores within a
// slug are preserved (they are valid in apikit's slug pattern), while leading
// and trailing underscores are trimmed.
func TestSanitizeSlug_PreservesUnderscores(t *testing.T) {
	slug := sanitizeSlug("alice_bob", "uid-001")
	if slug != "alice_bob" {
		t.Errorf("sanitizeSlug(%q, %q) = %q; want %q", "alice_bob", "uid-001", slug, "alice_bob")
	}
}

// init-time check: ensure the test-local apikitValidateSlug function behaves
// consistently with the documented spec pattern. This guards against copy
// errors in the test helper.
func init() {
	// These must pass.
	for _, slug := range []string{"ab", "alice", "alice-bob", "u-42alice", "u-abcdefgh", "a1"} {
		if !apikitValidateSlug(slug) {
			panic("apikitValidateSlug rejected known-good slug: " + slug)
		}
	}
	// These must fail.
	for _, slug := range []string{"", "-a", "a-", "A", "alice bob"} {
		if apikitValidateSlug(slug) {
			panic("apikitValidateSlug accepted known-bad slug: " + slug)
		}
	}
}
