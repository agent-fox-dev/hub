package workspace

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TS-02-26: Schema migration adds nullable origin_sync_state, origin_sha
// and origin_synced_at columns to the patches table idempotently.
// Verifies: 02-REQ-6.1
func TestTS_02_26_SchemaMigration_OriginSyncColumns(t *testing.T) {
	// Database without the new columns
	dbWithout, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer dbWithout.Close()
	dbWithout.SetMaxOpenConns(1)

	// Create legacy patches table without the three new columns
	_, err = dbWithout.Exec(`
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

	_, err = dbWithout.Exec(`
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
		t.Fatalf("failed to create legacy patches table: %v", err)
	}

	// Insert an existing row before migration
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = dbWithout.Exec(`
		INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		VALUES ('p-existing', 'ws-test', 'patch-1', 1, 'active', ?, ?)
	`, now, now)
	if err != nil {
		t.Fatalf("failed to insert existing patch: %v", err)
	}

	// Verify columns are absent before migration
	colsBefore, err := existingColumns(dbWithout, "patches")
	if err != nil {
		t.Fatalf("existingColumns returned error: %v", err)
	}
	for _, col := range []string{"origin_sync_state", "origin_sha", "origin_synced_at"} {
		if colsBefore[col] {
			t.Fatalf("column %s should not exist before migration", col)
		}
	}

	// Run initSchema against dbWithout
	if err := initSchema(dbWithout); err != nil {
		t.Fatalf("initSchema(dbWithout) returned error: %v", err)
	}

	// Verify dbWithout gained the three nullable columns
	colsAfter, err := existingColumns(dbWithout, "patches")
	if err != nil {
		t.Fatalf("existingColumns returned error: %v", err)
	}
	for _, col := range []string{"origin_sync_state", "origin_sha", "origin_synced_at"} {
		if !colsAfter[col] {
			t.Errorf("column %s not added by migration", col)
		}
	}

	// Existing row keeps the three new columns null
	var (
		syncState sql.NullString
		originSHA sql.NullString
		syncedAt  sql.NullString
	)
	err = dbWithout.QueryRow(
		`SELECT origin_sync_state, origin_sha, origin_synced_at FROM patches WHERE id = 'p-existing'`,
	).Scan(&syncState, &originSHA, &syncedAt)
	if err != nil {
		t.Fatalf("failed to query existing patch: %v", err)
	}
	if syncState.Valid {
		t.Errorf("origin_sync_state = %q, want null", syncState.String)
	}
	if originSHA.Valid {
		t.Errorf("origin_sha = %q, want null", originSHA.String)
	}
	if syncedAt.Valid {
		t.Errorf("origin_synced_at = %q, want null", syncedAt.String)
	}

	// A second database that already has them
	dbWith, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer dbWith.Close()
	dbWith.SetMaxOpenConns(1)

	// Pre-create patches table already containing the columns
	_, err = dbWith.Exec(`
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
		t.Fatalf("failed to create workspaces table on dbWith: %v", err)
	}
	_, err = dbWith.Exec(`
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
		t.Fatalf("failed to create patches table on dbWith: %v", err)
	}

	// Run initSchema against dbWith - must be a no-op and not error
	if err := initSchema(dbWith); err != nil {
		t.Errorf("initSchema(dbWith) returned error: %v, want nil", err)
	}

	// Also verify re-running initSchema on dbWithout is idempotent and no-op
	if err := initSchema(dbWithout); err != nil {
		t.Errorf("second initSchema(dbWithout) returned error: %v, want nil", err)
	}
}
