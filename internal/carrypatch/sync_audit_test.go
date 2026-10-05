package carrypatch

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// Audit test helpers
// ===========================================================================

// auditSyncTestEnv is a test environment for audit emission tests during
// origin-mode sync.
type auditSyncTestEnv struct {
	echo          *echo.Echo
	db            *sql.DB
	queue         *jobqueue.Queue
	workspaceRoot string
	gitRunner     *mockGitRunner
	patchStore    *mockPatchStore
	emitter       *cpAuditEmitter

	mu        sync.Mutex
	variables map[string]string
}

func newAuditSyncTestEnv(t *testing.T, emitter audit.Emitter) *auditSyncTestEnv {
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

	env := &auditSyncTestEnv{
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
		FetchOrigin: func(_ context.Context, _ string, _ transport.AuthMethod) error {
			return nil
		},
		ResolveOriginAuth: func(_ string) (transport.AuthMethod, error) {
			return nil, nil
		},
		Audit: emitter,
	}
	RegisterSyncRoutes(api, syncCfg)

	env.echo = e
	return env
}

func (env *auditSyncTestEnv) setVar(key, value string) {
	env.mu.Lock()
	defer env.mu.Unlock()
	env.variables[key] = value
}

func (env *auditSyncTestEnv) doSync(t *testing.T) *httptest.ResponseRecorder {
	return env.doSyncAs(t, "alice")
}

func (env *auditSyncTestEnv) doSyncAs(t *testing.T, userID string) *httptest.ResponseRecorder {
	t.Helper()
	auth := rebuildUserAuth(userID)
	authJSON, _ := json.Marshal(auth)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/my-workspace/sync", nil)
	req.Header.Set("X-Test-Auth", string(authJSON))
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	return rec
}

// eventsByType filters events by event type.
func eventsByType(events []audit.HubEvent, eventType string) []audit.HubEvent {
	var result []audit.HubEvent
	for _, e := range events {
		if e.EventType == eventType {
			result = append(result, e)
		}
	}
	return result
}

// ===========================================================================
// TS-20-47 (integration): A mixed origin-mode sync emits one hub.patch.sync
// event with the branch lists and the standard actor, resource and slug
//
// Verifies: 20-REQ-8.1, 20-REQ-8.3
// ===========================================================================

