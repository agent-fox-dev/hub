package workspace

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	_ "modernc.org/sqlite"
)

// ===========================================================================
// TS-20-41 (integration): The migration adds the three nullable columns to an
// existing patches table idempotently without backfill.
//
// Verifies: 20-REQ-7.1
// ===========================================================================

func TestOriginSyncState_MigrationIdempotent_TS2041(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	// Create a pre-existing patches table without the origin columns.
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS workspaces (
			slug              TEXT PRIMARY KEY,
			git_url           TEXT NOT NULL,
			branch            TEXT,
			owner_id          TEXT NOT NULL,
			org_id            TEXT,
			status            TEXT NOT NULL DEFAULT 'active',
			display_name      TEXT NOT NULL DEFAULT '',
			description       TEXT NOT NULL DEFAULT '',
			clone_status      TEXT NOT NULL DEFAULT 'pending',
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

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS patches (
			id              TEXT PRIMARY KEY,
			workspace_slug  TEXT NOT NULL,
			branch_name     TEXT NOT NULL,
			position        INTEGER NOT NULL,
			status          TEXT NOT NULL DEFAULT 'active',
			conflict_files  TEXT,
			upstream_pr_url TEXT,
			description     TEXT,
			deleted_at      TEXT,
			added_at        TEXT NOT NULL,
			updated_at      TEXT NOT NULL,
			UNIQUE(workspace_slug, branch_name),
			UNIQUE(workspace_slug, position)
		)`)
	if err != nil {
		t.Fatalf("failed to create old patches table: %v", err)
	}

	// Insert a pre-existing row.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('p1', 'ws1', 'feature/a', 1, 'active', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	)
	if err != nil {
		t.Fatalf("failed to insert pre-existing row: %v", err)
	}

	// Verify origin columns do not exist yet.
	cols, err := existingColumns(db, "patches")
	if err != nil {
		t.Fatalf("existingColumns: %v", err)
	}
	for _, col := range []string{"origin_sync_state", "origin_sha", "origin_synced_at"} {
		if cols[col] {
			t.Fatalf("column %q should not exist before migration", col)
		}
	}

	// Run initSchema three times — all must succeed.
	for i := 1; i <= 3; i++ {
		if err := initSchema(db); err != nil {
			t.Fatalf("initSchema() run %d returned error: %v", i, err)
		}
	}

	// Verify the three columns now exist.
	cols, err = existingColumns(db, "patches")
	if err != nil {
		t.Fatalf("existingColumns after migration: %v", err)
	}
	for _, col := range []string{"origin_sync_state", "origin_sha", "origin_synced_at"} {
		if !cols[col] {
			t.Errorf("patches table missing column %q after migration", col)
		}
	}

	// Verify the pre-existing row has NULL in all three columns.
	var originSyncState, originSHA, originSyncedAt sql.NullString
	err = db.QueryRow(
		`SELECT origin_sync_state, origin_sha, origin_synced_at FROM patches WHERE id = 'p1'`,
	).Scan(&originSyncState, &originSHA, &originSyncedAt)
	if err != nil {
		t.Fatalf("failed to query pre-existing row: %v", err)
	}
	if originSyncState.Valid {
		t.Errorf("expected origin_sync_state NULL, got %q", originSyncState.String)
	}
	if originSHA.Valid {
		t.Errorf("expected origin_sha NULL, got %q", originSHA.String)
	}
	if originSyncedAt.Valid {
		t.Errorf("expected origin_synced_at NULL, got %q", originSyncedAt.String)
	}
}

// ===========================================================================
// TS-20-45 (integration): Every workspace patch REST response exposes the
// three fields and omits them when null.
//
// Verifies: 20-REQ-7.5
// ===========================================================================

func TestOriginSyncState_PatchRESTResponse_TS2045(t *testing.T) {
	env := newTestEnv(t)

	// Create a carry_patch workspace.
	auth := userAuth("alice-id")
	createBody := `{
		"slug": "ws-origin",
		"git_url": "https://github.com/example/repo",
		"workspace_mode": "carry_patch",
		"integration_branch": "integration",
		"upstream_url": "https://github.com/example/upstream"
	}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", createBody, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create workspace: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Add a patch.
	addBody := `{"branch_name": "feature/test", "skip_branch_check": true}`
	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-origin/patches", addBody, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add patch: status=%d body=%s", rec.Code, rec.Body.String())
	}

	var addResp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&addResp); err != nil {
		t.Fatalf("decode add response: %v", err)
	}
	patchID := addResp["id"].(string)

	// The create response should NOT contain origin fields (they are null).
	for _, key := range []string{"origin_sync_state", "origin_sha", "origin_synced_at"} {
		if _, ok := addResp[key]; ok {
			t.Errorf("create response should omit %q when null, but it was present", key)
		}
	}

	// Now set origin state directly in the DB.
	_, err := env.db.Exec(
		`UPDATE patches SET origin_sync_state = 'in_sync', origin_sha = 'abc123', origin_synced_at = '2024-06-01T12:00:00Z' WHERE id = ?`,
		patchID,
	)
	if err != nil {
		t.Fatalf("update origin state: %v", err)
	}

	// GET single patch — should include origin fields.
	rec = env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-origin/patches", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("list patches: status=%d body=%s", rec.Code, rec.Body.String())
	}

	var listResp []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listResp) != 1 {
		t.Fatalf("expected 1 patch, got %d", len(listResp))
	}

	patch := listResp[0]
	if patch["origin_sync_state"] != "in_sync" {
		t.Errorf("expected origin_sync_state='in_sync', got %v", patch["origin_sync_state"])
	}
	if patch["origin_sha"] != "abc123" {
		t.Errorf("expected origin_sha='abc123', got %v", patch["origin_sha"])
	}
	if patch["origin_synced_at"] != "2024-06-01T12:00:00Z" {
		t.Errorf("expected origin_synced_at='2024-06-01T12:00:00Z', got %v", patch["origin_synced_at"])
	}

	// Verify existing fields are unchanged.
	if patch["branch_name"] != "feature/test" {
		t.Errorf("expected branch_name='feature/test', got %v", patch["branch_name"])
	}
	if patch["status"] != "active" {
		t.Errorf("expected status='active', got %v", patch["status"])
	}

	// Update the patch — the response should still include origin fields.
	updateBody := `{"description": "updated desc"}`
	rec = env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/ws-origin/patches/"+patchID, updateBody, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("update patch: status=%d body=%s", rec.Code, rec.Body.String())
	}

	var updateResp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&updateResp); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updateResp["origin_sync_state"] != "in_sync" {
		t.Errorf("update response: expected origin_sync_state='in_sync', got %v", updateResp["origin_sync_state"])
	}
	if updateResp["origin_sha"] != "abc123" {
		t.Errorf("update response: expected origin_sha='abc123', got %v", updateResp["origin_sha"])
	}

	// Add a second patch without origin state — it should omit the fields.
	addBody2 := `{"branch_name": "feature/no-origin", "skip_branch_check": true}`
	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-origin/patches", addBody2, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add second patch: status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-origin/patches", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("list patches (2): status=%d body=%s", rec.Code, rec.Body.String())
	}

	var listResp2 []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&listResp2); err != nil {
		t.Fatalf("decode list response (2): %v", err)
	}

	// Find the patch without origin state.
	for _, p := range listResp2 {
		if p["branch_name"] == "feature/no-origin" {
			for _, key := range []string{"origin_sync_state", "origin_sha", "origin_synced_at"} {
				if _, ok := p[key]; ok {
					t.Errorf("patch without origin state should omit %q, but it was present", key)
				}
			}
		}
	}
}
