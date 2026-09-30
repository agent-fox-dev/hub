//go:build smoke

package carrypatch

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// ===========================================================================
// Task 8 — end-to-end verification of fork-authoritative sync
//
// TS-02-32, TS-02-33 and TS-02-34 exercise the real production wiring:
//   - the actual workspace.RegisterRoutes / workspace.RegisterCarryPatchSyncHook
//     path that cmd/af-hub/main.go uses for POST /workspaces/:slug/sync and
//     GET /workspaces/:slug/patches (not the package-local standalone
//     handleCarryPatchSyncEndpoint route, which production never mounts);
//   - carrypatch.RegisterPatchStatusRoutes for GET /workspaces/:slug/patch-status;
//   - a real GitRunner (carrypatch.NewGitRunnerFactory, i.e. gitcmd against a
//     real `git` subprocess) driven against real bare "origin" (fork) and
//     "upstream" repositories and a real trunk clone on disk;
//   - carrypatch.DefaultFetchFunc / carrypatch.DefaultOriginFetchFunc (go-git
//     fetches), exactly as cmd/af-hub/main.go wires them;
//   - a real carrypatch.SQLPatchStore backed by a real SQLite patches table;
//   - a real jobqueue.Queue (jobs are enqueued into a real SQLite jobs table;
//     the worker pool is not started, matching every other carrypatch HTTP
//     test in this package — no test in the package asserts on job
//     execution, only on enqueue);
//   - a real audit.Emitter implementation (cpAuditEmitter, the same in-memory
//     Emitter already used by TS-02-30/31 in handlers_audit_test.go — no
//     carrypatch test anywhere in the repository wires a DuckDB-backed
//     emitter; this is the established "real component" for audit in this
//     package).
// ===========================================================================

// ---------------------------------------------------------------------------
// Fixture: production-shaped workspaces/patches schema + full route wiring
// ---------------------------------------------------------------------------

// forkSyncEnv holds a real HTTP server wired the way cmd/af-hub/main.go wires
// fork-authoritative sync: workspace.RegisterRoutes for the workspace CRUD,
// sync and patches endpoints, workspace.RegisterCarryPatchSyncHook for the
// carry-patch sync delegation, and carrypatch.RegisterPatchStatusRoutes for
// the dashboard.
type forkSyncEnv struct {
	echo          *echo.Echo
	db            *sql.DB
	queue         *jobqueue.Queue
	workspaceRoot string
	patchStore    *SQLPatchStore
	audit         *cpAuditEmitter
}

// createForkSyncWorkspacesTable creates the workspaces table with the exact
// column set production's internal/workspace/schema.go createTableSQL
// declares (verified by reading that file), so that both
// workspace.RegisterRoutes's handlers and carrypatch's own queries against
// the same table work unmodified.
func createForkSyncWorkspacesTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS workspaces (
			slug              TEXT PRIMARY KEY,
			git_url           TEXT NOT NULL,
			branch            TEXT,
			owner_id          TEXT NOT NULL,
			org_id            TEXT,
			status            TEXT NOT NULL DEFAULT 'active',
			display_name      TEXT NOT NULL DEFAULT '',
			description       TEXT NOT NULL DEFAULT '',
			clone_status      TEXT NOT NULL DEFAULT 'pending' CHECK(clone_status IN ('pending','cloning','ready','failed','archived')),
			head_sha          TEXT,
			clone_error       TEXT,
			created_at        TEXT NOT NULL,
			updated_at        TEXT NOT NULL,
			sync_mode         TEXT NOT NULL DEFAULT 'pull_only',
			sync_status       TEXT NOT NULL DEFAULT 'idle',
			upstream_head_sha TEXT,
			last_sync_at      TEXT,
			sync_error        TEXT,
			workspace_mode    TEXT NOT NULL DEFAULT 'standard',
			upstream_url      TEXT,
			integration_branch TEXT
		)`)
	if err != nil {
		t.Fatalf("failed to create workspaces table: %v", err)
	}
}

// createForkSyncPatchesTable creates the patches table with the exact column
// set production's internal/workspace/schema.go createPatchesTableSQL plus
// patchFieldDDL declare, so that workspace's own patch handlers (added_at,
// description, deleted_at, upstream_pr_url) and carrypatch's SQLPatchStore /
// handlePatchStatus (origin_sync_state, origin_sha, origin_synced_at) read
// and write the same table without modification.
func createForkSyncPatchesTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS patches (
			id                TEXT PRIMARY KEY,
			workspace_slug    TEXT NOT NULL,
			branch_name       TEXT NOT NULL,
			position          INTEGER NOT NULL,
			status            TEXT NOT NULL DEFAULT 'active',
			conflict_files    TEXT,
			upstream_pr_url   TEXT,
			description       TEXT,
			deleted_at        TEXT,
			added_at          TEXT NOT NULL,
			updated_at        TEXT NOT NULL,
			origin_sync_state TEXT,
			origin_sha        TEXT,
			origin_synced_at  TEXT,
			UNIQUE(workspace_slug, branch_name),
			UNIQUE(workspace_slug, position)
		)`)
	if err != nil {
		t.Fatalf("failed to create patches table: %v", err)
	}
}

