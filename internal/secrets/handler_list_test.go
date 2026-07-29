package secrets

import (
	"net/http"
	"strings"
	"testing"
)

// TS-07-22: Verifies that GET on a secrets list endpoint returns all secret
// names and timestamps sorted alphabetically ascending (case-insensitive),
// never including values.
// Requirement: 07-REQ-9.1
func TestSecretList_SortedCaseInsensitive(t *testing.T) {
	env := newHandlerTestEnv(t)

	// Seed secrets with mixed case keys.
	seedSecret(t, env.db, "user", "user-list", "ZEBRA", "z-val")
	seedSecret(t, env.db, "user", "user-list", "apple", "a-val")
	seedSecret(t, env.db, "user", "user-list", "MANGO", "m-val")

	auth := patAuth("user-list", "secrets:list")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/user/secrets", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/user/secrets status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	entries := parseRawJSONArray(t, rec)
	if len(entries) != 3 {
		t.Fatalf("expected 3 secrets; got %d", len(entries))
	}

	// Expected order: apple, MANGO, ZEBRA (case-insensitive alphabetical).
	expectedOrder := []string{"apple", "MANGO", "ZEBRA"}
	for i, exp := range expectedOrder {
		got, _ := entries[i]["key"].(string)
		if got != exp {
			t.Errorf("entry[%d].key = %q; want %q", i, got, exp)
		}
	}

	// Verify no value field is present and timestamps exist.
	for i, entry := range entries {
		if _, ok := entry["value"]; ok {
			t.Errorf("entry[%d] has value field; secret values must never be returned", i)
		}
		if _, ok := entry["created_at"]; !ok {
			t.Errorf("entry[%d] missing created_at", i)
		}
		if _, ok := entry["updated_at"]; !ok {
			t.Errorf("entry[%d] missing updated_at", i)
		}
	}
}

// TestSecretList_ValuesNeverInResponse checks the raw response body
// does not contain any secret values (even encoded).
// Requirement: 07-REQ-9.1, 07-PROP-1
func TestSecretList_ValuesNeverInResponse(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedSecret(t, env.db, "user", "user-noval", "KEY_A", "my-secret-value-xyz")

	auth := patAuth("user-noval", "secrets:list")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/user/secrets", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d", rec.Code, http.StatusOK)
	}

	responseBody := rec.Body.String()
	if strings.Contains(responseBody, "my-secret-value-xyz") {
		t.Error("response body contains the raw secret value; values must never be returned")
	}
}

// TS-07-23: Verifies that GET on a secrets list endpoint for a scope with
// no secrets returns HTTP 200 with an empty array [].
// Requirement: 07-REQ-9.2
func TestSecretList_EmptyScope(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-empty-scope", "secrets:list")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/user/secrets", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/user/secrets (empty) status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	entries := parseRawJSONArray(t, rec)
	if len(entries) != 0 {
		t.Errorf("expected empty array; got %d entries", len(entries))
	}
}

// TestSecretList_OrgScoped verifies GET on org-scoped path returns secrets.
// Requirement: 07-REQ-9.1
func TestSecretList_OrgScoped(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedOrg(t, "org-list", "List Org", "list-org")
	env.seedOrgMember(t, "org-list", "user-org-list")
	seedSecret(t, env.db, "org", "org-list", "ORG_KEY", "orgval")

	auth := patAuth("user-org-list", "secrets:list")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/orgs/list-org/secrets", "", auth)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/v1/orgs/list-org/secrets status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TestSecretList_WorkspaceScoped verifies GET on workspace-scoped path.
// Requirement: 07-REQ-9.1
func TestSecretList_WorkspaceScoped(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedWorkspaceForTest(t, "ws-list", "user-ws-list")
	seedSecret(t, env.db, "workspace", "ws-list", "WS_KEY", "wsval")

	auth := patAuth("user-ws-list", "secrets:list")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-list/secrets", "", auth)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/v1/workspaces/ws-list/secrets status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TestSecretList_OrgNonMemberReturns404 verifies that listing org secrets
// when the caller is not an org member returns 404.
// Requirement: 07-REQ-9.E1
func TestSecretList_OrgNonMemberReturns404(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedOrg(t, "org-noaccess", "No Access Org", "noaccess-org")

	auth := patAuth("user-outsider-list", "secrets:list")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/orgs/noaccess-org/secrets", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/v1/orgs/noaccess-org/secrets status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}
}

// TestSecretList_PATSecretsWriteCannotList verifies that a PAT with
// secrets:write but not secrets:list or secrets:manage cannot list secrets.
// Requirement: 07-REQ-9.E2
func TestSecretList_PATSecretsWriteCannotList(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-writelist", "secrets:write")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/user/secrets", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("GET (secrets:write only) status = %d; want %d",
			rec.Code, http.StatusForbidden)
	}
}

// TestSecretList_SecretsManageCanList verifies that secrets:manage implies
// listing ability.
// Requirement: 07-REQ-9.1
func TestSecretList_SecretsManageCanList(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-manage-list", "secrets:manage")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/user/secrets", "", auth)

	if rec.Code != http.StatusOK {
		t.Errorf("GET (secrets:manage) status = %d; want %d",
			rec.Code, http.StatusOK)
	}
}
