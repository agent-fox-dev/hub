package workspace

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ========================================================================
// Spec 15 Task 3.1: Patches table schema
// (TS-15-21, TS-15-22)
// Requirements: 15-REQ-7
// ========================================================================

// TS-15-21: Hub startup creates the patches table with all required columns
// and constraints when the table does not yet exist.
// Requirement: 15-REQ-7.1
func TestCarryPatch_PatchesTableSchema_AllColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	// Run the schema initializer.
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	// Verify the patches table exists by querying PRAGMA table_info.
	rows, err := db.Query("PRAGMA table_info(patches)")
	if err != nil {
		t.Fatalf("PRAGMA table_info(patches) failed: %v", err)
	}
	defer rows.Close()

	type columnInfo struct {
		name    string
		colType string
		notNull bool
		dflt    *string
		pk      bool
	}
	columns := make(map[string]columnInfo)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt *string
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		columns[name] = columnInfo{
			name:    name,
			colType: ctype,
			notNull: notnull == 1,
			dflt:    dflt,
			pk:      pk == 1,
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}

	if len(columns) == 0 {
		t.Fatal("patches table does not exist or has no columns")
	}

	// Required columns per 15-REQ-7.1.
	requiredColumns := []string{
		"id", "workspace_slug", "branch_name", "position",
		"status", "upstream_pr_url", "description",
		"added_at", "updated_at",
	}
	for _, col := range requiredColumns {
		if _, ok := columns[col]; !ok {
			t.Errorf("patches table missing required column %q", col)
		}
	}

	// Verify id is TEXT PRIMARY KEY.
	if col, ok := columns["id"]; ok {
		if !col.pk {
			t.Error("id column should be PRIMARY KEY")
		}
	}

	// Verify workspace_slug is TEXT NOT NULL.
	if col, ok := columns["workspace_slug"]; ok {
		if !col.notNull {
			t.Error("workspace_slug should be NOT NULL")
		}
	}

	// Verify branch_name is TEXT NOT NULL.
	if col, ok := columns["branch_name"]; ok {
		if !col.notNull {
			t.Error("branch_name should be NOT NULL")
		}
	}

	// Verify position is INTEGER NOT NULL.
	if col, ok := columns["position"]; ok {
		if !col.notNull {
			t.Error("position should be NOT NULL")
		}
	}

	// Verify status is TEXT NOT NULL DEFAULT 'active'.
	if col, ok := columns["status"]; ok {
		if !col.notNull {
			t.Error("status should be NOT NULL")
		}
		if col.dflt == nil || *col.dflt != "'active'" {
			got := "<nil>"
			if col.dflt != nil {
				got = *col.dflt
			}
			t.Errorf("status default = %s; want 'active'", got)
		}
	}

	// Verify added_at is TEXT NOT NULL.
	if col, ok := columns["added_at"]; ok {
		if !col.notNull {
			t.Error("added_at should be NOT NULL")
		}
	}

	// Verify updated_at is TEXT NOT NULL.
	if col, ok := columns["updated_at"]; ok {
		if !col.notNull {
			t.Error("updated_at should be NOT NULL")
		}
	}

	// Verify UNIQUE constraints via PRAGMA index_list.
	idxRows, err := db.Query("PRAGMA index_list(patches)")
	if err != nil {
		t.Fatalf("PRAGMA index_list(patches) failed: %v", err)
	}
	defer idxRows.Close()

	type indexInfo struct {
		name    string
		unique  bool
		columns []string
	}
	var indexes []indexInfo
	for idxRows.Next() {
		var seq int
		var name, origin string
		var unique, partial int
		if err := idxRows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan index_list failed: %v", err)
		}
		idx := indexInfo{name: name, unique: unique == 1}

		// Get columns for this index.
		colRows, err := db.Query("PRAGMA index_info(?)", name)
		if err != nil {
			t.Fatalf("PRAGMA index_info(%s) failed: %v", name, err)
		}
		for colRows.Next() {
			var seqno, cid int
			var colName string
			if err := colRows.Scan(&seqno, &cid, &colName); err != nil {
				t.Fatalf("scan index_info failed: %v", err)
			}
			idx.columns = append(idx.columns, colName)
		}
		colRows.Close()

		indexes = append(indexes, idx)
	}

	// Check for UNIQUE(workspace_slug, branch_name).
	foundBranchUnique := false
	for _, idx := range indexes {
		if idx.unique && len(idx.columns) == 2 &&
			containsAll(idx.columns, "workspace_slug", "branch_name") {
			foundBranchUnique = true
			break
		}
	}
	if !foundBranchUnique {
		t.Error("patches table missing UNIQUE constraint on (workspace_slug, branch_name)")
	}

	// Check for UNIQUE(workspace_slug, position).
	foundPositionUnique := false
	for _, idx := range indexes {
		if idx.unique && len(idx.columns) == 2 &&
			containsAll(idx.columns, "workspace_slug", "position") {
			foundPositionUnique = true
			break
		}
	}
	if !foundPositionUnique {
		t.Error("patches table missing UNIQUE constraint on (workspace_slug, position)")
	}
}

