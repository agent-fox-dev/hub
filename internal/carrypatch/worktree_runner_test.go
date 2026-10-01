package carrypatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
)

// TS-01-8: the typed worktree methods work through GitRunnerAdapter against a
// real repository, and MergeNoFF honours its message parameter.
//
// Requirement: 01-REQ-1.7
func TestTS_01_8_WorktreeMethodsAndMergeMessage(t *testing.T) {
	repo := t.TempDir()
	runGitCmd(t, "", "init", "-b", "main", repo)
	configGitUserCmd(t, repo)
	writeFileHelper(t, filepath.Join(repo, "base.txt"), "base\n")
	runGitCmd(t, repo, "add", ".")
	runGitCmd(t, repo, "commit", "-m", "base")
	baseSHA := runGitCmd(t, repo, "rev-parse", "HEAD")

	// Two diverging branches that merge cleanly.
	runGitCmd(t, repo, "checkout", "-b", "b1")
	writeFileHelper(t, filepath.Join(repo, "b1.txt"), "b1\n")
	runGitCmd(t, repo, "add", ".")
	runGitCmd(t, repo, "commit", "-m", "b1 change")
	runGitCmd(t, repo, "checkout", "main")
	runGitCmd(t, repo, "checkout", "-b", "b2")
	writeFileHelper(t, filepath.Join(repo, "b2.txt"), "b2\n")
	runGitCmd(t, repo, "add", ".")
	runGitCmd(t, repo, "commit", "-m", "b2 change")
	runGitCmd(t, repo, "checkout", "main")
	b1SHA := runGitCmd(t, repo, "rev-parse", "refs/heads/b1")
	b2SHA := runGitCmd(t, repo, "rev-parse", "refs/heads/b2")

	ctx := context.Background()
	trunkRunner, err := gitcmd.New(repo, nil)
	if err != nil {
		t.Fatalf("gitcmd.New(trunk): %v", err)
	}
	var trunk GitRunner = NewGitRunnerAdapter(trunkRunner)

	// --- worktree add / list ------------------------------------------------
	wtPath := filepath.Join(t.TempDir(), "rebuild", "job-1")
	if err := trunk.WorktreeAdd(ctx, wtPath, baseSHA); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "base.txt")); err != nil {
		t.Fatalf("worktree files missing after add: %v", err)
	}
	list := runGitCmd(t, repo, "worktree", "list", "--porcelain")
	resolved, _ := filepath.EvalSymlinks(wtPath)
	if !strings.Contains(list, wtPath) && !strings.Contains(list, resolved) {
		t.Errorf("worktree %s not listed:\n%s", wtPath, list)
	}
	if !strings.Contains(list, "detached") {
		t.Errorf("worktree is not detached:\n%s", list)
	}

	// --- MergeNoFF with a message, inside the worktree ----------------------
	wtRaw, err := gitcmd.New(wtPath, nil)
	if err != nil {
		t.Fatalf("gitcmd.New(worktree): %v", err)
	}
	var wt GitRunner = NewGitRunnerAdapter(wtRaw)

	if err := wt.MergeNoFF(ctx, b1SHA, "Merge branch 'b1'"); err != nil {
		t.Fatalf("MergeNoFF with message: %v", err)
	}
	if got := runGitCmd(t, wtPath, "log", "-1", "--format=%s"); got != "Merge branch 'b1'" {
		t.Errorf("merge subject with message = %q, want %q", got, "Merge branch 'b1'")
	}

	// --- MergeNoFF with an empty message keeps git's default ----------------
	if err := wt.HardReset(ctx, baseSHA); err != nil {
		t.Fatalf("HardReset: %v", err)
	}
	if err := wt.MergeNoFF(ctx, b2SHA, ""); err != nil {
		t.Fatalf("MergeNoFF with empty message: %v", err)
	}
	// git's default for a SHA merge on a detached HEAD: "Merge commit '<sha>' ...".
	wantDefault := "Merge commit '" + b2SHA + "'"
	if got := runGitCmd(t, wtPath, "log", "-1", "--format=%s"); !strings.HasPrefix(got, wantDefault) {
		t.Errorf("merge subject with empty message = %q, want git default starting %q", got, wantDefault)
	}

	// --- remove / prune -----------------------------------------------------
	if err := trunk.WorktreeRemove(ctx, wtPath); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree directory still exists after remove: %v", err)
	}
	if err := trunk.WorktreePrune(ctx); err != nil {
		t.Fatalf("WorktreePrune: %v", err)
	}
	list = runGitCmd(t, repo, "worktree", "list", "--porcelain")
	if n := strings.Count(list, "worktree "); n != 1 {
		t.Errorf("expected only the trunk in worktree list, got %d entries:\n%s", n, list)
	}

	// Prune also drops a registration whose directory was deleted by hand.
	wtPath2 := filepath.Join(t.TempDir(), "rebuild", "job-2")
	if err := trunk.WorktreeAdd(ctx, wtPath2, baseSHA); err != nil {
		t.Fatalf("WorktreeAdd #2: %v", err)
	}
	if err := os.RemoveAll(wtPath2); err != nil {
		t.Fatal(err)
	}
	if err := trunk.WorktreePrune(ctx); err != nil {
		t.Fatalf("WorktreePrune #2: %v", err)
	}
	list = runGitCmd(t, repo, "worktree", "list", "--porcelain")
	if n := strings.Count(list, "worktree "); n != 1 {
		t.Errorf("expected stale registration pruned, got %d entries:\n%s", n, list)
	}

	// The mock satisfies the interface with the new methods.
	var _ GitRunner = (*mockGitRunner)(nil)
}
