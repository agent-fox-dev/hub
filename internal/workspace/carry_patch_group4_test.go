package workspace

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ========================================================================
// Spec 15 Task 4.1: Update patch operation
// (TS-15-31, TS-15-32, TS-15-33)
// Requirements: 15-REQ-10
// ========================================================================

// TS-15-31: PATCH /api/v1/workspaces/:slug/patches/:id with a new position
// shifts other patches and returns HTTP 200 with the updated patch object.
// Requirement: 15-REQ-10.1
func TestCarryPatch_UpdatePatch_PositionShift(t *testing.T) {
	slug := "cp-update-pos"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed three patches at positions 1, 2, 3.
	seedPatchRaw(t, env.db, "uuid-1", slug, "feature/a", 1)
	seedPatchRaw(t, env.db, "uuid-2", slug, "feature/b", 2)
	seedPatchRaw(t, env.db, "uuid-3", slug, "feature/c", 3)

	// Move uuid-3 from position 3 to position 1.
	body := `{"position": 1}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+slug+"/patches/uuid-3", body, auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify returned patch has id=uuid-3 and position=1.
	if id, ok := resp["id"]; !ok {
		t.Error("response missing 'id' field")
	} else if id != "uuid-3" {
		t.Errorf("id = %v; want %q", id, "uuid-3")
	}

	if pos, ok := resp["position"]; !ok {
		t.Error("response missing 'position' field")
	} else if pos != float64(1) {
		t.Errorf("position = %v; want 1", pos)
	}

	// Verify database state: uuid-3=1, uuid-1=2, uuid-2=3.
	rows, err := env.db.Query(
		`SELECT id, position FROM patches WHERE workspace_slug = ? ORDER BY position`,
		slug,
	)
	if err != nil {
		t.Fatalf("query patches failed: %v", err)
	}
	defer rows.Close()

	expected := []struct {
		id       string
		position int
	}{
		{"uuid-3", 1},
		{"uuid-1", 2},
		{"uuid-2", 3},
	}

	i := 0
	for rows.Next() {
		var id string
		var position int
		if err := rows.Scan(&id, &position); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		if i >= len(expected) {
			t.Fatalf("more rows than expected")
		}
		if id != expected[i].id {
			t.Errorf("row[%d] id = %q; want %q", i, id, expected[i].id)
		}
		if position != expected[i].position {
			t.Errorf("row[%d] position = %d; want %d", i, position, expected[i].position)
		}
		i++
	}
	if i != len(expected) {
		t.Errorf("got %d rows; want %d", i, len(expected))
	}

	// Verify updated_at changed.
	if ut, ok := resp["updated_at"].(string); ok {
		if _, err := time.Parse(time.RFC3339, ut); err != nil {
			t.Errorf("updated_at %q is not valid RFC 3339: %v", ut, err)
		}
	} else {
		t.Error("response missing or invalid 'updated_at' field")
	}
}

// TS-15-31 (variant): PATCH updates status, description, and upstream_pr_url fields.
// Requirement: 15-REQ-10.1
func TestCarryPatch_UpdatePatch_StatusDescriptionURL(t *testing.T) {
	slug := "cp-update-fields"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	seedPatchRaw(t, env.db, "uuid-1", slug, "feature/a", 1)

	body := `{"status": "disabled", "description": "updated desc", "upstream_pr_url": "https://github.com/upstream/repo/pull/42"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+slug+"/patches/uuid-1", body, auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if status, ok := resp["status"]; !ok {
		t.Error("response missing 'status' field")
	} else if status != "disabled" {
		t.Errorf("status = %v; want %q", status, "disabled")
	}

	if desc, ok := resp["description"]; !ok {
		t.Error("response missing 'description' field")
	} else if desc != "updated desc" {
		t.Errorf("description = %v; want %q", desc, "updated desc")
	}

	if prURL, ok := resp["upstream_pr_url"]; !ok {
		t.Error("response missing 'upstream_pr_url' field")
	} else if prURL != "https://github.com/upstream/repo/pull/42" {
		t.Errorf("upstream_pr_url = %v; want %q", prURL, "https://github.com/upstream/repo/pull/42")
	}
}

// 15-REQ-10.E1: PATCH ignores branch_name field — branch_name is immutable after creation.
// Requirement: 15-REQ-10.E1
func TestCarryPatch_UpdatePatch_BranchNameImmutable(t *testing.T) {
	slug := "cp-update-immutable-branch"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	seedPatchRaw(t, env.db, "uuid-1", slug, "feature/original", 1)

	// Attempt to change branch_name via PATCH — should be silently ignored.
	body := `{"branch_name": "feature/changed", "description": "updated"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+slug+"/patches/uuid-1", body, auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// branch_name should remain unchanged.
	if bn, ok := resp["branch_name"]; !ok {
		t.Error("response missing 'branch_name' field")
	} else if bn != "feature/original" {
		t.Errorf("branch_name = %v; want %q (immutable)", bn, "feature/original")
	}

	// description should be updated.
	if desc, ok := resp["description"]; !ok {
		t.Error("response missing 'description' field")
	} else if desc != "updated" {
		t.Errorf("description = %v; want %q", desc, "updated")
	}
}

// TS-15-32: PATCH /api/v1/workspaces/:slug/patches/:id with a non-existent
// patch ID returns HTTP 404 with a JSON error body.
// Requirement: 15-REQ-10.2
func TestCarryPatch_UpdatePatch_NotFound(t *testing.T) {
	slug := "cp-update-404"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	body := `{"description": "updated"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+slug+"/patches/nonexistent-uuid", body, auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("PATCH status = %d; want %d; body: %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		// Check for error body.
		if _, hasError := resp["error"]; !hasError {
			t.Error("expected JSON error body")
		}
	}
}

// TS-15-33: PATCH /api/v1/workspaces/:slug/patches/:id with an invalid status
// value returns HTTP 400 with a JSON error body.
// Requirement: 15-REQ-10.3
func TestCarryPatch_UpdatePatch_InvalidStatus(t *testing.T) {
	slug := "cp-update-badstatus"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	seedPatchRaw(t, env.db, "uuid-1", slug, "feature/a", 1)

	body := `{"status": "invalid_status"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+slug+"/patches/uuid-1", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if _, hasError := resp["error"]; !hasError {
			t.Error("expected JSON error body")
		}
	}
}

// 15-REQ-10.3 (variant): Each valid status value is accepted.
// Requirement: 15-REQ-10.3
func TestCarryPatch_UpdatePatch_ValidStatuses(t *testing.T) {
	slug := "cp-update-validstatus"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	seedPatchRaw(t, env.db, "uuid-1", slug, "feature/a", 1)

	validStatuses := []string{"active", "merged_upstream", "conflict", "disabled"}
	for _, status := range validStatuses {
		t.Run("status_"+status, func(t *testing.T) {
			body := `{"status": "` + status + `"}`
			rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+slug+"/patches/uuid-1", body, auth)

			if rec.Code != http.StatusOK {
				t.Errorf("PATCH with status=%q: status = %d; want %d; body: %s",
					status, rec.Code, http.StatusOK, rec.Body.String())
			}

			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}
			if got, ok := resp["status"]; !ok {
				t.Error("response missing 'status' field")
			} else if got != status {
				t.Errorf("status = %v; want %q", got, status)
			}
		})
	}
}

// 15-REQ-10.E2: A caller without patches:write scope attempting to update
// a patch receives HTTP 403 with JSON error body.
// Requirement: 15-REQ-10.E2
func TestCarryPatch_UpdatePatch_PATWithoutWriteScope(t *testing.T) {
	slug := "cp-update-noscope"
	env := newPatchTestEnv(t, slug, "deploy")

	// PAT with only patches:read scope.
	auth := patAuth("user-1", "patches:read")

	seedPatchRaw(t, env.db, "uuid-1", slug, "feature/a", 1)

	body := `{"description": "updated"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+slug+"/patches/uuid-1", body, auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("PATCH with patches:read scope: status = %d; want %d; body: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// 15-REQ-10.E3: Position < 1 or > total patch count returns HTTP 400.
// Requirement: 15-REQ-10.E3
func TestCarryPatch_UpdatePatch_InvalidPosition(t *testing.T) {
	slug := "cp-update-badpos"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	seedPatchRaw(t, env.db, "uuid-1", slug, "feature/a", 1)
	seedPatchRaw(t, env.db, "uuid-2", slug, "feature/b", 2)

	t.Run("position_0", func(t *testing.T) {
		body := `{"position": 0}`
		rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+slug+"/patches/uuid-1", body, auth)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH with position=0: status = %d; want %d; body: %s",
				rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	})

	t.Run("position_greater_than_count", func(t *testing.T) {
		body := `{"position": 5}`
		rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+slug+"/patches/uuid-1", body, auth)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH with position=5 (count=2): status = %d; want %d; body: %s",
				rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	})
}

// ========================================================================
// Spec 15 Task 4.1: Remove patch operation
// (TS-15-34, TS-15-35)
// Requirements: 15-REQ-11
// ========================================================================

// TS-15-34: DELETE /api/v1/workspaces/:slug/patches/:id removes the patch
// and compacts positions so there are no gaps, returning HTTP 204.
// Requirement: 15-REQ-11.1
func TestCarryPatch_RemovePatch_CompactsPositions(t *testing.T) {
	slug := "cp-remove-compact"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed three patches at positions 1, 2, 3.
	seedPatchRaw(t, env.db, "uuid-1", slug, "feature/a", 1)
	seedPatchRaw(t, env.db, "uuid-2", slug, "feature/b", 2)
	seedPatchRaw(t, env.db, "uuid-3", slug, "feature/c", 3)

	// Remove the middle patch (uuid-2 at position 2).
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/"+slug+"/patches/uuid-2", "", auth)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d; want %d; body: %s",
			rec.Code, http.StatusNoContent, rec.Body.String())
	}

	// Verify body is empty.
	if rec.Body.Len() != 0 {
		t.Errorf("response body should be empty; got: %s", rec.Body.String())
	}

	// Verify remaining patches have compacted positions: uuid-1=1, uuid-3=2.
	rows, err := env.db.Query(
		`SELECT id, position FROM patches WHERE workspace_slug = ? ORDER BY position`,
		slug,
	)
	if err != nil {
		t.Fatalf("query patches failed: %v", err)
	}
	defer rows.Close()

	expected := []struct {
		id       string
		position int
	}{
		{"uuid-1", 1},
		{"uuid-3", 2},
	}

	i := 0
	for rows.Next() {
		var id string
		var position int
		if err := rows.Scan(&id, &position); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		if i >= len(expected) {
			t.Fatalf("more rows than expected")
		}
		if id != expected[i].id {
			t.Errorf("row[%d] id = %q; want %q", i, id, expected[i].id)
		}
		if position != expected[i].position {
			t.Errorf("row[%d] position = %d; want %d", i, position, expected[i].position)
		}
		i++
	}
	if i != len(expected) {
		t.Errorf("got %d rows; want %d", i, len(expected))
	}
}

// TS-15-35: DELETE /api/v1/workspaces/:slug/patches/:id with a non-existent
// patch ID returns HTTP 404 with a JSON error body.
// Requirement: 15-REQ-11.2
func TestCarryPatch_RemovePatch_NotFound(t *testing.T) {
	slug := "cp-remove-404"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/"+slug+"/patches/nonexistent-uuid", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE status = %d; want %d; body: %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if _, hasError := resp["error"]; !hasError {
			t.Error("expected JSON error body")
		}
	}
}