// seedForkSyncPatch inserts a patch row using the added_at/updated_at column
// names internal/workspace's patch_store.go expects (as opposed to this
// package's own test-only created_at/updated_at helper in testhelpers_test.go).
func seedForkSyncPatch(t *testing.T, db *sql.DB, id, workspaceSlug, branchName string, position int, status string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, workspaceSlug, branchName, position, status, now, now,
	)
	if err != nil {
		t.Fatalf("seedForkSyncPatch(%q) returned error: %v", id, err)
	}
}

// newForkSyncSmokeEnv wires a real HTTP server against real components:
// SQLite (workspaces/patches/jobs tables), a real jobqueue.Queue, a real
// carrypatch.SQLPatchStore, and the actual production sync path (workspace's
// own POST /sync and GET /patches handlers, delegating to the carry-patch
// sync hook exactly as cmd/af-hub/main.go wires it).
func newForkSyncSmokeEnv(t *testing.T, getVar GetVariableFunc) *forkSyncEnv {
	t.Helper()

	db := openTestDB(t)
	createForkSyncWorkspacesTable(t, db)
	createForkSyncPatchesTable(t, db)

	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey() returned error: %v", err)
	}
	if err := jobqueue.MigrateProgress(db); err != nil {
		t.Fatalf("MigrateProgress() returned error: %v", err)
	}

	q, err := jobqueue.New(db, nopLogger())
	if err != nil {
		t.Fatalf("jobqueue.New() returned error: %v", err)
	}
	_ = RegisterRebuildJob(q, &RebuildHandler{})

	workspaceRoot := t.TempDir()
	patchStore := NewSQLPatchStore(db)
	auditEmitter := newCPAuditEmitter()

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())

	// The real production workspace routes: POST /workspaces/:slug/sync and
	// GET /workspaces/:slug/patches (among others), exactly as
	// workspace.MountWorkspaceHandlers registers them in cmd/af-hub/main.go.
	if err := workspace.RegisterRoutes(api, db); err != nil {
		t.Fatalf("workspace.RegisterRoutes() returned error: %v", err)
	}

	syncCfg := SyncAPIConfig{
		DB:            db,
		Queue:         q,
		WorkspaceRoot: workspaceRoot,
		NewGitRunner:  NewGitRunnerFactory(),
		Fetch:         DefaultFetchFunc(),
		ResolveAuth: func(_ string) (transport.AuthMethod, error) {
			return nil, nil
		},
		OriginFetch: DefaultOriginFetchFunc(),
		ResolveOriginAuth: func(_ string) (transport.AuthMethod, error) {
			return nil, nil
		},
		GetVariable: getVar,
		PatchStore:  patchStore,
		Audit:       auditEmitter,
	}

	// The real production wiring: workspace.RegisterCarryPatchSyncHook, the
	// exact call cmd/af-hub/main.go makes, not the package-local standalone
	// RegisterSyncRoutes route (which production never mounts).
	workspace.RegisterCarryPatchSyncHook(NewCarryPatchSyncHook(syncCfg))
	t.Cleanup(func() { workspace.RegisterCarryPatchSyncHook(nil) })

	RegisterPatchStatusRoutes(api, PatchStatusAPIConfig{
		DB:            db,
		Queue:         q,
		WorkspaceRoot: workspaceRoot,
		PatchStore:    patchStore,
	})

	return &forkSyncEnv{
		echo:          e,
		db:            db,
		queue:         q,
		workspaceRoot: workspaceRoot,
		patchStore:    patchStore,
		audit:         auditEmitter,
	}
}

