package carrypatch

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/aflib/jobqueue"
)

const wsPath = "/api/v1/workspaces/ws"

// rebuildEnv is a patchEnv with one active patch, so a rebuild may be submitted.
func rebuildEnv(t *testing.T) *env {
	t.Helper()
	e := patchEnv(t)
	seedPatch(t, e.db, "ws", "p1", "feature/a", 1, "active")
	return e
}

// Submitting a rebuild enqueues one job keyed by the workspace and serialised by
// its integration branch, with the strategy and fail mode captured from the
// settings or the body, and audits the enqueue.
func TestSubmitRebuild_EnqueuesWithCapturedStrategy(t *testing.T) {
	e := rebuildEnv(t)
	e.vars["REBUILD_STRATEGY"] = StrategyMerge
	e.vars["REBUILD_FAIL_MODE"] = FailModeContinue

	got := e.want(e.do(http.MethodPost, wsPath+"/rebuild", "", pat("alice", "rebuilds:write")), http.StatusAccepted)
	if got["type"] != "rebuild" || got["key"] != "ws" || got["group_key"] != "ws:integration" || got["status"] != jobqueue.StatusQueued || got["id"] == "" {
		t.Errorf("job response = %v", got)
	}
	payload := got["payload"].(map[string]any)
	if payload["strategy"] != StrategyMerge || payload["fail_mode"] != FailModeContinue || payload["integration_branch"] != "integration" || payload["submitted_by"] != "alice" {
		t.Errorf("payload = %v", payload)
	}
	events := e.audit.ofType("hub.rebuild.enqueue")
	if len(events) != 1 || events[0].ResourceID != got["id"] || events[0].Metadata["patch_count"] != 1 || events[0].ActorID != "alice" || events[0].ActorType != "pat" {
		t.Errorf("enqueue event = %+v", events)
	}
	jobs := e.jobs("ws")
	if len(jobs) != 1 || jobs[0].GroupKey != "ws:integration" {
		t.Errorf("jobs = %+v", jobs)
	}

	// A second submission while the first is queued is refused.
	_, kind := e.wantErr(e.do(http.MethodPost, wsPath+"/rebuild", "", alice), http.StatusConflict)
	if kind != "concurrent_rebuild" {
		t.Errorf("duplicate error_type = %q", kind)
	}

	// The body overrides the settings; the defaults are rebase and no fail mode.
	mustExec(t, e.db, `DELETE FROM jobs`)
	got = e.want(e.do(http.MethodPost, wsPath+"/rebuild", `{"strategy":"rebase","fail_mode":"fail_fast"}`, alice), http.StatusAccepted)
	payload = got["payload"].(map[string]any)
	if payload["strategy"] != StrategyRebase || payload["fail_mode"] != FailModeFailFast {
		t.Errorf("overridden payload = %v", payload)
	}
	mustExec(t, e.db, `DELETE FROM jobs`)
	delete(e.vars, "REBUILD_STRATEGY")
	delete(e.vars, "REBUILD_FAIL_MODE")
	e.audited = false
	got = e.want(e.do(http.MethodPost, wsPath+"/rebuild", "", alice), http.StatusAccepted)
	if raw, _ := json.Marshal(got["payload"]); !strings.Contains(string(raw), `"strategy":"rebase"`) || strings.Contains(string(raw), "fail_mode") {
		t.Errorf("default payload = %s", raw)
	}
}

