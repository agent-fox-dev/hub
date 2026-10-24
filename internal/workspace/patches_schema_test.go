package workspace

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// The tests of this file used to live beside the patch routes; they test the
// patches DDL, which the hub creates, and moved here when the routes moved to
// the carrypatch module.

// ========================================================================
// Spec 15 Task 3.1: Patches table schema
// (TS-15-21, TS-15-22)
// Requirements: 15-REQ-7
// ========================================================================

// TS-15-21: Hub startup creates the patches table with all required columns
// and constraints when the table does not yet exist.
// Requirement: 15-REQ-7.1
func TestCarryPatch_PatchesTableSchema_AllColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()
	// Limit to one connection so all PRAGMAs see the same in-memory database.
	db.SetMaxOpenConns(1)

	// Run the schema initializer.
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	// Verify the patches table exists by querying PRAGMA table_info.
	rows, err := db.Query("PRAGMA table_info(patches)")
	if err != nil {
		t.Fatalf("PRAGMA table_info(patches) failed: %v", err)
	}
	defer rows.Close()

	type columnInfo struct {
		name    string
		colType string
		notNull bool
		dflt    *string
		pk      bool
	}
	columns := make(map[string]columnInfo)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt *string
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		columns[name] = columnInfo{
			name:    name,
			colType: ctype,
			notNull: notnull == 1,
			dflt:    dflt,
			pk:      pk == 1,
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}

	if len(columns) == 0 {
		t.Fatal("patches table does not exist or has no columns")
	}

	// Required columns per 15-REQ-7.1.
	requiredColumns := []string{
		"id", "workspace_slug", "branch_name", "position",
		"status", "conflict_files", "upstream_pr_url", "description",
		"added_at", "updated_at",
	}
	for _, col := range requiredColumns {
		if _, ok := columns[col]; !ok {
			t.Errorf("patches table missing required column %q", col)
		}
	}

	// Verify id is TEXT PRIMARY KEY.
	if col, ok := columns["id"]; ok {
		if !col.pk {
			t.Error("id column should be PRIMARY KEY")
		}
	}

	// Verify workspace_slug is TEXT NOT NULL.
	if col, ok := columns["workspace_slug"]; ok {
		if !col.notNull {
			t.Error("workspace_slug should be NOT NULL")
		}
	}

	// Verify branch_name is TEXT NOT NULL.
	if col, ok := columns["branch_name"]; ok {
		if !col.notNull {
			t.Error("branch_name should be NOT NULL")
		}
	}

	// Verify position is INTEGER NOT NULL.
	if col, ok := columns["position"]; ok {
		if !col.notNull {
			t.Error("position should be NOT NULL")
		}
	}

	// Verify status is TEXT NOT NULL DEFAULT 'active'.
	if col, ok := columns["status"]; ok {
		if !col.notNull {
			t.Error("status should be NOT NULL")
		}
		if col.dflt == nil || *col.dflt != "'active'" {
			got := "<nil>"
			if col.dflt != nil {
				got = *col.dflt
			}
			t.Errorf("status default = %s; want 'active'", got)
		}
	}

	// Verify added_at is TEXT NOT NULL.
	if col, ok := columns["added_at"]; ok {
		if !col.notNull {
			t.Error("added_at should be NOT NULL")
		}
	}

	// Verify updated_at is TEXT NOT NULL.
	if col, ok := columns["updated_at"]; ok {
		if !col.notNull {
			t.Error("updated_at should be NOT NULL")
		}
	}

	// Verify UNIQUE constraints via PRAGMA index_list.
	// Collect index names first, then close the rows before querying each
	// index's columns to avoid nested queries on a single-connection pool.
	idxRows, err := db.Query("PRAGMA index_list(patches)")
	if err != nil {
		t.Fatalf("PRAGMA index_list(patches) failed: %v", err)
	}

	type indexInfo struct {
		name    string
		unique  bool
		columns []string
	}
	var indexes []indexInfo
	for idxRows.Next() {
		var seq int
		var name, origin string
		var unique, partial int
		if err := idxRows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan index_list failed: %v", err)
		}
		indexes = append(indexes, indexInfo{name: name, unique: unique == 1})
	}
	idxRows.Close()

	// Now query columns for each index.
	for i, idx := range indexes {
		colRows, err := db.Query(fmt.Sprintf("PRAGMA index_info(%s)", idx.name))
		if err != nil {
			t.Fatalf("PRAGMA index_info(%s) failed: %v", idx.name, err)
		}
		for colRows.Next() {
			var seqno, cid int
			var colName string
			if err := colRows.Scan(&seqno, &cid, &colName); err != nil {
				t.Fatalf("scan index_info failed: %v", err)
			}
			indexes[i].columns = append(indexes[i].columns, colName)
		}
		colRows.Close()
	}

	// Check for UNIQUE(workspace_slug, branch_name).
	foundBranchUnique := false
	for _, idx := range indexes {
		if idx.unique && len(idx.columns) == 2 &&
			containsAll(idx.columns, "workspace_slug", "branch_name") {
			foundBranchUnique = true
			break
		}
	}
	if !foundBranchUnique {
		t.Error("patches table missing UNIQUE constraint on (workspace_slug, branch_name)")
	}

	// Check for UNIQUE(workspace_slug, position).
	foundPositionUnique := false
	for _, idx := range indexes {
		if idx.unique && len(idx.columns) == 2 &&
			containsAll(idx.columns, "workspace_slug", "position") {
			foundPositionUnique = true
			break
		}
	}
	if !foundPositionUnique {
		t.Error("patches table missing UNIQUE constraint on (workspace_slug, position)")
	}
}

