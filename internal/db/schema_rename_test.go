package db

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// TS-06-1: Verifies that internal/db/db.go defines a `teams` table and
// `team_members` table with a `team_id` FK column, matching the previously
// defined schema structure.
// Requirement: 06-REQ-1.1
// ---------------------------------------------------------------------------

func TestDDLContainsTeamsTable(t *testing.T) {
	content, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("failed to read db.go: %v", err)
	}
	src := string(content)

	t.Run("teams table defined", func(t *testing.T) {
		if !strings.Contains(src, "CREATE TABLE") || !strings.Contains(src, "teams") {
			t.Error("db.go does not contain a CREATE TABLE statement for 'teams'")
		}
		// More precise: match the DDL string for CREATE TABLE ... teams
		re := regexp.MustCompile(`CREATE TABLE\s+(IF NOT EXISTS\s+)?teams\s*\(`)
		if !re.MatchString(src) {
			t.Error("db.go does not contain 'CREATE TABLE [IF NOT EXISTS] teams (' DDL")
		}
	})

	t.Run("team_members table defined", func(t *testing.T) {
		re := regexp.MustCompile(`CREATE TABLE\s+(IF NOT EXISTS\s+)?team_members\s*\(`)
		if !re.MatchString(src) {
			t.Error("db.go does not contain 'CREATE TABLE [IF NOT EXISTS] team_members (' DDL")
		}
	})

	t.Run("team_id FK in team_members DDL", func(t *testing.T) {
		// Extract the team_members DDL block and check for team_id column
		re := regexp.MustCompile(`(?s)CREATE TABLE\s+(IF NOT EXISTS\s+)?team_members\s*\([^)]+\)`)
		match := re.FindString(src)
		if match == "" {
			t.Fatal("could not find team_members DDL block")
		}
		if !strings.Contains(match, "team_id") {
			t.Error("team_members DDL does not contain 'team_id' column")
		}
	})

	t.Run("team_id FK in api_keys DDL", func(t *testing.T) {
		// Extract the api_keys DDL block and check for team_id column
		re := regexp.MustCompile(`(?s)CREATE TABLE\s+(IF NOT EXISTS\s+)?api_keys\s*\([^)]+\)`)
		match := re.FindString(src)
		if match == "" {
			t.Fatal("could not find api_keys DDL block")
		}
		if !strings.Contains(match, "team_id") {
			t.Error("api_keys DDL does not contain 'team_id' column")
		}
	})
}

// ---------------------------------------------------------------------------
// TS-06-2: Verifies that internal/db/db.go contains no references to legacy
// table or column names: `workspaces`, `workspace_members`, or `workspace_id`.
// Requirement: 06-REQ-1.2
// ---------------------------------------------------------------------------

func TestDDLNoLegacyWorkspaceNames(t *testing.T) {
	content, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("failed to read db.go: %v", err)
	}
	src := string(content)

	t.Run("no workspaces table reference", func(t *testing.T) {
		// Match word-boundary 'workspaces' — the legacy table name.
		re := regexp.MustCompile(`\bworkspaces\b`)
		if re.MatchString(src) {
			t.Error("db.go still contains a reference to legacy table name 'workspaces'")
		}
	})

	t.Run("no workspace_members table reference", func(t *testing.T) {
		re := regexp.MustCompile(`\bworkspace_members\b`)
		if re.MatchString(src) {
			t.Error("db.go still contains a reference to legacy table name 'workspace_members'")
		}
	})

	t.Run("no workspace_id column reference", func(t *testing.T) {
		re := regexp.MustCompile(`\bworkspace_id\b`)
		if re.MatchString(src) {
			t.Error("db.go still contains a reference to legacy column name 'workspace_id'")
		}
	})
}

// ---------------------------------------------------------------------------
// TS-06-3: Verifies that the schema initialisation function successfully
// creates the `teams` and `team_members` tables in a fresh SQLite database
// without returning an error.
// Requirement: 06-REQ-1.3
// ---------------------------------------------------------------------------

