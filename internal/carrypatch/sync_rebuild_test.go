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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// rebuildSyncTestEnv: test environment for rebuild trigger and sync
// completion tests (TS-20-27 through TS-20-43).
// ===========================================================================

type rebuildSyncTestEnv struct {
	echo          *echo.Echo
	db            *sql.DB
	queue         *jobqueue.Queue
	workspaceRoot string
	gitRunner     *mockGitRunner
	patchStore    *mockPatchStore

	mu        sync.Mutex
	variables map[string]string

	// Counters and controls for origin fetch.
	originFetchCount int
	fetchOriginFunc  func(ctx context.Context, repoPath string, auth transport.AuthMethod) error
}

func newRebuildSyncTestEnv(t *testing.T) *rebuildSyncTestEnv {
	t.Helper()

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

	workspaceRoot := t.TempDir()
	mock := newMockGitRunner()
	patches := newMockPatchStore(nil)

	env := &rebuildSyncTestEnv{
		db:            db,
		queue:         q,
		workspaceRoot: workspaceRoot,
		gitRunner:     mock,
		patchStore:    patches,
		variables:     make(map[string]string),
	}

	getVar := func(scope, slug, key string) (string, error) {
		env.mu.Lock()
		defer env.mu.Unlock()
		if v, ok := env.variables[key]; ok {
			return v, nil
		}
		return "", fmt.Errorf("variable not set")
	}

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())

	syncCfg := SyncAPIConfig{
		DB:            db,
		Queue:         q,
		WorkspaceRoot: workspaceRoot,
		NewGitRunner: func(_ string) (GitRunner, error) {
			return mock, nil
		},
		Fetch: func(_ context.Context, _ string, _ transport.AuthMethod) error {
			return nil
		},
		ResolveAuth: func(_ string) (transport.AuthMethod, error) {
			return nil, nil
		},
		GetVariable: getVar,
		PatchStore:  patches,
		FetchOrigin: func(ctx context.Context, repoPath string, auth transport.AuthMethod) error {
			env.mu.Lock()
			env.originFetchCount++
			fn := env.fetchOriginFunc
			env.mu.Unlock()
			if fn != nil {
				return fn(ctx, repoPath, auth)
			}
			return nil
		},
		ResolveOriginAuth: func(_ string) (transport.AuthMethod, error) {
			return nil, nil
		},
	}
	RegisterSyncRoutes(api, syncCfg)

	rebuildCfg := RebuildAPIConfig{
		DB:          db,
		Queue:       q,
		GetVariable: getVar,
	}
	RegisterRebuildRoutes(api, rebuildCfg)

	env.echo = e
	return env
}

func (env *rebuildSyncTestEnv) setVar(key, value string) {
	env.mu.Lock()
	defer env.mu.Unlock()
	env.variables[key] = value
}

func (env *rebuildSyncTestEnv) doSync(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	return env.doSyncAs(t, "alice")
}

func (env *rebuildSyncTestEnv) doSyncAs(t *testing.T, userID string) *httptest.ResponseRecorder {
	t.Helper()
	auth := rebuildUserAuth(userID)
	authJSON, _ := json.Marshal(auth)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/my-workspace/sync", nil)
	req.Header.Set("X-Test-Auth", string(authJSON))
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	return rec
}