// 15-REQ-11.E1: A caller without patches:write scope attempting to remove
// a patch receives HTTP 403 with JSON error body.
// Requirement: 15-REQ-11.E1
func TestCarryPatch_RemovePatch_PATWithoutWriteScope(t *testing.T) {
	slug := "cp-remove-noscope"
	env := newPatchTestEnv(t, slug, "deploy")

	auth := patAuth("user-1", "patches:read")

	seedPatchRaw(t, env.db, "uuid-1", slug, "feature/a", 1)

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/"+slug+"/patches/uuid-1", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("DELETE with patches:read scope: status = %d; want %d; body: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// ========================================================================
// Spec 15 Task 4.2: Reorder patches operation
// (TS-15-36, TS-15-37, TS-15-38, TS-15-39)
// Requirements: 15-REQ-12
// ========================================================================

// TS-15-36: POST /api/v1/workspaces/:slug/patches/reorder with a complete
// ordered list of all patch IDs reassigns positions 1-based and returns
// HTTP 200 with the reordered patch array.
// Requirement: 15-REQ-12.1
func TestCarryPatch_ReorderPatches_Success(t *testing.T) {
	slug := "cp-reorder-ok"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed three patches: uuid-1=1, uuid-2=2, uuid-3=3.
	seedPatchRaw(t, env.db, "uuid-1", slug, "feature/a", 1)
	seedPatchRaw(t, env.db, "uuid-2", slug, "feature/b", 2)
	seedPatchRaw(t, env.db, "uuid-3", slug, "feature/c", 3)

	// Reorder: uuid-3, uuid-1, uuid-2.
	body := `{"patch_ids": ["uuid-3", "uuid-1", "uuid-2"]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches/reorder", body, auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST reorder status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var patches []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &patches); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(patches) != 3 {
		t.Fatalf("got %d patches; want 3", len(patches))
	}

	// Verify order: uuid-3 at position 1, uuid-1 at position 2, uuid-2 at position 3.
	expectedOrder := []struct {
		id       string
		position float64
	}{
		{"uuid-3", 1},
		{"uuid-1", 2},
		{"uuid-2", 3},
	}

	for i, exp := range expectedOrder {
		if id, ok := patches[i]["id"]; !ok {
			t.Errorf("patch[%d] missing 'id' field", i)
		} else if id != exp.id {
			t.Errorf("patch[%d] id = %v; want %q", i, id, exp.id)
		}
		if pos, ok := patches[i]["position"]; !ok {
			t.Errorf("patch[%d] missing 'position' field", i)
		} else if pos != exp.position {
			t.Errorf("patch[%d] position = %v; want %v", i, pos, exp.position)
		}
	}

	// Verify database state matches.
	rows, err := env.db.Query(
		`SELECT id, position FROM patches WHERE workspace_slug = ? ORDER BY position`,
		slug,
	)
	if err != nil {
		t.Fatalf("query patches failed: %v", err)
	}
	defer rows.Close()

	i := 0
	for rows.Next() {
		var id string
		var position int
		if err := rows.Scan(&id, &position); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		if i >= len(expectedOrder) {
			t.Fatalf("more rows than expected")
		}
		if id != expectedOrder[i].id {
			t.Errorf("DB row[%d] id = %q; want %q", i, id, expectedOrder[i].id)
		}
		if position != int(expectedOrder[i].position) {
			t.Errorf("DB row[%d] position = %d; want %d", i, position, int(expectedOrder[i].position))
		}
		i++
	}
}

// TS-15-37: POST /api/v1/workspaces/:slug/patches/reorder with a patch_ids
// list missing one or more patches returns HTTP 400 without modifying positions.
// Requirement: 15-REQ-12.2
func TestCarryPatch_ReorderPatches_MissingIDs(t *testing.T) {
	slug := "cp-reorder-missing"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	seedPatchRaw(t, env.db, "uuid-1", slug, "feature/a", 1)
	seedPatchRaw(t, env.db, "uuid-2", slug, "feature/b", 2)
	seedPatchRaw(t, env.db, "uuid-3", slug, "feature/c", 3)

	// Capture positions before.
	before := queryPatchPositions(t, env.db, slug)

	// Only provide 2 of 3 patch IDs.
	body := `{"patch_ids": ["uuid-1", "uuid-2"]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches/reorder", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST reorder status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if _, hasError := resp["error"]; !hasError {
			t.Error("expected JSON error body")
		}
	}

	// Verify positions are unchanged.
	after := queryPatchPositions(t, env.db, slug)
	assertPositionsUnchanged(t, before, after)
}

