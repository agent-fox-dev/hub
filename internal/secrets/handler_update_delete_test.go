package secrets

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

// TS-07-24: Verifies that PATCH on a secrets endpoint updates the value and
// updated_at, returns HTTP 200 with key and timestamps but never the value.
// Requirement: 07-REQ-10.1
func TestSecretUpdate_Success(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedSecret(t, env.db, "user", "user-update", "DB_HOST", "oldvalue")

	auth := patAuth("user-update", "secrets:write")
	body := `{"value":"newvalue"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/secrets/DB_HOST", body, auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/v1/user/secrets/DB_HOST status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	result := parseRawJSON(t, rec)

	// Key should be returned.
	if result["key"] != "DB_HOST" {
		t.Errorf("key = %v; want DB_HOST", result["key"])
	}

	// Value must NOT be included.
	if _, ok := result["value"]; ok {
		t.Error("response includes value field; secret values must never be returned")
	}

	// Timestamps must be present.
	if _, ok := result["created_at"]; !ok {
		t.Error("response missing created_at")
	}
	if _, ok := result["updated_at"]; !ok {
		t.Error("response missing updated_at")
	}

	// Verify the stored value was actually updated (base64-encoded).
	var storedVal string
	err := env.db.QueryRow(
		"SELECT value FROM secrets WHERE owner_type = ? AND owner_id = ? AND key = ?",
		"user", "user-update", "DB_HOST",
	).Scan(&storedVal)
	if err != nil {
		t.Fatalf("query stored value failed: %v", err)
	}
	expected := base64.StdEncoding.EncodeToString([]byte("newvalue"))
	if storedVal != expected {
		t.Errorf("stored value = %q; want %q (base64 of 'newvalue')", storedVal, expected)
	}
}

// TestSecretUpdate_UpdatedAtChanges verifies that updated_at is newer than
// created_at after a PATCH.
// Requirement: 07-REQ-10.1
func TestSecretUpdate_UpdatedAtChanges(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedSecret(t, env.db, "user", "user-ts", "TS_KEY", "initial")

	auth := patAuth("user-ts", "secrets:write")
	body := `{"value":"updated"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/secrets/TS_KEY", body, auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d; want %d", rec.Code, http.StatusOK)
	}

	result := parseRawJSON(t, rec)
	createdAt, _ := result["created_at"].(string)
	updatedAt, _ := result["updated_at"].(string)

	if createdAt == "" || updatedAt == "" {
		t.Fatal("timestamps are empty")
	}
	if updatedAt < createdAt {
		t.Errorf("updated_at (%s) should be >= created_at (%s)", updatedAt, createdAt)
	}
}

// TS-07-25: Verifies that PATCH on a non-existent secret key returns HTTP 404.
// Requirement: 07-REQ-10.2
func TestSecretUpdate_NotFound(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-notfound", "secrets:write")
	body := `{"value":"somevalue"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/secrets/NONEXISTENT", body, auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("PATCH /api/v1/user/secrets/NONEXISTENT status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}
}

// TestSecretUpdate_MissingValue verifies that PATCH with missing value field
// returns HTTP 400.
// Requirement: 07-REQ-10.E1
func TestSecretUpdate_MissingValue(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedSecret(t, env.db, "user", "user-noval", "MY_KEY", "val")

	auth := patAuth("user-noval", "secrets:write")
	body := `{}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/secrets/MY_KEY", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH (missing value) status = %d; want %d",
			rec.Code, http.StatusBadRequest)
	}
}