func TestSubmitRebuild_RefusesUnfitWorkspacesAndBodies(t *testing.T) {
	e := rebuildEnv(t)
	seedWS(t, e.db, ws{slug: "std", mode: "standard"})
	seedWS(t, e.db, ws{slug: "archived", status: "archived"})
	seedWS(t, e.db, ws{slug: "cloning", clone: "cloning"})
	seedWS(t, e.db, ws{slug: "empty"})
	seedPatch(t, e.db, "empty", "pe", "feature/e", 1, "disabled")

	cases := []struct {
		name, slug, body, want, kind string
		status                       int
	}{
		{"standard", "std", "", "rebuild is only supported for carry_patch workspaces", "workspace_mode_mismatch", 400},
		{"inactive", "archived", "", "workspace is not active", "", 400},
		{"clone not ready", "cloning", "", "workspace clone is not ready", "", 400},
		{"no active patches", "empty", "", "no patches with status active or conflict", "no_active_patches", 400},
		{"unknown", "none", "", "workspace not found", "", 404},
		{"bad strategy", "ws", `{"strategy":"squash"}`, "strategy must be 'rebase' or 'merge'", "", 400},
		{"bad fail mode", "ws", `{"fail_mode":"stop"}`, "fail_mode must be 'fail_fast' or 'continue'", "", 400},
		{"malformed", "ws", `{"strategy":`, "invalid request body", "", 400},
	}
	for _, c := range cases {
		msg, kind := e.wantErr(e.do(http.MethodPost, "/api/v1/workspaces/"+c.slug+"/rebuild", c.body, alice), c.status)
		if msg != c.want || kind != c.kind {
			t.Errorf("%s: %q/%q", c.name, msg, kind)
		}
	}
	if len(e.jobs("ws")) != 0 {
		t.Error("a refused submission enqueued a job")
	}
}

// The rebuild routes refuse a PAT without the scope they need; rebuilds:write
// also reads.
func TestRebuildRoutes_ScopeMatrix(t *testing.T) {
	e := rebuildEnv(t)
	id := e.enqueueJob("ws", jobqueue.StatusQueued)
	routes := []struct {
		method, path, scope, ok string
	}{
		{http.MethodPost, "/rebuild", "rebuilds:write", "rebuilds:write"},
		{http.MethodGet, "/rebuilds", "rebuilds:read", "rebuilds:write"},
		{http.MethodGet, "/rebuilds/" + id, "rebuilds:read", "rebuilds:write"},
		{http.MethodDelete, "/rebuilds/" + id, "rebuilds:write", "rebuilds:write"},
		{http.MethodPost, "/rebuilds/" + id + "/requeue", "rebuilds:write", "rebuilds:write"},
		{http.MethodPost, "/rebuilds/" + id + "/rollback", "rebuilds:write", "rebuilds:write"},
		{http.MethodGet, "/rebuild-preview", "rebuilds:read", "rebuilds:read"},
		{http.MethodGet, "/patch-status", "workspaces:read", "workspaces:read"},
		{http.MethodGet, "/rerere", "workspaces:read", "workspaces:read"},
		{http.MethodDelete, "/rerere/x", "workspaces:write", "workspaces:write"},
	}
	for _, r := range routes {
		if rec := e.do(r.method, wsPath+r.path, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymous = %d", r.method, r.path, rec.Code)
		}
		msg, _ := e.wantErr(e.do(r.method, wsPath+r.path, "", pat("alice", "patches:write")), http.StatusForbidden)
		if msg != "missing required scope: "+r.scope {
			t.Errorf("%s %s = %q", r.method, r.path, msg)
		}
		if rec := e.do(r.method, wsPath+r.path, "", pat("alice", r.ok)); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s with %s = %d", r.method, r.path, r.ok, rec.Code)
		}
	}
}

// completed stores a finished job with a result for the workspace.
func completed(t *testing.T, e *env, slug string, result RebuildResult, createdAt time.Time) string {
	t.Helper()
	id := e.enqueueJob(slug, jobqueue.StatusCompleted)
	raw, _ := json.Marshal(result)
	mustExec(t, e.db, `UPDATE jobs SET result = ?, created_at = ?, updated_at = ? WHERE id = ?`, string(raw), apikit.FormatUTC(createdAt), apikit.FormatUTC(createdAt), id)
	return id
}