// TS-15-38: POST /api/v1/workspaces/:slug/patches/reorder with duplicate
// patch IDs returns HTTP 400 without modifying positions.
// Requirement: 15-REQ-12.3
func TestCarryPatch_ReorderPatches_DuplicateIDs(t *testing.T) {
	slug := "cp-reorder-dup"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	seedPatchRaw(t, env.db, "uuid-1", slug, "feature/a", 1)
	seedPatchRaw(t, env.db, "uuid-2", slug, "feature/b", 2)
	seedPatchRaw(t, env.db, "uuid-3", slug, "feature/c", 3)

	before := queryPatchPositions(t, env.db, slug)

	body := `{"patch_ids": ["uuid-1", "uuid-1", "uuid-2", "uuid-3"]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches/reorder", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST reorder status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if _, hasError := resp["error"]; !hasError {
			t.Error("expected JSON error body")
		}
	}

	after := queryPatchPositions(t, env.db, slug)
	assertPositionsUnchanged(t, before, after)
}

// TS-15-39: POST /api/v1/workspaces/:slug/patches/reorder with a patch ID
// that belongs to a different workspace returns HTTP 400.
// Requirement: 15-REQ-12.4
func TestCarryPatch_ReorderPatches_ForeignID(t *testing.T) {
	slug := "cp-reorder-foreign"
	otherSlug := "cp-reorder-other"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Set up a second carry_patch workspace.
	ensureCarryPatchColumns(t, env.db)
	seedCarryPatchWorkspaceRaw(t, env.db, otherSlug,
		"https://github.com/fork/other.git",
		"https://github.com/upstream/other.git",
		"deploy")

	seedPatchRaw(t, env.db, "uuid-1", slug, "feature/a", 1)
	seedPatchRaw(t, env.db, "uuid-2", slug, "feature/b", 2)
	seedPatchRaw(t, env.db, "uuid-foreign", otherSlug, "feature/foreign", 1)

	body := `{"patch_ids": ["uuid-1", "uuid-foreign"]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches/reorder", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST reorder status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if _, hasError := resp["error"]; !hasError {
			t.Error("expected JSON error body")
		}
	}
}

