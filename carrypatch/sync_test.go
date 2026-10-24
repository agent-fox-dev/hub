package carrypatch

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

const syncPath = wsPath + "/sync"

// hubSync is an env in hub mode whose fake runner reports the upstream base
// as newSHA, over a workspace that last saw storedSHA.
func hubSync(t *testing.T, storedSHA, newSHA string) *env {
	t.Helper()
	e := newEnv(t)
	seedWS(t, e.db, ws{slug: "ws", upstreamSHA: storedSHA})
	e.fake.run = func(args ...string) (string, error) {
		if args[0] == "rev-parse" {
			return newSHA, nil
		}
		return "", nil
	}
	return e
}

// A hub-mode sync fetches upstream, marks the active patches whose branch is
// now an ancestor of the upstream tip as merged, records the new upstream
// head, enqueues a rebuild and reports the carry-patch fields; the origin
// columns are cleared and no origin audit event is emitted.
func TestSync_HubModeMergeDetectionAndRebuild(t *testing.T) {
	e := hubSync(t, "old", "new")
	e.vars["REBUILD_STRATEGY"] = StrategyMerge
	seedPatch(t, e.db, "ws", "pa", "feature/a", 1, "active")
	seedPatch(t, e.db, "ws", "pb", "feature/b", 2, "active")
	seedPatch(t, e.db, "ws", "pc", "feature/c", 3, "conflict")
	seedPatch(t, e.db, "ws", "pm", "feature/m", 4, "merged_upstream")
	mustExec(t, e.db, `UPDATE patches SET origin_sync_state = 'in_sync', origin_sha = 'x'`)
	e.fake.isAncestor = func(a, d string) (bool, error) {
		return a == "feature/a" || a == "old", nil
	}
	fetched := 0
	e.fetch = func(_ context.Context, repoPath string, _ transport.AuthMethod) error {
		fetched++
		if repoPath != filepath.Join(e.root, "ws", "trunk") {
			t.Errorf("fetch path = %s", repoPath)
		}
		return nil
	}

	got := e.want(e.do(http.MethodPost, syncPath, "", alice), http.StatusOK)
	if merged := got["patches_merged"].([]any); len(merged) != 1 || merged[0] != "feature/a" {
		t.Errorf("patches_merged = %v", got["patches_merged"])
	}
	if got["rebuild_triggered"] != true || got["rebuild_job_id"] == nil || got["force_push_detected"] != false || got["origin_fetched"] != false {
		t.Errorf("response = %v", got)
	}
	if _, ok := got["patches_synced"]; ok {
		t.Error("patches_synced present in hub mode")
	}
	if fetched != 1 || e.fake.has("IsAncestor", "feature/c", "new") || e.fake.has("IsAncestor", "feature/m", "new") {
		t.Errorf("fetches=%d calls=%v (only active patches are checked)", fetched, e.fake.calls)
	}
	statuses := patchStatuses(t, e.db, "ws")
	if statuses["feature/a"] != "merged_upstream" || statuses["feature/b"] != "active" || statuses["feature/c"] != "conflict" {
		t.Errorf("statuses = %v", statuses)
	}
	var upstream, lastSync, origin sql.NullString
	if err := e.db.QueryRow(`SELECT upstream_head_sha, last_sync_at FROM workspaces WHERE slug = 'ws'`).Scan(&upstream, &lastSync); err != nil || upstream.String != "new" || !lastSync.Valid {
		t.Errorf("workspace row: upstream=%v last_sync=%v err=%v", upstream, lastSync, err)
	}
	if err := e.db.QueryRow(`SELECT origin_sync_state FROM patches WHERE id = 'pb'`).Scan(&origin); err != nil || origin.Valid {
		t.Error("hub mode left the origin columns")
	}
	jobs := e.jobs("ws")
	if len(jobs) != 1 || jobs[0].ID != got["rebuild_job_id"] || jobs[0].GroupKey != "ws:integration" || !strings.Contains(string(jobs[0].Payload), `"strategy":"merge"`) || jobs[0].SubmittedBy != "alice" {
		t.Errorf("job = %+v", jobs)
	}
	if len(e.audit.events) != 0 {
		t.Errorf("hub mode emitted %v", e.audit.events)
	}

	// Merged is monotonic and a queued rebuild suppresses another.
	e.fake.isAncestor = func(string, string) (bool, error) { return false, nil }
	got = e.want(e.do(http.MethodPost, syncPath, "", alice), http.StatusOK)
	if patchStatuses(t, e.db, "ws")["feature/a"] != "merged_upstream" {
		t.Error("merged_upstream reverted")
	}
	if got["rebuild_triggered"] != false || len(e.jobs("ws")) != 1 {
		t.Errorf("duplicate rebuild: %v", got)
	}
	if _, ok := got["rebuild_job_id"]; ok {
		t.Error("rebuild_job_id present without an enqueue")
	}
}

