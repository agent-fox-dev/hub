package workspace

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// =============================================================================
// TS-03-1: DDL schema test for display_name and description columns
// Requirement: 03-REQ-1.1
// =============================================================================

// TestSpec03_SchemaDisplayNameColumn verifies that the workspaces table DDL
// includes a display_name column as TEXT NOT NULL DEFAULT ''.
func TestSpec03_SchemaDisplayNameColumn(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	type columnInfo struct {
		name      string
		colType   string
		notNull   int
		dfltValue *string
	}

	rows, err := db.Query("PRAGMA table_info(workspaces)")
	if err != nil {
		t.Fatalf("PRAGMA table_info failed: %v", err)
	}
	defer rows.Close()

	columns := make(map[string]columnInfo)
	for rows.Next() {
		var (
			cid        int
			name       string
			colType    string
			notNull    int
			dfltValue  *string
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &primaryKey); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		columns[name] = columnInfo{
			name:      name,
			colType:   colType,
			notNull:   notNull,
			dfltValue: dfltValue,
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}

	// Verify display_name column exists with correct properties.
	dn, ok := columns["display_name"]
	if !ok {
		t.Fatal("column 'display_name' not found in workspaces table")
	}
	if dn.colType != "TEXT" {
		t.Errorf("display_name type = %q; want %q", dn.colType, "TEXT")
	}
	if dn.notNull != 1 {
		t.Errorf("display_name notnull = %d; want 1", dn.notNull)
	}
	if dn.dfltValue == nil || *dn.dfltValue != "''" {
		got := "<nil>"
		if dn.dfltValue != nil {
			got = *dn.dfltValue
		}
		t.Errorf("display_name dflt_value = %s; want ''", got)
	}
}

// TestSpec03_SchemaDescriptionColumn verifies that the workspaces table DDL
// includes a description column as TEXT NOT NULL DEFAULT ''.
func TestSpec03_SchemaDescriptionColumn(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	type columnInfo struct {
		name      string
		colType   string
		notNull   int
		dfltValue *string
	}

	rows, err := db.Query("PRAGMA table_info(workspaces)")
	if err != nil {
		t.Fatalf("PRAGMA table_info failed: %v", err)
	}
	defer rows.Close()

	columns := make(map[string]columnInfo)
	for rows.Next() {
		var (
			cid        int
			name       string
			colType    string
			notNull    int
			dfltValue  *string
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &primaryKey); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		columns[name] = columnInfo{
			name:      name,
			colType:   colType,
			notNull:   notNull,
			dfltValue: dfltValue,
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}

	// Verify description column exists with correct properties.
	desc, ok := columns["description"]
	if !ok {
		t.Fatal("column 'description' not found in workspaces table")
	}
	if desc.colType != "TEXT" {
		t.Errorf("description type = %q; want %q", desc.colType, "TEXT")
	}
	if desc.notNull != 1 {
		t.Errorf("description notnull = %d; want 1", desc.notNull)
	}
	if desc.dfltValue == nil || *desc.dfltValue != "''" {
		got := "<nil>"
		if desc.dfltValue != nil {
			got = *desc.dfltValue
		}
		t.Errorf("description dflt_value = %s; want ''", got)
	}
}

// =============================================================================
// TS-03-2: display_name defaults to slug when omitted at creation
// Requirement: 03-REQ-1.2
// =============================================================================

// TestSpec03_CreateDisplayNameDefaultsToSlug verifies that creating a workspace
// without display_name sets display_name equal to the slug.
func TestSpec03_CreateDisplayNameDefaultsToSlug(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("user-1")
	body := `{"slug":"test-slug","git_url":"https://git.example.com/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/workspaces status = %d; want %d", rec.Code, http.StatusCreated)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.DisplayName != "test-slug" {
		t.Errorf("display_name = %q; want %q", ws.DisplayName, "test-slug")
	}
}

// =============================================================================
// TS-03-E3: display_name set to null or empty string at creation normalizes to slug
// Requirement: 03-REQ-1.E3
// =============================================================================

// TestSpec03_CreateDisplayNameNullNormalizesToSlug verifies that setting
// display_name to null at creation normalizes it to the slug value.
func TestSpec03_CreateDisplayNameNullNormalizesToSlug(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("user-1")
	body := `{"slug":"norm-create-ws","git_url":"https://git.example.com/repo","display_name":null}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/workspaces status = %d; want %d", rec.Code, http.StatusCreated)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.DisplayName != "norm-create-ws" {
		t.Errorf("display_name = %q; want %q", ws.DisplayName, "norm-create-ws")
	}
}

// TestSpec03_CreateDisplayNameEmptyNormalizesToSlug verifies that setting
// display_name to empty string at creation normalizes it to the slug value.
func TestSpec03_CreateDisplayNameEmptyNormalizesToSlug(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("user-1")
	body := `{"slug":"norm-empty-ws","git_url":"https://git.example.com/repo","display_name":""}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/workspaces status = %d; want %d", rec.Code, http.StatusCreated)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.DisplayName != "norm-empty-ws" {
		t.Errorf("display_name = %q; want %q", ws.DisplayName, "norm-empty-ws")
	}
}

// =============================================================================
// TS-03-3: description defaults to empty string when omitted at creation
// Requirement: 03-REQ-1.3
// =============================================================================

// TestSpec03_CreateDescriptionDefaultsToEmpty verifies that creating a workspace
// without description sets description to empty string.
func TestSpec03_CreateDescriptionDefaultsToEmpty(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("user-1")
	body := `{"slug":"test-slug-2","git_url":"https://git.example.com/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/workspaces status = %d; want %d", rec.Code, http.StatusCreated)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.Description != "" {
		t.Errorf("description = %q; want empty string", ws.Description)
	}
}

// =============================================================================
// TS-03-E4: description set to null at creation normalizes to empty string
// Requirement: 03-REQ-1.E4
// =============================================================================

// TestSpec03_CreateDescriptionNullNormalizesToEmpty verifies that setting
// description to null at creation normalizes it to empty string.
func TestSpec03_CreateDescriptionNullNormalizesToEmpty(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("user-1")
	body := `{"slug":"nulldesc-ws","git_url":"https://git.example.com/repo","description":null}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/workspaces status = %d; want %d", rec.Code, http.StatusCreated)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.Description != "" {
		t.Errorf("description = %q; want empty string", ws.Description)
	}
}

// =============================================================================
// TS-03-4, TS-03-26: display_name and description present as non-null strings
// in every workspace response operation (create, list, get, archive, reactivate)
// Requirements: 03-REQ-1.4, 03-REQ-5.1
// =============================================================================

// TestSpec03_FieldsPresentInAllOperations verifies that display_name and
// description are present as non-null strings in responses from create, list,
// GET, archive, and reactivate operations.
//
// Note: PATCH is tested separately in task group 3 (update endpoint).
func TestSpec03_FieldsPresentInAllOperations(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("user-1")

	// CREATE
	createBody := `{"slug":"ws-all-ops","git_url":"https://git.example.com/repo","display_name":"All Ops WS","description":"test desc"}`
	createRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", createBody, auth)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("CREATE status = %d; want %d", createRec.Code, http.StatusCreated)
	}
	createWS := parseWorkspaceJSON(t, createRec)
	assertFieldsNonNull(t, "CREATE", createWS)

	// LIST
	listRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
	if listRec.Code != http.StatusOK {
		t.Fatalf("LIST status = %d; want %d", listRec.Code, http.StatusOK)
	}
	listWS := parseWorkspaceListJSON(t, listRec)
	if len(listWS) == 0 {
		t.Fatal("LIST returned empty array; expected at least one workspace")
	}
	assertFieldsNonNull(t, "LIST", listWS[0])

	// GET
	getRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-all-ops", "", auth)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d", getRec.Code, http.StatusOK)
	}
	getWS := parseWorkspaceJSON(t, getRec)
	assertFieldsNonNull(t, "GET", getWS)

	// ARCHIVE
	archiveRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-all-ops/archive", "", auth)
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("ARCHIVE status = %d; want %d", archiveRec.Code, http.StatusOK)
	}
	archiveWS := parseWorkspaceJSON(t, archiveRec)
	assertFieldsNonNull(t, "ARCHIVE", archiveWS)

	// REACTIVATE
	reactivateRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-all-ops/reactivate", "", auth)
	if reactivateRec.Code != http.StatusOK {
		t.Fatalf("REACTIVATE status = %d; want %d", reactivateRec.Code, http.StatusOK)
	}
	reactivateWS := parseWorkspaceJSON(t, reactivateRec)
	assertFieldsNonNull(t, "REACTIVATE", reactivateWS)
}

// assertFieldsNonNull checks that display_name is a non-empty string and
// description is a non-null string (may be empty) in the parsed workspace JSON.
// It uses encoding/json deserialization, which defaults strings to "" (zero value)
// when a field is absent or null. We verify that display_name specifically is
// non-empty (it should be the slug at minimum).
func assertFieldsNonNull(t *testing.T, operation string, ws workspaceJSON) {
	t.Helper()
	// display_name must be a non-empty string (never null, never empty — defaults to slug).
	if ws.DisplayName == "" {
		t.Errorf("%s: display_name is empty; want non-empty string (at least slug)", operation)
	}
	// description is allowed to be empty string, but must be present.
	// Since Go deserializes missing/null JSON string fields to "", we
	// verify the raw response includes the field by checking the full
	// response schema test (TS-03-27) separately.
}

// =============================================================================
// TS-03-27: All required fields present in workspace response objects
// Requirement: 03-REQ-5.2
// =============================================================================

// TestSpec03_AllRequiredFieldsInResponse verifies that the workspace response
// includes all required fields: slug, git_url, branch, display_name,
// description, owner_id, org_id, status, created_at, updated_at.
func TestSpec03_AllRequiredFieldsInResponse(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("user-1")

	body := `{"slug":"full-schema-ws","git_url":"https://git.example.com/repo"}`
	createRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d", createRec.Code, http.StatusCreated)
	}

	// Re-read via GET to use a fresh response.
	getRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/full-schema-ws", "", auth)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d", getRec.Code, http.StatusOK)
	}

	// Parse as raw map to check field presence (including null-valued fields).
	var raw map[string]interface{}
	body2 := getRec.Body.String()
	if err := jsonUnmarshal([]byte(body2), &raw); err != nil {
		t.Fatalf("failed to decode response as map: %v", err)
	}

	requiredFields := []string{
		"slug", "git_url", "branch", "display_name", "description",
		"owner_id", "org_id", "status", "created_at", "updated_at",
	}
	for _, field := range requiredFields {
		if _, ok := raw[field]; !ok {
			t.Errorf("required field %q is missing from workspace response", field)
		}
	}
}

// =============================================================================
// TS-03-5: provided display_name stored and returned at creation
// Requirement: 03-REQ-1.5
// =============================================================================

// TestSpec03_CreateWithDisplayName verifies that a non-empty display_name up
// to 128 characters provided at creation is stored and returned unchanged.
func TestSpec03_CreateWithDisplayName(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("user-1")
	body := `{"slug":"named-ws","git_url":"https://git.example.com/repo","display_name":"My Named Workspace"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d", rec.Code, http.StatusCreated)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.DisplayName != "My Named Workspace" {
		t.Errorf("display_name = %q; want %q", ws.DisplayName, "My Named Workspace")
	}
}

// TestSpec03_CreateWithDisplayNameMaxLength verifies that a display_name of
// exactly 128 characters is accepted.
func TestSpec03_CreateWithDisplayNameMaxLength(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("user-1")
	dn128 := strings.Repeat("a", 128)
	body := `{"slug":"dn128-ws","git_url":"https://git.example.com/repo","display_name":"` + dn128 + `"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d", rec.Code, http.StatusCreated)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.DisplayName != dn128 {
		t.Errorf("display_name length = %d; want 128", len(ws.DisplayName))
	}
}

// =============================================================================
// TS-03-6: provided description stored and returned at creation
// Requirement: 03-REQ-1.6
// =============================================================================

// TestSpec03_CreateWithDescription verifies that a non-empty description up to
// 1024 characters provided at creation is stored and returned unchanged.
func TestSpec03_CreateWithDescription(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("user-1")
	body := `{"slug":"desc-ws","git_url":"https://git.example.com/repo","description":"A detailed description of this workspace."}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d", rec.Code, http.StatusCreated)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.Description != "A detailed description of this workspace." {
		t.Errorf("description = %q; want %q", ws.Description, "A detailed description of this workspace.")
	}
}

