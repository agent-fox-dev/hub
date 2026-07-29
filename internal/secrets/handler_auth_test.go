package secrets

import (
	"net/http"
	"testing"
)

// TS-07-15: Verifies that admin tokens can access secrets and variables at any
// scope without ownership checks.
// Requirement: 07-REQ-7.1
func TestSecretAuthz_AdminFullAccess(t *testing.T) {
	env := newHandlerTestEnv(t)

	// Seed a workspace owned by user-a.
	env.seedWorkspaceForTest(t, "other-workspace", "user-a")

	auth := adminAuth()

	t.Run("list workspace secrets as admin (not owner)", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/other-workspace/secrets", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /api/v1/workspaces/other-workspace/secrets status = %d; want %d",
				rec.Code, http.StatusOK)
		}
	})

	t.Run("list user secrets as admin", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/user/secrets", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /api/v1/user/secrets status = %d; want %d",
				rec.Code, http.StatusOK)
		}
	})

	t.Run("list org secrets as admin (not member)", func(t *testing.T) {
		env.seedOrg(t, "org-admin-test", "Admin Test Org", "admin-test-org")
		rec := env.doRequest(t, http.MethodGet, "/api/v1/orgs/admin-test-org/secrets", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /api/v1/orgs/admin-test-org/secrets status = %d; want %d",
				rec.Code, http.StatusOK)
		}
	})
}

// TS-07-16: Verifies that API keys have implicit full access to secrets and
// variables for the authenticated user's owned resources.
// Requirement: 07-REQ-7.2
func TestSecretAuthz_APIKeyFullAccess(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := userAuth("user-apikey")

	t.Run("list user secrets with API key", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/user/secrets", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /api/v1/user/secrets status = %d; want %d",
				rec.Code, http.StatusOK)
		}
	})

	t.Run("create user secrets with API key", func(t *testing.T) {
		body := `{"entries":[{"key":"MY_SECRET","value":"myval"}]}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/user/secrets", body, auth)
		if rec.Code != http.StatusCreated {
			t.Errorf("POST /api/v1/user/secrets status = %d; want %d",
				rec.Code, http.StatusCreated)
		}
	})

	t.Run("list org secrets as org member with API key", func(t *testing.T) {
		env.seedOrg(t, "org-apikey", "APIKey Org", "apikey-org")
		env.seedOrgMember(t, "org-apikey", "user-apikey")
		rec := env.doRequest(t, http.MethodGet, "/api/v1/orgs/apikey-org/secrets", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /api/v1/orgs/apikey-org/secrets status = %d; want %d",
				rec.Code, http.StatusOK)
		}
	})

	t.Run("list workspace secrets as workspace owner with API key", func(t *testing.T) {
		env.seedWorkspaceForTest(t, "apikey-ws", "user-apikey")
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/apikey-ws/secrets", "", auth)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /api/v1/workspaces/apikey-ws/secrets status = %d; want %d",
				rec.Code, http.StatusOK)
		}
	})
}

// TS-07-17: Verifies that a PAT lacking the required scope for an endpoint
// receives HTTP 403.
// Requirement: 07-REQ-7.3
func TestSecretAuthz_PATMissingScope(t *testing.T) {
	env := newHandlerTestEnv(t)

	t.Run("list user secrets with only vars:delete scope", func(t *testing.T) {
		pat := patAuth("user-scope", "vars:delete")
		rec := env.doRequest(t, http.MethodGet, "/api/v1/user/secrets", "", pat)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET /api/v1/user/secrets status = %d; want %d",
				rec.Code, http.StatusForbidden)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Message == "" {
			t.Error("error.message is empty; want non-empty message indicating missing scope")
		}
	})

	t.Run("create user secrets with only secrets:list scope", func(t *testing.T) {
		pat := patAuth("user-scope", "secrets:list")
		body := `{"entries":[{"key":"MY_SECRET","value":"val"}]}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/user/secrets", body, pat)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST /api/v1/user/secrets status = %d; want %d",
				rec.Code, http.StatusForbidden)
		}
	})

	t.Run("update user secret with only secrets:list scope", func(t *testing.T) {
		pat := patAuth("user-scope", "secrets:list")
		body := `{"value":"newval"}`
		rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/secrets/MY_KEY", body, pat)
		if rec.Code != http.StatusForbidden {
			t.Errorf("PATCH /api/v1/user/secrets/MY_KEY status = %d; want %d",
				rec.Code, http.StatusForbidden)
		}
	})

	t.Run("delete user secret with only secrets:list scope", func(t *testing.T) {
		pat := patAuth("user-scope", "secrets:list")
		rec := env.doRequest(t, http.MethodDelete, "/api/v1/user/secrets/MY_KEY", "", pat)
		if rec.Code != http.StatusForbidden {
			t.Errorf("DELETE /api/v1/user/secrets/MY_KEY status = %d; want %d",
				rec.Code, http.StatusForbidden)
		}
	})
}

