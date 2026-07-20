package workspace

import (
	"net/http"
	"testing"
	"time"
)

// TS-01-39: Verify that POST /api/v1/workspaces/:slug/archive updates the
// workspace status to 'archived' and returns HTTP 200 with the updated object.
// Requirement: 01-REQ-8.1
func TestWorkspaceArchive_Success(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-id",
		Status:  "active",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/alice-ws/archive", "",
		userAuth("alice-id"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.Status != "archived" {
		t.Errorf("status = %q; want %q", ws.Status, "archived")
	}
	if ws.Slug != "alice-ws" {
		t.Errorf("slug = %q; want %q", ws.Slug, "alice-ws")
	}

	// Verify the database row was updated.
	var status string
	err := env.db.QueryRow("SELECT status FROM workspaces WHERE slug = ?", "alice-ws").Scan(&status)
	if err != nil {
		t.Fatalf("querying workspace status: %v", err)
	}
	if status != "archived" {
		t.Errorf("DB status = %q; want %q", status, "archived")
	}

	// Verify updated_at was refreshed.
	if _, err := time.Parse(time.RFC3339, ws.UpdatedAt); err != nil {
		t.Errorf("updated_at %q is not valid RFC 3339: %v", ws.UpdatedAt, err)
	}
}

// TS-01-40: Verify that archiving an already-archived workspace returns
// HTTP 400, not a no-op.
// Requirement: 01-REQ-8.2
func TestWorkspaceArchive_AlreadyArchived(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-id",
		Status:  "archived",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/alice-ws/archive", "",
		userAuth("alice-id"))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusBadRequest)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// TS-01-41: Verify that a PAT (workspaces:read or workspaces:create)
// attempting to archive a workspace is rejected with HTTP 403.
// Requirement: 01-REQ-8.3
func TestWorkspaceArchive_PATForbidden(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-id",
		Status:  "active",
	})

	t.Run("PAT with workspaces:read", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/alice-ws/archive", "",
			patAuth("alice-id", "workspaces:read"))

		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d; want %d", rec.Code, http.StatusForbidden)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusForbidden {
			t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusForbidden)
		}
		if resp.Error.Message == "" {
			t.Error("error.message is empty; want non-empty descriptive message")
		}
	})

	t.Run("PAT with workspaces:create", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/alice-ws/archive", "",
			patAuth("alice-id", "workspaces:create"))

		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d; want %d", rec.Code, http.StatusForbidden)
		}
	})
}

// TS-01-42: Verify that archiving a non-existent workspace or one the
// credential cannot access returns HTTP 404.
// Requirement: 01-REQ-8.4
func TestWorkspaceArchive_NotFound(t *testing.T) {
	env := newTestEnv(t)

	t.Run("nonexistent workspace", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ghost-ws/archive", "",
			userAuth("alice-id"))

		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d; want %d", rec.Code, http.StatusNotFound)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusNotFound {
			t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
		}
		if resp.Error.Message == "" {
			t.Error("error.message is empty; want non-empty descriptive message")
		}
	})

	t.Run("other user workspace", func(t *testing.T) {
		env.seedWorkspace(t, &Workspace{
			Slug:    "bob-ws",
			GitURL:  "https://github.com/org/bob-repo",
			OwnerID: "bob-id",
			Status:  "active",
		})

		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/bob-ws/archive", "",
			userAuth("alice-id"))

		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d; want %d (must be 404 to prevent slug enumeration)",
				rec.Code, http.StatusNotFound)
		}
	})
}

// TS-01-43: Verify that POST /api/v1/workspaces/:slug/reactivate updates an
// archived workspace to 'active' and returns HTTP 200 with the updated object.
// Requirement: 01-REQ-9.1
func TestWorkspaceReactivate_Success(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-id",
		Status:  "archived",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/alice-ws/reactivate", "",
		userAuth("alice-id"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.Status != "active" {
		t.Errorf("status = %q; want %q", ws.Status, "active")
	}
	if ws.Slug != "alice-ws" {
		t.Errorf("slug = %q; want %q", ws.Slug, "alice-ws")
	}

	// Verify the database row was updated.
	var status string
	err := env.db.QueryRow("SELECT status FROM workspaces WHERE slug = ?", "alice-ws").Scan(&status)
	if err != nil {
		t.Fatalf("querying workspace status: %v", err)
	}
	if status != "active" {
		t.Errorf("DB status = %q; want %q", status, "active")
	}
}

// TS-01-44: Verify that reactivating an already-active workspace returns
// HTTP 400, not a no-op.
// Requirement: 01-REQ-9.2
func TestWorkspaceReactivate_AlreadyActive(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-id",
		Status:  "active",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/alice-ws/reactivate", "",
		userAuth("alice-id"))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusBadRequest)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// TS-01-45: Verify that a PAT attempting to reactivate a workspace is rejected
