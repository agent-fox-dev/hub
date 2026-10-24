package workspace

import (
	"database/sql"
	"testing"
)

// oldWorkspacesDDL is the workspaces table before the status fold: a status
// of active or archived beside clone_status, sync_status and sync_mode.
const oldWorkspacesDDL = `
CREATE TABLE workspaces (
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
	integration_branch TEXT,
	settings          TEXT NOT NULL DEFAULT '{}'
)`

func openRawTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// A table from before the fold is rebuilt on InitSchema: status takes the
// clone state, an archived workspace stays archived, a disabled sync becomes
// the sync_enabled setting, an interrupted sync becomes a sync_error, and
// the old columns are gone. The other columns keep their values.
func TestFoldStatusColumns(t *testing.T) {
	db := openRawTestDB(t)
	if _, err := db.Exec(oldWorkspacesDDL); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO workspaces (slug, git_url, owner_id, org_id, status, clone_status, head_sha, clone_error, created_at, updated_at, sync_mode, sync_status, upstream_head_sha, sync_error, workspace_mode, integration_branch, settings)
	           VALUES (?, 'https://github.com/org/repo', 'alice', 'org-1', ?, ?, ?, ?, '2024-01-01T00:00:00.000000Z', '2024-01-01T00:00:00.000000Z', ?, ?, ?, ?, ?, ?, ?)`
	rows := []struct {
		slug, status, clone, syncMode, syncStatus string
		headSHA, cloneErr, upstream, syncErr      *string
		mode                                      string
		integration                               *string
		settings                                  string
	}{
		{"ready", "active", "ready", "pull_only", "idle", ptr("a1"), nil, ptr("u1"), nil, "carry_patch", ptr("deploy"), `{"rebuild_strategy":"merge"}`},
		{"pending", "active", "pending", "pull_only", "idle", nil, nil, nil, nil, "standard", nil, `{}`},
		{"failed", "active", "failed", "pull_only", "idle", nil, ptr("boom"), nil, nil, "standard", nil, `{}`},
		{"archived", "archived", "archived", "pull_only", "idle", ptr("a2"), nil, nil, nil, "standard", nil, `{}`},
		{"nosync", "active", "ready", "disabled", "error", ptr("a3"), nil, ptr("u3"), ptr("fetch failed"), "standard", nil, `{}`},
		{"midsync", "active", "ready", "pull_only", "syncing", ptr("a4"), nil, nil, nil, "standard", nil, `{}`},
	}
	for _, r := range rows {
		if _, err := db.Exec(insert, r.slug, r.status, r.clone, r.headSHA, r.cloneErr, r.syncMode, r.syncStatus, r.upstream, r.syncErr, r.mode, r.integration, r.settings); err != nil {
			t.Fatalf("seed %s: %v", r.slug, err)
		}
	}

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	cols, err := existingColumns(db, "workspaces")
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"clone_status", "sync_status", "sync_mode"} {
		if cols[gone] {
			t.Errorf("column %s still present after the fold", gone)
		}
	}

	want := map[string]struct {
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
	}
	for slug, w := range want {
		ws, err := getWorkspaceBySlug(db, slug)
		if err != nil || ws == nil {
			t.Fatalf("get %s: %v", slug, err)
		}
		if ws.Status != w.status {
			t.Errorf("%s: status = %q; want %q", slug, ws.Status, w.status)
		}
		if ws.Settings.SyncEnabled != w.syncEnabled {
			t.Errorf("%s: sync_enabled = %v; want %v", slug, ws.Settings.SyncEnabled, w.syncEnabled)
		}
		if got := deref(ws.SyncError); got != w.syncErr {
			t.Errorf("%s: sync_error = %q; want %q", slug, got, w.syncErr)
		}
	}

	ready, _ := getWorkspaceBySlug(db, "ready")
	if deref(ready.HeadSHA) != "a1" || deref(ready.UpstreamHeadSHA) != "u1" || ready.WorkspaceMode != "carry_patch" || deref(ready.IntegrationBranch) != "deploy" || ready.Settings.RebuildStrategy != "merge" || deref(ready.OrgID) != "org-1" {
		t.Errorf("ready: other columns not carried over: %+v", ready)
	}
	failed, _ := getWorkspaceBySlug(db, "failed")
	if deref(failed.CloneError) != "boom" {
		t.Errorf("failed: clone_error = %q; want boom", deref(failed.CloneError))
	}

	// The rebuilt table enforces the status list and the fold does not run
	// again.
	if _, err := db.Exec(`UPDATE workspaces SET status = 'active' WHERE slug = 'ready'`); err == nil {
		t.Error("status 'active' accepted after the fold; want a CHECK failure")
	}
	if err := InitSchema(db); err != nil {
		t.Fatalf("second InitSchema: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&n); err != nil || n != len(rows) {
		t.Errorf("rows after second InitSchema = %d, %v; want %d", n, err, len(rows))
	}
}

func ptr(s string) *string { return &s }

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