func TestSyncAudit_MixedOriginSync_EmitsPatchSyncEvent_TS2047(t *testing.T) {
	emitter := newCPAuditEmitter()
	env := newAuditSyncTestEnv(t, emitter)

	// Set origin mode.
	env.setVar("PATCH_BRANCH_SOURCE", "origin")

	// Seed workspace.
	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream", "abc123", "integration", "")

	// Set up a real git repo for the trunk.
	trunkDir := filepath.Join(env.workspaceRoot, "my-workspace", "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	runGitCmd(t, "", "init", "-b", "main", trunkDir)
	configGitUserCmd(t, trunkDir)
	writeFileHelper(t, filepath.Join(trunkDir, "file.txt"), "hello")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "initial")
	upstreamSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/remotes/upstream/HEAD", upstreamSHA)

	// Update workspace with correct upstream SHA.
	_, _ = env.db.Exec(`UPDATE workspaces SET upstream_head_sha = ? WHERE slug = ?`, upstreamSHA, "my-workspace")

	// Create branches for different outcomes:
	// 1. "created-branch" - only on origin (will be created)
	runGitCmd(t, trunkDir, "checkout", "-b", "temp-created")
	writeFileHelper(t, filepath.Join(trunkDir, "created.txt"), "created")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "created branch commit")
	createdSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/remotes/origin/created-branch", createdSHA)
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "branch", "-D", "temp-created")

	// 2. "ff-branch" - local is behind origin (will be fast-forwarded)
	runGitCmd(t, trunkDir, "checkout", "-b", "ff-branch")
	writeFileHelper(t, filepath.Join(trunkDir, "ff.txt"), "ff base")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "ff base")
	runGitCmd(t, trunkDir, "checkout", "-b", "temp-ff-ahead")
	writeFileHelper(t, filepath.Join(trunkDir, "ff2.txt"), "ff ahead")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "ff ahead")
	ffOriginSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/remotes/origin/ff-branch", ffOriginSHA)
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "branch", "-D", "temp-ff-ahead")

	// 3. "replaced-branch" - diverged, will be replaced (default policy)
	runGitCmd(t, trunkDir, "checkout", "-b", "replaced-branch")
	writeFileHelper(t, filepath.Join(trunkDir, "replaced-local.txt"), "local")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "local diverge")
	runGitCmd(t, trunkDir, "checkout", "main")
	// Create diverged origin ref
	runGitCmd(t, trunkDir, "checkout", "-b", "temp-replaced-origin")
	writeFileHelper(t, filepath.Join(trunkDir, "replaced-origin.txt"), "origin")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "origin diverge")
	replacedOriginSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/remotes/origin/replaced-branch", replacedOriginSHA)
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "branch", "-D", "temp-replaced-origin")

	// 4. "missing-branch" - local exists but not on origin
	runGitCmd(t, trunkDir, "checkout", "-b", "missing-branch")
	writeFileHelper(t, filepath.Join(trunkDir, "missing.txt"), "missing")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "missing commit")
	runGitCmd(t, trunkDir, "checkout", "main")

	// Use a real git runner.
	runner := newRealGitRunner(t, trunkDir)

	// Re-create echo with real git runner.
	patchStore := NewSQLPatchStore(env.db)
	getVar := func(scope, slug, key string) (string, error) {
		env.mu.Lock()
		defer env.mu.Unlock()
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
		WorkspaceRoot: env.workspaceRoot,
		NewGitRunner: func(_ string) (GitRunner, error) {
			return runner, nil
		},
		Fetch:             func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveAuth:       func(_ string) (transport.AuthMethod, error) { return nil, nil },
		GetVariable:       getVar,
		PatchStore:        patchStore,
		FetchOrigin:       func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveOriginAuth: func(_ string) (transport.AuthMethod, error) { return nil, nil },
		Audit:             emitter,
	}
	RegisterSyncRoutes(api, syncCfg)
	env.echo = e

	// Seed patches.
	seedPatch(t, env.db, "p-created", "my-workspace", "created-branch", 1, PatchStatusActive)
	seedPatch(t, env.db, "p-ff", "my-workspace", "ff-branch", 2, PatchStatusActive)
	seedPatch(t, env.db, "p-replaced", "my-workspace", "replaced-branch", 3, PatchStatusActive)
	seedPatch(t, env.db, "p-missing", "my-workspace", "missing-branch", 4, PatchStatusActive)

	rec := env.doSyncAs(t, "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want 200; body = %s", rec.Code, rec.Body.String())
	}

	// Check audit events.
	events := emitter.Events()
	syncEvents := eventsByType(events, audit.EventPatchSync)
	if len(syncEvents) != 1 {
		t.Fatalf("expected exactly 1 hub.patch.sync event, got %d (all events: %v)", len(syncEvents), events)
	}

	ev := syncEvents[0]

	// Check actor.
	if ev.ActorID != "alice" {
		t.Errorf("actor_id: want %q, got %q", "alice", ev.ActorID)
	}
	if ev.ActorType != "api_key" {
		t.Errorf("actor_type: want %q, got %q", "api_key", ev.ActorType)
	}

	// Check resource_type, resource_id, action and workspace. Like every
	// other hub emitter, the event sets ResourceID and Action; the sync
	// covers the whole workspace, so its resource is the slug.
	if ev.ResourceType != "patch" {
		t.Errorf("resource_type: want %q, got %q", "patch", ev.ResourceType)
	}
	if ev.ResourceID != "my-workspace" {
		t.Errorf("resource_id: want %q, got %q", "my-workspace", ev.ResourceID)
	}
	if ev.Action != "sync" {
		t.Errorf("action: want %q, got %q", "sync", ev.Action)
	}
	if ev.Workspace != "my-workspace" {
		t.Errorf("workspace: want %q, got %q", "my-workspace", ev.Workspace)
	}

	// Check metadata.
	meta := ev.Metadata
	if meta == nil {
		t.Fatal("metadata is nil")
	}

	if originFetched, ok := meta["origin_fetched"].(bool); !ok || !originFetched {
		t.Errorf("metadata.origin_fetched: want true, got %v", meta["origin_fetched"])
	}

	// Check branch lists.
	assertStringList(t, meta, "created", []string{"created-branch"})
	assertStringList(t, meta, "fast_forwarded", []string{"ff-branch"})
	assertStringList(t, meta, "replaced", []string{"replaced-branch"})
	assertStringList(t, meta, "missing_on_origin", []string{"missing-branch"})

	// diverged should be empty since we used replace policy.
	assertStringList(t, meta, "diverged", []string{})
}

