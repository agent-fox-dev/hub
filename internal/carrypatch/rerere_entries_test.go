package carrypatch

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// rr-cache images written by git carry unlabelled conflict markers, so the
// path is only known through MERGE_RR; the entry id is always available and
// resolved reflects the presence of a postimage.
func TestListRerereEntries_UnlabelledImagesAndMergeRR(t *testing.T) {
	gitDir := t.TempDir()
	mk := func(id, pre, post string) {
		dir := filepath.Join(gitDir, "rr-cache", id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if pre != "" {
			os.WriteFile(filepath.Join(dir, "preimage"), []byte(pre), 0o644)
		}
		if post != "" {
			os.WriteFile(filepath.Join(dir, "postimage"), []byte(post), 0o644)
		}
	}
	mk("id-resolved", "a\n<<<<<<<\nours\n=======\ntheirs\n>>>>>>>\nc\n", "a\nresolved\nc\n")
	mk("id-pending", "a\n<<<<<<<\nours\n=======\ntheirs\n>>>>>>>\nc\n", "")
	mk("id-empty", "", "")
	os.WriteFile(filepath.Join(gitDir, "MERGE_RR"), []byte("id-pending\tsrc/config.go\x00"), 0o644)

	entries := listRerereEntries(gitDir)
	if len(entries) != 2 {
		t.Fatalf("got %d entries; want 2 (empty entry skipped): %+v", len(entries), entries)
	}
	byID := map[string]RerereResolution{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	if e := byID["id-resolved"]; !e.Resolved || e.Path != nil || e.RecordedAt == nil {
		t.Errorf("id-resolved = %+v; want resolved, nil path, recorded_at set", e)
	}
	if e := byID["id-pending"]; e.Resolved || e.Path == nil || *e.Path != "src/config.go" {
		t.Errorf("id-pending = %+v; want unresolved with path from MERGE_RR", e)
	}
	if !hasRerereResolution(gitDir, "src/config.go") || hasRerereResolution(gitDir, "other.go") {
		t.Error("hasRerereResolution should follow MERGE_RR paths")
	}
	if !isRerereEntryID(filepath.Join(gitDir, "rr-cache"), "id-resolved") || isRerereEntryID(filepath.Join(gitDir, "rr-cache"), "../MERGE_RR") {
		t.Error("isRerereEntryID should accept existing ids only")
	}
}

// DELETE /rerere/<id> removes the cache entry even when git no longer knows
// its path (the state after a completed rebuild).
func TestRerereForget_ByEntryID(t *testing.T) {
	env := newFullTestEnv(t)
	seedWorkspace(t, env.db, "my-workspace", "alice", "active", "ready", "carry_patch", "integration")
	setupRRCacheDir(t, env.workspaceRoot, "my-workspace", []rrCacheEntry{
		{hash: "0123abcd", preimage: "a\n<<<<<<<\nours\n=======\ntheirs\n>>>>>>>\n", postimage: "a\nresolved\n"},
	})

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/my-workspace/rerere", "", rebuildUserAuth("alice"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rerere status = %d; body = %s", rec.Code, rec.Body.String())
	}
	rec = env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/my-workspace/rerere/0123abcd", "", rebuildUserAuth("alice"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /rerere/<id> status = %d; want 204; body = %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(env.workspaceRoot, "my-workspace", "trunk", ".git", "rr-cache", "0123abcd")); !os.IsNotExist(err) {
		t.Fatalf("rr-cache entry should be removed; stat err = %v", err)
	}
	rec = env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/my-workspace/rerere/0123abcd", "", rebuildUserAuth("alice"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second DELETE status = %d; want 404", rec.Code)
	}
}
