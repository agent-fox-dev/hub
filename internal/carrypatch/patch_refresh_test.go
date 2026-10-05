package carrypatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
)

// ===========================================================================
// TS-20-15 (integration): Only active, conflict and disabled patches are
// candidates, in position order, excluding the integration branch.
//
// Verifies: 20-REQ-3.1
// ===========================================================================

func TestRefreshPatchBranches_CandidateSelection_TS2015(t *testing.T) {
	// Set up a bare fork and a trunk that has origin pointing to the fork.
	forkDir := t.TempDir()
	runGitCmd(t, "", "init", "--bare", forkDir)

	// Create a working clone to populate the fork.
	workDir := t.TempDir()
	runGitCmd(t, "", "clone", forkDir, workDir)
	configGitUserCmd(t, workDir)

	// Initial commit on main.
	writeFileHelper(t, filepath.Join(workDir, "file.txt"), "hello")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "initial")

	// Create branches on the fork for each patch.
	for _, b := range []string{"patch-a", "patch-c", "patch-d", "patch-merged", "patch-deleted", "deploy"} {
		runGitCmd(t, workDir, "checkout", "-b", b)
		writeFileHelper(t, filepath.Join(workDir, b+".txt"), b)
		runGitCmd(t, workDir, "add", ".")
		runGitCmd(t, workDir, "commit", "-m", "commit on "+b)
		runGitCmd(t, workDir, "push", "origin", b)
		runGitCmd(t, workDir, "checkout", "main")
	}
	runGitCmd(t, workDir, "push", "origin", "main")

	// Create trunk repo with origin pointing to the fork.
	trunkDir := t.TempDir()
	runGitCmd(t, "", "init", "-b", "main", trunkDir)
	configGitUserCmd(t, trunkDir)
	writeFileHelper(t, filepath.Join(trunkDir, "init.txt"), "init")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "init")
	runGitCmd(t, trunkDir, "remote", "add", "origin", forkDir)
	runGitCmd(t, trunkDir, "fetch", "origin")

	// Create a real GitRunner for the trunk.
	runner := newRealGitRunner(t, trunkDir)

	// Patches in positions 1-6:
	// 1: active (patch-a)
	// 2: merged_upstream (patch-merged)
	// 3: conflict (patch-c)
	// 4: disabled (patch-d)
	// 5: deleted (patch-deleted)
	// 6: active but named like the integration branch (deploy)
	patches := []Patch{
		{ID: "p1", BranchName: "patch-a", Position: 1, Status: PatchStatusActive},
		{ID: "p2", BranchName: "patch-merged", Position: 2, Status: PatchStatusMergedUpstream},
		{ID: "p3", BranchName: "patch-c", Position: 3, Status: PatchStatusConflict},
		{ID: "p4", BranchName: "patch-d", Position: 4, Status: PatchStatusDisabled},
		{ID: "p5", BranchName: "patch-deleted", Position: 5, Status: PatchStatusDeleted},
		{ID: "p6", BranchName: "deploy", Position: 6, Status: PatchStatusActive},
	}

	ctx := context.Background()
	outcomes, patchAdvanced, err := refreshPatchBranches(ctx, runner, trunkDir, patches, "deploy", "replace")
	if err != nil {
		t.Fatalf("refreshPatchBranches returned error: %v", err)
	}

	// Should have exactly 3 candidates: patch-a (active), patch-c (conflict), patch-d (disabled).
	if len(outcomes) != 3 {
		t.Fatalf("expected 3 outcomes, got %d: %+v", len(outcomes), outcomes)
	}

	// Verify position order.
	expectedBranches := []string{"patch-a", "patch-c", "patch-d"}
	for i, want := range expectedBranches {
		if outcomes[i].BranchName != want {
			t.Errorf("outcome[%d].BranchName = %q; want %q", i, outcomes[i].BranchName, want)
		}
	}

	// All should have been created (no local branch existed).
	for _, o := range outcomes {
		if o.Action != ActionCreated {
			t.Errorf("outcome for %q: action = %q; want %q", o.BranchName, o.Action, ActionCreated)
		}
		if o.State != StateInSync {
			t.Errorf("outcome for %q: state = %q; want %q", o.BranchName, o.State, StateInSync)
		}
	}

	// patchAdvanced should be true because active and conflict patches were moved.
	if !patchAdvanced {
		t.Error("expected patchAdvanced=true because active and conflict patches were created")
	}

	// Verify that merged_upstream, deleted, and integration-named patches have no local refs.
	for _, b := range []string{"patch-merged", "patch-deleted"} {
		_, refErr := runGitCmdErr(t, trunkDir, "rev-parse", "--verify", "refs/heads/"+b)
		if refErr == nil {
			t.Errorf("expected refs/heads/%s to not exist (not a candidate), but it does", b)
		}
	}
	// deploy should not have been created as a local branch by the refresh
	// (it's excluded as the integration branch).
	_, refErr := runGitCmdErr(t, trunkDir, "rev-parse", "--verify", "refs/heads/deploy")
	if refErr == nil {
		t.Error("expected refs/heads/deploy to not exist (excluded as integration branch), but it does")
	}
}

// ===========================================================================
// TS-20-23 (property): For any pair of local and fork histories exactly the
// first matching rule applies, decided on SHAs.
//
// Verifies: 20-REQ-3.8
// ===========================================================================

