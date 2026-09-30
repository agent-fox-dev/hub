package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// ===========================================================================
// TS-02-1 (unit): PATCH_BRANCH_SOURCE is read fresh from GetVariableFunc on
// every sync and any value other than the exact string origin resolves to hub
//
// Verifies: 02-REQ-1.1
// ===========================================================================

func TestCarryPatchSync_TS02_1_PatchBranchSource_ReadFreshResolvesHub(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-branch-source"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	var mu sync.Mutex
	getVarCalls := make([]string, 0)
	currentVarValue := "origin"

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if scope == "workspace" && scopeID == slug {
			getVarCalls = append(getVarCalls, key)
			if key == "PATCH_BRANCH_SOURCE" {
				return currentVarValue, nil
			}
		}
		if key == "REBUILD_STRATEGY" {
			return "rebase", nil
		}
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "true", nil
		}
		return "", nil
	}

	stub := newRecordingOriginFetchStub(nil)
	env.originFetch = stub.Fetch(slug)

	auth := rebuildUserAuth("alice")

	// First sync: PATCH_BRANCH_SOURCE returns "origin" -> fetches origin
	rec1 := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first sync failed: status=%d body=%s", rec1.Code, rec1.Body.String())
	}
	if !stub.called || stub.callCount != 1 {
		t.Fatalf("expected origin fetch to be called on first sync, called=%v count=%d", stub.called, stub.callCount)
	}

	// Change variable to "weird" / unset
	mu.Lock()
	currentVarValue = "weird"
	stub.called = false
	mu.Unlock()

	// Second sync: PATCH_BRANCH_SOURCE returns "weird" -> resolves to hub, no origin fetch
	rec2 := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second sync failed: status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	if stub.called || stub.callCount != 1 {
		t.Fatalf("expected origin fetch NOT to be called on second sync, called=%v count=%d", stub.called, stub.callCount)
	}

	// Verify GetVariable was called with PATCH_BRANCH_SOURCE on both calls
	mu.Lock()
	defer mu.Unlock()
	branchSourceCalls := 0
	for _, k := range getVarCalls {
		if k == "PATCH_BRANCH_SOURCE" {
			branchSourceCalls++
		}
	}
	if branchSourceCalls < 2 {
		t.Errorf("expected at least 2 GetVariable calls for PATCH_BRANCH_SOURCE, got %d", branchSourceCalls)
	}
}

// ===========================================================================
// TS-02-2 (unit): PATCH_DIVERGENCE_POLICY is read only when PATCH_BRANCH_SOURCE
// is origin and any value other than the exact string report resolves to replace
//
// Verifies: 02-REQ-1.2
// ===========================================================================

func TestCarryPatchSync_TS02_2_PatchDivergencePolicy_ReadWhenOriginMode(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-divergence-policy"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	var mu sync.Mutex
	policyCalls := 0
	currentPolicy := "" // unset

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if scope == "workspace" && scopeID == slug {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
			if key == "PATCH_DIVERGENCE_POLICY" {
				policyCalls++
				return currentPolicy, nil
			}
		}
		if key == "REBUILD_STRATEGY" {
			return "rebase", nil
		}
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "true", nil
		}
		return "", nil
	}

	stub := newRecordingOriginFetchStub(nil)
	env.originFetch = stub.Fetch(slug)

	auth := rebuildUserAuth("alice")

	// Run 1: unset PATCH_DIVERGENCE_POLICY
	rec1 := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec1.Code != http.StatusOK {
		t.Fatalf("sync 1 failed: status=%d body=%s", rec1.Code, rec1.Body.String())
	}

	// Run 2: set PATCH_DIVERGENCE_POLICY to 'weird'
	mu.Lock()
	currentPolicy = "weird"
	mu.Unlock()

	rec2 := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec2.Code != http.StatusOK {
		t.Fatalf("sync 2 failed: status=%d body=%s", rec2.Code, rec2.Body.String())
	}

	mu.Lock()
	totalCalls := policyCalls
	mu.Unlock()
	if totalCalls != 2 {
		t.Errorf("expected PATCH_DIVERGENCE_POLICY to be read twice, got %d", totalCalls)
	}
}

