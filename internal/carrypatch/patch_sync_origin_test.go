package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// Helpers for integration tests that use real git repos
// ===========================================================================

// originIntegrationEnv holds everything needed for an origin-mode integration
// test: a real bare fork, a real trunk cloned from it, a database, and a
// configured echo server.
type originIntegrationEnv struct {
	forkDir  string // bare fork repo
	trunkDir string // trunk (clone of fork)
	wsRoot   string // workspace root (parent of <slug>/trunk)
	db       *sql.DB
	queue    *jobqueue.Queue
	echo     *echo.Echo

	// Variables for GetVariable.
	variables map[string]string
}

// setupOriginIntegrationEnv creates a bare fork, clones it as the trunk,
// sets up the database and echo server for origin-mode sync tests.
// The upstream tracking ref is set to the trunk's HEAD.
func setupOriginIntegrationEnv(t *testing.T) *originIntegrationEnv {
	t.Helper()

	// Create bare fork.
	forkDir := t.TempDir()
	runGitCmd(t, "", "init", "--bare", forkDir)

	// Create a working copy to push initial content.
	workDir := t.TempDir()
	runGitCmd(t, "", "clone", forkDir, workDir)
	configGitUserCmd(t, workDir)

	writeFileHelper(t, filepath.Join(workDir, "file.txt"), "hello")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "initial")
	runGitCmd(t, workDir, "push", "origin", "main")

	// Create trunk as a clone of the fork.
	wsRoot := t.TempDir()
	trunkDir := filepath.Join(wsRoot, "my-workspace", "trunk")
	runGitCmd(t, "", "clone", forkDir, trunkDir)
	configGitUserCmd(t, trunkDir)

	// Set up upstream tracking ref so resolveUpstreamBase works.
	upstreamSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/remotes/upstream/HEAD", upstreamSHA)

	// Database setup.
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)
	addWorkspaceColumns(t, db)

	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey: %v", err)
	}
	if err := jobqueue.MigrateProgress(db); err != nil {
		t.Fatalf("MigrateProgress: %v", err)
	}

	logger := nopLogger()
	q, err := jobqueue.New(db, logger)
	if err != nil {
		t.Fatalf("jobqueue.New: %v", err)
	}
	_ = RegisterRebuildJob(q, &RebuildHandler{})

	// Seed workspace.
	seedWorkspaceCarryPatch(t, db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		upstreamSHA,
		"integration", "")

	env := &originIntegrationEnv{
		forkDir:   forkDir,
		trunkDir:  trunkDir,
		wsRoot:    wsRoot,
		db:        db,
		queue:     q,
		variables: map[string]string{"PATCH_BRANCH_SOURCE": "origin"},
	}

	return env
}

// buildEcho creates the echo server with sync routes. Call after setting up
// variables and any custom runner.
func (env *originIntegrationEnv) buildEcho(t *testing.T, runner GitRunner) {
	t.Helper()

	patchStore := NewSQLPatchStore(env.db)

	getVar := func(scope, slug, key string) (string, error) {
		if v, ok := env.variables[key]; ok {
			return v, nil
		}
		return "", fmt.Errorf("not set")
	}

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())

	syncCfg := SyncAPIConfig{
		DB:            env.db,
		Queue:         env.queue,
		WorkspaceRoot: env.wsRoot,
		NewGitRunner: func(_ string) (GitRunner, error) {
			return runner, nil
		},
		Fetch:             func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveAuth:       func(_ string) (transport.AuthMethod, error) { return nil, nil },
		GetVariable:       getVar,
		PatchStore:        patchStore,
		FetchOrigin:       func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveOriginAuth: func(_ string) (transport.AuthMethod, error) { return nil, nil },
	}
	RegisterSyncRoutes(api, syncCfg)

	env.echo = e
}

// buildEchoWithProductionFetch creates the echo server using the real
// DefaultFetchOriginFunc for the origin fetch.
func (env *originIntegrationEnv) buildEchoWithProductionFetch(t *testing.T, runner GitRunner) {
	t.Helper()

	patchStore := NewSQLPatchStore(env.db)

	getVar := func(scope, slug, key string) (string, error) {
		if v, ok := env.variables[key]; ok {
			return v, nil
		}
		return "", fmt.Errorf("not set")
	}

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())

	syncCfg := SyncAPIConfig{
		DB:            env.db,
		Queue:         env.queue,
		WorkspaceRoot: env.wsRoot,
		NewGitRunner: func(_ string) (GitRunner, error) {
			return runner, nil
		},
		Fetch:             func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveAuth:       func(_ string) (transport.AuthMethod, error) { return nil, nil },
		GetVariable:       getVar,
		PatchStore:        patchStore,
		FetchOrigin:       DefaultFetchOriginFunc(),
		ResolveOriginAuth: func(_ string) (transport.AuthMethod, error) { return nil, nil },
	}
	RegisterSyncRoutes(api, syncCfg)

	env.echo = e
}

func (env *originIntegrationEnv) doSync(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	auth := rebuildUserAuth("alice")
	authJSON, _ := json.Marshal(auth)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/my-workspace/sync", nil)
	req.Header.Set("X-Test-Auth", string(authJSON))
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	return rec
}