func TestRefreshPatchBranches_FirstMatchingRule_SHAs_TS2023(t *testing.T) {
	type testCase struct {
		name           string
		setupFork      bool   // whether to create a fork branch
		setupLocal     bool   // whether to create a local branch
		relationship   string // "same", "local_ancestor", "fork_ancestor", "diverged"
		expectedAction string
		expectedState  string
	}

	cases := []testCase{
		{
			name:           "fork_missing",
			setupFork:      false,
			setupLocal:     true,
			relationship:   "",
			expectedAction: ActionNone,
			expectedState:  StateMissingOnOrigin,
		},
		{
			name:           "local_missing",
			setupFork:      true,
			setupLocal:     false,
			relationship:   "",
			expectedAction: ActionCreated,
			expectedState:  StateInSync,
		},
		{
			name:           "same_commit",
			setupFork:      true,
			setupLocal:     true,
			relationship:   "same",
			expectedAction: ActionNone,
			expectedState:  StateInSync,
		},
		{
			name:           "local_strict_ancestor_of_fork",
			setupFork:      true,
			setupLocal:     true,
			relationship:   "local_ancestor",
			expectedAction: ActionFastForwarded,
			expectedState:  StateInSync,
		},
		{
			name:           "fork_strict_ancestor_of_local",
			setupFork:      true,
			setupLocal:     true,
			relationship:   "fork_ancestor",
			expectedAction: ActionReplaced,
			expectedState:  StateInSync,
		},
		{
			name:           "diverged_unrelated",
			setupFork:      true,
			setupLocal:     true,
			relationship:   "diverged",
			expectedAction: ActionReplaced,
			expectedState:  StateInSync,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forkDir := t.TempDir()
			runGitCmd(t, "", "init", "--bare", forkDir)

			// Working clone to populate the fork.
			workDir := t.TempDir()
			runGitCmd(t, "", "clone", forkDir, workDir)
			configGitUserCmd(t, workDir)

			// Initial commit on main.
			writeFileHelper(t, filepath.Join(workDir, "file.txt"), "hello")
			runGitCmd(t, workDir, "add", ".")
			runGitCmd(t, workDir, "commit", "-m", "initial")
			runGitCmd(t, workDir, "push", "origin", "main")

			baseSHA := runGitCmd(t, workDir, "rev-parse", "HEAD")

			// Create trunk.
			trunkDir := t.TempDir()
			runGitCmd(t, "", "init", "-b", "main", trunkDir)
			configGitUserCmd(t, trunkDir)
			writeFileHelper(t, filepath.Join(trunkDir, "init.txt"), "init")
			runGitCmd(t, trunkDir, "add", ".")
			runGitCmd(t, trunkDir, "commit", "-m", "init")
			runGitCmd(t, trunkDir, "remote", "add", "origin", forkDir)

			branchName := "feat"

			if tc.setupFork {
				// Create the branch on the fork.
				runGitCmd(t, workDir, "checkout", "-b", branchName)
				writeFileHelper(t, filepath.Join(workDir, "fork.txt"), "fork content")
				runGitCmd(t, workDir, "add", ".")
				runGitCmd(t, workDir, "commit", "-m", "fork commit")

				if tc.relationship == "local_ancestor" {
					// Add another commit so fork is ahead.
					writeFileHelper(t, filepath.Join(workDir, "fork2.txt"), "fork content 2")
					runGitCmd(t, workDir, "add", ".")
					runGitCmd(t, workDir, "commit", "-m", "fork commit 2")
				}

				runGitCmd(t, workDir, "push", "origin", branchName)
				runGitCmd(t, workDir, "checkout", "main")
			}

			// Fetch origin into trunk.
			runGitCmd(t, trunkDir, "fetch", "origin")

			if tc.setupLocal {
				switch tc.relationship {
				case "same":
					// Local branch at the same commit as fork.
					forkTip := runGitCmd(t, trunkDir, "rev-parse", "refs/remotes/origin/"+branchName)
					runGitCmd(t, trunkDir, "branch", branchName, forkTip)
				case "local_ancestor":
					// Local branch at the first fork commit (ancestor of fork tip).
					// The fork has 2 commits; local is at the first one.
					forkTip := runGitCmd(t, trunkDir, "rev-parse", "refs/remotes/origin/"+branchName)
					// Get parent of fork tip.
					parent := runGitCmd(t, trunkDir, "rev-parse", forkTip+"^")
					runGitCmd(t, trunkDir, "branch", branchName, parent)
				case "fork_ancestor":
					// Fork is behind local: local has an extra commit.
					forkTip := runGitCmd(t, trunkDir, "rev-parse", "refs/remotes/origin/"+branchName)
					runGitCmd(t, trunkDir, "checkout", "-b", branchName, forkTip)
					writeFileHelper(t, filepath.Join(trunkDir, "local_extra.txt"), "local extra")
					runGitCmd(t, trunkDir, "add", ".")
					runGitCmd(t, trunkDir, "commit", "-m", "local extra commit")
					runGitCmd(t, trunkDir, "checkout", "main")
				case "diverged":
					// Unrelated commit on local.
					runGitCmd(t, trunkDir, "checkout", "-b", branchName)
					writeFileHelper(t, filepath.Join(trunkDir, "diverged.txt"), "diverged")
					runGitCmd(t, trunkDir, "add", ".")
					runGitCmd(t, trunkDir, "commit", "-m", "diverged commit")
					runGitCmd(t, trunkDir, "checkout", "main")
				case "":
					if tc.setupFork {
						// Fork missing case shouldn't reach here.
						t.Fatal("unexpected: setupFork=true with empty relationship and setupLocal=true")
					}
					// Fork missing: just create a local branch.
					runGitCmd(t, trunkDir, "branch", branchName, baseSHA)
				}
			}

			runner := newRealGitRunner(t, trunkDir)
			patches := []Patch{
				{ID: "p1", BranchName: branchName, Position: 1, Status: PatchStatusActive},
			}

			ctx := context.Background()
			outcomes, _, err := refreshPatchBranches(ctx, runner, trunkDir, patches, "integration", "replace")
			if err != nil {
				t.Fatalf("refreshPatchBranches returned error: %v", err)
			}

			if len(outcomes) != 1 {
				t.Fatalf("expected 1 outcome, got %d", len(outcomes))
			}

			o := outcomes[0]
			if o.Action != tc.expectedAction {
				t.Errorf("action = %q; want %q", o.Action, tc.expectedAction)
			}
			if o.State != tc.expectedState {
				t.Errorf("state = %q; want %q", o.State, tc.expectedState)
			}

			// Verify that after the refresh, the local ref matches the fork tip
			// for in_sync states (except missing_on_origin).
			if tc.expectedState == StateInSync && tc.setupFork {
				forkTip := runGitCmd(t, trunkDir, "rev-parse", "refs/remotes/origin/"+branchName)
				localTip := runGitCmd(t, trunkDir, "rev-parse", "refs/heads/"+branchName)
				if localTip != forkTip {
					t.Errorf("local tip %s != fork tip %s after %s", localTip, forkTip, tc.expectedAction)
				}
			}

			// For replaced: verify backup ref.
			if tc.expectedAction == ActionReplaced {
				backupRef := "refs/hub/replaced/" + branchName
				backupSHA := runGitCmd(t, trunkDir, "rev-parse", backupRef)
				if o.ReplacedSHA == "" {
					t.Error("expected ReplacedSHA to be set for replaced action")
				}
				if backupSHA != o.ReplacedSHA {
					t.Errorf("backup ref SHA %s != ReplacedSHA %s", backupSHA, o.ReplacedSHA)
				}
			}

			_ = baseSHA
		})
	}
}