// TestSecretUpdate_NullValue verifies that PATCH with null value returns 400.
// Requirement: 07-REQ-10.E1
func TestSecretUpdate_NullValue(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedSecret(t, env.db, "user", "user-null", "MY_KEY", "val")

	auth := patAuth("user-null", "secrets:write")
	body := `{"value":null}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/secrets/MY_KEY", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH (null value) status = %d; want %d",
			rec.Code, http.StatusBadRequest)
	}
}

// TestSecretUpdate_EmptyStringValue verifies that PATCH with empty string
// value is valid and returns HTTP 200.
// Requirement: 07-REQ-10.1
func TestSecretUpdate_EmptyStringValue(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedSecret(t, env.db, "user", "user-empty-val", "MY_KEY", "val")

	auth := patAuth("user-empty-val", "secrets:write")
	body := `{"value":""}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/secrets/MY_KEY", body, auth)

	if rec.Code != http.StatusOK {
		t.Errorf("PATCH (empty string value) status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TestSecretUpdate_CaseInsensitiveLookup verifies that PATCH performs
// case-insensitive key lookup and returns the originally stored key casing.
// Requirement: 07-REQ-10.E2
func TestSecretUpdate_CaseInsensitiveLookup(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedSecret(t, env.db, "user", "user-case", "DB_HOST", "oldval")

	auth := patAuth("user-case", "secrets:write")
	body := `{"value":"newval"}`
	// PATCH with lowercase 'db_host' should match stored 'DB_HOST'.
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/secrets/db_host", body, auth)

	if rec.Code != http.StatusOK {
		t.Errorf("PATCH (case-insensitive) status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify the response uses the originally stored key casing.
	result := parseRawJSON(t, rec)
	if result["key"] != "DB_HOST" {
		t.Errorf("returned key = %v; want 'DB_HOST' (original casing)", result["key"])
	}
}

// TestSecretUpdate_ValueTooLarge verifies that PATCH with value exceeding
// 262144 bytes returns HTTP 400.
// Requirement: 07-REQ-10.E3
func TestSecretUpdate_ValueTooLarge(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedSecret(t, env.db, "user", "user-bigpatch", "BIG_KEY", "smallval")

	auth := patAuth("user-bigpatch", "secrets:write")
	bigValue := strings.Repeat("y", MaxValueSize+1)
	body := `{"value":"` + bigValue + `"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/secrets/BIG_KEY", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH (value too large) status = %d; want %d",
			rec.Code, http.StatusBadRequest)
	}
}

// TS-07-26: Verifies that DELETE on a secrets endpoint removes the secret
// and returns HTTP 204 No Content.
// Requirement: 07-REQ-11.1
func TestSecretDelete_Success(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedSecret(t, env.db, "user", "user-delete", "DB_HOST", "val")

	auth := patAuth("user-delete", "secrets:delete")
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/user/secrets/DB_HOST", "", auth)

	if rec.Code != http.StatusNoContent {
		t.Errorf("DELETE /api/v1/user/secrets/DB_HOST status = %d; want %d",
			rec.Code, http.StatusNoContent)
	}

	// Verify the response body is empty.
	if rec.Body.Len() > 0 {
		t.Errorf("DELETE response body is not empty: %q", rec.Body.String())
	}

	// Verify the secret was actually deleted.
	var count int
	err := env.db.QueryRow(
		"SELECT COUNT(*) FROM secrets WHERE owner_type = ? AND owner_id = ? AND key = ?",
		"user", "user-delete", "DB_HOST",
	).Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("secret still exists after DELETE; count = %d", count)
	}
}

// TS-07-27: Verifies that DELETE on a non-existent secret key returns HTTP 404.
// Requirement: 07-REQ-11.2
func TestSecretDelete_NotFound(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-delnf", "secrets:delete")
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/user/secrets/NONEXISTENT", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE /api/v1/user/secrets/NONEXISTENT status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}
}

// TestSecretDelete_CaseInsensitiveLookup verifies that DELETE performs
// case-insensitive key lookup.
// Requirement: 07-REQ-11.E1
func TestSecretDelete_CaseInsensitiveLookup(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedSecret(t, env.db, "user", "user-del-case", "DB_HOST", "val")

	auth := patAuth("user-del-case", "secrets:delete")
	// DELETE with lowercase 'db_host' should match stored 'DB_HOST'.
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/user/secrets/db_host", "", auth)

	if rec.Code != http.StatusNoContent {
		t.Errorf("DELETE (case-insensitive) status = %d; want %d",
			rec.Code, http.StatusNoContent)
	}

	// Verify the secret was actually deleted.
	var count int
	err := env.db.QueryRow(
		"SELECT COUNT(*) FROM secrets WHERE owner_type = ? AND owner_id = ?",
		"user", "user-del-case",
	).Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("secret still exists after case-insensitive DELETE; count = %d", count)
	}
}

// TestSecretDelete_WorkspaceNonOwner verifies that DELETE on a workspace-scoped
// secret by a non-owner returns 404 (anti-enumeration).
// Requirement: 07-REQ-11.E2
func TestSecretDelete_WorkspaceNonOwner(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedWorkspaceForTest(t, "ws-del-enum", "user-a")
	seedSecret(t, env.db, "workspace", "ws-del-enum", "WS_KEY", "val")

	// user-b is NOT the workspace owner.
	pat := patAuth("user-b", "secrets:delete")
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws-del-enum/secrets/WS_KEY", "", pat)

	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE (workspace non-owner) status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}
}

// TestSecretUpdate_SecretsManageCanUpdate verifies that secrets:manage
// implies ability to update secrets.
// Requirement: 07-REQ-10.1
func TestSecretUpdate_SecretsManageCanUpdate(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedSecret(t, env.db, "user", "user-manage-up", "KEY_A", "oldval")

	auth := patAuth("user-manage-up", "secrets:manage")
	body := `{"value":"newval"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/secrets/KEY_A", body, auth)

	if rec.Code != http.StatusOK {
		t.Errorf("PATCH (secrets:manage) status = %d; want %d",
			rec.Code, http.StatusOK)
	}
}

// TestSecretDelete_SecretsManageCanDelete verifies that secrets:manage
// implies ability to delete secrets.
// Requirement: 07-REQ-11.1
func TestSecretDelete_SecretsManageCanDelete(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedSecret(t, env.db, "user", "user-manage-del", "KEY_B", "val")

	auth := patAuth("user-manage-del", "secrets:manage")
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/user/secrets/KEY_B", "", auth)

	if rec.Code != http.StatusNoContent {
		t.Errorf("DELETE (secrets:manage) status = %d; want %d",
			rec.Code, http.StatusNoContent)
	}
}
