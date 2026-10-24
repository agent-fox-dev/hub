package carrypatch

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-fox-dev/carrypatch/gitcmd"
)

// refreshRepo is a trunk whose origin tracking refs are set directly, which
// is all the refresh reads.
func refreshRepo(t *testing.T) *forkTrunk {
	t.Helper()
	return newForkTrunk(t, t.TempDir(), "ws")
}

func (f *forkTrunk) setOrigin(t *testing.T, branch, sha string) {
	t.Helper()
	runGit(t, f.trunk, "update-ref", "refs/remotes/origin/"+branch, sha)
}

// The refresh considers active, conflict and disabled patches other than the
// integration branch, applies the first matching rule to each, and reports a
// patch as advanced only when an active or conflict branch moved.
func TestRefreshPatchBranches_RulesAndCandidates(t *testing.T) {
	f := refreshRepo(t)
	ahead := f.localBranch(t, "tmp/ahead", "a.txt", "a\n")
	other := f.localBranch(t, "tmp/other", "o.txt", "o\n")
	runGit(t, f.trunk, "branch", "same", f.base)
	f.setOrigin(t, "same", f.base)
	runGit(t, f.trunk, "branch", "ff", f.base)
	f.setOrigin(t, "ff", ahead)
	f.setOrigin(t, "created", ahead)
	runGit(t, f.trunk, "branch", "div", other)
	f.setOrigin(t, "div", ahead)
	runGit(t, f.trunk, "branch", "missing", f.base)
	runGit(t, f.trunk, "branch", "off", f.base)
	f.setOrigin(t, "off", ahead)
	f.setOrigin(t, "integration", ahead)
	f.setOrigin(t, "merged", ahead)
	patches := []Patch{
		{ID: "1", BranchName: "same", Position: 1, Status: PatchStatusActive},
		{ID: "2", BranchName: "ff", Position: 2, Status: PatchStatusConflict},
		{ID: "3", BranchName: "created", Position: 3, Status: PatchStatusActive},
		{ID: "4", BranchName: "div", Position: 4, Status: PatchStatusActive},
		{ID: "5", BranchName: "missing", Position: 5, Status: PatchStatusActive},
		{ID: "6", BranchName: "off", Position: 6, Status: PatchStatusDisabled},
		{ID: "7", BranchName: "integration", Position: 7, Status: PatchStatusActive},
		{ID: "8", BranchName: "merged", Position: 8, Status: PatchStatusMergedUpstream},
		{ID: "9", BranchName: "gone", Position: 9, Status: PatchStatusDeleted},
	}
	g := realGit(t, f.trunk)

	outcomes, advanced, err := refreshPatchBranches(context.Background(), g, f.trunk, patches, "integration", "replace")
	if err != nil || !advanced {
		t.Fatalf("refresh: advanced=%v err=%v", advanced, err)
	}
	want := map[string]PatchRefreshOutcome{
		"same":    {Action: ActionNone, State: StateInSync, LocalSHA: f.base, OriginSHA: f.base},
		"ff":      {Action: ActionFastForwarded, State: StateInSync, LocalSHA: ahead, OriginSHA: ahead},
		"created": {Action: ActionCreated, State: StateInSync, LocalSHA: ahead, OriginSHA: ahead},
		"div":     {Action: ActionReplaced, State: StateInSync, LocalSHA: ahead, OriginSHA: ahead, ReplacedSHA: other},
		"missing": {Action: ActionNone, State: StateMissingOnOrigin, LocalSHA: f.base},
		"off":     {Action: ActionFastForwarded, State: StateInSync, LocalSHA: ahead, OriginSHA: ahead},
	}
	if len(outcomes) != len(want) {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	for _, o := range outcomes {
		w := want[o.BranchName]
		w.PatchID, w.BranchName, w.Position, w.Status = o.PatchID, o.BranchName, o.Position, o.Status
		if o != w {
			t.Errorf("%s = %+v, want %+v", o.BranchName, o, w)
		}
		if o.Action != ActionNone && refOf(f.trunk, o.BranchName) != o.LocalSHA {
			t.Errorf("%s ref = %s, want %s", o.BranchName, refOf(f.trunk, o.BranchName), o.LocalSHA)
		}
	}
	if refOf(f.trunk, "refs/hub/replaced/div") != other || refOf(f.trunk, "integration") != "" {
		t.Error("backup ref or integration branch wrong")
	}
	// Replacing again overwrites the backup; a disabled move alone is not an advance.
	runGit(t, f.trunk, "update-ref", "refs/heads/div", other)
	outcomes, advanced, err = refreshPatchBranches(context.Background(), g, f.trunk, patches[3:4], "integration", "replace")
	if err != nil || !advanced || outcomes[0].ReplacedSHA != other || refOf(f.trunk, "refs/hub/replaced/div") != other {
		t.Errorf("second replace: %+v %v %v", outcomes, advanced, err)
	}
	runGit(t, f.trunk, "update-ref", "refs/heads/off", f.base)
	if _, advanced, err := refreshPatchBranches(context.Background(), g, f.trunk, patches[5:6], "integration", "replace"); err != nil || advanced {
		t.Errorf("disabled move counted as advanced (%v)", err)
	}
	// Report policy leaves a diverged branch and writes no backup.
	runGit(t, f.trunk, "update-ref", "-d", "refs/hub/replaced/div")
	runGit(t, f.trunk, "update-ref", "refs/heads/div", other)
	outcomes, advanced, _ = refreshPatchBranches(context.Background(), g, f.trunk, patches[3:4], "integration", "report")
	if advanced || outcomes[0].State != StateDiverged || outcomes[0].Action != ActionNone || refOf(f.trunk, "div") != other || refOf(f.trunk, "refs/hub/replaced/div") != "" {
		t.Errorf("report policy: %+v", outcomes)
	}
}

// A failed step stops the refresh after the outcomes so far: a failed ref
// write or ancestry check produces no outcome for its branch, a failed
// work-tree reset after the ref moved keeps the outcome and the advance.
func TestRefreshPatchBranches_FailureStages(t *testing.T) {
	f := refreshRepo(t)
	ahead := f.localBranch(t, "tmp/ahead", "a.txt", "a\n")
	for _, b := range []string{"one", "two", "three"} {
		runGit(t, f.trunk, "branch", b, f.base)
		f.setOrigin(t, b, ahead)
	}
	patches := []Patch{{ID: "1", BranchName: "one", Status: PatchStatusActive}, {ID: "2", BranchName: "two", Status: PatchStatusActive}, {ID: "3", BranchName: "three", Status: PatchStatusActive}}
	real := realGit(t, f.trunk)
	ctx := context.Background()

	spy := &spyGit{GitRunner: real, onRun: func(args []string) (string, error, bool) {
		if args[0] == "update-ref" && args[1] == "refs/heads/two" {
			return "", errors.New("locked"), true
		}
		return "", nil, false
	}}
	outcomes, advanced, err := refreshPatchBranches(ctx, spy, f.trunk, patches, "", "replace")
	var rw *RefWriteError
	if !errors.As(err, &rw) || rw.Stage != RefStageWrite || rw.Branch != "two" || !advanced || len(outcomes) != 1 || outcomes[0].BranchName != "one" {
		t.Errorf("write failure: outcomes=%+v advanced=%v err=%v", outcomes, advanced, err)
	}
	if refOf(f.trunk, "two") != f.base || refOf(f.trunk, "three") != f.base {
		t.Error("refresh continued past the failure")
	}

	runGit(t, f.trunk, "update-ref", "refs/heads/one", f.base)
	compare := &spyGit{GitRunner: real, onIsAncestor: func(string, string) error { return errors.New("corrupt") }}
	outcomes, advanced, err = refreshPatchBranches(ctx, compare, f.trunk, patches, "", "replace")
	if !errors.As(err, &rw) || rw.Stage != RefStageCompare || advanced || len(outcomes) != 0 || refOf(f.trunk, "one") != f.base {
		t.Errorf("compare failure: outcomes=%+v advanced=%v err=%v", outcomes, advanced, err)
	}

	runGit(t, f.trunk, "checkout", "-q", "one")
	reset := &spyGit{GitRunner: real, onHardReset: func(string) error { return errors.New("dirty") }}
	outcomes, advanced, err = refreshPatchBranches(ctx, reset, f.trunk, patches[:1], "", "replace")
	if !errors.As(err, &rw) || rw.Stage != RefStageReset || !advanced || len(outcomes) != 1 || outcomes[0].Action != ActionFastForwarded || refOf(f.trunk, "one") != ahead {
		t.Errorf("reset failure: outcomes=%+v advanced=%v err=%v", outcomes, advanced, err)
	}
	// A disabled patch whose reset failed moved but is not an advance.
	runGit(t, f.trunk, "update-ref", "refs/heads/one", f.base)
	disabled := []Patch{{ID: "1", BranchName: "one", Status: PatchStatusDisabled}}
	if _, advanced, err := refreshPatchBranches(ctx, reset, f.trunk, disabled, "", "replace"); advanced || err == nil {
		t.Errorf("disabled reset failure: advanced=%v err=%v", advanced, err)
	}
}

// casMoveRef writes the ref only when it still holds the expected value, and
// resets the work tree only when the branch is checked out; its stage tells a
// failed write from a failed reset.
func TestCasMoveRef_StagesAndCheckout(t *testing.T) {
	f := refreshRepo(t)
	ahead := f.localBranch(t, "tmp/ahead", "a.txt", "a\n")
	runGit(t, f.trunk, "branch", "b", f.base)
	g := realGit(t, f.trunk)
	ctx := context.Background()

	if moved, stage, err := casMoveRef(ctx, g, f.trunk, "refs/heads/b", ahead, zeroSHA); moved || stage != RefStageWrite || err == nil {
		t.Errorf("create over an existing ref: moved=%v stage=%v err=%v", moved, stage, err)
	}
	if moved, stage, err := casMoveRef(ctx, g, f.trunk, "refs/heads/b", ahead, ahead); moved || stage != RefStageWrite || err == nil {
		t.Errorf("wrong expected value: moved=%v stage=%v err=%v", moved, stage, err)
	}
	if moved, _, err := casMoveRef(ctx, g, f.trunk, "refs/heads/b", ahead, f.base); !moved || err != nil || refOf(f.trunk, "b") != ahead {
		t.Errorf("move: moved=%v err=%v", moved, err)
	}
	if refOf(f.trunk, "HEAD") != f.base {
		t.Error("work tree reset although the branch is not checked out")
	}
	runGit(t, f.trunk, "checkout", "-q", "b")
	if moved, _, err := casMoveRef(ctx, g, f.trunk, "refs/heads/b", f.base, ahead); !moved || err != nil || refOf(f.trunk, "HEAD") != f.base {
		t.Errorf("move of the checked-out branch: moved=%v err=%v HEAD=%s", moved, err, refOf(f.trunk, "HEAD"))
	}
	if err := casUpdateRef(ctx, g, "refs/heads/new", ahead, zeroSHA); err != nil || refOf(f.trunk, "new") != ahead {
		t.Errorf("create: %v", err)
	}
	if err := maybeHardReset(ctx, g, f.trunk, "refs/heads/new"); err != nil {
		t.Errorf("reset of a non-checked-out branch: %v", err)
	}
	runGit(t, f.trunk, "checkout", "-q", "--detach", f.base)
	if err := maybeHardReset(ctx, g, f.trunk, "refs/heads/b"); err != nil {
		t.Errorf("detached HEAD: %v", err)
	}
}

// lookupRefSHA tells a missing ref (exit code 1) from a failed lookup.
func TestLookupRefSHA_MissingVersusFailed(t *testing.T) {
	f := refreshRepo(t)
	g := realGit(t, f.trunk)
	ctx := context.Background()
	if sha, found, err := lookupRefSHA(ctx, g, "refs/heads/main"); err != nil || !found || sha != f.base {
		t.Errorf("present = %s/%v/%v", sha, found, err)
	}
	if _, found, err := lookupRefSHA(ctx, g, "refs/heads/none"); err != nil || found {
		t.Errorf("missing = %v/%v", found, err)
	}
	failing := newFakeGit()
	failing.run = func(...string) (string, error) { return "", &gitcmd.GitError{ExitCode: 128, Stderr: "corrupt"} }
	if _, found, err := lookupRefSHA(ctx, failing, "refs/heads/x"); err == nil || found {
		t.Errorf("failed lookup = %v/%v, want an error", found, err)
	}
	failing.run = func(...string) (string, error) { return "", &gitcmd.GitError{ExitCode: 1} }
	if _, found, err := lookupRefSHA(ctx, failing, "refs/heads/x"); err != nil || found {
		t.Errorf("exit 1 = %v/%v, want missing", found, err)
	}
	failing.run = func(...string) (string, error) { return "  ", nil }
	if _, found, err := lookupRefSHA(ctx, failing, "refs/heads/x"); err != nil || found {
		t.Errorf("empty output = %v/%v, want missing", found, err)
	}
	if sha, err := resolveRefSHA(ctx, g, "refs/heads/main"); err != nil || sha != f.base {
		t.Errorf("resolveRefSHA = %s, %v", sha, err)
	}
}