// ===========================================================================
// TS-02-3 (unit): Hub mode skips reading PATCH_DIVERGENCE_POLICY and runs the
// origin fetch and fork-refresh as no-ops
//
// Verifies: 02-REQ-1.3
// ===========================================================================

func TestCarryPatchSync_TS02_3_HubMode_SkipsDivergencePolicyAndOriginFetch(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-hub-mode"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	var mu sync.Mutex
	policyCalled := false

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if key == "PATCH_DIVERGENCE_POLICY" {
			policyCalled = true
		}
		if key == "PATCH_BRANCH_SOURCE" {
			return "", nil // unset -> hub mode
		}
		if key == "REBUILD_STRATEGY" {
			return "rebase", nil
		}
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "true", nil
		}
		return "", nil
	}

	stub := newRecordingOriginFetchStub(nil)
	env.originFetch = stub.Fetch(slug)

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if policyCalled {
		t.Error("expected PATCH_DIVERGENCE_POLICY never to be read in hub mode")
	}
	if stub.callCount != 0 {
		t.Errorf("expected originFetch call count = 0, got %d", stub.callCount)
	}
}

// ===========================================================================
// TS-02-4 (unit): A workspace that previously ran in origin mode reproduces the
// pre-spec sync behaviour and response shape once PATCH_BRANCH_SOURCE is hub
//
// Verifies: 02-REQ-1.4
// ===========================================================================

func TestCarryPatchSync_TS02_4_PreviousOriginMode_ProducesPreSpecResponseInHubMode(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-prev-origin"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	var currentSource string = "origin"
	env.getVariable = func(scope, scopeID, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return currentSource, nil
		}
		if key == "REBUILD_STRATEGY" {
			return "rebase", nil
		}
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "true", nil
		}
		return "", nil
	}

	stub := newRecordingOriginFetchStub(nil)
	env.originFetch = stub.Fetch(slug)

	auth := rebuildUserAuth("alice")

	// First sync in origin mode
	rec1 := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec1.Code != http.StatusOK {
		t.Fatalf("origin sync failed: %s", rec1.Body.String())
	}

	// Now switch back to hub mode (unset)
	currentSource = ""
	rec2 := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec2.Code != http.StatusOK {
		t.Fatalf("hub sync failed: %s", rec2.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec2.Body.Bytes(), &raw); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if _, ok := raw["patches_synced"]; ok {
		t.Error("expected no 'patches_synced' field in hub-mode response")
	}

	var resp CarryPatchSyncResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal CarryPatchSyncResponse: %v", err)
	}
	if resp.OriginFetched {
		t.Error("expected origin_fetched to be false in hub mode")
	}
}

// ===========================================================================
// TS-02-5 (unit): Origin mode fetches the origin remote with the correct
// refspec, no tags, resolved auth, and the sync's wslock held
//
// Verifies: 02-REQ-2.1
// ===========================================================================

