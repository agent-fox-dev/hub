package workspace_test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/carrypatch"
	"github.com/agent-fox-dev/hub/internal/upstream"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// ===========================================================================
// Spec 01, task 9: every go-git path the hub uses on the trunk keeps working
// while linked rebuild worktrees are registered under it, and leaves each
// worktree usable. Requirement: 01-REQ-9.3
// ===========================================================================

// coGit runs git in dir and fails the test on error.
func coGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := coGitTry(dir, args...)
	if err != nil {
		t.Fatalf("git %v (dir=%s) failed: %v\n%s", args, dir, err, out)
	}
	return out
}

func coGitTry(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_EDITOR=true",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func coWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func coCommit(t *testing.T, dir, file, content, msg string) string {
	t.Helper()
	coWrite(t, filepath.Join(dir, file), content)
	coGit(t, dir, "add", file)
	coGit(t, dir, "commit", "-m", msg)
	return coGit(t, dir, "rev-parse", "HEAD")
}

// coWtState describes one registered linked worktree.
type coWtState struct {
	atSide bool // detached at the side commit instead of main's tip
	dirty  bool // tracked modification plus an untracked file
}

type coFixture struct {
	root        string
	trunk       string
	upstreamSrc string
	originBare  string
	worktrees   []string
	sideSHA     string
}

func newCoFixture(t *testing.T, states []coWtState) *coFixture {
	t.Helper()
	f := &coFixture{root: t.TempDir()}
	f.upstreamSrc = filepath.Join(f.root, "upstream-src")
	coGit(t, f.root, "init", "-b", "main", f.upstreamSrc)
	coCommit(t, f.upstreamSrc, "base.txt", "base\n", "base")

	f.trunk = filepath.Join(f.root, "ws", "trunk")
	coGit(t, f.root, "clone", f.upstreamSrc, f.trunk)
	coGit(t, f.trunk, "remote", "rename", "origin", "upstream")
	// A hub workspace never has a symbolic refs/remotes/upstream/HEAD (it is
	// written by upstream.Fetch as a plain ref); drop the one clone created.
	coGit(t, f.trunk, "remote", "set-head", "upstream", "-d")
	coGit(t, f.trunk, "config", "user.name", "Test")
	coGit(t, f.trunk, "config", "user.email", "test@example.com")

	f.originBare = filepath.Join(f.root, "origin.git")
	coGit(t, f.root, "init", "--bare", "-b", "main", f.originBare)
	coGit(t, f.trunk, "remote", "add", "origin", f.originBare)

	// A side commit, reachable only from the branch "side".
	coGit(t, f.trunk, "checkout", "-b", "side")
	f.sideSHA = coCommit(t, f.trunk, "side.txt", "side\n", "side commit")
	coGit(t, f.trunk, "checkout", "main")

	for i, st := range states {
		path := filepath.Join(f.root, "ws", "rebuild", fmt.Sprintf("job-%d", i))
		at := "main"
		if st.atSide {
			at = f.sideSHA
		}
		coGit(t, f.trunk, "worktree", "add", "--detach", path, at)
		if st.dirty {
			coWrite(t, filepath.Join(path, "base.txt"), "dirty edit\n")
			coWrite(t, filepath.Join(path, "untracked.txt"), "untracked\n")
		}
		f.worktrees = append(f.worktrees, path)
	}
	return f
}

type coWtSnap struct{ head, status, base string }

func coSnap(t *testing.T, path string) coWtSnap {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(path, "base.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return coWtSnap{
		head:   coGit(t, path, "rev-parse", "HEAD"),
		status: coGit(t, path, "status", "--porcelain"),
		base:   string(b),
	}
}

