package workspace

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openTestDB opens an in-memory SQLite database for test isolation.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Initialize the workspaces table schema.
	if err := initSchema(db); err != nil {
		t.Fatalf("failed to initialize schema: %v", err)
	}
	return db
}

// TS-01-1: Verify that a workspace record stores all required fields (slug,
// git_url, branch, owner_id, org_id, status, created_at, updated_at).
// Requirement: 01-REQ-1.1
func TestWorkspaceStore_AllFields(t *testing.T) {
	db := openTestDB(t)
	branch := "main"

	ws := &Workspace{
		Slug:    "my-workspace",
		GitURL:  "https://github.com/org/repo",
		Branch:  &branch,
		OwnerID: "user-uuid-1234",
		OrgID:   nil,
		Status:  "active",
	}

	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("insertWorkspace() returned error: %v", err)
	}

	var (
		slug, gitURL, ownerID, status, createdAt, updatedAt string
		branchVal, orgID                                     *string
	)
	row := db.QueryRow(
		"SELECT slug, git_url, branch, owner_id, org_id, status, created_at, updated_at FROM workspaces WHERE slug = ?",
		"my-workspace",
	)
	if err := row.Scan(&slug, &gitURL, &branchVal, &ownerID, &orgID, &status, &createdAt, &updatedAt); err != nil {
		t.Fatalf("querying workspace row: %v", err)
	}

	if slug != "my-workspace" {
		t.Errorf("slug = %q; want %q", slug, "my-workspace")
	}
	if gitURL != "https://github.com/org/repo" {
		t.Errorf("git_url = %q; want %q", gitURL, "https://github.com/org/repo")
	}
	if branchVal == nil || *branchVal != "main" {
		t.Errorf("branch = %v; want %q", branchVal, "main")
	}
	if ownerID != "user-uuid-1234" {
		t.Errorf("owner_id = %q; want %q", ownerID, "user-uuid-1234")
	}
	if orgID != nil {
		t.Errorf("org_id = %v; want nil", orgID)
	}
	if status != "active" {
		t.Errorf("status = %q; want %q", status, "active")
	}
	if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
		t.Errorf("created_at %q is not valid RFC 3339: %v", createdAt, err)
	}
	if _, err := time.Parse(time.RFC3339, updatedAt); err != nil {
		t.Errorf("updated_at %q is not valid RFC 3339: %v", updatedAt, err)
	}
}

// TS-01-8: Verify that the same git_url can appear in multiple workspaces
// with different slugs.
// Requirement: 01-REQ-1.8
func TestWorkspaceStore_DuplicateGitURL(t *testing.T) {
	db := openTestDB(t)
	sharedURL := "https://github.com/org/repo"

	wsA := &Workspace{
		Slug:    "ws-alice",
		GitURL:  sharedURL,
		OwnerID: "user-1",
		Status:  "active",
	}
	wsB := &Workspace{
		Slug:    "ws-bob",
		GitURL:  sharedURL,
		OwnerID: "user-1",
		Status:  "active",
	}

	if err := insertWorkspace(db, wsA); err != nil {
		t.Fatalf("insertWorkspace(ws-alice) returned error: %v", err)
	}
	if err := insertWorkspace(db, wsB); err != nil {
		t.Fatalf("insertWorkspace(ws-bob) returned error: %v", err)
	}

	rows, err := db.Query("SELECT slug FROM workspaces WHERE git_url = ?", sharedURL)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	defer rows.Close()

	slugs := make(map[string]bool)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		slugs[s] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}

	if len(slugs) != 2 {
		t.Errorf("got %d rows; want 2", len(slugs))
	}
	if !slugs["ws-alice"] {
		t.Error("missing slug ws-alice")
	}
	if !slugs["ws-bob"] {
		t.Error("missing slug ws-bob")
	}
}

// TS-01-9: Verify that deleted workspaces are physically removed and the
// value 'deleted' is never stored as status.
// Requirement: 01-REQ-1.9
func TestWorkspaceStore_DeletePhysicalRemoval(t *testing.T) {
	db := openTestDB(t)

	ws := &Workspace{
		Slug:    "ws-to-delete",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "archived",
	}

	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("insertWorkspace() returned error: %v", err)
	}

	if err := deleteWorkspace(db, "ws-to-delete"); err != nil {
		t.Fatalf("deleteWorkspace() returned error: %v", err)
	}

	// Verify the row is physically gone.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", "ws-to-delete").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("row with slug 'ws-to-delete' still exists; want 0 rows")
	}

	// Verify no row has status 'deleted' at any time.
	if err := db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE status = ?", "deleted").Scan(&count); err != nil {
		t.Fatalf("deleted status query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("found %d rows with status='deleted'; want 0", count)
	}
}

// TS-01-10: Verify that workspace records store created_at and updated_at as
// valid RFC 3339 strings.
// Requirement: 01-REQ-1.10
func TestWorkspaceStore_RFC3339Timestamps(t *testing.T) {
	db := openTestDB(t)

	ws := &Workspace{
		Slug:    "ts-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "active",
	}

	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("insertWorkspace() returned error: %v", err)
	}

	var createdAt, updatedAt string
	row := db.QueryRow("SELECT created_at, updated_at FROM workspaces WHERE slug = ?", "ts-ws")
	if err := row.Scan(&createdAt, &updatedAt); err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
		t.Errorf("created_at %q is not valid RFC 3339: %v", createdAt, err)
	}
	if _, err := time.Parse(time.RFC3339, updatedAt); err != nil {
		t.Errorf("updated_at %q is not valid RFC 3339: %v", updatedAt, err)
	}
}
