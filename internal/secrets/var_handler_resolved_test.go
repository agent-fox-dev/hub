package secrets

import (
	"net/http"
	"testing"
)

// TS-07-36: Verifies that GET /workspaces/:slug/vars/resolved merges variables
// from user, org, and workspace tiers with correct resolution order, origin
// field, and alphabetical sorting.
// Requirement: 07-REQ-16.1
func TestVarResolved_MergesAllTiers(t *testing.T) {
	env := newHandlerTestEnv(t)

	// Set up a workspace with org and user.
	env.seedOrg(t, "org-resolved", "ResolvedOrg", "resolved-org")
	env.seedOrgMember(t, "org-resolved", "user-resolved")
	env.seedWorkspaceWithOrg(t, "my-ws", "user-resolved", "org-resolved")

	// Seed variables at each tier.
	seedVariable(t, env.db, "user", "user-resolved", "APP_NAME", "user_app")
	seedVariable(t, env.db, "org", "org-resolved", "DB_HOST", "org_db")
	seedVariable(t, env.db, "workspace", "my-ws", "APP_NAME", "ws_app")
	seedVariable(t, env.db, "workspace", "my-ws", "DB_HOST", "ws_db")

	auth := userAuth("user-resolved")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/my-ws/vars/resolved", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/workspaces/my-ws/vars/resolved status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	entries := parseRawJSONArray(t, rec)
	if len(entries) < 2 {
		t.Fatalf("expected at least 2 entries; got %d", len(entries))
	}

	// Verify alphabetical case-insensitive sorting.
	for i := 1; i < len(entries); i++ {
		prev, _ := entries[i-1]["key"].(string)
		curr, _ := entries[i]["key"].(string)
		if prev > curr {
			t.Errorf("entries not sorted: %q before %q", prev, curr)
		}
	}

	// Find APP_NAME — should come from workspace (overrides user).
	var appEntry map[string]any
	for _, e := range entries {
		if e["key"] == "APP_NAME" {
			appEntry = e
			break
		}
	}
	if appEntry == nil {
		t.Fatal("APP_NAME not found in resolved response")
	}
	if appEntry["value"] != "ws_app" {
		t.Errorf("APP_NAME value = %v; want 'ws_app' (workspace overrides user)", appEntry["value"])
	}
	if appEntry["origin"] != "workspace" {
		t.Errorf("APP_NAME origin = %v; want 'workspace'", appEntry["origin"])
	}

	// Find DB_HOST — should come from workspace (overrides org).
	var dbEntry map[string]any
	for _, e := range entries {
		if e["key"] == "DB_HOST" {
			dbEntry = e
			break
		}
	}
	if dbEntry == nil {
		t.Fatal("DB_HOST not found in resolved response")
	}
	if dbEntry["value"] != "ws_db" {
		t.Errorf("DB_HOST value = %v; want 'ws_db' (workspace overrides org)", dbEntry["value"])
	}
	if dbEntry["origin"] != "workspace" {
		t.Errorf("DB_HOST origin = %v; want 'workspace'", dbEntry["origin"])
	}
}

// TestVarResolved_HasTimestampsFromWinningTier verifies that the resolved
// response includes created_at and updated_at from the winning tier's record.
// Requirement: 07-REQ-16.1
func TestVarResolved_HasTimestampsFromWinningTier(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedOrg(t, "org-resolved-ts", "TSOrg", "ts-org")
	env.seedOrgMember(t, "org-resolved-ts", "user-resolved-ts")
	env.seedWorkspaceWithOrg(t, "ws-ts", "user-resolved-ts", "org-resolved-ts")

	seedVariable(t, env.db, "workspace", "ws-ts", "MY_VAR", "ws_val")

	auth := userAuth("user-resolved-ts")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-ts/vars/resolved", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET resolved status = %d; want %d", rec.Code, http.StatusOK)
	}

	entries := parseRawJSONArray(t, rec)
	if len(entries) == 0 {
		t.Fatal("expected at least 1 entry")
	}

	for _, entry := range entries {
		if _, ok := entry["created_at"]; !ok {
			t.Errorf("entry %v missing created_at", entry["key"])
		}
		if _, ok := entry["updated_at"]; !ok {
			t.Errorf("entry %v missing updated_at", entry["key"])
		}
	}
}

