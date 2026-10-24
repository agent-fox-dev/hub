package carrypatch

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/carrypatch/gitcmd"
)

// The adapter leaves a conflicted cherry-pick in place for rerere, skips a
// commit whose change is already upstream, and passes other errors through.
func TestGitRunnerAdapter_CherryPick(t *testing.T) {
	trunk, base := newRebuildRepo(t, t.TempDir(), "ws")
	runGit(t, trunk, "checkout", "-q", "--detach", base)
	r := realGit(t, trunk)
	ctx := context.Background()

	if err := r.CherryPick(ctx, refOf(trunk, "feature/patch-a")); err != nil {
		t.Fatalf("clean cherry-pick: %v", err)
	}
	if out := runGit(t, trunk, "show", "--stat", "--format=", "HEAD"); !strings.Contains(out, "patch-a.txt") {
		t.Errorf("HEAD does not carry patch-a.txt: %s", out)
	}
	// The same change again stops as an empty cherry-pick: skipped, not a conflict.
	if err := r.CherryPick(ctx, refOf(trunk, "feature/patch-a")); err != nil {
		t.Errorf("already-applied cherry-pick = %v, want nil", err)
	}
	if refOf(trunk, "CHERRY_PICK_HEAD") != "" {
		t.Error("CHERRY_PICK_HEAD left behind after the skip")
	}

	err := r.CherryPick(ctx, refOf(trunk, "feature/conflict"))
	var conflict *CherryPickConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("conflicting cherry-pick = %v, want *CherryPickConflictError", err)
	}
	if refOf(trunk, "CHERRY_PICK_HEAD") == "" {
		t.Error("conflict was aborted; the adapter must leave it for rerere")
	}
	runGit(t, trunk, "cherry-pick", "--abort")

	if err := r.CherryPick(ctx, "0000000000000000000000000000000000000abc"); err == nil || errors.As(err, &conflict) {
		t.Errorf("unknown commit = %v, want a plain error", err)
	}
}

func TestGitRunnerAdapter_MergeNoFF(t *testing.T) {
	trunk, base := newRebuildRepo(t, t.TempDir(), "ws")
	runGit(t, trunk, "checkout", "-q", "--detach", base)
	r := realGit(t, trunk)
	ctx := context.Background()

	if err := r.MergeNoFF(ctx, refOf(trunk, "feature/patch-a"), "Merge branch 'feature/patch-a'"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if subject := runGit(t, trunk, "log", "-1", "--format=%s"); subject != "Merge branch 'feature/patch-a'" {
		t.Errorf("merge subject = %q", subject)
	}
	if parents := runGit(t, trunk, "log", "-1", "--format=%P"); len(strings.Fields(parents)) != 2 {
		t.Errorf("merge commit has parents %q, want two (--no-ff)", parents)
	}
	if err := r.MergeNoFF(ctx, refOf(trunk, "feature/patch-b"), ""); err != nil {
		t.Fatalf("merge with default message: %v", err)
	}

	err := r.MergeNoFF(ctx, refOf(trunk, "feature/conflict"), "")
	var conflict *MergeNoFFConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("conflicting merge = %v, want *MergeNoFFConflictError", err)
	}
	if refOf(trunk, "MERGE_HEAD") == "" {
		t.Error("conflicted merge was aborted; the adapter must leave it for rerere")
	}
	runGit(t, trunk, "merge", "--abort")

	if err := r.MergeNoFF(ctx, "no-such-ref", ""); err == nil || errors.As(err, &conflict) {
		t.Errorf("unknown ref = %v, want a plain error", err)
	}
}