// ===========================================================================
// TS-20-24 (integration): Every patch and backup ref is written with
// update-ref carrying the expected old SHA, never checkout, reset or branch -f.
//
// Verifies: 20-REQ-4.1
// ===========================================================================

func TestRefreshPatchBranches_CASRefWrites_TS2024(t *testing.T) {
	// Set up fork with branches.
	forkDir := t.TempDir()
	runGitCmd(t, "", "init", "--bare", forkDir)

	workDir := t.TempDir()
	runGitCmd(t, "", "clone", forkDir, workDir)
	configGitUserCmd(t, workDir)

	writeFileHelper(t, filepath.Join(workDir, "file.txt"), "hello")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "initial")
	baseSHA := runGitCmd(t, workDir, "rev-parse", "HEAD")

	// Branch for create case.
	runGitCmd(t, workDir, "checkout", "-b", "create-me")
	writeFileHelper(t, filepath.Join(workDir, "create.txt"), "create")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "create commit")
	runGitCmd(t, workDir, "push", "origin", "create-me")

	// Branch for fast-forward case.
	runGitCmd(t, workDir, "checkout", "main")
	runGitCmd(t, workDir, "checkout", "-b", "ff-me")
	writeFileHelper(t, filepath.Join(workDir, "ff1.txt"), "ff1")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "ff commit 1")
	ffBase := runGitCmd(t, workDir, "rev-parse", "HEAD")
	writeFileHelper(t, filepath.Join(workDir, "ff2.txt"), "ff2")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "ff commit 2")
	runGitCmd(t, workDir, "push", "origin", "ff-me")

	// Branch for replace case.
	runGitCmd(t, workDir, "checkout", "main")
	runGitCmd(t, workDir, "checkout", "-b", "replace-me")
	writeFileHelper(t, filepath.Join(workDir, "replace_fork.txt"), "fork version")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "fork replace commit")
	runGitCmd(t, workDir, "push", "origin", "replace-me")

	runGitCmd(t, workDir, "push", "origin", "main")

	// Create trunk.
	trunkDir := t.TempDir()
	runGitCmd(t, "", "init", "-b", "main", trunkDir)
	configGitUserCmd(t, trunkDir)
	writeFileHelper(t, filepath.Join(trunkDir, "init.txt"), "init")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "init")
	runGitCmd(t, trunkDir, "remote", "add", "origin", forkDir)
	runGitCmd(t, trunkDir, "fetch", "origin")

	// Set up local branches:
	// ff-me: at the first commit (ancestor of fork tip).
	runGitCmd(t, trunkDir, "branch", "ff-me", ffBase)

	// replace-me: diverged from fork.
	runGitCmd(t, trunkDir, "checkout", "-b", "replace-me", baseSHA)
	writeFileHelper(t, filepath.Join(trunkDir, "replace_local.txt"), "local version")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "local replace commit")
	localReplaceTip := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "checkout", "main")

	// Use a recording GitRunner that wraps a real one.
	realRunner := newRealGitRunner(t, trunkDir)
	recorder := &recordingGitRunner{real: realRunner}

	patches := []Patch{
		{ID: "p1", BranchName: "create-me", Position: 1, Status: PatchStatusActive},
		{ID: "p2", BranchName: "ff-me", Position: 2, Status: PatchStatusActive},
		{ID: "p3", BranchName: "replace-me", Position: 3, Status: PatchStatusActive},
	}

	ctx := context.Background()
	outcomes, _, err := refreshPatchBranches(ctx, recorder, trunkDir, patches, "integration", "replace")
	if err != nil {
		t.Fatalf("refreshPatchBranches returned error: %v", err)
	}

	if len(outcomes) != 3 {
		t.Fatalf("expected 3 outcomes, got %d", len(outcomes))
	}

	// Verify all ref writes used update-ref with expected-old.
	zeroSHA := strings.Repeat("0", 40)
	var updateRefCalls [][]string
	for _, call := range recorder.runCalls {
		if len(call) >= 1 && call[0] == "update-ref" {
			updateRefCalls = append(updateRefCalls, call)
		}
	}

	// We expect:
	// 1. create-me: update-ref refs/heads/create-me <new> 0000...0000
	// 2. ff-me: update-ref refs/heads/ff-me <new> <ffBase>
	// 3. replace-me backup: update-ref refs/hub/replaced/replace-me <localTip> 0000...0000
	// 4. replace-me: update-ref refs/heads/replace-me <new> <localTip>
	if len(updateRefCalls) < 4 {
		t.Fatalf("expected at least 4 update-ref calls, got %d: %v", len(updateRefCalls), updateRefCalls)
	}

	// Check create uses zero SHA.
	createCall := updateRefCalls[0]
	if createCall[len(createCall)-1] != zeroSHA {
		t.Errorf("create update-ref expected-old should be %s, got %s", zeroSHA, createCall[len(createCall)-1])
	}

	// Check ff uses the old tip.
	ffCall := updateRefCalls[1]
	if ffCall[len(ffCall)-1] != ffBase {
		t.Errorf("ff update-ref expected-old should be %s, got %s", ffBase, ffCall[len(ffCall)-1])
	}

	// Check replace backup uses zero SHA (first backup).
	backupCall := updateRefCalls[2]
	if !strings.Contains(strings.Join(backupCall, " "), "refs/hub/replaced/replace-me") {
		t.Errorf("expected backup ref call for replace-me, got %v", backupCall)
	}

	// Check replace branch uses old local tip.
	replaceCall := updateRefCalls[3]
	if replaceCall[len(replaceCall)-1] != localReplaceTip {
		t.Errorf("replace update-ref expected-old should be %s, got %s", localReplaceTip, replaceCall[len(replaceCall)-1])
	}

	// Verify no checkout, branch -f calls (apart from HardReset).
	for _, call := range recorder.runCalls {
		if len(call) >= 1 && call[0] == "checkout" {
			t.Errorf("unexpected checkout call: %v", call)
		}
		if len(call) >= 2 && call[0] == "branch" && call[1] == "-f" {
			t.Errorf("unexpected branch -f call: %v", call)
		}
	}
}

// ===========================================================================
// TS-20-25 (integration): Moving the checked-out branch hard-resets the
// working tree to the new tip.
//
// Verifies: 20-REQ-4.2
// ===========================================================================

