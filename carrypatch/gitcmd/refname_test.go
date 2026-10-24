package gitcmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRefName(t *testing.T) {
	valid := []string{"main", "feature/x", "release-1.0", "a.b", "patch/custom_auth"}
	for _, n := range valid {
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

// An option-looking ref must not be accepted by rev-parse (which would echo
// it back and exit 0 without --verify) nor be executed by checkout.
func TestOptionLikeRefsAreNotParsedAsOptions(t *testing.T) {
	requireGitMinVersion(t, 2, 38)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "t@example.com")
	runGit(t, dir, "config", "user.name", "t")
	writeFile(t, dir, "f.txt", "a")
	runGit(t, dir, "add", "f.txt")
	runGit(t, dir, "commit", "-q", "-m", "base")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	if _, err := runner.RevParse(ctx, "--detach"); err == nil {
		t.Error("RevParse(--detach) succeeded; want error")
	}
	if err := runner.Checkout(ctx, "--detach"); err == nil {
		t.Error("Checkout(--detach) succeeded; want error")
	}
	head := runGit(t, dir, "symbolic-ref", "HEAD")
	if head != "refs/heads/main" {
		t.Errorf("HEAD = %q after rejected checkout; want refs/heads/main", head)
	}
	if _, err := runner.Rebase(ctx, "--exec=touch pwned"); err == nil {
		t.Error("Rebase(--exec=...) succeeded; want error")
	}
	if _, err := runner.MergeTree(ctx, "--help", "main"); err == nil {
		t.Error("MergeTree(--help) succeeded; want error")
	}
}

func TestGitError_RedactsURLUserinfo(t *testing.T) {
	e := &GitError{
		Args:     []string{"remote", "add", "upstream", "https://bot:ghp_secret@github.com/o/r.git"},
		ExitCode: 128,
		Stderr:   "fatal: unable to access 'https://bot:ghp_secret@github.com/o/r.git/': boom",
	}
	msg := e.Error()
	if contains := "ghp_secret"; indexOf(msg, contains) >= 0 {
		t.Fatalf("GitError.Error() leaked credentials: %s", msg)
	}
	if indexOf(msg, "https://***@github.com/o/r.git") < 0 {
		t.Fatalf("GitError.Error() did not redact userinfo: %s", msg)
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