// addForkBranch creates a branch on the fork with a commit.
func (env *originIntegrationEnv) addForkBranch(t *testing.T, branchName, content string) string {
	t.Helper()
	workDir := t.TempDir()
	runGitCmd(t, "", "clone", env.forkDir, workDir)
	configGitUserCmd(t, workDir)
	runGitCmd(t, workDir, "checkout", "-b", branchName)
	writeFileHelper(t, filepath.Join(workDir, branchName+".txt"), content)
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "add "+branchName)
	sha := runGitCmd(t, workDir, "rev-parse", "HEAD")
	runGitCmd(t, workDir, "push", "origin", branchName)
	return sha
}

// addForkCommit adds a commit to an existing branch on the fork.
func (env *originIntegrationEnv) addForkCommit(t *testing.T, branchName, content string) string {
	t.Helper()
	workDir := t.TempDir()
	runGitCmd(t, "", "clone", "-b", branchName, env.forkDir, workDir)
	configGitUserCmd(t, workDir)
	writeFileHelper(t, filepath.Join(workDir, branchName+"-extra.txt"), content)
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "extra commit on "+branchName)
	sha := runGitCmd(t, workDir, "rev-parse", "HEAD")
	runGitCmd(t, workDir, "push", "origin", branchName)
	return sha
}

// fetchOriginInTrunk fetches origin in the trunk so tracking refs exist.
func (env *originIntegrationEnv) fetchOriginInTrunk(t *testing.T) {
	t.Helper()
	runGitCmd(t, env.trunkDir, "fetch", "origin")
}

// queryPatchOriginState reads the persisted origin sync state for a patch.
func (env *originIntegrationEnv) queryPatchOriginState(t *testing.T, patchID string) (state sql.NullString, sha sql.NullString, syncedAt sql.NullString) {
	t.Helper()
	err := env.db.QueryRow(
		`SELECT origin_sync_state, origin_sha, origin_synced_at FROM patches WHERE id = ?`, patchID,
	).Scan(&state, &sha, &syncedAt)
	if err != nil {
		t.Fatalf("query patch origin state for %s: %v", patchID, err)
	}
	return
}

// refExists checks if a ref exists in the trunk.
func refExists(t *testing.T, dir, ref string) bool {
	t.Helper()
	_, err := runGitCmdErr(t, dir, "rev-parse", "--verify", ref)
	return err == nil
}

// refSHA resolves a ref to its SHA in the given directory.
func refSHA(t *testing.T, dir, ref string) string {
	t.Helper()
	return runGitCmd(t, dir, "rev-parse", ref)
}

// ===========================================================================
// TS-20-14 (integration): A branch deleted on a real bare fork loses its
// tracking ref after an origin-mode sync
//
// Verifies: 20-REQ-2.7
// ===========================================================================

func TestSyncOriginIntegration_DeletedBranchLosesTrackingRef_TS2014(t *testing.T) {
	env := setupOriginIntegrationEnv(t)

	// Create feat on the fork and fetch it into the trunk.
	env.addForkBranch(t, "feat", "feat content")
	env.fetchOriginInTrunk(t)

	// Verify tracking ref exists.
	if !refExists(t, env.trunkDir, "refs/remotes/origin/feat") {
		t.Fatal("expected refs/remotes/origin/feat to exist after fetch")
	}

	// Register feat as an active patch.
	seedPatch(t, env.db, "p1", "my-workspace", "feat", 1, PatchStatusActive)

	// Delete feat on the fork.
	// We need to push a delete to the bare repo.
	workDir := t.TempDir()
	runGitCmd(t, "", "clone", env.forkDir, workDir)
	runGitCmd(t, workDir, "push", "origin", "--delete", "feat")

	// Build echo with production fetch (uses go-git with Prune=true).
	runner := newRealGitRunner(t, env.trunkDir)
	env.buildEchoWithProductionFetch(t, runner)

	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify tracking ref is gone.
	if refExists(t, env.trunkDir, "refs/remotes/origin/feat") {
		t.Error("expected refs/remotes/origin/feat to be pruned after sync")
	}

	// Parse response and check the element for feat.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	patchesSynced, ok := resp["patches_synced"].([]any)
	if !ok || len(patchesSynced) == 0 {
		t.Fatalf("expected patches_synced array, got %v", resp["patches_synced"])
	}

	elem := patchesSynced[0].(map[string]any)
	if elem["state"] != StateMissingOnOrigin {
		t.Errorf("expected state=%q, got %v", StateMissingOnOrigin, elem["state"])
	}

	// Verify persisted state.
	state, sha, syncedAt := env.queryPatchOriginState(t, "p1")
	if !state.Valid || state.String != StateMissingOnOrigin {
		t.Errorf("persisted state = %v; want %q", state, StateMissingOnOrigin)
	}
	if sha.Valid {
		t.Errorf("persisted origin_sha should be NULL for missing_on_origin, got %q", sha.String)
	}
	if !syncedAt.Valid {
		t.Error("persisted origin_synced_at should be set")
	}
}

// ===========================================================================
// TS-20-16 (integration): A branch missing on the fork is reported
// missing_on_origin and left alone
//
// Verifies: 20-REQ-3.2
// ===========================================================================