// TS-07-18: Verifies that a caller who is not a member of an org receives
// HTTP 404 for org-scoped endpoints (anti-enumeration).
// Requirement: 07-REQ-7.4
func TestSecretAuthz_OrgNonMember_AntiEnumeration(t *testing.T) {
	env := newHandlerTestEnv(t)

	// Create an org that user-nonmember is NOT a member of.
	env.seedOrg(t, "org-private", "Private Org", "private-org")

	t.Run("list org secrets as non-member PAT", func(t *testing.T) {
		pat := patAuth("user-nonmember", "secrets:manage")
		rec := env.doRequest(t, http.MethodGet, "/api/v1/orgs/private-org/secrets", "", pat)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/v1/orgs/private-org/secrets status = %d; want %d",
				rec.Code, http.StatusNotFound)
		}
	})

	t.Run("create org secrets as non-member PAT", func(t *testing.T) {
		pat := patAuth("user-nonmember", "secrets:manage")
		body := `{"entries":[{"key":"SECRET","value":"val"}]}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/orgs/private-org/secrets", body, pat)
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST /api/v1/orgs/private-org/secrets status = %d; want %d",
				rec.Code, http.StatusNotFound)
		}
	})

	t.Run("list org secrets as non-member API key", func(t *testing.T) {
		auth := userAuth("user-nonmember")
		rec := env.doRequest(t, http.MethodGet, "/api/v1/orgs/private-org/secrets", "", auth)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/v1/orgs/private-org/secrets (API key) status = %d; want %d",
				rec.Code, http.StatusNotFound)
		}
	})
}

// TS-07-19: Verifies that a caller who does not own a workspace receives
// HTTP 404 for workspace-scoped endpoints (anti-enumeration).
// Requirement: 07-REQ-7.5
func TestSecretAuthz_WorkspaceNonOwner_AntiEnumeration(t *testing.T) {
	env := newHandlerTestEnv(t)

	// Workspace owned by user-a.
	env.seedWorkspaceForTest(t, "user-a-workspace", "user-a")

	t.Run("list workspace secrets as non-owner PAT", func(t *testing.T) {
		pat := patAuth("user-b", "secrets:manage")
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/user-a-workspace/secrets", "", pat)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/v1/workspaces/user-a-workspace/secrets status = %d; want %d",
				rec.Code, http.StatusNotFound)
		}
	})

	t.Run("create workspace secrets as non-owner PAT", func(t *testing.T) {
		pat := patAuth("user-b", "secrets:manage")
		body := `{"entries":[{"key":"SECRET","value":"val"}]}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/user-a-workspace/secrets", body, pat)
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST /api/v1/workspaces/user-a-workspace/secrets status = %d; want %d",
				rec.Code, http.StatusNotFound)
		}
	})

	t.Run("list workspace secrets as non-owner API key", func(t *testing.T) {
		auth := userAuth("user-b")
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/user-a-workspace/secrets", "", auth)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/v1/workspaces/user-a-workspace/secrets (API key) status = %d; want %d",
				rec.Code, http.StatusNotFound)
		}
	})

	t.Run("delete workspace secret as non-owner PAT", func(t *testing.T) {
		pat := patAuth("user-b", "secrets:delete")
		rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/user-a-workspace/secrets/KEY", "", pat)
		if rec.Code != http.StatusNotFound {
			t.Errorf("DELETE /api/v1/workspaces/user-a-workspace/secrets/KEY status = %d; want %d",
				rec.Code, http.StatusNotFound)
		}
	})
}

// TestSecretAuthz_Unauthenticated verifies that requests with no token
// receive HTTP 401.
// Requirement: 07-REQ-7.E1
func TestSecretAuthz_Unauthenticated(t *testing.T) {
	env := newHandlerTestEnv(t)

	t.Run("no credential on user secrets", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/user/secrets", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET /api/v1/user/secrets (no auth) status = %d; want %d",
				rec.Code, http.StatusUnauthorized)
		}
	})

	t.Run("no credential on org secrets", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/orgs/some-org/secrets", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET /api/v1/orgs/some-org/secrets (no auth) status = %d; want %d",
				rec.Code, http.StatusUnauthorized)
		}
	})

	t.Run("no credential on workspace secrets", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws/secrets", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET /api/v1/workspaces/ws/secrets (no auth) status = %d; want %d",
				rec.Code, http.StatusUnauthorized)
		}
	})
}

// TestSecretAuthz_PATManageScopeAntiEnumOrgNonMember verifies that a PAT with
// secrets:manage targeting an org the holder is not a member of still gets 404.
// Requirement: 07-REQ-7.E2
func TestSecretAuthz_PATManageScopeAntiEnumOrgNonMember(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedOrg(t, "org-enum-test", "Enum Org", "enum-org")
	// user-outsider is NOT a member.

	pat := patAuth("user-outsider", "secrets:manage")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/orgs/enum-org/secrets", "", pat)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/v1/orgs/enum-org/secrets status = %d; want %d (anti-enumeration)",
			rec.Code, http.StatusNotFound)
	}
}

// TestSecretAuthz_VarsWriteImpliesRead verifies that a PAT with vars:write
// can list variables (because vars:write implies vars:read).
// Requirement: 07-REQ-7.E3
//
// Note: This test uses the vars list endpoint pattern. Since vars handlers
// are in the vars package (Group 3/7), this test validates the permission
// logic at the AuthInfo level as a unit test rather than an integration test.
func TestSecretAuthz_VarsWriteImpliesRead(t *testing.T) {
	auth := &AuthInfo{
		CredType:    CredentialPAT,
		UserID:      "user-1",
		Permissions: []string{"vars:write"},
	}
	if !auth.hasVarsRead() {
		t.Error("PAT with vars:write should satisfy hasVarsRead() per 07-REQ-7.E3")
	}
}
