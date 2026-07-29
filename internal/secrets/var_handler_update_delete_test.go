package secrets

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

// TS-07-32: Verifies that PATCH on a variables endpoint updates the value and
// updated_at, returns HTTP 200 with key, decoded value, and timestamps.
// Requirement: 07-REQ-14.1
func TestVarUpdate_Success(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedVariable(t, env.db, "user", "user-var-update", "DB_HOST", "oldvalue")

	auth := patAuth("user-var-update", "vars:write")
	body := `{"value":"newvalue"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/vars/DB_HOST", body, auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/v1/user/vars/DB_HOST status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	result := parseRawJSON(t, rec)

	// Key should be returned.
	if result["key"] != "DB_HOST" {
		t.Errorf("key = %v; want DB_HOST", result["key"])
	}

	// Value MUST be included for variables (unlike secrets).
	if result["value"] != "newvalue" {
		t.Errorf("value = %v; want newvalue", result["value"])
	}

	// Timestamps must be present.
	if _, ok := result["created_at"]; !ok {
		t.Error("response missing created_at")
	}
	if _, ok := result["updated_at"]; !ok {
		t.Error("response missing updated_at")
	}

	// Verify the stored value was actually updated (base64-encoded in DB).
	var storedVal string
	err := env.db.QueryRow(
		"SELECT value FROM variables WHERE owner_type = ? AND owner_id = ? AND key = ?",
		"user", "user-var-update", "DB_HOST",
	).Scan(&storedVal)
	if err != nil {
		t.Fatalf("query stored value failed: %v", err)
	}
	expected := base64.StdEncoding.EncodeToString([]byte("newvalue"))
	if storedVal != expected {
		t.Errorf("stored value = %q; want %q (base64 of 'newvalue')", storedVal, expected)
	}
}

// TestVarUpdate_UpdatedAtChanges verifies that updated_at is newer than
// created_at after a PATCH.
// Requirement: 07-REQ-14.1
func TestVarUpdate_UpdatedAtChanges(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedVariable(t, env.db, "user", "user-var-ts", "TS_VAR", "initial")

	auth := patAuth("user-var-ts", "vars:write")
	body := `{"value":"updated"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/vars/TS_VAR", body, auth)

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

// TestVarUpdate_MissingValue verifies that PATCH with missing value field
// returns HTTP 400.
// Requirement: 07-REQ-14.E1
func TestVarUpdate_MissingValue(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedVariable(t, env.db, "user", "user-var-noval", "MY_VAR", "val")

	auth := patAuth("user-var-noval", "vars:write")
	body := `{}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/vars/MY_VAR", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH (missing value) status = %d; want %d",
			rec.Code, http.StatusBadRequest)
	}
}

// TestVarUpdate_NullValue verifies that PATCH with null value returns 400.
// Requirement: 07-REQ-14.E1
func TestVarUpdate_NullValue(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedVariable(t, env.db, "user", "user-var-null", "MY_VAR", "val")

	auth := patAuth("user-var-null", "vars:write")
	body := `{"value":null}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/vars/MY_VAR", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH (null value) status = %d; want %d",
			rec.Code, http.StatusBadRequest)
	}
}

// TestVarUpdate_EmptyStringValue verifies that PATCH with empty string
// value is valid and returns HTTP 200.
// Requirement: 07-REQ-14.1
func TestVarUpdate_EmptyStringValue(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedVariable(t, env.db, "user", "user-var-emptyval", "MY_VAR", "val")

	auth := patAuth("user-var-emptyval", "vars:write")
	body := `{"value":""}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/vars/MY_VAR", body, auth)

	if rec.Code != http.StatusOK {
		t.Errorf("PATCH (empty string value) status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TS-07-33: Verifies that PATCH on a non-existent variable key returns HTTP 404.
// Requirement: 07-REQ-14.2
func TestVarUpdate_NotFound(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-var-notfound", "vars:write")
	body := `{"value":"somevalue"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/vars/NONEXISTENT", body, auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("PATCH /api/v1/user/vars/NONEXISTENT status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}
}

