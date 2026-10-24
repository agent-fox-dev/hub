package gitcmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// WorktreeAdd checks the commit out detached in a linked worktree;
// WorktreeRemove discards it even when it is dirty.
func TestWorktreeAddAndRemove(t *testing.T) {
	dir, _, featureSHA := divergedRepo(t, false)
	wt := filepath.Join(t.TempDir(), "wt")
	r := newRunner(t, dir)

	if err := r.WorktreeAdd(context.Background(), wt, featureSHA); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if head := runGit(t, wt, "rev-parse", "HEAD"); head != featureSHA {
		t.Errorf("worktree HEAD = %s, want %s", head, featureSHA)
	}
	if _, err := tryGit(wt, "symbolic-ref", "-q", "HEAD"); err == nil {
		t.Error("worktree HEAD is on a branch; want detached")
	}

	writeFile(t, wt, "feature.txt", "dirty")
	if err := r.WorktreeRemove(context.Background(), wt); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if exists(wt) {
		t.Error("worktree directory still exists")
	}
}

func TestWorktreePrune_DropsVanishedWorktrees(t *testing.T) {
	dir, _, featureSHA := divergedRepo(t, false)
	wt := filepath.Join(t.TempDir(), "wt")
	r := newRunner(t, dir)
	if err := r.WorktreeAdd(context.Background(), wt, featureSHA); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if err := r.WorktreePrune(context.Background()); err != nil {
		t.Fatalf("WorktreePrune: %v", err)
	}
	if list := runGit(t, dir, "worktree", "list", "--porcelain"); strings.Contains(list, wt) {
		t.Errorf("vanished worktree still registered:\n%s", list)
	}
}
