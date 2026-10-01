package carrypatch

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/wslock"
)

// TS-01-38 (carrypatch half): carry-patch sync, rollback and rerere forget do
// not consult the rebuild-active guard when the workspace lock is free.
// Requirement: 01-REQ-5.8
func TestSyncRollbackForget_RebuildGuardSet_NotRejected_TS_01_38(t *testing.T) {
	env := newFullTestEnv(t)

	const slug = "guard-ws"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)
	seedPatch(t, env.db, "pg1", slug, "feature/a", 1, PatchStatusActive)
	env.patchStore.Patches = []Patch{
		{ID: "pg1", WorkspaceID: slug, BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
	}
	env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
		return false, nil
	}

	previousSHA := "prev000000000000000000000000000000000001"
	result := RebuildResult{
		UpstreamHeadSHA:            "aaaa000000000000000000000000000000000001",
		IntegrationHeadSHA:         "bbbb000000000000000000000000000000000001",
		PreviousIntegrationHeadSHA: previousSHA,
		Strategy:                   "rebase",
		PatchesApplied:             1,
		PatchResults:               []PatchResult{{PatchID: "pg1", Status: "success"}},
	}
	resultJSON, _ := json.Marshal(result)
	seedRebuildJobWithResult(t, env.db, "job-guard", "completed", slug, "rebase", time.Now(), resultJSON)

	setupRRCacheDir(t, env.workspaceRoot, slug, []rrCacheEntry{
		{hash: "aabbccdd1", preimage: "<<<<<<< src/config.go\nours\n=======\ntheirs\n>>>>>>>"},
	})

	end, ok := wslock.BeginRebuild(slug)
	if !ok {
		t.Fatal("BeginRebuild failed")
	}
	defer end()

	auth := rebuildUserAuth("alice")
	base := "/api/v1/workspaces/" + slug

	rec := env.doRequest(t, http.MethodPost, base+"/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Errorf("sync status = %d; want 200; body: %s", rec.Code, rec.Body.String())
	}

	rec = env.doRequest(t, http.MethodPost, base+"/rebuilds/job-guard/rollback", "", auth)
	if rec.Code != http.StatusOK {
		t.Errorf("rollback status = %d; want 200; body: %s", rec.Code, rec.Body.String())
	}

	rec = env.doRequest(t, http.MethodDelete, base+"/rerere/src/config.go", "", auth)
	if rec.Code != http.StatusNoContent {
		t.Errorf("rerere forget status = %d; want 204; body: %s", rec.Code, rec.Body.String())
	}
}
