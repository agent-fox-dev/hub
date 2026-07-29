package secrets

import (
	"net/http"
	"testing"
)

// TS-07-30: Verifies that GET on a variables list endpoint returns all
// variables with decoded values sorted alphabetically ascending (case-insensitive).
// Requirement: 07-REQ-13.1
func TestVarList_SortedCaseInsensitive(t *testing.T) {
	env := newHandlerTestEnv(t)

	// Seed variables with mixed case keys.
	seedVariable(t, env.db, "user", "user-var-list", "ZEBRA", "z-val")
	seedVariable(t, env.db, "user", "user-var-list", "apple", "a-val")
	seedVariable(t, env.db, "user", "user-var-list", "MANGO", "m-val")

	auth := patAuth("user-var-list", "vars:read")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/user/vars", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/user/vars status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	entries := parseRawJSONArray(t, rec)
	if len(entries) != 3 {
		t.Fatalf("expected 3 variables; got %d", len(entries))
	}

	// Expected order: apple, MANGO, ZEBRA (case-insensitive alphabetical).
	expectedOrder := []string{"apple", "MANGO", "ZEBRA"}
	for i, exp := range expectedOrder {
		got, _ := entries[i]["key"].(string)
		if got != exp {
			t.Errorf("entry[%d].key = %q; want %q", i, got, exp)
		}
	}

	// Verify value field IS present (unlike secrets) and timestamps exist.
	for i, entry := range entries {
		if _, ok := entry["value"]; !ok {
			t.Errorf("entry[%d] missing value; variable values must be returned", i)
		}
		if _, ok := entry["created_at"]; !ok {
			t.Errorf("entry[%d] missing created_at", i)
		}
		if _, ok := entry["updated_at"]; !ok {
			t.Errorf("entry[%d] missing updated_at", i)
		}
	}
}

// TestVarList_ValuesIncludedAndDecoded verifies that variable values are
// returned decoded (not base64-encoded) in the list response.
// Requirement: 07-REQ-13.1, 07-PROP-2
func TestVarList_ValuesIncludedAndDecoded(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedVariable(t, env.db, "user", "user-var-decoded", "KEY_A", "my-plain-value")

	auth := patAuth("user-var-decoded", "vars:read")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/user/vars", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d", rec.Code, http.StatusOK)
	}

	entries := parseRawJSONArray(t, rec)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry; got %d", len(entries))
	}

	val, _ := entries[0]["value"].(string)
	if val != "my-plain-value" {
		t.Errorf("value = %q; want 'my-plain-value' (decoded from base64 storage)", val)
	}
}

// TS-07-31: Verifies that GET on a variables list endpoint for a scope with
// no variables returns HTTP 200 with an empty array [].
// Requirement: 07-REQ-13.2
func TestVarList_EmptyScope(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-var-empty-scope", "vars:read")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/user/vars", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/user/vars (empty) status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	entries := parseRawJSONArray(t, rec)
	if len(entries) != 0 {
		t.Errorf("expected empty array; got %d entries", len(entries))
	}
}

// TestVarList_OrgScoped verifies GET on org-scoped variables path.
// Requirement: 07-REQ-13.1
func TestVarList_OrgScoped(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedOrg(t, "org-var-list", "VarListOrg", "varlist-org")
	env.seedOrgMember(t, "org-var-list", "user-org-varlist")
	seedVariable(t, env.db, "org", "org-var-list", "ORG_KEY", "orgval")

	auth := patAuth("user-org-varlist", "vars:read")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/orgs/varlist-org/vars", "", auth)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/v1/orgs/varlist-org/vars status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TestVarList_WorkspaceScoped verifies GET on workspace-scoped variables path.
// Requirement: 07-REQ-13.1
func TestVarList_WorkspaceScoped(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedWorkspaceForTest(t, "ws-var-list", "user-ws-varlist")
	seedVariable(t, env.db, "workspace", "ws-var-list", "WS_KEY", "wsval")

	auth := patAuth("user-ws-varlist", "vars:read")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-var-list/vars", "", auth)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/v1/workspaces/ws-var-list/vars status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TestVarList_OrgNonMemberReturns404 verifies that listing org variables
// when the caller is not a member returns 404 (anti-enumeration).
// Requirement: 07-REQ-13.E1
func TestVarList_OrgNonMemberReturns404(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedOrg(t, "org-var-noaccess", "NoAccessVarOrg", "noaccess-varorg")

	auth := patAuth("user-outsider-varlist", "vars:read")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/orgs/noaccess-varorg/vars", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/v1/orgs/noaccess-varorg/vars status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}
}

// TestVarList_PATVarsDeleteCannotList verifies that a PAT with vars:delete
// but not vars:read, vars:write, or vars:manage cannot list variables.
// Requirement: 07-REQ-13.E2
func TestVarList_PATVarsDeleteCannotList(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-var-delonly", "vars:delete")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/user/vars", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("GET (vars:delete only) status = %d; want %d",
			rec.Code, http.StatusForbidden)
	}
}

// TestVarList_VarsWriteCanList verifies that vars:write implies vars:read
// and therefore allows listing variables.
// Requirement: 07-REQ-13.1
func TestVarList_VarsWriteCanList(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-var-write-list", "vars:write")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/user/vars", "", auth)

	if rec.Code != http.StatusOK {
		t.Errorf("GET (vars:write) status = %d; want %d",
			rec.Code, http.StatusOK)
	}
}

// TestVarList_VarsManageCanList verifies that vars:manage implies listing.
// Requirement: 07-REQ-13.1
func TestVarList_VarsManageCanList(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-var-manage-list", "vars:manage")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/user/vars", "", auth)

	if rec.Code != http.StatusOK {
		t.Errorf("GET (vars:manage) status = %d; want %d",
			rec.Code, http.StatusOK)
	}
}
