package workspace

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// ========================================================================
// Spec 15 Task 1.1: Workspace mode schema migration
// (TS-15-1, TS-15-2, TS-15-3)
// Requirements: 15-REQ-1
// ========================================================================

// legacyCreateTableSQL is the workspaces DDL before the carry-patch columns
// were added. Used to simulate an existing database that lacks workspace_mode,
// upstream_url, and integration_branch.
const legacyCreateTableSQL = `
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
	sync_error        TEXT
)`

// TS-15-1: Running the migration against an existing database that lacks
// the new columns adds workspace_mode, upstream_url, and integration_branch
// columns, and existing rows read back with correct defaults.
// Requirement: 15-REQ-1.1
func TestCarryPatch_SchemaMigration_AddsColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	// Create the legacy workspaces table (without carry-patch columns).
	if _, err := db.Exec(legacyCreateTableSQL); err != nil {
		t.Fatalf("failed to create legacy table: %v", err)
	}

	// Insert an existing workspace row.
	if _, err := db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, display_name, description, created_at, updated_at)
		 VALUES ('existing-ws', 'https://github.com/org/repo', 'user-1', 'active', 'existing', '', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("failed to insert legacy workspace row: %v", err)
	}

	// Run the migration function.
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	// Verify the columns were added by querying PRAGMA table_info.
	cols, err := existingColumns(db, "workspaces")
	if err != nil {
		t.Fatalf("existingColumns() returned error: %v", err)
	}

	for _, colName := range []string{"workspace_mode", "upstream_url", "integration_branch"} {
		if !cols[colName] {
			t.Errorf("column %q not found in workspaces table after migration", colName)
		}
	}

	// Verify existing row reads back with correct defaults.
	var wsMode string
	var upstreamURL, integrationBranch *string
	err = db.QueryRow(
		`SELECT workspace_mode, upstream_url, integration_branch FROM workspaces WHERE slug = ?`,
		"existing-ws",
	).Scan(&wsMode, &upstreamURL, &integrationBranch)
	if err != nil {
		t.Fatalf("failed to query existing row after migration: %v", err)
	}

	if wsMode != "standard" {
		t.Errorf("existing row workspace_mode = %q; want %q", wsMode, "standard")
	}
	if upstreamURL != nil {
		t.Errorf("existing row upstream_url = %v; want NULL", *upstreamURL)
	}
	if integrationBranch != nil {
		t.Errorf("existing row integration_branch = %v; want NULL", *integrationBranch)
	}
}

// 15-REQ-1.E1: Migration against a database that already has the columns
// skips ALTER TABLE and completes without error (idempotent re-run).
func TestCarryPatch_SchemaMigration_IdempotentReRun(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	// First run creates everything.
	if err := initSchema(db); err != nil {
		t.Fatalf("first initSchema() returned error: %v", err)
	}

	// Second run should detect existing columns and skip without error.
	if err := initSchema(db); err != nil {
		t.Errorf("second initSchema() returned error: %v; want nil (idempotent)", err)
	}

	// Verify the carry-patch columns still exist.
	cols, err := existingColumns(db, "workspaces")
	if err != nil {
		t.Fatalf("existingColumns() returned error: %v", err)
	}
	for _, colName := range []string{"workspace_mode", "upstream_url", "integration_branch"} {
		if !cols[colName] {
			t.Errorf("column %q missing after idempotent re-run", colName)
		}
	}
}

// TS-15-2: Initializing schema against a fresh empty database creates the
// workspaces table with all three new columns present without requiring
// ALTER TABLE.
// Requirement: 15-REQ-1.2
func TestCarryPatch_SchemaFresh_IncludesColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	// Initialize schema on a fresh (empty) database.
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
		name      string
		colType   string
		notNull   bool
		dfltValue *string
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
			name:      name,
			colType:   colType,
			notNull:   notNull == 1,
			dfltValue: dfltValue,
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}

	// Verify workspace_mode: TEXT NOT NULL DEFAULT 'standard'
	t.Run("workspace_mode", func(t *testing.T) {
		col, ok := columns["workspace_mode"]
		if !ok {
			t.Fatal("column 'workspace_mode' not found in workspaces table")
		}
		if col.colType != "TEXT" {
			t.Errorf("workspace_mode type = %q; want TEXT", col.colType)
		}
		if !col.notNull {
			t.Error("workspace_mode should be NOT NULL")
		}
		if col.dfltValue == nil || *col.dfltValue != "'standard'" {
			got := "<nil>"
			if col.dfltValue != nil {
				got = *col.dfltValue
			}
			t.Errorf("workspace_mode default = %s; want 'standard'", got)
		}
	})

	// Verify upstream_url: TEXT nullable
	t.Run("upstream_url", func(t *testing.T) {
		col, ok := columns["upstream_url"]
		if !ok {
			t.Fatal("column 'upstream_url' not found in workspaces table")
		}
		if col.colType != "TEXT" {
			t.Errorf("upstream_url type = %q; want TEXT", col.colType)
		}
		if col.notNull {
			t.Error("upstream_url should be nullable (NOT NULL = false)")
		}
	})

	// Verify integration_branch: TEXT nullable
	t.Run("integration_branch", func(t *testing.T) {
		col, ok := columns["integration_branch"]
		if !ok {
			t.Fatal("column 'integration_branch' not found in workspaces table")
		}
		if col.colType != "TEXT" {
			t.Errorf("integration_branch type = %q; want TEXT", col.colType)
		}
		if col.notNull {
			t.Error("integration_branch should be nullable (NOT NULL = false)")
		}
	})
}

// TS-15-3: Inserting a workspace row without specifying workspace_mode stores
// 'standard' as the default, and inserting with workspace_mode=NULL is
// rejected by the database constraint.
// Requirement: 15-REQ-1.3
func TestCarryPatch_SchemaDefaults_WorkspaceMode(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	// Case A: INSERT omitting workspace_mode → should default to 'standard'.
	t.Run("default_to_standard", func(t *testing.T) {
		_, err := db.Exec(
			`INSERT INTO workspaces (slug, git_url, owner_id, status, display_name, description, created_at, updated_at)
			 VALUES ('default-mode-ws', 'https://github.com/org/repo', 'user-1', 'active', '', '', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		)
		if err != nil {
			t.Fatalf("INSERT omitting workspace_mode failed: %v", err)
		}

		var wsMode string
		err = db.QueryRow("SELECT workspace_mode FROM workspaces WHERE slug = ?", "default-mode-ws").Scan(&wsMode)
		if err != nil {
			t.Fatalf("SELECT workspace_mode failed: %v", err)
		}
		if wsMode != "standard" {
			t.Errorf("workspace_mode = %q; want %q", wsMode, "standard")
		}
	})

	// Case B: INSERT with workspace_mode = NULL → should be rejected by NOT NULL constraint.
	t.Run("null_rejected", func(t *testing.T) {
		_, err := db.Exec(
			`INSERT INTO workspaces (slug, git_url, owner_id, status, workspace_mode, display_name, description, created_at, updated_at)
			 VALUES ('null-mode-ws', 'https://github.com/org/repo', 'user-1', 'active', NULL, '', '', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		)
		if err == nil {
			t.Fatal("INSERT with workspace_mode=NULL should fail with NOT NULL constraint; got nil error")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "not null") &&
			!strings.Contains(strings.ToLower(err.Error()), "constraint") {
			t.Errorf("expected NOT NULL constraint error; got: %v", err)
		}
	})
}