// containsAll returns true if slice contains all specified items.
func containsAll(slice []string, items ...string) bool {
	set := make(map[string]bool, len(slice))
	for _, s := range slice {
		set[s] = true
	}
	for _, item := range items {
		if !set[item] {
			return false
		}
	}
	return true
}

// TS-15-21 (status default): Inserting a row without specifying status
// defaults to 'active'.
// Requirement: 15-REQ-7.1
func TestCarryPatch_PatchesTableSchema_StatusDefault(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, added_at, updated_at)
		 VALUES ('test-id-1', 'ws-1', 'feature/foo', 1, ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("INSERT without status failed: %v", err)
	}

	var status string
	err = db.QueryRow("SELECT status FROM patches WHERE id = ?", "test-id-1").Scan(&status)
	if err != nil {
		t.Fatalf("SELECT status failed: %v", err)
	}
	if status != "active" {
		t.Errorf("status = %q; want %q (default)", status, "active")
	}
}

// TS-15-22: Inserting two patches with the same (workspace_slug, branch_name)
// pair is rejected by the database unique constraint.
// Requirement: 15-REQ-7.2
func TestCarryPatch_PatchesTableSchema_DuplicateBranchNameRejected(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// First insert should succeed.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-1', 'cp-ws', 'feature/foo', 1, 'active', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("first INSERT failed: %v", err)
	}

	// Second insert with same (workspace_slug, branch_name) should fail.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-2', 'cp-ws', 'feature/foo', 2, 'active', ?, ?)`,
		now, now,
	)
	if err == nil {
		t.Fatal("second INSERT with duplicate (workspace_slug, branch_name) should fail; got nil error")
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") &&
		!strings.Contains(strings.ToLower(err.Error()), "constraint") {
		t.Errorf("expected UNIQUE constraint error; got: %v", err)
	}
}

// TS-15-22 (position uniqueness): Inserting two patches with the same
// (workspace_slug, position) is rejected by the database unique constraint.
// Requirement: 15-REQ-7.2
func TestCarryPatch_PatchesTableSchema_DuplicatePositionRejected(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// First insert at position 1.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-1', 'cp-ws', 'feature/a', 1, 'active', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("first INSERT failed: %v", err)
	}

	// Second insert at same position 1 for same workspace should fail.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-2', 'cp-ws', 'feature/b', 1, 'active', ?, ?)`,
		now, now,
	)
	if err == nil {
		t.Fatal("second INSERT with duplicate (workspace_slug, position) should fail; got nil error")
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") &&
		!strings.Contains(strings.ToLower(err.Error()), "constraint") {
		t.Errorf("expected UNIQUE constraint error; got: %v", err)
	}
}

// Verify same branch_name across different workspaces is allowed.
// Requirement: 15-REQ-7.2 (uniqueness is per-workspace, not global)
func TestCarryPatch_PatchesTableSchema_SameBranchDifferentWorkspaces(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema() returned error: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Same branch name in different workspaces should succeed.
	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-1', 'ws-a', 'feature/foo', 1, 'active', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("first INSERT failed: %v", err)
	}

	_, err = db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES ('uuid-2', 'ws-b', 'feature/foo', 1, 'active', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Errorf("same branch_name in different workspace should succeed; got: %v", err)
	}
}

// ========================================================================
// Spec 15 Task 3.2: Add patch operation
// (TS-15-23, TS-15-24, TS-15-25, TS-15-26, TS-15-27, TS-15-28)
// Requirements: 15-REQ-8
// ========================================================================

// newPatchTestEnv creates a test environment with carry_patch workspace support.
// It ensures the carry_patch columns exist and seeds a carry_patch workspace
// with the given slug and integration branch.
func newPatchTestEnv(t *testing.T, slug, integrationBranch string) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	ensureCarryPatchColumns(t, env.db)
	seedCarryPatchWorkspaceRaw(t, env.db, slug,
		"https://github.com/fork/repo.git",
		"https://github.com/upstream/repo.git",
		integrationBranch)
	return env
}

