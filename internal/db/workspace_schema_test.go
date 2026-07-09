package db

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// newWorkspaceTestDB creates a fresh SQLite in-memory database with
// PRAGMA foreign_keys=ON and the full schema applied via InitSchema.
// The spec requires FK enforcement (07-REQ-1.E2).
func newWorkspaceTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)

	// Enable FK enforcement — SQLite disables it by default.
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		t.Fatalf("failed to enable foreign_keys: %v", err)
	}

	if err := InitSchema(db); err != nil {
		db.Close()
		t.Fatalf("InitSchema failed: %v", err)
	}

	return db
}

// seedUser inserts a minimal user row for FK satisfaction and returns its ID.
func seedUser(t *testing.T, db *sql.DB, username string) string {
	t.Helper()
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO users (id, username, email, provider, provider_id, status, created_at, updated_at)
		 VALUES (?, ?, ?, 'local', ?, 'active', ?, ?)`,
		id, username, username+"@test.com", username+"_pid", now, now,
	)
	if err != nil {
		t.Fatalf("seedUser(%q) failed: %v", username, err)
	}
	return id
}

// TS-07-1: Verifies the workspaces table DDL defines all required columns
// with correct types and constraints.
func TestWorkspacesTable_ColumnDefinitions(t *testing.T) {
	db := newWorkspaceTestDB(t)
	defer db.Close()

	rows, err := db.Query("PRAGMA table_info('workspaces')")
	if err != nil {
		t.Fatalf("PRAGMA table_info failed: %v", err)
	}
	defer rows.Close()

	type colInfo struct {
		Name    string
		Type    string
		NotNull int
		PK      int
	}

	cols := make(map[string]colInfo)
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan column info failed: %v", err)
		}
		cols[name] = colInfo{Name: name, Type: colType, NotNull: notNull, PK: pk}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration failed: %v", err)
	}

	// Required columns per spec 07-REQ-1.1.
	expected := []struct {
		name    string
		colType string
		notNull int // 1 = NOT NULL required
		pk      int // 1 = PRIMARY KEY
	}{
		{"id", "TEXT", 0, 1}, // PRIMARY KEY implies NOT NULL in SQLite but notnull flag may be 0
		{"slug", "TEXT", 1, 0},
		{"git_url", "TEXT", 1, 0},
		{"branch", "TEXT", 0, 0},    // nullable
		{"owner_id", "TEXT", 1, 0},  // NOT NULL, FK to users(id)
		{"team_id", "TEXT", 0, 0},   // nullable, FK to teams(id)
		{"status", "TEXT", 1, 0},    // NOT NULL DEFAULT 'active'
		{"created_at", "DATETIME", 1, 0}, // NOT NULL DEFAULT CURRENT_TIMESTAMP
	}

	for _, exp := range expected {
		col, ok := cols[exp.name]
		if !ok {
			t.Errorf("column %q not found in workspaces table", exp.name)
			continue
		}

		if !strings.EqualFold(col.Type, exp.colType) {
			t.Errorf("column %q: expected type %q, got %q", exp.name, exp.colType, col.Type)
		}

		if exp.pk == 1 {
			if col.PK != 1 {
				t.Errorf("column %q: expected PRIMARY KEY, but pk=%d", exp.name, col.PK)
			}
		} else if exp.notNull == 1 && col.NotNull != 1 {
			t.Errorf("column %q: expected NOT NULL, but notnull=%d", exp.name, col.NotNull)
		}

		// Nullable columns must NOT have NOT NULL.
		if exp.notNull == 0 && exp.pk == 0 && col.NotNull == 1 {
			t.Errorf("column %q: expected nullable, but notnull=%d", exp.name, col.NotNull)
		}
	}

	// Ensure old columns (name, url, created_by) are NOT present.
	for _, oldCol := range []string{"name", "url", "created_by"} {
		if _, ok := cols[oldCol]; ok {
			t.Errorf("legacy column %q should not exist in new workspaces table", oldCol)
		}
	}
}

// TS-07-2: Verifies the UNIQUE constraint on slug and NOT NULL constraints
// are enforced at the database layer.
func TestWorkspacesTable_UniqueSlugConstraint(t *testing.T) {
	db := newWorkspaceTestDB(t)
	defer db.Close()

	userID := seedUser(t, db, "slug-constraint-user")

	// First insert succeeds.
	_, err := db.Exec(
		`INSERT INTO workspaces (id, slug, git_url, owner_id, status, created_at)
		 VALUES (?, 'test-ws', 'https://github.com/org/repo.git', ?, 'active', CURRENT_TIMESTAMP)`,
		uuid.New().String(), userID,
	)
	if err != nil {
		t.Fatalf("first insert failed: %v", err)
	}

	// Duplicate slug insert must fail.
	_, err = db.Exec(
		`INSERT INTO workspaces (id, slug, git_url, owner_id, status, created_at)
		 VALUES (?, 'test-ws', 'https://github.com/org/other.git', ?, 'active', CURRENT_TIMESTAMP)`,
		uuid.New().String(), userID,
	)
	if err == nil {
		t.Fatal("expected UNIQUE constraint error for duplicate slug, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unique constraint failed") {
		t.Errorf("expected 'UNIQUE constraint failed' in error, got: %v", err)
	}

	// Verify only one row exists with this slug.
	var count int
	if err := db.QueryRow("SELECT count(*) FROM workspaces WHERE slug='test-ws'").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row with slug='test-ws', got %d", count)
	}
}

// TS-07-3: Verifies that status defaults to 'active' and created_at defaults
// to CURRENT_TIMESTAMP when not explicitly provided.
func TestWorkspacesTable_StatusAndCreatedAtDefaults(t *testing.T) {
	db := newWorkspaceTestDB(t)
	defer db.Close()

	userID := seedUser(t, db, "defaults-user")

	// Insert without specifying status or created_at.
	wsID := uuid.New().String()
	_, err := db.Exec(
		`INSERT INTO workspaces (id, slug, git_url, owner_id)
		 VALUES (?, 'my-ws', 'https://github.com/org/repo.git', ?)`,
		wsID, userID,
	)
	if err != nil {
		t.Fatalf("insert without defaults failed: %v", err)
	}

	var status string
	var createdAt sql.NullString
	err = db.QueryRow(
		"SELECT status, created_at FROM workspaces WHERE id=?", wsID,
	).Scan(&status, &createdAt)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if status != "active" {
		t.Errorf("expected status 'active', got %q", status)
	}

	if !createdAt.Valid || createdAt.String == "" {
		t.Fatal("expected created_at to be non-null, got NULL or empty")
	}

	// created_at should be approximately now (within 5 seconds).
	parsed, err := time.Parse("2006-01-02 15:04:05", createdAt.String)
	if err != nil {
		// Try RFC 3339 format as well.
		parsed, err = time.Parse(time.RFC3339, createdAt.String)
		if err != nil {
			t.Fatalf("created_at %q is not parseable as a timestamp: %v", createdAt.String, err)
		}
	}
	diff := time.Since(parsed)
	if diff < 0 {
		diff = -diff
	}
	if diff > 5*time.Second {
		t.Errorf("created_at %q is not approximately now (diff=%v)", createdAt.String, diff)
	}
}

// TS-07-E1: Verifies the database rejects a duplicate slug INSERT with a
// UNIQUE constraint violation and leaves the existing row unmodified.
func TestWorkspacesTable_DuplicateSlugLeavesOriginalUnchanged(t *testing.T) {
	db := newWorkspaceTestDB(t)
	defer db.Close()

	userID := seedUser(t, db, "dup-edge-user")
	origID := uuid.New().String()

	// Insert original row.
	_, err := db.Exec(
		`INSERT INTO workspaces (id, slug, git_url, owner_id, status, created_at)
		 VALUES (?, 'dup-ws', 'https://github.com/org/repo.git', ?, 'active', CURRENT_TIMESTAMP)`,
		origID, userID,
	)
	if err != nil {
		t.Fatalf("original insert failed: %v", err)
	}

	// Attempt duplicate slug insert.
	_, err = db.Exec(
		`INSERT INTO workspaces (id, slug, git_url, owner_id, status, created_at)
		 VALUES (?, 'dup-ws', 'https://github.com/org/other.git', ?, 'active', CURRENT_TIMESTAMP)`,
		uuid.New().String(), userID,
	)
	if err == nil {
		t.Fatal("expected UNIQUE constraint error for duplicate slug, got nil")
	}
	if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Errorf("expected 'UNIQUE constraint failed' in error, got: %v", err)
	}

	// Verify count remains 1.
	var count int
	if err := db.QueryRow("SELECT count(*) FROM workspaces WHERE slug='dup-ws'").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row with slug='dup-ws', got %d", count)
	}

	// Verify original row is unchanged.
	var id string
	if err := db.QueryRow("SELECT id FROM workspaces WHERE slug='dup-ws'").Scan(&id); err != nil {
		t.Fatalf("query original row failed: %v", err)
	}
	if id != origID {
		t.Errorf("expected original id %q, got %q", origID, id)
	}
}

// TS-07-E2: Verifies DDL creation of the workspaces table fails or is blocked
// when the teams table does not exist (spec 06 dependency).
func TestWorkspacesTable_FailsWithoutTeamsTable(t *testing.T) {
	// Use a completely fresh database without any prior schema.
	dir := t.TempDir()
	dbPath := dir + "/bare.db"

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open bare db: %v", err)
	}
	defer rawDB.Close()

	// Enable WAL mode (required by OpenDatabase).
	if _, err := rawDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("failed to enable WAL: %v", err)
	}

	// Enable FK enforcement so missing teams table causes an error.
	if _, err := rawDB.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("failed to enable foreign_keys: %v", err)
	}

	// Create ONLY the users table (not teams).
	_, err = rawDB.Exec(`CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		username TEXT UNIQUE NOT NULL,
		email TEXT,
		full_name TEXT,
		provider TEXT NOT NULL,
		provider_id TEXT NOT NULL,
		status TEXT DEFAULT 'active',
		created_at TEXT,
		updated_at TEXT,
		UNIQUE(provider, provider_id)
	)`)
	if err != nil {
		t.Fatalf("failed to create users table: %v", err)
	}

	// Attempt to create the workspaces table which references teams(id).
	// This should fail because the teams table does not exist.
	//
	// Note: SQLite with CREATE TABLE IF NOT EXISTS may not error on the DDL
	// itself (it defers FK checks to DML), but with FK enforcement ON, an
	// INSERT referencing the missing teams table will fail. We test BOTH:
	// 1. If DDL errors, that's a valid failure mode.
	// 2. If DDL succeeds but INSERT with team_id fails, that also confirms
	//    the dependency.
	workspacesDDL := `CREATE TABLE IF NOT EXISTS workspaces (
		id TEXT PRIMARY KEY,
		slug TEXT UNIQUE NOT NULL,
		git_url TEXT NOT NULL,
		branch TEXT,
		owner_id TEXT NOT NULL REFERENCES users(id),
		team_id TEXT REFERENCES teams(id),
		status TEXT NOT NULL DEFAULT 'active',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`

	_, ddlErr := rawDB.Exec(workspacesDDL)

	if ddlErr != nil {
		// DDL itself failed — teams table dependency is enforced at DDL level.
		t.Logf("DDL correctly failed without teams table: %v", ddlErr)
		return
	}

	// DDL succeeded (SQLite defers FK checks). Try to insert with a team_id.
	userID := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = rawDB.Exec(
		`INSERT INTO users (id, username, email, provider, provider_id, status, created_at, updated_at)
		 VALUES (?, 'fk-test', 'fk@test.com', 'local', 'fk1', 'active', ?, ?)`,
		userID, now, now,
	)
	if err != nil {
		t.Fatalf("insert user failed: %v", err)
	}

	_, insertErr := rawDB.Exec(
		`INSERT INTO workspaces (id, slug, git_url, owner_id, team_id)
		 VALUES (?, 'fk-ws', 'https://github.com/org/repo.git', ?, 'nonexistent-team-id')`,
		uuid.New().String(), userID,
	)
	if insertErr == nil {
		t.Error("expected FK error when inserting workspace with team_id referencing nonexistent teams table, got nil")
	} else {
		t.Logf("INSERT correctly failed with FK error: %v", insertErr)
	}
}
