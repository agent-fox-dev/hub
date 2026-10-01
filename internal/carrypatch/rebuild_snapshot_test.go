package carrypatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// Spec 01, task 6: patch tips are snapshotted as SHAs and reported as
// source_sha.
// ===========================================================================

// snapshotArgs returns the branch when args is `rev-parse --verify
// refs/heads/<branch>^{commit}`.
func snapshotArgs(args []string) (string, bool) {
	if len(args) != 3 || args[0] != "rev-parse" || args[1] != "--verify" {
		return "", false
	}
	if !strings.HasPrefix(args[2], "refs/heads/") || !strings.HasSuffix(args[2], "^{commit}") {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(args[2], "refs/heads/"), "^{commit}"), true
}

func resultOf(t *testing.T, res any) *RebuildResult {
	t.Helper()
	r, ok := res.(*RebuildResult)
	if !ok || r == nil {
		t.Fatalf("result is %T, want *RebuildResult", res)
	}
	return r
}

// ===========================================================================
// TS-01-39: patch tips are resolved to SHAs in the trunk runner and only SHAs
// are used afterwards. Requirement: 01-REQ-6.1
// ===========================================================================

func TestSnapshot_TS_01_39_TipsResolvedAndOnlyShasUsed(t *testing.T) {
	patches := []Patch{
		{ID: "p1", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		{ID: "p2", BranchName: "feature/merged", Position: 2, Status: PatchStatusMergedUpstream},
		{ID: "p3", BranchName: "feature/disabled", Position: 3, Status: PatchStatusDisabled},
		{ID: "p4", BranchName: "feature/deleted", Position: 4, Status: PatchStatusDeleted},
		{ID: "p5", BranchName: "feature/b", Position: 5, Status: PatchStatusActive},
	}
	snap := map[string]string{"feature/a": "1a1a1a1a", "feature/b": "1b1b1b1b"}

	for _, strategy := range []string{StrategyRebase, StrategyMerge} {
		t.Run(strategy, func(t *testing.T) {
			e := newWtEnv(t, patches)

			var mu sync.Mutex
			resolved := map[string]int{}
			base := e.trunk.RunFunc
			e.trunk.RunFunc = func(ctx context.Context, args ...string) (string, error) {
				if br, ok := snapshotArgs(args); ok {
					mu.Lock()
					n := resolved[br]
					resolved[br]++
					mu.Unlock()
					if s, found := snap[br]; found {
						if n > 0 { // the branch moved after the snapshot
							return "moved-" + s, nil
						}
						return s, nil
					}
				}
				return base(ctx, args...)
			}

			res, retryable, err := e.h.HandleRebuildJob(context.Background(), e.payload(strategy, ""))
			if err != nil {
				t.Fatalf("HandleRebuildJob: retryable=%v err=%v", retryable, err)
			}
			r := resultOf(t, res)

			mu.Lock()
			for br := range resolved {
				if _, ok := snap[br]; !ok {
					t.Errorf("rev-parse --verify ran for %q, which takes no part in the run", br)
				}
			}
			for br := range snap {
				if resolved[br] == 0 {
					t.Errorf("branch %q was not resolved", br)
				}
			}
			mu.Unlock()

			// The resolution happens in the trunk runner before the first
			// worktree command.
			firstWt := e.events.index("wt:")
			lastSnap := -1
			// Only the resolutions before the worktree removal are the
			// snapshot; the stale-input check re-resolves the tips after it.
			for i, ev := range e.events.snapshot() {
				if strings.HasPrefix(ev, "trunk:WorktreeRemove") {
					break
				}
				if strings.HasPrefix(ev, "trunk:run rev-parse --verify refs/heads/") && strings.HasSuffix(ev, "^{commit}") {
					lastSnap = i
				}
			}
			if lastSnap < 0 || firstWt < 0 || lastSnap > firstWt {
				t.Errorf("snapshot must precede the first worktree command (snap=%d wt=%d); events=%v",
					lastSnap, firstWt, e.events.snapshot())
			}

			// Worktree commands use only the snapshot SHAs.
			e.wt.mu.Lock()
			defer e.wt.mu.Unlock()
			branches := []string{"feature/a", "feature/b"}
			for _, c := range e.wt.RunCalls {
				for _, a := range c.Args {
					for _, br := range branches {
						if strings.Contains(a, br) {
							t.Errorf("worktree git call %v uses branch name %q", c.Args, br)
						}
					}
				}
			}
			for _, c := range e.wt.MergeNoFFCalls {
				if c.Branch != snap["feature/a"] && c.Branch != snap["feature/b"] {
					t.Errorf("MergeNoFF ref = %q, want a snapshot SHA", c.Branch)
				}
			}
			if strategy == StrategyRebase {
				var logs []string
				for _, c := range e.wt.RunCalls {
					if len(c.Args) > 0 && c.Args[0] == "log" {
						logs = append(logs, c.Args[len(c.Args)-1])
					}
				}
				want := []string{wtBaseSHA + "..." + snap["feature/a"], wtBaseSHA + "..." + snap["feature/b"]}
				if fmt.Sprint(logs) != fmt.Sprint(want) {
					t.Errorf("log ranges = %v, want %v", logs, want)
				}
			} else if len(e.wt.MergeNoFFCalls) != 2 {
				t.Errorf("MergeNoFF calls = %d, want 2", len(e.wt.MergeNoFFCalls))
			}
			if r.PatchResults[0].SourceSHA != snap["feature/a"] || r.PatchResults[4].SourceSHA != snap["feature/b"] {
				t.Errorf("source_sha = %q/%q, want the first resolution", r.PatchResults[0].SourceSHA, r.PatchResults[4].SourceSHA)
			}
		})
	}
}

// ===========================================================================
// TS-01-40: the rebase strategy lists commits with git log ... <base>...<sha>
// in the worktree. Requirement: 01-REQ-6.2
// ===========================================================================

func TestSnapshot_TS_01_40_RebaseLogUsesSnapshotRange(t *testing.T) {
	e := newWtEnv(t, onePatch())
	base := e.trunk.RunFunc
	e.trunk.RunFunc = func(ctx context.Context, args ...string) (string, error) {
		if _, ok := snapshotArgs(args); ok {
			return "sha1", nil
		}
		return base(ctx, args...)
	}

	if _, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, "")); err != nil {
		t.Fatalf("HandleRebuildJob: %v", err)
	}

	var logArgs []string
	e.wt.mu.Lock()
	for _, c := range e.wt.RunCalls {
		if len(c.Args) > 0 && c.Args[0] == "log" {
			logArgs = c.Args
		}
	}
	e.wt.mu.Unlock()
	if logArgs == nil {
		t.Fatal("the worktree runner received no log call")
	}
	// --format=%H is what makes the output parseable; the spec's argument
	// list is otherwise exact.
	var got []string
	for _, a := range logArgs {
		if !strings.HasPrefix(a, "--format=") {
			got = append(got, a)
		}
	}
	want := []string{"log", "--reverse", "--no-merges", "--right-only", "--cherry-pick", wtBaseSHA + "...sha1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("log args = %v, want %v", got, want)
	}
	for _, c := range e.trunk.RunCalls {
		if len(c.Args) > 0 && c.Args[0] == "log" {
			t.Errorf("trunk runner ran log: %v", c.Args)
		}
	}
}

