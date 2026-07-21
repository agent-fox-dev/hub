package workspace

import (
	"net/http"
	"testing"

	"github.com/txsvc/apikit"
)

// =============================================================================
// Task 2.1: Unit tests — MountHandlers registers workspaces:write and
// workspaces:delete scopes via apikit.Permission
// TS-03-7, TS-03-8, TS-03-9
// Requirements: 03-REQ-2.1, 03-REQ-2.2, 03-REQ-2.3
// =============================================================================

// TS-03-7: Verify that MountHandlers registers the 'workspaces:write' scope
// via apikit.Permission.
// Requirement: 03-REQ-2.1
func TestSpec03_Group2_PermWriteScopeRegistered(t *testing.T) {
	perms := WorkspacePermissions()
	expected := apikit.Permission{Resource: "workspaces", Action: "write"}
	found := false
	for _, p := range perms {
		if p == expected {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("workspaces:write permission not found in WorkspacePermissions(); got %v", perms)
	}
}

// TS-03-8: Verify that MountHandlers registers the 'workspaces:delete' scope
// via apikit.Permission.
// Requirement: 03-REQ-2.2
func TestSpec03_Group2_PermDeleteScopeRegistered(t *testing.T) {
	perms := WorkspacePermissions()
	expected := apikit.Permission{Resource: "workspaces", Action: "delete"}
	found := false
	for _, p := range perms {
		if p == expected {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("workspaces:delete permission not found in WorkspacePermissions(); got %v", perms)
	}
}

// TS-03-9: Verify that adding the new scopes does not change the behaviour
// of workspaces:read and workspaces:create PATs.
// Requirement: 03-REQ-2.3
func TestSpec03_Group2_ExistingScopesUnchanged(t *testing.T) {
	// Unit check: workspaces:read and workspaces:create are still registered.
	perms := WorkspacePermissions()
	readPerm := apikit.Permission{Resource: "workspaces", Action: "read"}
	createPerm := apikit.Permission{Resource: "workspaces", Action: "create"}

	foundRead := false
	foundCreate := false
	for _, p := range perms {
		if p == readPerm {
			foundRead = true
		}
		if p == createPerm {
			foundCreate = true
		}
	}
	if !foundRead {
		t.Errorf("workspaces:read permission missing from WorkspacePermissions(); got %v", perms)
	}
	if !foundCreate {
		t.Errorf("workspaces:create permission missing from WorkspacePermissions(); got %v", perms)
	}

	// Integration: PAT-read can still GET; PAT-create can still POST.
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "existing-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	t.Run("PAT-read GET returns 200", func(t *testing.T) {
		auth := patAuth("u1-id", "workspaces:read")
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/existing-ws", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /api/v1/workspaces/existing-ws status = %d; want %d",
				rec.Code, http.StatusOK)
		}
	})

	t.Run("PAT-create POST returns 201", func(t *testing.T) {
		auth := patAuth("u1-id", "workspaces:create")
		body := `{"slug":"new-compat-ws","git_url":"https://git.example.com/repo"}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
		if rec.Code != http.StatusCreated {
			t.Errorf("POST /api/v1/workspaces status = %d; want %d",
				rec.Code, http.StatusCreated)
		}
	})
}

// =============================================================================
// Task 2.2: Integration tests — workspaces:write implies read access;
// workspaces:delete does not
// TS-03-10, TS-03-11, TS-03-E5
// Requirements: 03-REQ-2.4, 03-REQ-2.5, 03-REQ-2.E1
// =============================================================================

// TS-03-10: Verify that a PAT with workspaces:write can list and get the PAT
// owner's workspaces (implied read access).
// Requirement: 03-REQ-2.4
func TestSpec03_Group2_WriteImpliesRead(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "write-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	auth := patAuth("u1-id", "workspaces:write")

	t.Run("list returns 200", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /api/v1/workspaces status = %d; want %d",
				rec.Code, http.StatusOK)
		}
	})

	t.Run("get returns 200", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/write-ws", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /api/v1/workspaces/write-ws status = %d; want %d",
				rec.Code, http.StatusOK)
		}
	})
}

// TS-03-11: Verify that a PAT with only workspaces:delete does not grant list
// or get access to workspaces.
// Requirement: 03-REQ-2.5
func TestSpec03_Group2_DeleteDoesNotImplyRead(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "delete-only-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	auth := patAuth("u1-id", "workspaces:delete")

	t.Run("list returns 404", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/v1/workspaces status = %d; want %d (anti-enumeration)",
				rec.Code, http.StatusNotFound)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Message == "" {
			t.Error("error.message is empty; want non-empty descriptive message")
		}
	})

	t.Run("get returns 404", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/delete-only-ws", "", auth)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/v1/workspaces/delete-only-ws status = %d; want %d (anti-enumeration)",
				rec.Code, http.StatusNotFound)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Message == "" {
			t.Error("error.message is empty; want non-empty descriptive message")
		}
	})
}

// TS-03-E5: Verify that a PAT with only workspaces:delete is denied list and
// get access with HTTP 404 (anti-enumeration).
// Requirement: 03-REQ-2.E1
func TestSpec03_Group2_DeleteOnlyPATAntiEnumeration(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "delete-scope-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	// PAT scoped to workspaces:delete only (no read, no write).
	auth := patAuth("u1-id", "workspaces:delete")

	t.Run("list returns 404", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/v1/workspaces status = %d; want %d",
				rec.Code, http.StatusNotFound)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusNotFound {
			t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
		}
	})

	t.Run("get returns 404", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/delete-scope-ws", "", auth)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/v1/workspaces/delete-scope-ws status = %d; want %d",
				rec.Code, http.StatusNotFound)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusNotFound {
			t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
		}
	})
}

// =============================================================================
// Task 2.3: Integration tests — admin token access matrix
// TS-03-12, TS-03-18
// Requirements: 03-REQ-3.1, 03-REQ-3.7
// =============================================================================

// TS-03-12: Verify that an admin token can call update, archive, reactivate,
// and delete on any workspace and receive HTTP 200 (204 for delete).
// Requirement: 03-REQ-3.1
//
// Note: the spec states DELETE returns 200, but the existing handler returns
// 204 No Content. Tests use 204 to match actual codebase behavior.
// See docs/errata/03_delete_status_code.md.
func TestSpec03_Group2_AdminFullMutationAccess(t *testing.T) {
	env := newTestEnv(t)

	// Workspace owned by u2, not admin — confirms cross-user admin access.
	env.seedWorkspace(t, &Workspace{
		Slug:    "admin-target-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u2-id",
		Status:  "active",
	})

	auth := adminAuth()

	t.Run("PATCH returns 200", func(t *testing.T) {
		body := `{"description":"admin update"}`
		rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/admin-target-ws", body, auth)
		if rec.Code != http.StatusOK {
			t.Errorf("PATCH status = %d; want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("archive returns 200", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/admin-target-ws/archive", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("archive status = %d; want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("reactivate returns 200", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/admin-target-ws/reactivate", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("reactivate status = %d; want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("archive again for delete", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/admin-target-ws/archive", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("archive status = %d; want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("DELETE returns 204", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/admin-target-ws", "", auth)
		if rec.Code != http.StatusNoContent {
			t.Errorf("DELETE status = %d; want %d", rec.Code, http.StatusNoContent)
		}
	})
}

// TS-03-18: Verify that an admin token attempting to create a workspace
// receives HTTP 403.
// Requirement: 03-REQ-3.7
func TestSpec03_Group2_AdminCannotCreateWorkspace(t *testing.T) {
	env := newTestEnv(t)

	body := `{"slug":"admin-create-ws","git_url":"https://git.example.com/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, adminAuth())

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /api/v1/workspaces status = %d; want %d",
			rec.Code, http.StatusForbidden)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusForbidden {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusForbidden)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// =============================================================================
// Task 2.4: Integration tests — owner API key access matrix
// TS-03-13
// Requirement: 03-REQ-3.2
// =============================================================================

// TS-03-13: Verify that a workspace owner using their own API key can update,
// archive, reactivate, and delete their own workspace.
// Requirement: 03-REQ-3.2
//
// Sequence: PATCH -> archive -> reactivate -> archive -> DELETE.
// DELETE uses 204 to match actual handler behavior (see errata).
func TestSpec03_Group2_OwnerAPIKeyFullAccess(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "owner-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	auth := userAuth("u1-id")

	t.Run("PATCH returns 200", func(t *testing.T) {
		body := `{"description":"owner update"}`
		rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/owner-ws", body, auth)
		if rec.Code != http.StatusOK {
			t.Errorf("PATCH status = %d; want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("archive returns 200", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/owner-ws/archive", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("archive status = %d; want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("reactivate returns 200", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/owner-ws/reactivate", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("reactivate status = %d; want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("archive again for delete", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/owner-ws/archive", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("archive status = %d; want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("DELETE returns 204", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/owner-ws", "", auth)
		if rec.Code != http.StatusNoContent {
			t.Errorf("DELETE status = %d; want %d", rec.Code, http.StatusNoContent)
		}
	})

	t.Run("workspace removed from database", func(t *testing.T) {
		var count int
		err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", "owner-ws").Scan(&count)
		if err != nil {
			t.Fatalf("count query failed: %v", err)
		}
		if count != 0 {
			t.Errorf("found %d rows with slug 'owner-ws'; want 0 (deleted)", count)
		}
	})
}

// =============================================================================
// Task 2.5: Integration tests — workspaces:write PAT and workspaces:delete PAT
// TS-03-14, TS-03-15, TS-03-16
// Requirements: 03-REQ-3.3, 03-REQ-3.4, 03-REQ-3.5
// =============================================================================

// TS-03-14: Verify that a PAT with workspaces:write can update, archive, and
// reactivate workspaces owned by the PAT's user.
// Requirement: 03-REQ-3.3
func TestSpec03_Group2_WritePATMutationAccess(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "write-target-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	auth := patAuth("u1-id", "workspaces:write")

	t.Run("PATCH returns 200", func(t *testing.T) {
		body := `{"description":"write pat update"}`
		rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/write-target-ws", body, auth)
		if rec.Code != http.StatusOK {
			t.Errorf("PATCH status = %d; want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("archive returns 200", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/write-target-ws/archive", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("archive status = %d; want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("reactivate returns 200", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/write-target-ws/reactivate", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("reactivate status = %d; want %d", rec.Code, http.StatusOK)
		}
	})
}

// TS-03-15: Verify that a PAT with workspaces:write is denied when attempting
// to delete a workspace, returning HTTP 404 (anti-enumeration).
// Requirement: 03-REQ-3.4
func TestSpec03_Group2_WritePATCannotDelete(t *testing.T) {
	env := newTestEnv(t)

	// Seed an archived workspace so the delete precondition (status=archived)
	// is met — the test should still get 404 because workspaces:write lacks
	// delete permission.
	env.seedWorkspace(t, &Workspace{
		Slug:    "write-del-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "archived",
	})

	auth := patAuth("u1-id", "workspaces:write")
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/write-del-ws", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE status = %d; want %d (anti-enumeration)",
			rec.Code, http.StatusNotFound)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusNotFound {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// TS-03-16: Verify that a PAT with workspaces:delete can delete an archived
// workspace owned by the PAT's user.
// Requirement: 03-REQ-3.5
//
// DELETE uses 204 to match actual handler behavior (see errata).
func TestSpec03_Group2_DeletePATCanDeleteArchived(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "archived-del-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "archived",
	})

	auth := patAuth("u1-id", "workspaces:delete")
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/archived-del-ws", "", auth)

	if rec.Code != http.StatusNoContent {
		t.Errorf("DELETE status = %d; want %d", rec.Code, http.StatusNoContent)
	}
}

// =============================================================================
// Task 2.6: Integration tests — read/create PAT denied on mutation endpoints;
// anti-enumeration on unowned workspaces
// TS-03-17, TS-03-E6
// Requirements: 03-REQ-3.6, 03-REQ-3.E1
// =============================================================================

// TS-03-17: Verify that PATs with workspaces:read or workspaces:create are
// denied when attempting update, archive, reactivate, or delete, returning
// HTTP 404 (anti-enumeration).
// Requirement: 03-REQ-3.6
func TestSpec03_Group2_ReadCreatePATsDeniedOnMutation(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "acl-test-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	pats := []struct {
		name string
		auth *AuthInfo
	}{
		{"workspaces:read", patAuth("u1-id", "workspaces:read")},
		{"workspaces:create", patAuth("u1-id", "workspaces:create")},
	}

	operations := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"PATCH", http.MethodPatch, "/api/v1/workspaces/acl-test-ws", `{"description":"x"}`},
		{"archive", http.MethodPost, "/api/v1/workspaces/acl-test-ws/archive", ""},
		{"reactivate", http.MethodPost, "/api/v1/workspaces/acl-test-ws/reactivate", ""},
		{"DELETE", http.MethodDelete, "/api/v1/workspaces/acl-test-ws", ""},
	}

	for _, pat := range pats {
		for _, op := range operations {
			t.Run(pat.name+"/"+op.name, func(t *testing.T) {
				rec := env.doRequest(t, op.method, op.path, op.body, pat.auth)
				if rec.Code != http.StatusNotFound {
					t.Errorf("%s %s with %s: status = %d; want %d (anti-enumeration)",
						op.method, op.path, pat.name, rec.Code, http.StatusNotFound)
				}
				resp := parseErrorEnvelope(t, rec)
				if resp.Error.Code != http.StatusNotFound {
					t.Errorf("error.code = %d; want %d",
						resp.Error.Code, http.StatusNotFound)
				}
				if resp.Error.Message == "" {
					t.Error("error.message is empty; want non-empty descriptive message")
				}
			})
		}
	}
}

// TS-03-E6: Verify that a requester attempting any workspace operation on a
// workspace they do not own receives HTTP 404 (anti-enumeration).
// Requirement: 03-REQ-3.E1
func TestSpec03_Group2_NonOwnerAntiEnumeration(t *testing.T) {
	env := newTestEnv(t)

	// Workspace owned by u1.
	env.seedWorkspace(t, &Workspace{
		Slug:    "other-user-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	// u2 attempts all operations on u1's workspace.
	auth := userAuth("u2-id")

	operations := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"GET", http.MethodGet, "/api/v1/workspaces/other-user-ws", ""},
		{"PATCH", http.MethodPatch, "/api/v1/workspaces/other-user-ws", `{"description":"x"}`},
		{"archive", http.MethodPost, "/api/v1/workspaces/other-user-ws/archive", ""},
		{"DELETE", http.MethodDelete, "/api/v1/workspaces/other-user-ws", ""},
	}

	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			rec := env.doRequest(t, op.method, op.path, op.body, auth)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s: status = %d; want %d (anti-enumeration; existence not disclosed)",
					op.method, op.path, rec.Code, http.StatusNotFound)
			}
			resp := parseErrorEnvelope(t, rec)
			if resp.Error.Code != http.StatusNotFound {
				t.Errorf("error.code = %d; want %d",
					resp.Error.Code, http.StatusNotFound)
			}
			if resp.Error.Message == "" {
				t.Error("error.message is empty; want non-empty descriptive message")
			}
		})
	}
}