// The list and detail routes render each job as a record: strategy from the
// payload, completed_at for terminal states, patch results and the
// integration SHAs from the result, and a running job's progress.
func TestRebuildRecords_ListAndGet(t *testing.T) {
	e := rebuildEnv(t)
	seedWS(t, e.db, ws{slug: "other"})
	now := time.Now()
	old := completed(t, e, "ws", RebuildResult{IntegrationHeadSHA: "head1", PreviousIntegrationHeadSHA: "prev1",
		PatchResults: []PatchResult{{PatchID: "p1", BranchName: "feature/a", Position: 1, Status: "success", SourceSHA: "s1"}}}, now.Add(-time.Hour))
	failed := e.enqueueJob("ws", jobqueue.StatusFailed)
	mustExec(t, e.db, `UPDATE jobs SET error = 'conflict in patch' WHERE id = ?`, failed)
	running := e.enqueueJob("ws", jobqueue.StatusRunning)
	if err := e.q.UpdateProgress(running, []PatchResult{{PatchID: "p1", Status: "success"}}); err != nil {
		t.Fatal(err)
	}
	foreign := e.enqueueJob("other", jobqueue.StatusQueued)

	list := e.want(e.do(http.MethodGet, wsPath+"/rebuilds", "", alice), http.StatusOK)
	jobs := list["jobs"].([]any)
	if len(jobs) != 3 {
		t.Fatalf("jobs = %d, want 3", len(jobs))
	}
	first := jobs[0].(map[string]any)
	if first["id"] != running || first["status"] != jobqueue.StatusRunning || first["completed_at"] != nil || first["patch_results"] == nil {
		t.Errorf("running record = %v (newest first, progress as patch_results)", first)
	}
	second := jobs[1].(map[string]any)
	if second["id"] != failed || second["error"] != "conflict in patch" || second["completed_at"] == nil {
		t.Errorf("failed record = %v", second)
	}
	last := jobs[2].(map[string]any)
	if last["id"] != old || last["strategy"] != StrategyRebase || last["integration_head_sha"] != "head1" || last["previous_integration_head_sha"] != "prev1" || last["completed_at"] == nil {
		t.Errorf("completed record = %v", last)
	}
	if results, _ := last["patch_results"].([]any); len(results) != 1 || results[0].(map[string]any)["source_sha"] != "s1" {
		t.Errorf("patch_results = %v", last["patch_results"])
	}

	got := e.want(e.do(http.MethodGet, wsPath+"/rebuilds/"+old, "", alice), http.StatusOK)
	if got["id"] != old || got["integration_head_sha"] != "head1" {
		t.Errorf("detail = %v", got)
	}
	if got := e.want(e.do(http.MethodGet, wsPath+"/rebuilds/"+running, "", alice), http.StatusOK); got["patch_results"] == nil {
		t.Error("running detail lacks progress")
	}
	mustExec(t, e.db, `UPDATE jobs SET progress = NULL WHERE id = ?`, running)
	if got := e.want(e.do(http.MethodGet, wsPath+"/rebuilds/"+running, "", alice), http.StatusOK); got["patch_results"] != nil {
		t.Error("running job without progress reports patch_results")
	}
	for _, id := range []string{foreign, "missing"} {
		if msg, _ := e.wantErr(e.do(http.MethodGet, wsPath+"/rebuilds/"+id, "", alice), http.StatusNotFound); msg != "rebuild job not found" {
			t.Errorf("job %s: %q", id, msg)
		}
	}
	mustExec(t, e.db, `DELETE FROM jobs`)
	if body := e.do(http.MethodGet, wsPath+"/rebuilds", "", alice).Body.String(); !strings.Contains(body, `"jobs":[]`) {
		t.Errorf("empty history = %s", body)
	}
}

