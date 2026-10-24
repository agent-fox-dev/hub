package carrypatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ===========================================================================
// Spec 01, task 8: rerere keeps working across rebuild worktrees. The
// resolutions are recorded in a linked worktree but live in the shared
// rr-cache that the rerere endpoints read.
// ===========================================================================

// conflictHook wraps a worktree runner. After a cherry-pick stopped on a
// conflict it runs after(worktreePath) and still returns the conflict error.
type conflictHook struct {
	GitRunner
	path  string
	after func(path string)
}

func (c *conflictHook) CherryPick(ctx context.Context, sha string) error {
	err := c.GitRunner.CherryPick(ctx, sha)
	var cpe *CherryPickConflictError
	if errors.As(err, &cpe) && c.after != nil {
		c.after(c.path)
	}
	return err
}

// hookWorktrees makes every worktree runner of the handler a conflictHook and
// returns the function that restores the original factory.
func (e *realEnv) hookWorktrees(after func(path string)) (restore func()) {
	base := e.h.NewGitRunner
	e.h.NewGitRunner = func(path string) (GitRunner, error) {
		r, err := base(path)
		if err != nil || path == e.trunk {
			return r, err
		}
		return &conflictHook{GitRunner: r, path: path, after: after}, nil
	}
	return func() { e.h.NewGitRunner = base }
}

// resolveAndRecord resolves every unmerged file in the worktree, stages it and
// runs `git rerere`, which records the postimage in the shared rr-cache.
func (e *realEnv) resolveAndRecord(path string) {
	e.t.Helper()
	out, _ := gitTry(path, "diff", "--name-only", "--diff-filter=U")
	for _, f := range splitNonEmpty(out) {
		writeFileHelper(e.t, filepath.Join(path, f), "resolved "+f+"\n")
		if o, err := gitTry(path, "add", f); err != nil {
			e.t.Fatalf("git add %s: %v\n%s", f, err, o)
		}
	}
	if o, err := gitTry(path, "rerere"); err != nil {
		e.t.Fatalf("git rerere: %v\n%s", err, o)
	}
}

// recordResolutions runs a rebuild of the branches in which every conflict is
// resolved and recorded in the rebuild's own worktree.
func (e *realEnv) recordResolutions(jobID string, branches ...string) {
	e.t.Helper()
	restore := e.hookWorktrees(e.resolveAndRecord)
	defer restore()
	e.setPatches(branches...)
	if _, _, err := e.run(context.Background(), jobID, StrategyRebase, FailModeFailFast); err != nil {
		e.t.Fatalf("recording rebuild %s: %v", jobID, err)
	}
}

// addConflictBranches adds n branches feature/c<i>, each of which conflicts
// with the upstream tip on its own file f<i>.txt, and moves the upstream base
// to that tip.
func (e *realEnv) addConflictBranches(n int) {
	e.t.Helper()
	runGitCmd(e.t, e.trunk, "checkout", "main")
	for i := 1; i <= n; i++ {
		writeFileHelper(e.t, filepath.Join(e.trunk, fmt.Sprintf("f%d.txt", i)), fmt.Sprintf("orig %d\n", i))
	}
	runGitCmd(e.t, e.trunk, "add", ".")
	runGitCmd(e.t, e.trunk, "commit", "-m", "add f files")
	c0 := runGitCmd(e.t, e.trunk, "rev-parse", "HEAD")
	for i := 1; i <= n; i++ {
		runGitCmd(e.t, e.trunk, "checkout", "-b", fmt.Sprintf("feature/c%d", i), c0)
		writeFileHelper(e.t, filepath.Join(e.trunk, fmt.Sprintf("f%d.txt", i)), fmt.Sprintf("feature %d\n", i))
		runGitCmd(e.t, e.trunk, "add", ".")
		runGitCmd(e.t, e.trunk, "commit", "-m", fmt.Sprintf("feature change %d", i))
		runGitCmd(e.t, e.trunk, "checkout", "main")
	}
	for i := 1; i <= n; i++ {
		writeFileHelper(e.t, filepath.Join(e.trunk, fmt.Sprintf("f%d.txt", i)), fmt.Sprintf("upstream %d\n", i))
	}
	runGitCmd(e.t, e.trunk, "add", ".")
	runGitCmd(e.t, e.trunk, "commit", "-m", "upstream f changes")
	e.baseSHA = runGitCmd(e.t, e.trunk, "rev-parse", "HEAD")
	runGitCmd(e.t, e.trunk, "update-ref", "refs/remotes/upstream/HEAD", e.baseSHA)
}