func TestRefreshPatchBranches_CheckedOutBranch_HardReset_TS2025(t *testing.T) {
	// Set up fork with a branch that has a new file.
	forkDir := t.TempDir()
	runGitCmd(t, "", "init", "--bare", forkDir)

	workDir := t.TempDir()
	runGitCmd(t, "", "clone", forkDir, workDir)
	configGitUserCmd(t, workDir)

	writeFileHelper(t, filepath.Join(workDir, "file.txt"), "hello")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "initial")

	// Create feat branch with a new file.
	runGitCmd(t, workDir, "checkout", "-b", "feat")
	writeFileHelper(t, filepath.Join(workDir, "new.txt"), "new file content")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "add new.txt")
	featBase := runGitCmd(t, workDir, "rev-parse", "HEAD")

	// Add another commit so fork is ahead.
	writeFileHelper(t, filepath.Join(workDir, "new2.txt"), "another new file")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "add new2.txt")

	runGitCmd(t, workDir, "push", "origin", "feat")
	runGitCmd(t, workDir, "push", "origin", "main")

	// Create trunk with HEAD pointing to feat.
	trunkDir := t.TempDir()
	runGitCmd(t, "", "init", "-b", "main", trunkDir)
	configGitUserCmd(t, trunkDir)
	writeFileHelper(t, filepath.Join(trunkDir, "file.txt"), "hello")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "initial")
	runGitCmd(t, trunkDir, "remote", "add", "origin", forkDir)
	runGitCmd(t, trunkDir, "fetch", "origin")

	// Create local feat at the base (ancestor of fork tip).
	runGitCmd(t, trunkDir, "checkout", "-b", "feat", featBase)

	// Verify new2.txt does NOT exist yet.
	if _, err := os.Stat(filepath.Join(trunkDir, "new2.txt")); err == nil {
		t.Fatal("new2.txt should not exist before sync")
	}

	// HEAD is now refs/heads/feat.
	headRef := runGitCmd(t, trunkDir, "symbolic-ref", "-q", "HEAD")
	if headRef != "refs/heads/feat" {
		t.Fatalf("HEAD = %q; want refs/heads/feat", headRef)
	}

	runner := newRealGitRunner(t, trunkDir)
	patches := []Patch{
		{ID: "p1", BranchName: "feat", Position: 1, Status: PatchStatusActive},
	}

	ctx := context.Background()
	outcomes, _, err := refreshPatchBranches(ctx, runner, trunkDir, patches, "integration", "replace")
	if err != nil {
		t.Fatalf("refreshPatchBranches returned error: %v", err)
	}

	if len(outcomes) != 1 {
		t.Fatalf("expected 1 outcome, got %d", len(outcomes))
	}
	if outcomes[0].Action != ActionFastForwarded {
		t.Errorf("action = %q; want %q", outcomes[0].Action, ActionFastForwarded)
	}

	// Verify new2.txt now exists in the working tree.
	if _, err := os.Stat(filepath.Join(trunkDir, "new2.txt")); err != nil {
		t.Errorf("new2.txt should exist after fast-forward of checked-out branch: %v", err)
	}

	// Verify git status is clean.
	statusOutput := runGitCmd(t, trunkDir, "status", "--porcelain")
	if statusOutput != "" {
		t.Errorf("expected clean working tree, got: %q", statusOutput)
	}

	// Now test that moving a branch that is NOT HEAD does NOT hard-reset.
	// Create another branch on the fork.
	runGitCmd(t, workDir, "checkout", "main")
	runGitCmd(t, workDir, "checkout", "-b", "other")
	writeFileHelper(t, filepath.Join(workDir, "other.txt"), "other")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "other commit")
	runGitCmd(t, workDir, "push", "origin", "other")

	// Fetch in trunk.
	runGitCmd(t, trunkDir, "fetch", "origin")

	recorder := &recordingGitRunner{real: runner}
	patches2 := []Patch{
		{ID: "p2", BranchName: "other", Position: 1, Status: PatchStatusActive},
	}

	_, _, err = refreshPatchBranches(ctx, recorder, trunkDir, patches2, "integration", "replace")
	if err != nil {
		t.Fatalf("refreshPatchBranches returned error: %v", err)
	}

	// Verify no HardReset was called (other is not HEAD).
	for _, call := range recorder.runCalls {
		if len(call) >= 2 && call[0] == "reset" && call[1] == "--hard" {
			t.Error("unexpected hard reset when moving a non-HEAD branch")
		}
	}
	if len(recorder.hardResetCalls) > 0 {
		t.Error("unexpected HardReset call when moving a non-HEAD branch")
	}
}

// ===========================================================================
// TS-20-28 (property): For any hub-mode sync or report-policy sync no backup
// ref is written and no diverged branch is moved; hub mode writes no patch
// ref at all.
//
// Verifies: 20-REQ-4.5
// ===========================================================================