// ---------------------------------------------------------------------------
// Real git fixture: bare "origin" (fork) and "upstream" repositories plus a
// trunk clone with both remotes configured, built with exec.Command("git",
// ...) directly against t.TempDir(), matching the style already established
// in sync_origin_test.go's TS-02-5 subtest.
// ---------------------------------------------------------------------------

type forkSyncRepos struct {
	originBare   string
	upstreamBare string
	trunkDir     string
}

// setupForkSyncRepos creates bare "origin" and "upstream" repositories and a
// trunk working directory with both remotes configured and a base commit
// pushed to both, plus an "integration" branch. The upstream bare repo's
// HEAD is explicitly pointed at its default branch so that
// "+HEAD:refs/remotes/upstream/HEAD" (internal/upstream.Fetch's refspec)
// resolves during a real fetch.
func setupForkSyncRepos(t *testing.T, workspaceRoot, slug string) *forkSyncRepos {
	t.Helper()

	tmp := t.TempDir()
	originBare := filepath.Join(tmp, "origin.git")
	upstreamBare := filepath.Join(tmp, "upstream.git")
	runGitCmd(t, "", "init", "--bare", "-q", originBare)
	runGitCmd(t, "", "init", "--bare", "-q", upstreamBare)

	trunkDir := filepath.Join(workspaceRoot, slug, "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	runGitCmd(t, "", "init", "-q", "-b", "main", trunkDir)
	configGitUserCmd(t, trunkDir)
	writeFileHelper(t, filepath.Join(trunkDir, "base.txt"), "base content\n")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-q", "-m", "base commit")
	runGitCmd(t, trunkDir, "branch", "integration")

	runGitCmd(t, trunkDir, "remote", "add", "origin", originBare)
	runGitCmd(t, trunkDir, "remote", "add", "upstream", upstreamBare)
	runGitCmd(t, trunkDir, "push", "-q", "origin", "main")
	runGitCmd(t, trunkDir, "push", "-q", "origin", "integration")
	runGitCmd(t, trunkDir, "push", "-q", "upstream", "main")

	// A fresh `git init --bare` defaults HEAD to whatever init.defaultBranch
	// resolves to; pin it explicitly to "main" so the pushed branch is what
	// "+HEAD:refs/remotes/upstream/HEAD" resolves to.
	runGitCmd(t, "", "--git-dir="+upstreamBare, "symbolic-ref", "HEAD", "refs/heads/main")

	return &forkSyncRepos{originBare: originBare, upstreamBare: upstreamBare, trunkDir: trunkDir}
}

// pushForkOnlyBranch creates a branch on the trunk, commits a change, pushes
// it to the fork ("origin") only, and then removes the local branch — so the
// branch exists on the fork and never on the hub's local clone, exactly as
// 02-PATH-1 describes.
func pushForkOnlyBranch(t *testing.T, trunkDir, branch, content string) string {
	t.Helper()
	runGitCmd(t, trunkDir, "checkout", "-q", "-b", branch)
	writeFileHelper(t, filepath.Join(trunkDir, branch+".txt"), content)
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-q", "-m", "fork-only change: "+branch)
	sha := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "push", "-q", "origin", branch)
	runGitCmd(t, trunkDir, "checkout", "-q", "main")
	runGitCmd(t, trunkDir, "branch", "-D", branch)
	return sha
}

// forkSyncAuth returns an api_key AuthInfo (not admin, not PAT) owning the
// workspace, matching the pattern used throughout this package's tests.
func forkSyncAuth(userID string) *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "api_key", UserID: userID}
}

// ===========================================================================
// TS-02-32 (smoke): A fork-only patch branch is created by sync, triggers a
// rebuild, and shows up on the patch-status dashboard
//
// Verifies: 02-PATH-1, 02-REQ-3.3, 02-REQ-4.1
// ===========================================================================