// runGoGitPaths exercises every go-git path the hub uses on the trunk and
// asserts that the registered worktrees stay usable and unchanged.
func (f *coFixture) runGoGitPaths(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	before := make([]coWtSnap, len(f.worktrees))
	for i, w := range f.worktrees {
		before[i] = coSnap(t, w)
	}

	// Post-push reset: PlainOpen + Worktree().Reset(HardReset) on a dirty trunk.
	coWrite(t, filepath.Join(f.trunk, "base.txt"), "trunk dirty\n")
	repo, err := git.PlainOpen(f.trunk)
	if err != nil {
		t.Fatalf("PlainOpen(trunk): %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("repo.Head(): %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("repo.Worktree(): %v", err)
	}
	if err := wt.Reset(&git.ResetOptions{Commit: head.Hash(), Mode: git.HardReset}); err != nil {
		t.Fatalf("Worktree().Reset(HardReset): %v", err)
	}
	if st := coGit(t, f.trunk, "status", "--porcelain"); st != "" {
		t.Errorf("trunk not clean after hard reset: %q", st)
	}

	// upstream.Fetch: upstream gains a commit, the fetch brings it in.
	newSHA := coCommit(t, f.upstreamSrc, "upstream.txt", "new upstream\n", "upstream advance")
	if err := upstream.Fetch(ctx, f.trunk, nil); err != nil {
		t.Fatalf("upstream.Fetch: %v", err)
	}
	if got := coGit(t, f.trunk, "rev-parse", upstream.HeadRef); got != newSHA {
		t.Errorf("fetched upstream HEAD = %s, want %s", got, newSHA)
	}

	// Sync fast-forward.
	branch := "main"
	if err := workspace.DefaultSyncUpdateLocalRefFnForTest(f.trunk, &branch, newSHA); err != nil {
		t.Fatalf("defaultSyncUpdateLocalRefFn: %v", err)
	}
	if got := coGit(t, f.trunk, "rev-parse", "main"); got != newSHA {
		t.Errorf("main = %s after fast-forward, want %s", got, newSHA)
	}
	if _, err := os.Stat(filepath.Join(f.trunk, "upstream.txt")); err != nil {
		t.Errorf("trunk working tree not advanced by the fast-forward: %v", err)
	}

	// Push of the integration branch to the local bare origin.
	push := carrypatch.DefaultPushIntegrationFunc(func(string) (transport.AuthMethod, error) { return nil, nil })
	if err := push(ctx, "ws", f.trunk, "main"); err != nil {
		t.Fatalf("DefaultPushIntegrationFunc: %v", err)
	}
	if got := coGit(t, f.originBare, "rev-parse", "refs/heads/main"); got != newSHA {
		t.Errorf("origin main = %s, want %s", got, newSHA)
	}

	// Every worktree is still registered, unchanged and usable.
	list := coGit(t, f.trunk, "worktree", "list", "--porcelain")
	if got := strings.Count(list, "worktree "); got != len(f.worktrees)+1 {
		t.Errorf("worktree list shows %d entries, want %d:\n%s", got, len(f.worktrees)+1, list)
	}
	for i, w := range f.worktrees {
		if _, err := coGitTry(w, "status"); err != nil {
			t.Errorf("worktree %d: git status failed", i)
		}
		if _, err := coGitTry(w, "log", "-1"); err != nil {
			t.Errorf("worktree %d: git log failed", i)
		}
		if after := coSnap(t, w); after != before[i] {
			t.Errorf("worktree %d changed: before=%+v after=%+v", i, before[i], after)
		}
	}
}

// TS-01-61: with a clean and a dirty linked worktree registered, each go-git
// path succeeds and leaves the worktrees usable.
func TestCoexistence_TS_01_61_GoGitPathsWithRegisteredWorktree(t *testing.T) {
	f := newCoFixture(t, []coWtState{{atSide: false, dirty: false}, {atSide: true, dirty: true}})
	f.runGoGitPaths(t)
}

// TS-01-62: the same holds for any number (0-3) of registered worktrees at
// any detached commit with any clean or dirty file state.
func TestCoexistence_TS_01_62_Property(t *testing.T) {
	rng := rand.New(rand.NewSource(0x5eed))
	const iterations = 10
	for i := 0; i < iterations; i++ {
		n := rng.Intn(4)
		if i < 4 {
			n = i // make sure 0, 1, 2 and 3 worktrees are all covered
		}
		states := make([]coWtState, n)
		for j := range states {
			states[j] = coWtState{atSide: rng.Intn(2) == 0, dirty: rng.Intn(2) == 0}
		}
		t.Run(fmt.Sprintf("case%d_%v", i, states), func(t *testing.T) {
			f := newCoFixture(t, states)
			f.runGoGitPaths(t)
		})
	}
}
