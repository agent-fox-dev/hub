package workspace

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// pushGit runs git in dir with a fixed identity and returns its trimmed
// output.
func pushGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newPushTrunk makes a trunk at trunkDir whose HEAD is main, with one commit
// on main and one of its own on each of branches, and an empty bare origin.
// It returns the bare repository's path, which is also origin's URL.
func newPushTrunk(t *testing.T, trunkDir string, branches ...string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	pushGit(t, filepath.Dir(bare), "init", "--bare", "-b", "main", bare)
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pushGit(t, trunkDir, "init", "-b", "main")
	pushCommit(t, trunkDir, "README.md", "# trunk")
	for _, b := range branches {
		pushGit(t, trunkDir, "checkout", "-q", "-b", b)
		pushCommit(t, trunkDir, strings.ReplaceAll(b, "/", "_")+".txt", b)
		pushGit(t, trunkDir, "checkout", "-q", "main")
	}
	pushGit(t, trunkDir, "remote", "add", "origin", bare)
	return bare
}

// pushCommit writes name with content in dir and commits it.
func pushCommit(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	pushGit(t, dir, "add", name)
	pushGit(t, dir, "commit", "-q", "-m", "add "+name)
	return pushGit(t, dir, "rev-parse", "HEAD")
}

// bareRef is the commit ref points at in the bare repository, "" when the ref
// does not exist.
func bareRef(t *testing.T, bare, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", bare, "rev-parse", "--verify", "--quiet", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// pushCall is one call of the shared push implementation.
type pushCall struct {
	refSpecs    []config.RefSpec
	force       bool
	hasDeadline bool
	deadlineIn  time.Duration // from the call to the deadline, when there is one
}

// recordPushes wraps the shared push implementation and records every call.
func recordPushes(t *testing.T) *[]pushCall {
	t.Helper()
	var calls []pushCall
	orig := pushToOriginFn
	pushToOriginFn = func(ctx context.Context, repoPath, gitURL string, auth transport.AuthMethod, refSpecs []config.RefSpec, force bool, progress io.Writer) error {
		deadline, hasDeadline := ctx.Deadline()
		calls = append(calls, pushCall{append([]config.RefSpec(nil), refSpecs...), force, hasDeadline, time.Until(deadline)})
		return orig(ctx, repoPath, gitURL, auth, refSpecs, force, progress)
	}
	t.Cleanup(func() { pushToOriginFn = orig })
	return &calls
}

// TS-38-51: The archive push and the branch push go through the same shared
// push implementation.
// Requirement: 38-REQ-10.1
func TestSharedPushImplementation_TS_38_51(t *testing.T) {
	trunk := filepath.Join(t.TempDir(), "trunk")
	bare := newPushTrunk(t, trunk, "a", "b")
	calls := recordPushes(t)

	if err := defaultArchiveOpenAndPushFn(trunk, bare, nil); err != nil {
		t.Fatalf("archive push: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("the archive push made %d calls of the shared push; want 1", len(*calls))
	}
	archive := (*calls)[0]
	if len(archive.refSpecs) != 1 || archive.refSpecs[0] != "refs/heads/*:refs/heads/*" || archive.force || archive.hasDeadline {
		t.Errorf("archive call = %+v; want refs/heads/*:refs/heads/*, no force, no deadline", archive)
	}

	// A one-branch refspec through the same function pushes that branch only.
	other := filepath.Join(t.TempDir(), "trunk")
	bare2 := newPushTrunk(t, other, "a", "b")
	if err := pushToOriginFn(context.Background(), other, bare2, nil, []config.RefSpec{"refs/heads/a:refs/heads/a"}, false, nil); err != nil {
		t.Fatalf("one-branch push: %v", err)
	}
	if got, want := bareRef(t, bare2, "refs/heads/a"), pushGit(t, other, "rev-parse", "refs/heads/a"); got != want {
		t.Errorf("origin's a = %q; want the trunk's %q", got, want)
	}
	for _, ref := range []string{"refs/heads/b", "refs/heads/main"} {
		if got := bareRef(t, bare2, ref); got != "" {
			t.Errorf("origin has %s at %s; a one-branch push must send only refs/heads/a", ref, got)
		}
	}

	// The branch push route goes through the same function, with a deadline.
	env := newBranchPushEnv(t)
	env.seedPushWorkspace(t, &Workspace{Slug: "shared", OwnerID: "user-a"}, "a", "b")
	pushedOK(t, env.push(t, "shared", "a", "", userAuth("user-a")))
	if len(*env.pushes) != 1 {
		t.Fatalf("the branch push made %d calls of the shared push; want 1", len(*env.pushes))
	}
	if branch := (*env.pushes)[0]; len(branch.refSpecs) != 1 || branch.refSpecs[0] != "refs/heads/a:refs/heads/a" || !branch.hasDeadline ||
		branch.deadlineIn > 2*time.Minute || branch.deadlineIn < 2*time.Minute-5*time.Second {
		t.Errorf("branch push call = %+v; want refs/heads/a:refs/heads/a with a two-minute deadline", branch)
	}
}

// TS-38-52: Archive and reclone still push all branches with the
// three-argument function and map up-to-date to ErrAlreadyUpToDate.
// Requirement: 38-REQ-10.2
func TestArchivePushUnchanged_TS_38_52(t *testing.T) {
	trunk := filepath.Join(t.TempDir(), "trunk")
	bare := newPushTrunk(t, trunk, "a", "b", "fix/42")

	var fn ArchiveOpenAndPushFuncType = defaultArchiveOpenAndPushFn
	if err := fn(trunk, bare, nil); err != nil {
		t.Fatalf("first archive push: %v", err)
	}
	for _, b := range []string{"main", "a", "b", "fix/42"} {
		if got, want := bareRef(t, bare, "refs/heads/"+b), pushGit(t, trunk, "rev-parse", "refs/heads/"+b); got != want {
			t.Errorf("origin's %s = %q; want the trunk's %q", b, got, want)
		}
	}
	if err := fn(trunk, bare, nil); !errors.Is(err, ErrAlreadyUpToDate) {
		t.Errorf("second archive push = %v; want ErrAlreadyUpToDate", err)
	}
}