// assertStringList checks that a metadata key holds the expected list of strings.
func assertStringList(t *testing.T, meta map[string]any, key string, expected []string) {
	t.Helper()
	raw, ok := meta[key]
	if !ok {
		t.Errorf("metadata missing key %q", key)
		return
	}
	list, ok := raw.([]string)
	if !ok {
		// Try []any (JSON round-trip).
		if anyList, ok2 := raw.([]any); ok2 {
			list = make([]string, len(anyList))
			for i, v := range anyList {
				list[i] = fmt.Sprintf("%v", v)
			}
		} else {
			t.Errorf("metadata[%q]: expected []string, got %T", key, raw)
			return
		}
	}
	if len(list) != len(expected) {
		t.Errorf("metadata[%q]: want %v, got %v", key, expected, list)
		return
	}
	for i, want := range expected {
		if list[i] != want {
			t.Errorf("metadata[%q][%d]: want %q, got %q", key, i, want, list[i])
		}
	}
}

// ===========================================================================
// TS-20-48 (integration): Each replaced branch emits a hub.patch.replace
// event and an info log with slug, branch and both SHAs
//
// Verifies: 20-REQ-8.2, 20-REQ-8.3
// ===========================================================================

func TestSyncAudit_ReplacedBranches_EmitPatchReplaceEvents_TS2048(t *testing.T) {
	emitter := newCPAuditEmitter()

	// Capture log output.
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	origLogger := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(origLogger)

	// Set up a real git repo.
	wsRoot := t.TempDir()
	trunkDir := filepath.Join(wsRoot, "my-workspace", "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	runGitCmd(t, "", "init", "-b", "main", trunkDir)
	configGitUserCmd(t, trunkDir)
	writeFileHelper(t, filepath.Join(trunkDir, "file.txt"), "hello")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "initial")
	upstreamSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/remotes/upstream/HEAD", upstreamSHA)

	// Create two diverged branches.
	// Branch "div-a": local diverges from origin.
	runGitCmd(t, trunkDir, "checkout", "-b", "div-a")
	writeFileHelper(t, filepath.Join(trunkDir, "div-a-local.txt"), "local a")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "local a")
	localTipA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "checkout", "-b", "temp-div-a-origin")
	writeFileHelper(t, filepath.Join(trunkDir, "div-a-origin.txt"), "origin a")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "origin a")
	originTipA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/remotes/origin/div-a", originTipA)
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "branch", "-D", "temp-div-a-origin")

	// Branch "div-b": local diverges from origin.
	runGitCmd(t, trunkDir, "checkout", "-b", "div-b")
	writeFileHelper(t, filepath.Join(trunkDir, "div-b-local.txt"), "local b")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "local b")
	localTipB := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "checkout", "-b", "temp-div-b-origin")
	writeFileHelper(t, filepath.Join(trunkDir, "div-b-origin.txt"), "origin b")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "origin b")
	originTipB := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/remotes/origin/div-b", originTipB)
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "branch", "-D", "temp-div-b-origin")

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
	jqLogger := nopLogger()
	q, err := jobqueue.New(db, jqLogger)
	if err != nil {
		t.Fatalf("jobqueue.New: %v", err)
	}
	_ = RegisterRebuildJob(q, &RebuildHandler{})

	seedWorkspaceCarryPatch(t, db, "my-workspace", "alice",
		"https://github.com/example/upstream", upstreamSHA, "integration", "")

	seedPatch(t, db, "p-a", "my-workspace", "div-a", 1, PatchStatusActive)
	seedPatch(t, db, "p-b", "my-workspace", "div-b", 2, PatchStatusActive)

	runner := newRealGitRunner(t, trunkDir)
	patchStore := NewSQLPatchStore(db)

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
		GetVariable: func(scope, slug, key string) (string, error) {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
			return "", fmt.Errorf("not set")
		},
		PatchStore:        patchStore,
		FetchOrigin:       func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveOriginAuth: func(_ string) (transport.AuthMethod, error) { return nil, nil },
		Audit:             emitter,
	}
	RegisterSyncRoutes(api, syncCfg)

	auth := rebuildUserAuth("alice")
	authJSON, _ := json.Marshal(auth)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/my-workspace/sync", nil)
	req.Header.Set("X-Test-Auth", string(authJSON))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want 200; body = %s", rec.Code, rec.Body.String())
	}

	// Check hub.patch.replace events.
	events := emitter.Events()
	replaceEvents := eventsByType(events, audit.EventPatchReplace)
	if len(replaceEvents) != 2 {
		t.Fatalf("expected 2 hub.patch.replace events, got %d", len(replaceEvents))
	}

	// Check first replace event.
	for _, ev := range replaceEvents {
		if ev.ActorID != "alice" {
			t.Errorf("replace event actor_id: want %q, got %q", "alice", ev.ActorID)
		}
		if ev.ActorType != "api_key" {
			t.Errorf("replace event actor_type: want %q, got %q", "api_key", ev.ActorType)
		}
		if ev.ResourceType != "patch" {
			t.Errorf("replace event resource_type: want %q, got %q", "patch", ev.ResourceType)
		}
		if ev.Workspace != "my-workspace" {
			t.Errorf("replace event workspace: want %q, got %q", "my-workspace", ev.Workspace)
		}

		meta := ev.Metadata
		branchName, _ := meta["branch_name"].(string)

		// Same shape as the reset handler's hub.patch.replace event: the
		// branch is the resource and the action is "replace".
		if ev.ResourceID != branchName {
			t.Errorf("replace event resource_id: want %q, got %q", branchName, ev.ResourceID)
		}
		if ev.Action != "replace" {
			t.Errorf("replace event action: want %q, got %q", "replace", ev.Action)
		}
		replacedSHA, _ := meta["replaced_sha"].(string)
		originSHA, _ := meta["origin_sha"].(string)

		switch branchName {
		case "div-a":
			if replacedSHA != localTipA {
				t.Errorf("div-a replaced_sha: want %q, got %q", localTipA, replacedSHA)
			}
			if originSHA != originTipA {
				t.Errorf("div-a origin_sha: want %q, got %q", originTipA, originSHA)
			}
		case "div-b":
			if replacedSHA != localTipB {
				t.Errorf("div-b replaced_sha: want %q, got %q", localTipB, replacedSHA)
			}
			if originSHA != originTipB {
				t.Errorf("div-b origin_sha: want %q, got %q", originTipB, originSHA)
			}
		default:
			t.Errorf("unexpected branch_name in replace event: %q", branchName)
		}
	}

	// Check info log lines.
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "div-a") || !strings.Contains(logOutput, localTipA) {
		t.Errorf("expected info log for div-a with SHA %s, got: %s", localTipA, logOutput)
	}
	if !strings.Contains(logOutput, "div-b") || !strings.Contains(logOutput, localTipB) {
		t.Errorf("expected info log for div-b with SHA %s, got: %s", localTipB, logOutput)
	}
}