// Only a queued job can be cancelled; a job of another workspace or an
// unknown id is 404.
func TestCancelRebuild_OnlyQueuedJobs(t *testing.T) {
	e := rebuildEnv(t)
	seedWS(t, e.db, ws{slug: "other"})
	queued := e.enqueueJob("ws", jobqueue.StatusQueued)
	got := e.want(e.do(http.MethodDelete, wsPath+"/rebuilds/"+queued, "", alice), http.StatusOK)
	if got["status"] != "cancelled" {
		t.Errorf("cancel = %v", got)
	}
	if j, _ := e.q.GetByID(queued); j.Status != jobqueue.StatusCancelled {
		t.Errorf("job status = %s", j.Status)
	}
	for _, status := range []string{jobqueue.StatusRunning, jobqueue.StatusCompleted, jobqueue.StatusFailed, jobqueue.StatusDeadLetter, jobqueue.StatusCancelled} {
		mustExec(t, e.db, `DELETE FROM jobs`)
		id := e.enqueueJob("ws", status)
		if msg, _ := e.wantErr(e.do(http.MethodDelete, wsPath+"/rebuilds/"+id, "", alice), http.StatusConflict); msg != "job cannot be cancelled in its current state" {
			t.Errorf("%s: %q", status, msg)
		}
	}
	mustExec(t, e.db, `DELETE FROM jobs`)
	foreign := e.enqueueJob("other", jobqueue.StatusQueued)
	for _, id := range []string{foreign, "missing"} {
		e.wantErr(e.do(http.MethodDelete, wsPath+"/rebuilds/"+id, "", alice), http.StatusNotFound)
	}
}

// Only a dead-lettered job can be requeued, and only while no job of the
// workspace is active.
func TestRequeueRebuild_DeadLetterOnly(t *testing.T) {
	e := rebuildEnv(t)
	seedWS(t, e.db, ws{slug: "other"})
	dead := e.enqueueJob("ws", jobqueue.StatusDeadLetter)
	got := e.want(e.do(http.MethodPost, wsPath+"/rebuilds/"+dead+"/requeue", "", alice), http.StatusOK)
	if got["id"] != dead || got["status"] != jobqueue.StatusQueued || got["completed_at"] != nil {
		t.Errorf("requeued record = %v", got)
	}
	if msg, _ := e.wantErr(e.do(http.MethodPost, wsPath+"/rebuilds/"+dead+"/requeue", "", alice), http.StatusConflict); !strings.Contains(msg, "not in dead_letter status") {
		t.Errorf("queued job requeue = %q", msg)
	}
	mustExec(t, e.db, `DELETE FROM jobs`)
	dead = e.enqueueJob("ws", jobqueue.StatusDeadLetter)
	e.enqueueJob("ws", jobqueue.StatusRunning)
	if msg, _ := e.wantErr(e.do(http.MethodPost, wsPath+"/rebuilds/"+dead+"/requeue", "", alice), http.StatusConflict); !strings.Contains(msg, "active job") {
		t.Errorf("requeue with an active job = %q", msg)
	}
	foreign := e.enqueueJob("other", jobqueue.StatusDeadLetter)
	for _, id := range []string{foreign, "missing"} {
		e.wantErr(e.do(http.MethodPost, wsPath+"/rebuilds/"+id+"/requeue", "", alice), http.StatusNotFound)
	}
}

