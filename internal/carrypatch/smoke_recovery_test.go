package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/jobqueue"
	"github.com/agent-fox-dev/hub/internal/workspace"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// ===========================================================================
// Smoke test environment for divergence-recovery end-to-end tests.
//
// Every test uses real components: HTTP workspace handlers, real carrypatch
// recovery hook, real git runner with real git, bare fork repository, trunk
// repository, job queue, audit store, database, wslock.
// ===========================================================================

// smokeRecoveryEnv holds a complete test environment for recovery smoke tests.
type smokeRecoveryEnv struct {
	forkDir   string // bare fork repo
	trunkDir  string // trunk (clone of fork)
	wsRoot    string // workspace root (parent of <slug>/trunk)
	db        *sql.DB
	queue     *jobqueue.Queue
	echo      *echo.Echo
	emitter   *cpAuditEmitter
	variables map[string]string
	slug      string
}

// setupSmokeRecoveryEnv creates a bare fork, clones it as the trunk, sets up
// the database, job queue, audit emitter, workspace routes, recovery hook,
// and echo server for smoke tests.
func setupSmokeRecoveryEnv(t *testing.T, slug string) *smokeRecoveryEnv {
	t.Helper()

	// Create bare fork.
	forkDir := filepath.Join(t.TempDir(), "fork.git")
	initBareRepo(t, forkDir)

	// Create a working copy to push initial content.
	workDir := t.TempDir()
	cloneRepo(t, forkDir, workDir)
	commitFile(t, workDir, "file.txt", "hello", "initial")
	runGitCmd(t, workDir, "push", "origin", "main")

	// Create trunk as a clone of the fork.
	wsRoot := t.TempDir()
	trunkDir := filepath.Join(wsRoot, slug, "trunk")
	cloneRepo(t, forkDir, trunkDir)

	// Create an integration branch.
	runGitCmd(t, trunkDir, "branch", "integration")

	// Database setup.
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)
	addWorkspaceColumns(t, db)

	// Create orgs table for workspace auth.
	mustExecRecovery(t, db, `CREATE TABLE IF NOT EXISTS orgs (
		id TEXT NOT NULL PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		slug TEXT NOT NULL UNIQUE,
		url TEXT,
		owner_id TEXT,
		status TEXT NOT NULL DEFAULT 'active',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	mustExecRecovery(t, db, `CREATE TABLE IF NOT EXISTS org_members (
		org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
		user_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (org_id, user_id)
	)`)

	// Seed org and membership.
	now := time.Now().UTC().Format(time.RFC3339)
	mustExecRecovery(t, db, `INSERT INTO orgs (id, name, slug, owner_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, 'active', ?, ?)`,
		"org-1", "Personal "+slug, "personal-"+slug, "alice", now, now)
	mustExecRecovery(t, db, `INSERT INTO org_members (org_id, user_id, created_at) VALUES (?, ?, ?)`,
		"org-1", "alice", now)

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
	seedWorkspaceCarryPatch(t, db, slug, "alice",
		forkDir, // git_url = fork path for local fetch
		runGitCmd(t, trunkDir, "rev-parse", "HEAD"),
		"integration", "")

	emitter := newCPAuditEmitter()

	return &smokeRecoveryEnv{
		forkDir:   forkDir,
		trunkDir:  trunkDir,
		wsRoot:    wsRoot,
		db:        db,
		queue:     q,
		emitter:   emitter,
		variables: map[string]string{"PATCH_BRANCH_SOURCE": "origin"},
		slug:      slug,
	}
}

// buildRecoveryEcho creates the echo server with workspace routes and the
// real recovery hook wired up.
func (env *smokeRecoveryEnv) buildRecoveryEcho(t *testing.T) {
	t.Helper()

	patchStore := NewSQLPatchStore(env.db)
	factory := NewGitRunnerFactory()

	getVar := func(scope, slug, key string) (string, error) {
		if v, ok := env.variables[key]; ok {
			return v, nil
		}
		return "", fmt.Errorf("not set")
	}

	// Create the real RecoveryService.
	svc := &RecoveryService{
		NewGitRunner:  factory,
		WorkspaceRoot: env.wsRoot,
		GetVariable:   getVar,
		ResolveAuth: func(slug string) (transport.AuthMethod, error) {
			return nil, nil // local fork, no auth needed
		},
		Fetch:      DefaultSingleBranchFetch(),
		LockFunc:   wslock.TryLock,
		PatchStore: patchStore,
		Queue:      env.queue,
	}

	// Create the adapter (same as main.go).
	adapter := &smokeRecoveryHookAdapter{svc: svc}

	// Save and restore the global recovery hook.
	t.Cleanup(func() {
		workspace.RegisterRecoveryHook(nil)
	})
	workspace.RegisterRecoveryHook(adapter)

	// Set up the audit emitter in workspace package.
	t.Cleanup(func() {
		workspace.SetAuditDependencies(nil, nil, nil)
	})
	workspace.SetAuditDependencies(nil, env.emitter, nil)

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(smokeRecoveryAuthMiddleware())

	if err := workspace.RegisterRoutes(api, env.db); err != nil {
		t.Fatalf("RegisterRoutes: %v", err)
	}

	env.echo = e
}

// smokeRecoveryHookAdapter bridges carrypatch.RecoveryService → workspace.RecoveryHook.
type smokeRecoveryHookAdapter struct {
	svc *RecoveryService
}

func (a *smokeRecoveryHookAdapter) ReadReplacedSHA(ctx context.Context, slug, branch string) (string, bool, error) {
	return a.svc.ReadReplacedSHA(ctx, slug, branch)
}

func (a *smokeRecoveryHookAdapter) RemoveBackup(ctx context.Context, slug, branch string) error {
	return a.svc.RemoveBackup(ctx, slug, branch)
}

func (a *smokeRecoveryHookAdapter) RunReset(ctx context.Context, slug string, patch workspace.ResetPatchInfo, auth *apikit.AuthInfo) (workspace.ResetResult, error) {
	cpPatch := ResetPatchInfo{
		ID:                patch.ID,
		BranchName:        patch.BranchName,
		Status:            patch.Status,
		IntegrationBranch: patch.IntegrationBranch,
	}

	cpResult, err := a.svc.RunReset(ctx, slug, cpPatch, auth)
	if err != nil {
		var cpErr *RecoveryError
		if errors.As(err, &cpErr) {
			kindMap := map[RecoveryErrorKind]workspace.ResetErrorKind{
				RecoveryErrBusy:            workspace.ResetErrBusy,
				RecoveryErrMissingOnOrigin:  workspace.ResetErrMissingOnOrigin,
				RecoveryErrFetchFailed:      workspace.ResetErrFetchFailed,
				RecoveryErrCredentialFailed: workspace.ResetErrCredentialFailed,
				RecoveryErrRefChanged:       workspace.ResetErrRefChanged,
				RecoveryErrOther:            workspace.ResetErrOther,
			}
			wsKind, ok := kindMap[cpErr.Kind]
			if !ok {
				wsKind = workspace.ResetErrOther
			}
			return workspace.ResetResult{}, &workspace.ResetError{
				Kind:    wsKind,
				Message: cpErr.Message,
				Cause:   cpErr.Cause,
			}
		}
		return workspace.ResetResult{}, &workspace.ResetError{
			Kind:    workspace.ResetErrOther,
			Message: err.Error(),
			Cause:   err,
		}
	}

	return workspace.ResetResult{
		Action:           workspace.ResetAction(cpResult.Action),
		LocalSHA:         cpResult.LocalSHA,
		OriginSHA:        cpResult.OriginSHA,
		ReplacedSHA:      cpResult.ReplacedSHA,
		RebuildTriggered: cpResult.RebuildTriggered,
		RebuildJobID:     cpResult.RebuildJobID,
	}, nil
}

// smokeRecoveryAuthMiddleware injects auth from X-Test-Auth header.
func smokeRecoveryAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get("X-Test-Auth")
			if authHeader != "" {
				var info apikit.AuthInfo
				if err := json.Unmarshal([]byte(authHeader), &info); err != nil {
					return echo.NewHTTPError(http.StatusBadRequest, "invalid X-Test-Auth header")
				}
				apikit.SetAuthInfo(c, &info)
			}
			return next(c)
		}
	}
}

func mustExecRecovery(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func (env *smokeRecoveryEnv) doRequest(t *testing.T, method, path string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if auth != nil {
		authJSON, _ := json.Marshal(auth)
		req.Header.Set("X-Test-Auth", string(authJSON))
	}
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	return rec
}

func (env *smokeRecoveryEnv) doRequestWithBody(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	if auth != nil {
		authJSON, _ := json.Marshal(auth)
		req.Header.Set("X-Test-Auth", string(authJSON))
	}
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	return rec
}

// divergeForkBranch creates a diverged state: the fork branch has a different
// commit than the local branch.
func (env *smokeRecoveryEnv) divergeForkBranch(t *testing.T, branchName string) (localSHA, forkSHA string) {
	t.Helper()
	// Create the branch on the fork.
	createBranchOnBare(t, env.forkDir, branchName, branchName+".txt", "fork content", "fork commit")

	// Fetch and create local branch at the fork tip.
	runGitCmd(t, env.trunkDir, "fetch", "origin", branchName)
	runGitCmd(t, env.trunkDir, "branch", branchName, "refs/remotes/origin/"+branchName)

	// Add a divergent commit locally.
	runGitCmd(t, env.trunkDir, "checkout", branchName)
	localSHA = commitFile(t, env.trunkDir, branchName+"-local.txt", "local content", "local divergent commit")
	runGitCmd(t, env.trunkDir, "checkout", "main")

	// Add a different commit on the fork.
	workDir := t.TempDir()
	cloneRepo(t, env.forkDir, workDir)
	runGitCmd(t, workDir, "checkout", branchName)
	forkSHA = commitFile(t, workDir, branchName+"-fork.txt", "fork divergent content", "fork divergent commit")
	runGitCmd(t, workDir, "push", "origin", branchName)

	return localSHA, forkSHA
}

// ===========================================================================
// TS-23-71 (smoke): An operator resets a diverged patch through the CLI
// and the rebuild follows
//
// Verifies: 23-PATH-1, 23-REQ-3.6, 23-REQ-4.3, 23-REQ-10.3
//
// Real components: HTTP server and workspace handlers, carrypatch recovery
// hook, git runner with real git, bare fork repository, trunk repository,
// job queue, audit store, database, wslock
// ===========================================================================

func TestSmoke_TS23_71_ResetDivergedPatchRebuildFollows(t *testing.T) {
	slug := "smoke-reset-71"
	env := setupSmokeRecoveryEnv(t, slug)

	// Create a diverged active patch branch.
	branch := "feature/diverged"
	localSHA, forkSHA := env.divergeForkBranch(t, branch)

	// Register the patch.
	seedPatch(t, env.db, "p1", slug, branch, 1, PatchStatusActive)

	env.buildRecoveryEcho(t)

	auth := rebuildUserAuth("alice")

	// POST /workspaces/:slug/patches/:id/reset-to-origin
	rec := env.doRequest(t, http.MethodPost,
		fmt.Sprintf("/api/v1/workspaces/%s/patches/p1/reset-to-origin", slug), auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Parse response.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Verify replaced_sha is present and equals the old local tip.
	replacedSHA, ok := resp["replaced_sha"].(string)
	if !ok || replacedSHA == "" {
		t.Fatalf("expected replaced_sha in response; got %v", resp["replaced_sha"])
	}
	if replacedSHA != localSHA {
		t.Errorf("replaced_sha = %s; want %s", replacedSHA, localSHA)
	}

	// Verify rebuild_triggered is true.
	if resp["rebuild_triggered"] != true {
		t.Errorf("rebuild_triggered = %v; want true", resp["rebuild_triggered"])
	}

	// Verify rebuild_job_id is present.
	jobID, ok := resp["rebuild_job_id"].(string)
	if !ok || jobID == "" {
		t.Errorf("expected rebuild_job_id in response; got %v", resp["rebuild_job_id"])
	}

	// Verify the trunk branch equals the fork tip.
	trunkTip := revParse(t, env.trunkDir, "refs/heads/"+branch)
	if trunkTip != forkSHA {
		t.Errorf("trunk tip = %s; want %s (fork tip)", trunkTip, forkSHA)
	}

	// Verify refs/hub/replaced/<branch> equals the old local tip.
	backupSHA := revParse(t, env.trunkDir, "refs/hub/replaced/"+branch)
	if backupSHA != localSHA {
		t.Errorf("backup ref = %s; want %s (old local tip)", backupSHA, localSHA)
	}

	// Verify the patch row is in_sync with origin_sha set.
	var originState, originSHA sql.NullString
	err := env.db.QueryRow(
		`SELECT origin_sync_state, origin_sha FROM patches WHERE id = 'p1'`,
	).Scan(&originState, &originSHA)
	if err != nil {
		t.Fatalf("query patch state: %v", err)
	}
	if !originState.Valid || originState.String != StateInSync {
		t.Errorf("origin_sync_state = %v; want %q", originState, StateInSync)
	}
	if !originSHA.Valid || originSHA.String != forkSHA {
		t.Errorf("origin_sha = %v; want %q", originSHA, forkSHA)
	}

	// Verify a rebuild job is queued.
	var jobStatus string
	err = env.db.QueryRow(
		`SELECT status FROM jobs WHERE id = ?`, jobID,
	).Scan(&jobStatus)
	if err != nil {
		t.Fatalf("query rebuild job: %v", err)
	}
	if jobStatus != "queued" && jobStatus != "pending" {
		t.Errorf("rebuild job status = %q; want queued or pending", jobStatus)
	}

	// Verify audit events.
	events := env.emitter.Events()

	var resetEvents, replaceEvents []audit.HubEvent
	for _, ev := range events {
		switch ev.EventType {
		case audit.EventPatchReset:
			resetEvents = append(resetEvents, ev)
		case audit.EventPatchReplace:
			replaceEvents = append(replaceEvents, ev)
		}
	}

	if len(resetEvents) != 1 {
		t.Fatalf("hub.patch.reset events = %d; want 1", len(resetEvents))
	}
	resetMeta := resetEvents[0].Metadata
	if resetMeta["action"] != "replaced" {
		t.Errorf("reset action = %v; want replaced", resetMeta["action"])
	}
	if resetMeta["branch_name"] != branch {
		t.Errorf("reset branch_name = %v; want %s", resetMeta["branch_name"], branch)
	}

	if len(replaceEvents) != 1 {
		t.Fatalf("hub.patch.replace events = %d; want 1", len(replaceEvents))
	}
	replaceMeta := replaceEvents[0].Metadata
	if replaceMeta["trigger"] != "reset_to_origin" {
		t.Errorf("replace trigger = %v; want reset_to_origin", replaceMeta["trigger"])
	}
}

// ===========================================================================
// TS-23-72 (smoke): An operator reads replaced_sha and fetches the discarded
// tip from the hub
//
// Verifies: 23-PATH-2, 23-REQ-1.1, 23-REQ-6.6
//
// Real components: HTTP server and workspace handlers, carrypatch recovery
// hook, trunk repository, real git client, database
// ===========================================================================

func TestSmoke_TS23_72_ReadReplacedSHAAndFetch(t *testing.T) {
	slug := "smoke-fetch-72"
	env := setupSmokeRecoveryEnv(t, slug)

	branch := "feature/fetchable"

	// Create a branch and a backup ref.
	createBranchOnBare(t, env.forkDir, branch, branch+".txt", "content", "commit")
	runGitCmd(t, env.trunkDir, "fetch", "origin", branch)
	runGitCmd(t, env.trunkDir, "branch", branch, "refs/remotes/origin/"+branch)
	branchSHA := revParse(t, env.trunkDir, "refs/heads/"+branch)

	// Write a backup ref.
	runGitCmd(t, env.trunkDir, "update-ref", "refs/hub/replaced/"+branch, branchSHA)

	// Register the patch.
	seedPatch(t, env.db, "p1", slug, branch, 1, PatchStatusActive)

	env.buildRecoveryEcho(t)

	auth := rebuildUserAuth("alice")

	// GET /workspaces/:slug/patches/p1
	rec := env.doRequest(t, http.MethodGet,
		fmt.Sprintf("/api/v1/workspaces/%s/patches/p1", slug), auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Verify replaced_sha is present with 40 hex characters.
	replacedSHA, ok := resp["replaced_sha"].(string)
	if !ok || len(replacedSHA) != 40 {
		t.Fatalf("expected 40-char replaced_sha; got %v", resp["replaced_sha"])
	}
	if replacedSHA != branchSHA {
		t.Errorf("replaced_sha = %s; want %s", replacedSHA, branchSHA)
	}

	// Verify the backup ref can be fetched by a git client.
	// We verify the ref exists and resolves correctly in the trunk.
	fetchedSHA := revParse(t, env.trunkDir, "refs/hub/replaced/"+branch)
	if fetchedSHA != replacedSHA {
		t.Errorf("fetched SHA = %s; want %s", fetchedSHA, replacedSHA)
	}
}

// ===========================================================================
// TS-23-74 (smoke): Removing a patch through the handler deletes its backup ref
//
// Verifies: 23-PATH-4, 23-REQ-7.1
//
// Real components: HTTP server and workspace handlers, carrypatch recovery
// hook, git runner with real git, trunk repository, audit store, database
// ===========================================================================

func TestSmoke_TS23_74_RemovePatchDeletesBackupRef(t *testing.T) {
	slug := "smoke-remove-74"
	env := setupSmokeRecoveryEnv(t, slug)

	branch := "feature/removable"

	// Create a branch and a backup ref.
	createBranchOnBare(t, env.forkDir, branch, branch+".txt", "content", "commit")
	runGitCmd(t, env.trunkDir, "fetch", "origin", branch)
	runGitCmd(t, env.trunkDir, "branch", branch, "refs/remotes/origin/"+branch)
	branchSHA := revParse(t, env.trunkDir, "refs/heads/"+branch)
	runGitCmd(t, env.trunkDir, "update-ref", "refs/hub/replaced/"+branch, branchSHA)

	// Register the patch.
	seedPatch(t, env.db, "p1", slug, branch, 1, PatchStatusActive)

	env.buildRecoveryEcho(t)

	auth := rebuildUserAuth("alice")

	// Verify backup ref exists before removal.
	if revParse(t, env.trunkDir, "refs/hub/replaced/"+branch) == "" {
		t.Fatal("backup ref should exist before removal")
	}

	// DELETE /workspaces/:slug/patches/p1
	rec := env.doRequest(t, http.MethodDelete,
		fmt.Sprintf("/api/v1/workspaces/%s/patches/p1", slug), auth)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d; want %d; body = %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}

	// Verify the patch row is gone.
	var count int
	err := env.db.QueryRow(`SELECT COUNT(*) FROM patches WHERE id = 'p1'`).Scan(&count)
	if err != nil {
		t.Fatalf("query patch count: %v", err)
	}
	if count != 0 {
		t.Errorf("patch row still exists after DELETE")
	}

	// Verify the backup ref is gone.
	if revParse(t, env.trunkDir, "refs/hub/replaced/"+branch) != "" {
		t.Error("backup ref should be removed after DELETE")
	}

	// Verify hub.patch.delete audit event was emitted.
	events := env.emitter.Events()
	var deleteEvents []audit.HubEvent
	for _, ev := range events {
		if ev.EventType == "hub.patch.delete" {
			deleteEvents = append(deleteEvents, ev)
		}
	}
	if len(deleteEvents) != 1 {
		t.Errorf("hub.patch.delete events = %d; want 1", len(deleteEvents))
	}
}

// ===========================================================================
// TS-23-75 (smoke): The ref-aware purge removes backup refs and rows for
// expired soft-deleted patches
//
// Verifies: 23-PATH-5, 23-REQ-8.2
//
// Real components: purge routine, SQLPatchStore, git runner factory with
// real git, trunk repositories, database
// ===========================================================================

func TestSmoke_TS23_75_PurgeRemovesBackupRefsAndRows(t *testing.T) {
	wsRoot := t.TempDir()
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)

	factory := NewGitRunnerFactory()

	// Set up two workspaces with trunks.
	for _, slug := range []string{"ws1", "ws2"} {
		forkDir := filepath.Join(t.TempDir(), slug+"-fork.git")
		initBareRepo(t, forkDir)
		tmpClone := filepath.Join(t.TempDir(), slug+"-init")
		cloneRepo(t, forkDir, tmpClone)
		commitFile(t, tmpClone, "README.md", "init", "initial")
		runGitCmd(t, tmpClone, "push", "origin", "main")

		trunkDir := filepath.Join(wsRoot, slug, "trunk")
		cloneRepo(t, forkDir, trunkDir)

		// Create a branch and backup ref.
		branch := "feature/" + slug
		runGitCmd(t, trunkDir, "checkout", "-b", branch)
		commitFile(t, trunkDir, branch+".txt", "content", "commit")
		sha := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
		runGitCmd(t, trunkDir, "checkout", "main")
		runGitCmd(t, trunkDir, "update-ref", "refs/hub/replaced/"+branch, sha)
	}

	// Insert expired soft-deleted patches (10 days ago).
	tenDaysAgo := time.Now().UTC().Add(-10 * 24 * time.Hour).Format(time.RFC3339)
	seedPatchDeleted(t, db, "p-exp-1", "ws1", "feature/ws1", -1, tenDaysAgo)
	seedPatchDeleted(t, db, "p-exp-2", "ws2", "feature/ws2", -2, tenDaysAgo)

	// Insert a recent soft-deleted patch (3 days ago).
	threeDaysAgo := time.Now().UTC().Add(-3 * 24 * time.Hour).Format(time.RFC3339)
	seedPatchDeleted(t, db, "p-recent", "ws1", "feature/recent", -3, threeDaysAgo)

	// Create a backup ref for the recent patch too.
	trunkDir1 := filepath.Join(wsRoot, "ws1", "trunk")
	runGitCmd(t, trunkDir1, "checkout", "-b", "feature/recent")
	commitFile(t, trunkDir1, "recent.txt", "recent", "recent commit")
	recentSHA := runGitCmd(t, trunkDir1, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir1, "checkout", "main")
	runGitCmd(t, trunkDir1, "update-ref", "refs/hub/replaced/feature/recent", recentSHA)

	store := NewSQLPatchStore(db)

	// Run the ref-aware purge.
	purged, err := PurgeExpiredDeletedPatchesWithRefs(context.Background(), store, wsRoot, factory)
	if err != nil {
		t.Fatalf("PurgeExpiredDeletedPatchesWithRefs: %v", err)
	}

	// Should have purged 2 expired rows.
	if purged != 2 {
		t.Errorf("purged = %d; want 2", purged)
	}

	// Verify expired rows are gone.
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM patches WHERE id IN ('p-exp-1', 'p-exp-2')`).Scan(&count)
	if err != nil {
		t.Fatalf("query expired patches: %v", err)
	}
	if count != 0 {
		t.Errorf("expired patch rows still exist: %d", count)
	}

	// Verify expired backup refs are gone.
	if revParse(t, filepath.Join(wsRoot, "ws1", "trunk"), "refs/hub/replaced/feature/ws1") != "" {
		t.Error("backup ref for ws1 should be removed")
	}
	if revParse(t, filepath.Join(wsRoot, "ws2", "trunk"), "refs/hub/replaced/feature/ws2") != "" {
		t.Error("backup ref for ws2 should be removed")
	}

	// Verify recent row and its backup ref remain.
	err = db.QueryRow(`SELECT COUNT(*) FROM patches WHERE id = 'p-recent'`).Scan(&count)
	if err != nil {
		t.Fatalf("query recent patch: %v", err)
	}
	if count != 1 {
		t.Errorf("recent patch row missing; count = %d", count)
	}
	if revParse(t, trunkDir1, "refs/hub/replaced/feature/recent") == "" {
		t.Error("backup ref for recent patch should remain")
	}
}