// with HTTP 403.
// Requirement: 01-REQ-9.3
func TestWorkspaceReactivate_PATForbidden(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-id",
		Status:  "archived",
	})

	t.Run("PAT with workspaces:create", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/alice-ws/reactivate", "",
			patAuth("alice-id", "workspaces:create"))

		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d; want %d", rec.Code, http.StatusForbidden)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusForbidden {
			t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusForbidden)
		}
		if resp.Error.Message == "" {
			t.Error("error.message is empty; want non-empty descriptive message")
		}
	})

	t.Run("PAT with workspaces:read", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/alice-ws/reactivate", "",
			patAuth("alice-id", "workspaces:read"))

		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d; want %d", rec.Code, http.StatusForbidden)
		}
	})
}

// TS-01-46: Verify that reactivating a non-existent workspace or one the
// credential cannot access returns HTTP 404.
// Requirement: 01-REQ-9.4
func TestWorkspaceReactivate_NotFound(t *testing.T) {
	env := newTestEnv(t)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ghost-ws/reactivate", "",
		userAuth("alice-id"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusNotFound)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusNotFound {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// TS-01-47: Verify that DELETE /api/v1/workspaces/:slug physically removes the
// row from the workspaces table and returns HTTP 204 with empty body.
// Requirement: 01-REQ-10.1
func TestWorkspaceDelete_Success(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-id",
		Status:  "archived",
	})

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/alice-ws", "",
		userAuth("alice-id"))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusNoContent)
	}
	if body := rec.Body.String(); body != "" {
		t.Errorf("response body = %q; want empty", body)
	}

	// Verify the row is physically gone from the database.
	var count int
	err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", "alice-ws").Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("row with slug 'alice-ws' still exists; want 0 rows")
	}
}

// TS-01-48: Verify that attempting to delete an active workspace returns
// HTTP 400.
// Requirement: 01-REQ-10.2
func TestWorkspaceDelete_ActiveRejected(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-id",
		Status:  "active",
	})

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/alice-ws", "",
		userAuth("alice-id"))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusBadRequest)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}

	// Verify the row is still present.
	var count int
	err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", "alice-ws").Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected row still present; got %d rows", count)
	}
}

// TS-01-49: Verify that a PAT attempting to delete a workspace is rejected
// with HTTP 403.
// Requirement: 01-REQ-10.3
func TestWorkspaceDelete_PATForbidden(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-id",
		Status:  "archived",
	})

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/alice-ws", "",
		patAuth("alice-id", "workspaces:read"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusForbidden)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusForbidden {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusForbidden)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// TS-01-50: Verify that DELETE /api/v1/workspaces/:slug returns HTTP 404 when
// the workspace does not exist or the credential cannot access it.
// Requirement: 01-REQ-10.4
func TestWorkspaceDelete_NotFound(t *testing.T) {
	env := newTestEnv(t)

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ghost-ws", "",
		userAuth("alice-id"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusNotFound)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusNotFound {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// TS-01-51: Verify that workspace deletion performs a physical delete removing
// the row from the workspaces table with no soft-delete flag or audit record.
// Requirement: 01-REQ-10.5
func TestWorkspaceDelete_PhysicalDelete(t *testing.T) {
	db := openTestDB(t)

	ws := &Workspace{
		Slug:    "to-delete",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "archived",
	}
	if err := insertWorkspace(db, ws); err != nil {
		t.Fatalf("insertWorkspace() returned error: %v", err)
	}

	if err := deleteWorkspace(db, "to-delete"); err != nil {
		t.Fatalf("deleteWorkspace() returned error: %v", err)
	}

	// Verify no row with slug 'to-delete' exists.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", "to-delete").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("row with slug 'to-delete' still exists; want 0 rows")
	}

	// Verify no row with status='deleted' exists anywhere in the table.
	if err := db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE status = ?", "deleted").Scan(&count); err != nil {
		t.Fatalf("deleted status query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("found %d rows with status='deleted'; want 0 (physical delete, not soft-delete)", count)
	}
}

// TS-01-E12: Verify that a database DELETE statement failure after
// authorization passes returns HTTP 500.
// Requirement: 01-REQ-10.E1
func TestWorkspaceDelete_DBFailure(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-id",
		Status:  "archived",
	})

	// Install a trigger that causes DELETE to fail, simulating a transient
	// database error after authorization has already passed.
	if _, err := env.db.Exec(`
		CREATE TRIGGER prevent_delete BEFORE DELETE ON workspaces
		BEGIN
			SELECT RAISE(FAIL, 'simulated database failure');
		END
	`); err != nil {
		t.Fatalf("failed to create trigger: %v", err)
	}

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/alice-ws", "",
		userAuth("alice-id"))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusInternalServerError)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusInternalServerError {
		t.Errorf("error.code = %d; want %d",
			resp.Error.Code, http.StatusInternalServerError)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}