// ===========================================================================
// TS-01-41 (integration): a merge-strategy patch merges the snapshot SHA while
// naming the branch in the commit message. Requirement: 01-REQ-6.3
// ===========================================================================

func TestSnapshot_TS_01_41_MergeNamesBranchInMessage(t *testing.T) {
	e := newRealEnv(t)
	e.setPatches("feature/patch-a")

	res, retryable, err := e.run(context.Background(), "job-ts-01-41", StrategyMerge, FailModeFailFast)
	if err != nil {
		t.Fatalf("rebuild: retryable=%v err=%v", retryable, err)
	}
	r := resultOf(t, res)

	msg := runGitCmd(t, e.trunk, "log", "-1", "--format=%B", "integration")
	if !strings.Contains(msg, "feature/patch-a") {
		t.Errorf("integration tip message %q does not name the branch", msg)
	}
	if !strings.Contains(msg, "Merge branch 'feature/patch-a'") {
		t.Errorf("integration tip message %q, want \"Merge branch 'feature/patch-a'\"", msg)
	}
	if n := len(strings.Fields(runGitCmd(t, e.trunk, "log", "-1", "--format=%P", "integration"))); n != 2 {
		t.Errorf("integration tip has %d parents, want a merge commit (2)", n)
	}
	if want := runGitCmd(t, e.trunk, "rev-parse", "feature/patch-a"); r.PatchResults[0].SourceSHA != want {
		t.Errorf("source_sha = %q, want %q", r.PatchResults[0].SourceSHA, want)
	}
}

