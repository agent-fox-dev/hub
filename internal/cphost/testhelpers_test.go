package cphost

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/aflib/jobqueue"
	"github.com/agent-fox-dev/carrypatch"

	"github.com/agent-fox-dev/hub/internal/audit"

	_ "modernc.org/sqlite"
)

// The tests of this package drive the carry-patch module's git hooks through
// the hub's git server, as the hub wires them.

func TestMain(m *testing.M) {
	// Fixtures init repos without naming the branch and push main.
	os.Setenv("GIT_CONFIG_COUNT", "1")
	os.Setenv("GIT_CONFIG_KEY_0", "init.defaultBranch")
	os.Setenv("GIT_CONFIG_VALUE_0", "main")
	os.Exit(m.Run())
}

// hubAudit adapts a recorder of the module's events to the hub's audit
// emitter, so one recorder sees the git server's events and the module's.
type hubAudit struct {
	rec carrypatch.Emitter
}

func (h hubAudit) Emit(ctx context.Context, ev audit.HubEvent) error {
	return h.rec.Emit(ctx, carrypatch.Event{
		EventType: ev.EventType, ActorID: ev.ActorID, ActorType: ev.ActorType,
		ResourceType: ev.ResourceType, ResourceID: ev.ResourceID, Action: ev.Action,
		Workspace: ev.Workspace, Metadata: ev.Metadata,
	})
}

// recordingEmitter records the module's events for assertions.
type recordingEmitter struct {
	mu     sync.Mutex
	events []carrypatch.Event
	err    error // if set, Emit returns this error
}

func (e *recordingEmitter) Emit(_ context.Context, event carrypatch.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
	return e.err
}

func (e *recordingEmitter) Events() []carrypatch.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	cp := make([]carrypatch.Event, len(e.events))
	copy(cp, e.events)
	return cp
}

func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// openTestDB opens an in-memory SQLite database on one connection.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("failed to set WAL mode: %v", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		t.Fatalf("failed to set busy_timeout: %v", err)
	}
	return db
}

// createWorkspacesTable creates the workspaces table with the columns the
// hub's schema has.
func createWorkspacesTable(t *testing.T, db *sql.DB) {
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

// createPatchesTable creates the patches table with the columns the hub's
// schema has.
func createPatchesTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS patches (
			id                TEXT PRIMARY KEY,
			workspace_slug    TEXT NOT NULL,
			branch_name       TEXT NOT NULL,
			position          INTEGER NOT NULL,
			status            TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','conflict','disabled','merged_upstream','deleted')),
			conflict_files    TEXT,
			upstream_pr_url   TEXT,
			description       TEXT,
			deleted_at        TEXT,
			added_at          TEXT NOT NULL,
			updated_at        TEXT NOT NULL,
			origin_sync_state TEXT,
			origin_sha        TEXT,
			origin_synced_at  TEXT,
			FOREIGN KEY (workspace_slug) REFERENCES workspaces(slug)
		)`)
	if err != nil {
		t.Fatalf("failed to create patches table: %v", err)
	}
}

// seedWorkspaceWithGitURL inserts a workspace row.
func seedWorkspaceWithGitURL(t *testing.T, db *sql.DB, slug, ownerID, status, cloneStatus, mode, integrationBranch, gitURL string) {
	t.Helper()
	now := "2024-01-01T00:00:00Z"
	_, err := db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, workspace_mode, integration_branch, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		slug, gitURL, ownerID, status, cloneStatus, mode, integrationBranch, now, now,
	)
	if err != nil {
		t.Fatalf("seedWorkspaceWithGitURL(%q) returned error: %v", slug, err)
	}
}

// seedPatch inserts a patch row.
func seedPatch(t *testing.T, db *sql.DB, id, workspaceSlug, branchName string, position int, status string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, workspaceSlug, branchName, position, status, now, now,
	)
	if err != nil {
		t.Fatalf("seedPatch(%q) returned error: %v", id, err)
	}
}

// writeFileHelper writes a file, creating its directory.
func writeFileHelper(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// mirrorTestEnv is a carry_patch workspace whose trunk has a local bare
// origin, with a job queue that accepts rebuild jobs.
type mirrorTestEnv struct {
	db            *sql.DB
	queue         *jobqueue.Queue
	workspaceRoot string
	bareOrigin    string // path to the local bare origin repo
	trunk         string // path to the workspace trunk repo
}

func newMirrorTestEnv(t *testing.T) *mirrorTestEnv {
	t.Helper()

	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)

	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey: %v", err)
	}

	q, err := jobqueue.New(db, nopLogger())
	if err != nil {
		t.Fatalf("jobqueue.New: %v", err)
	}
	if err := q.Register("rebuild", func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return nil, false, nil
	}, nil); err != nil {
		t.Fatalf("register rebuild: %v", err)
	}

	wsRoot := t.TempDir()

	bareOrigin := t.TempDir()
	runGitCmdR(t, "", "init", "--bare", "-b", "main", bareOrigin)

	trunkPath := filepath.Join(wsRoot, "myws", "trunk")
	if err := os.MkdirAll(trunkPath, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	runGitCmdR(t, "", "init", "-b", "main", trunkPath)
	runGitCmdR(t, trunkPath, "config", "user.name", "Test")
	runGitCmdR(t, trunkPath, "config", "user.email", "test@test.com")
	writeFileHelper(t, filepath.Join(trunkPath, "README.md"), "# test\n")
	runGitCmdR(t, trunkPath, "add", ".")
	runGitCmdR(t, trunkPath, "commit", "-m", "init")
	runGitCmdR(t, trunkPath, "remote", "add", "origin", bareOrigin)
	runGitCmdR(t, trunkPath, "push", "origin", "main")

	seedWorkspaceWithGitURL(t, db, "myws", "user-1", "active", "ready",
		"carry_patch", "main-int", bareOrigin)

	return &mirrorTestEnv{
		db:            db,
		queue:         q,
		workspaceRoot: wsRoot,
		bareOrigin:    bareOrigin,
		trunk:         trunkPath,
	}
}

// mirrorRefExists reports whether a ref exists in a git repo.
func mirrorRefExists(t *testing.T, repoPath, ref string) bool {
	t.Helper()
	_, err := gitCmdOutR(repoPath, "rev-parse", "--verify", ref)
	return err == nil
}