// When neither upstream nor a patch changed the sync detects nothing, writes
// only last_sync_at and enqueues nothing.
func TestSync_HubModeUnchangedUpstream(t *testing.T) {
	e := hubSync(t, fakeSHA, fakeSHA)
	seedPatch(t, e.db, "ws", "pa", "feature/a", 1, "active")
	e.fake.isAncestor = func(string, string) (bool, error) { return true, nil }
	got := e.want(e.do(http.MethodPost, syncPath, "", alice), http.StatusOK)
	if len(got["patches_merged"].([]any)) != 0 || got["rebuild_triggered"] != false || len(e.jobs("ws")) != 0 {
		t.Errorf("response = %v", got)
	}
	if e.fake.has("IsAncestor") {
		t.Error("merge detection ran without a change")
	}
	var lastSync sql.NullString
	if err := e.db.QueryRow(`SELECT last_sync_at FROM workspaces WHERE slug = 'ws'`).Scan(&lastSync); err != nil || !lastSync.Valid {
		t.Error("last_sync_at not written")
	}
}

// Force-push detection: the new upstream tip not descending from the stored
// one; an ancestry error or no stored tip reads as no force push.
func TestSync_ForcePushDetection(t *testing.T) {
	cases := []struct {
		name   string
		stored string
		anc    func(a, d string) (bool, error)
		want   bool
	}{
		{"rewritten", "old", func(string, string) (bool, error) { return false, nil }, true},
		{"advanced", "old", func(string, string) (bool, error) { return true, nil }, false},
		{"ancestry error", "old", func(string, string) (bool, error) { return false, errors.New("x") }, false},
		{"first sync", "", func(string, string) (bool, error) { return false, nil }, false},
	}
	for _, c := range cases {
		e := hubSync(t, c.stored, "new")
		e.fake.isAncestor = c.anc
		if got := e.want(e.do(http.MethodPost, syncPath, "", alice), http.StatusOK); got["force_push_detected"] != c.want {
			t.Errorf("%s: force_push_detected = %v", c.name, got["force_push_detected"])
		}
	}
}

// AUTO_REBUILD_AFTER_SYNC=false keeps the sync from enqueuing; any other
// value, or none, enqueues.
func TestSync_AutoRebuildSetting(t *testing.T) {
	for value, want := range map[string]bool{"false": false, "true": true, "": true, "no": true} {
		e := hubSync(t, "old", "new")
		if value != "" {
			e.vars["AUTO_REBUILD_AFTER_SYNC"] = value
		}
		got := e.want(e.do(http.MethodPost, syncPath, "", alice), http.StatusOK)
		if got["rebuild_triggered"] != want || (len(e.jobs("ws")) == 1) != want {
			t.Errorf("AUTO_REBUILD_AFTER_SYNC=%q: triggered=%v jobs=%d", value, got["rebuild_triggered"], len(e.jobs("ws")))
		}
	}
	if !autoRebuildEnabledFor(nil, "ws") {
		t.Error("nil lookup must enable")
	}
}

