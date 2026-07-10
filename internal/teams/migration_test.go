package teams_test

import (
	"database/sql"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/agent-fox-dev/hub/internal/teams"
)

// ---------------------------------------------------------------------------
// Test Helpers
// ---------------------------------------------------------------------------

// openTestDB opens an in-memory SQLite database with foreign keys enabled
// and registers cleanup.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory SQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Enable WAL mode and foreign keys to match production settings.
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatalf("failed to set %s: %v", pragma, err)
		}
	}
	return db
}

// createStubUsersTable creates a minimal users table that satisfies the
// foreign key constraint in team_members (user_id → users.id).
// The users table is owned by spec 02 (oauth_and_users); this is a test stub.
func createStubUsersTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id         TEXT PRIMARY KEY,
		username   TEXT NOT NULL,
		email      TEXT NOT NULL,
		full_name  TEXT NOT NULL DEFAULT '',
		status     TEXT NOT NULL DEFAULT 'active',
		provider   TEXT NOT NULL,
		provider_id TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	)`)
	if err != nil {
		t.Fatalf("failed to create stub users table: %v", err)
	}
}

// tableExists returns true if the given table exists in the database.
func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	err := db.QueryRow(
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", name,
	).Scan(&count)
	if err != nil {
		t.Fatalf("failed to check table existence for %q: %v", name, err)
	}
	return count > 0
}

// columnInfo holds metadata about a database column from PRAGMA table_info.
type columnInfo struct {
	CID     int
	Name    string
	Type    string
	NotNull bool
	Default sql.NullString
	PK      int // >0 for primary key columns
}

// getColumns returns column metadata for the given table, keyed by column name.
func getColumns(t *testing.T, db *sql.DB, table string) map[string]columnInfo {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s) failed: %v", table, err)
	}
	defer rows.Close()

	cols := make(map[string]columnInfo)
	for rows.Next() {
		var c columnInfo
		var notNull int
		if err := rows.Scan(&c.CID, &c.Name, &c.Type, &notNull, &c.Default, &c.PK); err != nil {
			t.Fatalf("failed to scan column info for %s: %v", table, err)
		}
		c.NotNull = notNull == 1
		cols[c.Name] = c
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error for %s: %v", table, err)
	}
	return cols
}

// indexDDL holds the SQL definition and metadata for an index from sqlite_master.
type indexDDL struct {
	Name string
	SQL  string // The CREATE INDEX statement
}

// getIndexDDLs returns all user-created index DDL statements for the given table.
func getIndexDDLs(t *testing.T, db *sql.DB, table string) []indexDDL {
	t.Helper()
	rows, err := db.Query(
		"SELECT name, sql FROM sqlite_master WHERE type='index' AND tbl_name=? AND sql IS NOT NULL",
		table,
	)
	if err != nil {
		t.Fatalf("failed to query indexes for %s: %v", table, err)
	}
	defer rows.Close()

	var indexes []indexDDL
	for rows.Next() {
		var idx indexDDL
		if err := rows.Scan(&idx.Name, &idx.SQL); err != nil {
			t.Fatalf("failed to scan index info for %s: %v", table, err)
		}
		indexes = append(indexes, idx)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error for %s indexes: %v", table, err)
	}
	return indexes
}

// fkInfo holds foreign key metadata from PRAGMA foreign_key_list.
type fkInfo struct {
	ID       int
	Seq      int
	Table    string
	From     string
	To       string
	OnUpdate string
	OnDelete string
	Match    string
}

// getForeignKeys returns foreign key constraints for the given table.
func getForeignKeys(t *testing.T, db *sql.DB, table string) []fkInfo {
	t.Helper()
	rows, err := db.Query("PRAGMA foreign_key_list(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA foreign_key_list(%s) failed: %v", table, err)
	}
	defer rows.Close()

	var fks []fkInfo
	for rows.Next() {
		var fk fkInfo
		if err := rows.Scan(&fk.ID, &fk.Seq, &fk.Table, &fk.From, &fk.To, &fk.OnUpdate, &fk.OnDelete, &fk.Match); err != nil {
			t.Fatalf("failed to scan FK info for %s: %v", table, err)
		}
		fks = append(fks, fk)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error for %s FKs: %v", table, err)
	}
	return fks
}

// initSchemaWithUsersStub creates the stub users table and then applies
// the teams schema. This is the common setup for migration tests.
func initSchemaWithUsersStub(t *testing.T, db *sql.DB) {
	t.Helper()
	createStubUsersTable(t, db)
	if err := teams.InitSchema(db); err != nil {
		t.Fatalf("InitSchema failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TS-03-1: Teams table schema
// Requirement: 03-REQ-1.1
//
// Verifies that the teams migration creates the `teams` table with all
// required columns and partial UNIQUE indexes on `name` and `slug`
// scoped to non-deleted rows.
// ---------------------------------------------------------------------------

func TestMigration_TeamsTableSchema(t *testing.T) {
	db := openTestDB(t)
	initSchemaWithUsersStub(t, db)

	t.Run("table_exists", func(t *testing.T) {
		if !tableExists(t, db, "teams") {
			t.Fatal("expected teams table to exist after InitSchema")
		}
	})

	t.Run("has_required_columns", func(t *testing.T) {
		if !tableExists(t, db, "teams") {
			t.Skip("teams table does not exist; skipping column checks")
		}
		cols := getColumns(t, db, "teams")

		expected := []struct {
			name     string
			wantType string
		}{
			{"id", "TEXT"},
			{"name", "TEXT"},
			{"slug", "TEXT"},
			{"url", "TEXT"},
			{"status", "TEXT"},
			{"created_at", "DATETIME"},
			{"updated_at", "DATETIME"},
		}

		for _, exp := range expected {
			col, ok := cols[exp.name]
			if !ok {
				t.Errorf("missing column %q in teams table", exp.name)
				continue
			}
			if !strings.EqualFold(col.Type, exp.wantType) {
				t.Errorf("column %q: got type %q, want %q", exp.name, col.Type, exp.wantType)
			}
		}
	})

	t.Run("id_is_primary_key", func(t *testing.T) {
		if !tableExists(t, db, "teams") {
			t.Skip("teams table does not exist")
		}
		cols := getColumns(t, db, "teams")
		idCol, ok := cols["id"]
		if !ok {
			t.Fatal("id column not found")
		}
		if idCol.PK == 0 {
			t.Error("id column should be the primary key (pk > 0)")
		}
	})

	t.Run("url_is_nullable", func(t *testing.T) {
		if !tableExists(t, db, "teams") {
			t.Skip("teams table does not exist")
		}
		cols := getColumns(t, db, "teams")
		urlCol, ok := cols["url"]
		if !ok {
			t.Fatal("url column not found")
		}
		if urlCol.NotNull {
			t.Error("url column should be nullable (NOT NULL should not be set)")
		}
	})

	t.Run("partial_unique_index_on_name", func(t *testing.T) {
		if !tableExists(t, db, "teams") {
			t.Skip("teams table does not exist")
		}
		indexes := getIndexDDLs(t, db, "teams")

		found := false
		for _, idx := range indexes {
			sql := strings.ToLower(idx.SQL)
			// Check for a UNIQUE index that covers `name` with a WHERE clause
			// excluding deleted teams.
			if strings.Contains(sql, "unique") &&
				strings.Contains(sql, "name") &&
				strings.Contains(sql, "where") &&
				strings.Contains(sql, "deleted") {
				found = true
				break
			}
		}
		if !found {
			t.Error("missing partial UNIQUE index on name WHERE status != 'deleted'")
		}
	})

	t.Run("partial_unique_index_on_slug", func(t *testing.T) {
		if !tableExists(t, db, "teams") {
			t.Skip("teams table does not exist")
		}
		indexes := getIndexDDLs(t, db, "teams")

		found := false
		for _, idx := range indexes {
			sql := strings.ToLower(idx.SQL)
			if strings.Contains(sql, "unique") &&
				strings.Contains(sql, "slug") &&
				strings.Contains(sql, "where") &&
				strings.Contains(sql, "deleted") {
				found = true
				break
			}
		}
		if !found {
			t.Error("missing partial UNIQUE index on slug WHERE status != 'deleted'")
		}
	})
}

// ---------------------------------------------------------------------------
// TS-03-2: Team members table schema
// Requirement: 03-REQ-1.2
//
// Verifies that the team_members migration creates the `team_members` table
// with correct columns, composite primary key, and foreign key constraints.
// ---------------------------------------------------------------------------

func TestMigration_TeamMembersTableSchema(t *testing.T) {
	db := openTestDB(t)
	initSchemaWithUsersStub(t, db)

	t.Run("table_exists", func(t *testing.T) {
		if !tableExists(t, db, "team_members") {
			t.Fatal("expected team_members table to exist after InitSchema")
		}
	})

	t.Run("has_required_columns", func(t *testing.T) {
		if !tableExists(t, db, "team_members") {
			t.Skip("team_members table does not exist; skipping column checks")
		}
		cols := getColumns(t, db, "team_members")

		for _, name := range []string{"team_id", "user_id", "created_at"} {
			if _, ok := cols[name]; !ok {
				t.Errorf("missing column %q in team_members table", name)
			}
		}
	})

	t.Run("composite_primary_key", func(t *testing.T) {
		if !tableExists(t, db, "team_members") {
			t.Skip("team_members table does not exist")
		}
		cols := getColumns(t, db, "team_members")

		// In SQLite, PRAGMA table_info reports pk > 0 for columns that
		// are part of the primary key, with pk value indicating position.
		teamIDCol, ok1 := cols["team_id"]
		userIDCol, ok2 := cols["user_id"]
		if !ok1 || !ok2 {
			t.Fatal("team_id or user_id column not found")
		}
		if teamIDCol.PK == 0 {
			t.Error("team_id should be part of the composite primary key (pk > 0)")
		}
		if userIDCol.PK == 0 {
			t.Error("user_id should be part of the composite primary key (pk > 0)")
		}

		// created_at should NOT be part of the primary key.
		createdAtCol, ok := cols["created_at"]
		if ok && createdAtCol.PK != 0 {
			t.Error("created_at should not be part of the primary key")
		}
	})

	t.Run("foreign_key_team_id_to_teams_with_cascade_delete", func(t *testing.T) {
		if !tableExists(t, db, "team_members") {
			t.Skip("team_members table does not exist")
		}
		fks := getForeignKeys(t, db, "team_members")

		found := false
		for _, fk := range fks {
			if fk.From == "team_id" && fk.Table == "teams" && fk.To == "id" {
				found = true
				if !strings.EqualFold(fk.OnDelete, "CASCADE") {
					t.Errorf("team_id FK on_delete: got %q, want CASCADE", fk.OnDelete)
				}
				break
			}
		}
		if !found {
			t.Error("missing foreign key: team_id -> teams(id)")
		}
	})

	t.Run("foreign_key_user_id_to_users", func(t *testing.T) {
		if !tableExists(t, db, "team_members") {
			t.Skip("team_members table does not exist")
		}
		fks := getForeignKeys(t, db, "team_members")

		found := false
		for _, fk := range fks {
			if fk.From == "user_id" && fk.Table == "users" {
				found = true
				break
			}
		}
		if !found {
			t.Error("missing foreign key: user_id -> users(id)")
		}
	})
}

// ---------------------------------------------------------------------------
// TS-03-3: Migration idempotency and ordering
// Requirement: 03-REQ-1.3
//
// Verifies that migration files are embedded via embed.FS, use sequential
// numeric prefixes, and are applied in ascending numeric order exactly once.
//
// Adapted for the project's boot-time DDL pattern: InitSchema uses
// CREATE TABLE IF NOT EXISTS for idempotency rather than a migration
// tracking table. See docs/errata/03_migration_and_schema_divergences.md.
// ---------------------------------------------------------------------------

func TestMigration_IdempotencyAndOrdering(t *testing.T) {
	t.Run("embedded_migration_files_exist", func(t *testing.T) {
		// Verify that MigrationsFS contains at least one SQL file.
		entries, err := fs.ReadDir(teams.MigrationsFS, ".")
		if err != nil {
			t.Fatalf("failed to read MigrationsFS root: %v", err)
		}

		var sqlFiles []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
				sqlFiles = append(sqlFiles, e.Name())
			}
		}
		// Also check subdirectories (e.g., "migrations/")
		if len(sqlFiles) == 0 {
			// Try common subdirectory names
			for _, dir := range []string{"migrations", "sql"} {
				subEntries, err := fs.ReadDir(teams.MigrationsFS, dir)
				if err != nil {
					continue
				}
				for _, e := range subEntries {
					if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
						sqlFiles = append(sqlFiles, e.Name())
					}
				}
			}
		}

		if len(sqlFiles) == 0 {
			t.Fatal("expected at least one .sql migration file in MigrationsFS, got none")
		}
	})

	t.Run("files_have_sequential_numeric_prefixes", func(t *testing.T) {
		// Collect all SQL files from MigrationsFS (root and subdirectories).
		var sqlFiles []string
		err := fs.WalkDir(teams.MigrationsFS, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), ".sql") {
				sqlFiles = append(sqlFiles, d.Name())
			}
			return nil
		})
		if err != nil {
			t.Fatalf("failed to walk MigrationsFS: %v", err)
		}
		if len(sqlFiles) == 0 {
			t.Skip("no SQL files found in MigrationsFS")
		}

		// All files must match pattern: NNN_<descriptive_name>.sql
		pattern := regexp.MustCompile(`^\d+_.*\.sql$`)
		var prefixes []int
		for _, f := range sqlFiles {
			if !pattern.MatchString(f) {
				t.Errorf("migration file %q does not match expected pattern ^\\d+_.*\\.sql$", f)
				continue
			}
			// Extract numeric prefix.
			parts := strings.SplitN(f, "_", 2)
			n, err := strconv.Atoi(parts[0])
			if err != nil {
				t.Errorf("failed to parse numeric prefix from %q: %v", f, err)
				continue
			}
			prefixes = append(prefixes, n)
		}

		// Prefixes must be in ascending order (files sorted by name achieve
		// this when prefixes are zero-padded).
		if len(prefixes) > 1 {
			sorted := make([]int, len(prefixes))
			copy(sorted, prefixes)
			sort.Ints(sorted)
			for i := range prefixes {
				if prefixes[i] != sorted[i] {
					t.Errorf("migration file prefixes are not in ascending order: got %v, want %v", prefixes, sorted)
					break
				}
			}
		}
	})

	t.Run("second_run_is_idempotent", func(t *testing.T) {
		db := openTestDB(t)
		createStubUsersTable(t, db)

		// First run.
		if err := teams.InitSchema(db); err != nil {
			t.Fatalf("first InitSchema call failed: %v", err)
		}

		// Capture state after first run.
		teamsExist1 := tableExists(t, db, "teams")
		membersExist1 := tableExists(t, db, "team_members")

		// Second run — must not error.
		if err := teams.InitSchema(db); err != nil {
			t.Fatalf("second InitSchema call failed: %v", err)
		}

		// State should be unchanged.
		teamsExist2 := tableExists(t, db, "teams")
		membersExist2 := tableExists(t, db, "team_members")

		if teamsExist1 != teamsExist2 {
			t.Errorf("teams table existence changed between runs: %v -> %v", teamsExist1, teamsExist2)
		}
		if membersExist1 != membersExist2 {
			t.Errorf("team_members table existence changed between runs: %v -> %v", membersExist1, membersExist2)
		}

		// Both tables must exist after schema init.
		if !teamsExist2 {
			t.Error("teams table should exist after InitSchema")
		}
		if !membersExist2 {
			t.Error("team_members table should exist after InitSchema")
		}
	})
}

// ---------------------------------------------------------------------------
// TS-03-E1: Migration runner skips already-applied migration files
// Requirement: 03-REQ-1.E1
//
// Verifies that running InitSchema a second time on an already-migrated
// database does not re-apply or duplicate anything.
//
// Adapted: since the project uses CREATE TABLE IF NOT EXISTS rather than
// a migration tracking table, idempotency is verified by confirming the
// schema is unchanged and no errors occur on re-run.
// ---------------------------------------------------------------------------

func TestMigration_SkipsAlreadyApplied(t *testing.T) {
	db := openTestDB(t)
	createStubUsersTable(t, db)

	// Apply schema once.
	if err := teams.InitSchema(db); err != nil {
		t.Fatalf("first InitSchema failed: %v", err)
	}

	if !tableExists(t, db, "teams") {
		t.Fatal("teams table must exist after first InitSchema")
	}
	if !tableExists(t, db, "team_members") {
		t.Fatal("team_members table must exist after first InitSchema")
	}

	// Record column counts as a proxy for schema stability.
	cols1 := getColumns(t, db, "teams")
	idx1 := getIndexDDLs(t, db, "teams")

	// Apply schema again — must be idempotent.
	if err := teams.InitSchema(db); err != nil {
		t.Fatalf("second InitSchema failed: %v", err)
	}

	// Schema must be unchanged.
	cols2 := getColumns(t, db, "teams")
	idx2 := getIndexDDLs(t, db, "teams")

	if len(cols1) != len(cols2) {
		t.Errorf("teams column count changed: %d -> %d", len(cols1), len(cols2))
	}
	if len(idx1) != len(idx2) {
		t.Errorf("teams index count changed: %d -> %d", len(idx1), len(idx2))
	}

	// Verify no duplicate indexes were created.
	indexNames := make(map[string]int)
	for _, idx := range idx2 {
		indexNames[idx.Name]++
	}
	for name, count := range indexNames {
		if count > 1 {
			t.Errorf("duplicate index %q found after second InitSchema run (count=%d)", name, count)
		}
	}
}