// ===========================================================================
// TS-01-42: an unresolvable or empty-resolving branch, or an empty rebase
// commit list, is skipped with branch_not_found. Requirement: 01-REQ-6.4
// ===========================================================================

func TestSnapshot_TS_01_42_BranchNotFoundIsSkipped(t *testing.T) {
	patches := []Patch{
		{ID: "p0", BranchName: "feature/err", Position: 1, Status: PatchStatusActive},
		{ID: "p1", BranchName: "feature/empty", Position: 2, Status: PatchStatusActive},
		{ID: "p2", BranchName: "feature/nolog", Position: 3, Status: PatchStatusActive},
		{ID: "p3", BranchName: "feature/ok", Position: 4, Status: PatchStatusActive},
	}
	e := newWtEnv(t, patches)
	base := e.trunk.RunFunc
	e.trunk.RunFunc = func(ctx context.Context, args ...string) (string, error) {
		if br, ok := snapshotArgs(args); ok {
			switch br {
			case "feature/err":
				return "", errors.New("fatal: Needed a single revision")
			case "feature/empty":
				return "", nil
			case "feature/nolog":
				return "5050", nil
			case "feature/ok":
				return "6060", nil
			}
		}
		return base(ctx, args...)
	}
	wbase := e.wt.RunFunc
	e.wt.RunFunc = func(ctx context.Context, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "log" && strings.HasSuffix(args[len(args)-1], "...5050") {
			return "", nil
		}
		return wbase(ctx, args...)
	}

	res, retryable, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, ""))
	if err != nil {
		t.Fatalf("the run must not fail: retryable=%v err=%v", retryable, err)
	}
	r := resultOf(t, res)
	if len(r.PatchResults) != 4 {
		t.Fatalf("patch results = %d, want 4", len(r.PatchResults))
	}
	for i := 0; i < 3; i++ {
		if r.PatchResults[i].Status != "skipped" || r.PatchResults[i].SkippedReason != "branch_not_found" {
			t.Errorf("patch %d = %s/%s, want skipped/branch_not_found", i, r.PatchResults[i].Status, r.PatchResults[i].SkippedReason)
		}
	}
	if r.PatchResults[3].Status != "success" {
		t.Errorf("healthy patch status = %q, want success", r.PatchResults[3].Status)
	}
	if r.PatchesSkipped != 3 || r.PatchesApplied != 1 {
		t.Errorf("skipped/applied = %d/%d, want 3/1", r.PatchesSkipped, r.PatchesApplied)
	}
}

// ===========================================================================
// TS-01-43 (integration): a tag or remote-tracking ref with the patch's name
// is not matched. Requirement: 01-REQ-6.5
// ===========================================================================

func TestSnapshot_TS_01_43_TagAndRemoteRefNotMatched(t *testing.T) {
	for _, strategy := range []string{StrategyRebase, StrategyMerge} {
		t.Run(strategy, func(t *testing.T) {
			e := newRealEnv(t)
			runGitCmd(t, e.trunk, "tag", "p1", "feature/patch-a")
			runGitCmd(t, e.trunk, "update-ref", "refs/remotes/origin/p2", runGitCmd(t, e.trunk, "rev-parse", "feature/patch-b"))
			e.setPatches("p1", "p2")

			res, retryable, err := e.run(context.Background(), "job-ts-01-43", strategy, FailModeFailFast)
			if err != nil {
				t.Fatalf("rebuild: retryable=%v err=%v", retryable, err)
			}
			r := resultOf(t, res)
			for i, name := range []string{"p1", "p2"} {
				pr := r.PatchResults[i]
				if pr.BranchName != name || pr.Status != "skipped" || pr.SkippedReason != "branch_not_found" {
					t.Errorf("%s = %s/%s, want skipped/branch_not_found", name, pr.Status, pr.SkippedReason)
				}
				if pr.SourceSHA != "" {
					t.Errorf("%s has source_sha %q although it was not found", name, pr.SourceSHA)
				}
			}
			if got := runGitCmd(t, e.trunk, "rev-parse", "integration"); got != e.baseSHA {
				t.Errorf("integration = %s, want the untouched base %s", got, e.baseSHA)
			}
		})
	}
}

// ===========================================================================
// TS-01-44: source_sha is set for snapshotted patches including conflicts,
// carried in the result, the progress and the API JSON, and omitted when
// empty. Requirement: 01-REQ-6.6
// ===========================================================================