func TestRefreshPatchBranches_ReportPolicy_NoBackupNoMove_TS2028(t *testing.T) {
	// Set up fork with a branch.
	forkDir := t.TempDir()
	runGitCmd(t, "", "init", "--bare", forkDir)

	workDir := t.TempDir()
	runGitCmd(t, "", "clone", forkDir, workDir)
	configGitUserCmd(t, workDir)

	writeFileHelper(t, filepath.Join(workDir, "file.txt"), "hello")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "initial")
	baseSHA := runGitCmd(t, workDir, "rev-parse", "HEAD")

	// Create a branch on the fork.
	runGitCmd(t, workDir, "checkout", "-b", "diverged-branch")
	writeFileHelper(t, filepath.Join(workDir, "fork.txt"), "fork")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "fork commit")
	runGitCmd(t, workDir, "push", "origin", "diverged-branch")
	runGitCmd(t, workDir, "push", "origin", "main")

	// Create trunk with a diverged local branch.
	trunkDir := t.TempDir()
	runGitCmd(t, "", "init", "-b", "main", trunkDir)
	configGitUserCmd(t, trunkDir)
	writeFileHelper(t, filepath.Join(trunkDir, "init.txt"), "init")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "init")
	runGitCmd(t, trunkDir, "remote", "add", "origin", forkDir)
	runGitCmd(t, trunkDir, "fetch", "origin")

	// Create diverged local branch.
	runGitCmd(t, trunkDir, "checkout", "-b", "diverged-branch", baseSHA)
	writeFileHelper(t, filepath.Join(trunkDir, "local.txt"), "local")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "local commit")
	localTip := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "checkout", "main")

	runner := newRealGitRunner(t, trunkDir)
	patches := []Patch{
		{ID: "p1", BranchName: "diverged-branch", Position: 1, Status: PatchStatusActive},
	}

	// Test with report policy.
	ctx := context.Background()
	outcomes, patchAdvanced, err := refreshPatchBranches(ctx, runner, trunkDir, patches, "integration", "report")
	if err != nil {
		t.Fatalf("refreshPatchBranches returned error: %v", err)
	}

	if len(outcomes) != 1 {
		t.Fatalf("expected 1 outcome, got %d", len(outcomes))
	}

	o := outcomes[0]
	if o.Action != ActionNone {
		t.Errorf("report policy: action = %q; want %q", o.Action, ActionNone)
	}
	if o.State != StateDiverged {
		t.Errorf("report policy: state = %q; want %q", o.State, StateDiverged)
	}

	// Verify local branch is unchanged.
	currentTip := runGitCmd(t, trunkDir, "rev-parse", "refs/heads/diverged-branch")
	if currentTip != localTip {
		t.Errorf("report policy: local tip changed from %s to %s", localTip, currentTip)
	}

	// Verify no backup ref was created.
	_, backupErr := runGitCmdErr(t, trunkDir, "rev-parse", "--verify", "refs/hub/replaced/diverged-branch")
	if backupErr == nil {
		t.Error("report policy: backup ref should not exist")
	}

	// patchAdvanced should be false (diverged with report = no move).
	if patchAdvanced {
		t.Error("report policy: expected patchAdvanced=false")
	}

	// Test hub mode: the engine is never called, so we verify that
	// hub mode means no patch ref writes at all. The spec says
	// "in hub mode the engine is never called", so we just verify
	// the engine with report policy doesn't write backup refs.
	// The hub-mode check is at the caller level (sync handler).
}

// ===========================================================================
// Additional test: disabled patch does not trigger patchAdvanced
//
// Verifies: 20-REQ-4.4
// ===========================================================================

func TestRefreshPatchBranches_DisabledPatchNotAdvanced_TS2028(t *testing.T) {
	forkDir := t.TempDir()
	runGitCmd(t, "", "init", "--bare", forkDir)

	workDir := t.TempDir()
	runGitCmd(t, "", "clone", forkDir, workDir)
	configGitUserCmd(t, workDir)

	writeFileHelper(t, filepath.Join(workDir, "file.txt"), "hello")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "initial")

	runGitCmd(t, workDir, "checkout", "-b", "disabled-feat")
	writeFileHelper(t, filepath.Join(workDir, "disabled.txt"), "disabled")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "disabled commit")
	runGitCmd(t, workDir, "push", "origin", "disabled-feat")
	runGitCmd(t, workDir, "push", "origin", "main")

	trunkDir := t.TempDir()
	runGitCmd(t, "", "init", "-b", "main", trunkDir)
	configGitUserCmd(t, trunkDir)
	writeFileHelper(t, filepath.Join(trunkDir, "init.txt"), "init")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "init")
	runGitCmd(t, trunkDir, "remote", "add", "origin", forkDir)
	runGitCmd(t, trunkDir, "fetch", "origin")

	runner := newRealGitRunner(t, trunkDir)
	patches := []Patch{
		{ID: "p1", BranchName: "disabled-feat", Position: 1, Status: PatchStatusDisabled},
	}

	ctx := context.Background()
	outcomes, patchAdvanced, err := refreshPatchBranches(ctx, runner, trunkDir, patches, "integration", "replace")
	if err != nil {
		t.Fatalf("refreshPatchBranches returned error: %v", err)
	}

	if len(outcomes) != 1 {
		t.Fatalf("expected 1 outcome, got %d", len(outcomes))
	}

	// The branch should be created.
	if outcomes[0].Action != ActionCreated {
		t.Errorf("action = %q; want %q", outcomes[0].Action, ActionCreated)
	}

	// But patchAdvanced should be false because the patch is disabled.
	if patchAdvanced {
		t.Error("expected patchAdvanced=false for disabled patch")
	}
}

// ===========================================================================
// Additional test: ref-write failure stops refresh and returns error
//
// Verifies: 20-REQ-4.3
// ===========================================================================

func TestRefreshPatchBranches_RefWriteFailure_TS2024(t *testing.T) {
	forkDir := t.TempDir()
	runGitCmd(t, "", "init", "--bare", forkDir)

	workDir := t.TempDir()
	runGitCmd(t, "", "clone", forkDir, workDir)
	configGitUserCmd(t, workDir)

	writeFileHelper(t, filepath.Join(workDir, "file.txt"), "hello")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "initial")

	// Create two branches.
	for _, b := range []string{"good-branch", "bad-branch"} {
		runGitCmd(t, workDir, "checkout", "-b", b)
		writeFileHelper(t, filepath.Join(workDir, b+".txt"), b)
		runGitCmd(t, workDir, "add", ".")
		runGitCmd(t, workDir, "commit", "-m", "commit on "+b)
		runGitCmd(t, workDir, "push", "origin", b)
		runGitCmd(t, workDir, "checkout", "main")
	}
	runGitCmd(t, workDir, "push", "origin", "main")

	trunkDir := t.TempDir()
	runGitCmd(t, "", "init", "-b", "main", trunkDir)
	configGitUserCmd(t, trunkDir)
	writeFileHelper(t, filepath.Join(trunkDir, "init.txt"), "init")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "init")
	runGitCmd(t, trunkDir, "remote", "add", "origin", forkDir)
	runGitCmd(t, trunkDir, "fetch", "origin")

	// Use a runner that fails on the second update-ref.
	realRunner := newRealGitRunner(t, trunkDir)
	callCount := 0
	failingRunner := &recordingGitRunner{
		real: realRunner,
		runOverride: func(ctx context.Context, args ...string) (string, error) {
			if len(args) >= 1 && args[0] == "update-ref" {
				callCount++
				if callCount == 2 {
					return "", fmt.Errorf("simulated CAS failure")
				}
			}
			return realRunner.Run(ctx, args...)
		},
	}

	patches := []Patch{
		{ID: "p1", BranchName: "good-branch", Position: 1, Status: PatchStatusActive},
		{ID: "p2", BranchName: "bad-branch", Position: 2, Status: PatchStatusActive},
	}

	ctx := context.Background()
	outcomes, _, err := refreshPatchBranches(ctx, failingRunner, trunkDir, patches, "integration", "replace")

	// Should return an error.
	if err == nil {
		t.Fatal("expected error from ref-write failure")
	}

	// Should be a RefWriteError.
	var refErr *RefWriteError
	if !isRefWriteError(err, &refErr) {
		t.Fatalf("expected RefWriteError, got %T: %v", err, err)
	}

	if refErr.Branch != "bad-branch" {
		t.Errorf("RefWriteError.Branch = %q; want %q", refErr.Branch, "bad-branch")
	}
	if refErr.Stage != RefStageWrite {
		t.Errorf("RefWriteError.Stage = %v; want RefStageWrite", refErr.Stage)
	}

	// Should have 1 outcome (the good branch).
	if len(outcomes) != 1 {
		t.Fatalf("expected 1 outcome (good branch), got %d", len(outcomes))
	}
	if outcomes[0].BranchName != "good-branch" {
		t.Errorf("outcome[0].BranchName = %q; want %q", outcomes[0].BranchName, "good-branch")
	}
}

