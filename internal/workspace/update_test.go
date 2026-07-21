package workspace

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// Task 3.1: Integration tests — valid PATCH updates fields and sets updated_at
// TS-03-19, TS-03-20
// Requirements: 03-REQ-4.1, 03-REQ-4.2
// =============================================================================

// TS-03-19: Verify that a valid PATCH on an active workspace updates provided
// fields, sets updated_at, and returns HTTP 200 with the full workspace object.
// Requirement: 03-REQ-4.1
func TestSpec03_Group3_ValidPatchUpdatesFieldsAndTimestamp(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "patch-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	// Record initial updated_at.
	getRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/patch-ws", "", userAuth("u1-id"))
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d", getRec.Code, http.StatusOK)
	}
	initialWS := parseWorkspaceJSON(t, getRec)
	initialUpdatedAt := initialWS.UpdatedAt

	// Small delay to ensure updated_at will differ.
	time.Sleep(10 * time.Millisecond)

	// PATCH with description update.
	body := `{"description":"new description"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/patch-ws", body, userAuth("u1-id"))

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d; want %d", rec.Code, http.StatusOK)
	}

	ws := parseWorkspaceJSON(t, rec)

	// Verify description was updated.
	if ws.Description != "new description" {
		t.Errorf("description = %q; want %q", ws.Description, "new description")
	}

	// Verify updated_at is a valid RFC 3339 timestamp strictly greater than initial.
	patchedTime, err := time.Parse(time.RFC3339, ws.UpdatedAt)
	if err != nil {
		t.Fatalf("updated_at %q is not valid RFC 3339: %v", ws.UpdatedAt, err)
	}
	initialTime, err := time.Parse(time.RFC3339, initialUpdatedAt)
	if err != nil {
		t.Fatalf("initial updated_at %q is not valid RFC 3339: %v", initialUpdatedAt, err)
	}
	if !patchedTime.After(initialTime) {
		t.Errorf("updated_at %q is not strictly after initial %q",
			ws.UpdatedAt, initialUpdatedAt)
	}

	// Verify all workspace fields are present in the response.
	requiredFields := []string{
		"slug", "git_url", "branch", "display_name", "description",
		"owner_id", "org_id", "status", "created_at", "updated_at",
	}
	var rawMap map[string]any
	// Re-request to parse as raw map (rec.Body is already consumed).
	getRec2 := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/patch-ws", "", userAuth("u1-id"))
	if getRec2.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d", getRec2.Code, http.StatusOK)
	}
	if err := json.NewDecoder(getRec2.Body).Decode(&rawMap); err != nil {
		t.Fatalf("failed to decode raw response: %v", err)
	}
	for _, field := range requiredFields {
		if _, ok := rawMap[field]; !ok {
			t.Errorf("required field %q missing from workspace response", field)
		}
	}
}

// TS-03-20: Verify that PATCH with only display_name does not alter description
// or org_id in the workspaces table.
// Requirement: 03-REQ-4.2
func TestSpec03_Group3_PartialUpdateLeavesUnmentionedFieldsUnchanged(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "partial-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	// First set description to a known value.
	setupBody := `{"description":"keep me"}`
	setupRec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/partial-ws", setupBody, userAuth("u1-id"))
	if setupRec.Code != http.StatusOK {
		// If PATCH is not yet implemented, this will fail — that's expected for
		// the "tests fail before implementation" phase.
		t.Logf("setup PATCH status = %d (expected to fail before implementation)", setupRec.Code)
	}

	// PATCH with only display_name.
	body := `{"display_name":"New Name"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/partial-ws", body, userAuth("u1-id"))

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d; want %d", rec.Code, http.StatusOK)
	}

	ws := parseWorkspaceJSON(t, rec)

	if ws.DisplayName != "New Name" {
		t.Errorf("display_name = %q; want %q", ws.DisplayName, "New Name")
	}
	if ws.Description != "keep me" {
		t.Errorf("description = %q; want %q (unchanged)", ws.Description, "keep me")
	}
	if ws.OrgID != nil {
		t.Errorf("org_id = %v; want nil (unchanged)", ws.OrgID)
	}
}

// =============================================================================
// Task 3.2: Integration tests — normalization of null/empty display_name and
// description in PATCH
// TS-03-21, TS-03-22
// Requirements: 03-REQ-4.3, 03-REQ-4.4
// =============================================================================