func TestSyncOriginIntegration_MissingOnFork_TS2016(t *testing.T) {
	env := setupOriginIntegrationEnv(t)

	// Create a local feat branch in the trunk (not on the fork).
	runGitCmd(t, env.trunkDir, "checkout", "-b", "feat")
	writeFileHelper(t, filepath.Join(env.trunkDir, "feat.txt"), "local feat")
	runGitCmd(t, env.trunkDir, "add", ".")
	runGitCmd(t, env.trunkDir, "commit", "-m", "local feat commit")
	localTip := runGitCmd(t, env.trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, env.trunkDir, "checkout", "main")

	// Register feat as an active patch.
	seedPatch(t, env.db, "p1", "my-workspace", "feat", 1, PatchStatusActive)

	// Fetch origin to ensure tracking refs are up to date (feat won't exist).
	env.fetchOriginInTrunk(t)

	runner := newRealGitRunner(t, env.trunkDir)
	env.buildEcho(t, runner)

	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// refs/heads/feat should be unchanged.
	afterTip := runGitCmd(t, env.trunkDir, "rev-parse", "refs/heads/feat")
	if afterTip != localTip {
		t.Errorf("refs/heads/feat changed: %s → %s", localTip, afterTip)
	}

	// Parse response.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	patchesSynced := resp["patches_synced"].([]any)
	if len(patchesSynced) != 1 {
		t.Fatalf("expected 1 element in patches_synced, got %d", len(patchesSynced))
	}

	elem := patchesSynced[0].(map[string]any)
	if elem["state"] != StateMissingOnOrigin {
		t.Errorf("state = %v; want %q", elem["state"], StateMissingOnOrigin)
	}
	if elem["action"] != ActionNone {
		t.Errorf("action = %v; want %q", elem["action"], ActionNone)
	}
	if _, hasOriginSHA := elem["origin_sha"]; hasOriginSHA {
		t.Error("expected no origin_sha for missing_on_origin")
	}
	if elem["local_sha"] != localTip {
		t.Errorf("local_sha = %v; want %s", elem["local_sha"], localTip)
	}

	// Verify patch status is still active.
	var patchStatus string
	env.db.QueryRow(`SELECT status FROM patches WHERE id = 'p1'`).Scan(&patchStatus)
	if patchStatus != PatchStatusActive {
		t.Errorf("patch status = %q; want %q", patchStatus, PatchStatusActive)
	}

	// Verify persisted state.
	state, sha, _ := env.queryPatchOriginState(t, "p1")
	if !state.Valid || state.String != StateMissingOnOrigin {
		t.Errorf("persisted state = %v; want %q", state, StateMissingOnOrigin)
	}
	if sha.Valid {
		t.Errorf("persisted origin_sha should be NULL, got %q", sha.String)
	}
}

// ===========================================================================
// TS-20-17 (integration): A missing local branch is created at the fork tip
//
// Verifies: 20-REQ-3.3
// ===========================================================================

func TestSyncOriginIntegration_MissingLocalCreated_TS2017(t *testing.T) {
	env := setupOriginIntegrationEnv(t)

	// Create feat on the fork.
	forkTip := env.addForkBranch(t, "feat", "feat content")

	// Fetch origin so tracking ref exists.
	env.fetchOriginInTrunk(t)

	// No local refs/heads/feat exists.
	if refExists(t, env.trunkDir, "refs/heads/feat") {
		t.Fatal("expected no local refs/heads/feat before sync")
	}

	// Register feat as an active patch.
	seedPatch(t, env.db, "p1", "my-workspace", "feat", 1, PatchStatusActive)

	runner := newRealGitRunner(t, env.trunkDir)
	env.buildEcho(t, runner)

	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// refs/heads/feat should now exist and equal the fork tip.
	localTip := runGitCmd(t, env.trunkDir, "rev-parse", "refs/heads/feat")
	if localTip != forkTip {
		t.Errorf("refs/heads/feat = %s; want %s (fork tip)", localTip, forkTip)
	}

	// Parse response.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	patchesSynced := resp["patches_synced"].([]any)
	elem := patchesSynced[0].(map[string]any)
	if elem["action"] != ActionCreated {
		t.Errorf("action = %v; want %q", elem["action"], ActionCreated)
	}
	if elem["state"] != StateInSync {
		t.Errorf("state = %v; want %q", elem["state"], StateInSync)
	}
	if elem["local_sha"] != forkTip {
		t.Errorf("local_sha = %v; want %s", elem["local_sha"], forkTip)
	}
	if elem["origin_sha"] != forkTip {
		t.Errorf("origin_sha = %v; want %s", elem["origin_sha"], forkTip)
	}

	// Verify persisted state.
	state, sha, syncedAt := env.queryPatchOriginState(t, "p1")
	if !state.Valid || state.String != StateInSync {
		t.Errorf("persisted state = %v; want %q", state, StateInSync)
	}
	if !sha.Valid || sha.String != forkTip {
		t.Errorf("persisted origin_sha = %v; want %s", sha, forkTip)
	}
	if !syncedAt.Valid {
		t.Error("persisted origin_synced_at should be set")
	}
}

// ===========================================================================
// TS-20-18 (integration): Equal tips write no ref and report in_sync
//
// Verifies: 20-REQ-3.4
// ===========================================================================

