package workspace

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ValidateSlug tests
// ---------------------------------------------------------------------------

func TestValidateSlug_ValidSlugs(t *testing.T) {
	valid := []string{
		"abc",                        // minimum length (3 chars)
		"my-workspace-123",           // typical slug with hyphens and digits
		"a" + strings.Repeat("b", 63), // maximum length (64 chars)
		"hello",
		"test-workspace",
		"a1b",
		"workspace-with-many-hyphens-in-it",
	}
	for _, slug := range valid {
		if err := ValidateSlug(slug); err != nil {
			t.Errorf("ValidateSlug(%q) returned error: %v; want nil", slug, err)
		}
	}
}

func TestValidateSlug_TooShort(t *testing.T) {
	// 2-char slug (below minimum of 3) — TS-04-E1
	if err := ValidateSlug("ab"); err == nil {
		t.Error("ValidateSlug(\"ab\") = nil; want error for 2-char slug")
	}
}

func TestValidateSlug_TooLong(t *testing.T) {
	// 65-char slug (above maximum of 64) — TS-04-E1
	slug65 := "a" + strings.Repeat("b", 63) + "c"
	if len(slug65) != 65 {
		t.Fatalf("expected 65-char slug, got %d", len(slug65))
	}
	if err := ValidateSlug(slug65); err == nil {
		t.Errorf("ValidateSlug(%d chars) = nil; want error", len(slug65))
	}
}

func TestValidateSlug_BoundaryLengths(t *testing.T) {
	// 3-char (minimum) should succeed — TS-04-E1
	if err := ValidateSlug("abc"); err != nil {
		t.Errorf("ValidateSlug(\"abc\") = %v; want nil (3-char boundary)", err)
	}

	// 64-char (maximum) should succeed — TS-04-E1
	slug64 := "a" + strings.Repeat("b", 63)
	if len(slug64) != 64 {
		t.Fatalf("expected 64-char slug, got %d", len(slug64))
	}
	if err := ValidateSlug(slug64); err != nil {
		t.Errorf("ValidateSlug(%d chars) = %v; want nil (64-char boundary)", len(slug64), err)
	}
}

func TestValidateSlug_StartsWithDigit(t *testing.T) {
	// TS-04-E3
	if err := ValidateSlug("1workspace"); err == nil {
		t.Error("ValidateSlug(\"1workspace\") = nil; want error for digit start")
	}
}

func TestValidateSlug_StartsWithHyphen(t *testing.T) {
	// TS-04-E3
	if err := ValidateSlug("-workspace"); err == nil {
		t.Error("ValidateSlug(\"-workspace\") = nil; want error for hyphen start")
	}
}

func TestValidateSlug_TrailingHyphen(t *testing.T) {
	// TS-04-E2
	if err := ValidateSlug("my-workspace-"); err == nil {
		t.Error("ValidateSlug(\"my-workspace-\") = nil; want error for trailing hyphen")
	}
}

func TestValidateSlug_UppercaseLetters(t *testing.T) {
	// TS-04-2
	if err := ValidateSlug("Invalid-Slug"); err == nil {
		t.Error("ValidateSlug(\"Invalid-Slug\") = nil; want error for uppercase")
	}
}

func TestValidateSlug_SpecialCharacters(t *testing.T) {
	invalid := []string{
		"my_workspace",    // underscore
		"my.workspace",    // dot
		"my workspace",    // space
		"my!workspace",    // exclamation
		"my@workspace",    // at sign
		"Invalid-Slug!",   // mixed invalid
	}
	for _, slug := range invalid {
		if err := ValidateSlug(slug); err == nil {
			t.Errorf("ValidateSlug(%q) = nil; want error for special characters", slug)
		}
	}
}

func TestValidateSlug_EmptyString(t *testing.T) {
	if err := ValidateSlug(""); err == nil {
		t.Error("ValidateSlug(\"\") = nil; want error for empty string")
	}
}

// ---------------------------------------------------------------------------
// ValidateGitURL tests
// ---------------------------------------------------------------------------

func TestValidateGitURL_HTTPS(t *testing.T) {
	// TS-04-4
	valid := []string{
		"https://github.com/org/repo.git",
		"https://gitlab.com/group/project",
		"https://example.com/repo",
	}
	for _, url := range valid {
		if err := ValidateGitURL(url); err != nil {
			t.Errorf("ValidateGitURL(%q) = %v; want nil", url, err)
		}
	}
}

