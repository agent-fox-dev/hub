package gitcmd

import (
	"context"
	"testing"
)

func TestRemoteAdd_RegistersRemote(t *testing.T) {
	dir := initRepo(t)
	if err := newRunner(t, dir).RemoteAdd(context.Background(), "upstream", "https://example.com/repo.git"); err != nil {
		t.Fatalf("RemoteAdd: %v", err)
	}
	if got := runGit(t, dir, "remote", "get-url", "upstream"); got != "https://example.com/repo.git" {
		t.Errorf("upstream url = %q", got)
	}
}

// An existing name, or an empty name or URL, is a GitError.
func TestRemoteAdd_RejectsExistingNameAndEmptyArguments(t *testing.T) {
	dir := initRepo(t)
	runGit(t, dir, "remote", "add", "origin", "https://example.com/first.git")
	r := newRunner(t, dir)
	for _, c := range [][2]string{{"origin", "https://example.com/repo.git"}, {"", "https://example.com/repo.git"}, {"upstream", ""}} {
		asGitError(t, r.RemoteAdd(context.Background(), c[0], c[1]))
	}
	if got := runGit(t, dir, "remote", "get-url", "origin"); got != "https://example.com/first.git" {
		t.Errorf("origin url = %q after rejected RemoteAdd", got)
	}
}
