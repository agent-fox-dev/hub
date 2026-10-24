package carrypatch

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rrCache writes rr-cache entries under gitDir: a map of entry id to the
// files it holds.
func rrCache(t *testing.T, gitDir string, entries map[string]map[string]string) {
	t.Helper()
	for id, files := range entries {
		for name, content := range files {
			writeFile(t, filepath.Join(gitDir, "rr-cache", id, name), content)
		}
	}
}

// The listing names each cache entry by id, marks it resolved when a
// postimage (or a later variant) exists, takes the path from MERGE_RR or a
// labelled conflict marker, and skips entries without images.
func TestListRerereEntries_CacheShapes(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	if got := listRerereEntries(gitDir); len(got) != 0 {
		t.Errorf("missing cache = %v, want empty", got)
	}
	rrCache(t, gitDir, map[string]map[string]string{
		"resolved":   {"preimage": "<<<<<<<\nx\n=======\ny\n>>>>>>>\n", "postimage": "z\n"},
		"unresolved": {"preimage": "<<<<<<< src/a.go\nx\n=======\ny\n>>>>>>>\n"},
		"variant":    {"preimage": "a\n", "preimage.1": "b\n", "postimage.1": "c\n"},
		"inprogress": {"preimage": "<<<<<<<\n"},
		"empty":      {"notes": "x"},
	})
	writeFile(t, filepath.Join(gitDir, "MERGE_RR"), "inprogress\tlib/b.go\x00\x00bogus-no-tab\x00")
	writeFile(t, filepath.Join(gitDir, "rr-cache", "stray-file"), "x")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(gitDir, "rr-cache", "variant", "postimage.1"), old, old); err != nil {
		t.Fatal(err)
	}

	byID := map[string]RerereResolution{}
	for _, r := range listRerereEntries(gitDir) {
		byID[r.ID] = r
	}
	if len(byID) != 4 {
		t.Fatalf("entries = %v", byID)
	}
	if r := byID["resolved"]; !r.Resolved || r.Path != nil || r.RecordedAt == nil {
		t.Errorf("resolved = %+v", r)
	}
	if r := byID["unresolved"]; r.Resolved || r.Path == nil || *r.Path != "src/a.go" || r.RecordedAt == nil {
		t.Errorf("unresolved = %+v", r)
	}
	if r := byID["variant"]; !r.Resolved || r.RecordedAt == nil || !strings.HasPrefix(*r.RecordedAt, old.UTC().Format("2006-01-02T15:04")) {
		t.Errorf("variant = %+v", r)
	}
	if r := byID["inprogress"]; r.Path == nil || *r.Path != "lib/b.go" {
		t.Errorf("in-progress path from MERGE_RR = %+v", r)
	}
	if !hasRerereResolution(gitDir, "src/a.go") || hasRerereResolution(gitDir, "nope.go") {
		t.Error("hasRerereResolution")
	}
	rr := filepath.Join(gitDir, "rr-cache")
	for name, want := range map[string]bool{"resolved": true, "missing": false, "": false, "../x": false, ".hidden": false, "stray-file": false} {
		if got := isRerereEntryID(rr, name); got != want {
			t.Errorf("isRerereEntryID(%q) = %v", name, got)
		}
	}
}

