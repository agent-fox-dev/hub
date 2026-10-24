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
	for _, c := range []string{"slug", "git_url", "branch", "owner_id", "org_id", "status", "display_name", "description",
		"head_sha", "clone_error", "created_at", "updated_at", "upstream_head_sha", "last_sync_at", "sync_error",
		"workspace_mode", "upstream_url", "integration_branch", "settings"} {
		if !ws[c] {
			t.Errorf("workspaces lacks %s", c)
		}
	}
	for _, c := range []string{"clone_status", "sync_status", "sync_mode"} {
		if ws[c] {
			t.Errorf("workspaces has the folded column %s", c)
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
	// The constraints of the tables: status is one of the five states.
	for _, status := range []string{"odd", "active"} {
		if _, err := db.Exec(`INSERT INTO workspaces (slug, git_url, owner_id, status, created_at, updated_at) VALUES ('x', 'u', 'o', ?, '', '')`, status); err == nil {
			t.Errorf("status %q was accepted", status)
		}
	}
	testutil.Exec(t, db, `INSERT INTO workspaces (slug, git_url, owner_id, created_at, updated_at) VALUES ('x', 'u', 'o', '', '')`)
	if ws, _ := getWorkspaceBySlug(db, "x"); ws.Status != "pending" {
		t.Errorf("default status %q; want pending", ws.Status)
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

// TestSchema_MigratesOldTable: a workspaces table of the first shape, with
// active/archived beside clone_status and nothing else, gets the sync,
// carry-patch and settings columns added with their defaults and its status
// folded; a row of it reads with the defaults, as does one with an empty
// settings document.
func TestSchema_MigratesOldTable(t *testing.T) {
	db := testutil.OpenDB(t)
	testutil.Exec(t, db, `CREATE TABLE workspaces (
		slug TEXT PRIMARY KEY, git_url TEXT NOT NULL, branch TEXT, owner_id TEXT NOT NULL, org_id TEXT,
		status TEXT NOT NULL DEFAULT 'active', display_name TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '',
		clone_status TEXT NOT NULL DEFAULT 'pending', head_sha TEXT, clone_error TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`)
	testutil.Exec(t, db, `INSERT INTO workspaces (slug, git_url, owner_id, created_at, updated_at) VALUES ('old', 'https://h/p', 'o', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	testutil.Exec(t, db, `CREATE TABLE patches (id TEXT PRIMARY KEY, workspace_slug TEXT NOT NULL, branch_name TEXT NOT NULL, position INTEGER NOT NULL,
		status TEXT NOT NULL DEFAULT 'active', upstream_pr_url TEXT, description TEXT, added_at TEXT NOT NULL, updated_at TEXT NOT NULL)`)

	if err := InitSchema(db); err != nil {
		t.Fatal(err)
	}
	ws := columns(t, db, "workspaces")
	for _, c := range []string{"upstream_head_sha", "last_sync_at", "sync_error", "workspace_mode", "upstream_url", "integration_branch", "settings"} {
		if !ws[c] {
			t.Errorf("workspaces lacks %s after the migration", c)
		}
	}
	if ws["clone_status"] {
		t.Error("workspaces keeps clone_status after the migration")
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
	if row.Status != "pending" || row.WorkspaceMode != "standard" || row.Settings != DefaultSettings() || row.SyncError != nil || row.UpstreamHeadSHA != nil {
		t.Errorf("old row %+v", row)
	}
	testutil.Exec(t, db, `UPDATE workspaces SET settings = '{}' WHERE slug = 'old'`)
	if row, _ := getWorkspaceBySlug(db, "old"); row.Settings != DefaultSettings() {
		t.Errorf("empty settings read as %+v", row.Settings)
	}
	body := workspaceResponse(&Workspace{Slug: "x"}, nil, "owner")
	if body["workspace_mode"] != "standard" || body["status"] != "" {
		t.Errorf("response of an empty row: %v", body)
	}
	for _, gone := range []string{"clone_status", "sync_status", "sync_mode"} {
		if _, ok := body[gone]; ok {
			t.Errorf("response has the folded key %s", gone)
		}
	}
	if extractColumnName("CREATE TABLE x") != "" || extractColumnName("ALTER TABLE t ADD COLUMN c") != "c" || !isDuplicateColumnError(sqlError("duplicate column name: c")) || isDuplicateColumnError(nil) {
		t.Error("the DDL helpers")
	}
}

// TestSchema_FoldsStatusColumns: a table from just before the fold, with
// status active/archived beside clone_status, sync_status and sync_mode, is
// rebuilt on InitSchema: status takes the clone state and archived stays
// archived, a disabled sync_mode becomes the sync_enabled setting, a sync
// left syncing becomes a sync_error, the three columns go and the other
// columns keep their values; the rebuilt table has the CHECK and a second
// InitSchema leaves it alone.
func TestSchema_FoldsStatusColumns(t *testing.T) {
	db := testutil.OpenDB(t)
	testutil.Exec(t, db, `CREATE TABLE workspaces (
		slug TEXT PRIMARY KEY, git_url TEXT NOT NULL, branch TEXT, owner_id TEXT NOT NULL, org_id TEXT,
		status TEXT NOT NULL DEFAULT 'active', display_name TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '',
		clone_status TEXT NOT NULL DEFAULT 'pending' CHECK(clone_status IN ('pending','cloning','ready','failed','archived')),
		head_sha TEXT, clone_error TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		sync_mode TEXT NOT NULL DEFAULT 'pull_only', sync_status TEXT NOT NULL DEFAULT 'idle',
		upstream_head_sha TEXT, last_sync_at TEXT, sync_error TEXT,
		workspace_mode TEXT NOT NULL DEFAULT 'standard', upstream_url TEXT, integration_branch TEXT, settings TEXT NOT NULL DEFAULT '{}')`)
	rows := []struct {
		slug, status, clone, syncMode, syncStatus string
		headSHA, cloneErr, upstream, syncErr      *string
		mode                                      string
		integration                               *string
		settings                                  string
	}{
		{"ready", "active", "ready", "pull_only", "idle", strPtr("a1"), nil, strPtr("u1"), nil, "carry_patch", strPtr("deploy"), `{"rebuild_strategy":"merge"}`},
		{"pending", "active", "pending", "pull_only", "idle", nil, nil, nil, nil, "standard", nil, `{}`},
		{"failed", "active", "failed", "pull_only", "idle", nil, strPtr("boom"), nil, nil, "standard", nil, `{}`},
		{"archived", "archived", "archived", "pull_only", "idle", strPtr("a2"), nil, nil, nil, "standard", nil, `{}`},
		{"nosync", "active", "ready", "disabled", "error", strPtr("a3"), nil, strPtr("u3"), strPtr("fetch failed"), "standard", nil, `{}`},
		{"midsync", "active", "ready", "pull_only", "syncing", strPtr("a4"), nil, nil, nil, "standard", nil, `{}`},
	}
	for _, r := range rows {
		testutil.Exec(t, db, `INSERT INTO workspaces (slug, git_url, owner_id, org_id, status, clone_status, head_sha, clone_error, created_at, updated_at, sync_mode, sync_status, upstream_head_sha, sync_error, workspace_mode, integration_branch, settings)
			VALUES (?, 'https://github.com/org/repo', 'alice', 'org-1', ?, ?, ?, ?, '2024-01-01T00:00:00.000000Z', '2024-01-01T00:00:00.000000Z', ?, ?, ?, ?, ?, ?, ?)`,
			r.slug, r.status, r.clone, r.headSHA, r.cloneErr, r.syncMode, r.syncStatus, r.upstream, r.syncErr, r.mode, r.integration, r.settings)
	}

	if err := InitSchema(db); err != nil {
		t.Fatal(err)
	}
	cols := columns(t, db, "workspaces")
	for _, gone := range []string{"clone_status", "sync_status", "sync_mode"} {
		if cols[gone] {
			t.Errorf("column %s still present after the fold", gone)
		}
	}
	for slug, w := range map[string]struct {
		status      string
		syncEnabled bool
		syncErr     string
	}{
		"ready":    {"ready", true, ""},
		"pending":  {"pending", true, ""},
		"failed":   {"failed", true, ""},
		"archived": {"archived", true, ""},
		"nosync":   {"ready", false, "fetch failed"},
		"midsync":  {"ready", true, "sync interrupted by server restart"},
	} {
		ws, err := getWorkspaceBySlug(db, slug)
		if err != nil || ws == nil {
			t.Fatalf("get %s: %v", slug, err)
		}
		syncErr := ""
		if ws.SyncError != nil {
			syncErr = *ws.SyncError
		}
		if ws.Status != w.status || ws.Settings.SyncEnabled != w.syncEnabled || syncErr != w.syncErr {
			t.Errorf("%s: status %q, sync_enabled %v, sync_error %q; want %q, %v, %q", slug, ws.Status, ws.Settings.SyncEnabled, syncErr, w.status, w.syncEnabled, w.syncErr)
		}
	}
	ready, _ := getWorkspaceBySlug(db, "ready")
	if *ready.HeadSHA != "a1" || *ready.UpstreamHeadSHA != "u1" || ready.WorkspaceMode != "carry_patch" || *ready.IntegrationBranch != "deploy" ||
		ready.Settings.RebuildStrategy != "merge" || *ready.OrgID != "org-1" || ready.CreatedAt != "2024-01-01T00:00:00.000000Z" {
		t.Errorf("ready: other columns not carried over: %+v", ready)
	}
	if failed, _ := getWorkspaceBySlug(db, "failed"); *failed.CloneError != "boom" {
		t.Errorf("failed: clone_error %q; want boom", *failed.CloneError)
	}

	// The rebuilt table enforces the status list and the fold does not run
	// again.
	if _, err := db.Exec(`UPDATE workspaces SET status = 'active' WHERE slug = 'ready'`); err == nil {
		t.Error("status 'active' accepted after the fold; want a CHECK failure")
	}
	if err := InitSchema(db); err != nil {
		t.Fatalf("second InitSchema: %v", err)
	}
	if n := testutil.Count(t, db, `SELECT COUNT(*) FROM workspaces`); n != len(rows) {
		t.Errorf("%d rows after the second InitSchema; want %d", n, len(rows))
	}
}

type sqlError string

func (e sqlError) Error() string { return string(e) }