// The remaining adapter methods delegate to gitcmd over the real repository.
func TestGitRunnerAdapter_Delegates(t *testing.T) {
	trunk, base := newRebuildRepo(t, t.TempDir(), "ws")
	r := realGit(t, trunk)
	ctx := context.Background()
	patchA := refOf(trunk, "feature/patch-a")

	if tree, err := r.MergeTree(ctx, base, patchA); err != nil || tree == "" {
		t.Errorf("MergeTree clean = %q, %v", tree, err)
	}
	var mc *gitcmd.MergeConflictError
	if _, err := r.MergeTree(ctx, base, refOf(trunk, "feature/conflict")); !errors.As(err, &mc) || !equalStrings(mc.ConflictingFiles, []string{"base.txt"}) {
		t.Errorf("MergeTree conflict = %v", err)
	}
	root := runGit(t, trunk, "rev-list", "--max-parents=0", "HEAD")
	if ok, err := r.IsAncestor(ctx, root, base); err != nil || !ok {
		t.Errorf("IsAncestor(root, base) = %v, %v", ok, err)
	}
	if ok, _ := r.IsAncestor(ctx, base, patchA); ok {
		t.Error("IsAncestor(base, patch-a) = true; the patch branched before the upstream change")
	}
	if applied, pending, err := r.Cherry(ctx, base, patchA); err != nil || len(applied) != 0 || len(pending) != 1 {
		t.Errorf("Cherry = applied %v pending %v err %v", applied, pending, err)
	}
	if err := r.UpdateRef(ctx, "refs/heads/moved", patchA); err != nil || refOf(trunk, "moved") != patchA {
		t.Errorf("UpdateRef: %v, ref = %s", err, refOf(trunk, "moved"))
	}
	runGit(t, trunk, "checkout", "-q", "--detach", patchA)
	if err := r.HardReset(ctx, base); err != nil || refOf(trunk, "HEAD") != base {
		t.Errorf("HardReset: %v, HEAD = %s", err, refOf(trunk, "HEAD"))
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if err := r.WorktreeAdd(ctx, wt, base); err != nil || refOf(wt, "HEAD") != base {
		t.Fatalf("WorktreeAdd: %v, HEAD = %s", err, refOf(wt, "HEAD"))
	}
	if err := r.WorktreeRemove(ctx, wt); err != nil {
		t.Errorf("WorktreeRemove: %v", err)
	}
	if err := r.WorktreePrune(ctx); err != nil {
		t.Errorf("WorktreePrune: %v", err)
	}
	if strings.Count(runGit(t, trunk, "worktree", "list", "--porcelain"), "worktree ") != 1 {
		t.Error("linked worktree still registered after remove and prune")
	}
	if out, err := r.Run(ctx, "rev-parse", "--verify", "refs/heads/main^{commit}"); err != nil || out == "" {
		t.Errorf("Run = %q, %v", out, err)
	}
	if _, err := NewGitRunnerFactory()(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("factory accepted a missing directory")
	}
}

// Explicit overrides win over the workspace settings; an unset or invalid
// setting falls back to the default.
func TestBuildRebuildPayload_OverridesAndDefaults(t *testing.T) {
	vars := map[string]string{"REBUILD_STRATEGY": StrategyMerge, "REBUILD_FAIL_MODE": FailModeContinue}
	cases := []struct {
		name                   string
		vars                   map[string]string
		strategy, failMode     string
		wantStrategy, wantFail string
	}{
		{"settings", vars, "", "", StrategyMerge, FailModeContinue},
		{"overrides", vars, StrategyRebase, FailModeFailFast, StrategyRebase, FailModeFailFast},
		{"invalid settings", map[string]string{"REBUILD_STRATEGY": "x", "REBUILD_FAIL_MODE": "y"}, "", "", StrategyRebase, ""},
		{"unset", map[string]string{}, "", "", StrategyRebase, ""},
	}
	for _, c := range cases {
		p := BuildRebuildPayload("ws", "deploy", "me", varsOf(c.vars, nil), c.strategy, c.failMode)
		if p.Strategy != c.wantStrategy || p.FailMode != c.wantFail || p.WorkspaceSlug != "ws" || p.IntegrationBranch != "deploy" || p.SubmittedBy != "me" {
			t.Errorf("%s: payload = %+v", c.name, p)
		}
	}
	if p := BuildRebuildPayload("ws", "", "", nil, "", ""); p.Strategy != StrategyRebase || p.FailMode != "" {
		t.Errorf("nil lookup: %+v", p)
	}
	if FormatGroupKey("ws", "deploy") != "ws:deploy" {
		t.Error("FormatGroupKey")
	}
	if perms := CarryPatchPermissions(); len(perms) != 2 || perms[0].Resource != "rebuilds" || perms[1].Action != "write" {
		t.Errorf("permissions = %v", perms)
	}
}

// The upstream base is the first of upstream/HEAD, upstream/<branch> and
// FETCH_HEAD that resolves.
func TestResolveUpstreamBase_FallbackOrder(t *testing.T) {
	cases := []struct {
		name     string
		resolves map[string]string
		branch   string
		want     string
		wantErr  bool
	}{
		{"upstream HEAD", map[string]string{UpstreamHeadRef + "^{commit}": "h1", "refs/remotes/upstream/main^{commit}": "m1", "FETCH_HEAD": "f1"}, "main", "h1", false},
		{"workspace branch", map[string]string{"refs/remotes/upstream/main^{commit}": "m1", "FETCH_HEAD": "f1"}, "main", "m1", false},
		{"fetch head", map[string]string{"FETCH_HEAD": "f1"}, "main", "f1", false},
		{"no branch skips the tracking ref", map[string]string{"refs/remotes/upstream/main^{commit}": "m1", "FETCH_HEAD": "f1"}, "", "f1", false},
		{"nothing", map[string]string{}, "main", "", true},
	}
	for _, c := range cases {
		g := newFakeGit()
		g.run = func(args ...string) (string, error) {
			if sha, ok := c.resolves[args[len(args)-1]]; ok {
				return sha, nil
			}
			return "", errors.New("unknown revision")
		}
		got, err := resolveUpstreamBase(context.Background(), g, c.branch)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%s: %q, %v", c.name, got, err)
		}
	}
}