func TestSmoke_TS02_32_ForkOnlyPatchBranchCreatedBySync(t *testing.T) {
	slug := "cp-fork-create"

	getVar := func(_, _, key string) (string, error) {
		switch key {
		case "PATCH_BRANCH_SOURCE":
			return "origin", nil
		case "AUTO_REBUILD_AFTER_SYNC":
			return "true", nil
		case "REBUILD_STRATEGY":
			return "rebase", nil
		}
		return "", nil
	}
	env := newForkSyncSmokeEnv(t, getVar)

	repos := setupForkSyncRepos(t, env.workspaceRoot, slug)
	forkSHA := pushForkOnlyBranch(t, repos.trunkDir, "fork-only-feature", "fork content\n")

	seedWorkspaceCarryPatch(t, env.db, slug, "alice", repos.upstreamBare, "", "integration", "")
	seedForkSyncPatch(t, env.db, "patch-1", slug, "fork-only-feature", 1, PatchStatusActive)

	auth := forkSyncAuth("alice")
	rec := doRebuildAuditRequest(t, env.echo, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync returned %d; want 200. body: %s", rec.Code, rec.Body.String())
	}

	var resp CarryPatchSyncResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode sync response: %v (body: %s)", err, rec.Body.String())
	}
	if !resp.OriginFetched {
		t.Error("origin_fetched = false; want true")
	}
	if len(resp.PatchesSynced) != 1 {
		t.Fatalf("patches_synced = %+v; want exactly one entry", resp.PatchesSynced)
	}
	entry := resp.PatchesSynced[0]
	if entry.BranchName != "fork-only-feature" {
		t.Errorf("patches_synced[0].branch_name = %q; want %q", entry.BranchName, "fork-only-feature")
	}
	if entry.Action != "created" {
		t.Errorf("patches_synced[0].action = %q; want %q", entry.Action, "created")
	}
	if !resp.RebuildTriggered {
		t.Error("rebuild_triggered = false; want true")
	}

	// refs/heads/fork-only-feature now exists locally at the fork's tip.
	localSHA := runGitCmd(t, repos.trunkDir, "rev-parse", "--verify", "refs/heads/fork-only-feature")
	if localSHA != forkSHA {
		t.Errorf("local branch sha = %s; want %s (fork tip)", localSHA, forkSHA)
	}

	// A hub.patch.sync audit event was recorded.
	foundSync := false
	for _, ev := range env.audit.Events() {
		if ev.EventType == "hub.patch.sync" {
			foundSync = true
		}
	}
	if !foundSync {
		t.Error("expected a hub.patch.sync audit event, got none")
	}

	// A rebuild job was actually enqueued in the real jobs table.
	var jobCount int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE type = 'rebuild' AND key = ?`, slug).Scan(&jobCount); err != nil {
		t.Fatalf("query rebuild jobs: %v", err)
	}
	if jobCount == 0 {
		t.Error("rebuild_triggered=true but no rebuild job row found in jobs table")
	}

	// The patch-status dashboard reflects origin_sync_state and updated
	// summary counts.
	recStatus := doRebuildAuditRequest(t, env.echo, http.MethodGet, "/api/v1/workspaces/"+slug+"/patch-status", "", auth)
	if recStatus.Code != http.StatusOK {
		t.Fatalf("GET /patch-status returned %d; want 200. body: %s", recStatus.Code, recStatus.Body.String())
	}
	var statusResp PatchStatusResponse
	if err := json.Unmarshal(recStatus.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("failed to decode patch-status response: %v", err)
	}
	if len(statusResp.Patches) != 1 {
		t.Fatalf("patch-status patches = %+v; want exactly one entry", statusResp.Patches)
	}
	p := statusResp.Patches[0]
	if p.OriginSyncState == nil || *p.OriginSyncState != "in_sync" {
		t.Errorf("patch-status origin_sync_state = %v; want \"in_sync\"", p.OriginSyncState)
	}
	if p.OriginSHA == nil || *p.OriginSHA != forkSHA {
		t.Errorf("patch-status origin_sha = %v; want %q", p.OriginSHA, forkSHA)
	}
	if statusResp.Summary.TotalPatches != 1 || statusResp.Summary.Active != 1 {
		t.Errorf("patch-status summary = %+v; want total_patches=1, active=1", statusResp.Summary)
	}
}

// ===========================================================================
// TS-02-33 (smoke): A diverged patch branch is replaced under the default
// divergence policy, backing up the hub's tip and triggering a rebuild
//
// Verifies: 02-PATH-2, 02-REQ-3.6
// ===========================================================================

func TestSmoke_TS02_33_DivergedPatchBranchReplacedUnderDefaultPolicy(t *testing.T) {
	slug := "cp-fork-replace"

	getVar := func(_, _, key string) (string, error) {
		switch key {
		case "PATCH_BRANCH_SOURCE":
			return "origin", nil
		case "AUTO_REBUILD_AFTER_SYNC":
			return "true", nil
		case "REBUILD_STRATEGY":
			return "rebase", nil
			// PATCH_DIVERGENCE_POLICY intentionally left unset -> default "replace".
		}
		return "", nil
	}
	env := newForkSyncSmokeEnv(t, getVar)

	repos := setupForkSyncRepos(t, env.workspaceRoot, slug)

	// Shared base for the patch branch, pushed to the fork and kept locally.
	runGitCmd(t, repos.trunkDir, "checkout", "-q", "-b", "patch-diverge")
	writeFileHelper(t, filepath.Join(repos.trunkDir, "patch-diverge.txt"), "shared base\n")
	runGitCmd(t, repos.trunkDir, "add", ".")
	runGitCmd(t, repos.trunkDir, "commit", "-q", "-m", "shared base for patch-diverge")
	runGitCmd(t, repos.trunkDir, "push", "-q", "origin", "patch-diverge")

	// Hub-only rewrite: an additional commit that is never pushed anywhere.
	writeFileHelper(t, filepath.Join(repos.trunkDir, "patch-diverge.txt"), "hub-local rewrite\n")
	runGitCmd(t, repos.trunkDir, "add", ".")
	runGitCmd(t, repos.trunkDir, "commit", "-q", "-m", "hub rewrites patch-diverge")
	hubSHA := runGitCmd(t, repos.trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, repos.trunkDir, "checkout", "-q", "main")

	// Fork rewrite: a different clone force-pushes a different rewrite of the
	// same branch to the fork, simulating an operator's force-push from
	// another machine.
	forkClone := filepath.Join(t.TempDir(), "fork-clone")
	runGitCmd(t, "", "clone", "-q", repos.originBare, forkClone)
	configGitUserCmd(t, forkClone)
	runGitCmd(t, forkClone, "checkout", "-q", "patch-diverge")
	writeFileHelper(t, filepath.Join(forkClone, "patch-diverge.txt"), "fork rewrite\n")
	runGitCmd(t, forkClone, "add", ".")
	runGitCmd(t, forkClone, "commit", "-q", "-m", "fork rewrites patch-diverge")
	forkSHA := runGitCmd(t, forkClone, "rev-parse", "HEAD")
	runGitCmd(t, forkClone, "push", "-q", "-f", "origin", "patch-diverge")

	seedWorkspaceCarryPatch(t, env.db, slug, "alice", repos.upstreamBare, "", "integration", "")
	seedForkSyncPatch(t, env.db, "patch-1", slug, "patch-diverge", 1, PatchStatusActive)

	auth := forkSyncAuth("alice")
	rec := doRebuildAuditRequest(t, env.echo, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync returned %d; want 200. body: %s", rec.Code, rec.Body.String())
	}

	// refs/hub/replaced/patch-diverge now holds the old local tip.
	backupSHA := runGitCmd(t, repos.trunkDir, "rev-parse", "--verify", "refs/hub/replaced/patch-diverge")
	if backupSHA != hubSHA {
		t.Errorf("refs/hub/replaced/patch-diverge = %s; want %s (old hub tip)", backupSHA, hubSHA)
	}

	// refs/heads/patch-diverge now points at the fork's tip.
	newLocalSHA := runGitCmd(t, repos.trunkDir, "rev-parse", "--verify", "refs/heads/patch-diverge")
	if newLocalSHA != forkSHA {
		t.Errorf("refs/heads/patch-diverge = %s; want %s (fork tip)", newLocalSHA, forkSHA)
	}

	// hub.patch.replace and hub.patch.sync audit events were recorded.
	var replaceEvt, syncEvt bool
	for _, ev := range env.audit.Events() {
		switch ev.EventType {
		case "hub.patch.replace":
			replaceEvt = true
			if ev.Metadata["branch_name"] != "patch-diverge" {
				t.Errorf("hub.patch.replace branch_name = %v; want patch-diverge", ev.Metadata["branch_name"])
			}
			if ev.Metadata["replaced_sha"] != hubSHA {
				t.Errorf("hub.patch.replace replaced_sha = %v; want %s", ev.Metadata["replaced_sha"], hubSHA)
			}
			if ev.Metadata["origin_sha"] != forkSHA {
				t.Errorf("hub.patch.replace origin_sha = %v; want %s", ev.Metadata["origin_sha"], forkSHA)
			}
		case "hub.patch.sync":
			syncEvt = true
		}
	}
	if !replaceEvt {
		t.Error("expected a hub.patch.replace audit event, got none")
	}
	if !syncEvt {
		t.Error("expected a hub.patch.sync audit event, got none")
	}

	// The sync response's patches_synced has the replaced entry and
	// rebuild_triggered=true.
	var resp CarryPatchSyncResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode sync response: %v (body: %s)", err, rec.Body.String())
	}
	if len(resp.PatchesSynced) != 1 {
		t.Fatalf("patches_synced = %+v; want exactly one entry", resp.PatchesSynced)
	}
	entry := resp.PatchesSynced[0]
	if entry.Action != "replaced" {
		t.Errorf("patches_synced[0].action = %q; want %q", entry.Action, "replaced")
	}
	if entry.ReplacedSHA != hubSHA {
		t.Errorf("patches_synced[0].replaced_sha = %q; want %q", entry.ReplacedSHA, hubSHA)
	}
	if entry.OriginSHA != forkSHA {
		t.Errorf("patches_synced[0].origin_sha = %q; want %q", entry.OriginSHA, forkSHA)
	}
	if !resp.RebuildTriggered {
		t.Error("rebuild_triggered = false; want true")
	}

	// The patches row persists origin_sync_state/origin_sha/origin_synced_at.
	var originSyncState, originSHA, originSyncedAt sql.NullString
	if err := env.db.QueryRow(
		`SELECT origin_sync_state, origin_sha, origin_synced_at FROM patches WHERE id = 'patch-1'`,
	).Scan(&originSyncState, &originSHA, &originSyncedAt); err != nil {
		t.Fatalf("query patch row: %v", err)
	}
	if !originSyncState.Valid || originSyncState.String != "in_sync" {
		t.Errorf("persisted origin_sync_state = %v; want \"in_sync\"", originSyncState)
	}
	if !originSHA.Valid || originSHA.String != forkSHA {
		t.Errorf("persisted origin_sha = %v; want %q", originSHA, forkSHA)
	}
	if !originSyncedAt.Valid || originSyncedAt.String == "" {
		t.Error("persisted origin_synced_at is not set")
	}
}

// ===========================================================================
// TS-02-34 (smoke): A hub-mode workspace is unaffected by this spec and
// clears stale fork-sync state left from a prior origin-mode run
//
// Verifies: 02-PATH-3, 02-REQ-6.3, 02-REQ-4.3
// ===========================================================================

func TestSmoke_TS02_34_HubModeUnaffectedAndClearsStaleForkState(t *testing.T) {
	slug := "cp-hub-mode-clear"

	branchSource := "origin"
	getVar := func(_, _, key string) (string, error) {
		switch key {
		case "PATCH_BRANCH_SOURCE":
			return branchSource, nil
		case "AUTO_REBUILD_AFTER_SYNC":
			return "true", nil
		case "REBUILD_STRATEGY":
			return "rebase", nil
		}
		return "", nil
	}
	env := newForkSyncSmokeEnv(t, getVar)

	repos := setupForkSyncRepos(t, env.workspaceRoot, slug)

	// A patch branch kept in sync between hub and fork (pushed, kept locally
	// too) so the first, origin-mode sync records non-null fork-sync state
	// without needing any ref movement.
	runGitCmd(t, repos.trunkDir, "checkout", "-q", "-b", "patch-steady")
	writeFileHelper(t, filepath.Join(repos.trunkDir, "patch-steady.txt"), "steady content\n")
	runGitCmd(t, repos.trunkDir, "add", ".")
	runGitCmd(t, repos.trunkDir, "commit", "-q", "-m", "patch-steady change")
	runGitCmd(t, repos.trunkDir, "push", "-q", "origin", "patch-steady")
	runGitCmd(t, repos.trunkDir, "checkout", "-q", "main")

	seedWorkspaceCarryPatch(t, env.db, slug, "alice", repos.upstreamBare, "", "integration", "")
	seedForkSyncPatch(t, env.db, "patch-1", slug, "patch-steady", 1, PatchStatusActive)

	auth := forkSyncAuth("alice")

	// First sync: origin mode. Establishes non-null origin_sync_state.
	rec1 := doRebuildAuditRequest(t, env.echo, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first (origin-mode) POST /sync returned %d; want 200. body: %s", rec1.Code, rec1.Body.String())
	}

	var lastSyncBefore sql.NullString
	if err := env.db.QueryRow(`SELECT last_sync_at FROM workspaces WHERE slug = ?`, slug).Scan(&lastSyncBefore); err != nil {
		t.Fatalf("query last_sync_at before: %v", err)
	}

	var originSyncStateBefore sql.NullString
	if err := env.db.QueryRow(`SELECT origin_sync_state FROM patches WHERE id = 'patch-1'`).Scan(&originSyncStateBefore); err != nil {
		t.Fatalf("query origin_sync_state before: %v", err)
	}
	if !originSyncStateBefore.Valid {
		t.Fatal("expected non-null origin_sync_state after the origin-mode sync")
	}

	// Switch to hub mode (PATCH_BRANCH_SOURCE left unset) and re-sync.
	// apikit.NowUTC() has one-second resolution, so sleep past a second
	// boundary to guarantee a distinct last_sync_at.
	branchSource = ""
	time.Sleep(1100 * time.Millisecond)

	rec2 := doRebuildAuditRequest(t, env.echo, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second (hub-mode) POST /sync returned %d; want 200. body: %s", rec2.Code, rec2.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec2.Body.Bytes(), &raw); err != nil {
		t.Fatalf("failed to decode second sync response: %v", err)
	}
	if _, present := raw["patches_synced"]; present {
		t.Error("hub-mode sync response has a patches_synced field; want it absent")
	}

	var resp2 CarryPatchSyncResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("failed to decode second sync response: %v", err)
	}
	if resp2.OriginFetched {
		t.Error("hub-mode sync response origin_fetched = true; want false")
	}

	// last_sync_at is updated even though upstream did not advance.
	var lastSyncAfter sql.NullString
	if err := env.db.QueryRow(`SELECT last_sync_at FROM workspaces WHERE slug = ?`, slug).Scan(&lastSyncAfter); err != nil {
		t.Fatalf("query last_sync_at after: %v", err)
	}
	if !lastSyncAfter.Valid || lastSyncAfter.String == "" {
		t.Fatal("last_sync_at is not set after hub-mode sync")
	}
	if lastSyncAfter.String == lastSyncBefore.String {
		t.Errorf("last_sync_at did not change across the hub-mode sync (before=%q after=%q)",
			lastSyncBefore.String, lastSyncAfter.String)
	}

	// GET /workspaces/:slug/patches shows the three fork-sync fields as null.
	recPatches := doRebuildAuditRequest(t, env.echo, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches", "", auth)
	if recPatches.Code != http.StatusOK {
		t.Fatalf("GET /patches returned %d; want 200. body: %s", recPatches.Code, recPatches.Body.String())
	}
	var patchList []map[string]any
	if err := json.Unmarshal(recPatches.Body.Bytes(), &patchList); err != nil {
		t.Fatalf("failed to decode patches list: %v", err)
	}
	if len(patchList) != 1 {
		t.Fatalf("patches list = %+v; want exactly one entry", patchList)
	}
	for _, field := range []string{"origin_sync_state", "origin_sha", "origin_synced_at"} {
		val, ok := patchList[0][field]
		if !ok {
			t.Errorf("GET /patches response is missing field %q", field)
			continue
		}
		if val != nil {
			t.Errorf("GET /patches field %q = %v; want null", field, val)
		}
	}
}