// ===========================================================================
// TS-20-49 (unit): Hub mode emits no hub.patch.sync event
//
// Verifies: 20-REQ-8.4
// ===========================================================================

func TestSyncAudit_HubMode_NoEvents_TS2049(t *testing.T) {
	emitter := newCPAuditEmitter()
	env := newAuditSyncTestEnv(t, emitter)

	// Hub mode (default, no PATCH_BRANCH_SOURCE set).
	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream", "abc123", "integration", "")

	// Set up mock git runner to return upstream SHA.
	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" {
			return "abc123", nil
		}
		return "", nil
	}

	// Sync with no upstream change.
	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want 200; body = %s", rec.Code, rec.Body.String())
	}

	events := emitter.Events()
	syncEvents := eventsByType(events, audit.EventPatchSync)
	if len(syncEvents) != 0 {
		t.Errorf("expected 0 hub.patch.sync events in hub mode, got %d", len(syncEvents))
	}
	replaceEvents := eventsByType(events, audit.EventPatchReplace)
	if len(replaceEvents) != 0 {
		t.Errorf("expected 0 hub.patch.replace events in hub mode, got %d", len(replaceEvents))
	}

	// Also test with upstream change.
	_, _ = env.db.Exec(`UPDATE workspaces SET upstream_head_sha = ? WHERE slug = ?`, "old-sha", "my-workspace")
	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" {
			return "new-sha", nil
		}
		return "", nil
	}

	rec2 := env.doSync(t)
	if rec2.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want 200; body = %s", rec2.Code, rec2.Body.String())
	}

	events2 := emitter.Events()
	syncEvents2 := eventsByType(events2, audit.EventPatchSync)
	if len(syncEvents2) != 0 {
		t.Errorf("expected 0 hub.patch.sync events in hub mode after upstream change, got %d", len(syncEvents2))
	}
}