func TestSyncOriginIntegration_EqualTips_TS2018(t *testing.T) {
	env := setupOriginIntegrationEnv(t)

	// Create feat on the fork.
	forkTip := env.addForkBranch(t, "feat", "feat content")

	// Fetch origin and create local branch at the same commit.
	env.fetchOriginInTrunk(t)
	runGitCmd(t, env.trunkDir, "branch", "feat", "refs/remotes/origin/feat")

	localTip := runGitCmd(t, env.trunkDir, "rev-parse", "refs/heads/feat")
	if localTip != forkTip {
		t.Fatalf("setup error: local tip %s != fork tip %s", localTip, forkTip)
	}

	// Register feat as an active patch.
	seedPatch(t, env.db, "p1", "my-workspace", "feat", 1, PatchStatusActive)

	// Use a recording runner to verify no update-ref calls.
	realRunner := newRealGitRunner(t, env.trunkDir)
	recorder := &recordingGitRunner{real: realRunner}
	env.buildEcho(t, recorder)

	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Check no update-ref calls were made for feat.
	for _, call := range recorder.runCalls {
		if len(call) >= 2 && call[0] == "update-ref" {
			t.Errorf("unexpected update-ref call: %v", call)
		}
	}

	// Parse response.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	patchesSynced := resp["patches_synced"].([]any)
	elem := patchesSynced[0].(map[string]any)
	if elem["action"] != ActionNone {
		t.Errorf("action = %v; want %q", elem["action"], ActionNone)
	}
	if elem["state"] != StateInSync {
		t.Errorf("state = %v; want %q", elem["state"], StateInSync)
	}

	// Verify persisted state.
	state, _, _ := env.queryPatchOriginState(t, "p1")
	if !state.Valid || state.String != StateInSync {
		t.Errorf("persisted state = %v; want %q", state, StateInSync)
	}
}

// ===========================================================================
// TS-20-19 (integration): A local tip that is a strict ancestor of the fork
// tip is fast-forwarded
//
// Verifies: 20-REQ-3.5
// ===========================================================================

func TestSyncOriginIntegration_FastForward_TS2019(t *testing.T) {
	env := setupOriginIntegrationEnv(t)

	// Create feat on the fork with one commit.
	env.addForkBranch(t, "feat", "feat content")

	// Fetch and create local branch at the fork tip.
	env.fetchOriginInTrunk(t)
	runGitCmd(t, env.trunkDir, "branch", "feat", "refs/remotes/origin/feat")
	localBase := runGitCmd(t, env.trunkDir, "rev-parse", "refs/heads/feat")

	// Add two more commits on the fork.
	env.addForkCommit(t, "feat", "extra1")
	forkTip := env.addForkCommit(t, "feat", "extra2")

	// Fetch origin again to update tracking ref.
	env.fetchOriginInTrunk(t)

	// Register feat as an active patch.
	seedPatch(t, env.db, "p1", "my-workspace", "feat", 1, PatchStatusActive)

	runner := newRealGitRunner(t, env.trunkDir)
	env.buildEcho(t, runner)

	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// refs/heads/feat should equal the fork tip.
	localTip := runGitCmd(t, env.trunkDir, "rev-parse", "refs/heads/feat")
	if localTip != forkTip {
		t.Errorf("refs/heads/feat = %s; want %s (fork tip)", localTip, forkTip)
	}

	// No backup ref should exist.
	if refExists(t, env.trunkDir, "refs/hub/replaced/feat") {
		t.Error("expected no refs/hub/replaced/feat for fast-forward")
	}

	// Parse response.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	patchesSynced := resp["patches_synced"].([]any)
	elem := patchesSynced[0].(map[string]any)
	if elem["action"] != ActionFastForwarded {
		t.Errorf("action = %v; want %q", elem["action"], ActionFastForwarded)
	}
	if elem["state"] != StateInSync {
		t.Errorf("state = %v; want %q", elem["state"], StateInSync)
	}
	if _, hasReplaced := elem["replaced_sha"]; hasReplaced {
		t.Error("expected no replaced_sha for fast-forward")
	}

	_ = localBase
}

// ===========================================================================
// TS-20-20 (integration): Divergence under replace backs up the old tip and
// moves the branch to the fork tip
//
// Verifies: 20-REQ-3.6
// ===========================================================================