func TestValidateGitURL_SCPStyleSSH(t *testing.T) {
	// TS-04-5
	valid := []string{
		"git@github.com:org/repo.git",
		"git@gitlab.com:group/project.git",
		"git@bitbucket.org:team/repo.git",
	}
	for _, url := range valid {
		if err := ValidateGitURL(url); err != nil {
			t.Errorf("ValidateGitURL(%q) = %v; want nil", url, err)
		}
	}
}

func TestValidateGitURL_RejectedSchemes(t *testing.T) {
	// TS-04-6
	rejected := []string{
		"ssh://github.com/org/repo.git",
		"git://github.com/org/repo.git",
		"http://github.com/org/repo.git",
		"ftp://github.com/org/repo.git",
	}
	for _, url := range rejected {
		if err := ValidateGitURL(url); err == nil {
			t.Errorf("ValidateGitURL(%q) = nil; want error for rejected scheme", url)
		}
	}
}

func TestValidateGitURL_ExceedsMaxLength(t *testing.T) {
	// TS-04-7, TS-04-E4
	prefix := "https://github.com/org/"
	suffix := ".git"
	filler := strings.Repeat("a", 2049-len(prefix)-len(suffix))
	longURL := prefix + filler + suffix
	if len(longURL) != 2049 {
		t.Fatalf("expected 2049-char URL, got %d", len(longURL))
	}
	if err := ValidateGitURL(longURL); err == nil {
		t.Error("ValidateGitURL(2049 chars) = nil; want error for exceeding max length")
	}
}

func TestValidateGitURL_ExactMaxLength(t *testing.T) {
	// TS-04-E5
	prefix := "https://github.com/org/"
	suffix := ".git"
	filler := strings.Repeat("a", 2048-len(prefix)-len(suffix))
	exactURL := prefix + filler + suffix
	if len(exactURL) != 2048 {
		t.Fatalf("expected 2048-char URL, got %d", len(exactURL))
	}
	if err := ValidateGitURL(exactURL); err != nil {
		t.Errorf("ValidateGitURL(2048 chars) = %v; want nil (exact boundary)", err)
	}
}

func TestValidateGitURL_EmptyString(t *testing.T) {
	if err := ValidateGitURL(""); err == nil {
		t.Error("ValidateGitURL(\"\") = nil; want error")
	}
}

func TestValidateGitURL_RandomString(t *testing.T) {
	if err := ValidateGitURL("not-a-url"); err == nil {
		t.Error("ValidateGitURL(\"not-a-url\") = nil; want error")
	}
}

// ---------------------------------------------------------------------------
// ValidateBranch tests
// ---------------------------------------------------------------------------

func TestValidateBranch_Nil(t *testing.T) {
	// TS-04-8: omitted or null
	if err := ValidateBranch(nil); err != nil {
		t.Errorf("ValidateBranch(nil) = %v; want nil", err)
	}
}

func TestValidateBranch_ValidString(t *testing.T) {
	// TS-04-9
	b := "feature/my-branch"
	if err := ValidateBranch(&b); err != nil {
		t.Errorf("ValidateBranch(%q) = %v; want nil", b, err)
	}
}

func TestValidateBranch_Max255(t *testing.T) {
	b := strings.Repeat("a", 255)
	if err := ValidateBranch(&b); err != nil {
		t.Errorf("ValidateBranch(255 chars) = %v; want nil", err)
	}
}

func TestValidateBranch_Exceeds255(t *testing.T) {
	// TS-04-10
	b := strings.Repeat("a", 256)
	if err := ValidateBranch(&b); err == nil {
		t.Error("ValidateBranch(256 chars) = nil; want error for exceeding max length")
	}
}

func TestValidateBranch_WhitespaceVariants(t *testing.T) {
	// TS-04-11
	whitespaceTests := []struct {
		name  string
		value string
	}{
		{"space", "my branch"},
		{"tab", "my\tbranch"},
		{"newline", "my\nbranch"},
		{"carriage return", "my\rbranch"},
	}
	for _, tc := range whitespaceTests {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.value
			if err := ValidateBranch(&v); err == nil {
				t.Errorf("ValidateBranch(%q) = nil; want error for %s", tc.value, tc.name)
			}
		})
	}
}

func TestValidateBranch_EmptyString(t *testing.T) {
	// TS-04-E6: empty string passes (no whitespace, within length)
	b := ""
	if err := ValidateBranch(&b); err != nil {
		t.Errorf("ValidateBranch(\"\") = %v; want nil (empty string is valid per rules)", err)
	}
}