// SQUASH_MERGE_DETECTION selects ancestry, content (git cherry plus the PR
// number in squash-merge subjects) or both; an ancestry error skips the patch.
func TestSync_SquashMergeDetectionModes(t *testing.T) {
	type outcome struct {
		merged                      bool
		ancestorChecked, cherryUsed bool
	}
	run := func(mode string, anc func(string, string) (bool, error), cherry func(string, string) ([]string, []string, error), log string, prURL string) outcome {
		e := hubSync(t, "old", "new")
		if mode != "" {
			e.vars["SQUASH_MERGE_DETECTION"] = mode
		}
		seedPatch(t, e.db, "ws", "pa", "feature/a", 1, "active")
		if prURL != "" {
			mustExec(t, e.db, `UPDATE patches SET upstream_pr_url = ? WHERE id = 'pa'`, prURL)
		}
		e.fake.isAncestor = anc
		e.fake.cherry = cherry
		e.fake.run = func(args ...string) (string, error) {
			switch args[0] {
			case "rev-parse":
				return "new", nil
			case "log":
				return log, nil
			}
			return "", nil
		}
		got := e.want(e.do(http.MethodPost, syncPath, "", alice), http.StatusOK)
		return outcome{len(got["patches_merged"].([]any)) == 1, e.fake.has("IsAncestor", "feature/a"), e.fake.has("Cherry")}
	}
	no := func(string, string) (bool, error) { return false, nil }
	noCherry := func(string, string) ([]string, []string, error) { return nil, nil, nil }
	applied := func(string, string) ([]string, []string, error) { return []string{"c1"}, nil, nil }
	pending := func(string, string) ([]string, []string, error) { return []string{"c1"}, []string{"c2"}, nil }

	if got := run("content_based", no, applied, "", ""); got != (outcome{true, false, true}) {
		t.Errorf("content_based = %+v", got)
	}
	if got := run("ancestry_only", no, applied, "", ""); got != (outcome{false, true, false}) {
		t.Errorf("ancestry_only = %+v", got)
	}
	if got := run("both", no, pending, "", ""); got != (outcome{false, true, true}) {
		t.Errorf("both with pending commits = %+v", got)
	}
	if got := run("", no, noCherry, "Feature (#42)\nother", "https://github.com/o/r/pull/42"); !got.merged {
		t.Errorf("PR number scan = %+v", got)
	}
	if got := run("", no, noCherry, `Revert "Feature (#42)" (#57)`, "https://github.com/o/r/pull/42"); got.merged {
		t.Errorf("PR number inside a subject counted = %+v", got)
	}
	errAnc := func(string, string) (bool, error) { return false, errors.New("unknown ref") }
	if got := run("bogus-mode", errAnc, applied, "", ""); got.merged || got.cherryUsed {
		t.Errorf("ancestry error must skip the patch = %+v", got)
	}
}

