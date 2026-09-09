package merge

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// Two consecutive merge jobs against the same trunk must both succeed and
// leave the working tree on an existing branch with no uncommitted changes.
// Previously the job deleted the source branch while it was checked out,
// leaving HEAD dangling and breaking every later git operation.
func TestHandleMergeJob_LeavesTrunkOnTargetBranch(t *testing.T) {
	root := t.TempDir()
	trunk := filepath.Join(root, "ws1", "trunk")
	if err := os.MkdirAll(trunk, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, trunk, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(trunk, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, trunk, "add", ".")
	gitIn(t, trunk, "commit", "-q", "-m", "base")
	for _, b := range []string{"feature/one", "feature/two"} {
		gitIn(t, trunk, "checkout", "-q", "-b", b, "main")
		if err := os.WriteFile(filepath.Join(trunk, b[len("feature/"):]+".txt"), []byte(b+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, trunk, "add", ".")
		gitIn(t, trunk, "commit", "-q", "-m", b)
	}
	gitIn(t, trunk, "checkout", "-q", "main")

	h := &Handler{
		WorkspaceRoot: root,
		Fetch:         func(_ context.Context, _, _ string, _ transport.AuthMethod) error { return nil },
		ResolveAuth:   func(string) (transport.AuthMethod, error) { return nil, nil },
		GetVariable:   stubGetVariable(nil),
		Executor:      &ShellExecutor{},
		Rollback:      DefaultRollbackFunc(),
	}

	for _, b := range []string{"feature/one", "feature/two"} {
		payload, _ := json.Marshal(MergePayload{WorkspaceSlug: "ws1", TargetBranch: "main", SourceRef: b, SubmittedBy: "t"})
		result, retryable, err := h.HandleMergeJob(context.Background(), payload)
		if err != nil {
			t.Fatalf("merge of %s: err=%v retryable=%v", b, err, retryable)
		}
		if result == nil {
			t.Fatalf("merge of %s: nil result", b)
		}
		head := gitIn(t, trunk, "symbolic-ref", "-q", "HEAD")
		if head != "refs/heads/main\n" {
			t.Fatalf("after merge of %s HEAD = %q; want refs/heads/main", b, head)
		}
		if status := gitIn(t, trunk, "status", "--porcelain"); status != "" {
			t.Fatalf("after merge of %s worktree is dirty:\n%s", b, status)
		}
		// The source branch is gone and main contains its file.
		if _, err := exec.Command("git", "-C", trunk, "rev-parse", "--verify", b).Output(); err == nil {
			t.Fatalf("source branch %s still exists after merge", b)
		}
	}
	if _, err := os.Stat(filepath.Join(trunk, "two.txt")); err != nil {
		t.Fatalf("main working tree should contain two.txt after both merges: %v", err)
	}
}