func TestSyncOriginIntegration_DivergenceReplace_TS2020(t *testing.T) {
	env := setupOriginIntegrationEnv(t)

	// Create feat on the fork.
	env.addForkBranch(t, "feat", "fork feat content")

	// Fetch and create local branch.
	env.fetchOriginInTrunk(t)
	runGitCmd(t, env.trunkDir, "branch", "feat", "refs/remotes/origin/feat")

	// Add a divergent commit on the fork.
	forkTip := env.addForkCommit(t, "feat", "fork divergent")

	// Add a divergent commit locally.
	runGitCmd(t, env.trunkDir, "checkout", "feat")
	writeFileHelper(t, filepath.Join(env.trunkDir, "local-divergent.txt"), "local divergent")
	runGitCmd(t, env.trunkDir, "add", ".")
	runGitCmd(t, env.trunkDir, "commit", "-m", "local divergent commit")
	oldLocalTip := runGitCmd(t, env.trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, env.trunkDir, "checkout", "main")

	// Fetch origin again.
	env.fetchOriginInTrunk(t)

	// Register feat as an active patch with replace policy (default).
	seedPatch(t, env.db, "p1", "my-workspace", "feat", 1, PatchStatusActive)
	env.variables["PATCH_DIVERGENCE_POLICY"] = "replace"

	runner := newRealGitRunner(t, env.trunkDir)
	env.buildEcho(t, runner)

	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// refs/hub/replaced/feat should hold the old local tip.
	backupSHA := runGitCmd(t, env.trunkDir, "rev-parse", "refs/hub/replaced/feat")
	if backupSHA != oldLocalTip {
		t.Errorf("backup ref = %s; want %s (old local tip)", backupSHA, oldLocalTip)
	}

	// refs/heads/feat should hold the fork tip.
	localTip := runGitCmd(t, env.trunkDir, "rev-parse", "refs/heads/feat")
	if localTip != forkTip {
		t.Errorf("refs/heads/feat = %s; want %s (fork tip)", localTip, forkTip)
	}

	// Parse response.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	patchesSynced := resp["patches_synced"].([]any)
	elem := patchesSynced[0].(map[string]any)
	if elem["action"] != ActionReplaced {
		t.Errorf("action = %v; want %q", elem["action"], ActionReplaced)
	}
	if elem["state"] != StateInSync {
		t.Errorf("state = %v; want %q", elem["state"], StateInSync)
	}
	if elem["replaced_sha"] != oldLocalTip {
		t.Errorf("replaced_sha = %v; want %s", elem["replaced_sha"], oldLocalTip)
	}

	// Verify a second sync overwrites the backup ref.
	// Add another divergent commit locally.
	runGitCmd(t, env.trunkDir, "checkout", "feat")
	writeFileHelper(t, filepath.Join(env.trunkDir, "local-divergent2.txt"), "local divergent 2")
	runGitCmd(t, env.trunkDir, "add", ".")
	runGitCmd(t, env.trunkDir, "commit", "-m", "local divergent commit 2")
	newLocalTip := runGitCmd(t, env.trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, env.trunkDir, "checkout", "main")

	rec2 := env.doSync(t)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second sync status = %d; want %d; body = %s", rec2.Code, http.StatusOK, rec2.Body.String())
	}

	// Backup should now hold the new local tip (overwritten).
	backupSHA2 := runGitCmd(t, env.trunkDir, "rev-parse", "refs/hub/replaced/feat")
	if backupSHA2 != newLocalTip {
		t.Errorf("backup ref after second sync = %s; want %s", backupSHA2, newLocalTip)
	}
}

// ===========================================================================
// TS-20-21 (integration): Divergence under report leaves the local branch
// and reports both SHAs
//
// Verifies: 20-REQ-3.7
// ===========================================================================

func TestSyncOriginIntegration_DivergenceReport_TS2021(t *testing.T) {
	env := setupOriginIntegrationEnv(t)

	// Create feat on the fork.
	env.addForkBranch(t, "feat", "fork feat content")

	// Fetch and create local branch.
	env.fetchOriginInTrunk(t)
	runGitCmd(t, env.trunkDir, "branch", "feat", "refs/remotes/origin/feat")

	// Add a divergent commit on the fork.
	env.addForkCommit(t, "feat", "fork divergent")

	// Add a divergent commit locally.
	runGitCmd(t, env.trunkDir, "checkout", "feat")
	writeFileHelper(t, filepath.Join(env.trunkDir, "local-divergent.txt"), "local divergent")
	runGitCmd(t, env.trunkDir, "add", ".")
	runGitCmd(t, env.trunkDir, "commit", "-m", "local divergent commit")
	oldLocalTip := runGitCmd(t, env.trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, env.trunkDir, "checkout", "main")

	// Fetch origin again.
	env.fetchOriginInTrunk(t)

	// Register feat as an active patch with report policy.
	seedPatch(t, env.db, "p1", "my-workspace", "feat", 1, PatchStatusActive)
	env.variables["PATCH_DIVERGENCE_POLICY"] = "report"

	runner := newRealGitRunner(t, env.trunkDir)
	env.buildEcho(t, runner)

	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// refs/heads/feat should be unchanged.
	localTip := runGitCmd(t, env.trunkDir, "rev-parse", "refs/heads/feat")
	if localTip != oldLocalTip {
		t.Errorf("refs/heads/feat changed: %s → %s", oldLocalTip, localTip)
	}

	// No backup ref.
	if refExists(t, env.trunkDir, "refs/hub/replaced/feat") {
		t.Error("expected no refs/hub/replaced/feat under report policy")
	}

	// Parse response.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	patchesSynced := resp["patches_synced"].([]any)
	elem := patchesSynced[0].(map[string]any)
	if elem["state"] != StateDiverged {
		t.Errorf("state = %v; want %q", elem["state"], StateDiverged)
	}
	if elem["action"] != ActionNone {
		t.Errorf("action = %v; want %q", elem["action"], ActionNone)
	}
	localSHA, _ := elem["local_sha"].(string)
	originSHA, _ := elem["origin_sha"].(string)
	if localSHA == "" || originSHA == "" {
		t.Error("expected both local_sha and origin_sha to be set")
	}
	if localSHA == originSHA {
		t.Error("expected local_sha and origin_sha to be different")
	}

	// patches_diverged should contain feat.
	patchesDiverged, _ := resp["patches_diverged"].([]any)
	found := false
	for _, pd := range patchesDiverged {
		if pd == "feat" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'feat' in patches_diverged, got %v", patchesDiverged)
	}

	// Verify persisted state.
	state, _, _ := env.queryPatchOriginState(t, "p1")
	if !state.Valid || state.String != StateDiverged {
		t.Errorf("persisted state = %v; want %q", state, StateDiverged)
	}
}