// TS-03-21: Verify that setting display_name to null or empty string in a PATCH
// normalizes it to the workspace slug.
// Requirement: 03-REQ-4.3
func TestSpec03_Group3_PatchDisplayNameNullNormalizesToSlug(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "norm-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	// First set display_name to a custom value so we can verify it resets.
	setupBody := `{"display_name":"Custom Name"}`
	env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/norm-ws", setupBody, userAuth("u1-id"))

	// Test null → normalizes to slug.
	t.Run("null normalizes to slug", func(t *testing.T) {
		body := `{"display_name":null}`
		rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/norm-ws", body, userAuth("u1-id"))

		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH status = %d; want %d", rec.Code, http.StatusOK)
		}

		ws := parseWorkspaceJSON(t, rec)
		if ws.DisplayName != "norm-ws" {
			t.Errorf("display_name = %q; want %q (slug)", ws.DisplayName, "norm-ws")
		}
	})

	// Restore custom name for next test.
	env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/norm-ws",
		`{"display_name":"Custom Name"}`, userAuth("u1-id"))

	// Test empty string → normalizes to slug.
	t.Run("empty string normalizes to slug", func(t *testing.T) {
		body := `{"display_name":""}`
		rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/norm-ws", body, userAuth("u1-id"))

		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH status = %d; want %d", rec.Code, http.StatusOK)
		}

		ws := parseWorkspaceJSON(t, rec)
		if ws.DisplayName != "norm-ws" {
			t.Errorf("display_name = %q; want %q (slug)", ws.DisplayName, "norm-ws")
		}
	})
}

// TS-03-22: Verify that setting description to null or empty string in a PATCH
// normalizes it to empty string.
// Requirement: 03-REQ-4.4
func TestSpec03_Group3_PatchDescriptionNullNormalizesToEmpty(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "desc-norm-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	// First set description to a known value.
	setupBody := `{"description":"Some description"}`
	env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/desc-norm-ws", setupBody, userAuth("u1-id"))

	// Test null → normalizes to empty string.
	t.Run("null normalizes to empty string", func(t *testing.T) {
		body := `{"description":null}`
		rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/desc-norm-ws", body, userAuth("u1-id"))

		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH status = %d; want %d", rec.Code, http.StatusOK)
		}

		ws := parseWorkspaceJSON(t, rec)
		if ws.Description != "" {
			t.Errorf("description = %q; want empty string", ws.Description)
		}
	})

	// Restore description for next test.
	env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/desc-norm-ws",
		`{"description":"Some description"}`, userAuth("u1-id"))

	// Test empty string → normalizes to empty string.
	t.Run("empty string normalizes to empty string", func(t *testing.T) {
		body := `{"description":""}`
		rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/desc-norm-ws", body, userAuth("u1-id"))

		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH status = %d; want %d", rec.Code, http.StatusOK)
		}

		ws := parseWorkspaceJSON(t, rec)
		if ws.Description != "" {
			t.Errorf("description = %q; want empty string", ws.Description)
		}
	})
}

// =============================================================================
// Task 3.3: Integration tests — org_id update and removal in PATCH
// TS-03-23, TS-03-24
// Requirements: 03-REQ-4.5, 03-REQ-4.6
// =============================================================================