func TestExtractPRNumber(t *testing.T) {
	cases := map[string]string{
		"https://github.com/o/r/pull/42":        "42",
		"https://github.com/o/r/pull/42/files":  "42",
		"https://github.com/o/r/pull/42?diff=1": "42",
		"https://github.com/o/r/issues/42":      "",
		"https://github.com/o/r/pull/abc":       "",
		"https://github.com/o/r/pull/":          "",
	}
	for in, want := range cases {
		if got := extractPRNumber(in); got != want {
			t.Errorf("extractPRNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

// Without a stored upstream tip the PR scan covers the last 50 subjects; a
// failing log reads as not merged; content detection needs at least one
// commit to compare.
func TestDetectSquashMerge_Helpers(t *testing.T) {
	g := newFakeGit()
	var ranges []string
	g.run = func(args ...string) (string, error) {
		ranges = append(ranges, strings.Join(args, " "))
		if strings.Contains(args[len(args)-1], "fail") {
			return "", errors.New("bad range")
		}
		return "Title (#7)", nil
	}
	ctx := context.Background()
	if !detectSquashMergeByPRNumber(ctx, g, "7", "old", "new") || !strings.HasSuffix(ranges[0], "old..new") {
		t.Errorf("ranged scan: %v", ranges)
	}
	if !detectSquashMergeByPRNumber(ctx, g, "7", "", "new") || !strings.Contains(ranges[1], "-50 new") {
		t.Errorf("unbounded scan: %v", ranges)
	}
	if detectSquashMergeByPRNumber(ctx, g, "7", "old", "fail") {
		t.Error("log failure read as merged")
	}
	g.cherry = func(string, string) ([]string, []string, error) { return nil, nil, errors.New("x") }
	if detectSquashMergeByContent(ctx, g, "b", "u") {
		t.Error("cherry error read as merged")
	}
	g.cherry = func(string, string) ([]string, []string, error) { return nil, nil, nil }
	if detectSquashMergeByContent(ctx, g, "b", "u") {
		t.Error("no commits read as merged")
	}
	url := "https://github.com/o/r/pull/7"
	if !detectSquashMerge(ctx, g, Patch{BranchName: "b", UpstreamPRURL: &url}, "old", "new") {
		t.Error("PR fallback not used")
	}
}

// The sync refuses before it touches anything: scope, access, mode, lock,
// credentials, the fetches and the git runner.
func TestSync_Refusals(t *testing.T) {
	e := hubSync(t, "old", "new")
	seedWS(t, e.db, ws{slug: "std", mode: "standard"})
	if got := e.want(e.do(http.MethodPost, "/api/v1/workspaces/std/sync", "", alice), http.StatusOK); got["handled"] != false {
		t.Errorf("standard workspace = %v, want not handled", got)
	}
	if rec := e.do(http.MethodPost, syncPath, "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", rec.Code)
	}
	if msg, _ := e.wantErr(e.do(http.MethodPost, syncPath, "", pat("alice", "workspaces:read")), http.StatusForbidden); msg != "missing required scope: workspaces:write" {
		t.Errorf("PAT = %q", msg)
	}
	e.wantErr(e.do(http.MethodPost, "/api/v1/workspaces/none/sync", "", alice), http.StatusNotFound)

	release := holdLock(t, "ws")
	_, kind := e.wantErr(e.do(http.MethodPost, syncPath, "", alice), http.StatusConflict)
	release()
	if kind != "workspace_busy" {
		t.Errorf("busy = %q", kind)
	}

	fetched := false
	e.fetch = func(context.Context, string, transport.AuthMethod) error { fetched = true; return nil }
	e.resolveAuth = func(string) (transport.AuthMethod, error) { return nil, errors.New("no creds") }
	if msg, _ := e.wantErr(e.do(http.MethodPost, syncPath, "", alice), http.StatusBadGateway); msg != "failed to resolve upstream credentials" || fetched {
		t.Errorf("credential failure = %q, fetched=%v", msg, fetched)
	}
	e.resolveAuth = noAuth
	e.fetch = func(context.Context, string, transport.AuthMethod) error { return errors.New("down") }
	if msg, _ := e.wantErr(e.do(http.MethodPost, syncPath, "", alice), http.StatusBadGateway); msg != "upstream fetch failed" {
		t.Errorf("fetch failure = %q", msg)
	}
	e.fetch = noFetch
	e.fake.run = func(args ...string) (string, error) { return "", errors.New("no refs") }
	if msg, _ := e.wantErr(e.do(http.MethodPost, syncPath, "", alice), http.StatusInternalServerError); msg != "failed to resolve upstream HEAD" {
		t.Errorf("no upstream base = %q", msg)
	}
	e.gitErr = errors.New("no runner")
	e.wantErr(e.do(http.MethodPost, syncPath, "", alice), http.StatusInternalServerError)
	e.gitErr = nil
	var upstream sql.NullString
	if err := e.db.QueryRow(`SELECT upstream_head_sha FROM workspaces WHERE slug = 'ws'`).Scan(&upstream); err != nil || upstream.String != "old" {
		t.Error("a refused sync changed upstream_head_sha")
	}

	// Origin mode needs an origin fetch and origin credentials before any fetch.
	e.vars["PATCH_BRANCH_SOURCE"] = "origin"
	e.syncNilFetchOrigin = true
	if msg, _ := e.wantErr(e.do(http.MethodPost, syncPath, "", alice), http.StatusInternalServerError); msg != "origin fetch is not configured" {
		t.Errorf("nil FetchOrigin = %q", msg)
	}
	e.syncNilFetchOrigin = false
	e.resolveOriginAuth = func(string) (transport.AuthMethod, error) { return nil, errors.New("no creds") }
	if msg, _ := e.wantErr(e.do(http.MethodPost, syncPath, "", alice), http.StatusBadGateway); msg != "failed to resolve origin credentials" {
		t.Errorf("origin credentials = %q", msg)
	}
	e.resolveOriginAuth = noAuth
	e.fetchOrigin = func(context.Context, string, transport.AuthMethod) error { return errors.New("fork down") }
	if msg, kind := e.wantErr(e.do(http.MethodPost, syncPath, "", alice), http.StatusBadGateway); msg != "origin fetch failed" || kind != "origin_fetch_failed" {
		t.Errorf("origin fetch = %q/%q", msg, kind)
	}
	// Only the exact value "origin" selects origin mode, and the two settings
	// are read on every sync.
	for _, v := range []string{"Origin", "hub", "github"} {
		e.vars["PATCH_BRANCH_SOURCE"] = v
		if got := e.want(e.do(http.MethodPost, syncPath, "", alice), http.StatusOK); got["origin_fetched"] != false {
			t.Errorf("PATCH_BRANCH_SOURCE=%q selected origin mode", v)
		}
	}
	if e.reads["PATCH_BRANCH_SOURCE"] < 3 || e.reads["PATCH_DIVERGENCE_POLICY"] < 3 {
		t.Errorf("settings not read per sync: %v", e.reads)
	}
}

// originSync is an env in origin mode over a real fork and trunk, with the
// upstream base unchanged so only the patch refresh drives the outcome.
func originSync(t *testing.T) (*env, *forkTrunk) {
	t.Helper()
	e := newEnv(t)
	e.useRealGit = true
	f := newForkTrunk(t, e.root, "ws")
	seedWS(t, e.db, ws{slug: "ws", upstreamSHA: f.base})
	e.vars["PATCH_BRANCH_SOURCE"] = "origin"
	e.fetchOrigin = DefaultFetchOriginFunc()
	return e, f
}

// originState reads the origin columns of a patch.
func originState(t *testing.T, db *sql.DB, id string) (state string, sha sql.NullString) {
	t.Helper()
	var st sql.NullString
	if err := db.QueryRow(`SELECT origin_sync_state, origin_sha FROM patches WHERE id = ?`, id).Scan(&st, &sha); err != nil {
		t.Fatal(err)
	}
	return st.String, sha
}

// In origin mode each candidate patch branch is brought to the fork's tip by
// the first matching rule, the outcome is persisted and reported, the branch
// moves enqueue a rebuild, and the sync is audited.
func TestSync_OriginModeRefreshesPatchBranches(t *testing.T) {
	e, f := originSync(t)
	createdTip := f.pushBranch(t, "feature/created", "c.txt", "c\n")
	runGit(t, f.trunk, "branch", "feature/same", f.base)
	runGit(t, f.trunk, "push", "-q", "origin", "feature/same")
	runGit(t, f.trunk, "branch", "feature/ff", f.base)
	ffTip := f.pushBranch(t, "feature/ff", "ff.txt", "ff\n")
	localDiv := f.localBranch(t, "feature/div", "l.txt", "local\n")
	divTip := f.pushBranch(t, "feature/div", "r.txt", "remote\n")
	f.pushBranch(t, "feature/off", "o.txt", "o\n")
	runGit(t, f.trunk, "branch", "feature/off", f.base)
	runGit(t, f.trunk, "branch", "feature/merged", f.base)
	for i, p := range []struct{ id, branch, status string }{
		{"pmiss", "feature/missing", "active"}, {"pcre", "feature/created", "active"}, {"psame", "feature/same", "active"},
		{"pff", "feature/ff", "conflict"}, {"pdiv", "feature/div", "active"}, {"poff", "feature/off", "disabled"},
		{"pint", "integration", "active"}, {"pmer", "feature/merged", "merged_upstream"},
	} {
		seedPatch(t, e.db, "ws", p.id, p.branch, i+1, p.status)
	}
	mustExec(t, e.db, `UPDATE patches SET origin_sync_state = 'in_sync', origin_sha = 'stale' WHERE id = 'pmer'`)

	got := e.want(e.do(http.MethodPost, syncPath, "", alice), http.StatusOK)
	if got["origin_fetched"] != true || got["rebuild_triggered"] != true || len(got["patches_diverged"].([]any)) != 0 {
		t.Errorf("response = %v", got)
	}
	synced := map[string]map[string]any{}
	for _, el := range got["patches_synced"].([]any) {
		m := el.(map[string]any)
		synced[m["branch_name"].(string)] = m
	}
	want := map[string][2]string{
		"feature/missing": {ActionNone, StateMissingOnOrigin}, "feature/created": {ActionCreated, StateInSync},
		"feature/same": {ActionNone, StateInSync}, "feature/ff": {ActionFastForwarded, StateInSync},
		"feature/div": {ActionReplaced, StateInSync}, "feature/off": {ActionFastForwarded, StateInSync},
	}
	if len(synced) != len(want) {
		t.Errorf("patches_synced = %v (integration and merged patches are not candidates)", synced)
	}
	for b, w := range want {
		if el := synced[b]; el["action"] != w[0] || el["state"] != w[1] {
			t.Errorf("%s = %v, want %v", b, el, w)
		}
	}
	if el := synced["feature/div"]; el["replaced_sha"] != localDiv || el["origin_sha"] != divTip || el["local_sha"] != divTip {
		t.Errorf("replaced element = %v", el)
	}
	if _, ok := synced["feature/missing"]["origin_sha"]; ok {
		t.Error("origin_sha present for a branch missing on origin")
	}
	if refOf(f.trunk, "feature/created") != createdTip || refOf(f.trunk, "feature/ff") != ffTip || refOf(f.trunk, "feature/div") != divTip || refOf(f.trunk, "refs/hub/replaced/feature/div") != localDiv {
		t.Error("branches not brought to the fork tips with a backup of the replaced one")
	}
	if st, sha := originState(t, e.db, "pmiss"); st != StateMissingOnOrigin || sha.Valid {
		t.Errorf("missing state = %s/%v", st, sha)
	}
	if st, sha := originState(t, e.db, "pdiv"); st != StateInSync || sha.String != divTip {
		t.Errorf("replaced state = %s/%v", st, sha)
	}
	if st, _ := originState(t, e.db, "pmer"); st != "" {
		t.Error("merged patch kept its origin state")
	}
	if len(e.jobs("ws")) != 1 {
		t.Error("rebuild not enqueued after branches moved")
	}

	syncs, replaces := e.audit.ofType(EventPatchSync), e.audit.ofType(EventPatchReplace)
	if len(syncs) != 1 || len(replaces) != 1 {
		t.Fatalf("events: sync=%d replace=%d", len(syncs), len(replaces))
	}
	m := syncs[0].Metadata
	lists := map[string][]string{"created": {"feature/created"}, "fast_forwarded": {"feature/ff", "feature/off"}, "replaced": {"feature/div"}, "diverged": {}, "missing_on_origin": {"feature/missing"}}
	for k, w := range lists {
		if got, _ := m[k].([]string); !equalStrings(got, w) {
			t.Errorf("sync metadata %s = %v, want %v", k, m[k], w)
		}
	}
	if m["origin_fetched"] != true || syncs[0].ResourceID != "ws" || syncs[0].Action != "sync" || syncs[0].ActorID != "alice" {
		t.Errorf("sync event = %+v", syncs[0])
	}
	if r := replaces[0]; r.ResourceID != "feature/div" || r.Metadata["replaced_sha"] != localDiv || r.Metadata["origin_sha"] != divTip || r.Action != "replace" {
		t.Errorf("replace event = %+v", r)
	}

	// Nothing moved: no rebuild, empty arrays, a sync event with empty lists.
	mustExec(t, e.db, `DELETE FROM jobs`)
	got = e.want(e.do(http.MethodPost, syncPath, "", alice), http.StatusOK)
	if got["rebuild_triggered"] != false || len(e.jobs("ws")) != 0 {
		t.Errorf("second sync enqueued a rebuild: %v", got)
	}
	if len(e.audit.ofType(EventPatchSync)) != 2 || len(e.audit.ofType(EventPatchReplace)) != 1 {
		t.Error("second sync events")
	}
}

// Only an active or conflict patch that moved counts as a change worth a
// rebuild; a moved disabled patch does not.
func TestSync_OriginModeDisabledPatchDoesNotTriggerRebuild(t *testing.T) {
	e, f := originSync(t)
	runGit(t, f.trunk, "branch", "feature/off", f.base)
	f.pushBranch(t, "feature/off", "o.txt", "o\n")
	seedPatch(t, e.db, "ws", "poff", "feature/off", 1, "disabled")
	got := e.want(e.do(http.MethodPost, syncPath, "", alice), http.StatusOK)
	if got["rebuild_triggered"] != false || len(e.jobs("ws")) != 0 || got["patches_synced"].([]any)[0].(map[string]any)["action"] != ActionFastForwarded {
		t.Errorf("response = %v", got)
	}
}

// Under PATCH_DIVERGENCE_POLICY=report a diverged branch is left alone and
// reported; any other value replaces.
func TestSync_OriginModeDivergencePolicy(t *testing.T) {
	for _, policy := range []string{"report", "", "Report", "replace"} {
		e, f := originSync(t)
		if policy != "" {
			e.vars["PATCH_DIVERGENCE_POLICY"] = policy
		}
		local := f.localBranch(t, "feature/div", "l.txt", "local\n")
		remote := f.pushBranch(t, "feature/div", "r.txt", "remote\n")
		seedPatch(t, e.db, "ws", "pdiv", "feature/div", 1, "active")
		got := e.want(e.do(http.MethodPost, syncPath, "", alice), http.StatusOK)
		el := got["patches_synced"].([]any)[0].(map[string]any)
		if policy == "report" {
			if el["action"] != ActionNone || el["state"] != StateDiverged || got["patches_diverged"].([]any)[0] != "feature/div" || got["rebuild_triggered"] != false {
				t.Errorf("report: %v", got)
			}
			if refOf(f.trunk, "feature/div") != local || refOf(f.trunk, "refs/hub/replaced/feature/div") != "" {
				t.Error("report policy moved the branch or wrote a backup")
			}
			if st, _ := originState(t, e.db, "pdiv"); st != StateDiverged {
				t.Errorf("state = %s", st)
			}
			if len(e.audit.ofType(EventPatchReplace)) != 0 {
				t.Error("replace event under report policy")
			}
		} else if el["action"] != ActionReplaced || refOf(f.trunk, "feature/div") != remote {
			t.Errorf("policy %q: %v", policy, el)
		}
	}
}

// A patch branch checked out in the trunk is reset to its new tip; a fork
// tip that is an ancestor of the local tip counts as diverged too.
func TestSync_OriginModeCheckedOutBranchAndForkBehind(t *testing.T) {
	e, f := originSync(t)
	runGit(t, f.trunk, "branch", "feature/ff", f.base)
	ffTip := f.pushBranch(t, "feature/ff", "ff.txt", "ff\n")
	runGit(t, f.trunk, "checkout", "-q", "feature/ff")
	runGit(t, f.trunk, "branch", "feature/behind", f.base)
	runGit(t, f.trunk, "push", "-q", "origin", "feature/behind")
	runGit(t, f.trunk, "checkout", "-q", "feature/behind")
	ahead := commitFile(t, f.trunk, "b.txt", "ahead\n", "local ahead of fork")
	runGit(t, f.trunk, "checkout", "-q", "feature/ff")
	seedPatch(t, e.db, "ws", "pff", "feature/ff", 1, "active")
	seedPatch(t, e.db, "ws", "pb", "feature/behind", 2, "active")

	got := e.want(e.do(http.MethodPost, syncPath, "", alice), http.StatusOK)
	if refOf(f.trunk, "HEAD") != ffTip {
		t.Error("checked-out branch not reset to the fork tip")
	}
	if _, err := os.Stat(filepath.Join(f.trunk, "ff.txt")); err != nil {
		t.Error("working tree not reset")
	}
	synced := got["patches_synced"].([]any)
	if el := synced[1].(map[string]any); el["action"] != ActionReplaced || el["replaced_sha"] != ahead || refOf(f.trunk, "feature/behind") != f.base {
		t.Errorf("fork behind local = %v", el)
	}
}

// A ref write that fails stops the refresh: the outcomes so far are kept,
// persisted and audited, a rebuild is enqueued for the branches that moved
// (unless auto rebuild is off), the answer is 500 and the sync is not
// recorded. A failed work-tree reset after the move and a failed ancestry
// check are reported as what they are.
func TestSync_OriginModeRefreshFailures(t *testing.T) {
	setup := func(t *testing.T) (*env, *forkTrunk) {
		e, f := originSync(t)
		runGit(t, f.trunk, "branch", "feature/one", f.base)
		f.pushBranch(t, "feature/one", "1.txt", "1\n")
		runGit(t, f.trunk, "branch", "feature/two", f.base)
		f.pushBranch(t, "feature/two", "2.txt", "2\n")
		seedPatch(t, e.db, "ws", "p1", "feature/one", 1, "active")
		seedPatch(t, e.db, "ws", "p2", "feature/two", 2, "active")
		return e, f
	}
	failRef := func(ref string) func(GitRunner) GitRunner {
		return func(g GitRunner) GitRunner {
			return &spyGit{GitRunner: g, onRun: func(args []string) (string, error, bool) {
				if args[0] == "update-ref" && args[1] == ref {
					return "", errors.New("ref locked"), true
				}
				return "", nil, false
			}}
		}
	}

	t.Run("write failure", func(t *testing.T) {
		e, f := setup(t)
		e.wrap = failRef("refs/heads/feature/two")
		msg, _ := e.wantErr(e.do(http.MethodPost, syncPath, "", alice), http.StatusInternalServerError)
		if msg != "failed to update patch branch feature/two" {
			t.Errorf("message = %q", msg)
		}
		if refOf(f.trunk, "feature/one") == f.base || refOf(f.trunk, "feature/two") != f.base {
			t.Error("branch moves: one should have moved, two not")
		}
		if st, _ := originState(t, e.db, "p1"); st != StateInSync {
			t.Error("outcome of the moved branch not persisted")
		}
		if st, _ := originState(t, e.db, "p2"); st != "" {
			t.Error("outcome persisted for the failed branch")
		}
		if len(e.jobs("ws")) != 1 {
			t.Error("rebuild not enqueued for the branch that moved")
		}
		syncs := e.audit.ofType(EventPatchSync)
		if len(syncs) != 1 || !equalStrings(syncs[0].Metadata["fast_forwarded"].([]string), []string{"feature/one"}) {
			t.Errorf("partial sync event = %+v", syncs)
		}
		var lastSync sql.NullString
		if err := e.db.QueryRow(`SELECT last_sync_at FROM workspaces WHERE slug = 'ws'`).Scan(&lastSync); err != nil || lastSync.Valid {
			t.Error("last_sync_at written on a failed sync")
		}
	})
	t.Run("write failure with auto rebuild off", func(t *testing.T) {
		e, _ := setup(t)
		e.vars["AUTO_REBUILD_AFTER_SYNC"] = "false"
		e.wrap = failRef("refs/heads/feature/two")
		e.wantErr(e.do(http.MethodPost, syncPath, "", alice), http.StatusInternalServerError)
		if len(e.jobs("ws")) != 0 {
			t.Error("rebuild enqueued although auto rebuild is off")
		}
	})
	t.Run("reset failure after the move", func(t *testing.T) {
		e, f := setup(t)
		runGit(t, f.trunk, "checkout", "-q", "feature/one")
		e.wrap = func(g GitRunner) GitRunner {
			return &spyGit{GitRunner: g, onHardReset: func(string) error { return errors.New("dirty tree") }}
		}
		msg, _ := e.wantErr(e.do(http.MethodPost, syncPath, "", alice), http.StatusInternalServerError)
		if msg != "failed to reset working tree for patch branch feature/one" {
			t.Errorf("message = %q", msg)
		}
		if refOf(f.trunk, "feature/one") == f.base {
			t.Error("the ref move must be kept")
		}
		if st, _ := originState(t, e.db, "p1"); st != StateInSync {
			t.Error("moved outcome not persisted")
		}
		if len(e.jobs("ws")) != 1 {
			t.Error("rebuild not enqueued for the moved branch")
		}
	})
	t.Run("ancestry failure", func(t *testing.T) {
		e, f := setup(t)
		e.wrap = func(g GitRunner) GitRunner {
			return &spyGit{GitRunner: g, onIsAncestor: func(string, string) error { return errors.New("corrupt") }}
		}
		msg, _ := e.wantErr(e.do(http.MethodPost, syncPath, "", alice), http.StatusInternalServerError)
		if msg != "failed to compare patch branch feature/one with origin" || refOf(f.trunk, "feature/one") != f.base {
			t.Errorf("message = %q", msg)
		}
	})
	if refreshFailureMessage(errors.New("plain")) != "failed to refresh patch branches" {
		t.Error("unclassified refresh failure message")
	}
}

// A patch marked merged by the detection that follows the refresh loses the
// origin state the refresh had just written.
func TestSync_OriginModeNewlyMergedPatchClearsOriginState(t *testing.T) {
	e, f := originSync(t)
	mustExec(t, e.db, `UPDATE workspaces SET upstream_head_sha = 'older' WHERE slug = 'ws'`)
	runGit(t, f.trunk, "branch", "feature/same", f.base)
	runGit(t, f.trunk, "push", "-q", "origin", "feature/same")
	seedPatch(t, e.db, "ws", "ps", "feature/same", 1, "active")
	got := e.want(e.do(http.MethodPost, syncPath, "", alice), http.StatusOK)
	if merged := got["patches_merged"].([]any); len(merged) != 1 || patchStatuses(t, e.db, "ws")["feature/same"] != "merged_upstream" {
		t.Errorf("merged = %v", got)
	}
	if st, _ := originState(t, e.db, "ps"); st != "" {
		t.Errorf("origin state of the newly merged patch = %q", st)
	}
}

// A nil or failing emitter changes nothing about an origin sync.
func TestSync_OriginModeEmitterAbsentOrFailing(t *testing.T) {
	for _, mode := range []string{"nil", "failing"} {
		e, f := originSync(t)
		f.localBranch(t, "feature/div", "l.txt", "l\n")
		f.pushBranch(t, "feature/div", "r.txt", "r\n")
		seedPatch(t, e.db, "ws", "pd", "feature/div", 1, "active")
		if mode == "nil" {
			e.audited = false
		} else {
			e.audit.err = errors.New("audit down")
		}
		sink := captureDefaultLog(t)
		got := e.want(e.do(http.MethodPost, syncPath, "", alice), http.StatusOK)
		if got["patches_synced"].([]any)[0].(map[string]any)["action"] != ActionReplaced {
			t.Errorf("%s: %v", mode, got)
		}
		if !strings.Contains(sink.String(), "patch branch replaced by origin") {
			t.Errorf("%s: replacement not logged", mode)
		}
		if mode == "failing" && !strings.Contains(sink.String(), "failed to emit hub.patch.replace") {
			t.Error("emit failure not logged")
		}
	}
}