// ===========================================================================
// Helpers
// ===========================================================================

// newRealGitRunner creates a real GitRunnerAdapter for integration tests.
func newRealGitRunner(t *testing.T, repoPath string) GitRunner {
	t.Helper()
	factory := NewGitRunnerFactory()
	runner, err := factory(repoPath)
	if err != nil {
		t.Fatalf("failed to create git runner for %s: %v", repoPath, err)
	}
	return runner
}

// recordingGitRunner wraps a real GitRunner and records all Run calls.
type recordingGitRunner struct {
	real           GitRunner
	runCalls       [][]string
	hardResetCalls []string
	runOverride    func(ctx context.Context, args ...string) (string, error)
}

func (r *recordingGitRunner) Run(ctx context.Context, args ...string) (string, error) {
	r.runCalls = append(r.runCalls, args)
	if r.runOverride != nil {
		return r.runOverride(ctx, args...)
	}
	return r.real.Run(ctx, args...)
}

func (r *recordingGitRunner) CherryPick(ctx context.Context, commitSHA string) error {
	return r.real.CherryPick(ctx, commitSHA)
}

func (r *recordingGitRunner) MergeNoFF(ctx context.Context, ref, message string) error {
	return r.real.MergeNoFF(ctx, ref, message)
}

func (r *recordingGitRunner) MergeTree(ctx context.Context, base, head string) (string, error) {
	return r.real.MergeTree(ctx, base, head)
}

func (r *recordingGitRunner) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	return r.real.IsAncestor(ctx, ancestor, descendant)
}

func (r *recordingGitRunner) Cherry(ctx context.Context, upstream, head string) ([]string, []string, error) {
	return r.real.Cherry(ctx, upstream, head)
}

func (r *recordingGitRunner) HardReset(ctx context.Context, ref string) error {
	r.hardResetCalls = append(r.hardResetCalls, ref)
	return r.real.HardReset(ctx, ref)
}

func (r *recordingGitRunner) WorktreeAdd(ctx context.Context, path, commit string) error {
	return r.real.WorktreeAdd(ctx, path, commit)
}

func (r *recordingGitRunner) WorktreeRemove(ctx context.Context, path string) error {
	return r.real.WorktreeRemove(ctx, path)
}

func (r *recordingGitRunner) WorktreePrune(ctx context.Context) error {
	return r.real.WorktreePrune(ctx)
}

func (r *recordingGitRunner) UpdateRef(ctx context.Context, ref, sha string) error {
	return r.real.UpdateRef(ctx, ref, sha)
}

// isRefWriteError checks if err is a *RefWriteError and extracts it.
func isRefWriteError(err error, target **RefWriteError) bool {
	if rwe, ok := err.(*RefWriteError); ok {
		*target = rwe
		return true
	}
	return false
}

// ===========================================================================
// Finding 4 of issue #42: a branch that moved but whose working-tree reset
// failed keeps its outcome; an ancestry-check failure is classified apart
// from a ref-write failure.
//
// Verifies: 20-REQ-4.3
// ===========================================================================

// failingResetRunner wraps a GitRunner and fails every HardReset, simulating
// a working-tree reset that fails after the branch ref has already moved.
type failingResetRunner struct {
	GitRunner
}

func (r *failingResetRunner) HardReset(_ context.Context, _ string) error {
	return fmt.Errorf("simulated hard reset failure")
}

// failingAncestorRunner wraps a GitRunner and fails every IsAncestor call.
type failingAncestorRunner struct {
	GitRunner
}

func (r *failingAncestorRunner) IsAncestor(_ context.Context, _, _ string) (bool, error) {
	return false, fmt.Errorf("simulated merge-base failure")
}

// refTrunk is a trunk repository for refresh tests. Origin tracking refs are
// written directly with update-ref, so no remote is needed.
type refTrunk struct {
	dir  string
	base string // SHA of the initial commit
}

func newRefTrunk(t *testing.T) *refTrunk {
	t.Helper()
	dir := t.TempDir()
	runGitCmd(t, "", "init", "-b", "main", dir)
	configGitUserCmd(t, dir)
	writeFileHelper(t, filepath.Join(dir, "file.txt"), "hello")
	runGitCmd(t, dir, "add", ".")
	runGitCmd(t, dir, "commit", "-m", "initial")
	return &refTrunk{dir: dir, base: runGitCmd(t, dir, "rev-parse", "HEAD")}
}

// commit creates a commit on top of parent without touching the work tree
// and returns its SHA. Distinct messages give distinct SHAs.
func (r *refTrunk) commit(t *testing.T, parent, msg string) string {
	t.Helper()
	tree := runGitCmd(t, r.dir, "rev-parse", parent+"^{tree}")
	return runGitCmd(t, r.dir, "commit-tree", tree, "-p", parent, "-m", msg)
}

// pointHeadAt points HEAD at refs/heads/<branch> without requiring the
// branch to exist, so the refresh sees HEAD on the branch it is about to move.
func (r *refTrunk) pointHeadAt(t *testing.T, branch string) {
	t.Helper()
	runGitCmd(t, r.dir, "symbolic-ref", "HEAD", "refs/heads/"+branch)
}

