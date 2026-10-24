package carrypatch

import (
	"os/exec"
	"strings"
	"testing"
)

// TestFixturesDefaultBranchIsMain guards the pin above: a plain `git init`
// must create `main`, whatever the host's git configuration says.
func TestFixturesDefaultBranchIsMain(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	out, err := exec.Command("git", "-C", dir, "symbolic-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("git symbolic-ref HEAD: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "refs/heads/main" {
		t.Errorf("HEAD of a fresh repo = %q; want refs/heads/main", got)
	}
}