// ===========================================================================
// TS-20-22 (integration): A fork tip that is a strict ancestor of the local
// tip is divergence and follows the policy
//
// Verifies: 20-REQ-3.6, 20-REQ-3.7
// ===========================================================================

func TestSyncOriginIntegration_ForkAncestorOfLocal_TS2022(t *testing.T) {
	t.Run("replace", func(t *testing.T) {
		env := setupOriginIntegrationEnv(t)

		// Create feat on the fork.
		forkTip := env.addForkBranch(t, "feat", "fork feat content")

		// Fetch and create local branch at fork tip.
		env.fetchOriginInTrunk(t)
		runGitCmd(t, env.trunkDir, "branch", "feat", "refs/remotes/origin/feat")

		// Add extra commits locally (hub is ahead of fork).
		runGitCmd(t, env.trunkDir, "checkout", "feat")
		writeFileHelper(t, filepath.Join(env.trunkDir, "local-extra.txt"), "local extra")
		runGitCmd(t, env.trunkDir, "add", ".")
		runGitCmd(t, env.trunkDir, "commit", "-m", "local extra commit")
		hubTip := runGitCmd(t, env.trunkDir, "rev-parse", "HEAD")
		runGitCmd(t, env.trunkDir, "checkout", "main")

		// Verify fork tip is ancestor of hub tip.
		out := runGitCmd(t, env.trunkDir, "merge-base", "--is-ancestor", forkTip, hubTip)
		_ = out // no error means it is an ancestor

		seedPatch(t, env.db, "p1", "my-workspace", "feat", 1, PatchStatusActive)
		env.variables["PATCH_DIVERGENCE_POLICY"] = "replace"

		runner := newRealGitRunner(t, env.trunkDir)
		env.buildEcho(t, runner)

		rec := env.doSync(t)
		if rec.Code != http.StatusOK {
			t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}

		// Backup should hold the longer hub tip.
		backupSHA := runGitCmd(t, env.trunkDir, "rev-parse", "refs/hub/replaced/feat")
		if backupSHA != hubTip {
			t.Errorf("backup ref = %s; want %s (hub tip)", backupSHA, hubTip)
		}

		// Local ref should equal the fork tip.
		localTip := runGitCmd(t, env.trunkDir, "rev-parse", "refs/heads/feat")
		if localTip != forkTip {
			t.Errorf("refs/heads/feat = %s; want %s (fork tip)", localTip, forkTip)
		}

		// Parse response.
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		patchesSynced := resp["patches_synced"].([]any)
		elem := patchesSynced[0].(map[string]any)
		if elem["action"] != ActionReplaced {
			t.Errorf("action = %v; want %q", elem["action"], ActionReplaced)
		}
	})

	t.Run("report", func(t *testing.T) {
		env := setupOriginIntegrationEnv(t)

		// Create feat on the fork.
		env.addForkBranch(t, "feat", "fork feat content")

		// Fetch and create local branch at fork tip.
		env.fetchOriginInTrunk(t)
		runGitCmd(t, env.trunkDir, "branch", "feat", "refs/remotes/origin/feat")

		// Add extra commits locally.
		runGitCmd(t, env.trunkDir, "checkout", "feat")
		writeFileHelper(t, filepath.Join(env.trunkDir, "local-extra.txt"), "local extra")
		runGitCmd(t, env.trunkDir, "add", ".")
		runGitCmd(t, env.trunkDir, "commit", "-m", "local extra commit")
		hubTip := runGitCmd(t, env.trunkDir, "rev-parse", "HEAD")
		runGitCmd(t, env.trunkDir, "checkout", "main")

		seedPatch(t, env.db, "p1", "my-workspace", "feat", 1, PatchStatusActive)
		env.variables["PATCH_DIVERGENCE_POLICY"] = "report"

		runner := newRealGitRunner(t, env.trunkDir)
		env.buildEcho(t, runner)

		rec := env.doSync(t)
		if rec.Code != http.StatusOK {
			t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}

		// Local ref should be unchanged.
		localTip := runGitCmd(t, env.trunkDir, "rev-parse", "refs/heads/feat")
		if localTip != hubTip {
			t.Errorf("refs/heads/feat = %s; want %s (unchanged hub tip)", localTip, hubTip)
		}

		// Parse response.
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		patchesSynced := resp["patches_synced"].([]any)
		elem := patchesSynced[0].(map[string]any)
		if elem["state"] != StateDiverged {
			t.Errorf("state = %v; want %q", elem["state"], StateDiverged)
		}
	})
}

// ===========================================================================
// TS-20-42 (integration): Origin mode persists state, fork SHA and an
// RFC 3339 UTC timestamp per candidate and clears merged and deleted rows
//
// Verifies: 20-REQ-7.2
// ===========================================================================