// seedPatchRaw inserts a patch row directly into the database.
func seedPatchRaw(t *testing.T, db *sql.DB, id, workspaceSlug, branchName string, position int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		 VALUES (?, ?, ?, ?, 'active', ?, ?)`,
		id, workspaceSlug, branchName, position, now, now,
	)
	if err != nil {
		t.Fatalf("seedPatchRaw(%q, %q, %q, %d) failed: %v", id, workspaceSlug, branchName, position, err)
	}
}

// TS-15-23: Adding a patch with only branch_name (no position) appends it
// at the end with position = max+1, status='active', and returns HTTP 201
// with all patch fields.
// Requirement: 15-REQ-8.1
func TestCarryPatch_AddPatch_AppendNoPosition(t *testing.T) {
	slug := "cp-add-append"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed two existing patches at positions 1 and 2.
	seedPatchRaw(t, env.db, "existing-1", slug, "feature/a", 1)
	seedPatchRaw(t, env.db, "existing-2", slug, "feature/b", 2)

	body := `{"branch_name": "feature/new-patch"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify position = 3 (max+1).
	if pos, ok := resp["position"]; !ok {
		t.Error("response missing 'position' field")
	} else if pos != float64(3) {
		t.Errorf("position = %v; want 3", pos)
	}

	// Verify status = 'active'.
	if status, ok := resp["status"]; !ok {
		t.Error("response missing 'status' field")
	} else if status != "active" {
		t.Errorf("status = %v; want %q", status, "active")
	}

	// Verify id is non-empty.
	if id, ok := resp["id"]; !ok {
		t.Error("response missing 'id' field")
	} else if id == "" {
		t.Error("id is empty; want non-empty UUID")
	}

	// Verify branch_name.
	if bn, ok := resp["branch_name"]; !ok {
		t.Error("response missing 'branch_name' field")
	} else if bn != "feature/new-patch" {
		t.Errorf("branch_name = %v; want %q", bn, "feature/new-patch")
	}

	// Verify added_at is RFC 3339.
	if at, ok := resp["added_at"].(string); ok {
		if _, err := time.Parse(time.RFC3339, at); err != nil {
			t.Errorf("added_at %q is not valid RFC 3339: %v", at, err)
		}
	} else {
		t.Error("response missing or invalid 'added_at' field")
	}

	// Verify updated_at is RFC 3339.
	if ut, ok := resp["updated_at"].(string); ok {
		if _, err := time.Parse(time.RFC3339, ut); err != nil {
			t.Errorf("updated_at %q is not valid RFC 3339: %v", ut, err)
		}
	} else {
		t.Error("response missing or invalid 'updated_at' field")
	}
}

// TS-15-24: Adding a patch with an explicit position inserts it at that
// position and shifts existing patches at that position or higher down by one.
// Requirement: 15-REQ-8.2
func TestCarryPatch_AddPatch_ExplicitPositionShifts(t *testing.T) {
	slug := "cp-add-shift"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed two existing patches.
	seedPatchRaw(t, env.db, "patch-a", slug, "feature/a", 1)
	seedPatchRaw(t, env.db, "patch-b", slug, "feature/b", 2)

	body := `{"branch_name": "feature/inserted", "position": 2}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// New patch should be at position 2.
	if pos, ok := resp["position"]; !ok {
		t.Error("response missing 'position' field")
	} else if pos != float64(2) {
		t.Errorf("new patch position = %v; want 2", pos)
	}

	// Verify database state: feature/a=1, feature/inserted=2, feature/b=3.
	rows, err := env.db.Query(
		`SELECT branch_name, position FROM patches WHERE workspace_slug = ? ORDER BY position`,
		slug,
	)
	if err != nil {
		t.Fatalf("query patches failed: %v", err)
	}
	defer rows.Close()

	expected := []struct {
		branch   string
		position int
	}{
		{"feature/a", 1},
		{"feature/inserted", 2},
		{"feature/b", 3},
	}

	i := 0
	for rows.Next() {
		var branch string
		var position int
		if err := rows.Scan(&branch, &position); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		if i >= len(expected) {
			t.Fatalf("more rows than expected")
		}
		if branch != expected[i].branch {
			t.Errorf("row[%d] branch = %q; want %q", i, branch, expected[i].branch)
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

// TS-15-25: Attempting to add a patch to a workspace with
// workspace_mode='standard' returns HTTP 400 with a JSON error body.
// Requirement: 15-REQ-8.3
func TestCarryPatch_AddPatch_StandardWorkspaceRejected(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("user-1")

	// Seed a standard workspace (no carry_patch setup).
	env.seedWorkspace(t, &Workspace{
		Slug:    "std-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "active",
	})

	body := `{"branch_name": "feature/foo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/std-ws/patches", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	// Verify error body.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if errObj, ok := resp["error"]; ok {
			if errMap, ok := errObj.(map[string]any); ok {
				if _, hasMsg := errMap["message"]; !hasMsg {
					t.Error("error response missing 'message' field")
				}
			}
		}
	}
}