// TestVarUpdate_CaseInsensitiveLookup verifies that PATCH performs
// case-insensitive key lookup and returns the originally stored key casing.
// Requirement: 07-REQ-14.E2
func TestVarUpdate_CaseInsensitiveLookup(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedVariable(t, env.db, "user", "user-var-case", "DB_HOST", "oldval")

	auth := patAuth("user-var-case", "vars:write")
	body := `{"value":"newval"}`
	// PATCH with lowercase 'db_host' should match stored 'DB_HOST'.
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/vars/db_host", body, auth)

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

// TestVarUpdate_ValueTooLarge verifies that PATCH with value exceeding
// 262144 bytes returns HTTP 400.
// Requirement: 07-REQ-14.E3
func TestVarUpdate_ValueTooLarge(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedVariable(t, env.db, "user", "user-var-bigpatch", "BIG_VAR", "smallval")

	auth := patAuth("user-var-bigpatch", "vars:write")
	bigValue := strings.Repeat("y", MaxValueSize+1)
	body := `{"value":"` + bigValue + `"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/vars/BIG_VAR", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH (value too large) status = %d; want %d",
			rec.Code, http.StatusBadRequest)
	}
}

// TestVarUpdate_VarsManageCanUpdate verifies that vars:manage implies
// ability to update variables.
// Requirement: 07-REQ-14.1
func TestVarUpdate_VarsManageCanUpdate(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedVariable(t, env.db, "user", "user-var-manage-up", "KEY_A", "oldval")

	auth := patAuth("user-var-manage-up", "vars:manage")
	body := `{"value":"newval"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/user/vars/KEY_A", body, auth)

	if rec.Code != http.StatusOK {
		t.Errorf("PATCH (vars:manage) status = %d; want %d",
			rec.Code, http.StatusOK)
	}
}

// TS-07-34: Verifies that DELETE on a variables endpoint removes the variable
// and returns HTTP 204 No Content.
// Requirement: 07-REQ-15.1
func TestVarDelete_Success(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedVariable(t, env.db, "user", "user-var-delete", "DB_HOST", "val")

	auth := patAuth("user-var-delete", "vars:delete")
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/user/vars/DB_HOST", "", auth)

	if rec.Code != http.StatusNoContent {
		t.Errorf("DELETE /api/v1/user/vars/DB_HOST status = %d; want %d",
			rec.Code, http.StatusNoContent)
	}

	// Verify the response body is empty.
	if rec.Body.Len() > 0 {
		t.Errorf("DELETE response body is not empty: %q", rec.Body.String())
	}

	// Verify the variable was actually deleted.
	var count int
	err := env.db.QueryRow(
		"SELECT COUNT(*) FROM variables WHERE owner_type = ? AND owner_id = ? AND key = ?",
		"user", "user-var-delete", "DB_HOST",
	).Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("variable still exists after DELETE; count = %d", count)
	}
}

// TS-07-35: Verifies that DELETE on a non-existent variable key returns HTTP 404.
// Requirement: 07-REQ-15.2
func TestVarDelete_NotFound(t *testing.T) {
	env := newHandlerTestEnv(t)

	auth := patAuth("user-var-delnf", "vars:delete")
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/user/vars/NONEXISTENT", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE /api/v1/user/vars/NONEXISTENT status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}
}

// TestVarDelete_CaseInsensitiveLookup verifies that DELETE performs
// case-insensitive key lookup.
// Requirement: 07-REQ-15.E1
func TestVarDelete_CaseInsensitiveLookup(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedVariable(t, env.db, "user", "user-var-del-case", "DB_HOST", "val")

	auth := patAuth("user-var-del-case", "vars:delete")
	// DELETE with lowercase 'db_host' should match stored 'DB_HOST'.
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/user/vars/db_host", "", auth)

	if rec.Code != http.StatusNoContent {
		t.Errorf("DELETE (case-insensitive) status = %d; want %d",
			rec.Code, http.StatusNoContent)
	}

	// Verify the variable was actually deleted.
	var count int
	err := env.db.QueryRow(
		"SELECT COUNT(*) FROM variables WHERE owner_type = ? AND owner_id = ?",
		"user", "user-var-del-case",
	).Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("variable still exists after case-insensitive DELETE; count = %d", count)
	}
}

// TestVarDelete_WorkspaceNonOwner verifies that DELETE on a workspace-scoped
// variable by a non-owner returns 404 (anti-enumeration).
// Requirement: 07-REQ-15.E2
func TestVarDelete_WorkspaceNonOwner(t *testing.T) {
	env := newHandlerTestEnv(t)

	env.seedWorkspaceForTest(t, "ws-var-del-enum", "user-a")
	seedVariable(t, env.db, "workspace", "ws-var-del-enum", "WS_VAR", "val")

	// user-b is NOT the workspace owner.
	pat := patAuth("user-b", "vars:delete")
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws-var-del-enum/vars/WS_VAR", "", pat)

	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE (workspace non-owner) status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}
}

// TestVarDelete_VarsManageCanDelete verifies that vars:manage implies
// ability to delete variables.
// Requirement: 07-REQ-15.1
func TestVarDelete_VarsManageCanDelete(t *testing.T) {
	env := newHandlerTestEnv(t)

	seedVariable(t, env.db, "user", "user-var-manage-del", "KEY_B", "val")

	auth := patAuth("user-var-manage-del", "vars:manage")
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/user/vars/KEY_B", "", auth)

	if rec.Code != http.StatusNoContent {
		t.Errorf("DELETE (vars:manage) status = %d; want %d",
			rec.Code, http.StatusNoContent)
	}
}