func TestSyncOriginIntegration_PersistOriginState_TS2042(t *testing.T) {
	env := setupOriginIntegrationEnv(t)

	// Create feat-a on the fork (will be in_sync after create).
	forkTipA := env.addForkBranch(t, "feat-a", "feat-a content")

	// Create feat-b on the fork (will be diverged under report).
	env.addForkBranch(t, "feat-b", "feat-b content")

	// Fetch origin.
	env.fetchOriginInTrunk(t)

	// Create local feat-b at fork tip, then diverge.
	runGitCmd(t, env.trunkDir, "branch", "feat-b", "refs/remotes/origin/feat-b")
	runGitCmd(t, env.trunkDir, "checkout", "feat-b")
	writeFileHelper(t, filepath.Join(env.trunkDir, "local-b.txt"), "local divergent")
	runGitCmd(t, env.trunkDir, "add", ".")
	runGitCmd(t, env.trunkDir, "commit", "-m", "local divergent on feat-b")
	runGitCmd(t, env.trunkDir, "checkout", "main")

	// Add a divergent commit on the fork for feat-b.
	env.addForkCommit(t, "feat-b", "fork divergent on feat-b")
	env.fetchOriginInTrunk(t)

	// Create feat-c locally but not on the fork (missing_on_origin).
	runGitCmd(t, env.trunkDir, "checkout", "-b", "feat-c")
	writeFileHelper(t, filepath.Join(env.trunkDir, "feat-c.txt"), "feat-c content")
	runGitCmd(t, env.trunkDir, "add", ".")
	runGitCmd(t, env.trunkDir, "commit", "-m", "feat-c commit")
	runGitCmd(t, env.trunkDir, "checkout", "main")

	// Seed patches.
	seedPatch(t, env.db, "p1", "my-workspace", "feat-a", 1, PatchStatusActive)       // will be created → in_sync
	seedPatch(t, env.db, "p2", "my-workspace", "feat-b", 2, PatchStatusActive)       // diverged → report
	seedPatch(t, env.db, "p3", "my-workspace", "feat-c", 3, PatchStatusActive)       // missing_on_origin
	seedPatch(t, env.db, "p4", "my-workspace", "feat-merged", 4, PatchStatusMergedUpstream) // should be cleared
	seedPatch(t, env.db, "p5", "my-workspace", "feat-deleted", 5, PatchStatusDeleted) // should be cleared

	// Set stale origin state on merged and deleted rows.
	_, err := env.db.Exec(`UPDATE patches SET origin_sync_state = 'in_sync', origin_sha = 'stale', origin_synced_at = '2020-01-01T00:00:00Z' WHERE id IN ('p4', 'p5')`)
	if err != nil {
		t.Fatalf("set stale origin state: %v", err)
	}

	env.variables["PATCH_DIVERGENCE_POLICY"] = "report"

	runner := newRealGitRunner(t, env.trunkDir)
	env.buildEcho(t, runner)

	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Check p1 (created → in_sync).
	state1, sha1, syncedAt1 := env.queryPatchOriginState(t, "p1")
	if !state1.Valid || state1.String != StateInSync {
		t.Errorf("p1 state = %v; want %q", state1, StateInSync)
	}
	if !sha1.Valid || sha1.String != forkTipA {
		t.Errorf("p1 origin_sha = %v; want %s", sha1, forkTipA)
	}
	if !syncedAt1.Valid {
		t.Error("p1 origin_synced_at should be set")
	}
	// Verify timestamp is parseable as RFC 3339.
	_, parseErr := time.Parse(time.RFC3339Nano, syncedAt1.String)
	if parseErr != nil {
		_, parseErr = time.Parse(time.RFC3339, syncedAt1.String)
	}
	if parseErr != nil {
		t.Errorf("p1 origin_synced_at %q is not valid RFC 3339: %v", syncedAt1.String, parseErr)
	}

	// Check p2 (diverged).
	state2, sha2, _ := env.queryPatchOriginState(t, "p2")
	if !state2.Valid || state2.String != StateDiverged {
		t.Errorf("p2 state = %v; want %q", state2, StateDiverged)
	}
	if !sha2.Valid {
		t.Error("p2 origin_sha should be set for diverged")
	}

	// Check p3 (missing_on_origin).
	state3, sha3, _ := env.queryPatchOriginState(t, "p3")
	if !state3.Valid || state3.String != StateMissingOnOrigin {
		t.Errorf("p3 state = %v; want %q", state3, StateMissingOnOrigin)
	}
	if sha3.Valid {
		t.Errorf("p3 origin_sha should be NULL for missing_on_origin, got %q", sha3.String)
	}

	// Check p4 (merged_upstream → cleared).
	state4, sha4, syncedAt4 := env.queryPatchOriginState(t, "p4")
	if state4.Valid {
		t.Errorf("p4 origin_sync_state should be NULL, got %q", state4.String)
	}
	if sha4.Valid {
		t.Errorf("p4 origin_sha should be NULL, got %q", sha4.String)
	}
	if syncedAt4.Valid {
		t.Errorf("p4 origin_synced_at should be NULL, got %q", syncedAt4.String)
	}

	// Check p5 (deleted → cleared).
	state5, sha5, syncedAt5 := env.queryPatchOriginState(t, "p5")
	if state5.Valid {
		t.Errorf("p5 origin_sync_state should be NULL, got %q", state5.String)
	}
	if sha5.Valid {
		t.Errorf("p5 origin_sha should be NULL, got %q", sha5.String)
	}
	if syncedAt5.Valid {
		t.Errorf("p5 origin_synced_at should be NULL, got %q", syncedAt5.String)
	}
}