// ===========================================================================
// TS-20-50 (integration): A ref-write failure still emits events for the
// outcomes already produced
//
// Verifies: 20-REQ-8.5
// ===========================================================================

func TestSyncAudit_RefWriteFailure_EmitsPartialEvents_TS2050(t *testing.T) {
	emitter := newCPAuditEmitter()

	// Set up a real git repo.
	wsRoot := t.TempDir()
	trunkDir := filepath.Join(wsRoot, "my-workspace", "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	runGitCmd(t, "", "init", "-b", "main", trunkDir)
	configGitUserCmd(t, trunkDir)
	writeFileHelper(t, filepath.Join(trunkDir, "file.txt"), "hello")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "initial")
	upstreamSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/remotes/upstream/HEAD", upstreamSHA)

	// Branch "a": diverged, will be replaced successfully.
	runGitCmd(t, trunkDir, "checkout", "-b", "branch-a")
	writeFileHelper(t, filepath.Join(trunkDir, "a-local.txt"), "local a")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "local a")
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "checkout", "-b", "temp-a-origin")
	writeFileHelper(t, filepath.Join(trunkDir, "a-origin.txt"), "origin a")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "origin a")
	originTipA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/remotes/origin/branch-a", originTipA)
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "branch", "-D", "temp-a-origin")

	// Branch "b": will have a fast-forward, but we'll make the CAS fail
	// by using a mock runner that wraps the real one.
	runGitCmd(t, trunkDir, "checkout", "-b", "branch-b")
	writeFileHelper(t, filepath.Join(trunkDir, "b.txt"), "b base")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "b base")
	runGitCmd(t, trunkDir, "checkout", "-b", "temp-b-ahead")
	writeFileHelper(t, filepath.Join(trunkDir, "b2.txt"), "b ahead")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "b ahead")
	bOriginSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/remotes/origin/branch-b", bOriginSHA)
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "branch", "-D", "temp-b-ahead")

	// Use a wrapper runner that fails the CAS for branch-b.
	realRunner := newRealGitRunner(t, trunkDir)
	callCount := 0
	wrapper := &casFailRunner{
		GitRunner:    realRunner,
		failBranch:   "branch-b",
		failOnNthCAS: 1,
		casCount:     &callCount,
	}

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
	jqLogger := nopLogger()
	q, err := jobqueue.New(db, jqLogger)
	if err != nil {
		t.Fatalf("jobqueue.New: %v", err)
	}
	_ = RegisterRebuildJob(q, &RebuildHandler{})

	seedWorkspaceCarryPatch(t, db, "my-workspace", "alice",
		"https://github.com/example/upstream", upstreamSHA, "integration", "")

	seedPatch(t, db, "p-a", "my-workspace", "branch-a", 1, PatchStatusActive)
	seedPatch(t, db, "p-b", "my-workspace", "branch-b", 2, PatchStatusActive)

	patchStore := NewSQLPatchStore(db)

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())
	syncCfg := SyncAPIConfig{
		DB:            db,
		Queue:         q,
		WorkspaceRoot: wsRoot,
		NewGitRunner: func(_ string) (GitRunner, error) {
			return wrapper, nil
		},
		Fetch:       func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveAuth: func(_ string) (transport.AuthMethod, error) { return nil, nil },
		GetVariable: func(scope, slug, key string) (string, error) {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
			return "", fmt.Errorf("not set")
		},
		PatchStore:        patchStore,
		FetchOrigin:       func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveOriginAuth: func(_ string) (transport.AuthMethod, error) { return nil, nil },
		Audit:             emitter,
	}
	RegisterSyncRoutes(api, syncCfg)

	auth := rebuildUserAuth("alice")
	authJSON, _ := json.Marshal(auth)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/my-workspace/sync", nil)
	req.Header.Set("X-Test-Auth", string(authJSON))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("sync status = %d; want 500; body = %s", rec.Code, rec.Body.String())
	}

	// Check that events for branch-a were still emitted.
	events := emitter.Events()
	syncEvents := eventsByType(events, audit.EventPatchSync)
	if len(syncEvents) != 1 {
		t.Fatalf("expected 1 hub.patch.sync event on ref-write failure, got %d", len(syncEvents))
	}

	meta := syncEvents[0].Metadata
	assertStringList(t, meta, "replaced", []string{"branch-a"})

	replaceEvents := eventsByType(events, audit.EventPatchReplace)
	if len(replaceEvents) != 1 {
		t.Fatalf("expected 1 hub.patch.replace event for branch-a, got %d", len(replaceEvents))
	}
	if replaceEvents[0].Metadata["branch_name"] != "branch-a" {
		t.Errorf("replace event branch_name: want %q, got %v", "branch-a", replaceEvents[0].Metadata["branch_name"])
	}
}