func TestCarryPatchSync_TS02_5_OriginMode_FetchesWithRefspecNoTagsAuthAndLockHeld(t *testing.T) {
	// First: verify the DefaultOriginFetchFunc refspecs and tags configuration
	if len(OriginRefSpecs) != 1 || OriginRefSpecs[0] != "+refs/heads/*:refs/remotes/origin/*" {
		t.Errorf("expected OriginRefSpecs to be [+refs/heads/*:refs/remotes/origin/*], got %v", OriginRefSpecs)
	}

	// Test DefaultOriginFetchFunc against a real repository
	t.Run("DefaultOriginFetchFunc execution", func(t *testing.T) {
		tempDir := t.TempDir()
		bareOrigin := filepath.Join(tempDir, "origin.git")
		cmd := exec.Command("git", "init", "--bare", bareOrigin)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init --bare failed: %v, out=%s", err, out)
		}

		// Create workspace trunk with origin remote pointing to bareOrigin
		trunkDir := filepath.Join(tempDir, "trunk")
		if out, err := exec.Command("git", "init", trunkDir).CombinedOutput(); err != nil {
			t.Fatalf("git init trunk failed: %v, out=%s", err, out)
		}
		if out, err := exec.Command("git", "-C", trunkDir, "config", "user.name", "Test").CombinedOutput(); err != nil {
			t.Fatalf("git config user.name failed: %v, out=%s", err, out)
		}
		if out, err := exec.Command("git", "-C", trunkDir, "config", "user.email", "test@example.com").CombinedOutput(); err != nil {
			t.Fatalf("git config user.email failed: %v, out=%s", err, out)
		}
		if out, err := exec.Command("git", "-C", trunkDir, "commit", "--allow-empty", "-m", "init").CombinedOutput(); err != nil {
			t.Fatalf("git commit failed: %v, out=%s", err, out)
		}
		if out, err := exec.Command("git", "-C", trunkDir, "remote", "add", "origin", bareOrigin).CombinedOutput(); err != nil {
			t.Fatalf("git remote add failed: %v, out=%s", err, out)
		}
		if out, err := exec.Command("git", "-C", trunkDir, "push", "origin", "HEAD:main").CombinedOutput(); err != nil {
			t.Fatalf("git push origin failed: %v, out=%s", err, out)
		}

		fetchFn := DefaultOriginFetchFunc()
		// First fetch: fetches refs
		if err := fetchFn(context.Background(), trunkDir, nil); err != nil {
			t.Fatalf("DefaultOriginFetchFunc first fetch failed: %v", err)
		}
		// Second fetch: already up to date, must return nil
		if err := fetchFn(context.Background(), trunkDir, nil); err != nil {
			t.Fatalf("DefaultOriginFetchFunc second fetch (up-to-date) failed: %v", err)
		}
	})

	// Second: verify that runCarryPatchSync invokes OriginFetch under wslock with resolved auth
	env := newFullTestEnv(t)
	slug := "ws-fetch-lock"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		if key == "REBUILD_STRATEGY" {
			return "rebase", nil
		}
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "true", nil
		}
		return "", nil
	}

	expectedAuth := &githttp.BasicAuth{Username: "x-token-auth", Password: "test-token"}
	env.resolveOriginAuth = func(s string) (transport.AuthMethod, error) {
		if s == slug {
			return expectedAuth, nil
		}
		return nil, nil
	}

	stub := newRecordingOriginFetchStub(nil)
	env.originFetch = stub.Fetch(slug)

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	if !stub.called {
		t.Fatal("expected origin fetch to be called")
	}
	if !stub.calledUnderLock {
		t.Error("expected origin fetch to be called while wslock is held")
	}
	if stub.auth != expectedAuth {
		t.Errorf("expected origin fetch auth %v, got %v", expectedAuth, stub.auth)
	}
}

// ===========================================================================
// TS-02-6 (unit): A failing origin fetch aborts the sync with 502 and leaves
// the workspace row and every patches row unchanged
//
// Verifies: 02-REQ-2.2
// ===========================================================================

func TestCarryPatchSync_TS02_6_OriginFetchFailure_Returns502AndLeavesStateUnchanged(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-fail-fetch"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)
	seedPatch(t, env.db, "p1", slug, "feature/one", 1, PatchStatusActive)

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		if key == "REBUILD_STRATEGY" {
			return "rebase", nil
		}
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "true", nil
		}
		return "", nil
	}

	stub := newRecordingOriginFetchStub(errors.New("network unreachable"))
	env.originFetch = stub.Fetch(slug)

	// Snapshot workspace row and patch row
	var wsHeadBefore, wsLastSyncBefore sql.NullString
	if err := env.db.QueryRow("SELECT upstream_head_sha, last_sync_at FROM workspaces WHERE slug = ?", slug).
		Scan(&wsHeadBefore, &wsLastSyncBefore); err != nil {
		t.Fatalf("failed to query workspace before: %v", err)
	}

	var patchStatusBefore string
	if err := env.db.QueryRow("SELECT status FROM patches WHERE id = 'p1'").Scan(&patchStatusBefore); err != nil {
		t.Fatalf("failed to query patch before: %v", err)
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected status 502, got %d; body=%s", rec.Code, rec.Body.String())
	}

	var errResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to decode error body: %v", err)
	}
	msg, _ := errResp["message"].(string)
	if msg == "" {
		if errObj, ok := errResp["error"].(map[string]any); ok {
			msg, _ = errObj["message"].(string)
		}
	}
	if msg != "origin fetch failed" {
		t.Errorf("expected error message 'origin fetch failed', got %q", msg)
	}

	// Verify workspace row unchanged
	var wsHeadAfter, wsLastSyncAfter sql.NullString
	if err := env.db.QueryRow("SELECT upstream_head_sha, last_sync_at FROM workspaces WHERE slug = ?", slug).
		Scan(&wsHeadAfter, &wsLastSyncAfter); err != nil {
		t.Fatalf("failed to query workspace after: %v", err)
	}
	if wsHeadBefore != wsHeadAfter {
		t.Errorf("upstream_head_sha changed: before=%v after=%v", wsHeadBefore, wsHeadAfter)
	}
	if wsLastSyncBefore != wsLastSyncAfter {
		t.Errorf("last_sync_at changed: before=%v after=%v", wsLastSyncBefore, wsLastSyncAfter)
	}

	// Verify patch row unchanged
	var patchStatusAfter string
	if err := env.db.QueryRow("SELECT status FROM patches WHERE id = 'p1'").Scan(&patchStatusAfter); err != nil {
		t.Fatalf("failed to query patch after: %v", err)
	}
	if patchStatusBefore != patchStatusAfter {
		t.Errorf("patch status changed: before=%s after=%s", patchStatusBefore, patchStatusAfter)
	}
}