// 15-REQ-12.E1: Empty patch_ids list when workspace has patches returns HTTP 400.
// Requirement: 15-REQ-12.E1
func TestCarryPatch_ReorderPatches_EmptyList(t *testing.T) {
	slug := "cp-reorder-empty"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	seedPatchRaw(t, env.db, "uuid-1", slug, "feature/a", 1)

	body := `{"patch_ids": []}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches/reorder", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST reorder with empty list: status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// 15-REQ-12.E2: A caller without patches:write scope attempting to reorder
// patches receives HTTP 403.
// Requirement: 15-REQ-12.E2
func TestCarryPatch_ReorderPatches_PATWithoutWriteScope(t *testing.T) {
	slug := "cp-reorder-noscope"
	env := newPatchTestEnv(t, slug, "deploy")

	auth := patAuth("user-1", "patches:read")

	seedPatchRaw(t, env.db, "uuid-1", slug, "feature/a", 1)

	body := `{"patch_ids": ["uuid-1"]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches/reorder", body, auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST reorder with patches:read scope: status = %d; want %d; body: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// ========================================================================
// Spec 15 Task 4.3: Cascade delete patches on workspace deletion
// (TS-15-40)
// Requirements: 15-REQ-13
// ========================================================================

// TS-15-40: Deleting a workspace also deletes all associated patches
// atomically within the same transaction.
// Requirement: 15-REQ-13.1
func TestCarryPatch_CascadeDeletePatches(t *testing.T) {
	slug := "cp-cascade-del"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed patches.
	seedPatchRaw(t, env.db, "cascade-1", slug, "feature/a", 1)
	seedPatchRaw(t, env.db, "cascade-2", slug, "feature/b", 2)
	seedPatchRaw(t, env.db, "cascade-3", slug, "feature/c", 3)

	// Verify patches exist before deletion.
	var countBefore int
	err := env.db.QueryRow("SELECT COUNT(*) FROM patches WHERE workspace_slug = ?", slug).Scan(&countBefore)
	if err != nil {
		t.Fatalf("count before: %v", err)
	}
	if countBefore != 3 {
		t.Fatalf("patches count before = %d; want 3", countBefore)
	}

	// Archive the workspace first (required before delete in existing impl).
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/archive", "", auth)
	if rec.Code != http.StatusOK {
		// If archive isn't supported for carry_patch workspaces, try direct delete.
		t.Logf("archive returned %d; trying direct delete", rec.Code)
	}

	// Delete the workspace.
	rec = env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/"+slug, "", auth)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE workspace status = %d; want %d; body: %s",
			rec.Code, http.StatusNoContent, rec.Body.String())
	}

	// Verify workspace is gone.
	var wsCount int
	err = env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", slug).Scan(&wsCount)
	if err != nil {
		t.Fatalf("workspace count query: %v", err)
	}
	if wsCount != 0 {
		t.Errorf("workspaces count = %d; want 0", wsCount)
	}

	// Verify all patches are gone.
	var patchCount int
	err = env.db.QueryRow("SELECT COUNT(*) FROM patches WHERE workspace_slug = ?", slug).Scan(&patchCount)
	if err != nil {
		t.Fatalf("patches count query: %v", err)
	}
	if patchCount != 0 {
		t.Errorf("patches count after workspace delete = %d; want 0", patchCount)
	}
}

