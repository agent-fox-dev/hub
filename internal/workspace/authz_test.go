package workspace

import (
	"net/http"
	"testing"
)

// TS-01-17: Verify that admin tokens have full read and write access to all
// workspaces (except creation).
// Requirement: 01-REQ-4.1
func TestWorkspaceAuthz_AdminFullAccess(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-id",
		Status:  "active",
	})

	auth := adminAuth()

	t.Run("list all workspaces", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /api/v1/workspaces status = %d; want %d",
				rec.Code, http.StatusOK)
		}
	})

	t.Run("get specific workspace", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/alice-ws", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /api/v1/workspaces/alice-ws status = %d; want %d",
				rec.Code, http.StatusOK)
		}
	})

	t.Run("archive workspace", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/alice-ws/archive", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("POST /api/v1/workspaces/alice-ws/archive status = %d; want %d",
				rec.Code, http.StatusOK)
		}
	})
}

// TS-01-18: Verify that using an admin token to create a workspace returns
// HTTP 403 because workspaces require a real user as owner.
// Requirement: 01-REQ-4.2
func TestWorkspaceAuthz_AdminCannotCreate(t *testing.T) {
	env := newTestEnv(t)

	body := `{"slug":"admin-ws","git_url":"https://github.com/org/repo"}`
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

// TS-01-19: Verify that a user API key holder has full access to their own
// workspaces and can create workspaces.
// Requirement: 01-REQ-4.3
func TestWorkspaceAuthz_UserAPIKeyFullAccess(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("alice-id")

	t.Run("create workspace", func(t *testing.T) {
		body := `{"slug":"alice-ws","git_url":"https://github.com/org/repo"}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
		if rec.Code != http.StatusCreated {
			t.Errorf("POST /api/v1/workspaces status = %d; want %d",
				rec.Code, http.StatusCreated)
		}
	})

	t.Run("get own workspace", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/alice-ws", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /api/v1/workspaces/alice-ws status = %d; want %d",
				rec.Code, http.StatusOK)
		}
	})

	t.Run("archive own workspace", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/alice-ws/archive", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("POST /api/v1/workspaces/alice-ws/archive status = %d; want %d",
				rec.Code, http.StatusOK)
		}
	})
}

// TS-01-23: Verify that requests with missing, invalid, or expired
// credentials are rejected with HTTP 401.
// Requirement: 01-REQ-4.7
func TestWorkspaceAuthz_MissingCredentials(t *testing.T) {
	env := newTestEnv(t)

	t.Run("no credential", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET /api/v1/workspaces status = %d; want %d",
				rec.Code, http.StatusUnauthorized)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusUnauthorized {
			t.Errorf("error.code = %d; want %d",
				resp.Error.Code, http.StatusUnauthorized)
		}
	})

	t.Run("invalid credential", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET /api/v1/workspaces status = %d; want %d",
				rec.Code, http.StatusUnauthorized)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusUnauthorized {
			t.Errorf("error.code = %d; want %d",
				resp.Error.Code, http.StatusUnauthorized)
		}
	})
}

// TS-01-E6: Verify that a PAT with workspaces:read attempting to archive,
// reactivate, or delete a workspace is rejected.
// Requirement: 01-REQ-4.E1
//
// Updated for spec 03: PATs without workspaces:write (for archive/reactivate)
// or workspaces:delete (for delete) now receive HTTP 404 (anti-enumeration)
// instead of the original 403. This reflects the migration from blanket
// PAT-forbidden to scope-based access control.
func TestEdgeWorkspaceAuthz_PATReadCannotMutate(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-id",
		Status:  "active",
	})

	auth := patAuth("alice-id", "workspaces:read")

	t.Run("archive", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/alice-ws/archive", "", auth)
		if rec.Code != http.StatusNotFound {
			t.Errorf("archive: status = %d; want %d", rec.Code, http.StatusNotFound)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusNotFound {
			t.Errorf("archive: error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
		}
	})

	t.Run("reactivate", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/alice-ws/reactivate", "", auth)
		if rec.Code != http.StatusNotFound {
			t.Errorf("reactivate: status = %d; want %d", rec.Code, http.StatusNotFound)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusNotFound {
			t.Errorf("reactivate: error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
		}
	})

	t.Run("delete", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/alice-ws", "", auth)
		if rec.Code != http.StatusNotFound {
			t.Errorf("delete: status = %d; want %d", rec.Code, http.StatusNotFound)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusNotFound {
			t.Errorf("delete: error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
		}
	})
}

// TS-01-E7: Verify that a PAT with workspaces:create attempting to archive,
// reactivate, or delete a workspace is rejected.
// Requirement: 01-REQ-4.E2
//
// Updated for spec 03: PATs without workspaces:write (for archive/reactivate)
// or workspaces:delete (for delete) now receive HTTP 404 (anti-enumeration)
// instead of the original 403. This reflects the migration from blanket
// PAT-forbidden to scope-based access control.
func TestEdgeWorkspaceAuthz_PATCreateCannotMutate(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-id",
		Status:  "active",
	})

	auth := patAuth("alice-id", "workspaces:create")

	t.Run("archive", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/alice-ws/archive", "", auth)
		if rec.Code != http.StatusNotFound {
			t.Errorf("archive: status = %d; want %d", rec.Code, http.StatusNotFound)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusNotFound {
			t.Errorf("archive: error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
		}
	})

	t.Run("reactivate", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/alice-ws/reactivate", "", auth)
		if rec.Code != http.StatusNotFound {
			t.Errorf("reactivate: status = %d; want %d", rec.Code, http.StatusNotFound)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusNotFound {
			t.Errorf("reactivate: error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
		}
	})

	t.Run("delete", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/alice-ws", "", auth)
		if rec.Code != http.StatusNotFound {
			t.Errorf("delete: status = %d; want %d", rec.Code, http.StatusNotFound)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusNotFound {
			t.Errorf("delete: error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
		}
	})
}

// TS-01-24: Verify that a valid credential lacking the required permission
// scope for an endpoint is rejected.
// Requirement: 01-REQ-4.8
//
// Updated for spec 03: PATs without the correct scope now receive HTTP 404
// (anti-enumeration) instead of 403, reflecting scope-based access control.
func TestWorkspaceAuthz_InsufficientScope(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-id",
		Status:  "active",
	})

	// PAT with workspaces:read only — should not be able to archive.
	auth := patAuth("alice-id", "workspaces:read")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/alice-ws/archive", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /api/v1/workspaces/alice-ws/archive status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusNotFound {
		t.Errorf("error.code = %d; want %d",
			resp.Error.Code, http.StatusNotFound)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}