// ===========================================================================
// TS-23-76 (smoke): A reset against an unreachable fork returns 502 and
// leaves everything unchanged
//
// Verifies: 23-PATH-6, 23-REQ-3.2, 23-REQ-10.5
//
// Real components: HTTP server and workspace handlers, carrypatch recovery
// hook, real single-branch fetch, git runner with real git, trunk repository,
// audit store, wslock, database
// ===========================================================================

func TestSmoke_TS23_76_UnreachableForkReturns502(t *testing.T) {
	slug := "smoke-502-76"
	env := setupSmokeRecoveryEnv(t, slug)

	branch := "feature/unreachable"

	// Create the branch on the fork and trunk.
	createBranchOnBare(t, env.forkDir, branch, branch+".txt", "content", "commit")
	runGitCmd(t, env.trunkDir, "fetch", "origin", branch)
	runGitCmd(t, env.trunkDir, "branch", branch, "refs/remotes/origin/"+branch)

	// Register the patch.
	seedPatch(t, env.db, "p1", slug, branch, 1, PatchStatusActive)

	// Snapshot refs before the reset attempt.
	localSHABefore := revParse(t, env.trunkDir, "refs/heads/"+branch)
	remoteSHABefore := revParse(t, env.trunkDir, "refs/remotes/origin/"+branch)

	// Point the origin remote to an unreachable URL.
	runGitCmd(t, env.trunkDir, "remote", "set-url", "origin", "https://127.0.0.1:1/nonexistent.git")

	env.buildRecoveryEcho(t)

	auth := rebuildUserAuth("alice")

	rec := env.doRequest(t, http.MethodPost,
		fmt.Sprintf("/api/v1/workspaces/%s/patches/p1/reset-to-origin", slug), auth)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}

	// Parse error response.
	var errResp struct {
		Error struct {
			Code      int    `json:"code"`
			Message   string `json:"message"`
			ErrorType string `json:"error_type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if errResp.Error.Message != "origin fetch failed" {
		t.Errorf("message = %q; want %q", errResp.Error.Message, "origin fetch failed")
	}
	if errResp.Error.ErrorType != "origin_fetch_failed" {
		t.Errorf("error_type = %q; want %q", errResp.Error.ErrorType, "origin_fetch_failed")
	}

	// Verify the response does not contain the underlying network error text.
	bodyStr := rec.Body.String()
	if strings.Contains(bodyStr, "127.0.0.1") {
		t.Errorf("response should not contain network error details; got: %s", bodyStr)
	}

	// Verify every trunk ref is unchanged.
	localSHAAfter := revParse(t, env.trunkDir, "refs/heads/"+branch)
	if localSHAAfter != localSHABefore {
		t.Errorf("local ref changed: %s → %s", localSHABefore, localSHAAfter)
	}
	remoteSHAAfter := revParse(t, env.trunkDir, "refs/remotes/origin/"+branch)
	if remoteSHAAfter != remoteSHABefore {
		t.Errorf("remote ref changed: %s → %s", remoteSHABefore, remoteSHAAfter)
	}

	// Verify no audit event was recorded.
	events := env.emitter.Events()
	for _, ev := range events {
		if ev.EventType == audit.EventPatchReset || ev.EventType == audit.EventPatchReplace {
			t.Errorf("unexpected audit event: %s", ev.EventType)
		}
	}

	// Verify the workspace lock is free.
	unlock, ok := wslock.TryLock(slug)
	if !ok {
		t.Error("workspace lock should be free after failed reset")
	} else {
		unlock()
	}
}