func (env *rebuildSyncTestEnv) rebuildJobCount(t *testing.T) int {
	t.Helper()
	var count int
	err := env.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='rebuild' AND key='my-workspace'`).Scan(&count)
	if err != nil {
		t.Fatalf("query job count: %v", err)
	}
	return count
}

// ===========================================================================
// TS-20-27 (integration): A moved disabled patch is refreshed and reported
// but does not trigger a rebuild; active and conflict ones do.
//
// Verifies: 20-REQ-4.4
// ===========================================================================

func TestSyncRebuild_DisabledPatchNoRebuild_ActiveConflictDo_TS2027(t *testing.T) {
	// For each status, set up a fork with a fast-forwardable branch,
	// run an origin-mode sync, and check whether a rebuild was triggered.
	for _, st := range []string{PatchStatusDisabled, PatchStatusConflict, PatchStatusActive} {
		t.Run("status_"+st, func(t *testing.T) {
			// Set up a real fork and trunk.
			forkDir := t.TempDir()
			runGitCmd(t, "", "init", "--bare", forkDir)

			workDir := t.TempDir()
			runGitCmd(t, "", "clone", forkDir, workDir)
			configGitUserCmd(t, workDir)

			writeFileHelper(t, filepath.Join(workDir, "file.txt"), "hello")
			runGitCmd(t, workDir, "add", ".")
			runGitCmd(t, workDir, "commit", "-m", "initial")

			// Create feat branch with two commits.
			runGitCmd(t, workDir, "checkout", "-b", "feat")
			writeFileHelper(t, filepath.Join(workDir, "feat.txt"), "feat1")
			runGitCmd(t, workDir, "add", ".")
			runGitCmd(t, workDir, "commit", "-m", "feat commit 1")
			featBase := runGitCmd(t, workDir, "rev-parse", "HEAD")

			writeFileHelper(t, filepath.Join(workDir, "feat2.txt"), "feat2")
			runGitCmd(t, workDir, "add", ".")
			runGitCmd(t, workDir, "commit", "-m", "feat commit 2")

			runGitCmd(t, workDir, "push", "origin", "feat")
			runGitCmd(t, workDir, "push", "origin", "main")

			// Create trunk.
			trunkDir := t.TempDir()
			runGitCmd(t, "", "init", "-b", "main", trunkDir)
			configGitUserCmd(t, trunkDir)
			writeFileHelper(t, filepath.Join(trunkDir, "file.txt"), "hello")
			runGitCmd(t, trunkDir, "add", ".")
			runGitCmd(t, trunkDir, "commit", "-m", "initial")
			runGitCmd(t, trunkDir, "remote", "add", "origin", forkDir)
			runGitCmd(t, trunkDir, "fetch", "origin")

			// Set local feat at the base (ancestor of fork tip) → fast-forwardable.
			runGitCmd(t, trunkDir, "branch", "feat", featBase)

			// Also set up upstream tracking ref so resolveUpstreamBase works.
			upstreamSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
			runGitCmd(t, trunkDir, "update-ref", "refs/remotes/upstream/HEAD", upstreamSHA)

			// Create the test env with a real git runner for this trunk.
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

			// Seed workspace with upstream_head_sha = upstreamSHA (unchanged).
			seedWorkspaceCarryPatch(t, db, "my-workspace", "alice",
				"https://github.com/example/upstream",
				upstreamSHA,
				"integration", "")

			seedPatch(t, db, "p1", "my-workspace", "feat", 1, st)

			runner := newRealGitRunner(t, trunkDir)
			patchStore := NewSQLPatchStore(db)

			getVar := func(scope, slug, key string) (string, error) {
				if key == "PATCH_BRANCH_SOURCE" {
					return "origin", nil
				}
				return "", fmt.Errorf("not set")
			}

			wsRoot := filepath.Dir(filepath.Dir(trunkDir))

			e := echo.New()
			api := e.Group("/api/v1")
			api.Use(rebuildTestAuthMiddleware())

			syncCfg := SyncAPIConfig{
				DB:            db,
				Queue:         q,
				WorkspaceRoot: wsRoot,
				NewGitRunner: func(_ string) (GitRunner, error) {
					return runner, nil
				},
				Fetch:       func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
				ResolveAuth: func(_ string) (transport.AuthMethod, error) { return nil, nil },
				GetVariable: getVar,
				PatchStore:  patchStore,
				FetchOrigin: func(_ context.Context, _ string, _ transport.AuthMethod) error {
					return nil
				},
				ResolveOriginAuth: func(_ string) (transport.AuthMethod, error) { return nil, nil },
			}
			RegisterSyncRoutes(api, syncCfg)

			auth := rebuildUserAuth("alice")
			authJSON, _ := json.Marshal(auth)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/my-workspace/sync", nil)
			req.Header.Set("X-Test-Auth", string(authJSON))
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
			}

			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			rebuildTriggered, _ := resp["rebuild_triggered"].(bool)

			if st == PatchStatusDisabled {
				if rebuildTriggered {
					t.Error("expected rebuild_triggered=false for disabled patch")
				}
			} else {
				if !rebuildTriggered {
					t.Errorf("expected rebuild_triggered=true for %s patch", st)
				}
			}

			// Verify the branch was moved (fast-forwarded) regardless of status.
			forkTip := runGitCmd(t, trunkDir, "rev-parse", "refs/remotes/origin/feat")
			localTip := runGitCmd(t, trunkDir, "rev-parse", "refs/heads/feat")
			if localTip != forkTip {
				t.Errorf("expected local tip to match fork tip after fast-forward, got local=%s fork=%s", localTip, forkTip)
			}
		})
	}
}

// ===========================================================================
// TS-20-29 (integration): A patch-only change with upstream unchanged reaches
// merge detection on the refreshed tips.
//
// Verifies: 20-REQ-5.1, 20-REQ-5.2
// ===========================================================================

func TestSyncRebuild_PatchOnlyChangeReachesMergeDetection_TS2029(t *testing.T) {
	// Set up a fork and trunk where:
	// - upstream has not advanced (upstream_head_sha == current upstream HEAD)
	// - an active patch fast-forwards to a commit that is an ancestor of upstream HEAD
	//   (so merge detection marks it merged_upstream)

	forkDir := t.TempDir()
	runGitCmd(t, "", "init", "--bare", forkDir)

	workDir := t.TempDir()
	runGitCmd(t, "", "clone", forkDir, workDir)
	configGitUserCmd(t, workDir)

	writeFileHelper(t, filepath.Join(workDir, "file.txt"), "hello")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "initial")
	baseSHA := runGitCmd(t, workDir, "rev-parse", "HEAD")

	// Create feat branch at the base commit.
	runGitCmd(t, workDir, "checkout", "-b", "feat")
	// feat is at baseSHA, which IS an ancestor of upstream HEAD.
	// Push feat to fork.
	runGitCmd(t, workDir, "push", "origin", "feat")

	// Now advance main (upstream) beyond baseSHA.
	runGitCmd(t, workDir, "checkout", "main")
	writeFileHelper(t, filepath.Join(workDir, "upstream.txt"), "upstream change")
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "upstream advance")
	upstreamSHA := runGitCmd(t, workDir, "rev-parse", "HEAD")

	// Now update feat on the fork to a commit that is still an ancestor of upstream.
	// We'll make feat point to baseSHA (which is an ancestor of upstreamSHA).
	// Actually, feat is already at baseSHA. Let's make the local feat at an
	// older commit and the fork feat at baseSHA. baseSHA is ancestor of upstreamSHA.

	runGitCmd(t, workDir, "push", "origin", "main")

	// Create trunk.
	trunkDir := t.TempDir()
	runGitCmd(t, "", "init", "-b", "main", trunkDir)
	configGitUserCmd(t, trunkDir)
	writeFileHelper(t, filepath.Join(trunkDir, "file.txt"), "hello")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "initial")
	localBaseSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")

	runGitCmd(t, trunkDir, "remote", "add", "origin", forkDir)
	runGitCmd(t, trunkDir, "fetch", "origin")

	// Set up upstream tracking ref at upstreamSHA.
	// We need to create a commit in trunk that matches upstreamSHA.
	// Actually, we fetched origin which has main at upstreamSHA.
	// Let's use the fetched origin/main as the upstream HEAD.
	fetchedUpstreamSHA := runGitCmd(t, trunkDir, "rev-parse", "refs/remotes/origin/main")
	runGitCmd(t, trunkDir, "update-ref", "refs/remotes/upstream/HEAD", fetchedUpstreamSHA)

	// Create local feat at an older commit (localBaseSHA) so it can be fast-forwarded.
	// The fork's feat is at baseSHA (from the fork's initial commit).
	// After fast-forward, local feat will be at baseSHA.
	// baseSHA is an ancestor of upstreamSHA, so merge detection should mark it merged.

	// But wait - baseSHA in the fork is a different object than localBaseSHA in trunk.
	// We need to use the fetched refs. The fork's feat is at the fork's baseSHA.
	forkFeatSHA := runGitCmd(t, trunkDir, "rev-parse", "refs/remotes/origin/feat")

	// Create local feat at a commit that is an ancestor of forkFeatSHA.
	// Since forkFeatSHA is the initial commit in the fork, there's no ancestor.
	// Let's restructure: make feat have 2 commits, local at first, fork at second.

	// Actually, let me simplify. The key test is:
	// 1. upstream unchanged
	// 2. patch fast-forwards
	// 3. after fast-forward, the patch tip is an ancestor of upstream HEAD
	// 4. merge detection runs and marks it merged_upstream

	// Let's just use the mock approach for merge detection but real git for the refresh.
	// Actually the test says "integration" so let's use real git throughout.

	// Restructure: feat branch has 2 commits. Local at commit 1, fork at commit 2.
	// Both commits are ancestors of upstream HEAD.

	// Let me start over with a cleaner setup.
	_ = baseSHA
	_ = upstreamSHA
	_ = localBaseSHA
	_ = forkFeatSHA

	// Clean approach: use a single repo setup.
	forkDir2 := t.TempDir()
	runGitCmd(t, "", "init", "--bare", forkDir2)

	workDir2 := t.TempDir()
	runGitCmd(t, "", "clone", forkDir2, workDir2)
	configGitUserCmd(t, workDir2)

	// Base commit.
	writeFileHelper(t, filepath.Join(workDir2, "file.txt"), "hello")
	runGitCmd(t, workDir2, "add", ".")
	runGitCmd(t, workDir2, "commit", "-m", "initial")

	// Create feat branch.
	runGitCmd(t, workDir2, "checkout", "-b", "feat")
	writeFileHelper(t, filepath.Join(workDir2, "feat.txt"), "feat")
	runGitCmd(t, workDir2, "add", ".")
	runGitCmd(t, workDir2, "commit", "-m", "feat commit")
	featSHA := runGitCmd(t, workDir2, "rev-parse", "HEAD")

	// Push feat to fork.
	runGitCmd(t, workDir2, "push", "origin", "feat")

	// Go back to main and merge feat (so feat is ancestor of main).
	runGitCmd(t, workDir2, "checkout", "main")
	runGitCmd(t, workDir2, "merge", "feat")
	upstreamSHA2 := runGitCmd(t, workDir2, "rev-parse", "HEAD")
	runGitCmd(t, workDir2, "push", "origin", "main")

	// Create trunk.
	trunkDir2 := t.TempDir()
	runGitCmd(t, "", "clone", forkDir2, trunkDir2)
	configGitUserCmd(t, trunkDir2)

	// Set up upstream tracking ref.
	runGitCmd(t, trunkDir2, "update-ref", "refs/remotes/upstream/HEAD", upstreamSHA2)

	// Delete local feat (we'll let the refresh create it).
	// Actually, we need local feat to exist at an older commit for fast-forward.
	// The feat branch on the fork is at featSHA. We need local feat at an ancestor.

	// Get the parent of featSHA.
	featParent := runGitCmd(t, trunkDir2, "rev-parse", featSHA+"^")

	// Delete the local feat branch (it was created by clone).
	// First check if it exists; clone may not have created it as a local branch.
	_, delErr := runGitCmdErr(t, trunkDir2, "branch", "-D", "feat")
	_ = delErr // ignore if not found
	// Create it at the parent.
	runGitCmd(t, trunkDir2, "branch", "feat", featParent)

	// Now: local feat is at featParent, fork feat is at featSHA.
	// featSHA is an ancestor of upstreamSHA2 (because we merged feat into main).
	// upstream_head_sha = upstreamSHA2 (unchanged).

	// Set up the test env.
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

	// Seed workspace with upstream_head_sha = upstreamSHA2 (unchanged).
	seedWorkspaceCarryPatch(t, db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		upstreamSHA2,
		"integration", "")

	seedPatch(t, db, "p1", "my-workspace", "feat", 1, PatchStatusActive)

	runner := newRealGitRunner(t, trunkDir2)
	patchStore := NewSQLPatchStore(db)

	getVar := func(scope, slug, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		return "", fmt.Errorf("not set")
	}

	wsRoot := filepath.Dir(filepath.Dir(trunkDir2))

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())

	syncCfg := SyncAPIConfig{
		DB:            db,
		Queue:         q,
		WorkspaceRoot: wsRoot,
		NewGitRunner: func(_ string) (GitRunner, error) {
			return runner, nil
		},
		Fetch:       func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveAuth: func(_ string) (transport.AuthMethod, error) { return nil, nil },
		GetVariable: getVar,
		PatchStore:  patchStore,
		FetchOrigin: func(_ context.Context, _ string, _ transport.AuthMethod) error {
			return nil
		},
		ResolveOriginAuth: func(_ string) (transport.AuthMethod, error) { return nil, nil },
	}
	RegisterSyncRoutes(api, syncCfg)

	auth := rebuildUserAuth("alice")
	authJSON, _ := json.Marshal(auth)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/my-workspace/sync", nil)
	req.Header.Set("X-Test-Auth", string(authJSON))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Merge detection should have run and found feat merged upstream.
	patchesMerged, _ := resp["patches_merged"].([]any)
	found := false
	for _, pm := range patchesMerged {
		if pm == "feat" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'feat' in patches_merged, got %v", resp["patches_merged"])
	}

	// Now do a second sync where nothing changed.
	// The patch is now merged_upstream, so it won't be a candidate.
	// Upstream hasn't changed. No merge detection should run.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/my-workspace/sync", nil)
	req2.Header.Set("X-Test-Auth", string(authJSON))
	e.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("second sync status = %d; want %d; body = %s", rec2.Code, http.StatusOK, rec2.Body.String())
	}

	var resp2 map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode second response: %v", err)
	}

	patchesMerged2, _ := resp2["patches_merged"].([]any)
	if len(patchesMerged2) != 0 {
		t.Errorf("second sync: expected empty patches_merged, got %v", patchesMerged2)
	}

	_ = featSHA
}

// ===========================================================================
// TS-20-30 (unit): With the hub source merge detection runs exactly when
// upstream advanced.
//
// Verifies: 20-REQ-5.2
// ===========================================================================

func TestSyncRebuild_HubModeMergeDetection_TS2030(t *testing.T) {
	// Use a mock env where we can count IsAncestor calls for merge detection.
	env := newRebuildSyncTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration", "")

	seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
	}

	// Hub mode (default, no PATCH_BRANCH_SOURCE set).
	// Count IsAncestor calls for merge detection.
	isAncestorCount := 0
	env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
		isAncestorCount++
		return false, nil
	}

	// Sync 1: upstream unchanged → no merge detection.
	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		return "aaaa000000000000000000000000000000000001", nil
	}

	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync 1 status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	if isAncestorCount != 0 {
		t.Errorf("sync 1 (unchanged): expected 0 IsAncestor calls, got %d", isAncestorCount)
	}

	// Sync 2: upstream advanced → merge detection runs.
	isAncestorCount = 0
	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		return "bbbb000000000000000000000000000000000002", nil
	}

	rec2 := env.doSync(t)
	if rec2.Code != http.StatusOK {
		t.Fatalf("sync 2 status = %d; want %d; body = %s", rec2.Code, http.StatusOK, rec2.Body.String())
	}

	// IsAncestor should have been called at least once for merge detection.
	// It's called once for force-push detection (storedSHA vs new) and once
	// for the patch merge check.
	if isAncestorCount == 0 {
		t.Error("sync 2 (advanced): expected IsAncestor calls for merge detection, got 0")
	}
}

// ===========================================================================
// TS-20-31 (integration): A patch-only change enqueues one deduplicated
// rebuild job with the standard key, group and submitter.
//
// Verifies: 20-REQ-5.3
// ===========================================================================

func TestSyncRebuild_PatchOnlyChangeEnqueuesRebuild_TS2031(t *testing.T) {
	// Scenario 1: patch-only change enqueues a rebuild.
	t.Run("patch_change_enqueues", func(t *testing.T) {
		forkDir := t.TempDir()
		runGitCmd(t, "", "init", "--bare", forkDir)

		workDir := t.TempDir()
		runGitCmd(t, "", "clone", forkDir, workDir)
		configGitUserCmd(t, workDir)

		writeFileHelper(t, filepath.Join(workDir, "file.txt"), "hello")
		runGitCmd(t, workDir, "add", ".")
		runGitCmd(t, workDir, "commit", "-m", "initial")

		runGitCmd(t, workDir, "checkout", "-b", "feat")
		writeFileHelper(t, filepath.Join(workDir, "feat1.txt"), "feat1")
		runGitCmd(t, workDir, "add", ".")
		runGitCmd(t, workDir, "commit", "-m", "feat commit 1")
		featBase := runGitCmd(t, workDir, "rev-parse", "HEAD")

		writeFileHelper(t, filepath.Join(workDir, "feat2.txt"), "feat2")
		runGitCmd(t, workDir, "add", ".")
		runGitCmd(t, workDir, "commit", "-m", "feat commit 2")

		runGitCmd(t, workDir, "push", "origin", "feat")
		runGitCmd(t, workDir, "checkout", "main")
		runGitCmd(t, workDir, "push", "origin", "main")

		trunkDir := t.TempDir()
		runGitCmd(t, "", "clone", forkDir, trunkDir)
		configGitUserCmd(t, trunkDir)

		upstreamSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
		runGitCmd(t, trunkDir, "update-ref", "refs/remotes/upstream/HEAD", upstreamSHA)

		// Set local feat at ancestor.
		_, _ = runGitCmdErr(t, trunkDir, "branch", "-D", "feat")
		runGitCmd(t, trunkDir, "branch", "feat", featBase)

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

		seedWorkspaceCarryPatch(t, db, "my-workspace", "alice",
			"https://github.com/example/upstream",
			upstreamSHA,
			"integration", "")

		seedPatch(t, db, "p1", "my-workspace", "feat", 1, PatchStatusActive)

		runner := newRealGitRunner(t, trunkDir)
		patchStore := NewSQLPatchStore(db)

		getVar := func(scope, slug, key string) (string, error) {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
			return "", fmt.Errorf("not set")
		}

		wsRoot := filepath.Dir(filepath.Dir(trunkDir))

		e := echo.New()
		api := e.Group("/api/v1")
		api.Use(rebuildTestAuthMiddleware())

		syncCfg := SyncAPIConfig{
			DB:            db,
			Queue:         q,
			WorkspaceRoot: wsRoot,
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

		auth := rebuildUserAuth("alice")
		authJSON, _ := json.Marshal(auth)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/my-workspace/sync", nil)
		req.Header.Set("X-Test-Auth", string(authJSON))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var resp CarryPatchSyncResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}

		if !resp.RebuildTriggered {
			t.Error("expected rebuild_triggered=true for patch-only change")
		}
		if resp.RebuildJobID == nil || *resp.RebuildJobID == "" {
			t.Error("expected non-empty rebuild_job_id")
		}

		// Verify job properties.
		var jobCount int
		db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='rebuild' AND key='my-workspace'`).Scan(&jobCount)
		if jobCount != 1 {
			t.Errorf("expected 1 rebuild job, got %d", jobCount)
		}

		// Check job details.
		var jobKey, jobGroup, jobSubmittedBy string
		db.QueryRow(`SELECT key, group_key, submitted_by FROM jobs WHERE type='rebuild' AND key='my-workspace'`).Scan(&jobKey, &jobGroup, &jobSubmittedBy)
		if jobKey != "my-workspace" {
			t.Errorf("job key = %q; want %q", jobKey, "my-workspace")
		}
		if jobGroup != "my-workspace:integration" {
			t.Errorf("job group = %q; want %q", jobGroup, "my-workspace:integration")
		}
		if jobSubmittedBy != "alice" {
			t.Errorf("job submitted_by = %q; want %q", jobSubmittedBy, "alice")
		}
	})

	// Scenario 2: deduplicated when a rebuild job is already queued.
	t.Run("deduplicated", func(t *testing.T) {
		env := newRebuildSyncTestEnv(t)

		seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
			"https://github.com/example/upstream",
			"aaaa000000000000000000000000000000000001",
			"integration", "")

		seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
		env.patchStore.Patches = []Patch{
			{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		}

		// Upstream advances.
		env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
			return "bbbb000000000000000000000000000000000002", nil
		}
		env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
			return false, nil
		}

		// First sync enqueues.
		rec1 := env.doSync(t)
		if rec1.Code != http.StatusOK {
			t.Fatalf("sync 1 status = %d; body = %s", rec1.Code, rec1.Body.String())
		}
		var resp1 CarryPatchSyncResponse
		json.NewDecoder(rec1.Body).Decode(&resp1)
		if !resp1.RebuildTriggered {
			t.Error("first sync: expected rebuild_triggered=true")
		}

		// Second sync: deduplicated.
		rec2 := env.doSync(t)
		if rec2.Code != http.StatusOK {
			t.Fatalf("sync 2 status = %d; body = %s", rec2.Code, rec2.Body.String())
		}
		var resp2 CarryPatchSyncResponse
		json.NewDecoder(rec2.Body).Decode(&resp2)
		if resp2.RebuildTriggered {
			t.Error("second sync: expected rebuild_triggered=false (deduplicated)")
		}
	})
}