// TS-15-26: Adding a patch with a branch_name that already exists in the
// patch list for the workspace returns HTTP 409 with a JSON error body.
// Requirement: 15-REQ-8.4
func TestCarryPatch_AddPatch_DuplicateBranchName(t *testing.T) {
	slug := "cp-add-dup"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed an existing patch.
	seedPatchRaw(t, env.db, "existing-patch", slug, "feature/existing", 1)

	body := `{"branch_name": "feature/existing"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusConflict {
		t.Errorf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if errObj, ok := resp["error"]; ok {
			if errMap, ok := errObj.(map[string]any); ok {
				if _, hasMsg := errMap["message"]; !hasMsg {
					t.Error("error response missing 'message' field")
				}
			}
		}
	}
}

// TS-15-27: Adding a patch whose branch_name equals the workspace's
// integration_branch returns HTTP 400 with a JSON error body.
// Requirement: 15-REQ-8.5
func TestCarryPatch_AddPatch_IntegrationBranchRejected(t *testing.T) {
	slug := "cp-add-intbranch"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	body := `{"branch_name": "deploy"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
		if errObj, ok := resp["error"]; ok {
			if errMap, ok := errObj.(map[string]any); ok {
				if _, hasMsg := errMap["message"]; !hasMsg {
					t.Error("error response missing 'message' field")
				}
			}
		}
	}
}

// TS-15-28: Adding a patch with an absent or empty branch_name returns
// HTTP 400 with a JSON error body.
// Requirement: 15-REQ-8.6
func TestCarryPatch_AddPatch_EmptyBranchName(t *testing.T) {
	slug := "cp-add-empty"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	t.Run("empty_branch_name", func(t *testing.T) {
		body := `{"branch_name": ""}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST with empty branch_name: status = %d; want %d; body: %s",
				rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	})

	t.Run("absent_branch_name", func(t *testing.T) {
		body := `{}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST without branch_name: status = %d; want %d; body: %s",
				rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	})
}

// 15-REQ-8.E1: Branch existence is NOT validated at add time — non-existent
// branches are accepted (validation deferred to rebuild).
// Requirement: 15-REQ-8.E1
func TestCarryPatch_AddPatch_NonExistentBranchAccepted(t *testing.T) {
	slug := "cp-add-noexist"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	body := `{"branch_name": "feature/does-not-exist-in-git"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Errorf("POST with non-existent branch: status = %d; want %d (accepted); body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}
}

// 15-REQ-8.E2: Position greater than (max+1) is clamped to (max+1).
// Requirement: 15-REQ-8.E2
func TestCarryPatch_AddPatch_PositionClampedToMax(t *testing.T) {
	slug := "cp-add-clamp"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed one existing patch at position 1.
	seedPatchRaw(t, env.db, "clamp-1", slug, "feature/a", 1)

	// Request position 100 — should be clamped to 2.
	body := `{"branch_name": "feature/clamped", "position": 100}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if pos, ok := resp["position"]; !ok {
		t.Error("response missing 'position' field")
	} else if pos != float64(2) {
		t.Errorf("position = %v; want 2 (clamped from 100)", pos)
	}
}