// Rollback resets the integration branch named in the job's payload to the
// previous head recorded in its result, under the workspace lock.
func TestRollbackRebuild_ResetsIntegrationBranch(t *testing.T) {
	e := rebuildEnv(t)
	seedWS(t, e.db, ws{slug: "other"})
	id := completed(t, e, "ws", RebuildResult{IntegrationHeadSHA: "new", PreviousIntegrationHeadSHA: "prev"}, time.Now())

	got := e.want(e.do(http.MethodPost, wsPath+"/rebuilds/"+id+"/rollback", "", alice), http.StatusOK)
	if got["rolled_back_to"] != "prev" || !e.fake.has("branch", "-f", "integration", "prev") {
		t.Errorf("rollback = %v, calls = %v", got, e.fake.calls)
	}
	// Without an integration branch in the payload the default is "deploy".
	mustExec(t, e.db, `UPDATE jobs SET payload = '{"workspace_slug":"ws"}' WHERE id = ?`, id)
	e.want(e.do(http.MethodPost, wsPath+"/rebuilds/"+id+"/rollback", "", alice), http.StatusOK)
	if !e.fake.has("branch", "-f", "deploy", "prev") {
		t.Error("default integration branch not used")
	}

	release := holdLock(t, "ws")
	_, kind := e.wantErr(e.do(http.MethodPost, wsPath+"/rebuilds/"+id+"/rollback", "", alice), http.StatusConflict)
	release()
	if kind != "workspace_busy" {
		t.Errorf("busy error_type = %q", kind)
	}
	e.fake.run = func(args ...string) (string, error) { return "", errors.New("git failed") }
	if msg, _ := e.wantErr(e.do(http.MethodPost, wsPath+"/rebuilds/"+id+"/rollback", "", alice), http.StatusInternalServerError); msg != "failed to roll back integration branch" {
		t.Errorf("git failure = %q", msg)
	}
	e.gitErr = errors.New("no runner")
	e.wantErr(e.do(http.MethodPost, wsPath+"/rebuilds/"+id+"/rollback", "", alice), http.StatusInternalServerError)
	e.gitErr = nil

	noPrev := completed(t, e, "ws", RebuildResult{IntegrationHeadSHA: "new"}, time.Now())
	if msg, _ := e.wantErr(e.do(http.MethodPost, wsPath+"/rebuilds/"+noPrev+"/rollback", "", alice), http.StatusConflict); msg != "no previous integration head to roll back to" {
		t.Errorf("first rebuild = %q", msg)
	}
	noResult := e.enqueueJob("ws", jobqueue.StatusFailed)
	e.wantErr(e.do(http.MethodPost, wsPath+"/rebuilds/"+noResult+"/rollback", "", alice), http.StatusConflict)
	foreign := completed(t, e, "other", RebuildResult{PreviousIntegrationHeadSHA: "prev"}, time.Now())
	for _, j := range []string{foreign, "missing"} {
		e.wantErr(e.do(http.MethodPost, wsPath+"/rebuilds/"+j+"/rollback", "", alice), http.StatusNotFound)
	}
}

// The preview predicts each active patch against the upstream base with
// merge-tree, cumulatively, without moving any ref.
func TestRebuildPreview_PredictsWithoutMutation(t *testing.T) {
	e := newEnv(t)
	e.useRealGit = true
	seedWS(t, e.db, ws{slug: "ws"})
	seedWS(t, e.db, ws{slug: "std", mode: "standard"})
	trunk, base := newRebuildRepo(t, e.root, "ws")
	seedPatch(t, e.db, "ws", "p1", "feature/patch-a", 1, "active")
	seedPatch(t, e.db, "ws", "p2", "feature/conflict", 2, "conflict")
	seedPatch(t, e.db, "ws", "p3", "feature/patch-b", 3, "active")
	seedPatch(t, e.db, "ws", "p4", "feature/disabled", 4, "disabled")
	seedPatch(t, e.db, "ws", "p5", "feature/merged", 5, "merged_upstream")
	seedPatch(t, e.db, "ws", "p6", "feature/missing", 6, "active")
	refsBefore := runGit(t, trunk, "for-each-ref")

	got := e.want(e.do(http.MethodGet, wsPath+"/rebuild-preview", "", alice), http.StatusOK)
	results := got["patch_results"].([]any)
	if len(results) != 4 {
		t.Fatalf("results = %v", results)
	}
	want := []struct{ branch, status string }{{"feature/patch-a", "would_succeed"}, {"feature/conflict", "would_conflict"}, {"feature/patch-b", "would_succeed"}, {"feature/missing", "would_succeed"}}
	for i, w := range want {
		r := results[i].(map[string]any)
		if r["branch_name"] != w.branch || r["status"] != w.status {
			t.Errorf("result %d = %v", i, r)
		}
		if w.status == "would_succeed" && w.branch != "feature/missing" && r["tree_sha"] == "" {
			t.Errorf("%s has no tree_sha", w.branch)
		}
	}
	if files := results[1].(map[string]any)["conflict_files"].([]any); len(files) != 1 || files[0] != "base.txt" {
		t.Errorf("conflict_files = %v", files)
	}
	if clean := results[0].(map[string]any)["conflict_files"].([]any); len(clean) != 0 {
		t.Errorf("clean patch conflict_files = %v", clean)
	}
	if runGit(t, trunk, "for-each-ref") != refsBefore || refOf(trunk, "HEAD") != base {
		t.Error("preview changed refs or HEAD")
	}

	mustExec(t, e.db, `DELETE FROM patches`)
	if got := e.want(e.do(http.MethodGet, wsPath+"/rebuild-preview", "", alice), http.StatusOK); len(got["patch_results"].([]any)) != 0 {
		t.Errorf("empty preview = %v", got)
	}
	if msg, _ := e.wantErr(e.do(http.MethodGet, "/api/v1/workspaces/std/rebuild-preview", "", alice), http.StatusBadRequest); msg != "rebuild-preview is only supported for carry_patch workspaces" {
		t.Errorf("standard = %q", msg)
	}
	e.wantErr(e.do(http.MethodGet, "/api/v1/workspaces/none/rebuild-preview", "", alice), http.StatusNotFound)
	runGit(t, trunk, "update-ref", "-d", UpstreamHeadRef)
	if msg, _ := e.wantErr(e.do(http.MethodGet, wsPath+"/rebuild-preview", "", alice), http.StatusInternalServerError); !strings.Contains(msg, "sync the workspace first") {
		t.Errorf("no upstream base = %q", msg)
	}
	e.gitErr = errors.New("no runner")
	e.wantErr(e.do(http.MethodGet, wsPath+"/rebuild-preview", "", alice), http.StatusInternalServerError)
}