// ===========================================================================
// TS-20-32 (unit): AUTO_REBUILD_AFTER_SYNC=false suppresses the rebuild for
// a patch change and for an upstream advance.
//
// Verifies: 20-REQ-5.4
// ===========================================================================

func TestSyncRebuild_AutoRebuildFalseSuppresses_TS2032(t *testing.T) {
	t.Run("upstream_advance", func(t *testing.T) {
		env := newRebuildSyncTestEnv(t)
		env.setVar("AUTO_REBUILD_AFTER_SYNC", "false")

		seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
			"https://github.com/example/upstream",
			"aaaa000000000000000000000000000000000001",
			"integration", "")

		seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
		env.patchStore.Patches = []Patch{
			{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		}

		// Upstream advances.
		env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
			return "bbbb000000000000000000000000000000000002", nil
		}
		env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
			return false, nil
		}

		rec := env.doSync(t)
		if rec.Code != http.StatusOK {
			t.Fatalf("sync status = %d; body = %s", rec.Code, rec.Body.String())
		}

		var resp CarryPatchSyncResponse
		json.NewDecoder(rec.Body).Decode(&resp)
		if resp.RebuildTriggered {
			t.Error("expected rebuild_triggered=false when AUTO_REBUILD_AFTER_SYNC=false")
		}

		if env.rebuildJobCount(t) != 0 {
			t.Error("expected 0 rebuild jobs")
		}
	})

	t.Run("patch_only_change_origin_mode", func(t *testing.T) {
		env := newRebuildSyncTestEnv(t)
		env.setVar("AUTO_REBUILD_AFTER_SYNC", "false")
		env.setVar("PATCH_BRANCH_SOURCE", "origin")

		seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
			"https://github.com/example/upstream",
			"aaaa000000000000000000000000000000000001",
			"integration", "")

		seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
		env.patchStore.Patches = []Patch{
			{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		}

		// Upstream unchanged.
		env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
			// For rev-parse of upstream HEAD.
			if len(args) >= 2 && args[0] == "rev-parse" {
				return "aaaa000000000000000000000000000000000001", nil
			}
			return "", nil
		}
		env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
			return false, nil
		}

		rec := env.doSync(t)
		if rec.Code != http.StatusOK {
			t.Fatalf("sync status = %d; body = %s", rec.Code, rec.Body.String())
		}

		var resp CarryPatchSyncResponse
		json.NewDecoder(rec.Body).Decode(&resp)
		if resp.RebuildTriggered {
			t.Error("expected rebuild_triggered=false when AUTO_REBUILD_AFTER_SYNC=false")
		}

		if env.rebuildJobCount(t) != 0 {
			t.Error("expected 0 rebuild jobs")
		}
	})
}