// rrCacheIDs lists the entries of the trunk's rr-cache.
func rrCacheIDs(t *testing.T, trunk string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(trunk, ".git", "rr-cache"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var ids []string
	for _, en := range entries {
		if en.IsDir() {
			ids = append(ids, en.Name())
		}
	}
	sort.Strings(ids)
	return ids
}

// rerereHTTP is a real workspace behind the rerere routes.
type rerereHTTP struct {
	*fullTestEnv
	real *realEnv
}

func newRerereHTTP(t *testing.T) *rerereHTTP {
	t.Helper()
	fe := newFullTestEnv(t)
	seedWorkspace(t, fe.db, "ws-real", "alice", "active", "ready", "carry_patch", "integration")
	return &rerereHTTP{fullTestEnv: fe, real: newRealEnvAt(t, fe.workspaceRoot, "ws-real")}
}

// list performs GET /rerere and returns the resolution ids.
func (r *rerereHTTP) list(t *testing.T) []string {
	t.Helper()
	rec := r.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-real/rerere", "", rebuildUserAuth("alice"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rerere status = %d; body = %s", rec.Code, rec.Body.String())
	}
	var resp RerereListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, res := range resp.Resolutions {
		ids = append(ids, res.ID)
	}
	sort.Strings(ids)
	return ids
}

func listedAsResolved(t *testing.T, r *rerereHTTP, id string) bool {
	t.Helper()
	rec := r.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-real/rerere", "", rebuildUserAuth("alice"))
	var resp RerereListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	for _, res := range resp.Resolutions {
		if res.ID == id {
			return res.Resolved
		}
	}
	return false
}

func (r *rerereHTTP) forget(t *testing.T, id string) int {
	t.Helper()
	return r.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws-real/rerere/"+id, "", rebuildUserAuth("alice")).Code
}