// The patch-status dashboard joins the workspace row, the latest rebuild, the
// visible patches with their last result and origin state, the summary
// counts and the rerere cache size.
func TestPatchStatus_Dashboard(t *testing.T) {
	e := rebuildEnv(t)
	seedWS(t, e.db, ws{slug: "std", mode: "standard"})
	seedPatch(t, e.db, "ws", "p2", "feature/b", 2, "conflict")
	seedPatch(t, e.db, "ws", "p3", "feature/c", 3, "disabled")
	seedPatch(t, e.db, "ws", "p4", "feature/d", 4, "merged_upstream")
	seedPatch(t, e.db, "ws", "p5", "feature/gone", -5, "deleted")
	mustExec(t, e.db, `UPDATE patches SET origin_sync_state = 'diverged', origin_sha = 'o1', origin_synced_at = 't1' WHERE id = 'p1'`)
	mustExec(t, e.db, `UPDATE patches SET origin_sync_state = 'missing_on_origin' WHERE id = 'p2'`)
	mustExec(t, e.db, `UPDATE workspaces SET head_sha = 'h', upstream_head_sha = 'u', last_sync_at = 'ls', clone_error = 'ce', sync_error = 'se' WHERE slug = 'ws'`)
	completed(t, e, "ws", RebuildResult{IntegrationHeadSHA: "old"}, time.Now().Add(-time.Hour))
	latest := completed(t, e, "ws", RebuildResult{IntegrationHeadSHA: "ih", PatchResults: []PatchResult{
		{PatchID: "p1", Status: "success"}, {PatchID: "p2", Status: "conflict", ConflictFiles: []string{"x.go"}}}}, time.Now())
	rr := filepath.Join(e.root, "ws", "trunk", ".git", "rr-cache")
	writeFile(t, filepath.Join(rr, "e1", "preimage"), "<<<<<<<\n")
	writeFile(t, filepath.Join(rr, "e1", "postimage"), "x\n")
	writeFile(t, filepath.Join(rr, "e2", "preimage"), "<<<<<<<\n")
	writeFile(t, filepath.Join(rr, "bad", "other"), "x\n")

	got := e.want(e.do(http.MethodGet, wsPath+"/patch-status", "", alice), http.StatusOK)
	if got["workspace_slug"] != "ws" || got["workspace_mode"] != "carry_patch" || got["head_sha"] != "h" || got["upstream_head_sha"] != "u" ||
		got["last_sync_at"] != "ls" || got["clone_error"] != "ce" || got["sync_error"] != "se" || got["integration_branch"] != "integration" || got["integration_head_sha"] != "ih" {
		t.Errorf("workspace fields = %v", got)
	}
	if lr := got["last_rebuild"].(map[string]any); lr["id"] != latest || lr["status"] != jobqueue.StatusCompleted {
		t.Errorf("last_rebuild = %v", lr)
	}
	patches := got["patches"].([]any)
	if len(patches) != 4 {
		t.Fatalf("patches = %v", patches)
	}
	p1 := patches[0].(map[string]any)
	if p1["last_rebuild_result"] != "success" || p1["origin_sync_state"] != "diverged" || p1["origin_sha"] != "o1" || p1["origin_synced_at"] != "t1" {
		t.Errorf("p1 = %v", p1)
	}
	p2 := patches[1].(map[string]any)
	if p2["last_rebuild_result"] != "conflict" || p2["conflict_files"].([]any)[0] != "x.go" {
		t.Errorf("p2 = %v", p2)
	}
	if p3 := patches[2].(map[string]any); p3["last_rebuild_result"] != nil {
		t.Errorf("p3 without a result = %v", p3)
	}
	summary := got["summary"].(map[string]any)
	want := map[string]float64{"total_patches": 4, "active": 1, "conflict": 1, "disabled": 1, "merged_upstream": 1, "total_rerere_resolutions": 2, "patches_diverged": 1, "patches_missing_on_origin": 1}
	for k, v := range want {
		if summary[k] != v {
			t.Errorf("summary[%s] = %v, want %v", k, summary[k], v)
		}
	}

	mustExec(t, e.db, `DELETE FROM patches; DELETE FROM jobs`)
	got = e.want(e.do(http.MethodGet, wsPath+"/patch-status", "", alice), http.StatusOK)
	if got["last_rebuild"] != nil || got["integration_head_sha"] != "" || len(got["patches"].([]any)) != 0 || got["summary"].(map[string]any)["total_patches"] != 0.0 {
		t.Errorf("empty dashboard = %v", got)
	}
	if msg, _ := e.wantErr(e.do(http.MethodGet, "/api/v1/workspaces/std/patch-status", "", alice), http.StatusBadRequest); msg != "patch-status is only available for carry_patch workspaces" {
		t.Errorf("standard = %q", msg)
	}
	e.wantErr(e.do(http.MethodGet, "/api/v1/workspaces/none/patch-status", "", alice), http.StatusNotFound)
}