// ===========================================================================
// TS-20-33 (unit): last_sync_at and updated_at are written on every completed
// sync in both modes while upstream_head_sha is written only on advance.
//
// Verifies: 20-REQ-5.5
// ===========================================================================

func TestSyncRebuild_LastSyncAtAlwaysWritten_TS2033(t *testing.T) {
	for _, mode := range []string{"hub", "origin"} {
		t.Run("mode_"+mode, func(t *testing.T) {
			env := newRebuildSyncTestEnv(t)

			if mode == "origin" {
				env.setVar("PATCH_BRANCH_SOURCE", "origin")
			}

			oldSyncAt := "2020-01-01T00:00:00Z"
			oldUpdatedAt := "2020-01-01T00:00:00Z"
			storedSHA := "aaaa000000000000000000000000000000000001"

			// Seed workspace with old timestamps.
			now := time.Now().UTC().Format(time.RFC3339Nano)
			_, err := env.db.Exec(
				`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, workspace_mode, integration_branch, upstream_url, upstream_head_sha, last_sync_at, created_at, updated_at)
				 VALUES (?, ?, ?, 'active', 'ready', 'carry_patch', ?, ?, ?, ?, ?, ?)`,
				"my-workspace", "https://github.com/example/repo", "alice",
				"integration", "https://github.com/example/upstream",
				storedSHA, oldSyncAt, now, oldUpdatedAt,
			)
			if err != nil {
				t.Fatalf("seed workspace: %v", err)
			}

			// Upstream unchanged.
			env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
				return storedSHA, nil
			}

			rec := env.doSync(t)
			if rec.Code != http.StatusOK {
				t.Fatalf("sync status = %d; body = %s", rec.Code, rec.Body.String())
			}

			// Check last_sync_at was updated.
			var newSyncAt, newUpdatedAt, newSHA sql.NullString
			env.db.QueryRow(`SELECT last_sync_at, updated_at, upstream_head_sha FROM workspaces WHERE slug = ?`, "my-workspace").Scan(&newSyncAt, &newUpdatedAt, &newSHA)

			if !newSyncAt.Valid || newSyncAt.String == oldSyncAt {
				t.Errorf("last_sync_at should have been updated, got %v", newSyncAt)
			}
			if !newUpdatedAt.Valid || newUpdatedAt.String == oldUpdatedAt {
				t.Errorf("updated_at should have been updated, got %v", newUpdatedAt)
			}
			// upstream_head_sha should be unchanged.
			if newSHA.String != storedSHA {
				t.Errorf("upstream_head_sha should be unchanged, got %s (want %s)", newSHA.String, storedSHA)
			}
		})
	}

	// Test that upstream_head_sha IS updated when upstream advances.
	t.Run("upstream_advance_updates_sha", func(t *testing.T) {
		env := newRebuildSyncTestEnv(t)

		storedSHA := "aaaa000000000000000000000000000000000001"
		newSHA := "bbbb000000000000000000000000000000000002"

		seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
			"https://github.com/example/upstream",
			storedSHA,
			"integration", "")

		seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
		env.patchStore.Patches = []Patch{
			{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		}

		env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
			return newSHA, nil
		}
		env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
			return false, nil
		}

		rec := env.doSync(t)
		if rec.Code != http.StatusOK {
			t.Fatalf("sync status = %d; body = %s", rec.Code, rec.Body.String())
		}

		var dbSHA sql.NullString
		env.db.QueryRow(`SELECT upstream_head_sha FROM workspaces WHERE slug = ?`, "my-workspace").Scan(&dbSHA)
		if dbSHA.String != newSHA {
			t.Errorf("upstream_head_sha = %s; want %s", dbSHA.String, newSHA)
		}
	})
}