func contains(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// TS-01-20: a resolution recorded in one rebuild's worktree is replayed with no
// conflict in the next rebuild. Requirement: 01-REQ-3.2
func TestRerereWorktree_TS_01_20_RecordedResolutionReplayed(t *testing.T) {
	e := newRealEnv(t)
	branches := []string{"feature/patch-a", "feature/conflict"}

	// Without a recorded resolution the second patch conflicts.
	e.setPatches(branches...)
	if _, _, err := e.run(context.Background(), "job-0", StrategyRebase, FailModeFailFast); err == nil ||
		!strings.Contains(err.Error(), "conflict") {
		t.Fatalf("fixture: expected a conflict, got %v", err)
	}
	if ids := rrCacheIDs(t, e.trunk); len(resolvedEntries(e.trunk, ids)) != 0 {
		t.Fatalf("fixture: a resolution is recorded before any was: %v", ids)
	}

	// The first rebuild resolves the conflict in its worktree and records it.
	e.recordResolutions("job-1", branches...)
	recorded := resolvedEntries(e.trunk, rrCacheIDs(t, e.trunk))
	if len(recorded) != 1 {
		t.Fatalf("expected exactly one resolution in the shared rr-cache, got %v", recorded)
	}

	// The second rebuild, with no help, replays it.
	e.setPatches(branches...)
	res, _, err := e.run(context.Background(), "job-2", StrategyRebase, FailModeFailFast)
	if err != nil {
		t.Fatalf("second rebuild must replay the recorded resolution: %v", err)
	}
	rr := res.(*RebuildResult)
	if len(rr.PatchResults) != 2 || rr.PatchResults[1].Status != "success" {
		t.Fatalf("patch[1] should be success, got %+v", rr.PatchResults)
	}
	if got := runGitCmd(t, e.trunk, "show", "integration:base.txt"); got != "resolved base.txt" {
		t.Errorf("integration base.txt = %q, want the recorded resolution", got)
	}
	if after := resolvedEntries(e.trunk, rrCacheIDs(t, e.trunk)); len(after) != 1 || after[0] != recorded[0] {
		t.Errorf("rr-cache changed during the replay: before=%v after=%v", recorded, after)
	}
}

// resolvedEntries returns the ids that have a postimage.
func resolvedEntries(trunk string, ids []string) []string {
	var out []string
	for _, id := range ids {
		if _, err := newestRRImage(filepath.Join(trunk, ".git", "rr-cache", id), "postimage"); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// A resolution recorded as a postimage variant (postimage.1, left by a
// conflict id that already had an aborted preimage) is listed as resolved.
func TestListRerereEntries_PostimageVariantIsResolved(t *testing.T) {
	gitDir := t.TempDir()
	dir := filepath.Join(gitDir, "rr-cache", "id-variant")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileHelper(t, filepath.Join(dir, "preimage"), "a\n<<<<<<<\nx\n=======\ny\n>>>>>>>\n")
	writeFileHelper(t, filepath.Join(dir, "preimage.1"), "a\n<<<<<<<\nx\n=======\ny\n>>>>>>>\n")
	writeFileHelper(t, filepath.Join(dir, "postimage.1"), "a\nresolved\n")
	got := listRerereEntries(gitDir)
	if len(got) != 1 || !got[0].Resolved || got[0].RecordedAt == nil {
		t.Fatalf("got %+v, want one resolved entry", got)
	}
}

// TS-01-21: the rerere endpoints list a resolution recorded in a rebuild
// worktree and DELETE forgets it. Requirement: 01-REQ-3.3
func TestRerereWorktree_TS_01_21_EndpointsListAndForget(t *testing.T) {
	r := newRerereHTTP(t)
	r.real.recordResolutions("job-1", "feature/conflict")
	ids := resolvedEntries(r.real.trunk, rrCacheIDs(t, r.real.trunk))
	if len(ids) != 1 {
		t.Fatalf("fixture: want one recorded resolution, got %v", ids)
	}
	id := ids[0]

	if got := r.list(t); !contains(got, id) {
		t.Fatalf("GET /rerere = %v, want it to list %s", got, id)
	}
	if !listedAsResolved(t, r, id) {
		t.Errorf("entry %s should be listed with resolved=true", id)
	}
	if code := r.forget(t, id); code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", code)
	}
	if got := r.list(t); contains(got, id) {
		t.Errorf("GET /rerere still lists %s after DELETE: %v", id, got)
	}
	if _, err := os.Stat(filepath.Join(r.real.trunk, ".git", "rr-cache", id)); !os.IsNotExist(err) {
		t.Errorf("rr-cache entry still on disk (err=%v)", err)
	}
}

// TS-01-22 (property): for any set of recorded resolutions and any deletion
// order the endpoints list exactly what is not yet deleted.
// Requirement: 01-REQ-3.3
func TestRerereWorktree_TS_01_22_Property(t *testing.T) {
	rng := rand.New(rand.NewSource(20260102))
	iterations := 4
	if testing.Short() {
		iterations = 2
	}
	for it := 0; it < iterations; it++ {
		k := 1 + rng.Intn(4)
		if it == 0 {
			k = 4 // always cover the largest set
		}
		order := rng.Perm(k)
		t.Run(fmt.Sprintf("k%d-it%d", k, it), func(t *testing.T) {
			r := newRerereHTTP(t)
			r.real.addConflictBranches(k)
			var branches []string
			for i := 1; i <= k; i++ {
				branches = append(branches, fmt.Sprintf("feature/c%d", i))
			}
			r.real.recordResolutions("job-1", branches...)

			ids := resolvedEntries(r.real.trunk, rrCacheIDs(t, r.real.trunk))
			if len(ids) != k {
				t.Fatalf("recorded %d resolutions, want %d: %v", len(ids), k, ids)
			}
			if got := r.list(t); strings.Join(got, ",") != strings.Join(ids, ",") {
				t.Fatalf("GET = %v, want %v", got, ids)
			}
			remaining := map[string]bool{}
			for _, id := range ids {
				remaining[id] = true
			}
			for _, idx := range order {
				id := ids[idx]
				if code := r.forget(t, id); code != http.StatusNoContent {
					t.Fatalf("DELETE %s status = %d", id, code)
				}
				delete(remaining, id)
				var want []string
				for id := range remaining {
					want = append(want, id)
				}
				sort.Strings(want)
				got := r.list(t)
				if len(want) == 0 {
					want = []string{}
				}
				if strings.Join(got, ",") != strings.Join(want, ",") {
					t.Fatalf("after deleting %s: GET = %v, want %v", id, got, want)
				}
			}
		})
	}
}

// TS-01-23: discarding a worktree mid-conflict leaves the shared rr-cache and
// the trunk's MERGE_RR unaffected. Requirement: 01-REQ-3.4
func TestRerereWorktree_TS_01_23_DiscardMidConflictLeavesCacheAndMergeRR(t *testing.T) {
	r := newRerereHTTP(t)
	e := r.real
	e.addConflictBranches(1)
	e.recordResolutions("job-1", "feature/conflict")
	before := r.list(t)
	if len(before) != 1 {
		t.Fatalf("fixture: want one recorded resolution, got %v", before)
	}

	// feature/c1 conflicts with no recorded resolution: the rebuild stops on
	// it. Observe MERGE_RR in the worktree at the moment of the conflict.
	var wtMergeRR string
	restore := e.hookWorktrees(func(path string) {
		out, _ := gitTry(path, "rev-parse", "--git-path", "MERGE_RR")
		if !filepath.IsAbs(out) {
			out = filepath.Join(path, out)
		}
		if data, err := os.ReadFile(out); err == nil {
			wtMergeRR = string(data)
		}
	})
	e.setPatches("feature/c1")
	_, _, err := e.run(context.Background(), "job-2", StrategyRebase, FailModeFailFast)
	restore()
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected a fail-fast conflict, got %v", err)
	}
	if wtMergeRR == "" {
		t.Error("fixture: MERGE_RR was not populated in the worktree during the conflict")
	}
	if n := e.worktreeCount(); n != 1 {
		t.Errorf("%d worktrees registered after the run", n)
	}

	after := r.list(t) // asserts 200
	for _, id := range before {
		if !contains(after, id) {
			t.Errorf("entry %s recorded before the discard is gone: %v", id, after)
		}
	}
	data, err := os.ReadFile(filepath.Join(e.trunk, ".git", "MERGE_RR"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Errorf("trunk MERGE_RR holds %q, want absent or empty", data)
	}
}