// casFailRunner wraps a real GitRunner but fails CAS update-ref calls for a
// specific branch after the first N successful CAS writes.
type casFailRunner struct {
	GitRunner
	failBranch   string
	failOnNthCAS int
	casCount     *int
}

func (r *casFailRunner) Run(ctx context.Context, args ...string) (string, error) {
	// Intercept update-ref calls for the target branch.
	if len(args) >= 3 && args[0] == "update-ref" && strings.Contains(args[1], r.failBranch) {
		*r.casCount++
		return "", fmt.Errorf("simulated CAS failure for %s", r.failBranch)
	}
	return r.GitRunner.Run(ctx, args...)
}

// ===========================================================================
// TS-20-51 (unit): A nil audit emitter skips emission and the sync completes
//
// Verifies: 20-REQ-8.6
// ===========================================================================

func TestSyncAudit_NilEmitter_SyncCompletes_TS2051(t *testing.T) {
	// Capture log output: a nil emitter skips emission, not the info log of
	// each replacement (20-REQ-8.2).
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	origLogger := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(origLogger)

	// Create env with nil audit emitter.
	env := newAuditSyncTestEnv(t, nil)

	env.setVar("PATCH_BRANCH_SOURCE", "origin")

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream", "abc123", "integration", "")

	// Set up a real git repo.
	trunkDir := filepath.Join(env.workspaceRoot, "my-workspace", "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	runGitCmd(t, "", "init", "-b", "main", trunkDir)
	configGitUserCmd(t, trunkDir)
	writeFileHelper(t, filepath.Join(trunkDir, "file.txt"), "hello")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "initial")
	upstreamSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/remotes/upstream/HEAD", upstreamSHA)
	_, _ = env.db.Exec(`UPDATE workspaces SET upstream_head_sha = ? WHERE slug = ?`, upstreamSHA, "my-workspace")

	// Create a diverged branch to trigger a replace.
	runGitCmd(t, trunkDir, "checkout", "-b", "patch-a")
	writeFileHelper(t, filepath.Join(trunkDir, "local.txt"), "local")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "local")
	localSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "checkout", "-b", "temp-origin")
	writeFileHelper(t, filepath.Join(trunkDir, "origin.txt"), "origin")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "origin")
	originSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/remotes/origin/patch-a", originSHA)
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "branch", "-D", "temp-origin")

	seedPatch(t, env.db, "p-a", "my-workspace", "patch-a", 1, PatchStatusActive)

	// Re-create echo with real git runner and nil audit.
	runner := newRealGitRunner(t, trunkDir)
	patchStore := NewSQLPatchStore(env.db)
	getVar := func(scope, slug, key string) (string, error) {
		env.mu.Lock()
		defer env.mu.Unlock()
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
		WorkspaceRoot: env.workspaceRoot,
		NewGitRunner: func(_ string) (GitRunner, error) {
			return runner, nil
		},
		Fetch:             func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveAuth:       func(_ string) (transport.AuthMethod, error) { return nil, nil },
		GetVariable:       getVar,
		PatchStore:        patchStore,
		FetchOrigin:       func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveOriginAuth: func(_ string) (transport.AuthMethod, error) { return nil, nil },
		Audit:             nil, // nil emitter
	}
	RegisterSyncRoutes(api, syncCfg)
	env.echo = e

	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want 200; body = %s", rec.Code, rec.Body.String())
	}

	// Parse response and verify patches_synced has the replaced action.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	patchesSynced, ok := resp["patches_synced"].([]any)
	if !ok || len(patchesSynced) == 0 {
		t.Fatalf("expected patches_synced array, got %v", resp["patches_synced"])
	}
	elem := patchesSynced[0].(map[string]any)
	if elem["action"] != "replaced" {
		t.Errorf("expected action=replaced, got %v", elem["action"])
	}

	// The replacement is still logged at info level with slug, branch and
	// both SHAs although there is no emitter.
	var found bool
	dec := json.NewDecoder(&logBuf)
	for dec.More() {
		var rec map[string]any
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("decode log record: %v", err)
		}
		if rec["msg"] != "patch branch replaced by origin" {
			continue
		}
		found = true
		if rec["level"] != "INFO" {
			t.Errorf("log level = %v; want INFO", rec["level"])
		}
		if rec["workspace"] != "my-workspace" || rec["branch"] != "patch-a" {
			t.Errorf("log record workspace/branch = %v/%v; want my-workspace/patch-a", rec["workspace"], rec["branch"])
		}
		if rec["replaced_sha"] != localSHA || rec["origin_sha"] != originSHA {
			t.Errorf("log record replaced_sha/origin_sha = %v/%v; want %s/%s",
				rec["replaced_sha"], rec["origin_sha"], localSHA, originSHA)
		}
	}
	if !found {
		t.Errorf("expected an info log \"patch branch replaced by origin\" with a nil emitter; got: %s", logBuf.String())
	}
}