// ===========================================================================
// TS-20-34 (unit): A sync where nothing changed returns empty patches_merged
// and rebuild_triggered false.
//
// Verifies: 20-REQ-5.6
// ===========================================================================

func TestSyncRebuild_NoChangeReturnsEmpty_TS2034(t *testing.T) {
	for _, mode := range []string{"hub", "origin"} {
		t.Run("mode_"+mode, func(t *testing.T) {
			env := newRebuildSyncTestEnv(t)

			if mode == "origin" {
				env.setVar("PATCH_BRANCH_SOURCE", "origin")
			}

			storedSHA := "aaaa000000000000000000000000000000000001"
			seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
				"https://github.com/example/upstream",
				storedSHA,
				"integration", "")

			seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
			env.patchStore.Patches = []Patch{
				{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
			}

			// Upstream unchanged.
			env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
				return storedSHA, nil
			}

			rec := env.doSync(t)
			if rec.Code != http.StatusOK {
				t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
			}

			var resp CarryPatchSyncResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			if len(resp.PatchesMerged) != 0 {
				t.Errorf("expected empty patches_merged, got %v", resp.PatchesMerged)
			}
			if resp.RebuildTriggered {
				t.Error("expected rebuild_triggered=false")
			}

			if env.rebuildJobCount(t) != 0 {
				t.Errorf("expected 0 rebuild jobs, got %d", env.rebuildJobCount(t))
			}
		})
	}
}