// containsAll returns true if slice contains all specified items.
func containsAll(slice []string, items ...string) bool {
	set := make(map[string]bool, len(slice))
	for _, s := range slice {
		set[s] = true
	}
	for _, item := range items {
		if !set[item] {
			return false
		}
	}
	return true
}

// TS-15-21 (status default): Inserting a row without specifying status
// defaults to 'active'.
// Requirement: 15-REQ-7.1
func TestCarryPatch_PatchesTableSchema_StatusDefault(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, added_at, updated_at)
		 VALUES ('test-id-1', 'ws-1', 'feature/foo', 1, ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("INSERT without status failed: %v", err)
	}

	var status string
	err = db.QueryRow("SELECT status FROM patches WHERE id = ?", "test-id-1").Scan(&status)
	if err != nil {
		t.Fatalf("SELECT status failed: %v", err)
	}
	if status != "active" {
		t.Errorf("status = %q; want %q (default)", status, "active")
	}
}

// TS-15-22: Inserting two patches with the same (workspace_slug, branch_name)
// pair is rejected by the database unique constraint.
// Requirement: 15-REQ-7.2
func TestCarryPatch_PatchesTableSchema_DuplicateBranchNameRejected(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// First insert should succeed.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-1', 'cp-ws', 'feature/foo', 1, 'active', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("first INSERT failed: %v", err)
	}

	// Second insert with same (workspace_slug, branch_name) should fail.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-2', 'cp-ws', 'feature/foo', 2, 'active', ?, ?)`,
		now, now,
	)
	if err == nil {
		t.Fatal("second INSERT with duplicate (workspace_slug, branch_name) should fail; got nil error")
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") &&
		!strings.Contains(strings.ToLower(err.Error()), "constraint") {
		t.Errorf("expected UNIQUE constraint error; got: %v", err)
	}
}

// TS-15-22 (position uniqueness): Inserting two patches with the same
// (workspace_slug, position) is rejected by the database unique constraint.
// Requirement: 15-REQ-7.2
func TestCarryPatch_PatchesTableSchema_DuplicatePositionRejected(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// First insert at position 1.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-1', 'cp-ws', 'feature/a', 1, 'active', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("first INSERT failed: %v", err)
	}

	// Second insert at same position 1 for same workspace should fail.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-2', 'cp-ws', 'feature/b', 1, 'active', ?, ?)`,
		now, now,
	)
	if err == nil {
		t.Fatal("second INSERT with duplicate (workspace_slug, position) should fail; got nil error")
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") &&
		!strings.Contains(strings.ToLower(err.Error()), "constraint") {
		t.Errorf("expected UNIQUE constraint error; got: %v", err)
	}
}

// Verify same branch_name across different workspaces is allowed.
// Requirement: 15-REQ-7.2 (uniqueness is per-workspace, not global)
func TestCarryPatch_PatchesTableSchema_SameBranchDifferentWorkspaces(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Same branch name in different workspaces should succeed.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-1', 'ws-a', 'feature/foo', 1, 'active', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("first INSERT failed: %v", err)
	}

	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-2', 'ws-b', 'feature/foo', 1, 'active', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Errorf("same branch_name in different workspace should succeed; got: %v", err)
	}
}