// 15-REQ-13.E2: Deleting a workspace with no patches works normally.
// Requirement: 15-REQ-13.E2
func TestCarryPatch_CascadeDeletePatches_NoPatches(t *testing.T) {
	slug := "cp-cascade-empty"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// No patches seeded — just the workspace.

	// Archive first.
	env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/archive", "", auth)

	// Delete should succeed.
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/"+slug, "", auth)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE workspace (no patches) status = %d; want %d; body: %s",
			rec.Code, http.StatusNoContent, rec.Body.String())
	}

	// Verify workspace is gone.
	var wsCount int
	err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", slug).Scan(&wsCount)
	if err != nil {
		t.Fatalf("workspace count query: %v", err)
	}
	if wsCount != 0 {
		t.Errorf("workspaces count = %d; want 0", wsCount)
	}
}

// 15-REQ-13.1 (store-level): deleteWorkspace function deletes patches
// alongside the workspace row.
// Requirement: 15-REQ-13.1
func TestCarryPatch_CascadeDeletePatches_StoreLevel(t *testing.T) {
	db := openTestDB(t)
	ensureCarryPatchColumns(t, db)

	slug := "cp-cascade-store"

	// Insert a carry_patch workspace directly.
	seedCarryPatchWorkspaceRaw(t, db, slug,
		"https://github.com/fork/repo.git",
		"https://github.com/upstream/repo.git",
		"deploy")

	// Seed patches.
	seedPatchRaw(t, db, "store-1", slug, "feature/a", 1)
	seedPatchRaw(t, db, "store-2", slug, "feature/b", 2)

	// Call deleteWorkspace directly.
	if err := deleteWorkspace(db, slug); err != nil {
		t.Fatalf("deleteWorkspace() returned error: %v", err)
	}

	// Verify no patches remain.
	var patchCount int
	err := db.QueryRow("SELECT COUNT(*) FROM patches WHERE workspace_slug = ?", slug).Scan(&patchCount)
	if err != nil {
		t.Fatalf("patches count query: %v", err)
	}
	if patchCount != 0 {
		t.Errorf("patches count after deleteWorkspace = %d; want 0", patchCount)
	}

	// Verify workspace is gone.
	var wsCount int
	err = db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", slug).Scan(&wsCount)
	if err != nil {
		t.Fatalf("workspace count query: %v", err)
	}
	if wsCount != 0 {
		t.Errorf("workspace count = %d; want 0", wsCount)
	}
}

