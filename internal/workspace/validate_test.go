package workspace

import "testing"

// TS-01-2: Verify that the slug validator accepts slugs that are 3–64 chars,
// lowercase alphanumeric + hyphens, start with a letter, have no trailing
// hyphen, and have no consecutive hyphens.
// Requirement: 01-REQ-1.2
func TestWorkspaceValidateSlug_ValidSlugs(t *testing.T) {
	valid := []struct {
		name string
		slug string
	}{
		{"three chars minimum", "abc"},
		{"with hyphens", "my-workspace"},
		{"alphanumeric mixed", "a1b2c3"},
		{"multiple hyphens separated", "workspace-one-two"},
		{"64 chars maximum", "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz01"},
	}

	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateSlug(tc.slug); err != nil {
				t.Errorf("validateSlug(%q) returned error: %v; want nil", tc.slug, err)
			}
		})
	}
}

// TS-01-3: Verify that the slug validator rejects slugs failing format,
// length, leading-character, trailing-hyphen, or consecutive-hyphen rules.
// Requirement: 01-REQ-1.3
func TestWorkspaceValidateSlug_InvalidSlugs(t *testing.T) {
	invalid := []struct {
		name string
		slug string
	}{
		{"too short (2 chars)", "ab"},
		{"uppercase letters", "MY-WORKSPACE"},
		{"leading hyphen", "-bad-start"},
		{"trailing hyphen", "bad-end-"},
		{"consecutive hyphens", "bad--consecutive"},
		{"contains space", "has space"},
		{"contains underscore", "has_underscore"},
	}

	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateSlug(tc.slug); err == nil {
				t.Errorf("validateSlug(%q) returned nil; want error", tc.slug)
			}
		})
	}
}

// TS-01-E1: Verify that the slug validator rejects slugs of exactly 2
// characters (below minimum) and exactly 65 characters (above maximum).
// Requirement: 01-REQ-1.E1
func TestWorkspaceValidateSlug_BoundaryLengths(t *testing.T) {
	t.Run("too short 2 chars", func(t *testing.T) {
		if err := validateSlug("ab"); err == nil {
			t.Error("validateSlug(2-char slug) returned nil; want error")
		}
	})

	t.Run("too long 65 chars", func(t *testing.T) {
		slug65 := "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz012"
		if len(slug65) != 65 {
			t.Fatalf("test setup: slug65 has length %d, want 65", len(slug65))
		}
		if err := validateSlug(slug65); err == nil {
			t.Error("validateSlug(65-char slug) returned nil; want error")
		}
	})
}

// TS-01-E2: Verify that the slug validator rejects slugs starting with a
// digit or hyphen.
// Requirement: 01-REQ-1.E2
func TestWorkspaceValidateSlug_InvalidLeadingChars(t *testing.T) {
	t.Run("starts with digit", func(t *testing.T) {
		if err := validateSlug("1workspace"); err == nil {
			t.Error("validateSlug(digit-start slug) returned nil; want error")
		}
	})

	t.Run("starts with hyphen", func(t *testing.T) {
		if err := validateSlug("-workspace"); err == nil {
			t.Error("validateSlug(hyphen-start slug) returned nil; want error")
		}
	})
}

// TS-01-4: Verify that the git_url validator accepts valid HTTPS and SSH URLs
// with non-empty host and path.
// Requirement: 01-REQ-1.4
func TestWorkspaceValidateGitURL_ValidURLs(t *testing.T) {
	valid := []struct {
		name string
		url  string
	}{
		{"HTTPS github", "https://github.com/org/repo"},
		{"HTTPS gitlab", "https://gitlab.com/team/project"},
		{"SSH github", "git@github.com:org/repo"},
		{"SSH bitbucket with .git", "git@bitbucket.org:team/project.git"},
	}

	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateGitURL(tc.url); err != nil {
				t.Errorf("validateGitURL(%q) returned error: %v; want nil", tc.url, err)
			}
		})
	}
}

// TS-01-5: Verify that the git_url validator rejects empty URLs, plain HTTP
// URLs, URLs with empty host, and URLs with empty path.
// Requirement: 01-REQ-1.5
func TestWorkspaceValidateGitURL_InvalidURLs(t *testing.T) {
	invalid := []struct {
		name string
		url  string
	}{
		{"empty string", ""},
		{"plain HTTP", "http://github.com/org/repo"},
		{"HTTPS empty host", "https:///repo"},
		{"HTTPS empty path", "https://github.com/"},
		{"SSH empty host", "git@:/org/repo"},
		{"SSH empty path", "git@github.com:"},
	}

	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateGitURL(tc.url); err == nil {
				t.Errorf("validateGitURL(%q) returned nil; want error", tc.url)
			}
		})
	}
}

// TS-01-E4: Verify that the git_url validator rejects plain HTTP URLs
// (http://).
// Requirement: 01-REQ-1.E4
func TestWorkspaceValidateGitURL_PlainHTTP(t *testing.T) {
	if err := validateGitURL("http://github.com/org/repo"); err == nil {
		t.Error("validateGitURL(http:// URL) returned nil; want error")
	}
}

// TS-01-6: Verify that the branch validator accepts valid git ref names that
// comply with all git ref naming rules.
// Requirement: 01-REQ-1.6
func TestWorkspaceValidateBranch_ValidBranches(t *testing.T) {
	valid := []struct {
		name   string
		branch string
	}{
		{"simple name", "main"},
		{"with slash", "feature/my-feature"},
		{"with dot and digits", "release-1.0"},
		{"hotfix with slash", "hotfix/fix-123"},
	}

	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateBranch(tc.branch); err != nil {
				t.Errorf("validateBranch(%q) returned error: %v; want nil", tc.branch, err)
			}
		})
	}
}

// TS-01-7: Verify that the branch validator rejects branch values that
// violate git ref naming rules.
// Requirement: 01-REQ-1.7
func TestWorkspaceValidateBranch_InvalidBranches(t *testing.T) {
	invalid := []struct {
		name   string
		branch string
	}{
		{"contains space", "branch with space"},
		{"contains tilde", "branch~1"},
		{"contains caret", "branch^2"},
		{"contains colon", "branch:name"},
		{"contains question mark", "branch?q"},
		{"contains asterisk", "branch*"},
		{"contains bracket", "branch[0]"},
		{"contains backslash", "branch\\path"},
		{"double dot sequence", "branch..name"},
		{"trailing .lock", "branch.lock"},
		{"trailing dot", "branch."},
		{"leading dot", ".branch"},
	}

	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateBranch(tc.branch); err == nil {
				t.Errorf("validateBranch(%q) returned nil; want error", tc.branch)
			}
		})
	}
}

// TS-01-E3: Verify that the branch validator rejects an explicitly provided
// empty string branch.
// Requirement: 01-REQ-1.E3
func TestWorkspaceValidateBranch_EmptyString(t *testing.T) {
	if err := validateBranch(""); err == nil {
		t.Error("validateBranch(\"\") returned nil; want error")
	}
}