// ===========================================================================
// TS-20-52 (unit): A failing emitter is logged and does not change the response
//
// Verifies: 20-REQ-8.7
// ===========================================================================

func TestSyncAudit_FailingEmitter_LoggedAndResponseUnchanged_TS2052(t *testing.T) {
	// Run 1: with a failing emitter.
	var logBuf1 bytes.Buffer
	logger1 := slog.New(slog.NewJSONHandler(&logBuf1, &slog.HandlerOptions{Level: slog.LevelInfo}))
	origLogger := slog.Default()
	slog.SetDefault(logger1)

	failEmitter := &failingCPAuditEmitter{}
	resp1Body := runSyncWithEmitter(t, failEmitter)

	slog.SetDefault(origLogger)

	// Run 2: with a succeeding emitter.
	okEmitter := newCPAuditEmitter()
	resp2Body := runSyncWithEmitter(t, okEmitter)

	// Compare status and body (ignoring timestamps which may differ).
	var resp1, resp2 map[string]any
	if err := json.Unmarshal(resp1Body, &resp1); err != nil {
		t.Fatalf("decode resp1: %v", err)
	}
	if err := json.Unmarshal(resp2Body, &resp2); err != nil {
		t.Fatalf("decode resp2: %v", err)
	}

	// Check key fields match.
	if resp1["origin_fetched"] != resp2["origin_fetched"] {
		t.Errorf("origin_fetched mismatch: %v vs %v", resp1["origin_fetched"], resp2["origin_fetched"])
	}
	if resp1["rebuild_triggered"] != resp2["rebuild_triggered"] {
		t.Errorf("rebuild_triggered mismatch: %v vs %v", resp1["rebuild_triggered"], resp2["rebuild_triggered"])
	}

	// Check that the error was logged.
	logOutput := logBuf1.String()
	if !strings.Contains(logOutput, "audit") {
		t.Errorf("expected audit error in log, got: %s", logOutput)
	}
}