// ========================================================================
// Spec 15 Task 4.3: Patch permission scopes
// (TS-15-46, TS-15-47)
// Requirements: 15-REQ-15
// ========================================================================

// TS-15-46: GET /api/v1/workspaces/:slug/patches requires patches:read scope
// and POST/PATCH/DELETE require patches:write scope; insufficient scope
// returns HTTP 403.
// Requirement: 15-REQ-15.1
func TestCarryPatch_PatchPermissions_ScopeEnforcement(t *testing.T) {
	slug := "cp-perms"
	env := newPatchTestEnv(t, slug, "deploy")

	seedPatchRaw(t, env.db, "perm-1", slug, "feature/a", 1)

	t.Run("GET_no_scope_returns_403", func(t *testing.T) {
		// PAT with no patch scopes at all.
		auth := patAuth("user-1")
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches", "", auth)

		if rec.Code != http.StatusForbidden {
			t.Errorf("GET with no scope: status = %d; want %d; body: %s",
				rec.Code, http.StatusForbidden, rec.Body.String())
		}
	})

	t.Run("POST_read_only_scope_returns_403", func(t *testing.T) {
		// PAT with only patches:read scope — cannot write.
		auth := patAuth("user-1", "patches:read")
		body := `{"branch_name": "feature/x"}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

		if rec.Code != http.StatusForbidden {
			t.Errorf("POST with patches:read scope: status = %d; want %d; body: %s",
				rec.Code, http.StatusForbidden, rec.Body.String())
		}
	})

	t.Run("PATCH_read_only_scope_returns_403", func(t *testing.T) {
		auth := patAuth("user-1", "patches:read")
		body := `{"description": "updated"}`
		rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+slug+"/patches/perm-1", body, auth)

		if rec.Code != http.StatusForbidden {
			t.Errorf("PATCH with patches:read scope: status = %d; want %d; body: %s",
				rec.Code, http.StatusForbidden, rec.Body.String())
		}
	})

	t.Run("DELETE_read_only_scope_returns_403", func(t *testing.T) {
		auth := patAuth("user-1", "patches:read")
		rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/"+slug+"/patches/perm-1", "", auth)

		if rec.Code != http.StatusForbidden {
			t.Errorf("DELETE with patches:read scope: status = %d; want %d; body: %s",
				rec.Code, http.StatusForbidden, rec.Body.String())
		}
	})

	t.Run("GET_with_read_scope_succeeds", func(t *testing.T) {
		auth := patAuth("user-1", "patches:read")
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches", "", auth)

		// patches:read should allow GET — expect 200.
		if rec.Code != http.StatusOK {
			t.Errorf("GET with patches:read scope: status = %d; want %d; body: %s",
				rec.Code, http.StatusOK, rec.Body.String())
		}
	})

	t.Run("POST_with_write_scope_succeeds", func(t *testing.T) {
		auth := patAuth("user-1", "patches:write")
		body := `{"branch_name": "feature/write-test"}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

		// patches:write should allow POST — expect 201.
		if rec.Code != http.StatusCreated {
			t.Errorf("POST with patches:write scope: status = %d; want %d; body: %s",
				rec.Code, http.StatusCreated, rec.Body.String())
		}
	})
}