func TestWorkspaceBranch_ReadsTheWorkspaceRow(t *testing.T) {
	db := openDB(t)
	seedWS(t, db, ws{slug: "ws", branch: "develop"})
	if got := workspaceBranch(db, "ws"); got != "develop" {
		t.Errorf("branch = %q", got)
	}
	if got := workspaceBranch(db, "nope"); got != "" {
		t.Errorf("unknown slug = %q", got)
	}
	if got := workspaceBranch((*sql.DB)(nil), "ws"); got != "" {
		t.Errorf("nil db = %q", got)
	}
}

// The upstream fetch records the upstream default branch under
// refs/remotes/upstream/HEAD and every upstream branch under
// refs/remotes/upstream/*; an up-to-date remote is success.
func TestFetchUpstream_RecordsUpstreamHead(t *testing.T) {
	f := newForkTrunk(t, t.TempDir(), "ws")
	up := newForkTrunk(t, t.TempDir(), "up")
	work := filepath.Join(t.TempDir(), "upwork")
	runGit(t, "", "clone", "-q", up.fork, work)
	upTip := commitFile(t, work, "u.txt", "u\n", "upstream change")
	runGit(t, work, "push", "-q", "origin", "main")
	runGit(t, f.trunk, "update-ref", "-d", UpstreamHeadRef)
	runGit(t, f.trunk, "remote", "add", "upstream", up.fork)

	fetch := DefaultFetchFunc()
	ctx := context.Background()
	if err := fetch(ctx, f.trunk, nil); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := refOf(f.trunk, UpstreamHeadRef); got != upTip {
		t.Errorf("upstream/HEAD = %s, want %s", got, upTip)
	}
	if got := refOf(f.trunk, "refs/remotes/upstream/main"); got != upTip {
		t.Errorf("upstream/main = %s, want %s", got, upTip)
	}
	if err := fetch(ctx, f.trunk, nil); err != nil {
		t.Errorf("second fetch (up to date): %v", err)
	}
	runGit(t, f.trunk, "remote", "remove", "upstream")
	if err := fetch(ctx, f.trunk, nil); err == nil || !strings.Contains(err.Error(), `remote "upstream"`) {
		t.Errorf("missing remote = %v", err)
	}
	if err := fetch(ctx, filepath.Join(t.TempDir(), "none"), nil); err == nil {
		t.Error("missing repository accepted")
	}
}

// The origin fetch brings every fork branch into refs/remotes/origin/*,
// fetches no tags and prunes tracking refs of deleted fork branches.
func TestDefaultFetchOriginFunc_BranchesNoTagsPrune(t *testing.T) {
	f := newForkTrunk(t, t.TempDir(), "ws")
	tip := f.pushBranch(t, "feat", "f.txt", "f\n")
	work := filepath.Join(t.TempDir(), "tagger")
	runGit(t, "", "clone", "-q", f.fork, work)
	runGit(t, work, "tag", "v1")
	runGit(t, work, "push", "-q", "origin", "v1")

	fetch := DefaultFetchOriginFunc()
	ctx := context.Background()
	if err := fetch(ctx, f.trunk, nil); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if refOf(f.trunk, "refs/remotes/origin/feat") != tip {
		t.Error("origin/feat not fetched")
	}
	if out, _ := gitErr(f.trunk, "tag", "-l"); out != "" {
		t.Errorf("tags fetched: %q", out)
	}
	runGit(t, f.fork, "update-ref", "-d", "refs/heads/feat")
	if err := fetch(ctx, f.trunk, nil); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if refOf(f.trunk, "refs/remotes/origin/feat") != "" {
		t.Error("tracking ref of the deleted fork branch was not pruned")
	}
	if err := fetch(ctx, f.trunk, nil); err != nil {
		t.Errorf("up-to-date fetch: %v", err)
	}
	runGit(t, f.trunk, "remote", "remove", "origin")
	if err := fetch(ctx, f.trunk, nil); err == nil {
		t.Error("missing origin remote accepted")
	}
	if err := fetch(ctx, filepath.Join(t.TempDir(), "none"), nil); err == nil {
		t.Error("missing repository accepted")
	}
}

