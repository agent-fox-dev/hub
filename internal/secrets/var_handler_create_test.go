package secrets

import (
	"net/http"
	"strings"
	"testing"
)

// TS-07-28: Verifies that a valid POST to a variables endpoint creates entries
// atomically and returns HTTP 201 with key, decoded value, and timestamps.
// Requirement: 07-REQ-12.1
func TestVarCreate_Success(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-var-create", "vars:manage")
	body := `{"entries":[{"key":"DB_HOST","value":"localhost:5432"},{"key":"DB_NAME","value":"mydb"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/vars", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/user/vars status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	entries := parseRawJSONArray(t, rec)
	if len(entries) != 2 {
		t.Fatalf("response body has %d entries; want 2", len(entries))
	}

	// Verify first entry.
	if entries[0]["key"] != "DB_HOST" {
		t.Errorf("entries[0].key = %v; want DB_HOST", entries[0]["key"])
	}
	if entries[0]["value"] != "localhost:5432" {
		t.Errorf("entries[0].value = %v; want localhost:5432", entries[0]["value"])
	}

	// Verify second entry.
	if entries[1]["key"] != "DB_NAME" {
		t.Errorf("entries[1].key = %v; want DB_NAME", entries[1]["key"])
	}
	if entries[1]["value"] != "mydb" {
		t.Errorf("entries[1].value = %v; want mydb", entries[1]["value"])
	}

	// Verify both entries have timestamps and value.
	for i, entry := range entries {
		if _, ok := entry["created_at"]; !ok {
			t.Errorf("entries[%d] missing created_at", i)
		}
		if _, ok := entry["updated_at"]; !ok {
			t.Errorf("entries[%d] missing updated_at", i)
		}
		if _, ok := entry["value"]; !ok {
			t.Errorf("entries[%d] missing value; variable values must be returned", i)
		}
	}
}

// TestVarCreate_ResponseIncludesDecodedValue verifies that variable values
// are returned decoded (not base64-encoded) in the create response.
// Requirement: 07-REQ-12.1, 07-PROP-2
func TestVarCreate_ResponseIncludesDecodedValue(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-var-decoded", "vars:manage")
	body := `{"entries":[{"key":"MY_VAR","value":"plain-text-value"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/vars", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d", rec.Code, http.StatusCreated)
	}

	entries := parseRawJSONArray(t, rec)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry; got %d", len(entries))
	}
	if entries[0]["value"] != "plain-text-value" {
		t.Errorf("value = %v; want 'plain-text-value' (decoded)", entries[0]["value"])
	}
}

// TestVarCreate_EmptyEntries verifies that POST with empty entries returns 400.
// Requirement: 07-REQ-12.1
func TestVarCreate_EmptyEntries(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-var-empty", "vars:manage")
	body := `{"entries":[]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/vars", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /api/v1/user/vars (empty entries) status = %d; want %d",
			rec.Code, http.StatusBadRequest)
	}
}

// TS-07-29: Verifies that a POST to a variables endpoint with a key that
// already exists at the same scope returns HTTP 409 without writing any entries.
// Requirement: 07-REQ-12.2
func TestVarCreate_DuplicateKey_Conflict(t *testing.T) {
	env := newHandlerTestEnv(t)

	// Seed an existing variable for the user.
	seedVariable(t, env.db, "user", "user-var-dup", "DB_HOST", "oldvalue")

	auth := patAuth("user-var-dup", "vars:manage")
	body := `{"entries":[{"key":"DB_HOST","value":"newvalue"},{"key":"NEW_VAR","value":"v"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/vars", body, auth)

	if rec.Code != http.StatusConflict {
		t.Errorf("POST /api/v1/user/vars (duplicate key) status = %d; want %d",
			rec.Code, http.StatusConflict)
	}

	// Verify NEW_VAR was not written (all-or-nothing).
	var count int
	err := env.db.QueryRow(
		"SELECT COUNT(*) FROM variables WHERE owner_type = ? AND owner_id = ?",
		"user", "user-var-dup",
	).Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 variable (only original); got %d", count)
	}
}

// TestVarCreate_InvalidKeyName verifies that POST with an invalid key name
// returns HTTP 400.
// Requirement: 07-REQ-12.1 (validates key naming rules)
func TestVarCreate_InvalidKeyName(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-var-badkey", "vars:manage")

	tests := []struct {
		name string
		body string
	}{
		{"starts with digit", `{"entries":[{"key":"1BAD","value":"v"}]}`},
		{"contains dash", `{"entries":[{"key":"BAD-KEY","value":"v"}]}`},
		{"empty key", `{"entries":[{"key":"","value":"v"}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := env.doRequest(t, http.MethodPost, "/api/v1/user/vars", tt.body, auth)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("POST status = %d; want %d for key %q",
					rec.Code, http.StatusBadRequest, tt.name)
			}
		})
	}
}

// TestVarCreate_ValueTooLarge verifies that POST with a value exceeding
// 256 KB returns HTTP 400.
// Requirement: 07-REQ-12.1 (validates value size)
func TestVarCreate_ValueTooLarge(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-var-bigval", "vars:manage")
	bigValue := strings.Repeat("x", MaxValueSize+1)
	body := `{"entries":[{"key":"BIG_VAR","value":"` + bigValue + `"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/vars", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST (value too large) status = %d; want %d",
			rec.Code, http.StatusBadRequest)
	}
}

// TestVarCreate_OrgScoped verifies POST on org-scoped variables path.
// Requirement: 07-REQ-12.1
func TestVarCreate_OrgScoped(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedOrg(t, "org-var-create", "VarOrg", "var-org")
	env.seedOrgMember(t, "org-var-create", "user-org-var")

	auth := patAuth("user-org-var", "vars:manage")
	body := `{"entries":[{"key":"ORG_VAR","value":"orgval"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/orgs/var-org/vars", body, auth)

	if rec.Code != http.StatusCreated {
		t.Errorf("POST /api/v1/orgs/var-org/vars status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}
}

// TestVarCreate_WorkspaceScoped verifies POST on workspace-scoped variables path.
// Requirement: 07-REQ-12.1
func TestVarCreate_WorkspaceScoped(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedWorkspaceForTest(t, "ws-var-create", "user-ws-var")

	auth := patAuth("user-ws-var", "vars:manage")
	body := `{"entries":[{"key":"WS_VAR","value":"wsval"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-var-create/vars", body, auth)

	if rec.Code != http.StatusCreated {
		t.Errorf("POST /api/v1/workspaces/ws-var-create/vars status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}
}

// TestVarCreate_OrgNotFound verifies POST on a non-existent org returns 404.
// Requirement: 07-REQ-12.E1
func TestVarCreate_OrgNotFound(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-var-nforg", "vars:manage")
	body := `{"entries":[{"key":"VAR","value":"v"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/orgs/nonexistent-org/vars", body, auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /api/v1/orgs/nonexistent-org/vars status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}
}

// TestVarCreate_PATVarsReadCannotCreate verifies that a PAT with vars:read
// but not vars:manage cannot create variables.
// Requirement: 07-REQ-12.E3
func TestVarCreate_PATVarsReadCannotCreate(t *testing.T) {
	env := newHandlerTestEnv(t)

	pat := patAuth("user-var-readonly", "vars:read")
	body := `{"entries":[{"key":"VAR","value":"val"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/vars", body, pat)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST (vars:read only) status = %d; want %d",
			rec.Code, http.StatusForbidden)
	}
}