// ===========================================================================
// Finding 5 of issue #42: a patch that merge detection marks merged_upstream
// in the same sync keeps no origin columns
//
// Verifies: 20-REQ-7.2
// ===========================================================================

func TestSyncOriginIntegration_NewlyMergedPatchClearsOriginState_Finding5(t *testing.T) {
	env := setupOriginIntegrationEnv(t)
	env.variables["PATCH_DIVERGENCE_POLICY"] = "report"

	// Both patches exist on the fork and locally with diverging content.
	env.addForkBranch(t, "merged-feat", "fork merged-feat")
	env.addForkBranch(t, "stay-feat", "fork stay-feat")
	env.fetchOriginInTrunk(t)

	for _, b := range []string{"merged-feat", "stay-feat"} {
		runGitCmd(t, env.trunkDir, "checkout", "-b", b, "main")
		writeFileHelper(t, filepath.Join(env.trunkDir, b+"-local.txt"), "local "+b)
		runGitCmd(t, env.trunkDir, "add", ".")
		runGitCmd(t, env.trunkDir, "commit", "-m", "local "+b)
		runGitCmd(t, env.trunkDir, "checkout", "main")
	}

	// Upstream advances past merged-feat's local tip only, so merge detection
	// finds merged-feat (ancestry) but not stay-feat.
	runGitCmd(t, env.trunkDir, "checkout", "-b", "temp-upstream", "merged-feat")
	writeFileHelper(t, filepath.Join(env.trunkDir, "upstream-extra.txt"), "upstream extra")
	runGitCmd(t, env.trunkDir, "add", ".")
	runGitCmd(t, env.trunkDir, "commit", "-m", "upstream extra")
	newUpstream := runGitCmd(t, env.trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, env.trunkDir, "checkout", "main")
	runGitCmd(t, env.trunkDir, "branch", "-D", "temp-upstream")
	runGitCmd(t, env.trunkDir, "update-ref", "refs/remotes/upstream/HEAD", newUpstream)

	seedPatch(t, env.db, "p-merged", "my-workspace", "merged-feat", 1, PatchStatusActive)
	seedPatch(t, env.db, "p-stay", "my-workspace", "stay-feat", 2, PatchStatusActive)

	env.buildEcho(t, newRealGitRunner(t, env.trunkDir))
	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want 200; body = %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	merged, _ := resp["patches_merged"].([]any)
	if len(merged) != 1 || merged[0] != "merged-feat" {
		t.Fatalf("patches_merged = %v; want [merged-feat]", resp["patches_merged"])
	}

	// The merged patch ends with all three origin columns NULL.
	var status string
	if err := env.db.QueryRow(`SELECT status FROM patches WHERE id = 'p-merged'`).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != PatchStatusMergedUpstream {
		t.Fatalf("p-merged status = %q; want %q", status, PatchStatusMergedUpstream)
	}
	state, sha, syncedAt := env.queryPatchOriginState(t, "p-merged")
	if state.Valid || sha.Valid || syncedAt.Valid {
		t.Errorf("p-merged origin columns = %v / %v / %v; want all NULL", state, sha, syncedAt)
	}

	// A patch that stayed active keeps the state the refresh recorded.
	state, sha, syncedAt = env.queryPatchOriginState(t, "p-stay")
	wantSHA := refSHA(t, env.trunkDir, "refs/remotes/origin/stay-feat")
	if !state.Valid || state.String != StateDiverged || !sha.Valid || sha.String != wantSHA || !syncedAt.Valid {
		t.Errorf("p-stay origin columns = %v / %v / %v; want diverged / %s / set", state, sha, syncedAt, wantSHA)
	}

	// patch-status counts only the patch that is still diverged.
	api := env.echo.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())
	RegisterPatchStatusRoutes(api, PatchStatusAPIConfig{
		DB:            env.db,
		Queue:         env.queue,
		WorkspaceRoot: env.wsRoot,
		PatchStore:    NewSQLPatchStore(env.db),
	})
	authJSON, _ := json.Marshal(rebuildUserAuth("alice"))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/my-workspace/patch-status", nil)
	req.Header.Set("X-Test-Auth", string(authJSON))
	statusRec := httptest.NewRecorder()
	env.echo.ServeHTTP(statusRec, req)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("patch-status = %d; want 200; body = %s", statusRec.Code, statusRec.Body.String())
	}
	var dash map[string]any
	if err := json.Unmarshal(statusRec.Body.Bytes(), &dash); err != nil {
		t.Fatalf("decode patch-status: %v", err)
	}
	summary := dash["summary"].(map[string]any)
	if summary["patches_diverged"] != float64(1) {
		t.Errorf("summary.patches_diverged = %v; want 1 (only stay-feat)", summary["patches_diverged"])
	}
	if summary["merged_upstream"] != float64(1) {
		t.Errorf("summary.merged_upstream = %v; want 1", summary["merged_upstream"])
	}
}

// Suppress unused import warnings.
var (
	_ = os.Stat
	_ = time.Now
	_ = apikit.NowUTC
)