// CarryPatchSyncResponse serialises patches_synced and patches_diverged only
// in origin mode (non-nil), as [] when empty, and rebuild_job_id only when set.
func TestCarryPatchSyncResponse_MarshalJSON(t *testing.T) {
	hub, _ := json.Marshal(CarryPatchSyncResponse{PatchesMerged: []string{}})
	if s := string(hub); strings.Contains(s, "patches_synced") || strings.Contains(s, "rebuild_job_id") || !strings.Contains(s, `"origin_fetched":false`) {
		t.Errorf("hub mode = %s", s)
	}
	id := "job-1"
	origin, _ := json.Marshal(CarryPatchSyncResponse{PatchesMerged: []string{"a"}, RebuildJobID: &id, OriginFetched: true,
		PatchesSynced: []PatchSyncedElement{}, PatchesDiverged: []string{}})
	for _, want := range []string{`"patches_synced":[]`, `"patches_diverged":[]`, `"rebuild_job_id":"job-1"`, `"patches_merged":["a"]`} {
		if !strings.Contains(string(origin), want) {
			t.Errorf("origin mode %s lacks %s", origin, want)
		}
	}
	extras := (&CarryPatchSyncResponse{PatchesMerged: []string{}, RebuildJobID: &id, PatchesDiverged: []string{"d"}}).asExtras()
	if extras["rebuild_job_id"] != "job-1" || extras["patches_diverged"] == nil {
		t.Errorf("extras = %v", extras)
	}
	if _, ok := extras["patches_synced"]; ok {
		t.Error("nil patches_synced rendered")
	}
}
