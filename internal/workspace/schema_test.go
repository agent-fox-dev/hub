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

	if len(columns) != 8 {
		t.Errorf("got %d columns; want 8", len(columns))
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
