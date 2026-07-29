package secrets

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

// TS-07-20: Verifies that a valid POST to a secrets endpoint creates entries
// atomically and returns HTTP 201 with key and timestamps but never the value.
// Requirement: 07-REQ-8.1
func TestSecretCreate_Success(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-create", "secrets:manage")
	body := `{"entries":[{"key":"DB_HOST","value":"localhost:5432"},{"key":"DB_NAME","value":"mydb"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/secrets", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/user/secrets status = %d; want %d; body: %s",
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

	// Verify both entries have timestamps but no value.
	for i, entry := range entries {
		if _, ok := entry["created_at"]; !ok {
			t.Errorf("entries[%d] missing created_at", i)
		}
		if _, ok := entry["updated_at"]; !ok {
			t.Errorf("entries[%d] missing updated_at", i)
		}
		if _, ok := entry["value"]; ok {
			t.Errorf("entries[%d] has value field; secret values must never be returned", i)
		}
	}
}

// TestSecretCreate_ResponseNeverIncludesValue is a dedicated test for PROP-1:
// secret values are never returned by any API endpoint.
// Requirement: 07-REQ-8.1, 07-PROP-1
func TestSecretCreate_ResponseNeverIncludesValue(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-prop1", "secrets:manage")
	body := `{"entries":[{"key":"API_TOKEN","value":"super-secret-value-12345"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/secrets", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d", rec.Code, http.StatusCreated)
	}

	// Check the raw response body does not contain the value anywhere.
	responseBody := rec.Body.String()
	if strings.Contains(responseBody, "super-secret-value-12345") {
		t.Error("response body contains the secret value; it must never be returned")
	}
	if strings.Contains(responseBody, base64.StdEncoding.EncodeToString([]byte("super-secret-value-12345"))) {
		t.Error("response body contains the base64-encoded secret value; it must never be returned")
	}
}

// TestSecretCreate_EmptyEntries verifies that POST with empty entries returns 400.
// Requirement: 07-REQ-8.1
func TestSecretCreate_EmptyEntries(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-empty", "secrets:manage")
	body := `{"entries":[]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/secrets", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /api/v1/user/secrets (empty entries) status = %d; want %d",
			rec.Code, http.StatusBadRequest)
	}
}

// TS-07-21: Verifies that a POST request containing a key that already exists
// at the same scope returns HTTP 409 without writing any entries.
// Requirement: 07-REQ-8.2
func TestSecretCreate_DuplicateKey_Conflict(t *testing.T) {
	env := newHandlerTestEnv(t)

	// Seed an existing secret for the user.
	seedSecret(t, env.db, "user", "user-dup", "DB_HOST", "oldvalue")

	auth := patAuth("user-dup", "secrets:manage")
	body := `{"entries":[{"key":"DB_HOST","value":"newvalue"},{"key":"NEW_KEY","value":"v"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/secrets", body, auth)

	if rec.Code != http.StatusConflict {
		t.Errorf("POST /api/v1/user/secrets (duplicate key) status = %d; want %d",
			rec.Code, http.StatusConflict)
	}

	// Verify NEW_KEY was not written (all-or-nothing).
	var count int
	err := env.db.QueryRow(
		"SELECT COUNT(*) FROM secrets WHERE owner_type = ? AND owner_id = ?",
		"user", "user-dup",
	).Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 secret (only original); got %d", count)
	}
}

// TestSecretCreate_InvalidKeyName verifies that POST with an invalid key name
// returns HTTP 400.
// Requirement: 07-REQ-8.1 (validates key naming rules)
func TestSecretCreate_InvalidKeyName(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-badkey", "secrets:manage")

	tests := []struct {
		name string
		key  string
	}{
		{"starts with digit", `{"entries":[{"key":"1BAD","value":"v"}]}`},
		{"contains dash", `{"entries":[{"key":"BAD-KEY","value":"v"}]}`},
		{"empty key", `{"entries":[{"key":"","value":"v"}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := env.doRequest(t, http.MethodPost, "/api/v1/user/secrets", tt.key, auth)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("POST status = %d; want %d for key %q",
					rec.Code, http.StatusBadRequest, tt.name)
			}
		})
	}
}

// TestSecretCreate_ValueTooLarge verifies that POST with a value exceeding
// 256 KB returns HTTP 400.
// Requirement: 07-REQ-8.1 (validates value size)
func TestSecretCreate_ValueTooLarge(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-bigval", "secrets:manage")
	// Create a value slightly over MaxValueSize (262144 bytes).
	bigValue := strings.Repeat("x", MaxValueSize+1)
	body := `{"entries":[{"key":"BIG_SECRET","value":"` + bigValue + `"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/secrets", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST (value too large) status = %d; want %d",
			rec.Code, http.StatusBadRequest)
	}
}

// TestSecretCreate_OrgScoped verifies POST on org-scoped path.
// Requirement: 07-REQ-8.1
func TestSecretCreate_OrgScoped(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedOrg(t, "org-create", "Create Org", "create-org")
	env.seedOrgMember(t, "org-create", "user-org-create")

	auth := patAuth("user-org-create", "secrets:manage")
	body := `{"entries":[{"key":"ORG_SECRET","value":"orgval"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/orgs/create-org/secrets", body, auth)

	if rec.Code != http.StatusCreated {
		t.Errorf("POST /api/v1/orgs/create-org/secrets status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}
}

// TestSecretCreate_WorkspaceScoped verifies POST on workspace-scoped path.
// Requirement: 07-REQ-8.1
func TestSecretCreate_WorkspaceScoped(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedWorkspaceForTest(t, "ws-create", "user-ws-create")

	auth := patAuth("user-ws-create", "secrets:manage")
	body := `{"entries":[{"key":"WS_SECRET","value":"wsval"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-create/secrets", body, auth)

	if rec.Code != http.StatusCreated {
		t.Errorf("POST /api/v1/workspaces/ws-create/secrets status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}
}

// TestSecretCreate_OrgNotFound verifies POST on a non-existent org returns 404.
// Requirement: 07-REQ-8.E1
func TestSecretCreate_OrgNotFound(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-notfound", "secrets:manage")
	body := `{"entries":[{"key":"SECRET","value":"v"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/orgs/nonexistent-org/secrets", body, auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /api/v1/orgs/nonexistent-org/secrets status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}
}

// TestSecretCreate_PATSecretsListCannotCreate verifies that a PAT with
// secrets:list but not secrets:manage cannot create secrets.
// Requirement: 07-REQ-8.E3
func TestSecretCreate_PATSecretsListCannotCreate(t *testing.T) {
	env := newHandlerTestEnv(t)

	pat := patAuth("user-listonly", "secrets:list")
	body := `{"entries":[{"key":"SECRET","value":"val"}]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/user/secrets", body, pat)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST (secrets:list only) status = %d; want %d",
			rec.Code, http.StatusForbidden)
	}
}