// The single-branch fetch brings one branch, answers ErrBranchNotOnOrigin
// from the typed go-git error for a branch the fork lacks, and treats an
// up-to-date fetch as success.
func TestDefaultSingleBranchFetch_Classification(t *testing.T) {
	f := newForkTrunk(t, t.TempDir(), "ws")
	tip := f.pushBranch(t, "feat", "f.txt", "f\n")
	f.pushBranch(t, "other", "o.txt", "o\n")
	fetch := DefaultSingleBranchFetch()
	ctx := context.Background()

	if err := fetch(ctx, f.trunk, "feat", nil); err != nil {
		t.Fatalf("fetch feat: %v", err)
	}
	if refOf(f.trunk, "refs/remotes/origin/feat") != tip || refOf(f.trunk, "refs/remotes/origin/other") != "" {
		t.Error("fetch did not bring exactly the requested branch")
	}
	if err := fetch(ctx, f.trunk, "feat", nil); err != nil {
		t.Errorf("up-to-date fetch = %v", err)
	}
	if err := fetch(ctx, f.trunk, "missing", nil); !errors.Is(err, ErrBranchNotOnOrigin) {
		t.Errorf("missing branch = %v, want ErrBranchNotOnOrigin", err)
	}
	if err := fetch(ctx, filepath.Join(t.TempDir(), "none"), "feat", nil); err == nil || errors.Is(err, ErrBranchNotOnOrigin) {
		t.Errorf("missing repository = %v", err)
	}
	runGit(t, f.trunk, "remote", "remove", "origin")
	if err := fetch(ctx, f.trunk, "feat", nil); err == nil || errors.Is(err, ErrBranchNotOnOrigin) {
		t.Errorf("missing remote = %v", err)
	}
}

// The integration push force-updates the fork's branch with the resolved
// origin credentials; a credential failure is returned before any push.
func TestDefaultPushIntegrationFunc_ForcePushesToOrigin(t *testing.T) {
	f := newForkTrunk(t, t.TempDir(), "ws")
	first := f.localBranch(t, "integration", "i.txt", "one\n")
	push := DefaultPushIntegrationFunc(noAuth)
	ctx := context.Background()

	if err := push(ctx, "ws", f.trunk, "integration"); err != nil {
		t.Fatalf("push: %v", err)
	}
	if refOf(f.fork, "integration") != first {
		t.Error("fork integration branch not updated")
	}
	runGit(t, f.trunk, "update-ref", "refs/heads/integration", f.base)
	if err := push(ctx, "ws", f.trunk, "integration"); err != nil || refOf(f.fork, "integration") != f.base {
		t.Errorf("rewrite push: %v, fork = %s", err, refOf(f.fork, "integration"))
	}
	if err := push(ctx, "ws", f.trunk, "integration"); err != nil {
		t.Errorf("up-to-date push: %v", err)
	}
	failing := DefaultPushIntegrationFunc(func(string) (transport.AuthMethod, error) { return nil, errors.New("no creds") })
	if err := failing(ctx, "ws", f.trunk, "integration"); err == nil || !strings.Contains(err.Error(), "resolve origin credentials") {
		t.Errorf("credential failure = %v", err)
	}
	if err := push(ctx, "ws", filepath.Join(t.TempDir(), "none"), "integration"); err == nil {
		t.Error("missing repository accepted")
	}
}

func TestErrorTypes_Messages(t *testing.T) {
	cause := errors.New("boom")
	cases := map[string]struct {
		err  error
		want string
	}{
		"transient":           {&TransientError{Err: cause}, "transient error: boom"},
		"transient bare":      {&TransientError{}, "transient error"},
		"cherry-pick":         {&CherryPickConflictError{}, "cherry-pick conflict"},
		"merge":               {&MergeNoFFConflictError{}, "merge --no-ff conflict"},
		"rebuild conflict":    {&rebuildConflictError{files: []string{"a", "b"}}, "unresolved conflict: a, b"},
		"ref write":           {&RefWriteError{Branch: "f", Err: cause}, "failed to update patch branch f: boom"},
		"ref compare":         {&RefWriteError{Branch: "f", Err: cause, Stage: RefStageCompare}, "failed to compare patch branch f with origin: boom"},
		"ref reset":           {&RefWriteError{Branch: "f", Err: cause, Stage: RefStageReset}, "failed to reset working tree for patch branch f: boom"},
		"reset with cause":    {&ResetError{Message: "m", Cause: cause}, "m: boom"},
		"reset without cause": {&ResetError{Message: "m"}, "m"},
		"resolve with cause":  {newBranchResolveError(BranchResolveKindNotFound, cause), "not_found: boom"},
		"resolve bare":        {newBranchResolveError(BranchResolveKindWorkspaceBusy, nil), "workspace_busy"},
	}
	for name, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	for _, err := range []error{&TransientError{Err: cause}, &RefWriteError{Err: cause}, &ResetError{Cause: cause}, newBranchResolveError("k", cause)} {
		if !errors.Is(err, cause) {
			t.Errorf("%T does not unwrap its cause", err)
		}
	}
}