// TS-15-47: Admin tokens and workspace API keys have implicit full access
// to all patch endpoints without requiring explicit scope grants.
// Requirement: 15-REQ-15.2
func TestCarryPatch_PatchPermissions_AdminAndAPIKeyAccess(t *testing.T) {
	slug := "cp-perms-admin"
	env := newPatchTestEnv(t, slug, "deploy")

	seedPatchRaw(t, env.db, "admin-1", slug, "feature/a", 1)

	t.Run("admin_token_GET", func(t *testing.T) {
		auth := adminAuth()
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches", "", auth)

		if rec.Code != http.StatusOK {
			t.Errorf("GET with admin token: status = %d; want %d; body: %s",
				rec.Code, http.StatusOK, rec.Body.String())
		}
	})

	t.Run("api_key_GET", func(t *testing.T) {
		auth := userAuth("user-1")
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches", "", auth)

		if rec.Code != http.StatusOK {
			t.Errorf("GET with API key: status = %d; want %d; body: %s",
				rec.Code, http.StatusOK, rec.Body.String())
		}
	})

	t.Run("admin_token_POST", func(t *testing.T) {
		auth := adminAuth()
		body := `{"branch_name": "feature/admin-post"}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

		if rec.Code != http.StatusCreated {
			t.Errorf("POST with admin token: status = %d; want %d; body: %s",
				rec.Code, http.StatusCreated, rec.Body.String())
		}
	})

	t.Run("api_key_DELETE", func(t *testing.T) {
		auth := userAuth("user-1")
		rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/"+slug+"/patches/admin-1", "", auth)

		if rec.Code != http.StatusNoContent {
			t.Errorf("DELETE with API key: status = %d; want %d; body: %s",
				rec.Code, http.StatusNoContent, rec.Body.String())
		}
	})
}

// 15-REQ-15.E2: Unauthenticated request to any patch endpoint returns
// HTTP 401 before scope evaluation.
// Requirement: 15-REQ-15.E2
func TestCarryPatch_PatchPermissions_Unauthenticated(t *testing.T) {
	slug := "cp-perms-unauth"
	env := newPatchTestEnv(t, slug, "deploy")

	seedPatchRaw(t, env.db, "unauth-1", slug, "feature/a", 1)

	// No auth header.
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches", "", nil)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET without auth: status = %d; want %d; body: %s",
			rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

// ========================================================================
// Helpers for reorder tests
// ========================================================================

// patchPosition holds a patch ID and its position.
type patchPosition struct {
	id       string
	position int
}

// queryPatchPositions returns all patches for a workspace ordered by position.
func queryPatchPositions(t *testing.T, db *sql.DB, slug string) []patchPosition {
	t.Helper()
	rows, err := db.Query(
		`SELECT id, position FROM patches WHERE workspace_slug = ? ORDER BY position`,
		slug,
	)
	if err != nil {
		t.Fatalf("query patch positions: %v", err)
	}
	defer rows.Close()

	var result []patchPosition
	for rows.Next() {
		var pp patchPosition
		if err := rows.Scan(&pp.id, &pp.position); err != nil {
			t.Fatalf("scan patch position: %v", err)
		}
		result = append(result, pp)
	}
	return result
}

// assertPositionsUnchanged verifies that patch positions haven't changed.
func assertPositionsUnchanged(t *testing.T, before, after []patchPosition) {
	t.Helper()
	if len(before) != len(after) {
		t.Errorf("patch count changed: before=%d, after=%d", len(before), len(after))
		return
	}
	for i := range before {
		if before[i].id != after[i].id || before[i].position != after[i].position {
			t.Errorf("position changed at [%d]: before=%+v, after=%+v", i, before[i], after[i])
		}
	}
}

// seedSoftDeletedPatch inserts a patch row with status='deleted' and deleted_at set.
func seedSoftDeletedPatch(t *testing.T, db *sql.DB, id, workspaceSlug, branchName string, position int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, deleted_at, added_at, updated_at)
		 VALUES (?, ?, ?, ?, 'deleted', ?, ?, ?)`,
		id, workspaceSlug, branchName, position, now, now, now,
	)
	if err != nil {
		t.Fatalf("seedSoftDeletedPatch(%q, %q, %q, %d) failed: %v", id, workspaceSlug, branchName, position, err)
	}
}

// ========================================================================
// Soft-deleted patch edge case tests (reviewer findings)
// These verify that patchCount, reorderPatches, and updatePatchPosition
// correctly exclude soft-deleted patches (status='deleted') so that
// position contiguity (15-PROP-1) is maintained for visible patches.
// ========================================================================

// patchCount should exclude soft-deleted patches so position validation
// matches what the user sees via listPatches.
func TestCarryPatch_PatchCount_ExcludesSoftDeleted(t *testing.T) {
	db := openTestDB(t)
	ensureCarryPatchColumns(t, db)
	slug := "cp-count-softdel"

	seedCarryPatchWorkspaceRaw(t, db, slug,
		"https://github.com/fork/repo.git",
		"https://github.com/upstream/repo.git",
		"deploy")

	// Seed 3 active patches and 1 soft-deleted.
	seedPatchRaw(t, db, "cnt-1", slug, "feature/a", 1)
	seedPatchRaw(t, db, "cnt-2", slug, "feature/b", 2)
	seedPatchRaw(t, db, "cnt-3", slug, "feature/c", 3)
	seedSoftDeletedPatch(t, db, "cnt-del", slug, "feature/deleted", 4)

	count, err := patchCount(db, slug)
	if err != nil {
		t.Fatalf("patchCount() returned error: %v", err)
	}
	if count != 3 {
		t.Errorf("patchCount() = %d; want 3 (should exclude soft-deleted)", count)
	}
}

// reorderPatches should only require IDs for non-deleted patches.
// A workspace with 3 active + 1 soft-deleted should accept reorder of just
// the 3 active IDs.
func TestCarryPatch_ReorderPatches_ExcludesSoftDeleted(t *testing.T) {
	db := openTestDB(t)
	ensureCarryPatchColumns(t, db)
	slug := "cp-reorder-softdel"

	seedCarryPatchWorkspaceRaw(t, db, slug,
		"https://github.com/fork/repo.git",
		"https://github.com/upstream/repo.git",
		"deploy")

	seedPatchRaw(t, db, "ro-1", slug, "feature/a", 1)
	seedPatchRaw(t, db, "ro-2", slug, "feature/b", 2)
	seedPatchRaw(t, db, "ro-3", slug, "feature/c", 3)
	seedSoftDeletedPatch(t, db, "ro-del", slug, "feature/deleted", 4)

	// Reorder only the 3 active patches — should succeed.
	patches, err := reorderPatches(db, slug, []string{"ro-3", "ro-1", "ro-2"})
	if err != nil {
		t.Fatalf("reorderPatches() returned error: %v", err)
	}
	if len(patches) != 3 {
		t.Fatalf("reorderPatches() returned %d patches; want 3", len(patches))
	}

	// Verify new positions.
	expected := []struct {
		id       string
		position int
	}{
		{"ro-3", 1},
		{"ro-1", 2},
		{"ro-2", 3},
	}
	for i, exp := range expected {
		if patches[i].ID != exp.id {
			t.Errorf("patches[%d].ID = %q; want %q", i, patches[i].ID, exp.id)
		}
		if patches[i].Position != exp.position {
			t.Errorf("patches[%d].Position = %d; want %d", i, patches[i].Position, exp.position)
		}
	}
}

// updatePatchPosition should only consider non-deleted patches when
// computing the new ordering.
func TestCarryPatch_UpdatePatchPosition_ExcludesSoftDeleted(t *testing.T) {
	db := openTestDB(t)
	ensureCarryPatchColumns(t, db)
	slug := "cp-movepos-softdel"

	seedCarryPatchWorkspaceRaw(t, db, slug,
		"https://github.com/fork/repo.git",
		"https://github.com/upstream/repo.git",
		"deploy")

	seedPatchRaw(t, db, "mv-1", slug, "feature/a", 1)
	seedPatchRaw(t, db, "mv-2", slug, "feature/b", 2)
	seedPatchRaw(t, db, "mv-3", slug, "feature/c", 3)
	seedSoftDeletedPatch(t, db, "mv-del", slug, "feature/deleted", 4)

	// Move mv-1 to position 3 (among visible patches only).
	err := updatePatchPosition(db, slug, "mv-1", 3)
	if err != nil {
		t.Fatalf("updatePatchPosition() returned error: %v", err)
	}

	// Verify resulting positions of active patches: mv-2=1, mv-3=2, mv-1=3.
	patches, err := listPatches(db, slug)
	if err != nil {
		t.Fatalf("listPatches() returned error: %v", err)
	}
	if len(patches) != 3 {
		t.Fatalf("listPatches() returned %d patches; want 3", len(patches))
	}

	expected := []struct {
		id       string
		position int
	}{
		{"mv-2", 1},
		{"mv-3", 2},
		{"mv-1", 3},
	}
	for i, exp := range expected {
		if patches[i].ID != exp.id {
			t.Errorf("patches[%d].ID = %q; want %q", i, patches[i].ID, exp.id)
		}
		if patches[i].Position != exp.position {
			t.Errorf("patches[%d].Position = %d; want %d", i, patches[i].Position, exp.position)
		}
	}
}