// TS-07-37: Verifies that when a workspace has no org_id, the resolved
// endpoint skips the org tier and merges only user and workspace variables.
// Requirement: 07-REQ-16.2
func TestVarResolved_SkipsOrgWhenNoOrgID(t *testing.T) {
	env := newHandlerTestEnv(t)

	// Create a workspace without org (seedWorkspaceForTest sets org_id to NULL).
	env.seedWorkspaceForTest(t, "no-org-ws", "user-no-org")

	// Seed user-level and workspace-level variables.
	seedVariable(t, env.db, "user", "user-no-org", "USER_VAR", "user_val")
	seedVariable(t, env.db, "workspace", "no-org-ws", "WS_VAR", "ws_val")

	auth := userAuth("user-no-org")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/no-org-ws/vars/resolved", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/workspaces/no-org-ws/vars/resolved status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	entries := parseRawJSONArray(t, rec)

	// Verify no entry has origin='org'.
	for _, entry := range entries {
		if entry["origin"] == "org" {
			t.Errorf("entry %v has origin 'org'; expected org tier to be skipped", entry["key"])
		}
	}

	// Verify USER_VAR comes from user tier.
	var userVarEntry map[string]any
	for _, e := range entries {
		if e["key"] == "USER_VAR" {
			userVarEntry = e
			break
		}
	}
	if userVarEntry == nil {
		t.Fatal("USER_VAR not found in resolved response")
	}
	if userVarEntry["origin"] != "user" {
		t.Errorf("USER_VAR origin = %v; want 'user'", userVarEntry["origin"])
	}

	// Verify WS_VAR comes from workspace tier.
	var wsVarEntry map[string]any
	for _, e := range entries {
		if e["key"] == "WS_VAR" {
			wsVarEntry = e
			break
		}
	}
	if wsVarEntry == nil {
		t.Fatal("WS_VAR not found in resolved response")
	}
	if wsVarEntry["origin"] != "workspace" {
		t.Errorf("WS_VAR origin = %v; want 'workspace'", wsVarEntry["origin"])
	}
}

// TS-07-38: Verifies that when the same key exists at multiple tiers, the
// most specific tier wins and the origin field reflects that tier.
// Requirement: 07-REQ-16.3
func TestVarResolved_MostSpecificTierWins(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedOrg(t, "org-tier", "TierOrg", "tier-org")
	env.seedOrgMember(t, "org-tier", "user-tier")
	env.seedWorkspaceWithOrg(t, "ws-tier", "user-tier", "org-tier")

	// Same key at all three tiers.
	seedVariable(t, env.db, "user", "user-tier", "SHARED_KEY", "user_val")
	seedVariable(t, env.db, "org", "org-tier", "SHARED_KEY", "org_val")
	seedVariable(t, env.db, "workspace", "ws-tier", "SHARED_KEY", "ws_val")

	auth := userAuth("user-tier")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-tier/vars/resolved", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET resolved status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	entries := parseRawJSONArray(t, rec)

	// Find SHARED_KEY.
	var sharedEntry map[string]any
	for _, e := range entries {
		if e["key"] == "SHARED_KEY" {
			sharedEntry = e
			break
		}
	}
	if sharedEntry == nil {
		t.Fatal("SHARED_KEY not found in resolved response")
	}

	// Workspace should win over org and user.
	if sharedEntry["value"] != "ws_val" {
		t.Errorf("SHARED_KEY value = %v; want 'ws_val' (workspace is most specific)", sharedEntry["value"])
	}
	if sharedEntry["origin"] != "workspace" {
		t.Errorf("SHARED_KEY origin = %v; want 'workspace'", sharedEntry["origin"])
	}
	if _, ok := sharedEntry["created_at"]; !ok {
		t.Error("SHARED_KEY missing created_at")
	}
	if _, ok := sharedEntry["updated_at"]; !ok {
		t.Error("SHARED_KEY missing updated_at")
	}
}

// TestVarResolved_OrgOverridesUser verifies that org-level variables override
// user-level when no workspace-level override exists.
// Requirement: 07-REQ-16.3, 07-PROP-5
func TestVarResolved_OrgOverridesUser(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedOrg(t, "org-oou", "OrgOverUser", "org-over-user")
	env.seedOrgMember(t, "org-oou", "user-oou")
	env.seedWorkspaceWithOrg(t, "ws-oou", "user-oou", "org-oou")

	// Same key at user and org tiers only (no workspace override).
	seedVariable(t, env.db, "user", "user-oou", "PRIORITY_KEY", "user_val")
	seedVariable(t, env.db, "org", "org-oou", "PRIORITY_KEY", "org_val")

	auth := userAuth("user-oou")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-oou/vars/resolved", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET resolved status = %d; want %d", rec.Code, http.StatusOK)
	}

	entries := parseRawJSONArray(t, rec)

	var found map[string]any
	for _, e := range entries {
		if e["key"] == "PRIORITY_KEY" {
			found = e
			break
		}
	}
	if found == nil {
		t.Fatal("PRIORITY_KEY not found in resolved response")
	}
	if found["value"] != "org_val" {
		t.Errorf("PRIORITY_KEY value = %v; want 'org_val' (org overrides user)", found["value"])
	}
	if found["origin"] != "org" {
		t.Errorf("PRIORITY_KEY origin = %v; want 'org'", found["origin"])
	}
}

