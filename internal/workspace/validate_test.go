package workspace

import (
	"strings"
	"testing"
)

// TestValidate_Slug: 3 to 64 lowercase letters, digits and single hyphens,
// starting with a letter and not ending with a hyphen.
func TestValidate_Slug(t *testing.T) {
	for _, ok := range []string{"abc", "a-b", "a1b2", "abc-123-xyz", "a" + strings.Repeat("b", 62) + "c", "a1"[:2] + "x"} {
		if err := validateSlug(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for bad, want := range map[string]string{
		"ab": "at least 3", strings.Repeat("a", 65): "at most 64", "1ab": "lowercase letter", "-ab": "lowercase letter",
		"ab-": "end with a hyphen", "a--b": "consecutive hyphens", "aBc": "only lowercase", "a_b": "only lowercase", "a b": "only lowercase",
	} {
		if err := validateSlug(bad); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v; want %q", bad, err, want)
		}
	}
}

// TestValidate_GitURL: https://host/path or git@host:path, both parts
// non-empty, nothing else.
func TestValidate_GitURL(t *testing.T) {
	for _, ok := range []string{"https://github.com/org/repo", "https://h/p.git", "git@github.com:org/repo.git", "git@h:p"} {
		if err := validateGitURL(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for bad, want := range map[string]string{
		"": "must not be empty", "http://h/p": "not plain HTTP", "https://": "HTTPS format", "https://host": "HTTPS format",
		"https:///p": "HTTPS format", "git@host": "SSH format", "git@:p": "SSH format", "git@h:": "SSH format",
		"ssh://git@h/p": "HTTPS (https://) or SSH (git@)", "github.com/org/repo": "HTTPS (https://) or SSH (git@)",
	} {
		if err := validateGitURL(bad); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v; want %q", bad, err, want)
		}
	}
}

// TestValidate_Branch: git's ref name rules, including the option-like names.
func TestValidate_Branch(t *testing.T) {
	for _, ok := range []string{"main", "feature/x-1", "a.b", "v1.0", "release/2026.01", "UPPER", "with_underscore"} {
		if err := validateBranch(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for bad, want := range map[string]string{
		"": "must not be empty", "a b": "spaces", "a\tb": "control characters", "a~b": "'~'", "a^b": "'^'", "a:b": "':'",
		"a?b": "'?'", "a*b": "'*'", "a[b": "'['", `a\b`: `'\'`, "a..b": "'..'", "x.lock": "'.lock'", "x.": "end with '.'",
		".x": "leading dot", "a/.b": "leading dot", "-flag": "branch", "a//b": "branch", "a/": "branch", "@": "branch",
	} {
		if err := validateBranch(bad); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v; want %q", bad, err, want)
		}
	}
}
