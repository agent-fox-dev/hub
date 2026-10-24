package gitcmd

import (
	"context"
	"testing"
)

func TestCheckout_SwitchesBranch(t *testing.T) {
	dir, _, _ := divergedRepo(t, false)
	if err := newRunner(t, dir).Checkout(context.Background(), "feature"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if head := runGit(t, dir, "symbolic-ref", "HEAD"); head != "refs/heads/feature" {
		t.Errorf("HEAD = %s, want refs/heads/feature", head)
	}
}

// An unknown, empty or option-like ref is a GitError and HEAD stays put.
func TestCheckout_RejectsBadRefs(t *testing.T) {
	dir, _, _ := divergedRepo(t, false)
	r := newRunner(t, dir)
	for _, ref := range []string{"no-such-branch", "", "--detach"} {
		if ge := asGitError(t, r.Checkout(context.Background(), ref)); ge.Stderr == "" {
			t.Errorf("Checkout(%q): GitError.Stderr is empty", ref)
		}
	}
	if head := runGit(t, dir, "symbolic-ref", "HEAD"); head != "refs/heads/main" {
		t.Errorf("HEAD = %s after rejected checkouts, want refs/heads/main", head)
	}
}
