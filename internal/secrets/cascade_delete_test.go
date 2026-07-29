package secrets

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// setupCascadeTestDB opens an in-memory SQLite database with secrets, variables,
// users, orgs, org_members, and workspaces tables for cascading deletion tests.
func setupCascadeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)

	schemaSQL := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT NOT NULL PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS orgs (
			id TEXT NOT NULL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			slug TEXT NOT NULL UNIQUE,
			url TEXT,
			owner_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS org_members (
			org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (org_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS workspaces (
			slug TEXT NOT NULL PRIMARY KEY,
			git_url TEXT NOT NULL,
			branch TEXT,
			owner_id TEXT NOT NULL,
			org_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			display_name TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			clone_status TEXT NOT NULL DEFAULT 'pending',
			head_sha TEXT,
			clone_error TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range schemaSQL {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create supporting schema: %v", err)
		}
	}
	return db
}

// seedUser inserts a user row into the database for test setup.
func seedUser(t *testing.T, db *sql.DB, userID, name, email string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO users (id, name, email, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		userID, name, email, now, now,
	)
	if err != nil {
		t.Fatalf("seedUser(%q) returned error: %v", userID, err)
	}
}

// seedOrgForCascade inserts an org row into the database for cascade tests.
func seedOrgForCascade(t *testing.T, db *sql.DB, orgID, name, slug string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO orgs (id, name, slug, status, created_at, updated_at) VALUES (?, ?, ?, 'active', ?, ?)`,
		orgID, name, slug, now, now,
	)
	if err != nil {
		t.Fatalf("seedOrgForCascade(%q) returned error: %v", orgID, err)
	}
}

// seedWorkspaceForCascade inserts a workspace row into the database for cascade tests.
func seedWorkspaceForCascade(t *testing.T, db *sql.DB, slug, ownerID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, display_name, description, clone_status, created_at, updated_at)
		 VALUES (?, ?, ?, 'active', ?, '', 'pending', ?, ?)`,
		slug, "https://github.com/org/repo", ownerID, slug, now, now,
	)
	if err != nil {
		t.Fatalf("seedWorkspaceForCascade(%q) returned error: %v", slug, err)
	}
}

// TS-07-39: Verifies that deleting a user also deletes all associated secrets
// and variables within the same database transaction.
// Requirement: 07-REQ-17.1
func TestCascadeDelete_UserDeletesSecretsAndVars(t *testing.T) {
	db := setupCascadeTestDB(t)
	store := NewStore(db)

	// Seed user with 3 secrets and 2 variables.
	seedUser(t, db, "user-cascade", "Cascade User", "cascade@test.com")
	seedSecrets(t, db, "user", "user-cascade", 3)
	seedVariables(t, db, "user", "user-cascade", 2)

	// Verify data exists before deletion.
	var secretCount, varCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM secrets WHERE owner_type = ? AND owner_id = ?",
		"user", "user-cascade").Scan(&secretCount)
	_ = db.QueryRow("SELECT COUNT(*) FROM variables WHERE owner_type = ? AND owner_id = ?",
		"user", "user-cascade").Scan(&varCount)
	if secretCount != 3 {
		t.Fatalf("expected 3 secrets before deletion; got %d", secretCount)
	}
	if varCount != 2 {
		t.Fatalf("expected 2 variables before deletion; got %d", varCount)
	}

	// Delete user via cascade.
	err := store.DeleteUserCascade("user-cascade")
	if err != nil {
		t.Fatalf("DeleteUserCascade() returned error: %v", err)
	}

	// Verify user row is deleted.
	var userCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM users WHERE id = ?", "user-cascade").Scan(&userCount)
	if userCount != 0 {
		t.Errorf("user still exists after cascade deletion; count = %d", userCount)
	}

	// Verify all secrets are deleted.
	_ = db.QueryRow("SELECT COUNT(*) FROM secrets WHERE owner_type = ? AND owner_id = ?",
		"user", "user-cascade").Scan(&secretCount)
	if secretCount != 0 {
		t.Errorf("expected 0 secrets after cascade; got %d", secretCount)
	}

	// Verify all variables are deleted.
	_ = db.QueryRow("SELECT COUNT(*) FROM variables WHERE owner_type = ? AND owner_id = ?",
		"user", "user-cascade").Scan(&varCount)
	if varCount != 0 {
		t.Errorf("expected 0 variables after cascade; got %d", varCount)
	}
}

// TestCascadeDelete_OrgDeletesSecretsAndVars verifies that deleting an org
// also deletes all org-scoped secrets and variables in the same transaction.
// Requirement: 07-REQ-17.1
func TestCascadeDelete_OrgDeletesSecretsAndVars(t *testing.T) {
	db := setupCascadeTestDB(t)
	store := NewStore(db)

	seedOrgForCascade(t, db, "org-cascade", "Cascade Org", "cascade-org")
	seedSecrets(t, db, "org", "org-cascade", 2)
	seedVariables(t, db, "org", "org-cascade", 3)

	err := store.DeleteOrgCascade("org-cascade")
	if err != nil {
		t.Fatalf("DeleteOrgCascade() returned error: %v", err)
	}

	// Verify org row is deleted.
	var orgCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM orgs WHERE id = ?", "org-cascade").Scan(&orgCount)
	if orgCount != 0 {
		t.Errorf("org still exists after cascade deletion; count = %d", orgCount)
	}

	// Verify all secrets are deleted.
	var secretCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM secrets WHERE owner_type = ? AND owner_id = ?",
		"org", "org-cascade").Scan(&secretCount)
	if secretCount != 0 {
		t.Errorf("expected 0 secrets after cascade; got %d", secretCount)
	}

	// Verify all variables are deleted.
	var varCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM variables WHERE owner_type = ? AND owner_id = ?",
		"org", "org-cascade").Scan(&varCount)
	if varCount != 0 {
		t.Errorf("expected 0 variables after cascade; got %d", varCount)
	}
}

