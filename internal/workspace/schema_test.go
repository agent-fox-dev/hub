package workspace

import (
	"database/sql"
	"testing"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// columns lists the columns of table.
func columns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	cols, err := existingColumns(db, table)
	if err != nil {
		t.Fatal(err)
	}
	return cols
}

// TestSchema_Fresh: InitSchema creates the workspaces, patches and binding
// tables with every column, is idempotent, and refuses a nil or closed
// database.
func TestSchema_Fresh(t *testing.T) {
	db := testutil.OpenDB(t)
	for i := 0; i < 2; i++ {
		if err := InitSchema(db); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	ws := columns(t, db, "workspaces")
	for _, c := range []string{"slug", "git_url", "branch", "owner_id", "org_id", "status", "display_name", "description", "clone_status",
		"head_sha", "clone_error", "created_at", "updated_at", "sync_mode", "sync_status", "upstream_head_sha", "last_sync_at", "sync_error",
		"workspace_mode", "upstream_url", "integration_branch", "settings"} {
		if !ws[c] {
			t.Errorf("workspaces lacks %s", c)
		}
	}
	patches := columns(t, db, "patches")
	for _, c := range []string{"id", "workspace_slug", "branch_name", "position", "status", "conflict_files", "upstream_pr_url", "description",
		"deleted_at", "added_at", "updated_at", "origin_sync_state", "origin_sha", "origin_synced_at"} {
		if !patches[c] {
			t.Errorf("patches lacks %s", c)
		}
	}
	if !columns(t, db, "token_workspace_bindings")["token_id"] {
		t.Error("the binding table is missing")
	}
	// The constraints of the tables.
	if _, err := db.Exec(`INSERT INTO workspaces (slug, git_url, owner_id, clone_status, created_at, updated_at) VALUES ('x', 'u', 'o', 'odd', '', '')`); err == nil {
		t.Error("an unknown clone_status was accepted")
	}
	testutil.Exec(t, db, `INSERT INTO patches (id, workspace_slug, branch_name, position, added_at, updated_at) VALUES ('p1', 'w', 'b', 1, '', '')`)
	if _, err := db.Exec(`INSERT INTO patches (id, workspace_slug, branch_name, position, added_at, updated_at) VALUES ('p2', 'w', 'b', 2, '', '')`); err == nil {
		t.Error("a duplicate branch of one workspace was accepted")
	}
	if _, err := db.Exec(`INSERT INTO patches (id, workspace_slug, branch_name, position, added_at, updated_at) VALUES ('p3', 'w', 'c', 1, '', '')`); err == nil {
		t.Error("a duplicate position of one workspace was accepted")
	}
	testutil.Exec(t, db, `INSERT INTO patches (id, workspace_slug, branch_name, position, added_at, updated_at) VALUES ('p4', 'v', 'b', 1, '', '')`)

	if err := InitSchema(nil); err == nil {
		t.Error("nil database accepted")
	}
	closed := testutil.OpenDB(t)
	closed.Close()
	if err := InitSchema(closed); err == nil {
		t.Error("closed database accepted")
	}
}

// TestSchema_MigratesOldTable: a workspaces table of the first shape, with the
// status columns only, gets the sync, carry-patch and settings columns added
// with their defaults; a row whose added columns are NULL reads with the
// defaults, as does one with an empty settings document.
func TestSchema_MigratesOldTable(t *testing.T) {
	db := testutil.OpenDB(t)
	testutil.Exec(t, db, `CREATE TABLE workspaces (
		slug TEXT PRIMARY KEY, git_url TEXT NOT NULL, branch TEXT, owner_id TEXT NOT NULL, org_id TEXT,
		status TEXT NOT NULL DEFAULT 'active', display_name TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '',
		clone_status TEXT NOT NULL DEFAULT 'pending', head_sha TEXT, clone_error TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`)
	// Two columns a hand-run migration added without NOT NULL.
	testutil.Exec(t, db, `ALTER TABLE workspaces ADD COLUMN sync_mode TEXT`)
	testutil.Exec(t, db, `ALTER TABLE workspaces ADD COLUMN workspace_mode TEXT`)
	testutil.Exec(t, db, `INSERT INTO workspaces (slug, git_url, owner_id, created_at, updated_at) VALUES ('old', 'https://h/p', 'o', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	testutil.Exec(t, db, `CREATE TABLE patches (id TEXT PRIMARY KEY, workspace_slug TEXT NOT NULL, branch_name TEXT NOT NULL, position INTEGER NOT NULL,
		status TEXT NOT NULL DEFAULT 'active', upstream_pr_url TEXT, description TEXT, added_at TEXT NOT NULL, updated_at TEXT NOT NULL)`)

	if err := InitSchema(db); err != nil {
		t.Fatal(err)
	}
	ws := columns(t, db, "workspaces")
	for _, c := range []string{"sync_mode", "sync_status", "upstream_head_sha", "last_sync_at", "sync_error", "workspace_mode", "upstream_url", "integration_branch", "settings"} {
		if !ws[c] {
			t.Errorf("workspaces lacks %s after the migration", c)
		}
	}
	for _, c := range []string{"conflict_files", "deleted_at", "origin_sync_state", "origin_sha", "origin_synced_at"} {
		if !columns(t, db, "patches")[c] {
			t.Errorf("patches lacks %s after the migration", c)
		}
	}
	row, err := getWorkspaceBySlug(db, "old")
	if err != nil {
		t.Fatal(err)
	}
	if row.SyncMode != "pull_only" || row.SyncStatus != "idle" || row.WorkspaceMode != "standard" || row.Settings != DefaultSettings() || row.Status != "active" || row.CloneStatus != "pending" {
		t.Errorf("old row %+v", row)
	}
	testutil.Exec(t, db, `UPDATE workspaces SET settings = NULL WHERE slug = 'old'`)
	if row, _ := getWorkspaceBySlug(db, "old"); row.Settings != DefaultSettings() {
		t.Errorf("NULL settings read as %+v", row.Settings)
	}
	body := workspaceResponse(&Workspace{Slug: "x"}, nil, "owner")
	if body["sync_mode"] != "pull_only" || body["sync_status"] != "idle" || body["workspace_mode"] != "standard" {
		t.Errorf("response of an empty row: %v", body)
	}
	if extractColumnName("CREATE TABLE x") != "" || extractColumnName("ALTER TABLE t ADD COLUMN c") != "c" || !isDuplicateColumnError(sqlError("duplicate column name: c")) || isDuplicateColumnError(nil) {
		t.Error("the DDL helpers")
	}
}

type sqlError string

func (e sqlError) Error() string { return string(e) }