// ===========================================================================
// TS-20-43 (integration): A hub-mode sync clears origin columns of every
// patch row of the workspace only.
//
// Verifies: 20-REQ-7.3
// ===========================================================================

func TestSyncRebuild_HubModeClearsOriginColumns_TS2043(t *testing.T) {
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

	storedSHA := "aaaa000000000000000000000000000000000001"

	// Seed two workspaces.
	seedWorkspaceCarryPatch(t, db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		storedSHA,
		"integration", "")

	seedWorkspaceCarryPatch(t, db, "other-workspace", "bob",
		"https://github.com/example/upstream2",
		"cccc000000000000000000000000000000000003",
		"integration", "")

	// Seed patches with origin state for both workspaces.
	seedPatch(t, db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
	seedPatch(t, db, "p2", "my-workspace", "feature/b", 2, PatchStatusConflict)
	seedPatch(t, db, "p3", "my-workspace", "feature/c", 3, PatchStatusDisabled)
	seedPatch(t, db, "p4", "my-workspace", "feature/d", 4, PatchStatusMergedUpstream)
	seedPatch(t, db, "p5", "other-workspace", "feature/x", 1, PatchStatusActive)

	// Set origin state on all patches.
	for _, pid := range []string{"p1", "p2", "p3", "p4"} {
		_, err := db.Exec(`UPDATE patches SET origin_sync_state = 'in_sync', origin_sha = 'deadbeef', origin_synced_at = '2024-01-01T00:00:00Z' WHERE id = ?`, pid)
		if err != nil {
			t.Fatalf("set origin state for %s: %v", pid, err)
		}
	}
	_, err = db.Exec(`UPDATE patches SET origin_sync_state = 'diverged', origin_sha = 'cafebabe', origin_synced_at = '2024-01-01T00:00:00Z' WHERE id = 'p5'`)
	if err != nil {
		t.Fatalf("set origin state for p5: %v", err)
	}

	// Use SQLPatchStore for real DB operations.
	patchStore := NewSQLPatchStore(db)

	mock := newMockGitRunner()
	// Upstream unchanged.
	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		return storedSHA, nil
	}

	getVar := func(scope, slug, key string) (string, error) {
		// Hub mode (default).
		return "", fmt.Errorf("not set")
	}

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())

	syncCfg := SyncAPIConfig{
		DB:            db,
		Queue:         q,
		WorkspaceRoot: t.TempDir(),
		NewGitRunner: func(_ string) (GitRunner, error) {
			return mock, nil
		},
		Fetch:       func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveAuth: func(_ string) (transport.AuthMethod, error) { return nil, nil },
		GetVariable: getVar,
		PatchStore:  patchStore,
	}
	RegisterSyncRoutes(api, syncCfg)

	auth := rebuildUserAuth("alice")
	authJSON, _ := json.Marshal(auth)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/my-workspace/sync", nil)
	req.Header.Set("X-Test-Auth", string(authJSON))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify all patches of my-workspace have NULL origin columns.
	rows, err := db.Query(`SELECT id, origin_sync_state, origin_sha, origin_synced_at FROM patches WHERE workspace_slug = 'my-workspace'`)
	if err != nil {
		t.Fatalf("query patches: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		var state, sha, syncedAt sql.NullString
		if err := rows.Scan(&id, &state, &sha, &syncedAt); err != nil {
			t.Fatalf("scan patch: %v", err)
		}
		if state.Valid {
			t.Errorf("patch %s: origin_sync_state should be NULL, got %q", id, state.String)
		}
		if sha.Valid {
			t.Errorf("patch %s: origin_sha should be NULL, got %q", id, sha.String)
		}
		if syncedAt.Valid {
			t.Errorf("patch %s: origin_synced_at should be NULL, got %q", id, syncedAt.String)
		}
	}

	// Verify other-workspace's patch is unchanged.
	var otherState, otherSHA sql.NullString
	db.QueryRow(`SELECT origin_sync_state, origin_sha FROM patches WHERE id = 'p5'`).Scan(&otherState, &otherSHA)
	if !otherState.Valid || otherState.String != "diverged" {
		t.Errorf("other workspace patch: origin_sync_state should be 'diverged', got %v", otherState)
	}
	if !otherSHA.Valid || otherSHA.String != "cafebabe" {
		t.Errorf("other workspace patch: origin_sha should be 'cafebabe', got %v", otherSHA)
	}
}

// Suppress unused import warnings.
var (
	_ = os.Stat
	_ = strings.TrimSpace
	_ = time.Now
	_ = apikit.NowUTC
)