// TestCascadeDelete_WorkspaceDeletesSecretsAndVars verifies that deleting a
// workspace also deletes all workspace-scoped secrets and variables.
// Requirement: 07-REQ-17.1
func TestCascadeDelete_WorkspaceDeletesSecretsAndVars(t *testing.T) {
	db := setupCascadeTestDB(t)
	store := NewStore(db)

	seedWorkspaceForCascade(t, db, "ws-cascade", "user-ws-cascade")
	seedSecrets(t, db, "workspace", "ws-cascade", 2)
	seedVariables(t, db, "workspace", "ws-cascade", 1)

	err := store.DeleteWorkspaceCascade("ws-cascade")
	if err != nil {
		t.Fatalf("DeleteWorkspaceCascade() returned error: %v", err)
	}

	// Verify workspace row is deleted.
	var wsCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", "ws-cascade").Scan(&wsCount)
	if wsCount != 0 {
		t.Errorf("workspace still exists after cascade deletion; count = %d", wsCount)
	}

	// Verify all secrets are deleted.
	var secretCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM secrets WHERE owner_type = ? AND owner_id = ?",
		"workspace", "ws-cascade").Scan(&secretCount)
	if secretCount != 0 {
		t.Errorf("expected 0 secrets after cascade; got %d", secretCount)
	}

	// Verify all variables are deleted.
	var varCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM variables WHERE owner_type = ? AND owner_id = ?",
		"workspace", "ws-cascade").Scan(&varCount)
	if varCount != 0 {
		t.Errorf("expected 0 variables after cascade; got %d", varCount)
	}
}

// TS-07-40: Verifies that parent resource deletion and child secrets/variables
// deletions are wrapped in a single database transaction so that if the child
// deletion fails, the parent resource remains intact (rollback).
// Requirement: 07-REQ-17.2
func TestCascadeDelete_RollbackOnChildFailure(t *testing.T) {
	db := setupCascadeTestDB(t)
	store := NewStore(db)

	seedWorkspaceForCascade(t, db, "ws-rollback", "user-ws-rollback")
	seedSecrets(t, db, "workspace", "ws-rollback", 2)
	seedVariables(t, db, "workspace", "ws-rollback", 1)

	// Inject a failure hook that fires after the parent row is deleted but
	// before child secrets/variables are deleted. This simulates a mid-
	// transaction failure. The store should roll back the entire transaction.
	store.TestHookAfterParentDelete = func() error {
		return fmt.Errorf("simulated failure after parent delete")
	}

	err := store.DeleteWorkspaceCascade("ws-rollback")
	if err == nil {
		t.Fatal("DeleteWorkspaceCascade() should return error when hook fails")
	}

	// Verify workspace still exists (rolled back).
	var wsCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", "ws-rollback").Scan(&wsCount)
	if wsCount != 1 {
		t.Errorf("workspace count = %d; want 1 (should be rolled back)", wsCount)
	}

	// Verify secrets still exist (rolled back).
	var secretCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM secrets WHERE owner_type = ? AND owner_id = ?",
		"workspace", "ws-rollback").Scan(&secretCount)
	if secretCount != 2 {
		t.Errorf("secret count = %d; want 2 (should be rolled back)", secretCount)
	}

	// Verify variables still exist (rolled back).
	var varCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM variables WHERE owner_type = ? AND owner_id = ?",
		"workspace", "ws-rollback").Scan(&varCount)
	if varCount != 1 {
		t.Errorf("variable count = %d; want 1 (should be rolled back)", varCount)
	}
}

// TestCascadeDelete_NoSecretsOrVars verifies that cascade deletion succeeds
// even when there are no secrets or variables to delete.
// Requirement: 07-REQ-17.E2
func TestCascadeDelete_NoSecretsOrVars(t *testing.T) {
	db := setupCascadeTestDB(t)
	store := NewStore(db)

	seedWorkspaceForCascade(t, db, "ws-empty-cascade", "user-ws-empty")

	// No secrets or variables seeded for this workspace.
	err := store.DeleteWorkspaceCascade("ws-empty-cascade")
	if err != nil {
		t.Fatalf("DeleteWorkspaceCascade() returned error: %v", err)
	}

	// Verify workspace is deleted.
	var wsCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", "ws-empty-cascade").Scan(&wsCount)
	if wsCount != 0 {
		t.Errorf("workspace still exists after cascade deletion; count = %d", wsCount)
	}
}

// TestCascadeDelete_UserWithNoData verifies that cascading user deletion
// works when the user has no secrets or variables.
// Requirement: 07-REQ-17.E2
func TestCascadeDelete_UserWithNoData(t *testing.T) {
	db := setupCascadeTestDB(t)
	store := NewStore(db)

	seedUser(t, db, "user-nodata", "NoData User", "nodata@test.com")

	err := store.DeleteUserCascade("user-nodata")
	if err != nil {
		t.Fatalf("DeleteUserCascade() returned error: %v", err)
	}

	var userCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM users WHERE id = ?", "user-nodata").Scan(&userCount)
	if userCount != 0 {
		t.Errorf("user still exists after deletion; count = %d", userCount)
	}
}