// TS-03-23: Verify that setting org_id to null in a PATCH removes the
// organization association and returns org_id as null.
// Requirement: 03-REQ-4.5
func TestSpec03_Group3_PatchOrgIDNullRemovesAssociation(t *testing.T) {
	env := newTestEnv(t)

	// Set up org and membership.
	env.seedOrg(t, "org-uuid-123", "Test Org", "test-org")
	env.seedOrgMember(t, "org-uuid-123", "u1-id")

	orgID := "org-uuid-123"
	env.seedWorkspace(t, &Workspace{
		Slug:    "org-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		OrgID:   &orgID,
		Status:  "active",
	})

	// PATCH with org_id: null.
	body := `{"org_id":null}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/org-ws", body, userAuth("u1-id"))

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d; want %d", rec.Code, http.StatusOK)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.OrgID != nil {
		t.Errorf("org_id = %v; want nil", ws.OrgID)
	}

	// Verify DB state.
	var dbOrgID *string
	err := env.db.QueryRow("SELECT org_id FROM workspaces WHERE slug = ?", "org-ws").Scan(&dbOrgID)
	if err != nil {
		t.Fatalf("DB query failed: %v", err)
	}
	if dbOrgID != nil {
		t.Errorf("DB org_id = %v; want NULL", dbOrgID)
	}
}

// TS-03-24: Verify that setting org_id to a non-null value when the owner is a
// member of that organization updates org_id and returns HTTP 200.
// Requirement: 03-REQ-4.6
func TestSpec03_Group3_PatchOrgIDSetWithValidMembership(t *testing.T) {
	env := newTestEnv(t)

	// Set up org and membership.
	env.seedOrg(t, "org-uuid-456", "New Org", "new-org")
	env.seedOrgMember(t, "org-uuid-456", "u1-id")

	env.seedWorkspace(t, &Workspace{
		Slug:    "orgset-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	// PATCH with valid org_id.
	body := `{"org_id":"org-uuid-456"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/orgset-ws", body, userAuth("u1-id"))

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d; want %d", rec.Code, http.StatusOK)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.OrgID == nil || *ws.OrgID != "org-uuid-456" {
		got := "<nil>"
		if ws.OrgID != nil {
			got = *ws.OrgID
		}
		t.Errorf("org_id = %s; want %q", got, "org-uuid-456")
	}
}

// =============================================================================
// Task 3.4: Integration tests — immutable field rejection in PATCH
// TS-03-25
// Requirement: 03-REQ-4.7
// =============================================================================

// TS-03-25: Verify that PATCH requests including immutable fields (slug,
// git_url, branch, owner_id) are rejected with HTTP 400.
// Requirement: 03-REQ-4.7
func TestSpec03_Group3_PatchRejectsImmutableFields(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "immutable-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	auth := userAuth("u1-id")

	// Table-driven test over immutable fields.
	immutableBodies := []struct {
		name string
		body string
	}{
		{"slug", `{"slug":"new-slug"}`},
		{"git_url", `{"git_url":"https://other.com/repo"}`},
		{"branch", `{"branch":"new-branch"}`},
		{"owner_id", `{"owner_id":"other-user"}`},
	}

	for _, tc := range immutableBodies {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/immutable-ws", tc.body, auth)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("PATCH with %s: status = %d; want %d",
					tc.name, rec.Code, http.StatusBadRequest)
			}

			resp := parseErrorEnvelope(t, rec)
			if resp.Error.Code != http.StatusBadRequest {
				t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
			}
			if resp.Error.Message == "" {
				t.Error("error.message is empty; want non-empty descriptive message")
			}
		})
	}

	// Confirm no mutation to workspaces table after all rejections.
	var dbGitURL string
	err := env.db.QueryRow("SELECT git_url FROM workspaces WHERE slug = ?", "immutable-ws").Scan(&dbGitURL)
	if err != nil {
		t.Fatalf("DB query failed: %v", err)
	}
	if dbGitURL != "https://git.example.com/repo" {
		t.Errorf("DB git_url = %q; want %q (unchanged)", dbGitURL, "https://git.example.com/repo")
	}
}

// =============================================================================
// Task 3.5: Edge-case tests — PATCH error conditions
// TS-03-E7, TS-03-E8, TS-03-E9, TS-03-E10, TS-03-E11, TS-03-E12, TS-03-E13
// Requirements: 03-REQ-4.E1 through 03-REQ-4.E7
// =============================================================================

// TS-03-E7: Verify that a PATCH with an empty body (no fields) returns HTTP 400
// and performs no database write.
// Requirement: 03-REQ-4.E1
func TestSpec03_Group3_PatchEmptyBodyReturns400(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "empty-patch-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	// Record initial updated_at.
	var initialUpdatedAt string
	err := env.db.QueryRow("SELECT updated_at FROM workspaces WHERE slug = ?", "empty-patch-ws").Scan(&initialUpdatedAt)
	if err != nil {
		t.Fatalf("DB query failed: %v", err)
	}

	// PATCH with empty body.
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/empty-patch-ws", "{}", userAuth("u1-id"))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH status = %d; want %d", rec.Code, http.StatusBadRequest)
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}

	// Verify updated_at unchanged.
	var afterUpdatedAt string
	err = env.db.QueryRow("SELECT updated_at FROM workspaces WHERE slug = ?", "empty-patch-ws").Scan(&afterUpdatedAt)
	if err != nil {
		t.Fatalf("DB query failed: %v", err)
	}
	if afterUpdatedAt != initialUpdatedAt {
		t.Errorf("updated_at changed from %q to %q; want unchanged", initialUpdatedAt, afterUpdatedAt)
	}
}