func TestRefreshPatchBranches_HardResetFailure_KeepsMovedOutcome(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(t *testing.T, tr *refTrunk) (forkTip string)
		wantAction string
	}{
		{
			name: "create",
			setup: func(t *testing.T, tr *refTrunk) string {
				fork := tr.commit(t, tr.base, "fork tip")
				runGitCmd(t, tr.dir, "update-ref", "refs/remotes/origin/feat", fork)
				// HEAD names the branch that is about to be created.
				tr.pointHeadAt(t, "feat")
				return fork
			},
			wantAction: ActionCreated,
		},
		{
			name: "fast_forward",
			setup: func(t *testing.T, tr *refTrunk) string {
				runGitCmd(t, tr.dir, "branch", "feat", tr.base)
				fork := tr.commit(t, tr.base, "fork tip")
				runGitCmd(t, tr.dir, "update-ref", "refs/remotes/origin/feat", fork)
				tr.pointHeadAt(t, "feat")
				return fork
			},
			wantAction: ActionFastForwarded,
		},
		{
			name: "replace",
			setup: func(t *testing.T, tr *refTrunk) string {
				local := tr.commit(t, tr.base, "local tip")
				runGitCmd(t, tr.dir, "branch", "feat", local)
				fork := tr.commit(t, tr.base, "fork tip")
				runGitCmd(t, tr.dir, "update-ref", "refs/remotes/origin/feat", fork)
				tr.pointHeadAt(t, "feat")
				return fork
			},
			wantAction: ActionReplaced,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := newRefTrunk(t)
			forkTip := tc.setup(t, tr)
			runner := &failingResetRunner{GitRunner: newRealGitRunner(t, tr.dir)}

			patches := []Patch{
				{ID: "p1", BranchName: "feat", Position: 1, Status: PatchStatusActive},
				{ID: "p2", BranchName: "later", Position: 2, Status: PatchStatusActive},
			}
			outcomes, patchAdvanced, err := refreshPatchBranches(
				context.Background(), runner, tr.dir, patches, "integration", "replace")

			if err == nil {
				t.Fatal("expected an error from the failed hard reset")
			}
			var refErr *RefWriteError
			if !errors.As(err, &refErr) {
				t.Fatalf("expected *RefWriteError, got %T: %v", err, err)
			}
			if refErr.Branch != "feat" {
				t.Errorf("RefWriteError.Branch = %q; want %q", refErr.Branch, "feat")
			}
			if refErr.Stage != RefStageReset {
				t.Errorf("RefWriteError.Stage = %v; want RefStageReset (the ref write itself succeeded)", refErr.Stage)
			}

			// The ref moved, so the outcome is kept and counts as advanced.
			if got := refSHA(t, tr.dir, "refs/heads/feat"); got != forkTip {
				t.Fatalf("refs/heads/feat = %s; want fork tip %s (ref write should have succeeded)", got, forkTip)
			}
			if len(outcomes) != 1 {
				t.Fatalf("expected the moved branch's outcome to be kept, got %d outcomes: %+v", len(outcomes), outcomes)
			}
			o := outcomes[0]
			if o.BranchName != "feat" || o.Action != tc.wantAction || o.State != StateInSync {
				t.Errorf("outcome = %+v; want feat / %s / %s", o, tc.wantAction, StateInSync)
			}
			if o.LocalSHA != forkTip || o.OriginSHA != forkTip {
				t.Errorf("outcome SHAs local=%s origin=%s; want both %s", o.LocalSHA, o.OriginSHA, forkTip)
			}
			if !patchAdvanced {
				t.Error("patchAdvanced = false; a moved active branch must count as advanced")
			}
		})
	}
}

func TestRefreshPatchBranches_HardResetFailure_DisabledPatchNotAdvanced(t *testing.T) {
	tr := newRefTrunk(t)
	runGitCmd(t, tr.dir, "branch", "feat", tr.base)
	fork := tr.commit(t, tr.base, "fork tip")
	runGitCmd(t, tr.dir, "update-ref", "refs/remotes/origin/feat", fork)
	tr.pointHeadAt(t, "feat")
	runner := &failingResetRunner{GitRunner: newRealGitRunner(t, tr.dir)}

	patches := []Patch{{ID: "p1", BranchName: "feat", Position: 1, Status: PatchStatusDisabled}}
	outcomes, patchAdvanced, err := refreshPatchBranches(
		context.Background(), runner, tr.dir, patches, "integration", "replace")

	var refErr *RefWriteError
	if !errors.As(err, &refErr) || refErr.Stage != RefStageReset {
		t.Fatalf("expected a RefStageReset error, got %T: %v", err, err)
	}
	if len(outcomes) != 1 {
		t.Fatalf("expected the moved branch's outcome to be kept, got %d", len(outcomes))
	}
	// A disabled patch never counts as advanced (20-REQ-5.2).
	if patchAdvanced {
		t.Error("patchAdvanced = true for a moved disabled patch; want false")
	}
}

func TestRefreshPatchBranches_AncestorCheckFailure_ClassifiedSeparately(t *testing.T) {
	tr := newRefTrunk(t)
	runGitCmd(t, tr.dir, "branch", "feat", tr.base)
	fork := tr.commit(t, tr.base, "fork tip")
	runGitCmd(t, tr.dir, "update-ref", "refs/remotes/origin/feat", fork)
	runner := &failingAncestorRunner{GitRunner: newRealGitRunner(t, tr.dir)}

	patches := []Patch{{ID: "p1", BranchName: "feat", Position: 1, Status: PatchStatusActive}}
	outcomes, patchAdvanced, err := refreshPatchBranches(
		context.Background(), runner, tr.dir, patches, "integration", "replace")

	var refErr *RefWriteError
	if !errors.As(err, &refErr) {
		t.Fatalf("expected *RefWriteError, got %T: %v", err, err)
	}
	if refErr.Stage != RefStageCompare {
		t.Errorf("RefWriteError.Stage = %v; want RefStageCompare", refErr.Stage)
	}
	if refErr.Branch != "feat" {
		t.Errorf("RefWriteError.Branch = %q; want %q", refErr.Branch, "feat")
	}
	// Nothing moved: the branch is untouched and no outcome is produced.
	if got := refSHA(t, tr.dir, "refs/heads/feat"); got != tr.base {
		t.Errorf("refs/heads/feat moved to %s; want unchanged %s", got, tr.base)
	}
	if len(outcomes) != 0 {
		t.Errorf("expected no outcome for an unmoved branch, got %+v", outcomes)
	}
	if patchAdvanced {
		t.Error("patchAdvanced = true although nothing moved")
	}
}

// ===========================================================================
// Issue #45 finding 1: the compare-and-swap move is one shared helper
// ===========================================================================