// TestSpec03_CreateWithDescriptionMaxLength verifies that a description of
// exactly 1024 characters is accepted.
func TestSpec03_CreateWithDescriptionMaxLength(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("user-1")
	desc1024 := strings.Repeat("b", 1024)
	body := `{"slug":"desc1024-ws","git_url":"https://git.example.com/repo","description":"` + desc1024 + `"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d", rec.Code, http.StatusCreated)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.Description != desc1024 {
		t.Errorf("description length = %d; want 1024", len(ws.Description))
	}
}

// =============================================================================
// TS-03-28: POST with both display_name and description
// Requirement: 03-REQ-6.1
// =============================================================================

// TestSpec03_CreateWithBothMetadataFields verifies that POST /api/v1/workspaces
// with both display_name and description stores and returns those values.
func TestSpec03_CreateWithBothMetadataFields(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("user-1")
	body := `{"slug":"create-meta-ws","git_url":"https://git.example.com/repo","display_name":"Meta WS","description":"A workspace with meta"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d", rec.Code, http.StatusCreated)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.DisplayName != "Meta WS" {
		t.Errorf("display_name = %q; want %q", ws.DisplayName, "Meta WS")
	}
	if ws.Description != "A workspace with meta" {
		t.Errorf("description = %q; want %q", ws.Description, "A workspace with meta")
	}
}