// TS-03-E8: Verify that attempting to PATCH an archived workspace returns HTTP
// 400 with an error indicating it must be reactivated.
// Requirement: 03-REQ-4.E2
func TestSpec03_Group3_PatchArchivedWorkspaceReturns400(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "archived-patch-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "archived",
	})

	body := `{"description":"should fail"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/archived-patch-ws", body, userAuth("u1-id"))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH status = %d; want %d", rec.Code, http.StatusBadRequest)
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}

	// Verify no changes written to the workspaces table by reading via API.
	getAfter := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/archived-patch-ws", "", userAuth("u1-id"))
	if getAfter.Code == http.StatusOK {
		afterWS := parseWorkspaceJSON(t, getAfter)
		if afterWS.Description == "should fail" {
			t.Error("description was written to DB despite archived status; want no changes")
		}
	}
}

// TS-03-E9: Verify that a PATCH with display_name exceeding 128 characters
// returns HTTP 400 and performs no database write.
// Requirement: 03-REQ-4.E3
func TestSpec03_Group3_PatchDisplayNameTooLongReturns400(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "dn-limit-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	// Record original display_name via API.
	getBefore := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/dn-limit-ws", "", userAuth("u1-id"))
	if getBefore.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d", getBefore.Code, http.StatusOK)
	}
	origWS := parseWorkspaceJSON(t, getBefore)
	origDN := origWS.DisplayName

	longName := strings.Repeat("a", 129)
	body := `{"display_name":"` + longName + `"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/dn-limit-ws", body, userAuth("u1-id"))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH status = %d; want %d", rec.Code, http.StatusBadRequest)
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}

	// Verify display_name unchanged via API.
	getAfter := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/dn-limit-ws", "", userAuth("u1-id"))
	if getAfter.Code == http.StatusOK {
		afterWS := parseWorkspaceJSON(t, getAfter)
		if afterWS.DisplayName != origDN {
			t.Errorf("display_name changed from %q to %q; want unchanged", origDN, afterWS.DisplayName)
		}
	}
}

// TS-03-E10: Verify that a PATCH with description exceeding 1024 characters
// returns HTTP 400 and performs no database write.
// Requirement: 03-REQ-4.E4
func TestSpec03_Group3_PatchDescriptionTooLongReturns400(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "desc-limit-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	// Record original description via API.
	getBefore := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/desc-limit-ws", "", userAuth("u1-id"))
	if getBefore.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d", getBefore.Code, http.StatusOK)
	}
	origWS := parseWorkspaceJSON(t, getBefore)
	origDesc := origWS.Description

	longDesc := strings.Repeat("b", 1025)
	body := `{"description":"` + longDesc + `"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/desc-limit-ws", body, userAuth("u1-id"))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH status = %d; want %d", rec.Code, http.StatusBadRequest)
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}

	// Verify description unchanged via API.
	getAfter := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/desc-limit-ws", "", userAuth("u1-id"))
	if getAfter.Code == http.StatusOK {
		afterWS := parseWorkspaceJSON(t, getAfter)
		if afterWS.Description != origDesc {
			t.Errorf("description changed from %q to %q; want unchanged", origDesc, afterWS.Description)
		}
	}
}

// TS-03-E11: Verify that setting org_id to an org where the workspace owner is
// not a member returns HTTP 403 and does not update org_id.
// Requirement: 03-REQ-4.E5
func TestSpec03_Group3_PatchOrgIDNonMemberReturns403(t *testing.T) {
	env := newTestEnv(t)

	// Create org but do NOT add u1 as member.
	env.seedOrg(t, "org-uuid-forbidden", "Forbidden Org", "forbidden-org")

	env.seedWorkspace(t, &Workspace{
		Slug:    "non-member-org-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	body := `{"org_id":"org-uuid-forbidden"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/non-member-org-ws", body, userAuth("u1-id"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("PATCH status = %d; want %d", rec.Code, http.StatusForbidden)
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusForbidden {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusForbidden)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}

	// Verify org_id remains null in DB.
	var dbOrgID *string
	err := env.db.QueryRow("SELECT org_id FROM workspaces WHERE slug = ?", "non-member-org-ws").Scan(&dbOrgID)
	if err != nil {
		t.Fatalf("DB query failed: %v", err)
	}
	if dbOrgID != nil {
		t.Errorf("org_id = %v; want nil (unchanged)", *dbOrgID)
	}
}

