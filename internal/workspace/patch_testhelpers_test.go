package workspace

import (
	"database/sql"
	"testing"
	"time"
)

// The helpers of this file seed carry_patch workspaces and patch rows for the
// tests of the hub's own handling of them (access by org members, cascade
// delete, the sync response).

// newPatchTestEnv is newTestEnv with a carry_patch workspace of the given slug
// and integration branch, owned by user-1.
func newPatchTestEnv(t *testing.T, slug, integrationBranch string) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	ensureCarryPatchColumns(t, env.db)
	seedCarryPatchWorkspaceRaw(t, env.db, slug,
		"https://github.com/fork/repo.git",
		"https://github.com/upstream/repo.git",
		integrationBranch)
	return env
}

// seedPatchRaw inserts an active patch row.
func seedPatchRaw(t *testing.T, db *sql.DB, id, workspaceSlug, branchName string, position int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES (?, ?, ?, ?, 'active', ?, ?)`,
		id, workspaceSlug, branchName, position, now, now,
	)
	if err != nil {
		t.Fatalf("seedPatchRaw(%q, %q, %q, %d) failed: %v", id, workspaceSlug, branchName, position, err)
	}
}