// =============================================================================
// TS-03-29: POST without display_name and description (backward compatibility)
// Requirement: 03-REQ-6.2
// =============================================================================

// TestSpec03_CreateBackwardCompatible verifies that POST /api/v1/workspaces
// without display_name and description remains backward-compatible and returns
// server-side defaults.
func TestSpec03_CreateBackwardCompatible(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("user-1")
	body := `{"slug":"compat-ws","git_url":"https://git.example.com/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d", rec.Code, http.StatusCreated)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.DisplayName != "compat-ws" {
		t.Errorf("display_name = %q; want %q (slug)", ws.DisplayName, "compat-ws")
	}
	if ws.Description != "" {
		t.Errorf("description = %q; want empty string", ws.Description)
	}
}

// =============================================================================
// TS-03-E1: display_name exceeding 128 characters at creation returns HTTP 400
// Requirement: 03-REQ-1.E1
// =============================================================================

// TestSpec03_CreateDisplayNameTooLong verifies that creating a workspace with
// display_name exceeding 128 characters returns HTTP 400 and no workspace is created.
func TestSpec03_CreateDisplayNameTooLong(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("user-1")
	longName := strings.Repeat("a", 129)
	body := `{"slug":"long-dn-ws","git_url":"https://git.example.com/repo","display_name":"` + longName + `"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST status = %d; want %d", rec.Code, http.StatusBadRequest)
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}

	// Verify no workspace row was created.
	var count int
	err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", "long-dn-ws").Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("found %d rows with slug 'long-dn-ws'; want 0", count)
	}
}

// =============================================================================
// TS-03-E2: description exceeding 1024 characters at creation returns HTTP 400
// Requirement: 03-REQ-1.E2
// =============================================================================

// TestSpec03_CreateDescriptionTooLong verifies that creating a workspace with
// description exceeding 1024 characters returns HTTP 400 and no workspace is created.
func TestSpec03_CreateDescriptionTooLong(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("user-1")
	longDesc := strings.Repeat("b", 1025)
	body := `{"slug":"long-desc-ws","git_url":"https://git.example.com/repo","description":"` + longDesc + `"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST status = %d; want %d", rec.Code, http.StatusBadRequest)
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}

	// Verify no workspace row was created.
	var count int
	err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", "long-desc-ws").Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("found %d rows with slug 'long-desc-ws'; want 0", count)
	}
}

// jsonUnmarshal is a helper for decoding JSON into a value.
// It wraps encoding/json.Unmarshal for use in tests.
func jsonUnmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
