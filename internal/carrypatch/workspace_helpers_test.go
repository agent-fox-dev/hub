package carrypatch

import (
	"database/sql"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// The helpers of this file serve the tests that drive the workspace package's
// patch routes with the carry-patch resolver behind them.

// workspaceTestDB is an in-memory database with the workspace schema and the
// apikit orgs tables the workspace handlers read.
func workspaceTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	if err := workspace.InitSchema(db); err != nil {
		t.Fatalf("workspace schema: %v", err)
	}
	if err := secrets.InitSchema(db); err != nil {
		t.Fatalf("secrets schema: %v", err)
	}
	for _, stmt := range []string{
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
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("org schema: %v", err)
		}
	}
	return db
}

// ensureCarryPatchColumns adds the carry-patch columns to a workspaces table
// that predates them. Errors are ignored: the column may already exist.
func ensureCarryPatchColumns(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, ddl := range []string{
		`ALTER TABLE workspaces ADD COLUMN workspace_mode TEXT NOT NULL DEFAULT 'standard'`,
		`ALTER TABLE workspaces ADD COLUMN upstream_url TEXT`,
		`ALTER TABLE workspaces ADD COLUMN integration_branch TEXT`,
	} {
		_, _ = db.Exec(ddl)
	}
}

// seedCarryPatchWorkspaceRaw inserts an active carry_patch workspace owned by
// user-1 with a pending clone.
func seedCarryPatchWorkspaceRaw(t *testing.T, db *sql.DB, slug, gitURL, upstreamURL, integrationBranch string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, display_name, description,
		 clone_status, created_at, updated_at, sync_mode, sync_status,
		 workspace_mode, upstream_url, integration_branch)
		 VALUES (?, ?, 'user-1', 'active', '', '', 'pending', ?, ?, 'pull_only', 'idle',
		 'carry_patch', ?, ?)`,
		slug, gitURL, now, now, upstreamURL, integrationBranch,
	)
	if err != nil {
		t.Fatalf("seed carry_patch workspace %q: %v", slug, err)
	}
}

// userAuth is the apikit.AuthInfo of a user API key.
func userAuth(userID string) *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "api_key", UserID: userID}
}

// newMockAuditEmitter returns an empty mockAuditEmitter.
func newMockAuditEmitter() *mockAuditEmitter {
	return &mockAuditEmitter{}
}