// TestVarResolved_WorkspaceNotFoundReturns404 verifies that requesting
// resolved variables for a non-existent workspace returns 404.
// Requirement: 07-REQ-16.E1
func TestVarResolved_WorkspaceNotFoundReturns404(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := userAuth("user-resolved-nf")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/nonexistent-ws/vars/resolved", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/v1/workspaces/nonexistent-ws/vars/resolved status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}
}

// TestVarResolved_NonOwnerReturns404 verifies that requesting resolved
// variables for a workspace the caller does not own returns 404.
// Requirement: 07-REQ-16.E1
func TestVarResolved_NonOwnerReturns404(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedWorkspaceForTest(t, "ws-resolved-nowner", "user-a-resolved")

	// user-b is NOT the workspace owner.
	auth := userAuth("user-b-resolved")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-resolved-nowner/vars/resolved", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET resolved (non-owner) status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}
}

// TestVarResolved_EmptyReturnsEmptyArray verifies that resolving for a
// workspace with no variables at any tier returns HTTP 200 with [].
// Requirement: 07-REQ-16.E2
func TestVarResolved_EmptyReturnsEmptyArray(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedWorkspaceForTest(t, "ws-resolved-empty", "user-resolved-empty")

	auth := userAuth("user-resolved-empty")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-resolved-empty/vars/resolved", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET resolved (empty) status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	entries := parseRawJSONArray(t, rec)
	if len(entries) != 0 {
		t.Errorf("expected empty array; got %d entries", len(entries))
	}
}

// TestVarResolved_PATVarsDeleteCannotResolve verifies that a PAT with
// vars:delete but not vars:read, vars:write, or vars:manage cannot use
// the resolved endpoint.
// Requirement: 07-REQ-16.E4
func TestVarResolved_PATVarsDeleteCannotResolve(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedWorkspaceForTest(t, "ws-resolved-perm", "user-resolved-perm")

	auth := patAuth("user-resolved-perm", "vars:delete")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-resolved-perm/vars/resolved", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("GET resolved (vars:delete only) status = %d; want %d",
			rec.Code, http.StatusForbidden)
	}
}

// TestVarResolved_AdminFullAccess verifies that an admin token grants full
// access to the resolved endpoint without ownership checks.
// Requirement: 07-REQ-16.E5
func TestVarResolved_AdminFullAccess(t *testing.T) {
	env := newHandlerTestEnv(t)

	// Workspace owned by user-a, accessed by admin (not the owner).
	env.seedWorkspaceForTest(t, "ws-resolved-admin", "user-a-admin")
	seedVariable(t, env.db, "workspace", "ws-resolved-admin", "ADMIN_VAR", "admin_val")

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-resolved-admin/vars/resolved", "", auth)

	if rec.Code != http.StatusOK {
		t.Errorf("GET resolved (admin) status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TestVarResolved_SortedCaseInsensitive verifies that the resolved response
// is sorted alphabetically ascending by key (case-insensitive).
// Requirement: 07-REQ-16.1, 07-PROP-6
func TestVarResolved_SortedCaseInsensitive(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedWorkspaceForTest(t, "ws-resolved-sort", "user-resolved-sort")

	seedVariable(t, env.db, "workspace", "ws-resolved-sort", "ZEBRA", "z")
	seedVariable(t, env.db, "workspace", "ws-resolved-sort", "apple", "a")
	seedVariable(t, env.db, "workspace", "ws-resolved-sort", "MANGO", "m")

	auth := userAuth("user-resolved-sort")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-resolved-sort/vars/resolved", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET resolved (sort) status = %d; want %d", rec.Code, http.StatusOK)
	}

	entries := parseRawJSONArray(t, rec)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries; got %d", len(entries))
	}

	expectedOrder := []string{"apple", "MANGO", "ZEBRA"}
	for i, exp := range expectedOrder {
		got, _ := entries[i]["key"].(string)
		if got != exp {
			t.Errorf("entry[%d].key = %q; want %q", i, got, exp)
		}
	}
}