// 15-REQ-8.E3: Position less than 1 returns HTTP 400.
// Requirement: 15-REQ-8.E3
func TestCarryPatch_AddPatch_PositionLessThanOneRejected(t *testing.T) {
	slug := "cp-add-negpos"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	for _, pos := range []string{"0", "-1"} {
		t.Run("position_"+pos, func(t *testing.T) {
			body := `{"branch_name": "feature/neg", "position": ` + pos + `}`
			rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("POST with position=%s: status = %d; want %d; body: %s",
					pos, rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}

// 15-REQ-8.E4: A caller without patches:write scope is rejected with HTTP 403.
// Requirement: 15-REQ-8.E4
func TestCarryPatch_AddPatch_PATWithoutWriteScope(t *testing.T) {
	slug := "cp-add-noscope"
	env := newPatchTestEnv(t, slug, "deploy")

	// PAT with only patches:read scope.
	auth := patAuth("user-1", "patches:read")

	body := `{"branch_name": "feature/forbidden"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST with patches:read scope: status = %d; want %d; body: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// 15-REQ-8.E6: Workspace does not exist returns HTTP 400.
// Requirement: 15-REQ-8.E6
func TestCarryPatch_AddPatch_WorkspaceNotFound(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("user-1")

	body := `{"branch_name": "feature/foo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/nonexistent-ws/patches", body, auth)

	// Spec says HTTP 400 for non-existent workspace via WriteAPIError.
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST to non-existent workspace: status = %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	// Verify error envelope format (WriteAPIError).
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// ========================================================================
// Spec 15 Task 3.3: List patches operation
// (TS-15-29, TS-15-30)
// Requirements: 15-REQ-9
// ========================================================================

// TS-15-29: GET /api/v1/workspaces/:slug/patches returns HTTP 200 with a
// JSON array of patches ordered by position ascending.
// Requirement: 15-REQ-9.1
func TestCarryPatch_ListPatches_OrderedByPosition(t *testing.T) {
	slug := "cp-list-ordered"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed three patches in non-sequential insert order.
	seedPatchRaw(t, env.db, "list-3", slug, "feature/c", 3)
	seedPatchRaw(t, env.db, "list-1", slug, "feature/a", 1)
	seedPatchRaw(t, env.db, "list-2", slug, "feature/b", 2)

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var patches []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &patches); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(patches) != 3 {
		t.Fatalf("got %d patches; want 3", len(patches))
	}

	// Verify order: position 1, 2, 3.
	for i, expectedPos := range []float64{1, 2, 3} {
		if pos, ok := patches[i]["position"]; !ok {
			t.Errorf("patch[%d] missing 'position' field", i)
		} else if pos != expectedPos {
			t.Errorf("patch[%d] position = %v; want %v", i, pos, expectedPos)
		}
	}

	// Verify patch fields are present.
	requiredFields := []string{"id", "branch_name", "position", "status", "added_at", "updated_at"}
	for i, p := range patches {
		for _, field := range requiredFields {
			if _, ok := p[field]; !ok {
				t.Errorf("patch[%d] missing field %q", i, field)
			}
		}
	}
}

// TS-15-29 (empty): GET returns an empty array when no patches exist.
// Requirement: 15-REQ-9.1
func TestCarryPatch_ListPatches_EmptyArray(t *testing.T) {
	slug := "cp-list-empty"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var patches []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &patches); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(patches) != 0 {
		t.Errorf("got %d patches; want 0 (empty array)", len(patches))
	}
}

// TS-15-30: A caller without patches:read scope attempting to list patches
// receives HTTP 403 with a JSON error body.
// Requirement: 15-REQ-9.2
func TestCarryPatch_ListPatches_PATWithoutReadScope(t *testing.T) {
	slug := "cp-list-noscope"
	env := newPatchTestEnv(t, slug, "deploy")

	// PAT with no patches:read scope.
	auth := patAuth("user-1")

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("GET without patches:read scope: status = %d; want %d; body: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// 15-REQ-9.E1: GET /api/v1/workspaces/:slug/patches for a non-existent
// workspace returns HTTP 404 via WriteAPIError.
// Requirement: 15-REQ-9.E1
func TestCarryPatch_ListPatches_WorkspaceNotFound(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("user-1")

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/no-such-ws/patches", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET for non-existent workspace: status = %d; want %d; body: %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}

	// Verify error envelope format (WriteAPIError).
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusNotFound {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// 15-REQ-9.E2: GET /api/v1/workspaces/:slug/patches on a standard workspace
// returns HTTP 200 with an empty array.
// Requirement: 15-REQ-9.E2
func TestCarryPatch_ListPatches_StandardWorkspaceReturnsEmpty(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("user-1")

	env.seedWorkspace(t, &Workspace{
		Slug:    "std-list-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "active",
	})

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/std-list-ws/patches", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var patches []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &patches); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(patches) != 0 {
		t.Errorf("got %d patches for standard workspace; want 0 (empty array)", len(patches))
	}
}