// TS-03-E12: Verify that PATCH on a non-existent slug returns HTTP 404 with
// error body.
// Requirement: 03-REQ-4.E6
func TestSpec03_Group3_PatchNonExistentSlugReturns404(t *testing.T) {
	env := newTestEnv(t)

	body := `{"description":"test"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/nonexistent-ws", body, userAuth("u1-id"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("PATCH status = %d; want %d", rec.Code, http.StatusNotFound)
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusNotFound {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// TS-03-E13: Verify that PATCH on an existing workspace by a non-owner (who is
// not admin) returns HTTP 404 (anti-enumeration).
// Requirement: 03-REQ-4.E7
func TestSpec03_Group3_PatchNonOwnerReturns404AntiEnumeration(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "owned-by-u1-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	// u2 attempts to PATCH u1's workspace.
	body := `{"description":"hacked"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/owned-by-u1-ws", body, userAuth("u2-id"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("PATCH status = %d; want %d (anti-enumeration)", rec.Code, http.StatusNotFound)
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusNotFound {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}

	// Verify workspace data was not changed by reading via API as u1.
	getAfter := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/owned-by-u1-ws", "", userAuth("u1-id"))
	if getAfter.Code == http.StatusOK {
		afterWS := parseWorkspaceJSON(t, getAfter)
		if afterWS.Description == "hacked" {
			t.Error("description was changed by non-owner; want unchanged")
		}
	}
}

// =============================================================================
// Task 3.6: Edge-case test — org membership service timeout
// TS-03-E14
// Requirement: 03-REQ-4.E8
// =============================================================================

// TS-03-E14: Verify that when the organization membership check service times
// out or errors, PATCH returns HTTP 500 and no partial state is written.
// Requirement: 03-REQ-4.E8
//
// Uses the injectable orgMembershipCheckFn to simulate a service error for a
// specific org_id ("org-uuid-timeout"). The injected checker returns HTTP 500
// for this org, mimicking a timeout or upstream failure. All other org checks
// delegate to the real checkOrgMembership implementation.
func TestSpec03_Group3_PatchOrgMembershipServiceErrorReturns500(t *testing.T) {
	env := newTestEnv(t)

	// Inject a failing org membership checker for "org-uuid-timeout".
	origCheck := orgMembershipCheckFn
	orgMembershipCheckFn = func(db *sql.DB, userID, orgID string) (int, string) {
		if orgID == "org-uuid-timeout" {
			return http.StatusInternalServerError, "organization membership check timed out"
		}
		return origCheck(db, userID, orgID)
	}
	t.Cleanup(func() { orgMembershipCheckFn = origCheck })

	env.seedWorkspace(t, &Workspace{
		Slug:    "org-timeout-ws",
		GitURL:  "https://git.example.com/repo",
		OwnerID: "u1-id",
		Status:  "active",
	})

	body := `{"org_id":"org-uuid-timeout"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/org-timeout-ws", body, userAuth("u1-id"))

	if rec.Code == http.StatusOK {
		t.Fatalf("PATCH status = %d; want non-200 (org membership check should fail)", rec.Code)
	}

	// Spec expects 500 for timeout/error scenarios.
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("PATCH status = %d; want %d (org service error)",
			rec.Code, http.StatusInternalServerError)
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}

	// Core invariant: org_id must remain NULL — no partial write.
	var dbOrgID *string
	err := env.db.QueryRow("SELECT org_id FROM workspaces WHERE slug = ?", "org-timeout-ws").Scan(&dbOrgID)
	if err != nil {
		t.Fatalf("DB query failed: %v", err)
	}
	if dbOrgID != nil {
		t.Errorf("org_id = %v; want nil (no partial write)", *dbOrgID)
	}
}
