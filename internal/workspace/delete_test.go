package workspace

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// ========================================================================
// Spec 05 Task 3.2: Delete lifecycle changes
// (TS-05-24)
// ========================================================================

// TS-05-24: DELETE /api/v1/workspaces/:slug checks for and deletes the
// workspace directory if it exists, then deletes the database row, returning
// HTTP 204; logs a warning if directory deletion fails but still deletes
// the row.
// Requirement: 05-REQ-8.1

// TestDelete_Spec05_DirectoryCleanup verifies that deleting an archived
// workspace removes the workspace directory under WORKSPACE_ROOT before
// deleting the database row.
func TestDelete_Spec05_DirectoryCleanup(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = oldRoot }()

	env.seedWorkspace(t, &Workspace{
		Slug:    "archived-ws-to-delete",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "archived",
	})

	// Create workspace directory to verify it gets cleaned up.
	wsDir := filepath.Join(wsRoot, "archived-ws-to-delete")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}
	// Add a file inside to verify recursive deletion.
	if err := os.WriteFile(filepath.Join(wsDir, "leftover.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write leftover file: %v", err)
	}

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/archived-ws-to-delete", "",
		userAuth("user-1"))

	// Assert HTTP 204.
	if rec.Code != http.StatusNoContent {
		t.Fatalf("HTTP status = %d; want %d\nbody: %s",
			rec.Code, http.StatusNoContent, rec.Body.String())
	}

	// Assert workspace directory was deleted.
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("workspace directory %q should be deleted after DELETE", wsDir)
	}

	// Assert database row was deleted.
	var count int
	if err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?",
		"archived-ws-to-delete").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Errorf("workspace row still exists; want 0 rows after DELETE")
	}
}

// TestDelete_Spec05_DirectoryMissing verifies that when the workspace
// directory does not exist under WORKSPACE_ROOT, the delete handler
// skips directory deletion and proceeds to delete the database row.
// Requirement: 05-REQ-8.E1
func TestDelete_Spec05_DirectoryMissing(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = oldRoot }()

	env.seedWorkspace(t, &Workspace{
		Slug:    "no-dir-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "archived",
	})

	// Do NOT create the workspace directory — it should not exist.
	wsDir := filepath.Join(wsRoot, "no-dir-ws")
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Fatalf("precondition: workspace directory %q should not exist", wsDir)
	}

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/no-dir-ws", "",
		userAuth("user-1"))

	// Assert HTTP 204 (success even without directory to delete).
	if rec.Code != http.StatusNoContent {
		t.Fatalf("HTTP status = %d; want %d\nbody: %s",
			rec.Code, http.StatusNoContent, rec.Body.String())
	}

	// Assert database row was deleted.
	var count int
	if err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?",
		"no-dir-ws").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Errorf("workspace row still exists; want 0 rows after DELETE")
	}
}

// TestDelete_Spec05_NotArchivedReturns409 verifies that deleting a workspace
// that is not in archived status returns HTTP 409.
// Requirement: 05-REQ-8.E2
func TestDelete_Spec05_NotArchivedReturns409(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "active-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "active",
	})

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/active-ws", "",
		userAuth("user-1"))

	// Spec 05-REQ-8.E2 requires HTTP 409.
	// Note: current implementation returns 400; spec 05 changes this to 409.
	if rec.Code != http.StatusConflict {
		t.Errorf("HTTP status = %d; want %d (workspace must be archived before deletion)",
			rec.Code, http.StatusConflict)
	}

	// Verify workspace row still exists.
	var count int
	if err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?",
		"active-ws").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("workspace row count = %d; want 1 (not deleted)", count)
	}
}
