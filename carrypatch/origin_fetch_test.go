package carrypatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ===========================================================================
// TS-20-7 (integration): The origin fetch brings in fork branches with the
// heads refspec, no tags, and treats up-to-date as success.
//
// Verifies: 20-REQ-2.1
// ===========================================================================

func TestOriginFetch_BringsInBranches_NoTags_UpToDateSuccess_TS207(t *testing.T) {
	// Create a bare fork repository.
	forkDir := t.TempDir()
	runGitCmd(t, "", "init", "--bare", forkDir)

	// Create a temporary working clone to add branches and a tag to the fork.
	workDir := t.TempDir()
	runGitCmd(t, "", "clone", forkDir, workDir)
	configGitUserCmd(t, workDir)

	// Create initial commit on main.
	writeFileHelper(t, filepath.Join(workDir, "file.txt"), "hello")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "initial")

	// Create a feature branch.
	runGitCmd(t, workDir, "checkout", "-b", "feature/x")
	writeFileHelper(t, filepath.Join(workDir, "feature.txt"), "feature x")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "feature x")
	featureTip := runGitCmd(t, workDir, "rev-parse", "HEAD")

	// Create a tag on the fork.
	runGitCmd(t, workDir, "tag", "v1.0")

	// Push everything to the bare fork.
	runGitCmd(t, workDir, "push", "origin", "main")
	runGitCmd(t, workDir, "push", "origin", "feature/x")
	runGitCmd(t, workDir, "push", "origin", "v1.0")

	mainTip := runGitCmd(t, workDir, "rev-parse", "main")

	// Create a trunk by initialising a new repo and adding the fork as origin.
	// This avoids clone bringing in tracking refs automatically.
	trunkDir := t.TempDir()
	runGitCmd(t, "", "init", "-b", "main", trunkDir)
	configGitUserCmd(t, trunkDir)

	// Create an initial commit so HEAD is valid.
	writeFileHelper(t, filepath.Join(trunkDir, "init.txt"), "init")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "init")

	// Add the fork as origin remote.
	runGitCmd(t, trunkDir, "remote", "add", "origin", forkDir)

	// Verify tracking refs don't exist yet.
	_, err := runGitCmdErr(t, trunkDir, "rev-parse", "--verify", "refs/remotes/origin/feature/x")
	if err == nil {
		t.Fatal("expected refs/remotes/origin/feature/x to be absent before test fetch")
	}

	// Call the production FetchOrigin.
	fetchOrigin := DefaultFetchOriginFunc()
	ctx := context.Background()

	err = fetchOrigin(ctx, trunkDir, nil)
	if err != nil {
		t.Fatalf("first FetchOrigin call failed: %v", err)
	}

	// Verify refs/remotes/origin/main and refs/remotes/origin/feature/x exist.
	gotMain := runGitCmd(t, trunkDir, "rev-parse", "refs/remotes/origin/main")
	if gotMain != mainTip {
		t.Errorf("origin/main = %s; want %s", gotMain, mainTip)
	}

	gotFeature := runGitCmd(t, trunkDir, "rev-parse", "refs/remotes/origin/feature/x")
	if gotFeature != featureTip {
		t.Errorf("origin/feature/x = %s; want %s", gotFeature, featureTip)
	}

	// Verify no tag was created in the trunk.
	tagOutput, tagErr := runGitCmdErr(t, trunkDir, "tag", "-l")
	if tagErr != nil {
		t.Fatalf("git tag -l failed: %v", tagErr)
	}
	if tagOutput != "" {
		t.Errorf("expected no tags in trunk, got: %q", tagOutput)
	}

	// Second call should return nil (already up to date).
	err = fetchOrigin(ctx, trunkDir, nil)
	if err != nil {
		t.Fatalf("second FetchOrigin call (up-to-date) failed: %v", err)
	}
}

// TestOriginFetch_PrunesDeletedBranch_TS207 verifies that a branch deleted on
// the fork loses its refs/remotes/origin/<branch> tracking ref after sync
// (20-REQ-2.7).
func TestOriginFetch_PrunesDeletedBranch_TS207(t *testing.T) {
	// Create a bare fork repository.
	forkDir := t.TempDir()
	runGitCmd(t, "", "init", "--bare", forkDir)

	// Create a working clone to add branches.
	workDir := t.TempDir()
	runGitCmd(t, "", "clone", forkDir, workDir)
	configGitUserCmd(t, workDir)

	writeFileHelper(t, filepath.Join(workDir, "file.txt"), "hello")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "initial")

	runGitCmd(t, workDir, "checkout", "-b", "feature/to-delete")
	writeFileHelper(t, filepath.Join(workDir, "delete.txt"), "will be deleted")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "to delete")

	runGitCmd(t, workDir, "push", "origin", "main")
	runGitCmd(t, workDir, "push", "origin", "feature/to-delete")

	// Create trunk clone.
	trunkDir := t.TempDir()
	runGitCmd(t, "", "clone", forkDir, trunkDir)
	configGitUserCmd(t, trunkDir)

	// Verify the tracking ref exists.
	runGitCmd(t, trunkDir, "rev-parse", "--verify", "refs/remotes/origin/feature/to-delete")

	// Delete the branch on the fork.
	runGitCmd(t, forkDir, "branch", "-D", "feature/to-delete")

	// Fetch with pruning.
	fetchOrigin := DefaultFetchOriginFunc()
	ctx := context.Background()

	err := fetchOrigin(ctx, trunkDir, nil)
	if err != nil {
		t.Fatalf("FetchOrigin after branch deletion failed: %v", err)
	}

	// Verify the tracking ref is gone.
	_, err = runGitCmdErr(t, trunkDir, "rev-parse", "--verify", "refs/remotes/origin/feature/to-delete")
	if err == nil {
		t.Error("expected refs/remotes/origin/feature/to-delete to be pruned after branch deletion on fork")
	}
}

// ===========================================================================
// Helper: runGitCmdErr is like runGitCmd but returns the error instead of
// failing the test.
// ===========================================================================

func runGitCmdErr(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