func TestSchemaInit_TeamsAndTeamMembersTables(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	err := InitSchema(db)
	if err != nil {
		t.Fatalf("InitSchema returned error: %v", err)
	}

	// Verify 'teams' table exists
	assertTableExists(t, db, "teams")

	// Verify 'team_members' table exists
	assertTableExists(t, db, "team_members")

	// Verify 'api_keys' table exists
	assertTableExists(t, db, "api_keys")

	// Verify team_members has 'team_id' column
	assertColumnExists(t, db, "team_members", "team_id")

	// Verify api_keys has 'team_id' column
	assertColumnExists(t, db, "api_keys", "team_id")

	// Negative: verify legacy table names do NOT exist
	assertTableNotExists(t, db, "workspaces")
	assertTableNotExists(t, db, "workspace_members")

	// Negative: verify legacy column names do NOT exist
	assertColumnNotExists(t, db, "api_keys", "workspace_id")
}

// ---------------------------------------------------------------------------
// TS-06-E1: Verifies that a grep/AST scan of internal/db/db.go for legacy
// names returns a non-zero exit code if any residual reference exists.
// Requirement: 06-REQ-1.E1
// ---------------------------------------------------------------------------

func TestLegacyNameGrepLint(t *testing.T) {
	// Locate the project root by walking up from the test working directory
	// (internal/db/) to find go.mod.
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("could not find project root: %v", err)
	}
	dbGoPath := filepath.Join(projectRoot, "internal", "db", "db.go")

	t.Run("grep finds no legacy names in db.go", func(t *testing.T) {
		// grep -E 'workspaces|workspace_members|workspace_id' internal/db/db.go
		// Expected: exit code 1 (no matches) when the file is correctly renamed.
		cmd := exec.Command("grep", "-E", `workspaces|workspace_members|workspace_id`, dbGoPath)
		output, err := cmd.CombinedOutput()
		if err == nil {
			// grep exited 0, meaning it found matches — this is a CI failure
			t.Errorf("grep found legacy names in db.go (should find none):\n%s", string(output))
		}
		// err != nil with exit code 1 means no matches — that's the passing case
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() != 1 {
				t.Errorf("grep exited with unexpected code %d (expected 1 for no match): %s",
					exitErr.ExitCode(), string(output))
			}
			// exit code 1 = no matches found = CI passes — this is correct
		}
	})

	t.Run("grep correctly detects legacy names when present", func(t *testing.T) {
		// Simulate a broken state: write a temp file with legacy names
		// and verify grep finds them (exit code 0).
		tmpDir := t.TempDir()
		tmpFile := filepath.Join(tmpDir, "bad_ddl.go")
		badContent := []byte(`package db
const ddl = "CREATE TABLE workspaces (id TEXT PRIMARY KEY)"
`)
		if err := os.WriteFile(tmpFile, badContent, 0644); err != nil {
			t.Fatalf("failed to write temp file: %v", err)
		}

		cmd := exec.Command("grep", "-E", `workspaces|workspace_members|workspace_id`, tmpFile)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("grep did not find legacy names in simulated bad file (should have found them): %v\noutput: %s",
				err, string(output))
		}
		// exit code 0 = matches found = grep works correctly for the negative case
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// assertTableExists checks that the named table exists in sqlite_master.
func assertTableExists(t *testing.T, db *sql.DB, tableName string) {
	t.Helper()
	var name string
	err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name=?",
		tableName,
	).Scan(&name)
	if err != nil {
		t.Errorf("table %q not found in database: %v", tableName, err)
	}
}

// assertTableNotExists checks that the named table does NOT exist in sqlite_master.
func assertTableNotExists(t *testing.T, db *sql.DB, tableName string) {
	t.Helper()
	var name string
	err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name=?",
		tableName,
	).Scan(&name)
	if err == nil {
		t.Errorf("legacy table %q should not exist in database, but it does", tableName)
	}
}

// assertColumnExists checks that a column exists in the given table
// using PRAGMA table_info.
func assertColumnExists(t *testing.T, db *sql.DB, tableName, columnName string) {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + tableName + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s) failed: %v", tableName, err)
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue *string
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("failed to scan PRAGMA row: %v", err)
		}
		if name == columnName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("column %q not found in table %q", columnName, tableName)
	}
}

// assertColumnNotExists checks that a column does NOT exist in the given table.
func assertColumnNotExists(t *testing.T, db *sql.DB, tableName, columnName string) {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + tableName + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s) failed: %v", tableName, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue *string
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("failed to scan PRAGMA row: %v", err)
		}
		if name == columnName {
			t.Errorf("legacy column %q should not exist in table %q, but it does", columnName, tableName)
			return
		}
	}
}

// findProjectRoot walks up from the current directory to find the directory
// containing go.mod.
func findProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
