package gitcmd

import "testing"

// Names git would read as options, or that check-ref-format rejects, are
// refused; ordinary branch names pass.
func TestValidateRefName(t *testing.T) {
	for _, n := range []string{"main", "feature/x", "release-1.0", "a.b", "patch/custom_auth"} {
		if err := ValidateRefName(n); err != nil {
			t.Errorf("ValidateRefName(%q) = %v; want nil", n, err)
		}
	}
	invalid := []string{"", "-x", "--detach", "--exec=touch pwned", "a..b", "a b", "a~1", "a^", "a:b",
		"a?", "a*", "a[b", `a\b`, "/a", "a/", "a//b", "a.", ".a", "a/.b", "a.lock", "a@{1}", "@", "a\x01b"}
	for _, n := range invalid {
		if err := ValidateRefName(n); err == nil {
			t.Errorf("ValidateRefName(%q) = nil; want error", n)
		}
	}
}