// runSyncWithEmitter runs a sync with the given emitter and returns the response body.
func runSyncWithEmitter(t *testing.T, emitter audit.Emitter) []byte {
	t.Helper()

	wsRoot := t.TempDir()
	trunkDir := filepath.Join(wsRoot, "my-workspace", "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	runGitCmd(t, "", "init", "-b", "main", trunkDir)
	configGitUserCmd(t, trunkDir)
	writeFileHelper(t, filepath.Join(trunkDir, "file.txt"), "hello")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "initial")
	upstreamSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/remotes/upstream/HEAD", upstreamSHA)

	// Create a branch that will be fast-forwarded.
	runGitCmd(t, trunkDir, "checkout", "-b", "patch-x")
	writeFileHelper(t, filepath.Join(trunkDir, "x.txt"), "x base")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "x base")
	runGitCmd(t, trunkDir, "checkout", "-b", "temp-x-ahead")
	writeFileHelper(t, filepath.Join(trunkDir, "x2.txt"), "x ahead")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "x ahead")
	xOriginSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/remotes/origin/patch-x", xOriginSHA)
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "branch", "-D", "temp-x-ahead")

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
	jqLogger := nopLogger()
	q, err := jobqueue.New(db, jqLogger)
	if err != nil {
		t.Fatalf("jobqueue.New: %v", err)
	}
	_ = RegisterRebuildJob(q, &RebuildHandler{})

	seedWorkspaceCarryPatch(t, db, "my-workspace", "alice",
		"https://github.com/example/upstream", upstreamSHA, "integration", "")
	seedPatch(t, db, "p-x", "my-workspace", "patch-x", 1, PatchStatusActive)

	runner := newRealGitRunner(t, trunkDir)
	patchStore := NewSQLPatchStore(db)

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
		GetVariable: func(scope, slug, key string) (string, error) {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
			return "", fmt.Errorf("not set")
		},
		PatchStore:        patchStore,
		FetchOrigin:       func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveOriginAuth: func(_ string) (transport.AuthMethod, error) { return nil, nil },
		Audit:             emitter,
	}
	RegisterSyncRoutes(api, syncCfg)

	auth := rebuildUserAuth("alice")
	authJSON, _ := json.Marshal(auth)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/my-workspace/sync", nil)
	req.Header.Set("X-Test-Auth", string(authJSON))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want 200; body = %s", rec.Code, rec.Body.String())
	}

	return rec.Body.Bytes()
}