func TestCasUpdateRef_RunsOnlyTheCompareAndSwapUpdateRef(t *testing.T) {
	m := newMockGitRunner()

	if err := casUpdateRef(context.Background(), m, "refs/heads/x", "new-sha", "old-sha"); err != nil {
		t.Fatalf("casUpdateRef returned %v", err)
	}
	if len(m.RunCalls) != 1 || strings.Join(m.RunCalls[0].Args, " ") != "update-ref refs/heads/x new-sha old-sha" {
		t.Errorf("git calls = %v; want exactly one update-ref <ref> <new> <old>", m.RunCalls)
	}

	boom := errors.New("boom")
	m.RunFunc = func(context.Context, ...string) (string, error) { return "", boom }
	if err := casUpdateRef(context.Background(), m, "refs/heads/x", "n", "o"); !errors.Is(err, boom) {
		t.Errorf("casUpdateRef error = %v; want it to wrap %v", err, boom)
	}
}

func TestCasMoveRef_StageAndMoved(t *testing.T) {
	const ref = "refs/heads/feat"
	writeErr := errors.New("cannot lock ref")
	resetErr := errors.New("cannot reset")

	cases := []struct {
		name      string
		updateErr error
		head      string // what `symbolic-ref -q HEAD` prints; "" means a detached HEAD
		resetErr  error

		wantMoved  bool
		wantStage  RefStage
		wantErr    error
		wantResets int
	}{
		{name: "write_fails", updateErr: writeErr, head: ref,
			wantMoved: false, wantStage: RefStageWrite, wantErr: writeErr, wantResets: 0},
		{name: "moved_head_elsewhere", head: "refs/heads/main",
			wantMoved: true, wantResets: 0},
		{name: "moved_detached_head", head: "",
			wantMoved: true, wantResets: 0},
		{name: "moved_head_on_branch", head: ref,
			wantMoved: true, wantResets: 1},
		{name: "moved_reset_fails", head: ref, resetErr: resetErr,
			wantMoved: true, wantStage: RefStageReset, wantErr: resetErr, wantResets: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockGitRunner()
			m.RunFunc = func(_ context.Context, args ...string) (string, error) {
				switch args[0] {
				case "update-ref":
					return "", tc.updateErr
				case "symbolic-ref":
					if tc.head == "" {
						return "", errors.New("not a symbolic ref")
					}
					return tc.head + "\n", nil
				}
				return "", nil
			}
			m.HardResetFunc = func(context.Context, string) error { return tc.resetErr }

			moved, stage, err := casMoveRef(context.Background(), m, "/trunk", ref, "new-sha", "old-sha")

			if moved != tc.wantMoved {
				t.Errorf("moved = %v; want %v", moved, tc.wantMoved)
			}
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("err = %v; want nil", err)
				}
			} else {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("err = %v; want %v", err, tc.wantErr)
				}
				if stage != tc.wantStage {
					t.Errorf("stage = %v; want %v", stage, tc.wantStage)
				}
			}
			if len(m.HardResetCalls) != tc.wantResets {
				t.Errorf("hard resets = %v; want %d", m.HardResetCalls, tc.wantResets)
			}
			// A failed write must stop before the work tree is looked at.
			if !tc.wantMoved {
				for _, call := range m.RunCalls {
					if call.Args[0] == "symbolic-ref" {
						t.Error("HEAD was inspected after a failed ref write")
					}
				}
			}
		})
	}
}

// ===========================================================================
// Issue #45 findings 4, 7, 8: a missing ref and a failed lookup are told apart
// by the typed git exit code, never by error text
// ===========================================================================

func TestLookupRefSHA_MissingVersusFailed(t *testing.T) {
	const sha = "aabbccddee00112233445566778899aabbccddee"
	ctx := context.Background()

	cases := []struct {
		name      string
		out       string
		runErr    error
		wantSHA   string
		wantFound bool
		wantErr   bool
	}{
		{name: "found", out: sha + "\n", wantSHA: sha, wantFound: true},
		{name: "exit_1_is_missing", runErr: &gitcmd.GitError{ExitCode: 1}},
		{name: "wrapped_exit_1_is_missing", runErr: fmt.Errorf("run: %w", &gitcmd.GitError{ExitCode: 1})},
		{name: "exit_128_is_a_failure", runErr: &gitcmd.GitError{ExitCode: 128, Stderr: "fatal: not a git repository"}, wantErr: true},
		{name: "exit_128_with_update_ref_text_is_a_failure", runErr: &gitcmd.GitError{ExitCode: 128, Stderr: "update_ref failed for ref"}, wantErr: true},
		{name: "untyped_error_is_a_failure", runErr: errors.New("exec: git: executable file not found"), wantErr: true},
		{name: "empty_output_is_missing", out: "\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockGitRunner()
			m.RunFunc = func(context.Context, ...string) (string, error) { return tc.out, tc.runErr }

			got, found, err := lookupRefSHA(ctx, m, "refs/hub/replaced/feat")

			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v; want error: %v", err, tc.wantErr)
			}
			if got != tc.wantSHA || found != tc.wantFound {
				t.Errorf("got (%q, %v); want (%q, %v)", got, found, tc.wantSHA, tc.wantFound)
			}
			wantArgs := "rev-parse --verify --quiet refs/hub/replaced/feat^{commit}"
			if len(m.RunCalls) != 1 || strings.Join(m.RunCalls[0].Args, " ") != wantArgs {
				t.Errorf("git calls = %v; want one %q", m.RunCalls, wantArgs)
			}
		})
	}
}

func TestLookupRefSHA_RealGit(t *testing.T) {
	tr := newRefTrunk(t)
	runner := newRealGitRunner(t, tr.dir)
	ctx := context.Background()

	t.Run("existing_ref", func(t *testing.T) {
		sha, found, err := lookupRefSHA(ctx, runner, "refs/heads/main")
		if err != nil || !found || sha != tr.base {
			t.Errorf("got (%q, %v, %v); want (%q, true, nil)", sha, found, err, tr.base)
		}
	})

	t.Run("missing_ref", func(t *testing.T) {
		// Real git must exit 1 here, or the typed classification is wrong.
		sha, found, err := lookupRefSHA(ctx, runner, "refs/hub/replaced/feature/none")
		if err != nil || found || sha != "" {
			t.Errorf("got (%q, %v, %v); want (\"\", false, nil)", sha, found, err)
		}
	})

	t.Run("cancelled_context_is_a_failure", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, found, err := lookupRefSHA(cancelled, runner, "refs/heads/main")
		if err == nil || found {
			t.Errorf("got (found=%v, err=%v); want a failure, not a missing ref", found, err)
		}
	})
}
