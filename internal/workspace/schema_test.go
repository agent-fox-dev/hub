package workspace

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// TS-01-11: Verify that on server boot, the workspaces table is created with
// the correct schema using CREATE TABLE IF NOT EXISTS.
// Requirement: 01-REQ-2.1
func TestWorkspaceSchema_CreatesTable(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	// Query table structure using PRAGMA table_info.
	rows, err := db.Query("PRAGMA table_info(workspaces)")
	if err != nil {
		t.Fatalf("PRAGMA table_info failed: %v", err)
	}
	defer rows.Close()

	type columnInfo struct {
		name       string
		colType    string
		notNull    bool
		dfltValue  *string
		primaryKey bool
	}

	columns := make(map[string]columnInfo)
	for rows.Next() {
		var (
			cid        int
			name       string
			colType    string
			notNull    int
			dfltValue  *string
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &primaryKey); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		columns[name] = columnInfo{
			name:       name,
			colType:    colType,
			notNull:    notNull == 1,
			dfltValue:  dfltValue,
			primaryKey: primaryKey == 1,
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}

	// 13 original columns + 5 sync columns (sync_mode, sync_status,
	// upstream_head_sha, last_sync_at, sync_error) + 3 carry-patch columns
	// (workspace_mode, upstream_url, integration_branch) = 21 total.
	if len(columns) != 21 {
		t.Errorf("got %d columns; want 21", len(columns))
	}

	// Verify each column's properties.
	expectations := []struct {
		name       string
		colType    string
		notNull    bool
		dfltValue  string
		primaryKey bool
	}{
		{"slug", "TEXT", false, "", true}, // PK implies NOT NULL in SQLite
		{"git_url", "TEXT", true, "", false},
		{"branch", "TEXT", false, "", false},
		{"owner_id", "TEXT", true, "", false},
		{"org_id", "TEXT", false, "", false},
		{"status", "TEXT", true, "'active'", false},
		{"display_name", "TEXT", true, "''", false},
		{"description", "TEXT", true, "''", false},
		{"created_at", "TEXT", true, "", false},
		{"updated_at", "TEXT", true, "", false},
	}

	for _, exp := range expectations {
		col, ok := columns[exp.name]
		if !ok {
			t.Errorf("column %q not found in workspaces table", exp.name)
			continue
		}
		if col.colType != exp.colType {
			t.Errorf("column %q type = %q; want %q", exp.name, col.colType, exp.colType)
		}
		if col.primaryKey != exp.primaryKey {
			t.Errorf("column %q primaryKey = %v; want %v", exp.name, col.primaryKey, exp.primaryKey)
		}
		if col.notNull != exp.notNull {
			// Note: SQLite primary keys report notNull=false in PRAGMA table_info
			// but enforce NOT NULL via the PRIMARY KEY constraint.
			if !exp.primaryKey {
				t.Errorf("column %q notNull = %v; want %v", exp.name, col.notNull, exp.notNull)
			}
		}
		if exp.dfltValue != "" {
			if col.dfltValue == nil || *col.dfltValue != exp.dfltValue {
				got := "<nil>"
				if col.dfltValue != nil {
					got = *col.dfltValue
				}
				t.Errorf("column %q default = %s; want %s", exp.name, got, exp.dfltValue)
			}
		}
	}
}

// TS-01-12: Verify that calling initSchema twice on the same database
// succeeds without error (IF NOT EXISTS semantics).
// Requirement: 01-REQ-2.2
func TestWorkspaceSchema_IdempotentCreation(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("first initSchema() returned error: %v", err)
	}

	if err := initSchema(db); err != nil {
		t.Errorf("second initSchema() returned error: %v; want nil", err)
	}
}

// TS-01-E5: Verify that if the database is unavailable, initSchema returns a
// non-nil error.
// Requirement: 01-REQ-2.E1
func TestWorkspaceSchema_UnavailableDB(t *testing.T) {
	// Use a closed database to simulate an unavailable DB.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	db.Close() // Close immediately to make it unavailable.

	if err := initSchema(db); err == nil {
		t.Error("initSchema(closed DB) returned nil; want non-nil error")
	}
}

// TS-NS-1: Fresh database: patches table includes conflict_files column after boot.
// Requirement: NS-REQ-1
func TestPatchesSchema_FreshDB_HasConflictFilesColumn(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	cols, err := existingColumns(db, "patches")
	if err != nil {
		t.Fatalf("existingColumns(patches) returned error: %v", err)
	}

	if !cols["conflict_files"] {
		t.Error("patches table missing conflict_files column on fresh database")
	}
}

// TS-NS-2: Existing database without conflict_files: idempotent migration adds
// the column on boot. A second call to initSchema does not error.
// Requirement: NS-REQ-2
func TestPatchesSchema_Migration_AddsConflictFilesColumn(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	// Create the old patches DDL without conflict_files.
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
		t.Fatalf("failed to create old workspaces table: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS patches (
			id              TEXT PRIMARY KEY,
			workspace_slug  TEXT NOT NULL,
			branch_name     TEXT NOT NULL,
			position        INTEGER NOT NULL,
			status          TEXT NOT NULL DEFAULT 'active',
			upstream_pr_url TEXT,
			description     TEXT,
			added_at        TEXT NOT NULL,
			updated_at      TEXT NOT NULL,
			UNIQUE(workspace_slug, branch_name),
			UNIQUE(workspace_slug, position)
		)`)
	if err != nil {
		t.Fatalf("failed to create old patches table: %v", err)
	}

	// Verify conflict_files is absent before migration.
	cols, err := existingColumns(db, "patches")
	if err != nil {
		t.Fatalf("existingColumns(patches) returned error: %v", err)
	}
	if cols["conflict_files"] {
		t.Fatal("conflict_files should not exist before migration")
	}

	// Run initSchema — should add conflict_files via ALTER TABLE.
	if err := initSchema(db); err != nil {
		t.Fatalf("first initSchema() returned error: %v", err)
	}

	cols, err = existingColumns(db, "patches")
	if err != nil {
		t.Fatalf("existingColumns(patches) returned error: %v", err)
	}
	if !cols["conflict_files"] {
		t.Error("conflict_files column not added by migration")
	}

	// Second call to initSchema should succeed (idempotent).
	if err := initSchema(db); err != nil {
		t.Errorf("second initSchema() returned error: %v; want nil", err)
	}
}