func TestSnapshot_TS_01_44_SourceSHAInResultProgressAndAPI(t *testing.T) {
	q, db := newTestQueue(t)
	patches := []Patch{
		{ID: "p1", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		{ID: "p2", BranchName: "feature/c", Position: 2, Status: PatchStatusActive},
		{ID: "p3", BranchName: "feature/d", Position: 3, Status: PatchStatusDisabled},
	}
	snap := map[string]string{"feature/a": "1a1a1a1a", "feature/c": "2c2c2c2c"}
	e := newWtEnv(t, patches)
	e.h.Queue = q
	_ = RegisterRebuildJob(q, e.h)

	base := e.trunk.RunFunc
	e.trunk.RunFunc = func(ctx context.Context, args ...string) (string, error) {
		if br, ok := snapshotArgs(args); ok {
			if s, found := snap[br]; found {
				return s, nil
			}
		}
		return base(ctx, args...)
	}
	wbase := e.wt.RunFunc
	e.wt.RunFunc = func(ctx context.Context, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "log" && strings.HasSuffix(args[len(args)-1], "..."+snap["feature/c"]) {
			return "cc99", nil
		}
		return wbase(ctx, args...)
	}
	e.wt.CherryPickFunc = func(_ context.Context, sha string) error {
		if sha == "cc99" {
			return &CherryPickConflictError{Files: []string{"x.txt"}}
		}
		return nil
	}

	jobID, _, err := q.Enqueue(jobqueue.EnqueueParams{
		Type: "rebuild", Key: e.slug, Nonce: "nonce-ts-01-44",
		Payload: e.payload(StrategyRebase, FailModeContinue), SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := db.Exec("UPDATE jobs SET status = 'running' WHERE id = ?", jobID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	ctx := jobqueue.ContextWithJobID(context.Background(), jobID)

	res, retryable, err := e.h.HandleRebuildJob(ctx, e.payload(StrategyRebase, FailModeContinue))
	if err != nil {
		t.Fatalf("HandleRebuildJob: retryable=%v err=%v", retryable, err)
	}
	r := resultOf(t, res)

	// Result.
	if r.PatchResults[0].Status != "success" || r.PatchResults[0].SourceSHA != snap["feature/a"] {
		t.Errorf("success patch = %+v", r.PatchResults[0])
	}
	if r.PatchResults[1].Status != "conflict" || r.PatchResults[1].SourceSHA != snap["feature/c"] {
		t.Errorf("conflict patch = %+v", r.PatchResults[1])
	}
	if r.PatchResults[2].SourceSHA != "" {
		t.Errorf("disabled patch has source_sha %q", r.PatchResults[2].SourceSHA)
	}

	// JSON: omitted when empty.
	raw, _ := json.Marshal(r.PatchResults)
	var generic []map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	if generic[0]["source_sha"] != snap["feature/a"] {
		t.Errorf("result JSON source_sha = %v", generic[0]["source_sha"])
	}
	if _, has := generic[2]["source_sha"]; has {
		t.Error("the disabled patch's JSON has a source_sha key")
	}

	// Progress of the running job.
	job, err := q.GetByID(jobID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	var progress []PatchResult
	if err := json.Unmarshal(job.Progress, &progress); err != nil {
		t.Fatalf("progress: %v (%s)", err, job.Progress)
	}
	if len(progress) != 3 || progress[0].SourceSHA != snap["feature/a"] || progress[1].SourceSHA != snap["feature/c"] || progress[2].SourceSHA != "" {
		t.Errorf("progress source_sha = %+v", progress)
	}

	// API: a running job exposes the progress, a completed one the result.
	resultJSON, _ := json.Marshal(r)
	for name, j := range map[string]*jobqueue.Job{
		"running":   {ID: "j1", Status: jobqueue.StatusRunning, Progress: job.Progress},
		"completed": {ID: "j2", Status: jobqueue.StatusCompleted, Result: resultJSON},
	} {
		rec := jobToRebuildRecord(j)
		var prs []map[string]any
		if err := json.Unmarshal(rec.PatchResults, &prs); err != nil || len(prs) != 3 {
			t.Fatalf("%s: patch_results = %s (err=%v)", name, rec.PatchResults, err)
		}
		if prs[0]["source_sha"] != snap["feature/a"] || prs[1]["source_sha"] != snap["feature/c"] {
			t.Errorf("%s: API source_sha = %v / %v", name, prs[0]["source_sha"], prs[1]["source_sha"])
		}
		if _, has := prs[2]["source_sha"]; has {
			t.Errorf("%s: disabled patch has a source_sha key in the API", name)
		}
	}
}

// ===========================================================================
// TS-01-45 (property): for any patch branch that moves after its snapshot,
// exactly the snapshot's commits are applied and reported.
// Requirement: 01-REQ-6.7
// ===========================================================================

// moveOnFirstRun wraps a runner and performs move once, right before the
// first worktree command, which is after the tips were snapshotted.
type moveOnFirstRun struct {
	GitRunner
	once sync.Once
	move func()
}

func (r *moveOnFirstRun) Run(ctx context.Context, args ...string) (string, error) {
	r.once.Do(r.move)
	return r.GitRunner.Run(ctx, args...)
}

func TestSnapshot_TS_01_45_MovedBranchAppliesSnapshotOnly(t *testing.T) {
	movements := []struct {
		name string
		move func(t *testing.T, e *realEnv, old string)
	}{
		{"new commit pushed", func(t *testing.T, e *realEnv, old string) {
			// A child of the snapshot whose tree carries patch-b.txt, so
			// applying it would be visible in the integration branch.
			tree := runGitCmd(t, e.trunk, "rev-parse", "feature/patch-b^{tree}")
			tmp := runGitCmd(t, e.trunk, "commit-tree", tree, "-p", old, "-m", "pushed later")
			runGitCmd(t, e.trunk, "update-ref", "refs/heads/feature/patch-a", tmp)
		}},
		{"branch reset to base", func(t *testing.T, e *realEnv, old string) {
			runGitCmd(t, e.trunk, "update-ref", "refs/heads/feature/patch-a", e.baseSHA)
		}},
		{"branch deleted", func(t *testing.T, e *realEnv, old string) {
			runGitCmd(t, e.trunk, "update-ref", "-d", "refs/heads/feature/patch-a")
		}},
		{"branch pointed at another patch", func(t *testing.T, e *realEnv, old string) {
			runGitCmd(t, e.trunk, "update-ref", "refs/heads/feature/patch-a", runGitCmd(t, e.trunk, "rev-parse", "feature/patch-b"))
		}},
	}

	for _, strategy := range []string{StrategyRebase, StrategyMerge} {
		for _, mv := range movements {
			t.Run(strategy+"/"+mv.name, func(t *testing.T) {
				e := newRealEnv(t)
				e.setPatches("feature/patch-a")
				snapshot := runGitCmd(t, e.trunk, "rev-parse", "feature/patch-a")

				inner := e.h.NewGitRunner
				e.h.NewGitRunner = func(path string) (GitRunner, error) {
					r, err := inner(path)
					if err != nil || path == e.trunk {
						return r, err
					}
					return &moveOnFirstRun{GitRunner: r, move: func() { mv.move(t, e, snapshot) }}, nil
				}

				res, retryable, err := e.run(context.Background(), "job-ts-01-45-"+strategy+"-"+strings.ReplaceAll(mv.name, " ", "-"), strategy, FailModeFailFast)
				if err != nil {
					t.Fatalf("rebuild: retryable=%v err=%v", retryable, err)
				}
				r := resultOf(t, res)

				if r.PatchResults[0].Status != "success" {
					t.Fatalf("status = %q, want success", r.PatchResults[0].Status)
				}
				if r.PatchResults[0].SourceSHA != snapshot {
					t.Errorf("source_sha = %q, want the snapshot %q", r.PatchResults[0].SourceSHA, snapshot)
				}
				files := runGitCmd(t, e.trunk, "ls-tree", "-r", "--name-only", "integration")
				if !strings.Contains(files, "patch-a.txt") {
					t.Errorf("snapshot's change missing from the integration branch: %q", files)
				}
				if strings.Contains(files, "patch-b.txt") {
					t.Errorf("a commit that is not reachable from the snapshot was applied: %q", files)
				}
				subjects := runGitCmd(t, e.trunk, "log", "--no-merges", "--format=%s", e.baseSHA+"..integration")
				if subjects != "patch a change" {
					t.Errorf("applied commits = %q, want only the snapshot's", subjects)
				}
				if strategy == StrategyMerge {
					parents := runGitCmd(t, e.trunk, "log", "-1", "--format=%P", "integration")
					if !strings.Contains(parents, snapshot) {
						t.Errorf("merge parents %q do not include the snapshot %s", parents, snapshot)
					}
				}
			})
		}
	}
}