func TestRerereList_Endpoint(t *testing.T) {
	e := rebuildEnv(t)
	gitDir := filepath.Join(e.root, "ws", "trunk", ".git")
	if body := e.do(http.MethodGet, wsPath+"/rerere", "", alice).Body.String(); !strings.Contains(body, `"resolutions":[]`) {
		t.Errorf("empty cache = %s", body)
	}
	rrCache(t, gitDir, map[string]map[string]string{"abc": {"preimage": "<<<<<<< f.go\n", "postimage": "ok\n"}})
	got := e.want(e.do(http.MethodGet, wsPath+"/rerere", "", alice), http.StatusOK)
	list := got["resolutions"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != "abc" || list[0].(map[string]any)["path"] != "f.go" || list[0].(map[string]any)["resolved"] != true {
		t.Errorf("resolutions = %v", list)
	}
	e.wantErr(e.do(http.MethodGet, "/api/v1/workspaces/none/rerere", "", alice), http.StatusNotFound)
}

// Forgetting takes an entry id (removed from the cache directly) or a path
// git still knows (git rerere forget), under the workspace lock.
func TestRerereForget_ByEntryIDOrPath(t *testing.T) {
	e := rebuildEnv(t)
	gitDir := filepath.Join(e.root, "ws", "trunk", ".git")
	rrCache(t, gitDir, map[string]map[string]string{
		"byid":   {"preimage": "<<<<<<<\n", "postimage": "x\n"},
		"bypath": {"preimage": "<<<<<<< src/deep/file.go\n", "postimage": "x\n"},
	})
	forget := func(pathspec string) int {
		return e.do(http.MethodDelete, wsPath+"/rerere/"+pathspec, "", alice).Code
	}

	if code := forget("byid"); code != http.StatusNoContent {
		t.Errorf("by id = %d", code)
	}
	if _, err := os.Stat(filepath.Join(gitDir, "rr-cache", "byid")); !os.IsNotExist(err) {
		t.Error("entry not removed from the cache")
	}
	if code := forget("src/deep/file.go"); code != http.StatusNoContent || !e.fake.has("rerere", "forget", "src/deep/file.go") {
		t.Errorf("by path = %d, calls = %v", code, e.fake.calls)
	}
	for pathspec, want := range map[string]int{"unknown.go": 404, "": 400, "-x": 400} {
		if code := forget(pathspec); code != want {
			t.Errorf("forget %q = %d, want %d", pathspec, code, want)
		}
	}
	release := holdLock(t, "ws")
	_, kind := e.wantErr(e.do(http.MethodDelete, wsPath+"/rerere/src/deep/file.go", "", alice), http.StatusConflict)
	release()
	if kind != "workspace_busy" {
		t.Errorf("busy error_type = %q", kind)
	}
	e.fake.run = func(args ...string) (string, error) { return "", errors.New("git failed") }
	if code := forget("src/deep/file.go"); code != http.StatusInternalServerError {
		t.Errorf("git failure = %d", code)
	}
	e.gitErr = errors.New("no runner")
	if code := forget("src/deep/file.go"); code != http.StatusInternalServerError {
		t.Errorf("no runner = %d", code)
	}
}

// A resolution recorded in the repository's rerere cache is replayed by the
// next rebuild, so the conflicting patch applies; the cache lives in the
// trunk and is shared with the per-run worktree.
func TestRebuild_RerereReplaysRecordedResolution(t *testing.T) {
	r := newRebuild(t)
	r.patches("feature/conflict")
	runGit(t, r.trunk, "config", "rerere.enabled", "true")
	runGit(t, r.trunk, "checkout", "-q", "--detach", r.base)
	if _, err := gitErr(r.trunk, "cherry-pick", refOf(r.trunk, "feature/conflict")); err == nil {
		t.Fatal("cherry-pick of the conflicting patch did not conflict")
	}
	writeFile(t, filepath.Join(r.trunk, "base.txt"), "resolved by hand\n")
	runGit(t, r.trunk, "add", "base.txt")
	runGit(t, r.trunk, "rerere")
	runGit(t, r.trunk, "cherry-pick", "--abort")
	runGit(t, r.trunk, "checkout", "-q", "main")

	res, _, err := r.run(context.Background(), StrategyRebase, "")
	if err != nil {
		t.Fatalf("rebuild with a recorded resolution: %v", err)
	}
	if res.PatchesApplied != 1 || runGit(t, r.trunk, "show", "integration:base.txt") != "resolved by hand" {
		t.Errorf("result = %+v, base.txt = %q", res, runGit(t, r.trunk, "show", "integration:base.txt"))
	}
	entries := listRerereEntries(filepath.Join(r.trunk, ".git"))
	if len(entries) != 1 || !entries[0].Resolved {
		t.Errorf("cache after replay = %+v", entries)
	}
	if patchStatuses(t, r.db, r.slug)["feature/conflict"] != PatchStatusActive {
		t.Error("replayed patch marked as conflict")
	}
	// The merge strategy replays it too.
	if res, _, err := r.run(context.Background(), StrategyMerge, ""); err != nil || res.PatchesApplied != 1 {
		t.Errorf("merge replay: %+v, %v", res, err)
	}
}