// ===========================================================================
// TS-02-7 (unit): An origin fetch error satisfying errors.Is(err,
// git.NoErrAlreadyUpToDate) is treated as success
//
// Verifies: 02-REQ-2.3
// ===========================================================================

func TestCarryPatchSync_TS02_7_OriginFetch_AlreadyUpToDate_TreatedAsSuccess(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-up-to-date"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		if key == "REBUILD_STRATEGY" {
			return "rebase", nil
		}
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "true", nil
		}
		return "", nil
	}

	// OriginFetch returns git.NoErrAlreadyUpToDate
	env.originFetch = func(_ context.Context, _ string, _ transport.AuthMethod) error {
		return git.NoErrAlreadyUpToDate
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d; body=%s", rec.Code, rec.Body.String())
	}

	var resp CarryPatchSyncResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if !resp.OriginFetched {
		t.Error("expected origin_fetched=true when origin is up to date")
	}
}

// ===========================================================================
// TS-02-8 (unit): origin_fetched is true only when the origin fetch was
// attempted and succeeded, false in hub mode and on fetch failure
//
// Verifies: 02-REQ-2.4
// ===========================================================================

func TestCarryPatchSync_TS02_8_OriginFetched_FieldValues(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-origin-fetched-test"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	var currentMode string
	env.getVariable = func(scope, scopeID, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return currentMode, nil
		}
		if key == "REBUILD_STRATEGY" {
			return "rebase", nil
		}
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "true", nil
		}
		return "", nil
	}

	var fetchErr error
	env.originFetch = func(_ context.Context, _ string, _ transport.AuthMethod) error {
		return fetchErr
	}

	auth := rebuildUserAuth("alice")

	// 1. Hub mode: origin_fetched must be false
	currentMode = "hub"
	fetchErr = nil
	recHub := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if recHub.Code != http.StatusOK {
		t.Fatalf("hub mode failed: %s", recHub.Body.String())
	}
	var hubResp CarryPatchSyncResponse
	if err := json.Unmarshal(recHub.Body.Bytes(), &hubResp); err != nil {
		t.Fatalf("failed to parse hub resp: %v", err)
	}
	if hubResp.OriginFetched {
		t.Error("hub mode expected origin_fetched=false, got true")
	}

	// 2. Origin mode with success: origin_fetched must be true
	currentMode = "origin"
	fetchErr = nil
	recOrigin := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if recOrigin.Code != http.StatusOK {
		t.Fatalf("origin mode failed: %s", recOrigin.Body.String())
	}
	var originResp CarryPatchSyncResponse
	if err := json.Unmarshal(recOrigin.Body.Bytes(), &originResp); err != nil {
		t.Fatalf("failed to parse origin resp: %v", err)
	}
	if !originResp.OriginFetched {
		t.Error("origin mode success expected origin_fetched=true, got false")
	}

	// 3. Origin mode with failure: 502 returned
	currentMode = "origin"
	fetchErr = errors.New("network error")
	recFail := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if recFail.Code != http.StatusBadGateway {
		t.Errorf("origin mode failure expected status 502, got %d", recFail.Code)
	}
}
